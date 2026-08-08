package main

import (
	"strings"
	"testing"

	"github.com/gratefulagents/gratefulagents/internal/tools"
	agent "github.com/gratefulagents/sdk/pkg/agentsdk"
)

func TestBuildAgentGuideIncludesCompactTaskPacketGuidance(t *testing.T) {
	t.Parallel()

	a := &agent.Agent{}
	specialists := map[string]*agent.Agent{
		"executor": {Name: "executor", HandoffDescription: "Implement a bounded change"},
	}

	guide := agent.BuildDelegationGuide(a, specialists)
	wants := []string{
		`- executor: Implement a bounded change`,
		`share_parent_context=true`,
		`sharing increases token usage and can disclose unrelated or sensitive context`,
		`compact, self-contained task packet`,
		`Do NOT send the same large background block to every task if only one sub-agent needs it.`,
		`Planning/review/design tasks usually need the broader objective plus the current findings or draft they are building on.`,
		`Execution/test/fix tasks usually need the concrete slice they own plus adjacent contracts they must preserve.`,
	}
	for _, want := range wants {
		if !strings.Contains(guide, want) {
			t.Fatalf("buildAgentGuide() missing %q\nGuide:\n%s", want, guide)
		}
	}
}

func TestSubAgentToolExposesOptInParentContextSharing(t *testing.T) {
	t.Parallel()

	scheduler := agent.NewSubAgentScheduler(agent.SubAgentSchedulerConfig{})
	var subagentTool agent.Tool
	for _, tool := range agent.BuildSubAgentTaskTools(scheduler, "executor") {
		if tool.Name() == "subagent" {
			subagentTool = tool
			break
		}
	}
	if subagentTool == nil {
		t.Fatal("SDK runtime did not expose the subagent tool")
	}

	schema := string(subagentTool.InputSchema())
	for _, want := range []string{
		`"share_parent_context"`,
		`Defaults to false`,
		`increases token usage`,
		`may expose unrelated or sensitive context`,
	} {
		if !strings.Contains(schema, want) {
			t.Fatalf("subagent schema missing %q\nSchema:\n%s", want, schema)
		}
	}
}

func TestFilterToolsByAccessReadOnlyAndAnalysis(t *testing.T) {
	t.Parallel()

	allTools := []agent.Tool{
		&agent.FunctionTool{ToolName: "Read", ReadOnly: true},
		&agent.FunctionTool{ToolName: "Edit", ReadOnly: false},
		tools.NewRegistry(t.TempDir()).Get("Bash"),
	}

	for _, access := range []string{"read-only", "analysis"} {
		t.Run(access, func(t *testing.T) {
			filtered := agent.FilterToolsByAccess(allTools, access)
			if len(filtered) != 2 {
				t.Fatalf("len(filtered) = %d, want 2", len(filtered))
			}

			if toolByName(filtered, "Read") == nil {
				t.Fatalf("expected read-only tool to remain available")
			}
			if toolByName(filtered, "Edit") != nil {
				t.Fatalf("expected mutating tool to be removed")
			}

			bash := toolByName(filtered, "Bash")
			if bash == nil {
				t.Fatalf("expected Bash to remain available in read-only form")
			}
			if !bash.IsReadOnly() {
				t.Fatalf("expected Bash to be wrapped as read-only")
			}
		})
	}
}

func TestFilterToolsByAccessExecutionAndFull(t *testing.T) {
	t.Parallel()

	allTools := []agent.Tool{
		&agent.FunctionTool{ToolName: "Read", ReadOnly: true},
		&agent.FunctionTool{ToolName: "Edit", ReadOnly: false},
		tools.NewRegistry(t.TempDir()).Get("Bash"),
	}

	for _, access := range []string{"execution", "full"} {
		t.Run(access, func(t *testing.T) {
			filtered := agent.FilterToolsByAccess(allTools, access)
			if len(filtered) != len(allTools) {
				t.Fatalf("len(filtered) = %d, want %d", len(filtered), len(allTools))
			}

			if toolByName(filtered, "Edit") == nil {
				t.Fatalf("expected mutating tool to remain available")
			}

			bash := toolByName(filtered, "Bash")
			if bash == nil {
				t.Fatalf("expected Bash to remain available")
			}
			if bash.IsReadOnly() {
				t.Fatalf("expected Bash to remain unrestricted")
			}
		})
	}
}

func toolByName(tools []agent.Tool, name string) agent.Tool {
	for _, tool := range tools {
		if tool.Name() == name {
			return tool
		}
	}
	return nil
}
