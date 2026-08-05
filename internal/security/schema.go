package security

import (
	"fmt"
	"strings"
)

// FindingJSONSchema is a JSON Schema (draft-07) describing a single Finding
// as emitted by a scanning agent, matching the Finding json tags.
const FindingJSONSchema = `{
  "$schema": "http://json-schema.org/draft-07/schema#",
  "title": "SecurityFinding",
  "description": "A single security finding produced by a scanning agent.",
  "type": "object",
  "additionalProperties": false,
  "required": ["title", "category", "severity", "description"],
  "properties": {
    "title": {
      "type": "string",
      "minLength": 1,
      "maxLength": 300,
      "description": "Short, specific summary of the issue."
    },
    "category": {
      "type": "string",
      "enum": ["injection", "authn", "authz", "secrets", "crypto", "ssrf", "xss", "deserialization", "path-traversal", "race-condition", "dos", "memory-safety", "supply-chain", "misconfiguration", "logic-flaw", "info-leak", "other"],
      "description": "Curated vulnerability category."
    },
    "severity": {
      "type": "string",
      "enum": ["critical", "high", "medium", "low", "info"],
      "description": "Impact severity of the issue."
    },
    "confidence": {
      "type": "string",
      "enum": ["confirmed", "firm", "tentative"],
      "description": "How certain the agent is that the issue is real."
    },
    "repository": {
      "type": "string",
      "description": "Repository the finding belongs to."
    },
    "revision": {
      "type": "string",
      "description": "Git revision (commit SHA) that was scanned."
    },
    "file_path": {
      "type": "string",
      "description": "Repository-relative path of the vulnerable file, forward slashes."
    },
    "start_line": {
      "type": "integer",
      "minimum": 0,
      "description": "First line of the vulnerable code (1-based, 0 = unknown)."
    },
    "end_line": {
      "type": "integer",
      "minimum": 0,
      "description": "Last line of the vulnerable code (1-based, 0 = unknown)."
    },
    "symbol": {
      "type": "string",
      "description": "Function, method, or symbol containing the issue."
    },
    "cwe": {
      "type": "array",
      "items": {"type": "string", "pattern": "^CWE-[0-9]+$"},
      "description": "Relevant CWE identifiers, e.g. CWE-89."
    },
    "description": {
      "type": "string",
      "minLength": 1,
      "maxLength": 8000,
      "description": "Detailed explanation of the vulnerability and why it is exploitable."
    },
    "impact": {
      "type": "string",
      "maxLength": 8000,
      "description": "What an attacker gains by exploiting this issue."
    },
    "attack_vector": {
      "type": "string",
      "maxLength": 8000,
      "description": "How the issue is reached and exploited (e.g. remote unauthenticated HTTP request)."
    },
    "remediation": {
      "type": "string",
      "maxLength": 8000,
      "description": "Concrete fix guidance."
    },
    "evidence": {
      "type": "array",
      "description": "Code citations proving the issue.",
      "items": {
        "type": "object",
        "additionalProperties": false,
        "properties": {
          "file_path": {"type": "string", "description": "Repository-relative path of the cited file."},
          "start_line": {"type": "integer", "minimum": 0, "description": "First cited line (1-based)."},
          "end_line": {"type": "integer", "minimum": 0, "description": "Last cited line (1-based)."},
          "snippet": {"type": "string", "maxLength": 4000, "description": "Verbatim code excerpt."},
          "note": {"type": "string", "description": "Why this snippet matters."}
        }
      }
    },
    "references": {
      "type": "array",
      "items": {"type": "string"},
      "description": "External references (advisories, docs, CVEs)."
    },
    "source_agent": {
      "type": "string",
      "description": "Identifier of the agent that produced the finding."
    },
    "scan_step": {
      "type": "string",
      "description": "Scan pipeline step that produced the finding."
    },
    "tags": {
      "type": "array",
      "items": {"type": "string"},
      "description": "Free-form lowercase tags, e.g. remote, pre-auth, rce."
    }
  }
}`

// FindingSchemaPrompt returns a model-facing markdown block describing the
// finding schema: every field, the allowed severities, confidences, and
// categories, and the reporting rules agents must follow.
func FindingSchemaPrompt() string {
	var b strings.Builder
	b.WriteString("## Security finding format\n\n")
	b.WriteString("Report each security issue as a single JSON object with these fields:\n\n")
	b.WriteString("| Field | Type | Required | Description |\n")
	b.WriteString("| --- | --- | --- | --- |\n")
	rows := [][4]string{
		{"title", "string", "yes", "Short, specific summary (max 300 chars)."},
		{"category", "string", "yes", "One of the categories listed below."},
		{"severity", "string", "yes", "One of the severities listed below."},
		{"confidence", "string", "no", "One of the confidences listed below; defaults to tentative."},
		{"repository", "string", "no", "Repository the finding belongs to."},
		{"revision", "string", "no", "Git commit SHA that was scanned."},
		{"file_path", "string", "no", "Repository-relative path, forward slashes, no leading ./"},
		{"start_line", "integer", "no", "First vulnerable line (1-based)."},
		{"end_line", "integer", "no", "Last vulnerable line (1-based)."},
		{"symbol", "string", "no", "Function/method/symbol containing the issue."},
		{"cwe", "string[]", "no", "CWE identifiers, e.g. [\"CWE-89\"]."},
		{"description", "string", "yes", "Detailed explanation of the vulnerability and why it is exploitable (max 8000 chars)."},
		{"impact", "string", "no", "What an attacker gains by exploiting it."},
		{"attack_vector", "string", "no", "How the issue is reached and exploited."},
		{"remediation", "string", "no", "Concrete fix guidance."},
		{"evidence", "object[]", "no", "Code citations: {file_path, start_line, end_line, snippet, note}."},
		{"references", "string[]", "no", "External references (advisories, docs, CVEs)."},
		{"source_agent", "string", "no", "Identifier of the reporting agent."},
		{"scan_step", "string", "no", "Scan pipeline step name."},
		{"tags", "string[]", "no", "Lowercase tags, e.g. remote, pre-auth, rce."},
	}
	for _, r := range rows {
		fmt.Fprintf(&b, "| `%s` | %s | %s | %s |\n", r[0], r[1], r[2], r[3])
	}
	b.WriteString("\nAllowed `severity` values (most to least severe): ")
	b.WriteString("`" + strings.Join(Severities, "`, `") + "`.\n\n")
	b.WriteString("Allowed `confidence` values: `" + ConfidenceConfirmed + "` (proven exploitable), `" +
		ConfidenceFirm + "` (strong evidence), `" + ConfidenceTentative + "` (plausible but unverified).\n\n")
	b.WriteString("Allowed `category` values: `" + strings.Join(Categories, "`, `") + "`.\n\n")
	b.WriteString("Rules:\n")
	b.WriteString("- Report exactly one finding per real, exploitable issue; do not split one issue into many findings or bundle unrelated issues together.\n")
	b.WriteString("- Cite the exact file and line numbers (`file_path`, `start_line`, `end_line`) and include verbatim code snippets in `evidence`.\n")
	b.WriteString("- Do not speculate: every finding must be backed by concrete evidence from the code. If you cannot show the vulnerable code, do not report it.\n")
	b.WriteString("- Choose severity from real-world impact, not theoretical worst case, and lower `confidence` when exploitability is unproven.\n")
	return b.String()
}
