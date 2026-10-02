// Copyright RetailNext, Inc. 2026

package spanneracl

import (
	"context"
	"fmt"
	"testing"

	"cloud.google.com/go/spanner"
	database "cloud.google.com/go/spanner/admin/database/apiv1"
	databasepb "cloud.google.com/go/spanner/admin/database/apiv1/databasepb"
	instance "cloud.google.com/go/spanner/admin/instance/apiv1"
	instancepb "cloud.google.com/go/spanner/admin/instance/apiv1/instancepb"
	"github.com/retailnext/terraform-provider-spanner/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/option"
	"google.golang.org/api/option/internaloption"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

const (
	testInstanceID = "test-instance"
	testDatabaseID = "test-db"
)

// newTestClient starts a Cloud Spanner emulator, provisions a test
// instance and database inside it, and returns a Client connected to that
// database.
func newTestClient(t *testing.T) *Client {
	ctx := context.Background()

	uri, projectID := testutil.NewTestSpannerContainer(t)

	opts := []option.ClientOption{
		option.WithEndpoint(uri),
		option.WithGRPCDialOption(grpc.WithTransportCredentials(insecure.NewCredentials())),
		option.WithoutAuthentication(),
		internaloption.SkipDialSettingsValidation(),
	}

	instanceAdmin, err := instance.NewInstanceAdminClient(ctx, opts...)
	require.NoError(t, err)
	defer instanceAdmin.Close()

	instanceOp, err := instanceAdmin.CreateInstance(ctx, &instancepb.CreateInstanceRequest{
		Parent:     fmt.Sprintf("projects/%s", projectID),
		InstanceId: testInstanceID,
		Instance:   &instancepb.Instance{DisplayName: testInstanceID},
	})
	require.NoError(t, err)
	_, err = instanceOp.Wait(ctx)
	require.NoError(t, err)

	databaseAdmin, err := database.NewDatabaseAdminClient(ctx, opts...)
	require.NoError(t, err)
	defer databaseAdmin.Close()

	databaseOp, err := databaseAdmin.CreateDatabase(ctx, &databasepb.CreateDatabaseRequest{
		Parent:          fmt.Sprintf("projects/%s/instances/%s", projectID, testInstanceID),
		CreateStatement: fmt.Sprintf("CREATE DATABASE `%s`", testDatabaseID),
	})
	require.NoError(t, err)
	_, err = databaseOp.Wait(ctx)
	require.NoError(t, err)

	databasePath := fmt.Sprintf("projects/%s/instances/%s/databases/%s", projectID, testInstanceID, testDatabaseID)

	client, err := NewClient(ctx, databasePath, opts...)
	require.NoError(t, err)
	t.Cleanup(client.Close)

	return client
}

// skipGetRoleEmulatorGap marks a test as skipped due to a known
// cloud-spanner-emulator limitation: CREATE ROLE/DROP ROLE DDL is accepted,
// but the emulator never surfaces roles back out again, through
// INFORMATION_SCHEMA.DATABASE_ROLES, GetDatabaseDdl, or the
// databaseRoles.list API. GetRole is written against real Spanner's
// documented INFORMATION_SCHEMA behavior; unskip these once the upstream
// issue is resolved.
//
// https://github.com/GoogleCloudPlatform/cloud-spanner-emulator/issues/350
func skipGetRoleEmulatorGap(t *testing.T) {
	t.Skip("cloud-spanner-emulator does not implement INFORMATION_SCHEMA.DATABASE_ROLES: " +
		"https://github.com/GoogleCloudPlatform/cloud-spanner-emulator/issues/350")
}

func TestCreateRoleInvalidName(t *testing.T) {
	// Validation happens before any network call, so no emulator is needed.
	client := &Client{}

	err := client.CreateRole(context.Background(), Role{Name: "bad role!"})
	assert.Error(t, err)
}

func TestGetRoleNotFound(t *testing.T) {
	skipGetRoleEmulatorGap(t)

	client := newTestClient(t)
	ctx := context.Background()

	_, err := client.GetRole(ctx, "it_should_not_exist")
	assert.ErrorIs(t, err, ErrRoleNotFound)
}

func TestCreateRole(t *testing.T) {
	client := newTestClient(t)
	ctx := context.Background()

	inputRole := Role{Name: "test_role"}

	require.NoError(t, client.CreateRole(ctx, inputRole))

	// On real Spanner, re-creating the same role fails since there is no
	// "CREATE ROLE IF NOT EXISTS". The emulator doesn't track role
	// metadata at all (see skipGetRoleEmulatorGap), so it lets this
	// through as a silent no-op instead of erroring - that's an emulator
	// gap, not asserted here.
}

func TestGetRoleAfterCreate(t *testing.T) {
	skipGetRoleEmulatorGap(t)

	client := newTestClient(t)
	ctx := context.Background()

	inputRole := Role{Name: "test_role"}
	require.NoError(t, client.CreateRole(ctx, inputRole))

	role, err := client.GetRole(ctx, inputRole.Name)
	require.NoError(t, err)
	assert.Equal(t, inputRole, role)
}

func TestDeleteRole(t *testing.T) {
	client := newTestClient(t)
	ctx := context.Background()

	inputRole := Role{Name: "test_role"}

	require.NoError(t, client.CreateRole(ctx, inputRole))
	require.NoError(t, client.DeleteRole(ctx, inputRole))
}

// chunkTestTables returns n CREATE TABLE statements for tables chunk_<i>.
func chunkTestTables(n int) []string {
	statements := make([]string, n)
	for i := range n {
		statements[i] = fmt.Sprintf("CREATE TABLE `chunk_%02d` (id INT64) PRIMARY KEY (id)", i)
	}
	return statements
}

// listChunkTestTables returns the names of every chunk_* table, sorted.
func listChunkTestTables(t *testing.T, client *Client) []string {
	iter := client.DataClient.Single().Query(context.Background(), spanner.Statement{
		SQL: `SELECT TABLE_NAME FROM INFORMATION_SCHEMA.TABLES
				WHERE TABLE_SCHEMA = '' AND STARTS_WITH(TABLE_NAME, 'chunk_') ORDER BY TABLE_NAME`,
	})
	defer iter.Stop()

	var names []string
	require.NoError(t, iter.Do(func(row *spanner.Row) error {
		var name string
		if err := row.Columns(&name); err != nil {
			return err
		}
		names = append(names, name)
		return nil
	}))
	return names
}

// TestUpdateDatabaseDdlChunksLargeBatches checks that more than
// maxDdlStatementsPerBatch statements are all applied, in order, across chunks.
func TestUpdateDatabaseDdlChunksLargeBatches(t *testing.T) {
	client := newTestClient(t)
	statements := chunkTestTables(maxDdlStatementsPerBatch*2 + 5)

	require.NoError(t, client.updateDatabaseDdl(context.Background(), statements...))

	names := listChunkTestTables(t, client)
	assert.Len(t, names, len(statements))
}

// TestUpdateDatabaseDdlFailedChunkKeepsEarlierChunks checks the documented
// partial-application semantics: when a later chunk fails, earlier chunks stay
// applied, the failed chunk applies nothing (a duplicate CREATE TABLE fails
// server-side validation, which rejects the whole request), and the error says
// how many statements were already applied.
func TestUpdateDatabaseDdlFailedChunkKeepsEarlierChunks(t *testing.T) {
	client := newTestClient(t)
	statements := chunkTestTables(maxDdlStatementsPerBatch + 1)
	// Second chunk: a valid new table followed by a duplicate of chunk_00.
	statements = append(statements, statements[0])

	err := client.updateDatabaseDdl(context.Background(), statements...)
	require.Error(t, err)
	assert.ErrorContains(t, err, fmt.Sprintf("the first %d of %d statements", maxDdlStatementsPerBatch, len(statements)))

	names := listChunkTestTables(t, client)
	assert.Len(t, names, maxDdlStatementsPerBatch)
	assert.NotContains(t, names, fmt.Sprintf("chunk_%02d", maxDdlStatementsPerBatch))
}
