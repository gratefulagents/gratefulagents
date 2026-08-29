package configtest

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"sigs.k8s.io/yaml"
)

var workflowParameterRefPattern = regexp.MustCompile(`\{\{\s*params\.([a-zA-Z_][a-zA-Z0-9_]*)`)

func TestSecurityProgramTargetReferencesAndParameters(t *testing.T) {
	t.Parallel()

	workflowEntries, err := os.ReadDir(repoPath("configs", "securityworkflows"))
	if err != nil {
		t.Fatal(err)
	}
	workflows := make(map[string]triggersv1alpha1.SecurityWorkflow, len(workflowEntries))
	for _, entry := range workflowEntries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".yaml") {
			continue
		}
		name := strings.TrimSuffix(entry.Name(), ".yaml")
		var workflow triggersv1alpha1.SecurityWorkflow
		readBootstrapAsset(t, "securityworkflows", name, &workflow)
		workflows[name] = workflow

		declared := make(map[string]bool, len(workflow.Spec.Parameters))
		for _, parameter := range workflow.Spec.Parameters {
			declared[parameter.Name] = true
		}
		for _, task := range workflow.Spec.Tasks {
			for _, match := range workflowParameterRefPattern.FindAllStringSubmatch(task.Objective, -1) {
				if !declared[match[1]] {
					t.Errorf("workflow %q task %q references undeclared parameter %q", name, task.Name, match[1])
				}
			}
		}
	}

	programEntries, err := os.ReadDir(repoPath("configs", "securityprograms"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range programEntries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".yaml") {
			continue
		}
		name := strings.TrimSuffix(entry.Name(), ".yaml")
		var program triggersv1alpha1.SecurityProgram
		readBootstrapAsset(t, "securityprograms", name, &program)
		for _, target := range program.Spec.EffectiveScanTargets() {
			workflow, exists := workflows[target.WorkflowRef]
			if !exists {
				t.Errorf("program %q target %q references missing workflow %q", name, target.ScanName, target.WorkflowRef)
				continue
			}
			if _, err := os.Stat(repoPath("configs", "securitypolicypacks", target.PolicyPackRef+".yaml")); err != nil {
				t.Errorf("program %q target %q references missing policy pack %q: %v", name, target.ScanName, target.PolicyPackRef, err)
			}
			declared := make(map[string]triggersv1alpha1.SecurityWorkflowParameter, len(workflow.Spec.Parameters))
			for _, parameter := range workflow.Spec.Parameters {
				declared[parameter.Name] = parameter
			}
			for parameter := range target.ParameterValues {
				if _, exists := declared[parameter]; !exists {
					t.Errorf("program %q target %q supplies undeclared parameter %q to workflow %q", name, target.ScanName, parameter, target.WorkflowRef)
				}
			}
			for parameterName, parameter := range declared {
				_, supplied := target.ParameterValues[parameterName]
				if parameter.Required && !supplied && parameter.Default == "" {
					t.Errorf("program %q target %q omits required parameter %q for workflow %q", name, target.ScanName, parameterName, target.WorkflowRef)
				}
			}
		}
	}
}

func TestSecurityProgramLibrary(t *testing.T) {
	t.Parallel()

	expectedCategories := map[string][]string{
		"immunefi-cosmos":    {"Blockchain/DLT"},
		"immunefi-etherfi":   {"Smart Contract"},
		"immunefi-marinade":  {"Smart Contract"},
		"immunefi-1inch":     {"Smart Contract"},
		"immunefi-aave":      {"Smart Contract"},
		"immunefi-arbitrum":  {"Smart Contract"},
		"immunefi-chainlink": {"Smart Contract", "Web & App"},
		"immunefi-filecoin":  {"Blockchain/DLT"},
		"immunefi-jito":      {"Blockchain/DLT", "Smart Contract"},
		"immunefi-kamino":    {"Smart Contract", "Web & App"},
		"immunefi-lido":      {"Smart Contract", "Web & App"},
		"immunefi-optimism":  {"Blockchain/DLT", "Smart Contract", "Web & App"},
		"immunefi-sei":       {"Blockchain/DLT"},
		"immunefi-sky":       {"Smart Contract", "Web & App"},
		"immunefi-spark":     {"Smart Contract", "Web & App"},
		"immunefi-wormhole":  {"Blockchain/DLT", "Smart Contract"},
	}
	expectedProgramURLs := map[string]string{
		"coinkite-coldcard":               "https://coinkite.com/responsible-disclosure",
		"hackenproof-1inch-contracts":     "https://hackenproof.com/programs/1inch-smart-contract",
		"hackenproof-1inch-wallet":        "https://hackenproof.com/programs/1inch-wallet",
		"hackenproof-account-abstraction": "https://hackenproof.com/programs/account-abstraction-bugs",
		"hackenproof-adi-zkvm":            "https://hackenproof.com/programs/adi-foundation-zkvm-verification",
		"hackenproof-aptos-network":       "https://hackenproof.com/programs/aptos-network",
		"hackenproof-atomone":             "https://hackenproof.com/programs/atomone",
		"hackenproof-aurora":              "https://hackenproof.com/programs/aurora-smart-contract",
		"hackenproof-deltaprime":          "https://hackenproof.com/programs/deltaprime-smart-contracts",
		"hackenproof-enkrypt-wallet":      "https://hackenproof.com/programs/enkrypt-wallet",
		"hackenproof-flow-protocol":       "https://hackenproof.com/programs/flow-protocol",
		"hackenproof-hyperbridge":         "https://hackenproof.com/programs/hyperbridge-protocol",
		"hackenproof-kaia":                "https://hackenproof.com/programs/kaia-protocol",
		"hackenproof-layer3":              "https://hackenproof.com/programs/layer3-smart-contracts",
		"hackenproof-linear":              "https://hackenproof.com/programs/linear-protocol",
		"hackenproof-myetherwallet":       "https://hackenproof.com/programs/myetherwallet",
		"hackenproof-near-bridges":        "https://hackenproof.com/programs/near-intents-bridges",
		"hackenproof-near-contracts":      "https://hackenproof.com/programs/near-smart-contracts-1",
		"hackenproof-near-intents":        "https://hackenproof.com/programs/near-intents-smart-contracts",
		"hackenproof-risc-zero-verifiers": "https://hackenproof.com/programs/risc-zero-blockchain-verifiers",
		"hackenproof-scallop":             "https://hackenproof.com/programs/scallop-protocol-smart-contract",
		"hackenproof-snowbridge":          "https://hackenproof.com/programs/snowbridge-on-chain-code",
		"hackenproof-slush-wallet":        "https://hackenproof.com/programs/slush-wallet",
		"hackenproof-sui-protocol":        "https://hackenproof.com/programs/sui-protocol",
		"hackenproof-vechainthor":         "https://hackenproof.com/programs/vechainthor",
		"shiftcrypto-bitbox":              "https://bitbox.swiss/policies/bug-bounty-policy/",
		"solana-agave":                    "https://github.com/anza-xyz/agave/security",
		"firedancer":                      "https://bounty.firedancer.io/",
		"hackerone-coinbase":              "https://hackerone.com/coinbase",
		"bugcrowd-openai":                 "https://bugcrowd.com/engagements/openai",
		"immunefi-filecoin":               "https://immunefi.com/bug-bounty/filecoin/scope/",
	}
	expectedVerbatimMarkers := map[string][]string{
		"coinkite-coldcard":               {"Official Coinkite responsible-disclosure policy snapshot", "Security vulnerabilities in Coinkite hardware, firmware, and bootloaders", "Coinkite evaluates rewards case by case"},
		"hackenproof-1inch-wallet":        {"Public HackenProof scope snapshot", "Live Program is active now", "Range of bounty: $100 - $100,000"},
		"hackenproof-1inch-contracts":     {"Public HackenProof scope snapshot", "https://github.com/1inch/cross-chain-swap", "Range of bounty: $100 - $500,000"},
		"hackenproof-account-abstraction": {"Public HackenProof scope snapshot", "https://github.com/eth-infinitism/account-abstraction", "Range of bounty: $1,000 - $250,000", "4cbc06072cdc19fd60f285c5997f4f7f57a588de"},
		"hackenproof-aurora":              {"Public HackenProof scope snapshot", "https://github.com/aurora-is-near/aurora-engine/tree/master/engine", "Range of bounty: $500 - $300,000"},
		"hackenproof-deltaprime":          {"Public HackenProof scope snapshot", "https://github.com/DeltaPrimeLabs/deltaprime-contracts/tree/v1.1.0", "Range of bounty: $1,000 - $250,000"},
		"hackenproof-flow-protocol":       {"Public HackenProof scope snapshot", "https://github.com/onflow/flow-evm-bridge", "Range of bounty: $0 - $25,000"},
		"hackenproof-layer3":              {"Public HackenProof scope snapshot", "https://github.com/layer3xyz/cubes/blob/main/src/escrow/Factory.sol", "Range of bounty: $0 - $500,000"},
		"hackenproof-risc-zero-verifiers": {"Public HackenProof scope snapshot", "https://github.com/risc0/risc0-ethereum/tree/main/contracts/src", "Range of bounty: $250 - $150,000"},
		"hackenproof-scallop":             {"Public HackenProof scope snapshot", "https://github.com/scallop-io/sui-lending-protocol", "Range of bounty: $30 - $300,000"},
		"hackenproof-adi-zkvm":            {"Public HackenProof scope snapshot", "ADI Foundation zkVM Verification", "Range of bounty: $200 - $10,000"},
		"hackenproof-aptos-network":       {"Public HackenProof scope snapshot", "https://github.com/aptos-labs/aptos-core/tree/mainnet", "Range of bounty: $0 - $250,000"},
		"hackenproof-atomone":             {"Public HackenProof scope snapshot", "https://github.com/atomone-hub/atomone", "Range of bounty: $200 - $10,000"},
		"hackenproof-enkrypt-wallet":      {"Public HackenProof scope snapshot", "https://github.com/enkryptcom/enKrypt", "Range of bounty: $350 - $3,000"},
		"hackenproof-hyperbridge":         {"Public HackenProof scope snapshot", "https://github.com/polytope-labs/hyperbridge", "Range of bounty: $200 - $50,000"},
		"hackenproof-kaia":                {"Public HackenProof scope snapshot", "https://github.com/kaiachain/kaia", "Range of bounty: $100 - $30,000"},
		"hackenproof-linear":              {"Public HackenProof scope snapshot", "https://github.com/linear-protocol/LiNEAR", "Range of bounty: $100 - $5,000"},
		"hackenproof-myetherwallet":       {"Public HackenProof scope snapshot", "https://github.com/MyEtherWallet/MyEtherWallet/tree/feat/v7", "Range of bounty: $350 - $3,000"},
		"hackenproof-near-bridges":        {"Public HackenProof scope snapshot", "https://github.com/near/threshold-signatures", "$100 - $300,000 range"},
		"hackenproof-near-contracts":      {"Public HackenProof scope snapshot", "https://github.com/near/core-contracts/tree/master/lockup", "Range of bounty: $100 - $300,000"},
		"hackenproof-near-intents":        {"Public HackenProof scope snapshot", "https://github.com/near/intents/tree/main/escrow-swap", "Range of bounty: $100 - $300,000"},
		"hackenproof-snowbridge":          {"Public HackenProof scope snapshot", "https://github.com/Snowfork/snowbridge", "Range of bounty: $200 - $75,000"},
		"hackenproof-slush-wallet":        {"Public HackenProof scope snapshot", "Impact in Scope for Wallet", "Range of bounty: $1,000 - $30,000"},
		"hackenproof-sui-protocol":        {"Public HackenProof scope snapshot", "https://github.com/MystenLabs/sui/tree/testnet/crates/sui-node", "Range of bounty: $5,000 - $1,000,000"},
		"hackenproof-vechainthor":         {"Public HackenProof scope snapshot", "https://github.com/vechain/thor", "Range of bounty: $1,000 - $35,000"},
		"shiftcrypto-bitbox":              {"Official Shift Crypto bug bounty policy snapshot", "Official code implementations in production", "All Shift Crypto hardware"},
		"solana-agave":                    {"## Reporting security problems in the Agave Validator", "### Out of Scope:", "### Payment of Bug Bounties:"},
		"firedancer":                      {"Version effective: 2026-08-06", "Scope\nAny reachable code in the firedancer/fdctl", "Submission and Conduct"},
		"hackerone-coinbase":              {"Browser-verified public scope snapshot (2026-08-15)", "In-scope asset rows captured: 16", "Out-of-scope asset rows captured: 3", "Low and Medium findings are out of scope"},
		"bugcrowd-openai":                 {"Browser-verified program boundary (2026-08-15)", "Security Bug Bounty boundary", "Safety Bug Bounty is separate", "historical 25-row table"},
		"immunefi-filecoin":               {"Immunefi scope snapshot verified 2026-08-24", "29 source-code assets", "running local devnet", "$50,000 maximum bounty"},
	}
	type researchedBoundaryExpectation struct {
		impactCount     int
		outOfScopeCount int
		testingCount    int
		impactClause    string
		outOfScope      string
		prohibitedTest  string
		policyMarker    string
	}
	expectedResearchedBoundaries := map[string]researchedBoundaryExpectation{
		"hackenproof-scallop": {
			impactCount: 7, outOfScopeCount: 13, testingCount: 17,
			impactClause: "Fee payment bypass", outOfScope: "Theoretical vulnerabilities without any proof or demonstration.",
			prohibitedTest: "Any testing with mainnet or public testnet contracts; all testing should be done on private testnets",
			policyMarker:   "reported exclusively through hackenproof.com no later than 24 hours after discovery",
		},
		"immunefi-cosmos": {
			impactCount: 9, outOfScopeCount: 6, testingCount: 6,
			impactClause: "Non-determinism / consensus fork / AppHash divergence", outOfScope: "Assets not explicitly listed as in scope, including assets not owned by participating teams",
			prohibitedTest: "Do not test live mainnet or public testnet networks, deployed code, or production infrastructure; use local forks, local clusters, or private deployments",
			policyMarker:   "running local 4-node network",
		},
		"immunefi-etherfi": {
			impactCount: 21, outOfScopeCount: 5, testingCount: 5,
			impactClause: "Protocol insolvency", outOfScope: "Vulnerabilities whose root cause lies in third-party infrastructure or protocols",
			prohibitedTest: "Do not test deployed mainnet or public-testnet code; use a local fork",
			policyMarker:   "The live Immunefi table, not repository membership, is the authority for deployed eligibility.",
		},
		"immunefi-marinade": {
			impactCount: 6, outOfScopeCount: 15, testingCount: 8,
			impactClause: "Protocol insolvency", outOfScope: "Best practice critiques",
			prohibitedTest: "Any testing on mainnet or public testnet deployed code; all testing should be done on local-forks of either public testnet or mainnet",
			policyMarker:   "This scan selects the repository's mainnet branch for production relevance",
		},
	}
	containsString := func(values []string, want string) bool {
		for _, value := range values {
			if value == want {
				return true
			}
		}
		return false
	}
	containsImpact := func(values []triggersv1alpha1.SecurityProgramImpact, want string) bool {
		for _, value := range values {
			if value.Impact == want {
				return true
			}
		}
		return false
	}
	browserResearchedCatalogPrograms := map[string]struct{}{
		"coinkite-coldcard":               {},
		"hackenproof-1inch-wallet":        {},
		"hackenproof-1inch-contracts":     {},
		"hackenproof-account-abstraction": {},
		"hackenproof-aurora":              {},
		"hackenproof-deltaprime":          {},
		"hackenproof-flow-protocol":       {},
		"hackenproof-layer3":              {},
		"hackenproof-risc-zero-verifiers": {},
		"hackenproof-scallop":             {},
		"hackenproof-adi-zkvm":            {},
		"hackenproof-aptos-network":       {},
		"hackenproof-atomone":             {},
		"hackenproof-enkrypt-wallet":      {},
		"hackenproof-hyperbridge":         {},
		"hackenproof-kaia":                {},
		"hackenproof-linear":              {},
		"hackenproof-myetherwallet":       {},
		"hackenproof-near-bridges":        {},
		"hackenproof-near-contracts":      {},
		"hackenproof-near-intents":        {},
		"hackenproof-snowbridge":          {},
		"hackenproof-slush-wallet":        {},
		"hackenproof-sui-protocol":        {},
		"hackenproof-vechainthor":         {},
		"shiftcrypto-bitbox":              {},
		"hackerone-coinbase":              {},
		"bugcrowd-openai":                 {},
		"immunefi-filecoin":               {},
		"immunefi-cosmos":                 {},
		"immunefi-etherfi":                {},
		"immunefi-marinade":               {},
	}
	catalogProgramsWithoutImportTargets := map[string]string{
		"bugcrowd-openai":          "authenticated Bugcrowd",
		"hackenproof-1inch-wallet": "no source repository",
		"hackenproof-slush-wallet": "does not identify an exact source repository",
	}
	expectedCatalogTargetCounts := map[string]int{
		"coinkite-coldcard":               1,
		"hackenproof-1inch-contracts":     8,
		"hackenproof-account-abstraction": 1,
		"hackenproof-adi-zkvm":            3,
		"hackenproof-aptos-network":       1,
		"hackenproof-atomone":             1,
		"hackenproof-aurora":              3,
		"hackenproof-deltaprime":          1,
		"hackenproof-enkrypt-wallet":      1,
		"hackenproof-flow-protocol":       7,
		"hackenproof-hyperbridge":         1,
		"hackenproof-kaia":                1,
		"hackenproof-layer3":              1,
		"hackenproof-linear":              1,
		"hackenproof-myetherwallet":       1,
		"hackenproof-near-bridges":        5,
		"hackenproof-near-contracts":      2,
		"hackenproof-near-intents":        1,
		"hackenproof-risc-zero-verifiers": 1,
		"hackenproof-scallop":             1,
		"hackenproof-snowbridge":          2,
		"hackenproof-sui-protocol":        4,
		"hackenproof-vechainthor":         1,
		"shiftcrypto-bitbox":              2,
		"hackerone-coinbase":              2,
		"immunefi-filecoin":               28,
		"immunefi-cosmos":                 2,
		"immunefi-etherfi":                2,
		"immunefi-marinade":               1,
	}
	expectedCatalogTargetWorkflows := map[string]string{
		"coinkite-coldcard":               "wallet-security-review",
		"hackenproof-1inch-contracts":     "evm-orderbook-settlement-review",
		"hackenproof-account-abstraction": "bounty-hunt-evm",
		"hackenproof-adi-zkvm":            "bridge-l2-zk-security-review",
		"hackenproof-aptos-network":       "aptos-move-security-review",
		"hackenproof-atomone":             "cosmos-abci-halt-review",
		"hackenproof-aurora":              "blockchain-protocol-audit",
		"hackenproof-deltaprime":          "evm-lending-cdp-review",
		"hackenproof-enkrypt-wallet":      "wallet-security-review",
		"hackenproof-flow-protocol":       "blockchain-protocol-audit",
		"hackenproof-hyperbridge":         "substrate-xcm-security-review",
		"hackenproof-kaia":                "blockchain-protocol-audit",
		"hackenproof-layer3":              "evm-orderbook-settlement-review",
		"hackenproof-linear":              "near-contract-review",
		"hackenproof-myetherwallet":       "wallet-security-review",
		"hackenproof-near-bridges":        "bridge-l2-zk-security-review",
		"hackenproof-near-contracts":      "near-contract-review",
		"hackenproof-near-intents":        "near-contract-review",
		"hackenproof-risc-zero-verifiers": "bridge-l2-zk-security-review",
		"hackenproof-scallop":             "sui-move-security-review",
		"hackenproof-snowbridge":          "substrate-xcm-security-review",
		"hackenproof-sui-protocol":        "blockchain-protocol-audit",
		"hackenproof-vechainthor":         "blockchain-protocol-audit",
		"shiftcrypto-bitbox":              "wallet-security-review",
		"hackerone-coinbase":              "mpc-cryptography-security-review",
		"immunefi-filecoin":               "filecoin-security-review",
		"immunefi-cosmos":                 "blockchain-protocol-audit",
		"immunefi-etherfi":                "bounty-hunt-evm",
		"immunefi-marinade":               "solana-defi-program-review",
	}
	// A program whose in-scope repositories span more than one execution
	// environment cannot be reviewed by a single workflow: the Solana crates
	// in the 1inch scope and the Cadence contracts in the Flow scope need
	// different reviewers than their EVM and Go siblings. These overrides pin
	// the per-target choice so a future edit cannot quietly retarget one.
	expectedCatalogTargetWorkflowsByScan := map[string]map[string]string{
		"hackenproof-1inch-contracts": {
			"1inch-solana-crosschain-protocol": "solana-defi-program-review",
			"1inch-solana-fusion":              "solana-defi-program-review",
		},
		"hackenproof-flow-protocol": {
			"flow-core-contracts": "flow-cadence-review",
			"flow-evm-bridge":     "flow-cadence-review",
		},
	}
	expectedLatestReleaseTargets := map[string]map[string]string{
		"firedancer": {
			"firedancer": "v1.1.4",
		},
		"hackenproof-deltaprime": {
			"deltaprime-contracts": "v1.1.0",
		},
		"hackenproof-1inch-contracts": {
			"1inch-limit-order-protocol":       "4.3.2",
			"1inch-fusion-protocol":            "3.1.1",
			"1inch-cross-chain-swap":           "1.1.0",
			"1inch-token-plugins":              "2.0.0",
			"1inch-farming":                    "4.0.0",
			"1inch-delegating":                 "2.0.0",
			"1inch-solana-crosschain-protocol": "1.1.0",
			"1inch-solana-fusion":              "1.0.0-release",
		},
	}
	type expectedRepositoryTarget struct {
		repositoryURL string
		baseBranch    string
		scanName      string
	}
	expectedRepositoryTargets := map[string][]expectedRepositoryTarget{
		"hackenproof-scallop": {
			{repositoryURL: "https://github.com/scallop-io/sui-lending-protocol", baseBranch: "main", scanName: "hackenproof-scallop"},
		},
		"immunefi-cosmos": {
			{repositoryURL: "https://github.com/CosmWasm/cosmwasm", baseBranch: "v3.0.9", scanName: "immunefi-cosmos-cosmwasm"},
			{repositoryURL: "https://github.com/CosmWasm/wasmvm", baseBranch: "v3.0.7", scanName: "immunefi-cosmos-wasmvm"},
		},
		"immunefi-etherfi": {
			{repositoryURL: "https://github.com/etherfi-protocol/smart-contracts", baseBranch: "master", scanName: "immunefi-etherfi-smart-contracts"},
			{repositoryURL: "https://github.com/etherfi-protocol/cash-v3", baseBranch: "master", scanName: "immunefi-etherfi-cash-v3"},
		},
		"immunefi-marinade": {
			{repositoryURL: "https://github.com/marinade-finance/liquid-staking-program", baseBranch: "mainnet", scanName: "immunefi-marinade-liquid-staking-program"},
		},
		"coinkite-coldcard": {
			{repositoryURL: "https://github.com/Coldcard/firmware", baseBranch: "master", scanName: "coldcard-firmware"},
		},
		"hackenproof-enkrypt-wallet": {
			{repositoryURL: "https://github.com/enkryptcom/enKrypt", baseBranch: "main", scanName: "enkrypt-wallet"},
		},
		"hackenproof-myetherwallet": {
			{repositoryURL: "https://github.com/MyEtherWallet/MyEtherWallet", baseBranch: "feat/v7", scanName: "myetherwallet-web"},
		},
		"shiftcrypto-bitbox": {
			{repositoryURL: "https://github.com/BitBoxSwiss/bitbox02-firmware", baseBranch: "master", scanName: "bitbox02-firmware"},
			{repositoryURL: "https://github.com/BitBoxSwiss/bitbox-wallet-app", baseBranch: "master", scanName: "bitbox-wallet-app"},
		},
	}
	expectedImmunefiTargets := map[string]string{
		"immunefi-1inch":  "https://github.com/1inch/limit-order-protocol",
		"immunefi-aave":   "https://github.com/aave-dao/aave-v3-origin",
		"immunefi-jito":   "https://github.com/jito-foundation/jito-solana",
		"immunefi-kamino": "https://github.com/Kamino-Finance/klend",
		"immunefi-lido":   "https://github.com/lidofinance/core",
		"immunefi-sei":    "https://github.com/sei-protocol/sei-chain",
	}
	// Each smart-contract program is reviewed by the workflow written for its
	// protocol family, because the toolchain, harness and proof-of-concept
	// substrate differ per family: a lending protocol is exercised through its
	// own fork and invariant suites, a messaging protocol through its
	// two-domain mock harness, and a rollup through its node and bridge tests.
	expectedProtocolFamilyWorkflow := map[string]string{
		"immunefi-1inch":     "evm-orderbook-settlement-review",
		"immunefi-aave":      "evm-lending-cdp-review",
		"immunefi-arbitrum":  "cross-chain-messaging-review",
		"immunefi-axelar":    "cross-chain-messaging-review",
		"immunefi-chainlink": "cross-chain-messaging-review",
		"immunefi-filecoin":  "filecoin-security-review",
		"immunefi-hyperlane": "cross-chain-messaging-review",
		"immunefi-kamino":    "solana-defi-program-review",
		"immunefi-layerzero": "cross-chain-messaging-review",
		"immunefi-lido":      "evm-lending-cdp-review",
		"immunefi-olympus":   "evm-lending-cdp-review",
		"immunefi-optimism":  "rollup-stack-review",
		"immunefi-sky":       "evm-lending-cdp-review",
		"immunefi-spark":     "evm-lending-cdp-review",
		"immunefi-wormhole":  "cross-chain-messaging-review",
		"immunefi-zksync":    "rollup-stack-review",
	}
	// Only active programs with complete captured scopes belong in the shipped
	// catalog. Dead or archived programs must be removed instead of retained.
	const minimumProgramCount = 20

	paths, err := filepath.Glob(repoPath("configs", "securityprograms", "*.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) < minimumProgramCount {
		t.Fatalf("security program count = %d, want at least %d", len(paths), minimumProgramCount)
	}

	seen := make(map[string]struct{}, len(paths))
	for _, sourcePath := range paths {
		sourcePath := sourcePath
		t.Run(strings.TrimSuffix(filepath.Base(sourcePath), ".yaml"), func(t *testing.T) {
			source, err := os.ReadFile(sourcePath)
			if err != nil {
				t.Fatal(err)
			}
			mirrorPath := repoPath("dist", "chart", "files", "bootstrap", "securityprograms", filepath.Base(sourcePath))
			mirror, err := os.ReadFile(mirrorPath)
			if err != nil {
				t.Fatalf("chart bootstrap mirror missing: %v", err)
			}
			if !bytes.Equal(source, mirror) {
				t.Fatalf("%s and %s differ", sourcePath, mirrorPath)
			}

			var program triggersv1alpha1.SecurityProgram
			if err := yaml.UnmarshalStrict(source, &program); err != nil {
				t.Fatalf("parse %s: %v", sourcePath, err)
			}
			if program.APIVersion != "triggers.gratefulagents.dev/v1alpha1" || program.Kind != "SecurityProgram" {
				t.Fatalf("unexpected type metadata %q/%q", program.APIVersion, program.Kind)
			}
			if _, ok := seen[program.Name]; ok {
				t.Fatalf("duplicate metadata.name %q", program.Name)
			}
			seen[program.Name] = struct{}{}
			if errs := triggersv1alpha1.ValidateSecurityProgramSpec(program.Spec); len(errs) != 0 {
				t.Fatalf("invalid spec: %v", errs)
			}
			if program.Spec.ScanTarget != nil {
				t.Error("shipped programs must use scanTargets instead of deprecated scanTarget")
			}
			targets := program.Spec.EffectiveScanTargets()
			if reason, ok := catalogProgramsWithoutImportTargets[program.Name]; ok {
				if len(targets) != 0 {
					t.Errorf("program with unenforced testing prerequisite has %d importable scan targets", len(targets))
				}
				if !strings.Contains(program.Spec.ScopePolicy, reason) {
					t.Errorf("scopePolicy does not explain non-importable target boundary %q", reason)
				}
			}
			if _, ok := browserResearchedCatalogPrograms[program.Name]; ok {
				if len(program.Spec.OutOfScope) == 0 {
					t.Error("browser-researched catalog program has no typed out-of-scope boundary")
				}
				if len(program.Spec.ProhibitedTesting) == 0 {
					t.Error("browser-researched catalog program has no typed testing restrictions")
				}
				if strings.Contains(program.Spec.ScopePolicy, "Verification boundary") {
					t.Error("catalog program still contains the pre-research placeholder scope")
				}
				if want, expected := expectedResearchedBoundaries[program.Name]; expected {
					if len(program.Spec.InScopeImpacts) != want.impactCount || len(program.Spec.OutOfScope) != want.outOfScopeCount || len(program.Spec.ProhibitedTesting) != want.testingCount {
						t.Errorf("typed boundary counts = impacts %d, out-of-scope %d, testing %d; want %d, %d, %d", len(program.Spec.InScopeImpacts), len(program.Spec.OutOfScope), len(program.Spec.ProhibitedTesting), want.impactCount, want.outOfScopeCount, want.testingCount)
					}
					if !containsImpact(program.Spec.InScopeImpacts, want.impactClause) {
						t.Errorf("typed impacts missing researched clause %q", want.impactClause)
					}
					if !containsString(program.Spec.OutOfScope, want.outOfScope) {
						t.Errorf("typed out-of-scope boundary missing researched clause %q", want.outOfScope)
					}
					if !containsString(program.Spec.ProhibitedTesting, want.prohibitedTest) {
						t.Errorf("typed testing boundary missing researched clause %q", want.prohibitedTest)
					}
					if !strings.Contains(program.Spec.ScopePolicy, want.policyMarker) {
						t.Errorf("scopePolicy missing researched qualification %q", want.policyMarker)
					}
				}
			}
			if want, ok := expectedCatalogTargetCounts[program.Name]; ok {
				if got := len(targets); got != want {
					t.Fatalf("importable scan target count = %d, want %d", got, want)
				}
				if expected, ok := expectedRepositoryTargets[program.Name]; ok {
					for index, wantTarget := range expected {
						gotTarget := targets[index]
						if gotTarget.RepositoryURL != wantTarget.repositoryURL || gotTarget.TargetURL != "" || gotTarget.BaseBranch != wantTarget.baseBranch || gotTarget.ScanName != wantTarget.scanName {
							t.Errorf("scanTargets[%d] source boundary = repository %q, target %q, branch %q, scan %q; want repository %q, no target URL, branch %q, scan %q", index, gotTarget.RepositoryURL, gotTarget.TargetURL, gotTarget.BaseBranch, gotTarget.ScanName, wantTarget.repositoryURL, wantTarget.baseBranch, wantTarget.scanName)
						}
					}
				}
				for index, target := range targets {
					candidate := target.TargetURL
					if target.RepositoryURL != "" {
						candidate = target.RepositoryURL
					}
					if !strings.HasPrefix(candidate, "https://") {
						t.Errorf("scanTargets[%d] is not an explicit HTTPS target: %q", index, candidate)
					}
					if strings.ContainsAny(candidate, "*<>{}") {
						t.Errorf("scanTargets[%d] promotes a wildcard or placeholder: %q", index, candidate)
					}
					if target.RepositoryURL != "" {
						if target.BaseBranch == "" {
							t.Errorf("scanTargets[%d] repository has no verified default branch", index)
						}
						wantWorkflow := expectedCatalogTargetWorkflows[program.Name]
						if override, ok := expectedCatalogTargetWorkflowsByScan[program.Name][target.ScanName]; ok {
							wantWorkflow = override
						}
						if wantWorkflow == "" {
							wantWorkflow = "default-deep-scan"
						}
						if target.WorkflowRef != wantWorkflow || target.PolicyPackRef != "bug-bounty" {
							t.Errorf("scanTargets[%d] repository uses workflow/policy %q/%q, want %q/bug-bounty", index, target.WorkflowRef, target.PolicyPackRef, wantWorkflow)
						}
						continue
					}
					if target.BaseBranch != "" {
						t.Errorf("scanTargets[%d] web target unexpectedly declares baseBranch %q", index, target.BaseBranch)
					}
					if target.WorkflowRef != "web-app-full-assessment" && target.WorkflowRef != "web-api-assessment" {
						t.Errorf("scanTargets[%d] web target uses workflow %q", index, target.WorkflowRef)
					}
					if target.PolicyPackRef != "web-application" {
						t.Errorf("scanTargets[%d] web target uses policy %q", index, target.PolicyPackRef)
					}
				}
			}
			if program.Name == "firedancer" {
				if len(targets) != 1 {
					t.Fatalf("Firedancer importable scan target count = %d, want 1", len(targets))
				}
				if got := targets[0].RepositoryURL; got != "https://github.com/firedancer-io/firedancer" {
					t.Errorf("Firedancer scan target repository = %q, want official repository", got)
				}
				if got := targets[0].WorkflowRef; got != "blockchain-protocol-audit" {
					t.Errorf("Firedancer scan target workflow = %q, want blockchain-protocol-audit", got)
				}
			}
			if program.Name == "immunefi-ethena" {
				if len(targets) != 0 {
					t.Error("unreachable repository must not be exposed as an importable scan target")
				}
			}
			if program.Name == "immunefi-ethena" {
				const staleSpecHash = "ab616f9e4f6e286c9ad8cb6e0a027091a723da53b7d3c16e5283d401b6c83565"
				if got := program.Annotations["platform.gratefulagents.dev/bootstrap-replaces-spec-hashes"]; got != staleSpecHash {
					t.Errorf("bootstrap replacement hash = %q, want %q", got, staleSpecHash)
				}
			}
			if want, ok := expectedImmunefiTargets[program.Name]; ok {
				if len(targets) == 0 {
					t.Fatalf("active program with an in-scope repository has no scan target")
				}
				if got := targets[0].RepositoryURL; got != want {
					t.Errorf("scan target repository = %q, want %q", got, want)
				}
			}
			if wantWorkflow, ok := expectedProtocolFamilyWorkflow[program.Name]; ok {
				if len(targets) == 0 {
					t.Fatal("smart-contract bounty program has no scan target")
				}
				for index, target := range targets {
					if target.WorkflowRef != wantWorkflow {
						t.Errorf("scanTargets[%d].workflowRef = %q, want %q", index, target.WorkflowRef, wantWorkflow)
					}
				}
			}
			if releases, ok := expectedLatestReleaseTargets[program.Name]; ok {
				seenReleases := make(map[string]bool, len(releases))
				for index, target := range targets {
					want, expected := releases[target.ScanName]
					if !expected {
						t.Errorf("scanTargets[%d] %q has no verified release expectation", index, target.ScanName)
						continue
					}
					seenReleases[target.ScanName] = true
					if target.BaseBranch != want {
						t.Errorf("scanTargets[%d].baseBranch = %q, want verified release %q", index, target.BaseBranch, want)
					}
					if program.Name == "firedancer" {
						if _, supplied := target.ParameterValues["release_tag"]; supplied {
							t.Errorf("scanTargets[%d] supplies release_tag to a workflow that does not declare it", index)
						}
					} else if got := target.ParameterValues["release_tag"]; got != want {
						t.Errorf("scanTargets[%d].parameterValues[release_tag] = %q, want %q", index, got, want)
					}
				}
				if len(seenReleases) != len(releases) {
					t.Errorf("verified releases covered %d targets, want %d", len(seenReleases), len(releases))
				}
			}
			if program.Name == "immunefi-aave" {
				want := map[string]string{"project_root": "."}
				if got := targets[0].ParameterValues; !reflect.DeepEqual(got, want) {
					t.Errorf("scan target parameterValues = %#v, want %#v", got, want)
				}
			}
			isImmunefi := program.Spec.Provider == "Immunefi"
			if isImmunefi && !strings.HasPrefix(program.Spec.ProgramURL, "https://immunefi.com/bug-bounty/") {
				t.Fatalf("unexpected Immunefi provenance URL: %q", program.Spec.ProgramURL)
			}
			// Typed scope is what downstream gates read. A severity label is
			// only meaningful together with the ladder that assigned it, so
			// every shipped program names the system it is judged under.
			switch got := program.Spec.SeveritySystem; {
			case got == "":
				t.Error("shipped program declares no severitySystem")
			case isImmunefi && program.Name != "immunefi-cosmos" && got != string(triggersv1alpha1.SeveritySystemImmunefiV23):
				t.Errorf("severitySystem = %q, want immunefi-v2.3", got)
			case program.Name == "immunefi-cosmos" && got != string(triggersv1alpha1.SeveritySystemCustom):
				t.Errorf("severitySystem = %q, want custom Cosmos impact/likelihood framework", got)
			case program.Spec.Provider == "Ethereum Foundation" && got != string(triggersv1alpha1.SeveritySystemEthereumFoundation):
				t.Errorf("severitySystem = %q, want ethereum-foundation", got)
			}
			// Immunefi programs must publish the impact clauses a report has
			// to select from, or the report writer has nothing to select.
			if isImmunefi && len(program.Spec.InScopeImpacts) == 0 {
				t.Error("Immunefi program has no transcribed in-scope impacts")
			}
			for index, impact := range program.Spec.InScopeImpacts {
				if strings.TrimSpace(impact.Impact) == "" || strings.TrimSpace(impact.Level) == "" {
					t.Errorf("inScopeImpacts[%d] is incomplete: %+v", index, impact)
				}
				if !strings.Contains(program.Spec.ScopePolicy, impact.Impact) {
					t.Errorf("inScopeImpacts[%d] is not verbatim from the scope snapshot: %q", index, impact.Impact)
				}
			}
			if !isImmunefi {
				expectedURL := expectedProgramURLs[program.Name]
				if program.Spec.Provider == "Ethereum Foundation" {
					expectedURL = "https://ethereum.org/en/bug-bounty/"
				}
				if expectedURL == "" || program.Spec.ProgramURL != expectedURL {
					t.Errorf("unexpected non-Immunefi provider or provenance URL: %q %q", program.Spec.Provider, program.Spec.ProgramURL)
				}
				markers := expectedVerbatimMarkers[program.Name]
				if program.Spec.Provider == "Ethereum Foundation" {
					markers = []string{"# In Scope", "Currently execution layer clients (Besu, Erigon, Geth, Nethermind and Reth)", "# Out of scope", "Critical severity", "Take down the entire network"}
				}
				for _, marker := range markers {
					if !strings.Contains(program.Spec.ScopePolicy, marker) {
						t.Errorf("scopePolicy missing verbatim marker %q", marker)
					}
				}
				for _, summaryMarker := range []string{"Authoritative scope: the", "Eligible impacts:", "Testing and submission:"} {
					if strings.Contains(program.Spec.ScopePolicy, summaryMarker) {
						t.Errorf("scopePolicy still contains summarized section %q", summaryMarker)
					}
				}
			}
			categories, hasCategoryExpectation := expectedCategories[program.Name]
			if strings.Contains(program.Spec.ScopePolicy, "Verbatim Immunefi scope") {
				for _, marker := range []string{
					"Verbatim Immunefi scope",
					"Displayed wording is preserved; layout whitespace is normalized.",
					"Assets in Scope",
					"Impacts in Scope",
					"Out of scope",
				} {
					if !strings.Contains(program.Spec.ScopePolicy, marker) {
						t.Errorf("scopePolicy missing %q", marker)
					}
				}
				for _, summarizedMarker := range []string{"Repository targets:", "Eligible impacts:", "Testing and submission:"} {
					if strings.Contains(program.Spec.ScopePolicy, summarizedMarker) {
						t.Errorf("scopePolicy still contains summarized section %q", summarizedMarker)
					}
				}
			} else if isImmunefi {
				t.Error("dead or archived Immunefi program must not be shipped")
			}
			if hasCategoryExpectation && len(categories) > 0 {
				if got := strings.Count(program.Spec.ScopePolicy, "Assets in Scope"); got != len(categories) {
					t.Errorf("Assets in Scope sections = %d, want %d categories", got, len(categories))
				}
				if got := strings.Count(program.Spec.ScopePolicy, "Out of scope"); got != len(categories) {
					t.Errorf("Out of scope sections = %d, want %d categories", got, len(categories))
				}
				for _, category := range categories {
					if !strings.Contains(program.Spec.ScopePolicy, "\n"+category+"\n") {
						t.Errorf("scopePolicy missing category %q", category)
					}
				}
			}
		})
	}
}
