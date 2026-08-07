// Copyright RetailNext, Inc. 2026

package spanneracl

import "errors"

// ErrRoleNotFound is returned when a database role does not exist.
var ErrRoleNotFound = errors.New("role not found")

// Role represents a Spanner database role used for fine-grained access
// control (FGAC). Unlike ScyllaDB roles, Spanner database roles carry no
// attributes of their own (no login/superuser flags): they are purely
// named containers that privileges and other roles get granted to.
type Role struct {
	Name string
}
