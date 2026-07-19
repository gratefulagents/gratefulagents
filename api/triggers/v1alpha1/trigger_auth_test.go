package v1alpha1

import "testing"

func TestTriggerAuthIsUserAllowed(t *testing.T) {
	tests := []struct {
		name string
		auth *TriggerAuth
		user string
		want bool
	}{
		{"nil auth allows list-only platform", nil, "U123", true},
		{"empty auth allows list-only platform", &TriggerAuth{}, "U123", true},
		{"allowed user", &TriggerAuth{AllowedUsers: []string{"U123", "U456"}}, "U123", true},
		{"denied by allow list", &TriggerAuth{AllowedUsers: []string{"U456"}}, "U123", false},
		{"deny takes precedence", &TriggerAuth{AllowedUsers: []string{"U123"}, DenyUsers: []string{"U123"}}, "U123", false},
		{"deny list only", &TriggerAuth{DenyUsers: []string{"U123"}}, "U123", false},
		{"deny list allows others", &TriggerAuth{DenyUsers: []string{"U123"}}, "U456", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.auth.IsUserAllowed(tt.user); got != tt.want {
				t.Errorf("IsUserAllowed() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestTriggerAuthIsGitHubActorAllowed(t *testing.T) {
	tests := []struct {
		name              string
		auth              *TriggerAuth
		login             string
		authorAssociation string
		want              bool
	}{
		{"org member allowed with empty list", nil, "member", "MEMBER", true},
		{"collaborator allowed with empty list", &TriggerAuth{}, "collab", "COLLABORATOR", true},
		{"owner allowed case-insensitively", &TriggerAuth{}, "owner", "owner", true},
		{"random user rejected with empty list", nil, "random", "NONE", false},
		{"missing association rejected with empty list", &TriggerAuth{}, "random", "", false},
		{"explicit allowlist allows none association", &TriggerAuth{AllowedUsers: []string{"random"}}, "random", "NONE", true},
		{"explicit allowlist rejects non-listed member", &TriggerAuth{AllowedUsers: []string{"other"}}, "member", "MEMBER", false},
		{"deny beats explicit allowlist", &TriggerAuth{AllowedUsers: []string{"random"}, DenyUsers: []string{"random"}}, "random", "NONE", false},
		{"deny beats trusted association", &TriggerAuth{DenyUsers: []string{"member"}}, "member", "MEMBER", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.auth.IsGitHubActorAllowed(tt.login, tt.authorAssociation); got != tt.want {
				t.Errorf("IsGitHubActorAllowed() = %v, want %v", got, tt.want)
			}
		})
	}
}
