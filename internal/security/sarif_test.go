package security

import (
	"encoding/json"
	"slices"
	"testing"
)

func TestRenderSARIF(t *testing.T) {
	raw, err := RenderSARIF(testReportInput())
	if err != nil {
		t.Fatalf("RenderSARIF: %v", err)
	}

	var log struct {
		Schema  string `json:"$schema"`
		Version string `json:"version"`
		Runs    []struct {
			Tool struct {
				Driver struct {
					Name  string `json:"name"`
					Rules []struct {
						ID         string `json:"id"`
						Properties struct {
							Tags []string `json:"tags"`
						} `json:"properties"`
					} `json:"rules"`
				} `json:"driver"`
			} `json:"tool"`
			Results []struct {
				RuleID    string `json:"ruleId"`
				RuleIndex int    `json:"ruleIndex"`
				Level     string `json:"level"`
				Message   struct {
					Text string `json:"text"`
				} `json:"message"`
				Locations []struct {
					PhysicalLocation struct {
						ArtifactLocation struct {
							URI string `json:"uri"`
						} `json:"artifactLocation"`
						Region struct {
							StartLine int `json:"startLine"`
							EndLine   int `json:"endLine"`
						} `json:"region"`
					} `json:"physicalLocation"`
				} `json:"locations"`
				PartialFingerprints map[string]string `json:"partialFingerprints"`
			} `json:"results"`
		} `json:"runs"`
	}
	if err := json.Unmarshal(raw, &log); err != nil {
		t.Fatalf("SARIF output is not valid JSON: %v", err)
	}

	if log.Version != "2.1.0" {
		t.Errorf("version = %q, want 2.1.0", log.Version)
	}
	if len(log.Runs) != 1 {
		t.Fatalf("runs = %d, want 1", len(log.Runs))
	}
	run := log.Runs[0]
	if run.Tool.Driver.Name != "gratefulagents-security-scan" {
		t.Errorf("driver name = %q", run.Tool.Driver.Name)
	}

	if len(run.Tool.Driver.Rules) != 2 {
		t.Fatalf("rules = %d, want 2 (injection, info-leak)", len(run.Tool.Driver.Rules))
	}
	// Rules sorted by id: info-leak before injection.
	if run.Tool.Driver.Rules[0].ID != "info-leak" || run.Tool.Driver.Rules[1].ID != "injection" {
		t.Errorf("rule ids = %q, %q, want info-leak, injection", run.Tool.Driver.Rules[0].ID, run.Tool.Driver.Rules[1].ID)
	}
	if !slices.Contains(run.Tool.Driver.Rules[1].Properties.Tags, "CWE-89") {
		t.Errorf("injection rule tags = %v, want CWE-89", run.Tool.Driver.Rules[1].Properties.Tags)
	}

	if len(run.Results) != 2 {
		t.Fatalf("results = %d, want 2", len(run.Results))
	}
	sqli := run.Results[0]
	if sqli.RuleID != "injection" || sqli.RuleIndex != 1 {
		t.Errorf("sqli ruleId=%q ruleIndex=%d, want injection/1", sqli.RuleID, sqli.RuleIndex)
	}
	if sqli.Level != "error" {
		t.Errorf("critical level = %q, want error", sqli.Level)
	}
	if run.Results[1].Level != "note" {
		t.Errorf("low level = %q, want note", run.Results[1].Level)
	}
	if len(sqli.Locations) != 1 {
		t.Fatalf("sqli locations = %d, want 1", len(sqli.Locations))
	}
	loc := sqli.Locations[0].PhysicalLocation
	if loc.ArtifactLocation.URI != "internal/api/search.go" {
		t.Errorf("uri = %q", loc.ArtifactLocation.URI)
	}
	if loc.Region.StartLine != 42 || loc.Region.EndLine != 48 {
		t.Errorf("region = %d-%d, want 42-48", loc.Region.StartLine, loc.Region.EndLine)
	}
	if got := sqli.PartialFingerprints["gratefulagentsFindingFingerprint/v1"]; got != "aaaaaaaaaaaaaaaa" {
		t.Errorf("partial fingerprint = %q, want aaaaaaaaaaaaaaaa", got)
	}
	// Second finding had no explicit fingerprint: one must be computed.
	if got := run.Results[1].PartialFingerprints["gratefulagentsFindingFingerprint/v1"]; len(got) != 16 {
		t.Errorf("computed fingerprint = %q, want 16 hex chars", got)
	}
}

func TestRenderSARIFLevelMapping(t *testing.T) {
	tests := []struct {
		severity, want string
	}{
		{"critical", "error"}, {"high", "error"}, {"medium", "warning"}, {"low", "note"}, {"info", "note"}, {"", "note"},
	}
	for _, tt := range tests {
		if got := sarifLevel(tt.severity); got != tt.want {
			t.Errorf("sarifLevel(%q) = %q, want %q", tt.severity, got, tt.want)
		}
	}
}

func TestRenderSARIFEmpty(t *testing.T) {
	raw, err := RenderSARIF(ReportInput{})
	if err != nil {
		t.Fatalf("RenderSARIF(empty): %v", err)
	}
	var log map[string]any
	if err := json.Unmarshal(raw, &log); err != nil {
		t.Fatalf("empty SARIF is not valid JSON: %v", err)
	}
	runs, _ := log["runs"].([]any)
	if len(runs) != 1 {
		t.Fatalf("runs = %d, want 1", len(runs))
	}
	run, _ := runs[0].(map[string]any)
	results, ok := run["results"].([]any)
	if !ok || len(results) != 0 {
		t.Errorf("results = %v, want empty array present", run["results"])
	}
}
