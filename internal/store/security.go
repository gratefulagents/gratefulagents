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
	ID        uuid.UUID
	ScanID    uuid.UUID
	Namespace string
	ScanName  string
	RunName   string
	// ExecutionID groups every run of one deterministic execution (fan-out
	// instances and retries share it) and TaskName identifies the task
	// inside that execution, so findings can be aggregated per execution
	// instead of per run. On reobservation of an already stored
	// fingerprint the two behave differently: ExecutionID is always
	// re-stamped from the reporting run, because a reobservation is a
	// finding of the CURRENT execution and must appear in its report,
	// summaries; TaskName keeps its first non-empty value only within that
	// same execution and is re-stamped whenever the finding enters a new
	// execution.
	ExecutionID  string
	TaskName     string
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
	// Provenance: SourceKind is "agent" or "scanner"; Tool, ToolVersion,
	// and RuleID identify the deterministic scanner rule for scanner
	// findings and are empty for agent findings.
	SourceKind  string
	Tool        string
	ToolVersion string
	RuleID      string
	// CorrelatedFingerprints cross-references findings from the other
	// source kind that describe the same issue. Recording a correlation
	// never merges or rewrites either side.
	CorrelatedFingerprints []string
	Score                  float64
	Status                 string
	DuplicateOf            *uuid.UUID
	Occurrences            int32
	Raw                    json.RawMessage
	FirstSeenAt            time.Time
	LastSeenAt             time.Time
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
	// Governed suppression fields (SecurityPolicyPack suppression rules).
	// A finding is suppressed while SuppressedBy is non-empty: it is
	// excluded from failOnSeverity gating and default list/summary results
	// but never deleted, and every transition is audited.
	SuppressedBy         string
	SuppressedReason     string
	SuppressedOwner      string
	SuppressionExpiresAt *time.Time
	SuppressedAt         *time.Time
}

// SecurityFindingFilter selects findings for listing. Zero-value string
// fields are not filtered on.
type SecurityFindingFilter struct {
	Namespace string
	// ScanID narrows findings to one persisted scan record. This is distinct
	// from ScanName: deterministic executions share one scan record across
	// several task AgentRuns, each of which retains its own RunName.
	ScanID   uuid.UUID
	ScanName string
	RunName  string
	// ExecutionID and TaskName narrow to one deterministic execution and
	// one task within it; they match across every run of that execution.
	ExecutionID       string
	TaskName          string
	Repository        string
	Category          string
	Severity          string
	Status            string
	Search            string
	MinScore          float64
	IncludeDuplicates bool
	BaselineState     string
	Assignee          string
	// Suppressed controls how governed-suppressed findings are filtered:
	// SecuritySuppressedExclude (or "") omits them, SecuritySuppressedInclude
	// returns suppressed and unsuppressed findings alike, and
	// SecuritySuppressedOnly returns only suppressed findings.
	Suppressed string
	// ExcludedScanNames omits findings belonging to any of the named scans.
	// Callers use it to push per-user scan visibility into the query so
	// namespace-wide lists never leak another user's scan data.
	ExcludedScanNames []string
	Limit             int32
	Offset            int32
}

// Suppressed filter values for SecurityFindingFilter and the
// ListSecurityFindings RPC.
const (
	SecuritySuppressedExclude = "exclude"
	SecuritySuppressedInclude = "include"
	SecuritySuppressedOnly    = "only"
)

// ValidSecuritySuppressedFilter reports whether s is a known suppressed
// filter value ("" means exclude).
func ValidSecuritySuppressedFilter(s string) bool {
	switch s {
	case "", SecuritySuppressedExclude, SecuritySuppressedInclude, SecuritySuppressedOnly:
		return true
	}
	return false
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

// SecurityRunActivityPoint is one completed scan run's observed finding
// counts for a scan configuration, used for trend visualization.
type SecurityRunActivityPoint struct {
	RunName     string
	CompletedAt time.Time
	// SeverityCounts maps severity (critical/high/medium/low/info) to the
	// number of findings the run observed.
	SeverityCounts map[string]int32
	// Total is the number of findings the run observed.
	Total int32
}

// SecurityConfigPosture aggregates one scan configuration's stored posture:
// current deduplicated finding counts (same keys as
// SummarizeSecurityFindings), the latest persisted run's metadata, and
// recent completed-run activity ordered oldest first.
type SecurityConfigPosture struct {
	ScanName string
	Counts   map[string]int32
	// Repository is the repository of the configuration's latest run.
	Repository      string
	LastRunName     string
	LastRunStatus   string
	LastStartedAt   *time.Time
	LastCompletedAt *time.Time
	Activity        []SecurityRunActivityPoint
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

// SecuritySuppressionMatcher selects the findings a suppression rule
// applies to. All set fields must match (AND); at least one is required.
type SecuritySuppressionMatcher struct {
	// Category matches the finding's category exactly.
	Category string
	// CWE matches findings whose CWE list contains this identifier.
	CWE string
	// PathGlob matches the finding's file path with a glob pattern where
	// '*' matches any run of characters and '?' matches one character.
	PathGlob string
	// Fingerprint matches the finding's fingerprint exactly.
	Fingerprint string
}

// IsZero reports whether no matcher field is set.
func (m SecuritySuppressionMatcher) IsZero() bool {
	return m.Category == "" && m.CWE == "" && m.PathGlob == "" && m.Fingerprint == ""
}

// SecuritySuppressionRule is one governed suppression rule, typically
// "<policy pack name>/<rule name>".
type SecuritySuppressionRule struct {
	// ID identifies the rule; it is recorded on suppressed findings and in
	// their audit events.
	ID string
	// Reason documents why matched findings are suppressed.
	Reason string
	// Owner is who is accountable for the suppression.
	Owner string
	// Matcher selects the findings to suppress.
	Matcher SecuritySuppressionMatcher
	// ExpiresAt optionally bounds the suppression; past it,
	// ExpireSecuritySuppressions clears it.
	ExpiresAt *time.Time
}

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

// SecurityRetentionPolicy is a per-class retention configuration in days.
// 0 keeps the class forever. Cutoffs are computed against the row's most
// recent activity timestamp (last_seen_at for findings, completed_at for
// scan runs, created_at for audit events).
type SecurityRetentionPolicy struct {
	// ScanDays bounds completed scan run records and their per-run
	// observation rows. A scan run row is only deleted once no finding is
	// attributed to it anymore, so finding identity is never cascaded away.
	ScanDays int32
	// FindingDays bounds finding rows; expired rows are deleted together
	// with their audit events.
	FindingDays int32
	// ReportDays bounds the scan report artifacts (markdown and SARIF).
	ReportDays int32
	// EvidenceDays bounds finding evidence (code snippets/citations).
	// Expired evidence is REDACTED in place: the finding row, its identity,
	// and its audit history are preserved, and an "evidence_purged" audit
	// event is appended.
	EvidenceDays int32
	// PoCDays bounds proof-of-concept / attack-vector narratives. Expired
	// content is REDACTED in place like evidence, with a "poc_purged" audit
	// event.
	PoCDays int32
	// AuditEventDays bounds finding audit-trail events.
	AuditEventDays int32
}

// IsZero reports whether no retention class is configured.
func (p SecurityRetentionPolicy) IsZero() bool {
	return p == SecurityRetentionPolicy{}
}

// SecurityRetentionCounts reports how many rows one purge batch affected,
// per class.
type SecurityRetentionCounts struct {
	ScansDeleted       int32
	FindingsDeleted    int32
	ReportsDeleted     int32
	EvidenceRedacted   int32
	PoCsRedacted       int32
	AuditEventsDeleted int32
}

// IsZero reports whether the batch affected no rows.
func (c SecurityRetentionCounts) IsZero() bool {
	return c == SecurityRetentionCounts{}
}

// SecurityFindingSummaryScope selects which non-duplicate findings a
// summary aggregates. Empty string fields do not narrow the scope, so
// summaries can be taken per namespace, scan, run, deterministic execution,
// or a single task inside an execution.
type SecurityFindingSummaryScope struct {
	Namespace string
	// ScanID scopes a summary to one persisted scan record, including all
	// sibling task runs that reported into a deterministic execution.
	ScanID            uuid.UUID
	ScanName          string
	RunName           string
	ExecutionID       string
	TaskName          string
	IncludeSuppressed bool
	// ExcludedScanNames omits findings belonging to any of the named scans,
	// so a namespace-wide summary can respect per-user scan visibility.
	ExcludedScanNames []string
}

// SecurityFindingStore persists security scans, findings, and finding events.
type SecurityFindingStore interface {
	// UpsertSecurityScan inserts or updates the scan keyed by
	// (namespace, run_name) and returns the stored record.
	UpsertSecurityScan(ctx context.Context, rec *SecurityScanRecord) (*SecurityScanRecord, error)
	// GetSecurityScan returns (nil, nil) when the scan does not exist.
	GetSecurityScan(ctx context.Context, namespace, runName string) (*SecurityScanRecord, error)
	// ListSecurityScans lists scans in a namespace, optionally filtered by
	// scan name, newest first. Scans whose scan_name is in excludedScanNames
	// are filtered inside the query, so the limit counts only rows the
	// caller may see.
	ListSecurityScans(ctx context.Context, namespace, scanName string, limit int32, excludedScanNames []string) ([]SecurityScanRecord, error)
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
	// CorrelateSecurityFindings records that the two findings identified by
	// fingerprint within (namespace, scanName, repository) describe the
	// same issue: each side's correlated_fingerprints gains the other's
	// fingerprint and a "correlated" audit event is appended to any side
	// that changed. Neither row's content, provenance, or status is
	// otherwise touched — a correlated pair lists both sources instead of
	// one absorbing the other. Idempotent: re-correlating an already
	// cross-referenced pair changes nothing and appends no event. The bool
	// reports whether anything changed. It returns
	// ErrSecurityFindingNotFound when either fingerprint does not match a
	// finding; an empty namespace is rejected with an error.
	CorrelateSecurityFindings(ctx context.Context, namespace, scanName, repository, fingerprintA, fingerprintB, reason, actor string) (bool, error)
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
	// ApplySecuritySuppressions marks the scan's findings that match any of
	// the rules as suppressed (recording rule id, reason, owner, and expiry)
	// and appends a "suppressed" audit event per newly suppressed finding.
	// Findings are never deleted or erased. Already-suppressed findings are
	// left under their current rule, though a finding suppressed by the same
	// rule id has its reason/owner/expiry refreshed without a new event, so
	// re-running with edited rules converges. Idempotent; returns the number
	// of findings newly suppressed.
	ApplySecuritySuppressions(ctx context.Context, namespace, scanName string, rules []SecuritySuppressionRule) (int32, error)
	// ExpireSecuritySuppressions clears suppressions whose expiry has passed
	// and appends a "suppression_expired" event per finding. The finding row
	// and its audit history are preserved. Idempotent and namespace-scoped;
	// returns the number of suppressions expired.
	ExpireSecuritySuppressions(ctx context.Context, namespace string) (int32, error)
	// RevokeSecuritySuppressions clears suppressions on the scan's findings
	// whose governing rule was revoked: the finding's suppressed_by rule id
	// is absent from activeRules (rule deleted, pack swapped for another
	// pack, or the pack/policyPackRef removed entirely — pass no rules to
	// revoke every suppression on the scan), or the finding no longer
	// matches its rule's current matcher. Each revocation appends a
	// "suppression_revoked" event recording the previous rule, owner, and
	// reason; the finding row and its audit history are preserved.
	// Idempotent and scan-scoped; returns the number of suppressions
	// revoked.
	RevokeSecuritySuppressions(ctx context.Context, namespace, scanName string, activeRules []SecuritySuppressionRule) (int32, error)
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
	// excludedScanNames omits findings of the named scans from a
	// namespace-wide aggregation (per-user scan visibility).
	GetSecurityFindingTrends(ctx context.Context, namespace, scanName string, excludedScanNames []string) (*SecurityFindingTrends, error)
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
	// baseline state. It also reports "source_agent" / "source_scanner"
	// counts by finding provenance and "correlated" counting findings with
	// at least one recorded cross-source correlation. Empty scanName /
	// runName match all. Governed-suppressed
	// findings are excluded from every count unless includeSuppressed is
	// true; either way their number is reported under the "suppressed" key.
	SummarizeSecurityFindings(ctx context.Context, namespace, scanName, runName string, includeSuppressed bool) (map[string]int32, error)
	// SummarizeSecurityFindingsScoped computes the same summary as
	// SummarizeSecurityFindings over any combination of scan, run,
	// execution, and task scope, so a fanned-out execution can be
	// aggregated across all of its runs.
	SummarizeSecurityFindingsScoped(ctx context.Context, scope SecurityFindingSummaryScope) (map[string]int32, error)
	// ListSecurityConfigPostures aggregates the namespace's non-duplicate
	// findings grouped per scan configuration (scan_name): finding counts
	// with the same keys as SummarizeSecurityFindings, the latest persisted
	// run's metadata, and per-run observation counts for the newest
	// activityLimit completed runs of each configuration (returned oldest
	// first; a limit <= 0 uses a small default). Configurations named in
	// excludedScanNames are omitted (per-user scan visibility). Results are
	// ordered by scan name.
	ListSecurityConfigPostures(ctx context.Context, namespace string, activityLimit int32, excludedScanNames []string) ([]SecurityConfigPosture, error)
	// DeleteSecurityScanData removes every scan run, finding, and event for
	// (namespace, scan_name). Idempotent. It is called when a SecurityScan
	// resource is deleted.
	DeleteSecurityScanData(ctx context.Context, namespace, scanName string) error
	// PurgeExpiredSecurityData applies the retention policy to the
	// namespace's persisted security data: one bounded batch per call, each
	// class purged by its own namespace-scoped, deterministically ordered,
	// LIMIT-bounded statement so a single call stays cheap. Scan runs,
	// findings, reports, and audit events are deleted; expired evidence and
	// PoC content are redacted in place so the finding row and its audit
	// history survive (an audit event records each redaction). moreWork is
	// true when any class hit batchLimit, i.e. the caller should call again.
	// Idempotent and resumable: re-running never re-purges already-purged
	// rows. batchLimit <= 0 uses a small default.
	PurgeExpiredSecurityData(ctx context.Context, namespace string, policy SecurityRetentionPolicy, batchLimit int) (SecurityRetentionCounts, bool, error)
	// ClaimSecurityNotifications persists notification dedupe markers for
	// the (namespace, scanName, ruleKey, fingerprint) tuples and returns the
	// subset of fingerprints that were newly claimed (not already marked).
	// Callers send only for claimed fingerprints, so a finding never
	// notifies twice for the same rule/channel.
	ClaimSecurityNotifications(ctx context.Context, namespace, scanName, ruleKey string, fingerprints []string) ([]string, error)
	// ReleaseSecurityNotifications removes previously claimed markers so a
	// failed delivery can be retried. Releasing an absent marker is not an
	// error.
	ReleaseSecurityNotifications(ctx context.Context, namespace, scanName, ruleKey string, fingerprints []string) error
}
