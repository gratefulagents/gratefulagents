package dashboard

import (
	"context"
	"fmt"
	"strings"

	"connectrpc.com/connect"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
)

func agentRunOverseerSpecFromProto(config *platform.AgentRunOverseerConfig) (*platformv1alpha1.AgentRunOverseerSpec, error) {
	if config == nil {
		return nil, nil
	}
	modeName := strings.TrimSpace(config.ModeRefName)
	modeVersion := strings.TrimSpace(config.ModeRefVersion)
	modeChannel := strings.TrimSpace(config.ModeRefChannel)
	if modeName == "" && (modeVersion != "" || modeChannel != "") {
		return nil, fmt.Errorf("overseer mode version and channel require a mode name")
	}
	authority := strings.ToLower(strings.TrimSpace(config.Authority))
	if authority == "" {
		authority = string(platformv1alpha1.AgentRunOverseerAuthorityAdvise)
	}
	switch platformv1alpha1.AgentRunOverseerAuthority(authority) {
	case platformv1alpha1.AgentRunOverseerAuthorityObserve, platformv1alpha1.AgentRunOverseerAuthorityAdvise, platformv1alpha1.AgentRunOverseerAuthorityEnforce:
	default:
		return nil, fmt.Errorf("invalid overseer authority %q (want observe, advise, or enforce)", config.Authority)
	}
	interval := int32(10)
	if config.IntervalMinutes != nil {
		interval = *config.IntervalMinutes
		if interval < 1 || interval > platformv1alpha1.AgentRunOverseerMaxIntervalMinutes {
			return nil, fmt.Errorf("overseer interval minutes must be between 1 and %d", platformv1alpha1.AgentRunOverseerMaxIntervalMinutes)
		}
	}
	maxInterventions := int32(5)
	if config.MaxInterventions != nil {
		maxInterventions = *config.MaxInterventions
		if maxInterventions < 0 || maxInterventions > platformv1alpha1.AgentRunOverseerMaxInterventions {
			return nil, fmt.Errorf("overseer max interventions must be between 0 and %d", platformv1alpha1.AgentRunOverseerMaxInterventions)
		}
	}
	spec := &platformv1alpha1.AgentRunOverseerSpec{Model: strings.TrimSpace(config.Model), Authority: platformv1alpha1.AgentRunOverseerAuthority(authority), IntervalMinutes: interval, MaxInterventions: maxInterventions}
	if modeName != "" {
		spec.ModeRef = &platformv1alpha1.ModeRef{Name: modeName, Version: modeVersion, Channel: modeChannel}
	}
	return spec, nil
}

func validateOverseerAuthority(authority string) (platformv1alpha1.AgentRunOverseerAuthority, error) {
	spec, err := agentRunOverseerSpecFromProto(&platform.AgentRunOverseerConfig{Authority: authority})
	if err != nil {
		return "", err
	}
	return spec.Authority, nil
}

func (s *Server) AttachAgentRunOverseer(ctx context.Context, req *platform.AttachAgentRunOverseerRequest) (*platform.AgentRun, error) {
	if req.Overseer == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("overseer config is required"))
	}
	overseer, err := agentRunOverseerSpecFromProto(req.Overseer)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	if err := s.requireAgentRunOwner(ctx, req.Namespace, req.Name, "attach an overseer to"); err != nil {
		return nil, err
	}
	updated, err := s.patchAgentRunWithRetry(ctx, req.Namespace, req.Name, func(run *platformv1alpha1.AgentRun) error {
		if isTerminalAgentRunPhase(run.Status.Phase) {
			return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("cannot attach an overseer to a terminal run in phase %s", run.Status.Phase))
		}
		if run.Spec.Overseer != nil {
			return connect.NewError(connect.CodeAlreadyExists, fmt.Errorf("run already has an overseer"))
		}
		if strings.TrimSpace(run.Annotations[platformv1alpha1.OverseerDetachingAnnotation]) != "" || run.Status.OverseerSummary != nil {
			return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("the previous overseer is still detaching"))
		}
		run.Spec.Overseer = overseer.DeepCopy()
		return nil
	})
	if err != nil {
		if connect.CodeOf(err) != connect.CodeUnknown {
			return nil, err
		}
		return nil, mapK8sError("attach AgentRun overseer", err)
	}
	return s.enrichAgentRunProto(ctx, k8sAgentRunToProto(updated))
}

func (s *Server) UpdateAgentRunOverseer(ctx context.Context, req *platform.UpdateAgentRunOverseerRequest) (*platform.AgentRun, error) {
	if req.Authority == nil && req.IntervalMinutes == nil && req.MaxInterventions == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("at least one overseer field is required"))
	}
	var authority platformv1alpha1.AgentRunOverseerAuthority
	var err error
	if req.Authority != nil {
		authority, err = validateOverseerAuthority(*req.Authority)
		if err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
	}
	if req.IntervalMinutes != nil && (*req.IntervalMinutes < 1 || *req.IntervalMinutes > platformv1alpha1.AgentRunOverseerMaxIntervalMinutes) {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("overseer interval minutes must be between 1 and %d", platformv1alpha1.AgentRunOverseerMaxIntervalMinutes))
	}
	if req.MaxInterventions != nil && (*req.MaxInterventions < 0 || *req.MaxInterventions > platformv1alpha1.AgentRunOverseerMaxInterventions) {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("overseer max interventions must be between 0 and %d", platformv1alpha1.AgentRunOverseerMaxInterventions))
	}
	if err := s.requireAgentRunOwner(ctx, req.Namespace, req.Name, "update the overseer on"); err != nil {
		return nil, err
	}
	updated, err := s.patchAgentRunWithRetry(ctx, req.Namespace, req.Name, func(run *platformv1alpha1.AgentRun) error {
		if run.Status.Phase == platformv1alpha1.AgentRunPhaseCancelled {
			return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("cannot update the overseer on a cancelled run"))
		}
		if run.Spec.Overseer == nil {
			return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("run does not have an overseer"))
		}
		if req.Authority != nil {
			run.Spec.Overseer.Authority = authority
		}
		if req.IntervalMinutes != nil {
			run.Spec.Overseer.IntervalMinutes = *req.IntervalMinutes
		}
		if req.MaxInterventions != nil {
			run.Spec.Overseer.MaxInterventions = *req.MaxInterventions
		}
		return nil
	})
	if err != nil {
		if connect.CodeOf(err) != connect.CodeUnknown {
			return nil, err
		}
		return nil, mapK8sError("update AgentRun overseer", err)
	}
	return s.enrichAgentRunProto(ctx, k8sAgentRunToProto(updated))
}

func (s *Server) DetachAgentRunOverseer(ctx context.Context, req *platform.DetachAgentRunOverseerRequest) (*platform.AgentRun, error) {
	if err := s.requireAgentRunOwner(ctx, req.Namespace, req.Name, "detach the overseer from"); err != nil {
		return nil, err
	}
	updated, err := s.patchAgentRunWithRetry(ctx, req.Namespace, req.Name, func(run *platformv1alpha1.AgentRun) error {
		if run.Spec.Overseer != nil || run.Status.OverseerSummary != nil {
			if run.Annotations == nil {
				run.Annotations = map[string]string{}
			}
			run.Annotations[platformv1alpha1.OverseerDetachingAnnotation] = "true"
		}
		run.Spec.Overseer = nil
		return nil
	})
	if err != nil {
		return nil, mapK8sError("detach AgentRun overseer", err)
	}
	return s.enrichAgentRunProto(ctx, k8sAgentRunToProto(updated))
}
