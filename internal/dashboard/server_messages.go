package dashboard

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"

	"connectrpc.com/connect"
	"sigs.k8s.io/controller-runtime/pkg/client"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/mcppolicy"
	"github.com/gratefulagents/gratefulagents/internal/mode"
	"github.com/gratefulagents/gratefulagents/internal/store"
	"github.com/gratefulagents/gratefulagents/internal/store/sessionclient"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
)

// planModeName is the ModeTemplate that hosts the plan-first workflow.
// Plan is a regular mode: entering and leaving it goes through the standard
// mode-switch machinery (RBAC, audit, snapshot pinning).
const planModeName = "plan"

// parseSessionModeCommand maps the plan-phase slash commands to their target
// ModeTemplate names. Legacy /chat remains an alias for leaving plan mode, but
// resumes in autopilot because autonomous pacing is the only pacing contract.
func parseSessionModeCommand(message string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(message)) {
	case "/plan":
		return planModeName, true
	case "/exit-plan", "/chat":
		return "autopilot", true
	default:
		return "", false
	}
}

func parseModeCommand(message string) (string, bool) {
	msg := strings.TrimSpace(message)
	if !strings.HasPrefix(strings.ToLower(msg), "/mode ") {
		return "", false
	}
	target := strings.TrimSpace(msg[6:])
	if target == "" {
		return "", false
	}
	return strings.ToLower(target), true
}

// parseAutopilotCommand keeps /autopilot as a compatibility alias. /stop must
// reach the worker so it pauses the current run without changing its mode.
func parseAutopilotCommand(message string) (string, bool) {
	if strings.EqualFold(strings.TrimSpace(message), "/autopilot") {
		return "autopilot", true
	}
	return "", false
}

func parseActionMessage(message string) (string, string, bool) {
	msg := strings.TrimSpace(message)
	if !strings.HasPrefix(msg, "__action:") {
		return "", "", false
	}
	rest := msg[len("__action:"):]
	if idx := strings.IndexByte(rest, ' '); idx >= 0 {
		return rest[:idx], strings.TrimSpace(rest[idx+1:]), true
	}
	return rest, "", true
}

func normalizeMessageMode(mode platform.AgentRunMessageMode) platform.AgentRunMessageMode {
	switch mode {
	case platform.AgentRunMessageMode_AGENT_RUN_MESSAGE_MODE_IMMEDIATE:
		return platform.AgentRunMessageMode_AGENT_RUN_MESSAGE_MODE_IMMEDIATE
	case platform.AgentRunMessageMode_AGENT_RUN_MESSAGE_MODE_UNSPECIFIED,
		platform.AgentRunMessageMode_AGENT_RUN_MESSAGE_MODE_ENQUEUE:
		return platform.AgentRunMessageMode_AGENT_RUN_MESSAGE_MODE_ENQUEUE
	default:
		return platform.AgentRunMessageMode_AGENT_RUN_MESSAGE_MODE_ENQUEUE
	}
}

func userMessageMetadata(mode platform.AgentRunMessageMode) json.RawMessage {
	return userMessageMetadataWithImages(mode, nil)
}

// userMessageMetadataWithImages builds the conversation_messages.metadata JSON
// for a user message, encoding the delivery mode and any image attachments.
func userMessageMetadataWithImages(mode platform.AgentRunMessageMode, images []sessionclient.MessageImage) json.RawMessage {
	normalized := normalizeMessageMode(mode)
	modeStr := strings.ToLower(strings.TrimPrefix(normalized.String(), "AGENT_RUN_MESSAGE_MODE_"))
	return sessionclient.EncodeUserMessageMetadataWithImages(sessionclient.UserMessageMode(modeStr), images)
}

// userMessageMetadataForPendingRequest links a queued answer to the exact input
// request it consumed. The worker uses this nonce when it starts the resumed
// turn to repair the AgentRun CRD mirror after the dashboard has already
// cleared the Postgres request.
func userMessageMetadataForPendingRequest(mode platform.AgentRunMessageMode, requestID string) json.RawMessage {
	metadata := userMessageMetadata(mode)
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return metadata
	}
	var payload map[string]any
	if json.Unmarshal(metadata, &payload) != nil {
		payload = map[string]any{}
	}
	payload["pending_request_id"] = requestID
	encoded, err := json.Marshal(payload)
	if err != nil {
		return metadata
	}
	return encoded
}

type pendingAction struct {
	Label string
	Mode  string
}

// stalePendingRequestError is returned when a client answers a pending input
// request that was already answered by another client or replaced by a newer
// question. The caller's snapshot is stale; it must refresh before acting.
func stalePendingRequestError() *connect.Error {
	return connect.NewError(connect.CodeFailedPrecondition,
		errors.New("this request was already answered or replaced by a newer one; refresh and try again"))
}

func sessionEndedError() *connect.Error {
	return connect.NewError(connect.CodeFailedPrecondition,
		errors.New("run has ended; retry the run to send another message"))
}

// checkRequestedPendingID rejects answers explicitly bound (via
// SendAgentRunMessageRequest.pending_request_id) to a request that is no
// longer the session's current one, so a stale tab cannot answer a question
// it never displayed. An empty requested ID (legacy client) passes.
func checkRequestedPendingID(requestedID string, sess *store.Session) error {
	requestedID = strings.TrimSpace(requestedID)
	if requestedID == "" || requestedID == sess.PendingRequestID {
		return nil
	}
	return stalePendingRequestError()
}

// recordPendingAnswer records one user answer bound to the session's current
// pending input request. With a PendingInputAnswerer store the insert and the
// request consumption are one transaction, so concurrent clients can never
// both answer the same request. Returns stalePendingRequestError when the
// request was consumed or replaced concurrently — nothing is inserted then.
func (s *Server) recordPendingAnswer(ctx context.Context, sess *store.Session, content string, metadata json.RawMessage) error {
	if answerer, ok := s.stateStore.(store.PendingInputAnswerer); ok && sess.PendingRequestID != "" {
		_, answered, err := answerer.AnswerPendingInput(ctx, sess.ID, store.PendingInputAnswer{
			RequestID: sess.PendingRequestID,
			Phase:     "running",
			Content:   content,
			Metadata:  metadata,
		})
		if err != nil {
			if errors.Is(err, store.ErrSessionEnded) {
				return sessionEndedError()
			}
			return connect.NewError(connect.CodeUnavailable, fmt.Errorf("recording answer message: %w", err))
		}
		if !answered {
			return stalePendingRequestError()
		}
		return nil
	}
	// Legacy stores: append, then consume only the exact request. Not atomic,
	// but the nonce-checked clear still never touches a replacement request.
	if _, err := s.stateStore.AppendMessage(ctx, sess.ID, "user", content, metadata); err != nil {
		return connect.NewError(connect.CodeUnavailable, fmt.Errorf("recording answer message: %w", err))
	}
	if clearer, ok := s.stateStore.(store.PendingInputClearer); ok && sess.PendingRequestID != "" {
		if _, err := clearer.ClearPendingInputIfID(ctx, sess.ID, sess.PendingRequestID, "running"); err != nil {
			log.Printf("WARN: clearing exact pending input after answer (session %s): %v", sess.ID, err)
		}
	} else if err := s.stateStore.ClearPendingAction(ctx, sess.ID, "running"); err != nil {
		log.Printf("WARN: clearing pending action after answer (session %s): %v", sess.ID, err)
	}
	return nil
}

// appendActiveUserMessage inserts a plain (non-answer) user message, refusing
// when the session finished concurrently so the message cannot become
// undeliverable.
func (s *Server) appendActiveUserMessage(ctx context.Context, sess *store.Session, content string, metadata json.RawMessage) error {
	if appender, ok := s.stateStore.(store.ActiveUserMessageAppender); ok {
		if _, err := appender.AppendUserMessageIfActive(ctx, sess.ID, content, metadata); err != nil {
			if errors.Is(err, store.ErrSessionEnded) {
				return sessionEndedError()
			}
			return connect.NewError(connect.CodeInternal, fmt.Errorf("writing message to Postgres: %w", err))
		}
		return nil
	}
	if _, err := s.stateStore.AppendMessage(ctx, sess.ID, "user", content, metadata); err != nil {
		return connect.NewError(connect.CodeInternal, fmt.Errorf("writing message to Postgres: %w", err))
	}
	return nil
}

const (
	planAcceptActionID            = "accept_plan"
	legacyAcceptBuildActionID     = "accept_build"
	legacyAcceptBuildAutoActionID = "accept_build_auto"
)

func findPendingAction(pendingActions json.RawMessage, id string) *pendingAction {
	if len(pendingActions) == 0 {
		return nil
	}
	var actions []struct {
		ID    string `json:"id"`
		Label string `json:"label"`
		Mode  string `json:"mode"`
	}
	if json.Unmarshal(pendingActions, &actions) != nil {
		return nil
	}
	for _, a := range actions {
		if a.ID == id {
			return &pendingAction{Label: a.Label, Mode: a.Mode}
		}
	}
	return nil
}

func isPlanAcceptAction(inputType, id string) bool {
	switch id {
	case planAcceptActionID, legacyAcceptBuildActionID, legacyAcceptBuildAutoActionID:
		return true
	case "reject", "request_changes":
		return false
	default:
		// Agent-authored plan actions historically used arbitrary IDs and could
		// attach a nonexistent target mode such as "build". Any affirmative
		// action on a plan-review request approves in place; its mode is ignored.
		return strings.EqualFold(strings.TrimSpace(inputType), string(platformv1alpha1.UserInputPlanReview))
	}
}

// validateQuickAction reports whether an __action:<id> click is acceptable in
// the session's current state. Unknown or out-of-context IDs are rejected so a
// forged or stale action message can neither enter the conversation as a user
// turn nor consume a pending prompt it never belonged to.
func validateQuickAction(actionID string, sess *store.Session) error {
	if findPendingAction(sess.PendingActions, actionID) != nil {
		return nil
	}
	switch actionID {
	case planAcceptActionID, legacyAcceptBuildActionID, legacyAcceptBuildAutoActionID:
		// Inline plan approval is valid without a pending request; its handler
		// separately requires plan mode and a saved plan.
		return nil
	case "approve", "reject", "request_changes":
		// Generic decision buttons are only meaningful while some input request
		// is actually pending.
		if strings.TrimSpace(sess.PendingRequestID) != "" || strings.TrimSpace(sess.PendingInputType) != "" {
			return nil
		}
		return connect.NewError(connect.CodeFailedPrecondition,
			fmt.Errorf("no pending request accepts action %q", actionID))
	}
	return connect.NewError(connect.CodeFailedPrecondition,
		fmt.Errorf("unknown action %q for the current pending request", actionID))
}

// runInPlanMode reports whether the run's active mode template is the plan
// mode. There is no separate per-session plan state.
func runInPlanMode(run *platformv1alpha1.AgentRun) bool {
	return run != nil && strings.EqualFold(strings.TrimSpace(run.Status.ModeName), planModeName)
}

func (s *Server) runHasSavedPlan(ctx context.Context, run *platformv1alpha1.AgentRun, sess *store.Session) bool {
	if s.stateStore != nil && sess != nil {
		if art, err := s.stateStore.GetArtifact(ctx, sess.ID, "plan"); err == nil && art != nil && strings.TrimSpace(art.Content) != "" {
			return true
		}
	}
	return run != nil && run.Status.Artifacts != nil && run.Status.Artifacts.PlanRef != nil && strings.TrimSpace(run.Status.Artifacts.PlanRef.Name) != ""
}

func (s *Server) handlePlanAcceptAction(ctx context.Context, req *platform.SendAgentRunMessageRequest, run *platformv1alpha1.AgentRun, sess *store.Session, action *pendingAction, freeform string) error {
	// Inline plan approval buttons are intentionally not tied to a pending
	// present_plan action. When no pending action matches, require the run to
	// still be in plan mode and have a saved/current plan before accepting.
	if action == nil {
		if !runInPlanMode(run) {
			return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("plan acceptance requires plan mode"))
		}
		if !s.runHasSavedPlan(ctx, run, sess) {
			return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("no saved plan is available to accept"))
		}
	}

	// Plan approval is a continuation signal, not a mode transition. Refresh a
	// plan run in place so sessions paused under the former read-only template
	// receive the current template permissions before implementation resumes.
	if runInPlanMode(run) {
		if _, err := mode.RefreshCurrentSnapshot(ctx, s.k8sClient, client.ObjectKey{Namespace: req.Namespace, Name: req.Name}); err != nil {
			return connect.NewError(connect.CodeUnavailable, fmt.Errorf("refreshing plan mode for implementation: %w", err))
		}
	}
	resumeMessage := "Plan approved. Continue with implementation."
	if trimmed := strings.TrimSpace(freeform); trimmed != "" {
		resumeMessage = fmt.Sprintf("%s Notes: %s", resumeMessage, trimmed)
	}
	metadata := userMessageMetadataForPendingRequest(req.GetMessageMode(), sess.PendingRequestID)
	if sess.PendingRequestID != "" {
		// Atomic: record the continuation and consume the exact request in one
		// transaction, so a failed persist never leaves the prompt cleared and a
		// concurrent answer never double-approves.
		return s.recordPendingAnswer(ctx, sess, resumeMessage, metadata)
	}
	// Inline plan approval with no pending request: nothing to consume.
	return s.appendActiveUserMessage(ctx, sess, resumeMessage, metadata)
}

// SendAgentRunMessage routes a public run-surface message to the current compatibility adapter.
func (s *Server) SendAgentRunMessage(ctx context.Context, req *platform.SendAgentRunMessageRequest) (*platform.SendAgentRunMessageResponse, error) {
	if err := s.requireAgentRunCollaborator(ctx, req.Namespace, req.Name, "send messages to"); err != nil {
		return nil, err
	}
	run := &platformv1alpha1.AgentRun{}
	key := client.ObjectKey{Namespace: req.Namespace, Name: req.Name}
	if err := s.k8sClient.Get(ctx, key, run); err != nil {
		return nil, mapK8sError(fmt.Sprintf("get AgentRun %s/%s", req.Namespace, req.Name), err)
	}
	hasMediaAttachments := len(req.GetImageDataUrls()) > 0 || len(req.GetVideoDataUrls()) > 0
	rejectCommandAttachments := func() error {
		if !hasMediaAttachments {
			return nil
		}
		return connect.NewError(connect.CodeInvalidArgument, errors.New("attachments cannot be combined with chat control commands"))
	}
	readinessErr := func() error {
		if s.stateStore == nil {
			return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("state store not configured"))
		}
		sess, err := s.stateStore.GetSessionByRun(ctx, req.Name, req.Namespace)
		if err != nil {
			return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("session is still starting up"))
		}
		if ready, reason := agentRunMessageReadiness(run, sess); !ready {
			return connect.NewError(connect.CodeFailedPrecondition, errors.New(strings.TrimSpace(reason)))
		}
		return nil
	}

	// Handle /plan and /exit-plan (or /chat) as explicit mode switches into and
	// out of the plan ModeTemplate. Plan approval itself never uses this path.
	if targetMode, ok := parseSessionModeCommand(req.Message); ok {
		if err := rejectCommandAttachments(); err != nil {
			return nil, err
		}
		if err := readinessErr(); err != nil {
			return nil, err
		}
		return s.switchModeViaCommand(ctx, req, targetMode)
	}

	// Handle /autopilot and /stop as autonomy toggles. They map to a mode switch
	// between the autonomous "autopilot" template and "chat".
	if autopilotTarget, ok := parseAutopilotCommand(req.Message); ok {
		if err := rejectCommandAttachments(); err != nil {
			return nil, err
		}
		if err := readinessErr(); err != nil {
			return nil, err
		}
		return s.switchModeViaCommand(ctx, req, autopilotTarget)
	}

	// Handle /mode <name> command for configurable mode switching.
	if targetModeName, ok := parseModeCommand(req.Message); ok {
		if err := rejectCommandAttachments(); err != nil {
			return nil, err
		}
		if err := readinessErr(); err != nil {
			return nil, err
		}
		return s.switchModeViaCommand(ctx, req, targetModeName)
	}

	// Handle __action:<id> messages from quick-action buttons.
	if actionID, freeform, ok := parseActionMessage(req.Message); ok && s.stateStore != nil {
		if err := rejectCommandAttachments(); err != nil {
			return nil, err
		}
		if err := readinessErr(); err != nil {
			return nil, err
		}
		sess, err := s.stateStore.GetSessionByRun(ctx, req.Name, req.Namespace)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("looking up session: %w", err))
		}

		action := findPendingAction(sess.PendingActions, actionID)
		label := actionID
		if action != nil {
			label = action.Label
		}
		// A client that displayed a specific question must not answer a newer
		// one it never saw.
		if err := checkRequestedPendingID(req.GetPendingRequestId(), sess); err != nil {
			return nil, err
		}
		pendingMCPRequest, err := mcppolicy.PendingRequest(run)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("decoding pending MCP break-glass request: %w", err))
		}
		if pendingMCPRequest != nil {
			switch actionID {
			case "approve":
				if err := s.handleApproveMCPBreakGlass(ctx, run, sess, pendingMCPRequest, freeform); err != nil {
					return nil, err
				}
			case "reject":
				if err := s.handleRejectMCPBreakGlass(ctx, run, sess, pendingMCPRequest, freeform); err != nil {
					return nil, err
				}
			default:
				return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("MCP break-glass requests must be approved or rejected with the action buttons"))
			}
			// The MCP handler conditionally consumes the exact Postgres request;
			// never clear here because a replacement may have raced the decision.
			return &platform.SendAgentRunMessageResponse{}, nil
		}

		// Reject forged or stale action IDs before they can enter the
		// conversation as a user turn or consume the pending prompt.
		if err := validateQuickAction(actionID, sess); err != nil {
			return nil, err
		}

		answerMetadata := userMessageMetadataForPendingRequest(req.GetMessageMode(), sess.PendingRequestID)
		switch {
		case isPlanAcceptAction(sess.PendingInputType, actionID):
			if err := s.handlePlanAcceptAction(ctx, req, run, sess, action, freeform); err != nil {
				return nil, err
			}

		case action != nil && action.Mode != "":
			// Approve — trigger mode switch via standard path (user's real role).
			switchResp, err := s.SwitchAgentRunMode(ctx, &platform.SwitchAgentRunModeRequest{
				Namespace:  req.Namespace,
				Name:       req.Name,
				TargetMode: action.Mode,
				Source:     "action-button",
			})
			if err != nil {
				return nil, err
			}
			if strings.EqualFold(strings.TrimSpace(switchResp.Result), "denied") {
				// Agent-authored buttons can target modes that don't exist (or
				// that the user's role can't reach). Silently resuming here
				// leaves the run in whatever mode it was in — for plan-mode
				// runs that means every turn stays clamped read-only while the
				// UI still shows a write-capable policy. Fail loudly instead
				// and keep the buttons pending.
				reason := strings.TrimSpace(switchResp.DenialReason)
				if reason == "" {
					reason = "mode switch denied"
				}
				msg := fmt.Sprintf("Mode switch to %q denied: %s", action.Mode, reason)
				if runInPlanMode(run) {
					msg += " The run remains in plan mode; choose an available mode from the mode menu if you want to switch."
				}
				if _, appendErr := s.stateStore.AppendMessage(ctx, sess.ID, "system", msg, nil); appendErr != nil {
					log.Printf("WARN: recording denied mode-switch action (session %s): %v", sess.ID, appendErr)
				}
				return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("%s", msg))
			}
			msg := label
			if freeform != "" {
				msg += ": " + freeform
			}
			// Atomic: the continuation message and the request consumption are
			// one transaction. If persisting fails the prompt stays pending, and
			// a retry is safe because the mode switch becomes a no-op.
			if sess.PendingRequestID != "" {
				if err := s.recordPendingAnswer(ctx, sess, msg, answerMetadata); err != nil {
					return nil, err
				}
			} else if err := s.appendActiveUserMessage(ctx, sess, msg, answerMetadata); err != nil {
				return nil, err
			}
			if switchResp.Result == "applied" {
				if _, appendErr := s.stateStore.AppendMessage(ctx, sess.ID, "assistant",
					fmt.Sprintf("Mode switched to **%s**.", switchResp.NewMode), nil); appendErr != nil {
					log.Printf("WARN: recording mode-switch notice (session %s): %v", sess.ID, appendErr)
				}
			}

		case actionID == "reject":
			// Reject — stay paused, don't auto-continue.
			// Consume the exact request first so a concurrent approval and this
			// rejection cannot both take effect, then record the decision as a
			// system message: a user message would make the agent loop treat the
			// reject click itself as fresh turn input.
			if sess.PendingRequestID != "" {
				if clearer, ok := s.stateStore.(store.PendingInputClearer); ok {
					cleared, err := clearer.ClearPendingInputIfID(ctx, sess.ID, sess.PendingRequestID, "running")
					if err != nil {
						return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("recording rejection: %w", err))
					}
					if !cleared {
						return nil, stalePendingRequestError()
					}
				} else if err := s.stateStore.ClearPendingAction(ctx, sess.ID, "running"); err != nil {
					log.Printf("WARN: clearing pending action after rejection (session %s): %v", sess.ID, err)
				}
			}
			msg := "Plan rejected. Waiting for your next message."
			if freeform != "" {
				msg = fmt.Sprintf("Plan rejected: %s\nWaiting for your next message.", freeform)
			}
			if _, err := s.stateStore.AppendMessage(ctx, sess.ID, "system", msg, nil); err != nil {
				// The request is already consumed; losing the note keeps the run
				// paused, which is what reject means. Don't fail the call.
				log.Printf("WARN: recording rejection note (session %s): %v", sess.ID, err)
			}

		case actionID == "approve":
			// Generic approval token. Phase approval gates have been removed; an
			// approve action now just records the canonical token so the agent
			// loop resumes on the next turn.
			msg := "approve"
			if freeform != "" {
				msg += ": " + freeform
			}
			if err := s.recordPendingAnswer(ctx, sess, msg, answerMetadata); err != nil {
				return nil, err
			}

		case actionID == "request_changes":
			// Compatibility fallback: older approval prompts may still surface a
			// request_changes button. Treat it as immediate feedback that should
			// resume the run.
			msg := "Please revise and continue."
			if freeform != "" {
				msg = fmt.Sprintf("Please revise and continue. Feedback: %s", freeform)
			}
			if err := s.recordPendingAnswer(ctx, sess, msg, answerMetadata); err != nil {
				return nil, err
			}

		default:
			// Request changes or other actions — agent resumes normally.
			msg := label
			if freeform != "" {
				msg += ": " + freeform
			}
			if err := s.recordPendingAnswer(ctx, sess, msg, answerMetadata); err != nil {
				return nil, err
			}
		}

		// Each branch above consumes the exact request it answered inside its
		// own write, so a replacement request created concurrently always
		// remains visible and a failed write never clears the prompt. Legacy
		// sessions without a request nonce cannot be consumed atomically;
		// clear their pending action state now that the answer is recorded.
		if sess.PendingRequestID == "" {
			if err := s.stateStore.ClearPendingAction(ctx, sess.ID, "running"); err != nil {
				log.Printf("WARN: clearing pending action after quick action (session %s): %v", sess.ID, err)
			}
		}
		return &platform.SendAgentRunMessageResponse{}, nil
	}

	// Write message to Postgres (single source of truth).
	if s.stateStore == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("state store not configured"))
	}
	sess, err := s.stateStore.GetSessionByRun(ctx, req.Name, req.Namespace)
	if err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("session is still starting up"))
	}
	if ready, reason := agentRunMessageReadiness(run, sess); !ready {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New(strings.TrimSpace(reason)))
	}
	if pendingMCPRequest, err := mcppolicy.PendingRequest(run); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("decoding pending MCP break-glass request: %w", err))
	} else if pendingMCPRequest != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("MCP break-glass approval is pending. Use the approve or reject action to continue."))
	}
	// A message explicitly bound to a request the session no longer has is a
	// stale tab answering a replaced question. Reject before persisting assets.
	explicitAnswer := strings.TrimSpace(req.GetPendingRequestId()) != ""
	if err := checkRequestedPendingID(req.GetPendingRequestId(), sess); err != nil {
		return nil, err
	}
	images, err := sessionclient.ParseImageDataURLs(req.GetImageDataUrls())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid image attachment: %w", err))
	}
	videoFrames, err := sessionclient.ParseVideoDataURLs(ctx, req.GetVideoDataUrls(), 8-len(images))
	if err != nil {
		return nil, videoAttachmentError(err)
	}
	images = append(images, videoFrames...)
	images, err = s.persistMessageImageAssets(ctx, run, images)
	if err != nil {
		return nil, messageAssetError(err)
	}
	metadata := userMessageMetadataWithImages(req.GetMessageMode(), images)
	if sess.PendingRequestID != "" {
		var payload map[string]any
		_ = json.Unmarshal(metadata, &payload)
		payload["pending_request_id"] = sess.PendingRequestID
		metadata, _ = json.Marshal(payload)
	}

	if answerer, ok := s.stateStore.(store.PendingInputAnswerer); ok && sess.PendingRequestID != "" {
		// Atomic: the message becomes visible and the exact request is consumed
		// in one transaction, so two clients can never both answer it.
		_, answered, err := answerer.AnswerPendingInput(ctx, sess.ID, store.PendingInputAnswer{
			RequestID: sess.PendingRequestID,
			Phase:     "running",
			Content:   req.Message,
			Metadata:  metadata,
		})
		switch {
		case errors.Is(err, store.ErrSessionEnded):
			s.deleteMessageImageAssets(ctx, images)
			return nil, sessionEndedError()
		case err != nil:
			s.deleteMessageImageAssets(ctx, images)
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("writing message to Postgres: %w", err))
		case answered:
			return &platform.SendAgentRunMessageResponse{}, nil
		case explicitAnswer:
			// The exact question this client answered is gone.
			s.deleteMessageImageAssets(ctx, images)
			return nil, stalePendingRequestError()
		}
		// The request was consumed or replaced between the session snapshot and
		// the answer. A legacy freeform message is still valid steering input;
		// deliver it without consuming the newer request.
		metadata = userMessageMetadataWithImages(req.GetMessageMode(), images)
		if err := s.appendActiveUserMessage(ctx, sess, req.Message, metadata); err != nil {
			s.deleteMessageImageAssets(ctx, images)
			return nil, err
		}
		return &platform.SendAgentRunMessageResponse{}, nil
	}

	if err := s.appendActiveUserMessage(ctx, sess, req.Message, metadata); err != nil {
		s.deleteMessageImageAssets(ctx, images)
		return nil, err
	}

	// Legacy stores without the atomic answer extension: consume only the
	// exact request the message answered, never a replacement.
	if clearer, ok := s.stateStore.(store.PendingInputClearer); ok && sess.PendingRequestID != "" {
		if _, err := clearer.ClearPendingInputIfID(ctx, sess.ID, sess.PendingRequestID, "running"); err != nil {
			log.Printf("WARN: clearing exact pending input after user message (session %s): %v", sess.ID, err)
		}
	} else if sess.PendingRequestID != "" || strings.TrimSpace(sess.PendingInputType) != "" {
		if strings.EqualFold(strings.TrimSpace(sess.PendingInputType), string(platformv1alpha1.UserInputApproval)) {
			if err := s.stateStore.ClearPendingAction(ctx, sess.ID, "running"); err != nil {
				log.Printf("WARN: clearing pending action after user message (session %s): %v", sess.ID, err)
			}
		} else if err := s.stateStore.ClearPendingQuestion(ctx, sess.ID, "running"); err != nil {
			log.Printf("WARN: clearing pending question after user message (session %s): %v", sess.ID, err)
		}
	}

	return &platform.SendAgentRunMessageResponse{}, nil
}

// CancelAgentRunMessage withdraws a pending (queued or steering) user message
// before the agent loop consumes it. Once the message has been delivered the
// cancellation fails with FailedPrecondition — the turn already saw it.
func (s *Server) CancelAgentRunMessage(ctx context.Context, req *platform.CancelAgentRunMessageRequest) (*platform.CancelAgentRunMessageResponse, error) {
	if err := s.requireAgentRunCollaborator(ctx, req.Namespace, req.Name, "cancel messages on"); err != nil {
		return nil, err
	}
	if req.MessageId <= 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("message_id is required"))
	}
	if s.stateStore == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("state store not configured"))
	}
	sess, err := s.stateStore.GetSessionByRun(ctx, req.Name, req.Namespace)
	if err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("session is still starting up"))
	}
	messages, err := s.stateStore.GetMessagesIncludingCancelled(ctx, sess.ID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("loading message attachments for cancellation: %w", err))
	}
	var generatedImages []sessionclient.MessageImage
	for _, message := range messages {
		if message.ID == req.MessageId && message.Role == "user" {
			generatedImages = sessionclient.ImagesFromMetadata(message.Metadata)
			break
		}
	}
	if err := s.stateStore.CancelUndeliveredUserMessage(ctx, sess.ID, req.MessageId); err != nil {
		switch {
		case errors.Is(err, store.ErrMessageDelivered):
			return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("the agent already picked this message up"))
		case errors.Is(err, store.ErrMessageNotFound):
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("message not found"))
		default:
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("cancelling message: %w", err))
		}
	}
	s.deleteMessageImageAssets(ctx, generatedImages)
	return &platform.CancelAgentRunMessageResponse{}, nil
}

// switchModeViaCommand routes a /mode, /autopilot, /stop, /plan, or /exit-plan
// chat command through SwitchAgentRunMode and records any denial/no-op result.
// The raw command is never persisted as a user message so it does not render
// as a chat bubble.
func (s *Server) switchModeViaCommand(ctx context.Context, req *platform.SendAgentRunMessageRequest, targetMode string) (*platform.SendAgentRunMessageResponse, error) {
	switchResp, err := s.SwitchAgentRunMode(ctx, &platform.SwitchAgentRunModeRequest{
		Namespace:  req.Namespace,
		Name:       req.Name,
		TargetMode: targetMode,
		Source:     "chat-command",
	})
	if err != nil {
		return nil, err
	}
	if s.stateStore != nil {
		if sess, sessErr := s.stateStore.GetSessionByRun(ctx, req.Name, req.Namespace); sessErr == nil {
			// The "applied" notice is written by SwitchAgentRunMode itself.
			var msg string
			switch switchResp.Result {
			case "denied":
				msg = fmt.Sprintf("Mode switch denied: %s", switchResp.DenialReason)
			case "noop":
				msg = fmt.Sprintf("Already in mode %s.", switchResp.PreviousMode)
			}
			if msg != "" {
				if _, err := s.stateStore.AppendMessage(ctx, sess.ID, "system", msg, nil); err != nil {
					log.Printf("WARN: recording mode-switch result message (session %s): %v", sess.ID, err)
				}
			}
		}
	}
	return &platform.SendAgentRunMessageResponse{}, nil
}
