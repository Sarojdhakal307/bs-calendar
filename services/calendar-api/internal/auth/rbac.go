// Package auth implements admin authentication (password login, JWT access tokens,
// rotating refresh tokens), API keys for public clients, and role-based permissions.
package auth

import "slices"

// Roles, lowest to highest privilege.
const (
	RoleViewer        = "viewer"
	RoleEditor        = "editor"
	RoleDesigner      = "designer"
	RoleCalendarAdmin = "calendar_admin"
	RoleSuperAdmin    = "super_admin"
)

// Roles lists every valid role.
var Roles = []string{RoleViewer, RoleEditor, RoleDesigner, RoleCalendarAdmin, RoleSuperAdmin}

// ValidRole reports whether r is a known role.
func ValidRole(r string) bool { return slices.Contains(Roles, r) }

// Permission is a capability checked on admin routes.
type Permission string

// Permissions (see the role matrix in docs/architecture.md §12).
const (
	PermRead            Permission = "read"
	PermEventsWrite     Permission = "events:write"
	PermCategoriesWrite Permission = "categories:write"
	PermYearsPropose    Permission = "years:propose"
	PermYearsApprove    Permission = "years:approve"
	PermConfigDraft     Permission = "config:draft"
	PermConfigPublish   Permission = "config:publish"
	PermPlatformManage  Permission = "platform:manage"
)

var rolePerms = map[string][]Permission{
	RoleViewer:        {PermRead},
	RoleEditor:        {PermRead, PermEventsWrite},
	RoleDesigner:      {PermRead, PermConfigDraft, PermConfigPublish},
	RoleCalendarAdmin: {PermRead, PermEventsWrite, PermCategoriesWrite, PermYearsPropose, PermYearsApprove},
	RoleSuperAdmin: {PermRead, PermEventsWrite, PermCategoriesWrite, PermYearsPropose, PermYearsApprove,
		PermConfigDraft, PermConfigPublish, PermPlatformManage},
}

// Can reports whether a role has a permission.
func Can(role string, p Permission) bool { return slices.Contains(rolePerms[role], p) }
