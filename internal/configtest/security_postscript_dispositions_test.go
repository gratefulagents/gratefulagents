package configtest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"

	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
)

// accepted_risk is a human decision recorded from the dashboard; the
// update_security_finding tool rejects it for scan runs. A post-script prompt
// that still instructs the model to set it would only produce tool errors.
func TestPostScriptPromptsNeverSetAcceptedRisk(t *testing.T) {
	t.Parallel()

	paths, err := filepath.Glob(repoPath("configs", "securitypostscripts", "*.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) == 0 {
		t.Fatal("no post-script prompts found")
	}
	for _, path := range paths {
		name := strings.TrimSuffix(filepath.Base(path), ".yaml")
		var script triggersv1alpha1.SecurityPostScript
		readBootstrapAsset(t, "securitypostscripts", name, &script)
		prompt := strings.ToLower(strings.Join(strings.Fields(script.Spec.Prompt), " "))
		for _, forbidden := range []string{
			"set status `accepted_risk`",
			"set status accepted_risk",
			"status to `accepted_risk`",
			"status to accepted_risk",
			"become status accepted_risk",
			"become status `accepted_risk`",
		} {
			if strings.Contains(prompt, forbidden) {
				t.Errorf("%s instructs %q; accepted_risk is a human-only decision", name, forbidden)
			}
		}
	}
}

// The scripts that run a PoC must know the environment escape hatch, so an
// unbuildable repository or a missing fork endpoint becomes a retried
// unreproducible_env disposition instead of a terminal status.
func TestPoCScriptsRecordUnreproducibleEnvironment(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"poc-validator", "poc-builder", "validate-finding", "bounty-worthiness-check"} {
		var script triggersv1alpha1.SecurityPostScript
		readBootstrapAsset(t, "securitypostscripts", name, &script)
		prompt := strings.Join(strings.Fields(script.Spec.Prompt), " ")
		for _, marker := range []string{"`unreproducible_env`", "`triaged`", "`accepted_risk` is a human decision recorded from the dashboard; a post-script never sets it."} {
			if !strings.Contains(prompt, marker) {
				t.Errorf("%s prompt is missing %q", name, marker)
			}
		}
		check := "`policy_check` `reproduction`"
		if name == "bounty-worthiness-check" {
			check = "`policy_check` set to `bounty`"
		}
		if !strings.Contains(prompt, check) {
			t.Errorf("%s prompt must bind unreproducible_env to %s", name, check)
		}
	}
}

// Workflow tasks are scan-run actors too; none may instruct the model to
// record accepted_risk.
func TestWorkflowObjectivesNeverSetAcceptedRisk(t *testing.T) {
	t.Parallel()

	paths, err := filepath.Glob(repoPath("configs", "securityworkflows", "*.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) == 0 {
		t.Fatal("no workflows found")
	}
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var workflow triggersv1alpha1.SecurityWorkflow
		if err := yaml.UnmarshalStrict(raw, &workflow); err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		for _, task := range workflow.Spec.Tasks {
			objective := strings.ToLower(strings.Join(strings.Fields(task.Objective), " "))
			for _, forbidden := range []string{"become status accepted_risk", "set status accepted_risk", "set status `accepted_risk`"} {
				if strings.Contains(objective, forbidden) {
					t.Errorf("%s task %q instructs %q; accepted_risk is a human-only decision", filepath.Base(path), task.Name, forbidden)
				}
			}
		}
	}
}
