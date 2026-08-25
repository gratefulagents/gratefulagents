package configtest

import (
	"strings"
	"testing"

	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
)

func TestPoCBuilderIsFallbackOnly(t *testing.T) {
	t.Parallel()

	var builder triggersv1alpha1.SecurityPostScript
	readBootstrapAsset(t, "securitypostscripts", "poc-builder", &builder)
	prompt := strings.Join(strings.Fields(builder.Spec.Prompt), " ")

	for _, marker := range []string{
		"call get_security_poc first",
		"current candidate for this exact finding and execution",
		"reuse that canonical PoC",
		"do not rebuild, rerun, or overwrite it",
		"only when get_security_poc reports that no current exact candidate exists",
		"On the fallback path, call save_security_poc",
	} {
		if !strings.Contains(prompt, marker) {
			t.Errorf("poc-builder prompt is missing %q", marker)
		}
	}
	if strings.Index(prompt, "get_security_poc") > strings.Index(prompt, "save_security_poc") {
		t.Error("poc-builder must check for a current candidate before saving a fallback")
	}

	var validator triggersv1alpha1.SecurityPostScript
	readBootstrapAsset(t, "securitypostscripts", "poc-validator", &validator)
	validatorPrompt := strings.Join(strings.Fields(validator.Spec.Prompt), " ")
	if !strings.Contains(validator.Spec.Description, "Independently") {
		t.Error("poc-validator description must remain independent")
	}
	for _, marker := range []string{"immutable candidate_sha256", "Do not replace it with a new PoC", "validate_security_poc"} {
		if !strings.Contains(validatorPrompt, marker) {
			t.Errorf("poc-validator must remain independent: missing %q", marker)
		}
	}
}

func TestPolicyDispositionsPreserveTechnicalStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		markers []string
	}{
		{"prior-art-check", []string{"`known_issue`", "`bot_findable`", "`not_ready`", "retain `confirmed`", "set status `triaged`"}},
		{"scope-eligibility-check", []string{"`scope_excluded`", "`not_ready`", "retain `confirmed`", "set status `triaged`"}},
		{"bounty-worthiness-check", []string{"`scope_excluded`", "`known_issue`", "`not_ready`", "set status `confirmed`", "set status `triaged`"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var script triggersv1alpha1.SecurityPostScript
			readBootstrapAsset(t, "securitypostscripts", tc.name, &script)
			prompt := strings.Join(strings.Fields(script.Spec.Prompt), " ")
			for _, marker := range tc.markers {
				if !strings.Contains(prompt, marker) {
					t.Errorf("prompt is missing %q", marker)
				}
			}
			if !strings.Contains(prompt, "Only an explicit owner decision may set status `accepted_risk`") {
				t.Error("prompt must reserve accepted_risk for an explicit owner decision")
			}
			for _, forbidden := range []string{
				"set status `accepted_risk` for a confirmed known-issue",
				"set status `accepted_risk` only for a definitive scope exclusion",
				"`accepted_risk` for out-of-scope",
			} {
				if strings.Contains(prompt, forbidden) {
					t.Errorf("prompt retains policy-to-risk mapping %q", forbidden)
				}
			}
		})
	}
}
