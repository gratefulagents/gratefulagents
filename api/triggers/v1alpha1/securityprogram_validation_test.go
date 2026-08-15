/*
Copyright 2026.

SPDX-License-Identifier: AGPL-3.0-only
*/

package v1alpha1

import (
	"fmt"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func validSecurityProgramSpec() SecurityProgramSpec {
	return SecurityProgramSpec{
		ScanTargets: []SecurityProgramScanTarget{
			{
				RepositoryURL: "https://github.com/acme/widget",
				BaseBranch:    "main",
				WorkflowRef:   "blockchain-protocol-audit",
				PolicyPackRef: "bug-bounty",
				ScanName:      "acme-widget",
				DisplayName:   "Acme Widget",
				Priority:      1,
				Featured:      true,
				ParameterValues: map[string]string{
					"project_root": ".",
				},
			},
			{
				TargetURL:     "https://app.acme.example",
				WorkflowRef:   "web-app-full-assessment",
				PolicyPackRef: "bug-bounty",
				ScanName:      "acme-web",
				DisplayName:   "Acme Web",
				Priority:      2,
			},
		},
		Provider:    "HackerOne",
		DisplayName: "Acme Bug Bounty",
		ProgramURL:  "https://hackerone.com/acme",
		ScopePolicy: "Only the acme/widget repository is in scope.",
		VerifiedAt:  metav1.NewTime(time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)),
	}
}

func TestValidateSecurityProgramSpec(t *testing.T) {
	if errs := ValidateSecurityProgramSpec(validSecurityProgramSpec()); len(errs) != 0 {
		t.Fatalf("ValidateSecurityProgramSpec(valid) = %v", errs)
	}
	multibyte := validSecurityProgramSpec()
	multibyte.ScopePolicy = strings.Repeat("界", MaxSecurityProgramScopePolicyLength)
	if errs := ValidateSecurityProgramSpec(multibyte); len(errs) != 0 {
		t.Fatalf("ValidateSecurityProgramSpec(multibyte boundary) = %v", errs)
	}

	for name, mutate := range map[string]func(*SecurityProgramSpec){
		"provider required":     func(s *SecurityProgramSpec) { s.Provider = " " },
		"display name required": func(s *SecurityProgramSpec) { s.DisplayName = "" },
		"https required":        func(s *SecurityProgramSpec) { s.ProgramURL = "http://example.com/program" },
		"host required":         func(s *SecurityProgramSpec) { s.ProgramURL = "https:///program" },
		"userinfo rejected":     func(s *SecurityProgramSpec) { s.ProgramURL = "https://user@example.com/program" },
		"scope required":        func(s *SecurityProgramSpec) { s.ScopePolicy = "\n\t" },
		"scope bounded": func(s *SecurityProgramSpec) {
			s.ScopePolicy = strings.Repeat("x", MaxSecurityProgramScopePolicyLength+1)
		},
		"multibyte scope bounded by characters": func(s *SecurityProgramSpec) {
			s.ScopePolicy = strings.Repeat("界", MaxSecurityProgramScopePolicyLength+1)
		},
		"verification required": func(s *SecurityProgramSpec) { s.VerifiedAt = metav1.Time{} },
		"repository HTTPS required": func(s *SecurityProgramSpec) {
			s.ScanTargets[0].RepositoryURL = "http://github.com/acme/widget"
		},
		"repository host required": func(s *SecurityProgramSpec) { s.ScanTargets[0].RepositoryURL = "https:///widget" },
		"repository userinfo rejected": func(s *SecurityProgramSpec) {
			s.ScanTargets[0].RepositoryURL = "https://user@github.com/acme/widget"
		},
		"target URL invalid":           func(s *SecurityProgramSpec) { s.ScanTargets[1].TargetURL = "ftp://app.acme.example" },
		"target URL fragment rejected": func(s *SecurityProgramSpec) { s.ScanTargets[1].TargetURL = "https://app.acme.example/#private" },
		"target URL comma rejected":    func(s *SecurityProgramSpec) { s.ScanTargets[1].TargetURL = "https://app.acme.example/a,b" },
		"bare target path rejected":    func(s *SecurityProgramSpec) { s.ScanTargets[1].TargetURL = "app.acme.example/path" },
		"hostless target rejected":     func(s *SecurityProgramSpec) { s.ScanTargets[1].TargetURL = "https://?query" },
		"target URL and repository mutually exclusive": func(s *SecurityProgramSpec) {
			s.ScanTargets[1].RepositoryURL = "https://github.com/acme/web"
		},
		"target kind required":  func(s *SecurityProgramSpec) { s.ScanTargets[1].TargetURL = "" },
		"base branch bounded":   func(s *SecurityProgramSpec) { s.ScanTargets[0].BaseBranch = strings.Repeat("b", 256) },
		"workflow ref valid":    func(s *SecurityProgramSpec) { s.ScanTargets[0].WorkflowRef = "Not Valid!" },
		"policy pack ref valid": func(s *SecurityProgramSpec) { s.ScanTargets[0].PolicyPackRef = "Not Valid!" },
		"scan name valid":       func(s *SecurityProgramSpec) { s.ScanTargets[0].ScanName = "Not Valid!" },
		"target display name required": func(s *SecurityProgramSpec) {
			s.ScanTargets[0].DisplayName = " "
		},
		"priority nonnegative": func(s *SecurityProgramSpec) { s.ScanTargets[0].Priority = -1 },
		"parameter name valid": func(s *SecurityProgramSpec) {
			s.ScanTargets[0].ParameterValues = map[string]string{"not a name": "value"}
		},
		"parameter name bounded": func(s *SecurityProgramSpec) {
			s.ScanTargets[0].ParameterValues = map[string]string{strings.Repeat("n", MaxSecurityProgramParameterName+1): "value"}
		},
		"parameter value bounded": func(s *SecurityProgramSpec) {
			s.ScanTargets[0].ParameterValues = map[string]string{"name": strings.Repeat("界", MaxSecurityProgramParameterValue+1)}
		},
		"parameter count bounded": func(s *SecurityProgramSpec) {
			s.ScanTargets[0].ParameterValues = make(map[string]string, MaxSecurityProgramTargetParameters+1)
			for index := 0; index <= MaxSecurityProgramTargetParameters; index++ {
				s.ScanTargets[0].ParameterValues[fmt.Sprintf("parameter_%d", index)] = "value"
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			spec := validSecurityProgramSpec()
			mutate(&spec)
			if errs := ValidateSecurityProgramSpec(spec); len(errs) == 0 {
				t.Fatal("ValidateSecurityProgramSpec() returned no errors")
			}
		})
	}
}

func TestValidateSecurityProgramSpecWithoutScanTarget(t *testing.T) {
	spec := validSecurityProgramSpec()
	spec.ScanTargets = nil
	spec.ScanTarget = nil
	if errs := ValidateSecurityProgramSpec(spec); len(errs) != 0 {
		t.Fatalf("ValidateSecurityProgramSpec(without scan target) = %v", errs)
	}
}

func TestSecurityProgramScanTargetDeepCopy(t *testing.T) {
	target := validSecurityProgramSpec().ScanTargets[0]
	copy := target.DeepCopy()
	copy.ParameterValues["project_root"] = "contracts"

	if got := target.ParameterValues["project_root"]; got != "." {
		t.Fatalf("original parameter value = %q, want .", got)
	}
}

func TestValidateSecurityProgramSpecLegacyScanTarget(t *testing.T) {
	spec := validSecurityProgramSpec()
	legacy := spec.ScanTargets[0]
	spec.ScanTargets = nil
	spec.ScanTarget = &legacy
	if errs := ValidateSecurityProgramSpec(spec); len(errs) != 0 {
		t.Fatalf("ValidateSecurityProgramSpec(legacy scan target) = %v", errs)
	}
	if got := spec.EffectiveScanTargets(); len(got) != 1 || got[0].ScanName != legacy.ScanName {
		t.Fatalf("EffectiveScanTargets() = %+v", got)
	}
}

func TestValidateSecurityProgramSpecRejectsAmbiguousAndDuplicateTargets(t *testing.T) {
	t.Run("both target forms", func(t *testing.T) {
		spec := validSecurityProgramSpec()
		legacy := spec.ScanTargets[0]
		spec.ScanTarget = &legacy
		if errs := ValidateSecurityProgramSpec(spec); !hasSecurityProgramFieldError(errs, "scanTargets") {
			t.Fatalf("ValidateSecurityProgramSpec() = %v, want scanTargets error", errs)
		}
	})

	t.Run("duplicate scan name", func(t *testing.T) {
		spec := validSecurityProgramSpec()
		spec.ScanTargets[1].ScanName = spec.ScanTargets[0].ScanName
		if errs := ValidateSecurityProgramSpec(spec); !hasSecurityProgramFieldError(errs, "scanTargets[1].scanName") {
			t.Fatalf("ValidateSecurityProgramSpec() = %v, want indexed duplicate error", errs)
		}
	})

	t.Run("too many targets", func(t *testing.T) {
		spec := validSecurityProgramSpec()
		target := spec.ScanTargets[0]
		spec.ScanTargets = make([]SecurityProgramScanTarget, MaxSecurityProgramScanTargets+1)
		for index := range spec.ScanTargets {
			spec.ScanTargets[index] = target
			spec.ScanTargets[index].ScanName = fmt.Sprintf("target-%d", index)
		}
		if errs := ValidateSecurityProgramSpec(spec); !hasSecurityProgramFieldError(errs, "scanTargets") {
			t.Fatalf("ValidateSecurityProgramSpec() = %v, want scanTargets bound error", errs)
		}
	})
}

func hasSecurityProgramFieldError(errs []SecurityWorkflowFieldError, field string) bool {
	for _, err := range errs {
		if err.Field == field {
			return true
		}
	}
	return false
}

func TestValidateSecurityProgramTypedScope(t *testing.T) {
	t.Parallel()

	valid := validSecurityProgramSpec()
	valid.SeveritySystem = string(SeveritySystemImmunefiV23)
	valid.Primacy = string(PrimacyImpact)
	valid.PoCRequired = true
	valid.PoCEnvironment = string(PoCEnvironmentMainnetFork)
	valid.InScopeImpacts = []SecurityProgramImpact{
		{Impact: "Direct theft of any user funds", Level: "critical", AssetType: "Smart Contract"},
		// The same clause under a different asset category is legitimate.
		{Impact: "Direct theft of any user funds", Level: "high", AssetType: "Blockchain/DLT"},
	}
	valid.OutOfScope = []string{"Attacks requiring leaked keys"}
	valid.ProhibitedTesting = []string{"Testing on mainnet or public testnet"}
	valid.Assets = []SecurityProgramAsset{
		{ChainID: "1", Address: "0xabc", DisplayName: "Vault"},
		{RepositoryURL: "https://github.com/acme/widget"},
	}
	valid.KnownIssues = []SecurityProgramKnownIssue{
		{Source: "prior audit", Summary: "Rounding in withdraw is acknowledged", Reference: "https://example.com/audit.pdf"},
	}
	valid.SubmissionBudget = &SecurityProgramSubmissionBudget{MaxPerPeriod: 2, PeriodDays: 30}
	valid.KYCRequired = true
	if errs := ValidateSecurityProgramSpec(valid); len(errs) != 0 {
		t.Fatalf("expected typed scope to validate, got %v", errs)
	}

	if level, ok := valid.ImpactLevel("Direct theft of any user funds"); !ok || level != "critical" {
		t.Fatalf("ImpactLevel() = %q, %v; want critical, true", level, ok)
	}
	if _, ok := valid.ImpactLevel("Loss of the operator's patience"); ok {
		t.Fatal("ImpactLevel() accepted an impact the program never published")
	}

	cases := []struct {
		name   string
		field  string
		mutate func(*SecurityProgramSpec)
	}{
		{
			name:   "unknown severity system",
			field:  "severitySystem",
			mutate: func(spec *SecurityProgramSpec) { spec.SeveritySystem = "cvss" },
		},
		{
			name:   "unknown primacy",
			field:  "primacy",
			mutate: func(spec *SecurityProgramSpec) { spec.Primacy = "vibes" },
		},
		{
			name:   "unknown poc environment",
			field:  "pocEnvironment",
			mutate: func(spec *SecurityProgramSpec) { spec.PoCEnvironment = "screenshot" },
		},
		{
			name:  "blank impact",
			field: "inScopeImpacts[0].impact",
			mutate: func(spec *SecurityProgramSpec) {
				spec.InScopeImpacts = []SecurityProgramImpact{{Impact: "  ", Level: "high"}}
			},
		},
		{
			name:  "duplicate impact in one asset type",
			field: "inScopeImpacts[1].impact",
			mutate: func(spec *SecurityProgramSpec) {
				spec.InScopeImpacts = []SecurityProgramImpact{
					{Impact: "Permanent freezing of funds", Level: "critical", AssetType: "Smart Contract"},
					{Impact: "Permanent freezing of funds", Level: "high", AssetType: "Smart Contract"},
				}
			},
		},
		{
			name:  "impact level outside the ladder",
			field: "inScopeImpacts[0].level",
			mutate: func(spec *SecurityProgramSpec) {
				spec.InScopeImpacts = []SecurityProgramImpact{{Impact: "Protocol insolvency", Level: "severe"}}
			},
		},
		{
			name:  "sherlock does not judge lows",
			field: "inScopeImpacts[0].level",
			mutate: func(spec *SecurityProgramSpec) {
				spec.SeveritySystem = string(SeveritySystemSherlock)
				spec.InScopeImpacts = []SecurityProgramImpact{{Impact: "Gas optimization", Level: "low"}}
			},
		},
		{
			name:  "address without a chain",
			field: "assets[0].chainID",
			mutate: func(spec *SecurityProgramSpec) {
				spec.Assets = []SecurityProgramAsset{{Address: "0xabc"}}
			},
		},
		{
			name:  "asset identifies nothing",
			field: "assets[0]",
			mutate: func(spec *SecurityProgramSpec) {
				spec.Assets = []SecurityProgramAsset{{DisplayName: "Mystery"}}
			},
		},
		{
			name:  "known issue without a source",
			field: "knownIssues[0].source",
			mutate: func(spec *SecurityProgramSpec) {
				spec.KnownIssues = []SecurityProgramKnownIssue{{Summary: "Acknowledged"}}
			},
		},
		{
			name:  "known issue reference must be https",
			field: "knownIssues[0].reference",
			mutate: func(spec *SecurityProgramSpec) {
				spec.KnownIssues = []SecurityProgramKnownIssue{{Source: "audit", Summary: "Acknowledged", Reference: "http://example.com"}}
			},
		},
		{
			name:  "budget period without a cap",
			field: "submissionBudget.maxPerPeriod",
			mutate: func(spec *SecurityProgramSpec) {
				spec.SubmissionBudget = &SecurityProgramSubmissionBudget{PeriodDays: 30}
			},
		},
		{
			name:   "blank exclusion",
			field:  "outOfScope[0]",
			mutate: func(spec *SecurityProgramSpec) { spec.OutOfScope = []string{" "} },
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			spec := valid
			spec.InScopeImpacts = nil
			spec.Assets = nil
			spec.KnownIssues = nil
			spec.OutOfScope = nil
			spec.SubmissionBudget = nil
			testCase.mutate(&spec)
			errs := ValidateSecurityProgramSpec(spec)
			var found bool
			for _, err := range errs {
				if err.Field == testCase.field {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("expected an error on %s, got %v", testCase.field, errs)
			}
		})
	}
}

func TestValidateSecurityProgramSpecWithoutTypedScope(t *testing.T) {
	t.Parallel()
	// Typed scope is optional: programs transcribed before it existed must
	// keep validating so their prose snapshot stays usable.
	if errs := ValidateSecurityProgramSpec(validSecurityProgramSpec()); len(errs) != 0 {
		t.Fatalf("expected untyped program to validate, got %v", errs)
	}
}
