package dashboard

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"connectrpc.com/connect"
	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/auth"
	"github.com/gratefulagents/gratefulagents/internal/mcppolicy"
	"github.com/gratefulagents/gratefulagents/internal/store"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var errMCPBreakGlassRequestChanged = errors.New("MCP break-glass request changed before the decision was applied")

func (s *Server) handleApproveMCPBreakGlass(ctx context.Context, run *platformv1alpha1.AgentRun, sess *store.Session, request *mcppolicy.BreakGlassRequest, note string) error {
	if request == nil {
		return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("no MCP break-glass request is pending"))
	}
	policy, err := s.resolveMCPPolicyForRun(ctx, run)
	if err != nil {
		return connect.NewError(connect.CodeInternal, fmt.Errorf("resolving MCPPolicy: %w", err))
	}
	evaluator := mcppolicy.NewEvaluator(run, policy)
	cfg := evaluator.BreakGlass()
	if !cfg.Enabled {
		return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("MCP break-glass is no longer enabled for this run"))
	}
	if cfg.RequireAuditReason && strings.TrimSpace(request.Reason) == "" {
		return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("MCP break-glass approval requires an audit reason"))
	}

	actor := requestActorFromContext(ctx)
	if cfg.AdminMediated && !strings.EqualFold(strings.TrimSpace(actor.Role), auth.RoleAdmin) {
		return connect.NewError(connect.CodePermissionDenied, fmt.Errorf("MCP break-glass approval requires an admin"))
	}

	// Keep the observed request byte-for-byte intact for the race check. Only
	// bound copies that are written to grants, audit details, or conversation.
	boundedReason := truncateForConversation(request.Reason)
	note = truncateForConversation(note)

	decidedAt := time.Now().UTC().Format(time.RFC3339)
	decidedBy := strings.TrimSpace(actor.Subject)
	grant := mcppolicy.BreakGlassGrant{
		RequestID:   request.ID,
		Server:      request.Server,
		Tool:        request.Tool,
		Reason:      boundedReason,
		RequestedAt: request.RequestedAt,
		RequestedBy: request.RequestedBy,
		ApprovedAt:  decidedAt,
		ApprovedBy:  decidedBy,
	}
	if err := s.patchMCPBreakGlassDecision(ctx, run, request, func(fresh *platformv1alpha1.AgentRun) error {
		if fresh.Annotations == nil {
			fresh.Annotations = map[string]string{}
		}
		grants, err := mcppolicy.GrantedGrants(fresh)
		if err != nil {
			return err
		}
		if err := mcppolicy.SetGrantedGrants(fresh.Annotations, mcppolicy.UpsertGrant(grants, grant)); err != nil {
			return err
		}
		decisions, err := mcppolicy.BreakGlassDecisions(fresh)
		if err != nil {
			return err
		}
		decision := mcppolicy.BreakGlassDecision{RequestID: request.ID, Decision: "approved", DecidedAt: decidedAt, DecidedBy: decidedBy}
		if err := mcppolicy.SetBreakGlassDecisions(fresh.Annotations, mcppolicy.UpsertBreakGlassDecision(decisions, decision)); err != nil {
			return err
		}
		mcppolicy.ClearPendingRequest(fresh.Annotations)
		return nil
	}); err != nil {
		return mapMCPBreakGlassDecisionError("approval", err)
	}
	if err := s.clearExactMCPInput(ctx, sess); err != nil {
		return err
	}

	systemMessage := fmt.Sprintf("[SYSTEM] MCP break-glass approved for %s.", formatMCPBreakGlassTarget(request.Server, request.Tool))
	if boundedReason != "" {
		systemMessage += fmt.Sprintf(" Approved reason: %s.", boundedReason)
	}
	if strings.TrimSpace(note) != "" {
		systemMessage += fmt.Sprintf(" Approval note: %s.", strings.TrimSpace(note))
	}
	if _, err := s.stateStore.AppendMessage(ctx, sess.ID, "system", systemMessage, nil); err != nil {
		log.Printf("WARN: recording MCP break-glass approval system message (session %s): %v", sess.ID, err)
	}
	userMessage := fmt.Sprintf("MCP break-glass approved for %s. Continue.", formatMCPBreakGlassTarget(request.Server, request.Tool))
	if _, err := s.stateStore.AppendMessage(ctx, sess.ID, "user", userMessage, nil); err != nil {
		// The approval annotation is already persisted; without this message the
		// agent never resumes, so surface the failure instead of hanging silently.
		return connect.NewError(connect.CodeUnavailable, fmt.Errorf("approval was recorded but the resume message failed; send a message to continue the run: %w", err))
	}

	detail, _ := json.Marshal(map[string]string{
		"server":      request.Server,
		"tool":        request.Tool,
		"reason":      boundedReason,
		"approved_by": strings.TrimSpace(actor.Subject),
		"note":        strings.TrimSpace(note),
	})
	if _, err := s.stateStore.WriteActivityEvent(ctx, sess.ID, "mcp_break_glass_approved", summarizeMCPBreakGlassTarget("Approved", request.Server, request.Tool), detail); err != nil {
		log.Printf("WARN: recording MCP break-glass approval audit event (session %s): %v", sess.ID, err)
	}
	return nil
}

func (s *Server) handleRejectMCPBreakGlass(ctx context.Context, run *platformv1alpha1.AgentRun, sess *store.Session, request *mcppolicy.BreakGlassRequest, note string) error {
	if request == nil {
		return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("no MCP break-glass request is pending"))
	}
	note = truncateForConversation(note)
	actor := requestActorFromContext(ctx)
	decidedAt := time.Now().UTC().Format(time.RFC3339)
	decidedBy := strings.TrimSpace(actor.Subject)
	if err := s.patchMCPBreakGlassDecision(ctx, run, request, func(fresh *platformv1alpha1.AgentRun) error {
		if fresh.Annotations == nil {
			fresh.Annotations = map[string]string{}
		}
		decisions, err := mcppolicy.BreakGlassDecisions(fresh)
		if err != nil {
			return err
		}
		decision := mcppolicy.BreakGlassDecision{RequestID: request.ID, Decision: "denied", DecidedAt: decidedAt, DecidedBy: decidedBy}
		if err := mcppolicy.SetBreakGlassDecisions(fresh.Annotations, mcppolicy.UpsertBreakGlassDecision(decisions, decision)); err != nil {
			return err
		}
		mcppolicy.ClearPendingRequest(fresh.Annotations)
		return nil
	}); err != nil {
		return mapMCPBreakGlassDecisionError("denial", err)
	}
	if err := s.clearExactMCPInput(ctx, sess); err != nil {
		return err
	}

	systemMessage := fmt.Sprintf("[SYSTEM] MCP break-glass was denied for %s. Continue without that access.", formatMCPBreakGlassTarget(request.Server, request.Tool))
	if strings.TrimSpace(note) != "" {
		systemMessage += fmt.Sprintf(" Feedback: %s.", strings.TrimSpace(note))
	}
	if _, err := s.stateStore.AppendMessage(ctx, sess.ID, "system", systemMessage, nil); err != nil {
		log.Printf("WARN: recording MCP break-glass denial system message (session %s): %v", sess.ID, err)
	}

	userMessage := fmt.Sprintf("MCP break-glass denied for %s. Continue without that access.", formatMCPBreakGlassTarget(request.Server, request.Tool))
	if strings.TrimSpace(note) != "" {
		userMessage += " " + strings.TrimSpace(note)
	}
	if _, err := s.stateStore.AppendMessage(ctx, sess.ID, "user", userMessage, nil); err != nil {
		// The denial is already persisted; without this message the agent never
		// resumes, so surface the failure instead of hanging silently.
		return connect.NewError(connect.CodeUnavailable, fmt.Errorf("denial was recorded but the resume message failed; send a message to continue the run: %w", err))
	}

	detail, _ := json.Marshal(map[string]string{
		"server": request.Server,
		"tool":   request.Tool,
		"reason": request.Reason,
		"note":   strings.TrimSpace(note),
	})
	if _, err := s.stateStore.WriteActivityEvent(ctx, sess.ID, "mcp_break_glass_denied", summarizeMCPBreakGlassTarget("Denied", request.Server, request.Tool), detail); err != nil {
		log.Printf("WARN: recording MCP break-glass denial audit event (session %s): %v", sess.ID, err)
	}
	return nil
}

func (s *Server) resolveMCPPolicyForRun(ctx context.Context, run *platformv1alpha1.AgentRun) (*platformv1alpha1.MCPPolicy, error) {
	if run == nil || run.Spec.MCPPolicyRef == nil || strings.TrimSpace(run.Spec.MCPPolicyRef.Name) == "" {
		return nil, nil
	}
	policy := &platformv1alpha1.MCPPolicy{}
	if err := s.k8sClient.Get(ctx, client.ObjectKey{Namespace: run.Namespace, Name: run.Spec.MCPPolicyRef.Name}, policy); err != nil {
		return nil, err
	}
	return policy, nil
}

func (s *Server) patchMCPBreakGlassDecision(ctx context.Context, run *platformv1alpha1.AgentRun, expected *mcppolicy.BreakGlassRequest, mutateAnnotations func(*platformv1alpha1.AgentRun) error) error {
	key := client.ObjectKeyFromObject(run)
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		fresh := &platformv1alpha1.AgentRun{}
		if err := s.k8sClient.Get(ctx, key, fresh); err != nil {
			return err
		}
		current, err := mcppolicy.PendingRequest(fresh)
		if err != nil {
			return err
		}
		if !mcppolicy.SameBreakGlassRequest(current, expected) {
			return errMCPBreakGlassRequestChanged
		}

		metaPatch := client.MergeFrom(fresh.DeepCopy())
		if err := mutateAnnotations(fresh); err != nil {
			return err
		}
		return s.k8sClient.Patch(ctx, fresh, metaPatch)
	})
}

func (s *Server) clearExactMCPInput(ctx context.Context, sess *store.Session) error {
	clearer, ok := s.stateStore.(store.PendingInputClearer)
	if !ok {
		return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("state store does not support request-bound approval"))
	}
	cleared, err := clearer.ClearPendingInputIfID(ctx, sess.ID, sess.PendingRequestID, "running")
	if err != nil {
		return connect.NewError(connect.CodeUnavailable, fmt.Errorf("clearing exact MCP approval request: %w", err))
	}
	if !cleared {
		return connect.NewError(connect.CodeFailedPrecondition, errMCPBreakGlassRequestChanged)
	}
	key := client.ObjectKey{Namespace: sess.AgentRunNS, Name: sess.AgentRunName}
	if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		fresh := &platformv1alpha1.AgentRun{}
		if err := s.k8sClient.Get(ctx, key, fresh); err != nil {
			return err
		}
		if fresh.Status.Phase != platformv1alpha1.AgentRunPhaseWaitingApproval {
			return nil
		}
		patch := client.MergeFrom(fresh.DeepCopy())
		fresh.Status.Phase = platformv1alpha1.AgentRunPhaseRunning
		fresh.Status.Queue = &platformv1alpha1.AgentRunQueueStatus{State: "Running"}
		return s.k8sClient.Status().Patch(ctx, fresh, patch)
	}); err != nil {
		return connect.NewError(connect.CodeUnavailable, fmt.Errorf("resuming AgentRun after MCP decision: %w", err))
	}
	return nil
}

func mapMCPBreakGlassDecisionError(decision string, err error) error {
	if errors.Is(err, errMCPBreakGlassRequestChanged) {
		return connect.NewError(connect.CodeFailedPrecondition, err)
	}
	return connect.NewError(connect.CodeInternal, fmt.Errorf("persisting MCP break-glass %s: %w", decision, err))
}

func formatMCPBreakGlassTarget(server, tool string) string {
	if strings.TrimSpace(tool) == "" {
		return fmt.Sprintf("server %q", server)
	}
	return fmt.Sprintf("server %q tool %q", server, tool)
}

// truncateForConversation caps user-controlled free-form text injected into
// agent conversations.
func truncateForConversation(s string) string {
	const maxLen = 1000
	s = strings.TrimSpace(s)
	if len(s) > maxLen {
		return s[:maxLen] + "…"
	}
	return s
}

func summarizeMCPBreakGlassTarget(verb, server, tool string) string {
	if strings.TrimSpace(tool) == "" {
		return fmt.Sprintf("%s MCP break-glass for server %q", verb, server)
	}
	return fmt.Sprintf("%s MCP break-glass for %q/%q", verb, server, tool)
}
