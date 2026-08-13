package configtest

import (
	"bytes"
	"os"
	"path/filepath"
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
		"solana-agave": "https://github.com/anza-xyz/agave/security",
		"firedancer":   "https://bounty.firedancer.io/",
	}
	expectedVerbatimMarkers := map[string][]string{
		"solana-agave": {"## Reporting security problems in the Agave Validator", "### Out of Scope:", "### Payment of Bug Bounties:"},
		"firedancer":   {"Version effective: 2026-08-06", "Scope\nAny reachable code in the firedancer/fdctl", "Submission and Conduct"},
	}
	expectedImmunefiTargets := map[string]string{
		"immunefi-1inch":  "https://github.com/1inch/limit-order-protocol",
		"immunefi-aave":   "https://github.com/aave-dao/aave-v3-origin",
		"immunefi-jito":   "https://github.com/jito-foundation/jito-solana",
		"immunefi-kamino": "https://github.com/Kamino-Finance/klend",
		"immunefi-lido":   "https://github.com/lidofinance/core",
		"immunefi-sei":    "https://github.com/sei-protocol/sei-chain",
	}
	// Active programs use complete captured scopes. Programs no longer present
	// in Immunefi's live catalog retain an explicitly archived summary.
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
			if program.Name == "firedancer" && program.Spec.ScanTarget != nil {
				t.Error("Firedancer must not suggest a scan target without a selected in-scope release")
			}
			if program.Name == "immunefi-ethena" || program.Name == "immunefi-euler" {
				if program.Spec.ScanTarget != nil {
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
				if program.Spec.ScanTarget == nil {
					t.Fatalf("active program with an in-scope repository has no scan target")
				}
				if got := program.Spec.ScanTarget.RepositoryURL; got != want {
					t.Errorf("scan target repository = %q, want %q", got, want)
				}
			}
			isImmunefi := program.Spec.Provider == "Immunefi"
			if isImmunefi && !strings.HasPrefix(program.Spec.ProgramURL, "https://immunefi.com/bug-bounty/") {
				t.Fatalf("unexpected Immunefi provenance URL: %q", program.Spec.ProgramURL)
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
				for _, marker := range []string{"Archived last-known scope summary", "Repository targets:", "Rewards:", "Eligible impacts:", "Out of scope:", "Testing and submission:"} {
					if !strings.Contains(program.Spec.ScopePolicy, marker) {
						t.Errorf("scopePolicy missing %q", marker)
					}
				}
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
