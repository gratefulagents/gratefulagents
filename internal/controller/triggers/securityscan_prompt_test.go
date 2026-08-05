package triggers

import (
	"strings"
	"testing"

	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/security"
)

func securityScanPromptSpec() triggersv1alpha1.SecurityScanSpec {
	return triggersv1alpha1.SecurityScanSpec{
		RepoURL:     "https://github.com/acme/widget.git",
		BaseBranch:  "release",
		Revision:    "abc123",
		Parallelism: 3,
		Scope: &triggersv1alpha1.SecurityScanScope{
			Focus:        "authentication boundaries",
			IncludePaths: []string{"internal/auth/**", "pkg/session/**"},
			ExcludePaths: []string{"vendor/**", "**/*_test.go"},
		},
		Workflow: []triggersv1alpha1.SecurityScanTask{{
			Name:      "session-review",
			Objective: "Trace session creation and invalidation.",
			Role:      "security-reviewer",
		}},
		SeverityRankers: []triggersv1alpha1.SecurityRanker{{
			Name:  "exploitability",
			Rules: "Unauthenticated remote execution is critical.",
		}},
		PostScripts: []triggersv1alpha1.SecurityPostScript{{
			Name:   "proof",
			RunOn:  "high-and-above",
			Prompt: "Validate exploitability with a minimal proof of concept.",
		}},
		Dedupe:      &triggersv1alpha1.SecurityScanDedupe{SimilarityThresholdPermille: 900},
		MinSeverity: "medium",
	}
}

func TestBuildSecurityScanPromptReturnsDeterministicOutput(t *testing.T) {
	spec := securityScanPromptSpec()
	if first, second := BuildSecurityScanPrompt(spec), BuildSecurityScanPrompt(spec); first != second {
		t.Fatal("BuildSecurityScanPrompt() returned different output for the same spec")
	}
}

func TestBuildSecurityScanPromptIncludesWorkflowTaskNameAndObjective(t *testing.T) {
	spec := securityScanPromptSpec()
	prompt := BuildSecurityScanPrompt(spec)
	for _, task := range spec.Workflow {
		if !strings.Contains(prompt, task.Name) {
			t.Fatalf("prompt does not contain workflow task name %q", task.Name)
		}
		if !strings.Contains(prompt, task.Objective) {
			t.Fatalf("prompt does not contain workflow task objective %q", task.Objective)
		}
	}
}

func TestBuildSecurityScanPromptIncludesEveryRankerRule(t *testing.T) {
	spec := securityScanPromptSpec()
	prompt := BuildSecurityScanPrompt(spec)
	for _, ranker := range spec.SeverityRankers {
		if !strings.Contains(prompt, ranker.Rules) {
			t.Fatalf("prompt does not contain ranker rule %q", ranker.Rules)
		}
	}
}

func TestBuildSecurityScanPromptIncludesEveryPostScriptPrompt(t *testing.T) {
	spec := securityScanPromptSpec()
	prompt := BuildSecurityScanPrompt(spec)
	for _, script := range spec.PostScripts {
		if !strings.Contains(prompt, script.Prompt) {
			t.Fatalf("prompt does not contain post-script prompt %q", script.Prompt)
		}
	}
}

func TestBuildSecurityScanPromptIncludesFindingSchemaContract(t *testing.T) {
	prompt := BuildSecurityScanPrompt(securityScanPromptSpec())
	if !strings.Contains(prompt, security.FindingSchemaPrompt()) {
		t.Fatal("prompt does not contain the complete finding schema contract")
	}
}

func TestBuildSecurityScanPromptIncludesDedupeAndMinimumSeverityPolicy(t *testing.T) {
	prompt := BuildSecurityScanPrompt(securityScanPromptSpec())
	if !strings.Contains(prompt, "similarity of at least 900/1000") {
		t.Fatal("prompt does not contain dedupe threshold")
	}
	if !strings.Contains(prompt, `Exclude findings below severity "medium"`) {
		t.Fatal("prompt does not contain minimum severity policy")
	}
}

func TestBuildSecurityScanPromptIncludesFinalReportInstruction(t *testing.T) {
	prompt := BuildSecurityScanPrompt(securityScanPromptSpec())
	if !strings.Contains(prompt, "call submit_security_scan_report exactly once") {
		t.Fatal("prompt does not require final submit_security_scan_report call")
	}
}

func TestBuildSecurityScanPromptIncludesConfiguredScope(t *testing.T) {
	spec := securityScanPromptSpec()
	prompt := BuildSecurityScanPrompt(spec)
	for _, want := range []string{
		spec.Scope.Focus,
		strings.Join(spec.Scope.IncludePaths, ", "),
		strings.Join(spec.Scope.ExcludePaths, ", "),
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt does not contain scope value %q", want)
		}
	}
}
