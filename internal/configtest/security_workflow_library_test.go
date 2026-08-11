package configtest

import (
	"encoding/json"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"

	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	runtimetools "github.com/gratefulagents/gratefulagents/internal/tools"
)

// workflowTaskOutputRefPattern matches the {{tasks.<name>.output}} references
// an objective interpolates.
var workflowTaskOutputRefPattern = regexp.MustCompile(`\{\{\s*tasks\.([a-zA-Z0-9-]+)\.output`)

// securityWorkflowLibrary lists the SecurityWorkflow assets shipped in
// configs/securityworkflows/ and mirrored into the chart bootstrap, so Helm
// installs a usable bug-hunting library into every release namespace.
var securityWorkflowLibrary = []string{
	"api-service-audit",
	"auth-surface-audit",
	"blockchain-protocol-audit",
	"cosmos-abci-halt-review",
	"default-deep-scan",
	"external-flow-analysis",
	"kubernetes-operator-audit",
	"pr-diff-review",
	"secrets-and-supply-chain",
	"smart-contract-review",
	"validated-critical-hunt",
	"web-app-owasp",
}

// TestSecurityWorkflowLibraryAssets holds every shipped workflow to the rules
// the SecurityScan controller and the deterministic engine enforce at runtime:
// a workflow that fails them is rejected after installation, when the operator
// has already pointed a scan at it.
func TestSecurityWorkflowLibraryAssets(t *testing.T) {
	t.Parallel()

	for _, name := range securityWorkflowLibrary {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			var workflow triggersv1alpha1.SecurityWorkflow
			readBootstrapAsset(t, "securityworkflows", name, &workflow)

			if workflow.Name != name {
				t.Fatalf("metadata.name = %q, want %q", workflow.Name, name)
			}
			if strings.TrimSpace(workflow.Spec.Description) == "" {
				t.Error("workflow must carry a description for the library catalog")
			}
			if errs := triggersv1alpha1.ValidateSecurityWorkflowTasks(workflow.Spec.Tasks); len(errs) != 0 {
				t.Errorf("tasks fail validation: %v", errs)
			}
			if errs := triggersv1alpha1.ValidateSecurityWorkflowParameters(workflow.Spec.Parameters); len(errs) != 0 {
				t.Errorf("parameters fail validation: %v", errs)
			}
			if p := workflow.Spec.Parallelism; p != 0 && (p < 1 || p > 16) {
				t.Errorf("parallelism = %d, want unset or 1-16", p)
			}

			// Only sink tasks receive the scan-report instruction, so a
			// workflow with two sinks would submit two reports and one with
			// none would submit nothing.
			depended := make(map[string]bool, len(workflow.Spec.Tasks))
			for _, task := range workflow.Spec.Tasks {
				for _, dep := range task.DependsOn {
					depended[dep] = true
				}
			}
			var sinks []string
			for _, task := range workflow.Spec.Tasks {
				if !depended[task.Name] {
					sinks = append(sinks, task.Name)
				}
			}
			if len(sinks) != 1 || sinks[0] != "triage-and-report" {
				t.Errorf("sink tasks = %v, want exactly [triage-and-report]", sinks)
			}

			byName := make(map[string]triggersv1alpha1.SecurityScanTask, len(workflow.Spec.Tasks))
			for _, task := range workflow.Spec.Tasks {
				byName[task.Name] = task
			}
			for _, task := range workflow.Spec.Tasks {
				role := task.EffectiveRole()
				if _, err := os.Stat(repoPath("configs", "roleinstructions", role+".yaml")); err != nil {
					t.Errorf("task %q references role %q with no shipped RoleInstruction asset: %v", task.Name, role, err)
				}
				for _, ref := range task.SkillRefs {
					if _, err := os.Stat(repoPath("configs", "skills", ref.Name+".yaml")); err != nil {
						t.Errorf("task %q references skill %q with no shipped Skill asset: %v", task.Name, ref.Name, err)
					}
				}
				if task.Tools == nil || !slices.Contains(task.Tools.Denied, "Bash") {
					t.Errorf("task %q must deny the registered Bash tool so loaded skills cannot execute untrusted repository code", task.Name)
				}
				// A task only gets the submit_task_output tool when it
				// declares outputSchema, so interpolating the output of a
				// schemaless task waits for data that can never arrive.
				for _, match := range workflowTaskOutputRefPattern.FindAllStringSubmatch(task.Objective, -1) {
					source, ok := byName[match[1]]
					if !ok {
						continue // ValidateSecurityWorkflowTasks reports the dangling reference.
					}
					if strings.TrimSpace(source.OutputSchema) == "" {
						t.Errorf("task %q interpolates {{tasks.%s.output}} but %q declares no outputSchema", task.Name, source.Name, source.Name)
					}
				}
				if schema := strings.TrimSpace(task.OutputSchema); schema != "" {
					var object map[string]any
					if err := json.Unmarshal([]byte(schema), &object); err != nil {
						t.Errorf("task %q outputSchema is not valid JSON: %v", task.Name, err)
					} else if object == nil {
						t.Errorf("task %q outputSchema must be a JSON object", task.Name)
					}
				}
				if task.ForEach == "" {
					continue
				}
				// The engine fans out over the source's structured output, so
				// the source has to promise a JSON array.
				source, ok := byName[task.ForEach]
				if !ok {
					t.Errorf("task %q fans out over unknown task %q", task.Name, task.ForEach)
					continue
				}
				var schema struct {
					Type string `json:"type"`
				}
				if err := json.Unmarshal([]byte(source.OutputSchema), &schema); err != nil {
					t.Errorf("task %q forEach source %q outputSchema is not valid JSON: %v", task.Name, source.Name, err)
					continue
				}
				if schema.Type != "array" {
					t.Errorf("task %q forEach source %q outputSchema type = %q, want array", task.Name, source.Name, schema.Type)
				}
			}

			// Connect the shipped policy spelling to the case-sensitive runtime
			// registry so a typo cannot silently leave the shell registered.
			first := workflow.Spec.Tasks[0]
			registry := runtimetools.NewRegistry(t.TempDir(), runtimetools.WithToolNameFilter(nil, first.Tools.Denied))
			if registry.Get("Bash") != nil {
				t.Errorf("task %q denial did not remove the registered Bash tool", first.Name)
			}
		})
	}
}

// TestSmartContractReviewLifecycle prevents the EVM workflow from regressing
// back to a collection of disconnected manual checklists. The lifecycle stages
// and executable-tool boundary are part of the shipped workflow's contract.
func TestSmartContractReviewLifecycle(t *testing.T) {
	t.Parallel()

	var workflow triggersv1alpha1.SecurityWorkflow
	readBootstrapAsset(t, "securityworkflows", "smart-contract-review", &workflow)

	byName := make(map[string]triggersv1alpha1.SecurityScanTask, len(workflow.Spec.Tasks))
	for _, task := range workflow.Spec.Tasks {
		byName[task.Name] = task
	}
	required := []string{
		"inventory-scope-repositories-deployments",
		"map-architecture-assets-roles-entrypoints",
		"define-security-invariants",
		"reproducible-build-artifact-deployment-review",
		"aderyn-static-analysis",
		"deterministic-forge-tests-and-invariants",
		"echidna-stateful-property-fuzzing",
		"bounded-mythril-symbolic-analysis",
		"symbolic-and-formal-applicability",
		"deployment-and-privileged-configuration",
		"validate-high-impact-exploits",
		"account-lifecycle-coverage",
		"remediation-and-retest",
		"triage-and-report",
	}
	for _, name := range required {
		if _, ok := byName[name]; !ok {
			t.Errorf("required EVM lifecycle task %q is missing", name)
		}
	}

	for name, tool := range map[string]string{
		"aderyn-static-analysis":                   "aderyn",
		"deterministic-forge-tests-and-invariants": "forge-security-tests",
		"echidna-stateful-property-fuzzing":        "echidna",
		"bounded-mythril-symbolic-analysis":        "mythril",
	} {
		task, ok := byName[name]
		if !ok {
			continue
		}
		objective := strings.ToLower(task.Objective)
		if !strings.Contains(objective, "run_security_tool") || !strings.Contains(objective, tool) {
			t.Errorf("task %q must execute %s through run_security_tool", name, tool)
		}
	}

	triage, ok := byName["triage-and-report"]
	if ok {
		objective := strings.ToLower(triage.Objective)
		for _, status := range []string{"examined", "skipped", "unsupported", "inconclusive", "retest"} {
			if !strings.Contains(objective, status) {
				t.Errorf("triage-and-report must account for %q coverage", status)
			}
		}
	}
}
