package security

import (
	"strings"
	"testing"
)

func TestFingerprintStability(t *testing.T) {
	base := Finding{
		Title:      "SQL injection in user search",
		Category:   "injection",
		Repository: "github.com/acme/app",
		FilePath:   "internal/api/search.go",
		CWE:        []string{"CWE-89"},
	}
	want := Fingerprint(base)
	if len(want) != 16 {
		t.Fatalf("fingerprint length = %d, want 16 (%q)", len(want), want)
	}

	tests := []struct {
		name string
		f    Finding
		same bool
	}{
		{"identical", base, true},
		{"case insensitive title", func() Finding { f := base; f.Title = "SQL INJECTION IN User Search"; return f }(), true},
		{"whitespace and punctuation insensitive", func() Finding { f := base; f.Title = "  SQL  injection,   in user-search!  "; return f }(), true},
		{"word order insensitive", func() Finding { f := base; f.Title = "user search: SQL injection"; return f }(), true},
		{"stopwords ignored", func() Finding { f := base; f.Title = "The SQL injection in the user search"; return f }(), true},
		{"duplicate tokens ignored", func() Finding { f := base; f.Title = "SQL SQL injection user user search"; return f }(), true},
		{"file path ./ prefix insensitive", func() Finding { f := base; f.FilePath = "./internal/api/search.go"; return f }(), true},
		{"cwe format insensitive", func() Finding { f := base; f.CWE = []string{"cwe-89"}; return f }(), true},
		{"different title", func() Finding { f := base; f.Title = "XSS in user search"; return f }(), false},
		{"different file", func() Finding { f := base; f.FilePath = "internal/api/list.go"; return f }(), false},
		{"different category", func() Finding { f := base; f.Category = "xss"; return f }(), false},
		{"different cwe", func() Finding { f := base; f.CWE = []string{"CWE-79"}; return f }(), false},
		{"different repository", func() Finding { f := base; f.Repository = "github.com/acme/other"; return f }(), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Fingerprint(tt.f)
			if (got == want) != tt.same {
				t.Errorf("Fingerprint = %q, base = %q, want same=%v", got, want, tt.same)
			}
		})
	}
}

func TestFingerprintEmptyFinding(t *testing.T) {
	got := Fingerprint(Finding{})
	if len(got) != 16 {
		t.Fatalf("Fingerprint(zero) = %q, want 16 hex chars", got)
	}
}

func TestNormalizeAliasMapping(t *testing.T) {
	tests := []struct {
		name                               string
		in                                 Finding
		severity, confidence, category, fp string
	}{
		{
			name:     "canonical values unchanged",
			in:       Finding{Severity: "critical", Confidence: "confirmed", Category: "injection"},
			severity: SeverityCritical, confidence: ConfidenceConfirmed, category: "injection",
		},
		{
			name:     "uppercase and aliases",
			in:       Finding{Severity: " CRIT ", Confidence: "Certain", Category: "SQL Injection"},
			severity: SeverityCritical, confidence: ConfidenceConfirmed, category: "injection",
		},
		{
			name:     "moderate maps to medium, high confidence to firm",
			in:       Finding{Severity: "Moderate", Confidence: "high", Category: "directory traversal"},
			severity: SeverityMedium, confidence: ConfidenceFirm, category: "path-traversal",
		},
		{
			name:     "informational and low confidence",
			in:       Finding{Severity: "informational", Confidence: "low", Category: "information-disclosure"},
			severity: SeverityInfo, confidence: ConfidenceTentative, category: "info-leak",
		},
		{
			name:     "empty confidence defaults to tentative",
			in:       Finding{Severity: "high", Category: "authz"},
			severity: SeverityHigh, confidence: ConfidenceTentative, category: "authz",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := tt.in
			f.Normalize()
			if f.Severity != tt.severity {
				t.Errorf("Severity = %q, want %q", f.Severity, tt.severity)
			}
			if f.Confidence != tt.confidence {
				t.Errorf("Confidence = %q, want %q", f.Confidence, tt.confidence)
			}
			if f.Category != tt.category {
				t.Errorf("Category = %q, want %q", f.Category, tt.category)
			}
			if f.Fingerprint == "" || len(f.Fingerprint) != 16 {
				t.Errorf("Fingerprint = %q, want 16 hex chars", f.Fingerprint)
			}
		})
	}
}

func TestNormalizeClampsAndCleans(t *testing.T) {
	f := Finding{
		Title:        "  " + strings.Repeat("t", 400) + "  ",
		Description:  strings.Repeat("d", 9000),
		Impact:       strings.Repeat("i", 9000),
		AttackVector: strings.Repeat("a", 9000),
		Remediation:  strings.Repeat("r", 9000),
		Severity:     "high",
		Category:     "xss",
		Repository:   "github.com/acme/app.git",
		FilePath:     ".\\app\\web\\render.go",
		StartLine:    -3,
		EndLine:      -1,
		CWE:          []string{"79", "CWE-79", "cwe-89", ""},
		Tags:         []string{"Remote", "remote", " RCE ", ""},
		References:   []string{"https://b", "https://a", "https://b", " "},
		Evidence: []Evidence{{
			FilePath:  "./app/web/render.go",
			StartLine: -1,
			EndLine:   -2,
			Snippet:   strings.Repeat("s", 5000),
			Note:      "  note  ",
		}},
	}
	f.Normalize()

	if len(f.Title) != maxTitleLen {
		t.Errorf("Title len = %d, want %d", len(f.Title), maxTitleLen)
	}
	for name, s := range map[string]string{"Description": f.Description, "Impact": f.Impact, "AttackVector": f.AttackVector, "Remediation": f.Remediation} {
		if len(s) != maxTextLen {
			t.Errorf("%s len = %d, want %d", name, len(s), maxTextLen)
		}
	}
	if f.FilePath != "web/render.go" {
		t.Errorf("FilePath = %q, want %q (backslashes converted, ./ and repo prefix stripped)", f.FilePath, "web/render.go")
	}
	if f.StartLine != 0 || f.EndLine != 0 {
		t.Errorf("lines = %d,%d, want clamped to 0,0", f.StartLine, f.EndLine)
	}
	wantCWE := []string{"CWE-79", "CWE-89"}
	if !equalStrings(f.CWE, wantCWE) {
		t.Errorf("CWE = %v, want %v", f.CWE, wantCWE)
	}
	if !equalStrings(f.Tags, []string{"rce", "remote"}) {
		t.Errorf("Tags = %v, want [rce remote]", f.Tags)
	}
	if !equalStrings(f.References, []string{"https://a", "https://b"}) {
		t.Errorf("References = %v, want [https://a https://b]", f.References)
	}
	e := f.Evidence[0]
	if e.FilePath != "web/render.go" {
		t.Errorf("Evidence.FilePath = %q, want web/render.go", e.FilePath)
	}
	if e.StartLine != 0 || e.EndLine != 0 {
		t.Errorf("evidence lines = %d,%d, want 0,0", e.StartLine, e.EndLine)
	}
	if len(e.Snippet) != maxSnippetLen {
		t.Errorf("Snippet len = %d, want %d", len(e.Snippet), maxSnippetLen)
	}
	if e.Note != "note" {
		t.Errorf("Note = %q, want %q", e.Note, "note")
	}
}

func TestNormalizeNil(t *testing.T) {
	var f *Finding
	f.Normalize() // must not panic
}

func TestValidate(t *testing.T) {
	valid := Finding{Title: "t", Description: "d", Category: "injection", Severity: "high"}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid finding: unexpected error %v", err)
	}

	tests := []struct {
		name string
		f    Finding
		want []string
	}{
		{"empty finding", Finding{}, []string{"title is required", "description is required", "category is required", "severity is required"}},
		{"unknown category", Finding{Title: "t", Description: "d", Category: "nonsense", Severity: "high"}, []string{`unknown category "nonsense"`}},
		{"unknown severity", Finding{Title: "t", Description: "d", Category: "injection", Severity: "gigantic"}, []string{`unknown severity "gigantic"`}},
		{"unknown confidence", Finding{Title: "t", Description: "d", Category: "injection", Severity: "high", Confidence: "sure"}, []string{`unknown confidence "sure"`}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.f.Validate()
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			for _, want := range tt.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q missing %q", err.Error(), want)
				}
			}
		})
	}
}

func TestSeverityRank(t *testing.T) {
	tests := []struct {
		s    string
		want int
	}{
		{SeverityCritical, 4}, {SeverityHigh, 3}, {SeverityMedium, 2}, {SeverityLow, 1}, {SeverityInfo, 0},
		{"Moderate", 2}, {"CRIT", 4},
		{"", -1}, {"bogus", -1},
	}
	for _, tt := range tests {
		if got := SeverityRank(tt.s); got != tt.want {
			t.Errorf("SeverityRank(%q) = %d, want %d", tt.s, got, tt.want)
		}
	}
}

func TestSeverityAtLeast(t *testing.T) {
	tests := []struct {
		s, min string
		want   bool
	}{
		{"critical", "high", true},
		{"high", "high", true},
		{"medium", "high", false},
		{"info", "low", false},
		{"low", "", true},
		{"bogus", "low", false},
		{"", "", false},
	}
	for _, tt := range tests {
		if got := SeverityAtLeast(tt.s, tt.min); got != tt.want {
			t.Errorf("SeverityAtLeast(%q, %q) = %v, want %v", tt.s, tt.min, got, tt.want)
		}
	}
}

func TestSummarize(t *testing.T) {
	got := Summarize(nil)
	for _, s := range Severities {
		if got[s] != 0 {
			t.Errorf("Summarize(nil)[%q] = %d, want 0", s, got[s])
		}
	}
	if got["total"] != 0 {
		t.Errorf("total = %d, want 0", got["total"])
	}

	findings := []Finding{
		{Severity: "critical"}, {Severity: "critical"}, {Severity: "high"}, {Severity: "info"},
	}
	got = Summarize(findings)
	want := map[string]int{"critical": 2, "high": 1, "medium": 0, "low": 0, "info": 1, "total": 4}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("Summarize[%q] = %d, want %d", k, got[k], v)
		}
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
