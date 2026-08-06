package security

import (
	"fmt"
	"strings"
	"time"
)

// Session artifact kinds under which scan reports are stored. Written by the
// agent's submit_security_scan_report tool and read back by the dashboard's
// GetSecurityScanReport RPC.
const (
	ReportArtifactKind = "security_report"
	SARIFArtifactKind  = "security_sarif"
)

// ReportInput carries everything needed to render a scan report.
type ReportInput struct {
	ScanName    string
	Namespace   string
	Repository  string
	Revision    string
	Summary     string
	StartedAt   time.Time
	CompletedAt time.Time
	Ranked      []RankedFinding
}

func reportFindings(in ReportInput) []Finding {
	findings := make([]Finding, 0, len(in.Ranked))
	for _, r := range in.Ranked {
		findings = append(findings, r.Finding)
	}
	return findings
}

// RenderMarkdown renders the scan report as human-readable markdown: header,
// scan metadata, a severity count table, and one section per finding.
func RenderMarkdown(in ReportInput) string {
	var b strings.Builder

	title := strings.TrimSpace(in.ScanName)
	if title == "" {
		title = "Security Scan"
	}
	fmt.Fprintf(&b, "# Security Scan Report: %s\n\n", title)

	meta := [][2]string{
		{"Namespace", in.Namespace},
		{"Repository", in.Repository},
		{"Revision", in.Revision},
	}
	if !in.StartedAt.IsZero() {
		meta = append(meta, [2]string{"Started", in.StartedAt.UTC().Format(time.RFC3339)})
	}
	if !in.CompletedAt.IsZero() {
		meta = append(meta, [2]string{"Completed", in.CompletedAt.UTC().Format(time.RFC3339)})
		if !in.StartedAt.IsZero() && in.CompletedAt.After(in.StartedAt) {
			meta = append(meta, [2]string{"Duration", in.CompletedAt.Sub(in.StartedAt).Round(time.Second).String()})
		}
	}
	for _, kv := range meta {
		if kv[1] != "" {
			fmt.Fprintf(&b, "- **%s:** %s\n", kv[0], kv[1])
		}
	}
	b.WriteString("\n")

	if s := strings.TrimSpace(in.Summary); s != "" {
		b.WriteString(s)
		b.WriteString("\n\n")
	}

	counts := Summarize(reportFindings(in))
	b.WriteString("## Summary\n\n")
	b.WriteString("| Severity | Count |\n| --- | --- |\n")
	for _, sev := range Severities {
		fmt.Fprintf(&b, "| %s | %d |\n", sev, counts[sev])
	}
	fmt.Fprintf(&b, "| **total** | **%d** |\n\n", counts["total"])

	if len(in.Ranked) == 0 {
		b.WriteString("No findings.\n")
		return b.String()
	}

	b.WriteString("## Findings\n\n")
	byFingerprint := make(map[string]Finding, len(in.Ranked))
	for _, r := range in.Ranked {
		byFingerprint[r.Finding.Fingerprint] = r.Finding
	}
	for i, r := range in.Ranked {
		f := r.Finding
		fmt.Fprintf(&b, "### %d. [%s] %s\n\n", i+1, strings.ToUpper(orUnknown(f.Severity)), f.Title)

		fmt.Fprintf(&b, "- **Severity:** %s\n", orUnknown(f.Severity))
		fmt.Fprintf(&b, "- **Confidence:** %s\n", orUnknown(f.Confidence))
		fmt.Fprintf(&b, "- **Category:** %s\n", orUnknown(f.Category))
		fmt.Fprintf(&b, "- **Source:** %s\n", findingSourceLabel(f))
		fmt.Fprintf(&b, "- **Score:** %.1f\n", r.Score)
		if loc := formatLocation(f.FilePath, f.StartLine, f.EndLine); loc != "" {
			fmt.Fprintf(&b, "- **Location:** `%s`\n", loc)
		}
		if f.Symbol != "" {
			fmt.Fprintf(&b, "- **Symbol:** `%s`\n", f.Symbol)
		}
		if len(f.CWE) > 0 {
			fmt.Fprintf(&b, "- **CWE:** %s\n", strings.Join(f.CWE, ", "))
		}
		if len(r.Reasons) > 0 {
			fmt.Fprintf(&b, "- **Ranking reasons:** %s\n", strings.Join(r.Reasons, "; "))
		}
		if len(f.CorrelatedFingerprints) > 0 {
			labels := make([]string, 0, len(f.CorrelatedFingerprints))
			for _, fp := range f.CorrelatedFingerprints {
				if other, ok := byFingerprint[fp]; ok {
					labels = append(labels, fmt.Sprintf("%s (fingerprint %s)", findingSourceLabel(other), fp))
				} else {
					labels = append(labels, fmt.Sprintf("fingerprint %s", fp))
				}
			}
			fmt.Fprintf(&b, "- **Correlated with:** %s\n", strings.Join(labels, "; "))
		}
		b.WriteString("\n")

		if f.Description != "" {
			b.WriteString(f.Description)
			b.WriteString("\n\n")
		}
		if f.Impact != "" {
			fmt.Fprintf(&b, "**Impact:** %s\n\n", f.Impact)
		}
		if f.AttackVector != "" {
			fmt.Fprintf(&b, "**Attack vector:** %s\n\n", f.AttackVector)
		}
		for _, e := range f.Evidence {
			label := formatLocation(e.FilePath, e.StartLine, e.EndLine)
			if label == "" {
				label = "evidence"
			}
			fmt.Fprintf(&b, "**Evidence** (`%s`)", label)
			if e.Note != "" {
				fmt.Fprintf(&b, " — %s", e.Note)
			}
			b.WriteString(":\n\n")
			if e.Snippet != "" {
				fmt.Fprintf(&b, "```\n%s\n```\n\n", e.Snippet)
			}
		}
		if f.Remediation != "" {
			fmt.Fprintf(&b, "**Remediation:** %s\n\n", f.Remediation)
		}
		if len(f.References) > 0 {
			b.WriteString("**References:**\n\n")
			for _, ref := range f.References {
				fmt.Fprintf(&b, "- %s\n", ref)
			}
			b.WriteString("\n")
		}
	}
	return b.String()
}

// findingSourceLabel renders the finding's source attribution: the tool
// name, version, and rule id for scanner findings, the reporting agent for
// agent findings.
func findingSourceLabel(f Finding) string {
	if f.IsScannerFinding() {
		label := "scanner " + orUnknown(f.Tool)
		if f.ToolVersion != "" {
			label += " " + f.ToolVersion
		}
		if f.RuleID != "" {
			label += ", rule " + f.RuleID
		}
		return label
	}
	if f.SourceAgent != "" {
		return "agent " + f.SourceAgent
	}
	return "agent"
}

func formatLocation(filePath string, startLine, endLine int) string {
	if filePath == "" {
		return ""
	}
	switch {
	case startLine > 0 && endLine > startLine:
		return fmt.Sprintf("%s:%d-%d", filePath, startLine, endLine)
	case startLine > 0:
		return fmt.Sprintf("%s:%d", filePath, startLine)
	}
	return filePath
}
