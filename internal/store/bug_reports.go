package store

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
)

// Agent bug report categories. A "bug" is something concretely broken (a tool
// failure, a controller misbehavior); a "complaint" is friction or a papercut
// worth surfacing without a hard failure; a "feature" is a request for a
// capability the platform lacks.
const (
	AgentBugReportCategoryBug       = "bug"
	AgentBugReportCategoryComplaint = "complaint"
	AgentBugReportCategoryFeature   = "feature"
)

// Agent bug report triage statuses.
const (
	AgentBugReportStatusOpen         = "open"
	AgentBugReportStatusAcknowledged = "acknowledged"
	AgentBugReportStatusResolved     = "resolved"
	AgentBugReportStatusDismissed    = "dismissed"
)

// ErrAgentBugReportNotFound is returned when a bug report does not exist.
var ErrAgentBugReportNotFound = errors.New("agent bug report not found")

// ValidAgentBugReportCategory reports whether c is a known category.
func ValidAgentBugReportCategory(c string) bool {
	switch c {
	case AgentBugReportCategoryBug, AgentBugReportCategoryComplaint,
		AgentBugReportCategoryFeature:
		return true
	}
	return false
}

// ValidAgentBugReportStatus reports whether s is a known triage status.
func ValidAgentBugReportStatus(s string) bool {
	switch s {
	case AgentBugReportStatusOpen, AgentBugReportStatusAcknowledged,
		AgentBugReportStatusResolved, AgentBugReportStatusDismissed:
		return true
	}
	return false
}

// AgentBugReportRecord is one deduplicated agent-filed platform bug report or
// complaint, unique per (namespace, fingerprint). RunName and SessionID track
// the most recent run that observed the problem.
type AgentBugReportRecord struct {
	ID          uuid.UUID
	Namespace   string
	RunName     string
	SessionID   *uuid.UUID
	Category    string
	ToolName    string
	Title       string
	Body        string
	Fingerprint string
	Occurrences int32
	Status      string
	StatusNote  string
	StatusActor string
	FirstSeenAt time.Time
	LastSeenAt  time.Time
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// AgentBugReportFilter narrows ListAgentBugReports. Zero values mean "no
// constraint" except Namespace, which is required.
type AgentBugReportFilter struct {
	Namespace string
	Status    string
	Category  string
	Limit     int32
}

// AgentBugReportStore persists agent-filed platform bug reports across runs.
type AgentBugReportStore interface {
	// UpsertAgentBugReport inserts the report or, when the
	// (namespace, fingerprint) key already exists, merges into the existing
	// row: occurrences and last_seen_at are bumped and the latest run's
	// run_name, session_id, and body are kept. A previously resolved report
	// that reoccurs regresses to open (dismissed reports stay dismissed).
	// The bool reports whether a new row was created.
	UpsertAgentBugReport(ctx context.Context, rec *AgentBugReportRecord) (*AgentBugReportRecord, bool, error)
	// GetAgentBugReport returns (nil, nil) when the report does not exist.
	GetAgentBugReport(ctx context.Context, namespace string, id uuid.UUID) (*AgentBugReportRecord, error)
	// ListAgentBugReports lists reports matching the filter, most recently
	// seen first.
	ListAgentBugReports(ctx context.Context, f AgentBugReportFilter) ([]AgentBugReportRecord, error)
	// SetAgentBugReportStatus updates the triage status of one report.
	SetAgentBugReportStatus(ctx context.Context, namespace string, id uuid.UUID, status, actor, note string) error
}
