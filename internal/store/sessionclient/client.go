// Package sessionclient provides a client for agent pods to persist
// conversation state to Postgres. Postgres is the single source of truth
// for durable session data. The CRD stores cluster-visible execution status
// such as phase, queue state, timings, and artifact refs.
package sessionclient

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/google/uuid"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/store"
)

// Client manages an agent session backed by Postgres.
// CRD status is updated only for cluster-visible execution state.
type Client struct {
	store       store.StateStore
	crd         client.Client
	sessionID   uuid.UUID
	runName     string
	runNS       string
	ownerUserID string // looked up from resource_ownership at init
	claimToken  uuid.UUID
}

type UserMessageMode string

const (
	UserMessageModeEnqueue   UserMessageMode = "enqueue"
	UserMessageModeImmediate UserMessageMode = "immediate"
)

type UserMessage struct {
	store.Message
	Mode   UserMessageMode
	Images []MessageImage
}

// MessageImage is a base64-encoded image attached to a user message. Data holds
// the raw base64 payload (no data-URL prefix).
type MessageImage struct {
	MediaType    string `json:"media_type"`
	Data         string `json:"data"`
	AssetID      string `json:"asset_id,omitempty"`
	AssetVersion int    `json:"asset_version,omitempty"`
	AssetSHA256  string `json:"asset_sha256,omitempty"`
	AssetPath    string `json:"asset_path,omitempty"`
	ProjectName  string `json:"project_name,omitempty"`
}

// New creates or resumes a session for the given AgentRun.
func New(ctx context.Context, ss store.StateStore, crd client.Client, runName, runNS, phase, currentStep string) (*Client, error) {
	sess, err := ss.GetSessionByRun(ctx, runName, runNS)
	if err != nil {
		// Session doesn't exist yet — create it.
		sess, err = ss.CreateSession(ctx, runName, runNS, phase, currentStep)
		if err != nil {
			return nil, fmt.Errorf("creating session: %w", err)
		}
		log.Printf("Created new session %s for %s/%s", sess.ID, runNS, runName)
	} else {
		log.Printf("Resumed session %s for %s/%s (phase=%s step=%s)", sess.ID, runNS, runName, sess.Phase, sess.CurrentStep)
	}

	return &Client{
		store:       ss,
		crd:         crd,
		sessionID:   sess.ID,
		runName:     runName,
		runNS:       runNS,
		ownerUserID: lookupOwnerUserID(ctx, ss, runName, runNS),
		claimToken:  uuid.New(),
	}, nil
}

func (c *Client) SessionID() uuid.UUID { return c.sessionID }

// StateStore returns the underlying state store for direct access.
func (c *Client) StateStore() store.StateStore { return c.store }

// Session returns the current session state from Postgres.
func (c *Client) Session(ctx context.Context) (*store.Session, error) {
	return c.store.GetSession(ctx, c.sessionID)
}

// --- Phase/Step (Postgres primary, CRD best-effort mirror) ---

func (c *Client) UpdatePhase(ctx context.Context, phase, currentStep string) error {
	if err := c.store.UpdatePhase(ctx, c.sessionID, phase, currentStep); err != nil {
		return err
	}
	crdPhase := toCRDPhase(phase)
	if crdPhase == "" {
		return nil
	}
	return c.patchCRDStatus(ctx, func(run *platformv1alpha1.AgentRun) {
		run.Status.Phase = crdPhase
		run.Status.CurrentStep = currentStep
	})
}

// --- Conversation (Postgres only) ---

func (c *Client) AppendUserMessage(ctx context.Context, content string) (*store.Message, error) {
	return c.AppendUserMessageWithMode(ctx, content, UserMessageModeEnqueue)
}

func (c *Client) AppendUserMessageWithMode(ctx context.Context, content string, mode UserMessageMode) (*store.Message, error) {
	return c.store.AppendMessage(ctx, c.sessionID, "user", content, EncodeUserMessageMetadata(mode))
}

// AppendUserMessageWithImages persists a user message carrying optional image
// attachments alongside the chosen delivery mode.
func (c *Client) AppendUserMessageWithImages(ctx context.Context, content string, mode UserMessageMode, images []MessageImage) (*store.Message, error) {
	return c.store.AppendMessage(ctx, c.sessionID, "user", content, EncodeUserMessageMetadataWithImages(mode, images))
}

func (c *Client) AppendAssistantMessage(ctx context.Context, content string) (*store.Message, error) {
	return c.store.AppendMessage(ctx, c.sessionID, "assistant", content, nil)
}

func (c *Client) AppendSystemMessage(ctx context.Context, content string) (*store.Message, error) {
	return c.store.AppendMessage(ctx, c.sessionID, "system", content, nil)
}

func (c *Client) GetMessages(ctx context.Context) ([]store.Message, error) {
	return c.store.GetMessages(ctx, c.sessionID)
}

func (c *Client) GetMessagesSince(ctx context.Context, afterID int64) ([]store.Message, error) {
	return c.store.GetMessagesSince(ctx, c.sessionID, afterID)
}

func NormalizeUserMessageMode(mode UserMessageMode) UserMessageMode {
	switch mode {
	case UserMessageModeImmediate:
		return UserMessageModeImmediate
	default:
		return UserMessageModeEnqueue
	}
}

func EncodeUserMessageMetadata(mode UserMessageMode) json.RawMessage {
	return EncodeUserMessageMetadataWithImages(mode, nil)
}

// EncodeUserMessageMetadataWithImages serializes the delivery mode and any image
// attachments into the message metadata blob.
func EncodeUserMessageMetadataWithImages(mode UserMessageMode, images []MessageImage) json.RawMessage {
	normalized := NormalizeUserMessageMode(mode)
	payload := userMessageMetadata{Mode: string(normalized)}
	for _, img := range images {
		if strings.TrimSpace(img.Data) == "" {
			continue
		}
		payload.Images = append(payload.Images, img)
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return json.RawMessage(`{"mode":"enqueue"}`)
	}
	return encoded
}

// userMessageMetadata is the JSON shape stored in conversation_messages.metadata
// for user messages.
type userMessageMetadata struct {
	Mode             string         `json:"mode"`
	Images           []MessageImage `json:"images,omitempty"`
	PendingRequestID string         `json:"pending_request_id,omitempty"`
	// DeliveredAtUnix is stamped by the store when the agent loop consumes
	// the message (see StateStore.MarkMessagesDelivered). Zero means the
	// message is still waiting to be delivered.
	DeliveredAtUnix int64 `json:"delivered_at_unix,omitempty"`
	// CancelledAtUnix is stamped when the user withdraws a queued/steering
	// message before delivery (StateStore.CancelUndeliveredUserMessage).
	// Cancelled messages are skipped by delivery polls and hidden from the
	// rendered conversation.
	CancelledAtUnix int64 `json:"cancelled_at_unix,omitempty"`
}

func messageModeFromMetadata(metadata json.RawMessage) UserMessageMode {
	if len(metadata) == 0 {
		return UserMessageModeEnqueue
	}
	var payload userMessageMetadata
	if err := json.Unmarshal(metadata, &payload); err != nil {
		return UserMessageModeEnqueue
	}
	return NormalizeUserMessageMode(UserMessageMode(strings.ToLower(strings.TrimSpace(payload.Mode))))
}

// UserMessageStateFromMetadata extracts the delivery mode and the delivery
// timestamp (0 while undelivered) from a user message's metadata JSON.
func UserMessageStateFromMetadata(metadata json.RawMessage) (UserMessageMode, int64) {
	mode := messageModeFromMetadata(metadata)
	if len(metadata) == 0 {
		return mode, 0
	}
	var payload userMessageMetadata
	if err := json.Unmarshal(metadata, &payload); err != nil {
		return mode, 0
	}
	return mode, payload.DeliveredAtUnix
}

// UserMessageCancelled reports whether a user message was withdrawn by the
// user before the agent loop consumed it.
func PendingRequestIDFromMetadata(metadata json.RawMessage) string {
	var payload userMessageMetadata
	if len(metadata) == 0 || json.Unmarshal(metadata, &payload) != nil {
		return ""
	}
	return strings.TrimSpace(payload.PendingRequestID)
}

func UserMessageCancelled(metadata json.RawMessage) bool {
	if len(metadata) == 0 {
		return false
	}
	var payload userMessageMetadata
	if err := json.Unmarshal(metadata, &payload); err != nil {
		return false
	}
	return payload.CancelledAtUnix > 0
}

// ImagesFromMetadata extracts any image attachments stored in a message's
// metadata JSON. It returns nil when the message carries no images.
func ImagesFromMetadata(metadata json.RawMessage) []MessageImage {
	return imagesFromMetadata(metadata)
}

// imagesFromMetadata extracts any image attachments stored in message metadata.
func imagesFromMetadata(metadata json.RawMessage) []MessageImage {
	if len(metadata) == 0 {
		return nil
	}
	var payload userMessageMetadata
	if err := json.Unmarshal(metadata, &payload); err != nil {
		return nil
	}
	out := make([]MessageImage, 0, len(payload.Images))
	for _, img := range payload.Images {
		if strings.TrimSpace(img.Data) == "" {
			continue
		}
		out = append(out, img)
	}
	return out
}

func wrapUserMessages(messages []store.Message) []UserMessage {
	out := make([]UserMessage, 0, len(messages))
	for _, msg := range messages {
		out = append(out, UserMessage{
			Message: msg,
			Mode:    messageModeFromMetadata(msg.Metadata),
			Images:  imagesFromMetadata(msg.Metadata),
		})
	}
	return out
}

// ResumeState returns the session state and the latest message cursor for
// resuming a chat/plan loop after pod restart. If the session has no messages,
// cursor is 0 and the returned messages slice is empty.
func (c *Client) ResumeState(ctx context.Context) (session *store.Session, messages []store.Message, cursor int64, err error) {
	session, err = c.store.GetSession(ctx, c.sessionID)
	if err != nil {
		return nil, nil, 0, fmt.Errorf("loading session: %w", err)
	}
	messages, err = c.store.GetMessages(ctx, c.sessionID)
	if err != nil {
		return nil, nil, 0, fmt.Errorf("loading messages: %w", err)
	}
	// Delivery state, not assistant row IDs, determines unresolved work. Cursor
	// starts at zero; the caller raises it only to the durable user-stop floor.
	// This preserves pending holes inserted before a later assistant reply.
	cursor = 0
	log.Printf("Session resume: %d messages, cursor=%d, phase=%s step=%s", len(messages), cursor, session.Phase, session.CurrentStep)
	return session, messages, cursor, nil
}

// PollForUserMessages blocks until any new user messages appear after afterID.
// Returned messages are ordered by message ID.
func (c *Client) PollForUserMessages(ctx context.Context, afterID int64, pollInterval time.Duration) ([]UserMessage, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	msgs, err := c.store.PollNewUserMessages(ctx, c.sessionID, afterID)
	if err != nil {
		log.Printf("WARN: polling for user messages: %v", err)
	} else if len(msgs) > 0 {
		return wrapUserMessages(msgs), nil
	}

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
		}

		msgs, err := c.store.PollNewUserMessages(ctx, c.sessionID, afterID)
		if err != nil {
			log.Printf("WARN: polling for user messages: %v", err)
			continue
		}
		if len(msgs) > 0 {
			return wrapUserMessages(msgs), nil
		}
	}
}

// PeekForUserMessages does a non-blocking check for new user messages after afterID.
func (c *Client) PeekForUserMessages(ctx context.Context, afterID int64) ([]UserMessage, error) {
	msgs, err := c.store.PollNewUserMessages(ctx, c.sessionID, afterID)
	if err != nil {
		return nil, err
	}
	return wrapUserMessages(msgs), nil
}

// ClaimUserMessage atomically establishes that this runner owns a pending
// message before its content enters model context. Stores without the durable
// claim extension retain the legacy mark behavior for compatibility.
func (c *Client) ClaimUserMessage(ctx context.Context, message UserMessage) (UserMessage, bool, error) {
	if claimer, ok := c.store.(store.MessageClaimer); ok {
		claimed, won, err := claimer.ClaimUserMessage(ctx, c.sessionID, message.ID, c.claimToken)
		if err != nil || !won {
			return UserMessage{}, won, err
		}
		return wrapUserMessages([]store.Message{*claimed})[0], true, nil
	}
	if err := c.store.MarkMessagesDelivered(ctx, c.sessionID, []int64{message.ID}); err != nil {
		return UserMessage{}, false, err
	}
	return message, true, nil
}

func (c *Client) AppendAssistantAndCompleteClaims(ctx context.Context, content string) (*store.Message, error) {
	if claimer, ok := c.store.(store.MessageClaimer); ok {
		return claimer.AppendAssistantAndCompleteClaims(ctx, c.sessionID, c.claimToken, content)
	}
	return c.AppendAssistantMessage(ctx, content)
}

func (c *Client) CompleteClaims(ctx context.Context) error {
	if claimer, ok := c.store.(store.MessageClaimer); ok {
		return claimer.CompleteClaims(ctx, c.sessionID, c.claimToken)
	}
	return nil
}

func (c *Client) RecoverClaimedUserMessages(ctx context.Context) error {
	if claimer, ok := c.store.(store.MessageClaimer); ok {
		return claimer.RecoverClaimedUserMessages(ctx, c.sessionID, c.claimToken)
	}
	return nil
}

func (c *Client) MarkUserMessagesDelivered(ctx context.Context, ids ...int64) {
	if len(ids) == 0 {
		return
	}
	if err := c.store.MarkMessagesDelivered(ctx, c.sessionID, ids); err != nil {
		log.Printf("WARN: failed to mark user messages %v delivered: %v", ids, err)
	}
}

// --- User input requests (Postgres primary, CRD phase/queue mirror) ---

// SetUserInputRequest writes a pending user input request.
// Full details (message, actions) live in Postgres. The CRD only mirrors the
// blocking phase/queue so controllers and watches can observe execution state.
// For actionable input types, a notification is also created for the run owner.
func (c *Client) SetUserInputRequest(ctx context.Context, inputType platformv1alpha1.UserInputRequestType, message string, actions json.RawMessage) error {
	phase := crdPhaseForInputType(inputType)
	if len(actions) > 0 {
		if err := c.store.SetPendingAction(ctx, c.sessionID, phase, message, actions, string(inputType)); err != nil {
			return err
		}
	} else {
		if err := c.store.SetPendingQuestion(ctx, c.sessionID, phase, message, string(inputType)); err != nil {
			return err
		}
	}

	c.maybeNotifyOwner(ctx, inputType, message)

	return c.patchCRDStatus(ctx, func(run *platformv1alpha1.AgentRun) {
		run.Status.Phase = platformv1alpha1.AgentRunPhase(phase)
		run.Status.Queue = &platformv1alpha1.AgentRunQueueStatus{
			State:         phase,
			BlockedReason: string(inputType),
		}
		switch inputType {
		case platformv1alpha1.UserInputQuestion, platformv1alpha1.UserInputApproval,
			platformv1alpha1.UserInputPlanReview, platformv1alpha1.UserInputTurnLimit,
			platformv1alpha1.UserInputIdle:
			run.Status.CurrentStep = "awaiting-user"
		case platformv1alpha1.UserInputStopped:
			run.Status.CurrentStep = "stopped"
		case platformv1alpha1.UserInputCircuitBreak:
			run.Status.CurrentStep = "blocked"
		}
	})
}

// ClearIdleUserInputRequest consumes the current idle boundary, if any. It is
// used for kickoff/legacy messages that predate request-bound message metadata.
// The nonce-based clear keeps a concurrently published replacement request safe.
func (c *Client) ClearIdleUserInputRequest(ctx context.Context) error {
	sess, err := c.store.GetSession(ctx, c.sessionID)
	if err != nil {
		return err
	}
	if !strings.EqualFold(strings.TrimSpace(sess.PendingInputType), string(platformv1alpha1.UserInputIdle)) || strings.TrimSpace(sess.PendingRequestID) == "" {
		return nil
	}
	return c.ClearUserInputRequestIfID(ctx, sess.PendingRequestID)
}

func (c *Client) ClearUserInputRequestIfID(ctx context.Context, requestID string) error {
	if clearer, ok := c.store.(store.PendingInputClearer); ok && strings.TrimSpace(requestID) != "" {
		cleared, err := clearer.ClearPendingInputIfID(ctx, c.sessionID, requestID, "running")
		if err != nil {
			return err
		}
		if !cleared {
			// The dashboard may already have consumed this exact request when it
			// accepted the message. Repair the CRD mirror once the runtime actually
			// starts that turn, but do not overwrite a replacement input request.
			sess, err := c.store.GetSession(ctx, c.sessionID)
			if err != nil {
				return err
			}
			if strings.TrimSpace(sess.PendingRequestID) != "" {
				return nil
			}
		}
		return c.patchCRDStatus(ctx, func(run *platformv1alpha1.AgentRun) {
			// Only repair phases that can represent the input request this message
			// consumed. A controller may have paused or terminated the run after the
			// message was queued; that newer lifecycle decision must remain authoritative.
			// WaitingApproval is safe to repair here: the nonce check above proved the
			// consumed message answered the request that was still current, and MCP
			// break-glass decisions clear their own request before this path can run.
			switch run.Status.Phase {
			case platformv1alpha1.AgentRunPhaseRunning, platformv1alpha1.AgentRunPhaseQuestion,
				platformv1alpha1.AgentRunPhaseWaitingApproval:
				run.Status.Phase = platformv1alpha1.AgentRunPhaseRunning
				run.Status.Queue = &platformv1alpha1.AgentRunQueueStatus{State: "Running"}
				run.Status.CurrentStep = "chat-followup"
			}
		})
	}
	return nil
}

func (c *Client) ClearUserInputRequest(ctx context.Context) error {
	if err := c.store.ClearPendingAction(ctx, c.sessionID, "running"); err != nil {
		return err
	}
	return c.patchCRDStatus(ctx, func(run *platformv1alpha1.AgentRun) {
		// Only reset Phase to Running if it isn't in WaitingApproval —
		// approval state must be cleared explicitly by the approval handler.
		if run.Status.Phase != platformv1alpha1.AgentRunPhaseWaitingApproval {
			run.Status.Phase = platformv1alpha1.AgentRunPhaseRunning
		}
		run.Status.Queue = &platformv1alpha1.AgentRunQueueStatus{State: "Running"}
		run.Status.CurrentStep = "chat-followup"
	})
}

// crdPhaseForInputType maps an input request type to the CRD Phase value.
func crdPhaseForInputType(t platformv1alpha1.UserInputRequestType) string {
	switch t {
	case platformv1alpha1.UserInputApproval:
		return string(platformv1alpha1.AgentRunPhaseWaitingApproval)
	case platformv1alpha1.UserInputQuestion, platformv1alpha1.UserInputPlanReview,
		platformv1alpha1.UserInputTurnLimit:
		return string(platformv1alpha1.AgentRunPhaseQuestion)
	default:
		return string(platformv1alpha1.AgentRunPhaseRunning)
	}
}

// --- Artifacts (Postgres primary) ---

func (c *Client) GetPlan(ctx context.Context) (string, error) {
	art, err := c.store.GetArtifact(ctx, c.sessionID, "plan")
	if err != nil {
		return "", err
	}
	return art.Content, nil
}

// --- Activity events (Postgres primary) ---

func (c *Client) WriteActivity(ctx context.Context, eventType, summary string, detail json.RawMessage) error {
	_, err := c.store.WriteActivityEvent(ctx, c.sessionID, eventType, summary, detail)
	return err
}

// --- Metrics (Postgres primary, CRD best-effort mirror) ---

func (c *Client) WriteMetrics(ctx context.Context, metrics SessionMetrics) error {
	encoded, err := json.Marshal(metrics)
	if err != nil {
		return fmt.Errorf("marshaling session metrics: %w", err)
	}
	if err := c.store.MergeSessionMetadata(ctx, c.sessionID, metadataKeyMetrics, encoded); err != nil {
		return fmt.Errorf("merging session metrics: %w", err)
	}
	return c.patchCRDStatus(ctx, func(run *platformv1alpha1.AgentRun) {
		run.Status.Metrics = &platformv1alpha1.AgentRunMetrics{
			CostUsd:       fmt.Sprintf("%.4f", metrics.CostUSD),
			InputTokens:   metrics.InputTokens,
			OutputTokens:  metrics.OutputTokens,
			ToolCallCount: metrics.ToolCallCount,
		}
		if run.Status.Phase == "" || run.Status.Phase == platformv1alpha1.AgentRunPhasePending || run.Status.Phase == platformv1alpha1.AgentRunPhaseAdmitted {
			run.Status.Phase = platformv1alpha1.AgentRunPhaseRunning
		}
		if run.Status.Queue == nil || run.Status.Queue.State == "" {
			run.Status.Queue = &platformv1alpha1.AgentRunQueueStatus{State: "Running"}
		}
	})
}

// WriteResult writes the final execution result.
func (c *Client) WriteResult(ctx context.Context, status, prURL, lastError, activityLogURL, diffURL string) error {
	if activityLogURL != "" || diffURL != "" {
		meta, _ := json.Marshal(map[string]string{
			"activity_log_url": activityLogURL,
			"diff_url":         diffURL,
			"pr_url":           prURL,
		})
		_, _ = c.store.UpsertArtifact(ctx, c.sessionID, "activity_log", "", activityLogURL, "", meta)
	}
	phase := "succeeded"
	if status == "failed" {
		phase = "failed"
	}
	if err := c.store.UpdatePhase(ctx, c.sessionID, phase, phase); err != nil {
		return err
	}
	return c.patchCRDStatus(ctx, func(run *platformv1alpha1.AgentRun) {
		run.Status.Artifacts = ensureRunArtifacts(run.Status.Artifacts)
		run.Status.Artifacts.ActivityLogURL = activityLogURL
		run.Status.Artifacts.DiffURL = diffURL
		run.Status.LastError = lastError
		if prURL != "" {
			run.Status.Artifacts.ReviewSummaryRef = &platformv1alpha1.ArtifactRef{
				Kind: "PullRequest",
				Name: prURL,
			}
		}
		if status == "failed" {
			run.Status.Phase = platformv1alpha1.AgentRunPhaseFailed
			run.Status.CurrentStep = "failed"
			run.Status.Queue = &platformv1alpha1.AgentRunQueueStatus{State: "Failed", BlockedReason: lastError}
		} else {
			run.Status.Phase = platformv1alpha1.AgentRunPhaseSucceeded
			run.Status.CurrentStep = "review-complete"
			run.Status.Queue = &platformv1alpha1.AgentRunQueueStatus{State: "Succeeded"}
		}
	})
}

// --- Owner lookup & notifications ---

// lookupOwnerUserID resolves the owner of the agent_run from resource_ownership.
// Returns empty string if not found (best-effort).
func lookupOwnerUserID(ctx context.Context, ss store.StateStore, runName, runNS string) string {
	owner, err := ss.GetResourceOwner(ctx, "agent_run", runName, runNS)
	if err != nil || owner == nil {
		return ""
	}
	return owner.OwnerID
}

// maybeNotifyOwner creates an in-app notification for the run owner when the
// agent needs actionable user input.
func (c *Client) maybeNotifyOwner(ctx context.Context, inputType platformv1alpha1.UserInputRequestType, message string) {
	if c.ownerUserID == "" {
		return
	}
	title := notificationTitle(inputType)
	if title == "" {
		return // non-actionable type (idle, stopped)
	}
	// Skip if there's already an unread notification for this run.
	if exists, err := c.store.HasUnreadNotification(ctx, c.ownerUserID, "user_input_required", c.runName, c.runNS); err != nil {
		log.Printf("WARN: failed to check existing notification: %v", err)
	} else if exists {
		return
	}
	if err := c.store.CreateNotification(ctx, &store.Notification{
		UserID:            c.ownerUserID,
		Type:              "user_input_required",
		Title:             title,
		Body:              message,
		ResourceType:      "agent_run",
		ResourceID:        c.runName,
		ResourceNamespace: c.runNS,
	}); err != nil {
		log.Printf("WARN: failed to create input notification: %v", err)
	}
}

func notificationTitle(t platformv1alpha1.UserInputRequestType) string {
	switch t {
	case platformv1alpha1.UserInputQuestion:
		return "Agent has a question"
	case platformv1alpha1.UserInputApproval:
		return "Agent needs approval"
	case platformv1alpha1.UserInputPlanReview:
		return "Agent plan ready for review"
	case platformv1alpha1.UserInputTurnLimit:
		return "Agent reached turn limit"
	case platformv1alpha1.UserInputCircuitBreak:
		return "Agent circuit breaker triggered"
	default:
		return "" // idle, stopped — not actionable
	}
}

// --- CRD helpers ---

// toCRDPhase maps internal lowercase phase strings to CRD-validated enum values.
var phaseMap = map[string]platformv1alpha1.AgentRunPhase{
	"pending":         platformv1alpha1.AgentRunPhasePending,
	"admitted":        platformv1alpha1.AgentRunPhaseAdmitted,
	"waitingapproval": platformv1alpha1.AgentRunPhaseWaitingApproval,
	"provisioning":    platformv1alpha1.AgentRunPhaseProvisioning,
	"running":         platformv1alpha1.AgentRunPhaseRunning,
	"question":        platformv1alpha1.AgentRunPhaseQuestion,
	"blocked":         platformv1alpha1.AgentRunPhaseBlocked,
	"succeeded":       platformv1alpha1.AgentRunPhaseSucceeded,
	"failed":          platformv1alpha1.AgentRunPhaseFailed,
	"cancelled":       platformv1alpha1.AgentRunPhaseCancelled,
	"awaiting-user":   platformv1alpha1.AgentRunPhaseQuestion,
}

func toCRDPhase(phase string) platformv1alpha1.AgentRunPhase {
	if p, ok := phaseMap[strings.ToLower(phase)]; ok {
		return p
	}
	// If already a valid CRD phase (e.g. "Running"), use as-is.
	return platformv1alpha1.AgentRunPhase(phase)
}

func (c *Client) patchCRDStatus(ctx context.Context, mutate func(*platformv1alpha1.AgentRun)) error {
	if c.crd == nil {
		return nil
	}
	run := &platformv1alpha1.AgentRun{}
	if err := c.crd.Get(ctx, types.NamespacedName{Name: c.runName, Namespace: c.runNS}, run); err != nil {
		return nil // best-effort dual-write
	}
	patch := client.MergeFrom(run.DeepCopy())
	mutate(run)
	if err := c.crd.Status().Patch(ctx, run, patch); err != nil {
		log.Printf("WARN: CRD status dual-write failed: %v", err)
		return nil // don't fail on CRD write — Postgres is primary
	}
	return nil
}

func ensureRunArtifacts(in *platformv1alpha1.AgentRunArtifacts) *platformv1alpha1.AgentRunArtifacts {
	if in != nil {
		return in
	}
	return &platformv1alpha1.AgentRunArtifacts{}
}
