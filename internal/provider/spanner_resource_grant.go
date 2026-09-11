// Copyright RetailNext, Inc. 2026

package provider

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/setplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
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

// caseInsensitiveRequiresReplace returns a plan modifier for Privilege/
// ResourceType: it behaves exactly like stringplanmodifier.RequiresReplace
// (same skip-on-create/skip-on-destroy/no-op-if-unchanged rules - mirrored
// from its implementation) except that a change which only differs in case
// is not treated as a change at all. This matches spanneracl.ValidateGrant
// (via spanneracl.NormalizeGrant), which treats these two fields
// case-insensitively - without this, changing "select" to "SELECT" in
// config, or importing a lower-case ID and then writing the upper-case
// spelling, would plan a spurious destroy/recreate even though the
// underlying grant is unchanged.
func caseInsensitiveRequiresReplace() planmodifier.String {
	return caseInsensitiveRequiresReplaceModifier{}
}

type caseInsensitiveRequiresReplaceModifier struct{}

func (m caseInsensitiveRequiresReplaceModifier) Description(_ context.Context) string {
	return "If the value of this attribute changes (case-insensitively), Terraform will destroy and recreate the resource."
}

func (m caseInsensitiveRequiresReplaceModifier) MarkdownDescription(ctx context.Context) string {
	return m.Description(ctx)
}

func (m caseInsensitiveRequiresReplaceModifier) PlanModifyString(_ context.Context, req planmodifier.StringRequest, resp *planmodifier.StringResponse) {
	// Do not replace on resource creation.
	if req.State.Raw.IsNull() {
		return
	}
	// Do not replace on resource destroy.
	if req.Plan.Raw.IsNull() {
		return
	}
	// No change at all.
	if req.PlanValue.Equal(req.StateValue) {
		return
	}
	// An unknown value (e.g. derived from another resource's attribute)
	// can't be case-folded against - conservatively require replace, same
	// as the plan/state values simply differing.
	if req.PlanValue.IsUnknown() || req.StateValue.IsUnknown() {
		resp.RequiresReplace = true
		return
	}
	// A change that is only a case difference is not a real change.
	if strings.EqualFold(req.PlanValue.ValueString(), req.StateValue.ValueString()) {
		return
	}
	resp.RequiresReplace = true
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
	grant = spanneracl.NormalizeGrant(grant)

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
					"https://cloud.google.com/spanner/docs/fgac-privileges. Case-insensitive - changing only the " +
					"case of an already-applied value does not plan a replace.",
				Required: true,
				Validators: []validator.String{
					stringvalidator.OneOfCaseInsensitive(
						"SELECT", "INSERT", "UPDATE", "DELETE", "EXECUTE", "USAGE",
					),
				},
				PlanModifiers: []planmodifier.String{
					caseInsensitiveRequiresReplace(),
				},
			},
			"resource_type": schema.StringAttribute{
				Description: "The type of resource being granted on: `TABLE`, `VIEW`, or `CHANGE STREAM`. Spanner also " +
					"supports granting on `SEQUENCE`, `SCHEMA`, and `TABLE FUNCTION`, but those aren't supported by this " +
					"resource yet - there's no confirmed way to read such a grant back, which would make Read unable to " +
					"detect drift and Terraform loop trying to re-create the resource on every plan. Case-insensitive - " +
					"changing only the case of an already-applied value does not plan a replace.",
				Required: true,
				Validators: []validator.String{
					stringvalidator.OneOfCaseInsensitive("TABLE", "VIEW", "CHANGE STREAM"),
				},
				PlanModifiers: []planmodifier.String{
					caseInsensitiveRequiresReplace(),
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
			"Unexpected Data Resource Configure Type",
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

	var columns []string
	if parts[4] != "" {
		columns = strings.Split(parts[4], ",")
	}

	grant := spanneracl.Grant{
		RoleName:     parts[0],
		Privilege:    parts[1],
		ResourceType: parts[2],
		Resource:     parts[3],
		Columns:      columns,
	}

	// Not strictly required for correctness; just fail fast.
	if err := spanneracl.ValidateGrant(grant); err != nil {
		resp.Diagnostics.AddError("Invalid Import ID", err.Error())
		return
	}

	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), grantID(grant))...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("role_name"), parts[0])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("privilege"), parts[1])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("resource_type"), parts[2])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("resource"), parts[3])...)
	if len(columns) > 0 {
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("columns"), columns)...)
	}
}
