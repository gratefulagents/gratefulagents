package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/orchestration"
	"github.com/gratefulagents/gratefulagents/internal/store"
	"github.com/gratefulagents/sdk/pkg/agentsdk"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

const (
	waitConditionPRCreated         = "pr_created"
	waitConditionAwaitingUserInput = "awaiting_user_input"
	waitConditionIdle              = "idle"
	waitConditionTerminal          = "terminal"
	waitConditionBlocked           = "blocked"
)

type waitForRunsTool struct {
	maintainerToolBase
	pollInterval time.Duration
}

type waitForRunsInput struct {
	RunNames       []string `json:"run_names,omitempty"`
	Until          []string `json:"until,omitempty"`
	Match          string   `json:"match,omitempty"`
	TimeoutSeconds int      `json:"timeout_seconds,omitempty"`
}

type waitRunSnapshot struct {
	Name             string                          `json:"name"`
	Phase            platformv1alpha1.AgentRunPhase  `json:"phase,omitempty"`
	ConditionsMet    []string                        `json:"conditions_met"`
	PullRequestURLs  []string                        `json:"pull_request_urls,omitempty"`
	PendingInput     *orchestration.PendingUserInput `json:"pending_input,omitempty"`
	BlockedReason    string                          `json:"blocked_reason,omitempty"`
	NotFoundYet      bool                            `json:"not_found_yet,omitempty"`
	PRAlreadyPresent bool                            `json:"pr_already_present,omitempty"`
}

type waitForRunsOutput struct {
	Satisfied      bool              `json:"satisfied"`
	TimedOut       bool              `json:"timed_out"`
	ElapsedSeconds int               `json:"elapsed_seconds"`
	Runs           []waitRunSnapshot `json:"runs"`
}

func (t *waitForRunsTool) Name() string { return "wait_for_runs" }
func (t *waitForRunsTool) Description() string {
	return "Block while authorized fleet runs reach pull-request, user-input, idle, blocked, or terminal states; newly dispatched named runs may appear after waiting begins."
}
func (t *waitForRunsTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"run_names":{"type":"array","items":{"type":"string"}},"until":{"type":"array","items":{"type":"string","enum":["pr_created","awaiting_user_input","idle","terminal","blocked"]}},"match":{"type":"string","enum":["any","all"]},"timeout_seconds":{"type":"integer","minimum":1,"maximum":1800}}}`)
}
func (t *waitForRunsTool) IsReadOnly() bool                      { return true }
func (t *waitForRunsTool) IsEnabled(_ *agentsdk.RunContext) bool { return true }
func (t *waitForRunsTool) NeedsApproval() bool                   { return false }
func (t *waitForRunsTool) TimeoutSeconds() int                   { return 1860 }

func (t *waitForRunsTool) Execute(ctx context.Context, input json.RawMessage, _ string) (Result, error) {
	var in waitForRunsInput
	if err := json.Unmarshal(input, &in); err != nil {
		return Result{Content: fmt.Sprintf("invalid input: %v", err), IsError: true}, nil
	}
	until, err := normalizeWaitConditions(in.Until)
	if err != nil {
		return Result{Content: err.Error(), IsError: true}, nil
	}
	match := strings.ToLower(strings.TrimSpace(in.Match))
	if match == "" {
		match = "any"
	}
	if match != "any" && match != "all" {
		return Result{Content: `match must be "any" or "all"`, IsError: true}, nil
	}
	timeout := in.TimeoutSeconds
	if timeout == 0 {
		timeout = 600
	}
	if timeout < 1 || timeout > 1800 {
		return Result{Content: "timeout_seconds must be between 1 and 1800", IsError: true}, nil
	}
	if _, err := t.currentRun(ctx); err != nil {
		return Result{Content: err.Error(), IsError: true}, nil
	}
	names := normalizedRunNames(in.RunNames)
	if len(names) == 0 {
		fleet, err := t.fleetRuns(ctx)
		if err != nil {
			return Result{Content: err.Error(), IsError: true}, nil
		}
		for i := range fleet {
			if !maintainerTerminal(fleet[i].Status.Phase) {
				names = append(names, fleet[i].Name)
			}
		}
	}
	baseline := make(map[string]bool, len(names))
	for _, name := range names {
		run, err := t.fleetRun(ctx, name)
		if err == nil {
			baseline[name] = len(waitPullRequestURLs(run)) > 0
			continue
		}
		if !apierrors.IsNotFound(err) {
			return Result{Content: fmt.Sprintf("failed to verify fleet AgentRun %q: %v", name, err), IsError: true}, nil
		}
	}
	started := time.Now()
	interval := t.pollInterval
	if interval <= 0 {
		interval = 10 * time.Second
	}
	for {
		snapshots, satisfied, err := t.waitSnapshots(ctx, names, until, match, baseline)
		if err != nil {
			return Result{Content: err.Error(), IsError: true}, nil
		}
		elapsed := int(time.Since(started).Seconds())
		if satisfied {
			return marshalWaitResult(waitForRunsOutput{Satisfied: true, ElapsedSeconds: elapsed, Runs: snapshots})
		}
		remaining := time.Until(started.Add(time.Duration(timeout) * time.Second))
		if remaining <= 0 {
			return marshalWaitResult(waitForRunsOutput{TimedOut: true, ElapsedSeconds: elapsed, Runs: snapshots})
		}
		if remaining < interval {
			interval = remaining
		}
		select {
		case <-ctx.Done():
			return Result{Content: fmt.Sprintf("wait cancelled: %v", ctx.Err()), IsError: true}, nil
		case <-time.After(interval):
		}
	}
}

func marshalWaitResult(out waitForRunsOutput) (Result, error) {
	encoded, err := json.Marshal(out)
	if err != nil {
		return Result{}, err
	}
	return Result{Content: string(encoded)}, nil
}

func normalizeWaitConditions(conditions []string) (map[string]struct{}, error) {
	if len(conditions) == 0 {
		return map[string]struct{}{waitConditionTerminal: {}}, nil
	}
	out := make(map[string]struct{}, len(conditions))
	for _, condition := range conditions {
		condition = strings.ToLower(strings.TrimSpace(condition))
		switch condition {
		case waitConditionPRCreated, waitConditionAwaitingUserInput, waitConditionIdle, waitConditionTerminal, waitConditionBlocked:
			out[condition] = struct{}{}
		default:
			return nil, fmt.Errorf("until contains unsupported condition %q", condition)
		}
	}
	return out, nil
}

func normalizedRunNames(names []string) []string {
	seen := make(map[string]struct{}, len(names))
	out := make([]string, 0, len(names))
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if _, exists := seen[name]; !exists {
			seen[name] = struct{}{}
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

func (t *waitForRunsTool) waitSnapshots(ctx context.Context, names []string, until map[string]struct{}, match string, baseline map[string]bool) ([]waitRunSnapshot, bool, error) {
	snapshots := make([]waitRunSnapshot, 0, len(names))
	matched := 0
	for _, name := range names {
		run, err := t.fleetRun(ctx, name)
		if apierrors.IsNotFound(err) {
			snapshots = append(snapshots, waitRunSnapshot{Name: name, ConditionsMet: []string{}, NotFoundYet: true})
			continue
		}
		if err != nil {
			return nil, false, fmt.Errorf("failed to verify fleet AgentRun %q: %w", name, err)
		}
		session, err := t.stateStore.GetSessionByRun(ctx, run.Name, run.Namespace)
		if err != nil {
			return nil, false, fmt.Errorf("failed to resolve session for fleet AgentRun %q: %w", name, err)
		}
		met := conditionsMet(run, session, baseline)
		selected := false
		for _, condition := range met {
			if _, ok := until[condition]; ok {
				selected = true
				break
			}
		}
		if selected {
			matched++
		}
		snapshots = append(snapshots, waitRunSnapshot{
			Name: name, Phase: run.Status.Phase, ConditionsMet: met, PullRequestURLs: waitPullRequestURLs(run),
			PendingInput: orchestration.PendingUserInputForSession(session), BlockedReason: maintainerBlockedReason(run),
			PRAlreadyPresent: baseline[name] && len(waitPullRequestURLs(run)) > 0,
		})
	}
	if len(names) == 0 {
		return snapshots, true, nil
	}
	return snapshots, (match == "any" && matched > 0) || (match == "all" && matched == len(names)), nil
}

func conditionsMet(run *platformv1alpha1.AgentRun, session *store.Session, baseline map[string]bool) []string {
	if run == nil {
		return []string{}
	}
	met := make([]string, 0, 5)
	prURLs := waitPullRequestURLs(run)
	if len(prURLs) > 0 || baseline[run.Name] {
		met = append(met, waitConditionPRCreated)
	}
	pending := orchestration.PendingUserInputForSession(session) != nil
	awaitingInput := run.Status.Phase == platformv1alpha1.AgentRunPhaseQuestion || pending
	if awaitingInput {
		met = append(met, waitConditionAwaitingUserInput)
	}
	blocked := run.Status.Phase == platformv1alpha1.AgentRunPhaseBlocked || maintainerBlockedReason(run) != ""
	if blocked {
		met = append(met, waitConditionBlocked)
	}
	terminal := maintainerTerminal(run.Status.Phase)
	if terminal {
		met = append(met, waitConditionTerminal)
	}
	if terminal || blocked || awaitingInput || run.Status.Phase == platformv1alpha1.AgentRunPhasePaused {
		met = append(met, waitConditionIdle)
	}
	return met
}

func waitPullRequestURLs(run *platformv1alpha1.AgentRun) []string {
	if run == nil || run.Status.Artifacts == nil {
		return nil
	}
	urls := append([]string(nil), run.Status.Artifacts.PullRequestURLs...)
	if len(urls) == 0 && run.Status.Artifacts.PullRequestURL != "" {
		urls = append(urls, run.Status.Artifacts.PullRequestURL)
	}
	return urls
}
