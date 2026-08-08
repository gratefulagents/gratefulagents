package triggers

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
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
// all were model-discretionary and unauditable. The engine instead
// materializes the finding x post-script matrix once per execution and runs
// each cell as its own AgentRun whose terminal state gates the final report.
const (
	// securityScanMaxCoverageGaps mirrors the CRD's MaxItems on
	// status.lastExecution.coverageGaps; exceeding it would make the status
	// write fail validation, so the last slot is spent on an overflow notice.
	securityScanMaxCoverageGaps = 50
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

// securityScanRetractPostScriptJobGaps drops the coverage gaps the given jobs
// recorded, leaving every gap from another source intact.
func securityScanRetractPostScriptJobGaps(exec *triggersv1alpha1.SecurityScanExecutionStatus, jobs []triggersv1alpha1.SecurityScanPostScriptJobStatus) {
	if exec == nil || len(jobs) == 0 || len(exec.CoverageGaps) == 0 {
		return
	}
	prefixes := make([]string, 0, len(jobs))
	for _, job := range jobs {
		prefixes = append(prefixes, securityScanPostScriptJobGapPrefix(job.Script, job.Fingerprint))
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

// postScripts returns the effective post-script list in spec order; the index
// is the job order that serializes scripts per finding.
func (e *securityScanExecutionEngine) postScripts() []triggersv1alpha1.SecurityScanPostScript {
	return e.resolved.spec.PostScripts
}

// materializePostScripts computes the ordered finding x post-script matrix
// exactly once per execution, after the research phase is over: findings only
// exist once the non-sink tasks are terminal, and re-deriving the matrix later
// would re-run scripts whose verdicts already changed the findings they
// selected on. Jobs are ordered by finding and then by the script's spec
// index, so a later script for one finding observes the status an earlier one
// set.
func (e *securityScanExecutionEngine) materializePostScripts(ctx context.Context) {
	exec := e.exec
	if len(e.postScripts()) == 0 || exec.PostScriptsMaterialized {
		return
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
		appendSecurityScanCoverageGap(exec, "post-scripts did not run as platform jobs: every workflow task is a terminal (sink) task, so no research phase precedes them and no findings existed to build the finding x post-script matrix from")
		return
	}
	sinks := securityScanSinkTasks(e.order)
	for _, entry := range exec.Tasks {
		if !sinks[entry.Name] && !securityScanTaskTerminal(entry.State) {
			return
		}
	}
	if e.r.Findings == nil {
		// No Postgres: the findings the matrix selects on do not exist, so
		// the scan proceeds to its report with the gap stated explicitly
		// instead of blocking on jobs that can never be built.
		exec.PostScriptsMaterialized = true
		appendSecurityScanCoverageGap(exec, "post-scripts did not run: no finding store is configured, so the finding x post-script matrix could not be materialized")
		return
	}
	// One row past the job cap is requested so a finding list that exactly
	// fills the cap is distinguishable from one the store truncated: with a
	// single post-script the two numbers coincide, and listing exactly the cap
	// would silently drop every finding past it with no gap recorded.
	listLimit := int32(triggersv1alpha1.MaxSecurityScanPostScriptJobs) + 1
	findings, err := e.r.Findings.ListSecurityFindings(ctx, store.SecurityFindingFilter{
		Namespace:   e.scan.Namespace,
		ScanName:    e.scan.Name,
		ExecutionID: exec.ID,
		Limit:       listLimit,
	})
	if err != nil {
		logf.FromContext(ctx).Error(err, "listing findings to materialize post-scripts", "execution", exec.ID)
		return // transient: the matrix is materialized on a later reconcile
	}
	eligible := make([]store.SecurityFindingRecord, 0, len(findings))
	for _, f := range findings {
		if f.DuplicateOf != nil {
			continue // a duplicate is validated through the finding it duplicates
		}
		eligible = append(eligible, f)
	}
	// Fingerprint order makes the matrix reproducible: the store orders by
	// score and last-seen time, both of which post-script verdicts mutate.
	sort.Slice(eligible, func(i, j int) bool { return eligible[i].Fingerprint < eligible[j].Fingerprint })

	scripts := e.postScripts()
	if maxFindings := triggersv1alpha1.MaxSecurityScanPostScriptJobs / len(scripts); len(eligible) > maxFindings {
		// Truncate whole findings, never a finding's script sequence: a
		// half-run sequence would leave a verdict the next script expects.
		// The count is a lower bound when the store itself capped the list.
		total := strconv.Itoa(len(eligible))
		if len(findings) >= int(listLimit) {
			total = "at least " + total
		}
		appendSecurityScanCoverageGap(exec, fmt.Sprintf("post-scripts ran on %d of %s eligible findings: the execution is capped at %d finding x post-script jobs",
			maxFindings, total, triggersv1alpha1.MaxSecurityScanPostScriptJobs))
		eligible = eligible[:maxFindings]
	}
	jobs := make([]triggersv1alpha1.SecurityScanPostScriptJobStatus, 0, len(eligible)*len(scripts))
	for _, f := range eligible {
		for i, script := range scripts {
			jobs = append(jobs, triggersv1alpha1.SecurityScanPostScriptJobStatus{
				Script:      script.Name,
				Order:       int32(i), //nolint:gosec // bounded by the CRD's postScripts item limit
				FindingID:   f.ID.String(),
				Fingerprint: f.Fingerprint,
				State:       triggersv1alpha1.SecurityScanPostScriptStatePending,
			})
		}
	}
	exec.PostScriptJobs = jobs
	exec.PostScriptsMaterialized = true
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
		switch run.Status.Phase {
		case platformv1alpha1.AgentRunPhaseSucceeded:
			job.State = triggersv1alpha1.SecurityScanPostScriptStateSucceeded
			job.LastError = ""
			job.FinishedAt = &e.now
			job.Result = truncateSecurityScanError(e.postScriptVerdict(ctx, job))
		case platformv1alpha1.AgentRunPhaseFailed, platformv1alpha1.AgentRunPhaseCancelled:
			reason := strings.TrimSpace(run.Status.LastError)
			if reason == "" {
				reason = "post-script run " + strings.ToLower(string(run.Status.Phase))
			}
			e.recordPostScriptFailure(job, reason, classifySecurityScanTaskFailure(reason))
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
	appendSecurityScanCoverageGap(e.exec, securityScanPostScriptJobGapPrefix(job.Script, job.Fingerprint)+reason)
}

// dispatchPostScripts starts the jobs whose turn it is. Per-finding
// serialization is the ordering contract: every lower-order job for the SAME
// finding must be terminal first, while different findings run in parallel
// under the execution's parallelism bound.
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
		script, ok := scripts[job.Script]
		if !ok {
			// The post-script was removed from the spec mid-execution; the
			// job can never run, so it is terminal with the gap recorded.
			job.State = triggersv1alpha1.SecurityScanPostScriptStateFailed
			job.FinishedAt = &e.now
			job.Result = truncateSecurityScanError(fmt.Sprintf("failed: post-script %q no longer exists in the spec", job.Script))
			appendSecurityScanCoverageGap(e.exec, securityScanPostScriptJobGapPrefix(job.Script, job.Fingerprint)+"it was removed from the spec mid-execution")
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
		if e.launchPostScript(ctx, job, script) {
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

// postScriptPredecessorsTerminal reports whether every lower-order job for
// the same finding has finished.
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

// launchPostScript re-evaluates runOn against the CURRENT finding and, on a
// match, creates the job's AgentRun. Re-evaluating at dispatch is the point of
// the ordering contract: an earlier script may have set the very status a
// later script selects on, so the predicate is never decided at
// materialization time.
func (e *securityScanExecutionEngine) launchPostScript(ctx context.Context, job *triggersv1alpha1.SecurityScanPostScriptJobStatus, script triggersv1alpha1.SecurityScanPostScript) bool {
	log := logf.FromContext(ctx)
	rec, err := e.loadPostScriptFinding(ctx, job)
	if err != nil {
		log.Error(err, "reading finding to dispatch post-script", "script", script.Name, "fingerprint", job.Fingerprint)
		return false // transient: re-evaluated on the next reconcile
	}
	if rec == nil {
		job.State = triggersv1alpha1.SecurityScanPostScriptStateSkipped
		job.FinishedAt = &e.now
		job.Result = "skipped: the finding no longer exists"
		return false
	}
	if matched, reason := securityScanPostScriptMatches(script.EffectiveRunOn(), *rec); !matched {
		job.State = triggersv1alpha1.SecurityScanPostScriptStateSkipped
		job.FinishedAt = &e.now
		job.Result = truncateSecurityScanError(reason)
		return false
	}

	attempt := job.Attempts + 1
	runName := securityScanPostScriptRunName(e.scan.Name, e.exec.ID, script.Name, job.FindingID, attempt, e.exec.LastResumeToken)
	if _, err := e.r.createScanPostScriptRun(ctx, e.scan, e.resolved, e.runCtx, e.exec, script, *rec, runName); err != nil {
		log.Error(err, "failed to create post-script AgentRun", "script", script.Name, "run", runName)
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

// securityScanPostScriptMatches evaluates a post-script's runOn predicate
// against a finding, returning the reason on a non-match so the skip states
// which status or severity failed the selector.
func securityScanPostScriptMatches(runOn string, rec store.SecurityFindingRecord) (bool, string) {
	switch runOn {
	case "confirmed":
		if rec.Status != store.SecurityFindingStatusConfirmed {
			return false, fmt.Sprintf("skipped: runOn %q does not match the finding's current status %q", runOn, rec.Status)
		}
	case "high-and-above":
		if !security.SeverityAtLeast(rec.Severity, security.SeverityHigh) {
			return false, fmt.Sprintf("skipped: runOn %q does not match the finding's current severity %q (status %q)", runOn, rec.Severity, rec.Status)
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
	for attempt := int32(1); attempt <= job.Attempts; attempt++ {
		names = append(names, securityScanPostScriptRunName(scanName, executionID, job.Script, job.FindingID, attempt, resumeToken))
	}
	return names
}

// createScanPostScriptRun creates one post-script job AgentRun. It shares the
// task-run construction path (defaults, labels, owner ref, event context,
// policy annotations, task mode template) so a post-script run is governed
// exactly like the research runs, and adds the post-script and finding
// annotations that bind the run to its job. The tool policy is narrowed by
// postScriptToolPolicy.
func (r *SecurityScanReconciler) createScanPostScriptRun(ctx context.Context, scan *triggersv1alpha1.SecurityScan, resolved *resolvedSecurityScanSpec, runCtx *securityScanRunContext, exec *triggersv1alpha1.SecurityScanExecutionStatus, script triggersv1alpha1.SecurityScanPostScript, rec store.SecurityFindingRecord, runName string) (bool, error) {
	base, err := r.buildScanRunBase(ctx, scan, resolved, runName, runCtx, "")
	if err != nil {
		return false, err
	}
	annotations := base.annotations
	// The execution id keeps the run inside the same finding campaign as the
	// research runs, so its update lands on the findings this execution owns.
	annotations[triggersv1alpha1.SecurityScanExecutionIDAnnotation] = exec.ID
	annotations[triggersv1alpha1.SecurityScanPostScriptAnnotation] = script.Name
	annotations[triggersv1alpha1.SecurityScanPostScriptFindingAnnotation] = rec.Fingerprint

	modeRef := base.modeRef
	if scan.Spec.Defaults.ModeRef == nil {
		modeRef = &platformv1alpha1.ModeRef{Name: securityScanTaskModeTemplate}
	}

	created, _, err := CreateTriggerRun(ctx, r.Client, r.StateStore, TriggerRunSpec{
		RunName:            runName,
		Namespace:          scan.Namespace,
		TriggerKind:        securityScanKind,
		TriggerName:        scan.Name,
		ExternalID:         exec.ID,
		ExternalIdentifier: fmt.Sprintf("%s/post-script/%s/%s", exec.ID, script.Name, rec.ID.String()),
		SeedMessage:        BuildSecurityPostScriptPrompt(resolved.spec, securityScanPromptEvent(runCtx), script, securityPostScriptFinding(rec)),
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
		ToolPolicy:    r.postScriptToolPolicy(ctx, resolved),
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
	workflow := resolved.spec.EffectiveWorkflow()
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
