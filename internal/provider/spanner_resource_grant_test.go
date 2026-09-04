// Copyright RetailNext, Inc. 2026

package provider

import (
	"context"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/retailnext/terraform-provider-spanner/spanneracl"
	"github.com/stretchr/testify/assert"
)

func TestGrantIDColumnOrderIndependent(t *testing.T) {
	base := spanneracl.Grant{
		RoleName:     "test_role",
		Privilege:    "SELECT",
		ResourceType: "TABLE",
		Resource:     "test_table",
	}

	forward := base
	forward.Columns = []string{"a", "b", "c"}
	reversed := base
	reversed.Columns = []string{"c", "b", "a"}

	assert.Equal(t, grantID(forward), grantID(reversed))
	// Sorting for ID purposes must not mutate the caller's slice.
	assert.Equal(t, []string{"c", "b", "a"}, reversed.Columns)
}

// These are plan-only unit tests: ValidateConfig runs spanneracl.ValidateGrant
// purely against the config values, with no Spanner client call involved, so
// they catch bad grant configuration before Create ever runs - no
// Docker/emulator needed, same as provider_test.go's Configure-validation
// tests.

func TestAccGrantResourceValidateConfigInvalidPrivilege(t *testing.T) {
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: `
provider "spanner" {
  project  = "test-project"
  instance = "test-instance"
  database = "test-db"
}
resource "spanner_grant" "example" {
  role_name     = "test_role"
  privilege     = "NOTAPRIV"
  resource_type = "TABLE"
  resource      = "test_table"
}
`,
				ExpectError: regexp.MustCompile(`Invalid Grant Configuration`),
			},
		},
	})
}

func TestAccGrantResourceValidateConfigInvalidResourceType(t *testing.T) {
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: `
provider "spanner" {
  project  = "test-project"
  instance = "test-instance"
  database = "test-db"
}
resource "spanner_grant" "example" {
  role_name     = "test_role"
  privilege     = "SELECT"
  resource_type = "NOT_A_RESOURCE_TYPE"
  resource      = "test_table"
}
`,
				ExpectError: regexp.MustCompile(`Invalid Grant Configuration`),
			},
		},
	})
}

func TestAccGrantResourceValidateConfigColumnsRequireTable(t *testing.T) {
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: `
provider "spanner" {
  project  = "test-project"
  instance = "test-instance"
  database = "test-db"
}
resource "spanner_grant" "example" {
  role_name     = "test_role"
  privilege     = "SELECT"
  resource_type = "VIEW"
  resource      = "test_view"
  columns       = ["a_column"]
}
`,
				ExpectError: regexp.MustCompile(`Invalid Grant Configuration`),
			},
		},
	})
}

// TestGrantResourceModelToGrantValid checks that a well-formed grant model
// passes validation - the counterpart to the three ExpectError tests above.
// This deliberately does NOT go through resource.UnitTest/Terraform CLI:
// doing so would require provider Configure() to fully succeed (it needs a
// real spanneracl.Client, which needs real or Application Default
// Credentials), for an assertion that has nothing to do with credentials at
// all. toGrant/ValidateConfig never touch g.client, so calling toGrant
// directly tests the exact same logic with zero credential/network
// dependency - safe to run on any machine, including CI with no ADC
// configured.
func TestGrantResourceModelToGrantValid(t *testing.T) {
	model := grantResourceModel{
		RoleName:     types.StringValue("test_role"),
		Privilege:    types.StringValue("select"),
		ResourceType: types.StringValue("table"),
		Resource:     types.StringValue("test_table"),
		Columns:      types.SetNull(types.StringType),
	}

	_, diags := model.toGrant(context.Background())
	assert.False(t, diags.HasError(), "expected a well-formed grant to validate cleanly: %v", diags)
}
