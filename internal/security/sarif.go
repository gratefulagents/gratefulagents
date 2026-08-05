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

// RenderSARIF renders the scan report as SARIF 2.1.0 JSON. Rules are derived
// from the finding categories (with CWE tags), and each result carries the
// finding fingerprint under
// partialFingerprints["gratefulagentsFindingFingerprint/v1"].
func RenderSARIF(in ReportInput) ([]byte, error) {
	ruleCWEs := map[string]map[string]bool{}
	for _, r := range in.Ranked {
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

	results := make([]sarifResult, 0, len(in.Ranked))
	for _, r := range in.Ranked {
		f := r.Finding
		cat := f.Category
		if cat == "" {
			cat = "other"
		}
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
			RuleID:    cat,
			RuleIndex: ruleIndex[cat],
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
		results = append(results, res)
	}

	log := sarifLog{
		Schema:  "https://raw.githubusercontent.com/oasis-tcs/sarif-spec/master/Schemata/sarif-schema-2.1.0.json",
		Version: "2.1.0",
		Runs: []sarifRun{{
			Tool: sarifTool{Driver: sarifDriver{
				Name:  sarifToolName,
				Rules: rules,
			}},
			Results: results,
		}},
	}
	return json.MarshalIndent(log, "", "  ")
}
