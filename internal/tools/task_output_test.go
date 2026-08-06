package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

const taskOutputTestSchema = `{
	"type": "object",
	"properties": {
		"endpoints": {"type": "array", "items": {"type": "string"}},
		"summary": {"type": "string"}
	},
	"required": ["endpoints"],
	"additionalProperties": false
}`

func newTaskOutputTestRegistry(t *testing.T, schema string) (*Registry, *[]string) {
	t.Helper()
	r := NewRegistry(t.TempDir(), WithReadOnlyTools())
	var persisted []string
	err := RegisterTaskOutputTool(r, schema, func(_ context.Context, outputJSON string) error {
		persisted = append(persisted, outputJSON)
		return nil
	})
	if err != nil {
		t.Fatalf("RegisterTaskOutputTool: %v", err)
	}
	return r, &persisted
}

func executeTaskOutput(t *testing.T, r *Registry, input string) Result {
	t.Helper()
	tool := r.Get("submit_task_output")
	if tool == nil {
		t.Fatal("submit_task_output not registered")
	}
	result, err := tool.Execute(context.Background(), json.RawMessage(input), "")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	return result
}

func TestRegisterTaskOutputToolRejectsInvalidSchema(t *testing.T) {
	r := NewRegistry(t.TempDir())
	err := RegisterTaskOutputTool(r, "not a schema", func(context.Context, string) error { return nil })
	if err == nil {
		t.Fatal("expected error for invalid schema")
	}
	if r.Get("submit_task_output") != nil {
		t.Fatal("tool must not be registered when the schema is invalid")
	}
}

func TestTaskOutputToolSurvivesReadOnlyRegistry(t *testing.T) {
	r, _ := newTaskOutputTestRegistry(t, taskOutputTestSchema)
	if r.Get("submit_task_output") == nil {
		t.Fatal("submit_task_output must be registered in a read-only registry")
	}
}

func TestTaskOutputToolPersistsValidOutput(t *testing.T) {
	r, persisted := newTaskOutputTestRegistry(t, taskOutputTestSchema)
	result := executeTaskOutput(t, r, `{"output":{"endpoints":["/a","/b"],"summary":"two"}}`)
	if result.IsError {
		t.Fatalf("valid output rejected: %s", result.Content)
	}
	if len(*persisted) != 1 {
		t.Fatalf("persist calls = %d, want 1", len(*persisted))
	}
	var roundTrip map[string]any
	if err := json.Unmarshal([]byte((*persisted)[0]), &roundTrip); err != nil {
		t.Fatalf("persisted output is not JSON: %v", err)
	}
	if _, ok := roundTrip["endpoints"]; !ok {
		t.Fatalf("persisted output missing endpoints: %s", (*persisted)[0])
	}
}

func TestTaskOutputToolAcceptsJSONEncodedString(t *testing.T) {
	r, persisted := newTaskOutputTestRegistry(t, taskOutputTestSchema)
	input, err := json.Marshal(map[string]any{"output": `{"endpoints":[]}`})
	if err != nil {
		t.Fatal(err)
	}
	result := executeTaskOutput(t, r, string(input))
	if result.IsError {
		t.Fatalf("JSON-encoded string output rejected: %s", result.Content)
	}
	if len(*persisted) != 1 {
		t.Fatalf("persist calls = %d, want 1", len(*persisted))
	}
}

func TestTaskOutputToolLastWriteWins(t *testing.T) {
	r, persisted := newTaskOutputTestRegistry(t, taskOutputTestSchema)
	executeTaskOutput(t, r, `{"output":{"endpoints":["/a"]}}`)
	executeTaskOutput(t, r, `{"output":{"endpoints":["/b"]}}`)
	if len(*persisted) != 2 {
		t.Fatalf("persist calls = %d, want 2", len(*persisted))
	}
	if !strings.Contains((*persisted)[1], "/b") {
		t.Fatalf("second submission not persisted last: %v", *persisted)
	}
}

func TestTaskOutputToolRejectsSchemaViolations(t *testing.T) {
	r, persisted := newTaskOutputTestRegistry(t, taskOutputTestSchema)
	for name, input := range map[string]string{
		"missing required":    `{"output":{"summary":"no endpoints"}}`,
		"wrong type":          `{"output":{"endpoints":"not an array"}}`,
		"additional property": `{"output":{"endpoints":[],"extra":1}}`,
		"missing output":      `{}`,
	} {
		result := executeTaskOutput(t, r, input)
		if !result.IsError {
			t.Errorf("%s: expected schema rejection, got: %s", name, result.Content)
		}
	}
	if len(*persisted) != 0 {
		t.Fatalf("rejected outputs were persisted: %v", *persisted)
	}
}

func TestTaskOutputToolRejectsOversizedOutput(t *testing.T) {
	r, persisted := newTaskOutputTestRegistry(t, taskOutputTestSchema)
	big := strings.Repeat("x", maxTaskOutputBytes)
	input, err := json.Marshal(map[string]any{"output": map[string]any{"endpoints": []string{big}}})
	if err != nil {
		t.Fatal(err)
	}
	result := executeTaskOutput(t, r, string(input))
	if !result.IsError || !strings.Contains(result.Content, "too large") {
		t.Fatalf("oversized output not rejected: %s", result.Content)
	}
	if len(*persisted) != 0 {
		t.Fatal("oversized output was persisted")
	}
}

func TestTaskOutputToolSurfacesPersistErrors(t *testing.T) {
	r := NewRegistry(t.TempDir())
	err := RegisterTaskOutputTool(r, taskOutputTestSchema, func(context.Context, string) error {
		return errors.New("boom")
	})
	if err != nil {
		t.Fatal(err)
	}
	result := executeTaskOutput(t, r, `{"output":{"endpoints":[]}}`)
	if !result.IsError || !strings.Contains(result.Content, "boom") {
		t.Fatalf("persist error not surfaced: %s", result.Content)
	}
}

func TestTaskOutputToolDescriptionMentionsDownstreamConsumption(t *testing.T) {
	r, _ := newTaskOutputTestRegistry(t, taskOutputTestSchema)
	desc := r.Get("submit_task_output").Description()
	for _, marker := range []string{"downstream", "typed", "overwrites", "endpoints"} {
		if !strings.Contains(desc, marker) {
			t.Errorf("description must mention %q", marker)
		}
	}
}

func TestTaskOutputToolKeepsSchemaValidStringWithoutUnwrapping(t *testing.T) {
	r, persisted := newTaskOutputTestRegistry(t, `{"type":"string"}`)
	// "123" parses as a JSON number, but the raw string already satisfies the
	// schema, so it must not be unwrapped and coerced.
	result := executeTaskOutput(t, r, `{"output":"123"}`)
	if result.IsError {
		t.Fatalf("schema-valid string rejected: %s", result.Content)
	}
	if len(*persisted) != 1 || (*persisted)[0] != `"123"` {
		t.Fatalf("persisted = %#v, want the raw string \"123\"", *persisted)
	}

	// A JSON-encoded object string still passes as a plain string when the
	// schema wants a string.
	result = executeTaskOutput(t, r, `{"output":"{\"a\":1}"}`)
	if result.IsError {
		t.Fatalf("schema-valid encoded-object string rejected: %s", result.Content)
	}
	if len(*persisted) != 2 || (*persisted)[1] != `"{\"a\":1}"` {
		t.Fatalf("persisted = %#v, want the raw encoded-object string", *persisted)
	}
}
