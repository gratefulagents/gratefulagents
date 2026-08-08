package configtest

import (
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
	{"poc-builder", "high-and-above"},
	{"exploitability-score", "all"},
	{"patched-since-check", "all"},
	{"resource-exhaustion-classifier", "all"},
	{"false-positive-check", "all"},
	{"scope-eligibility-check", "all"},
}

// securityTriageRankers are the shipped severity rankers for bug hunting.
var securityTriageRankers = []string{
	"web-app-impact",
	"api-service-impact",
	"blockchain-impact",
	"bug-bounty-triage",
	"library-impact",
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

func TestSecurityTriageRankerAssets(t *testing.T) {
	t.Parallel()

	for _, name := range securityTriageRankers {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			var ranker triggersv1alpha1.SecurityRanker
			readBootstrapAsset(t, "securityrankers", name, &ranker)

			if ranker.Kind != "SecurityRanker" {
				t.Errorf("kind = %q, want SecurityRanker", ranker.Kind)
			}
			if ranker.Name != name {
				t.Errorf("metadata.name = %q, want %q", ranker.Name, name)
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

			floors := 0
			for i, rule := range ranker.Spec.Rules {
				trimmed := strings.TrimSpace(rule)
				if trimmed == "" {
					t.Errorf("rules[%d] is blank", i)
					continue
				}
				directive, value, isDirective := strings.Cut(trimmed, ":")
				if isDirective && strings.TrimSpace(directive) == "severity-floor" {
					floors++
					category, severity, ok := strings.Cut(strings.TrimSpace(value), "=")
					if !ok {
						t.Errorf("rules[%d] = %q, want severity-floor: <category>=<severity>", i, trimmed)
						continue
					}
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
			if floors == 0 {
				t.Error("ranker must set at least one severity-floor directive")
			}
		})
	}
}
