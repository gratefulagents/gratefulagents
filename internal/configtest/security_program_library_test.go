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
		"solana-agave":          "https://github.com/anza-xyz/agave/security",
		"firedancer":            "https://bounty.firedancer.io/",
		"hackerone-gitlab":      "https://hackerone.com/gitlab",
		"hackerone-shopify":     "https://hackerone.com/shopify",
		"hackerone-uber":        "https://hackerone.com/uber",
		"hackerone-coinbase":    "https://hackerone.com/coinbase",
		"hackerone-cloudflare":  "https://hackerone.com/cloudflare",
		"hackerone-playstation": "https://hackerone.com/playstation",
		"hackerone-security":    "https://hackerone.com/security",
		"bugcrowd-openai":       "https://bugcrowd.com/engagements/openai",
		"bugcrowd-atlassian":    "https://bugcrowd.com/engagements/atlassian",
		"bugcrowd-opera":        "https://bugcrowd.com/engagements/opera",
		"intigriti-dropbox":     "https://app.intigriti.com/programs/dropbox/dropbox/detail",
	}
	expectedVerbatimMarkers := map[string][]string{
		"solana-agave":          {"## Reporting security problems in the Agave Validator", "### Out of Scope:", "### Payment of Bug Bounties:"},
		"firedancer":            {"Version effective: 2026-08-06", "Scope\nAny reachable code in the firedancer/fdctl", "Submission and Conduct"},
		"hackerone-gitlab":      {"Browser-verified public scope snapshot (2026-08-15)", "In-scope asset rows captured: 19", "Out-of-scope asset rows captured: 25", "yourhandle@wearehackerone.com"},
		"hackerone-shopify":     {"Browser-verified public scope snapshot (2026-08-15)", "In-scope asset rows captured: 20", "Out-of-scope asset rows captured: 9", "YOURHANDLE@wearehackerone.com"},
		"hackerone-uber":        {"Browser-verified public scope snapshot (2026-08-15)", "In-scope asset rows captured: 4", "Out-of-scope asset rows captured: 19", "*.uberinternal.com"},
		"hackerone-coinbase":    {"Browser-verified public scope snapshot (2026-08-15)", "In-scope asset rows captured: 16", "Out-of-scope asset rows captured: 3", "Low and Medium findings are out of scope"},
		"hackerone-cloudflare":  {"Browser-verified public scope snapshot (2026-08-15)", "In-scope asset rows captured: 51", "Out-of-scope asset rows captured: 20", "Customer zones and properties"},
		"hackerone-playstation": {"Browser-verified public scope snapshot (2026-08-15)", "In-scope asset rows captured: 24", "Out-of-scope asset rows captured: 0", "*.api.playstation.com"},
		"hackerone-security":    {"Browser-verified public scope snapshot (2026-08-15)", "In-scope asset rows captured: 25", "Out-of-scope asset rows captured: 8", "X-Bug-Bounty: HackerOne-<username>"},
		"bugcrowd-openai":       {"Browser-verified program boundary (2026-08-15)", "Security Bug Bounty boundary", "Safety Bug Bounty is separate", "historical 25-row table"},
		"bugcrowd-atlassian":    {"Browser-verified public scope snapshot (2026-08-15)", "In-scope asset rows captured: 48", "Out-of-scope asset rows captured: 13", "bugbounty-test-<bugcrowd-name>"},
		"bugcrowd-opera":        {"Browser-verified public scope snapshot (2026-08-15)", "In-scope asset rows captured: 89", "Out-of-scope asset rows captured: 22", "com.opera.minipay"},
		"intigriti-dropbox":     {"Public Intigriti scope snapshot verified 2026-08-15", "Tier 1 assets", "Explicit out-of-scope asset rows", "X-Intigriti-Username: <username>"},
	}
	catalogProgramsWithoutImportTargets := map[string]string{
		"hackerone-gitlab":      "account registered as yourhandle@wearehackerone.com",
		"hackerone-shopify":     "researcher-specific",
		"hackerone-uber":        "wildcard/impact based",
		"hackerone-coinbase":    "production financial infrastructure",
		"hackerone-cloudflare":  "Cloudflare customer traffic",
		"hackerone-playstation": "researcher-controlled accounts",
		"hackerone-security":    "mandatory X-Bug-Bounty header",
		"bugcrowd-openai":       "authenticated Bugcrowd",
		"bugcrowd-atlassian":    "researcher-owned test tenant",
		"bugcrowd-opera":        "production wildcard domains, CIDRs",
		"intigriti-dropbox":     "mandatory identity headers",
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
