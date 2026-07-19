package dashboard

import (
	"testing"

	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
)

// TestPreserveAdminOnlyTriggerDefaults pins the helper shared by every
// dashboard trigger update (Cron, GitHubRepository, LinearProject, SlackAgent):
// rebuilt defaults keep the stored kubectl-only admin flags, in both
// directions (set stays set, unset stays unset).
func TestPreserveAdminOnlyTriggerDefaults(t *testing.T) {
	rebuilt := triggersv1alpha1.AgentRunDefaults{Model: "gpt-5.2"}
	preserveAdminOnlyTriggerDefaults(&rebuilt, triggersv1alpha1.AgentRunDefaults{
		DisableCommandSandbox: true,
		KubernetesAdmin:       true,
	})
	if !rebuilt.DisableCommandSandbox || !rebuilt.KubernetesAdmin {
		t.Fatalf("rebuilt = %+v, want admin-only flags preserved", rebuilt)
	}
	if rebuilt.Model != "gpt-5.2" {
		t.Fatalf("Model = %q, want request value kept", rebuilt.Model)
	}

	cleared := triggersv1alpha1.AgentRunDefaults{DisableCommandSandbox: true, KubernetesAdmin: true}
	preserveAdminOnlyTriggerDefaults(&cleared, triggersv1alpha1.AgentRunDefaults{})
	if cleared.DisableCommandSandbox || cleared.KubernetesAdmin {
		t.Fatalf("cleared = %+v, want flags mirroring the stored trigger (off)", cleared)
	}
}
