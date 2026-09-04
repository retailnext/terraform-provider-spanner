// Copyright RetailNext, Inc. 2026

package spanneracl

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestValidateIdentifier covers validateIdentifier once for every caller
// that shares it: role names (roles.go) and grant resource/schema/column
// names (grant.go) all follow the same unquoted-identifier rule.
func TestValidateIdentifier(t *testing.T) {
	assert.NoError(t, validateIdentifier("valid_name_123"))
	assert.Error(t, validateIdentifier(""))
	assert.Error(t, validateIdentifier("bad name"))
	assert.Error(t, validateIdentifier("bad-name"))
	assert.Error(t, validateIdentifier("bad`name"))
}
