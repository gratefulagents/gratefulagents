package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/gratefulagents/sdk/pkg/agentsdk"

	"github.com/gratefulagents/gratefulagents/internal/store"
)

const reportBugToolName = "report_bug"

// RegisterReportBugTool registers the report_bug tool, which lets any agent
// file a durable platform/tooling bug report, complaint, or feature request
// in the platform database. Reports persist across runs, deduplicate by
// fingerprint (a reoccurrence bumps the occurrence count), and surface in the
// dashboard for humans to triage. A nil store (no Postgres) leaves the tool
// unregistered.
func RegisterReportBugTool(registry *Registry, reportStore store.AgentBugReportStore, namespace, runName string, sessionID uuid.UUID) {
	if registry == nil || reportStore == nil {
		return
	}
	registry.Register(&reportBugTool{
		store:     reportStore,
		namespace: namespace,
		runName:   runName,
		sessionID: sessionID,
	})
}

type reportBugTool struct {
	store     store.AgentBugReportStore
	namespace string
	runName   string
	sessionID uuid.UUID
}

type reportBugInput struct {
	Title    string `json:"title"`
	Body     string `json:"body"`
	Category string `json:"category"`
	ToolName string `json:"tool_name"`
}

func (t *reportBugTool) Name() string { return reportBugToolName }

func (t *reportBugTool) Description() string {
	return "File a durable bug report, complaint, or feature request about the agent platform " +
		"or its tooling (for example a tool that fails repeatedly or behaves incorrectly). " +
		"Reports are stored in the platform database across all runs and reviewed by humans, " +
		"who may choose to fix them. Repeated reports of the same problem are deduplicated " +
		"into one record with an occurrence count. Use this ONLY for platform/harness issues " +
		"— never for problems in the user's repository or the task you are working on."
}

func (t *reportBugTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"title": {
				"type": "string",
				"minLength": 8,
				"maxLength": 160,
				"description": "Short, stable summary of the problem (used for deduplication across runs)"
			},
			"body": {
				"type": "string",
				"minLength": 20,
				"maxLength": 8000,
				"description": "Details: what you attempted, expected vs actual behavior, exact error output, and any workaround. Do not include secrets."
			},
			"category": {
				"type": "string",
				"enum": ["bug", "complaint", "feature"],
				"description": "bug = something is broken; complaint = friction or papercut; feature = missing capability. Defaults to bug."
			},
			"tool_name": {
				"type": "string",
				"maxLength": 120,
				"description": "Name of the affected tool, if the report is about a specific tool (e.g. ApplyPatch)"
			}
		},
		"required": ["title", "body"]
	}`)
}

func (t *reportBugTool) IsReadOnly() bool                      { return false }
func (t *reportBugTool) IsEnabled(_ *agentsdk.RunContext) bool { return true }
func (t *reportBugTool) NeedsApproval() bool                   { return false }
func (t *reportBugTool) TimeoutSeconds() int                   { return 30 }

func (t *reportBugTool) Execute(ctx context.Context, input json.RawMessage, _ string) (Result, error) {
	var in reportBugInput
	if err := json.Unmarshal(input, &in); err != nil {
		return Result{Content: fmt.Sprintf("invalid input: %v", err), IsError: true}, nil
	}
	title := strings.TrimSpace(in.Title)
	body := strings.TrimSpace(in.Body)
	if len(title) < 8 || len(title) > 160 {
		return Result{Content: "title must be between 8 and 160 characters", IsError: true}, nil
	}
	if len(body) < 20 || len(body) > 8000 {
		return Result{Content: "body must be between 20 and 8000 characters", IsError: true}, nil
	}
	category := strings.TrimSpace(strings.ToLower(in.Category))
	if category == "" {
		category = store.AgentBugReportCategoryBug
	}
	if !store.ValidAgentBugReportCategory(category) {
		return Result{Content: fmt.Sprintf("invalid category %q (want bug, complaint, or feature)", in.Category), IsError: true}, nil
	}
	toolName := strings.TrimSpace(in.ToolName)
	if len(toolName) > 120 {
		return Result{Content: "tool_name must be at most 120 characters", IsError: true}, nil
	}

	rec := &store.AgentBugReportRecord{
		Namespace:   t.namespace,
		RunName:     t.runName,
		Category:    category,
		ToolName:    toolName,
		Title:       title,
		Body:        body,
		Fingerprint: BugReportFingerprint(category, toolName, title),
	}
	if t.sessionID != uuid.Nil {
		sid := t.sessionID
		rec.SessionID = &sid
	}
	stored, created, err := t.store.UpsertAgentBugReport(ctx, rec)
	if err != nil {
		return Result{Content: fmt.Sprintf("storing bug report: %v", err), IsError: true}, nil
	}
	payload, _ := json.Marshal(map[string]any{
		"id":          stored.ID.String(),
		"created":     created,
		"duplicate":   !created,
		"occurrences": stored.Occurrences,
		"status":      stored.Status,
	})
	return Result{Content: string(payload)}, nil
}

// BugReportFingerprint derives the cross-run deduplication key for a report.
// The title is lowercased and whitespace-collapsed so cosmetic rephrasings of
// the same stable title still merge.
func BugReportFingerprint(category, toolName, title string) string {
	normTitle := strings.Join(strings.Fields(strings.ToLower(title)), " ")
	sum := sha256.Sum256([]byte(category + "\x00" + strings.ToLower(toolName) + "\x00" + normTitle))
	return hex.EncodeToString(sum[:])
}
