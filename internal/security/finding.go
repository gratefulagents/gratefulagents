// Package security implements the pure findings core for gratefulagents
// security scans.
//
// The pipeline: scanning agents emit Findings, which are passed through
// Normalize and Validate, merged with Dedupe, prioritized with Rank
// (optionally steered by operator-authored RankRules), and finally rendered
// as a Markdown report (RenderMarkdown) or SARIF 2.1.0 (RenderSARIF).
package security

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path"
	"sort"
	"strings"
)

// Severity levels, descending.
const (
	SeverityCritical = "critical"
	SeverityHigh     = "high"
	SeverityMedium   = "medium"
	SeverityLow      = "low"
	SeverityInfo     = "info"
)

// Confidence levels, descending.
const (
	ConfidenceConfirmed = "confirmed"
	ConfidenceFirm      = "firm"
	ConfidenceTentative = "tentative"
)

// Severities lists the known severity levels from most to least severe.
var Severities = []string{SeverityCritical, SeverityHigh, SeverityMedium, SeverityLow, SeverityInfo}

// Categories is the curated set of finding categories.
var Categories = []string{
	"injection",
	"authn",
	"authz",
	"secrets",
	"crypto",
	"ssrf",
	"xss",
	"deserialization",
	"path-traversal",
	"race-condition",
	"dos",
	"memory-safety",
	"supply-chain",
	"misconfiguration",
	"logic-flaw",
	"info-leak",
	"other",
}

const (
	maxTitleLen   = 300
	maxTextLen    = 8000
	maxSnippetLen = 4000
)

// Evidence is a concrete code citation supporting a Finding.
type Evidence struct {
	FilePath  string `json:"file_path,omitempty"`
	StartLine int    `json:"start_line,omitempty"`
	EndLine   int    `json:"end_line,omitempty"`
	Snippet   string `json:"snippet,omitempty"`
	Note      string `json:"note,omitempty"`
}

// Finding is a single security issue reported by a scanning agent.
type Finding struct {
	Title      string `json:"title"`
	Category   string `json:"category"`
	Severity   string `json:"severity"`
	Confidence string `json:"confidence,omitempty"`

	Repository string `json:"repository,omitempty"`
	Revision   string `json:"revision,omitempty"`
	FilePath   string `json:"file_path,omitempty"`
	StartLine  int    `json:"start_line,omitempty"`
	EndLine    int    `json:"end_line,omitempty"`
	Symbol     string `json:"symbol,omitempty"`

	CWE []string `json:"cwe,omitempty"`

	Description  string `json:"description"`
	Impact       string `json:"impact,omitempty"`
	AttackVector string `json:"attack_vector,omitempty"`
	Remediation  string `json:"remediation,omitempty"`

	Evidence   []Evidence `json:"evidence,omitempty"`
	References []string   `json:"references,omitempty"`

	SourceAgent string   `json:"source_agent,omitempty"`
	ScanStep    string   `json:"scan_step,omitempty"`
	Tags        []string `json:"tags,omitempty"`

	Fingerprint string `json:"fingerprint,omitempty"`
}

var severityAliases = map[string]string{
	"critical":      SeverityCritical,
	"crit":          SeverityCritical,
	"blocker":       SeverityCritical,
	"high":          SeverityHigh,
	"important":     SeverityHigh,
	"severe":        SeverityHigh,
	"medium":        SeverityMedium,
	"moderate":      SeverityMedium,
	"med":           SeverityMedium,
	"warning":       SeverityMedium,
	"low":           SeverityLow,
	"minor":         SeverityLow,
	"info":          SeverityInfo,
	"informational": SeverityInfo,
	"note":          SeverityInfo,
	"none":          SeverityInfo,
}

var confidenceAliases = map[string]string{
	"confirmed": ConfidenceConfirmed,
	"certain":   ConfidenceConfirmed,
	"verified":  ConfidenceConfirmed,
	"high":      ConfidenceFirm,
	"firm":      ConfidenceFirm,
	"medium":    ConfidenceFirm,
	"likely":    ConfidenceFirm,
	"probable":  ConfidenceFirm,
	"tentative": ConfidenceTentative,
	"low":       ConfidenceTentative,
	"possible":  ConfidenceTentative,
	"suspected": ConfidenceTentative,
}

var categoryAliases = map[string]string{
	"sql-injection":               "injection",
	"sqli":                        "injection",
	"command-injection":           "injection",
	"code-injection":              "injection",
	"template-injection":          "injection",
	"ldap-injection":              "injection",
	"authentication":              "authn",
	"auth":                        "authn",
	"broken-authentication":       "authn",
	"authorization":               "authz",
	"access-control":              "authz",
	"broken-access-control":       "authz",
	"privilege-escalation":        "authz",
	"idor":                        "authz",
	"secret":                      "secrets",
	"credential":                  "secrets",
	"credentials":                 "secrets",
	"hardcoded-secret":            "secrets",
	"hardcoded-credentials":       "secrets",
	"cryptography":                "crypto",
	"weak-crypto":                 "crypto",
	"weak-cryptography":           "crypto",
	"server-side-request-forgery": "ssrf",
	"cross-site-scripting":        "xss",
	"insecure-deserialization":    "deserialization",
	"unsafe-deserialization":      "deserialization",
	"directory-traversal":         "path-traversal",
	"lfi":                         "path-traversal",
	"local-file-inclusion":        "path-traversal",
	"race":                        "race-condition",
	"toctou":                      "race-condition",
	"denial-of-service":           "dos",
	"resource-exhaustion":         "dos",
	"buffer-overflow":             "memory-safety",
	"use-after-free":              "memory-safety",
	"out-of-bounds":               "memory-safety",
	"dependency":                  "supply-chain",
	"vulnerable-dependency":       "supply-chain",
	"dependencies":                "supply-chain",
	"config":                      "misconfiguration",
	"configuration":               "misconfiguration",
	"security-misconfiguration":   "misconfiguration",
	"business-logic":              "logic-flaw",
	"logic":                       "logic-flaw",
	"information-disclosure":      "info-leak",
	"info-disclosure":             "info-leak",
	"information-leak":            "info-leak",
	"information-leakage":         "info-leak",
	"sensitive-data-exposure":     "info-leak",
	"miscellaneous":               "other",
	"misc":                        "other",
	"unknown":                     "other",
}

var knownCategories = func() map[string]bool {
	m := make(map[string]bool, len(Categories))
	for _, c := range Categories {
		m[c] = true
	}
	return m
}()

func normalizeToken(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.Join(strings.Fields(s), "-")
	return strings.ReplaceAll(s, "_", "-")
}

func normalizeSeverity(s string) string {
	t := normalizeToken(s)
	if canon, ok := severityAliases[t]; ok {
		return canon
	}
	return t
}

func normalizeConfidence(s string) string {
	t := normalizeToken(s)
	if canon, ok := confidenceAliases[t]; ok {
		return canon
	}
	return t
}

func normalizeCategory(s string) string {
	t := normalizeToken(s)
	if canon, ok := categoryAliases[t]; ok {
		return canon
	}
	return t
}

func clampText(s string, max int) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max])
}

func normalizePath(p, repository string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	p = strings.ReplaceAll(p, "\\", "/")
	p = path.Clean(p)
	p = strings.TrimPrefix(p, "/")
	for strings.HasPrefix(p, "./") {
		p = p[2:]
	}
	if p == "." {
		return ""
	}
	if repository != "" {
		repoName := path.Base(strings.ReplaceAll(strings.TrimSpace(repository), "\\", "/"))
		repoName = strings.TrimSuffix(repoName, ".git")
		if repoName != "" && repoName != "." {
			if rest, ok := strings.CutPrefix(p, repoName+"/"); ok {
				p = rest
			}
		}
	}
	return p
}

func normalizeStringSet(in []string, transform func(string) string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if transform != nil {
			s = transform(s)
		}
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	if len(out) == 0 {
		return nil
	}
	sort.Strings(out)
	return out
}

func normalizeCWE(s string) string {
	s = strings.ToUpper(strings.TrimSpace(s))
	s = strings.TrimPrefix(s, "CWE-")
	s = strings.TrimPrefix(s, "CWE")
	s = strings.TrimSpace(strings.TrimPrefix(s, "-"))
	if s == "" {
		return ""
	}
	return "CWE-" + s
}

// Normalize canonicalizes the finding in place: trims and clamps text,
// alias-maps severity/confidence/category, dedupes and sorts CWE, tags, and
// references, normalizes file paths and line numbers, and recomputes the
// Fingerprint.
func (f *Finding) Normalize() {
	if f == nil {
		return
	}
	f.Title = clampText(f.Title, maxTitleLen)
	f.Category = normalizeCategory(f.Category)
	f.Severity = normalizeSeverity(f.Severity)
	f.Confidence = normalizeConfidence(f.Confidence)
	if f.Confidence == "" {
		f.Confidence = ConfidenceTentative
	}

	f.Repository = strings.TrimSpace(f.Repository)
	f.Revision = strings.TrimSpace(f.Revision)
	f.FilePath = normalizePath(f.FilePath, f.Repository)
	if f.StartLine < 0 {
		f.StartLine = 0
	}
	if f.EndLine < 0 {
		f.EndLine = 0
	}
	f.Symbol = strings.TrimSpace(f.Symbol)

	f.CWE = normalizeStringSet(f.CWE, normalizeCWE)

	f.Description = clampText(f.Description, maxTextLen)
	f.Impact = clampText(f.Impact, maxTextLen)
	f.AttackVector = clampText(f.AttackVector, maxTextLen)
	f.Remediation = clampText(f.Remediation, maxTextLen)

	if len(f.Evidence) == 0 {
		f.Evidence = nil
	}
	for i := range f.Evidence {
		e := &f.Evidence[i]
		e.FilePath = normalizePath(e.FilePath, f.Repository)
		if e.StartLine < 0 {
			e.StartLine = 0
		}
		if e.EndLine < 0 {
			e.EndLine = 0
		}
		e.Snippet = clampText(e.Snippet, maxSnippetLen)
		e.Note = strings.TrimSpace(e.Note)
	}

	f.References = normalizeStringSet(f.References, nil)
	f.SourceAgent = strings.TrimSpace(f.SourceAgent)
	f.ScanStep = strings.TrimSpace(f.ScanStep)
	f.Tags = normalizeStringSet(f.Tags, func(s string) string { return strings.ToLower(s) })

	f.Fingerprint = Fingerprint(*f)
}

// Validate reports whether the finding has the required, well-known fields.
// It returns a single error aggregating every problem, or nil.
func (f Finding) Validate() error {
	var problems []string
	if strings.TrimSpace(f.Title) == "" {
		problems = append(problems, "title is required")
	}
	if strings.TrimSpace(f.Description) == "" {
		problems = append(problems, "description is required")
	}
	switch {
	case strings.TrimSpace(f.Category) == "":
		problems = append(problems, "category is required")
	case !knownCategories[f.Category]:
		problems = append(problems, fmt.Sprintf("unknown category %q (known: %s)", f.Category, strings.Join(Categories, ", ")))
	}
	switch {
	case strings.TrimSpace(f.Severity) == "":
		problems = append(problems, "severity is required")
	case SeverityRank(f.Severity) < 0:
		problems = append(problems, fmt.Sprintf("unknown severity %q (known: %s)", f.Severity, strings.Join(Severities, ", ")))
	}
	if f.Confidence != "" {
		switch f.Confidence {
		case ConfidenceConfirmed, ConfidenceFirm, ConfidenceTentative:
		default:
			problems = append(problems, fmt.Sprintf("unknown confidence %q (known: %s, %s, %s)", f.Confidence, ConfidenceConfirmed, ConfidenceFirm, ConfidenceTentative))
		}
	}
	if len(problems) == 0 {
		return nil
	}
	return fmt.Errorf("invalid finding: %s", strings.Join(problems, "; "))
}

var stopwords = map[string]bool{
	"a": true, "an": true, "the": true, "of": true, "in": true, "on": true,
	"at": true, "to": true, "for": true, "is": true, "are": true, "was": true,
	"were": true, "be": true, "been": true, "by": true, "with": true,
	"via": true, "and": true, "or": true, "as": true, "from": true,
	"this": true, "that": true, "it": true, "its": true, "can": true,
	"may": true, "when": true, "which": true,
}

func tokenize(s string) []string {
	fields := strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9')
	})
	out := fields[:0]
	for _, t := range fields {
		if t == "" || stopwords[t] {
			continue
		}
		out = append(out, t)
	}
	return out
}

func sortedTokenSet(s string) []string {
	toks := tokenize(s)
	seen := make(map[string]bool, len(toks))
	out := toks[:0]
	for _, t := range toks {
		if seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

// Fingerprint computes a stable 16-hex-character identity for a finding from
// its repository, file path, category, CWEs, and normalized title tokens.
// Whitespace, casing, and word order in the title do not change the result.
func Fingerprint(f Finding) string {
	cwes := normalizeStringSet(f.CWE, normalizeCWE)
	parts := []string{
		strings.ToLower(strings.TrimSpace(f.Repository)),
		normalizePath(f.FilePath, f.Repository),
		normalizeCategory(f.Category),
		strings.Join(cwes, ","),
		strings.Join(sortedTokenSet(f.Title), " "),
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "|")))
	return hex.EncodeToString(sum[:8])
}

var severityRanks = map[string]int{
	SeverityCritical: 4,
	SeverityHigh:     3,
	SeverityMedium:   2,
	SeverityLow:      1,
	SeverityInfo:     0,
}

// SeverityRank maps a severity to a numeric rank: critical=4 down to info=0.
// Unknown severities return -1.
func SeverityRank(s string) int {
	if r, ok := severityRanks[s]; ok {
		return r
	}
	if r, ok := severityRanks[normalizeSeverity(s)]; ok {
		return r
	}
	return -1
}

// SeverityAtLeast reports whether severity s is at least as severe as min.
// An unknown s is never at least anything; an unknown or empty min passes
// any known severity.
func SeverityAtLeast(s, min string) bool {
	rs := SeverityRank(s)
	return rs >= 0 && rs >= SeverityRank(min)
}

// Summarize counts findings by severity. The result always contains every
// known severity (possibly zero) plus the key "total".
func Summarize(findings []Finding) map[string]int {
	out := make(map[string]int, len(Severities)+1)
	for _, s := range Severities {
		out[s] = 0
	}
	for _, f := range findings {
		out[normalizeSeverity(f.Severity)]++
	}
	out["total"] = len(findings)
	return out
}
