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

func TestRenderSARIFScannerRunAttribution(t *testing.T) {
	raw, err := RenderSARIF(testScannerReportInput(t))
	if err != nil {
		t.Fatalf("RenderSARIF: %v", err)
	}
	var log struct {
		Runs []struct {
			Tool struct {
				Driver struct {
					Name    string `json:"name"`
					Version string `json:"version"`
					Rules   []struct {
						ID string `json:"id"`
					} `json:"rules"`
				} `json:"driver"`
			} `json:"tool"`
			Results []struct {
				RuleID     string         `json:"ruleId"`
				Properties map[string]any `json:"properties"`
			} `json:"results"`
		} `json:"runs"`
	}
	if err := json.Unmarshal(raw, &log); err != nil {
		t.Fatalf("SARIF output is not valid JSON: %v", err)
	}
	if len(log.Runs) != 2 {
		t.Fatalf("runs = %d, want 2 (agent run + gosec run)", len(log.Runs))
	}

	agentRun := log.Runs[0]
	if agentRun.Tool.Driver.Name != "gratefulagents-security-scan" {
		t.Errorf("agent driver = %q", agentRun.Tool.Driver.Name)
	}
	if len(agentRun.Results) != 1 || agentRun.Results[0].RuleID != "crypto" {
		t.Fatalf("agent results = %+v", agentRun.Results)
	}
	props := agentRun.Results[0].Properties
	if props["sourceKind"] != "agent" || props["sourceAgent"] != "scan-run-1" {
		t.Errorf("agent result properties = %v", props)
	}
	if _, ok := props["correlatedFingerprints"]; !ok {
		t.Errorf("agent result missing correlatedFingerprints: %v", props)
	}

	scannerRun := log.Runs[1]
	// The scanner's findings are attributed to the scanner's own driver
	// and rule ids — gratefulagents must not claim another tool's rules.
	if scannerRun.Tool.Driver.Name != "gosec" || scannerRun.Tool.Driver.Version != "2.18.2" {
		t.Errorf("scanner driver = %q %q, want gosec 2.18.2", scannerRun.Tool.Driver.Name, scannerRun.Tool.Driver.Version)
	}
	if len(scannerRun.Tool.Driver.Rules) != 1 || scannerRun.Tool.Driver.Rules[0].ID != "G401" {
		t.Errorf("scanner rules = %+v, want [G401]", scannerRun.Tool.Driver.Rules)
	}
	if len(scannerRun.Results) != 1 || scannerRun.Results[0].RuleID != "G401" {
		t.Fatalf("scanner results = %+v", scannerRun.Results)
	}
	sprops := scannerRun.Results[0].Properties
	if sprops["sourceKind"] != "scanner" || sprops["tool"] != "gosec" || sprops["toolVersion"] != "2.18.2" || sprops["ruleId"] != "G401" {
		t.Errorf("scanner result properties = %v", sprops)
	}
	if _, ok := sprops["correlatedFingerprints"]; !ok {
		t.Errorf("scanner result missing correlatedFingerprints: %v", sprops)
	}
}
