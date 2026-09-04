// Copyright RetailNext, Inc. 2026

package provider

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/setplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/retailnext/terraform-provider-spanner/spanneracl"
)

// Ensure grantResource fully satisfies framework interfaces.
var _ resource.Resource = &grantResource{}
var _ resource.ResourceWithConfigure = &grantResource{}
var _ resource.ResourceWithImportState = &grantResource{}
var _ resource.ResourceWithValidateConfig = &grantResource{}

func NewGrantResource() resource.Resource {
	return &grantResource{}
}

// grantResource defines the resource implementation.
type grantResource struct {
	client *spanneracl.Client
}

// grantResourceModel maps the resource schema data.
type grantResourceModel struct {
	ID           types.String `tfsdk:"id"`
	RoleName     types.String `tfsdk:"role_name"`
	Privilege    types.String `tfsdk:"privilege"`
	ResourceType types.String `tfsdk:"resource_type"`
	Resource     types.String `tfsdk:"resource"`
	Columns      types.Set    `tfsdk:"columns"`
}

// toGrant converts the model to a spanneracl.Grant and validates it via
// spanneracl.ValidateGrant, so every caller - Create/Read/Update/Delete as
// well as ValidateConfig - gets the same validation for free instead of
// each one having to remember to call it separately. Columns is only read
// when it's known and non-null - an unset/null "columns" means a
// table-level grant, matching Grant.Columns' zero value (nil).
func (m grantResourceModel) toGrant(ctx context.Context) (spanneracl.Grant, diag.Diagnostics) {
	var diags diag.Diagnostics

	var columns []string
	if !m.Columns.IsNull() && !m.Columns.IsUnknown() {
		diags.Append(m.Columns.ElementsAs(ctx, &columns, false)...)
	}

	grant := spanneracl.Grant{
		RoleName:     m.RoleName.ValueString(),
		Privilege:    m.Privilege.ValueString(),
		ResourceType: m.ResourceType.ValueString(),
		Resource:     m.Resource.ValueString(),
		Columns:      columns,
	}

	if err := spanneracl.ValidateGrant(grant); err != nil {
		diags.AddError("Invalid Grant Configuration", err.Error())
	}

	return grant, diags
}

// grantID builds the synthetic ID used for this resource: a pipe-delimited
// encoding of every field that identifies the grant. Every field
// (including columns) is part of the grant's identity, since Spanner has
// no "ALTER GRANT" - changing any of them means revoking the old grant and
// creating a new one, not updating in place. Columns is sorted on a copy
// (grant.Columns itself is left untouched) so two configs granting the
// same columns in a different order produce the same ID instead of
// spuriously planning a replace.
func grantID(grant spanneracl.Grant) string {
	columns := slices.Clone(grant.Columns)
	slices.Sort(columns)

	return fmt.Sprintf("%s|%s|%s|%s|%s",
		grant.RoleName, grant.Privilege, grant.ResourceType, grant.Resource, strings.Join(columns, ","))
}

// Metadata returns the resource type name.
func (g *grantResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_grant"
}

// Schema defines the supported configuration, plan, and state attributes.
func (g *grantResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages a single privilege grant on a Spanner resource to a database role. Spanner has " +
			"no `ALTER GRANT`/partial-update DDL: a grant is identified entirely by its role, privilege, resource " +
			"type, resource, and (for column-level grants) column list, so changing any attribute forces the grant " +
			"to be revoked and re-granted rather than updated in place.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "Synthetic ID identifying the grant: `role_name|privilege|resource_type|resource|columns` " +
					"(columns comma-separated, empty when the grant is table-level).",
				Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"role_name": schema.StringAttribute{
				Description: "The database role the privilege is granted to.",
				Required:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"privilege": schema.StringAttribute{
				Description: "The privilege to grant: `SELECT`, `INSERT`, `UPDATE`, `DELETE`, `EXECUTE`, or `USAGE`. " +
					"Which privileges apply to which `resource_type` is documented at " +
					"https://cloud.google.com/spanner/docs/fgac-privileges.",
				Required: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"resource_type": schema.StringAttribute{
				Description: "The type of resource being granted on: `TABLE`, `VIEW`, or `CHANGE STREAM`. Spanner also " +
					"supports granting on `SEQUENCE`, `SCHEMA`, and `TABLE FUNCTION`, but those aren't supported by this " +
					"resource yet - there's no confirmed way to read such a grant back, which would make Read unable to " +
					"detect drift and Terraform loop trying to re-create the resource on every plan.",
				Required: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"resource": schema.StringAttribute{
				Description: "The name of the resource, optionally schema-qualified as `schema_name.resource_name`.",
				Required:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"columns": schema.SetAttribute{
				Description: "Column names to restrict a `TABLE` grant to, for column-level `SELECT`/`INSERT`/`UPDATE` " +
					"privileges. Leave unset for a table-level grant. Unordered - listing the same columns in a " +
					"different order does not plan a change.",
				ElementType: types.StringType,
				Optional:    true,
				PlanModifiers: []planmodifier.Set{
					setplanmodifier.RequiresReplace(),
				},
			},
		},
	}
}

// Configure fetches the configured client from the provider.
func (g *grantResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	// Prevent panic if the provider has not been configured.
	if req.ProviderData == nil {
		return
	}

	client, ok := req.ProviderData.(*spanneracl.Client)
	if !ok {
		resp.Diagnostics.AddError(
			"Unexpected Data Source Configure Type",
			fmt.Sprintf("Expected *spanneracl.Client, got: %T. Please report this issue to the provider developers.", req.ProviderData),
		)

		return
	}

	g.client = client
}

// ValidateConfig gives early, config-time feedback on Privilege/ResourceType
// vocabulary and the TABLE-specific column rules, via toGrant's call to
// spanneracl.ValidateGrant - the same validation every other CRUD method
// gets from toGrant, surfaced before `terraform apply` instead of only at
// apply time.
func (g *grantResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var config grantResourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Unknown values (e.g. derived from another resource's attribute) can't
	// be validated yet - Create/Update/Delete still validate the final
	// values at apply time regardless, via toGrant.
	if config.RoleName.IsUnknown() || config.Privilege.IsUnknown() || config.ResourceType.IsUnknown() ||
		config.Resource.IsUnknown() || config.Columns.IsUnknown() {
		return
	}

	_, diags := config.toGrant(ctx)
	resp.Diagnostics.Append(diags...)
}

// Create grants the privilege.
func (g *grantResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan grantResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	grant, diags := plan.toGrant(ctx)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := g.client.CreateGrant(ctx, grant); err != nil {
		resp.Diagnostics.AddError(
			"Unable to create the grant",
			err.Error(),
		)
		return
	}

	plan.ID = types.StringValue(grantID(grant))

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Read checks whether the grant still exists and refreshes state
// accordingly. The provider invokes this before every plan.
func (g *grantResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state grantResourceModel

	// Read the state.
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	grant, diags := state.toGrant(ctx)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	exists, err := g.client.GrantExists(ctx, grant)
	if err != nil {
		resp.Diagnostics.AddError(
			"Unable to read the grant",
			err.Error(),
		)
		return
	}
	if !exists {
		// Revoked out-of-band since the last apply - drop it from state so
		// the next plan re-creates it instead of erroring.
		resp.State.RemoveResource(ctx)
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update is required to satisfy resource.Resource, but is never actually
// driven by Terraform for this resource: every configurable attribute
// carries a RequiresReplace plan modifier (Spanner has no ALTER GRANT), so
// any change plans a destroy-and-recreate instead of an update.
func (g *grantResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan grantResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	grant, diags := plan.toGrant(ctx)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	plan.ID = types.StringValue(grantID(grant))

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete revokes the grant.
func (g *grantResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state grantResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	grant, diags := state.toGrant(ctx)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := g.client.DeleteGrant(ctx, grant); err != nil {
		resp.Diagnostics.AddError(
			"Unable to delete the grant",
			err.Error(),
		)
		return
	}
}

// ImportState imports an existing grant from its synthetic ID:
// "role_name|privilege|resource_type|resource|columns" (columns
// comma-separated, may be empty for a table-level grant). The framework
// runs Read automatically after ImportState returns, so this only needs to
// populate state from the ID - GrantExists confirms the grant is real.
func (g *grantResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.Split(req.ID, "|")
	if len(parts) != 5 {
		resp.Diagnostics.AddError(
			"Invalid Import ID",
			fmt.Sprintf("Expected ID format: role_name|privilege|resource_type|resource|columns, got: %s", req.ID),
		)
		return
	}

	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("role_name"), parts[0])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("privilege"), parts[1])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("resource_type"), parts[2])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("resource"), parts[3])...)
	if parts[4] != "" {
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("columns"), strings.Split(parts[4], ","))...)
	}
}
