package dashboard

import (
	"context"
	"fmt"
	"strings"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/mode"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ListAvailableModes returns all available mode templates from K8s CRDs.
func (s *Server) ListAvailableModes(ctx context.Context, req *platform.ListAvailableModesRequest) (*platform.ListAvailableModesResponse, error) {
	resp := &platform.ListAvailableModesResponse{}

	// List all cluster-scoped ModeTemplate CRDs directly.
	var list platformv1alpha1.ModeTemplateList
	if err := s.k8sClient.List(ctx, &list); err != nil {
		return nil, fmt.Errorf("list mode templates: %w", err)
	}
	for i := range list.Items {
		resp.Modes = append(resp.Modes, k8sModeTemplateToProto(&list.Items[i]))
	}

	return resp, nil
}

// actorModeRole maps the verified request identity to a mode RBAC role.
// Internal invocations (no RPC actor recorded) get admin; authenticated users
// are mapped from their JWT role. The system role is never derivable from a
// request — it is reserved for in-cluster automation.
func actorModeRole(ctx context.Context) mode.Role {
	actor, recorded := requestActorFromContextOK(ctx)
	if !recorded {
		return mode.RoleAdmin
	}
	switch strings.ToLower(strings.TrimSpace(actor.Role)) {
	case "admin", "owner":
		return mode.RoleAdmin
	case "member":
		return mode.RoleMember
	default:
		return mode.RoleViewer
	}
}

// GetModeTemplate returns a single mode template by name.
func (s *Server) GetModeTemplate(ctx context.Context, req *platform.GetModeTemplateRequest) (*platform.ModeTemplate, error) {
	name := req.GetName()

	// Read directly from K8s (ModeTemplates are cluster-scoped).
	var tmpl platformv1alpha1.ModeTemplate
	if err := s.k8sClient.Get(ctx, client.ObjectKey{Name: name}, &tmpl); err != nil {
		return nil, fmt.Errorf("get mode template %q: %w", name, err)
	}

	return k8sModeTemplateToProto(&tmpl), nil
}

// SwitchAgentRunMode evaluates and executes a mode switch on an active run.
func (s *Server) SwitchAgentRunMode(ctx context.Context, req *platform.SwitchAgentRunModeRequest) (*platform.SwitchAgentRunModeResponse, error) {
	ns := req.GetNamespace()
	name := req.GetName()
	key := client.ObjectKey{Namespace: ns, Name: name}

	if err := s.requireAgentRunCollaborator(ctx, ns, name, "switch modes on"); err != nil {
		return nil, err
	}

	// Get current run to read mode state.
	var run platformv1alpha1.AgentRun
	if err := s.k8sClient.Get(ctx, key, &run); err != nil {
		return nil, fmt.Errorf("get agent run: %w", err)
	}

	previousMode := run.Status.ModeName

	// Determine the actor role from the verified request identity. The
	// request's source field is informational only — honoring a
	// client-supplied "system" source would bypass all mode RBAC.
	actorRole := actorModeRole(ctx)
	source := strings.TrimSpace(req.GetSource())
	if _, recorded := requestActorFromContextOK(ctx); recorded && strings.EqualFold(source, "system") {
		// "system" additionally bypasses approval gates; it is reserved for
		// in-cluster automation and can never be asserted by an RPC caller.
		source = "user"
	}

	// Resolve target template directly from K8s (no in-memory cache needed).
	// Legacy requests for chat are aliases for the sole autonomous pacing mode.
	targetMode := strings.TrimSpace(req.GetTargetMode())
	if strings.EqualFold(targetMode, "chat") {
		targetMode = "autopilot"
	}
	var target *platformv1alpha1.ModeTemplateSpec
	var resolveErr error
	if req.GetTargetVersion() == "" {
		var crd platformv1alpha1.ModeTemplate
		if err := s.k8sClient.Get(ctx, client.ObjectKey{Name: targetMode}, &crd); err != nil {
			resolveErr = fmt.Errorf("mode template %q not found: %w", targetMode, err)
		} else {
			target = crd.Spec.DeepCopy()
		}
	} else {
		target, resolveErr = mode.ResolveTemplate(ctx, s.k8sClient, targetMode, req.GetTargetVersion())
	}
	if resolveErr == nil && target != nil {
		// Templates specialize instructions and permissions; pacing is always
		// autonomous and completion remains gated on finish.
		target.Autonomous = true
	}
	if resolveErr != nil {
		resp := &platform.SwitchAgentRunModeResponse{
			Result:       string(mode.ResultDenied),
			PreviousMode: previousMode,
			DenialReason: resolveErr.Error(),
		}
		_ = mode.RecordDenied(ctx, s.k8sClient, key, req.GetTargetMode(), resolveErr.Error(), "user", source)
		return resp, nil
	}

	// Evaluate the transition with RBAC and gate checks.
	eval := mode.Evaluate(
		run.Status.ModeSnapshot,
		target,
		mode.EvaluateOpts{
			Run:       &run,
			ActorRole: actorRole,
			Source:    source,
		},
	)

	resp := &platform.SwitchAgentRunModeResponse{
		Result:       string(eval.Result),
		PreviousMode: previousMode,
	}

	switch eval.Result {
	case mode.ResultDenied:
		resp.DenialReason = eval.Reason
		_ = mode.RecordDenied(ctx, s.k8sClient, key, req.GetTargetMode(), eval.Reason, "user", source)
		return resp, nil

	case mode.ResultNoop:
		resp.NewMode = previousMode
		_ = mode.RecordNoop(ctx, s.k8sClient, key, req.GetTargetMode(), eval.Reason)
		return resp, nil

	case mode.ResultApplied:
		event, err := mode.ExecuteSwitch(ctx, s.k8sClient, key, eval, "user", source)
		if err != nil {
			return nil, fmt.Errorf("execute mode switch: %w", err)
		}
		resp.NewMode = event.ToMode
		resp.Revision = run.Status.ModeRevision + 1

		// Write a subtle inline notice so the switch is visible in the
		// conversation without rendering as an assistant chat bubble.
		if s.stateStore != nil {
			if sess, sessErr := s.stateStore.GetSessionByRun(ctx, name, ns); sessErr == nil {
				msg := fmt.Sprintf("Switched to %s mode.", resp.NewMode)
				_, _ = s.stateStore.AppendMessage(ctx, sess.ID, "system", msg, nil)
			}
		}

		return resp, nil

	default:
		return nil, fmt.Errorf("unexpected evaluation result: %s", eval.Result)
	}
}
