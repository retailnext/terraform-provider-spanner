// Copyright RetailNext, Inc. 2026

package provider

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	databasepb "cloud.google.com/go/spanner/admin/database/apiv1/databasepb"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/retailnext/terraform-provider-spanner/spanneracl"
	"github.com/stretchr/testify/require"
)

// realSpannerTestDatabaseEnv names the environment variable holding the full
// path of a real Cloud Spanner database, e.g.
// "projects/my-project/instances/my-instance/databases/my-database".
// Acceptance tests that need real Spanner - the emulator lacks
// INFORMATION_SCHEMA.TABLE_PRIVILEGES and ROLES, see
// https://github.com/GoogleCloudPlatform/cloud-spanner-emulator/issues/350 -
// run against it when it's set, and are skipped otherwise. They also need
// TF_ACC=1, like every resource.Test, and authenticate with Application
// Default Credentials, which need DDL rights on the database (roles/spanner.databaseUser
// grants it).
const realSpannerTestDatabaseEnv = "SPANNER_TEST_DATABASE"

// realSpannerAccTest is the setup shared by acceptance tests against a real
// database: a client for out-of-band setup and checks, a provider block for
// the same database, and a unique name suffix for the objects a test creates.
type realSpannerAccTest struct {
	client         *spanneracl.Client
	providerConfig string
	suffix         string
}

// newRealSpannerAccTest skips the test unless SPANNER_TEST_DATABASE and
// TF_ACC are both set, then connects to the database.
func newRealSpannerAccTest(t *testing.T) *realSpannerAccTest {
	t.Helper()

	databasePath := os.Getenv(realSpannerTestDatabaseEnv)
	if databasePath == "" {
		t.Skipf("%s not set: this acceptance test needs a real Cloud Spanner database", realSpannerTestDatabaseEnv)
	}
	if os.Getenv("TF_ACC") == "" {
		t.Skip("TF_ACC not set: acceptance tests are skipped unless TF_ACC=1")
	}

	// projects/{project}/instances/{instance}/databases/{database}
	parts := strings.Split(databasePath, "/")
	if len(parts) != 6 || parts[0] != "projects" || parts[2] != "instances" || parts[4] != "databases" {
		t.Fatalf("%s=%q is not a projects/{p}/instances/{i}/databases/{d} path", realSpannerTestDatabaseEnv, databasePath)
	}

	client, err := spanneracl.NewClient(context.Background(), databasePath)
	require.NoError(t, err)
	t.Cleanup(client.Close)

	return &realSpannerAccTest{
		client: client,
		providerConfig: fmt.Sprintf(`
provider "spanner" {
  project  = %q
  instance = %q
  database = %q
}
`, parts[1], parts[3], parts[5]),
		suffix: fmt.Sprintf("%d_%d", time.Now().Unix(), rand.IntN(1_000_000)),
	}
}

// updateDdl applies DDL statements directly, for setup the provider has no
// resource for (e.g. creating the table under test).
func (a *realSpannerAccTest) updateDdl(t *testing.T, statements ...string) error {
	t.Helper()
	ctx := context.Background()
	op, err := a.client.AdminClient.UpdateDatabaseDdl(ctx, &databasepb.UpdateDatabaseDdlRequest{
		Database:   a.client.DatabasePath,
		Statements: statements,
	})
	if err != nil {
		return err
	}
	return op.Wait(ctx)
}

// createTable creates a uniquely named table and registers a cleanup that
// removes it, along with any grants and roles still left from a failed run.
// Registered before the table is created so a partial setup is still cleaned.
func (a *realSpannerAccTest) createTable(t *testing.T, roles ...string) string {
	t.Helper()
	table := "acltest_table_" + a.suffix

	t.Cleanup(func() {
		ctx := context.Background()
		if err := a.client.RevokeAllGrantsOnIdentifier(ctx, table, "TABLE"); err != nil {
			t.Errorf("cleanup: failed to revoke grants on %s: %v", table, err)
		}
		for _, role := range roles {
			_, err := a.client.GetRole(ctx, role)
			if errors.Is(err, spanneracl.ErrRoleNotFound) {
				continue
			}
			if err == nil {
				err = a.client.DeleteRole(ctx, spanneracl.Role{Name: role})
			}
			if err != nil {
				t.Errorf("cleanup: failed to drop role %s: %v", role, err)
			}
		}
		if err := a.updateDdl(t, "DROP TABLE `"+table+"`"); err != nil && !strings.Contains(err.Error(), "not found") {
			t.Errorf("cleanup: failed to drop table %s: %v", table, err)
		}
	})

	require.NoError(t, a.updateDdl(t,
		"CREATE TABLE `"+table+"` (id INT64, name STRING(MAX)) PRIMARY KEY (id)"))
	return table
}

// checkPermissions asserts the table's actual table-level grants, read straight
// from Spanner, are exactly want.
func (a *realSpannerAccTest) checkPermissions(table string, want map[string][]string) resource.TestCheckFunc {
	return func(_ *terraform.State) error {
		got, err := a.client.GetAllRolePermissionsPerId(context.Background(), table, "TABLE")
		if err != nil {
			return err
		}
		normalize := func(m map[string][]string) map[string][]string {
			out := make(map[string][]string, len(m))
			for role, privileges := range m {
				sorted := slices.Clone(privileges)
				slices.Sort(sorted)
				out[role] = sorted
			}
			return out
		}
		if !reflect.DeepEqual(normalize(got), normalize(want)) {
			return fmt.Errorf("grants on %s: got %v, want %v", table, got, want)
		}
		return nil
	}
}

// TestAccTableGrantsResource runs spanner_table_grants through its lifecycle
// against a real database: create, update, re-apply over out-of-band grants
// (table- and column-level), import, and destroy.
func TestAccTableGrantsResource(t *testing.T) {
	a := newRealSpannerAccTest(t)
	reader := "acltest_reader_" + a.suffix
	writer := "acltest_writer_" + a.suffix
	table := a.createTable(t, reader, writer)

	config := func(grants string) string {
		return a.providerConfig + fmt.Sprintf(`
resource "spanner_role" "reader" {
  name = %q
}

resource "spanner_role" "writer" {
  name = %q
}

resource "spanner_table_grants" "test" {
  table = %q
%s
}
`, reader, writer, table, grants)
	}
	const readerWriterGrants = `
  grant {
    role       = spanner_role.reader.name
    privileges = ["SELECT"]
  }
  grant {
    role       = spanner_role.writer.name
    privileges = ["SELECT", "INSERT", "UPDATE"]
  }
`
	const readerOnlyGrants = `
  grant {
    role       = spanner_role.reader.name
    privileges = ["SELECT", "DELETE"]
  }
`

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy: func(_ *terraform.State) error {
			// Destroy revokes every grant on the table; the roles are gone
			// too, so nothing may be left.
			perms, err := a.client.GetAllRolePermissionsPerId(context.Background(), table, "TABLE")
			if err != nil {
				return err
			}
			if len(perms) != 0 {
				return fmt.Errorf("grants left on %s after destroy: %v", table, perms)
			}
			return nil
		},
		Steps: []resource.TestStep{
			{
				Config: config(readerWriterGrants),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("spanner_table_grants.test", "id", table),
					resource.TestCheckResourceAttr("spanner_table_grants.test", "grant.#", "2"),
					resource.TestCheckResourceAttr("spanner_table_grants.test", "column_level_grants.#", "0"),
					a.checkPermissions(table, map[string][]string{
						reader: {"SELECT"},
						writer: {"SELECT", "INSERT", "UPDATE"},
					}),
				),
			},
			{
				// writer is dropped from the bindings, so loses everything;
				// reader gains DELETE.
				Config: config(readerOnlyGrants),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("spanner_table_grants.test", "grant.#", "1"),
					a.checkPermissions(table, map[string][]string{
						reader: {"SELECT", "DELETE"},
					}),
				),
			},
			{
				// Grants made outside Terraform - a table-level grant for a
				// role not in the bindings, and a column-level grant - are
				// revoked by the next apply.
				PreConfig: func() {
					ctx := context.Background()
					require.NoError(t, a.client.CreateGrant(ctx, spanneracl.Grant{
						RoleName: writer, Privilege: "SELECT", ResourceType: "TABLE", Resource: table,
					}))
					require.NoError(t, a.client.CreateGrant(ctx, spanneracl.Grant{
						RoleName: writer, Privilege: "UPDATE", ResourceType: "TABLE", Resource: table,
						Columns: []string{"name"},
					}))
				},
				Config: config(readerOnlyGrants),
				Check: resource.ComposeAggregateTestCheckFunc(
					a.checkPermissions(table, map[string][]string{
						reader: {"SELECT", "DELETE"},
					}),
					func(_ *terraform.State) error {
						exists, err := a.client.GrantExists(context.Background(), spanneracl.Grant{
							RoleName: writer, Privilege: "UPDATE", ResourceType: "TABLE", Resource: table,
							Columns: []string{"name"},
						})
						if err != nil {
							return err
						}
						if exists {
							return fmt.Errorf("column-level UPDATE(name) for %s survived the apply", writer)
						}
						return nil
					},
				),
			},
			{
				// A column-level grant made outside Terraform, with no other
				// change, must still show up as drift: Read reports it in
				// column_level_grants, which is always planned as empty.
				PreConfig: func() {
					require.NoError(t, a.client.CreateGrant(context.Background(), spanneracl.Grant{
						RoleName: reader, Privilege: "UPDATE", ResourceType: "TABLE", Resource: table,
						Columns: []string{"name"},
					}))
				},
				Config:             config(readerOnlyGrants),
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
			{
				// The next apply revokes it.
				Config: config(readerOnlyGrants),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("spanner_table_grants.test", "column_level_grants.#", "0"),
					a.checkPermissions(table, map[string][]string{
						reader: {"SELECT", "DELETE"},
					}),
					func(_ *terraform.State) error {
						columnPerms, err := a.client.GetColumnLevelOnlyPermissionsPerId(context.Background(), table, "TABLE")
						if err != nil {
							return err
						}
						if len(columnPerms) != 0 {
							return fmt.Errorf("column-level grants survived the apply: %v", columnPerms)
						}
						return nil
					},
				),
			},
			{
				ResourceName:      "spanner_table_grants.test",
				ImportState:       true,
				ImportStateId:     table,
				ImportStateVerify: true,
			},
		},
	})
}
