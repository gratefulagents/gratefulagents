package v1alpha1

import "strings"

// TriggerAuth gates who can trigger AgentRun creation from an external system.
type TriggerAuth struct {
	// allowedUsers restricts who can trigger runs. Each entry is a platform-specific
	// identifier: Slack user ID, Discord user ID, GitHub username, or email.
	// Empty means no restriction for list-only platforms such as Linear. For
	// GitHub ingress, empty allowedUsers defaults to repository owners, members,
	// and collaborators based on GitHub author_association.
	// +optional
	AllowedUsers []string `json:"allowedUsers,omitempty"`

	// denyUsers explicitly blocks specific users from triggering runs.
	// Deny list takes precedence over allow list.
	// +optional
	DenyUsers []string `json:"denyUsers,omitempty"`
}

// IsUserAllowed checks list-only trigger authorization. DenyUsers takes
// precedence over allowedUsers; empty allowedUsers means open access.
func (auth *TriggerAuth) IsUserAllowed(userID string) bool {
	if auth == nil {
		return true
	}
	if auth.isDenied(userID) {
		return false
	}
	if len(auth.AllowedUsers) == 0 {
		return true
	}
	return auth.isExplicitlyAllowed(userID)
}

// IsGitHubActorAllowed checks GitHub trigger authorization. DenyUsers always
// wins. Non-empty allowedUsers is an exact-login allowlist. With no allowlist,
// only actors whose author_association is OWNER, MEMBER, or COLLABORATOR are
// allowed.
func (auth *TriggerAuth) IsGitHubActorAllowed(login, authorAssociation string) bool {
	if auth != nil {
		if auth.isDenied(login) {
			return false
		}
		if len(auth.AllowedUsers) > 0 {
			return auth.isExplicitlyAllowed(login)
		}
	}
	switch strings.ToUpper(strings.TrimSpace(authorAssociation)) {
	case "OWNER", "MEMBER", "COLLABORATOR":
		return true
	default:
		return false
	}
}

func (auth *TriggerAuth) isDenied(userID string) bool {
	for _, denied := range auth.DenyUsers {
		if denied == userID {
			return true
		}
	}
	return false
}

func (auth *TriggerAuth) isExplicitlyAllowed(userID string) bool {
	for _, allowed := range auth.AllowedUsers {
		if allowed == userID {
			return true
		}
	}
	return false
}
