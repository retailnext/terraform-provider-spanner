// Copyright RetailNext, Inc. 2026

package spanneracl

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"cloud.google.com/go/spanner"
	databasepb "cloud.google.com/go/spanner/admin/database/apiv1/databasepb"
	"google.golang.org/api/iterator"
)

// maxDdlStatementsPerBatch caps how many statements updateDatabaseDdl sends in
// one UpdateDatabaseDdl request. Spanner's published quotas list no per-request
// statement count (only a 10 MiB size limit), but a 20-statement limit for
// GRANT/REVOKE batches is set as precaution and to avoid long-running requests.
const maxDdlStatementsPerBatch = 20

// GetRole looks up a database role by name via INFORMATION_SCHEMA.ROLES.
//
// INFORMATION_SCHEMA.ROLES returns the querying principal's current role
// plus any roles reachable through inheritance - it is not an unfiltered
// listing of every role in the database. This is a non-issue for a
// connection that isn't itself scoped to a fine-grained-access-control
// (FGAC) database role (e.g. one authenticated via a plain
// roles/spanner.databaseUser IAM grant, as documented in the README): such
// connections aren't subject to FGAC row filtering and see every role. It
// only matters if this client's credentials are ever bound into FGAC via an
// IAM-conditional roles/spanner.fineGrainedAccessUser grant.
//
// The Database Admin API's ListDatabaseRoles avoids that caveat entirely,
// but requires the spanner.databaseRoles.list permission, which is only
// granted by roles/spanner.admin - not roles/spanner.viewer or
// roles/spanner.databaseUser - so it was not used here.
func (c *Client) GetRole(ctx context.Context, roleName string) (Role, error) {
	stmt := spanner.Statement{
		SQL: `SELECT ROLE_NAME FROM INFORMATION_SCHEMA.ROLES WHERE ROLE_NAME = @role_name`,
		Params: map[string]any{
			"role_name": roleName,
		},
	}

	iter := c.DataClient.Single().Query(ctx, stmt)
	defer iter.Stop()

	row, err := iter.Next()
	if errors.Is(err, iterator.Done) {
		return Role{}, ErrRoleNotFound
	}
	if err != nil {
		return Role{}, fmt.Errorf("failed to query role %q: %w", roleName, err)
	}

	var role Role
	if err := row.Columns(&role.Name); err != nil {
		return Role{}, fmt.Errorf("failed to scan role %q: %w", roleName, err)
	}

	return role, nil
}

// CreateRole creates a new database role.
func (c *Client) CreateRole(ctx context.Context, role Role) error {
	if err := validateIdentifier(role.Name); err != nil {
		return fmt.Errorf("invalid role name %q: %w", role.Name, err)
	}

	return c.updateDatabaseDdl(ctx, fmt.Sprintf("CREATE ROLE `%s`", role.Name))
}

// DeleteRole drops a database role.
func (c *Client) DeleteRole(ctx context.Context, role Role) error {
	if err := validateIdentifier(role.Name); err != nil {
		return fmt.Errorf("invalid role name %q: %w", role.Name, err)
	}

	return c.updateDatabaseDdl(ctx, fmt.Sprintf("DROP ROLE `%s`", role.Name))
}

// updateDatabaseDdl issues one or more DDL statements against the database in
// order, in chunks of at most maxDdlStatementsPerBatch, waiting for each
// chunk's long-running operation to complete before sending the next.
//
// This is NOT atomic, within a chunk or across chunks. Per the
// UpdateDatabaseDdl API docs, the server checks that every statement in a
// request is executable (syntax, referenced objects exist) before enqueueing
// any of them - so those failures reject that whole chunk with nothing in it
// applied - but statements are then applied "in order but not necessarily all
// at once", and if one fails at execution time the rest of its chunk is
// cancelled while the ones before it stay applied. Earlier chunks are never
// rolled back, and later chunks are not sent once one fails.
func (c *Client) updateDatabaseDdl(ctx context.Context, statements ...string) error {
	applied := 0
	for chunk := range slices.Chunk(statements, maxDdlStatementsPerBatch) {
		if err := c.updateDatabaseDdlBatch(ctx, chunk); err != nil {
			if applied > 0 {
				return fmt.Errorf("%w (in addition, the first %d of %d statements, in earlier batches, were already applied and are not rolled back)",
					err, applied, len(statements))
			}
			return err
		}
		applied += len(chunk)
	}
	return nil
}

// updateDatabaseDdlBatch sends statements as a single UpdateDatabaseDdl request
// and waits for its long-running operation to complete.
func (c *Client) updateDatabaseDdlBatch(ctx context.Context, statements []string) error {
	op, err := c.AdminClient.UpdateDatabaseDdl(ctx, &databasepb.UpdateDatabaseDdlRequest{
		Database:   c.DatabasePath,
		Statements: statements,
	})
	if err != nil {
		return fmt.Errorf("failed to submit ddl statements %q: %w", statements, err)
	}

	// A submit error means the request was rejected before being enqueued, so
	// nothing in it was applied. A Wait error can come after some leading
	// statements of this batch already committed, so say so explicitly.
	if err := op.Wait(ctx); err != nil {
		return fmt.Errorf("failed to apply ddl statements %q (some leading statements of this batch may already be applied): %w",
			statements, err)
	}

	return nil
}
