package security

import (
	"strings"
	"testing"
)

func validScannerRecord() ScannerRecord {
	return ScannerRecord{
		Tool:        "gosec",
		ToolVersion: "2.18.2",
		RuleID:      "G401",
		RuleName:    "Use of weak cryptographic primitive",
		Message:     "Use of weak cryptographic primitive md5",
		Severity:    "HIGH",
		FilePath:    "internal/crypto/hash.go",
		StartLine:   42,
		EndLine:     44,
		Symbol:      "hashPassword",
		CWE:         "CWE-327",
		References:  []string{"https://cwe.mitre.org/data/definitions/327.html"},
		RawEvidence: "sum := md5.Sum(password)",
	}
}

func TestNormalizeScannerRecord(t *testing.T) {
	f, err := NormalizeScannerRecord(validScannerRecord(), "github.com/acme/widget", "abc123")
	if err != nil {
		t.Fatalf("NormalizeScannerRecord: %v", err)
	}
	if f.SourceKind != SourceKindScanner {
		t.Errorf("SourceKind = %q, want %q", f.SourceKind, SourceKindScanner)
	}
	if f.Tool != "gosec" || f.ToolVersion != "2.18.2" || f.RuleID != "G401" {
		t.Errorf("tool identity = %q/%q/%q", f.Tool, f.ToolVersion, f.RuleID)
	}
	if f.Title != "Use of weak cryptographic primitive" {
		t.Errorf("Title = %q, want rule name", f.Title)
	}
	if f.Description != "Use of weak cryptographic primitive md5" {
		t.Errorf("Description = %q, want message", f.Description)
	}
	if f.Severity != SeverityHigh {
		t.Errorf("Severity = %q, want high", f.Severity)
	}
	if f.Confidence != ConfidenceFirm {
		t.Errorf("Confidence = %q, want firm (deterministic rule match)", f.Confidence)
	}
	if f.Category != "crypto" {
		t.Errorf("Category = %q, want crypto from CWE-327", f.Category)
	}
	if f.Repository != "github.com/acme/widget" || f.Revision != "abc123" {
		t.Errorf("repository/revision = %q/%q", f.Repository, f.Revision)
	}
	if len(f.CWE) != 1 || f.CWE[0] != "CWE-327" {
		t.Errorf("CWE = %v", f.CWE)
	}
	if len(f.Evidence) != 1 || f.Evidence[0].Snippet != "sum := md5.Sum(password)" || f.Evidence[0].FilePath != "internal/crypto/hash.go" {
		t.Errorf("Evidence = %+v", f.Evidence)
	}
	if err := f.Validate(); err != nil {
		t.Errorf("normalized scanner finding must validate: %v", err)
	}
	if f.Fingerprint == "" {
		t.Error("Fingerprint must be set")
	}
}

func TestNormalizeScannerRecordRequiredFields(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*ScannerRecord)
		want   []string
	}{
		{"missing tool", func(r *ScannerRecord) { r.Tool = " " }, []string{"tool is required"}},
		{"missing rule id", func(r *ScannerRecord) { r.RuleID = "" }, []string{"rule_id is required"}},
		{"missing message", func(r *ScannerRecord) { r.Message = "" }, []string{"message is required"}},
		{"missing severity", func(r *ScannerRecord) { r.Severity = "" }, []string{"severity is required"}},
		{"unmappable severity", func(r *ScannerRecord) { r.Severity = "apocalyptic" }, []string{`severity "apocalyptic" does not map`}},
		{"missing file path", func(r *ScannerRecord) { r.FilePath = "" }, []string{"file_path is required"}},
		{"aggregates all problems", func(r *ScannerRecord) {
			r.Tool, r.RuleID, r.Message, r.Severity, r.FilePath = "", "", "", "", ""
		}, []string{"tool is required", "rule_id is required", "message is required", "severity is required", "file_path is required"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := validScannerRecord()
			tt.mutate(&rec)
			_, err := NormalizeScannerRecord(rec, "repo", "rev")
			if err == nil {
				t.Fatal("expected error")
			}
			for _, want := range tt.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q missing %q", err, want)
				}
			}
		})
	}
}

func TestScannerSeverityMapping(t *testing.T) {
	tests := map[string]string{
		"ERROR":      SeverityHigh, // semgrep / SARIF
		"error":      SeverityHigh,
		"WARNING":    SeverityMedium,
		"warn":       SeverityMedium,
		"note":       SeverityInfo,
		"INFO":       SeverityInfo,
		"CRITICAL":   SeverityCritical, // trivy
		"HIGH":       SeverityHigh,
		"MEDIUM":     SeverityMedium,
		"LOW":        SeverityLow,
		"UNKNOWN":    SeverityInfo,
		"moderate":   SeverityMedium, // npm audit
		"negligible": SeverityInfo,   // grype
	}
	for in, want := range tests {
		rec := validScannerRecord()
		rec.Severity = in
		f, err := NormalizeScannerRecord(rec, "repo", "rev")
		if err != nil {
			t.Errorf("severity %q: %v", in, err)
			continue
		}
		if f.Severity != want {
			t.Errorf("severity %q mapped to %q, want %q", in, f.Severity, want)
		}
	}
}

func TestScannerCategoryResolution(t *testing.T) {
	rec := validScannerRecord()
	rec.Category = "sql-injection" // alias
	f, err := NormalizeScannerRecord(rec, "repo", "rev")
	if err != nil || f.Category != "injection" {
		t.Errorf("explicit alias category = %q, %v; want injection", f.Category, err)
	}

	rec = validScannerRecord()
	rec.Category = ""
	rec.CWE = "798"
	f, err = NormalizeScannerRecord(rec, "repo", "rev")
	if err != nil || f.Category != "secrets" {
		t.Errorf("CWE-derived category = %q, %v; want secrets", f.Category, err)
	}

	rec = validScannerRecord()
	rec.Category = ""
	rec.CWE = ""
	f, err = NormalizeScannerRecord(rec, "repo", "rev")
	if err != nil || f.Category != "other" {
		t.Errorf("fallback category = %q, %v; want other", f.Category, err)
	}
}

func TestNormalizeScannerRecordRedactsSecrets(t *testing.T) {
	awsKey := "AKIA" + "IOSFODNN7EXAMPLE"
	rec := validScannerRecord()
	rec.Message = "hardcoded key " + awsKey + " found"
	rec.RawEvidence = `key := "` + awsKey + `"`
	f, err := NormalizeScannerRecord(rec, "repo", "rev")
	if err != nil {
		t.Fatalf("NormalizeScannerRecord: %v", err)
	}
	if strings.Contains(f.Description, awsKey) || !strings.Contains(f.Description, RedactedMarker) {
		t.Errorf("Description not redacted: %q", f.Description)
	}
	if strings.Contains(f.Evidence[0].Snippet, awsKey) || !strings.Contains(f.Evidence[0].Snippet, RedactedMarker) {
		t.Errorf("Evidence snippet not redacted: %q", f.Evidence[0].Snippet)
	}
}

func TestScannerRecordRedacted(t *testing.T) {
	awsKey := "AKIA" + "0123456789ABCDEF"
	ghToken := "ghp_" + strings.Repeat("a", 24)
	skKey := "sk-" + strings.Repeat("b", 24)
	rec := validScannerRecord()
	rec.Message = "token " + ghToken
	rec.RawEvidence = "key " + awsKey
	rec.Extra = map[string]string{"match": skKey}
	red := rec.Redacted()
	for name, got := range map[string]string{
		"Message":      red.Message,
		"RawEvidence":  red.RawEvidence,
		"Extra[match]": red.Extra["match"],
	} {
		if !strings.Contains(got, RedactedMarker) {
			t.Errorf("%s not redacted: %q", name, got)
		}
	}
	if !strings.Contains(rec.Extra["match"], "sk-") {
		t.Error("Redacted must not mutate the original record")
	}
}

func TestScannerFingerprintStability(t *testing.T) {
	f1, err := NormalizeScannerRecord(validScannerRecord(), "repo", "rev1")
	if err != nil {
		t.Fatal(err)
	}
	// A tool upgrade rewording the message and shifting the region keeps
	// the identity: only tool/rule/repo/path/anchor participate.
	rec := validScannerRecord()
	rec.ToolVersion = "2.19.0"
	rec.Message = "MD5 is a weak cryptographic primitive; use SHA-256"
	rec.StartLine = 45
	rec.EndLine = 47
	f2, err := NormalizeScannerRecord(rec, "repo", "rev2")
	if err != nil {
		t.Fatal(err)
	}
	if f1.Fingerprint != f2.Fingerprint {
		t.Errorf("fingerprint changed across re-run: %q vs %q", f1.Fingerprint, f2.Fingerprint)
	}

	// A different rule is a different finding.
	rec = validScannerRecord()
	rec.RuleID = "G402"
	f3, _ := NormalizeScannerRecord(rec, "repo", "rev1")
	if f3.Fingerprint == f1.Fingerprint {
		t.Error("different rule ids must not share a fingerprint")
	}

	// A different tool is a different finding.
	rec = validScannerRecord()
	rec.Tool = "semgrep"
	f4, _ := NormalizeScannerRecord(rec, "repo", "rev1")
	if f4.Fingerprint == f1.Fingerprint {
		t.Error("different tools must not share a fingerprint")
	}

	// Without a symbol the start line anchors the identity.
	rec = validScannerRecord()
	rec.Symbol = ""
	f5, _ := NormalizeScannerRecord(rec, "repo", "rev1")
	rec.StartLine = 100
	f6, _ := NormalizeScannerRecord(rec, "repo", "rev1")
	if f5.Fingerprint == f6.Fingerprint {
		t.Error("without a symbol, different start lines must not share a fingerprint")
	}
}

func TestScannerFingerprintDistinctFromAgentFingerprint(t *testing.T) {
	scanner, err := NormalizeScannerRecord(validScannerRecord(), "github.com/acme/widget", "abc")
	if err != nil {
		t.Fatal(err)
	}
	// An agent finding describing the exact same issue at the same
	// location must never collide: merging is an explicit correlation.
	agent := Finding{
		Title:       scanner.Title,
		Category:    scanner.Category,
		Severity:    scanner.Severity,
		Repository:  scanner.Repository,
		FilePath:    scanner.FilePath,
		StartLine:   scanner.StartLine,
		EndLine:     scanner.EndLine,
		Symbol:      scanner.Symbol,
		CWE:         scanner.CWE,
		Description: scanner.Description,
	}
	agent.Normalize()
	if agent.Fingerprint == scanner.Fingerprint {
		t.Errorf("agent and scanner fingerprints must differ, both %q", agent.Fingerprint)
	}
	// Recomputing a scanner fingerprint via the generic Fingerprint
	// dispatch matches Normalize's result.
	if got := Fingerprint(scanner); got != scanner.Fingerprint {
		t.Errorf("Fingerprint(scanner) = %q, want %q", got, scanner.Fingerprint)
	}
}

func testCorrelationPair(t *testing.T) (Finding, Finding) {
	t.Helper()
	agent := Finding{
		Title:       "Weak password hashing with MD5",
		Category:    "crypto",
		Severity:    "high",
		Repository:  "github.com/acme/widget",
		FilePath:    "internal/crypto/hash.go",
		StartLine:   40,
		EndLine:     46,
		CWE:         []string{"CWE-327"},
		Description: "Passwords are hashed with MD5 which is broken.",
	}
	agent.Normalize()
	scanner, err := NormalizeScannerRecord(validScannerRecord(), "github.com/acme/widget", "abc")
	if err != nil {
		t.Fatal(err)
	}
	return agent, scanner
}

func TestCorrelateAgentAndScannerFindings(t *testing.T) {
	agent, scanner := testCorrelationPair(t)
	correlations := Correlate([]Finding{agent, scanner})
	if len(correlations) != 1 {
		t.Fatalf("correlations = %d, want 1", len(correlations))
	}
	c := correlations[0]
	if c.AgentFingerprint != agent.Fingerprint || c.ScannerFingerprint != scanner.Fingerprint {
		t.Errorf("correlation = %+v", c)
	}
	if !strings.Contains(c.Reason, "CWE") {
		t.Errorf("reason = %q, want shared-CWE reason", c.Reason)
	}

	findings := []Finding{agent, scanner}
	ApplyCorrelations(findings, correlations)
	if len(findings[0].CorrelatedFingerprints) != 1 || findings[0].CorrelatedFingerprints[0] != scanner.Fingerprint {
		t.Errorf("agent side not cross-referenced: %v", findings[0].CorrelatedFingerprints)
	}
	if len(findings[1].CorrelatedFingerprints) != 1 || findings[1].CorrelatedFingerprints[0] != agent.Fingerprint {
		t.Errorf("scanner side not cross-referenced: %v", findings[1].CorrelatedFingerprints)
	}
	// Provenance preserved on both sides.
	if findings[0].IsScannerFinding() || findings[1].Tool != "gosec" || findings[1].RuleID != "G401" {
		t.Error("correlation must not rewrite provenance")
	}

	// Already cross-referenced pairs are not re-reported.
	if again := Correlate(findings); len(again) != 0 {
		t.Errorf("re-correlate = %d, want 0", len(again))
	}
}

func TestCorrelateRequiresProximityAndTaxonomyMatch(t *testing.T) {
	agent, scanner := testCorrelationPair(t)

	other := agent
	other.FilePath = "internal/api/server.go"
	other.Normalize()
	if got := Correlate([]Finding{other, scanner}); len(got) != 0 {
		t.Errorf("different file must not correlate, got %d", len(got))
	}

	far := agent
	far.StartLine, far.EndLine = 400, 410
	far.Normalize()
	if got := Correlate([]Finding{far, scanner}); len(got) != 0 {
		t.Errorf("distant lines must not correlate, got %d", len(got))
	}

	near := agent
	near.StartLine, near.EndLine = 39, 0
	near.CWE = nil
	near.Category = "secrets"
	near.Normalize()
	if got := Correlate([]Finding{near, scanner}); len(got) != 0 {
		t.Errorf("no shared CWE and different category must not correlate, got %d", len(got))
	}

	nearSameCategory := near
	nearSameCategory.Category = "crypto"
	nearSameCategory.Normalize()
	got := Correlate([]Finding{nearSameCategory, scanner})
	if len(got) != 1 || !strings.Contains(got[0].Reason, "category") {
		t.Errorf("matching category at same location must correlate, got %+v", got)
	}

	// Two agent findings (or two scanner findings) never correlate.
	agent2 := agent
	agent2.Title = "MD5 used for credential digests"
	agent2.Normalize()
	if got := Correlate([]Finding{agent, agent2}); len(got) != 0 {
		t.Errorf("same-source findings must not correlate, got %d", len(got))
	}
}

func TestDedupeNeverMergesAcrossSourceKinds(t *testing.T) {
	// Make both sides textually near-identical so similarity alone would
	// merge them; the source-kind gate must keep them separate.
	agent := Finding{
		Title:       "Use of weak cryptographic primitive",
		Category:    "crypto",
		Severity:    "high",
		Repository:  "github.com/acme/widget",
		FilePath:    "internal/crypto/hash.go",
		StartLine:   42,
		EndLine:     44,
		CWE:         []string{"CWE-327"},
		Description: "Use of weak cryptographic primitive md5",
	}
	agent.Normalize()
	scanner, err := NormalizeScannerRecord(validScannerRecord(), "github.com/acme/widget", "abc")
	if err != nil {
		t.Fatal(err)
	}
	if Similarity(agent, scanner) < defaultDedupeThreshold {
		t.Fatalf("test premise broken: similarity %.2f below threshold", Similarity(agent, scanner))
	}
	clusters := Dedupe([]Finding{agent, scanner}, 0)
	if len(clusters) != 2 {
		t.Fatalf("clusters = %d, want 2 (agent and scanner findings must not merge)", len(clusters))
	}

	// Same-source similarity merging still works.
	agent2 := agent
	agent2.Title = "Weak cryptographic primitive used"
	agent2.Normalize()
	if got := Dedupe([]Finding{agent, agent2}, 0); len(got) != 1 {
		t.Errorf("same-source near-duplicates = %d clusters, want 1", len(got))
	}
	rec2 := validScannerRecord()
	rec2.ToolVersion = "9.9.9"
	scanner2, _ := NormalizeScannerRecord(rec2, "github.com/acme/widget", "def")
	if got := Dedupe([]Finding{scanner, scanner2}, 0); len(got) != 1 {
		t.Errorf("identical scanner re-runs = %d clusters, want 1 (fingerprint merge)", len(got))
	}
}
