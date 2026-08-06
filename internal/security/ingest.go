package security

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// ScannerRecord is the canonical normalized record a deterministic scanner
// tool (semgrep, gosec, trivy, bandit, ...) must be adapted into before
// ingestion. It is intentionally much flatter than Finding: deterministic
// tools report a rule match at a location, not a narrative, so the adapter
// contract only carries what such tools can actually attest to.
type ScannerRecord struct {
	// Tool is the scanner's name, e.g. "gosec". Required.
	Tool string `json:"tool"`
	// ToolVersion is the scanner's version, e.g. "2.18.2". Optional but
	// strongly recommended: it is preserved in the finding's provenance.
	ToolVersion string `json:"tool_version,omitempty"`
	// RuleID is the tool's rule identifier, e.g. "G401". Required; it is
	// part of the finding's deterministic fingerprint.
	RuleID string `json:"rule_id"`
	// RuleName is the tool's human-readable rule name. Optional; used as
	// the finding title when present.
	RuleName string `json:"rule_name,omitempty"`
	// Message is the tool's finding message. Required; becomes the finding
	// description (and the title when RuleName is empty).
	Message string `json:"message"`
	// Severity is the tool's severity in the tool's own vocabulary
	// (e.g. ERROR, WARNING, HIGH, moderate). Required; it must map onto the
	// platform scale (critical/high/medium/low/info).
	Severity string `json:"severity"`
	// Category optionally names the platform finding category (or an alias
	// of one). When absent it is derived from CWE, falling back to "other".
	Category string `json:"category,omitempty"`

	// FilePath is the repository-relative path of the matched file,
	// forward slashes. Required.
	FilePath string `json:"file_path"`
	// StartLine / EndLine bound the matched region (1-based, 0 = unknown).
	StartLine int `json:"start_line,omitempty"`
	EndLine   int `json:"end_line,omitempty"`
	// Symbol is the enclosing function/method/symbol when the tool reports
	// one; it anchors the fingerprint more stably than line numbers.
	Symbol string `json:"symbol,omitempty"`

	// CWE is the rule's CWE identifier, e.g. "CWE-798". Optional.
	CWE string `json:"cwe,omitempty"`
	// References are external URLs (rule docs, advisories). Optional.
	References []string `json:"references,omitempty"`
	// RawEvidence is the tool's verbatim matched snippet. Optional; stored
	// as finding evidence after secret redaction.
	RawEvidence string `json:"raw_evidence,omitempty"`
	// Extra carries tool-specific key/value metadata preserved verbatim
	// (subject to secret redaction) in the finding's raw payload.
	Extra map[string]string `json:"extra,omitempty"`
}

// Redacted returns a copy of the record with obvious credential material
// replaced by RedactedMarker in every free-text field, so the raw payload
// can be preserved verbatim-except-secrets.
func (r ScannerRecord) Redacted() ScannerRecord {
	r.RuleName = redactSecrets(r.RuleName)
	r.Message = redactSecrets(r.Message)
	r.RawEvidence = redactSecrets(r.RawEvidence)
	if len(r.References) > 0 {
		refs := make([]string, len(r.References))
		for i, ref := range r.References {
			refs[i] = redactSecrets(ref)
		}
		r.References = refs
	}
	if len(r.Extra) > 0 {
		extra := make(map[string]string, len(r.Extra))
		for k, v := range r.Extra {
			extra[k] = redactSecrets(v)
		}
		r.Extra = extra
	}
	return r
}

// scannerSeverityAliases maps severity vocabulary common to deterministic
// tools (SARIF levels, semgrep, gosec, trivy, npm audit, ...) onto the
// platform scale, before falling back to the shared severity aliases.
var scannerSeverityAliases = map[string]string{
	"error":      SeverityHigh, // SARIF / semgrep ERROR
	"err":        SeverityHigh,
	"warn":       SeverityMedium,
	"unknown":    SeverityInfo, // trivy UNKNOWN
	"negligible": SeverityInfo,
}

// mapScannerSeverity maps a tool severity into the platform scale. It
// returns "" when the value is not recognizably mappable.
func mapScannerSeverity(s string) string {
	t := normalizeToken(s)
	if canon, ok := scannerSeverityAliases[t]; ok {
		return canon
	}
	canon := normalizeSeverity(t)
	if SeverityRank(canon) >= 0 {
		return canon
	}
	return ""
}

// cweCategories maps well-known CWE identifiers onto platform categories so
// records without an explicit category still land somewhere meaningful.
var cweCategories = map[string]string{
	"CWE-77": "injection", "CWE-78": "injection", "CWE-89": "injection",
	"CWE-94": "injection", "CWE-95": "injection", "CWE-917": "injection",
	"CWE-79":  "xss",
	"CWE-22":  "path-traversal",
	"CWE-23":  "path-traversal",
	"CWE-259": "secrets", "CWE-321": "secrets", "CWE-522": "secrets", "CWE-798": "secrets",
	"CWE-295": "crypto", "CWE-326": "crypto", "CWE-327": "crypto",
	"CWE-328": "crypto", "CWE-338": "crypto",
	"CWE-918": "ssrf",
	"CWE-502": "deserialization",
	"CWE-362": "race-condition", "CWE-367": "race-condition",
	"CWE-400": "dos", "CWE-770": "dos",
	"CWE-119": "memory-safety", "CWE-125": "memory-safety", "CWE-416": "memory-safety",
	"CWE-476": "memory-safety", "CWE-787": "memory-safety",
	"CWE-200": "info-leak", "CWE-209": "info-leak", "CWE-532": "info-leak",
	"CWE-287": "authn", "CWE-306": "authn", "CWE-521": "authn",
	"CWE-284": "authz", "CWE-285": "authz", "CWE-639": "authz",
	"CWE-862": "authz", "CWE-863": "authz",
	"CWE-614": "misconfiguration", "CWE-1004": "misconfiguration",
	"CWE-829": "supply-chain", "CWE-1104": "supply-chain",
}

// scannerCategory resolves the record's platform category: an explicit
// (alias-mapped) category wins, then the CWE mapping, then "other".
func scannerCategory(rec ScannerRecord) string {
	if cat := normalizeCategory(rec.Category); knownCategories[cat] {
		return cat
	}
	if cat, ok := cweCategories[normalizeCWE(rec.CWE)]; ok {
		return cat
	}
	return "other"
}

// NormalizeScannerRecord validates a ScannerRecord against the adapter
// contract and converts it into a platform Finding.
//
// Required fields: tool, rule_id, message, severity (mappable onto the
// platform scale), and file_path. Violations are aggregated into a single
// error so a batch caller can report everything wrong with a record at
// once. The returned Finding went through the same Normalize path as agent
// findings (text clamping, alias mapping, secret redaction of prose and
// evidence) and carries scanner provenance: SourceKind =
// SourceKindScanner, the tool identity/version, and the rule id.
//
// Deterministic tools attest that a rule matched, not that the issue is
// exploitable, so Confidence is fixed at "firm": stronger than an agent's
// unverified "tentative" hypothesis, weaker than a "confirmed" PoC.
// Neither source is authoritative alone — agents are expected to validate
// and correlate scanner findings, not to trust or discard them wholesale.
func NormalizeScannerRecord(rec ScannerRecord, repository, revision string) (Finding, error) {
	var problems []string
	if strings.TrimSpace(rec.Tool) == "" {
		problems = append(problems, "tool is required")
	}
	if strings.TrimSpace(rec.RuleID) == "" {
		problems = append(problems, "rule_id is required")
	}
	if strings.TrimSpace(rec.Message) == "" {
		problems = append(problems, "message is required")
	}
	severity := ""
	if strings.TrimSpace(rec.Severity) == "" {
		problems = append(problems, "severity is required")
	} else if severity = mapScannerSeverity(rec.Severity); severity == "" {
		problems = append(problems, fmt.Sprintf("severity %q does not map onto the platform scale (%s)", rec.Severity, strings.Join(Severities, ", ")))
	}
	if strings.TrimSpace(rec.FilePath) == "" {
		problems = append(problems, "file_path is required")
	}
	if len(problems) > 0 {
		return Finding{}, fmt.Errorf("invalid scanner record: %s", strings.Join(problems, "; "))
	}

	title := strings.TrimSpace(rec.RuleName)
	if title == "" {
		title = strings.TrimSpace(rec.Message)
	}
	// The title derives from tool output that can quote matched secrets
	// (e.g. a secret scanner's message); redact it up front. Scanner
	// fingerprints never use the title, so this cannot change identity.
	title = redactSecrets(title)
	f := Finding{
		Title:       title,
		Category:    scannerCategory(rec),
		Severity:    severity,
		Confidence:  ConfidenceFirm,
		Repository:  repository,
		Revision:    revision,
		FilePath:    rec.FilePath,
		StartLine:   rec.StartLine,
		EndLine:     rec.EndLine,
		Symbol:      rec.Symbol,
		Description: rec.Message,
		References:  rec.References,
		SourceKind:  SourceKindScanner,
		Tool:        rec.Tool,
		ToolVersion: rec.ToolVersion,
		RuleID:      rec.RuleID,
	}
	if cwe := normalizeCWE(rec.CWE); cwe != "" {
		f.CWE = []string{cwe}
	}
	if strings.TrimSpace(rec.RawEvidence) != "" {
		f.Evidence = []Evidence{{
			FilePath:  rec.FilePath,
			StartLine: rec.StartLine,
			EndLine:   rec.EndLine,
			Snippet:   rec.RawEvidence,
			Note:      "verbatim scanner match",
		}}
	}
	f.Normalize()
	if err := f.Validate(); err != nil {
		return Finding{}, fmt.Errorf("invalid scanner record: %w", err)
	}
	return f, nil
}

// scannerFingerprint derives the identity of a deterministic scanner
// finding from (tool, rule id, repository, normalized path, symbol-or-line
// anchor).
//
// Why this derivation, and why it differs from agent fingerprints:
//   - Re-runs of the same tool converge: the hash uses only fields a
//     deterministic tool reproduces run over run, never the message text,
//     so a tool upgrade that rewords its message keeps the same identity.
//   - The symbol anchors the location when the tool reports one (stable
//     across unrelated line drift); otherwise the start line does.
//   - The input is domain-separated with a leading "scanner" component and
//     includes tool + rule id, which agent fingerprints never contain, so a
//     scanner finding can never collide with an agent finding describing
//     the same code. Merging the two sources is an explicit correlation
//     act (CorrelatedFingerprints), recorded on both rows with both
//     provenances intact — never an accident of a shared hash.
func scannerFingerprint(f Finding) string {
	anchor := strings.ToLower(strings.TrimSpace(f.Symbol))
	if anchor == "" && f.StartLine > 0 {
		anchor = "L" + strconv.Itoa(f.StartLine)
	}
	parts := []string{
		"scanner",
		strings.ToLower(strings.TrimSpace(f.Tool)),
		strings.TrimSpace(f.RuleID),
		strings.ToLower(strings.TrimSpace(f.Repository)),
		normalizePath(f.FilePath, f.Repository),
		anchor,
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "|")))
	return hex.EncodeToString(sum[:8])
}

// Correlation is one recorded link between an agent finding and a scanner
// finding that describe the same issue.
type Correlation struct {
	AgentFingerprint   string
	ScannerFingerprint string
	Reason             string
}

// correlationLineProximity is how many lines apart two findings in the same
// file may start and still be considered the same location.
const correlationLineProximity = 5

// correlatable reports whether an agent finding and a scanner finding
// describe the same issue: same repository and file, line ranges that
// overlap or start within correlationLineProximity lines, and either a
// shared CWE or the same category (the scanner rule's category mapping
// matching the agent's classification). It returns the matched reason.
func correlatable(agent, scanner Finding) (string, bool) {
	if agent.FilePath == "" || scanner.FilePath == "" {
		return "", false
	}
	if !strings.EqualFold(strings.TrimSpace(agent.Repository), strings.TrimSpace(scanner.Repository)) {
		return "", false
	}
	if normalizePath(agent.FilePath, agent.Repository) != normalizePath(scanner.FilePath, scanner.Repository) {
		return "", false
	}
	near := linesOverlap(agent.StartLine, agent.EndLine, scanner.StartLine, scanner.EndLine)
	if !near && agent.StartLine > 0 && scanner.StartLine > 0 {
		d := agent.StartLine - scanner.StartLine
		if d < 0 {
			d = -d
		}
		near = d <= correlationLineProximity
	}
	if !near {
		return "", false
	}
	switch {
	case sharesCWE(agent.CWE, scanner.CWE):
		return "same location and shared CWE", true
	case agent.Category != "" && agent.Category == scanner.Category:
		return "same location and matching category", true
	}
	return "", false
}

// Correlate finds agent↔scanner finding pairs that describe the same issue
// and are not yet cross-referenced on both sides. It never merges: each
// returned Correlation is meant to be recorded on BOTH findings
// (CorrelatedFingerprints), preserving each side's provenance — the agent
// finding keeps its confidence semantics, the scanner finding keeps its
// rule identity, and neither is treated as authoritative alone. Results are
// deterministic: sorted by agent then scanner fingerprint.
func Correlate(findings []Finding) []Correlation {
	var agents, scanners []Finding
	for _, f := range findings {
		f := f
		if f.Fingerprint == "" {
			f.Fingerprint = Fingerprint(f)
		}
		if f.IsScannerFinding() {
			scanners = append(scanners, f)
		} else {
			agents = append(agents, f)
		}
	}
	var out []Correlation
	seen := make(map[string]bool)
	for _, a := range agents {
		for _, s := range scanners {
			reason, ok := correlatable(a, s)
			if !ok {
				continue
			}
			if containsString(a.CorrelatedFingerprints, s.Fingerprint) &&
				containsString(s.CorrelatedFingerprints, a.Fingerprint) {
				continue
			}
			key := a.Fingerprint + "|" + s.Fingerprint
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, Correlation{
				AgentFingerprint:   a.Fingerprint,
				ScannerFingerprint: s.Fingerprint,
				Reason:             reason,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].AgentFingerprint != out[j].AgentFingerprint {
			return out[i].AgentFingerprint < out[j].AgentFingerprint
		}
		return out[i].ScannerFingerprint < out[j].ScannerFingerprint
	})
	return out
}

// ApplyCorrelations records the correlations on both sides of each pair in
// the slice, in place: each finding's CorrelatedFingerprints gains the
// counterpart's fingerprint (set semantics, sorted). Nothing else about
// either finding changes.
func ApplyCorrelations(findings []Finding, correlations []Correlation) {
	if len(correlations) == 0 {
		return
	}
	add := make(map[string][]string, len(correlations)*2)
	for _, c := range correlations {
		add[c.AgentFingerprint] = append(add[c.AgentFingerprint], c.ScannerFingerprint)
		add[c.ScannerFingerprint] = append(add[c.ScannerFingerprint], c.AgentFingerprint)
	}
	for i := range findings {
		fp := findings[i].Fingerprint
		if fp == "" {
			fp = Fingerprint(findings[i])
		}
		others := add[fp]
		if len(others) == 0 {
			continue
		}
		findings[i].CorrelatedFingerprints = normalizeStringSet(
			append(findings[i].CorrelatedFingerprints, others...), strings.ToLower)
	}
}

func containsString(set []string, s string) bool {
	for _, v := range set {
		if v == s {
			return true
		}
	}
	return false
}
