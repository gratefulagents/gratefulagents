package triggers

import (
	"testing"
	"time"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TestBuildTriggerRunDisableCommandSandbox covers the admin-set trigger
// option that completely disables the bubblewrap command sandbox for runs
// created from that trigger.
func TestBuildTriggerRunDisableCommandSandbox(t *testing.T) {
	run := BuildTriggerRun(TriggerRunSpec{
		RunName:     "run-1",
		Namespace:   "default",
		TriggerKind: "GitHubRepository",
		TriggerName: "payments",
		Defaults: triggersv1alpha1.AgentRunDefaults{
			RepoURL:               "https://github.com/example/repo.git",
			Model:                 "gpt-5.4",
			DisableCommandSandbox: true,
		},
	})
	if !run.Spec.DisableCommandSandbox {
		t.Fatal("Spec.DisableCommandSandbox = false, want true (copied from trigger defaults)")
	}
}

// TestBuildTriggerRunCommandSandboxEnabledByDefault pins the fail-safe
// default: triggers that do not opt out keep the enforcing sandbox.
func TestBuildTriggerRunCommandSandboxEnabledByDefault(t *testing.T) {
	run := BuildTriggerRun(TriggerRunSpec{
		RunName:     "run-1",
		Namespace:   "default",
		TriggerKind: "GitHubRepository",
		TriggerName: "payments",
		Defaults: triggersv1alpha1.AgentRunDefaults{
			RepoURL: "https://github.com/example/repo.git",
			Model:   "gpt-5.4",
		},
	})
	if run.Spec.DisableCommandSandbox {
		t.Fatal("Spec.DisableCommandSandbox = true, want false by default")
	}
}

// TestApplyPolicyRefsDoesNotReenableSandbox ensures defaults without the
// opt-out never clear a flag already set on the spec by another path.
func TestApplyPolicyRefsDoesNotReenableSandbox(t *testing.T) {
	spec := platformv1alpha1.AgentRunSpec{DisableCommandSandbox: true}
	applyPolicyRefs(&spec, triggersv1alpha1.AgentRunDefaults{})
	if !spec.DisableCommandSandbox {
		t.Fatal("applyPolicyRefs cleared DisableCommandSandbox; defaults without the opt-out must leave it untouched")
	}
}

// TestBuildTriggerRunKubernetesAdmin covers the admin-set trigger option that
// grants created runs cluster-admin RBAC and platform introspection tools.
func TestBuildTriggerRunKubernetesAdmin(t *testing.T) {
	run := BuildTriggerRun(TriggerRunSpec{
		RunName:     "run-1",
		Namespace:   "default",
		TriggerKind: "GitHubRepository",
		TriggerName: "payments",
		Defaults: triggersv1alpha1.AgentRunDefaults{
			RepoURL:         "https://github.com/example/repo.git",
			Model:           "gpt-5.4",
			KubernetesAdmin: true,
		},
	})
	if !run.Spec.KubernetesAdmin {
		t.Fatal("Spec.KubernetesAdmin = false, want true (copied from trigger defaults)")
	}
}

// TestBuildTriggerRunKubernetesAdminOffByDefault pins the fail-safe default:
// triggers that do not opt in create runs without cluster-admin RBAC.
func TestBuildTriggerRunKubernetesAdminOffByDefault(t *testing.T) {
	run := BuildTriggerRun(TriggerRunSpec{
		RunName:     "run-1",
		Namespace:   "default",
		TriggerKind: "GitHubRepository",
		TriggerName: "payments",
		Defaults: triggersv1alpha1.AgentRunDefaults{
			RepoURL: "https://github.com/example/repo.git",
			Model:   "gpt-5.4",
		},
	})
	if run.Spec.KubernetesAdmin {
		t.Fatal("Spec.KubernetesAdmin = true, want false by default")
	}
}

// TestApplyPolicyRefsDoesNotClearKubernetesAdmin ensures defaults without the
// grant never clear a flag already set on the spec by another path.
func TestApplyPolicyRefsDoesNotClearKubernetesAdmin(t *testing.T) {
	spec := platformv1alpha1.AgentRunSpec{KubernetesAdmin: true}
	applyPolicyRefs(&spec, triggersv1alpha1.AgentRunDefaults{})
	if !spec.KubernetesAdmin {
		t.Fatal("applyPolicyRefs cleared KubernetesAdmin; defaults without the grant must leave it untouched")
	}
}

// TestBuildTriggerRunTimeoutSetsMaxRuntime covers defaults.timeout, which
// documents "the maximum duration for created AgentRuns".
func TestBuildTriggerRunTimeoutSetsMaxRuntime(t *testing.T) {
	run := BuildTriggerRun(TriggerRunSpec{
		RunName:     "run-1",
		Namespace:   "default",
		TriggerKind: "Cron",
		TriggerName: "nightly",
		Defaults: triggersv1alpha1.AgentRunDefaults{
			Model:   "gpt-5.4",
			Timeout: metav1.Duration{Duration: 45 * time.Minute},
		},
	})
	if run.Spec.Limits == nil || run.Spec.Limits.MaxRuntime.Duration != 45*time.Minute {
		t.Fatalf("Spec.Limits = %+v, want MaxRuntime 45m from trigger defaults", run.Spec.Limits)
	}
}

// TestBuildTriggerRunNoTimeoutLeavesLimitsUnset pins that a zero timeout does
// not materialize an empty limits block (the controller default applies).
func TestBuildTriggerRunNoTimeoutLeavesLimitsUnset(t *testing.T) {
	run := BuildTriggerRun(TriggerRunSpec{
		RunName:     "run-1",
		Namespace:   "default",
		TriggerKind: "Cron",
		TriggerName: "nightly",
		Defaults:    triggersv1alpha1.AgentRunDefaults{Model: "gpt-5.4"},
	})
	if run.Spec.Limits != nil {
		t.Fatalf("Spec.Limits = %+v, want nil when defaults.timeout is unset", run.Spec.Limits)
	}
}

// TestApplyPolicyRefsKeepsExistingMaxRuntime ensures a limit already present
// on the spec wins over the trigger default.
func TestApplyPolicyRefsKeepsExistingMaxRuntime(t *testing.T) {
	spec := platformv1alpha1.AgentRunSpec{
		Limits: &platformv1alpha1.AgentRunLimits{MaxRuntime: metav1.Duration{Duration: 2 * time.Hour}},
	}
	applyPolicyRefs(&spec, triggersv1alpha1.AgentRunDefaults{Timeout: metav1.Duration{Duration: 30 * time.Minute}})
	if spec.Limits.MaxRuntime.Duration != 2*time.Hour {
		t.Fatalf("MaxRuntime = %s, want existing 2h preserved", spec.Limits.MaxRuntime.Duration)
	}
}
