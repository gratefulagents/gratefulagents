package agentrun

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	"k8s.io/apimachinery/pkg/types"
)

func TestValidateTeamParentAllowsLinearPlanParentBeforeTeamBootstrap(t *testing.T) {
	run := &platformv1alpha1.AgentRun{
		Spec: platformv1alpha1.AgentRunSpec{
			WorkflowMode:  platformv1alpha1.WorkflowModeChat,
			ExecutionMode: platformv1alpha1.ExecutionModeLinear,
		},
	}

	if err := ValidateTeamParent(run); err != nil {
		t.Fatalf("ValidateTeamParent() error = %v", err)
	}
}

func TestRuntimeParentBindingFromEnvFallsBackToCurrentRunForSameParentLifecycle(t *testing.T) {
	t.Setenv("AGENTRUN_CURRENT_NAMESPACE", "engg")
	t.Setenv("AGENTRUN_CURRENT_NAME", "same-parent-run")
	t.Setenv("AGENTRUN_CURRENT_UID", "same-parent-uid")

	binding, ok, err := RuntimeParentBindingFromEnv()
	if err != nil {
		t.Fatalf("RuntimeParentBindingFromEnv() error = %v", err)
	}
	if !ok {
		t.Fatal("RuntimeParentBindingFromEnv() ok = false, want true")
	}
	if binding.Parent.Namespace != "engg" || binding.Parent.Name != "same-parent-run" {
		t.Fatalf("binding.Parent = %#v, want engg/same-parent-run", binding.Parent)
	}
	if binding.ParentUID != "same-parent-uid" {
		t.Fatalf("binding.ParentUID = %q, want same-parent-uid", binding.ParentUID)
	}
}

func TestValidateRuntimeParentUIDAcceptsCurrentRunFallbackBinding(t *testing.T) {
	t.Setenv("AGENTRUN_CURRENT_NAMESPACE", "engg")
	t.Setenv("AGENTRUN_CURRENT_NAME", "same-parent-run")
	t.Setenv("AGENTRUN_CURRENT_UID", "same-parent-uid")

	parent := &platformv1alpha1.AgentRun{}
	parent.UID = types.UID("same-parent-uid")

	if err := ValidateRuntimeParentUID(parent); err != nil {
		t.Fatalf("ValidateRuntimeParentUID() error = %v", err)
	}
}

func TestTeamProductSurfaceAvoidsTmuxRuntimeTerms(t *testing.T) {
	t.Parallel()

	root := repoRootFromThisTest(t)
	forbidden := regexp.MustCompile(`(?i)\b(tmux|tmux-session|mailbox|worktree|pane|panes)\b`)
	tests := []struct {
		name  string
		path  string
		start string
		end   string
	}{
		{name: "agentrun api contract", path: "api/platform/v1alpha1/agentrun_types.go"},
		{name: "shared team service contract", path: "internal/agentrun/team_service.go"},
		{name: "dashboard team rpc surface", path: "internal/dashboard/server_team.go"},
		{name: "team proto surface", path: "rpc/platform/service.proto", start: "message TeamParentRef", end: "message WaitForTeamRunChangeResponse"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			content := readRepoFile(t, root, tc.path)
			if tc.start != "" || tc.end != "" {
				content = sliceBetween(t, content, tc.start, tc.end)
			}
			if match := forbidden.FindString(content); match != "" {
				t.Fatalf("%s contains forbidden team-runtime term %q", tc.path, match)
			}
		})
	}
}

func repoRootFromThisTest(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller(0) = false")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

func readRepoFile(t *testing.T, root, rel string) string {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", rel, err)
	}
	return string(content)
}

func sliceBetween(t *testing.T, content, start, end string) string {
	t.Helper()
	startIdx := 0
	if start != "" {
		startIdx = strings.Index(content, start)
		if startIdx < 0 {
			t.Fatalf("start marker %q not found", start)
		}
	}
	endIdx := len(content)
	if end != "" {
		endIdx = strings.Index(content, end)
		if endIdx < 0 {
			t.Fatalf("end marker %q not found", end)
		}
	}
	if endIdx < startIdx {
		t.Fatalf("invalid slice markers: start=%q end=%q", start, end)
	}
	return content[startIdx:endIdx]
}
