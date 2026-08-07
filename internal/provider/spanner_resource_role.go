// Copyright RetailNext, Inc. 2026

package provider

import (
	"context"
	"errors"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/retailnext/terraform-provider-spanner/spanneracl"
)

// Ensure roleResource fully satisfies framework interfaces.
var _ resource.Resource = &roleResource{}
var _ resource.ResourceWithConfigure = &roleResource{}
var _ resource.ResourceWithImportState = &roleResource{}

func NewRoleResource() resource.Resource {
	return &roleResource{}
}

// roleResource defines the resource implementation.
type roleResource struct {
	client *spanneracl.Client
}

// roleResourceModel maps the resource schema data.
type roleResourceModel struct {
	ID   types.String `tfsdk:"id"`
	Name types.String `tfsdk:"name"`
}

// Metadata returns the resource type name.
func (r *roleResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_role"
}

// Schema defines the supported configuration, plan, and state attributes.
func (r *roleResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages a Spanner database role used for fine-grained access control (FGAC).",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "The name of the role. Identical to `name`.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(), // the attribute is not configurable and should not show updates from the existing state
				},
			},
			"name": schema.StringAttribute{
				Description: "The name of the database role. Spanner has no rename-role operation, so changing this forces the role to be dropped and re-created.",
				Required:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
		},
	}
}

// Configure fetches the configured client from the provider.
func (r *roleResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	// Prevent panic if the provider has not been configured.
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

// Create creates a new role.
func (r *roleResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	// Retrieve values from plan
	var plan roleResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	role := spanneracl.Role{Name: plan.Name.ValueString()}

	if err := r.client.CreateRole(ctx, role); err != nil {
		resp.Diagnostics.AddError(
			"Unable to create the role",
			err.Error(),
		)
		return
	}

	// Populate computed attribute values
	plan.ID = types.StringValue(role.Name)

	// Set state to fully populate data
	resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
}

// Read retrieves the resource's current information and refreshes state.
// The provider invokes this before every plan.
func (r *roleResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state roleResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	curRole, err := r.client.GetRole(ctx, state.ID.ValueString())
	if err != nil {
		if errors.Is(err, spanneracl.ErrRoleNotFound) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError(
			"Unable to read the role",
			err.Error(),
		)
		return
	}

	// Overwrite with refreshed state.
	state = roleResourceModel{
		ID:   types.StringValue(curRole.Name),
		Name: types.StringValue(curRole.Name),
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update is required to satisfy resource.Resource, but is never actually
// driven by Terraform for this resource: `name` is the only configurable
// attribute and it carries a RequiresReplace plan modifier (Spanner has no
// ALTER ROLE / rename-role operation), so any change to it plans a
// destroy-and-recreate instead of an update.
func (r *roleResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan roleResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	plan.ID = types.StringValue(plan.Name.ValueString())

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete drops the role.
func (r *roleResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state roleResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	role := spanneracl.Role{Name: state.Name.ValueString()}

	if err := r.client.DeleteRole(ctx, role); err != nil {
		resp.Diagnostics.AddError(
			"Unable to delete the role",
			err.Error(),
		)
		return
	}
}

// ImportState imports an existing role by name.
func (r *roleResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
