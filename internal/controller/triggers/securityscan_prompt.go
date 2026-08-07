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
	if spec.DedupeEnabled() {
		fmt.Fprintf(b, "- Deduplicate findings: treat findings with similarity of at least %d/1000 as duplicates and report each issue once.\n", spec.DedupeSimilarityThresholdPermille())
	} else {
		b.WriteString("- Deduplication is disabled: report every finding, including near-duplicates.\n")
	}
	fmt.Fprintf(b, "- Exclude findings below severity %q from the report.\n", spec.EffectiveMinSeverity())
	if maxFindings > 0 {
		fmt.Fprintf(b, "- Finding budget: report at most %d findings in total; prioritize the most severe, highest-confidence issues. The platform enforces this cap on the persisted findings regardless of what is reported.\n", maxFindings)
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

	workflow := spec.EffectiveWorkflow()
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
	// Sink reports that no other task depends on this task. Only sink tasks
	// are instructed to call submit_security_scan_report: they are the
	// terminal aggregation points of the DAG, so the scan-wide report is
	// submitted exactly where all research results converge.
	Sink bool
}

// BuildSecurityScanTaskPrompt renders the focused single-task packet seeded
// as the first user message of one deterministic task AgentRun: the scan
// target, event, and scope context, this task's rendered objective and role,
// the machine-readable finding contract, the structured-output contract when
// the task declares an outputSchema, and — for sink tasks only — the final
// scan-report step. Output is deterministic for a given input.
func BuildSecurityScanTaskPrompt(spec triggersv1alpha1.SecurityScanSpec, event *SecurityScanPromptEvent, task triggersv1alpha1.SecurityScanTask, inst SecurityScanTaskInstance) string {
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
	if inst.Total > 1 {
		fmt.Fprintf(&b, "- Instance: %d of %d parallel instances of this task; stay within this instance's slice of the work.\n", inst.Instance+1, inst.Total)
	}
	if inst.ItemJSON != "" {
		fmt.Fprintf(&b, "- This instance handles exactly this input record:\n\n```json\n%s\n```\n", inst.ItemJSON)
	}
	b.WriteString("\n")

	b.WriteString("## Finding contract\n\n")
	b.WriteString("Report every vulnerability by calling the report_security_finding tool with one structured finding. Never inline findings as prose in your replies; a finding that is not reported through the tool does not exist.\n\n")
	b.WriteString(security.FindingSchemaPrompt())
	b.WriteString("\n\n")

	maxFindings := task.MaxFindings
	if maxFindings <= 0 && spec.Budgets != nil {
		maxFindings = spec.Budgets.MaxFindings
	}
	writeSecurityScanReportingPolicy(&b, spec, maxFindings)

	if strings.TrimSpace(task.OutputSchema) != "" {
		b.WriteString("## Structured output\n\n")
		b.WriteString("This task has a typed output contract: downstream tasks consume its result as machine-readable data. Before finishing, call submit_task_output exactly once with an output that conforms to this JSON Schema:\n\n")
		fmt.Fprintf(&b, "```json\n%s\n```\n\n", strings.TrimSpace(task.OutputSchema))
		b.WriteString("A run that never submits a conforming output fails its task even when the analysis itself succeeded.\n\n")
	}

	if inst.Sink {
		if len(spec.PostScripts) > 0 {
			b.WriteString("## Post-scripts\n\n")
			b.WriteString("Run each post-script below once per matching finding before submitting the report.\n\n")
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
