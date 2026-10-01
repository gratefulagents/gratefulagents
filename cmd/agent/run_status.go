package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/store/sessionclient"
	internaltools "github.com/gratefulagents/gratefulagents/internal/tools"
	agent "github.com/gratefulagents/sdk/pkg/agentsdk"
)

// --- CRD helpers (kept for status writing) ---

func getAgentRun(ctx context.Context, c client.Client, taskName, namespace string) *platformv1alpha1.AgentRun {
	run := &platformv1alpha1.AgentRun{}
	if err := c.Get(ctx, types.NamespacedName{Name: taskName, Namespace: namespace}, run); err != nil {
		return nil
	}
	return run
}

// Transient API-server and Postgres errors are retried with capped
// exponential backoff; vars so tests can shrink the delays.
var (
	transientRetryBaseDelay = 250 * time.Millisecond
	transientRetryMaxDelay  = 10 * time.Second
)

// permanentError marks an error retryTransient must not retry.
type permanentError struct{ error }

func (e permanentError) Unwrap() error { return e.error }

// retryTransient runs fn until it succeeds, returns a permanentError, ctx
// ends, or attempts (when > 0) are exhausted. attempts <= 0 retries until ctx
// is cancelled: one transient blip must not end a long-lived session.
func retryTransient(ctx context.Context, what string, attempts int, fn func(context.Context) error) error {
	delay := transientRetryBaseDelay
	for attempt := 1; ; attempt++ {
		err := fn(ctx)
		if err == nil {
			return nil
		}
		var permanent permanentError
		if errors.As(err, &permanent) {
			return permanent.error
		}
		if ctx.Err() != nil {
			return err
		}
		if attempts > 0 && attempt >= attempts {
			return err
		}
		log.Printf("WARN: %s failed (attempt %d): %v — retrying in %s", what, attempt, err, delay)
		select {
		case <-ctx.Done():
			return err
		case <-time.After(delay):
		}
		delay = min(delay*2, transientRetryMaxDelay)
	}
}

// readAgentRun reads the AgentRun, retrying transient API errors (attempts <=
// 0: until ctx ends). NotFound is permanent.
func readAgentRun(ctx context.Context, c client.Client, name, namespace string, attempts int) (*platformv1alpha1.AgentRun, error) {
	run := &platformv1alpha1.AgentRun{}
	err := retryTransient(ctx, "reading AgentRun "+namespace+"/"+name, attempts, func(ctx context.Context) error {
		err := c.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, run)
		if apierrors.IsNotFound(err) {
			return permanentError{err}
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	return run, nil
}

// --- CRD status writing ---

func writeResultToStatus(ctx context.Context, c client.Client, taskName, namespace string, result runResult, eventsLogURL string) error {
	return patchAgentRunStatus(ctx, c, taskName, namespace, func(run *platformv1alpha1.AgentRun) {
		run.Status.Artifacts = ensureRunArtifacts(run.Status.Artifacts)
		// Only record a real artifact URL: an empty value would erase the
		// events log a previous pod of this run published.
		if eventsLogURL != "" {
			run.Status.Artifacts.EventsLogURL = eventsLogURL
		}
		run.Status.LastError = result.Error
		run.Status.Phase = platformv1alpha1.AgentRunPhaseSucceeded
		run.Status.CurrentStep = "review-complete"
		run.Status.Queue = &platformv1alpha1.AgentRunQueueStatus{State: "Succeeded"}
		if result.Status == "failed" {
			run.Status.Phase = platformv1alpha1.AgentRunPhaseFailed
			run.Status.CurrentStep = "failed"
			run.Status.Queue = &platformv1alpha1.AgentRunQueueStatus{State: "Failed", BlockedReason: result.Error}
		}
	})
}

// --- Progress loop ---

// contextBudget is the compaction budget for the active model, published by
// the chat loop each turn and attached to session metrics so the dashboard can
// render the context-usage bar (used tokens come from the trace stream).
type contextBudget struct {
	TriggerTokens int64
	TargetTokens  int64
}

// currentContextBudget is written by the chat loop (per turn, model may change
// mid-run) and read by the progress loop's metrics writer.
var currentContextBudget atomic.Pointer[contextBudget]

// currentContextTokens is the prompt-side size of the main agent's latest
// generation, captured by contextUsageHooks.
var currentContextTokens atomic.Int64

// publishContextBudget records the active compaction thresholds for status
// reporting. Disabled compaction publishes zeros (hides the bar).
func publishContextBudget(cfg agent.CompactionConfig) {
	budget := &contextBudget{}
	if cfg.Enabled {
		budget.TriggerTokens = int64(cfg.TriggerTokens)
		budget.TargetTokens = int64(cfg.TargetTokens)
	}
	currentContextBudget.Store(budget)
}

type progressMetricsBaseline struct {
	CostUSD       float64
	InputTokens   int64
	OutputTokens  int64
	ToolCallCount int32
}

func progressMetricsBaselineFromRun(run *platformv1alpha1.AgentRun) progressMetricsBaseline {
	if run == nil || run.Status.Metrics == nil {
		return progressMetricsBaseline{}
	}
	metrics := run.Status.Metrics
	baseline := progressMetricsBaseline{
		InputTokens:   metrics.InputTokens,
		OutputTokens:  metrics.OutputTokens,
		ToolCallCount: metrics.ToolCallCount,
	}
	if cost, err := strconv.ParseFloat(strings.TrimSpace(metrics.CostUsd), 64); err == nil && cost >= 0 {
		baseline.CostUSD = cost
	}
	return baseline
}

// startupMetricsReadAttempts bounds the startup reads of the prior cost
// baseline before the pod refuses to start.
const startupMetricsReadAttempts = 6

// loadProgressMetricsBaseline captures the spend recorded by earlier pods as
// the field-wise max of the Postgres session metrics (written first) and the
// AgentRun status mirror. A source that stays unreadable after bounded retries
// is skipped; if neither is readable startup fails instead of silently
// resetting spend to zero, which would re-grant the whole cost cap.
func loadProgressMetricsBaseline(ctx context.Context, c client.Client, sc *sessionclient.Client, name, namespace string) (progressMetricsBaseline, error) {
	var baseline progressMetricsBaseline
	run, runErr := readAgentRun(ctx, c, name, namespace, startupMetricsReadAttempts)
	if runErr == nil {
		baseline = progressMetricsBaselineFromRun(run)
	}
	var pgErr error
	if sc != nil {
		var metrics sessionclient.SessionMetrics
		pgErr = retryTransient(ctx, "reading session metrics", startupMetricsReadAttempts, func(ctx context.Context) error {
			var err error
			metrics, err = sc.ReadMetrics(ctx)
			return err
		})
		if pgErr == nil {
			baseline = maxProgressMetrics(baseline, progressMetricsBaseline{
				CostUSD:       max(metrics.CostUSD, 0),
				InputTokens:   metrics.InputTokens,
				OutputTokens:  metrics.OutputTokens,
				ToolCallCount: metrics.ToolCallCount,
			})
		}
	} else {
		pgErr = errors.New("no session client")
	}
	if runErr != nil && pgErr != nil {
		return progressMetricsBaseline{}, fmt.Errorf("reading prior run metrics: AgentRun: %v; session: %v", runErr, pgErr)
	}
	if runErr != nil {
		log.Printf("WARN: AgentRun metrics unreadable (%v) — cost baseline taken from the session store", runErr)
	}
	if pgErr != nil {
		log.Printf("WARN: session metrics unreadable (%v) — cost baseline taken from the AgentRun status", pgErr)
	}
	return baseline, nil
}

func maxProgressMetrics(a, b progressMetricsBaseline) progressMetricsBaseline {
	return progressMetricsBaseline{
		CostUSD:       max(a.CostUSD, b.CostUSD),
		InputTokens:   max(a.InputTokens, b.InputTokens),
		OutputTokens:  max(a.OutputTokens, b.OutputTokens),
		ToolCallCount: max(a.ToolCallCount, b.ToolCallCount),
	}
}

func cumulativeProgressMetrics(baseline progressMetricsBaseline, snap agent.ProgressSnapshot) progressMetricsBaseline {
	return progressMetricsBaseline{
		CostUSD:       baseline.CostUSD + snap.CostUsd,
		InputTokens:   baseline.InputTokens + snap.InputTokens,
		OutputTokens:  baseline.OutputTokens + snap.OutputTokens,
		ToolCallCount: baseline.ToolCallCount + snap.ToolCallCount,
	}
}

// sessionMetricsFromSnapshot merges cumulative tracker usage with the
// published, instantaneous context budget into the durable metrics record.
func sessionMetricsFromSnapshot(baseline progressMetricsBaseline, snap agent.ProgressSnapshot) sessionclient.SessionMetrics {
	cumulative := cumulativeProgressMetrics(baseline, snap)
	metrics := sessionclient.SessionMetrics{
		CostUSD:       cumulative.CostUSD,
		InputTokens:   cumulative.InputTokens,
		OutputTokens:  cumulative.OutputTokens,
		ToolCallCount: cumulative.ToolCallCount,
	}
	if budget := currentContextBudget.Load(); budget != nil {
		metrics.ContextTriggerTokens = budget.TriggerTokens
		metrics.ContextTargetTokens = budget.TargetTokens
	}
	metrics.ContextTokens = currentContextTokens.Load()
	return metrics
}

// progressMetricsGate skips metrics writes whose payload is identical to the
// last successful write. Each session metrics write is a JSONB merge that
// fires a session UPDATE and a NOTIFY; while the run is idle those no-op
// writes wake this pod's own pollers every 5s and defeat their relaxed idle
// interval. The final write always goes through.
type progressMetricsGate struct {
	last    sessionclient.SessionMetrics
	written bool
}

// shouldWrite reports whether metrics differ from the last recorded write.
func (g *progressMetricsGate) shouldWrite(metrics sessionclient.SessionMetrics, final bool) bool {
	return final || !g.written || g.last != metrics
}

// recordWritten remembers a successful write; failed writes are retried on
// the next tick even if the payload has not changed.
func (g *progressMetricsGate) recordWritten(metrics sessionclient.SessionMetrics) {
	g.last = metrics
	g.written = true
}

func startProgressLoop(ctx context.Context, crdClient client.Client, cfg runConfig, tracker *agent.RunProgress, sc *sessionclient.Client, baseline progressMetricsBaseline) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	var gate progressMetricsGate
	write := func(ctx context.Context, final bool) {
		metrics := sessionMetricsFromSnapshot(baseline, tracker.Snapshot())
		if !gate.shouldWrite(metrics, final) {
			return
		}
		if writeProgressMetrics(ctx, crdClient, cfg, metrics, sc, final) {
			gate.recordWritten(metrics)
		}
	}
	for {
		select {
		case <-ctx.Done():
			writeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			write(writeCtx, true)
			cancel()
			return

		case <-ticker.C:
			write(ctx, false)
		}
	}
}

// writeProgressMetrics persists one authoritative cumulative metrics update.
// Postgres is primary when a session client exists and its WriteMetrics call
// mirrors the same values to the CRD; the direct CRD path is only a fallback.
// It reports whether the write succeeded.
func writeProgressMetrics(ctx context.Context, crdClient client.Client, cfg runConfig, metrics sessionclient.SessionMetrics, sc *sessionclient.Client, final bool) bool {
	if sc != nil {
		if err := sc.WriteMetrics(ctx, metrics); err != nil {
			prefix := ""
			if final {
				prefix = "final "
			}
			log.Printf("WARN: %smetrics write to postgres failed: %v", prefix, err)
			return false
		}
		return true
	}
	if err := writeProgressToStatus(ctx, crdClient, cfg.TaskName, cfg.Namespace, metrics); err != nil {
		prefix := ""
		if final {
			prefix = "final "
		}
		log.Printf("WARN: %sprogress write failed: %v", prefix, err)
		return false
	}
	return true
}

// writeProgressToStatus patches the AgentRun status with cumulative metrics.
func writeProgressToStatus(ctx context.Context, c client.Client, taskName, taskNamespace string, metrics sessionclient.SessionMetrics) error {
	return patchAgentRunStatus(ctx, c, taskName, taskNamespace, func(run *platformv1alpha1.AgentRun) {
		run.Status.Metrics = &platformv1alpha1.AgentRunMetrics{
			CostUsd:       fmt.Sprintf("%.4f", metrics.CostUSD),
			InputTokens:   metrics.InputTokens,
			OutputTokens:  metrics.OutputTokens,
			ToolCallCount: metrics.ToolCallCount,
		}
		if run.Status.Phase == "" || run.Status.Phase == platformv1alpha1.AgentRunPhasePending || run.Status.Phase == platformv1alpha1.AgentRunPhaseAdmitted {
			run.Status.Phase = platformv1alpha1.AgentRunPhaseRunning
		}
		if run.Status.Queue == nil || run.Status.Queue.State == "" {
			run.Status.Queue = &platformv1alpha1.AgentRunQueueStatus{State: "Running"}
		}
	})
}

// --- Shared CRD helpers ---

func patchAgentRunStatus(ctx context.Context, c client.Client, name, namespace string, mutate func(*platformv1alpha1.AgentRun)) error {
	return patchAgentRunWithRetry(ctx, c, name, namespace, "status", mutate,
		func(ctx context.Context, run *platformv1alpha1.AgentRun, patch client.Patch) error {
			return c.Status().Patch(ctx, run, patch)
		})
}

// patchAgentRunSpec mutates the AgentRun spec (not status). The worker owns
// status; the only spec field it touches is restartRequests — the documented
// "this pod must be re-provisioned" signal, bumped when a degraded read-only
// pod detects that write access has resolved.
func patchAgentRunSpec(ctx context.Context, c client.Client, name, namespace string, mutate func(*platformv1alpha1.AgentRun)) error {
	return patchAgentRunWithRetry(ctx, c, name, namespace, "spec", mutate,
		func(ctx context.Context, run *platformv1alpha1.AgentRun, patch client.Patch) error {
			return c.Patch(ctx, run, patch)
		})
}

// patchAgentRunWithRetry is the worker's get → mutate → optimistic-lock patch
// loop, retried on conflict. A deleted AgentRun is not an error: the pod is
// about to be torn down with it.
func patchAgentRunWithRetry(
	ctx context.Context, c client.Client, name, namespace, what string,
	mutate func(*platformv1alpha1.AgentRun),
	apply func(context.Context, *platformv1alpha1.AgentRun, client.Patch) error,
) error {
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		run := &platformv1alpha1.AgentRun{}
		if err := c.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, run); err != nil {
			if apierrors.IsNotFound(err) {
				return nil
			}
			return fmt.Errorf("getting AgentRun: %w", err)
		}
		patch := client.MergeFromWithOptions(run.DeepCopy(), client.MergeFromWithOptimisticLock{})
		mutate(run)
		if err := apply(ctx, run, patch); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("patching AgentRun %s: %w", what, err)
	}
	return nil
}

func ensureRunArtifacts(in *platformv1alpha1.AgentRunArtifacts) *platformv1alpha1.AgentRunArtifacts {
	if in != nil {
		return in
	}
	return &platformv1alpha1.AgentRunArtifacts{}
}

// --- Mode and reviewer helpers ---

// reviewerModeName matches the PR loop's default reviewer ModeTemplate.
// Used only as a legacy fallback for templates without permissionMode.
const reviewerModeName = "review"

// snapshotPermissionMode returns the mode template's declared permission
// clamp, if any.
func snapshotPermissionMode(run *platformv1alpha1.AgentRun) platformv1alpha1.PermissionMode {
	if run == nil || run.Status.ModeSnapshot == nil {
		return ""
	}
	return run.Status.ModeSnapshot.PermissionMode
}

// snapshotIsAutonomous reports whether the run's pinned mode template is
// autonomous (single-mode runs like review, whose permission clamp can be
// applied at the pod level because the mode never changes mid-run).
func snapshotIsAutonomous(run *platformv1alpha1.AgentRun) bool {
	if run == nil || run.Status.ModeSnapshot == nil {
		return false
	}
	return run.Status.ModeSnapshot.Autonomous
}

// snapshotAllowedMutatingTools returns the mode template's mutating-tool
// allowlist, if any.
func snapshotAllowedMutatingTools(run *platformv1alpha1.AgentRun) []string {
	if run == nil || run.Status.ModeSnapshot == nil {
		return nil
	}
	return run.Status.ModeSnapshot.AllowedMutatingTools
}

// effectiveAllowedMutatingTools returns the exact exceptions enforced at both
// registry construction and SDK per-turn filtering. The reviewer fallback
// keeps templates installed before allowedMutatingTools was introduced working
// consistently at both layers.
func effectiveAllowedMutatingTools(run *platformv1alpha1.AgentRun) []string {
	if allowed := snapshotAllowedMutatingTools(run); len(allowed) > 0 {
		return append([]string(nil), allowed...)
	}
	if isActiveReviewerRun(run) {
		return internaltools.ReviewerMutatingToolNames()
	}
	return nil
}

// effectiveRuntimeAllowedMutatingTools adds rollback-only maintainer tools from
// the live repository cutover. Repository read failures and Controller/default
// mode fail closed by retaining only the ModeTemplate's normal exceptions.
func effectiveRuntimeAllowedMutatingTools(
	ctx context.Context,
	c client.Client,
	run *platformv1alpha1.AgentRun,
	repositoryName, repositoryNamespace string,
) []string {
	allowed := effectiveAllowedMutatingTools(run)
	if c == nil || strings.TrimSpace(repositoryName) == "" || strings.TrimSpace(repositoryNamespace) == "" {
		return allowed
	}
	repository := &triggersv1alpha1.GitHubRepository{}
	if err := c.Get(ctx, client.ObjectKey{Name: repositoryName, Namespace: repositoryNamespace}, repository); err != nil {
		return allowed
	}
	cutover := triggersv1alpha1.MaintainerWorkItemCutoverController
	if repository.Spec.Maintainer != nil && repository.Spec.Maintainer.WorkItemCutover != "" {
		cutover = repository.Spec.Maintainer.WorkItemCutover
	}
	if cutover != triggersv1alpha1.MaintainerWorkItemCutoverLegacy &&
		cutover != triggersv1alpha1.MaintainerWorkItemCutoverDualRead {
		return allowed
	}
	seen := make(map[string]struct{}, len(allowed)+len(internaltools.MaintainerLegacyMutationToolNames()))
	out := make([]string, 0, len(allowed)+len(internaltools.MaintainerLegacyMutationToolNames()))
	for _, name := range append(allowed, internaltools.MaintainerLegacyMutationToolNames()...) {
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	return out
}

// isActiveReviewerRun applies the legacy allowlist only to the current mode.
// ModeRef is a creation-time fallback and must not re-authorize review outputs
// after a live switch updates the status mode or snapshot.
func isActiveReviewerRun(run *platformv1alpha1.AgentRun) bool {
	if run == nil {
		return false
	}
	if run.Status.ModeName != "" {
		return run.Status.ModeName == reviewerModeName
	}
	if run.Status.ModeSnapshot != nil && run.Status.ModeSnapshot.Name != "" {
		return run.Status.ModeSnapshot.Name == reviewerModeName
	}
	return run.Spec.ModeRef != nil && run.Spec.ModeRef.Name == reviewerModeName
}

// isReviewerRun reports whether the run executes the reviewer mode. Reviewer
// runs are clamped to read-only: their only legitimate outputs are GitHub
// review comments and the recorded verdict.
func isReviewerRun(run *platformv1alpha1.AgentRun) bool {
	if run == nil {
		return false
	}
	if run.Status.ModeName == reviewerModeName {
		return true
	}
	return run.Spec.ModeRef != nil && run.Spec.ModeRef.Name == reviewerModeName
}

// --- Cost ceiling helpers ---

// validatedCostCapUSD returns the run's spec.limits.maxCostUsd. configured is
// true whenever a value is set; an invalid value is an error so the caller
// fails closed instead of running uncapped.
func validatedCostCapUSD(run *platformv1alpha1.AgentRun) (float64, bool, error) {
	if run == nil || run.Spec.Limits == nil {
		return 0, false, nil
	}
	return run.Spec.Limits.CostCapUSD()
}
