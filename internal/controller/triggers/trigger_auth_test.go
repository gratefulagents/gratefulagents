package triggers

import (
	"testing"

	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
)

func TestIsUserAllowed(t *testing.T) {
	tests := []struct {
		name   string
		auth   *triggersv1alpha1.TriggerAuth
		userID string
		want   bool
	}{
		{"nil auth allows all", nil, "U123", true},
		{"empty auth allows all", &triggersv1alpha1.TriggerAuth{}, "U123", true},
		{"allowed user", &triggersv1alpha1.TriggerAuth{AllowedUsers: []string{"U123", "U456"}}, "U123", true},
		{"denied by allow list", &triggersv1alpha1.TriggerAuth{AllowedUsers: []string{"U456"}}, "U123", false},
		{"deny takes precedence", &triggersv1alpha1.TriggerAuth{AllowedUsers: []string{"U123"}, DenyUsers: []string{"U123"}}, "U123", false},
		{"deny list only", &triggersv1alpha1.TriggerAuth{DenyUsers: []string{"U123"}}, "U123", false},
		{"deny list allows others", &triggersv1alpha1.TriggerAuth{DenyUsers: []string{"U123"}}, "U456", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsUserAllowed(tt.auth, tt.userID); got != tt.want {
				t.Errorf("IsUserAllowed() = %v, want %v", got, tt.want)
			}
		})
	}
}
