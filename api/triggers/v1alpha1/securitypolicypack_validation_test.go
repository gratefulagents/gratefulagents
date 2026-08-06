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

func validPolicyPackSpec() SecurityPolicyPackSpec {
	expires := metav1.NewTime(time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC))
	return SecurityPolicyPackSpec{
		Description:            "org policy",
		RequiredCategories:     []string{"injection"},
		MinSeverity:            "low",
		FailOnSeverity:         "high",
		Dedupe:                 &SecurityScanDedupe{SimilarityThresholdPermille: 900},
		AllowedRuntimeProfiles: []string{"locked-down"},
		Enforced: []string{
			SecurityPolicyFieldMinSeverity,
			SecurityPolicyFieldFailOnSeverity,
			SecurityPolicyFieldDedupe,
			SecurityPolicyFieldRequiredCategories,
			SecurityPolicyFieldAllowedRuntimeProfiles,
			SecurityPolicyFieldBudgets,
		},
		Retention: &SecurityPolicyPackRetention{
			ScanDays:       30,
			FindingDays:    365,
			ReportDays:     90,
			EvidenceDays:   14,
			PoCDays:        7,
			AuditEventDays: 730,
		},
		Budgets: &SecurityScanBudgets{
			MaxModelJobs:      16,
			MaxCostUSD:        "2.50",
			MaxTokens:         500000,
			MaxRuntime:        metav1.Duration{Duration: 2 * time.Hour},
			MaxFindings:       200,
			MaxValidationJobs: 8,
		},
		Suppressions: []SecurityPolicySuppression{{
			Name:      "noisy-vendor",
			Reason:    "vendored code",
			Owner:     "appsec",
			Matcher:   SecuritySuppressionMatcher{PathGlob: "vendor/*"},
			ExpiresAt: &expires,
		}},
	}
}

func TestValidateSecurityPolicyPackSpecValid(t *testing.T) {
	if errs := ValidateSecurityPolicyPackSpec(validPolicyPackSpec()); len(errs) != 0 {
		t.Fatalf("ValidateSecurityPolicyPackSpec(valid) = %v, want none", errs)
	}
	if errs := ValidateSecurityPolicyPackSpec(SecurityPolicyPackSpec{}); len(errs) != 0 {
		t.Fatalf("ValidateSecurityPolicyPackSpec(empty) = %v, want none", errs)
	}
}

func TestValidateSecurityPolicyPackSpecErrors(t *testing.T) {
	tests := map[string]struct {
		mutate    func(*SecurityPolicyPackSpec)
		wantField string
	}{
		"invalid minSeverity": {
			mutate:    func(s *SecurityPolicyPackSpec) { s.MinSeverity = "urgent" },
			wantField: "minSeverity",
		},
		"invalid failOnSeverity": {
			mutate:    func(s *SecurityPolicyPackSpec) { s.FailOnSeverity = "sev1" },
			wantField: "failOnSeverity",
		},
		"dedupe threshold out of range": {
			mutate:    func(s *SecurityPolicyPackSpec) { s.Dedupe.SimilarityThresholdPermille = 2000 },
			wantField: "dedupe.similarityThresholdPermille",
		},
		"unknown enforced field": {
			mutate:    func(s *SecurityPolicyPackSpec) { s.Enforced = []string{"notAField"} },
			wantField: "enforced[0]",
		},
		"duplicate enforced field": {
			mutate: func(s *SecurityPolicyPackSpec) {
				s.Enforced = []string{SecurityPolicyFieldDedupe, SecurityPolicyFieldDedupe}
			},
			wantField: "enforced[1]",
		},
		"enforced minSeverity without value": {
			mutate: func(s *SecurityPolicyPackSpec) { s.MinSeverity = "" },
			// validPolicyPackSpec enforces minSeverity at index 0.
			wantField: "enforced[0]",
		},
		"enforced requiredCategories without values": {
			mutate:    func(s *SecurityPolicyPackSpec) { s.RequiredCategories = nil },
			wantField: "enforced[3]",
		},
		"enforced allowedRuntimeProfiles without values": {
			mutate:    func(s *SecurityPolicyPackSpec) { s.AllowedRuntimeProfiles = nil },
			wantField: "enforced[4]",
		},
		"blank required category": {
			mutate:    func(s *SecurityPolicyPackSpec) { s.RequiredCategories = []string{" "} },
			wantField: "requiredCategories[0]",
		},
		"invalid suppression name": {
			mutate:    func(s *SecurityPolicyPackSpec) { s.Suppressions[0].Name = "Not A Name!" },
			wantField: "suppressions[0].name",
		},
		"duplicate suppression name": {
			mutate: func(s *SecurityPolicyPackSpec) {
				s.Suppressions = append(s.Suppressions, s.Suppressions[0])
			},
			wantField: "suppressions[1].name",
		},
		"missing suppression reason": {
			mutate:    func(s *SecurityPolicyPackSpec) { s.Suppressions[0].Reason = " " },
			wantField: "suppressions[0].reason",
		},
		"missing suppression owner": {
			mutate:    func(s *SecurityPolicyPackSpec) { s.Suppressions[0].Owner = "" },
			wantField: "suppressions[0].owner",
		},
		"empty suppression matcher": {
			mutate: func(s *SecurityPolicyPackSpec) {
				s.Suppressions[0].Matcher = SecuritySuppressionMatcher{}
			},
			wantField: "suppressions[0].matcher",
		},
		"retention days above the bound": {
			mutate:    func(s *SecurityPolicyPackSpec) { s.Retention.FindingDays = SecurityRetentionMaxDays + 1 },
			wantField: "retention.findingDays",
		},
		"negative retention days": {
			mutate:    func(s *SecurityPolicyPackSpec) { s.Retention.PoCDays = -1 },
			wantField: "retention.pocDays",
		},
		"invalid budget cost": {
			mutate:    func(s *SecurityPolicyPackSpec) { s.Budgets.MaxCostUSD = "$5" },
			wantField: "budgets.maxCostUSD",
		},
		"negative budget tokens": {
			mutate:    func(s *SecurityPolicyPackSpec) { s.Budgets.MaxTokens = -1 },
			wantField: "budgets.maxTokens",
		},
		"negative budget runtime": {
			mutate:    func(s *SecurityPolicyPackSpec) { s.Budgets.MaxRuntime = metav1.Duration{Duration: -time.Minute} },
			wantField: "budgets.maxRuntime",
		},
		"enforced budgets without values": {
			mutate:    func(s *SecurityPolicyPackSpec) { s.Budgets = nil },
			wantField: "enforced[5]",
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			spec := validPolicyPackSpec()
			tt.mutate(&spec)
			errs := ValidateSecurityPolicyPackSpec(spec)
			if len(errs) == 0 {
				t.Fatal("ValidateSecurityPolicyPackSpec() = no errors, want at least one")
			}
			found := false
			for _, err := range errs {
				if err.Field == tt.wantField {
					found = true
				}
			}
			if !found {
				var got []string
				for _, err := range errs {
					got = append(got, err.Error())
				}
				t.Fatalf("errors = %v, want one addressed to %q", strings.Join(got, "; "), tt.wantField)
			}
		})
	}
}
