// Copyright RetailNext, Inc. 2026

package spanneracl

import (
	"context"
	"fmt"
	"math/rand/v2"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// realSpannerTestDatabaseEnv names the environment variable holding the full
// path of a real Cloud Spanner database, e.g.
// "projects/my-project/instances/my-instance/databases/my-database". Tests
// that the emulator can't support (INFORMATION_SCHEMA.TABLE_PRIVILEGES and
// friends are missing there - see
// https://github.com/GoogleCloudPlatform/cloud-spanner-emulator/issues/350)
// run against it when set, and are skipped otherwise. Authenticates with
// Application Default Credentials; the principal needs DDL rights on the
// database (e.g. roles/spanner.databaseAdmin).
const realSpannerTestDatabaseEnv = "SPANNER_TEST_DATABASE"

// realSpannerFixture is a table plus a set of roles, all uniquely named for
// this test so concurrent runs against a shared database don't collide.
type realSpannerFixture struct {
	client *Client
	table  string
	roles  []string

	// What setup actually created, so cleanup never tries to drop something
	// that a failed setup step never got around to creating.
	tableCreated bool
	createdRoles []string
}

// newRealSpannerFixture connects to the database named by
// SPANNER_TEST_DATABASE (skipping the test if unset), creates a uniquely named
// table and roleCount uniquely named roles, and registers a cleanup that
// revokes every privilege on the table, then drops the roles and the table.
func newRealSpannerFixture(t *testing.T, roleCount int) *realSpannerFixture {
	t.Helper()

	databasePath := os.Getenv(realSpannerTestDatabaseEnv)
	if databasePath == "" {
		t.Skipf("%s not set: this test needs a real Cloud Spanner database, since the emulator "+
			"does not implement INFORMATION_SCHEMA.TABLE_PRIVILEGES: "+
			"https://github.com/GoogleCloudPlatform/cloud-spanner-emulator/issues/350",
			realSpannerTestDatabaseEnv)
	}

	ctx := context.Background()
	client, err := NewClient(ctx, databasePath)
	require.NoError(t, err)
	t.Cleanup(client.Close)

	suffix := fmt.Sprintf("%d_%d", time.Now().Unix(), rand.IntN(1_000_000))
	f := &realSpannerFixture{
		client: client,
		table:  "acltest_table_" + suffix,
	}
	for i := range roleCount {
		f.roles = append(f.roles, fmt.Sprintf("acltest_role%d_%s", i, suffix))
	}

	// Registered before creating anything, so a failure partway through
	// setup still cleans up whatever did get created. Each step is
	// best-effort: report and keep going, so one failure doesn't strand the rest.
	t.Cleanup(func() { f.cleanup(t) })

	require.NoError(t, client.updateDatabaseDdl(ctx,
		"CREATE TABLE `"+f.table+"` (id INT64, name STRING(MAX)) PRIMARY KEY (id)"))
	f.tableCreated = true
	for _, role := range f.roles {
		require.NoError(t, client.CreateRole(ctx, Role{Name: role}))
		f.createdRoles = append(f.createdRoles, role)
	}

	return f
}

func (f *realSpannerFixture) cleanup(t *testing.T) {
	ctx := context.Background()
	if !f.tableCreated {
		return
	}

	// Spanner refuses to drop a role that still holds privileges, so revoke
	// everything on the table first - column-level grants, then table-level.
	id := ParsedIdentifier{ResourceType: "TABLE", ResourceName: f.table, Original: f.table}
	perms, err := f.client.getAllRolePermissionsPerId(ctx, id)
	if err != nil {
		t.Errorf("cleanup: failed to list privileges on %s: %v", f.table, err)
	}
	columnPerms, err := f.client.getColumnLevelOnlyPermissions(ctx, id, perms)
	if err != nil {
		t.Errorf("cleanup: failed to list column privileges on %s: %v", f.table, err)
	}
	for role, privs := range columnPerms {
		for priv, columns := range privs {
			if err := f.client.DeleteGrant(ctx, Grant{
				RoleName: role, Privilege: priv, ResourceType: "TABLE", Resource: f.table, Columns: columns,
			}); err != nil {
				t.Errorf("cleanup: failed to revoke %s(%v) on %s from %s: %v", priv, columns, f.table, role, err)
			}
		}
	}
	for role, privs := range perms {
		for _, priv := range privs {
			if err := f.client.DeleteGrant(ctx, Grant{
				RoleName: role, Privilege: priv, ResourceType: "TABLE", Resource: f.table,
			}); err != nil {
				t.Errorf("cleanup: failed to revoke %s on %s from %s: %v", priv, f.table, role, err)
			}
		}
	}

	for _, role := range f.createdRoles {
		if err := f.client.DeleteRole(ctx, Role{Name: role}); err != nil {
			t.Errorf("cleanup: failed to drop role %s: %v", role, err)
		}
	}
	if err := f.client.updateDatabaseDdl(ctx, "DROP TABLE `"+f.table+"`"); err != nil {
		t.Errorf("cleanup: failed to drop table %s: %v", f.table, err)
	}
}
