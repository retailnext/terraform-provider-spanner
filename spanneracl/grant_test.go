// Copyright RetailNext, Inc. 2026

package spanneracl

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testGrantTableName = "test_table"

// newTestClientWithTableAndRole starts a test client and provisions a table
// and a role for grant tests to target.
func newTestClientWithTableAndRole(t *testing.T) *Client {
	client := newTestClient(t)
	ctx := context.Background()

	require.NoError(t, client.updateDatabaseDdl(ctx,
		"CREATE TABLE `"+testGrantTableName+"` (id INT64, name STRING(MAX)) PRIMARY KEY (id)"))
	require.NoError(t, client.CreateRole(ctx, Role{Name: "test_role"}))

	return client
}

func TestNormalizeGrant(t *testing.T) {
	grant := normalizeGrant(Grant{
		RoleName:     "test_role",
		Privilege:    "select",
		ResourceType: "table",
		Resource:     testGrantTableName,
	})
	assert.Equal(t, "SELECT", grant.Privilege)
	assert.Equal(t, "TABLE", grant.ResourceType)
	// Fields outside Privilege/ResourceType are left untouched.
	assert.Equal(t, "test_role", grant.RoleName)
	assert.Equal(t, testGrantTableName, grant.Resource)
}

func TestNewGrantTemplateDataAcceptsEitherCase(t *testing.T) {
	data, err := newGrantTemplateData(Grant{
		RoleName:     "test_role",
		Privilege:    "select",
		ResourceType: "table",
		Resource:     testGrantTableName,
	})
	require.NoError(t, err)
	assert.Equal(t, "SELECT", data.Privilege)
	assert.Equal(t, "TABLE", data.ResourceType)
}

func TestValidateGrantAcceptsEitherCase(t *testing.T) {
	// ValidateGrant normalizes internally, so a provider-layer caller can
	// validate a Grant straight from user config without normalizing first.
	err := ValidateGrant(Grant{
		RoleName:     "test_role",
		Privilege:    "select",
		ResourceType: "table",
		Resource:     testGrantTableName,
	})
	assert.NoError(t, err)
}

func TestValidateGrantRejectsUnsupportedResourceTypes(t *testing.T) {
	// SEQUENCE, SCHEMA, and TABLE FUNCTION are part of Spanner's documented
	// grant vocabulary, but deliberately excluded from validResourceTypes:
	// GrantExists has no confirmed INFORMATION_SCHEMA view to read a grant
	// back on any of them, so allowing CreateGrant to accept them would let
	// Terraform create a grant that Read can never detect, looping
	// create->drop forever. See validResourceTypes' doc comment.
	for _, resourceType := range []string{"SEQUENCE", "SCHEMA", "TABLE FUNCTION"} {
		err := ValidateGrant(Grant{
			RoleName:     "test_role",
			Privilege:    "SELECT",
			ResourceType: resourceType,
			Resource:     "some_resource",
		})
		assert.Errorf(t, err, "expected ResourceType %q to be rejected", resourceType)
	}
}

func TestQuoteQualifiedIdentifier(t *testing.T) {
	quoted, err := quoteQualifiedIdentifier("my_table")
	require.NoError(t, err)
	assert.Equal(t, "`my_table`", quoted)

	quoted, err = quoteQualifiedIdentifier("my_schema.my_table")
	require.NoError(t, err)
	assert.Equal(t, "`my_schema`.`my_table`", quoted)

	_, err = quoteQualifiedIdentifier("bad name")
	assert.Error(t, err)

	_, err = quoteQualifiedIdentifier("bad schema.my_table")
	assert.Error(t, err)
}

func TestCreateGrantInvalidRoleName(t *testing.T) {
	// Validation happens before any network call, so no emulator is needed.
	client := &Client{}

	err := client.CreateGrant(context.Background(), Grant{
		RoleName:     "bad role!",
		Privilege:    "SELECT",
		ResourceType: "TABLE",
		Resource:     testGrantTableName,
	})
	assert.Error(t, err)
}

func TestCreateGrantInvalidResourceName(t *testing.T) {
	client := &Client{}

	err := client.CreateGrant(context.Background(), Grant{
		RoleName:     "test_role",
		Privilege:    "SELECT",
		ResourceType: "TABLE",
		Resource:     "bad table!",
	})
	assert.Error(t, err)
}

func TestCreateGrantInvalidPrivilege(t *testing.T) {
	client := &Client{}

	// A privilege outside Spanner's documented vocabulary must be rejected
	// for every resource type, not just TABLE - this used to slip through
	// unvalidated straight into the DDL for any ResourceType != "TABLE".
	err := client.CreateGrant(context.Background(), Grant{
		RoleName:     "test_role",
		Privilege:    "DROP TABLE foo; --",
		ResourceType: "VIEW",
		Resource:     "test_view",
	})
	assert.Error(t, err)
}

func TestCreateGrantInvalidResourceType(t *testing.T) {
	client := &Client{}

	err := client.CreateGrant(context.Background(), Grant{
		RoleName:     "test_role",
		Privilege:    "SELECT",
		ResourceType: "not a real resource type",
		Resource:     testGrantTableName,
	})
	assert.Error(t, err)
}

// skipGrantExistsEmulatorGap marks a test as skipped due to a known
// cloud-spanner-emulator limitation: INFORMATION_SCHEMA.TABLE_PRIVILEGES
// (and, by the same gap, .COLUMN_PRIVILEGES) does not exist in the
// emulator's schema catalog at all - the same class of gap documented for
// INFORMATION_SCHEMA.ROLES in CLAUDE.md and skipGetRoleEmulatorGap. GrantExists
// is written against real Spanner's documented INFORMATION_SCHEMA behavior;
// unskip once the upstream issue is resolved or when testing against a real
// GCP project.
//
// https://github.com/GoogleCloudPlatform/cloud-spanner-emulator/issues/350
func skipGrantExistsEmulatorGap(t *testing.T) {
	t.Skip("cloud-spanner-emulator does not implement INFORMATION_SCHEMA.TABLE_PRIVILEGES/COLUMN_PRIVILEGES: " +
		"https://github.com/GoogleCloudPlatform/cloud-spanner-emulator/issues/350")
}

func TestCreateAndDeleteGrant(t *testing.T) {
	client := newTestClientWithTableAndRole(t)
	ctx := context.Background()

	grant := Grant{
		RoleName:     "test_role",
		Privilege:    "SELECT",
		ResourceType: "TABLE",
		Resource:     testGrantTableName,
	}

	require.NoError(t, client.CreateGrant(ctx, grant))
	require.NoError(t, client.DeleteGrant(ctx, grant))
}

func TestGrantExistsAfterCreate(t *testing.T) {
	skipGrantExistsEmulatorGap(t)

	client := newTestClientWithTableAndRole(t)
	ctx := context.Background()

	grant := Grant{
		RoleName:     "test_role",
		Privilege:    "SELECT",
		ResourceType: "TABLE",
		Resource:     testGrantTableName,
	}

	require.NoError(t, client.CreateGrant(ctx, grant))

	found, err := client.GrantExists(ctx, grant)
	require.NoError(t, err)
	assert.True(t, found, "expected grant to be found after CreateGrant")

	require.NoError(t, client.DeleteGrant(ctx, grant))

	found, err = client.GrantExists(ctx, grant)
	require.NoError(t, err)
	assert.False(t, found, "expected grant to be gone after DeleteGrant")
}

// TestGrantColumnLevel is skipped entirely, not just its GrantExists checks:
// the emulator rejects column-level GRANT DDL outright ("Emulator does not
// yet support column level access controls"), so CreateGrant itself cannot
// be exercised here, unlike the table/view-level gap which only blocks
// read-back. GrantExists's column-level branch is written against real
// Spanner's documented INFORMATION_SCHEMA.COLUMN_PRIVILEGES behavior;
// unskip when testing against a real GCP project.
func TestGrantColumnLevel(t *testing.T) {
	t.Skip("cloud-spanner-emulator does not support column-level access control DDL at all")

	client := newTestClientWithTableAndRole(t)
	ctx := context.Background()

	grant := Grant{
		RoleName:     "test_role",
		Privilege:    "SELECT",
		ResourceType: "TABLE",
		Resource:     testGrantTableName,
		Columns:      []string{"name"},
	}

	require.NoError(t, client.CreateGrant(ctx, grant))

	found, err := client.GrantExists(ctx, grant)
	require.NoError(t, err)
	assert.True(t, found, "expected column-level grant to be found after CreateGrant")

	require.NoError(t, client.DeleteGrant(ctx, grant))

	found, err = client.GrantExists(ctx, grant)
	require.NoError(t, err)
	assert.False(t, found, "expected column-level grant to be gone after DeleteGrant")
}

// TestDeleteGrantAlreadyGone documents an empirical result, not a
// guarantee: on the cloud-spanner-emulator, REVOKE on a grant that was
// never created (role, table, and grant all absent) does not error, unlike
// CQL's REVOKE on ScyllaDB (see scylladb/grant.go's isAlreadyGoneError). But
// the emulator is separately known to accept a duplicate CREATE ROLE
// without erroring too (roles_test.go's TestCreateRole), simply because it
// doesn't track role/grant metadata at all - so this leniency may be an
// emulator gap rather than real Spanner's documented behavior. Re-verify
// against a real GCP project before relying on this; if real Spanner
// errors here, DeleteGrant will need scylladb.DeleteGrant's
// swallow-if-already-gone shim added back.
func TestDeleteGrantAlreadyGone(t *testing.T) {
	client := newTestClient(t)
	ctx := context.Background()

	err := client.DeleteGrant(ctx, Grant{
		RoleName:     "it_should_not_exist",
		Privilege:    "SELECT",
		ResourceType: "TABLE",
		Resource:     "it_should_not_exist_either",
	})
	assert.NoError(t, err, "emulator-observed behavior, not yet confirmed against real Spanner - see doc comment")
}

// TestDeleteGrantTwice is the same empirical caveat as
// TestDeleteGrantAlreadyGone, for the narrower case where the role and
// table still exist but the grant itself was already revoked once.
func TestDeleteGrantTwice(t *testing.T) {
	client := newTestClientWithTableAndRole(t)
	ctx := context.Background()

	grant := Grant{
		RoleName:     "test_role",
		Privilege:    "SELECT",
		ResourceType: "TABLE",
		Resource:     testGrantTableName,
	}
	require.NoError(t, client.CreateGrant(ctx, grant))
	require.NoError(t, client.DeleteGrant(ctx, grant))

	err := client.DeleteGrant(ctx, grant)
	assert.NoError(t, err, "emulator-observed behavior, not yet confirmed against real Spanner - see TestDeleteGrantAlreadyGone's doc comment")
}
