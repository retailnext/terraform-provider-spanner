// Copyright RetailNext, Inc. 2026

package provider

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// grantModel is one grant block: a role and the complete set of privileges it
// should hold on the resource.
type grantModel struct {
	Role       types.String `tfsdk:"role"`
	Privileges types.Set    `tfsdk:"privileges"`
}

// Grants is the set of grant blocks on an authoritative grants resource.
type Grants []grantModel

// grantPermissionMapToGrants converts a role -> privileges map, as returned by
// spanneracl.GetAllRolePermissionsPerId, into grant blocks, one per role in
// role-name order.
func grantPermissionMapToGrants(ctx context.Context, permissionMap map[string][]string) (Grants, diag.Diagnostics) {
	roles := make([]string, 0, len(permissionMap))
	for role := range permissionMap {
		roles = append(roles, role)
	}
	slices.Sort(roles)

	var diags diag.Diagnostics
	grants := make(Grants, 0, len(roles))
	for _, role := range roles {
		privileges, d := types.SetValueFrom(ctx, types.StringType, permissionMap[role])
		diags.Append(d...)
		if diags.HasError() {
			return nil, diags
		}
		grants = append(grants, grantModel{
			Role:       types.StringValue(role),
			Privileges: privileges,
		})
	}
	return grants, diags
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
