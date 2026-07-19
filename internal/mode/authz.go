package mode

import (
	"context"
	"fmt"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Role defines the RBAC level for mode transitions.
type Role string

const (
	RoleViewer   Role = "viewer"
	RoleMember   Role = "member"
	RoleAdmin    Role = "admin"
	RoleOrgOwner Role = "org_owner"
	RoleSystem   Role = "system"
)

// roleRank returns the numeric rank of a role for comparison.
func roleRank(r Role) int {
	switch r {
	case RoleViewer:
		return 0
	case RoleMember:
		return 1
	case RoleAdmin:
		return 2
	case RoleOrgOwner:
		return 3
	case RoleSystem:
		return 4
	default:
		return -1
	}
}

const (
	// minRoleInstalledTemplate is the minimum role required to switch to any
	// installed ModeTemplate. Installing templates is the admin-controlled
	// act; every system template (chat, plan, autopilot, review, ...) is
	// member-accessible regardless of category. Viewers can never switch.
	minRoleInstalledTemplate = RoleMember
	// minRoleUnknownTemplate is the fail-closed minimum role when the target
	// template cannot be resolved.
	minRoleUnknownTemplate = RoleAdmin
)

// AuthorizeTransition checks whether the given role is allowed to switch to
// the target mode. Any installed ModeTemplate is member-accessible; unknown
// modes fail closed to admin.
func AuthorizeTransition(ctx context.Context, k8s client.Reader, targetModeName string, actorRole Role) *GateResult {
	if actorRole == RoleSystem {
		return nil // system always allowed
	}

	// Look up the live CRD: installed templates require member.
	var tmpl platformv1alpha1.ModeTemplate
	minRole := minRoleUnknownTemplate
	if err := k8s.Get(ctx, client.ObjectKey{Name: targetModeName}, &tmpl); err == nil {
		minRole = minRoleInstalledTemplate
	}

	if roleRank(actorRole) < roleRank(minRole) {
		return &GateResult{
			Gate:     "rbac",
			Passed:   false,
			Reason:   fmt.Sprintf("role %q insufficient for mode %q (requires %q)", actorRole, targetModeName, minRole),
			DenyCode: DenyRBACDenied,
		}
	}
	return nil
}

// WorkspaceAllowlist checks whether a mode template is allowed in a namespace.
// Always reads the live CRD — ModeTemplates are cluster-scoped.
func WorkspaceAllowlist(ctx context.Context, k8s client.Client, namespace string, templateKey string) *GateResult {
	var tmpl platformv1alpha1.ModeTemplate
	if err := k8s.Get(ctx, client.ObjectKey{Name: templateKey}, &tmpl); err != nil {
		return &GateResult{
			Gate:     "workspace_allowlist",
			Passed:   false,
			Reason:   fmt.Sprintf("mode template %q not found", templateKey),
			DenyCode: DenyTemplateNotFound,
		}
	}
	return nil
}
