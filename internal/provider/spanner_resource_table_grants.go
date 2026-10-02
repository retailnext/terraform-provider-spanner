// Copyright RetailNext, Inc. 2026

package provider

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/setvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/retailnext/terraform-provider-spanner/spanneracl"
)

var _ resource.Resource = &tableGrantsResource{}
var _ resource.ResourceWithImportState = &tableGrantsResource{}
var _ resource.ResourceWithConfigure = &tableGrantsResource{}
var _ resource.ResourceWithValidateConfig = &tableGrantsResource{}

// NewTableGrantsResource returns a new spanner_table_grants resource, for
// registration in the provider's Resources list.
func NewTableGrantsResource() resource.Resource {
	return &tableGrantsResource{}
}

// tableGrantsResource implements spanner_table_grants: it authoritatively
// manages every role's privileges on one table, via
// spanneracl.ApplyAuthoritativeBinding. Any grant on the table that isn't in
// configuration - including column-level grants and grants created outside
// Terraform - is revoked on apply, and destroying the resource revokes every
// grant on the table.
type tableGrantsResource struct {
	client *spanneracl.Client
}

// tableGrantsResourceModel is the Terraform state/plan shape of
// spanner_table_grants.
type tableGrantsResourceModel struct {
	// ID is the table identifier passed to spanneracl: "table", or
	// "schema.table" when Schema is set.
	ID types.String `tfsdk:"id"`
	// Table is the table name, without any schema prefix.
	Table types.String `tfsdk:"table"`
	// Schema is the named schema containing Table; null for the default schema.
	Schema types.String `tfsdk:"schema"`
	// Grants is the complete set of role -> privileges bindings on the table.
	Grants Grants `tfsdk:"grant"`
	// ColumnLevelGrants lists the column-level grants found on the table, each
	// formatted as "role:PRIVILEGE(col1,col2)". Always planned as empty, since
	// apply revokes them all; Read fills it, so one made outside Terraform shows
	// up as drift.
	ColumnLevelGrants types.Set `tfsdk:"column_level_grants"`
}

// Metadata sets the resource type name to "<provider>_table_grants".
func (r *tableGrantsResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_table_grants"
}

// Schema defines spanner_table_grants: the target table (and optional schema),
// both forcing replacement on change, plus one or more grant blocks, each
// binding one role to its complete set of table-level privileges. Each role
// may appear in at most one grant block.
func (r *tableGrantsResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Authoritatively manages every privilege grant on one Spanner table, for " +
			"fine-grained access control. The `grant` blocks are the complete set of table-level grants on the " +
			"table: on every apply, any listed grant that's missing is granted, and any grant not listed is " +
			"revoked - including grants made outside Terraform, grants managed by `spanner_grant` resources on " +
			"the same table, and all column-level grants on the table. Destroying this resource revokes every " +
			"grant on the table, from every role.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "The table identifier: `table` for a table in the default schema, or " +
					"`schema.table` for a table in a named schema. Also the import ID.",
				Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"table": schema.StringAttribute{
				MarkdownDescription: "The name of the table whose grants this resource manages, without any " +
					"schema prefix (use `schema` for a table in a named schema). Letters, digits and underscores " +
					"only. The table must already exist. Changing this forces a new resource: grants on the old " +
					"table are all revoked and the new table's grants are set.",
				Required: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"schema": schema.StringAttribute{
				MarkdownDescription: "The named schema containing `table`. Omit it for a table in the default " +
					"schema. Letters, digits and underscores only. Changing this forces a new resource.",
				Optional: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"column_level_grants": schema.SetAttribute{
				MarkdownDescription: "Column-level grants currently on the table, each as " +
					"`role:PRIVILEGE(column1,column2)`. This resource manages table-level privileges only and " +
					"revokes every column-level grant on the table, so this is always planned as empty: a " +
					"column-level grant made outside Terraform appears here on refresh, shows up in the plan as " +
					"a change, and is revoked by the next apply.",
				ElementType: types.StringType,
				Computed:    true,
				PlanModifiers: []planmodifier.Set{
					emptySetPlanModifier{},
				},
			},
		},
		Blocks: map[string]schema.Block{
			"grant": schema.SetNestedBlock{
				MarkdownDescription: "One role and the complete set of table-level privileges it holds on the " +
					"table. At least one `grant` block is required, and each role may appear in only one block. A " +
					"role not listed in any block ends up with no privileges on the table.",
				NestedObject: schema.NestedBlockObject{
					Attributes: map[string]schema.Attribute{
						"role": schema.StringAttribute{
							MarkdownDescription: "The database role that receives `privileges`, e.g. " +
								"`spanner_role.example.name`. The role must already exist.",
							Required: true,
						},
						"privileges": schema.SetAttribute{
							MarkdownDescription: "The table-level privileges this role holds on the table: any of " +
								"`SELECT`, `INSERT`, `UPDATE` and `DELETE`, in uppercase. Privileges the role currently " +
								"holds on the table but that aren't listed here are revoked.",
							ElementType: types.StringType,
							Required:    true,
							Validators: []validator.Set{
								setvalidator.ValueStringsAre(
									stringvalidator.OneOf("SELECT", "INSERT", "UPDATE", "DELETE"),
								),
							},
						},
					},
				},
				Validators: []validator.Set{
					setvalidator.SizeAtLeast(1),
					uniqueGrantRolesValidator{},
				},
			},
		},
	}
}

// Configure stores the *spanneracl.Client the provider built in its own
// Configure. ProviderData is nil during early validation, before the provider
// is configured, so that case is a no-op.
func (r *tableGrantsResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}

	client, ok := req.ProviderData.(*spanneracl.Client)
	if !ok {
		resp.Diagnostics.AddError(
			"Unexpected Resource Configure Type",
			fmt.Sprintf("Expected *spanneracl.Client, got: %T. Please report this issue to the provider developers.", req.ProviderData),
		)
		return
	}
	r.client = client
}

// Create applies the planned grants authoritatively - revoking anything
// already on the table that isn't planned - and stores the plan as state.
func (r *tableGrantsResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan tableGrantsResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(r.applyPlanData(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// applyPlanData converts plan into an identifier and bindings, reconciles the
// table's grants to exactly those bindings, and sets plan.ID to the
// identifier. Shared by Create and Update.
func (r *tableGrantsResource) applyPlanData(ctx context.Context, plan *tableGrantsResourceModel) (diags diag.Diagnostics) {
	id, bindings, extractDiags := r.extractPlanData(ctx, plan)
	diags.Append(extractDiags...)
	if diags.HasError() {
		return diags
	}

	if err := r.client.ApplyAuthoritativeBinding(ctx, id, "TABLE", bindings); err != nil {
		diags.AddError("Error Applying Table Grants", err.Error())
		return
	}

	plan.ID = types.StringValue(id)
	// The apply revoked every column-level grant on the table.
	plan.ColumnLevelGrants = types.SetValueMust(types.StringType, []attr.Value{})
	return diags
}

// extractPlanData builds the spanneracl table identifier ("table" or
// "schema.table") and one AuthoritativeBinding per grant block from plan.
func (r *tableGrantsResource) extractPlanData(ctx context.Context, plan *tableGrantsResourceModel) (
	tableResourceID string,
	bindings []*spanneracl.AuthoritativeBinding,
	diags diag.Diagnostics,
) {
	tableResourceID, err := tableGrantsIdentifier(plan.Table.ValueString(), plan.Schema.ValueString())
	if err != nil {
		diags.AddAttributeError(path.Root("table"), "Invalid Table Identifier", err.Error())
		return
	}

	bindings = make([]*spanneracl.AuthoritativeBinding, 0, len(plan.Grants))
	for _, g := range plan.Grants {
		var privs []string
		diags.Append(g.Privileges.ElementsAs(ctx, &privs, false)...)
		if diags.HasError() {
			return
		}
		bindings = append(bindings, &spanneracl.AuthoritativeBinding{
			Role:       g.Role.ValueString(),
			Privileges: privs,
		})
	}

	return
}

// Read refreshes the grant blocks from the table's current table-level
// privileges, so grants added or revoked outside Terraform show up as drift.
func (r *tableGrantsResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state tableGrantsResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if state.ID.IsNull() || state.ID.IsUnknown() {
		resp.Diagnostics.AddError(
			"Invalid Table ID",
			"Table ID is null or unknown",
		)
		return
	}
	rolePermissions, err := r.client.GetAllRolePermissionsPerId(ctx, state.ID.ValueString(), "TABLE")
	if errors.Is(err, spanneracl.ErrResourceNotFound) {
		// The table was dropped outside Terraform, taking its grants with it.
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Error Reading Table Grants", err.Error())
		return
	}
	grants, diags := grantPermissionMapToGrants(ctx, rolePermissions)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	columnPermissions, err := r.client.GetColumnLevelOnlyPermissionsPerId(ctx, state.ID.ValueString(), "TABLE")
	if err != nil {
		resp.Diagnostics.AddError("Error Reading Table Column-Level Grants", err.Error())
		return
	}
	columnLevelGrants, diags := columnLevelGrantsToSet(ctx, columnPermissions)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	state.Grants = grants
	state.ColumnLevelGrants = columnLevelGrants
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update reconciles the table's grants to the new plan. Only the grant blocks
// can change in place: table and schema force replacement.
func (r *tableGrantsResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan tableGrantsResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	diags := r.applyPlanData(ctx, &plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete revokes every grant on the table, from every role - not only the
// ones this resource configured. A table that no longer exists is treated as
// already clean.
func (r *tableGrantsResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state tableGrantsResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id := state.ID.ValueString()
	if err := r.client.RevokeAllGrantsOnIdentifier(ctx, id, "TABLE"); err != nil {
		resp.Diagnostics.AddError("Error Deleting Table Grants", err.Error())
		return
	}
}

// ImportState imports the grants on an existing table. The import ID is the
// table identifier: "table" or "schema.table". The table must already have at
// least one grant, since the resource requires at least one grant block.
func (r *tableGrantsResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	id := req.ID
	tableGrantModel, err := tableGrantsFromImportID(id)
	if err != nil {
		resp.Diagnostics.AddError("Invalid Import ID", err.Error())
		return
	}
	permissionMap, err := r.client.GetAllRolePermissionsPerId(ctx, id, "TABLE")
	if err != nil {
		resp.Diagnostics.AddError("Error Importing Table Grants", err.Error())
		return
	}
	if len(permissionMap) == 0 {
		resp.Diagnostics.AddError("No Grants Found to Import", fmt.Sprintf("No grants were found on %q. Only identifiers with existing grants can be imported.", id))
		return
	}
	grants, diags := grantPermissionMapToGrants(ctx, permissionMap)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	tableGrantModel.Grants = grants
	state := tableGrantModel
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// ValidateConfig checks the table and schema names at plan time, so an invalid
// identifier is reported before anything is applied. Unknown values (e.g. from
// another resource's not-yet-created output) are skipped.
func (r *tableGrantsResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var config tableGrantsResourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if config.Table.IsUnknown() || config.Schema.IsUnknown() {
		return
	}
	if _, err := tableGrantsIdentifier(config.Table.ValueString(), config.Schema.ValueString()); err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("table"), "Invalid Table Identifier", err.Error())
	}
}

// tableGrantsIdentifier validates table and schema and returns the spanneracl
// identifier for them: "table", or "schema.table" when schema is non-empty.
// table must be a bare name - a schema-qualified name belongs in schema.
func tableGrantsIdentifier(table, schema string) (string, error) {
	if strings.Contains(table, ".") {
		return "", fmt.Errorf("table %q must not contain a schema prefix; set schema instead", table)
	}
	id := table
	if schema != "" {
		id = schema + "." + table
	}
	if _, err := spanneracl.ParseIdentifier(id, "TABLE"); err != nil {
		return "", err
	}
	return id, nil
}

// tableGrantsFromImportID builds the state for an import ID ("table" or
// "schema.table"), with ID, Table and Schema set and Grants left for the
// caller. Schema is null, not "", for a table in the default schema, matching a
// configuration that omits schema - otherwise the first plan after import would
// see a change and force replacement.
func tableGrantsFromImportID(id string) (tg tableGrantsResourceModel, err error) {
	parsed, err := spanneracl.ParseIdentifier(id, "TABLE")
	if err != nil {
		return tg, err
	}
	schema := types.StringNull()
	if parsed.Schema != "" {
		schema = types.StringValue(parsed.Schema)
	}
	tg.ID = types.StringValue(id)
	tg.Table = types.StringValue(parsed.ResourceName)
	tg.Schema = schema
	// Filled in by the Read that follows the import.
	tg.ColumnLevelGrants = types.SetNull(types.StringType)
	return tg, nil
}
