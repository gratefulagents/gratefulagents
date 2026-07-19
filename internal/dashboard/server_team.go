package dashboard

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"connectrpc.com/connect"
	apierrors "k8s.io/apimachinery/pkg/api/errors"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/agentrun"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
)

const errAgentRunTeamModeDisabled = "agentrun team mode is disabled; restart the dashboard with --enable-agentrun-team-mode to expose experimental team orchestration endpoints"

func (s *Server) teamOps() agentrun.TeamService {
	if s.teamService == nil && s.k8sClient != nil && s.scheme != nil {
		ts := agentrun.NewKubeTeamService(s.k8sClient, s.scheme)
		if s.stateStore != nil {
			ts.WithStateStore(s.stateStore)
		}
		s.teamService = ts
	}
	return s.teamService
}

func (s *Server) requireTeamModeEnabled() error {
	if s.teamModeEnabled {
		return nil
	}
	return connect.NewError(connect.CodeUnimplemented, errors.New(errAgentRunTeamModeDisabled))
}

// requireTeamParentAccess enforces access to the parent AgentRun before any
// team child-run operation. K8s parent/child binding alone does not establish
// that the caller may act on this team.
func (s *Server) requireTeamParentAccess(ctx context.Context, parent *platform.TeamParentRef, level, action string) error {
	ref := parentRefFromProto(parent)
	if ref.Namespace == "" || ref.Name == "" {
		return connect.NewError(connect.CodeInvalidArgument, errors.New("parent run reference is required"))
	}
	min := AccessViewer
	if level == "collaborator" {
		min = AccessCollaborator
	}
	return s.requireAgentRunAccess(ctx, ref.Namespace, ref.Name, min, action+" this team run")
}

func (s *Server) CreateTeamChildRun(ctx context.Context, req *platform.CreateTeamChildRunRequest) (*platform.TeamChildRunStatus, error) {
	if err := s.requireTeamModeEnabled(); err != nil {
		return nil, err
	}
	if err := s.requireTeamParentAccess(ctx, req.GetParent(), "collaborator", "create child runs for"); err != nil {
		return nil, err
	}
	resp, err := s.teamOps().CreateChildRun(ctx, agentrun.CreateChildRunRequest{
		Parent:   parentRefFromProto(req.GetParent()),
		StepName: strings.TrimSpace(req.GetStepName()),
		TaskName: strings.TrimSpace(req.GetTaskName()),
	})
	if err != nil {
		return nil, mapTeamServiceError(err)
	}
	return teamChildStatusToProto(resp), nil
}

func (s *Server) ListTeamChildRuns(ctx context.Context, req *platform.ListTeamChildRunsRequest) (*platform.ListTeamChildRunsResponse, error) {
	if err := s.requireTeamModeEnabled(); err != nil {
		return nil, err
	}
	if err := s.requireTeamParentAccess(ctx, req.GetParent(), "viewer", "view child runs of"); err != nil {
		return nil, err
	}
	resp, err := s.teamOps().ListChildRuns(ctx, agentrun.ListChildRunsRequest{
		Parent:   parentRefFromProto(req.GetParent()),
		StepName: strings.TrimSpace(req.GetStepName()),
	})
	if err != nil {
		return nil, mapTeamServiceError(err)
	}
	out := &platform.ListTeamChildRunsResponse{}
	for _, child := range resp.Children {
		out.Children = append(out.Children, teamChildStatusToProto(&child))
	}
	return out, nil
}

func (s *Server) GetTeamChildRunStatus(ctx context.Context, req *platform.GetTeamChildRunStatusRequest) (*platform.TeamChildRunStatus, error) {
	if err := s.requireTeamModeEnabled(); err != nil {
		return nil, err
	}
	if err := s.requireTeamParentAccess(ctx, req.GetParent(), "viewer", "view child runs of"); err != nil {
		return nil, err
	}
	resp, err := s.teamOps().GetChildRunStatus(ctx, agentrun.GetChildRunStatusRequest{
		Parent: parentRefFromProto(req.GetParent()),
		Child:  childRefFromProto(req.GetChild()),
	})
	if err != nil {
		return nil, mapTeamServiceError(err)
	}
	return teamChildStatusToProto(resp), nil
}

func (s *Server) GetTeamChildRunLogs(ctx context.Context, req *platform.GetTeamChildRunLogsRequest) (*platform.TeamChildRunLogs, error) {
	if err := s.requireTeamModeEnabled(); err != nil {
		return nil, err
	}
	if err := s.requireTeamParentAccess(ctx, req.GetParent(), "viewer", "view child run logs of"); err != nil {
		return nil, err
	}
	resp, err := s.teamOps().GetChildRunLogs(ctx, agentrun.GetChildRunStatusRequest{
		Parent: parentRefFromProto(req.GetParent()),
		Child:  childRefFromProto(req.GetChild()),
	})
	if err != nil {
		return nil, mapTeamServiceError(err)
	}
	return teamChildLogsToProto(resp), nil
}

func (s *Server) GetTeamChildRunArtifact(ctx context.Context, req *platform.GetTeamChildRunArtifactRequest) (*platform.TeamChildRunArtifact, error) {
	if err := s.requireTeamModeEnabled(); err != nil {
		return nil, err
	}
	if err := s.requireTeamParentAccess(ctx, req.GetParent(), "viewer", "view child run artifacts of"); err != nil {
		return nil, err
	}
	resp, err := s.teamOps().GetChildRunArtifact(ctx, agentrun.GetChildRunArtifactRequest{
		Parent:   parentRefFromProto(req.GetParent()),
		Child:    childRefFromProto(req.GetChild()),
		Artifact: strings.TrimSpace(req.GetArtifact()),
	})
	if err != nil {
		return nil, mapTeamServiceError(err)
	}
	return teamChildArtifactToProto(resp), nil
}

func (s *Server) SendTeamChildMessage(ctx context.Context, req *platform.SendTeamChildMessageRequest) (*platform.TeamChildRunStatus, error) {
	if err := s.requireTeamModeEnabled(); err != nil {
		return nil, err
	}
	if err := s.requireTeamParentAccess(ctx, req.GetParent(), "collaborator", "message child runs of"); err != nil {
		return nil, err
	}
	resp, err := s.teamOps().SendMessageToChild(ctx, agentrun.SendMessageToChildRequest{
		Parent:  parentRefFromProto(req.GetParent()),
		Child:   childRefFromProto(req.GetChild()),
		Message: strings.TrimSpace(req.GetMessage()),
	})
	if err != nil {
		return nil, mapTeamServiceError(err)
	}
	return teamChildStatusToProto(resp), nil
}

func (s *Server) GetAgentRunTeamStatus(ctx context.Context, req *platform.GetAgentRunTeamStatusRequest) (*platform.AgentRunTeamSummary, error) {
	if err := s.requireTeamModeEnabled(); err != nil {
		return nil, err
	}
	if err := s.requireTeamParentAccess(ctx, req.GetParent(), "viewer", "view team status of"); err != nil {
		return nil, err
	}
	resp, err := s.teamOps().GetParentTeamStatus(ctx, agentrun.GetParentTeamStatusRequest{Parent: parentRefFromProto(req.GetParent())})
	if err != nil {
		return nil, mapTeamServiceError(err)
	}
	return teamSummaryToProto(resp), nil
}

func (s *Server) WaitForTeamRunChange(ctx context.Context, req *platform.WaitForTeamRunChangeRequest) (*platform.WaitForTeamRunChangeResponse, error) {
	if err := s.requireTeamModeEnabled(); err != nil {
		return nil, err
	}
	if err := s.requireTeamParentAccess(ctx, req.GetParent(), "viewer", "watch team status of"); err != nil {
		return nil, err
	}
	resp, err := s.teamOps().WaitForRunChange(ctx, agentrun.WaitForRunChangeRequest{
		Parent:      parentRefFromProto(req.GetParent()),
		Scope:       strings.TrimSpace(req.GetScope()),
		TimeoutMS:   req.GetTimeoutMs(),
		UntilPhases: append([]string(nil), req.GetUntilPhases()...),
	})
	if err != nil {
		return nil, mapTeamServiceError(err)
	}
	return &platform.WaitForTeamRunChangeResponse{Changed: resp.Changed, RunName: resp.RunName, Phase: resp.Phase}, nil
}

func (s *Server) CancelTeamChildRun(ctx context.Context, req *platform.CancelTeamChildRunRequest) (*platform.TeamChildRunStatus, error) {
	if err := s.requireTeamModeEnabled(); err != nil {
		return nil, err
	}
	if err := s.requireTeamParentAccess(ctx, req.GetParent(), "collaborator", "cancel child runs of"); err != nil {
		return nil, err
	}
	resp, err := s.teamOps().CancelChildRun(ctx, agentrun.CancelChildRunRequest{
		Parent: parentRefFromProto(req.GetParent()),
		Child:  childRefFromProto(req.GetChild()),
	})
	if err != nil {
		return nil, mapTeamServiceError(err)
	}
	return teamChildStatusToProto(resp), nil
}

func (s *Server) RetryTeamChildRun(ctx context.Context, req *platform.RetryTeamChildRunRequest) (*platform.TeamChildRunStatus, error) {
	if err := s.requireTeamModeEnabled(); err != nil {
		return nil, err
	}
	if err := s.requireTeamParentAccess(ctx, req.GetParent(), "collaborator", "retry child runs of"); err != nil {
		return nil, err
	}
	resp, err := s.teamOps().RetryChildRun(ctx, agentrun.RetryChildRunRequest{
		Parent: parentRefFromProto(req.GetParent()),
		Child:  childRefFromProto(req.GetChild()),
	})
	if err != nil {
		return nil, mapTeamServiceError(err)
	}
	return teamChildStatusToProto(resp), nil
}

func (s *Server) GetTeamApprovalStatus(ctx context.Context, req *platform.GetTeamApprovalStatusRequest) (*platform.TeamApprovalStatus, error) {
	if err := s.requireTeamModeEnabled(); err != nil {
		return nil, err
	}
	if err := s.requireTeamParentAccess(ctx, req.GetParent(), "viewer", "view team approval status of"); err != nil {
		return nil, err
	}
	resp, err := s.teamOps().GetApprovalStatus(ctx, agentrun.GetApprovalStatusRequest{Parent: parentRefFromProto(req.GetParent())})
	if err != nil {
		return nil, mapTeamServiceError(err)
	}
	return &platform.TeamApprovalStatus{State: resp.State}, nil
}

func parentRefFromProto(ref *platform.TeamParentRef) agentrun.ParentRunRef {
	if ref == nil {
		return agentrun.ParentRunRef{}
	}
	return agentrun.ParentRunRef{Namespace: strings.TrimSpace(ref.GetNamespace()), Name: strings.TrimSpace(ref.GetName())}
}

func childRefFromProto(ref *platform.TeamChildRef) agentrun.ChildRunRef {
	if ref == nil {
		return agentrun.ChildRunRef{}
	}
	return agentrun.ChildRunRef{Namespace: strings.TrimSpace(ref.GetNamespace()), Name: strings.TrimSpace(ref.GetName())}
}

func teamChildStatusToProto(status *agentrun.ChildRunStatus) *platform.TeamChildRunStatus {
	if status == nil {
		return nil
	}
	return &platform.TeamChildRunStatus{
		Name:          status.Name,
		Namespace:     status.Namespace,
		Step:          status.Step,
		Role:          status.Role,
		Phase:         status.Phase,
		BlockedReason: status.BlockReason,
		CurrentTask:   status.CurrentTask,
		CurrentWorker: status.CurrentWorker,
	}
}

func teamChildLogsToProto(logs *agentrun.ChildRunLogs) *platform.TeamChildRunLogs {
	if logs == nil {
		return nil
	}
	pendingQuestion := strings.TrimSpace(logs.PendingQuestion)
	if pendingQuestion == "" {
		pendingQuestion = logs.UserInputType
	}
	out := &platform.TeamChildRunLogs{
		Status:                 teamChildStatusToProto(&logs.Status),
		PendingQuestion:        pendingQuestion,
		LastError:              logs.LastError,
		ActivityLogUrl:         logs.ActivityLogURL,
		CanUnblockWithRetry:    logs.CanUnblockWithRetry,
		SuggestedUnblockAction: logs.SuggestedUnblockAction,
	}
	for _, item := range logs.RecentActivity {
		out.RecentActivity = append(out.RecentActivity, &platform.AgentActivity{
			TimestampUnix: item.Timestamp.Unix(),
			EventType:     item.EventType,
			Summary:       item.Summary,
		})
	}
	for _, msg := range logs.ConversationTail {
		out.ConversationTail = append(out.ConversationTail, &platform.ChatMessage{
			Role:          msg.Role,
			Content:       msg.Content,
			TimestampUnix: msg.Timestamp.Unix(),
		})
	}
	return out
}

func teamChildArtifactToProto(artifact *agentrun.ChildRunArtifact) *platform.TeamChildRunArtifact {
	if artifact == nil {
		return nil
	}
	return &platform.TeamChildRunArtifact{
		Artifact: artifact.Artifact,
		Kind:     artifact.Kind,
		Name:     artifact.Name,
		Key:      artifact.Key,
		Url:      artifact.URL,
		Content:  artifact.Content,
	}
}

func teamSummaryToProto(summary *platformv1alpha1.AgentRunTeamSummary) *platform.AgentRunTeamSummary {
	if summary == nil {
		return nil
	}
	return &platform.AgentRunTeamSummary{
		CurrentStepIndex:  summary.CurrentStepIndex,
		CurrentStep:       summary.CurrentStep,
		ApprovalState:     summary.ApprovalState,
		TotalChildren:     summary.TotalChildren,
		PendingChildren:   summary.PendingChildren,
		RunningChildren:   summary.RunningChildren,
		SucceededChildren: summary.SucceededChildren,
		FailedChildren:    summary.FailedChildren,
		CancelledChildren: summary.CancelledChildren,
		BlockedReason:     summary.BlockedReason,
	}
}

func mapTeamServiceError(err error) error {
	switch {
	case err == nil:
		return nil
	case apierrors.IsNotFound(err):
		return mapK8sError("team AgentRun operation", err)
	case apierrors.IsAlreadyExists(err):
		return connect.NewError(connect.CodeAlreadyExists, err)
	case errors.Is(err, agentrun.ErrExecutionModeNotTeam),
		errors.Is(err, agentrun.ErrTeamSpecRequired),
		errors.Is(err, agentrun.ErrTeamStepNotFound),
		errors.Is(err, agentrun.ErrTeamTaskNotFound),
		errors.Is(err, agentrun.ErrChildArtifactNotFound),
		errors.Is(err, agentrun.ErrTeamScopeInvalid),
		errors.Is(err, agentrun.ErrWorkflowModeUnsupported),
		errors.Is(err, agentrun.ErrParentOnlyDelegation),
		errors.Is(err, agentrun.ErrParentScopeMismatch),
		errors.Is(err, agentrun.ErrRuntimeParentBinding),
		errors.Is(err, agentrun.ErrChildCrossNamespace),
		errors.Is(err, agentrun.ErrChildMessageRequired):
		return connect.NewError(connect.CodeInvalidArgument, err)
	case errors.Is(err, agentrun.ErrChildRunNotOwned):
		return connect.NewError(connect.CodePermissionDenied, err)
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return connect.NewError(connect.CodeDeadlineExceeded, err)
	default:
		return connect.NewError(connect.CodeInternal, fmt.Errorf("team service: %w", err))
	}
}
