package configtest

import (
	"strings"
	"testing"

	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
)

func TestFilecoinSecurityProgramExactScope(t *testing.T) {
	t.Parallel()

	var program triggersv1alpha1.SecurityProgram
	readBootstrapAsset(t, "securityprograms", "immunefi-filecoin", &program)

	expectedTargets := map[string]string{
		"https://github.com/whyrusleeping/cbor-gen":                    "master",
		"https://github.com/filecoin-project/boost":                    "main",
		"https://github.com/ipfs/go-graphsync":                         "main",
		"https://github.com/filecoin-project/lotus":                    "master",
		"https://github.com/filecoin-project/rust-fil-proofs-ffi":      "master",
		"https://github.com/filecoin-project/rust-filecoin-proofs-api": "master",
		"https://github.com/filecoin-project/rust-fil-proofs":          "master",
		"https://github.com/filecoin-project/bellperson":               "master",
		"https://github.com/filecoin-project/merkletree":               "master",
		"https://github.com/lurk-lab/neptune":                          "main",
		"https://github.com/lurk-lab/neptune-triton":                   "master",
		"https://github.com/filecoin-project/paired":                   "master",
		"https://github.com/filecoin-project/go-address":               "master",
		"https://github.com/filecoin-project/go-amt-ipld":              "master",
		"https://github.com/filecoin-project/go-bitfield":              "master",
		"https://github.com/filecoin-project/go-cbor-util":             "master",
		"https://github.com/filecoin-project/go-crypto":                "master",
		"https://github.com/filecoin-project/go-data-transfer":         "master",
		"https://github.com/filecoin-project/go-fil-commcid":           "master",
		"https://github.com/filecoin-project/go-padreader":             "master",
		"https://github.com/filecoin-project/go-sectorbuilder":         "master",
		"https://github.com/filecoin-project/go-statemachine":          "master",
		"https://github.com/filecoin-project/go-statestore":            "master",
		"https://github.com/ipfs/go-hamt-ipld":                         "master",
		"https://github.com/ipfs/go-ipld-cbor":                         "master",
		"https://github.com/filecoin-project/builtin-actors":           "master",
		"https://github.com/filecoin-project/ref-fvm":                  "master",
		"https://github.com/filecoin-project/go-f3":                    "main",
	}
	if got := len(program.Spec.EffectiveScanTargets()); got != len(expectedTargets) {
		t.Fatalf("Filecoin scan targets = %d, want %d", got, len(expectedTargets))
	}
	for _, target := range program.Spec.EffectiveScanTargets() {
		branch, ok := expectedTargets[target.RepositoryURL]
		if !ok {
			t.Errorf("unexpected Filecoin scan target %q", target.RepositoryURL)
			continue
		}
		if target.BaseBranch != branch {
			t.Errorf("Filecoin target %q branch = %q, want %q", target.RepositoryURL, target.BaseBranch, branch)
		}
		if target.WorkflowRef != "filecoin-security-review" {
			t.Errorf("Filecoin target %q workflow = %q, want filecoin-security-review", target.RepositoryURL, target.WorkflowRef)
		}
		delete(expectedTargets, target.RepositoryURL)
	}
	if len(expectedTargets) != 0 {
		t.Errorf("missing Filecoin scan targets: %v", expectedTargets)
	}

	expectedAssets := map[string]struct{}{
		"https://github.com/whyrusleeping/cbor-gen": {}, "https://github.com/filecoin-project/boost": {},
		"https://github.com/ipfs/go-graphsync": {}, "https://github.com/filecoin-project/lotus/tree/master/miner": {},
		"https://github.com/filecoin-project/rust-fil-proofs-ffi": {}, "https://github.com/filecoin-project/rust-filecoin-proofs-api": {},
		"https://github.com/filecoin-project/rust-fil-proofs": {}, "https://github.com/filecoin-project/bellperson": {},
		"https://github.com/filecoin-project/merkletree": {}, "https://github.com/lurk-lab/neptune": {},
		"https://github.com/lurk-lab/neptune-triton": {}, "https://github.com/filecoin-project/paired": {},
		"https://github.com/filecoin-project/go-address": {}, "https://github.com/filecoin-project/go-amt-ipld": {},
		"https://github.com/filecoin-project/go-bitfield": {}, "https://github.com/filecoin-project/go-cbor-util": {},
		"https://github.com/filecoin-project/go-crypto": {}, "https://github.com/filecoin-project/go-data-transfer": {},
		"https://github.com/filecoin-project/go-fil-commcid": {}, "https://github.com/filecoin-project/go-padreader": {},
		"https://github.com/filecoin-project/go-sectorbuilder": {}, "https://github.com/filecoin-project/go-statemachine": {},
		"https://github.com/filecoin-project/go-statestore": {}, "https://github.com/ipfs/go-hamt-ipld": {},
		"https://github.com/ipfs/go-ipld-cbor": {}, "https://github.com/filecoin-project/lotus": {},
		"https://github.com/filecoin-project/builtin-actors": {}, "https://github.com/filecoin-project/ref-fvm": {},
		"https://github.com/filecoin-project/go-f3": {},
	}
	if got := len(program.Spec.Assets); got != len(expectedAssets) {
		t.Fatalf("Filecoin typed source assets = %d, want %d", got, len(expectedAssets))
	}
	for _, asset := range program.Spec.Assets {
		if _, ok := expectedAssets[asset.RepositoryURL]; !ok {
			t.Errorf("unexpected Filecoin source asset %q", asset.RepositoryURL)
		}
		delete(expectedAssets, asset.RepositoryURL)
	}
	if len(expectedAssets) != 0 {
		t.Errorf("missing Filecoin source assets: %v", expectedAssets)
	}

	expectedImpacts := map[string]string{
		"Direct loss of funds": "critical",
		"DoS of greater than 10% but less than 30% of validator or miner nodes and does not shut down the network":                                                                      "low",
		"High compute consumption by validator/mining nodes where a crash, memory exhaustion, or any other demonstrated lasting effect involving network availability is demonstrated.": "medium",
		"Unintended permanent chain split requiring hard fork (network partition requiring hard fork)":                                                                                  "critical",
		"Permanent freezing of funds (fix requires hardfork)":                                                                                                                           "critical",
		"Underpricing transaction fees relative to computation time":                                                                                                                    "low",
		"Contract on the platform fails to deliver promised returns, but doesn’t lose values":                                                                                           "low",
		"EVM instruction fails to execute when provided with concrete parameters":                                                                                                       "low",
		"Unintended chain split (Network partition) with localized impacts (which would require hard fork but doesn’t affect the chain as whole)":                                       "high",
		"Transient consensus failures (Temporary halt in transactions leading to consensus failure)":                                                                                    "high",
		"Protocol-level bug preventing contracts from using their funds":                                                                                                                "high",
		"Protocol-level bug causing the inability for developers to deploy new smart contracts":                                                                                         "high",
		"Protocol-level bug rendering a single contract unusable after the exploit (i.e. contract bricked)":                                                                             "high",
		"DoS of greater than 30% of validator or miner nodes and does not shut down the network":                                                                                        "medium",
		"EVM instruction fails to execute, in a general way":                                                                                                                            "medium",
		"Inability to deploy a contract under a specific circumstances":                                                                                                                 "medium",
		"Total Chain halt": "critical",
		"Protocol-level bug that causes a general breakage of all contracts deployed on the chain":     "critical",
		"Protocol-level bug that enables tricking contracts into sending funds to arbitrary addresses": "critical",
		"Inability to propagate new transactions (limited to fraction of the network)":                 "high",
	}
	if got := len(program.Spec.InScopeImpacts); got != len(expectedImpacts) {
		t.Fatalf("Filecoin impacts = %d, want %d", got, len(expectedImpacts))
	}
	for _, impact := range program.Spec.InScopeImpacts {
		level, ok := expectedImpacts[impact.Impact]
		if !ok || impact.Level != level {
			t.Errorf("unexpected Filecoin impact: %q (%s)", impact.Impact, impact.Level)
		}
		delete(expectedImpacts, impact.Impact)
	}
	if len(expectedImpacts) != 0 {
		t.Errorf("missing Filecoin impacts: %v", expectedImpacts)
	}
	if !strings.Contains(program.Spec.ScopePolicy, "https://filecoin.io    Primacy of Impact") {
		t.Error("Filecoin snapshot is missing the thirtieth non-source primacy row")
	}
}

func TestFilecoinSecurityWorkflowRequiresDevnetImpactProof(t *testing.T) {
	t.Parallel()

	var workflow triggersv1alpha1.SecurityWorkflow
	readBootstrapAsset(t, "securityworkflows", "filecoin-security-review", &workflow)
	byName := make(map[string]triggersv1alpha1.SecurityScanTask, len(workflow.Spec.Tasks))
	for _, task := range workflow.Spec.Tasks {
		byName[task.Name] = task
		if refs := workflowTaskOutputRefPattern.FindAllStringSubmatch(task.Objective, -1); len(refs) > 3 {
			t.Errorf("Filecoin task %q fans in %d full outputs, want at most 3", task.Name, len(refs))
		}
	}
	for _, name := range []string{
		"map-filecoin-scope-and-integration",
		"review-core-protocol",
		"review-storage-market-and-network",
		"review-state-data-and-encoding",
		"review-proofs-crypto-and-native",
		"merge-core-and-storage-candidates",
		"merge-state-and-proofs-candidates",
		"validate-on-filecoin-local-devnet",
		"triage-and-report",
	} {
		if _, ok := byName[name]; !ok {
			t.Errorf("Filecoin workflow is missing task %q", name)
		}
	}
	validator := byName["validate-on-filecoin-local-devnet"]
	for _, marker := range []string{
		"FilecoinFoundationWeb/filecoin-audit-kit",
		"running local devnet",
		"negative control",
		"never confirmed",
		"Do not test mainnet or a public testnet",
	} {
		if !strings.Contains(validator.Objective, marker) {
			t.Errorf("Filecoin devnet validator is missing %q", marker)
		}
	}
	for _, marker := range []string{"oracle_calibrated", "negative_control_passed", `"const":true`} {
		if !strings.Contains(validator.OutputSchema, marker) {
			t.Errorf("Filecoin devnet validator schema is missing %q", marker)
		}
	}
	mapper := byName["map-filecoin-scope-and-integration"]
	for _, track := range []string{"core-protocol", "storage-market-network", "state-data-encoding", "proofs-crypto-native"} {
		if !strings.Contains(mapper.OutputSchema, track) {
			t.Errorf("Filecoin workflow mapper is missing track %q", track)
		}
	}
}
