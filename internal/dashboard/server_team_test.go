package dashboard

import (
	"context"
	"errors"
	"testing"

	"connectrpc.com/connect"
	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/agentrun"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
)

type dashboardTeamServiceStub struct {
	listResp        *agentrun.ListChildRunsResponse
	listReq         agentrun.ListChildRunsRequest
	logsResp        *agentrun.ChildRunLogs
	logsReq         agentrun.GetChildRunStatusRequest
	artifactResp    *agentrun.ChildRunArtifact
	artifactReq     agentrun.GetChildRunArtifactRequest
	sendMessageReq  agentrun.SendMessageToChildRequest
	sendMessageResp *agentrun.ChildRunStatus
}

func (s *dashboardTeamServiceStub) CreateChildRun(context.Context, agentrun.CreateChildRunRequest) (*agentrun.ChildRunStatus, error) {
	return nil, errors.New("unexpected call")
}

func (s *dashboardTeamServiceStub) ListChildRuns(_ context.Context, req agentrun.ListChildRunsRequest) (*agentrun.ListChildRunsResponse, error) {
	s.listReq = req
	return s.listResp, nil
}

func (s *dashboardTeamServiceStub) GetChildRunStatus(context.Context, agentrun.GetChildRunStatusRequest) (*agentrun.ChildRunStatus, error) {
	return nil, errors.New("unexpected call")
}

func (s *dashboardTeamServiceStub) GetChildRunArtifact(_ context.Context, req agentrun.GetChildRunArtifactRequest) (*agentrun.ChildRunArtifact, error) {
	s.artifactReq = req
	return s.artifactResp, nil
}

func (s *dashboardTeamServiceStub) GetChildRunLogs(_ context.Context, req agentrun.GetChildRunStatusRequest) (*agentrun.ChildRunLogs, error) {
	s.logsReq = req
	return s.logsResp, nil
}

func (s *dashboardTeamServiceStub) GetParentTeamStatus(context.Context, agentrun.GetParentTeamStatusRequest) (*platformv1alpha1.AgentRunTeamSummary, error) {
	return nil, errors.New("unexpected call")
}

func (s *dashboardTeamServiceStub) WaitForRunChange(context.Context, agentrun.WaitForRunChangeRequest) (*agentrun.WaitForRunChangeResponse, error) {
	return nil, errors.New("unexpected call")
}

func (s *dashboardTeamServiceStub) CancelChildRun(context.Context, agentrun.CancelChildRunRequest) (*agentrun.ChildRunStatus, error) {
	return nil, errors.New("unexpected call")
}

func (s *dashboardTeamServiceStub) RetryChildRun(context.Context, agentrun.RetryChildRunRequest) (*agentrun.ChildRunStatus, error) {
	return nil, errors.New("unexpected call")
}

func (s *dashboardTeamServiceStub) SendMessageToChild(_ context.Context, req agentrun.SendMessageToChildRequest) (*agentrun.ChildRunStatus, error) {
	s.sendMessageReq = req
	return s.sendMessageResp, nil
}

func (s *dashboardTeamServiceStub) GetApprovalStatus(context.Context, agentrun.GetApprovalStatusRequest) (*agentrun.ApprovalStatus, error) {
	return nil, errors.New("unexpected call")
}

func (s *dashboardTeamServiceStub) HasActiveChildren(context.Context, agentrun.ParentRunRef) (bool, error) {
	return false, errors.New("unexpected call")
}

func TestListTeamChildRunsRequiresFeatureGate(t *testing.T) {
	srv := &Server{}

	_, err := srv.ListTeamChildRuns(context.Background(), &platform.ListTeamChildRunsRequest{
		Parent: &platform.TeamParentRef{Namespace: "default", Name: "parent"},
	})
	if err == nil {
		t.Fatal("ListTeamChildRuns() error = nil, want feature-gate error")
	}
	if connect.CodeOf(err) != connect.CodeUnimplemented {
		t.Fatalf("connect.CodeOf(err) = %v, want %v", connect.CodeOf(err), connect.CodeUnimplemented)
	}
	if err.Error() != "unimplemented: "+errAgentRunTeamModeDisabled {
		t.Fatalf("error = %q", err.Error())
	}
}

func TestListTeamChildRunsDelegatesWhenFeatureGateEnabled(t *testing.T) {
	service := &dashboardTeamServiceStub{
		listResp: &agentrun.ListChildRunsResponse{
			Children: []agentrun.ChildRunStatus{{Name: "child-a", Namespace: "default", Phase: "Running"}},
		},
	}
	srv := &Server{teamModeEnabled: true, teamService: service}

	resp, err := srv.ListTeamChildRuns(context.Background(), &platform.ListTeamChildRunsRequest{
		Parent:   &platform.TeamParentRef{Namespace: "default", Name: "parent"},
		StepName: "implement",
	})
	if err != nil {
		t.Fatalf("ListTeamChildRuns() error = %v", err)
	}
	if len(resp.Children) != 1 || resp.Children[0].Name != "child-a" {
		t.Fatalf("Children = %#v", resp.Children)
	}
	if service.listReq.Parent.Name != "parent" || service.listReq.StepName != "implement" {
		t.Fatalf("ListChildRuns request = %#v", service.listReq)
	}
}

func TestGetTeamChildRunLogsDelegatesWhenFeatureGateEnabled(t *testing.T) {
	service := &dashboardTeamServiceStub{
		logsResp: &agentrun.ChildRunLogs{
			Status:                 agentrun.ChildRunStatus{Name: "child-a", Namespace: "default", Phase: "Blocked"},
			UserInputType:          "approval",
			PendingQuestion:        "Please confirm migration strategy",
			LastError:              "command failed",
			ActivityLogURL:         "https://example.com/log",
			RecentActivity:         []platformv1alpha1.AgentRunActivity{{EventType: "tool_use", Summary: "Ran grep"}},
			ConversationTail:       []platformv1alpha1.AgentRunChatMessage{{Role: "assistant", Content: "Please confirm"}},
			CanUnblockWithRetry:    true,
			SuggestedUnblockAction: "retry_child_run",
		},
	}
	srv := &Server{teamModeEnabled: true, teamService: service}

	resp, err := srv.GetTeamChildRunLogs(context.Background(), &platform.GetTeamChildRunLogsRequest{
		Parent: &platform.TeamParentRef{Namespace: "default", Name: "parent"},
		Child:  &platform.TeamChildRef{Name: "child-a"},
	})
	if err != nil {
		t.Fatalf("GetTeamChildRunLogs() error = %v", err)
	}
	if resp.Status == nil || resp.Status.Name != "child-a" {
		t.Fatalf("Status = %#v", resp.Status)
	}
	if resp.PendingQuestion != "Please confirm migration strategy" || resp.LastError != "command failed" {
		t.Fatalf("logs response = %#v", resp)
	}
	if len(resp.RecentActivity) != 1 || resp.RecentActivity[0].Summary != "Ran grep" {
		t.Fatalf("RecentActivity = %#v", resp.RecentActivity)
	}
	if len(resp.ConversationTail) != 1 || resp.ConversationTail[0].Content != "Please confirm" {
		t.Fatalf("ConversationTail = %#v", resp.ConversationTail)
	}
	if service.logsReq.Parent.Name != "parent" || service.logsReq.Child.Name != "child-a" {
		t.Fatalf("GetChildRunLogs request = %#v", service.logsReq)
	}
}

func TestGetTeamChildRunArtifactDelegatesWhenFeatureGateEnabled(t *testing.T) {
	service := &dashboardTeamServiceStub{
		artifactResp: &agentrun.ChildRunArtifact{
			Artifact: "output",
			Kind:     "ConfigMap",
			Name:     "child-output",
			Key:      "plan.md",
			Content:  "done",
		},
	}
	srv := &Server{teamModeEnabled: true, teamService: service}

	resp, err := srv.GetTeamChildRunArtifact(context.Background(), &platform.GetTeamChildRunArtifactRequest{
		Parent:   &platform.TeamParentRef{Namespace: "default", Name: "parent"},
		Child:    &platform.TeamChildRef{Name: "child-a"},
		Artifact: "output",
	})
	if err != nil {
		t.Fatalf("GetTeamChildRunArtifact() error = %v", err)
	}
	if resp.Name != "child-output" || resp.Content != "done" {
		t.Fatalf("artifact response = %#v", resp)
	}
	if service.artifactReq.Artifact != "output" || service.artifactReq.Child.Name != "child-a" {
		t.Fatalf("GetChildRunArtifact request = %#v", service.artifactReq)
	}
}

func TestSendTeamChildMessageDelegatesWhenFeatureGateEnabled(t *testing.T) {
	service := &dashboardTeamServiceStub{
		sendMessageResp: &agentrun.ChildRunStatus{Name: "child-a", Namespace: "default", Phase: "Running"},
	}
	srv := &Server{teamModeEnabled: true, teamService: service}

	resp, err := srv.SendTeamChildMessage(context.Background(), &platform.SendTeamChildMessageRequest{
		Parent:  &platform.TeamParentRef{Namespace: "default", Name: "parent"},
		Child:   &platform.TeamChildRef{Name: "child-a"},
		Message: "continue",
	})
	if err != nil {
		t.Fatalf("SendTeamChildMessage() error = %v", err)
	}
	if resp.Name != "child-a" || resp.Phase != "Running" {
		t.Fatalf("status response = %#v", resp)
	}
	if service.sendMessageReq.Message != "continue" || service.sendMessageReq.Child.Name != "child-a" {
		t.Fatalf("SendMessageToChild request = %#v", service.sendMessageReq)
	}
}
