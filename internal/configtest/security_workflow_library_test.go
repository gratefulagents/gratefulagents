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
	"algorand-security-review",
	"aptos-move-security-review",
	"auth-surface-audit",
	"bitcoin-lightning-security-review",
	"blockchain-protocol-audit",
	"bridge-l2-zk-security-review",
	"cairo-starknet-security-review",
	"cosmos-abci-halt-review",
	"cosmos-ibc-security-review",
	"default-deep-scan",
	"external-flow-analysis",
	"kubernetes-operator-audit",
	"mpc-cryptography-security-review",
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
	"web-app-owasp",
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
			"aderyn-static-analysis",
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
		"aderyn-static-analysis":                   "aderyn",
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
