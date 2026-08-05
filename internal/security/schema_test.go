package security

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestFindingJSONSchemaIsValidJSON(t *testing.T) {
	var schema map[string]any
	if err := json.Unmarshal([]byte(FindingJSONSchema), &schema); err != nil {
		t.Fatalf("FindingJSONSchema is not valid JSON: %v", err)
	}
	if got := schema["$schema"]; got != "http://json-schema.org/draft-07/schema#" {
		t.Errorf("$schema = %v, want draft-07", got)
	}
	if got := schema["type"]; got != "object" {
		t.Errorf("type = %v, want object", got)
	}
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatal("properties missing or not an object")
	}
	wantProps := []string{
		"title", "category", "severity", "confidence", "repository", "revision",
		"file_path", "start_line", "end_line", "symbol", "cwe", "description",
		"impact", "attack_vector", "remediation", "evidence", "references",
		"source_agent", "scan_step", "tags",
	}
	for _, p := range wantProps {
		if _, ok := props[p]; !ok {
			t.Errorf("schema missing property %q", p)
		}
	}
	sev, _ := props["severity"].(map[string]any)
	enum, _ := sev["enum"].([]any)
	if len(enum) != len(Severities) {
		t.Errorf("severity enum = %v, want %v", enum, Severities)
	}
	cat, _ := props["category"].(map[string]any)
	catEnum, _ := cat["enum"].([]any)
	if len(catEnum) != len(Categories) {
		t.Errorf("category enum has %d entries, want %d", len(catEnum), len(Categories))
	}
	for i, c := range Categories {
		if i < len(catEnum) && catEnum[i] != c {
			t.Errorf("category enum[%d] = %v, want %q", i, catEnum[i], c)
		}
	}
	ev, _ := props["evidence"].(map[string]any)
	items, _ := ev["items"].(map[string]any)
	evProps, _ := items["properties"].(map[string]any)
	for _, p := range []string{"file_path", "start_line", "end_line", "snippet", "note"} {
		if _, ok := evProps[p]; !ok {
			t.Errorf("evidence items schema missing property %q", p)
		}
	}
}

func TestFindingJSONSchemaMatchesStructTags(t *testing.T) {
	f := Finding{
		Title: "t", Category: "injection", Severity: "high", Confidence: "firm",
		Repository: "r", Revision: "abc", FilePath: "a/b.go", StartLine: 1, EndLine: 2,
		Symbol: "F", CWE: []string{"CWE-89"}, Description: "d", Impact: "i",
		AttackVector: "av", Remediation: "rm",
		Evidence:   []Evidence{{FilePath: "a/b.go", StartLine: 1, EndLine: 2, Snippet: "s", Note: "n"}},
		References: []string{"ref"}, SourceAgent: "sa", ScanStep: "ss", Tags: []string{"remote"},
	}
	raw, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	var asMap map[string]any
	if err := json.Unmarshal(raw, &asMap); err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Properties map[string]any `json:"properties"`
	}
	if err := json.Unmarshal([]byte(FindingJSONSchema), &schema); err != nil {
		t.Fatal(err)
	}
	for key := range asMap {
		if key == "fingerprint" {
			continue // computed field, not part of the agent-facing schema
		}
		if _, ok := schema.Properties[key]; !ok {
			t.Errorf("Finding json tag %q missing from FindingJSONSchema properties", key)
		}
	}
}

func TestFindingSchemaPrompt(t *testing.T) {
	prompt := FindingSchemaPrompt()
	for _, want := range []string{
		"`title`", "`category`", "`severity`", "`confidence`", "`file_path`",
		"`start_line`", "`end_line`", "`cwe`", "`description`", "`evidence`",
		"`attack_vector`", "`remediation`", "`tags`",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt missing field %s", want)
		}
	}
	for _, sev := range Severities {
		if !strings.Contains(prompt, "`"+sev+"`") {
			t.Errorf("prompt missing severity %q", sev)
		}
	}
	for _, cat := range Categories {
		if !strings.Contains(prompt, "`"+cat+"`") {
			t.Errorf("prompt missing category %q", cat)
		}
	}
	for _, conf := range []string{ConfidenceConfirmed, ConfidenceFirm, ConfidenceTentative} {
		if !strings.Contains(prompt, "`"+conf+"`") {
			t.Errorf("prompt missing confidence %q", conf)
		}
	}
	for _, rule := range []string{"one finding per real, exploitable issue", "exact file and line", "speculate"} {
		if !strings.Contains(prompt, rule) {
			t.Errorf("prompt missing rule text %q", rule)
		}
	}
}
