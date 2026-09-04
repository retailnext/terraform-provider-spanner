// Copyright RetailNext, Inc. 2026

package spanneracl

import (
	"errors"
	"fmt"
	"unicode"
)

// ErrRoleNotFound is returned when a database role does not exist.
var ErrRoleNotFound = errors.New("role not found")

// Role represents a Spanner database role used for fine-grained access
// control (FGAC). Unlike ScyllaDB roles, Spanner database roles carry no
// attributes of their own (no login/superuser flags): they are purely
// named containers that privileges and other roles get granted to.
type Role struct {
	Name string
}

// Grant represents a single privilege grant to a Spanner database role.
//
// DDL: https://docs.cloud.google.com/spanner/docs/reference/standard-sql/data-definition-language#grant_and_revoke_statements
// Note:
// 1. Each Grant maps to exactly one privilege on exactly one resource, matching
// scylladb.Grant's one-call-per-grant shape rather than Spanner's native
// comma-separated multi-privilege/multi-target GRANT syntax.
// 2. Granting role to another role is not considered in this provider.
// 3. ResourceType is narrower than Spanner's full grant vocabulary - see
// validResourceTypes in grant.go for why SEQUENCE, SCHEMA, and TABLE
// FUNCTION are deliberately not supported yet (no confirmed
// INFORMATION_SCHEMA privilege view to read a grant back with).
type Grant struct {
	RoleName     string   // grantee role name
	Privilege    string   // SELECT, INSERT, UPDATE, DELETE, EXECUTE, USAGE
	ResourceType string   // TABLE, VIEW, or CHANGE STREAM - see validResourceTypes in grant.go
	Resource     string   // resource name, optionally schema-qualified as "schema.name"
	Columns      []string // optional: column-level SELECT/INSERT/UPDATE on TABLE only
}

// validateIdentifier reports whether name is a well-formed unquoted Spanner
// identifier: non-empty, and only letters, digits, and underscores. Shared
// by every unquoted identifier this package validates before interpolating
// it into DDL - role names (roles.go) and grant resource/schema/column
// names (grant.go alike) all follow the same rule, so there is exactly one
// definition of it. Callers should wrap the returned error with context
// (e.g. "invalid role name %q: %w") since this doesn't know what kind of
// identifier it was given.
func validateIdentifier(name string) error {
	if name == "" {
		return errors.New("identifier must not be empty")
	}
	for _, r := range name {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' {
			return fmt.Errorf("invalid character in identifier: %c", r)
		}
	}
	return nil
}
