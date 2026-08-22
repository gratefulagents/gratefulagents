package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
)

var (
	ErrSecurityResearchTargetNotFound      = errors.New("security research target not found")
	ErrSecurityResearchRevisionNotFound    = errors.New("security research revision not found")
	ErrSecurityResearchDossierNotFound     = errors.New("security research dossier not found")
	ErrSecurityResearchHypothesisNotFound  = errors.New("security research hypothesis not found")
	ErrSecurityResearchSweepNotFound       = errors.New("security research variant sweep not found")
	ErrSecurityResearchSubmissionNotFound  = errors.New("security research submission not found")
	ErrSecurityResearchVersionConflict     = errors.New("security research optimistic version conflict")
	ErrSecurityResearchInvalidTransition   = errors.New("invalid security research hypothesis transition")
	ErrSecurityResearchLineageCycle        = errors.New("security research hypothesis lineage cycle")
	ErrSecurityResearchReservationConflict = errors.New("security research submission reservation is owned by another attempt")
)

const (
	SecurityHypothesisProposed      = "proposed"
	SecurityHypothesisInvestigating = "investigating"
	SecurityHypothesisSupported     = "supported"
	SecurityHypothesisWeakened      = "weakened"
	SecurityHypothesisFalsified     = "falsified"
	SecurityHypothesisBlocked       = "blocked"
	SecurityHypothesisSuperseded    = "superseded"
	SecurityHypothesisPromoted      = "promoted"

	SecurityHypothesisResultPending      = "pending"
	SecurityHypothesisResultPositive     = "positive"
	SecurityHypothesisResultNegative     = "negative"
	SecurityHypothesisResultFailed       = "failed"
	SecurityHypothesisResultTimedOut     = "timed_out"
	SecurityHypothesisResultInconclusive = "inconclusive"
	SecurityHypothesisResultAbandoned    = "abandoned"
)

func ValidSecurityCoverageDimension(value string) bool {
	switch value {
	case SecurityCoverageInvariant, SecurityCoverageActor, SecurityCoverageState, SecurityCoverageTransition:
		return true
	default:
		return false
	}
}

func ValidSecurityCoverageVerdict(value string) bool {
	switch value {
	case SecurityCoverageDisproved, SecurityCoverageAdequatelyTested, SecurityCoverageInadequatelyTested, SecurityCoverageNotTested:
		return true
	default:
		return false
	}
}

const (
	SecurityCoverageInvariant  = "invariant"
	SecurityCoverageActor      = "actor"
	SecurityCoverageState      = "state"
	SecurityCoverageTransition = "transition"

	SecurityCoverageDisproved          = "disproved"
	SecurityCoverageAdequatelyTested   = "adequately_tested"
	SecurityCoverageInadequatelyTested = "inadequately_tested"
	SecurityCoverageNotTested          = "not_tested"
)

// ValidSecurityVariantSweepCompletionEvidence validates the minimum durable
// evidence contract used by both persistence and submission gating.
func ValidSecurityVariantSweepCompletionEvidence(raw json.RawMessage) bool {
	var value struct {
		SearchedScope []string `json:"searched_scope"`
		Methods       []string `json:"methods"`
		Evidence      []string `json:"evidence"`
		Summary       string   `json:"summary"`
	}
	if json.Unmarshal(raw, &value) != nil {
		return false
	}
	return len(value.SearchedScope) > 0 && len(value.Methods) > 0 && len(value.Evidence) > 0 && strings.TrimSpace(value.Summary) != ""
}

const (
	SecurityVariantSweepPending   = "pending"
	SecurityVariantSweepRunning   = "running"
	SecurityVariantSweepCompleted = "completed"
	SecurityVariantSweepBlocked   = "blocked"

	SecuritySubmissionOutcomeAccepted    = "accepted"
	SecuritySubmissionOutcomeDuplicate   = "duplicate"
	SecuritySubmissionOutcomeInformative = "informative"
	SecuritySubmissionOutcomeRejected    = "rejected"
	SecuritySubmissionOutcomeResolved    = "resolved"
)

// SecurityResearchTarget is the stable identity of one research target.
type SecurityResearchTarget struct {
	ID        uuid.UUID
	Namespace string
	TargetKey string
	Kind      string
	Locator   string
	Metadata  json.RawMessage
	CreatedAt time.Time
	UpdatedAt time.Time
}

// SecurityResearchRevision binds an exact immutable revision to a stable target.
type SecurityResearchRevision struct {
	ID        uuid.UUID
	TargetID  uuid.UUID
	Revision  string
	SourceURI string
	Metadata  json.RawMessage
	CreatedAt time.Time
}

type SecurityResearchDossier struct {
	ID             uuid.UUID
	RevisionID     uuid.UUID
	Version        int32
	ParentID       *uuid.UUID
	Content        json.RawMessage
	ChangeSummary  string
	Actor          string
	IdempotencyKey string
	CreatedAt      time.Time
}

type SecurityResearchHypothesis struct {
	ID             uuid.UUID
	RevisionID     uuid.UUID
	HypothesisKey  string
	Title          string
	Invariant      string
	Status         string
	Result         string
	Detail         json.RawMessage
	Actor          string
	Version        int32
	IdempotencyKey string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

type SecurityResearchHypothesisEvent struct {
	ID                int64
	HypothesisID      uuid.UUID
	EventType         string
	FromStatus        string
	ToStatus          string
	Result            string
	Actor             string
	Rationale         string
	Detail            json.RawMessage
	HypothesisVersion int32
	IdempotencyKey    string
	CreatedAt         time.Time
}

type SecurityHypothesisTransition struct {
	ExpectedVersion int32
	ToStatus        string
	Result          string
	Actor           string
	Rationale       string
	Detail          json.RawMessage
	IdempotencyKey  string
}

type SecurityHypothesisLineage struct {
	ChildID   uuid.UUID
	ParentID  uuid.UUID
	Relation  string
	CreatedAt time.Time
}

type SecurityResearchCoverage struct {
	ID             uuid.UUID
	RevisionID     uuid.UUID
	HypothesisID   *uuid.UUID
	Dimension      string
	SubjectKey     string
	Verdict        string
	Bounds         json.RawMessage
	Evidence       json.RawMessage
	Actor          string
	IdempotencyKey string
	CreatedAt      time.Time
}

type SecurityResearchVariantSweep struct {
	ID               uuid.UUID
	RevisionID       uuid.UUID
	FindingID        *uuid.UUID
	RootHypothesisID *uuid.UUID
	RootCause        string
	Scope            json.RawMessage
	Status           string
	Result           json.RawMessage
	Actor            string
	IdempotencyKey   string
	CreatedAt        time.Time
	CompletedAt      *time.Time
}

type SecurityResearchVariantSweepEvent struct {
	ID             int64
	SweepID        uuid.UUID
	EventType      string
	Actor          string
	Detail         json.RawMessage
	IdempotencyKey string
	CreatedAt      time.Time
}

type SecurityResearchSubmission struct {
	ID           uuid.UUID
	RevisionID   uuid.UUID
	TargetID     uuid.UUID
	FindingID    *uuid.UUID
	Workflow     string
	CandidateKey string
	Rank         int32
	Payload      json.RawMessage
	Status       string
	CreatedAt    time.Time
	SubmittedAt  *time.Time
}

type SecuritySubmissionReservationRequest struct {
	SubmissionID   uuid.UUID
	Workflow       string
	PeriodDays     int32
	BudgetLimit    int32
	IdempotencyKey string
}

type SecuritySubmissionReservation struct {
	ID             uuid.UUID
	SubmissionID   uuid.UUID
	TargetID       uuid.UUID
	Workflow       string
	PeriodDays     int32
	BudgetLimit    int32
	IdempotencyKey string
	ReservedAt     time.Time
	ExpiresAt      time.Time
	VoidedAt       *time.Time
}

// SecuritySubmissionReservationResult reports budget exhaustion as state, not an error.
type SecuritySubmissionReservationResult struct {
	Reservation *SecuritySubmissionReservation
	Reserved    bool
	Used        int32
	Limit       int32
}

type SecuritySubmissionOutcomeEvent struct {
	ID                int64
	SubmissionID      uuid.UUID
	Outcome           string
	ExternalReference string
	Rationale         string
	Actor             string
	CorrectionOf      *int64
	IdempotencyKey    string
	CreatedAt         time.Time
}

type SecuritySubmissionOutcome struct {
	SubmissionID      uuid.UUID
	EventID           int64
	Outcome           string
	ExternalReference string
	RecordedAt        time.Time
}

type SecuritySubmissionOutcomeInput struct {
	RevisionID        uuid.UUID
	Outcome           string
	ExternalReference string
	Rationale         string
	Actor             string
	CorrectionOf      *int64
	IdempotencyKey    string
}

// SecuritySubmissionPrecision preserves exact integer counts; callers may
// render Accepted/Submitted without storing a rounded floating-point value.
type SecuritySubmissionPrecision struct {
	Submitted   int64
	Accepted    int64
	Duplicate   int64
	Informative int64
	Rejected    int64
	Resolved    int64
}

type SecurityResearchDecisionSnapshot struct {
	ID             uuid.UUID
	RevisionID     uuid.UUID
	SubmissionID   *uuid.UUID
	Workflow       string
	CandidateKey   string
	Decision       string
	Reason         string
	Rank           int32
	Inputs         json.RawMessage
	IdempotencyKey string
	CreatedAt      time.Time
}

// SecurityFindingConfirmationStore implements the cross-table invariant that
// a confirmed finding and its required variant sweep are committed together.
// It is separate from SecurityResearchStore so non-SQL research stores do not
// claim atomicity across finding and research records.
type SecurityFindingConfirmationStore interface {
	ConfirmSecurityFindingWithVariantSweep(context.Context, string, uuid.UUID, string, string) error
}

// SecuritySubmissionReservationCleanupStore releases an owned reservation
// when artifact construction or upload fails before a package is produced.
type SecuritySubmissionReservationCleanupStore interface {
	VoidSecurityResearchSubmissionReservation(context.Context, string, uuid.UUID, string) error
}

// SecurityResearchStore persists high-cardinality research state outside Kubernetes.
type SecurityResearchStore interface {
	UpsertSecurityResearchTarget(context.Context, *SecurityResearchTarget) (*SecurityResearchTarget, error)
	BindSecurityResearchRevision(context.Context, string, *SecurityResearchRevision) (*SecurityResearchRevision, bool, error)
	GetSecurityResearchRevision(context.Context, string, string, string) (*SecurityResearchRevision, error)
	AmendSecurityResearchDossier(context.Context, string, *SecurityResearchDossier) (*SecurityResearchDossier, bool, error)
	GetLatestSecurityResearchDossier(context.Context, string, uuid.UUID) (*SecurityResearchDossier, error)

	CreateSecurityResearchHypothesis(context.Context, string, *SecurityResearchHypothesis) (*SecurityResearchHypothesis, bool, error)
	TransitionSecurityResearchHypothesis(context.Context, string, uuid.UUID, SecurityHypothesisTransition) (*SecurityResearchHypothesis, error)
	ReopenSecurityResearchHypothesis(context.Context, string, uuid.UUID, SecurityHypothesisTransition) (*SecurityResearchHypothesis, error)
	AddSecurityResearchHypothesisLineage(context.Context, string, SecurityHypothesisLineage, string) error
	ListSecurityResearchHypotheses(context.Context, string, uuid.UUID) ([]SecurityResearchHypothesis, error)
	ListSecurityResearchHypothesisEvents(context.Context, string, uuid.UUID) ([]SecurityResearchHypothesisEvent, error)

	RecordSecurityResearchCoverage(context.Context, string, *SecurityResearchCoverage) (*SecurityResearchCoverage, bool, error)
	ListSecurityResearchCoverage(context.Context, string, uuid.UUID) ([]SecurityResearchCoverage, error)

	CreateSecurityResearchVariantSweep(context.Context, string, *SecurityResearchVariantSweep) (*SecurityResearchVariantSweep, bool, error)
	CompleteSecurityResearchVariantSweep(context.Context, string, uuid.UUID, string, json.RawMessage, string, string) (*SecurityResearchVariantSweep, error)
	ListSecurityResearchVariantSweeps(context.Context, string, uuid.UUID) ([]SecurityResearchVariantSweep, error)
	ListSecurityResearchVariantSweepEvents(context.Context, string, uuid.UUID) ([]SecurityResearchVariantSweepEvent, error)

	CreateSecurityResearchSubmission(context.Context, string, *SecurityResearchSubmission) (*SecurityResearchSubmission, bool, error)
	ReserveSecurityResearchSubmission(context.Context, string, SecuritySubmissionReservationRequest) (*SecuritySubmissionReservationResult, error)
	MarkSecurityResearchSubmissionSubmitted(context.Context, string, uuid.UUID, time.Time) error
	RecordSecuritySubmissionOutcome(context.Context, string, uuid.UUID, SecuritySubmissionOutcomeInput) (*SecuritySubmissionOutcome, bool, error)
	ListSecuritySubmissionOutcomeEvents(context.Context, string, uuid.UUID, uuid.UUID) ([]SecuritySubmissionOutcomeEvent, error)
	GetSecuritySubmissionPrecision(context.Context, string, uuid.UUID, string, *time.Time) (*SecuritySubmissionPrecision, error)
	CreateSecurityResearchDecisionSnapshot(context.Context, string, *SecurityResearchDecisionSnapshot) (*SecurityResearchDecisionSnapshot, bool, error)
}
