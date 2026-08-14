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
			},
			{
				RepositoryURL: "https://github.com/acme/contracts",
				BaseBranch:    "develop",
				WorkflowRef:   "smart-contract-review",
				PolicyPackRef: "bug-bounty",
				ScanName:      "acme-contracts",
				DisplayName:   "Acme Contracts",
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
		"base branch bounded":   func(s *SecurityProgramSpec) { s.ScanTargets[0].BaseBranch = strings.Repeat("b", 256) },
		"workflow ref valid":    func(s *SecurityProgramSpec) { s.ScanTargets[0].WorkflowRef = "Not Valid!" },
		"policy pack ref valid": func(s *SecurityProgramSpec) { s.ScanTargets[0].PolicyPackRef = "Not Valid!" },
		"scan name valid":       func(s *SecurityProgramSpec) { s.ScanTargets[0].ScanName = "Not Valid!" },
		"target display name required": func(s *SecurityProgramSpec) {
			s.ScanTargets[0].DisplayName = " "
		},
		"priority nonnegative": func(s *SecurityProgramSpec) { s.ScanTargets[0].Priority = -1 },
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
