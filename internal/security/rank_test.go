package security

import (
	"strings"
	"testing"
)

func TestParseRankRules(t *testing.T) {
	text := `We care most about pre-auth issues in the public API.

severity-floor: secrets=high
severity-ceiling: dos = medium
exclude: info-leak, misconfiguration
min-severity: low
weight: exploitability=0.3
weight: severity=0.4, exposure=0.1

Please be conservative about test-only code.
severity-floor: not-a-category=high`

	rules := ParseRankRules(text)

	if got := rules.SeverityFloors["secrets"]; got != "high" {
		t.Errorf("SeverityFloors[secrets] = %q, want high", got)
	}
	if got := rules.SeverityCeilings["dos"]; got != "medium" {
		t.Errorf("SeverityCeilings[dos] = %q, want medium", got)
	}
	if !equalStrings(rules.ExcludeCategories, []string{"info-leak", "misconfiguration"}) {
		t.Errorf("ExcludeCategories = %v", rules.ExcludeCategories)
	}
	if rules.MinSeverity != "low" {
		t.Errorf("MinSeverity = %q, want low", rules.MinSeverity)
	}
	w := rules.Weights
	if w.Exploitability != 0.3 || w.Severity != 0.4 || w.Exposure != 0.1 {
		t.Errorf("Weights = %+v, want exploitability=0.3 severity=0.4 exposure=0.1", w)
	}
	if w.Confidence != DefaultRankWeights().Confidence {
		t.Errorf("Confidence weight = %v, want default %v", w.Confidence, DefaultRankWeights().Confidence)
	}
	for _, prose := range []string{"pre-auth issues in the public API", "conservative about test-only code", "severity-floor: not-a-category=high"} {
		if !strings.Contains(rules.Text, prose) {
			t.Errorf("Text %q missing prose %q", rules.Text, prose)
		}
	}
	for _, directive := range []string{"severity-floor: secrets", "min-severity: low", "weight:", "exclude:"} {
		if strings.Contains(rules.Text, directive) {
			t.Errorf("Text retained parsed directive %q: %q", directive, rules.Text)
		}
	}
}

func TestParseRankRulesEmpty(t *testing.T) {
	rules := ParseRankRules("")
	if rules.Weights != DefaultRankWeights() {
		t.Errorf("Weights = %+v, want defaults", rules.Weights)
	}
	if rules.Text != "" || rules.MinSeverity != "" || len(rules.ExcludeCategories) != 0 {
		t.Errorf("unexpected non-zero rules: %+v", rules)
	}
}

func TestRankFloorsCeilingsExcludesMinSeverity(t *testing.T) {
	findings := []Finding{
		{Title: "leaked key", Category: "secrets", Severity: "low", Confidence: "confirmed", Description: "d"},
		{Title: "slowloris", Category: "dos", Severity: "critical", Confidence: "firm", Description: "d"},
		{Title: "noise", Category: "info-leak", Severity: "high", Confidence: "confirmed", Description: "d"},
		{Title: "trivial", Category: "xss", Severity: "info", Confidence: "tentative", Description: "d"},
	}
	rules := RankRules{
		SeverityFloors:    map[string]string{"secrets": "high"},
		SeverityCeilings:  map[string]string{"dos": "medium"},
		ExcludeCategories: []string{"info-leak"},
		MinSeverity:       "low",
	}
	ranked := Rank(findings, rules)

	byTitle := map[string]RankedFinding{}
	for _, r := range ranked {
		byTitle[r.Finding.Title] = r
	}
	if len(ranked) != 2 {
		t.Fatalf("ranked = %d findings (%v), want 2 (info-leak excluded, info dropped by min-severity)", len(ranked), byTitle)
	}
	if _, ok := byTitle["noise"]; ok {
		t.Error("excluded category info-leak still present")
	}
	if _, ok := byTitle["trivial"]; ok {
		t.Error("finding below min-severity still present")
	}
	leak := byTitle["leaked key"]
	if leak.Finding.Severity != "high" {
		t.Errorf("floored severity = %q, want high", leak.Finding.Severity)
	}
	if !containsSubstring(leak.Reasons, "raised from low to high") {
		t.Errorf("Reasons = %v, want floor reason", leak.Reasons)
	}
	dos := byTitle["slowloris"]
	if dos.Finding.Severity != "medium" {
		t.Errorf("ceilinged severity = %q, want medium", dos.Finding.Severity)
	}
	if !containsSubstring(dos.Reasons, "lowered from critical to medium") {
		t.Errorf("Reasons = %v, want ceiling reason", dos.Reasons)
	}
}

func TestRankMinSeverityAppliesAfterFloor(t *testing.T) {
	findings := []Finding{{Title: "t", Category: "secrets", Severity: "info", Confidence: "firm", Description: "d"}}
	rules := RankRules{SeverityFloors: map[string]string{"secrets": "high"}, MinSeverity: "high"}
	ranked := Rank(findings, rules)
	if len(ranked) != 1 {
		t.Fatalf("ranked = %d, want 1 (floor lifts finding above min-severity)", len(ranked))
	}
}

func TestRankScoringAndOrdering(t *testing.T) {
	critical := Finding{Title: "b critical remote rce", Category: "injection", Severity: "critical", Confidence: "confirmed",
		AttackVector: "remote unauthenticated attacker sends crafted request, leading to RCE",
		FilePath:     "internal/api/handler.go", Tags: []string{"remote", "pre-auth"}, Description: "d"}
	lowTest := Finding{Title: "a low local", Category: "xss", Severity: "low", Confidence: "tentative",
		AttackVector: "local user with shell access", FilePath: "internal/foo/foo_test.go", Description: "d"}

	ranked := Rank([]Finding{lowTest, critical}, RankRules{})
	if len(ranked) != 2 {
		t.Fatalf("ranked = %d, want 2", len(ranked))
	}
	if ranked[0].Finding.Title != critical.Title {
		t.Errorf("first = %q, want the critical remote finding", ranked[0].Finding.Title)
	}
	if ranked[0].Score <= ranked[1].Score {
		t.Errorf("scores not descending: %v then %v", ranked[0].Score, ranked[1].Score)
	}
	for _, r := range ranked {
		if r.Score < 0 || r.Score > 100 {
			t.Errorf("score %v out of [0,100]", r.Score)
		}
	}
	if !containsSubstring(ranked[0].Reasons, "exploitability boosted by") {
		t.Errorf("Reasons = %v, want exploitability boost", ranked[0].Reasons)
	}
	if !containsSubstring(ranked[0].Reasons, "high exposure") {
		t.Errorf("Reasons = %v, want high exposure for handler path", ranked[0].Reasons)
	}
	if !containsSubstring(ranked[1].Reasons, "low exposure") {
		t.Errorf("Reasons = %v, want low exposure for test path", ranked[1].Reasons)
	}
}

func TestRankTieBreaksByTitle(t *testing.T) {
	a := Finding{Title: "aaa", Category: "other", Severity: "medium", Confidence: "firm", Description: "d"}
	b := Finding{Title: "bbb", Category: "other", Severity: "medium", Confidence: "firm", Description: "d"}
	ranked := Rank([]Finding{b, a}, RankRules{})
	if len(ranked) != 2 || ranked[0].Finding.Title != "aaa" {
		t.Errorf("tie not broken by title asc: %+v", ranked)
	}
}

func TestRankEmptyAndZeroRules(t *testing.T) {
	if got := Rank(nil, RankRules{}); len(got) != 0 {
		t.Errorf("Rank(nil) = %v, want empty", got)
	}
	ranked := Rank([]Finding{{Title: "t", Category: "other", Severity: "high", Description: "d"}}, RankRules{})
	if len(ranked) != 1 || ranked[0].Score <= 0 {
		t.Errorf("zero-value rules should use default weights, got %+v", ranked)
	}
}

func containsSubstring(list []string, sub string) bool {
	for _, s := range list {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
