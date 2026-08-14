package configtest

import (
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/security"
	"github.com/gratefulagents/gratefulagents/internal/store"
)

// promptStatusPattern matches the finding statuses a post-script prompt tells
// the run to set, as in: set ... status `false_positive` (the prompt text
// wraps, so the status may sit on the next line).
var promptStatusPattern = regexp.MustCompile("status\\s+`([a-z_]+)`")

// securityTriagePostScripts are the per-finding triage post-scripts shipped
// for bug hunting, with the runOn selector each one is expected to keep: a
// silent change there re-targets the script at a different finding set.
var securityTriagePostScripts = []struct {
	name  string
	runOn string
}{
	{"report-writer", "all"},
	{"poc-builder", "medium-and-above-actionable"},
	{"poc-validator", "medium-and-above-actionable"},
	{"bounty-worthiness-check", "all"},
	{"exploitability-score", "all"},
	{"patched-since-check", "all"},
	{"resource-exhaustion-classifier", "all"},
	{"false-positive-check", "all"},
	{"scope-eligibility-check", "all"},
	{"prior-art-check", "all"},
}

// securityTriageRankers are the shipped severity rankers for bug hunting,
// each with the severity-floor categories it is allowed to declare. A floor
// is applied by security.Rank to every finding of that category before the
// minSeverity cut, and no prose can condition it, so a floor may only stay
// where the ranker's own rules never place a finding of that category below
// it. bug-bounty-triage remains prose-only because its evidence requirements
// cannot be represented by an unconditional category floor.
var securityTriageRankers = []struct {
	name   string
	floors []string
}{
	{"web-app-impact", []string{"authn"}},
	{"api-service-impact", []string{"authz", "authn"}},
	{"blockchain-impact", nil},
	{"bug-bounty-triage", nil},
	{"library-impact", nil},
}

func TestSecurityTriagePostScriptAssets(t *testing.T) {
	t.Parallel()

	for _, tc := range securityTriagePostScripts {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var script triggersv1alpha1.SecurityPostScript
			readBootstrapAsset(t, "securitypostscripts", tc.name, &script)

			if script.Kind != "SecurityPostScript" {
				t.Errorf("kind = %q, want SecurityPostScript", script.Kind)
			}
			if script.Name != tc.name {
				t.Errorf("metadata.name = %q, want %q", script.Name, tc.name)
			}
			if errs := triggersv1alpha1.ValidateSecurityPostScriptSpec(script.Spec); len(errs) != 0 {
				t.Fatalf("spec is invalid: %v", errs)
			}
			if got := script.Spec.EffectiveRunOn(); got != tc.runOn {
				t.Errorf("runOn = %q, want %q", got, tc.runOn)
			}
			if desc := strings.TrimSpace(script.Spec.Description); len(desc) < 40 {
				t.Errorf("description %q is too thin to explain what the script does", desc)
			}

			prompt := strings.TrimSpace(script.Spec.Prompt)
			// The scan prompt inlines every post-script, so a bloated prompt
			// costs tokens on every finding; a stub prompt does nothing.
			if lines := strings.Count(prompt, "\n") + 1; lines < 12 || lines > 40 {
				t.Errorf("prompt has %d lines, want a tight 12-40 line script", lines)
			}
			if len(prompt) < 400 || len(prompt) > 3000 {
				t.Errorf("prompt is %d bytes, want between 400 and 3000", len(prompt))
			}
			// Post-scripts run once per finding: without this constraint the
			// run drifts into a fresh hunt and re-reports known issues.
			const opener = "You are processing exactly one finding. Do not hunt for new issues."
			if !strings.HasPrefix(prompt, opener) {
				t.Errorf("prompt must start with %q", opener)
			}
			// A post-script that never writes back leaves no trace on the
			// finding, so the whole run is wasted.
			if !strings.Contains(prompt, "update_security_finding") {
				t.Error("prompt must require the update_security_finding call")
			}
			// update_security_finding only writes status and note, so a
			// prompt asking for any other field would ask for the impossible.
			if !strings.Contains(prompt, "`note`") {
				t.Error("prompt must record its output in the note field")
			}
			for _, unsupported := range []string{"`evidence`", "`severity`", "`poc`", "`report`", "`confidence`"} {
				if strings.Contains(prompt, unsupported) {
					t.Errorf("prompt asks for %s, which update_security_finding cannot write", unsupported)
				}
			}
			statuses := promptStatusPattern.FindAllStringSubmatch(prompt, -1)
			if len(statuses) == 0 {
				t.Error("prompt must name the status the run should set")
			}
			for _, match := range statuses {
				if !store.ValidSecurityFindingStatus(match[1]) {
					t.Errorf("prompt names unknown finding status %q", match[1])
				}
			}
		})
	}
}

func TestReportWriterRequiresEvidenceBackedSubmission(t *testing.T) {
	t.Parallel()

	var script triggersv1alpha1.SecurityPostScript
	readBootstrapAsset(t, "securitypostscripts", "report-writer", &script)
	prompt := script.Spec.Prompt

	for _, marker := range []string{
		"## Title", "## Summary", "## Scope and affected component",
		"## Root cause", "## Preconditions and environment",
		"## Reproduction and evidence", "## Expected versus actual behavior",
		"## Impact and severity", "## Remediation",
		"## References and testing limits", "source -> guards -> sink",
		"Do not invent requests", "If no deterministic",
		"save_security_bounty_submission", "update_security_finding",
	} {
		if !strings.Contains(prompt, marker) {
			t.Errorf("report-writer prompt is missing %q", marker)
		}
	}
	if strings.Index(prompt, "save_security_bounty_submission") > strings.Index(prompt, "update_security_finding") {
		t.Error("report-writer must store the report before updating the finding")
	}
}

func TestSecurityTriageRankerAssets(t *testing.T) {
	t.Parallel()

	for _, tc := range securityTriageRankers {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var ranker triggersv1alpha1.SecurityRanker
			readBootstrapAsset(t, "securityrankers", tc.name, &ranker)

			if ranker.Kind != "SecurityRanker" {
				t.Errorf("kind = %q, want SecurityRanker", ranker.Kind)
			}
			if ranker.Name != tc.name {
				t.Errorf("metadata.name = %q, want %q", ranker.Name, tc.name)
			}
			if errs := triggersv1alpha1.ValidateSecurityRankerRules(ranker.Spec.Rules); len(errs) != 0 {
				t.Fatalf("rules are invalid: %v", errs)
			}
			if desc := strings.TrimSpace(ranker.Spec.Description); len(desc) < 40 {
				t.Errorf("description %q is too thin to explain what the ranker prioritizes", desc)
			}
			if n := len(ranker.Spec.Rules); n < 5 || n > 12 {
				t.Errorf("ranker has %d rules, want 5-12", n)
			}

			var floored []string
			for i, rule := range ranker.Spec.Rules {
				trimmed := strings.TrimSpace(rule)
				if trimmed == "" {
					t.Errorf("rules[%d] is blank", i)
					continue
				}
				directive, value, isDirective := strings.Cut(trimmed, ":")
				if isDirective && strings.TrimSpace(directive) == "severity-floor" {
					category, severity, ok := strings.Cut(strings.TrimSpace(value), "=")
					if !ok {
						t.Errorf("rules[%d] = %q, want severity-floor: <category>=<severity>", i, trimmed)
						continue
					}
					floored = append(floored, category)
					if !slices.Contains(security.Severities, severity) {
						t.Errorf("rules[%d] floors to unknown severity %q, want one of %v", i, severity, security.Severities)
					}
					if !slices.Contains(security.Categories, category) {
						t.Errorf("rules[%d] floors unknown category %q, want one of %v", i, category, security.Categories)
					}
					continue
				}
				// Prose rules steer the model, so one-liners like "be
				// careful" are worse than no rule at all.
				if len(trimmed) < 60 {
					t.Errorf("rules[%d] = %q is too vague to calibrate anything", i, trimmed)
				}
			}
			// security.Rank raises every finding of a floored category
			// unconditionally, so a floor the ranker's prose contradicts
			// inflates speculative findings past the pack's minSeverity cut.
			// Adding one here means proving the floor holds for every
			// finding of that category this ranker can see.
			if !slices.Equal(floored, tc.floors) {
				t.Errorf("severity-floor categories = %v, want %v", floored, tc.floors)
			}
		})
	}
}

// TestBugBountyAcceptanceGuards pins the policy decisions that keep repository
// hardening noise out of bounty reports: CI and repository automation need a
// complete untrusted-trigger-to-impact path.
func TestBugBountyAcceptanceGuards(t *testing.T) {
	t.Parallel()

	var ranker triggersv1alpha1.SecurityRanker
	readBootstrapAsset(t, "securityrankers", "bug-bounty-triage", &ranker)
	rules := strings.ToLower(strings.Join(ranker.Spec.Rules, "\n"))
	for _, marker := range []string{"github actions", "untrusted external actor", "production secret", "protected-branch write", "not bounty vulnerabilities"} {
		if !strings.Contains(rules, marker) {
			t.Errorf("bug-bounty ranker must contain acceptance guard %q", marker)
		}
	}

	var scope triggersv1alpha1.SecurityPostScript
	readBootstrapAsset(t, "securitypostscripts", "scope-eligibility-check", &scope)
	prompt := strings.ToLower(strings.Join(strings.Fields(scope.Spec.Prompt), " "))
	for _, marker := range []string{"attached security program scope snapshot", "eligibility is unknown", "preserve the finding as nonterminal", "program url by itself is provenance only", "must not be fetched", "essential acceptance fact", "untrusted actor-controlled event", "attacker path that is merely unestablished is unknown", "set status `accepted_risk` only for a definitive scope exclusion", "set status `triaged` for eligible or unknown"} {
		if !strings.Contains(prompt, marker) {
			t.Errorf("scope eligibility check must contain conservative guard %q", marker)
		}
	}

	var gate triggersv1alpha1.SecurityPostScript
	readBootstrapAsset(t, "securitypostscripts", "bounty-worthiness-check", &gate)
	gatePrompt := strings.ToLower(strings.Join(strings.Fields(gate.Spec.Prompt), " "))
	for _, marker := range []string{"final bounty acceptance gate", "severity is high or critical", "complete code trace", "unavailable sandbox validation is inconclusive", "missing evidence", "set status `confirmed` for accepted", "`false_positive` for disproved", "`accepted_risk` for out-of-scope", "`triaged` for inconclusive"} {
		if !strings.Contains(gatePrompt, marker) {
			t.Errorf("bounty worthiness check must contain final guard %q", marker)
		}
	}
	for _, marker := range []string{"a prior-art record names the sources searched", "without that record, return inconclusive"} {
		if !strings.Contains(gatePrompt, marker) {
			t.Errorf("bounty worthiness check must require prior-art evidence: missing %q", marker)
		}
	}

	for _, forbidden := range []string{"locally reproducible path", "missing evidence means rejected"} {
		if strings.Contains(rules, forbidden) || strings.Contains(gatePrompt, forbidden) {
			t.Errorf("bug-bounty policy must not terminally reject findings merely because validation evidence is unavailable: %q", forbidden)
		}
	}

	var writer triggersv1alpha1.SecurityPostScript
	readBootstrapAsset(t, "securitypostscripts", "report-writer", &writer)
	writerPrompt := strings.ToLower(strings.Join(strings.Fields(writer.Spec.Prompt), " "))
	for _, marker := range []string{"submission-ready bundle with their independently validated poc", "triaged findings also produce a review bundle", "without claiming confirmation", "if the save tool succeeds", "without claiming an artifact exists"} {
		if !strings.Contains(writerPrompt, marker) {
			t.Errorf("report writer must preserve triaged findings in a review bundle: missing %q", marker)
		}
	}
}

// TestPriorArtCheckExcludesKnownAndBotFindableWork pins the pre-PoC exclusion
// gate: platforms rule bot-findable findings and their duplicates unpayable,
// so this check must run its searches before any proof budget is spent, and it
// must never turn absent evidence into a confirmed duplicate.
func TestPriorArtCheckExcludesKnownAndBotFindableWork(t *testing.T) {
	t.Parallel()

	var script triggersv1alpha1.SecurityPostScript
	readBootstrapAsset(t, "securitypostscripts", "prior-art-check", &script)
	prompt := strings.ToLower(strings.Join(strings.Fields(script.Spec.Prompt), " "))

	for _, marker := range []string{
		"would a standard detector",
		"slither, aderyn",
		"semgrep-style rule",
		"platform's own bot pass",
		"`knownissues` entries",
		"attached security program scope snapshot",
		"quoted, untrusted policy data",
		"open and closed issues and pull requests",
		"commit messages, release notes",
		"`audits/` or `security/`",
		"security.md",
		"osv, ghsa, and cve",
		"already fixed in the release line",
		"a near match is not automatically a duplicate",
		"what is novel here",
		"never claim a search you did not run",
		"missing or unavailable evidence is inconclusive, never a confirmed match",
	} {
		if !strings.Contains(prompt, marker) {
			t.Errorf("prior-art check must contain exclusion guard %q", marker)
		}
	}
	for _, mapping := range []string{
		"set status `accepted_risk` for a confirmed known-issue or bot-findable match",
		"set status `fixed` when the behaviour is already fixed in the relevant release line",
		"set status `triaged` for novel or inconclusive",
		"keep `confirmed` when the finding is already confirmed and novel",
	} {
		if !strings.Contains(prompt, mapping) {
			t.Errorf("prior-art check must state the status mapping %q", mapping)
		}
	}
	// The check runs before the expensive steps, so it must not overwrite a
	// terminal verdict an earlier script in the pack already recorded.
	if want := strings.ToLower(strings.Join(strings.Fields(terminalStatusInstruction), " ")); !strings.Contains(prompt, want) {
		t.Errorf("prior-art check must carry the terminal-status instruction %q", terminalStatusInstruction)
	}
	for _, status := range terminalFindingStatuses {
		if !strings.Contains(prompt, "`"+status+"`") {
			t.Errorf("prior-art check never mentions the terminal status %q it must not overwrite", status)
		}
	}
}

// terminalFindingStatuses are the verdicts a post-script must never overwrite:
// once a finding is a false positive, an accepted risk, or fixed, a later
// script in the chain that unconditionally sets `triaged` or `confirmed`
// would silently resurrect it.
var terminalFindingStatuses = []string{
	store.SecurityFindingStatusFalsePositive,
	store.SecurityFindingStatusAcceptedRisk,
	store.SecurityFindingStatusFixed,
}

// terminalStatusInstruction is the sentence each post-script prompt carries so
// the status rule it states afterwards cannot undo an earlier script's
// terminal verdict. Post-scripts run in pack order over the same finding.
const terminalStatusInstruction = "A finding that already carries status `false_positive`, `accepted_risk`, or `fixed` keeps it: record your note and leave the status unchanged."

func TestSecurityPostScriptsPreserveTerminalStatuses(t *testing.T) {
	t.Parallel()

	paths, err := filepath.Glob(repoPath("configs", "securitypostscripts", "*.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) == 0 {
		t.Fatal("no shipped post-script assets found")
	}
	for _, path := range paths {
		name := strings.TrimSuffix(filepath.Base(path), ".yaml")
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			var script triggersv1alpha1.SecurityPostScript
			readBootstrapAsset(t, "securitypostscripts", name, &script)

			// The prompt wraps, so compare on collapsed whitespace.
			prompt := strings.Join(strings.Fields(script.Spec.Prompt), " ")
			for _, status := range terminalFindingStatuses {
				if !strings.Contains(prompt, "`"+status+"`") {
					t.Errorf("prompt never mentions the terminal status %q it must not overwrite", status)
				}
			}
			if want := strings.Join(strings.Fields(terminalStatusInstruction), " "); !strings.Contains(prompt, want) {
				t.Errorf("prompt must carry the terminal-status instruction %q", terminalStatusInstruction)
			}
		})
	}
}
