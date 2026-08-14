package triggers

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/security"
	"github.com/gratefulagents/gratefulagents/internal/store"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

// Durable post-scripts: a post-script used to be prose in the sink task's
// prompt, so which findings it ran on, in what order, and whether it ran at
// all were model-discretionary and unauditable. The engine instead selects
// applicable scripts once per execution and normally runs one ordered AgentRun
// pipeline per finding. Oversized prompts split at script boundaries.
const (
	// securityScanMaxCoverageGaps mirrors the CRD's MaxItems on
	// status.lastExecution.coverageGaps; exceeding it would make the status
	// write fail validation, so the last slot is spent on an overflow notice.
	securityScanMaxCoverageGaps = 50
	// securityScanMaxPostScriptPipelinePromptBytes keeps the combined seed
	// message comfortably below Kubernetes' object-size ceiling. Pipelines
	// that would cross it are split at script boundaries; an individually
	// oversized script remains a singleton, matching the legacy behavior.
	securityScanMaxPostScriptPipelinePromptBytes = 512 * 1024
)

// appendSecurityScanCoverageGap records one reason this execution's results
// are not complete. Gaps are deduplicated (a truncation observed on repeated
// reconciles is one gap, not many) and bounded: coverage disclosure must
// never be the reason a status update is rejected.
func appendSecurityScanCoverageGap(exec *triggersv1alpha1.SecurityScanExecutionStatus, gap string) {
	gap = truncateSecurityScanError(strings.TrimSpace(gap))
	if exec == nil || gap == "" {
		return
	}
	const overflow = "additional coverage gaps omitted: the execution reached the 50-entry limit"
	for _, existing := range exec.CoverageGaps {
		if existing == gap || existing == overflow {
			return
		}
	}
	if len(exec.CoverageGaps) >= securityScanMaxCoverageGaps {
		exec.CoverageGaps[securityScanMaxCoverageGaps-1] = overflow
		return
	}
	exec.CoverageGaps = append(exec.CoverageGaps, gap)
}

// postScriptsGateSink reports whether the sink tasks must keep waiting: the
// final report may only be submitted once every post-script verdict is
// durable, otherwise the summary would describe findings whose validation is
// still in flight.
func (e *securityScanExecutionEngine) postScriptsGateSink() bool {
	if len(e.resolved.spec.PostScripts) == 0 {
		return false
	}
	if !e.exec.PostScriptsMaterialized {
		return true
	}
	for _, job := range e.exec.PostScriptJobs {
		if !triggersv1alpha1.SecurityScanPostScriptJobTerminal(job.State) {
			return true
		}
	}
	return false
}

// runningPostScriptJobs counts the jobs holding a live AgentRun; they share
// the execution's parallelism bound with the task instances.
func (e *securityScanExecutionEngine) runningPostScriptJobs() int32 {
	var running int32
	for _, job := range e.exec.PostScriptJobs {
		if job.State == triggersv1alpha1.SecurityScanPostScriptStateRunning {
			running++
		}
	}
	return running
}

// workflowHasResearchPhase reports whether any task feeds a sink. It is the
// precondition for platform-executed post-scripts: the matrix is derived from
// the findings a research phase produced, so a workflow made entirely of sink
// tasks has nothing to materialize from. Derived from the effective workflow
// on every pass, never stored, so the answer survives a controller restart.
func (e *securityScanExecutionEngine) workflowHasResearchPhase() bool {
	sinks := securityScanSinkTasks(e.order)
	for _, task := range e.order {
		if !sinks[task.Name] {
			return true
		}
	}
	return false
}

// securityScanPostScriptJobGapPrefix is the source tag of every coverage gap
// ONE post-script job produced. Coverage gaps are plain strings in the CRD, so
// the prefix is the only durable provenance available: a resume that resets a
// job must retract exactly that job's gap and nothing else (fan-out
// truncations in particular can never be re-derived, since expandFanOuts skips
// already-expanded tasks).
func securityScanPostScriptJobGapPrefix(script, fingerprint string) string {
	return fmt.Sprintf("post-script %q did not complete for finding %s: ", script, fingerprint)
}

// securityScanPostScriptJobNames returns the pipeline snapshot, while keeping
// status written by older controllers (which only populated Script) runnable.
func securityScanPostScriptJobNames(job triggersv1alpha1.SecurityScanPostScriptJobStatus) []string {
	if len(job.Scripts) > 0 {
		return job.Scripts
	}
	if job.Script != "" {
		return []string{job.Script}
	}
	return nil
}

func securityScanPostScriptJobLabel(job triggersv1alpha1.SecurityScanPostScriptJobStatus) string {
	return strings.Join(securityScanPostScriptJobNames(job), ", ")
}

// securityScanRetractPostScriptJobGaps drops the coverage gaps the given jobs
// recorded, leaving every gap from another source intact.
func securityScanRetractPostScriptJobGaps(exec *triggersv1alpha1.SecurityScanExecutionStatus, jobs []triggersv1alpha1.SecurityScanPostScriptJobStatus) {
	if exec == nil || len(jobs) == 0 || len(exec.CoverageGaps) == 0 {
		return
	}
	prefixes := make([]string, 0, len(jobs))
	for _, job := range jobs {
		prefixes = append(prefixes, securityScanPostScriptJobGapPrefix(securityScanPostScriptJobLabel(job), job.Fingerprint))
	}
	kept := exec.CoverageGaps[:0]
	for _, gap := range exec.CoverageGaps {
		retract := false
		for _, prefix := range prefixes {
			// The stored gap may be truncated to the status size bound, so a
			// short gap is matched by the prefix it is a prefix OF.
			if strings.HasPrefix(gap, prefix) || strings.HasPrefix(prefix, gap) {
				retract = true
				break
			}
		}
		if !retract {
			kept = append(kept, gap)
		}
	}
	exec.CoverageGaps = kept
}

// postScripts returns the effective post-script list in the order preserved
// inside every per-finding pipeline.
func (e *securityScanExecutionEngine) postScripts() []triggersv1alpha1.SecurityScanPostScript {
	return e.resolved.spec.PostScripts
}

// materializePostScripts computes ordered post-script pipelines per finding
// exactly once per execution, after the research phase is over. runOn is
// evaluated here against the stable research result, so inapplicable scripts
// never inflate status or consume scheduler work. Each applicable sequence is
// snapshotted, then split into bounded jobs only when its prompt is oversized.
func (e *securityScanExecutionEngine) materializePostScripts(ctx context.Context) bool {
	exec := e.exec
	if len(e.postScripts()) == 0 || exec.PostScriptsMaterialized {
		return false
	}
	if !e.workflowHasResearchPhase() {
		// Every task is a sink, so there is no research phase whose end
		// defines "the findings exist now": materializing here would open the
		// gate on an empty matrix computed before any task ran, and the sink
		// prompt would then assert platform-executed post-scripts that never
		// happened. The matrix is declared vacuously done so the sink is not
		// deadlocked, the gap is disclosed, and the sink keeps the prose
		// post-script instructions (see taskInstanceContext) so the model
		// compensates for what the platform could not run.
		exec.PostScriptsMaterialized = true
		appendSecurityScanCoverageGap(exec, "post-scripts did not run as platform jobs: every workflow task is a terminal (sink) task, so no research phase precedes them and no findings existed to build the per-finding post-script pipeline list from")
		return true
	}
	sinks := securityScanSinkTasks(e.order)
	for _, entry := range exec.Tasks {
		if !sinks[entry.Name] && !securityScanTaskTerminal(entry.State) {
			return false
		}
	}
	if e.r.Findings == nil {
		// No Postgres: the findings the matrix selects on do not exist, so
		// the scan proceeds to its report with the gap stated explicitly
		// instead of blocking on jobs that can never be built.
		exec.PostScriptsMaterialized = true
		appendSecurityScanCoverageGap(exec, "post-scripts did not run: no finding store is configured, so the per-finding post-script pipeline list could not be materialized")
		return true
	}
	// One row past the pipeline cap is requested so a finding list that exactly
	// fills the cap is distinguishable from one the store truncated.
	listLimit := int32(triggersv1alpha1.MaxSecurityScanPostScriptJobs) + 1
	scripts := e.postScripts()
	type findingPipeline struct {
		finding store.SecurityFindingRecord
		scripts []string
	}
	eligible := make([]findingPipeline, 0, triggersv1alpha1.MaxSecurityScanPostScriptJobs+1)
	offset := int32(0)
	moreFindings := false
	for len(eligible) < int(listLimit) {
		findings, err := e.r.Findings.ListSecurityFindings(ctx, store.SecurityFindingFilter{
			Namespace:   e.scan.Namespace,
			ScanName:    e.scan.Name,
			ExecutionID: exec.ID,
			Limit:       listLimit,
			Offset:      offset,
		})
		if err != nil {
			logf.FromContext(ctx).Error(err, "listing findings to materialize post-scripts", "execution", exec.ID)
			return false // transient: the pipeline list is materialized on a later reconcile
		}
		for _, f := range findings {
			if f.DuplicateOf != nil {
				continue // a duplicate is validated through the finding it duplicates
			}
			selected := make([]string, 0, len(scripts))
			for _, script := range scripts {
				if matched, _ := securityScanPostScriptMatches(script.EffectiveRunOn(), f, e.payableSeverityFloor()); matched {
					selected = append(selected, script.Name)
				}
			}
			if len(selected) > 0 {
				eligible = append(eligible, findingPipeline{finding: f, scripts: selected})
			}
			if len(eligible) >= int(listLimit) {
				moreFindings = true
				break
			}
		}
		if len(findings) < int(listLimit) {
			break
		}
		offset += int32(len(findings)) //nolint:gosec // page size is bounded above
	}
	// Fingerprint order makes the selected pipelines reproducible: the store
	// orders by score and last-seen time, both of which verdicts mutate.
	sort.Slice(eligible, func(i, j int) bool { return eligible[i].finding.Fingerprint < eligible[j].finding.Fingerprint })

	selectedCount := 0
	keep := len(eligible)
	for i, pipeline := range eligible {
		if selectedCount+len(pipeline.scripts) > triggersv1alpha1.MaxSecurityScanPostScriptJobs {
			keep = i
			break
		}
		selectedCount += len(pipeline.scripts)
	}
	if keep < len(eligible) {
		// Bound total snapshotted script memberships to preserve the original
		// etcd object-size safety property; never split a finding's pipeline.
		total := strconv.Itoa(len(eligible))
		if moreFindings {
			total = "at least " + total
		}
		appendSecurityScanCoverageGap(exec, fmt.Sprintf("post-scripts ran on %d of %s eligible findings: the execution is capped at %d selected post-scripts across per-finding pipelines",
			keep, total, triggersv1alpha1.MaxSecurityScanPostScriptJobs))
		eligible = eligible[:keep]
	}
	jobs := make([]triggersv1alpha1.SecurityScanPostScriptJobStatus, 0, len(eligible))
	for _, pipeline := range eligible {
		f, selected := pipeline.finding, pipeline.scripts
		for order, chunk := range e.postScriptPipelineChunks(scripts, selected, f) {
			jobs = append(jobs, triggersv1alpha1.SecurityScanPostScriptJobStatus{
				Script:      chunk[0], // compatibility with clients predating Scripts
				Scripts:     chunk,
				Order:       int32(order), //nolint:gosec // selected scripts are bounded by the status cap
				FindingID:   f.ID.String(),
				Fingerprint: f.Fingerprint,
				State:       triggersv1alpha1.SecurityScanPostScriptStatePending,
			})
		}
	}
	exec.PostScriptJobs = jobs
	exec.PostScriptsMaterialized = true
	return true
}

// postScriptPipelineChunks greedily packs selected scripts into bounded seed
// messages without reordering or splitting one script. The exact rendered
// prompt is measured so target, event, scope, and finding context count too.
func (e *securityScanExecutionEngine) postScriptPipelineChunks(all []triggersv1alpha1.SecurityScanPostScript, selected []string, finding store.SecurityFindingRecord) [][]string {
	byName := make(map[string]triggersv1alpha1.SecurityScanPostScript, len(all))
	for _, script := range all {
		byName[script.Name] = script
	}
	var chunks [][]string
	var names []string
	var pipeline []triggersv1alpha1.SecurityScanPostScript
	for _, name := range selected {
		script := byName[name]
		// Actionable-only stages need an AgentRun boundary so a successful
		// predecessor can reject the finding before this stage is dispatched.
		if securityScanPostScriptRunOnActionable(script.EffectiveRunOn()) {
			if len(names) > 0 {
				chunks = append(chunks, append([]string(nil), names...))
			}
			chunks = append(chunks, []string{name})
			names = nil
			pipeline = nil
			continue
		}
		// Built-in bounty artifact stages must cross an AgentRun boundary so
		// builder, validator, and report provenance are independently durable.
		// Other post-scripts retain the normal prompt-packing behavior.
		if name == "report-writer" {
			if len(names) > 0 {
				chunks = append(chunks, append([]string(nil), names...))
			}
			chunks = append(chunks, []string{name})
			names = nil
			pipeline = nil
			continue
		}
		candidate := append(append([]triggersv1alpha1.SecurityScanPostScript(nil), pipeline...), script)
		prompt := BuildSecurityPostScriptPipelinePromptWithProgram(e.resolved.spec, securityScanPromptEvent(e.runCtx), candidate, securityPostScriptFinding(finding), e.resolved.program)
		if len(pipeline) > 0 && len(prompt) > securityScanMaxPostScriptPipelinePromptBytes {
			chunks = append(chunks, append([]string(nil), names...))
			names = []string{name}
			pipeline = []triggersv1alpha1.SecurityScanPostScript{script}
			continue
		}
		names = append(names, name)
		pipeline = candidate
	}
	if len(names) > 0 {
		chunks = append(chunks, append([]string(nil), names...))
	}
	return chunks
}

// observePostScripts folds terminal post-script AgentRun phases into the job
// entries, mirroring observe() for tasks. A succeeded job records the
// finding's resulting status: that reloaded status is the durable evidence
// the script actually reached a verdict, since the run itself keeps nothing.
func (e *securityScanExecutionEngine) observePostScripts(ctx context.Context) {
	for i := range e.exec.PostScriptJobs {
		job := &e.exec.PostScriptJobs[i]
		if job.State != triggersv1alpha1.SecurityScanPostScriptStateRunning {
			continue
		}
		run, err := e.getRun(ctx, job.RunName)
		if err != nil {
			continue // transient read error: retry next reconcile
		}
		if run == nil {
			e.recordPostScriptFailure(job, "post-script run disappeared before completing", triggersv1alpha1.SecurityScanTaskFailureRetryable)
			continue
		}
		// Paused is published before the AgentRun controller drains the old
		// sandbox. Keep the pipeline and sink gated until that worker can no
		// longer mutate the finding during shutdown.
		if run.Status.Phase == platformv1alpha1.AgentRunPhasePaused && run.Status.Sandbox != nil {
			continue
		}
		switch run.Status.Phase {
		case platformv1alpha1.AgentRunPhaseSucceeded:
			job.State = triggersv1alpha1.SecurityScanPostScriptStateSucceeded
			job.LastError = ""
			job.FinishedAt = &e.now
			job.Result = truncateSecurityScanError(e.postScriptVerdict(ctx, job))
		case platformv1alpha1.AgentRunPhaseFailed, platformv1alpha1.AgentRunPhaseCancelled:
			reason := securityScanAgentRunFailureReason(run, "post-script")
			e.recordPostScriptFailure(job, reason, classifySecurityScanTaskFailure(reason))
		case platformv1alpha1.AgentRunPhasePaused:
			// A pause requires a limit change to resume. Retrying the same
			// immutable scan attempt would carry the same exhausted limit and,
			// because Paused has no completion timestamp, bypass retry backoff.
			e.recordPostScriptFailure(job, securityScanAgentRunFailureReason(run, "post-script"),
				triggersv1alpha1.SecurityScanTaskFailureNonRetryable)
		}
	}
}

// postScriptVerdict reloads the finding once to describe what the job left
// behind. A read failure is not a job failure: the run succeeded, only the
// audit line is unavailable.
func (e *securityScanExecutionEngine) postScriptVerdict(ctx context.Context, job *triggersv1alpha1.SecurityScanPostScriptJobStatus) string {
	rec, err := e.loadPostScriptFinding(ctx, job)
	switch {
	case err != nil:
		return "completed; finding status could not be read back"
	case rec == nil:
		return "completed; the finding no longer exists"
	default:
		return fmt.Sprintf("completed; finding status is %q", rec.Status)
	}
}

// recordPostScriptFailure retries a retryable failure within the same
// per-task retry budget the task scheduler uses, or marks the job Failed. A
// terminal failure is a coverage gap, never an execution failure: a
// validation script that could not run must not discard the research results,
// but the report may no longer claim the finding was validated.
func (e *securityScanExecutionEngine) recordPostScriptFailure(job *triggersv1alpha1.SecurityScanPostScriptJobStatus, reason, class string) {
	reason = truncateSecurityScanError(reason)
	job.LastError = reason
	maxRetries := e.resolved.spec.EffectiveTaskMaxRetries(triggersv1alpha1.SecurityScanTask{})
	if class == triggersv1alpha1.SecurityScanTaskFailureRetryable && job.Attempts < 1+maxRetries {
		job.State = triggersv1alpha1.SecurityScanPostScriptStatePending
		return
	}
	job.State = triggersv1alpha1.SecurityScanPostScriptStateFailed
	job.FinishedAt = &e.now
	job.Result = truncateSecurityScanError("failed: " + reason)
	appendSecurityScanCoverageGap(e.exec, securityScanPostScriptJobGapPrefix(securityScanPostScriptJobLabel(*job), job.Fingerprint)+reason)
}

// dispatchPostScripts starts ordered pipeline chunks. Chunks for one finding
// are serialized; different findings run under the parallelism bound.
func (e *securityScanExecutionEngine) dispatchPostScripts(ctx context.Context) {
	if len(e.exec.PostScriptJobs) == 0 {
		return
	}
	scripts := make(map[string]triggersv1alpha1.SecurityScanPostScript, len(e.postScripts()))
	for _, script := range e.postScripts() {
		scripts[script.Name] = script
	}
	running := e.runningPostScriptJobs()
	for _, entry := range e.exec.Tasks {
		if entry.State == triggersv1alpha1.SecurityScanTaskStateRunning {
			running++
		}
	}
	parallelism := e.exec.EffectiveParallelism
	if parallelism <= 0 {
		parallelism = e.resolved.spec.EffectiveParallelism()
	}

	for i := range e.exec.PostScriptJobs {
		job := &e.exec.PostScriptJobs[i]
		if job.State != triggersv1alpha1.SecurityScanPostScriptStatePending {
			continue
		}
		if !e.postScriptPredecessorsTerminal(i) {
			continue
		}
		if running >= parallelism {
			continue
		}
		pipeline := make([]triggersv1alpha1.SecurityScanPostScript, 0, len(securityScanPostScriptJobNames(*job)))
		missing := ""
		for _, name := range securityScanPostScriptJobNames(*job) {
			script, ok := scripts[name]
			if !ok {
				missing = name
				break
			}
			pipeline = append(pipeline, script)
		}
		if missing != "" || len(pipeline) == 0 {
			// A snapshotted post-script was removed mid-execution; the
			// pipeline can never run, so it is terminal with the gap recorded.
			job.State = triggersv1alpha1.SecurityScanPostScriptStateFailed
			job.FinishedAt = &e.now
			job.Result = truncateSecurityScanError(fmt.Sprintf("failed: post-script %q no longer exists in the spec", missing))
			appendSecurityScanCoverageGap(e.exec, securityScanPostScriptJobGapPrefix(securityScanPostScriptJobLabel(*job), job.Fingerprint)+fmt.Sprintf("post-script %q was removed from the spec mid-execution", missing))
			continue
		}
		if !e.postScriptRetryReady(ctx, job) {
			continue
		}
		if e.budgets != nil && e.budgets.MaxModelJobs > 0 && e.totalAttempts()+e.sinkAttemptsToReserve() >= e.budgets.MaxModelJobs {
			// Post-script attempts count toward maxModelJobs, so a large
			// matrix would otherwise overshoot the budget and enforceBudgets
			// would then fail the WHOLE execution after the fact, discarding
			// research results that are already paid for. The reserved sink
			// attempts keep the last slots for the report: getting a report
			// out that discloses the coverage gap is strictly more valuable
			// than one more validation job, and spending the final slot here
			// would leave the sink schedulable at the cap, which
			// enforceBudgets turns into a whole-execution failure with no
			// report at all. The remaining jobs are instead marked terminal
			// here: leaving them Pending would hold postScriptsGateSink()
			// true forever, since no later pass can free budget.
			e.skipPostScriptsForBudget()
			return
		}
		if e.launchPostScript(ctx, job, pipeline) {
			running++
		}
	}
}

// sinkAttemptsToReserve is the number of model jobs post-script dispatch must
// leave unspent so the sink can still submit the report. Every sink instance
// that has not started yet needs at least one attempt; instances that are
// already Running (or terminal) have their attempt counted in totalAttempts
// and need no reservation, which matches exactly what enforceBudgets treats
// as schedulable work at the cap.
func (e *securityScanExecutionEngine) sinkAttemptsToReserve() int32 {
	sinks := securityScanSinkTasks(e.order)
	var reserved int32
	for _, entry := range e.exec.Tasks {
		if !sinks[entry.Name] {
			continue
		}
		switch entry.State {
		case triggersv1alpha1.SecurityScanTaskStatePending, triggersv1alpha1.SecurityScanTaskStateBlocked:
			reserved++
		}
	}
	return reserved
}

// skipPostScriptsForBudget terminates every job that has not started because
// budgets.maxModelJobs is exhausted, disclosing the un-run validation as one
// coverage gap rather than 200.
func (e *securityScanExecutionEngine) skipPostScriptsForBudget() {
	skipped := 0
	for i := range e.exec.PostScriptJobs {
		job := &e.exec.PostScriptJobs[i]
		if job.State != triggersv1alpha1.SecurityScanPostScriptStatePending {
			continue
		}
		job.State = triggersv1alpha1.SecurityScanPostScriptStateSkipped
		job.FinishedAt = &e.now
		job.Result = truncateSecurityScanError(fmt.Sprintf("skipped: budgets.maxModelJobs %d is exhausted", e.budgets.MaxModelJobs))
		skipped++
	}
	if skipped > 0 {
		appendSecurityScanCoverageGap(e.exec, fmt.Sprintf("%d post-script job(s) never ran: the execution reached budgets.maxModelJobs %d", skipped, e.budgets.MaxModelJobs))
	}
}

// postScriptPredecessorsTerminal preserves compatibility with an execution
// materialized by an older controller as one job per script.
func (e *securityScanExecutionEngine) postScriptPredecessorsTerminal(index int) bool {
	job := e.exec.PostScriptJobs[index]
	for _, other := range e.exec.PostScriptJobs {
		if other.FindingID != job.FindingID || other.Order >= job.Order {
			continue
		}
		if !triggersv1alpha1.SecurityScanPostScriptJobTerminal(other.State) {
			return false
		}
	}
	return true
}

// postScriptRetryReady spaces retries with the engine's exponential backoff.
// The deadline is derived from the failed run itself rather than from status:
// the job entry carries no retry timestamp, and re-deriving from cluster
// state keeps the decision identical across controller restarts.
func (e *securityScanExecutionEngine) postScriptRetryReady(ctx context.Context, job *triggersv1alpha1.SecurityScanPostScriptJobStatus) bool {
	if job.Attempts == 0 {
		return true
	}
	run, err := e.getRun(ctx, job.RunName)
	if err != nil {
		return false
	}
	if run == nil {
		return true // no run to measure from: the previous attempt is gone
	}
	last := run.CreationTimestamp.Time
	if run.Status.CompletedAt != nil {
		last = run.Status.CompletedAt.Time
	}
	ready := last.Add(securityScanRetryBackoff(e.resolved.spec.EffectiveRetryBackoff(), job.Attempts))
	delay := ready.Sub(e.now.Time)
	if delay <= 0 {
		return true
	}
	if e.postScriptRequeue == 0 || delay < e.postScriptRequeue {
		e.postScriptRequeue = delay
	}
	return false
}

// launchPostScript creates the AgentRun serving one snapshotted pipeline
// chunk. runOn was already evaluated during materialization.
func (e *securityScanExecutionEngine) launchPostScript(ctx context.Context, job *triggersv1alpha1.SecurityScanPostScriptJobStatus, scripts []triggersv1alpha1.SecurityScanPostScript) bool {
	log := logf.FromContext(ctx)
	rec, err := e.loadPostScriptFinding(ctx, job)
	if err != nil {
		log.Error(err, "reading finding to dispatch post-script pipeline", "scripts", securityScanPostScriptJobLabel(*job), "fingerprint", job.Fingerprint)
		return false // transient: re-evaluated on the next reconcile
	}
	if rec == nil {
		job.State = triggersv1alpha1.SecurityScanPostScriptStateSkipped
		job.FinishedAt = &e.now
		job.Result = "skipped: the finding no longer exists"
		return false
	}
	if job.Attempts == 0 && securityScanPostScriptsActionableOnly(scripts) &&
		e.postScriptHasSuccessfulPredecessor(*job) && securityScanFindingHasTerminalStatus(rec.Status) {
		job.State = triggersv1alpha1.SecurityScanPostScriptStateSkipped
		job.FinishedAt = &e.now
		job.Result = fmt.Sprintf("skipped: finding already has terminal status %q", rec.Status)
		return false
	}
	if len(job.Scripts) == 0 && len(scripts) == 1 {
		// Executions materialized by older controllers decided runOn at
		// dispatch time. Preserve that contract while they drain during a
		// rolling upgrade; new pipelines always carry a non-nil Scripts list.
		if matched, reason := securityScanPostScriptMatches(scripts[0].EffectiveRunOn(), *rec, e.payableSeverityFloor()); !matched {
			job.State = triggersv1alpha1.SecurityScanPostScriptStateSkipped
			job.FinishedAt = &e.now
			job.Result = truncateSecurityScanError(reason)
			return false
		}
	}
	attempt := job.Attempts + 1
	runKey := "pipeline"
	if len(job.Scripts) == 0 {
		runKey = job.Script
	} else if job.Order > 0 {
		runKey = fmt.Sprintf("pipeline-%d", job.Order)
	}
	runName := securityScanPostScriptRunName(e.scan.Name, e.exec.ID, runKey, job.FindingID, attempt, e.exec.LastResumeToken)
	if _, err := e.r.createScanPostScriptRun(ctx, e.scan, e.resolved, e.runCtx, e.exec, scripts, *rec, runName); err != nil {
		log.Error(err, "failed to create post-script pipeline AgentRun", "scripts", securityScanPostScriptJobLabel(*job), "run", runName)
		// The attempt is consumed even though no run exists: create errors
		// are classified retryable by default, so a permanently failing
		// create (admission rejection, quota, conflict) on a job whose
		// Attempts never advanced would re-dispatch forever, keep
		// postScriptsGateSink() true, and deadlock the sink. Counting it also
		// puts the loop under budgets.maxModelJobs, which only sums attempts.
		job.Attempts = attempt
		job.RunName = "" // no run exists: retry backoff has nothing to measure from
		e.recordPostScriptFailure(job, err.Error(), classifySecurityScanTaskFailure(err.Error()))
		return false
	}
	job.Attempts = attempt
	job.RunName = runName
	job.State = triggersv1alpha1.SecurityScanPostScriptStateRunning
	if job.StartedAt == nil {
		job.StartedAt = &e.now
	}
	return true
}

func securityScanFindingHasTerminalStatus(status string) bool {
	return status == store.SecurityFindingStatusFalsePositive ||
		status == store.SecurityFindingStatusAcceptedRisk ||
		status == store.SecurityFindingStatusFixed
}

func securityScanPostScriptsActionableOnly(scripts []triggersv1alpha1.SecurityScanPostScript) bool {
	return len(scripts) == 1 && securityScanPostScriptRunOnActionable(scripts[0].EffectiveRunOn())
}

// securityScanPostScriptRunOnActionable reports whether a runOn selector
// carries the 'actionable' semantics: the stage is skipped once a successful
// predecessor has already settled the finding.
func securityScanPostScriptRunOnActionable(runOn string) bool {
	return runOn == "high-and-above-actionable" || runOn == "medium-and-above-actionable"
}

func (e *securityScanExecutionEngine) postScriptHasSuccessfulPredecessor(job triggersv1alpha1.SecurityScanPostScriptJobStatus) bool {
	for _, other := range e.exec.PostScriptJobs {
		if other.FindingID == job.FindingID && other.Order < job.Order && other.State == triggersv1alpha1.SecurityScanPostScriptStateSucceeded {
			return true
		}
	}
	return false
}

// loadPostScriptFinding reloads the job's finding. A (nil, nil) result means
// the finding no longer exists.
func (e *securityScanExecutionEngine) loadPostScriptFinding(ctx context.Context, job *triggersv1alpha1.SecurityScanPostScriptJobStatus) (*store.SecurityFindingRecord, error) {
	if e.r.Findings == nil {
		return nil, fmt.Errorf("no finding store is configured")
	}
	id, err := uuid.Parse(job.FindingID)
	if err != nil {
		return nil, nil
	}
	return e.r.Findings.GetSecurityFinding(ctx, e.scan.Namespace, id)
}

// securityProgramPayableFloor returns the lowest severity the governing program
// itself publishes, which is the only severity floor that means anything: a
// program that publishes medium impacts pays for mediums, one that publishes
// only high and critical does not. Without a governing program there is no
// table to read, so the conservative high floor stands. It mirrors the floor
// the packaging tool enforces, so a stage is never dispatched for a finding
// the bundle gate would refuse afterwards.
func securityProgramPayableFloor(program *triggersv1alpha1.SecurityProgramSpec) string {
	floor := security.SeverityHigh
	if program == nil || len(program.InScopeImpacts) == 0 {
		return floor
	}
	// Dispatch and packaging must read the SAME scope state. The packaging
	// tool sees the encoded annotation and refuses to treat a truncated list
	// as authoritative, so a floor derived here from the complete spec would
	// dispatch mediums the bundle gate then rejects. Deriving it from the
	// encoded annotations keeps the two sides in agreement by construction.
	annotations := securityProgramScopeAnnotations(program)
	if annotations[triggersv1alpha1.SecurityScanProgramImpactsTruncatedAnnotation] == "true" {
		return floor
	}
	lowest := -1
	for line := range strings.SplitSeq(annotations[triggersv1alpha1.SecurityScanProgramImpactsAnnotation], "\n") {
		level, _, found := strings.Cut(line, "\t")
		if !found {
			continue
		}
		rank := security.SeverityRank(strings.TrimSpace(level))
		if rank < 0 {
			continue
		}
		if lowest < 0 || rank < lowest {
			lowest, floor = rank, strings.TrimSpace(level)
		}
	}
	return floor
}

// securityScanPostScriptMatches evaluates a post-script's runOn predicate
// against a finding, returning the reason on a non-match so the skip states
// which status or severity failed the selector. payableFloor is the governing
// program's own lowest published severity; the medium selector never dispatches
// below it, because two expensive AgentRuns whose result the bundle gate will
// refuse are worse than not running them.
func securityScanPostScriptMatches(runOn string, rec store.SecurityFindingRecord, payableFloor string) (bool, string) {
	switch runOn {
	case "confirmed":
		if rec.Status != store.SecurityFindingStatusConfirmed {
			return false, fmt.Sprintf("skipped: runOn %q does not match the finding's current status %q", runOn, rec.Status)
		}
	case "high-and-above", "high-and-above-actionable":
		if !security.SeverityAtLeast(rec.Severity, security.SeverityHigh) {
			return false, fmt.Sprintf("skipped: runOn %q does not match the finding's current severity %q (status %q)", runOn, rec.Severity, rec.Status)
		}
	case "medium-and-above-actionable":
		floor := security.SeverityMedium
		if payableFloor != "" && security.SeverityRank(payableFloor) > security.SeverityRank(floor) {
			floor = payableFloor
		}
		if !security.SeverityAtLeast(rec.Severity, floor) {
			return false, fmt.Sprintf("skipped: runOn %q does not match the finding's current severity %q at the governing program's lowest published severity %q (status %q)", runOn, rec.Severity, floor, rec.Status)
		}
	}
	return true, ""
}

// securityScanPostScriptRunName derives the deterministic post-script run
// name secscan-<scan>-<execution>-ps-<script>-<findingID>[-r<attempt>][-z<resume>],
// bounded to 63 characters with a stable hash suffix like
// securityScanTaskRunName. The finding's id, not its fingerprint, is the
// identity component: a fingerprint is only unique within (namespace, scan,
// repository), and CreateTriggerRun swallows AlreadyExists, so two
// same-fingerprint findings from different repositories would otherwise bind
// to ONE AgentRun and record a verdict for a script that never ran. The
// attempt is part of the name so a retry never collides with the run it
// replaces, and the whole name is a pure function of durable status so a
// restarted controller re-derives it instead of double-dispatching.
func securityScanPostScriptRunName(scanName, executionID, scriptName, findingID string, attempt int32, resumeToken string) string {
	sanitize := func(s string) string {
		s = cronNonAlphaNum.ReplaceAllString(strings.ToLower(s), "-")
		return strings.Trim(s, "-")
	}
	name := "secscan-" + sanitize(scanName) + "-" + sanitize(executionID) + "-ps-" + sanitize(scriptName) + "-" + sanitize(findingID)
	if attempt > 1 {
		name += fmt.Sprintf("-r%d", attempt)
	}
	if resumeToken != "" {
		hashBytes := sha1.Sum([]byte(resumeToken))
		name += "-z" + hex.EncodeToString(hashBytes[:])[:5]
	}
	if len(name) <= 63 {
		return name
	}
	hashBytes := sha1.Sum([]byte(name))
	hash := hex.EncodeToString(hashBytes[:])[:8]
	truncated := strings.TrimRight(name[:63-len(hash)-1], "-.")
	return truncated + "-" + hash
}

// securityScanPostScriptAttemptRunNames re-derives the run name of every
// attempt a job has made. launchPostScript OVERWRITES job.RunName on each
// retry and a post-script job deliberately carries no retry-history list, so
// the earlier runs are recovered by enumeration rather than from status: the
// names are a pure function of (scan, execution, script, finding, attempt),
// which is exactly what makes storing them unnecessary. An attempt consumed
// by a dispatch failure never created an AgentRun, so its name simply
// resolves to no run and the caller skips it.
func securityScanPostScriptAttemptRunNames(scanName, executionID string, job triggersv1alpha1.SecurityScanPostScriptJobStatus, resumeToken string) []string {
	names := make([]string, 0, job.Attempts)
	scriptName := job.Script
	if len(job.Scripts) > 0 {
		scriptName = "pipeline"
		if job.Order > 0 {
			scriptName = fmt.Sprintf("pipeline-%d", job.Order)
		}
	}
	for attempt := int32(1); attempt <= job.Attempts; attempt++ {
		names = append(names, securityScanPostScriptRunName(scanName, executionID, scriptName, job.FindingID, attempt, resumeToken))
	}
	return names
}

// createScanPostScriptRun creates one post-script job AgentRun. It shares the
// task-run construction path (defaults, labels, owner ref, event context,
// policy annotations, task mode template) so a post-script run is governed
// exactly like the research runs, and adds the post-script and finding
// annotations that bind the run to its job. The tool policy is narrowed by
// postScriptToolPolicy.
func (r *SecurityScanReconciler) createScanPostScriptRun(ctx context.Context, scan *triggersv1alpha1.SecurityScan, resolved *resolvedSecurityScanSpec, runCtx *securityScanRunContext, exec *triggersv1alpha1.SecurityScanExecutionStatus, scripts []triggersv1alpha1.SecurityScanPostScript, rec store.SecurityFindingRecord, runName string) (bool, error) {
	base, err := r.buildScanRunBase(ctx, scan, resolved, runName, runCtx, "")
	if err != nil {
		return false, err
	}
	annotations := base.annotations
	// The execution id keeps the run inside the same finding campaign as the
	// research runs, so its update lands on the findings this execution owns.
	annotations[triggersv1alpha1.SecurityScanExecutionIDAnnotation] = exec.ID
	names := make([]string, 0, len(scripts))
	for _, script := range scripts {
		names = append(names, script.Name)
	}
	annotations[triggersv1alpha1.SecurityScanPostScriptAnnotation] = strings.Join(names, ",")
	annotations[triggersv1alpha1.SecurityScanPostScriptFindingAnnotation] = rec.Fingerprint
	maps.Copy(annotations, securityProgramScopeAnnotations(resolved.program))

	modeRef := base.modeRef
	if scan.Spec.Defaults.ModeRef == nil {
		modeRef = &platformv1alpha1.ModeRef{Name: securityScanTaskModeTemplate}
	}

	toolPolicy := r.postScriptToolPolicy(ctx, resolved)
	if slices.Contains(names, "poc-validator") {
		toolPolicy.DeniedTools = append(toolPolicy.DeniedTools,
			"Browser", "WebFetch", "run_security_tool", "git_push", "create_pull_request")
	}

	created, _, err := CreateTriggerRun(ctx, r.Client, r.StateStore, TriggerRunSpec{
		RunName:            runName,
		Namespace:          scan.Namespace,
		TriggerKind:        securityScanKind,
		TriggerName:        scan.Name,
		ExternalID:         exec.ID,
		ExternalIdentifier: fmt.Sprintf("%s/post-script-pipeline/%s", exec.ID, rec.ID.String()),
		SeedMessage:        BuildSecurityPostScriptPipelinePromptWithProgram(resolved.spec, securityScanPromptEvent(runCtx), scripts, securityPostScriptFinding(rec), resolved.program),
		Revision:           base.revision,
		Defaults:           base.defaults,
		OwnerRef:           scan,
		Scheme:             r.Scheme,
		Labels:             map[string]string{securityScanLabel: securityScanLabelValue(scan.Name)},
		Annotations:        annotations,
		Context: &platformv1alpha1.AgentRunContext{
			ProjectRef: &platformv1alpha1.ProjectRef{Kind: securityScanKind, Name: scan.Name},
		},
		ModeRef:       modeRef,
		Limits:        base.limits,
		ToolPolicy:    toolPolicy,
		SeedLogPrefix: "securityscan",
	})
	return created, err
}

// postScriptToolPolicy builds the post-script run's tool policy:
// submit_security_scan_report is denied outright (only the sink submits the
// scan-wide report; a post-script that submitted one would finalize a scan
// whose remaining jobs have not run), then the SINK task's role tool-access
// narrowing is applied on top. The sink's role is the right contract because
// post-scripts used to run inside the sink run: keeping write/forge tools a
// read-only role denies there would silently widen access the operator
// already withheld. An unresolvable role narrows to read-only rather than
// granting writes — the sink dispatch fails on that role anyway.
func (r *SecurityScanReconciler) postScriptToolPolicy(ctx context.Context, resolved *resolvedSecurityScanSpec) *platformv1alpha1.AgentRunToolPolicy {
	policy := &platformv1alpha1.AgentRunToolPolicy{DeniedTools: []string{"submit_security_scan_report"}}
	workflow := resolved.spec.Workflow
	sinks := securityScanSinkTasks(workflow)
	for _, task := range workflow {
		if !sinks[task.Name] {
			continue
		}
		// The first sink in workflow order decides: a multi-sink workflow has
		// no single owning role, and the narrowest useful answer is a stable,
		// deterministic one rather than a per-dispatch choice.
		role, err := r.resolveSecurityScanTaskRole(ctx, task)
		if err != nil {
			logf.FromContext(ctx).Error(err, "resolving the sink role for a post-script run; denying write tools", "task", task.Name)
			break
		}
		return securityScanApplyRoleToolAccess(policy, role)
	}
	return securityScanApplyRoleToolAccess(policy, securityScanTaskRole{spec: platformv1alpha1.RoleInstructionSpec{ToolAccess: "read-only"}})
}

// securityPostScriptFinding projects the stored finding into the compact
// prompt rendering: identity, current triage state, and the evidence the
// script needs to reach a verdict.
func securityPostScriptFinding(rec store.SecurityFindingRecord) SecurityPostScriptFinding {
	location := rec.FilePath
	if location != "" && rec.StartLine > 0 {
		location += ":" + strconv.Itoa(int(rec.StartLine))
		if rec.EndLine > rec.StartLine {
			location += "-" + strconv.Itoa(int(rec.EndLine))
		}
	}
	if rec.Symbol != "" {
		if location != "" {
			location += " "
		}
		location += "(" + rec.Symbol + ")"
	}
	return SecurityPostScriptFinding{
		Fingerprint: rec.Fingerprint,
		ID:          rec.ID.String(),
		Title:       rec.Title,
		Category:    rec.Category,
		Severity:    rec.Severity,
		Status:      rec.Status,
		Location:    location,
		Description: rec.Description,
		Impact:      rec.Impact,
	}
}

// postScriptRequeueDelay returns the earliest post-script retry delay this
// pass computed, or 0 when the default poll interval suffices.
func (e *securityScanExecutionEngine) postScriptRequeueDelay() time.Duration {
	if e.postScriptRequeue > 0 && e.postScriptRequeue < securityScanExecutionPollInterval {
		return e.postScriptRequeue
	}
	return 0
}

// postScriptJobsInFlight reports whether any job still owes work, so the
// execution keeps polling instead of declaring itself Succeeded.
func (e *securityScanExecutionEngine) postScriptJobsInFlight() bool {
	if len(e.postScripts()) > 0 && !e.exec.PostScriptsMaterialized {
		return true
	}
	for _, job := range e.exec.PostScriptJobs {
		if !triggersv1alpha1.SecurityScanPostScriptJobTerminal(job.State) {
			return true
		}
	}
	return false
}

// payableSeverityFloor is the governing program's lowest published severity for
// this execution.
func (e *securityScanExecutionEngine) payableSeverityFloor() string {
	if e == nil || e.resolved == nil {
		return security.SeverityHigh
	}
	return securityProgramPayableFloor(e.resolved.program)
}

// securityProgramScopeAnnotations copies the governing program's typed scope
// facts onto a run so the packaging tool can enforce them in-process. The
// impact list is operator-verified configuration, so a submission that names
// an impact outside it is refused rather than argued with. The list is
// truncated at a byte bound to stay well inside the annotation size limit.
func securityProgramScopeAnnotations(program *triggersv1alpha1.SecurityProgramSpec) map[string]string {
	if program == nil {
		return nil
	}
	out := make(map[string]string, 3)
	if system := strings.TrimSpace(program.SeveritySystem); system != "" {
		out[triggersv1alpha1.SecurityScanProgramSeveritySystemAnnotation] = system
	}
	if budget := program.SubmissionBudget; budget != nil && budget.MaxPerPeriod > 0 {
		out[triggersv1alpha1.SecurityScanProgramSubmissionBudgetAnnotation] = strconv.FormatInt(int64(budget.MaxPerPeriod), 10)
		if budget.PeriodDays > 0 {
			out[triggersv1alpha1.SecurityScanProgramSubmissionPeriodAnnotation] = strconv.FormatInt(int64(budget.PeriodDays), 10)
		}
	}
	var impacts strings.Builder
	truncated := false
	for _, impact := range program.InScopeImpacts {
		clause := strings.TrimSpace(impact.Impact)
		level := strings.TrimSpace(impact.Level)
		if clause == "" || level == "" || strings.ContainsAny(clause, "\n\t") {
			// A clause that cannot be encoded verbatim is dropped, and the
			// list is no longer complete.
			truncated = true
			continue
		}
		line := level + "\t" + clause + "\n"
		if impacts.Len()+len(line) > triggersv1alpha1.MaxSecurityScanProgramImpactsAnnotationBytes {
			truncated = true
			continue
		}
		impacts.WriteString(line)
	}
	if impacts.Len() != 0 {
		out[triggersv1alpha1.SecurityScanProgramImpactsAnnotation] = impacts.String()
	}
	// A partial list must never become an authoritative allowlist: it can
	// still tell a consumer the severity of a clause it contains, but it
	// cannot prove that a clause the program published is absent.
	if truncated && impacts.Len() != 0 {
		out[triggersv1alpha1.SecurityScanProgramImpactsTruncatedAnnotation] = "true"
	}
	return out
}
