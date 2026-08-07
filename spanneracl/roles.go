// Copyright RetailNext, Inc. 2026

package spanneracl

import (
	"context"
	"errors"
	"fmt"
	"unicode"

	"cloud.google.com/go/spanner"
	databasepb "cloud.google.com/go/spanner/admin/database/apiv1/databasepb"
	"google.golang.org/api/iterator"
)

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
	if err := validateRoleName(role.Name); err != nil {
		return err
	}

	return c.updateDatabaseDdl(ctx, fmt.Sprintf("CREATE ROLE `%s`", role.Name))
}

// DeleteRole drops a database role.
func (c *Client) DeleteRole(ctx context.Context, role Role) error {
	if err := validateRoleName(role.Name); err != nil {
		return err
	}

	return c.updateDatabaseDdl(ctx, fmt.Sprintf("DROP ROLE `%s`", role.Name))
}

// updateDatabaseDdl issues a single DDL statement against the database and
// waits for the resulting long-running operation to complete.
func (c *Client) updateDatabaseDdl(ctx context.Context, statement string) error {
	op, err := c.AdminClient.UpdateDatabaseDdl(ctx, &databasepb.UpdateDatabaseDdlRequest{
		Database:   c.DatabasePath,
		Statements: []string{statement},
	})
	if err != nil {
		return fmt.Errorf("failed to submit ddl statement %q: %w", statement, err)
	}

	if err := op.Wait(ctx); err != nil {
		return fmt.Errorf("failed to apply ddl statement %q: %w", statement, err)
	}

	return nil
}

// validateRoleName only allows alphanumeric characters and underscores in
// role names.
func validateRoleName(name string) error {
	if name == "" {
		return errors.New("role name must not be empty")
	}
	for _, r := range name {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' {
			return fmt.Errorf("invalid character in role name: %c", r)
		}
	}
	return nil
}
