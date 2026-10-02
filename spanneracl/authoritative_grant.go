// Copyright RetailNext, Inc. 2026

package spanneracl

// This package provides structures and functions for managing authoritative grants and parsing
// resource identifiers in Spanner ACLs.
//
// Out of Scope
//
// - "ALL" privileges are out of scope for this package.
// - Grant bulk object permissions inside a schema. For example:
//     GRANT SELECT ON ALL TABLES IN SCHEMA MySchema TO ROLE myrole
// - SCHEMA-level grants (GRANT USAGE ON SCHEMA ... TO ROLE ...) are out of scope: there is no
//   confirmed INFORMATION_SCHEMA view for reading schema-level privileges back (checked the
//   information-schema, fgac-privileges, and named-schemas docs - only TABLE_PRIVILEGES/
//   COLUMN_PRIVILEGES and CHANGE_STREAM_PRIVILEGES are confirmed anywhere in Spanner's docs), so
//   GetAllRolePermissionsPerId could never detect an existing SCHEMA grant and
//   ApplyAuthoritativeBinding would try to (re-)create it forever. Matches the same reasoning
//   that dropped SEQUENCE/SCHEMA/TABLE FUNCTION from spanneracl.validResourceTypes in grant.go.
//   Revisit if a real read-back mechanism is ever confirmed.

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"cloud.google.com/go/spanner"
	"google.golang.org/api/iterator"
)

// ParsedIdentifier represents a parsed resource identifier.
type ParsedIdentifier struct {
	ResourceType string // "TABLE" or "VIEW" for now
	ResourceName string // name of the table or view
	Schema       string // name of the schema containing the table or view
	Original     string // original string as supplied by the user
}

// AuthoritativeBinding represents a set of privileges granted to a role.
type AuthoritativeBinding struct {
	Privileges []string
	Role       string
}

// Normalize updates the binding to ensure all privileges and the role are valid and returns an error if any are not.
func (b *AuthoritativeBinding) Normalize() error {
	normalizedPrivileges := []string{}
	if err := validateIdentifier(b.Role); err != nil {
		return fmt.Errorf("invalid role identifier: %w", err)
	}
	for _, priv := range b.Privileges {
		normalizedPriv := strings.ToUpper(priv)
		if !slices.Contains(validPrivileges, normalizedPriv) {
			return fmt.Errorf("privilege %q is not allowed in an authoritative binding", priv)
		}
		normalizedPrivileges = append(normalizedPrivileges, normalizedPriv)
	}
	b.Privileges = normalizedPrivileges
	return nil
}

// ParseIdentifier parses a resource identifier into its components based on the resource type.
// The expected format for the identifier is either "schema.resourceName" or "resourceName" depending on whether a schema is specified.
func ParseIdentifier(identifier string, resourceType string) (*ParsedIdentifier, error) {
	parsed := ParsedIdentifier{
		ResourceType: strings.ToUpper(resourceType),
		Original:     identifier,
	}
	if !slices.Contains(authGrantResourceTypes, parsed.ResourceType) {
		return nil, fmt.Errorf("unsupported resource type: %q", parsed.ResourceType)
	}
	parts := strings.Split(identifier, ".")
	if len(parts) >= 3 {
		return nil, fmt.Errorf("invalid identifier format: %q", identifier)
	}
	if len(parts) == 2 {
		if err := validateIdentifier(parts[0]); err != nil {
			return nil, fmt.Errorf("invalid schema identifier %q: %w", parts[0], err)
		}
		parsed.Schema = parts[0]
		parsed.ResourceName = parts[1]
	} else {
		parsed.ResourceName = parts[0]
	}
	if err := validateIdentifier(parsed.ResourceName); err != nil {
		return nil, fmt.Errorf("invalid resource identifier %q: %w", parsed.ResourceName, err)
	}

	return &parsed, nil
}

// ApplyAuthoritativeBinding makes bindings the complete set of table/view-level
// privileges on identifier: it revokes every column-level grant on the table,
// revokes every table-level grant not in bindings, and grants everything in
// bindings that's missing. Each role may appear in at most one binding.
//
// Every GRANT/REVOKE is rendered up front and applied in order via
// updateDatabaseDdl, in UpdateDatabaseDdl batches of at most
// maxDdlStatementsPerBatch. That is NOT a transaction - Spanner has no
// transactional DDL, and a failed batch leaves earlier batches applied (see
// updateDatabaseDdl).
func (c *Client) ApplyAuthoritativeBinding(ctx context.Context, identifier string, resourceType string, bindings []*AuthoritativeBinding) error {
	parsedIdentifier, err := ParseIdentifier(identifier, resourceType)
	if err != nil {
		return err
	}

	// Normalize bindings and check the validity of each binding. Each role may appear in at most
	// one binding. Then build desired permissions lookup
	desired := make(map[string]map[string]bool)
	for _, b := range bindings {
		if b == nil {
			return errors.New("authoritative binding must not be nil")
		}
		if err := b.Normalize(); err != nil {
			return fmt.Errorf("failed to normalize binding for role %q: %w", b.Role, err)
		}
		if _, ok := desired[b.Role]; ok {
			return fmt.Errorf("duplicate binding for role %q: each role may appear in only one binding", b.Role)
		}
		desired[b.Role] = make(map[string]bool)
		for _, priv := range b.Privileges {
			if !slices.Contains(authGrantPrivilegesByType[parsedIdentifier.ResourceType], priv) {
				return fmt.Errorf("privilege %q is not allowed for resource type %q", priv, parsedIdentifier.ResourceType)
			}
			// duplicate privileges are ignored since it doesn't affect the desired state.
			desired[b.Role][priv] = true
		}
	}

	// Checked after all the local validation above, before any other query.
	// Without this, a missing table/view reads back as "no grants": non-empty
	// bindings would then fail late (server-side batch validation), and empty
	// bindings would silently succeed with nothing to do.
	if err := c.checkResourceExists(ctx, *parsedIdentifier); err != nil {
		return err
	}

	// Get current permissions using role_permissions table
	currentPermsMap, err := c.getAllRolePermissionsPerId(ctx, *parsedIdentifier)
	if err != nil {
		return err
	}

	// Collect every statement first, then apply them as one ordered sequence,
	// which updateDatabaseDdl may split across several batches - so a failure in
	// a later batch leaves earlier ones applied (see the doc comment above).
	// Order matters: column-level revokes, then table-level revokes, then grants.
	var statements []string
	addStatement := func(render func(Grant) (string, error), grant Grant) error {
		stmt, err := render(grant)
		if err != nil {
			return err
		}
		statements = append(statements, stmt)
		return nil
	}

	// Revoke every column-level grant on the table - an authoritative binding
	// is table-level only, so any column-level grant is by definition not in
	// the desired state. Done before the table-level diff so a role moving
	// from column-level to table-level ends up with a clean table-level grant.
	if parsedIdentifier.ResourceType == "TABLE" {
		columnPerms, err := c.getColumnLevelOnlyPermissions(ctx, *parsedIdentifier, currentPermsMap)
		if err != nil {
			return err
		}
		for role, privs := range columnPerms {
			for priv, columns := range privs {
				if err := addStatement(deleteGrantStatement, Grant{
					RoleName:     role,
					Privilege:    priv,
					ResourceType: parsedIdentifier.ResourceType,
					Resource:     parsedIdentifier.Original,
					Columns:      columns,
				}); err != nil {
					return err
				}
			}
		}
	}

	// Revoke grants not present in desired
	for role, privs := range currentPermsMap {
		for _, priv := range privs {
			if !desired[role][strings.ToUpper(priv)] {
				if err := addStatement(deleteGrantStatement, Grant{
					RoleName:     role,
					Privilege:    priv,
					ResourceType: parsedIdentifier.ResourceType,
					Resource:     parsedIdentifier.Original,
				}); err != nil {
					return err
				}
			}
		}
	}

	// Build current permissions lookup
	current := make(map[string]map[string]bool)
	for role, privs := range currentPermsMap {
		current[role] = make(map[string]bool)
		for _, p := range privs {
			current[role][strings.ToUpper(p)] = true
		}
	}

	// Grant privileges that are missing
	for role, privs := range desired {
		for priv := range privs {
			if !current[role][priv] {
				if err := addStatement(createGrantStatement, Grant{
					RoleName:     role,
					Privilege:    priv,
					ResourceType: parsedIdentifier.ResourceType,
					Resource:     parsedIdentifier.Original,
				}); err != nil {
					return err
				}
			}
		}
	}

	if len(statements) == 0 {
		return nil
	}
	return c.updateDatabaseDdl(ctx, statements...)
}

// RevokeAllGrantsOnIdentifier revokes every table/view- and column-level grant
// held by every role on identifier. Used during resource destroy to leave no
// residual permissions behind. A table/view that no longer exists has no
// grants left to revoke, so ErrResourceNotFound is treated as success.
func (c *Client) RevokeAllGrantsOnIdentifier(ctx context.Context, identifier, resourceType string) error {
	err := c.ApplyAuthoritativeBinding(ctx, identifier, resourceType, nil)
	if errors.Is(err, ErrResourceNotFound) {
		return nil
	}
	return err
}

// GetAllRolePermissionsPerId returns every role's currently-granted table/view-level privileges
// on identifier, keyed by role name. Returns an error wrapping ErrResourceNotFound if identifier
// doesn't exist as resourceType - without that check a missing table/view would read back as
// "no grants", indistinguishable from one that exists with every grant revoked.
func (c *Client) GetAllRolePermissionsPerId(ctx context.Context, identifier, resourceType string) (map[string][]string, error) {
	parsedIdentifier, err := ParseIdentifier(identifier, resourceType)
	if err != nil {
		return nil, err
	}
	if err := c.checkResourceExists(ctx, *parsedIdentifier); err != nil {
		return nil, err
	}
	return c.getAllRolePermissionsPerId(ctx, *parsedIdentifier)
}

// getAllRolePermissionsPerId is GetAllRolePermissionsPerId for an already-parsed identifier,
// without the existence check - the source of truth ApplyAuthoritativeBinding diffs against to
// compute its revoke/grant set (it runs checkResourceExists itself). TABLE and VIEW are both read
// back from INFORMATION_SCHEMA.TABLE_PRIVILEGES.
func (c *Client) getAllRolePermissionsPerId(ctx context.Context, id ParsedIdentifier) (map[string][]string, error) {
	switch id.ResourceType {
	case "TABLE", "VIEW":
		return c.getRolePermissionsForTableOrView(ctx, id)
	default:
		return nil, fmt.Errorf("unsupported resource type: %q", id.ResourceType)
	}
}

// getRolePermissionsForTableOrView queries INFORMATION_SCHEMA.TABLE_PRIVILEGES for every
// (role, privilege) pair granted on id, grouping the rows by GRANTEE. Privilege names come back
// uppercase directly from INFORMATION_SCHEMA, matching the canonical form NormalizeGrant/
// ValidateGrant already expect elsewhere in this package.
func (c *Client) getRolePermissionsForTableOrView(ctx context.Context, id ParsedIdentifier) (map[string][]string, error) {
	stmt := spanner.Statement{
		SQL: `SELECT GRANTEE, PRIVILEGE_TYPE FROM INFORMATION_SCHEMA.TABLE_PRIVILEGES
				WHERE TABLE_SCHEMA = @table_schema AND TABLE_NAME = @table_name`,
		Params: map[string]any{
			"table_schema": id.Schema,
			"table_name":   id.ResourceName,
		},
	}
	iter := c.DataClient.Single().Query(ctx, stmt)
	defer iter.Stop()

	permissions := make(map[string][]string)
	for {
		row, err := iter.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("failed to query table/view privileges: %w", err)
		}
		var grantee, privilege string
		if err := row.Columns(&grantee, &privilege); err != nil {
			return nil, fmt.Errorf("failed to scan table/view privilege row: %w", err)
		}
		permissions[grantee] = append(permissions[grantee], privilege)
	}
	return permissions, nil
}

// GetColumnLevelOnlyPermissionsPerId returns every column-level-only grant on
// identifier, as role -> privilege -> columns: grants of a privilege on specific
// columns to a role that doesn't also hold that privilege on the whole table.
// ApplyAuthoritativeBinding revokes all of them, so a non-empty result means
// the table has drifted from any authoritative binding. Only tables have
// column-level grants, so a VIEW always returns an empty map. Returns an error
// wrapping ErrResourceNotFound if identifier doesn't exist as resourceType.
func (c *Client) GetColumnLevelOnlyPermissionsPerId(ctx context.Context, identifier, resourceType string) (map[string]map[string][]string, error) {
	parsedIdentifier, err := ParseIdentifier(identifier, resourceType)
	if err != nil {
		return nil, err
	}
	if err := c.checkResourceExists(ctx, *parsedIdentifier); err != nil {
		return nil, err
	}
	if parsedIdentifier.ResourceType != "TABLE" {
		return map[string]map[string][]string{}, nil
	}
	tableLevel, err := c.getAllRolePermissionsPerId(ctx, *parsedIdentifier)
	if err != nil {
		return nil, err
	}
	return c.getColumnLevelOnlyPermissions(ctx, *parsedIdentifier, tableLevel)
}

// getColumnLevelOnlyPermissions returns every column-level-only grant on id, as
// role -> privilege -> columns, read from INFORMATION_SCHEMA.COLUMN_PRIVILEGES.
// tableLevel is id's table-level grants (GetAllRolePermissionsPerId's result).
//
// COLUMN_PRIVILEGES can't be taken at face value: confirmed against real Spanner,
// a table-level grant is also listed there once per column, indistinguishable
// from an explicit column-level grant. So any (role, privilege) pair that also
// has a table-level grant is skipped: REVOKE of a column from a table-level
// holder is a silent no-op, and revoking the table-level grant also clears
// every column-level grant of the same privilege.
func (c *Client) getColumnLevelOnlyPermissions(ctx context.Context, id ParsedIdentifier, tableLevel map[string][]string) (map[string]map[string][]string, error) {
	stmt := spanner.Statement{
		SQL: `SELECT GRANTEE, PRIVILEGE_TYPE, COLUMN_NAME FROM INFORMATION_SCHEMA.COLUMN_PRIVILEGES
				WHERE TABLE_SCHEMA = @table_schema AND TABLE_NAME = @table_name`,
		Params: map[string]any{
			"table_schema": id.Schema,
			"table_name":   id.ResourceName,
		},
	}
	iter := c.DataClient.Single().Query(ctx, stmt)
	defer iter.Stop()

	permissions := make(map[string]map[string][]string)
	for {
		row, err := iter.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("failed to query column privileges: %w", err)
		}
		var grantee, privilege, column string
		if err := row.Columns(&grantee, &privilege, &column); err != nil {
			return nil, fmt.Errorf("failed to scan column privilege row: %w", err)
		}
		if slices.Contains(tableLevel[grantee], privilege) {
			continue
		}
		if permissions[grantee] == nil {
			permissions[grantee] = make(map[string][]string)
		}
		permissions[grantee][privilege] = append(permissions[grantee][privilege], column)
	}
	return permissions, nil
}

// checkResourceExists reports whether id names an existing object of id's
// ResourceType, dispatching per type. Returns an error wrapping
// ErrResourceNotFound if it doesn't exist, so callers can distinguish that
// from a lookup failure with errors.Is.
func (c *Client) checkResourceExists(ctx context.Context, id ParsedIdentifier) error {
	switch id.ResourceType {
	case "TABLE", "VIEW":
		return c.checkResourceTableOrViewExists(ctx, id)
	default:
		return fmt.Errorf("unsupported resource type: %q", id.ResourceType)
	}
}

// informationSchemaTableTypes maps a ParsedIdentifier.ResourceType to the
// INFORMATION_SCHEMA.TABLES.TABLE_TYPE value for that kind of object.
var informationSchemaTableTypes = map[string]string{
	"TABLE": "BASE TABLE",
	"VIEW":  "VIEW",
}

// checkResourceTableOrViewExists reports whether id names an existing object of id's
// ResourceType, via INFORMATION_SCHEMA.TABLES (which lists both tables and
// views). Returns an error wrapping ErrResourceNotFound if nothing by that
// name exists, or a type-mismatch error if it exists as the other kind (e.g.
// ResourceType TABLE for a view).
func (c *Client) checkResourceTableOrViewExists(ctx context.Context, id ParsedIdentifier) error {
	wantType, ok := informationSchemaTableTypes[id.ResourceType]
	if !ok {
		return fmt.Errorf("unsupported resource type: %q", id.ResourceType)
	}

	stmt := spanner.Statement{
		SQL: `SELECT TABLE_TYPE FROM INFORMATION_SCHEMA.TABLES
				WHERE TABLE_SCHEMA = @table_schema AND TABLE_NAME = @table_name`,
		Params: map[string]any{
			"table_schema": id.Schema,
			"table_name":   id.ResourceName,
		},
	}
	iter := c.DataClient.Single().Query(ctx, stmt)
	defer iter.Stop()

	row, err := iter.Next()
	if errors.Is(err, iterator.Done) {
		return fmt.Errorf("%s %q: %w", strings.ToLower(id.ResourceType), id.Original, ErrResourceNotFound)
	}
	if err != nil {
		return fmt.Errorf("failed to look up %q in INFORMATION_SCHEMA.TABLES: %w", id.Original, err)
	}
	var tableType string
	if err := row.Columns(&tableType); err != nil {
		return fmt.Errorf("failed to scan INFORMATION_SCHEMA.TABLES row: %w", err)
	}
	if tableType != wantType {
		return fmt.Errorf("%q is a %s, not a %s", id.Original, tableType, wantType)
	}
	return nil
}
