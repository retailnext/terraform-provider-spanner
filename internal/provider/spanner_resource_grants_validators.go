// Copyright RetailNext, Inc. 2026

package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// uniqueGrantRolesValidator rejects a grant set that contains duplicate role
// values, so a role's privileges are always defined in exactly one grant block.
// spanneracl.ApplyAuthoritativeBinding rejects duplicate roles too; checking
// here reports it at plan time instead of during apply.
type uniqueGrantRolesValidator struct{}

// Description returns a plain-text description of the validator.
func (uniqueGrantRolesValidator) Description(_ context.Context) string {
	return "Grant roles must be unique across all grant blocks."
}

// MarkdownDescription returns a Markdown description of the validator.
func (uniqueGrantRolesValidator) MarkdownDescription(_ context.Context) string {
	return "Grant roles must be unique across all grant blocks."
}

// ValidateSet reports an error on the first role that appears in more than one
// grant block. Unknown or null values are skipped: they can't be compared yet.
func (v uniqueGrantRolesValidator) ValidateSet(ctx context.Context, req validator.SetRequest, resp *validator.SetResponse) {
	if req.ConfigValue.IsUnknown() || req.ConfigValue.IsNull() {
		return
	}
	var grants []grantModel
	resp.Diagnostics.Append(req.ConfigValue.ElementsAs(ctx, &grants, true)...)
	if resp.Diagnostics.HasError() {
		return
	}
	seen := make(map[string]bool, len(grants))
	for _, g := range grants {
		if g.Role.IsUnknown() || g.Role.IsNull() {
			continue
		}
		role := g.Role.ValueString()
		if seen[role] {
			resp.Diagnostics.AddAttributeError(req.Path, "Duplicate Grant Role",
				fmt.Sprintf("Role %q appears in more than one grant block.", role))
			return
		}
		seen[role] = true
	}
}

// emptySetPlanModifier always plans a computed set attribute as an empty set,
// for attributes that report state the resource always removes on apply (e.g.
// column_level_grants). A non-empty value from Read then differs from the plan,
// so Terraform plans an update that removes it. Destroy plans are left alone.
type emptySetPlanModifier struct{}

// Description returns a plain-text description of the plan modifier.
func (emptySetPlanModifier) Description(_ context.Context) string {
	return "Always planned as an empty set; any value found on refresh is removed by the next apply."
}

// MarkdownDescription returns a Markdown description of the plan modifier.
func (m emptySetPlanModifier) MarkdownDescription(ctx context.Context) string {
	return m.Description(ctx)
}

// PlanModifySet sets the planned value to an empty set, unless the resource is
// being destroyed.
func (emptySetPlanModifier) PlanModifySet(ctx context.Context, req planmodifier.SetRequest, resp *planmodifier.SetResponse) {
	if req.Plan.Raw.IsNull() {
		return
	}
	resp.PlanValue = types.SetValueMust(req.PlanValue.ElementType(ctx), []attr.Value{})
}
