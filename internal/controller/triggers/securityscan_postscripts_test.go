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
	"github.com/gratefulagents/gratefulagents/internal/store"
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

func TestSecurityScanPostScriptMatchesMediumAndAboveActionable(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, runOn, severity, floor string
		want                         bool
	}{
		{name: "medium matches the medium floor", runOn: "medium-and-above-actionable", severity: "medium", floor: "medium", want: true},
		{name: "high matches the medium floor", runOn: "medium-and-above-actionable", severity: "high", floor: "medium", want: true},
		{name: "low stays below the medium floor", runOn: "medium-and-above-actionable", severity: "low", floor: "medium"},
		{name: "low matches the low floor", runOn: "low-and-above-actionable", severity: "low", floor: "low", want: true},
		{name: "info stays below the low floor", runOn: "low-and-above-actionable", severity: "info", floor: "low"},
		{name: "low is not dispatched under a medium-paying program", runOn: "low-and-above-actionable", severity: "low", floor: "medium"},
		{name: "medium stays below the high floor", runOn: "high-and-above-actionable", severity: "medium", floor: "high"},
		// A program that publishes no medium impacts does not pay for them, so
		// the two expensive PoC stages must not be dispatched at all: the
		// bundle gate would refuse the result afterwards anyway.
		{name: "medium is not dispatched under a high-paying program", runOn: "medium-and-above-actionable", severity: "medium", floor: "high"},
		{name: "high is still dispatched under a high-paying program", runOn: "medium-and-above-actionable", severity: "high", floor: "high", want: true},
		// Without a governing program the platform defaults to medium.
		{name: "medium is dispatched without a governing program", runOn: "medium-and-above-actionable", severity: "medium", floor: securityProgramPayableFloor(nil), want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rec := store.SecurityFindingRecord{Severity: tc.severity, Status: store.SecurityFindingStatusTriaged}
			got, reason := securityScanPostScriptMatches(tc.runOn, rec, tc.floor)
			if got != tc.want {
				t.Fatalf("securityScanPostScriptMatches(%q, %q) = %v (%s), want %v", tc.runOn, tc.severity, got, reason, tc.want)
			}
			if !got && !strings.Contains(reason, tc.severity) {
				t.Fatalf("skip reason %q does not name the severity %q", reason, tc.severity)
			}
		})
	}
}

func TestSecurityScanPostScriptsActionableOnlyCoversSeverityVariants(t *testing.T) {
	t.Parallel()
	for runOn, want := range map[string]bool{
		"medium-and-above-actionable": true,
		"low-and-above-actionable":    true,
		"high-and-above-actionable":   true,
		"high-and-above":              false,
		"all":                         false,
	} {
		scripts := []triggersv1alpha1.SecurityScanPostScript{{Name: "poc-builder", Prompt: "p", RunOn: runOn}}
		if got := securityScanPostScriptsActionableOnly(scripts); got != want {
			t.Fatalf("securityScanPostScriptsActionableOnly(%q) = %v, want %v", runOn, got, want)
		}
	}
}

// The dispatch floor and the packaging floor have to agree, or a stage runs and
// produces a result the bundle gate then refuses.
func TestSecurityProgramPayableFloorFollowsPublishedLevels(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		levels []string
		want   string
	}{
		"publishes mediums":       {levels: []string{"critical", "high", "medium"}, want: "medium"},
		"publishes critical only": {levels: []string{"critical"}, want: "medium"},
		"publishes only high up":  {levels: []string{"critical", "high"}, want: "high"},
		"publishes lows":          {levels: []string{"high", "low"}, want: "low"},
		"unreadable levels only":  {levels: []string{"", "spicy"}, want: "medium"},
		"no published impacts":    {levels: nil, want: "medium"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			program := &triggersv1alpha1.SecurityProgramSpec{}
			for i, level := range tc.levels {
				program.InScopeImpacts = append(program.InScopeImpacts, triggersv1alpha1.SecurityProgramImpact{
					Impact: fmt.Sprintf("clause-%d", i), Level: level,
				})
			}
			if got := securityProgramPayableFloor(program); got != tc.want {
				t.Fatalf("securityProgramPayableFloor(%v) = %q, want %q", tc.levels, got, tc.want)
			}
		})
	}
	if got := securityProgramPayableFloor(nil); got != "medium" {
		t.Fatalf("a nil program floor = %q, want medium", got)
	}
}
