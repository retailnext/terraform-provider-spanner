// Copyright RetailNext, Inc. 2026

package provider

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// grantModel is one grant block: a role and the complete set of privileges it
// should hold on the resource.
type grantModel struct {
	Role       types.String `tfsdk:"role"`
	Privileges types.Set    `tfsdk:"privileges"`
}

// grantObjectType is the Terraform type of one grant block, i.e. of
// grantModel. It must match the grant block's NestedObject in the resource
// schema.
var grantObjectType = types.ObjectType{AttrTypes: map[string]attr.Type{
	"role":       types.StringType,
	"privileges": types.SetType{ElemType: types.StringType},
}}

// grantPermissionMapToGrants converts a role -> privileges map, as returned by
// spanneracl.GetAllRolePermissionsPerId, into a set of grant blocks, one per
// role. An empty map gives an empty set, not null.
func grantPermissionMapToGrants(ctx context.Context, permissionMap map[string][]string) (types.Set, diag.Diagnostics) {
	var diags diag.Diagnostics
	grants := make([]grantModel, 0, len(permissionMap))
	for role, rolePrivileges := range permissionMap {
		privileges, d := types.SetValueFrom(ctx, types.StringType, rolePrivileges)
		diags.Append(d...)
		if diags.HasError() {
			return types.SetNull(grantObjectType), diags
		}
		grants = append(grants, grantModel{
			Role:       types.StringValue(role),
			Privileges: privileges,
		})
	}
	set, d := types.SetValueFrom(ctx, grantObjectType, grants)
	diags.Append(d...)
	return set, diags
}

// columnLevelGrantsToSet formats a role -> privilege -> columns map, as returned
// by spanneracl.GetColumnLevelOnlyPermissionsPerId, as a set of
// "role:PRIVILEGE(col1,col2)" strings, with columns sorted so the same grant
// always formats the same way.
func columnLevelGrantsToSet(ctx context.Context, permissions map[string]map[string][]string) (types.Set, diag.Diagnostics) {
	var grants []string
	for role, privileges := range permissions {
		for privilege, columns := range privileges {
			sorted := slices.Clone(columns)
			slices.Sort(sorted)
			grants = append(grants, fmt.Sprintf("%s:%s(%s)", role, privilege, strings.Join(sorted, ",")))
		}
	}
	slices.Sort(grants)
	if grants == nil {
		grants = []string{}
	}
	return types.SetValueFrom(ctx, types.StringType, grants)
}
