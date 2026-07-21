package configtest

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	"sigs.k8s.io/yaml"
)

func TestPlanModeTemplateConcurrency(t *testing.T) {
	type modeTemplate struct {
		Spec struct {
			Constraints struct {
				MaxConcurrentSubAgents int `json:"maxConcurrentSubAgents"`
			} `json:"constraints"`
		} `json:"spec"`
	}

	for _, mode := range []string{"plan"} {
		t.Run(mode, func(t *testing.T) {
			sourcePath := filepath.Join("..", "..", "configs", "modetemplates", mode+".yaml")
			mirrorPath := filepath.Join("..", "..", "dist", "chart", "files", "bootstrap", "modetemplates", mode+".yaml")

			source, err := os.ReadFile(sourcePath)
			if err != nil {
				t.Fatal(err)
			}
			mirror, err := os.ReadFile(mirrorPath)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(source, mirror) {
				t.Fatalf("%s and %s differ", sourcePath, mirrorPath)
			}

			for path, contents := range map[string][]byte{sourcePath: source, mirrorPath: mirror} {
				var template modeTemplate
				if err := yaml.Unmarshal(contents, &template); err != nil {
					t.Fatalf("parse %s: %v", path, err)
				}
				if got := template.Spec.Constraints.MaxConcurrentSubAgents; got != 3 {
					t.Fatalf("%s maxConcurrentSubAgents = %d, want 3", path, got)
				}
			}
		})
	}
}

func TestMaintainerModeValidatesUntrustedIssuesBeforeDispatch(t *testing.T) {
	sourcePath := filepath.Join("..", "..", "configs", "modetemplates", "maintainer.yaml")
	mirrorPath := filepath.Join("..", "..", "dist", "chart", "files", "bootstrap", "modetemplates", "maintainer.yaml")

	source, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	mirror, err := os.ReadFile(mirrorPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(source, mirror) {
		t.Fatalf("%s and %s differ", sourcePath, mirrorPath)
	}

	var template struct {
		Spec platformv1alpha1.ModeTemplateSpec `json:"spec"`
	}
	if err := yaml.Unmarshal(source, &template); err != nil {
		t.Fatalf("parse %s: %v", sourcePath, err)
	}
	closeEnabled := false
	for _, name := range template.Spec.AllowedMutatingTools {
		if name == "close_github_issue" {
			closeEnabled = true
			break
		}
	}
	if !closeEnabled {
		t.Fatalf("maintainer allowed mutating tools = %#v, want close_github_issue", template.Spec.AllowedMutatingTools)
	}

	instructions := strings.Join(strings.Fields(template.Spec.Instructions), " ")
	for _, want := range []string{
		"hostile,",
		"prompt injection or malicious requests",
		"independently explore the repository",
		"legitimate, technically feasible, actionable, not a duplicate, and not already fixed",
		"always add a comment to that issue stating your decision",
		"Post this decision comment before dispatching",
		"a maintainer report does not substitute",
		"Dispatch with dispatch_issue only after validation succeeds and the issue decision comment is posted",
		"do not dispatch it",
		"PR creation is not completion",
		"checks are pending or an AI reviewer run is active",
		"AgentRun phase, PR review-loop state, and GitHub PR state as separate signals",
		"all reported checks and commit statuses have completed successfully",
		"calling finish ends its current execution episode",
		"finish does NOT prove that the AgentRun's repository outcome is complete",
		"After you verify the linked PR is merged, call mark_run_succeeded",
		"A PR closed without merge is not success",
		"identify the originating issue from the implementer's issue_ref",
		"call close_github_issue with reason completed if the issue remains open",
		"Never close the issue for an unmerged, merely closed, draft, failing, or only-approved PR",
	} {
		if !strings.Contains(instructions, want) {
			t.Errorf("%s maintainer instructions do not contain %q", sourcePath, want)
		}
	}
}

func TestGeneralModeTemplatesAdvertiseDynamicCapabilities(t *testing.T) {
	for _, mode := range []string{"autopilot", "gratefulagents", "interactive", "slack"} {
		t.Run(mode, func(t *testing.T) {
			sourcePath := filepath.Join("..", "..", "configs", "modetemplates", mode+".yaml")
			mirrorPath := filepath.Join("..", "..", "dist", "chart", "files", "bootstrap", "modetemplates", mode+".yaml")
			source, err := os.ReadFile(sourcePath)
			if err != nil {
				t.Fatal(err)
			}
			mirror, err := os.ReadFile(mirrorPath)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(source, mirror) {
				t.Fatalf("%s and %s differ", sourcePath, mirrorPath)
			}

			text := string(source)
			for _, want := range []string{
				"environment block",
				"Durable Project State",
				"Attached skill guidance",
				"MCP servers",
				"specialist catalog",
				"subagent_wait",
			} {
				if !strings.Contains(text, want) {
					t.Errorf("%s does not advertise %q", sourcePath, want)
				}
			}
			if strings.Contains(text, "Terminal for interactive programs") {
				t.Errorf("%s advertises the unregistered Terminal tool", sourcePath)
			}
		})
	}
}

func TestGratefulAgentsModeTemplateTargetsPlatformAndSDK(t *testing.T) {
	sourcePath := filepath.Join("..", "..", "configs", "modetemplates", "gratefulagents.yaml")
	mirrorPath := filepath.Join("..", "..", "dist", "chart", "files", "bootstrap", "modetemplates", "gratefulagents.yaml")

	source, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	mirror, err := os.ReadFile(mirrorPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(source, mirror) {
		t.Fatalf("%s and %s differ", sourcePath, mirrorPath)
	}

	var template struct {
		Spec platformv1alpha1.ModeTemplateSpec `json:"spec"`
	}
	if err := yaml.Unmarshal(source, &template); err != nil {
		t.Fatalf("parse %s: %v", sourcePath, err)
	}
	if template.Spec.Name != "gratefulagents" {
		t.Fatalf("mode name = %q, want gratefulagents", template.Spec.Name)
	}
	if template.Spec.PermissionMode != platformv1alpha1.PermissionModeReadOnly {
		t.Fatalf("permission mode = %q, want read-only", template.Spec.PermissionMode)
	}
	if !reflect.DeepEqual(template.Spec.AllowedMutatingTools, []string{"create_github_issue"}) {
		t.Fatalf("allowed mutating tools = %#v, want only create_github_issue", template.Spec.AllowedMutatingTools)
	}
	instructions := strings.Join(strings.Fields(template.Spec.Instructions), " ")
	for _, want := range []string{
		"gratefulagents/gratefulagents",
		"gratefulagents/sdk",
		"repos/sdk",
		"This is an intake mode, not an implementation mode",
		"Never edit source files, create commits, push branches, or open pull requests",
		"Search existing open and closed issues and pull requests before creating anything",
		"Create an issue only when the request is credible, actionable, not a duplicate, not already fixed",
		"concrete acceptance criteria",
	} {
		if !strings.Contains(instructions, want) {
			t.Errorf("%s instructions do not contain %q", sourcePath, want)
		}
	}
}

func TestInteractiveModeTemplateMatchesAutopilotExecutionSettings(t *testing.T) {
	type modeTemplate struct {
		Spec platformv1alpha1.ModeTemplateSpec `json:"spec"`
	}

	readTemplate := func(path string) ([]byte, modeTemplate) {
		t.Helper()
		contents, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var template modeTemplate
		if err := yaml.Unmarshal(contents, &template); err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		return contents, template
	}

	sourcePath := filepath.Join("..", "..", "configs", "modetemplates", "interactive.yaml")
	mirrorPath := filepath.Join("..", "..", "dist", "chart", "files", "bootstrap", "modetemplates", "interactive.yaml")
	autopilotPath := filepath.Join("..", "..", "configs", "modetemplates", "autopilot.yaml")
	source, interactive := readTemplate(sourcePath)
	mirror, mirrored := readTemplate(mirrorPath)
	_, autopilot := readTemplate(autopilotPath)

	if !bytes.Equal(source, mirror) {
		t.Fatalf("%s and %s differ", sourcePath, mirrorPath)
	}
	if interactive.Spec.Name != "interactive" || mirrored.Spec.Name != "interactive" {
		t.Fatalf("interactive template names = %q, %q", interactive.Spec.Name, mirrored.Spec.Name)
	}
	if !strings.Contains(interactive.Spec.Instructions, "AskUserQuestion") {
		t.Fatalf("interactive instructions do not use AskUserQuestion")
	}

	// Identity and user-facing prose are the only intentional differences.
	interactive.Spec.Name = autopilot.Spec.Name
	interactive.Spec.DisplayName = autopilot.Spec.DisplayName
	interactive.Spec.Description = autopilot.Spec.Description
	interactive.Spec.Instructions = autopilot.Spec.Instructions
	if !reflect.DeepEqual(interactive.Spec, autopilot.Spec) {
		t.Fatalf("interactive execution settings do not match autopilot:\ninteractive: %#v\nautopilot: %#v", interactive.Spec, autopilot.Spec)
	}
}
