package security

import (
	"strings"
	"testing"
	"time"
)

func testReportInput() ReportInput {
	return ReportInput{
		ScanName:    "nightly-scan",
		Namespace:   "default",
		Repository:  "github.com/acme/app",
		Revision:    "abc1234",
		Summary:     "Two issues found in the API layer.",
		StartedAt:   time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
		CompletedAt: time.Date(2026, 1, 2, 3, 9, 5, 0, time.UTC),
		Ranked: []RankedFinding{
			{
				Finding: Finding{
					Title: "SQL injection in search", Category: "injection",
					Severity: "critical", Confidence: "confirmed",
					FilePath: "internal/api/search.go", StartLine: 42, EndLine: 48,
					Symbol: "Search", CWE: []string{"CWE-89"},
					Description:  "User input is concatenated into a SQL query.",
					Impact:       "Full database read/write.",
					AttackVector: "Remote unauthenticated HTTP request.",
					Remediation:  "Use parameterized queries.",
					Evidence: []Evidence{{
						FilePath: "internal/api/search.go", StartLine: 42, EndLine: 44,
						Snippet: `query := "SELECT * FROM users WHERE name = '" + name + "'"`,
						Note:    "raw concatenation",
					}},
					References:  []string{"https://owasp.org/sqli"},
					Fingerprint: "aaaaaaaaaaaaaaaa",
				},
				Score:   95.5,
				Reasons: []string{"severity critical", "high exposure"},
			},
			{
				Finding: Finding{
					Title: "Verbose error page", Category: "info-leak",
					Severity: "low", Confidence: "firm",
					FilePath: "internal/web/errors.go", StartLine: 10,
					Description: "Stack traces are returned to clients.",
				},
				Score: 22.0,
			},
		},
	}
}

func TestRenderMarkdown(t *testing.T) {
	md := RenderMarkdown(testReportInput())

	for _, want := range []string{
		"# Security Scan Report: nightly-scan",
		"- **Namespace:** default",
		"- **Repository:** github.com/acme/app",
		"- **Revision:** abc1234",
		"2026-01-02T03:04:05Z",
		"Two issues found in the API layer.",
		"| Severity | Count |",
		"| critical | 1 |",
		"| low | 1 |",
		"| medium | 0 |",
		"| **total** | **2** |",
		"### 1. [CRITICAL] SQL injection in search",
		"### 2. [LOW] Verbose error page",
		"`internal/api/search.go:42-48`",
		"CWE-89",
		"- **Score:** 95.5",
		"User input is concatenated into a SQL query.",
		"**Impact:** Full database read/write.",
		"**Attack vector:** Remote unauthenticated HTTP request.",
		"```\nquery := \"SELECT * FROM users WHERE name = '\" + name + \"'\"\n```",
		"raw concatenation",
		"**Remediation:** Use parameterized queries.",
		"https://owasp.org/sqli",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown missing %q", want)
		}
	}
	if idx := strings.Index(md, "[CRITICAL]"); idx > strings.Index(md, "[LOW]") {
		t.Error("findings out of order in markdown")
	}
}

func TestRenderMarkdownEmpty(t *testing.T) {
	md := RenderMarkdown(ReportInput{})
	for _, want := range []string{"# Security Scan Report: Security Scan", "| **total** | **0** |", "No findings."} {
		if !strings.Contains(md, want) {
			t.Errorf("empty-report markdown missing %q; got:\n%s", want, md)
		}
	}
	if strings.Contains(md, "0001-01-01") {
		t.Error("zero timestamps should be omitted")
	}
}

func testScannerReportInput(t *testing.T) ReportInput {
	t.Helper()
	agent := Finding{
		Title: "Weak password hashing with MD5", Category: "crypto",
		Severity: "high", Confidence: "confirmed",
		Repository: "github.com/acme/app",
		FilePath:   "internal/crypto/hash.go", StartLine: 40, EndLine: 46,
		CWE:         []string{"CWE-327"},
		Description: "Passwords are hashed with MD5 which is broken.",
		SourceAgent: "scan-run-1",
	}
	agent.Normalize()
	scanner, err := NormalizeScannerRecord(ScannerRecord{
		Tool: "gosec", ToolVersion: "2.18.2", RuleID: "G401",
		RuleName: "Use of weak cryptographic primitive",
		Message:  "Use of weak cryptographic primitive md5",
		Severity: "HIGH", FilePath: "internal/crypto/hash.go",
		StartLine: 42, EndLine: 44, Symbol: "hashPassword", CWE: "CWE-327",
	}, "github.com/acme/app", "abc1234")
	if err != nil {
		t.Fatal(err)
	}
	findings := []Finding{agent, scanner}
	ApplyCorrelations(findings, Correlate(findings))
	in := testReportInput()
	in.Ranked = []RankedFinding{
		{Finding: findings[0], Score: 80},
		{Finding: findings[1], Score: 70},
	}
	return in
}

func TestRenderMarkdownSourceAttribution(t *testing.T) {
	md := RenderMarkdown(testScannerReportInput(t))
	for _, want := range []string{
		"- **Source:** agent scan-run-1",
		"- **Source:** scanner gosec 2.18.2, rule G401",
		"- **Correlated with:** scanner gosec 2.18.2, rule G401 (fingerprint ",
		"- **Correlated with:** agent scan-run-1 (fingerprint ",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown missing %q:\n%s", want, md)
		}
	}
}

func TestRenderMarkdownDefaultAgentSource(t *testing.T) {
	md := RenderMarkdown(testReportInput())
	if !strings.Contains(md, "- **Source:** agent\n") {
		t.Errorf("markdown missing default agent source attribution:\n%s", md)
	}
}
