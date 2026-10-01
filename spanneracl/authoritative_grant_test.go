// Copyright RetailNext, Inc. 2026

package spanneracl

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseIdentifierTableQualified(t *testing.T) {
	parsed, err := ParseIdentifier("my_schema.my_table", "table")
	require.NoError(t, err)
	assert.Equal(t, "TABLE", parsed.ResourceType)
	assert.Equal(t, "my_schema", parsed.Schema)
	assert.Equal(t, "my_table", parsed.ResourceName)
	assert.Equal(t, "my_schema.my_table", parsed.Original)
}

func TestParseIdentifierTableUnqualified(t *testing.T) {
	parsed, err := ParseIdentifier("my_table", "TABLE")
	require.NoError(t, err)
	assert.Equal(t, "TABLE", parsed.ResourceType)
	assert.Empty(t, parsed.Schema)
	assert.Equal(t, "my_table", parsed.ResourceName)
}

func TestParseIdentifierView(t *testing.T) {
	parsed, err := ParseIdentifier("my_view", "view")
	require.NoError(t, err)
	assert.Equal(t, "VIEW", parsed.ResourceType)
	assert.Equal(t, "my_view", parsed.ResourceName)
}

// TestParseIdentifierRejectsSchemaResourceType covers a real decision, not a
// preemptive guard: SCHEMA used to be part of authGrantResourceTypes
// until GetAllRolePermissionsPerId's implementation surfaced that there's no
// confirmed INFORMATION_SCHEMA view to read schema-level USAGE grants back -
// see the "Out of Scope" doc comment at the top of authoritative_grant.go.
func TestParseIdentifierRejectsSchemaResourceType(t *testing.T) {
	_, err := ParseIdentifier("my_schema", "SCHEMA")
	assert.Error(t, err)
}

func TestParseIdentifierRejectsUnsupportedResourceType(t *testing.T) {
	_, err := ParseIdentifier("something", "SEQUENCE")
	assert.Error(t, err)
}

func TestParseIdentifierRejectsTooManyParts(t *testing.T) {
	_, err := ParseIdentifier("a.b.c", "TABLE")
	assert.Error(t, err)
}

func TestParseIdentifierRejectsInvalidSchema(t *testing.T) {
	_, err := ParseIdentifier("bad schema.my_table", "TABLE")
	assert.ErrorContains(t, err, "invalid schema identifier")
}

func TestParseIdentifierRejectsInvalidResourceName(t *testing.T) {
	_, err := ParseIdentifier("my_schema.bad-table", "TABLE")
	assert.ErrorContains(t, err, "invalid resource identifier")

	_, err = ParseIdentifier("", "TABLE")
	assert.ErrorContains(t, err, "invalid resource identifier")
}

func TestAuthoritativeBindingNormalizeUppercasesPrivileges(t *testing.T) {
	binding := AuthoritativeBinding{
		Role:       "test_role",
		Privileges: []string{"select", "Insert"},
	}
	require.NoError(t, binding.Normalize())
	assert.Equal(t, []string{"SELECT", "INSERT"}, binding.Privileges)
}

func TestAuthoritativeBindingNormalizeRejectsInvalidRole(t *testing.T) {
	binding := AuthoritativeBinding{Role: "bad role!", Privileges: []string{"SELECT"}}
	assert.Error(t, binding.Normalize())
}

func TestAuthoritativeBindingNormalizeRejectsInvalidPrivilege(t *testing.T) {
	binding := AuthoritativeBinding{Role: "test_role", Privileges: []string{"NOTAPRIV"}}
	assert.Error(t, binding.Normalize())
}

// TestGetAllRolePermissionsPerIdRejectsUnsupportedResourceType covers both
// entry points rejecting an unsupported ResourceType (notably SCHEMA - see the
// "Out of Scope" doc comment at the top of authoritative_grant.go) before
// touching the network: the exported one via ParseIdentifier, and the
// unexported one's own defense-in-depth check, since it takes an
// already-parsed identifier that could be hand-built. No emulator needed.
func TestGetAllRolePermissionsPerIdRejectsUnsupportedResourceType(t *testing.T) {
	client := &Client{}

	_, err := client.GetAllRolePermissionsPerId(context.Background(), "my_schema", "SCHEMA")
	assert.ErrorContains(t, err, "unsupported resource type")

	_, err = client.getAllRolePermissionsPerId(context.Background(), ParsedIdentifier{
		ResourceType: "SCHEMA",
		Schema:       "my_schema",
		Original:     "my_schema",
	})
	assert.ErrorContains(t, err, "unsupported resource type")
}

// TestGetAllRolePermissionsPerIdMissingResource checks a missing table reports
// ErrResourceNotFound rather than an empty map. Runs on the emulator: the
// existence check fails before any TABLE_PRIVILEGES query.
func TestGetAllRolePermissionsPerIdMissingResource(t *testing.T) {
	client := newTestClient(t)

	_, err := client.GetAllRolePermissionsPerId(context.Background(), "no_such_table", "TABLE")
	assert.ErrorIs(t, err, ErrResourceNotFound)
}

// The tests below need INFORMATION_SCHEMA.TABLE_PRIVILEGES, which the
// emulator doesn't implement (GetAllRolePermissionsPerId queries it, and
// ApplyAuthoritativeBinding calls GetAllRolePermissionsPerId internally), so
// they run against a real Cloud Spanner database via newRealSpannerFixture
// and are skipped unless SPANNER_TEST_DATABASE is set - see
// real_spanner_test.go.
//
// https://github.com/GoogleCloudPlatform/cloud-spanner-emulator/issues/350

func TestGetAllRolePermissionsPerIdTable(t *testing.T) {
	f := newRealSpannerFixture(t, 1)
	ctx := context.Background()
	role := f.roles[0]

	require.NoError(t, f.client.CreateGrant(ctx, Grant{
		RoleName: role, Privilege: "SELECT", ResourceType: "TABLE", Resource: f.table,
	}))
	require.NoError(t, f.client.CreateGrant(ctx, Grant{
		RoleName: role, Privilege: "INSERT", ResourceType: "TABLE", Resource: f.table,
	}))

	perms, err := f.client.GetAllRolePermissionsPerId(ctx, f.table, "TABLE")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"SELECT", "INSERT"}, perms[role])
}

func TestGetAllRolePermissionsPerIdTableNoGrants(t *testing.T) {
	f := newRealSpannerFixture(t, 1)
	ctx := context.Background()

	perms, err := f.client.GetAllRolePermissionsPerId(ctx, f.table, "TABLE")
	require.NoError(t, err)
	assert.Empty(t, perms)
}

// TestApplyAuthoritativeBindingReconciles exercises the full revoke/grant
// diff: moving from an initial binding set to a second, different one must
// leave the database holding exactly the second set - no leftover grants
// from the first, no missing grants the second set asked for.
func TestApplyAuthoritativeBindingReconciles(t *testing.T) {
	f := newRealSpannerFixture(t, 2)
	ctx := context.Background()
	role, otherRole := f.roles[0], f.roles[1]

	id, err := ParseIdentifier(f.table, "TABLE")
	require.NoError(t, err)

	// Start with role holding SELECT+INSERT.
	require.NoError(t, f.client.ApplyAuthoritativeBinding(ctx, f.table, "TABLE", []*AuthoritativeBinding{
		{Role: role, Privileges: []string{"SELECT", "INSERT"}},
	}))
	perms, err := f.client.getAllRolePermissionsPerId(ctx, *id)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"SELECT", "INSERT"}, perms[role])
	assert.Empty(t, perms[otherRole])

	// Reconcile to: role holds only SELECT, otherRole gains UPDATE.
	// role's INSERT must be revoked, otherRole's UPDATE must be
	// granted, and role's SELECT must be left alone (no spurious
	// revoke+regrant of a privilege that didn't change).
	require.NoError(t, f.client.ApplyAuthoritativeBinding(ctx, f.table, "TABLE", []*AuthoritativeBinding{
		{Role: role, Privileges: []string{"SELECT"}},
		{Role: otherRole, Privileges: []string{"UPDATE"}},
	}))
	perms, err = f.client.getAllRolePermissionsPerId(ctx, *id)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"SELECT"}, perms[role])
	assert.ElementsMatch(t, []string{"UPDATE"}, perms[otherRole])
}

// TestApplyAuthoritativeBindingRevokesColumnLevelGrants checks that every
// column-level grant on the table is revoked, whether or not its role or
// privilege is in the desired bindings: role gets table-level SELECT in place
// of its column-level SELECT and loses its column-level INSERT, and otherRole
// (not in the bindings at all) loses its column-level UPDATE.
func TestApplyAuthoritativeBindingRevokesColumnLevelGrants(t *testing.T) {
	f := newRealSpannerFixture(t, 2)
	ctx := context.Background()
	role, otherRole := f.roles[0], f.roles[1]
	columnGrant := func(role, priv string, columns ...string) Grant {
		return Grant{RoleName: role, Privilege: priv, ResourceType: "TABLE", Resource: f.table, Columns: columns}
	}

	require.NoError(t, f.client.CreateGrant(ctx, columnGrant(role, "SELECT", "name")))
	require.NoError(t, f.client.CreateGrant(ctx, columnGrant(role, "INSERT", "id", "name")))
	require.NoError(t, f.client.CreateGrant(ctx, columnGrant(otherRole, "UPDATE", "name")))

	id, err := ParseIdentifier(f.table, "TABLE")
	require.NoError(t, err)
	perms, err := f.client.getAllRolePermissionsPerId(ctx, *id)
	require.NoError(t, err)
	columnPerms, err := f.client.getColumnLevelOnlyPermissions(ctx, *id, perms)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"name"}, columnPerms[role]["SELECT"])
	assert.ElementsMatch(t, []string{"id", "name"}, columnPerms[role]["INSERT"])
	assert.ElementsMatch(t, []string{"name"}, columnPerms[otherRole]["UPDATE"])

	require.NoError(t, f.client.ApplyAuthoritativeBinding(ctx, f.table, "TABLE", []*AuthoritativeBinding{
		{Role: role, Privileges: []string{"SELECT"}},
	}))

	perms, err = f.client.getAllRolePermissionsPerId(ctx, *id)
	require.NoError(t, err)
	assert.Equal(t, map[string][]string{role: {"SELECT"}}, perms)
	columnPerms, err = f.client.getColumnLevelOnlyPermissions(ctx, *id, perms)
	require.NoError(t, err)
	assert.Empty(t, columnPerms)
}

// TestCheckResourceExists covers checkResourceExists against a real table and
// view: both found as their own type, a type mismatch in either direction
// rejected, and a missing name reported as ErrResourceNotFound - including
// through ApplyAuthoritativeBinding with empty bindings, which would otherwise
// have nothing to apply and silently succeed.
func TestCheckResourceExists(t *testing.T) {
	f := newRealSpannerFixture(t, 0)
	ctx := context.Background()

	view := strings.Replace(f.table, "acltest_table_", "acltest_view_", 1)
	require.NoError(t, f.client.updateDatabaseDdl(ctx,
		"CREATE VIEW `"+view+"` SQL SECURITY INVOKER AS SELECT t.id FROM `"+f.table+"` AS t"))
	// Registered after the fixture's cleanup, so it runs first - the table
	// can't be dropped while a view still depends on it.
	t.Cleanup(func() {
		if err := f.client.updateDatabaseDdl(context.Background(), "DROP VIEW `"+view+"`"); err != nil {
			t.Errorf("cleanup: failed to drop view %s: %v", view, err)
		}
	})

	check := func(name, resourceType string) error {
		id, err := ParseIdentifier(name, resourceType)
		require.NoError(t, err)
		return f.client.checkResourceExists(ctx, *id)
	}
	assert.NoError(t, check(f.table, "TABLE"))
	assert.NoError(t, check(view, "VIEW"))
	assert.ErrorContains(t, check(f.table, "VIEW"), "not a VIEW")
	assert.ErrorContains(t, check(view, "TABLE"), "not a BASE TABLE")
	assert.ErrorIs(t, check("acltest_no_such_table", "TABLE"), ErrResourceNotFound)

	err := f.client.ApplyAuthoritativeBinding(ctx, "acltest_no_such_table", "TABLE", nil)
	assert.ErrorIs(t, err, ErrResourceNotFound)
}

// TestApplyAuthoritativeBindingRevokesColumnGrantShadowedByTableGrant covers a
// role holding the same privilege explicitly at both levels. getColumnLevelOnlyPermissions
// skips the column rows for it (they're indistinguishable from the ones the
// table-level grant implies), relying on Spanner clearing the explicit column
// grant when the table-level one is revoked - this checks no column privilege
// survives the reconciliation.
func TestApplyAuthoritativeBindingRevokesColumnGrantShadowedByTableGrant(t *testing.T) {
	f := newRealSpannerFixture(t, 1)
	ctx := context.Background()
	role := f.roles[0]

	require.NoError(t, f.client.CreateGrant(ctx, Grant{
		RoleName: role, Privilege: "INSERT", ResourceType: "TABLE", Resource: f.table, Columns: []string{"name"},
	}))
	require.NoError(t, f.client.CreateGrant(ctx, Grant{
		RoleName: role, Privilege: "INSERT", ResourceType: "TABLE", Resource: f.table,
	}))

	require.NoError(t, f.client.ApplyAuthoritativeBinding(ctx, f.table, "TABLE", []*AuthoritativeBinding{
		{Role: role, Privileges: []string{"SELECT"}},
	}))

	id, err := ParseIdentifier(f.table, "TABLE")
	require.NoError(t, err)
	perms, err := f.client.getAllRolePermissionsPerId(ctx, *id)
	require.NoError(t, err)
	assert.Equal(t, map[string][]string{role: {"SELECT"}}, perms)
	// With only table-level SELECT left, any surviving INSERT column row would
	// be reported here as column-level-only.
	columnPerms, err := f.client.getColumnLevelOnlyPermissions(ctx, *id, perms)
	require.NoError(t, err)
	assert.Empty(t, columnPerms)
}

// TestApplyAuthoritativeBindingView covers the VIEW path end to end: grant,
// read back (views are listed in INFORMATION_SCHEMA.TABLE_PRIVILEGES too),
// reconcile to a different role, and revoke everything.
func TestApplyAuthoritativeBindingView(t *testing.T) {
	f := newRealSpannerFixture(t, 2)
	ctx := context.Background()
	role, otherRole := f.roles[0], f.roles[1]

	view := strings.Replace(f.table, "acltest_table_", "acltest_view_", 1)
	require.NoError(t, f.client.updateDatabaseDdl(ctx,
		"CREATE VIEW `"+view+"` SQL SECURITY INVOKER AS SELECT t.id FROM `"+f.table+"` AS t"))
	// Registered after the fixture's cleanup, so it runs first: the roles can't
	// be dropped while they hold grants on the view, and the table can't be
	// dropped while the view depends on it.
	t.Cleanup(func() {
		ctx := context.Background()
		if err := f.client.RevokeAllGrantsOnIdentifier(ctx, view, "VIEW"); err != nil {
			t.Errorf("cleanup: failed to revoke grants on view %s: %v", view, err)
		}
		if err := f.client.updateDatabaseDdl(ctx, "DROP VIEW `"+view+"`"); err != nil {
			t.Errorf("cleanup: failed to drop view %s: %v", view, err)
		}
	})

	require.NoError(t, f.client.ApplyAuthoritativeBinding(ctx, view, "VIEW", []*AuthoritativeBinding{
		{Role: role, Privileges: []string{"select"}},
	}))
	perms, err := f.client.GetAllRolePermissionsPerId(ctx, view, "VIEW")
	require.NoError(t, err)
	assert.Equal(t, map[string][]string{role: {"SELECT"}}, perms)

	require.NoError(t, f.client.ApplyAuthoritativeBinding(ctx, view, "VIEW", []*AuthoritativeBinding{
		{Role: otherRole, Privileges: []string{"SELECT"}},
	}))
	perms, err = f.client.GetAllRolePermissionsPerId(ctx, view, "VIEW")
	require.NoError(t, err)
	assert.Equal(t, map[string][]string{otherRole: {"SELECT"}}, perms)

	require.NoError(t, f.client.RevokeAllGrantsOnIdentifier(ctx, view, "VIEW"))
	perms, err = f.client.GetAllRolePermissionsPerId(ctx, view, "VIEW")
	require.NoError(t, err)
	assert.Empty(t, perms)
}

// TestApplyAuthoritativeBindingLargeBatch applies a reconciliation that needs
// more than maxDdlStatementsPerBatch statements (6 roles x 4 missing table
// privileges = 24 GRANTs), so updateDatabaseDdl sends it as more than one
// UpdateDatabaseDdl batch, and checks every grant from every batch landed.
func TestApplyAuthoritativeBindingLargeBatch(t *testing.T) {
	f := newRealSpannerFixture(t, 6)
	ctx := context.Background()

	allPrivileges := []string{"SELECT", "INSERT", "UPDATE", "DELETE"}
	var bindings []*AuthoritativeBinding
	for _, role := range f.roles {
		bindings = append(bindings, &AuthoritativeBinding{Role: role, Privileges: allPrivileges})
	}
	require.NoError(t, f.client.ApplyAuthoritativeBinding(ctx, f.table, "TABLE", bindings))

	id, err := ParseIdentifier(f.table, "TABLE")
	require.NoError(t, err)
	perms, err := f.client.getAllRolePermissionsPerId(ctx, *id)
	require.NoError(t, err)
	require.Len(t, perms, len(f.roles))
	for _, role := range f.roles {
		assert.ElementsMatch(t, allPrivileges, perms[role], role)
	}
}

// TestRevokeAllGrantsOnIdentifierRevokesEverything checks that destroy-time
// revocation leaves nothing behind: table-level grants for every role, and
// column-level grants too.
func TestRevokeAllGrantsOnIdentifierRevokesEverything(t *testing.T) {
	f := newRealSpannerFixture(t, 2)
	ctx := context.Background()
	role, otherRole := f.roles[0], f.roles[1]

	for _, grant := range []Grant{
		{RoleName: role, Privilege: "SELECT"},
		{RoleName: otherRole, Privilege: "UPDATE"},
		{RoleName: otherRole, Privilege: "INSERT", Columns: []string{"name"}},
	} {
		grant.ResourceType, grant.Resource = "TABLE", f.table
		require.NoError(t, f.client.CreateGrant(ctx, grant))
	}

	require.NoError(t, f.client.RevokeAllGrantsOnIdentifier(ctx, f.table, "TABLE"))

	id, err := ParseIdentifier(f.table, "TABLE")
	require.NoError(t, err)
	perms, err := f.client.getAllRolePermissionsPerId(ctx, *id)
	require.NoError(t, err)
	assert.Empty(t, perms)
	columnPerms, err := f.client.getColumnLevelOnlyPermissions(ctx, *id, perms)
	require.NoError(t, err)
	assert.Empty(t, columnPerms)
}

func TestApplyAuthoritativeBindingRejectsPrivilegeNotAllowedForResourceType(t *testing.T) {
	client := &Client{}

	// INSERT is not in authGrantPrivilegesByType["VIEW"] (VIEW only
	// allows SELECT) - must be rejected before any network call.
	err := client.ApplyAuthoritativeBinding(context.Background(), "my_view", "VIEW", []*AuthoritativeBinding{
		{Role: "test_role", Privileges: []string{"INSERT"}},
	})
	assert.ErrorContains(t, err, "is not allowed for resource type")
}

// TestApplyAuthoritativeBindingRejectsInvalidIdentifier covers the
// ParseIdentifier call at the top of ApplyAuthoritativeBinding: an unsupported
// resource type or a malformed identifier must be rejected before any
// network call.
func TestApplyAuthoritativeBindingRejectsInvalidIdentifier(t *testing.T) {
	client := &Client{}
	bindings := []*AuthoritativeBinding{{Role: "test_role", Privileges: []string{"SELECT"}}}

	err := client.ApplyAuthoritativeBinding(context.Background(), "my_schema", "SCHEMA", bindings)
	assert.ErrorContains(t, err, "unsupported resource type")

	err = client.ApplyAuthoritativeBinding(context.Background(), "bad-table", "TABLE", bindings)
	assert.ErrorContains(t, err, "invalid resource identifier")
}

func TestApplyAuthoritativeBindingRejectsDuplicateRole(t *testing.T) {
	client := &Client{}

	// Two bindings for the same role are ambiguous - must be rejected before
	// any network call.
	err := client.ApplyAuthoritativeBinding(context.Background(), "my_table", "TABLE", []*AuthoritativeBinding{
		{Role: "test_role", Privileges: []string{"SELECT"}},
		{Role: "test_role", Privileges: []string{"INSERT"}},
	})
	assert.ErrorContains(t, err, "duplicate binding for role")
}

// TestRevokeAllGrantsOnIdentifierMissingResource checks that revoking
// everything on a table that no longer exists succeeds - there's nothing left
// to revoke - while ApplyAuthoritativeBinding still reports it. Runs on the
// emulator: the existence check only needs INFORMATION_SCHEMA.TABLES, which the
// emulator has, and it fails before any TABLE_PRIVILEGES query.
func TestRevokeAllGrantsOnIdentifierMissingResource(t *testing.T) {
	client := newTestClient(t)
	ctx := context.Background()

	assert.NoError(t, client.RevokeAllGrantsOnIdentifier(ctx, "no_such_table", "TABLE"))
	assert.ErrorIs(t, client.ApplyAuthoritativeBinding(ctx, "no_such_table", "TABLE", nil), ErrResourceNotFound)
}
