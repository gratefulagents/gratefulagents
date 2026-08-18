package tools

import (
	"encoding/json"
	"testing"
)

func mustValidateSubset(t *testing.T, schema, value string) error {
	t.Helper()
	parsed, err := parseMinimalJSONSchema(schema)
	if err != nil {
		t.Fatalf("parse schema: %v", err)
	}
	var decoded any
	if err := json.Unmarshal([]byte(value), &decoded); err != nil {
		t.Fatalf("decode value: %v", err)
	}
	return parsed.Validate(decoded)
}

func TestParseMinimalJSONSchemaRejectsNonObjects(t *testing.T) {
	for _, schema := range []string{"not json", `"string"`, `[1,2]`, `42`} {
		if _, err := parseMinimalJSONSchema(schema); err == nil {
			t.Errorf("parseMinimalJSONSchema(%q) expected error", schema)
		}
	}
}

func TestMinimalJSONSchemaTypes(t *testing.T) {
	cases := []struct {
		name    string
		schema  string
		value   string
		wantErr bool
	}{
		{"object ok", `{"type":"object"}`, `{}`, false},
		{"object mismatch", `{"type":"object"}`, `[]`, true},
		{"array ok", `{"type":"array"}`, `[1]`, false},
		{"array mismatch", `{"type":"array"}`, `{}`, true},
		{"string ok", `{"type":"string"}`, `"x"`, false},
		{"string mismatch", `{"type":"string"}`, `3`, true},
		{"number ok", `{"type":"number"}`, `3.5`, false},
		{"integer ok", `{"type":"integer"}`, `3`, false},
		{"integer rejects fraction", `{"type":"integer"}`, `3.5`, true},
		{"boolean ok", `{"type":"boolean"}`, `true`, false},
		{"null ok", `{"type":"null"}`, `null`, false},
		{"null mismatch", `{"type":"null"}`, `0`, true},
		{"unknown type ignored", `{"type":"date-time"}`, `"anything"`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := mustValidateSubset(t, tc.schema, tc.value)
			if (err != nil) != tc.wantErr {
				t.Fatalf("Validate() error = %v, wantErr %t", err, tc.wantErr)
			}
		})
	}
}

func TestMinimalJSONSchemaObjectKeywords(t *testing.T) {
	schema := `{
		"type": "object",
		"properties": {
			"name": {"type": "string"},
			"count": {"type": "integer"},
			"level": {"enum": ["low", "high", 3]}
		},
		"required": ["name"],
		"additionalProperties": false
	}`

	if err := mustValidateSubset(t, schema, `{"name":"a","count":2,"level":"low"}`); err != nil {
		t.Errorf("valid object rejected: %v", err)
	}
	if err := mustValidateSubset(t, schema, `{"name":"a","level":3}`); err != nil {
		t.Errorf("numeric enum member rejected: %v", err)
	}
	if err := mustValidateSubset(t, schema, `{"count":2}`); err == nil {
		t.Error("missing required property accepted")
	}
	if err := mustValidateSubset(t, schema, `{"name":"a","extra":true}`); err == nil {
		t.Error("additional property accepted despite additionalProperties=false")
	}
	if err := mustValidateSubset(t, schema, `{"name":"a","level":"medium"}`); err == nil {
		t.Error("value outside enum accepted")
	}
	if err := mustValidateSubset(t, schema, `{"name":3}`); err == nil {
		t.Error("wrong property type accepted")
	}
}

func TestMinimalJSONSchemaAdditionalPropertiesDefaultsOpen(t *testing.T) {
	schema := `{"type":"object","properties":{"a":{"type":"string"}}}`
	if err := mustValidateSubset(t, schema, `{"a":"x","b":1}`); err != nil {
		t.Errorf("open object rejected extra property: %v", err)
	}
}

func TestMinimalJSONSchemaItems(t *testing.T) {
	schema := `{"type":"array","items":{"type":"object","required":["id"]}}`
	if err := mustValidateSubset(t, schema, `[{"id":1},{"id":2}]`); err != nil {
		t.Errorf("valid array rejected: %v", err)
	}
	if err := mustValidateSubset(t, schema, `[{"id":1},{}]`); err == nil {
		t.Error("array element missing required property accepted")
	}
}

func TestMinimalJSONSchemaCompositionBoundsAndConditionals(t *testing.T) {
	schema := `{
		"type":"object",
		"required":["status","attempts","tags"],
		"properties":{
			"status":{"enum":["ready","blocked"]},
			"attempts":{"type":"integer","minimum":1,"maximum":3},
			"tags":{"type":"array","minItems":1,"maxItems":2,"items":{"type":"string","minLength":2}},
			"reason":{"type":"string","minLength":1}
		},
		"allOf":[
			{"if":{"required":["status"],"properties":{"status":{"const":"blocked"}}},"then":{"required":["reason"]}}
		]
	}`
	if err := mustValidateSubset(t, schema, `{"status":"ready","attempts":1,"tags":["ok"]}`); err != nil {
		t.Fatalf("valid composed schema rejected: %v", err)
	}
	for _, value := range []string{
		`{"status":"blocked","attempts":1,"tags":["ok"]}`,
		`{"status":"ready","attempts":0,"tags":["ok"]}`,
		`{"status":"ready","attempts":1,"tags":[]}`,
		`{"status":"ready","attempts":1,"tags":["x"]}`,
	} {
		if err := mustValidateSubset(t, schema, value); err == nil {
			t.Errorf("expected composed schema error for %s", value)
		}
	}
	if err := mustValidateSubset(t, `{"oneOf":[{"const":"a"},{"const":"b"}]}`, `"b"`); err != nil {
		t.Errorf("oneOf rejected one matching branch: %v", err)
	}
	if err := mustValidateSubset(t, `{"anyOf":[{"const":"a"},{"const":"b"}]}`, `"c"`); err == nil {
		t.Error("anyOf accepted a value matching no branch")
	}
	if err := mustValidateSubset(t, `{"not":{"const":"forbidden"}}`, `"forbidden"`); err == nil {
		t.Error("not accepted a disallowed value")
	}
}

func TestMinimalJSONSchemaIgnoresUnsupportedKeywords(t *testing.T) {
	schema := `{
			"type": "string",
			"pattern": "^[0-9]+$",
			"format": "uuid"
		}`
	if err := mustValidateSubset(t, schema, `"abc"`); err != nil {
		t.Errorf("unsupported keywords must be ignored, got: %v", err)
	}
}
