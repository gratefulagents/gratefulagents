package dashboard

import (
	"testing"

	agent "github.com/gratefulagents/sdk/pkg/agentsdk"
)

func TestClassifySubagentStatus(t *testing.T) {
	cases := map[string]subagentStatusCategory{
		"":                subagentStatusLive,
		"running":         subagentStatusLive,
		"started":         subagentStatusLive,
		"Running":         subagentStatusLive,
		" completed ":     subagentStatusSucceeded,
		"succeeded":       subagentStatusSucceeded,
		"failed":          subagentStatusFailed,
		"timeout":         subagentStatusFailed,
		"timed_out":       subagentStatusFailed,
		"error":           subagentStatusFailed,
		"killed":          subagentStatusFailed,
		"stopped":         subagentStatusStopped,
		"cancelled":       subagentStatusStopped,
		"canceled":        subagentStatusStopped,
		"waiting":         subagentStatusWaiting,
		"pending":         subagentStatusWaiting,
		"reconciling":     subagentStatusWaiting,
		"dependency_wait": subagentStatusWaiting,
		"something_new":   subagentStatusUnknown,
		"ok":              subagentStatusUnknown,
	}
	for status, want := range cases {
		if got := classifySubagentStatus(status); got != want {
			t.Errorf("classifySubagentStatus(%q) = %q, want %q", status, got, want)
		}
	}
}

func TestIsTerminalSubagentStatusUsesCategories(t *testing.T) {
	for _, status := range []string{"completed", "succeeded", "failed", "timeout", "error", "stopped", "cancelled", "canceled"} {
		if !isTerminalSubagentStatus(status) {
			t.Errorf("%q should be terminal", status)
		}
	}
	for _, status := range []string{"", "running", "started", "waiting", "pending", "reconciling", "something_new"} {
		if isTerminalSubagentStatus(status) {
			t.Errorf("%q must not be terminal", status)
		}
	}
}

// A status the SDK has never emitted before must not leave the task alive
// forever: any terminal category closes it, unknown strings stay progress.
func TestContentEventToActivityEntryRoutesTerminalStatusesToCompleted(t *testing.T) {
	cases := map[string]string{
		"started":       "subagent_started",
		"completed":     "subagent_completed",
		"failed":        "subagent_completed",
		"cancelled":     "subagent_completed",
		"stopped":       "subagent_completed",
		"timeout":       "subagent_completed",
		"error":         "subagent_completed",
		"running":       "subagent_progress",
		"waiting":       "subagent_progress",
		"something_new": "subagent_progress",
	}
	for status, wantType := range cases {
		entry := contentEventToActivityEntry(&agent.ContentEvent{
			Type:      "subagent_status",
			Status:    status,
			AgentName: "reviewer",
			Message:   "review the diff",
		})
		if entry == nil {
			t.Fatalf("status %q produced no entry", status)
		}
		if entry.Type != wantType {
			t.Errorf("status %q routed to %q, want %q", status, entry.Type, wantType)
		}
		if entry.SubagentStatus != status {
			t.Errorf("status %q not preserved on entry: %q", status, entry.SubagentStatus)
		}
	}
}
