package triggers

import (
	"fmt"
	"strings"

	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/security"
)

// BuildSecurityScanPrompt renders the complete autonomous task packet seeded
// as the first user message of a scan AgentRun: the scan target and scope, the
// workflow as an explicit sub-agent DAG plan, the machine-readable finding
// contract, post-script and ranking instructions, and the final reporting
// step. Output is deterministic for a given spec.
func BuildSecurityScanPrompt(spec triggersv1alpha1.SecurityScanSpec) string {
	var b strings.Builder

	b.WriteString("# Security scan\n\n")
	b.WriteString("You are coordinating an autonomous security scan of a code base. ")
	b.WriteString("Execute the research plan below by fanning out one focused sub-agent per task, ")
	b.WriteString("collect every vulnerability as a structured finding, then submit the final report.\n\n")

	b.WriteString("## Target\n\n")
	fmt.Fprintf(&b, "- Repository: %s\n", spec.RepoURL)
	fmt.Fprintf(&b, "- Base branch: %s\n", spec.EffectiveBaseBranch())
	if rev := strings.TrimSpace(spec.Revision); rev != "" {
		fmt.Fprintf(&b, "- Pinned revision: %s\n", rev)
	}
	for _, repo := range spec.AdditionalRepos {
		fmt.Fprintf(&b, "- Additional repository (scanned alongside the target): %s\n", repo)
	}
	b.WriteString("\n")

	if scope := spec.Scope; scope != nil {
		b.WriteString("## Scope\n\n")
		if strings.TrimSpace(scope.Focus) != "" {
			fmt.Fprintf(&b, "- Focus: %s\n", scope.Focus)
		}
		if len(scope.IncludePaths) > 0 {
			fmt.Fprintf(&b, "- Only scan paths matching: %s\n", strings.Join(scope.IncludePaths, ", "))
		}
		if len(scope.ExcludePaths) > 0 {
			fmt.Fprintf(&b, "- Skip paths matching: %s\n", strings.Join(scope.ExcludePaths, ", "))
		}
		if len(scope.Languages) > 0 {
			fmt.Fprintf(&b, "- Restrict analysis to languages: %s\n", strings.Join(scope.Languages, ", "))
		}
		b.WriteString("\n")
	}

	workflow := spec.EffectiveWorkflow()
	b.WriteString("## Research plan (sub-agent DAG)\n\n")
	fmt.Fprintf(&b, "Spawn one sub-agent per task below. Run tasks whose dependencies are all complete in parallel, but never more than %d at a time. A task must not start until every task it depends on has finished.\n\n", spec.EffectiveParallelism())
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

	b.WriteString("## Reporting policy\n\n")
	if spec.DedupeEnabled() {
		fmt.Fprintf(&b, "- Deduplicate findings: treat findings with similarity of at least %d/1000 as duplicates and report each issue once.\n", spec.DedupeSimilarityThresholdPermille())
	} else {
		b.WriteString("- Deduplication is disabled: report every finding, including near-duplicates.\n")
	}
	fmt.Fprintf(&b, "- Exclude findings below severity %q from the report.\n", spec.EffectiveMinSeverity())
	b.WriteString("\n")

	b.WriteString("## Final step\n\n")
	b.WriteString("When every task and post-script has completed and all findings are reported, call submit_security_scan_report exactly once with the scan summary, then finish the run.\n")

	return b.String()
}
