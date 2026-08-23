package configtest

import (
	"encoding/json"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	runtimetools "github.com/gratefulagents/gratefulagents/internal/tools"
)

// workflowTaskOutputRefPattern matches the {{tasks.<name>.output}} references
// an objective interpolates.
var workflowTaskOutputRefPattern = regexp.MustCompile(`\{\{\s*tasks\.([a-zA-Z0-9-]+)\.output`)

// securityWorkflowLibrary lists the SecurityWorkflow assets shipped in
// configs/securityworkflows/ and mirrored into the chart bootstrap, so Helm
// installs a usable bug-hunting library into every release namespace.
var blockchainSecurityWorkflowLibrary = []string{
	"algorand-security-review",
	"bounty-hunt-evm",
	"aptos-move-security-review",
	"bitcoin-lightning-security-review",
	"blockchain-protocol-audit",
	"bridge-l2-zk-security-review",
	"cairo-starknet-security-review",
	"cosmos-abci-halt-review",
	"cosmos-ibc-security-review",
	"mpc-cryptography-security-review",
	"off-chain-services-security-review",
	"smart-contract-review",
	"solana-anchor-security-review",
	"substrate-xcm-security-review",
	"sui-move-security-review",
	"ton-security-review",
	"wallet-security-review",
	"cross-chain-messaging-review",
	"evm-lending-cdp-review",
	"evm-orderbook-settlement-review",
	"flow-cadence-review",
	"near-contract-review",
	"rollup-stack-review",
	"solana-defi-program-review",
}

// programLinkedSecurityWorkflows are the workflows selected by the shipped
// SecurityProgram scan targets. These are bounty-facing entry points, so a
// prompt-only finding must not bypass local proof, sibling search, campaign
// reconciliation, or the post-script bundle handoff.
var programLinkedSecurityWorkflows = []string{
	"aptos-move-security-review",
	"blockchain-protocol-audit",
	"bounty-hunt-evm",
	"bridge-l2-zk-security-review",
	"cosmos-abci-halt-review",
	"cross-chain-messaging-review",
	"evm-lending-cdp-review",
	"evm-orderbook-settlement-review",
	"flow-cadence-review",
	"mpc-cryptography-security-review",
	"near-contract-review",
	"rollup-stack-review",
	"solana-defi-program-review",
	"substrate-xcm-security-review",
	"wallet-security-review",
}

var fullAccessWebWorkflowLibrary = []string{
	"web-access-control-assessment",
	"web-api-assessment",
	"web-app-full-assessment",
	"web-auth-session-assessment",
	"web-business-logic-assessment",
	"web-client-side-assessment",
	"web-deployment-exposure-assessment",
	"web-recon-passive",
	"web-retest-confirmed-findings",
	"web-server-side-input-assessment",
}

var securityWorkflowLibrary = []string{
	"api-service-audit",
	"algorand-security-review",
	"aptos-move-security-review",
	"auth-surface-audit",
	"bitcoin-lightning-security-review",
	"blockchain-protocol-audit",
	"bounty-hunt-evm",
	"bridge-l2-zk-security-review",
	"cairo-starknet-security-review",
	"cosmos-abci-halt-review",
	"cosmos-ibc-security-review",
	"default-deep-scan",
	"external-flow-analysis",
	"kubernetes-operator-audit",
	"mpc-cryptography-security-review",
	"native-fuzz-campaign",
	"off-chain-services-security-review",
	"pr-diff-review",
	"secrets-and-supply-chain",
	"smart-contract-review",
	"solana-anchor-security-review",
	"substrate-xcm-security-review",
	"sui-move-security-review",
	"ton-security-review",
	"validated-critical-hunt",
	"wallet-security-review",
	"web-access-control-assessment",
	"web-api-assessment",
	"web-app-full-assessment",
	"web-app-owasp",
	"web-auth-session-assessment",
	"web-business-logic-assessment",
	"web-client-side-assessment",
	"web-deployment-exposure-assessment",
	"web-recon-passive",
	"web-retest-confirmed-findings",
	"web-server-side-input-assessment",
	"cross-chain-messaging-review",
	"evm-lending-cdp-review",
	"evm-orderbook-settlement-review",
	"flow-cadence-review",
	"near-contract-review",
	"rollup-stack-review",
	"solana-defi-program-review",
}

// TestSecurityWorkflowLibraryInventory prevents new bootstrap workflows from
// bypassing TestSecurityWorkflowLibraryAssets when this inventory is stale.
func TestSecurityWorkflowLibraryInventory(t *testing.T) {
	t.Parallel()

	entries, err := os.ReadDir(repoPath("configs", "securityworkflows"))
	if err != nil {
		t.Fatalf("read security workflow library: %v", err)
	}
	var discovered []string
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".yaml") {
			discovered = append(discovered, strings.TrimSuffix(entry.Name(), ".yaml"))
		}
	}
	slices.Sort(discovered)
	want := slices.Clone(securityWorkflowLibrary)
	slices.Sort(want)
	if !slices.Equal(discovered, want) {
		t.Fatalf("securityWorkflowLibrary = %v, want every shipped workflow %v", want, discovered)
	}
}

func TestFullAccessWebWorkflowsUsePromptOnlyLiveSafety(t *testing.T) {
	t.Parallel()

	for _, name := range fullAccessWebWorkflowLibrary {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			var workflow triggersv1alpha1.SecurityWorkflow
			readBootstrapAsset(t, "securityworkflows", name, &workflow)
			hasHTTPMethod := false
			for _, task := range workflow.Spec.Tasks {
				for _, ref := range task.SkillRefs {
					if ref.Name == "http-pentesting-method" {
						hasHTTPMethod = true
					}
				}
				if slices.ContainsFunc(task.SkillRefs, func(ref platformv1alpha1.NamedRef) bool {
					return ref.Name == "web-app-hunting"
				}) && !slices.ContainsFunc(task.SkillRefs, func(ref platformv1alpha1.NamedRef) bool {
					return ref.Name == "http-pentesting-method"
				}) {
					t.Errorf("task %q uses web-app-hunting without the live HTTP method", task.Name)
				}
				if task.Tools != nil {
					t.Errorf("task %q narrows tools; web workflows must retain the full run tool surface", task.Name)
				}
				if refs := workflowTaskOutputRefPattern.FindAllStringSubmatch(task.Objective, -1); len(refs) > 3 {
					t.Errorf("task %q interpolates %d upstream outputs; rendered objectives support at most three full task outputs", task.Name, len(refs))
				}
				if task.Name == "triage-and-report" {
					continue
				}
				objective := strings.ToLower(task.Objective)
				fullTools := strings.Contains(objective, "full tool") ||
					strings.Contains(objective, "full run tool") ||
					strings.Contains(objective, "full available") ||
					strings.Contains(objective, "all available tools") ||
					strings.Contains(objective, "keep all available tools") ||
					strings.Contains(objective, "tool surface remains available")
				if !fullTools {
					t.Errorf("task %q must state that full tool availability is intentional", task.Name)
				}
				promptOnlySafety := strings.Contains(objective, "stateful") ||
					strings.Contains(objective, "state-changing") ||
					strings.Contains(objective, "state change") ||
					strings.Contains(objective, "change state") ||
					strings.Contains(objective, "change server state") ||
					strings.Contains(objective, "mutating request")
				if !promptOnlySafety {
					t.Errorf("task %q must carry the prompt-only no-state-change rule", task.Name)
				}
			}
			if !hasHTTPMethod {
				t.Error("full-access web workflow must reference http-pentesting-method")
			}
		})
	}
}

func TestBlockchainSecurityWorkflowsUseResearchMethod(t *testing.T) {
	t.Parallel()

	for _, name := range blockchainSecurityWorkflowLibrary {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			var workflow triggersv1alpha1.SecurityWorkflow
			readBootstrapAsset(t, "securityworkflows", name, &workflow)
			if len(workflow.Spec.Tasks) == 0 {
				t.Fatal("blockchain workflow must have at least one task")
			}
			for _, task := range workflow.Spec.Tasks {
				if !slices.ContainsFunc(task.SkillRefs, func(ref platformv1alpha1.NamedRef) bool {
					return ref.Name == "blockchain-security-research-method"
				}) {
					t.Errorf("task %q must reference blockchain-security-research-method", task.Name)
				}
			}
		})
	}
}

func TestProgramLinkedWorkflowsCloseTheFindingLifecycle(t *testing.T) {
	t.Parallel()

	for _, name := range programLinkedSecurityWorkflows {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			var workflow triggersv1alpha1.SecurityWorkflow
			readBootstrapAsset(t, "securityworkflows", name, &workflow)
			all := strings.ToLower(workflow.Spec.Description)
			for _, task := range workflow.Spec.Tasks {
				all += "\n" + strings.ToLower(task.Objective)
			}
			for _, marker := range []string{
				"negative control",
				"oracle",
				"create_security_variant_sweep",
				"complete_security_variant_sweep",
				"get_security_campaign_status",
				"bundle",
			} {
				if !strings.Contains(all, marker) {
					t.Errorf("program-linked workflow is missing lifecycle marker %q", marker)
				}
			}
			triage := workflow.Spec.Tasks[len(workflow.Spec.Tasks)-1]
			if triage.Name != "triage-and-report" {
				t.Fatalf("last task = %q, want triage-and-report", triage.Name)
			}
			final := strings.ToLower(triage.Objective)
			for _, marker := range []string{"get_security_campaign_status", "bundle"} {
				if !strings.Contains(final, marker) {
					t.Errorf("final triage is missing %q", marker)
				}
			}
		})
	}
}

var evidenceContractBlockchainWorkflows = []string{
	"algorand-security-review",
	"aptos-move-security-review",
	"bitcoin-lightning-security-review",
	"bridge-l2-zk-security-review",
	"cairo-starknet-security-review",
	"cosmos-ibc-security-review",
	"mpc-cryptography-security-review",
	"off-chain-services-security-review",
	"solana-anchor-security-review",
	"substrate-xcm-security-review",
	"sui-move-security-review",
	"ton-security-review",
	"wallet-security-review",
}

func TestBlockchainValidationEvidenceContracts(t *testing.T) {
	t.Parallel()

	validConfirmed := `{"examined":[],"skipped":[],"unsupported":[],"inconclusive":[],"candidate_results":[{"fingerprint":"finding-1","verdict":"confirmed","attempts":1,"reachability":"public entry point reaches the state transition","impact":"one unit leaves protocol custody","reproduction":{"command":"project-test candidate-1","tool_run":"run-1","failing_assertion":"conservation invariant","observed_delta":"protocol=-1 attacker=+1","negative_control_passed":true,"oracle_can_fail":true}}],"uncovered":[],"limitations":[]}`
	invalidConfirmed := `{"examined":[],"skipped":[],"unsupported":[],"inconclusive":[],"candidate_results":[{"fingerprint":"finding-1","verdict":"confirmed","attempts":1}],"uncovered":[],"limitations":[]}`
	validTriaged := `{"examined":[],"skipped":[],"unsupported":[],"inconclusive":[],"candidate_results":[{"fingerprint":"finding-2","verdict":"triaged","attempts":1,"blocker":"required local toolchain is unavailable"}],"uncovered":[],"limitations":[]}`
	invalidTriaged := `{"examined":[],"skipped":[],"unsupported":[],"inconclusive":[],"candidate_results":[{"fingerprint":"finding-2","verdict":"triaged","attempts":1}],"uncovered":[],"limitations":[]}`
	validFalsePositive := `{"examined":[],"skipped":[],"unsupported":[],"inconclusive":[],"candidate_results":[{"fingerprint":"finding-3","verdict":"false_positive","attempts":1,"disproof":"the production caller verifies the signer before dispatch"}],"uncovered":[],"limitations":[]}`
	invalidFalsePositive := `{"examined":[],"skipped":[],"unsupported":[],"inconclusive":[],"candidate_results":[{"fingerprint":"finding-3","verdict":"false_positive","attempts":1}],"uncovered":[],"limitations":[]}`
	invalidEmptyEvidence := `{"examined":[],"skipped":[],"unsupported":[],"inconclusive":[],"candidate_results":[{"fingerprint":"finding-1","verdict":"confirmed","attempts":1,"reachability":"","impact":"loss","reproduction":{"command":"test","tool_run":"run-1","failing_assertion":"invariant","observed_delta":"delta","negative_control_passed":true,"oracle_can_fail":true}}],"uncovered":[],"limitations":[]}`
	invalidControls := `{"examined":[],"skipped":[],"unsupported":[],"inconclusive":[],"candidate_results":[{"fingerprint":"finding-1","verdict":"confirmed","attempts":1,"reachability":"reachable","impact":"loss","reproduction":{"command":"test","tool_run":"run-1","failing_assertion":"invariant","observed_delta":"delta","negative_control_passed":false,"oracle_can_fail":false}}],"uncovered":[],"limitations":[]}`
	trackedConfirmed := `{"examined":[],"skipped":[],"unsupported":[],"inconclusive":[],"candidate_results":[{"candidate_id":"candidate-1","fingerprint":"finding-1","hypothesis_id":"hypothesis-1","hypothesis_version":2,"hypothesis_status":"promoted","hypothesis_result":"positive","verdict":"confirmed","attempts":1,"reachability":"public entry point reaches transition","impact":"one unit leaves custody","coverage_ids":["coverage-1"],"reproduction":{"setup":"build pinned revision","environment":"local fixture with dummy assets","command":"project-test candidate-1","test_path":"test/candidate_1","tool_run":"run-1","failing_assertion":"conservation invariant","expected_output":"transition rejected","observed_output":"transition accepted","observed_delta":"protocol=-1 attacker=+1","negative_control_passed":true,"oracle_calibrated":true}}],"coverage_records":[],"uncovered":[],"limitations":[]}`
	trackedTriaged := `{"examined":[],"skipped":[],"unsupported":[],"inconclusive":[],"candidate_results":[{"candidate_id":"candidate-2","fingerprint":"finding-2","hypothesis_id":"hypothesis-2","hypothesis_version":2,"hypothesis_status":"blocked","hypothesis_result":"inconclusive","verdict":"triaged","attempts":1,"blocker":"required local toolchain is unavailable"}],"coverage_records":[],"uncovered":[],"limitations":[]}`
	trackedFalsePositive := `{"examined":[],"skipped":[],"unsupported":[],"inconclusive":[],"candidate_results":[{"candidate_id":"candidate-3","fingerprint":"finding-3","hypothesis_id":"hypothesis-3","hypothesis_version":2,"hypothesis_status":"falsified","hypothesis_result":"negative","verdict":"false_positive","attempts":1,"disproof":"production caller verifies signer"}],"coverage_records":[],"uncovered":[],"limitations":[]}`
	trackedInvalidControls := strings.Replace(trackedConfirmed, `"negative_control_passed":true`, `"negative_control_passed":false`, 1)

	for _, name := range evidenceContractBlockchainWorkflows {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var workflow triggersv1alpha1.SecurityWorkflow
			readBootstrapAsset(t, "securityworkflows", name, &workflow)
			var validation *triggersv1alpha1.SecurityScanTask
			for i := range workflow.Spec.Tasks {
				if workflow.Spec.Tasks[i].Name == "validate-triage-and-coverage" {
					validation = &workflow.Spec.Tasks[i]
					break
				}
			}
			if validation == nil {
				t.Fatal("missing validate-triage-and-coverage task")
			}
			if !strings.Contains(validation.Objective, "never submit an unvalidated candidate") {
				t.Error("validation objective must cover every candidate intended for the final findings list")
			}
			validOutputs := []string{validConfirmed, validTriaged, validFalsePositive}
			invalidOutputs := []string{invalidConfirmed, invalidTriaged, invalidFalsePositive, invalidEmptyEvidence, invalidControls}
			if strings.Contains(validation.OutputSchema, `"coverage_records"`) {
				validOutputs = []string{trackedConfirmed, trackedTriaged, trackedFalsePositive}
				invalidOutputs = []string{trackedInvalidControls}
			}
			for _, output := range validOutputs {
				if err := triggersv1alpha1.ValidateSecurityWorkflowOutput(validation.OutputSchema, output); err != nil {
					t.Errorf("valid evidence output rejected: %v", err)
				}
			}
			for _, output := range invalidOutputs {
				if err := triggersv1alpha1.ValidateSecurityWorkflowOutput(validation.OutputSchema, output); err == nil {
					t.Errorf("evidence-free verdict accepted: %s", output)
				}
			}
		})
	}
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
				if task.Tools != nil && slices.Contains(task.Tools.Denied, "Bash") {
					t.Errorf("task %q must allow the registered Bash tool for local validation", task.Name)
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

			// Connect the shipped workflow policy to the case-sensitive runtime
			// registry so local validation always has the Bash tool available.
			first := workflow.Spec.Tasks[0]
			var denied []string
			if first.Tools != nil {
				denied = first.Tools.Denied
			}
			registry := runtimetools.NewRegistry(t.TempDir(), runtimetools.WithToolNameFilter(nil, denied))
			if registry.Get("Bash") == nil {
				t.Errorf("task %q policy removed the registered Bash tool", first.Name)
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
		"slither-static-analysis",
		"merge-solidity-static-analysis",
		"deterministic-forge-tests-and-invariants",
		"echidna-stateful-property-fuzzing",
		"bounded-halmos-symbolic-tests",
		"merge-symbolic-tool-coverage",
		"symbolic-and-formal-applicability",
		"deployment-and-privileged-configuration",
		"validate-high-impact-exploits",
		"account-foundation-coverage",
		"account-build-and-static-coverage",
		"account-fuzz-and-formal-coverage",
		"account-calls-auth-accounting-coverage",
		"account-economics-oracle-liveness-coverage",
		"account-low-level-and-configuration-coverage",
		"account-scope-and-tool-coverage",
		"account-specialist-coverage",
		"account-lifecycle-coverage",
		"remediation-and-retest",
		"triage-and-report",
	}
	for _, name := range required {
		if _, ok := byName[name]; !ok {
			t.Errorf("required EVM lifecycle task %q is missing", name)
		}
	}

	// The lifecycle ledger used to interpolate every upstream output in one
	// objective. Large multi-repository reviews could therefore exceed the
	// deterministic engine's 256 KiB rendered-objective limit before the task
	// started. Keep the aggregation tree to at most three 64 KiB outputs per
	// hop, including the final validation merge.
	coverageInputs := map[string][]string{
		"account-foundation-coverage": {
			"inventory-scope-repositories-deployments",
			"map-architecture-assets-roles-entrypoints",
			"define-security-invariants",
		},
		"merge-solidity-static-analysis": {
			"slither-static-analysis",
		},
		"account-build-and-static-coverage": {
			"reproducible-build-artifact-deployment-review",
			"merge-solidity-static-analysis",
			"deterministic-forge-tests-and-invariants",
		},
		"merge-symbolic-tool-coverage": {
			"echidna-stateful-property-fuzzing",
			"bounded-halmos-symbolic-tests",
		},
		"account-fuzz-and-formal-coverage": {
			"merge-symbolic-tool-coverage",
			"symbolic-and-formal-applicability",
		},
		"account-calls-auth-accounting-coverage": {
			"external-calls-reentrancy-and-atomicity",
			"authorization-signatures-and-account-abstraction",
			"accounting-arithmetic-and-token-integrations",
		},
		"account-economics-oracle-liveness-coverage": {
			"governance-liquidations-auctions-and-mev",
			"oracles-randomness-time-and-ordering",
			"gas-liveness-returndata-and-state-growth",
		},
		"account-low-level-and-configuration-coverage": {
			"low-level-factories-proxies-and-storage",
			"deployment-and-privileged-configuration",
		},
		"account-scope-and-tool-coverage": {
			"account-foundation-coverage",
			"account-build-and-static-coverage",
			"account-fuzz-and-formal-coverage",
		},
		"account-specialist-coverage": {
			"account-calls-auth-accounting-coverage",
			"account-economics-oracle-liveness-coverage",
			"account-low-level-and-configuration-coverage",
		},
		"account-lifecycle-coverage": {
			"account-scope-and-tool-coverage",
			"account-specialist-coverage",
			"validate-high-impact-exploits",
		},
	}
	for name, want := range coverageInputs {
		task, ok := byName[name]
		if !ok {
			continue
		}
		matches := workflowTaskOutputRefPattern.FindAllStringSubmatch(task.Objective, -1)
		got := make([]string, 0, len(matches))
		for _, match := range matches {
			got = append(got, match[1])
		}
		if !slices.Equal(got, want) {
			t.Errorf("%s output refs = %v, want %v", name, got, want)
		}
		if !slices.Equal(task.DependsOn, want) {
			t.Errorf("%s dependencies = %v, want %v", name, task.DependsOn, want)
		}

		var schema struct {
			Items struct {
				Required   []string `json:"required"`
				Properties map[string]struct {
					Type string `json:"type"`
				} `json:"properties"`
			} `json:"items"`
		}
		if err := json.Unmarshal([]byte(task.OutputSchema), &schema); err != nil {
			t.Errorf("%s output schema: %v", name, err)
			continue
		}
		metadataTypes := map[string]string{
			"execution_status": "string",
			"tool_run":         "string",
			"seed":             "integer",
			"bounds":           "array",
			"harnesses":        "array",
			"artifacts":        "array",
		}
		for field, wantType := range metadataTypes {
			if !slices.Contains(schema.Items.Required, field) {
				t.Errorf("%s output schema must require executable-tool metadata field %q", name, field)
			}
			if gotType := schema.Items.Properties[field].Type; gotType != wantType {
				t.Errorf("%s output schema field %q type = %q, want %q", name, field, gotType, wantType)
			}
			if !strings.Contains(task.Objective, field) {
				t.Errorf("%s objective must explicitly preserve executable-tool metadata field %q", name, field)
			}
		}
	}

	validation := byName["validate-high-impact-exploits"]
	validConfirmed := `[{"fingerprint":"finding-1","verdict":"confirmed","method":"forge_local","execution_status":"findings","summary":"reproduced","reachability":"public deposit path","impact":"protocol loses one unit","reproduction":{"setup":"install pinned dependencies","environment":"local anvil with dummy assets","command":"forge test --match-test testExploit","test_path":"test/Exploit.t.sol","failing_assertion":"asset conservation","expected_output":"transaction reverts","observed_output":"test passes and attacker balance increases","observed_delta":"protocol=-1 attacker=+1","negative_control_passed":true,"oracle_can_fail":true}}]`
	invalidConfirmed := `[{"fingerprint":"finding-1","verdict":"confirmed","method":"local_trace","execution_status":"not_run","summary":"reasoning only","reachability":"assumed","impact":"loss","reproduction":{"setup":"none","environment":"none","command":"echo plausible","test_path":"test/Exploit.t.sol","failing_assertion":"asset conservation","expected_output":"fail","observed_output":"not run","observed_delta":"unknown","negative_control_passed":false,"oracle_can_fail":false}}]`
	if err := triggersv1alpha1.ValidateSecurityWorkflowOutput(validation.OutputSchema, validConfirmed); err != nil {
		t.Errorf("complete confirmed EVM evidence rejected: %v", err)
	}
	if err := triggersv1alpha1.ValidateSecurityWorkflowOutput(validation.OutputSchema, invalidConfirmed); err == nil {
		t.Error("EVM validation schema accepted confirmation without passing controls")
	}
	if !slices.Contains(validation.DependsOn, "bounded-halmos-symbolic-tests") || !strings.Contains(validation.Objective, "{{tasks.bounded-halmos-symbolic-tests.output}}") {
		t.Error("high-impact validation must consume bounded Halmos results directly")
	}
	for _, name := range []string{"bounded-halmos-symbolic-tests", "merge-symbolic-tool-coverage"} {
		var schema struct {
			Items struct {
				Required   []string `json:"required"`
				Properties map[string]struct {
					Type string `json:"type"`
				} `json:"properties"`
			} `json:"items"`
		}
		if err := json.Unmarshal([]byte(byName[name].OutputSchema), &schema); err != nil {
			t.Fatalf("decode %s schema: %v", name, err)
		}
		for _, field := range []string{"counterexamples", "limitations"} {
			if !slices.Contains(schema.Items.Required, field) || schema.Items.Properties[field].Type != "array" {
				t.Errorf("%s must require structured %s array", name, field)
			}
		}
	}

	for name, tool := range map[string]string{
		"slither-static-analysis":                  "slither",
		"deterministic-forge-tests-and-invariants": "forge-security-tests",
		"echidna-stateful-property-fuzzing":        "echidna",
		"bounded-halmos-symbolic-tests":            "halmos",
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

// TestBlockchainProtocolAuditComposition keeps the generic protocol workflow
// chain-aware: platform detection must feed each specialist and dormant chain
// skills must remain attached to the matching review task.
func TestBlockchainProtocolAuditComposition(t *testing.T) {
	t.Parallel()

	var workflow triggersv1alpha1.SecurityWorkflow
	readBootstrapAsset(t, "securityworkflows", "blockchain-protocol-audit", &workflow)

	byName := make(map[string]triggersv1alpha1.SecurityScanTask, len(workflow.Spec.Tasks))
	for _, task := range workflow.Spec.Tasks {
		byName[task.Name] = task
	}
	if _, ok := byName["detect-platforms-and-components"]; !ok {
		t.Fatal("blockchain protocol workflow must begin with platform detection")
	}
	requiredSkills := map[string][]string{
		"solana-anchor-specialist":            {"trail-of-bits-solana-vulnerability-scanner"},
		"cosmos-cosmwasm-ibc-specialist":      {"trail-of-bits-cosmos-vulnerability-scanner"},
		"substrate-polkadot-xcm-specialist":   {"trail-of-bits-substrate-vulnerability-scanner"},
		"aptos-move-specialist":               {"move-chain-security-review"},
		"sui-move-specialist":                 {"move-chain-security-review"},
		"cairo-starknet-specialist":           {"trail-of-bits-cairo-vulnerability-scanner"},
		"ton-specialist":                      {"trail-of-bits-ton-vulnerability-scanner"},
		"algorand-specialist":                 {"trail-of-bits-algorand-vulnerability-scanner"},
		"bitcoin-lightning-specialist":        {"bitcoin-lightning-security-review"},
		"bridge-l2-zk-specialist":             {"trail-of-bits-property-based-testing"},
		"wallet-mpc-cryptography-specialist":  {"trail-of-bits-constant-time-analysis"},
		"economics-mev-governance-specialist": {"evm-economic-and-mev-review"},
	}
	for name, skills := range requiredSkills {
		task, ok := byName[name]
		if !ok {
			t.Errorf("required chain-aware task %q is missing", name)
			continue
		}
		if !slices.Contains(task.DependsOn, "detect-platforms-and-components") {
			t.Errorf("task %q does not consume platform detection", name)
		}
		refs := make([]string, 0, len(task.SkillRefs))
		for _, ref := range task.SkillRefs {
			refs = append(refs, ref.Name)
		}
		for _, skill := range skills {
			if !slices.Contains(refs, skill) {
				t.Errorf("task %q does not attach skill %q", name, skill)
			}
		}
	}

	cardinalities := map[string]int{
		"account-chain-coverage-a":        3,
		"account-chain-coverage-b":        3,
		"account-chain-coverage-c":        4,
		"account-cross-system-coverage-a": 3,
		"account-cross-system-coverage-b": 1,
		"account-chain-coverage":          10,
		"account-cross-system-coverage":   4,
		"account-domain-coverage-a":       3,
		"account-domain-coverage-b":       3,
		"account-domain-coverage-c":       3,
		"account-domain-coverage-d":       3,
		"account-domain-coverage-ab":      6,
		"account-domain-coverage-cd":      6,
		"account-domain-coverage":         12,
	}
	for name, want := range cardinalities {
		task, ok := byName[name]
		if !ok {
			t.Errorf("coverage ledger task %q is missing", name)
			continue
		}
		var schema struct {
			MinItems int `json:"minItems"`
			MaxItems int `json:"maxItems"`
		}
		if err := json.Unmarshal([]byte(task.OutputSchema), &schema); err != nil {
			t.Errorf("%s output schema: %v", name, err)
		} else if schema.MinItems != want || schema.MaxItems != want {
			t.Errorf("%s output cardinality = %d..%d, want exactly %d", name, schema.MinItems, schema.MaxItems, want)
		}
	}

	triage := byName["triage-and-report"]
	for _, ledger := range []string{"account-complete-protocol-coverage", "account-chain-coverage", "account-cross-system-coverage"} {
		if !slices.Contains(triage.DependsOn, ledger) || !strings.Contains(triage.Objective, "{{tasks."+ledger+".output}}") {
			t.Errorf("triage-and-report does not consume %q", ledger)
		}
	}
	for _, status := range []string{"errors", "timeouts", "unsupported", "skipped", "inconclusive", "retest"} {
		if !strings.Contains(strings.ToLower(triage.Objective), status) {
			t.Errorf("triage-and-report must account for %q", status)
		}
	}
}

func TestNativeFuzzExecutionIsExplicit(t *testing.T) {
	t.Parallel()

	for _, name := range evidenceContractBlockchainWorkflows {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			var workflow triggersv1alpha1.SecurityWorkflow
			readBootstrapAsset(t, "securityworkflows", name, &workflow)
			var objective string
			for _, task := range workflow.Spec.Tasks {
				if task.Name == "applicable-tool-execution" {
					objective = strings.ToLower(task.Objective)
					break
				}
			}
			if objective == "" {
				t.Fatal("missing applicable-tool-execution task")
			}
			for _, required := range []string{
				"run_security_tool", "cargo-fuzz", "go-fuzz-tests", "fuzz/cargo.toml",
				"fuzzxxx", "at most two", "two minutes", "corpus provenance", "not_found_under",
				"null seed", "dependency-resolution egress",
			} {
				if !strings.Contains(objective, required) {
					t.Errorf("applicable-tool-execution must require %q", required)
				}
			}
			var schema struct {
				Items struct {
					Required   []string `json:"required"`
					Properties map[string]struct {
						AnyOf []json.RawMessage `json:"anyOf"`
					} `json:"properties"`
				} `json:"items"`
			}
			for _, task := range workflow.Spec.Tasks {
				if task.Name == "applicable-tool-execution" {
					if err := json.Unmarshal([]byte(task.OutputSchema), &schema); err != nil {
						t.Fatalf("decode applicable-tool-execution schema: %v", err)
					}
				}
			}
			for _, field := range []string{"selection_reason", "duration", "workers", "corpus_provenance"} {
				if !slices.Contains(schema.Items.Required, field) {
					t.Errorf("applicable-tool-execution schema must require %q", field)
				}
			}
			if len(schema.Items.Properties["tool_run"].AnyOf) != 2 {
				t.Error("tool_run must allow a run reference or null when no run exists")
			}
		})
	}
}

func TestNativeFuzzBaselineWorkflows(t *testing.T) {
	t.Parallel()

	assertBaseline := func(t *testing.T, workflowName, taskName string) triggersv1alpha1.SecurityScanTask {
		t.Helper()
		var workflow triggersv1alpha1.SecurityWorkflow
		readBootstrapAsset(t, "securityworkflows", workflowName, &workflow)
		byName := make(map[string]triggersv1alpha1.SecurityScanTask, len(workflow.Spec.Tasks))
		for _, task := range workflow.Spec.Tasks {
			byName[task.Name] = task
		}
		task, ok := byName[taskName]
		if !ok {
			t.Fatalf("%s is missing task %s", workflowName, taskName)
		}
		if task.Role != "native-fuzz-runner" {
			t.Errorf("%s role = %q, want native-fuzz-runner", taskName, task.Role)
		}
		objective := strings.ToLower(task.Objective)
		for _, required := range []string{
			"run_security_tool", "cargo-fuzz", "go-fuzz-tests", "at most two", "two minutes",
			"explicit seed", "workers", "corpus provenance", "not_found_under", "native-fuzz-campaign",
		} {
			if !strings.Contains(objective, required) {
				t.Errorf("%s must require %q", taskName, required)
			}
		}
		var schema struct {
			Properties map[string]struct {
				MaxItems int `json:"maxItems"`
				Items    struct {
					Properties map[string]struct {
						AnyOf []json.RawMessage `json:"anyOf"`
					} `json:"properties"`
				} `json:"items"`
			} `json:"properties"`
		}
		if err := json.Unmarshal([]byte(task.OutputSchema), &schema); err != nil {
			t.Fatalf("decode %s output schema: %v", taskName, err)
		}
		if schema.Properties["runs"].MaxItems != 2 {
			t.Errorf("%s runs maxItems = %d, want 2", taskName, schema.Properties["runs"].MaxItems)
		}
		if len(schema.Properties["runs"].Items.Properties["seed"].AnyOf) != 2 {
			t.Errorf("%s run seed must allow integer for Rust or null for Go", taskName)
		}
		return task
	}

	t.Run("blockchain-protocol-audit", func(t *testing.T) {
		t.Parallel()
		task := assertBaseline(t, "blockchain-protocol-audit", "run-upstream-fuzz-campaigns")
		if !slices.Equal(task.DependsOn, []string{"detect-platforms-and-components", "map-protocol-surfaces"}) {
			t.Errorf("fuzz dependencies = %v", task.DependsOn)
		}

		var workflow triggersv1alpha1.SecurityWorkflow
		readBootstrapAsset(t, "securityworkflows", "blockchain-protocol-audit", &workflow)
		byName := make(map[string]triggersv1alpha1.SecurityScanTask, len(workflow.Spec.Tasks))
		for _, workflowTask := range workflow.Spec.Tasks {
			byName[workflowTask.Name] = workflowTask
		}
		for _, consumer := range []string{"validate-high-impact-findings", "account-complete-protocol-coverage"} {
			if !slices.Contains(byName[consumer].DependsOn, task.Name) ||
				!strings.Contains(byName[consumer].Objective, "{{tasks."+task.Name+".output}}") {
				t.Errorf("%s must consume native fuzz output", consumer)
			}
		}
		var aggregateSchema struct {
			Properties map[string]struct {
				Required []string `json:"required"`
			} `json:"properties"`
		}
		if err := json.Unmarshal([]byte(byName["account-complete-protocol-coverage"].OutputSchema), &aggregateSchema); err != nil {
			t.Fatalf("decode complete protocol coverage schema: %v", err)
		}
		for _, field := range []string{"inventory", "runs", "uncovered", "limitations"} {
			if !slices.Contains(aggregateSchema.Properties["native_fuzz"].Required, field) {
				t.Errorf("native_fuzz aggregation must preserve %q", field)
			}
		}
	})

	t.Run("default-deep-scan", func(t *testing.T) {
		t.Parallel()
		task := assertBaseline(t, "default-deep-scan", "run-upstream-native-fuzz")
		var workflow triggersv1alpha1.SecurityWorkflow
		readBootstrapAsset(t, "securityworkflows", "default-deep-scan", &workflow)
		for _, consumer := range workflow.Spec.Tasks {
			if consumer.Name == "triage-and-report" {
				if !slices.Contains(consumer.DependsOn, task.Name) ||
					!strings.Contains(consumer.Objective, "{{tasks."+task.Name+".output}}") {
					t.Error("default triage must consume native fuzz output")
				}
				return
			}
		}
		t.Fatal("default deep scan is missing triage-and-report")
	})
}

func TestNativeFuzzCampaignUsesBoundedWarmRounds(t *testing.T) {
	t.Parallel()

	var workflow triggersv1alpha1.SecurityWorkflow
	readBootstrapAsset(t, "securityworkflows", "native-fuzz-campaign", &workflow)
	byName := make(map[string]triggersv1alpha1.SecurityScanTask, len(workflow.Spec.Tasks))
	for _, task := range workflow.Spec.Tasks {
		byName[task.Name] = task
	}
	for _, required := range []string{"select-upstream-fuzz-targets", "run-bounded-warm-rounds", "triage-and-report"} {
		if _, ok := byName[required]; !ok {
			t.Fatalf("native fuzz campaign is missing %s", required)
		}
	}
	run := byName["run-bounded-warm-rounds"]
	if run.Role != "native-fuzz-runner" {
		t.Errorf("run-bounded-warm-rounds role = %q, want native-fuzz-runner", run.Role)
	}
	objective := strings.ToLower(run.Objective)
	for _, required := range []string{
		"run_security_tool", "cargo-fuzz", "go-fuzz-tests", "four", "fifteen minutes",
		"distinct explicit seed", "restore the durable corpus", "stop scheduling", "not_found_under",
	} {
		if !strings.Contains(objective, required) {
			t.Errorf("warm campaign must require %q", required)
		}
	}
	var schema struct {
		Properties map[string]struct {
			MaxItems int `json:"maxItems"`
			Items    struct {
				Required   []string `json:"required"`
				Properties map[string]struct {
					AnyOf    []json.RawMessage `json:"anyOf"`
					Required []string          `json:"required"`
				} `json:"properties"`
			} `json:"items"`
		} `json:"properties"`
	}
	if err := json.Unmarshal([]byte(run.OutputSchema), &schema); err != nil {
		t.Fatalf("decode warm campaign schema: %v", err)
	}
	rounds := schema.Properties["rounds"]
	if rounds.MaxItems != 8 {
		t.Errorf("rounds maxItems = %d, want 8", rounds.MaxItems)
	}
	for _, field := range []string{"tool_version", "corpus"} {
		if !slices.Contains(rounds.Items.Required, field) {
			t.Errorf("warm round schema must require %q", field)
		}
	}
	if len(rounds.Items.Properties["seed"].AnyOf) != 2 {
		t.Error("warm round seed must allow integer for Rust or null for Go")
	}
	for _, field := range []string{"restored", "restored_revision", "parent_digest", "snapshot_digest", "input_entries", "input_bytes", "output_entries", "output_bytes"} {
		if !slices.Contains(rounds.Items.Properties["corpus"].Required, field) {
			t.Errorf("warm round corpus schema must require %q", field)
		}
	}
}

// TestBountyHuntEVMBuildsItsOracleFirst pins the four-task fork-harness lane.
// Build, calibration, hunting, and reproduction deliberately share one
// write-capable AgentRun so temporary harness files are not lost at handoffs.
func TestBountyHuntEVMBuildsItsOracleFirst(t *testing.T) {
	t.Parallel()

	var workflow triggersv1alpha1.SecurityWorkflow
	readBootstrapAsset(t, "securityworkflows", "bounty-hunt-evm", &workflow)

	order := make(map[string]int, len(workflow.Spec.Tasks))
	byName := make(map[string]triggersv1alpha1.SecurityScanTask, len(workflow.Spec.Tasks))
	for index, task := range workflow.Spec.Tasks {
		order[task.Name] = index
		byName[task.Name] = task
	}
	for _, name := range []string{
		"pin-target-and-deployment", "fork-harness-hunt", "quantify-impact-and-eligibility", "triage-and-report",
	} {
		if _, ok := byName[name]; !ok {
			t.Fatalf("workflow is missing task %q", name)
		}
	}

	if len(workflow.Spec.Tasks) != 4 {
		t.Fatalf("workflow has %d tasks, want the four-task fork-harness DAG", len(workflow.Spec.Tasks))
	}
	dependsOn := func(task, dependency string) bool {
		return slices.Contains(byName[task].DependsOn, dependency)
	}
	for _, edge := range [][2]string{
		{"fork-harness-hunt", "pin-target-and-deployment"},
		{"quantify-impact-and-eligibility", "fork-harness-hunt"},
		{"triage-and-report", "quantify-impact-and-eligibility"},
	} {
		if !dependsOn(edge[0], edge[1]) {
			t.Errorf("task %q must depend on %q", edge[0], edge[1])
		}
		if order[edge[0]] < order[edge[1]] {
			t.Errorf("task %q is declared before its dependency %q", edge[0], edge[1])
		}
	}

	huntTask := byName["fork-harness-hunt"]
	if huntTask.Role != "exploit-validator" {
		t.Errorf("fork-harness-hunt role = %q, want exploit-validator", huntTask.Role)
	}
	hunt := huntTask.Objective
	for _, marker := range []string{
		"one write-capable run", "local fork", "fork_status", "blocked",
		"mutant", "cannot detect", "never means the target is safe",
		"negative control", "reproducibility", "refuted or untested",
		"Always run source review", "continue source review", "project tests or a local devnet",
	} {
		if !strings.Contains(hunt, marker) {
			t.Errorf("fork-harness-hunt objective is missing %q", marker)
		}
	}
	for _, marker := range slices.Concat(payoutOrderMarkers, []string{"upstream-fork-diff", "bot-findable means unpayable"}) {
		if !strings.Contains(hunt, marker) {
			t.Errorf("fork-harness-hunt objective is missing %q", marker)
		}
	}
	schema := huntTask.OutputSchema
	for _, marker := range []string{"fork_status", "verified_pin", "executable_invariant_count", "calibrated_invariant_count", "tool_runs", "seed", "hypotheses", "reproduction"} {
		if !strings.Contains(schema, marker) {
			t.Errorf("fork-harness-hunt output schema is missing %q", marker)
		}
	}
	params := map[string]bool{}
	for _, param := range workflow.Spec.Parameters {
		params[param.Name] = param.Required || param.Default != ""
	}
	for _, name := range []string{"fork_endpoint_alias", "chain_id", "fork_block_number", "fork_block_hash", "project_root", "deployment_manifest"} {
		if !params[name] {
			t.Errorf("workflow parameter %q is missing or unusable", name)
		}
	}
	for _, param := range workflow.Spec.Parameters {
		if slices.Contains([]string{"fork_endpoint_alias", "chain_id", "fork_block_number", "fork_block_hash", "deployment_manifest"}, param.Name) {
			if param.Required || param.Default != "unconfigured" {
				t.Errorf("optional archive-chain parameter %q = required:%t default:%q, want optional unconfigured default", param.Name, param.Required, param.Default)
			}
		}
	}
	report := byName["triage-and-report"].Objective
	for _, marker := range []string{"failing assertion", "clause the program published", "not found under those bounds"} {
		if !strings.Contains(report, marker) {
			t.Errorf("report objective is missing %q", marker)
		}
	}
	// Impact has to be priced in the program's own vocabulary, verbatim.
	impact := byName["quantify-impact-and-eligibility"].Objective
	for _, marker := range []string{"verbatim", "maximum achievable impact", "never translate", "source-level claims do not"} {
		if !strings.Contains(impact, marker) {
			t.Errorf("impact objective is missing %q", marker)
		}
	}
}

// payoutOrderMarkers pins the priority order the hunt lanes are aimed at, so a
// rewrite cannot quietly drop a class or reorder it back to the generic
// vulnerability taxonomy the disclosed payouts no longer follow.
var payoutOrderMarkers = []string{
	"Priority 1, initialization and upgrade state on the DEPLOYED implementation",
	"Priority 2, cross-chain proof and identity binding",
	"Priority 3, fork-versus-upstream divergence",
	"Priority 4, encoding and decoding round trips and replay",
	"Priority 5, missing balance and state validation",
	"Priority 6, accounting, rounding, precision and decimals",
	"Priority 7, access control and role or permission inventories",
}

// TestHuntObjectivesFollowDisclosedPayoutOrder holds the two large blockchain
// review workflows to the same payout-ordered aim as the bounty hunt: the
// deployed-state, cross-chain identity, fork-divergence, encoding, validation,
// accounting and access-control classes are prioritized by class, each names
// the invariant it breaks, chain reads name the pack and the authorization they
// need, and the bot-findable classes stay available but off the default path.
func TestHuntObjectivesFollowDisclosedPayoutOrder(t *testing.T) {
	t.Parallel()

	workflows := map[string]map[string][]string{
		"smart-contract-review": {
			"low-level-factories-proxies-and-storage": {
				"Payout-ordered priority 1 is owned here: initialization and upgrade state on the DEPLOYED implementation",
				"The invariant is that every implementation behind a proxy is already initialized",
				"the chain-read pack reads the EIP-1967 implementation, admin and beacon slots",
				"deployed-bytecode-diff",
				"operator-authorized fork endpoint alias",
				"never assume a slot value you could not read",
				"Payout-ordered priority 3 also lands here",
				"name upstream-fork-diff",
				"the invariant the upstream code used to satisfy",
			},
			"authorization-signatures-and-account-abstraction": {
				"Payout-ordered priorities 2, 4 and 7 are owned here",
				"Priority 2, cross-chain proof and identity binding",
				"what msg.sender becomes after relaying",
				"Priority 4, encoding and decoding round trips and replay",
				"missing nonce or domain separation",
				"Priority 7, access control",
			},
			"accounting-arithmetic-and-token-integrations": {
				"Payout-ordered priorities 5 and 6 are owned here",
				"Priority 5, missing balance and state validation",
				"Priority 6, accounting, rounding, precision and decimals",
				"non-18-decimal tokens",
				"only the chain-read pack can settle",
				"never assume a decimals value you could not read",
			},
			"external-calls-reentrancy-and-atomicity": {
				"stay AVAILABLE here but sit off the default bounty path",
				"bot-findable means unpayable",
				"Zellic V12",
				"LightChaser",
			},
			"oracles-randomness-time-and-ordering": {
				"Templated flash-loan oracle manipulation",
				"stays AVAILABLE but is off the default bounty path",
				"bot-findable means unpayable",
			},
		},
		"blockchain-protocol-audit": {
			"review-critical-surface": slices.Concat(payoutOrderMarkers, []string{
				"naming the invariant you are trying to break",
				"chain-read and deployed-bytecode-diff packs can settle",
				"operator-authorized fork endpoint alias",
				"rather than assuming the deployed value",
				"bot-findable means unpayable",
			}),
			"consensus-finality-and-state-transition": {
				"Payout-ordered priority 3 is owned here",
				"name upstream-fork-diff",
				"the invariant the upstream code used to satisfy",
				"Record an unavailable upstream revision as a limitation",
			},
			"cross-chain-bridge-and-custody": {
				"Payout-ordered priority 2 is owned here, cross-chain proof and identity binding",
				"who is allowed to have produced this proof",
				"what msg.sender becomes on the far side after relaying",
			},
			"transaction-crypto-and-accounting": {
				"Payout-ordered priorities 4, 5 and 6 are owned here",
				"Priority 4, encoding and decoding round trips and replay",
				"Priority 5, missing balance and state validation",
				"Priority 6, accounting, rounding, precision and decimals",
				"non-18-decimal representations",
			},
			"genesis-deployment-and-upgrades": {
				"Payout-ordered priority 1 is owned here",
				"the chain-read pack reads the EIP-1967 implementation, admin and beacon slots",
				"deployed-bytecode-diff",
				"operator-authorized fork endpoint alias",
				"rather than assuming the deployed value",
			},
			"economics-mev-governance-specialist": {
				"Templated flash-loan oracle manipulation stays available but is off the default path",
				"bot-findable means unpayable",
			},
		},
	}

	for name, tasks := range workflows {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			var workflow triggersv1alpha1.SecurityWorkflow
			readBootstrapAsset(t, "securityworkflows", name, &workflow)
			byName := make(map[string]triggersv1alpha1.SecurityScanTask, len(workflow.Spec.Tasks))
			for _, task := range workflow.Spec.Tasks {
				byName[task.Name] = task
			}
			for taskName, markers := range tasks {
				task, ok := byName[taskName]
				if !ok {
					t.Errorf("workflow is missing hunt task %q", taskName)
					continue
				}
				for _, marker := range markers {
					if !strings.Contains(task.Objective, marker) {
						t.Errorf("task %q objective is missing %q", taskName, marker)
					}
				}
			}
		})
	}
}

// protocolFamilyWorkflowLibrary lists the workflows written for one protocol
// family each. They exist because the toolchain, harness and proof-of-concept
// substrate differ per family. Discovery must fan out across independent
// specialists before one write-capable validator builds proofs; otherwise the
// advertised parallelism is inert and the validator is asked to discover bugs
// despite its role contract.
var protocolFamilyWorkflowLibrary = []string{
	"cross-chain-messaging-review",
	"evm-lending-cdp-review",
	"evm-orderbook-settlement-review",
	"flow-cadence-review",
	"near-contract-review",
	"rollup-stack-review",
	"solana-defi-program-review",
}

// TestProtocolFamilyWorkflowsKeepTheirSpine pins the executable structure a
// rewrite must not drop: real parallel discovery, a separate proof stage,
// conditional evidence schemas, and explicit provider-eligibility state.
func TestProtocolFamilyWorkflowsKeepTheirSpine(t *testing.T) {
	t.Parallel()

	for _, name := range protocolFamilyWorkflowLibrary {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			var workflow triggersv1alpha1.SecurityWorkflow
			readBootstrapAsset(t, "securityworkflows", name, &workflow)

			byName := make(map[string]triggersv1alpha1.SecurityScanTask, len(workflow.Spec.Tasks))
			for _, task := range workflow.Spec.Tasks {
				byName[task.Name] = task
			}
			for _, required := range []string{"validate-candidates-in-harness", "quantify-impact-and-submission-readiness", "triage-and-report"} {
				if _, ok := byName[required]; !ok {
					t.Fatalf("workflow is missing task %q", required)
				}
			}
			validator := byName["validate-candidates-in-harness"]
			if role := validator.EffectiveRole(); role != "exploit-validator" {
				t.Errorf("validate-candidates-in-harness role = %q, want exploit-validator", role)
			}
			if workflow.Spec.Parallelism < 3 {
				t.Errorf("parallelism = %d, want at least 3 for independent discovery lanes", workflow.Spec.Parallelism)
			}
			discoveryTasks := 0
			for _, task := range workflow.Spec.Tasks {
				if task.EffectiveRole() != "vulnerability-hunter" {
					continue
				}
				discoveryTasks++
				if !slices.Contains(validator.DependsOn, task.Name) {
					t.Errorf("validator must depend on discovery task %q", task.Name)
				}
			}
			if discoveryTasks < 3 {
				t.Errorf("workflow has %d vulnerability-hunter discovery lanes, want at least 3", discoveryTasks)
			}
			for _, marker := range []string{"reproduction", "allOf", "const"} {
				if !strings.Contains(validator.OutputSchema, marker) {
					t.Errorf("validate-candidates-in-harness output schema is missing %q", marker)
				}
			}

			// Eligibility state is machine-readable rather than inferred from
			// prose at report time.
			for _, marker := range []string{"eligibility_source", "technical_status", "submission_ready", "allOf"} {
				if !strings.Contains(byName["quantify-impact-and-submission-readiness"].OutputSchema, marker) {
					t.Errorf("quantify-impact-and-submission-readiness output schema is missing %q", marker)
				}
			}
			for _, parameter := range workflow.Spec.Parameters {
				if parameter.Name != "release_tag" {
					continue
				}
				if !strings.Contains(workflow.Spec.Tasks[0].OutputSchema, "release_constraint") {
					t.Error("release-tag workflow pin task must emit release_constraint")
				}
				if !strings.Contains(byName["quantify-impact-and-submission-readiness"].OutputSchema, "release_constraint_satisfied") {
					t.Error("release-tag workflow eligibility schema must require release_constraint_satisfied")
				}
			}
		})
	}
}

func TestSmartContractReviewExecutionStatusEnums(t *testing.T) {
	t.Parallel()

	var workflow triggersv1alpha1.SecurityWorkflow
	readBootstrapAsset(t, "securityworkflows", "smart-contract-review", &workflow)

	executionStatusEnums := 0
	for _, task := range workflow.Spec.Tasks {
		if strings.TrimSpace(task.OutputSchema) == "" {
			continue
		}
		var schema any
		if err := json.Unmarshal([]byte(task.OutputSchema), &schema); err != nil {
			t.Fatalf("task %q output schema: %v", task.Name, err)
		}
		var walk func(any)
		walk = func(value any) {
			switch value := value.(type) {
			case map[string]any:
				for key, child := range value {
					if key == "execution_status" {
						executionStatusEnums++
						statusSchema, ok := child.(map[string]any)
						if !ok {
							t.Errorf("task %q execution_status schema has type %T, want object", task.Name, child)
							continue
						}
						values, ok := statusSchema["enum"].([]any)
						if !ok {
							t.Errorf("task %q execution_status enum has type %T, want array", task.Name, statusSchema["enum"])
							continue
						}
						if !slices.Contains(values, any("not_found_under")) {
							t.Errorf("task %q execution_status enum must include not_found_under", task.Name)
						}
						if slices.Contains(values, any("pass")) {
							t.Errorf("task %q execution_status enum must not include pass", task.Name)
						}
					}
					walk(child)
				}
			case []any:
				for _, child := range value {
					walk(child)
				}
			}
		}
		walk(schema)
	}
	if executionStatusEnums == 0 {
		t.Error("smart-contract-review has no execution_status enums")
	}
}
