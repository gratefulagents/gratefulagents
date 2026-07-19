package auth

import (
	"os"
	"strings"
)

// RoleAdmin has full access to everything.
const RoleAdmin = "admin"

// RoleMember can create/modify own resources and view shared resources.
const RoleMember = "member"

// RoleViewer has read-only access to all resources.
const RoleViewer = "viewer"

// RoleResolver determines what role a Google OAuth user should get.
type RoleResolver struct {
	adminEmails      map[string]bool
	ssoDefaultViewer bool
}

// NewRoleResolver creates a role resolver from environment configuration.
// ADMIN_EMAILS: comma-separated emails that get admin role.
// SSO_DEFAULT_READONLY: if "true", non-admin SSO users get viewer role instead of member.
func NewRoleResolver() *RoleResolver {
	adminEmails := make(map[string]bool)
	if raw := os.Getenv("ADMIN_EMAILS"); raw != "" {
		for _, email := range strings.Split(raw, ",") {
			email = strings.TrimSpace(strings.ToLower(email))
			if email != "" {
				adminEmails[email] = true
			}
		}
	}

	ssoDefault := strings.EqualFold(os.Getenv("SSO_DEFAULT_READONLY"), "true")

	return &RoleResolver{
		adminEmails:      adminEmails,
		ssoDefaultViewer: ssoDefault,
	}
}

// ResolveRole determines the role for a Google OAuth user based on their email.
func (r *RoleResolver) ResolveRole(email string) string {
	if r.adminEmails[strings.ToLower(email)] {
		return RoleAdmin
	}
	if r.ssoDefaultViewer {
		return RoleViewer
	}
	return RoleMember
}
