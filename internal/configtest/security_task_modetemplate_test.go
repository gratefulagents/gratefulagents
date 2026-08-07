package configtest

import (
	"slices"
	"strings"
	"testing"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
)

// TestSecurityScanTaskModeTemplateAsset validates the bootstrap ModeTemplate
// for single deterministic security workflow tasks: a linear, read-only run
// that executes exactly one assigned task and reports through the finding
// tools and submit_task_output.
func TestSecurityScanTaskModeTemplateAsset(t *testing.T) {
	t.Parallel()

	var mode platformv1alpha1.ModeTemplate
	readBootstrapAsset(t, "modetemplates", "security-scan-task", &mode)

	if mode.Name != "security-scan-task" || mode.Spec.Name != "security-scan-task" {
		t.Fatalf("mode template name = %q/%q, want security-scan-task", mode.Name, mode.Spec.Name)
	}
	if !mode.Spec.Autonomous {
		t.Error("scan task mode must be autonomous")
	}
	// Scanned repositories are untrusted input: task runs must never be able
	// to modify the workspace or push code.
	if mode.Spec.PermissionMode != platformv1alpha1.PermissionModeReadOnly {
		t.Errorf("permissionMode = %q, want read-only", mode.Spec.PermissionMode)
	}
	// A task run is one focused, linear unit of work: the workflow controller
	// owns fan-out, so the run itself must not orchestrate sub-agents.
	if mode.Spec.Constraints == nil {
		t.Fatal("scan task mode must declare constraints")
	}
	if got := mode.Spec.Constraints.MaxConcurrentSubAgents; got != 1 {
		t.Errorf("maxConcurrentSubAgents = %d, want 1 (no sub-agent fan-out)", got)
	}
	if mode.Spec.Constraints.MaxTurns <= 0 {
		t.Error("scan task mode must bound maxTurns")
	}
	if got := mode.Spec.Constraints.MaxRetries; got != 1 {
		t.Errorf("maxRetries = %d, want 1", got)
	}
	// The finding and typed-output tools write platform scan/run state only,
	// so they must survive the read-only clamp or the task has no outputs.
	wantTools := []string{
		"report_security_finding",
		"update_security_finding",
		"submit_security_scan_report",
		"submit_task_output",
	}
	for _, tool := range wantTools {
		if !slices.Contains(mode.Spec.AllowedMutatingTools, tool) {
			t.Errorf("allowedMutatingTools must include %q, got %v", tool, mode.Spec.AllowedMutatingTools)
		}
	}
	for _, marker := range []string{"report_security_finding", "submit_task_output", "file:line", "one"} {
		if !strings.Contains(mode.Spec.Instructions, marker) {
			t.Errorf("scan task mode instructions must mention %q", marker)
		}
	}
}
