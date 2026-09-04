// Copyright RetailNext, Inc. 2026

package spanneracl

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"slices"
	"strings"
	"text/template"

	"cloud.google.com/go/spanner"
	"google.golang.org/api/iterator"
)

const (
	createGrantTemplate = "GRANT {{.Privilege}}{{if .Columns}}({{.QuotedColumns}}){{end}} " +
		"ON {{.ResourceType}} {{.QuotedResource}} TO ROLE {{.QuotedRoleName}}"
	deleteGrantTemplate = "REVOKE {{.Privilege}}{{if .Columns}}({{.QuotedColumns}}){{end}} " +
		"ON {{.ResourceType}} {{.QuotedResource}} FROM ROLE {{.QuotedRoleName}}"
)

var (
	templateCreateGrant, _ = template.New("createGrant").Parse(createGrantTemplate)
	templateDeleteGrant, _ = template.New("deleteGrant").Parse(deleteGrantTemplate)
	tableGrantPrivileges   = []string{"SELECT", "INSERT", "UPDATE", "DELETE"}

	// validPrivileges and validResourceTypes are Spanner's full documented
	// privilege/object vocabulary (see the Grant doc comment in domain.go).
	// Every Grant is checked against these regardless of ResourceType,
	// since Privilege and ResourceType are both interpolated directly into
	// the GRANT/REVOKE DDL - unlike RoleName/Resource/Columns, neither goes
	// through quoteQualifiedIdentifier/validateIdentifier, so an
	// unvalidated value here would reach UpdateDatabaseDdl unguarded.
	validPrivileges    = []string{"SELECT", "INSERT", "UPDATE", "DELETE", "EXECUTE", "USAGE"}
	validResourceTypes = []string{"TABLE", "VIEW", "CHANGE STREAM", "TABLE FUNCTION", "SEQUENCE", "SCHEMA"}
)

// grantTemplateData adapts a Grant for rendering into DDL: it pre-quotes
// every identifier so the templates above never interpolate a raw,
// unvalidated string.
type grantTemplateData struct {
	Grant
	QuotedRoleName string
	QuotedResource string
	QuotedColumns  string
}

// CreateGrant grants a single privilege on a single resource to a role.
func (c *Client) CreateGrant(ctx context.Context, grant Grant) error {
	data, err := newGrantTemplateData(grant)
	if err != nil {
		return err
	}

	var buf bytes.Buffer
	if err := templateCreateGrant.Execute(&buf, data); err != nil {
		return fmt.Errorf("failed to render create grant statement: %w", err)
	}

	return c.updateDatabaseDdl(ctx, buf.String())
}

// DeleteGrant revokes a single privilege on a single resource from a role.
//
// Note that REVOKE statements in Spanner do not error if the grant does not exist.
func (c *Client) DeleteGrant(ctx context.Context, grant Grant) error {
	data, err := newGrantTemplateData(grant)
	if err != nil {
		return err
	}

	var buf bytes.Buffer
	if err := templateDeleteGrant.Execute(&buf, data); err != nil {
		return fmt.Errorf("failed to render delete grant statement: %w", err)
	}

	return c.updateDatabaseDdl(ctx, buf.String())
}

// GrantExists reports whether the given grant currently exists, read back via
// INFORMATION_SCHEMA.TABLE_PRIVILEGES (tables/views) or
// INFORMATION_SCHEMA.COLUMN_PRIVILEGES (column-level grants).
func (c *Client) GrantExists(ctx context.Context, grant Grant) (bool, error) {
	grant = normalizeGrant(grant)
	if len(grant.Columns) > 0 {
		return c.getGrantColumn(ctx, grant)
	}
	return c.getGrantTable(ctx, grant)
}

// getGrantTable reports whether grant.Privilege is granted on grant.Resource
// as a whole (table/view level, no column list), via
// INFORMATION_SCHEMA.TABLE_PRIVILEGES. grant.Privilege must already be
// normalized (see normalizeGrant) - INFORMATION_SCHEMA.TABLE_PRIVILEGES
// stores privilege names uppercase.
func (c *Client) getGrantTable(ctx context.Context, grant Grant) (bool, error) {
	schema, resource := splitSchemaQualified(grant.Resource)
	stmt := spanner.Statement{
		SQL: `SELECT 1 FROM INFORMATION_SCHEMA.TABLE_PRIVILEGES
				WHERE GRANTEE = @role_name AND TABLE_SCHEMA = @table_schema
				AND TABLE_NAME = @table_name AND PRIVILEGE_TYPE = @privilege LIMIT 1`,
		Params: map[string]any{
			"role_name":    grant.RoleName,
			"table_schema": schema,
			"table_name":   resource,
			"privilege":    grant.Privilege,
		},
	}
	iter := c.DataClient.Single().Query(ctx, stmt)
	defer iter.Stop()

	_, err := iter.Next()
	if errors.Is(err, iterator.Done) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("failed to query grant: %w", err)
	}
	return true, nil
}

// getGrantColumn reports whether every column in grant.Columns has
// grant.Privilege granted on it, via INFORMATION_SCHEMA.COLUMN_PRIVILEGES.
// The grantee may hold privileges on more columns than grant.Columns asks
// for (e.g. from a broader grant applied out-of-band) - that's still a
// match, since grant.Columns only needs to be a subset of what's actually
// granted. If any column in grant.Columns is missing, this logs exactly
// which ones before reporting false. grant.Privilege must already be
// normalized (see normalizeGrant) - INFORMATION_SCHEMA.COLUMN_PRIVILEGES
// stores privilege names uppercase.
func (c *Client) getGrantColumn(ctx context.Context, grant Grant) (bool, error) {
	schema, resource := splitSchemaQualified(grant.Resource)
	stmt := spanner.Statement{
		SQL: `SELECT COLUMN_NAME FROM INFORMATION_SCHEMA.COLUMN_PRIVILEGES
				WHERE GRANTEE = @role_name AND TABLE_SCHEMA = @table_schema
				AND TABLE_NAME = @table_name AND PRIVILEGE_TYPE = @privilege`,
		Params: map[string]any{
			"role_name":    grant.RoleName,
			"table_schema": schema,
			"table_name":   resource,
			"privilege":    grant.Privilege,
		},
	}
	iter := c.DataClient.Single().Query(ctx, stmt)
	defer iter.Stop()

	var grantedColumns []string
	for {
		row, err := iter.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		if err != nil {
			return false, fmt.Errorf("failed to query column grant: %w", err)
		}
		var columnName string
		if err := row.Columns(&columnName); err != nil {
			return false, fmt.Errorf("failed to scan column grant: %w", err)
		}
		grantedColumns = append(grantedColumns, columnName)
	}

	var missing []string
	for _, column := range grant.Columns {
		if !slices.Contains(grantedColumns, column) {
			missing = append(missing, column)
		}
	}
	if len(missing) > 0 {
		log.Printf("grant columns missing from INFORMATION_SCHEMA.COLUMN_PRIVILEGES "+
			"(role %q, table %q, privilege %q): %v", grant.RoleName, grant.Resource, grant.Privilege, missing)
		return false, nil
	}
	return true, nil
}

// normalizeGrant returns a copy of grant with Privilege and ResourceType
// uppercased, so CreateGrant/DeleteGrant/GrantExists all accept either case
// and validate/compare consistently - Spanner's DDL keywords and its
// INFORMATION_SCHEMA privilege/resource-type values are both uppercase
// regardless of the case a caller writes GRANT/REVOKE statements in.
func normalizeGrant(grant Grant) Grant {
	grant.Privilege = strings.ToUpper(grant.Privilege)
	grant.ResourceType = strings.ToUpper(grant.ResourceType)
	return grant
}

// newGrantTemplateData normalizes and validates grant (via normalizeGrant/
// ValidateGrant), validates its Resource/Columns identifiers, and returns a
// grantTemplateData with everything pre-quoted for the DDL templates above.
func newGrantTemplateData(grant Grant) (grantTemplateData, error) {
	grant = normalizeGrant(grant)

	if err := ValidateGrant(grant); err != nil {
		return grantTemplateData{}, err
	}

	quotedResource, err := quoteQualifiedIdentifier(grant.Resource)
	if err != nil {
		return grantTemplateData{}, err
	}

	quotedColumns := make([]string, 0, len(grant.Columns))
	for _, column := range grant.Columns {
		if err := validateIdentifier(column); err != nil {
			return grantTemplateData{}, fmt.Errorf("invalid column name %q: %w", column, err)
		}
		quotedColumns = append(quotedColumns, fmt.Sprintf("`%s`", column))
	}

	return grantTemplateData{
		Grant:          grant,
		QuotedRoleName: fmt.Sprintf("`%s`", grant.RoleName),
		QuotedResource: quotedResource,
		QuotedColumns:  strings.Join(quotedColumns, ", "),
	}, nil
}

// quoteQualifiedIdentifier validates and backtick-quotes a resource name
// that may be schema-qualified (e.g. "my_schema.my_table"), producing
// Spanner's "`my_schema`.`my_table`" form - each segment must be quoted
// separately, since backtick-quoting the whole dotted string as one
// identifier is not valid Spanner DDL syntax.
func quoteQualifiedIdentifier(name string) (string, error) {
	schema, resource := splitSchemaQualified(name)

	if err := validateIdentifier(resource); err != nil {
		return "", fmt.Errorf("invalid resource name %q: %w", name, err)
	}
	if schema == "" {
		return fmt.Sprintf("`%s`", resource), nil
	}
	if err := validateIdentifier(schema); err != nil {
		return "", fmt.Errorf("invalid resource name %q: %w", name, err)
	}
	return fmt.Sprintf("`%s`.`%s`", schema, resource), nil
}

// splitSchemaQualified splits "schema.name" into ("schema", "name"), or
// ("", name) if name is not schema-qualified.
func splitSchemaQualified(name string) (schema, resource string) {
	if idx := strings.LastIndex(name, "."); idx >= 0 {
		return name[:idx], name[idx+1:]
	}
	return "", name
}

// ValidateGrant validates a Grant's field values against Spanner's grant
// vocabulary and rules, independent of rendering it into DDL: that
// RoleName is a well-formed role identifier, that Privilege and
// ResourceType are both part of Spanner's documented FGAC vocabulary (see
// the Grant doc comment in domain.go), and the TABLE-specific rules -
// column-level grants only apply to ResourceType TABLE, DELETE is never
// column-level, and TABLE grants only use a privilege TABLE actually
// supports. Normalizes grant (see normalizeGrant) internally first, so
// callers can pass Privilege/ResourceType in whatever case they were
// written in - normalization only affects this function's own checks, not
// the grant a caller holds, since Grant is passed by value.
//
// This does not validate Resource or Columns - those are checked
// separately by quoteQualifiedIdentifier/validateIdentifier in
// newGrantTemplateData, since CreateGrant/DeleteGrant need them backtick-
// quoted anyway and there was no reason to validate them twice.
func ValidateGrant(grant Grant) error {
	grant = normalizeGrant(grant)

	if err := validateIdentifier(grant.RoleName); err != nil {
		return fmt.Errorf("invalid role name %q: %w", grant.RoleName, err)
	}
	if !slices.Contains(validPrivileges, grant.Privilege) {
		return fmt.Errorf("invalid grant privilege: %s", grant.Privilege)
	}
	if !slices.Contains(validResourceTypes, grant.ResourceType) {
		return fmt.Errorf("invalid grant resource type: %q", grant.ResourceType)
	}
	if grant.ResourceType != "TABLE" {
		if len(grant.Columns) > 0 {
			return fmt.Errorf("column-level grants are only allowed for TABLE resources")
		} else {
			return nil
		}
	}
	if len(grant.Columns) > 0 && grant.Privilege == "DELETE" {
		return fmt.Errorf("DELETE privilege cannot be column-level; table-level grant must not specify columns")
	}
	if !slices.Contains(tableGrantPrivileges, grant.Privilege) {
		return fmt.Errorf("invalid privilege for table grant: %s", grant.Privilege)
	}
	return nil
}
