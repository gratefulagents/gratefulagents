package dashboard

import "strings"

// subagentStatusCategory is the single vocabulary every dashboard surface uses
// to reason about a sub-agent task's lifecycle. Raw status strings come from
// the SDK event stream and may grow over time; classifying them here (and in
// the mirrored frontend helper, platform-app/frontend/src/lib/subagentStatus.ts)
// keeps a new or unexpected status from being rendered as success or as work
// that never finishes.
type subagentStatusCategory string

const (
	subagentStatusLive      subagentStatusCategory = "live"
	subagentStatusWaiting   subagentStatusCategory = "waiting"
	subagentStatusSucceeded subagentStatusCategory = "succeeded"
	subagentStatusFailed    subagentStatusCategory = "failed"
	subagentStatusStopped   subagentStatusCategory = "stopped"
	subagentStatusUnknown   subagentStatusCategory = "unknown"
)

var subagentStatusCategories = map[string]subagentStatusCategory{
	"":             subagentStatusLive,
	"running":      subagentStatusLive,
	"started":      subagentStatusLive,
	"initializing": subagentStatusLive,
	"resumed":      subagentStatusLive,

	"waiting":         subagentStatusWaiting,
	"pending":         subagentStatusWaiting,
	"queued":          subagentStatusWaiting,
	"reconciling":     subagentStatusWaiting,
	"dependency_wait": subagentStatusWaiting,
	"parent_wait":     subagentStatusWaiting,
	"managed_wait":    subagentStatusWaiting,

	"completed": subagentStatusSucceeded,
	"succeeded": subagentStatusSucceeded,
	"success":   subagentStatusSucceeded,
	"done":      subagentStatusSucceeded,

	"failed":    subagentStatusFailed,
	"failure":   subagentStatusFailed,
	"error":     subagentStatusFailed,
	"errored":   subagentStatusFailed,
	"timeout":   subagentStatusFailed,
	"timed_out": subagentStatusFailed,
	"killed":    subagentStatusFailed,
	"panicked":  subagentStatusFailed,

	"stopped":     subagentStatusStopped,
	"cancelled":   subagentStatusStopped,
	"canceled":    subagentStatusStopped,
	"interrupted": subagentStatusStopped,
}

// classifySubagentStatus maps a raw status string to its lifecycle category.
// Anything not in the vocabulary is unknown: callers must render it neutrally
// and never treat it as success or as live work.
func classifySubagentStatus(status string) subagentStatusCategory {
	if category, ok := subagentStatusCategories[strings.ToLower(strings.TrimSpace(status))]; ok {
		return category
	}
	return subagentStatusUnknown
}

// isTerminalSubagentStatus reports whether the status describes finished work
// (success, failure, or a stop). Unknown statuses are not terminal: a stale or
// novel string must not end a task's live tracking on its own.
func isTerminalSubagentStatus(status string) bool {
	switch classifySubagentStatus(status) {
	case subagentStatusSucceeded, subagentStatusFailed, subagentStatusStopped:
		return true
	}
	return false
}
