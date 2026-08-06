package security

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

const sarifToolName = "gratefulagents-security-scan"

type sarifLog struct {
	Schema  string     `json:"$schema"`
	Version string     `json:"version"`
	Runs    []sarifRun `json:"runs"`
}

type sarifRun struct {
	Tool    sarifTool     `json:"tool"`
	Results []sarifResult `json:"results"`
}

type sarifTool struct {
	Driver sarifDriver `json:"driver"`
}

type sarifDriver struct {
	Name           string      `json:"name"`
	Version        string      `json:"version,omitempty"`
	InformationURI string      `json:"informationUri,omitempty"`
	Rules          []sarifRule `json:"rules"`
}

type sarifRule struct {
	ID               string         `json:"id"`
	Name             string         `json:"name,omitempty"`
	ShortDescription sarifMessage   `json:"shortDescription"`
	Properties       sarifRuleProps `json:"properties,omitempty"`
}

type sarifRuleProps struct {
	Tags []string `json:"tags,omitempty"`
}

type sarifMessage struct {
	Text string `json:"text"`
}

type sarifResult struct {
	RuleID              string            `json:"ruleId"`
	RuleIndex           int               `json:"ruleIndex"`
	Level               string            `json:"level"`
	Message             sarifMessage      `json:"message"`
	Locations           []sarifLocation   `json:"locations,omitempty"`
	PartialFingerprints map[string]string `json:"partialFingerprints,omitempty"`
	Properties          map[string]any    `json:"properties,omitempty"`
}

type sarifLocation struct {
	PhysicalLocation sarifPhysicalLocation `json:"physicalLocation"`
}

type sarifPhysicalLocation struct {
	ArtifactLocation sarifArtifactLocation `json:"artifactLocation"`
	Region           *sarifRegion          `json:"region,omitempty"`
}

type sarifArtifactLocation struct {
	URI string `json:"uri"`
}

type sarifRegion struct {
	StartLine int `json:"startLine"`
	EndLine   int `json:"endLine,omitempty"`
}

func sarifLevel(severity string) string {
	switch severity {
	case SeverityCritical, SeverityHigh:
		return "error"
	case SeverityMedium:
		return "warning"
	default:
		return "note"
	}
}

// sarifResultFor renders the finding as a SARIF result under the given rule.
// The result carries the finding fingerprint under
// partialFingerprints["gratefulagentsFindingFingerprint/v1"] and source
// attribution under properties.
func sarifResultFor(r RankedFinding, ruleID string, ruleIndex int) sarifResult {
	f := r.Finding
	fingerprint := f.Fingerprint
	if fingerprint == "" {
		fingerprint = Fingerprint(f)
	}
	msg := strings.TrimSpace(f.Title)
	if msg == "" {
		msg = "(untitled finding)"
	}
	if d := strings.TrimSpace(f.Description); d != "" {
		msg += "\n\n" + d
	}
	res := sarifResult{
		RuleID:    ruleID,
		RuleIndex: ruleIndex,
		Level:     sarifLevel(f.Severity),
		Message:   sarifMessage{Text: msg},
		PartialFingerprints: map[string]string{
			"gratefulagentsFindingFingerprint/v1": fingerprint,
		},
		Properties: map[string]any{
			"severity":   f.Severity,
			"confidence": f.Confidence,
			"score":      r.Score,
		},
	}
	if f.IsScannerFinding() {
		res.Properties["sourceKind"] = SourceKindScanner
		res.Properties["tool"] = f.Tool
		if f.ToolVersion != "" {
			res.Properties["toolVersion"] = f.ToolVersion
		}
		res.Properties["ruleId"] = f.RuleID
	} else {
		res.Properties["sourceKind"] = SourceKindAgent
		if f.SourceAgent != "" {
			res.Properties["sourceAgent"] = f.SourceAgent
		}
	}
	if len(f.CorrelatedFingerprints) > 0 {
		res.Properties["correlatedFingerprints"] = append([]string(nil), f.CorrelatedFingerprints...)
	}
	if len(f.CWE) > 0 {
		res.Properties["cwe"] = append([]string(nil), f.CWE...)
	}
	if f.FilePath != "" {
		loc := sarifLocation{
			PhysicalLocation: sarifPhysicalLocation{
				ArtifactLocation: sarifArtifactLocation{URI: f.FilePath},
			},
		}
		if f.StartLine > 0 {
			region := &sarifRegion{StartLine: f.StartLine}
			if f.EndLine >= f.StartLine {
				region.EndLine = f.EndLine
			}
			loc.PhysicalLocation.Region = region
		}
		res.Locations = []sarifLocation{loc}
	}
	return res
}

// sarifAgentRun renders agent-authored findings as the gratefulagents run.
// Rules are derived from the finding categories (with CWE tags).
func sarifAgentRun(ranked []RankedFinding) sarifRun {
	ruleCWEs := map[string]map[string]bool{}
	for _, r := range ranked {
		cat := r.Finding.Category
		if cat == "" {
			cat = "other"
		}
		if ruleCWEs[cat] == nil {
			ruleCWEs[cat] = map[string]bool{}
		}
		for _, c := range r.Finding.CWE {
			if n := normalizeCWE(c); n != "" {
				ruleCWEs[cat][n] = true
			}
		}
	}
	ruleIDs := make([]string, 0, len(ruleCWEs))
	for id := range ruleCWEs {
		ruleIDs = append(ruleIDs, id)
	}
	sort.Strings(ruleIDs)

	rules := make([]sarifRule, 0, len(ruleIDs))
	ruleIndex := make(map[string]int, len(ruleIDs))
	for i, id := range ruleIDs {
		ruleIndex[id] = i
		tags := []string{"security", id}
		cwes := make([]string, 0, len(ruleCWEs[id]))
		for c := range ruleCWEs[id] {
			cwes = append(cwes, c)
		}
		sort.Strings(cwes)
		tags = append(tags, cwes...)
		rules = append(rules, sarifRule{
			ID:               id,
			Name:             id,
			ShortDescription: sarifMessage{Text: fmt.Sprintf("Security finding category: %s", id)},
			Properties:       sarifRuleProps{Tags: tags},
		})
	}

	results := make([]sarifResult, 0, len(ranked))
	for _, r := range ranked {
		cat := r.Finding.Category
		if cat == "" {
			cat = "other"
		}
		results = append(results, sarifResultFor(r, cat, ruleIndex[cat]))
	}
	return sarifRun{
		Tool:    sarifTool{Driver: sarifDriver{Name: sarifToolName, Rules: rules}},
		Results: results,
	}
}

// sarifScannerRun renders one deterministic tool's findings as that tool's
// own SARIF run: the driver is named after the tool (with its version) and
// the rules are the tool's real rule ids, so gratefulagents never claims to
// have produced another tool's rules.
func sarifScannerRun(tool string, ranked []RankedFinding) sarifRun {
	version := ""
	ruleTitle := map[string]string{}
	ruleTags := map[string]map[string]bool{}
	for _, r := range ranked {
		f := r.Finding
		if version == "" {
			version = f.ToolVersion
		}
		id := f.RuleID
		if _, ok := ruleTitle[id]; !ok {
			ruleTitle[id] = strings.TrimSpace(f.Title)
		}
		if ruleTags[id] == nil {
			ruleTags[id] = map[string]bool{}
		}
		if f.Category != "" {
			ruleTags[id][f.Category] = true
		}
		for _, c := range f.CWE {
			if n := normalizeCWE(c); n != "" {
				ruleTags[id][n] = true
			}
		}
	}
	ruleIDs := make([]string, 0, len(ruleTitle))
	for id := range ruleTitle {
		ruleIDs = append(ruleIDs, id)
	}
	sort.Strings(ruleIDs)

	rules := make([]sarifRule, 0, len(ruleIDs))
	ruleIndex := make(map[string]int, len(ruleIDs))
	for i, id := range ruleIDs {
		ruleIndex[id] = i
		tags := make([]string, 0, len(ruleTags[id])+1)
		tags = append(tags, "security")
		extra := make([]string, 0, len(ruleTags[id]))
		for t := range ruleTags[id] {
			extra = append(extra, t)
		}
		sort.Strings(extra)
		tags = append(tags, extra...)
		desc := ruleTitle[id]
		if desc == "" {
			desc = id
		}
		rules = append(rules, sarifRule{
			ID:               id,
			Name:             id,
			ShortDescription: sarifMessage{Text: desc},
			Properties:       sarifRuleProps{Tags: tags},
		})
	}

	results := make([]sarifResult, 0, len(ranked))
	for _, r := range ranked {
		results = append(results, sarifResultFor(r, r.Finding.RuleID, ruleIndex[r.Finding.RuleID]))
	}
	return sarifRun{
		Tool:    sarifTool{Driver: sarifDriver{Name: tool, Version: version, Rules: rules}},
		Results: results,
	}
}

// RenderSARIF renders the scan report as SARIF 2.1.0 JSON with one run per
// source: agent-authored findings under the gratefulagents driver, and one
// additional run per deterministic scanner tool under that tool's own
// driver name, version, and rule ids. Every result carries the finding
// fingerprint under partialFingerprints["gratefulagentsFindingFingerprint/v1"]
// plus source attribution (and correlated fingerprints) under properties.
func RenderSARIF(in ReportInput) ([]byte, error) {
	agent := make([]RankedFinding, 0, len(in.Ranked))
	scanners := map[string][]RankedFinding{}
	for _, r := range in.Ranked {
		if r.Finding.IsScannerFinding() {
			tool := strings.TrimSpace(r.Finding.Tool)
			if tool == "" {
				tool = "unknown-scanner"
			}
			scanners[tool] = append(scanners[tool], r)
			continue
		}
		agent = append(agent, r)
	}

	// The gratefulagents run always comes first (even when empty, so a
	// scan with no findings still renders one valid run); scanner runs
	// follow in deterministic tool-name order.
	runs := []sarifRun{sarifAgentRun(agent)}
	tools := make([]string, 0, len(scanners))
	for tool := range scanners {
		tools = append(tools, tool)
	}
	sort.Strings(tools)
	for _, tool := range tools {
		runs = append(runs, sarifScannerRun(tool, scanners[tool]))
	}

	log := sarifLog{
		Schema:  "https://raw.githubusercontent.com/oasis-tcs/sarif-spec/master/Schemata/sarif-schema-2.1.0.json",
		Version: "2.1.0",
		Runs:    runs,
	}
	return json.MarshalIndent(log, "", "  ")
}
