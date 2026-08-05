package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
)

// ErrSecurityFindingNotFound is returned when a finding lookup or update
// matches no row in the caller's namespace.
var ErrSecurityFindingNotFound = errors.New("security finding not found")

// SecurityScanRecord is one security scan run for a repository, unique per
// (namespace, run_name).
type SecurityScanRecord struct {
	ID          uuid.UUID
	Namespace   string
	ScanName    string
	RunName     string
	SessionID   *uuid.UUID
	Repository  string
	Revision    string
	Status      string
	Summary     string
	StartedAt   *time.Time
	CompletedAt *time.Time
	Counts      map[string]int32
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// SecurityFindingRecord is one deduplicated security finding, unique per
// (namespace, scan_name, repository, fingerprint).
type SecurityFindingRecord struct {
	ID           uuid.UUID
	ScanID       uuid.UUID
	Namespace    string
	ScanName     string
	RunName      string
	SessionID    *uuid.UUID
	Fingerprint  string
	Title        string
	Category     string
	Severity     string
	Confidence   string
	Repository   string
	Revision     string
	FilePath     string
	StartLine    int32
	EndLine      int32
	Symbol       string
	CWE          []string
	Description  string
	Impact       string
	AttackVector string
	Remediation  string
	References   []string
	SourceAgent  string
	ScanStep     string
	Score        float64
	Status       string
	DuplicateOf  *uuid.UUID
	Occurrences  int32
	Raw          json.RawMessage
	FirstSeenAt  time.Time
	LastSeenAt   time.Time
}

// SecurityFindingFilter selects findings for listing. Zero-value string
// fields are not filtered on.
type SecurityFindingFilter struct {
	Namespace         string
	ScanName          string
	RunName           string
	Repository        string
	Category          string
	Severity          string
	Status            string
	Search            string
	MinScore          float64
	IncludeDuplicates bool
	Limit             int32
	Offset            int32
}

// SecurityFindingEvent is one audit-trail entry for a finding.
type SecurityFindingEvent struct {
	ID        int64
	FindingID uuid.UUID
	EventType string
	Actor     string
	Note      string
	Detail    json.RawMessage
	CreatedAt time.Time
}

const (
	SecurityFindingStatusOpen          = "open"
	SecurityFindingStatusTriaged       = "triaged"
	SecurityFindingStatusConfirmed     = "confirmed"
	SecurityFindingStatusFalsePositive = "false_positive"
	SecurityFindingStatusFixed         = "fixed"
	SecurityFindingStatusAcceptedRisk  = "accepted_risk"
)

// ValidSecurityFindingStatus reports whether s is a known finding status.
func ValidSecurityFindingStatus(s string) bool {
	switch s {
	case SecurityFindingStatusOpen,
		SecurityFindingStatusTriaged,
		SecurityFindingStatusConfirmed,
		SecurityFindingStatusFalsePositive,
		SecurityFindingStatusFixed,
		SecurityFindingStatusAcceptedRisk:
		return true
	}
	return false
}

// SecurityFindingStore persists security scans, findings, and finding events.
type SecurityFindingStore interface {
	// UpsertSecurityScan inserts or updates the scan keyed by
	// (namespace, run_name) and returns the stored record.
	UpsertSecurityScan(ctx context.Context, rec *SecurityScanRecord) (*SecurityScanRecord, error)
	// GetSecurityScan returns (nil, nil) when the scan does not exist.
	GetSecurityScan(ctx context.Context, namespace, runName string) (*SecurityScanRecord, error)
	// ListSecurityScans lists scans in a namespace, optionally filtered by
	// scan name, newest first.
	ListSecurityScans(ctx context.Context, namespace, scanName string, limit int32) ([]SecurityScanRecord, error)
	// UpsertSecurityFinding inserts the finding or, when the
	// (namespace, scan_name, repository, fingerprint) key already exists,
	// merges into the existing row: occurrences and last_seen_at are bumped,
	// the highest severity and score win, mutable fields are refreshed
	// (run_name, session_id, scan_id included), triage status is preserved,
	// and a "reobserved" event is appended. The bool reports whether a new
	// row was created.
	UpsertSecurityFinding(ctx context.Context, rec *SecurityFindingRecord) (*SecurityFindingRecord, bool, error)
	// ListSecurityFindings lists findings matching the filter, ordered by
	// score desc, severity desc, last_seen_at desc. Limit defaults to 200
	// and is capped at 1000.
	ListSecurityFindings(ctx context.Context, f SecurityFindingFilter) ([]SecurityFindingRecord, error)
	// GetSecurityFinding returns (nil, nil) when the finding does not exist
	// in the namespace. An empty namespace is rejected with an error.
	GetSecurityFinding(ctx context.Context, namespace string, id uuid.UUID) (*SecurityFindingRecord, error)
	// SetSecurityFindingStatus validates the status, updates the finding in
	// the namespace, and appends a status-change event. It returns
	// ErrSecurityFindingNotFound when no finding matches. An empty namespace
	// is rejected with an error.
	SetSecurityFindingStatus(ctx context.Context, namespace string, id uuid.UUID, status, actor, note string) error
	// ListSecurityFindingEvents lists the events of a finding in the
	// namespace, newest first. An empty namespace is rejected with an error.
	ListSecurityFindingEvents(ctx context.Context, namespace string, id uuid.UUID, limit int32) ([]SecurityFindingEvent, error)
	// SummarizeSecurityFindings returns counts of non-duplicate findings
	// keyed by severity ("critical", "high", "medium", "low", "info"), plus
	// "total" (all findings), "open" (findings with status 'open'), and
	// "open_<severity>" keys (open_critical, open_high, open_medium,
	// open_low, open_info) counting only findings whose status is 'open'.
	// Empty scanName / runName match all.
	SummarizeSecurityFindings(ctx context.Context, namespace, scanName, runName string) (map[string]int32, error)
	// DeleteSecurityScanData removes every scan run, finding, and event for
	// (namespace, scan_name). Idempotent. It is called when a SecurityScan
	// resource is deleted.
	DeleteSecurityScanData(ctx context.Context, namespace, scanName string) error
}
