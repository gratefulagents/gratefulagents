package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
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
	// Collaboration / lifecycle fields.
	Assignee              string
	AcceptedRiskExpiresAt *time.Time
	TicketURL             string
	TicketProvider        string
	// BaselineState is one of the SecurityFindingBaseline* constants, or ""
	// for legacy rows written before observation tracking existed.
	BaselineState string
	ResolvedAt    *time.Time
	// TriagedAt is when the finding first left status "open".
	TriagedAt *time.Time
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
	BaselineState     string
	Assignee          string
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

// Baseline states classify a finding relative to the previous scan run.
// They are computed at write time: UpsertSecurityFinding classifies
// insertions and reobservations, and FinalizeSecurityScanBaseline marks
// findings absent from a completed run as resolved.
const (
	SecurityFindingBaselineNew       = "new"
	SecurityFindingBaselineRecurring = "recurring"
	SecurityFindingBaselineRegressed = "regressed"
	SecurityFindingBaselineResolved  = "resolved"
	SecurityFindingBaselineReopened  = "reopened"
)

// ValidSecurityFindingBaselineState reports whether s is a known baseline
// state.
func ValidSecurityFindingBaselineState(s string) bool {
	switch s {
	case SecurityFindingBaselineNew,
		SecurityFindingBaselineRecurring,
		SecurityFindingBaselineRegressed,
		SecurityFindingBaselineResolved,
		SecurityFindingBaselineReopened:
		return true
	}
	return false
}

// ValidateSecurityTicketURL rejects anything that is not an absolute
// http(s) URL so a stored ticket link can never smuggle another scheme into
// the UI.
func ValidateSecurityTicketURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid ticket URL: %w", err)
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return errors.New("ticket URL must be an absolute http(s) URL")
	}
	return nil
}

// SecuritySavedFilter is one saved finding-filter query, scoped to
// (namespace, owner) and unique by name within that scope.
type SecuritySavedFilter struct {
	ID        uuid.UUID
	Namespace string
	Owner     string
	Name      string
	Query     json.RawMessage
	CreatedAt time.Time
	UpdatedAt time.Time
}

// SecurityFindingTrends aggregates triage/remediation velocity for a scan
// (or a whole namespace when scanName is empty). Durations are seconds.
type SecurityFindingTrends struct {
	TriagedCount                  int32
	ResolvedCount                 int32
	AvgTimeToTriageSeconds        float64
	MedianTimeToTriageSeconds     float64
	AvgTimeToResolutionSeconds    float64
	MedianTimeToResolutionSeconds float64
}

// SecurityFindingBulkUpdate describes the changes a bulk triage operation
// applies to each selected finding. Nil pointers mean "leave unchanged"; a
// pointer to the empty string clears the assignee.
type SecurityFindingBulkUpdate struct {
	Status                *string
	Assignee              *string
	AcceptedRiskExpiresAt *time.Time
	Note                  string
	Actor                 string
}

// BulkSecurityFindingError identifies the finding that aborted an atomic
// bulk update. The whole batch is rolled back when it is returned.
type BulkSecurityFindingError struct {
	FindingID uuid.UUID
	Err       error
}

func (e *BulkSecurityFindingError) Error() string {
	return "finding " + e.FindingID.String() + ": " + e.Err.Error()
}

func (e *BulkSecurityFindingError) Unwrap() error { return e.Err }

// SecurityFindingAuditRecord is one exported audit-trail entry joined with
// identifying finding fields.
type SecurityFindingAuditRecord struct {
	Event       SecurityFindingEvent
	FindingID   uuid.UUID
	Fingerprint string
	Title       string
	Severity    string
	Status      string
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
	// (run_name, session_id, scan_id included), and a "reobserved" event is
	// appended. Triage status is preserved, EXCEPT: a "fixed" finding that
	// reappears regresses to open, and a false_positive/accepted_risk
	// finding regresses to open only when the evidence materially changed
	// (severity increased; the fingerprint pins evidence identity, so equal
	// fingerprints with equal-or-lower severity keep the suppression). The
	// row's baseline_state is classified (new / recurring / regressed /
	// reopened) and one observation row is recorded for the run in the same
	// transaction. The bool reports whether a new row was created.
	UpsertSecurityFinding(ctx context.Context, rec *SecurityFindingRecord) (*SecurityFindingRecord, bool, error)
	// ListSecurityFindings lists findings matching the filter, ordered by
	// score desc, severity desc, last_seen_at desc. Limit defaults to 200
	// and is capped at 1000.
	ListSecurityFindings(ctx context.Context, f SecurityFindingFilter) ([]SecurityFindingRecord, error)
	// GetSecurityFinding returns (nil, nil) when the finding does not exist
	// in the namespace. An empty namespace is rejected with an error.
	GetSecurityFinding(ctx context.Context, namespace string, id uuid.UUID) (*SecurityFindingRecord, error)
	// SetSecurityFindingStatus validates the status, updates the finding in
	// the namespace, and appends a status-change event. When the new status
	// is accepted_risk an optional expiry may be recorded; any other status
	// rejects a non-nil expiry and clears a previously stored one. The first
	// transition out of "open" stamps triaged_at. It returns
	// ErrSecurityFindingNotFound when no finding matches. An empty namespace
	// is rejected with an error.
	SetSecurityFindingStatus(ctx context.Context, namespace string, id uuid.UUID, status, actor, note string, acceptedRiskExpiresAt *time.Time) error
	// SetSecurityFindingAssignee sets (or clears, with "") the finding's
	// assignee and appends an "assignee_changed" event with from/to detail.
	SetSecurityFindingAssignee(ctx context.Context, namespace string, id uuid.UUID, assignee, actor string) error
	// SetSecurityFindingTicket links (non-empty http(s) URL) or unlinks
	// (empty URL) an external ticket, appending a "ticket_linked" or
	// "ticket_unlinked" event.
	SetSecurityFindingTicket(ctx context.Context, namespace string, id uuid.UUID, ticketURL, provider, actor string) error
	// ExpireAcceptedRisks flips accepted_risk findings whose expiry has
	// passed back to open, appending an "accepted_risk_expired" event per
	// finding. It is idempotent and namespace-scoped; it returns the number
	// of findings expired.
	ExpireAcceptedRisks(ctx context.Context, namespace string) (int32, error)
	// BulkUpdateSecurityFindings applies upd to every finding in ids inside
	// one transaction, scoped to (namespace, scanName). The batch is fully
	// atomic: when any id is missing, a duplicate child, or otherwise
	// invalid, the whole transaction is rolled back and a
	// *BulkSecurityFindingError identifies the offending finding. Every
	// applied change is audited per finding.
	BulkUpdateSecurityFindings(ctx context.Context, namespace, scanName string, ids []uuid.UUID, upd SecurityFindingBulkUpdate) error
	// FinalizeSecurityScanBaseline marks non-duplicate findings of the run's
	// scan that were NOT observed by that run as baseline-resolved (setting
	// resolved_at and appending a "resolved" event). It only acts when the
	// scan record exists and is completed, and when no newer run of the same
	// scan has recorded observations; callers must only invoke it for runs
	// that terminated successfully. Idempotent; returns the number of
	// findings newly resolved.
	FinalizeSecurityScanBaseline(ctx context.Context, namespace, runName string) (int32, error)
	// ListSecuritySavedFilters lists the caller's saved filters in the
	// namespace, ordered by name.
	ListSecuritySavedFilters(ctx context.Context, namespace, owner string) ([]SecuritySavedFilter, error)
	// SaveSecuritySavedFilter inserts or replaces the filter keyed by
	// (namespace, owner, name) and returns the stored record.
	SaveSecuritySavedFilter(ctx context.Context, rec *SecuritySavedFilter) (*SecuritySavedFilter, error)
	// DeleteSecuritySavedFilter removes the named filter owned by owner.
	// Deleting a filter that does not exist is not an error.
	DeleteSecuritySavedFilter(ctx context.Context, namespace, owner, name string) error
	// GetSecurityFindingTrends aggregates time-to-triage (triaged_at -
	// first_seen_at) and time-to-resolution (resolved_at - first_seen_at)
	// over non-duplicate findings, optionally scoped to one scan.
	GetSecurityFindingTrends(ctx context.Context, namespace, scanName string) (*SecurityFindingTrends, error)
	// ExportSecurityFindingEvents returns every audit event for the scan's
	// findings joined with identifying finding fields, ordered by event time
	// then id, capped at limit (default 10000).
	ExportSecurityFindingEvents(ctx context.Context, namespace, scanName string, limit int32) ([]SecurityFindingAuditRecord, error)
	// ListSecurityFindingEvents lists the events of a finding in the
	// namespace, newest first. An empty namespace is rejected with an error.
	ListSecurityFindingEvents(ctx context.Context, namespace string, id uuid.UUID, limit int32) ([]SecurityFindingEvent, error)
	// AddSecurityFindingComment appends a "comment" event to the finding's
	// audit trail and returns the stored event. It returns
	// ErrSecurityFindingNotFound when no finding matches in the namespace.
	// An empty namespace is rejected with an error.
	AddSecurityFindingComment(ctx context.Context, namespace string, id uuid.UUID, actor, body string) (*SecurityFindingEvent, error)
	// SummarizeSecurityFindings returns counts of non-duplicate findings
	// keyed by severity ("critical", "high", "medium", "low", "info"), plus
	// "total" (all findings), "open" (findings with status 'open'), and
	// "open_<severity>" keys (open_critical, open_high, open_medium,
	// open_low, open_info) counting only findings whose status is 'open'.
	// It also emits "baseline_<state>" keys (baseline_new,
	// baseline_recurring, baseline_regressed, baseline_resolved,
	// baseline_reopened) plus "baseline_tracked" counting findings with any
	// baseline state. Empty scanName / runName match all.
	SummarizeSecurityFindings(ctx context.Context, namespace, scanName, runName string) (map[string]int32, error)
	// DeleteSecurityScanData removes every scan run, finding, and event for
	// (namespace, scan_name). Idempotent. It is called when a SecurityScan
	// resource is deleted.
	DeleteSecurityScanData(ctx context.Context, namespace, scanName string) error
}
