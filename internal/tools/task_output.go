package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	"github.com/gratefulagents/sdk/pkg/agentsdk"
)

// maxTaskOutputBytes caps the serialized structured output so it always fits
// the AgentRun status.structuredOutput field (MaxLength=65536).
const maxTaskOutputBytes = 64 * 1024

// StructuredOutputPersister persists the validated, serialized task output to
// the run's AgentRun status.structuredOutput. cmd/agent wires this to its
// status patcher so the tool never holds a raw cluster client.
type StructuredOutputPersister func(ctx context.Context, outputJSON string) error

// RegisterTaskOutputTool registers submit_task_output, the typed-result sink
// for deterministic workflow task runs. It is only called when the run
// carries a task output schema (AGENTRUN_TASK_OUTPUT_SCHEMA); schemaJSON must
// parse as a JSON object or registration fails.
func RegisterTaskOutputTool(registry *Registry, schemaJSON string, persist StructuredOutputPersister) error {
	if registry == nil || persist == nil {
		return fmt.Errorf("registry and persister are required")
	}
	schema, err := parseMinimalJSONSchema(schemaJSON)
	if err != nil {
		return fmt.Errorf("task output schema: %w", err)
	}
	registry.Register(&submitTaskOutputTool{schema: schema, schemaJSON: schemaJSON, persist: persist})
	return nil
}

type submitTaskOutputTool struct {
	schema     *minimalJSONSchema
	schemaJSON string
	persist    StructuredOutputPersister
}

type submitTaskOutputInput struct {
	Output json.RawMessage `json:"output"`
}

func (t *submitTaskOutputTool) Name() string { return "submit_task_output" }

func (t *submitTaskOutputTool) Description() string {
	return "Submit this task's typed result. The output is the task's structured, " +
		"machine-readable deliverable: it is validated against the task's output " +
		"schema, stored on the run, and consumed by downstream workflow tasks as " +
		"their input, so it must contain the actual data (not prose about it). " +
		"Task output schema:\n" + t.schemaJSON + "\n" +
		"Call it when the result is complete; calling it again overwrites the " +
		"previous submission (last write wins). Only writes platform run state " +
		"(no workspace, repository, or network mutation), so it is available on " +
		"read-only task runs."
}

func (t *submitTaskOutputTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"output": {
				"description": "The task's structured output as a JSON value matching the task output schema. Pass the object or array directly; a JSON-encoded string is parsed before validation."
			}
		},
		"required": ["output"]
	}`)
}

// IsReadOnly is true for the same reason as submit_security_scan_report: the
// tool only writes platform run state, never the workspace, repository, or
// network, so it must remain available on read-only task runs.
func (t *submitTaskOutputTool) IsReadOnly() bool                      { return true }
func (t *submitTaskOutputTool) IsEnabled(_ *agentsdk.RunContext) bool { return true }
func (t *submitTaskOutputTool) NeedsApproval() bool                   { return false }
func (t *submitTaskOutputTool) TimeoutSeconds() int                   { return 0 }

func (t *submitTaskOutputTool) Execute(ctx context.Context, input json.RawMessage, _ string) (Result, error) {
	var in submitTaskOutputInput
	if err := json.Unmarshal(input, &in); err != nil {
		return Result{Content: fmt.Sprintf("invalid input: %v", err), IsError: true}, nil
	}
	if len(in.Output) == 0 {
		return Result{Content: "output is required", IsError: true}, nil
	}

	var value any
	if err := json.Unmarshal(in.Output, &value); err != nil {
		return Result{Content: fmt.Sprintf("output is not valid JSON: %v", err), IsError: true}, nil
	}
	// Models often double-encode structured values; a string that fails
	// validation but itself parses as JSON that passes validation is
	// unwrapped. A string the schema accepts as-is stays a plain string, so
	// a "type":"string" schema is never surprised by outputs like "123".
	if s, ok := value.(string); ok && t.schema.Validate(value) != nil {
		var unwrapped any
		if err := json.Unmarshal([]byte(s), &unwrapped); err == nil && t.schema.Validate(unwrapped) == nil {
			value = unwrapped
		}
	}

	if err := t.schema.Validate(value); err != nil {
		return Result{Content: fmt.Sprintf("output does not match the task output schema: %v", err), IsError: true}, nil
	}

	serialized, err := json.Marshal(value)
	if err != nil {
		return Result{Content: fmt.Sprintf("failed to serialize output: %v", err), IsError: true}, nil
	}
	if len(serialized) > maxTaskOutputBytes {
		return Result{Content: fmt.Sprintf("output is too large: %d bytes serialized, limit is %d bytes; reduce it to the fields downstream tasks need", len(serialized), maxTaskOutputBytes), IsError: true}, nil
	}

	if err := t.persist(ctx, string(serialized)); err != nil {
		return Result{Content: fmt.Sprintf("failed to persist task output: %v", err), IsError: true}, nil
	}

	log.Printf("submit_task_output: persisted %d bytes of structured output", len(serialized))
	return Result{Content: fmt.Sprintf("Task output accepted and stored (%d bytes). It replaces any previous submission and will be passed to downstream workflow tasks.", len(serialized))}, nil
}
