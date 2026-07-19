package triggers

import triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"

// IsUserAllowed checks whether a user is permitted to trigger an AgentRun.
// Deny list takes precedence over allow list. Empty auth means open access.
func IsUserAllowed(auth *triggersv1alpha1.TriggerAuth, userID string) bool {
	if auth == nil {
		return true
	}
	for _, denied := range auth.DenyUsers {
		if denied == userID {
			return false
		}
	}
	if len(auth.AllowedUsers) == 0 {
		return true
	}
	for _, allowed := range auth.AllowedUsers {
		if allowed == userID {
			return true
		}
	}
	return false
}
