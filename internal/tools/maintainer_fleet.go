package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/mcppolicy"
	"github.com/gratefulagents/gratefulagents/internal/orchestration"
	"github.com/gratefulagents/gratefulagents/internal/store"
	"github.com/gratefulagents/sdk/pkg/agentsdk"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	maintainerLegacyDispatchToolName = "dispatch_issue"
	maintainerLegacyMergeToolName    = "merge_pull_request"
	maintainerLegacyRunToolName      = "mark_run_succeeded"
	maintainerLegacyCloseToolName    = "close_github_issue"
)

// MaintainerLegacyMutationToolNames returns the live-cutover-controlled tool
// candidates retained for rollback without restarting the standing run.
func MaintainerLegacyMutationToolNames() []string {
	return []string{maintainerLegacyDispatchToolName, maintainerLegacyMergeToolName, maintainerLegacyRunToolName, maintainerLegacyCloseToolName}
}

func RegisterMaintainerTools(registry *Registry, stateStore store.StateStore, k8sClient client.Client, currentRunName, currentRunNamespace, repositoryName, repositoryNamespace string) {
	if registry == nil || stateStore == nil || k8sClient == nil || strings.TrimSpace(currentRunName) == "" || strings.TrimSpace(currentRunNamespace) == "" || strings.TrimSpace(repositoryName) == "" || strings.TrimSpace(repositoryNamespace) == "" {
		return
	}
	base := maintainerToolBase{
		stateStore: stateStore, k8sClient: k8sClient,
		currentRunName: currentRunName, currentRunNamespace: currentRunNamespace,
		repositoryName: repositoryName, repositoryNamespace: repositoryNamespace,
	}
	if closeTool := registry.Get(maintainerLegacyCloseToolName); closeTool != nil {
		registry.Register(&maintainerCutoverGuardedTool{Tool: closeTool, base: base})
	}
	registry.Register(&getFleetRunsTool{maintainerToolBase: base})
	registry.Register(&getFleetRunActivityTool{maintainerToolBase: base})
	registry.Register(&waitForRunsTool{maintainerToolBase: base, pollInterval: 10 * time.Second})
	registry.Register(&waitForRepoEventsTool{maintainerToolBase: base, runner: prReviewExecRunner{}, backlogPollInterval: defaultBacklogPollInterval, fleetPollInterval: defaultFleetEventsPollInterval})
	registry.Register(&triageIssueTool{maintainerToolBase: base})
	registry.Register(&breakdownIssueTool{maintainerToolBase: base})
	registry.Register(&requestDecisionTool{maintainerToolBase: base})
	registry.Register(&dispatchWorkItemTool{maintainerToolBase: base})
	registry.Register(&requestMergeTool{maintainerToolBase: base})
	registry.Register(&finalizeWorkItemTool{maintainerToolBase: base})
	registry.Register(&dispatchIssueTool{maintainerToolBase: base, runner: prReviewExecRunner{}})
	registry.Register(&mergePullRequestTool{maintainerToolBase: base, runner: prReviewExecRunner{}})
	registry.Register(&wakeAgentRunTool{maintainerToolBase: base})
	registry.Register(&getRunMessagesTool{maintainerToolBase: base})
	registry.Register(&cancelRunMessageTool{maintainerToolBase: base})
	registry.Register(&editRunMessageTool{maintainerToolBase: base})
	registry.Register(&getRunTranscriptTool{maintainerToolBase: base})
	registry.Register(&submitMaintainerReportTool{maintainerToolBase: base})
	registry.Register(&extendRunTimeoutTool{maintainerToolBase: base})
	registry.Register(&markRunSucceededTool{maintainerToolBase: base})
}

type maintainerCutoverGuardedTool struct {
	Tool
	base maintainerToolBase
}

func (t *maintainerCutoverGuardedTool) Execute(ctx context.Context, input json.RawMessage, workDir string) (Result, error) {
	if err := t.base.requireLegacyMutationAuthority(ctx); err != nil {
		return Result{Content: err.Error(), IsError: true}, nil
	}
	return t.Tool.Execute(ctx, input, workDir)
}

type fleetRunOutput struct {
	Name            string                          `json:"name"`
	Phase           platformv1alpha1.AgentRunPhase  `json:"phase"`
	Mode            string                          `json:"mode"`
	Role            string                          `json:"role,omitempty"`
	IssueRef        *platformv1alpha1.ExternalRef   `json:"issue_ref,omitempty"`
	CreatedAt       time.Time                       `json:"created_at,omitempty"`
	StartedAt       *metav1.Time                    `json:"started_at,omitempty"`
	CompletedAt     *metav1.Time                    `json:"completed_at,omitempty"`
	PullRequestURLs []string                        `json:"pull_request_urls,omitempty"`
	CostUSD         string                          `json:"cost_usd,omitempty"`
	QueueState      string                          `json:"queue_state,omitempty"`
	BlockedReason   string                          `json:"blocked_reason,omitempty"`
	PRLoopState     string                          `json:"pr_loop_state,omitempty"`
	ReviewRound     string                          `json:"review_round,omitempty"`
	PendingInput    *orchestration.PendingUserInput `json:"pending_input,omitempty"`
}

type fleetCapsOutput struct {
	ActiveDispatches        int   `json:"active_dispatches"`
	MaxConcurrentDispatches int32 `json:"max_concurrent_dispatches"`
	LedgerDispatches        int   `json:"ledger_dispatches"`
	MaxDispatchesPerDay     int32 `json:"max_dispatches_per_day"`
}

type getFleetRunsOutput struct {
	Runs []fleetRunOutput `json:"runs"`
	Caps fleetCapsOutput  `json:"caps"`
}

type getFleetRunsTool struct{ maintainerToolBase }

func (t *getFleetRunsTool) Name() string { return "get_fleet_runs" }
func (t *getFleetRunsTool) Description() string {
	return "List the maintained repository's dispatched implementer and reviewer runs with their lifecycle, artifacts, queue state, and pending input."
}
func (t *getFleetRunsTool) InputSchema() json.RawMessage          { return json.RawMessage(`{"type":"object"}`) }
func (t *getFleetRunsTool) IsReadOnly() bool                      { return true }
func (t *getFleetRunsTool) IsEnabled(_ *agentsdk.RunContext) bool { return true }
func (t *getFleetRunsTool) NeedsApproval() bool                   { return false }
func (t *getFleetRunsTool) TimeoutSeconds() int                   { return 0 }

func (t *getFleetRunsTool) Execute(ctx context.Context, _ json.RawMessage, _ string) (Result, error) {
	if _, err := t.currentRun(ctx); err != nil {
		return Result{Content: err.Error(), IsError: true}, nil
	}
	repository, err := t.repository(ctx)
	if err != nil {
		return Result{Content: err.Error(), IsError: true}, nil
	}
	fleet, err := t.fleetRuns(ctx)
	if err != nil {
		return Result{Content: err.Error(), IsError: true}, nil
	}
	out := getFleetRunsOutput{Runs: make([]fleetRunOutput, 0, len(fleet))}
	for i := range fleet {
		run := &fleet[i]
		entry, err := t.describeFleetRun(ctx, run)
		if err != nil {
			return Result{Content: err.Error(), IsError: true}, nil
		}
		out.Runs = append(out.Runs, entry)
	}
	maxConcurrent, maxDaily := maintainerDispatchCaps(repository)
	dispatchesToday := 0
	if repository.Status.Maintainer != nil {
		dispatchesToday = int(repository.Status.Maintainer.DispatchesToday)
	}
	for i := range fleet {
		if !maintainerIsReviewer(&fleet[i]) && !maintainerTerminal(fleet[i].Status.Phase) {
			out.Caps.ActiveDispatches++
		}
	}
	out.Caps.MaxConcurrentDispatches = maxConcurrent
	out.Caps.LedgerDispatches = dispatchesToday
	out.Caps.MaxDispatchesPerDay = maxDaily
	encoded, err := json.Marshal(out)
	if err != nil {
		return Result{}, err
	}
	return Result{Content: string(encoded)}, nil
}

func (t *getFleetRunsTool) describeFleetRun(ctx context.Context, run *platformv1alpha1.AgentRun) (fleetRunOutput, error) {
	entry := fleetRunOutput{Name: run.Name, Phase: run.Status.Phase, Mode: run.Status.ModeName, CreatedAt: run.CreationTimestamp.Time}
	if run.Spec.Trigger.ExternalRef != nil {
		ref := *run.Spec.Trigger.ExternalRef
		entry.IssueRef = &ref
	}
	entry.StartedAt, entry.CompletedAt = run.Status.StartedAt, run.Status.CompletedAt
	if maintainerIsReviewer(run) {
		entry.Role = "reviewer"
	} else {
		entry.Role = "implementer"
	}
	entry.PRLoopState = run.Labels[maintainerPRLoopStateLabel]
	entry.ReviewRound = run.Annotations[maintainerPRLoopRoundAnnotation]
	if run.Status.Artifacts != nil {
		entry.PullRequestURLs = append(entry.PullRequestURLs, run.Status.Artifacts.PullRequestURLs...)
		if len(entry.PullRequestURLs) == 0 && run.Status.Artifacts.PullRequestURL != "" {
			entry.PullRequestURLs = []string{run.Status.Artifacts.PullRequestURL}
		}
	}
	if run.Status.Metrics != nil {
		entry.CostUSD = run.Status.Metrics.CostUsd
	}
	if run.Status.Queue != nil {
		entry.QueueState = run.Status.Queue.State
		entry.BlockedReason = run.Status.Queue.BlockedReason
	}
	session, err := t.stateStore.GetSessionByRun(ctx, run.Name, run.Namespace)
	if err != nil {
		return fleetRunOutput{}, fmt.Errorf("failed to resolve session for fleet AgentRun %q: %w", run.Name, err)
	}
	entry.PendingInput = orchestration.PendingUserInputForSession(session)
	return entry, nil
}

type getFleetRunActivityTool struct{ maintainerToolBase }

type getFleetRunActivityInput struct {
	RunName        string `json:"run_name"`
	MessageCursor  int64  `json:"message_cursor,omitempty"`
	ActivityCursor int64  `json:"activity_cursor,omitempty"`
	Limit          int    `json:"limit,omitempty"`
}

func (t *getFleetRunActivityTool) Name() string { return "get_run_activity" }
func (t *getFleetRunActivityTool) Description() string {
	return "Read cursor-paged messages and activity for one authorized fleet AgentRun."
}
func (t *getFleetRunActivityTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"run_name":{"type":"string"},"message_cursor":{"type":"integer","minimum":0},"activity_cursor":{"type":"integer","minimum":0},"limit":{"type":"integer","minimum":1,"maximum":200}},"required":["run_name"]}`)
}
func (t *getFleetRunActivityTool) IsReadOnly() bool                      { return true }
func (t *getFleetRunActivityTool) IsEnabled(_ *agentsdk.RunContext) bool { return true }
func (t *getFleetRunActivityTool) NeedsApproval() bool                   { return false }
func (t *getFleetRunActivityTool) TimeoutSeconds() int                   { return 0 }

func (t *getFleetRunActivityTool) Execute(ctx context.Context, input json.RawMessage, _ string) (Result, error) {
	var in getFleetRunActivityInput
	if err := json.Unmarshal(input, &in); err != nil {
		return Result{Content: fmt.Sprintf("invalid input: %v", err), IsError: true}, nil
	}
	if strings.TrimSpace(in.RunName) == "" || in.MessageCursor < 0 || in.ActivityCursor < 0 || in.Limit < 0 {
		return Result{Content: "run_name is required; cursors must be non-negative and limit must be positive", IsError: true}, nil
	}
	if _, err := t.currentRun(ctx); err != nil {
		return Result{Content: err.Error(), IsError: true}, nil
	}
	run, err := t.fleetRun(ctx, strings.TrimSpace(in.RunName))
	if err != nil {
		return Result{Content: fmt.Sprintf("failed to verify fleet AgentRun: %v", err), IsError: true}, nil
	}
	session, err := t.stateStore.GetSessionByRun(ctx, run.Name, run.Namespace)
	if err != nil {
		return Result{Content: fmt.Sprintf("failed to resolve fleet session: %v", err), IsError: true}, nil
	}
	if session == nil {
		return Result{Content: "fleet session not found", IsError: true}, nil
	}
	inputRequest := orchestration.PendingUserInputForSession(session)
	pendingMCP, err := mcppolicy.PendingRequest(run)
	if err != nil {
		return Result{Content: fmt.Sprintf("failed to decode fleet MCP request: %v", err), IsError: true}, nil
	}
	if pendingMCP != nil {
		inputRequest = orchestration.BindPendingUserInputContext(inputRequest, pendingMCP.ID)
	}
	messages, err := t.stateStore.GetMessagesSince(ctx, session.ID, in.MessageCursor)
	if err != nil {
		return Result{Content: fmt.Sprintf("failed to read fleet messages: %v", err), IsError: true}, nil
	}
	events, err := t.stateStore.GetActivityEventsSince(ctx, session.ID, in.ActivityCursor)
	if err != nil {
		return Result{Content: fmt.Sprintf("failed to read fleet activity: %v", err), IsError: true}, nil
	}
	limit := in.Limit
	if limit == 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	out := supervisedActivityOutput{
		State:    supervisedRunState{Phase: run.Status.Phase, Mode: run.Status.ModeName, ModeRevision: run.Status.ModeRevision, UserInputRequest: inputRequest},
		Messages: make([]supervisedMessage, 0, min(limit, len(messages))), Activity: make([]supervisedEvent, 0, min(limit, len(events))),
		NextMessageCursor: in.MessageCursor, NextActivityCursor: in.ActivityCursor,
	}
	messageBudget := supervisedActivityStreamBudget
	for _, message := range messages[:min(limit, len(messages))] {
		content := truncateUTF8(message.Content, 4000)
		cost := len(content) + len(message.Role) + 96
		if len(out.Messages) > 0 && cost > messageBudget {
			break
		}
		if cost > messageBudget {
			content = truncateUTF8(content, max(0, messageBudget-96))
			cost = len(content) + len(message.Role) + 96
		}
		out.Messages = append(out.Messages, supervisedMessage{ID: message.ID, Role: message.Role, Content: content, CreatedAt: message.CreatedAt})
		out.NextMessageCursor = message.ID
		messageBudget -= min(cost, messageBudget)
	}
	activityBudget := supervisedActivityStreamBudget
	for _, event := range events[:min(limit, len(events))] {
		summary := truncateUTF8(event.Summary, 2000)
		detail := event.Detail
		if len(detail) > 4000 {
			detail = json.RawMessage(`{"truncated":true}`)
		}
		cost := len(summary) + len(detail) + len(event.EventType) + 112
		if len(out.Activity) > 0 && cost > activityBudget {
			break
		}
		if cost > activityBudget {
			summary = truncateUTF8(summary, max(0, activityBudget-len(detail)-112))
			cost = len(summary) + len(detail) + len(event.EventType) + 112
		}
		out.Activity = append(out.Activity, supervisedEvent{ID: event.ID, EventType: event.EventType, Summary: summary, Detail: detail, CreatedAt: event.CreatedAt})
		out.NextActivityCursor = event.ID
		activityBudget -= min(cost, activityBudget)
	}
	out.HasMore = len(out.Messages) < len(messages) || len(out.Activity) < len(events)
	encoded, err := json.Marshal(out)
	if err != nil {
		return Result{}, err
	}
	return Result{Content: string(encoded)}, nil
}
