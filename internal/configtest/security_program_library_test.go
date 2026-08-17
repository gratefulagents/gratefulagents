package configtest

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"sigs.k8s.io/yaml"
)

func TestSecurityProgramLibrary(t *testing.T) {
	t.Parallel()

	expectedCategories := map[string][]string{
		"immunefi-1inch":     {"Smart Contract"},
		"immunefi-aave":      {"Smart Contract"},
		"immunefi-arbitrum":  {"Smart Contract"},
		"immunefi-chainlink": {"Smart Contract", "Web & App"},
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
		"hackenproof-snowbridge":          "https://hackenproof.com/programs/snowbridge-on-chain-code",
		"hackenproof-slush-wallet":        "https://hackenproof.com/programs/slush-wallet",
		"hackenproof-sui-protocol":        "https://hackenproof.com/programs/sui-protocol",
		"hackenproof-vechainthor":         "https://hackenproof.com/programs/vechainthor",
		"shiftcrypto-bitbox":              "https://bitbox.swiss/policies/bug-bounty-policy/",
		"solana-agave":                    "https://github.com/anza-xyz/agave/security",
		"firedancer":                      "https://bounty.firedancer.io/",
		"hackerone-gitlab":                "https://hackerone.com/gitlab",
		"hackerone-shopify":               "https://hackerone.com/shopify",
		"hackerone-uber":                  "https://hackerone.com/uber",
		"hackerone-coinbase":              "https://hackerone.com/coinbase",
		"hackerone-cloudflare":            "https://hackerone.com/cloudflare",
		"hackerone-playstation":           "https://hackerone.com/playstation",
		"hackerone-security":              "https://hackerone.com/security",
		"bugcrowd-openai":                 "https://bugcrowd.com/engagements/openai",
		"bugcrowd-atlassian":              "https://bugcrowd.com/engagements/atlassian",
		"bugcrowd-opera":                  "https://bugcrowd.com/engagements/opera",
		"intigriti-dropbox":               "https://app.intigriti.com/programs/dropbox/dropbox/detail",
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
		"hackerone-gitlab":                {"Browser-verified public scope snapshot (2026-08-15)", "In-scope asset rows captured: 19", "Out-of-scope asset rows captured: 25", "yourhandle@wearehackerone.com"},
		"hackerone-shopify":               {"Browser-verified public scope snapshot (2026-08-15)", "In-scope asset rows captured: 20", "Out-of-scope asset rows captured: 9", "YOURHANDLE@wearehackerone.com"},
		"hackerone-uber":                  {"Browser-verified public scope snapshot (2026-08-15)", "In-scope asset rows captured: 4", "Out-of-scope asset rows captured: 19", "*.uberinternal.com"},
		"hackerone-coinbase":              {"Browser-verified public scope snapshot (2026-08-15)", "In-scope asset rows captured: 16", "Out-of-scope asset rows captured: 3", "Low and Medium findings are out of scope"},
		"hackerone-cloudflare":            {"Browser-verified public scope snapshot (2026-08-15)", "In-scope asset rows captured: 51", "Out-of-scope asset rows captured: 20", "Customer zones and properties"},
		"hackerone-playstation":           {"Browser-verified public scope snapshot (2026-08-15)", "In-scope asset rows captured: 24", "Out-of-scope asset rows captured: 0", "*.api.playstation.com"},
		"hackerone-security":              {"Browser-verified public scope snapshot (2026-08-15)", "In-scope asset rows captured: 25", "Out-of-scope asset rows captured: 8", "X-Bug-Bounty: HackerOne-<username>"},
		"bugcrowd-openai":                 {"Browser-verified program boundary (2026-08-15)", "Security Bug Bounty boundary", "Safety Bug Bounty is separate", "historical 25-row table"},
		"bugcrowd-atlassian":              {"Browser-verified public scope snapshot (2026-08-15)", "In-scope asset rows captured: 48", "Out-of-scope asset rows captured: 13", "bugbounty-test-<bugcrowd-name>"},
		"bugcrowd-opera":                  {"Browser-verified public scope snapshot (2026-08-15)", "In-scope asset rows captured: 89", "Out-of-scope asset rows captured: 22", "com.opera.minipay"},
		"intigriti-dropbox":               {"Public Intigriti scope snapshot verified 2026-08-15", "Tier 1 assets", "Explicit out-of-scope asset rows", "X-Intigriti-Username: <username>"},
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
		"hackerone-gitlab":                {},
		"hackerone-shopify":               {},
		"hackerone-uber":                  {},
		"hackerone-coinbase":              {},
		"hackerone-cloudflare":            {},
		"hackerone-playstation":           {},
		"hackerone-security":              {},
		"bugcrowd-openai":                 {},
		"bugcrowd-atlassian":              {},
		"bugcrowd-opera":                  {},
		"intigriti-dropbox":               {},
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
		"hackenproof-snowbridge":          2,
		"hackenproof-sui-protocol":        4,
		"hackenproof-vechainthor":         1,
		"shiftcrypto-bitbox":              2,
		"bugcrowd-atlassian":              8,
		"bugcrowd-opera":                  37,
		"hackerone-cloudflare":            6,
		"hackerone-coinbase":              2,
		"hackerone-gitlab":                9,
		"hackerone-playstation":           12,
		"hackerone-security":              7,
		"hackerone-shopify":               6,
		"hackerone-uber":                  1,
		"intigriti-dropbox":               6,
	}
	expectedCatalogTargetWorkflows := map[string]string{
		"coinkite-coldcard":               "wallet-security-review",
		"hackenproof-1inch-contracts":     "bounty-hunt-evm",
		"hackenproof-account-abstraction": "bounty-hunt-evm",
		"hackenproof-adi-zkvm":            "bridge-l2-zk-security-review",
		"hackenproof-aptos-network":       "aptos-move-security-review",
		"hackenproof-atomone":             "cosmos-abci-halt-review",
		"hackenproof-aurora":              "blockchain-protocol-audit",
		"hackenproof-deltaprime":          "bounty-hunt-evm",
		"hackenproof-enkrypt-wallet":      "wallet-security-review",
		"hackenproof-flow-protocol":       "blockchain-protocol-audit",
		"hackenproof-hyperbridge":         "substrate-xcm-security-review",
		"hackenproof-kaia":                "blockchain-protocol-audit",
		"hackenproof-layer3":              "bounty-hunt-evm",
		"hackenproof-myetherwallet":       "wallet-security-review",
		"hackenproof-near-bridges":        "bridge-l2-zk-security-review",
		"hackenproof-near-contracts":      "smart-contract-review",
		"hackenproof-near-intents":        "smart-contract-review",
		"hackenproof-risc-zero-verifiers": "bridge-l2-zk-security-review",
		"hackenproof-snowbridge":          "substrate-xcm-security-review",
		"hackenproof-sui-protocol":        "blockchain-protocol-audit",
		"hackenproof-vechainthor":         "blockchain-protocol-audit",
		"shiftcrypto-bitbox":              "wallet-security-review",
	}
	// A program whose in-scope repositories span more than one execution
	// environment cannot be reviewed by a single workflow: the Solana crates
	// in the 1inch scope and the Cadence contracts in the Flow scope need
	// different reviewers than their EVM and Go siblings. These overrides pin
	// the per-target choice so a future edit cannot quietly retarget one.
	expectedCatalogTargetWorkflowsByScan := map[string]map[string]string{
		"hackenproof-1inch-contracts": {
			"1inch-solana-crosschain-protocol": "solana-anchor-security-review",
			"1inch-solana-fusion":              "solana-anchor-security-review",
		},
		"hackenproof-flow-protocol": {
			"flow-core-contracts": "smart-contract-review",
			"flow-evm-bridge":     "smart-contract-review",
		},
	}
	type expectedRepositoryTarget struct {
		repositoryURL string
		baseBranch    string
		scanName      string
	}
	expectedWalletRepositoryTargets := map[string][]expectedRepositoryTarget{
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
	expectedEVMBountyWorkflow := map[string]struct{}{
		"immunefi-1inch":    {},
		"immunefi-aave":     {},
		"immunefi-arbitrum": {},
		"immunefi-lido":     {},
		"immunefi-olympus":  {},
		"immunefi-sky":      {},
		"immunefi-spark":    {},
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
			}
			if want, ok := expectedCatalogTargetCounts[program.Name]; ok {
				if got := len(targets); got != want {
					t.Fatalf("importable scan target count = %d, want %d", got, want)
				}
				if expected, ok := expectedWalletRepositoryTargets[program.Name]; ok {
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
			if program.Name == "firedancer" && len(targets) != 0 {
				t.Error("Firedancer must not suggest a scan target without a selected in-scope release")
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
			if _, ok := expectedEVMBountyWorkflow[program.Name]; ok {
				if len(targets) == 0 {
					t.Fatal("EVM bounty program has no scan target")
				}
				for index, target := range targets {
					if target.WorkflowRef != "bounty-hunt-evm" {
						t.Errorf("scanTargets[%d].workflowRef = %q, want bounty-hunt-evm", index, target.WorkflowRef)
					}
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
			case isImmunefi && got != string(triggersv1alpha1.SeveritySystemImmunefiV23):
				t.Errorf("severitySystem = %q, want immunefi-v2.3", got)
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
