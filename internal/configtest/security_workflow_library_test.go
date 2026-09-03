package configtest

import (
	"encoding/json"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

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
	"filecoin-security-review",
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
	"filecoin-security-review",
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
	"filecoin-security-review",
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

// TestSecurityWorkflowsRedTeamBountyWorthiness prevents a candidate from
// flowing directly from discovery or validation into the final report. The
// intervening task is deliberately adversarial: it must try to disprove the
// claim and separately decide whether a technically real issue is eligible for
// a bounty.
func TestSecurityWorkflowsRedTeamBountyWorthiness(t *testing.T) {
	t.Parallel()

	for _, name := range securityWorkflowLibrary {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			var workflow triggersv1alpha1.SecurityWorkflow
			readBootstrapAsset(t, "securityworkflows", name, &workflow)

			byName := make(map[string]triggersv1alpha1.SecurityScanTask, len(workflow.Spec.Tasks))
			order := make(map[string]int, len(workflow.Spec.Tasks))
			for index, task := range workflow.Spec.Tasks {
				byName[task.Name] = task
				order[task.Name] = index
			}

			gate, ok := byName["red-team-bounty-worthiness"]
			if !ok {
				t.Fatal("workflow is missing red-team-bounty-worthiness")
			}
			triage, ok := byName["triage-and-report"]
			if !ok {
				t.Fatal("workflow is missing triage-and-report")
			}
			if order[gate.Name] >= order[triage.Name] {
				t.Error("red-team-bounty-worthiness must be declared before triage-and-report")
			}
			if gate.Role != "exploit-validator" {
				t.Errorf("red-team-bounty-worthiness role = %q, want exploit-validator", gate.Role)
			}
			if gate.Category != "triage" {
				t.Errorf("red-team-bounty-worthiness category = %q, want triage", gate.Category)
			}
			for _, skill := range []string{"trail-of-bits-fp-check", "bug-bounty-reporting", "exploit-poc-discipline"} {
				if !slices.ContainsFunc(gate.SkillRefs, func(ref platformv1alpha1.NamedRef) bool {
					return ref.Name == skill
				}) {
					t.Errorf("red-team-bounty-worthiness is missing skill %q", skill)
				}
			}

			objective := strings.ToLower(gate.Objective)
			for _, marker := range []string{
				"skeptical bounty-triager", "list_security_findings", "get_security_finding",
				"complete evidence and audit trail", "all available tools", "update_security_finding",
				"confirmed", "accepted_risk", "false_positive", "triaged", "fixed",
				"policy_disposition", "scope_excluded", "known_issue", "bot_findable", "not_ready",
				"technically real", "missing evidence", "do not hunt for new",
				"read-only calls", "in-scope live targets", "minimally invasive", "real-user data",
				"state-changing", "destructive testing", "expand scope", "credentials", "denial-of-service",
			} {
				if !strings.Contains(objective, marker) {
					t.Errorf("red-team-bounty-worthiness objective is missing %q", marker)
				}
			}

			if !slices.Contains(triage.DependsOn, gate.Name) {
				t.Error("triage-and-report must depend on red-team-bounty-worthiness")
			}
			if len(gate.DependsOn) == 0 {
				t.Error("red-team-bounty-worthiness must wait for finding-producing prerequisites")
			}
			triagePrerequisites := slices.DeleteFunc(slices.Clone(triage.DependsOn), func(name string) bool {
				return name == gate.Name
			})
			gatePrerequisites := slices.Clone(gate.DependsOn)
			for _, prerequisite := range triagePrerequisites {
				if !slices.Contains(gatePrerequisites, prerequisite) {
					t.Errorf("red-team prerequisites = %v, missing triage prerequisite %q", gatePrerequisites, prerequisite)
				}
			}
			for _, prerequisite := range gatePrerequisites {
				if !slices.Contains(triagePrerequisites, prerequisite) && (gate.When == nil || gate.When.Task != prerequisite) {
					t.Errorf("red-team prerequisite %q is neither a triage prerequisite nor its readiness gate", prerequisite)
				}
			}
		})
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
			var hasNegativeControlSchema, hasOracleSchema, hasVariantSweepOwner bool
			for _, task := range workflow.Spec.Tasks {
				schema := strings.ToLower(task.OutputSchema)
				hasNegativeControlSchema = hasNegativeControlSchema || strings.Contains(schema, "negative_control_passed")
				hasOracleSchema = hasOracleSchema || strings.Contains(schema, "oracle_can_fail") || strings.Contains(schema, "oracle_calibrated")
				objective := strings.ToLower(task.Objective)
				hasTaskVariantSkill := false
				for _, ref := range task.SkillRefs {
					hasTaskVariantSkill = hasTaskVariantSkill || ref.Name == "trail-of-bits-variant-analysis"
				}
				hasVariantSweepOwner = hasVariantSweepOwner ||
					(hasTaskVariantSkill && strings.Contains(objective, "create_security_variant_sweep") &&
						strings.Contains(objective, "complete_security_variant_sweep"))
			}
			if !hasNegativeControlSchema || !hasOracleSchema {
				t.Error("program-linked workflow must schema-enforce negative-control and oracle calibration evidence")
			}
			if !hasVariantSweepOwner {
				t.Error("one task must own the variant-analysis skill and durable create/complete sweep persistence")
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
			objective := strings.ToLower(validation.Objective)
			conservesCandidates := strings.Contains(objective, "never submit an unvalidated candidate") ||
				(strings.Contains(objective, "every canonical candidate") && strings.Contains(objective, "exactly one"))
			if !conservesCandidates {
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
// compact and evidence-driven: a build-free target-priors pass and a runtime
// preflight feed four persistent investigators and one fuzz researcher, a
// toolchain gap degrades investigation to static mode instead of skipping it,
// and selected leads fan out for independent challenge or extension.
func TestBlockchainProtocolAuditComposition(t *testing.T) {
	t.Parallel()

	var workflow triggersv1alpha1.SecurityWorkflow
	readBootstrapAsset(t, "securityworkflows", "blockchain-protocol-audit", &workflow)

	byName := make(map[string]triggersv1alpha1.SecurityScanTask, len(workflow.Spec.Tasks))
	for _, task := range workflow.Spec.Tasks {
		byName[task.Name] = task
	}
	if len(workflow.Spec.Tasks) != 12 {
		t.Fatalf("protocol workflow has %d static tasks, want the compact 12-task graph", len(workflow.Spec.Tasks))
	}
	if workflow.Spec.Tasks[0].Name != "target-priors" {
		t.Fatalf("first task = %q, want the build-free target-priors pass", workflow.Spec.Tasks[0].Name)
	}
	priors := byName["target-priors"]
	if len(priors.DependsOn) != 0 || priors.Role != "threat-modeler" || priors.When != nil {
		t.Errorf("target-priors must be an ungated build-free threat-modeler task: dependsOn=%v role=%q when=%#v", priors.DependsOn, priors.Role, priors.When)
	}
	for _, marker := range []string{
		"get_security_research_context", "git log", "git diff", "release tag", "last audit date", "90 days", "300 commits",
		"changelog", "security.md", "advisories", "prior audit reports", "known issues", "todo", "unsafe", "fork or feature flags",
		"spec clauses", "upstream divergence", "stale-bug-class", "inline", "not as artifact ids", "32 kib",
	} {
		if !strings.Contains(strings.ToLower(priors.Objective), marker) {
			t.Errorf("target-priors must require %q", marker)
		}
	}
	for _, marker := range []string{
		`"profile"`, `"consensus_or_execution_client"`, `"networked_node"`, `"cross_chain_or_custody"`, `"smart_contract_platform"`,
		`"release_pipeline"`, `"evm_compatible"`, `"languages"`, `"priors"`, `"maxItems":24`, `"recent_change"`, `"changelog"`,
		`"advisory_or_audit"`, `"known_issue"`, `"spec_clause"`, `"fork_divergence"`, `"todo_or_unsafe"`, `"input_surface"`,
		`"anchor"`, `"summary"`, `"why_it_matters"`, `"suggested_experiment"`, `"commits"`, `"stale_classes"`, `"limitations"`,
	} {
		if !strings.Contains(priors.OutputSchema, marker) {
			t.Errorf("target-priors schema must declare %q", marker)
		}
	}
	validPriors := `{"revision":"abc123","profile":{"consensus_or_execution_client":true,"networked_node":true,"cross_chain_or_custody":false,"smart_contract_platform":false,"release_pipeline":true,"evm_compatible":true,"languages":["go"]},"priors":[{"id":"p1","kind":"recent_change","anchor":{"path":"core/types/tx.go","line":42,"symbol":"DecodeRLP"},"summary":"new blob tx decoder","why_it_matters":"pre-auth wire surface","suggested_experiment":"decoder strictness probe","commits":["0123456789ab"]}],"stale_classes":["Go map iteration in non-consensus paths"],"limitations":[]}`
	if err := triggersv1alpha1.ValidateSecurityWorkflowOutput(priors.OutputSchema, validPriors); err != nil {
		t.Fatalf("target-priors schema rejected a well-formed priors list: %v", err)
	}
	invalidPriorKind := strings.Replace(validPriors, `"kind":"recent_change"`, `"kind":"vibes"`, 1)
	if err := triggersv1alpha1.ValidateSecurityWorkflowOutput(priors.OutputSchema, invalidPriorKind); err == nil {
		t.Error("target-priors schema accepted an unknown prior kind")
	}

	preflight, ok := byName["runtime-preflight-and-dossier"]
	if !ok {
		t.Fatal("protocol workflow must carry a runtime readiness gate")
	}
	for _, marker := range []string{
		"initialize declared submodules", "repository-declared dependencies", "principal build", "focused test",
		"conditions.ready false", "revision_ready", "build_ready", "test_ready", "local_experiment_ready", "negative control", "blocker artifact",
		"static gate", "dynamic gate", "conditions.dynamic_ready", "no longer blocks static investigation",
	} {
		if !strings.Contains(strings.ToLower(preflight.Objective), marker) {
			t.Errorf("runtime preflight must require %q", marker)
		}
	}
	for _, marker := range []string{`"revision_ready"`, `"local_experiment_ready"`, `"dynamic_ready"`, `"allOf"`, `"const":true`} {
		if !strings.Contains(preflight.OutputSchema, marker) {
			t.Errorf("runtime preflight schema must enforce %q", marker)
		}
	}

	dynamicWithoutExperiment := `{"version":1,"artifact_ids":["aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"],"candidate_fingerprints":[],"coverage_ids":{},"blocker_ids":[],"conditions":{"ready":true,"revision_ready":true,"build_ready":true,"test_ready":true,"local_experiment_ready":false,"dynamic_ready":true}}`
	if err := triggersv1alpha1.ValidateSecurityWorkflowOutput(preflight.OutputSchema, dynamicWithoutExperiment); err == nil {
		t.Error("preflight schema accepted dynamic_ready=true without an operational local experiment")
	}
	readyWithoutRevision := `{"version":1,"artifact_ids":["aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"],"candidate_fingerprints":[],"coverage_ids":{},"blocker_ids":[],"conditions":{"ready":true,"revision_ready":false,"build_ready":false,"test_ready":false,"local_experiment_ready":false,"dynamic_ready":false}}`
	if err := triggersv1alpha1.ValidateSecurityWorkflowOutput(preflight.OutputSchema, readyWithoutRevision); err == nil {
		t.Error("preflight schema accepted ready=true without a verified revision")
	}
	staticOnlyPreflight := `{"version":1,"artifact_ids":["aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"],"candidate_fingerprints":[],"coverage_ids":{},"blocker_ids":["bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"],"conditions":{"ready":true,"revision_ready":true,"build_ready":false,"test_ready":false,"local_experiment_ready":false,"dynamic_ready":false}}`
	if err := triggersv1alpha1.ValidateSecurityWorkflowOutput(preflight.OutputSchema, staticOnlyPreflight); err != nil {
		t.Fatalf("preflight schema rejected a truthful static-only readiness (toolchain gap): %v", err)
	}
	blockedPreflight := `{"version":1,"artifact_ids":["aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"],"candidate_fingerprints":[],"coverage_ids":{},"blocker_ids":[],"conditions":{"ready":false,"revision_ready":false,"build_ready":false,"test_ready":false,"local_experiment_ready":false,"dynamic_ready":false}}`
	if err := triggersv1alpha1.ValidateSecurityWorkflowOutput(preflight.OutputSchema, blockedPreflight); err != nil {
		t.Fatalf("preflight schema rejected truthful blocked output: %v", err)
	}

	investigators := []string{
		"consensus-and-execution-investigator",
		"network-and-state-investigator",
		"cross-chain-and-custody-investigator",
		"crypto-economics-and-release-investigator",
	}
	profileGates := map[string]string{
		"network-and-state-investigator":       "profile.networked_node",
		"cross-chain-and-custody-investigator": "profile.cross_chain_or_custody",
	}
	for _, name := range investigators {
		task, ok := byName[name]
		if !ok {
			t.Errorf("persistent investigator %q is missing", name)
			continue
		}
		if !slices.Contains(task.DependsOn, "runtime-preflight-and-dossier") || !slices.Contains(task.DependsOn, "target-priors") {
			t.Errorf("investigator %q must depend on the preflight and the target priors: %v", name, task.DependsOn)
		}
		if task.When == nil {
			t.Errorf("investigator %q is not gated", name)
			continue
		}
		if profile, gated := profileGates[name]; gated {
			if task.When.Task != "target-priors" || task.When.Path != profile || task.When.Equals != "true" {
				t.Errorf("investigator %q gate = %#v, want target-priors %s", name, task.When, profile)
			}
			if !strings.Contains(task.When.OtherwiseOutput, `"reason":"profile_not_applicable"`) {
				t.Errorf("investigator %q skipped output must state profile_not_applicable", name)
			}
			if !strings.Contains(strings.ToLower(task.Objective), "conditions.ready false, return the ready:false handoff") {
				t.Errorf("profile-gated investigator %q must fall back to the static-blocked handoff when preflight is not ready", name)
			}
		} else if task.When.Task != "runtime-preflight-and-dossier" || task.When.Path != "conditions.ready" {
			t.Errorf("investigator %q is not gated by the static readiness gate: %#v", name, task.When)
		}
		if task.Timeout.Duration < 120*time.Minute || task.MaxTurns < 250 {
			t.Errorf("investigator %q is not persistent enough: timeout=%s maxTurns=%d", name, task.Timeout.Duration, task.MaxTurns)
		}
		if !strings.Contains(task.Objective, "{{tasks.target-priors.output}}") {
			t.Errorf("investigator %q must consume the inline target priors", name)
		}
		objective := strings.ToLower(task.Objective)
		for _, marker := range []string{
			"create_security_hypothesis", "local", "create_security_research_artifact", "record_security_coverage",
			"delta first", "at least half of your hypotheses", "not_tested coverage",
			"model-authored or model-modified", "never an experiment", "blocked, never falsified", "cited total guard",
			"conditions.dynamic_ready false", "static mode", "conditions.static_mode",
			"at least six distinct", "at least three calibrated dynamic experiments", "at least two methods", "not a stopping point",
			"budget-based", "next_experiment manifest", "conditions.next_experiment_artifact_id", "40% of the turn budget",
			"exhausted-surface artifact", "top_candidates", "conditions.hypotheses_examined", "conditions.dynamic_experiments",
			"conditions.experiment_methods", "conditions.surface_exhausted",
		} {
			if !strings.Contains(objective, marker) {
				t.Errorf("investigator %q must require %q", name, marker)
			}
		}
		for _, marker := range []string{
			`"hypotheses_examined"`, `"dynamic_experiments"`, `"experiment_methods"`, `"surface_exhausted"`,
			`"static_mode"`, `"next_experiment_artifact_id"`, `"top_candidates"`, `"maxItems":5`,
			`"minimum":6`, `"minimum":3`, `"minimum":2`, `"maxItems":64`, `"maxItems":32`,
		} {
			if !strings.Contains(task.OutputSchema, marker) {
				t.Errorf("investigator %q handoff schema must enforce %q", name, marker)
			}
		}
	}
	for _, marker := range []string{"spec-versus-implementation", "differential execution", "decoder strictness"} {
		if !strings.Contains(strings.ToLower(byName["consensus-and-execution-investigator"].Objective), marker) {
			t.Errorf("consensus investigator must carry the %q recipe", marker)
		}
	}
	for _, marker := range []string{"decoder-strictness campaign", "every wire surface", "pre-authentication resource bounds"} {
		if !strings.Contains(strings.ToLower(byName["network-and-state-investigator"].Objective), marker) {
			t.Errorf("network investigator must carry the %q recipe", marker)
		}
	}

	investigatorSchema := byName[investigators[0]].OutputSchema
	validQuota := `{"version":1,"artifact_ids":[],"candidate_fingerprints":[],"coverage_ids":{},"blocker_ids":[],"conditions":{"ready":true,"hypotheses_examined":6,"dynamic_experiments":3,"experiment_methods":2,"surface_exhausted":false}}`
	if err := triggersv1alpha1.ValidateSecurityWorkflowOutput(investigatorSchema, validQuota); err != nil {
		t.Fatalf("investigator schema rejected completed evidence quota: %v", err)
	}
	invalidQuota := `{"version":1,"artifact_ids":[],"candidate_fingerprints":[],"coverage_ids":{},"blocker_ids":[],"conditions":{"ready":true,"hypotheses_examined":6,"dynamic_experiments":0,"experiment_methods":0,"surface_exhausted":false,"static_mode":false}}`
	if err := triggersv1alpha1.ValidateSecurityWorkflowOutput(investigatorSchema, invalidQuota); err == nil {
		t.Error("investigator schema accepted a dynamic-mode handoff without experiments")
	}
	validStaticMode := `{"version":1,"artifact_ids":["aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"],"candidate_fingerprints":[],"coverage_ids":{},"blocker_ids":["bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"],"top_candidates":[{"handle":"aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa","impact":"pre-auth allocation before length check","confidence":"medium","next_experiment":"go test -run TestOversizedFrame ./p2p"}],"conditions":{"ready":true,"hypotheses_examined":6,"dynamic_experiments":0,"experiment_methods":0,"surface_exhausted":false,"static_mode":true,"next_experiment_artifact_id":"cccccccc-cccc-cccc-cccc-cccccccccccc"}}`
	if err := triggersv1alpha1.ValidateSecurityWorkflowOutput(investigatorSchema, validStaticMode); err != nil {
		t.Fatalf("investigator schema rejected a static-mode handoff: %v", err)
	}
	invalidStaticMode := `{"version":1,"artifact_ids":[],"candidate_fingerprints":[],"coverage_ids":{},"blocker_ids":[],"conditions":{"ready":true,"hypotheses_examined":2,"dynamic_experiments":0,"experiment_methods":0,"surface_exhausted":false,"static_mode":true}}`
	if err := triggersv1alpha1.ValidateSecurityWorkflowOutput(investigatorSchema, invalidStaticMode); err == nil {
		t.Error("investigator schema accepted static mode with fewer than six hypotheses")
	}
	validExhaustion := `{"version":1,"artifact_ids":["aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"],"candidate_fingerprints":[],"coverage_ids":{},"blocker_ids":[],"conditions":{"ready":true,"hypotheses_examined":2,"dynamic_experiments":1,"experiment_methods":1,"surface_exhausted":true}}`
	if err := triggersv1alpha1.ValidateSecurityWorkflowOutput(investigatorSchema, validExhaustion); err != nil {
		t.Fatalf("investigator schema rejected documented surface exhaustion: %v", err)
	}
	invalidExhaustion := `{"version":1,"artifact_ids":[],"candidate_fingerprints":[],"coverage_ids":{},"blocker_ids":[],"conditions":{"ready":true,"hypotheses_examined":2,"dynamic_experiments":1,"experiment_methods":1,"surface_exhausted":true}}`
	if err := triggersv1alpha1.ValidateSecurityWorkflowOutput(investigatorSchema, invalidExhaustion); err == nil {
		t.Error("investigator schema accepted surface_exhausted without an artifact")
	}
	unexplainedBlock := `{"version":1,"artifact_ids":[],"candidate_fingerprints":[],"coverage_ids":{},"blocker_ids":[],"conditions":{"ready":false,"hypotheses_examined":0,"dynamic_experiments":0,"experiment_methods":0,"surface_exhausted":false}}`
	if err := triggersv1alpha1.ValidateSecurityWorkflowOutput(investigatorSchema, unexplainedBlock); err == nil {
		t.Error("investigator schema accepted ready=false without a blocker or reason")
	}
	for _, name := range investigators {
		if err := triggersv1alpha1.ValidateSecurityWorkflowOutput(byName[name].OutputSchema, byName[name].When.OtherwiseOutput); err != nil {
			t.Fatalf("investigator %q schema rejected its gate-skipped output: %v", name, err)
		}
	}

	fuzz, ok := byName["hypothesis-driven-fuzz-research"]
	if !ok {
		t.Fatal("protocol workflow is missing hypothesis-driven-fuzz-research")
	}
	if fuzz.Role != "fuzz-researcher" || fuzz.Timeout.Duration < 120*time.Minute || fuzz.MaxTurns < 250 {
		t.Errorf("fuzz research must be a persistent fuzz-researcher task: role=%q timeout=%s maxTurns=%d", fuzz.Role, fuzz.Timeout.Duration, fuzz.MaxTurns)
	}
	if fuzz.DockerInDocker == nil || *fuzz.DockerInDocker {
		t.Error("fuzz research must run in the local sandbox with dockerInDocker false")
	}
	if fuzz.When == nil || fuzz.When.Task != "runtime-preflight-and-dossier" || fuzz.When.Path != "conditions.dynamic_ready" || fuzz.When.Equals != "true" {
		t.Errorf("fuzz research must gate on the dynamic readiness gate: %#v", fuzz.When)
	}
	if !slices.Contains(fuzz.DependsOn, "target-priors") || !strings.Contains(fuzz.Objective, "{{tasks.target-priors.output}}") {
		t.Error("fuzz research must consume the inline target priors")
	}
	for _, skill := range []string{
		"trail-of-bits-harness-writing", "trail-of-bits-cargo-fuzz", "trail-of-bits-fuzzing-dictionary",
		"trail-of-bits-fuzzing-obstacles", "trail-of-bits-coverage-analysis", "trail-of-bits-address-sanitizer",
	} {
		if !slices.ContainsFunc(fuzz.SkillRefs, func(ref platformv1alpha1.NamedRef) bool { return ref.Name == skill }) {
			t.Errorf("fuzz research is missing skill %q", skill)
		}
	}
	fuzzObjective := strings.ToLower(fuzz.Objective)
	for _, marker := range []string{
		"input_surface", "recent_change", "create_security_hypothesis", "repository harnesses", "testing.f", "cargo-fuzz", "proptest",
		"differential", "harness_origin", "model_authored", "model_modified", "harness_digest", "fixtures and test vectors",
		"run_security_tool", "go-fuzz-tests", "fuzztime up to 15m", "directly in the sandbox", "exact command",
		"harness_bug", "sanitizer_or_panic", "invariant_or_differential_violation", "minimize", "standalone deterministic regression test",
		"root-cause", "negative control", "reachability", "plateau", "at least three rounds", "at least two distinct surfaces",
		"harness_summary", "blocker artifact", "next_experiment manifest", "record_security_coverage", "experiment_kind fuzz",
		"crashes are candidates, not findings", "report_security_finding", "never fuzz live",
		"conditions.rounds_completed", "conditions.crashes_triaged", "conditions.surfaces_covered",
	} {
		if !strings.Contains(fuzzObjective, marker) {
			t.Errorf("fuzz research must require %q", marker)
		}
	}
	for _, marker := range []string{`"rounds_completed"`, `"crashes_triaged"`, `"surfaces_covered"`, `"top_candidates"`, `"static_mode"`} {
		if !strings.Contains(fuzz.OutputSchema, marker) {
			t.Errorf("fuzz handoff schema must declare %q", marker)
		}
	}
	validFuzz := `{"version":1,"artifact_ids":["aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"],"candidate_fingerprints":["fp-1"],"coverage_ids":{"not_tested":["dddddddd-dddd-dddd-dddd-dddddddddddd"]},"blocker_ids":[],"top_candidates":[{"handle":"fp-1","impact":"panic in pre-auth RLP decoder","confidence":"high","next_experiment":"replay minimized input through the sync handler"}],"conditions":{"ready":true,"rounds_completed":3,"crashes_triaged":2,"surfaces_covered":2,"hypotheses_examined":3,"dynamic_experiments":3,"experiment_methods":2,"surface_exhausted":false,"next_experiment_artifact_id":"cccccccc-cccc-cccc-cccc-cccccccccccc"}}`
	if err := triggersv1alpha1.ValidateSecurityWorkflowOutput(fuzz.OutputSchema, validFuzz); err != nil {
		t.Fatalf("fuzz schema rejected a completed research handoff: %v", err)
	}
	shallowFuzz := `{"version":1,"artifact_ids":["aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"],"candidate_fingerprints":[],"coverage_ids":{},"blocker_ids":[],"conditions":{"ready":true,"rounds_completed":1,"crashes_triaged":0,"surfaces_covered":1,"hypotheses_examined":1,"dynamic_experiments":1,"experiment_methods":1,"surface_exhausted":false}}`
	if err := triggersv1alpha1.ValidateSecurityWorkflowOutput(fuzz.OutputSchema, shallowFuzz); err == nil {
		t.Error("fuzz schema accepted a single smoke-test round as completed research")
	}
	if err := triggersv1alpha1.ValidateSecurityWorkflowOutput(fuzz.OutputSchema, fuzz.When.OtherwiseOutput); err != nil {
		t.Fatalf("fuzz schema rejected its dynamic-gate skipped output: %v", err)
	}
	if !strings.Contains(fuzz.When.OtherwiseOutput, `"reason":"runtime_dynamic_blocked"`) {
		t.Error("fuzz skipped output must state runtime_dynamic_blocked")
	}

	var role platformv1alpha1.RoleInstruction
	readBootstrapAsset(t, "roleinstructions", "protocol-investigator", &role)
	for _, marker := range []string{
		"hypothesis anchoring", "target priors list", "at least half of your hypotheses", "not_tested coverage",
		"definition of a dynamic experiment", "model-authored or model-modified",
		"unmodified repository test suite is preflight or reading, never an experiment",
		"blocked versus falsified", "blocked, never falsified", "stale-class ban", "static mode",
		"conditions.dynamic_ready false", "conditions.static_mode true", "budget- and coverage-based, never quota-based",
		"next_experiment manifest artifact", "more than about 40% of the turn budget unused", "delta first",
		"at least six distinct high-impact hypotheses", "at least three calibrated dynamic experiments",
		"at least two methods", "explicit exhausted-surface artifact", "hypotheses_examined",
		"dynamic_experiments", "experiment_methods", "surface_exhausted", "static_mode", "next_experiment_artifact_id",
	} {
		if !strings.Contains(strings.ToLower(role.Spec.Instructions), marker) {
			t.Errorf("protocol investigator role must require %q", marker)
		}
	}

	collector := byName["collect-investigator-handoffs"]
	for _, dependency := range append(append([]string{"runtime-preflight-and-dossier"}, investigators...), "hypothesis-driven-fuzz-research") {
		if !slices.Contains(collector.DependsOn, dependency) {
			t.Errorf("handoff collector must concatenate %q", dependency)
		}
	}
	if collector.Reduce != "concat" || !strings.Contains(collector.OutputSchema, `"minItems":6,"maxItems":6`) {
		t.Errorf("handoff collector must concatenate exactly six handoffs: reduce=%q", collector.Reduce)
	}
	if strings.Contains(collector.Objective, "include_payload") {
		t.Error("handoff collector must not recursively load durable artifact bodies")
	}

	selector := byName["select-adaptive-challenges"]
	for _, marker := range []string{"{{tasks.collect-investigator-handoffs.output}}", "{{tasks.target-priors.output}}"} {
		if !strings.Contains(selector.Objective, marker) {
			t.Errorf("adaptive selection must consume %s", marker)
		}
	}
	for _, marker := range []string{
		"between three and eight", "mode challenge", "mode extend", "fewer than three candidates", "blocked or inadequately_tested",
		"unexamined priors", "conditions.experiment", "instead of doing nothing", "conditions.mode", "conditions.static_mode",
	} {
		if !strings.Contains(strings.ToLower(selector.Objective), marker) {
			t.Errorf("adaptive selection must require %q", marker)
		}
	}
	if selector.When == nil || selector.When.Task != "runtime-preflight-and-dossier" || selector.When.Path != "conditions.ready" {
		t.Errorf("adaptive selection must gate on static readiness: %#v", selector.When)
	}
	selectedItem := func(mode, extra string) string {
		return `{"version":1,"artifact_ids":["aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"],"candidate_fingerprints":[],"coverage_ids":{},"blocker_ids":[],"conditions":{"ready":true,"mode":"` + mode + `","static_mode":false,"rationale":"highest expected information gain"` + extra + `}}`
	}
	threeSelections := "[" + strings.Join([]string{
		selectedItem("challenge", ""),
		selectedItem("extend", `,"hypothesis_id":"hyp-1","experiment":"author a mutant that widens the frame limit and rerun the decoder test"`),
		selectedItem("extend", `,"prior_id":"p3","experiment":"differential decode of blob transactions against the upstream revision"`),
	}, ",") + "]"
	if err := triggersv1alpha1.ValidateSecurityWorkflowOutput(selector.OutputSchema, threeSelections); err != nil {
		t.Fatalf("selection schema rejected a three-item challenge/extend plan: %v", err)
	}
	if err := triggersv1alpha1.ValidateSecurityWorkflowOutput(selector.OutputSchema, "["+selectedItem("challenge", "")+"]"); err == nil {
		t.Error("selection schema accepted a single item; the challenge phase must always have at least three work items")
	}
	if err := triggersv1alpha1.ValidateSecurityWorkflowOutput(selector.OutputSchema, "["+strings.Repeat(selectedItem("extend", "")+",", 2)+selectedItem("extend", "")+"]"); err == nil {
		t.Error("selection schema accepted an extend item without the named missing experiment")
	}
	if err := triggersv1alpha1.ValidateSecurityWorkflowOutput(selector.OutputSchema, selector.When.OtherwiseOutput); err != nil {
		t.Fatalf("selection schema rejected the gate-skipped empty plan: %v", err)
	}

	challenge := byName["challenge-and-variant-sweep"]
	if challenge.ForEach != "select-adaptive-challenges" || challenge.MaxInstances != 8 {
		t.Errorf("adaptive challenge fan-out = forEach %q maxInstances %d, want selected leads capped at 8", challenge.ForEach, challenge.MaxInstances)
	}
	if challenge.Timeout.Duration < 60*time.Minute {
		t.Errorf("challenge timeout = %s, want at least 60m to run extend experiments", challenge.Timeout.Duration)
	}
	for _, marker := range []string{"{{item}}", "mode challenge", "mode extend", "conditions.experiment", "conditions.static_mode", "cited total guard"} {
		if !strings.Contains(strings.ToLower(challenge.Objective), strings.ToLower(marker)) {
			t.Errorf("challenge objective must handle %q", marker)
		}
	}
	if !strings.Contains(challenge.OutputSchema, `"mode":{"type":"string","enum":["challenge","extend"]}`) {
		t.Error("challenge handoff must report its mode")
	}

	for _, name := range []string{"red-team-bounty-worthiness", "triage-and-report"} {
		if !slices.Contains(byName[name].DependsOn, "target-priors") {
			t.Errorf("%s must wait for target-priors", name)
		}
	}
	report := strings.ToLower(byName["triage-and-report"].Objective)
	for _, marker := range []string{"{{tasks.target-priors.output}}", "every prior", "not_tested", "next_experiment manifests", "fuzz"} {
		if !strings.Contains(report, marker) {
			t.Errorf("final report must reconcile %q", marker)
		}
	}
	for _, name := range append(append([]string{"runtime-preflight-and-dossier"}, investigators...), "hypothesis-driven-fuzz-research") {
		schema := byName[name].OutputSchema
		for _, field := range []string{"artifact_ids", "candidate_fingerprints", "coverage_ids", "blocker_ids"} {
			if !strings.Contains(schema, `"`+field+`"`) {
				t.Errorf("task %q handoff schema lacks %q", name, field)
			}
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
		var workflow triggersv1alpha1.SecurityWorkflow
		readBootstrapAsset(t, "securityworkflows", "blockchain-protocol-audit", &workflow)
		byName := make(map[string]triggersv1alpha1.SecurityScanTask, len(workflow.Spec.Tasks))
		for _, workflowTask := range workflow.Spec.Tasks {
			byName[workflowTask.Name] = workflowTask
		}
		// The protocol audit replaced the two-harness smoke baseline with
		// hypothesis-driven fuzz research; the fixed-bound baseline must not
		// creep back in beside it.
		if _, ok := byName["run-bounded-native-fuzz"]; ok {
			t.Fatal("blockchain protocol audit must not carry the fixed two-minute native fuzz baseline")
		}
		task, ok := byName["hypothesis-driven-fuzz-research"]
		if !ok {
			t.Fatal("blockchain protocol audit is missing hypothesis-driven-fuzz-research")
		}
		if task.Role != "fuzz-researcher" {
			t.Errorf("fuzz research role = %q, want fuzz-researcher", task.Role)
		}
		if !slices.Equal(task.DependsOn, []string{"runtime-preflight-and-dossier", "target-priors"}) || task.When == nil || task.When.Path != "conditions.dynamic_ready" {
			t.Errorf("fuzz research is not dynamic-readiness-gated: dependencies=%v when=%#v", task.DependsOn, task.When)
		}
		objective := strings.ToLower(task.Objective)
		for _, marker := range []string{"run_security_tool", "go-fuzz-tests", "cargo-fuzz", "fuzztime up to 15m", "not_found_under", "harness_summary", "at least three rounds"} {
			if !strings.Contains(objective, marker) {
				t.Errorf("fuzz research objective must require %q", marker)
			}
		}
		for _, stale := range []string{"at most two", "two minutes"} {
			if strings.Contains(objective, stale) {
				t.Errorf("fuzz research objective must not keep the smoke-test bound %q", stale)
			}
		}
		collector := byName["collect-investigator-handoffs"]
		if !slices.Contains(collector.DependsOn, task.Name) {
			t.Error("compact handoff collector must include fuzz research evidence IDs")
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

// TestBountyHuntEVMBuildsItsOracleFirst pins the fork-harness lane and its
// adversarial bounty gate. Build, calibration, hunting, and reproduction
// deliberately share one write-capable AgentRun so temporary harness files are
// not lost at handoffs.
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
		"pin-target-and-deployment", "fork-harness-hunt", "quantify-impact-and-eligibility",
		"red-team-bounty-worthiness", "triage-and-report",
	} {
		if _, ok := byName[name]; !ok {
			t.Fatalf("workflow is missing task %q", name)
		}
	}

	if len(workflow.Spec.Tasks) != 5 {
		t.Fatalf("workflow has %d tasks, want the five-task fork-harness DAG", len(workflow.Spec.Tasks))
	}
	dependsOn := func(task, dependency string) bool {
		return slices.Contains(byName[task].DependsOn, dependency)
	}
	for _, edge := range [][2]string{
		{"fork-harness-hunt", "pin-target-and-deployment"},
		{"quantify-impact-and-eligibility", "fork-harness-hunt"},
		{"red-team-bounty-worthiness", "quantify-impact-and-eligibility"},
		{"triage-and-report", "red-team-bounty-worthiness"},
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
			"consensus-and-execution-investigator": {
				"Payout-ordered priority 3 is owned here:",
				"Name upstream-fork-diff",
				"the invariant the upstream code used to satisfy",
				"record an unavailable upstream revision as a limitation",
			},
			"cross-chain-and-custody-investigator": {
				"Payout-ordered priority 2 is owned here: Priority 2",
				"who is allowed to have produced this proof",
				"what msg.sender becomes on the far side after relaying",
			},
			"crypto-economics-and-release-investigator": {
				"Payout-ordered priorities 1, 4, 5, 6, and 7 are owned here",
				"Priority 1, initialization and upgrade state",
				"Priority 4, encoding and decoding round trips and replay",
				"Priority 5, missing balance and state validation",
				"Priority 6, accounting, rounding, precision and decimals",
				"Priority 7, access control",
				"the chain-read pack reads the EIP-1967 implementation, admin and beacon slots",
				"operator-authorized fork endpoint alias",
				"Templated flash-loan oracle manipulation stays AVAILABLE but is off the default bounty path",
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

// TestBlockchainProtocolAuditTaskTimeoutsHonorTheBountyRuntimeBudget guards
// against a per-task timeout silently undercutting the bug-bounty policy
// pack's 4h maxRuntime. The controller applies the smallest of scan, budget,
// and task limits, so a 25m task timeout paused preflight runs on large
// client repositories (agave, reth) long before the 4h budget and stalled the
// whole DAG until an operator extended maxRuntime by hand.
func TestBlockchainProtocolAuditTaskTimeoutsHonorTheBountyRuntimeBudget(t *testing.T) {
	t.Parallel()

	var pack triggersv1alpha1.SecurityPolicyPack
	readBootstrapAsset(t, "securitypolicypacks", "bug-bounty", &pack)
	if pack.Spec.Budgets == nil || pack.Spec.Budgets.MaxRuntime.Duration <= 0 {
		t.Fatal("bug-bounty policy pack must declare budgets.maxRuntime")
	}
	budget := pack.Spec.Budgets.MaxRuntime.Duration

	var workflow triggersv1alpha1.SecurityWorkflow
	readBootstrapAsset(t, "securityworkflows", "blockchain-protocol-audit", &workflow)
	for _, task := range workflow.Spec.Tasks {
		if task.Timeout.Duration > 0 && task.Timeout.Duration < budget {
			t.Errorf("task %q timeout %s undercuts the bug-bounty maxRuntime %s; drop the task timeout or raise it to the budget", task.Name, task.Timeout.Duration, budget)
		}
	}
}
