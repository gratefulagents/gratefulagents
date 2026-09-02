package main

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	agent "github.com/gratefulagents/sdk/pkg/agentsdk"
	agentpolicy "github.com/gratefulagents/sdk/pkg/agentsdk/policy"
)

func namedTool(name string) agent.Tool { return &agent.FunctionTool{ToolName: name} }

func TestWaiterResultReportsChange(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		content string
		want    bool
	}{
		{"controller change", `{"changed":true,"migration_mode":"Controller","work_item_changes":[{"name":"mwi-a"}]}`, true},
		{"controller timeout", `{"changed":false,"timed_out":true,"migration_mode":"Controller","work_item_changes":null}`, false},
		{"controller reconnect without changes", `{"changed":false,"migration_mode":"Controller","reconnect_required":true}`, false},
		{"legacy change", `{"changed":true,"migration_mode":"Legacy","changed_issues":[1]}`, true},
		{"not json", `context canceled`, false},
	}
	for _, tc := range cases {
		if got := waiterResultReportsChange(tc.content); got != tc.want {
			t.Errorf("%s: waiterResultReportsChange() = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestStandingWorkspaceRefreshHooksRateLimitsAndFilters(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	h := newStandingWorkspaceRefreshHooks("/tmp/repo", "main")
	h.now = func() time.Time { return now }
	h.refresh = func(_ context.Context, repoDir, baseBranch string) error {
		if repoDir != "/tmp/repo" || baseBranch != "main" {
			t.Errorf("refresh(%q, %q)", repoDir, baseBranch)
		}
		calls.Add(1)
		return nil
	}
	waiter := namedTool("wait_for_repo_events")
	changed := agent.ToolResult{Content: `{"changed":true,"migration_mode":"Controller","work_item_changes":[{"name":"mwi-a"}]}`}

	h.OnToolEnd(nil, nil, namedTool("get_fleet_runs"), agent.ToolCallData{}, changed)
	if calls.Load() != 0 {
		t.Fatalf("unrelated tool triggered refresh")
	}
	h.OnToolEnd(nil, nil, waiter, agent.ToolCallData{}, agent.ToolResult{Content: changed.Content, IsError: true})
	if calls.Load() != 0 {
		t.Fatalf("error result triggered refresh")
	}
	h.OnToolEnd(nil, nil, waiter, agent.ToolCallData{}, agent.ToolResult{Content: `{"changed":false,"timed_out":true,"migration_mode":"Controller"}`})
	if calls.Load() != 0 {
		t.Fatalf("timeout triggered refresh")
	}
	h.OnToolEnd(nil, nil, waiter, agent.ToolCallData{}, changed)
	if calls.Load() != 1 {
		t.Fatalf("first change: refresh calls = %d, want 1", calls.Load())
	}
	now = now.Add(30 * time.Second)
	h.OnToolEnd(nil, nil, waiter, agent.ToolCallData{}, changed)
	if calls.Load() != 1 {
		t.Fatalf("within interval: refresh calls = %d, want 1", calls.Load())
	}
	now = now.Add(standingWorkspaceRefreshInterval)
	h.OnToolEnd(nil, nil, waiter, agent.ToolCallData{}, changed)
	if calls.Load() != 2 {
		t.Fatalf("after interval: refresh calls = %d, want 2", calls.Load())
	}
}

func TestStandingWorkspaceRefreshHooksSurvivesRefreshError(t *testing.T) {
	t.Parallel()
	h := newStandingWorkspaceRefreshHooks("/tmp/repo", "main")
	h.refresh = func(context.Context, string, string) error { return errors.New("remote unavailable") }
	h.OnToolEnd(nil, nil, namedTool("wait_for_repo_events"), agent.ToolCallData{}, agent.ToolResult{Content: `{"changed":true,"migration_mode":"Controller","work_item_changes":[{}]}`})
	if h.lastRefresh.IsZero() {
		t.Fatal("failed refresh should still consume the rate-limit slot")
	}
}

func TestSkipReadOnlyCheckpointRestore(t *testing.T) {
	t.Parallel()
	checkpoint := &workspaceCheckpointManifest{}
	cases := []struct {
		name string
		cfg  *runConfig
		want bool
	}{
		{"nil", nil, false},
		{"no checkpoint", &runConfig{PermissionMode: agentpolicy.PermissionModeReadOnly}, false},
		{"read-only by configuration", &runConfig{WorkspaceCheckpoint: checkpoint, PermissionMode: agentpolicy.PermissionModeReadOnly}, true},
		{"degraded read-only keeps WIP", &runConfig{WorkspaceCheckpoint: checkpoint, PermissionMode: agentpolicy.PermissionModeReadOnly, PermissionModeDegraded: true}, false},
		{"write mode", &runConfig{WorkspaceCheckpoint: checkpoint, PermissionMode: agentpolicy.PermissionModeWorkspaceWrite}, false},
	}
	for _, tc := range cases {
		if got := skipReadOnlyCheckpointRestore(tc.cfg); got != tc.want {
			t.Errorf("%s: skipReadOnlyCheckpointRestore() = %v, want %v", tc.name, got, tc.want)
		}
	}
}
