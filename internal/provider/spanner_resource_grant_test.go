// Copyright RetailNext, Inc. 2026

package provider

import (
	"context"
	"regexp"
	"testing"

	fwresource "github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
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

// grantID must agree on the ID regardless of the case Privilege/ResourceType
// are written in, matching spanneracl.ValidateGrant's own case-insensitive
// treatment of those two fields (via spanneracl.NormalizeGrant) - otherwise
// a config that only changes their case would spuriously plan a replace.
func TestGrantIDCaseInsensitive(t *testing.T) {
	lower := spanneracl.Grant{
		RoleName:     "test_role",
		Privilege:    "select",
		ResourceType: "table",
		Resource:     "test_table",
	}
	upper := lower
	upper.Privilege = "SELECT"
	upper.ResourceType = "TABLE"

	assert.Equal(t, grantID(upper), grantID(lower))
	// grantID must not mutate the caller's Grant.
	assert.Equal(t, "select", lower.Privilege)
	assert.Equal(t, "table", lower.ResourceType)
}

// ImportState must store the "id" attribute as grantID's canonical form,
// not the raw import ID string - Read never recomputes it afterwards, so a
// hand-typed import ID with different Privilege/ResourceType casing or
// out-of-order columns would otherwise leave state permanently disagreeing
// with what Create/Update would have produced for the same grant. Calls
// ImportState directly (no resource.UnitTest/real client involved) per the
// same reasoning as TestGrantResourceModelToGrantValid - this logic never
// touches g.client.
func TestGrantResourceImportStateNormalizesID(t *testing.T) {
	ctx := context.Background()

	var schemaResp fwresource.SchemaResponse
	(&grantResource{}).Schema(ctx, fwresource.SchemaRequest{}, &schemaResp)

	state := tfsdk.State{
		Schema: schemaResp.Schema,
		Raw:    tftypes.NewValue(schemaResp.Schema.Type().TerraformType(ctx), nil),
	}
	importResp := &fwresource.ImportStateResponse{State: state}

	(&grantResource{}).ImportState(ctx, fwresource.ImportStateRequest{
		ID: "test_role|select|table|test_table|c,b,a",
	}, importResp)

	assert.False(t, importResp.Diagnostics.HasError(), "%v", importResp.Diagnostics)

	var got grantResourceModel
	assert.False(t, importResp.State.Get(ctx, &got).HasError())

	want := grantID(spanneracl.Grant{
		RoleName:     "test_role",
		Privilege:    "select",
		ResourceType: "table",
		Resource:     "test_table",
		Columns:      []string{"c", "b", "a"},
	})
	assert.Equal(t, want, got.ID.ValueString())
	// The canonical ID differs from the raw (lowercase, unsorted) import ID -
	// otherwise this test wouldn't actually exercise the normalization.
	assert.NotEqual(t, "test_role|select|table|test_table|c,b,a", got.ID.ValueString())
}

// ImportState must reject an invalid grant (via spanneracl.ValidateGrant)
// itself, rather than relying solely on the framework's automatic
// post-import Read to catch it - see the comment on ImportState's
// ValidateGrant call.
func TestGrantResourceImportStateInvalidGrant(t *testing.T) {
	ctx := context.Background()

	var schemaResp fwresource.SchemaResponse
	(&grantResource{}).Schema(ctx, fwresource.SchemaRequest{}, &schemaResp)

	state := tfsdk.State{
		Schema: schemaResp.Schema,
		Raw:    tftypes.NewValue(schemaResp.Schema.Type().TerraformType(ctx), nil),
	}
	importResp := &fwresource.ImportStateResponse{State: state}

	(&grantResource{}).ImportState(ctx, fwresource.ImportStateRequest{
		ID: "test_role|NOTAPRIV|table|test_table|",
	}, importResp)

	assert.True(t, importResp.Diagnostics.HasError())
	assert.Contains(t, importResp.Diagnostics.Errors()[0].Summary(), "Invalid Import ID")
}

// TestCaseInsensitiveRequiresReplace covers the plan modifier used on
// privilege/resource_type: a change that only differs in case must not
// require replace (the bug a PR review comment flagged - RequiresReplace
// was comparing raw strings even though spanneracl.ValidateGrant treats
// these fields case-insensitively), but any other change still must.
func TestCaseInsensitiveRequiresReplace(t *testing.T) {
	ctx := context.Background()
	// The modifier only inspects State.Raw/Plan.Raw for nullness (create vs.
	// destroy vs. update) - the concrete type/value carried doesn't matter.
	nonNullRaw := tftypes.NewValue(tftypes.Bool, true)
	nullRaw := tftypes.NewValue(tftypes.Bool, nil)
	modifier := caseInsensitiveRequiresReplace()

	tests := []struct {
		name        string
		stateRaw    tftypes.Value
		planRaw     tftypes.Value
		stateValue  types.String
		planValue   types.String
		wantReplace bool
	}{
		{
			name:        "case-only change does not require replace",
			stateRaw:    nonNullRaw,
			planRaw:     nonNullRaw,
			stateValue:  types.StringValue("select"),
			planValue:   types.StringValue("SELECT"),
			wantReplace: false,
		},
		{
			name:        "an actual value change still requires replace",
			stateRaw:    nonNullRaw,
			planRaw:     nonNullRaw,
			stateValue:  types.StringValue("select"),
			planValue:   types.StringValue("INSERT"),
			wantReplace: true,
		},
		{
			name:        "no change at all does not require replace",
			stateRaw:    nonNullRaw,
			planRaw:     nonNullRaw,
			stateValue:  types.StringValue("SELECT"),
			planValue:   types.StringValue("SELECT"),
			wantReplace: false,
		},
		{
			name:        "resource creation never requires replace",
			stateRaw:    nullRaw,
			planRaw:     nonNullRaw,
			stateValue:  types.StringNull(),
			planValue:   types.StringValue("SELECT"),
			wantReplace: false,
		},
		{
			name:        "resource destroy never requires replace",
			stateRaw:    nonNullRaw,
			planRaw:     nullRaw,
			stateValue:  types.StringValue("SELECT"),
			planValue:   types.StringNull(),
			wantReplace: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := planmodifier.StringRequest{
				State:      tfsdk.State{Raw: tt.stateRaw},
				Plan:       tfsdk.Plan{Raw: tt.planRaw},
				StateValue: tt.stateValue,
				PlanValue:  tt.planValue,
			}
			var resp planmodifier.StringResponse
			modifier.PlanModifyString(ctx, req, &resp)
			assert.Equal(t, tt.wantReplace, resp.RequiresReplace)
		})
	}
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
