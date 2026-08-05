package security

import (
	"strings"
	"testing"
)

func TestSimilarity(t *testing.T) {
	tests := []struct {
		name     string
		a, b     Finding
		min, max float64
	}{
		{
			name: "identical text",
			a:    Finding{Title: "SQL injection in search handler", Description: "user input flows into query"},
			b:    Finding{Title: "SQL injection in search handler", Description: "user input flows into query"},
			min:  0.99, max: 1.0,
		},
		{
			name: "unrelated",
			a:    Finding{Title: "SQL injection in search", Description: "query built from raw input"},
			b:    Finding{Title: "weak password hashing", Description: "md5 used for credential storage"},
			min:  0.0, max: 0.1,
		},
		{
			name: "same file overlapping lines boosts",
			a:    Finding{Title: "SQL injection search", FilePath: "api/search.go", StartLine: 10, EndLine: 20},
			b:    Finding{Title: "injection issue search query", FilePath: "api/search.go", StartLine: 15, EndLine: 25},
			min:  0.15, max: 1.0,
		},
		{
			name: "shared cwe boosts",
			a:    Finding{Title: "alpha beta", CWE: []string{"CWE-89"}},
			b:    Finding{Title: "gamma delta", CWE: []string{"cwe-89"}},
			min:  0.1, max: 0.1,
		},
		{
			name: "both empty",
			a:    Finding{},
			b:    Finding{},
			min:  0.0, max: 0.0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Similarity(tt.a, tt.b)
			if got < tt.min || got > tt.max {
				t.Errorf("Similarity = %v, want in [%v, %v]", got, tt.min, tt.max)
			}
			if back := Similarity(tt.b, tt.a); back != got {
				t.Errorf("Similarity not symmetric: %v vs %v", got, back)
			}
		})
	}
}

func TestSimilarityBoostRequiresOverlap(t *testing.T) {
	a := Finding{Title: "alpha beta", FilePath: "api/x.go", StartLine: 10, EndLine: 12}
	b := Finding{Title: "gamma delta", FilePath: "api/x.go", StartLine: 100, EndLine: 110}
	if got := Similarity(a, b); got != 0 {
		t.Errorf("no line overlap should not boost, got %v", got)
	}
}

func TestDedupeExactFingerprintMerge(t *testing.T) {
	a := Finding{Title: "SQL injection in search", Category: "injection", Severity: "high", Confidence: "firm", FilePath: "api/search.go", Description: "long description with much detail about the query"}
	b := Finding{Title: "search: SQL Injection", Category: "injection", Severity: "medium", Confidence: "tentative", FilePath: "./api/search.go", Description: "short"}
	// Same fingerprint (title token set, category, path all match) but text
	// differs enough that similarity alone would not merge.
	if Fingerprint(a) != Fingerprint(b) {
		t.Fatalf("test setup: fingerprints differ: %q vs %q", Fingerprint(a), Fingerprint(b))
	}
	clusters := Dedupe([]Finding{b, a}, 0.999)
	if len(clusters) != 1 {
		t.Fatalf("clusters = %d, want 1", len(clusters))
	}
	c := clusters[0]
	if c.Canonical.Severity != "high" {
		t.Errorf("canonical severity = %q, want high (highest severity wins)", c.Canonical.Severity)
	}
	if len(c.Duplicates) != 1 || c.Duplicates[0].Severity != "medium" {
		t.Errorf("duplicates = %+v, want the medium finding", c.Duplicates)
	}
	if !strings.Contains(c.Reason, "fingerprint") {
		t.Errorf("Reason = %q, want mention of fingerprint", c.Reason)
	}
}

func TestDedupeSimilarityMerge(t *testing.T) {
	a := Finding{Title: "SQL injection in user search handler", Description: "raw user input concatenated into SQL query string", Category: "injection", Severity: "high", Confidence: "confirmed", FilePath: "api/search.go"}
	b := Finding{Title: "SQL injection in user search handler code", Description: "raw user input concatenated into SQL query string builder", Category: "injection", Severity: "medium", Confidence: "firm", FilePath: "api/other.go"}
	other := Finding{Title: "hardcoded AWS credentials", Description: "static access key committed to the repository", Category: "secrets", Severity: "critical", Confidence: "confirmed"}

	clusters := Dedupe([]Finding{b, other, a}, 0)
	if len(clusters) != 2 {
		t.Fatalf("clusters = %d, want 2: %+v", len(clusters), clusters)
	}
	// Deterministic order: severity desc -> secrets(critical) first.
	if clusters[0].Canonical.Category != "secrets" {
		t.Errorf("clusters[0] = %q, want secrets first (severity desc)", clusters[0].Canonical.Category)
	}
	if clusters[0].Reason != "no duplicates" {
		t.Errorf("singleton Reason = %q, want %q", clusters[0].Reason, "no duplicates")
	}
	inj := clusters[1]
	if inj.Canonical.Severity != "high" || len(inj.Duplicates) != 1 {
		t.Errorf("injection cluster canonical=%q duplicates=%d, want high canonical with 1 duplicate", inj.Canonical.Severity, len(inj.Duplicates))
	}
	if !strings.Contains(inj.Reason, "similarity >= 0.82") {
		t.Errorf("Reason = %q, want default threshold 0.82 mentioned", inj.Reason)
	}
}

func TestDedupeCanonicalSelection(t *testing.T) {
	mk := func(sev, conf string, evidence int, desc string) Finding {
		f := Finding{Title: "same exact title tokens", Category: "other", Severity: sev, Confidence: conf, Description: desc}
		for i := 0; i < evidence; i++ {
			f.Evidence = append(f.Evidence, Evidence{Snippet: "x"})
		}
		return f
	}
	tests := []struct {
		name string
		in   []Finding
		want Finding
	}{
		{"highest severity wins", []Finding{mk("low", "confirmed", 3, "long description"), mk("high", "tentative", 0, "s")}, mk("high", "tentative", 0, "s")},
		{"confidence breaks severity tie", []Finding{mk("high", "tentative", 3, "x"), mk("high", "confirmed", 0, "x")}, mk("high", "confirmed", 0, "x")},
		{"evidence breaks confidence tie", []Finding{mk("high", "firm", 1, "x"), mk("high", "firm", 2, "x")}, mk("high", "firm", 2, "x")},
		{"description length breaks evidence tie", []Finding{mk("high", "firm", 1, "short"), mk("high", "firm", 1, "much longer description")}, mk("high", "firm", 1, "much longer description")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clusters := Dedupe(tt.in, 0)
			if len(clusters) != 1 {
				t.Fatalf("clusters = %d, want 1", len(clusters))
			}
			got := clusters[0].Canonical
			if got.Severity != tt.want.Severity || got.Confidence != tt.want.Confidence ||
				len(got.Evidence) != len(tt.want.Evidence) || got.Description != tt.want.Description {
				t.Errorf("canonical = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestDedupeEmpty(t *testing.T) {
	if got := Dedupe(nil, 0); len(got) != 0 {
		t.Errorf("Dedupe(nil) = %v, want empty", got)
	}
}
