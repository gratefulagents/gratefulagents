package triggers

import (
	"fmt"
	"strings"

	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/security"
)

// SecurityScanPromptEvent is the repository-event context rendered into an
// event-triggered scan prompt: the trigger source, the platform-stamped
// revision, and the diff scope (changed files or an explicit full-scan
// fallback statement).
type SecurityScanPromptEvent struct {
	Source       string
	Repository   string
	Revision     string
	BaseRevision string
	Branch       string
	PRNumber     int
	PRURL        string
	Fork         bool
	HeadRepo     string
	DiffScope    bool
	ChangedFiles []string
	DiffFallback string
}

// securityScanPromptEvent converts a run context into the prompt event.
func securityScanPromptEvent(runCtx *securityScanRunContext) *SecurityScanPromptEvent {
	if runCtx == nil || runCtx.Event == nil {
		return nil
	}
	ev := runCtx.Event
	return &SecurityScanPromptEvent{
		Source:       ev.Source,
		Repository:   ev.Repository,
		Revision:     ev.Revision,
		BaseRevision: ev.BaseRevision,
		Branch:       ev.Branch,
		PRNumber:     ev.PRNumber,
		PRURL:        ev.PRURL,
		Fork:         ev.Fork,
		HeadRepo:     ev.HeadRepo,
		DiffScope:    len(runCtx.ChangedFiles) > 0 || runCtx.DiffFallback != "",
		ChangedFiles: runCtx.ChangedFiles,
		DiffFallback: runCtx.DiffFallback,
	}
}

// writeSecurityScanTarget renders the repository target section shared by the
// coordinator and per-task prompts.
func writeSecurityScanTarget(b *strings.Builder, spec triggersv1alpha1.SecurityScanSpec) {
	b.WriteString("## Target\n\n")
	fmt.Fprintf(b, "- Repository: %s\n", spec.RepoURL)
	fmt.Fprintf(b, "- Base branch: %s\n", spec.EffectiveBaseBranch())
	if rev := strings.TrimSpace(spec.Revision); rev != "" {
		fmt.Fprintf(b, "- Pinned revision: %s\n", rev)
	}
	for _, repo := range spec.AdditionalRepos {
		fmt.Fprintf(b, "- Additional repository (scanned alongside the target): %s\n", repo)
	}
	b.WriteString("\n")
}

// writeSecurityScanEvent renders the trigger-event section shared by the
// coordinator and per-task prompts. A nil event writes nothing.
func writeSecurityScanEvent(b *strings.Builder, event *SecurityScanPromptEvent) {
	if event == nil {
		return
	}
	b.WriteString("## Trigger event\n\n")
	fmt.Fprintf(b, "This scan was triggered by a repository %s event.\n\n", event.Source)
	fmt.Fprintf(b, "- Scan revision (checked out for you; do not change it): %s\n", event.Revision)
	if event.Branch != "" {
		fmt.Fprintf(b, "- Branch: %s\n", event.Branch)
	}
	if event.PRNumber > 0 {
		fmt.Fprintf(b, "- Pull request: #%d", event.PRNumber)
		if event.PRURL != "" {
			fmt.Fprintf(b, " (%s)", event.PRURL)
		}
		b.WriteString("\n")
	}
	if event.Fork {
		fmt.Fprintf(b, "- The change comes from fork %s: treat it as fully untrusted third-party code. This run intentionally has no repository write credentials.\n", event.HeadRepo)
	}
	if event.DiffScope {
		if event.DiffFallback != "" {
			fmt.Fprintf(b, "- Diff scope was requested but is unavailable (%s). FALLBACK: scan the FULL repository at the revision above.\n", event.DiffFallback)
		} else {
			if event.BaseRevision != "" {
				fmt.Fprintf(b, "- Diff scope: prioritize the files changed between %s and %s, listed below. Still follow data flows into unchanged code when a changed file feeds it.\n", event.BaseRevision, event.Revision)
			} else {
				b.WriteString("- Diff scope: prioritize the changed files listed below. Still follow data flows into unchanged code when a changed file feeds it.\n")
			}
			for _, f := range event.ChangedFiles {
				fmt.Fprintf(b, "  - %s\n", f)
			}
		}
	}
	b.WriteString("\n")
}

// writeSecurityScanScope renders the scope section shared by the coordinator
// and per-task prompts. A nil scope writes nothing.
func writeSecurityScanScope(b *strings.Builder, scope *triggersv1alpha1.SecurityScanScope) {
	if scope == nil {
		return
	}
	b.WriteString("## Scope\n\n")
	if strings.TrimSpace(scope.Focus) != "" {
		fmt.Fprintf(b, "- Focus: %s\n", scope.Focus)
	}
	if len(scope.IncludePaths) > 0 {
		fmt.Fprintf(b, "- Only scan paths matching: %s\n", strings.Join(scope.IncludePaths, ", "))
	}
	if len(scope.ExcludePaths) > 0 {
		fmt.Fprintf(b, "- Skip paths matching: %s\n", strings.Join(scope.ExcludePaths, ", "))
	}
	if len(scope.Languages) > 0 {
		fmt.Fprintf(b, "- Restrict analysis to languages: %s\n", strings.Join(scope.Languages, ", "))
	}
	b.WriteString("\n")
}

// writeSecurityScanReportingPolicy renders the dedupe / minimum-severity /
// finding-budget policy section shared by the coordinator and per-task
// prompts. maxFindings <= 0 omits the budget line.
func writeSecurityScanReportingPolicy(b *strings.Builder, spec triggersv1alpha1.SecurityScanSpec, maxFindings int32) {
	b.WriteString("## Reporting policy\n\n")
	writeSecurityScanReportingRules(b, spec)
	if maxFindings > 0 {
		fmt.Fprintf(b, "- Finding budget: report at most %d findings in total; prioritize the most severe, highest-confidence issues. The platform enforces this cap on the persisted findings regardless of what is reported.\n", maxFindings)
	}
	b.WriteString("\n")
}

// writeSecurityScanReportingRules renders the dedupe and minimum-severity
// rules every scan prompt states, independent of which budgets apply.
func writeSecurityScanReportingRules(b *strings.Builder, spec triggersv1alpha1.SecurityScanSpec) {
	if spec.DedupeEnabled() {
		fmt.Fprintf(b, "- Deduplicate findings: treat findings with similarity of at least %d/1000 as duplicates and report each issue once.\n", spec.DedupeSimilarityThresholdPermille())
	} else {
		b.WriteString("- Deduplication is disabled: report every finding, including near-duplicates.\n")
	}
	fmt.Fprintf(b, "- Exclude findings below severity %q from the report.\n", spec.EffectiveMinSeverity())
}

// writeSecurityScanTaskReportingPolicy renders the reporting policy of one
// deterministic task run, where TWO independent budgets apply: the task's own
// cap, counted across every parallel instance and retry of that task, and the
// scan-wide cap every task of the execution shares. Collapsing them into one
// number would misstate both — a task would either believe it may report the
// whole scan's budget alone, or that the scan-wide cap is as small as its own.
func writeSecurityScanTaskReportingPolicy(b *strings.Builder, spec triggersv1alpha1.SecurityScanSpec, taskMaxFindings int32) {
	b.WriteString("## Reporting policy\n\n")
	writeSecurityScanReportingRules(b, spec)
	if taskMaxFindings > 0 {
		fmt.Fprintf(b, "- Task finding budget: this task may report at most %d findings in total, counted across all of its parallel instances and any retries; prioritize the most severe, highest-confidence issues.\n", taskMaxFindings)
	}
	if spec.Budgets != nil && spec.Budgets.MaxFindings > 0 {
		fmt.Fprintf(b, "- Scan finding budget: every task of this execution shares one scan-wide cap of %d findings. The platform enforces both caps on the persisted findings regardless of what is reported.\n", spec.Budgets.MaxFindings)
	}
	b.WriteString("\n")
}

// BuildSecurityScanPrompt renders the complete autonomous task packet seeded
// as the first user message of a scan AgentRun: the scan target and scope, the
// workflow as an explicit sub-agent DAG plan, the machine-readable finding
// contract, post-script and ranking instructions, and the final reporting
// step. Output is deterministic for a given spec.
func BuildSecurityScanPrompt(spec triggersv1alpha1.SecurityScanSpec) string {
	return BuildSecurityScanPromptWithEvent(spec, nil, 0)
}

// BuildSecurityScanPromptWithEvent renders the scan prompt, optionally with a
// repository-event section describing the trigger, the pinned revision, and
// the diff scope. parallelism is the true concurrency bound stated in the
// prompt (the spec value clamped to the mode template's sub-agent ceiling);
// zero falls back to spec.EffectiveParallelism().
func BuildSecurityScanPromptWithEvent(spec triggersv1alpha1.SecurityScanSpec, event *SecurityScanPromptEvent, parallelism int32) string {
	if parallelism <= 0 {
		parallelism = spec.EffectiveParallelism()
	}
	var b strings.Builder

	b.WriteString("# Security scan\n\n")
	b.WriteString("You are coordinating an autonomous security scan of a code base. ")
	b.WriteString("Execute the research plan below by fanning out one focused sub-agent per task, ")
	b.WriteString("collect every vulnerability as a structured finding, then submit the final report.\n\n")

	writeSecurityScanTarget(&b, spec)
	writeSecurityScanEvent(&b, event)
	writeSecurityScanScope(&b, spec.Scope)

	workflow := spec.Workflow
	b.WriteString("## Research plan (sub-agent DAG)\n\n")
	fmt.Fprintf(&b, "Spawn one sub-agent per task below. Run tasks whose dependencies are all complete in parallel, but never more than %d at a time. A task must not start until every task it depends on has finished.\n\n", parallelism)
	for i, task := range workflow {
		fmt.Fprintf(&b, "%d. Task %q", i+1, task.Name)
		if task.Category != "" {
			fmt.Fprintf(&b, " (category: %s)", task.Category)
		}
		b.WriteString("\n")
		fmt.Fprintf(&b, "   - Objective: %s\n", task.Objective)
		fmt.Fprintf(&b, "   - Role: %s\n", task.EffectiveRole())
		if task.Model != "" {
			fmt.Fprintf(&b, "   - Model: %s\n", task.Model)
		}
		if len(task.DependsOn) > 0 {
			fmt.Fprintf(&b, "   - Depends on: %s\n", strings.Join(task.DependsOn, ", "))
		}
		if task.MaxFindings > 0 {
			fmt.Fprintf(&b, "   - Report at most %d findings\n", task.MaxFindings)
		}
	}
	b.WriteString("\n")

	b.WriteString("## Finding contract\n\n")
	b.WriteString("Report every vulnerability by calling the report_security_finding tool with one structured finding. Never inline findings as prose in your replies; a finding that is not reported through the tool does not exist.\n\n")
	b.WriteString(security.FindingSchemaPrompt())
	b.WriteString("\n\n")

	if len(spec.PostScripts) > 0 {
		b.WriteString("## Post-scripts\n\n")
		b.WriteString("After the research tasks complete, run each post-script below once per matching finding before submitting the report.\n\n")
		for _, script := range spec.PostScripts {
			fmt.Fprintf(&b, "- Post-script %q (runs on: %s findings): %s\n", script.Name, script.EffectiveRunOn(), script.Prompt)
		}
		b.WriteString("\n")
	}

	if len(spec.SeverityRankers) > 0 {
		b.WriteString("## Ranking rules\n\n")
		b.WriteString("Apply these operator-authored ranking rules when triaging findings, and pass them to submit_security_scan_report.\n\n")
		for _, ranker := range spec.SeverityRankers {
			fmt.Fprintf(&b, "### Ranker %q\n\n%s\n\n", ranker.Name, ranker.Rules)
		}
	}

	var budgetMaxFindings int32
	if spec.Budgets != nil {
		budgetMaxFindings = spec.Budgets.MaxFindings
	}
	writeSecurityScanReportingPolicy(&b, spec, budgetMaxFindings)

	b.WriteString("## Final step\n\n")
	b.WriteString("When every task and post-script has completed and all findings are reported, call submit_security_scan_report exactly once with the scan summary, then finish the run.\n")

	return b.String()
}

// SecurityScanTaskInstance carries the per-instance context rendered into a
// deterministic task prompt.
type SecurityScanTaskInstance struct {
	// Objective is the task objective with every template reference
	// ({{params.*}}, {{tasks.*}}, {{item*}}) already rendered.
	Objective string
	// Instance is the 0-based instance index; Total is the number of
	// instances of this task (fan-out or ensemble repeats).
	Instance int32
	Total    int32
	// ItemJSON, when non-empty, is the fan-out record this instance handles.
	ItemJSON string
	// ItemsJSON and the half-open record range describe a targetRuns chunk.
	ItemsJSON   string
	Chunked     bool
	RecordStart int32
	RecordEnd   int32
	// Sink reports that no other task depends on this task. Only sink tasks
	// are instructed to call submit_security_scan_report: they are the
	// terminal aggregation points of the DAG, so the scan-wide report is
	// submitted exactly where all research results converge.
	Sink bool
	// PostScriptsInline reports that the platform could NOT run the
	// post-scripts as durable per-finding jobs (a workflow whose every task
	// is a sink has no research phase to derive the matrix from). The sink is
	// then given the scripts as prose to run itself; asserting a
	// platform-enforced execution that never happened would leave the
	// findings unvalidated with nobody compensating.
	PostScriptsInline bool
	// CoverageGaps are the execution's explicit incomplete-coverage
	// statements (truncated fan-outs, post-script jobs that never reached a
	// verdict). They are rendered for sink tasks only: the report the sink
	// submits must disclose them instead of reading as authoritative.
	CoverageGaps []string
}

// SecurityScanTaskRole is the effective RoleInstruction contract a
// deterministic task run was dispatched with, as stated in its prompt. It
// mirrors what the run is actually configured with (instructions are injected
// separately, the tool policy is already narrowed), so the agent is never told
// it may do something the platform then denies.
type SecurityScanTaskRole struct {
	Name        string
	Description string
	// ToolAccess is the role's raw spec value; ReadOnly is its normalized
	// meaning (read-only and analysis both forbid mutation).
	ToolAccess string
	ReadOnly   bool
}

// BuildSecurityScanTaskPrompt renders the focused single-task packet seeded
// as the first user message of one deterministic task AgentRun: the scan
// target, event, and scope context, this task's rendered objective and role,
// the machine-readable finding contract, the structured-output contract when
// the task declares an outputSchema, and — for sink tasks only — the final
// scan-report step. A nil role renders the task's declared role name only.
// Output is deterministic for a given input.
func BuildSecurityScanTaskPrompt(spec triggersv1alpha1.SecurityScanSpec, event *SecurityScanPromptEvent, task triggersv1alpha1.SecurityScanTask, inst SecurityScanTaskInstance, role *SecurityScanTaskRole) string {
	var b strings.Builder

	b.WriteString("# Security scan task\n\n")
	b.WriteString("You are executing ONE focused task of an autonomous security scan. ")
	b.WriteString("The platform schedules the other tasks of the research plan separately; do exactly this task, ")
	b.WriteString("report every vulnerability you find as a structured finding, then finish.\n\n")

	writeSecurityScanTarget(&b, spec)
	writeSecurityScanEvent(&b, event)
	writeSecurityScanScope(&b, spec.Scope)

	b.WriteString("## Your task\n\n")
	fmt.Fprintf(&b, "- Task: %q", task.Name)
	if task.Category != "" {
		fmt.Fprintf(&b, " (category: %s)", task.Category)
	}
	b.WriteString("\n")
	objective := inst.Objective
	if objective == "" {
		objective = task.Objective
	}
	fmt.Fprintf(&b, "- Objective: %s\n", objective)
	fmt.Fprintf(&b, "- Role: %s\n", task.EffectiveRole())
	if role != nil {
		if role.Description != "" {
			fmt.Fprintf(&b, "- Role focus: %s\n", role.Description)
		}
		if role.ReadOnly {
			fmt.Fprintf(&b, "- Role tool access: %s. Do not attempt to modify the repository, commit, push, or post to the forge; those tools are withheld from this run.\n", role.ToolAccess)
		}
	}
	if inst.Total > 1 {
		fmt.Fprintf(&b, "- Instance: %d of %d parallel instances of this task; stay within this instance's slice of the work.\n", inst.Instance+1, inst.Total)
	}
	if inst.ItemJSON != "" {
		fmt.Fprintf(&b, "- This instance handles exactly this input record:\n\n```json\n%s\n```\n", inst.ItemJSON)
	}
	if inst.Chunked {
		fmt.Fprintf(&b, "- This instance handles source record indexes [%d,%d). The indexed input available as `{{items}}` is:\n\n```json\n%s\n```\n", inst.RecordStart, inst.RecordEnd, inst.ItemsJSON)
	}
	b.WriteString("\n")

	b.WriteString("## Finding contract\n\n")
	b.WriteString("Report every vulnerability by calling the report_security_finding tool with one structured finding. Never inline findings as prose in your replies; a finding that is not reported through the tool does not exist.\n\n")
	b.WriteString(security.FindingSchemaPrompt())
	b.WriteString("\n\n")

	writeSecurityScanTaskReportingPolicy(&b, spec, task.MaxFindings)

	if inst.Chunked {
		b.WriteString("## Structured output\n\n")
		fmt.Fprintf(&b, "Call submit_task_output exactly once with a JSON array containing exactly one entry for every assigned record index from %d through %d, in ascending order. Each entry must have exactly this shape: `{\"recordIndex\": absoluteInteger, \"result\": value}`. Missing, duplicate, non-integer, or out-of-range indexes fail the task.\n\n", inst.RecordStart, inst.RecordEnd-1)
		if schema := strings.TrimSpace(task.OutputSchema); schema != "" {
			b.WriteString("The complete output array must also conform to this JSON Schema:\n\n")
			fmt.Fprintf(&b, "```json\n%s\n```\n\n", schema)
		}
		b.WriteString("A run that never submits a conforming output fails its task even when the analysis itself succeeded.\n\n")
	} else if strings.TrimSpace(task.OutputSchema) != "" {
		b.WriteString("## Structured output\n\n")
		b.WriteString("This task has a typed output contract: downstream tasks consume its result as machine-readable data. Before finishing, call submit_task_output exactly once with an output that conforms to this JSON Schema:\n\n")
		fmt.Fprintf(&b, "```json\n%s\n```\n\n", strings.TrimSpace(task.OutputSchema))
		b.WriteString("A run that never submits a conforming output fails its task even when the analysis itself succeeded.\n\n")
	}

	if inst.Sink {
		if len(spec.PostScripts) > 0 {
			b.WriteString("## Post-scripts\n\n")
			if inst.PostScriptsInline {
				b.WriteString("The platform could NOT run these post-scripts as per-finding jobs for this workflow, so running them is YOUR responsibility: run each post-script below once per matching finding, record its verdict with update_security_finding, and only then submit the report.\n\n")
				for _, script := range spec.PostScripts {
					fmt.Fprintf(&b, "- Post-script %q (runs on: %s findings): %s\n", script.Name, script.EffectiveRunOn(), script.Prompt)
				}
				b.WriteString("\n")
			} else {
				b.WriteString("The post-scripts already ran: the platform executed each one as its own per-finding job before this task started, in a fixed order, and recorded every verdict on the finding itself. Do not re-run them. Call list_security_findings to read the resulting statuses and audit notes, and let them drive the report.\n\n")
			}
		}
		if len(inst.CoverageGaps) > 0 {
			b.WriteString("## Incomplete coverage\n\n")
			b.WriteString("This execution did NOT cover everything it was asked to. Your report must disclose each gap below explicitly and must not present the results as a complete or clean scan:\n\n")
			for _, gap := range inst.CoverageGaps {
				fmt.Fprintf(&b, "- %s\n", gap)
			}
			b.WriteString("\n")
		}
		if len(spec.SeverityRankers) > 0 {
			b.WriteString("## Ranking rules\n\n")
			b.WriteString("Apply these operator-authored ranking rules when triaging findings, and pass them to submit_security_scan_report.\n\n")
			for _, ranker := range spec.SeverityRankers {
				fmt.Fprintf(&b, "### Ranker %q\n\n%s\n\n", ranker.Name, ranker.Rules)
			}
		}
		b.WriteString("## Final step\n\n")
		b.WriteString("This task is a terminal task of the research plan: every other task's results feed into it. When your work is complete and all findings are reported, call submit_security_scan_report exactly once with the scan summary, then finish the run.\n")
	} else {
		b.WriteString("## Final step\n\n")
		b.WriteString("Do NOT call submit_security_scan_report: a terminal task of the research plan submits the scan-wide report. When your work is complete and all findings are reported")
		if strings.TrimSpace(task.OutputSchema) != "" {
			b.WriteString(" and your structured output is submitted")
		}
		b.WriteString(", finish the run.\n")
	}

	return b.String()
}

// SecurityPostScriptFinding is the finding one durable post-script job is
// bound to, rendered compactly into its prompt. The job runs against exactly
// this finding: the platform, not the model, decided that this script applies
// to it, so the prompt states the finding instead of asking the run to select
// one.
type SecurityPostScriptFinding struct {
	Fingerprint string
	ID          string
	Title       string
	Category    string
	Severity    string
	Status      string
	// Location is the rendered file:line(-line) (symbol) of the finding.
	Location    string
	Description string
	Impact      string
}

// BuildSecurityPostScriptPrompt retains the single-script API for callers and
// tests while rendering through the per-finding pipeline prompt.
func BuildSecurityPostScriptPrompt(spec triggersv1alpha1.SecurityScanSpec, event *SecurityScanPromptEvent, script triggersv1alpha1.SecurityScanPostScript, finding SecurityPostScriptFinding) string {
	return BuildSecurityPostScriptPipelinePrompt(spec, event, []triggersv1alpha1.SecurityScanPostScript{script}, finding)
}

// BuildSecurityPostScriptPipelinePrompt renders one AgentRun packet containing
// every post-script selected for a finding, in configured order. The run keeps
// intermediate conclusions in context and writes one aggregate durable verdict
// after completing the whole pipeline.
func BuildSecurityPostScriptPipelinePrompt(spec triggersv1alpha1.SecurityScanSpec, event *SecurityScanPromptEvent, scripts []triggersv1alpha1.SecurityScanPostScript, finding SecurityPostScriptFinding) string {
	var b strings.Builder

	b.WriteString("# Security scan post-script pipeline\n\n")
	fmt.Fprintf(&b, "You are executing an ordered pipeline of %d post-script(s) against ONE security finding of an autonomous security scan. ", len(scripts))
	b.WriteString("The research phase is over and the platform runs one pipeline job per finding. Complete every step below in order, carry evidence and conclusions forward between steps, then record one aggregate verdict.\n\n")

	writeSecurityScanTarget(&b, spec)
	writeSecurityScanEvent(&b, event)
	writeSecurityScanScope(&b, spec.Scope)

	b.WriteString("## Finding under review\n\n")
	fmt.Fprintf(&b, "- Fingerprint: %s\n", finding.Fingerprint)
	fmt.Fprintf(&b, "- Finding id: %s\n", finding.ID)
	fmt.Fprintf(&b, "- Title: %s\n", finding.Title)
	if finding.Category != "" {
		fmt.Fprintf(&b, "- Category: %s\n", finding.Category)
	}
	fmt.Fprintf(&b, "- Severity: %s\n", finding.Severity)
	fmt.Fprintf(&b, "- Current status: %s\n", finding.Status)
	if finding.Location != "" {
		fmt.Fprintf(&b, "- Location: %s\n", finding.Location)
	}
	if finding.Description != "" {
		fmt.Fprintf(&b, "- Description: %s\n", finding.Description)
	}
	if finding.Impact != "" {
		fmt.Fprintf(&b, "- Impact: %s\n", finding.Impact)
	}
	b.WriteString("\n")

	b.WriteString("## Ordered post-scripts\n\n")
	b.WriteString("Execute these instructions as pipeline steps. If an individual step asks you to finish by calling `update_security_finding`, retain that step's proposed status and note as an intermediate conclusion instead; do not call the tool until every step is complete. Later steps should consider all earlier evidence and proposed changes.\n\n")
	for i, script := range scripts {
		fmt.Fprintf(&b, "### %d. %s\n\n", i+1, script.Name)
		fmt.Fprintf(&b, "%s\n\n", strings.TrimSpace(script.Prompt))
	}

	b.WriteString("## Verdict contract\n\n")
	b.WriteString("Before finishing, call update_security_finding EXACTLY ONCE for this finding. That call is the only durable output of this run; a conclusion stated only in your reply does not exist.\n\n")
	fmt.Fprintf(&b, "- Identify the finding by `fingerprint: \"%s\"` (or `id: \"%s\"`).\n", finding.Fingerprint, finding.ID)
	b.WriteString("- `status` must be exactly one of: open, triaged, confirmed, false_positive, fixed, accepted_risk.\n")
	b.WriteString("- `note` carries your evidence and reasoning: what you did, what it proved or disproved, and why the status follows. The tool accepts no other fields, so anything the audit trail must keep belongs in the note.\n")
	b.WriteString("- Leave the status unchanged (re-state the current one) when your work was inconclusive, and say so in the note.\n\n")
	b.WriteString("Do NOT call submit_security_scan_report: a separate task submits the scan-wide report after every post-script pipeline has finished, and it reads your verdict from the finding. Do not open, re-scan, or triage other findings; this pipeline owns exactly the finding above.\n")

	return b.String()
}
