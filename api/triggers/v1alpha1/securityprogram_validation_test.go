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
