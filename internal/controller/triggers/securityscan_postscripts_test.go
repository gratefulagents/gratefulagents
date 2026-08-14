/*
Copyright 2026.

SPDX-License-Identifier: AGPL-3.0-only
*/

package triggers

import (
	"fmt"
	"strings"
	"testing"

	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
)

func TestSecurityProgramScopeAnnotationsMarkTruncation(t *testing.T) {
	t.Parallel()
	program := &triggersv1alpha1.SecurityProgramSpec{
		SeveritySystem: string(triggersv1alpha1.SeveritySystemImmunefiV23),
		SubmissionBudget: &triggersv1alpha1.SecurityProgramSubmissionBudget{
			MaxPerPeriod: 2, PeriodDays: 30,
		},
		InScopeImpacts: []triggersv1alpha1.SecurityProgramImpact{
			{Impact: "Permanent freezing of funds", Level: "critical"},
			{Impact: "Theft of unclaimed yield", Level: "high"},
		},
	}
	annotations := securityProgramScopeAnnotations(program)
	if got := annotations[triggersv1alpha1.SecurityScanProgramSeveritySystemAnnotation]; got != "immunefi-v2.3" {
		t.Errorf("severity system annotation = %q", got)
	}
	if got := annotations[triggersv1alpha1.SecurityScanProgramSubmissionBudgetAnnotation]; got != "2" {
		t.Errorf("budget annotation = %q", got)
	}
	if got := annotations[triggersv1alpha1.SecurityScanProgramSubmissionPeriodAnnotation]; got != "30" {
		t.Errorf("budget period annotation = %q", got)
	}
	if got := annotations[triggersv1alpha1.SecurityScanProgramImpactsAnnotation]; got != "critical\tPermanent freezing of funds\nhigh\tTheft of unclaimed yield\n" {
		t.Errorf("impacts annotation = %q", got)
	}
	if _, truncated := annotations[triggersv1alpha1.SecurityScanProgramImpactsTruncatedAnnotation]; truncated {
		t.Error("a complete impact list must not be marked truncated")
	}

	// A clause that cannot be encoded, or that does not fit, leaves the list
	// incomplete: it must be marked so consumers stop treating it as an
	// authoritative allowlist.
	long := strings.Repeat("x", 1024)
	for i := range 32 {
		program.InScopeImpacts = append(program.InScopeImpacts, triggersv1alpha1.SecurityProgramImpact{
			Impact: fmt.Sprintf("%s-%d", long, i), Level: "high",
		})
	}
	annotations = securityProgramScopeAnnotations(program)
	if annotations[triggersv1alpha1.SecurityScanProgramImpactsTruncatedAnnotation] != "true" {
		t.Error("an oversized impact list must be marked truncated")
	}
	if len(annotations[triggersv1alpha1.SecurityScanProgramImpactsAnnotation]) > triggersv1alpha1.MaxSecurityScanProgramImpactsAnnotationBytes {
		t.Error("the impacts annotation exceeded its bound")
	}
}
