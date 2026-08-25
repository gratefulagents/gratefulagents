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
		"Call update_security_finding with `poc_reused` and candidate_sha256 in `note`",
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

func TestPoCScriptsRequireProjectPrescribedWorkflow(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		markers []string
	}{
		{
			name: "poc-builder",
			markers: []string{
				"determine how this exact project requires a PoC to be built and run",
				"security program's policy and pocEnvironment",
				"Satisfy all applicable requirements",
				"generic repository or CI guidance cannot relax program policy or pocEnvironment",
				"Treat conflicts as non-reproduction, not permission to choose a weaker rule",
				"all applicable project and program requirements explicitly accept it as PoC evidence",
				"unit tests, mocks, or synthetic harnesses are insufficient",
				"use the required integration, e2e, fork, devnet, replay, binary, deployment",
				"project's prescribed toolchain and commands",
				"do not replace it with weaker evidence",
			},
		},
		{
			name: "poc-validator",
			markers: []string{
				"Independently determine how this exact project requires a PoC to be built and validated",
				"security program's policy and pocEnvironment",
				"Satisfy all applicable requirements",
				"generic repository or CI guidance cannot relax program policy or pocEnvironment",
				"Treat conflicts as non-reproduction, not permission to choose a weaker rule",
				"all applicable project and program requirements explicitly accept unit tests as PoC evidence",
				"Do not assume the stored command or harness is sufficient",
				"unit tests, mocks, or synthetic harnesses are insufficient",
				"validation must use the required integration, e2e, fork, devnet, replay, binary, deployment",
				"fail validation honestly",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var script triggersv1alpha1.SecurityPostScript
			readBootstrapAsset(t, "securitypostscripts", tc.name, &script)
			prompt := strings.Join(strings.Fields(script.Spec.Prompt), " ")
			for _, marker := range tc.markers {
				if !strings.Contains(prompt, marker) {
					t.Errorf("%s prompt is missing project-workflow requirement %q", tc.name, marker)
				}
			}
		})
	}
}

func TestPoCBuilderRequiresReproducibleEvidence(t *testing.T) {
	t.Parallel()

	var builder triggersv1alpha1.SecurityPostScript
	readBootstrapAsset(t, "securitypostscripts", "poc-builder", &builder)
	prompt := strings.Join(strings.Fields(builder.Spec.Prompt), " ")
	for _, marker := range []string{
		"Prove real target code executed",
		"same unchanged harness and oracle",
		"non-attacker input as a negative control",
		"calibration case known to make the assertion fail",
		"capture both transcripts",
		"Re-run the exploit against unmodified target code",
		"successful command or assertion that cannot fail is not a reproduction",
		"runnable from a clean checkout",
		"prerequisites, setup, environment variables, fixtures, seeds, generated assets",
		"Do not rely on unrecorded workspace state or prior commands",
		"stop instead of pivoting to a weaker harness",
		"self-contained artifact, complete transcript, and control/oracle evidence",
	} {
		if !strings.Contains(prompt, marker) {
			t.Errorf("poc-builder prompt is missing reproducibility requirement %q", marker)
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
