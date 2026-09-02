package main

import (
	"context"
	"encoding/json"
	"log"
	"strings"
	"sync"
	"time"

	agent "github.com/gratefulagents/sdk/pkg/agentsdk"
)

// standingWorkspaceRefreshInterval rate-limits base-branch refreshes of a
// read-only standing maintainer's checkout. Fetches are cheap when nothing
// moved, but the waiter can wake several times per second while commands are
// applied, so refreshes are coalesced.
const standingWorkspaceRefreshInterval = 2 * time.Minute

// standingWorkspaceRefreshTimeout bounds one fetch+reset so a slow remote can
// never stall the maintainer's turn for long.
const standingWorkspaceRefreshTimeout = 45 * time.Second

// standingWorkspaceRefreshHooks keeps a read-only standing maintainer's
// checkout at the current base-branch tip.
//
// The maintainer never edits the workspace (its mode is read-only) yet it
// reasons about code that its own fleet keeps merging into the base branch.
// The sandbox policy blocks `git fetch` from the model's Bash, so without this
// hook the checkout stays pinned at whatever the pod cloned at start — in
// production the maintainer ended up inspecting 32 KB PR diffs because "the
// local checkout is stale (pre-#52) and .git is read-only". Refreshing right
// after wait_for_repo_events reports a change means the tree is current
// exactly when the maintainer is about to act on it.
type standingWorkspaceRefreshHooks struct {
	agent.NoOpRunHooks

	repoDir    string
	baseBranch string
	interval   time.Duration
	timeout    time.Duration
	now        func() time.Time
	refresh    func(ctx context.Context, repoDir, baseBranch string) error

	mu          sync.Mutex
	lastRefresh time.Time
}

func newStandingWorkspaceRefreshHooks(repoDir, baseBranch string) *standingWorkspaceRefreshHooks {
	return &standingWorkspaceRefreshHooks{
		repoDir:    repoDir,
		baseBranch: strings.TrimSpace(baseBranch),
		interval:   standingWorkspaceRefreshInterval,
		timeout:    standingWorkspaceRefreshTimeout,
		now:        time.Now,
		refresh:    refreshReadOnlyCheckout,
	}
}

// OnToolEnd refreshes the checkout after a waiter result that carries changes.
// Timeouts, empty snapshots and errors do not trigger a fetch.
func (h *standingWorkspaceRefreshHooks) OnToolEnd(
	_ *agent.RunContext, _ *agent.Agent, tool agent.Tool, _ agent.ToolCallData, result agent.ToolResult,
) {
	if h == nil || tool == nil || tool.Name() != "wait_for_repo_events" || result.IsError {
		return
	}
	if !waiterResultReportsChange(result.Content) {
		return
	}
	h.maybeRefresh()
}

func (h *standingWorkspaceRefreshHooks) maybeRefresh() {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.now()
	if !h.lastRefresh.IsZero() && now.Sub(h.lastRefresh) < h.interval {
		return
	}
	h.lastRefresh = now
	ctx, cancel := context.WithTimeout(context.Background(), h.timeout)
	defer cancel()
	if err := h.refresh(ctx, h.repoDir, h.baseBranch); err != nil {
		log.Printf("WARN: standing maintainer workspace refresh failed (checkout may be stale): %v", err)
	}
}

// waiterResultReportsChange reports whether a wait_for_repo_events payload
// carries at least one work-item change. Legacy/DualRead payloads use
// different keys, so any `changed: true` counts for those.
func waiterResultReportsChange(content string) bool {
	var payload struct {
		Changed         bool              `json:"changed"`
		WorkItemChanges []json.RawMessage `json:"work_item_changes"`
		MigrationMode   string            `json:"migration_mode"`
	}
	if err := json.Unmarshal([]byte(content), &payload); err != nil || !payload.Changed {
		return false
	}
	if strings.EqualFold(payload.MigrationMode, "Controller") {
		return len(payload.WorkItemChanges) > 0
	}
	return true
}

// refreshReadOnlyCheckout fast-forwards a read-only checkout to the remote
// base-branch tip. The tree carries no local work by construction (read-only
// mode), so a hard reset is safe; it is the same operation a fresh clone
// would perform.
func refreshReadOnlyCheckout(ctx context.Context, repoDir, baseBranch string) error {
	if strings.TrimSpace(repoDir) == "" || baseBranch == "" {
		return nil
	}
	if _, err := gitOutput(ctx, repoDir, nil, "fetch", "--quiet", "--prune", "origin", baseBranch); err != nil {
		return err
	}
	before, _ := gitOutput(ctx, repoDir, nil, "rev-parse", "HEAD")
	if _, err := gitOutput(ctx, repoDir, nil, "reset", "--quiet", "--hard", "origin/"+baseBranch); err != nil {
		return err
	}
	after, _ := gitOutput(ctx, repoDir, nil, "rev-parse", "HEAD")
	if before != after {
		log.Printf("Standing maintainer workspace refreshed: %s → %s (origin/%s)",
			shortSHA(before), shortSHA(after), baseBranch)
	}
	return nil
}
