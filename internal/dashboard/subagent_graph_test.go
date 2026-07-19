package dashboard

import (
	"strings"
	"testing"
	"unicode/utf8"

	"google.golang.org/protobuf/proto"

	"github.com/gratefulagents/gratefulagents/rpc/platform"
)

func TestBuildSubagentGraphIgnoresLLMAttemptOnlySpawnBuckets(t *testing.T) {
	entries := []*platform.ActivityEntry{
		{
			TimestampUnix:          1,
			Type:                   "llm_attempt",
			TaskId:                 "call_spawn_parent",
			LlmAttemptId:           "attempt-1",
			LlmAttemptInputTokens:  120,
			LlmAttemptOutputTokens: 48,
			LlmAttemptTokensKnown:  true,
		},
		{
			TimestampUnix: 2,
			Type:          "tool_use",
			Tool:          "agent_analyst",
			ToolUseId:     "call_spawn_parent",
			Message:       "Investigate the realtime lag",
		},
		{
			TimestampUnix:       3,
			Type:                "subagent_started",
			TaskId:              "task_analyst",
			ToolUseId:           "call_spawn_parent",
			SubagentType:        "analyst",
			SubagentDescription: "Investigate the realtime lag",
			SubagentPrompt:      "Investigate the realtime lag",
			SubagentStatus:      "started",
		},
		{
			TimestampUnix:       4,
			Type:                "subagent_completed",
			TaskId:              "task_analyst",
			SubagentType:        "analyst",
			SubagentDescription: "Investigation complete",
			SubagentStatus:      "completed",
			SubagentToolCount:   4,
			SubagentTotalTokens: 168,
			SubagentDurationMs:  900,
		},
	}

	graph := BuildSubagentGraph(entries, "run-1")
	if graph == nil {
		t.Fatal("BuildSubagentGraph returned nil")
	}

	var taskIDs []string
	for _, node := range graph.Nodes {
		if node.Kind != "root" {
			taskIDs = append(taskIDs, node.TaskId)
		}
		if node.TaskId == "call_spawn_parent" {
			t.Fatalf("unexpected phantom subagent node for spawn call id: %+v", node)
		}
	}

	if len(taskIDs) != 1 || taskIDs[0] != "task_analyst" {
		t.Fatalf("graph task ids = %#v, want [task_analyst]", taskIDs)
	}
}

func TestBuildSubagentGraphCombinesSparseLiveMetrics(t *testing.T) {
	entries := []*platform.ActivityEntry{
		{
			TimestampUnix:       100,
			Type:                "subagent_started",
			TaskId:              "task_live",
			ToolUseId:           "call_live",
			SubagentType:        "executor",
			SubagentDescription: "Implement the feature",
			SubagentStatus:      "started",
			SubagentModel:       "gpt-5.4",
		},
		{
			TimestampUnix:       101,
			Type:                "subagent_progress",
			TaskId:              "task_live",
			SubagentType:        "executor",
			SubagentStatus:      "running",
			SubagentTotalTokens: 420,
			SubagentCostUsd:     0.0123,
			SubagentToolCount:   2,
		},
		{
			TimestampUnix:       102,
			Type:                "subagent_progress",
			TaskId:              "task_live",
			SubagentType:        "executor",
			SubagentStatus:      "running",
			SubagentCurrentStep: "editing",
		},
	}

	node := findGraphNodeByTaskID(BuildSubagentGraph(entries, "run-1"), "task_live")
	if node == nil {
		t.Fatal("missing live subagent node")
	}
	if node.Model != "gpt-5.4" || node.TotalTokens != 420 || node.CostUsd != 0.0123 || node.ToolCount != 2 {
		t.Fatalf("sparse live metrics were not combined: %+v", node)
	}
	if node.DurationMs != 0 || node.TimestampUnix != 100 {
		t.Fatalf("live node timing fields = duration %d, timestamp %d", node.DurationMs, node.TimestampUnix)
	}
}

func TestBuildSubagentGraphMarksCancelledSubagentTerminal(t *testing.T) {
	entries := []*platform.ActivityEntry{
		{
			TimestampUnix:       1,
			Type:                "subagent_started",
			TaskId:              "task_reviewer",
			ToolUseId:           "call_reviewer",
			SubagentType:        "reviewer",
			SubagentDescription: "Review the implementation",
			SubagentStatus:      "started",
		},
		{
			TimestampUnix:       2,
			Type:                "subagent_completed",
			TaskId:              "task_reviewer",
			ToolUseId:           "call_reviewer",
			SubagentType:        "reviewer",
			SubagentDescription: "agent \"reviewer\" cancelled: context canceled",
			SubagentStatus:      "cancelled",
			SubagentStopReason:  "cancelled",
		},
	}

	graph := BuildSubagentGraph(entries, "run-1")
	node := findGraphNodeByTaskID(graph, "task_reviewer")
	if node == nil {
		t.Fatalf("missing graph node for cancelled task: %+v", graph)
	}
	if node.Status != "cancelled" {
		t.Fatalf("cancelled task status = %q, want cancelled", node.Status)
	}
}

func TestBuildSubagentGraphMarksCancelledFallbackClusterTerminal(t *testing.T) {
	entries := []*platform.ActivityEntry{
		{
			TimestampUnix:       1,
			Type:                "subagent_started",
			ToolUseId:           "call_reviewer",
			SubagentType:        "reviewer",
			SubagentDescription: "Review the implementation",
			SubagentStatus:      "started",
		},
		{
			TimestampUnix:       2,
			Type:                "subagent_completed",
			ToolUseId:           "call_reviewer",
			SubagentType:        "reviewer",
			SubagentDescription: "agent \"reviewer\" cancelled: context canceled",
			SubagentStatus:      "cancelled",
			SubagentStopReason:  "cancelled",
		},
	}

	graph := BuildSubagentGraph(entries, "run-1")
	var cancelledNode *platform.SubagentGraphNode
	for _, node := range graph.Nodes {
		if node.Kind != "root" {
			cancelledNode = node
			break
		}
	}
	if cancelledNode == nil {
		t.Fatalf("missing fallback graph node for cancelled task: %+v", graph)
	}
	if cancelledNode.Status != "cancelled" {
		t.Fatalf("cancelled fallback status = %q, want cancelled", cancelledNode.Status)
	}
}

func TestBuildSubagentGraphAddsDependencyEdgesAndProgressSnapshot(t *testing.T) {
	entries := []*platform.ActivityEntry{
		{
			TimestampUnix:       1,
			Type:                "subagent_started",
			TaskId:              "task_explore",
			ToolUseId:           "call_explore",
			SubagentType:        "explore",
			SubagentDescription: "Explore the package",
			SubagentStatus:      "started",
		},
		{
			TimestampUnix:       2,
			Type:                "subagent_completed",
			TaskId:              "task_explore",
			SubagentType:        "explore",
			SubagentDescription: "Exploration complete",
			SubagentStatus:      "completed",
		},
		{
			TimestampUnix:             3,
			Type:                      "subagent_progress",
			TaskId:                    "task_execute",
			SubagentType:              "executor",
			SubagentDescription:       "Waiting for exploration",
			SubagentStatus:            "waiting",
			SubagentDependsOn:         []string{"task_explore"},
			SubagentWaitingOn:         []string{"task_explore"},
			SubagentCurrentStep:       "dependency_wait",
			SubagentLastTool:          "read",
			SubagentFilesWritten:      2,
			SubagentMessagesReceived:  1,
			SubagentLastParentMessage: "Narrow the patch to the adapter.",
		},
	}

	graph := BuildSubagentGraph(entries, "run-1")
	execNode := findGraphNodeByTaskID(graph, "task_execute")
	if execNode == nil {
		t.Fatalf("missing graph node for dependent task: %+v", graph)
	}
	if got := execNode.DependsOn; len(got) != 1 || got[0] != "task_explore" {
		t.Fatalf("depends_on = %#v, want [task_explore]", got)
	}
	if execNode.CurrentStep != "dependency_wait" || execNode.LastTool != "read" {
		t.Fatalf("progress snapshot not preserved: %+v", execNode)
	}

	foundDependencyEdge := false
	for _, edge := range graph.Edges {
		if edge.From == "task:task_explore" && edge.To == "task:task_execute" && edge.Kind == "depends-on" {
			foundDependencyEdge = true
			break
		}
	}
	if !foundDependencyEdge {
		t.Fatalf("missing dependency edge in graph: %+v", graph.Edges)
	}
}

func TestBuildSubagentGraphSkipsDependencyEdgeDuplicatingSpawn(t *testing.T) {
	// A child subagent that both was spawned by a parent inline subagent AND
	// records a depends-on back to that same parent must not produce a
	// redundant dashed depends-on edge layered on top of the spawn edge.
	entries := []*platform.ActivityEntry{
		{
			TimestampUnix: 1,
			Type:          "tool_use",
			Tool:          "agent_orchestrator",
			ToolUseId:     "call_parent",
			Message:       "Coordinate the work",
		},
		{
			TimestampUnix:       2,
			Type:                "subagent_started",
			TaskId:              "task_child",
			ToolUseId:           "call_child",
			ParentCallId:        "call_parent",
			SubagentType:        "worker",
			SubagentDescription: "Do the work",
			SubagentStatus:      "started",
			SubagentDependsOn:   []string{"call_parent"},
		},
		{
			TimestampUnix:       3,
			Type:                "subagent_completed",
			TaskId:              "task_child",
			ParentCallId:        "call_parent",
			SubagentType:        "worker",
			SubagentDescription: "Done",
			SubagentStatus:      "completed",
			SubagentDependsOn:   []string{"call_parent"},
		},
	}

	graph := BuildSubagentGraph(entries, "run-1")
	childNode := findGraphNodeByTaskID(graph, "task_child")
	if childNode == nil {
		t.Fatalf("missing graph node for child task: %+v", graph)
	}

	for _, edge := range graph.Edges {
		if edge.Kind != "depends-on" {
			continue
		}
		// A depends-on edge between the same pair as the structural spawn edge
		// is the redundant overlay we want to suppress.
		structuralFrom := childNode.ParentId
		if edge.To == childNode.Id && edge.From == structuralFrom {
			t.Fatalf("redundant depends-on edge duplicates spawn edge: %+v", edge)
		}
	}
}

func TestBuildSubagentGraphDeduplicatesRepeatedDependencyEdges(t *testing.T) {
	entries := []*platform.ActivityEntry{
		{
			TimestampUnix:       1,
			Type:                "subagent_started",
			TaskId:              "task_a",
			ToolUseId:           "call_a",
			SubagentType:        "explore",
			SubagentDescription: "A",
			SubagentStatus:      "started",
		},
		{
			TimestampUnix:       2,
			Type:                "subagent_completed",
			TaskId:              "task_a",
			SubagentType:        "explore",
			SubagentDescription: "A done",
			SubagentStatus:      "completed",
		},
		{
			TimestampUnix:       3,
			Type:                "subagent_progress",
			TaskId:              "task_b",
			SubagentType:        "executor",
			SubagentDescription: "B",
			SubagentStatus:      "waiting",
			SubagentDependsOn:   []string{"task_a"},
		},
		{
			TimestampUnix:       4,
			Type:                "subagent_progress",
			TaskId:              "task_b",
			SubagentType:        "executor",
			SubagentDescription: "B still waiting",
			SubagentStatus:      "waiting",
			SubagentDependsOn:   []string{"task_a"},
		},
	}

	graph := BuildSubagentGraph(entries, "run-1")
	count := 0
	for _, edge := range graph.Edges {
		if edge.Kind == "depends-on" && edge.From == "task:task_a" && edge.To == "task:task_b" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("depends-on edge count = %d, want exactly 1 (deduplicated)", count)
	}
}

func findGraphNodeByTaskID(graph *platform.SubagentGraph, taskID string) *platform.SubagentGraphNode {
	if graph == nil {
		return nil
	}
	for _, node := range graph.Nodes {
		if node.TaskId == taskID {
			return node
		}
	}
	return nil
}

// Registry task snapshots carry lifecycle noise ("spawned", "dependency_wait")
// in the description field; the node title must come from the task's
// objective (prompt) instead, and never regress to a status word.
func TestBuildSubagentGraphTitlesFromPromptNotLifecycleNoise(t *testing.T) {
	entries := []*platform.ActivityEntry{
		{
			TimestampUnix:       1,
			Type:                "subagent_progress",
			TaskId:              "task_res",
			SubagentType:        "researcher",
			SubagentDescription: "spawned",
			SubagentPrompt:      "Fetch the weather for Tokyo\nUse wttr.in as the source.",
			SubagentStatus:      "running",
		},
		{
			TimestampUnix:       2,
			Type:                "subagent_completed",
			TaskId:              "task_res",
			SubagentType:        "researcher",
			SubagentDescription: "completed",
			SubagentStatus:      "completed",
		},
	}

	graph := BuildSubagentGraph(entries, "run-1")
	node := findGraphNodeByTaskID(graph, "task_res")
	if node == nil {
		t.Fatalf("missing node: %+v", graph)
	}
	if node.Label != "Fetch the weather for Tokyo" {
		t.Fatalf("Label = %q, want prompt first line", node.Label)
	}
	if node.Status != "completed" {
		t.Fatalf("Status = %q, want completed", node.Status)
	}
}

// waiting_on shrinks as dependencies finish and is cleared on terminal
// snapshots; the node must reflect the latest snapshot, not the union of all
// snapshots (which froze nodes at "waiting N" forever).
func TestBuildSubagentGraphWaitingOnUsesLatestSnapshot(t *testing.T) {
	entries := []*platform.ActivityEntry{
		{
			TimestampUnix:     1,
			Type:              "subagent_progress",
			TaskId:            "task_writer",
			SubagentType:      "writer",
			SubagentStatus:    "waiting",
			SubagentDependsOn: []string{"task_a", "task_b"},
			SubagentWaitingOn: []string{"task_a", "task_b"},
		},
		{
			TimestampUnix:     2,
			Type:              "subagent_progress",
			TaskId:            "task_writer",
			SubagentType:      "writer",
			SubagentStatus:    "waiting",
			SubagentDependsOn: []string{"task_a", "task_b"},
			SubagentWaitingOn: []string{"task_b"},
		},
		{
			TimestampUnix:     3,
			Type:              "subagent_completed",
			TaskId:            "task_writer",
			SubagentType:      "writer",
			SubagentStatus:    "completed",
			SubagentDependsOn: []string{"task_a", "task_b"},
		},
	}

	graph := BuildSubagentGraph(entries, "run-1")
	node := findGraphNodeByTaskID(graph, "task_writer")
	if node == nil {
		t.Fatalf("missing node: %+v", graph)
	}
	if len(node.WaitingOn) != 0 {
		t.Fatalf("WaitingOn = %#v, want empty after terminal snapshot", node.WaitingOn)
	}
	if len(node.DependsOn) != 2 {
		t.Fatalf("DependsOn = %#v, want both dependencies", node.DependsOn)
	}
}

// A prompt whose 90-byte boundary lands inside a multi-byte rune must not
// produce invalid UTF-8 in the node label/description — proto marshaling
// rejects the whole response otherwise.
func TestBuildSubagentGraphTitleTruncationIsUTF8Safe(t *testing.T) {
	prompt := strings.Repeat("é", 200) // 2 bytes per rune: any byte slice at 89 splits a rune
	entries := []*platform.ActivityEntry{
		{
			TimestampUnix:       1,
			Type:                "subagent_progress",
			TaskId:              "task_unicode",
			SubagentType:        "researcher",
			SubagentDescription: "spawned",
			SubagentPrompt:      prompt,
			SubagentStatus:      "running",
		},
	}

	graph := BuildSubagentGraph(entries, "run-1")
	node := findGraphNodeByTaskID(graph, "task_unicode")
	if node == nil {
		t.Fatalf("missing node: %+v", graph)
	}
	if !utf8.ValidString(node.Label) || !utf8.ValidString(node.Description) {
		t.Fatalf("invalid UTF-8 in node fields: label=%q description=%q", node.Label, node.Description)
	}
	if _, err := proto.Marshal(graph); err != nil {
		t.Fatalf("graph must marshal cleanly: %v", err)
	}
	if got := []rune(node.Label); len(got) != 90 {
		t.Fatalf("label rune length = %d, want 90 (89 + ellipsis)", len(got))
	}
}
