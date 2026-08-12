/*
Copyright 2026.

SPDX-License-Identifier: AGPL-3.0-only
*/

package v1alpha1

import (
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func validSecurityProgramSpec() SecurityProgramSpec {
	return SecurityProgramSpec{
		ScanTarget: &SecurityProgramScanTarget{
			RepositoryURL: "https://github.com/acme/widget",
			WorkflowRef:   "blockchain-protocol-audit",
			PolicyPackRef: "bug-bounty",
			ScanName:      "acme-bounty",
			DisplayName:   "Acme",
			Priority:      1,
			Featured:      true,
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
		"verification required": func(s *SecurityProgramSpec) { s.VerifiedAt = metav1.Time{} },
		"repository HTTPS required": func(s *SecurityProgramSpec) {
			s.ScanTarget.RepositoryURL = "http://github.com/acme/widget"
		},
		"repository host required": func(s *SecurityProgramSpec) { s.ScanTarget.RepositoryURL = "https:///widget" },
		"repository userinfo rejected": func(s *SecurityProgramSpec) {
			s.ScanTarget.RepositoryURL = "https://user@github.com/acme/widget"
		},
		"workflow ref valid":    func(s *SecurityProgramSpec) { s.ScanTarget.WorkflowRef = "Not Valid!" },
		"policy pack ref valid": func(s *SecurityProgramSpec) { s.ScanTarget.PolicyPackRef = "Not Valid!" },
		"scan name valid":       func(s *SecurityProgramSpec) { s.ScanTarget.ScanName = "Not Valid!" },
		"target display name required": func(s *SecurityProgramSpec) {
			s.ScanTarget.DisplayName = " "
		},
		"priority nonnegative": func(s *SecurityProgramSpec) { s.ScanTarget.Priority = -1 },
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
	spec.ScanTarget = nil
	if errs := ValidateSecurityProgramSpec(spec); len(errs) != 0 {
		t.Fatalf("ValidateSecurityProgramSpec(without scan target) = %v", errs)
	}
}
