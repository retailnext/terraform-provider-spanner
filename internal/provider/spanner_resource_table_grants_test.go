// Copyright RetailNext, Inc. 2026

package provider

import (
	"context"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	fwresource "github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// tableGrantsSchemaResponse returns spanner_table_grants' schema.
func tableGrantsSchemaResponse(t *testing.T) fwresource.SchemaResponse {
	t.Helper()
	var resp fwresource.SchemaResponse
	(&tableGrantsResource{}).Schema(context.Background(), fwresource.SchemaRequest{}, &resp)
	require.False(t, resp.Diagnostics.HasError(), resp.Diagnostics)
	return resp
}

// testGrant builds a grant block for role holding privileges.
func testGrant(role string, privileges ...string) grantModel {
	elems := make([]attr.Value, len(privileges))
	for i, p := range privileges {
		elems[i] = types.StringValue(p)
	}
	return grantModel{Role: types.StringValue(role), Privileges: types.SetValueMust(types.StringType, elems)}
}

// tableGrantsPlan encodes model as a plan for spanner_table_grants' schema.
func tableGrantsPlan(t *testing.T, model tableGrantsResourceModel) tfsdk.Plan {
	t.Helper()
	ctx := context.Background()
	s := tableGrantsSchemaResponse(t).Schema
	if model.ColumnLevelGrants.ElementType(ctx) == nil {
		model.ColumnLevelGrants = types.SetNull(types.StringType)
	}
	plan := tfsdk.Plan{Schema: s, Raw: tftypes.NewValue(s.Type().TerraformType(ctx), nil)}
	diags := plan.Set(ctx, &model)
	require.False(t, diags.HasError(), diags)
	return plan
}

// The model's struct tags must match the schema's attribute and block names,
// or every plan/state read fails at runtime.
func TestTableGrantsModelRoundTripsThroughSchema(t *testing.T) {
	want := tableGrantsResourceModel{
		ID:     types.StringValue("my_schema.orders"),
		Table:  types.StringValue("orders"),
		Schema: types.StringValue("my_schema"),
		Grants: Grants{testGrant("reader", "SELECT"), testGrant("writer", "INSERT", "UPDATE")},
		ColumnLevelGrants: types.SetValueMust(types.StringType, []attr.Value{
			types.StringValue("other:UPDATE(name)"),
		}),
	}
	plan := tableGrantsPlan(t, want)

	var got tableGrantsResourceModel
	diags := plan.Get(context.Background(), &got)
	require.False(t, diags.HasError(), diags)
	assert.Equal(t, want.ID, got.ID)
	assert.Equal(t, want.Table, got.Table)
	assert.Equal(t, want.Schema, got.Schema)
	assert.ElementsMatch(t, want.Grants, got.Grants)
	assert.Equal(t, want.ColumnLevelGrants, got.ColumnLevelGrants)
}

func TestTableGrantsExtractPlanData(t *testing.T) {
	plan := tableGrantsResourceModel{
		Table:  types.StringValue("orders"),
		Schema: types.StringValue("my_schema"),
		Grants: Grants{testGrant("reader", "SELECT"), testGrant("writer", "INSERT", "UPDATE")},
	}

	id, bindings, diags := (&tableGrantsResource{}).extractPlanData(context.Background(), &plan)
	require.False(t, diags.HasError(), diags)
	assert.Equal(t, "my_schema.orders", id)
	require.Len(t, bindings, 2)
	got := map[string][]string{}
	for _, b := range bindings {
		require.NotNil(t, b)
		got[b.Role] = b.Privileges
	}
	assert.ElementsMatch(t, []string{"SELECT"}, got["reader"])
	assert.ElementsMatch(t, []string{"INSERT", "UPDATE"}, got["writer"])
}

func TestTableGrantsExtractPlanDataDefaultSchema(t *testing.T) {
	plan := tableGrantsResourceModel{
		Table:  types.StringValue("orders"),
		Schema: types.StringNull(),
		Grants: Grants{testGrant("reader", "SELECT")},
	}

	id, _, diags := (&tableGrantsResource{}).extractPlanData(context.Background(), &plan)
	require.False(t, diags.HasError(), diags)
	assert.Equal(t, "orders", id)
}

func TestTableGrantsIdentifier(t *testing.T) {
	for _, tc := range []struct {
		name, table, schema, want, wantErr string
	}{
		{name: "default schema", table: "orders", want: "orders"},
		{name: "named schema", table: "orders", schema: "my_schema", want: "my_schema.orders"},
		{name: "schema prefix in table", table: "my_schema.orders", wantErr: "must not contain a schema prefix"},
		{name: "invalid table name", table: "bad-table", wantErr: "invalid resource identifier"},
		{name: "invalid schema name", table: "orders", schema: "bad schema", wantErr: "invalid schema identifier"},
		{name: "empty table", table: "", wantErr: "invalid resource identifier"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tableGrantsIdentifier(tc.table, tc.schema)
			if tc.wantErr != "" {
				assert.ErrorContains(t, err, tc.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

// A table in the default schema must import with a null schema, matching a
// configuration that omits it - an empty string would differ from null and
// force replacement on the first plan.
func TestTableGrantsFromImportID(t *testing.T) {
	tg, err := tableGrantsFromImportID("orders")
	require.NoError(t, err)
	assert.Equal(t, types.StringValue("orders"), tg.ID)
	assert.Equal(t, types.StringValue("orders"), tg.Table)
	assert.True(t, tg.Schema.IsNull())

	tg, err = tableGrantsFromImportID("my_schema.orders")
	require.NoError(t, err)
	assert.Equal(t, types.StringValue("my_schema.orders"), tg.ID)
	assert.Equal(t, types.StringValue("orders"), tg.Table)
	assert.Equal(t, types.StringValue("my_schema"), tg.Schema)

	_, err = tableGrantsFromImportID("a.b.c")
	assert.Error(t, err)
}

func TestGrantPermissionMapToGrants(t *testing.T) {
	grants, diags := grantPermissionMapToGrants(context.Background(), map[string][]string{
		"writer": {"INSERT", "UPDATE"},
		"reader": {"SELECT"},
	})
	require.False(t, diags.HasError(), diags)
	assert.Equal(t, Grants{testGrant("reader", "SELECT"), testGrant("writer", "INSERT", "UPDATE")}, grants)

	grants, diags = grantPermissionMapToGrants(context.Background(), map[string][]string{})
	require.False(t, diags.HasError(), diags)
	assert.Empty(t, grants)
}

func TestColumnLevelGrantsToSet(t *testing.T) {
	ctx := context.Background()
	got, diags := columnLevelGrantsToSet(ctx, map[string]map[string][]string{
		"writer": {"UPDATE": {"name", "id"}, "INSERT": {"name"}},
		"reader": {"SELECT": {"name"}},
	})
	require.False(t, diags.HasError(), diags)
	var elems []string
	require.False(t, got.ElementsAs(ctx, &elems, false).HasError())
	// Columns are sorted, so the same grant always formats the same way.
	assert.ElementsMatch(t, []string{"reader:SELECT(name)", "writer:INSERT(name)", "writer:UPDATE(id,name)"}, elems)

	// No column-level grants is an empty set, not null, so it matches the
	// always-empty plan.
	got, diags = columnLevelGrantsToSet(ctx, map[string]map[string][]string{})
	require.False(t, diags.HasError(), diags)
	assert.False(t, got.IsNull())
	assert.Empty(t, got.Elements())
}

func TestEmptySetPlanModifier(t *testing.T) {
	ctx := context.Background()
	s := tableGrantsSchemaResponse(t).Schema
	objType := s.Type().TerraformType(ctx)
	nonEmpty := types.SetValueMust(types.StringType, []attr.Value{types.StringValue("reader:SELECT(name)")})

	// Create/update: whatever the state holds, the plan is empty.
	resp := &planmodifier.SetResponse{PlanValue: types.SetUnknown(types.StringType)}
	emptySetPlanModifier{}.PlanModifySet(ctx, planmodifier.SetRequest{
		Plan:       tfsdk.Plan{Schema: s, Raw: tftypes.NewValue(objType, tftypes.UnknownValue)},
		StateValue: nonEmpty,
		PlanValue:  types.SetUnknown(types.StringType),
	}, resp)
	assert.Equal(t, types.SetValueMust(types.StringType, []attr.Value{}), resp.PlanValue)

	// Destroy: the plan is left alone.
	resp = &planmodifier.SetResponse{PlanValue: types.SetNull(types.StringType)}
	emptySetPlanModifier{}.PlanModifySet(ctx, planmodifier.SetRequest{
		Plan:       tfsdk.Plan{Schema: s, Raw: tftypes.NewValue(objType, nil)},
		StateValue: nonEmpty,
		PlanValue:  types.SetNull(types.StringType),
	}, resp)
	assert.True(t, resp.PlanValue.IsNull())
}

func TestUniqueGrantRolesValidator(t *testing.T) {
	ctx := context.Background()
	grantSetType, ok := tableGrantsSchemaResponse(t).Schema.Blocks["grant"].Type().(types.SetType)
	require.True(t, ok, "grant block should be a set")
	grantType := grantSetType.ElemType

	validate := func(value types.Set) bool {
		var resp validator.SetResponse
		uniqueGrantRolesValidator{}.ValidateSet(ctx, validator.SetRequest{Path: path.Root("grant"), ConfigValue: value}, &resp)
		return resp.Diagnostics.HasError()
	}
	setOf := func(grants ...grantModel) types.Set {
		value, diags := types.SetValueFrom(ctx, grantType, grants)
		require.False(t, diags.HasError(), diags)
		return value
	}

	assert.False(t, validate(setOf(testGrant("reader", "SELECT"), testGrant("writer", "INSERT"))))
	assert.True(t, validate(setOf(testGrant("reader", "SELECT"), testGrant("reader", "INSERT"))))
	assert.False(t, validate(types.SetUnknown(grantType)))
	assert.False(t, validate(types.SetNull(grantType)))
}

func TestTableGrantsValidateConfig(t *testing.T) {
	validate := func(model tableGrantsResourceModel) *fwresource.ValidateConfigResponse {
		plan := tableGrantsPlan(t, model)
		resp := &fwresource.ValidateConfigResponse{}
		(&tableGrantsResource{}).ValidateConfig(context.Background(),
			fwresource.ValidateConfigRequest{Config: tfsdk.Config(plan)}, resp)
		return resp
	}
	grants := Grants{testGrant("reader", "SELECT")}

	resp := validate(tableGrantsResourceModel{
		ID: types.StringUnknown(), Table: types.StringValue("orders"), Schema: types.StringNull(), Grants: grants,
	})
	assert.False(t, resp.Diagnostics.HasError(), resp.Diagnostics)

	resp = validate(tableGrantsResourceModel{
		ID: types.StringUnknown(), Table: types.StringValue("my_schema.orders"), Schema: types.StringNull(), Grants: grants,
	})
	assert.True(t, resp.Diagnostics.HasError())

	// An unknown table name (e.g. another resource's output) can't be checked yet.
	resp = validate(tableGrantsResourceModel{
		ID: types.StringUnknown(), Table: types.StringUnknown(), Schema: types.StringNull(), Grants: grants,
	})
	assert.False(t, resp.Diagnostics.HasError(), resp.Diagnostics)
}

// tableGrantsTestProviderConfig is a provider block for plan-time validation
// tests; it's never used to reach a real database.
const tableGrantsTestProviderConfig = `
provider "spanner" {
  project  = "test-project"
  instance = "test-instance"
  database = "test-db"
}
`

// Plan-time validation errors surface through Terraform itself. These steps
// only assert errors, so they don't need working credentials.
func TestAccTableGrantsResourceValidateConfigDuplicateRole(t *testing.T) {
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: tableGrantsTestProviderConfig + `
resource "spanner_table_grants" "test" {
  table = "orders"
  grant {
    role       = "reader"
    privileges = ["SELECT"]
  }
  grant {
    role       = "reader"
    privileges = ["INSERT"]
  }
}
`,
			ExpectError: regexp.MustCompile(`Duplicate Grant Role`),
		}},
	})
}

func TestAccTableGrantsResourceValidateConfigInvalidTable(t *testing.T) {
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: tableGrantsTestProviderConfig + `
resource "spanner_table_grants" "test" {
  table = "my_schema.orders"
  grant {
    role       = "reader"
    privileges = ["SELECT"]
  }
}
`,
			ExpectError: regexp.MustCompile(`Invalid Table Identifier`),
		}},
	})
}
