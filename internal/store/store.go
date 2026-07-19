// Package store defines the state storage interface for agent sessions.
// It abstracts conversation history, activity events, and artifact persistence
// away from etcd/CRD status into a durable backend (Postgres + S3).
package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
)

// Sentinel errors for conversation message cancellation. Implementations of
// StateStore return (possibly wrapped) these so callers can map them to
// user-facing failures.
var (
	// ErrMessageNotFound is returned when the referenced user message does
	// not exist in the session (or was already cancelled).
	ErrMessageNotFound = errors.New("message not found")
	// ErrMessageDelivered is returned when the agent loop already consumed
	// the message, so it can no longer be cancelled.
	ErrMessageDelivered = errors.New("message already delivered")
	// ErrMessageAlreadyExists is returned when an idempotency key in message
	// metadata already exists for the session. Callers may treat it as a
	// successful replay of the original append.
	ErrMessageAlreadyExists = errors.New("message already exists")
)

// Session represents a persistent agent session tied to an AgentRun.
type Session struct {
	ID               uuid.UUID
	AgentRunName     string
	AgentRunNS       string
	Phase            string
	CurrentStep      string
	PendingQuestion  string
	PendingActions   json.RawMessage
	PendingInputType string
	PendingRequestID string
	Metadata         json.RawMessage
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// Message represents a single conversation turn.
type Message struct {
	ID               int64
	SessionID        uuid.UUID
	Role             string // "user", "assistant", "system"
	Content          string
	Metadata         json.RawMessage
	DeliveryState    string
	DeliverySequence int64
	ClaimedAt        *time.Time
	CreatedAt        time.Time
}

// ActivityEvent represents an agent activity log entry.
type ActivityEvent struct {
	ID        int64
	SessionID uuid.UUID
	EventType string
	Summary   string
	Detail    json.RawMessage
	CreatedAt time.Time
}

// Artifact represents a stored artifact (plan, diff, activity log, etc.).
type Artifact struct {
	ID          uuid.UUID
	SessionID   uuid.UUID
	Kind        string // "plan", "diff", "activity_log", "review"
	Content     string
	S3URL       string
	ContentHash string
	Metadata    json.RawMessage
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// SessionMetricsEntry holds parsed cost/token metrics for a session, keyed by AgentRun.
type SessionMetricsEntry struct {
	AgentRunName  string
	AgentRunNS    string
	CostUSD       float64
	InputTokens   int64
	OutputTokens  int64
	ToolCallCount int32
}

type ObservabilityQuery struct {
	Namespace     string
	Start         time.Time
	End           time.Time
	BucketSeconds int64
	AgentRunNames []string
}

type ObservabilityTotals struct {
	Runs, InputTokens, OutputTokens, ToolCalls, ToolErrors int64
	Subagents, SubagentFailures, LLMAttempts, LLMFailures  int64
	Compactions, TokensReclaimed                           int64
	GenerationInputTokens, GenerationOutputTokens          int64
	CostUSD, GenerationCostUSD                             float64
}

type ObservabilityBucket struct {
	Start  time.Time
	Totals ObservabilityTotals
}

type ObservabilityBreakdown struct {
	Name                                      string
	Count, Errors, InputTokens, OutputTokens  int64
	CostUSD, AverageDurationMS, P95DurationMS float64
}

type ObservabilityDataCompleteness struct {
	Sessions, SessionsWithMetrics, SessionsWithActivity  int64
	MetricsComplete, ActivityComplete, ActivityTruncated bool
}

type ObservabilityOverview struct {
	Totals       ObservabilityTotals
	Buckets      []ObservabilityBucket
	Tools        []ObservabilityBreakdown
	Subagents    []ObservabilityBreakdown
	Models       []ObservabilityBreakdown
	Completeness ObservabilityDataCompleteness
}

// SlackDraft is a proposed reply to an incoming Slack DM awaiting the owner's
// approval. After a decision it doubles as an audit record.
type SlackDraft struct {
	ID           uuid.UUID
	SlackAgent   string
	Namespace    string
	Kind         string // channel_reply ("triage" only in legacy rows from the removed inbox-monitoring feature)
	ChannelID    string
	ThreadTS     string
	TargetUser   string
	IncomingText string
	DraftText    string
	Status       string // pending | sent | dismissed
	CreatedAt    time.Time
	DecidedAt    *time.Time
}

// PendingInputResolution is a request-bound response to reserve atomically in
// the conversation before controller side effects are applied. Metadata must
// include a stable delivery_id; reserved messages remain hidden from agent
// polling until ReleasePendingInputResponse succeeds.
type PendingInputResolution struct {
	RequestID string
	Phase     string
	Role      string
	Content   string
	Metadata  json.RawMessage
}

// PendingInputResolver is the transactional extension used by trusted
// controllers to consume exactly one pending request without racing a user or
// a replacement request. It is separate from StateStore so lightweight stores
// that never resolve controller-managed input do not need to implement it.
type PendingInputResolver interface {
	ReservePendingInputResponse(ctx context.Context, sessionID uuid.UUID, resolution PendingInputResolution) (message *Message, reserved bool, err error)
	ReleasePendingInputResponse(ctx context.Context, sessionID uuid.UUID, messageID int64, deliveryID string) error
	CancelPendingInputResponse(ctx context.Context, sessionID uuid.UUID, messageID int64, deliveryID string) error
}

// PendingInputClearer conditionally consumes an exact request without touching
// a replacement. Interactive approval handlers use it after external effects
// have been committed.
type PendingInputClearer interface {
	ClearPendingInputIfID(ctx context.Context, sessionID uuid.UUID, requestID, phase string) (cleared bool, err error)
}

// MessageClaimer provides PostgreSQL's durable claim protocol without forcing
// lightweight/test StateStore implementations to emulate database CAS.
type MessageClaimer interface {
	ClaimUserMessage(ctx context.Context, sessionID uuid.UUID, messageID int64, claimToken uuid.UUID) (message *Message, claimed bool, err error)
	AppendAssistantAndCompleteClaims(ctx context.Context, sessionID uuid.UUID, claimToken uuid.UUID, content string) (*Message, error)
	CompleteClaims(ctx context.Context, sessionID uuid.UUID, claimToken uuid.UUID) error
	RecoverClaimedUserMessages(ctx context.Context, sessionID uuid.UUID, activeClaimToken uuid.UUID) error
}

// InterruptStore persists interrupts as append-only rows so concurrent stop
// requests cannot overwrite one another.
type InterruptStore interface {
	AppendInterrupt(ctx context.Context, sessionID uuid.UUID, requestedBy string) (int64, time.Time, error)
	PeekInterrupt(ctx context.Context, sessionID uuid.UUID) (id int64, requestedAt time.Time, requestedBy string, ok bool, err error)
	ConsumeInterrupt(ctx context.Context, sessionID uuid.UUID) (id int64, requestedAt time.Time, requestedBy string, ok bool, err error)
}

// WakeIntentStore atomically deduplicates a resume message. The Kubernetes
// wake counter is reconciled to TargetWakeRequests, making retries safe after
// either side of the PostgreSQL/Kubernetes boundary fails.
type WakeIntentStore interface {
	ReserveWakeIntent(ctx context.Context, sessionID uuid.UUID, idempotencyKey, content string, targetWakeRequests int64) (message *Message, target int64, created bool, err error)
	MarkWakeIntentApplied(ctx context.Context, sessionID uuid.UUID, idempotencyKey string) error
}

// StateStore is the interface for all agent session state persistence.
// Implementations must be safe for concurrent use.
type StateStore interface {
	// Session lifecycle
	CreateSession(ctx context.Context, agentRunName, agentRunNS, phase, currentStep string) (*Session, error)
	GetSession(ctx context.Context, id uuid.UUID) (*Session, error)
	GetSessionByRun(ctx context.Context, agentRunName, agentRunNS string) (*Session, error)
	// ListSessionsByNamespace returns every session whose AgentRun lives in
	// the given namespace; an empty namespace returns all sessions. Used to
	// batch per-run session lookups when enriching run lists.
	ListSessionsByNamespace(ctx context.Context, namespace string) ([]Session, error)
	UpdatePhase(ctx context.Context, id uuid.UUID, phase, currentStep string) error
	SetPendingQuestion(ctx context.Context, id uuid.UUID, phase, question, inputType string) error
	ClearPendingQuestion(ctx context.Context, id uuid.UUID, phase string) error
	SetPendingAction(ctx context.Context, id uuid.UUID, phase, question string, actions json.RawMessage, inputType string) error
	ClearPendingAction(ctx context.Context, id uuid.UUID, phase string) error
	UpdateMetadata(ctx context.Context, id uuid.UUID, metadata json.RawMessage) error
	MergeSessionMetadata(ctx context.Context, id uuid.UUID, key string, value json.RawMessage) error
	ListAllSessionMetrics(ctx context.Context) ([]SessionMetricsEntry, error)
	// DeleteAgentRunData deletes every DB row that is scoped to the given
	// AgentRun. projectID scopes durable project-state rows when available.
	// Implementations should be idempotent so Kubernetes finalizers can safely
	// retry cleanup after partial failure.
	DeleteAgentRunData(ctx context.Context, agentRunName, agentRunNS, projectID string) error

	// Conversation
	// AppendMessage atomically returns ErrMessageAlreadyExists when the same
	// non-empty github_event_key metadata value already exists in the session.
	AppendMessage(ctx context.Context, sessionID uuid.UUID, role, content string, metadata json.RawMessage) (*Message, error)
	GetMessages(ctx context.Context, sessionID uuid.UUID) ([]Message, error)
	// GetMessagesIncludingCancelled includes cancelled user messages for queue-history surfaces.
	GetMessagesIncludingCancelled(ctx context.Context, sessionID uuid.UUID) ([]Message, error)
	GetMessagesSince(ctx context.Context, sessionID uuid.UUID, afterID int64) ([]Message, error)
	PollNewUserMessages(ctx context.Context, sessionID uuid.UUID, afterID int64) ([]Message, error)
	// MarkMessagesDelivered records that the agent loop consumed the given
	// user messages, stamping delivered_at_unix into each message's metadata.
	MarkMessagesDelivered(ctx context.Context, sessionID uuid.UUID, messageIDs []int64) error
	// CancelUndeliveredUserMessage withdraws a queued/steering user message
	// before the agent loop consumes it, stamping cancelled_at_unix into its
	// metadata. Cancelled messages are excluded from delivery polls and from
	// the rendered conversation. Returns ErrMessageDelivered when the agent
	// already picked the message up and ErrMessageNotFound when no such user
	// message exists in the session.
	CancelUndeliveredUserMessage(ctx context.Context, sessionID uuid.UUID, messageID int64) error

	// Transcript snapshot: one bounded blob per session holding the agent's
	// serialized in-memory run transcript, upserted in place after each turn
	// so a restarted pod can replay full context. GetSessionTranscript
	// returns (nil, nil) when no snapshot exists.
	UpsertSessionTranscript(ctx context.Context, sessionID uuid.UUID, data []byte, itemCount int32) error
	GetSessionTranscript(ctx context.Context, sessionID uuid.UUID) ([]byte, error)
	DeleteSessionTranscript(ctx context.Context, sessionID uuid.UUID) error

	// Activity
	WriteActivityEvent(ctx context.Context, sessionID uuid.UUID, eventType, summary string, detail json.RawMessage) (*ActivityEvent, error)
	GetRecentActivity(ctx context.Context, sessionID uuid.UUID, limit int32) ([]ActivityEvent, error)
	GetAllActivity(ctx context.Context, sessionID uuid.UUID) ([]ActivityEvent, error)
	// GetActivityEventsSince returns activity events with ID greater than
	// afterID, in ascending ID order, for incremental log rebuilds.
	GetActivityEventsSince(ctx context.Context, sessionID uuid.UUID, afterID int64) ([]ActivityEvent, error)

	// GetSessionFingerprint returns a cheap opaque version string that changes
	// whenever the session row, its conversation, activity log, or plan
	// artifact change. Watchers compare fingerprints to skip re-enrichment.
	GetSessionFingerprint(ctx context.Context, sessionID uuid.UUID) (string, error)

	// Artifacts
	UpsertArtifact(ctx context.Context, sessionID uuid.UUID, kind, content, s3URL, contentHash string, metadata json.RawMessage) (*Artifact, error)
	GetArtifact(ctx context.Context, sessionID uuid.UUID, kind string) (*Artifact, error)
	GetArtifacts(ctx context.Context, sessionID uuid.UUID) ([]Artifact, error)

	// Resource ownership
	SetResourceOwner(ctx context.Context, resourceType, resourceID, resourceNS, ownerID string) error
	GetResourceOwner(ctx context.Context, resourceType, resourceID, resourceNS string) (*ResourceOwnership, error)
	ListOwnedResources(ctx context.Context, ownerID, resourceType string) ([]ResourceOwnership, error)

	// Resource sharing
	ShareResource(ctx context.Context, share *ResourceShare) (*ResourceShare, error)
	RevokeShare(ctx context.Context, shareID string) error
	UpdateSharePermission(ctx context.Context, shareID, permission string) error
	ListSharesForResource(ctx context.Context, resourceType, resourceID, resourceNS string) ([]ResourceShare, error)
	ListSharedWithMe(ctx context.Context, userID, resourceType string) ([]ResourceShare, error)
	GetSharePermission(ctx context.Context, resourceType, resourceID, resourceNS, userID string) (*ResourceShare, error)

	// Notifications
	CreateNotification(ctx context.Context, n *Notification) error
	HasUnreadNotification(ctx context.Context, userID, notifType, resourceID, resourceNamespace string) (bool, error)
	ListNotifications(ctx context.Context, userID string, unreadOnly bool, limit int32) ([]Notification, error)
	MarkNotificationRead(ctx context.Context, notificationID string) error
	MarkAllNotificationsRead(ctx context.Context, userID string) error
	GetUnreadNotificationCount(ctx context.Context, userID string) (int32, error)

	// Lifecycle
	Close() error
}
