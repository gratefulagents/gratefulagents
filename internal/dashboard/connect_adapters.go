package dashboard

import (
	"context"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/gratefulagents/gratefulagents/rpc/platform"
)

// PlatformServiceConnectHandler adapts the Server to the Connect handler interface.
type PlatformServiceConnectHandler struct {
	srv *Server
}

// NewPlatformServiceConnectHandler creates a new Connect adapter for the server.
func NewPlatformServiceConnectHandler(srv *Server) *PlatformServiceConnectHandler {
	return &PlatformServiceConnectHandler{srv: srv}
}

func (h *PlatformServiceConnectHandler) ListAgentRuns(ctx context.Context, req *connect.Request[platform.ListAgentRunsRequest]) (*connect.Response[platform.ListAgentRunsResponse], error) {
	resp, err := h.srv.ListAgentRuns(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) GetAgentRun(ctx context.Context, req *connect.Request[platform.GetAgentRunRequest]) (*connect.Response[platform.AgentRun], error) {
	resp, err := h.srv.GetAgentRun(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) WatchAgentRuns(ctx context.Context, req *connect.Request[platform.WatchAgentRunsRequest], stream *connect.ServerStream[platform.AgentRunEvent]) error {
	return h.srv.WatchAgentRuns(ctx, req.Msg, stream)
}

func (h *PlatformServiceConnectHandler) WatchAgentRun(ctx context.Context, req *connect.Request[platform.WatchAgentRunRequest], stream *connect.ServerStream[platform.AgentRun]) error {
	return h.srv.WatchAgentRun(ctx, req.Msg, stream)
}

func (h *PlatformServiceConnectHandler) GetActivityLog(ctx context.Context, req *connect.Request[platform.GetActivityLogRequest]) (*connect.Response[platform.GetActivityLogResponse], error) {
	resp, err := h.srv.GetActivityLog(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) GetActivityEntryDetail(ctx context.Context, req *connect.Request[platform.GetActivityEntryDetailRequest]) (*connect.Response[platform.GetActivityEntryDetailResponse], error) {
	resp, err := h.srv.GetActivityEntryDetail(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) WatchActivityLog(ctx context.Context, req *connect.Request[platform.GetActivityLogRequest], stream *connect.ServerStream[platform.GetActivityLogResponse]) error {
	return h.srv.WatchActivityLog(ctx, req.Msg, stream)
}

func (h *PlatformServiceConnectHandler) GetAgentRunUsage(ctx context.Context, req *connect.Request[platform.GetAgentRunUsageRequest]) (*connect.Response[platform.AgentRunUsageResponse], error) {
	resp, err := h.srv.GetAgentRunUsage(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) GetObservabilityOverview(ctx context.Context, req *connect.Request[platform.GetObservabilityOverviewRequest]) (*connect.Response[platform.ObservabilityOverviewResponse], error) {
	resp, err := h.srv.GetObservabilityOverview(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) ListLinearProjects(ctx context.Context, req *connect.Request[platform.ListLinearProjectsRequest]) (*connect.Response[platform.ListLinearProjectsResponse], error) {
	resp, err := h.srv.ListLinearProjects(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) WatchLinearProjects(ctx context.Context, req *connect.Request[platform.WatchLinearProjectsRequest], stream *connect.ServerStream[platform.LinearProjectEvent]) error {
	return h.srv.WatchLinearProjects(ctx, req.Msg, stream)
}

func (h *PlatformServiceConnectHandler) SendAgentRunMessage(ctx context.Context, req *connect.Request[platform.SendAgentRunMessageRequest]) (*connect.Response[platform.SendAgentRunMessageResponse], error) {
	resp, err := h.srv.SendAgentRunMessage(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) CancelAgentRunMessage(ctx context.Context, req *connect.Request[platform.CancelAgentRunMessageRequest]) (*connect.Response[platform.CancelAgentRunMessageResponse], error) {
	resp, err := h.srv.CancelAgentRunMessage(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) CreateAgentRun(ctx context.Context, req *connect.Request[platform.CreateAgentRunRequest]) (*connect.Response[platform.AgentRun], error) {
	resp, err := h.srv.CreateAgentRun(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) AttachAgentRunOverseer(ctx context.Context, req *connect.Request[platform.AttachAgentRunOverseerRequest]) (*connect.Response[platform.AgentRun], error) {
	resp, err := h.srv.AttachAgentRunOverseer(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) UpdateAgentRunOverseer(ctx context.Context, req *connect.Request[platform.UpdateAgentRunOverseerRequest]) (*connect.Response[platform.AgentRun], error) {
	resp, err := h.srv.UpdateAgentRunOverseer(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) DetachAgentRunOverseer(ctx context.Context, req *connect.Request[platform.DetachAgentRunOverseerRequest]) (*connect.Response[platform.AgentRun], error) {
	resp, err := h.srv.DetachAgentRunOverseer(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) ListMyCredentials(ctx context.Context, req *connect.Request[platform.ListMyCredentialsRequest]) (*connect.Response[platform.MyCredentials], error) {
	resp, err := h.srv.ListMyCredentials(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) UpdateMyCredentials(ctx context.Context, req *connect.Request[platform.UpdateMyCredentialsRequest]) (*connect.Response[platform.MyCredentials], error) {
	resp, err := h.srv.UpdateMyCredentials(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) StartProviderOAuth(ctx context.Context, req *connect.Request[platform.StartProviderOAuthRequest]) (*connect.Response[platform.ProviderOAuthStart], error) {
	resp, err := h.srv.StartProviderOAuth(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) CompleteProviderOAuth(ctx context.Context, req *connect.Request[platform.CompleteProviderOAuthRequest]) (*connect.Response[platform.ProviderOAuthResult], error) {
	resp, err := h.srv.CompleteProviderOAuth(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) PollProviderOAuth(ctx context.Context, req *connect.Request[platform.PollProviderOAuthRequest]) (*connect.Response[platform.ProviderOAuthResult], error) {
	resp, err := h.srv.PollProviderOAuth(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) ShareMyCredentials(ctx context.Context, req *connect.Request[platform.ShareMyCredentialsRequest]) (*connect.Response[platform.ShareMyCredentialsResponse], error) {
	resp, err := h.srv.ShareMyCredentials(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) ListSlackAgents(ctx context.Context, req *connect.Request[platform.ListSlackAgentsRequest]) (*connect.Response[platform.ListSlackAgentsResponse], error) {
	resp, err := h.srv.ListSlackAgents(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) ListMCPServers(ctx context.Context, req *connect.Request[platform.ListMCPServersRequest]) (*connect.Response[platform.ListMCPServersResponse], error) {
	resp, err := h.srv.ListMCPServers(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) UpsertMCPServer(ctx context.Context, req *connect.Request[platform.UpsertMCPServerRequest]) (*connect.Response[platform.MCPServerInfo], error) {
	resp, err := h.srv.UpsertMCPServer(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) DeleteMCPServer(ctx context.Context, req *connect.Request[platform.DeleteMCPServerRequest]) (*connect.Response[emptypb.Empty], error) {
	if err := h.srv.DeleteMCPServer(ctx, req.Msg); err != nil {
		return nil, err
	}
	return connect.NewResponse(&emptypb.Empty{}), nil
}

func (h *PlatformServiceConnectHandler) ListSkills(ctx context.Context, req *connect.Request[platform.ListSkillsRequest]) (*connect.Response[platform.ListSkillsResponse], error) {
	resp, err := h.srv.ListSkills(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) ListSkillCatalog(ctx context.Context, req *connect.Request[platform.ListSkillCatalogRequest]) (*connect.Response[platform.ListSkillCatalogResponse], error) {
	resp, err := h.srv.ListSkillCatalog(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) InstallSkillFromCatalog(ctx context.Context, req *connect.Request[platform.InstallSkillFromCatalogRequest]) (*connect.Response[platform.SkillInfo], error) {
	resp, err := h.srv.InstallSkillFromCatalog(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) UpsertSkill(ctx context.Context, req *connect.Request[platform.UpsertSkillRequest]) (*connect.Response[platform.SkillInfo], error) {
	resp, err := h.srv.UpsertSkill(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) DeleteSkill(ctx context.Context, req *connect.Request[platform.DeleteSkillRequest]) (*connect.Response[emptypb.Empty], error) {
	if err := h.srv.DeleteSkill(ctx, req.Msg); err != nil {
		return nil, err
	}
	return connect.NewResponse(&emptypb.Empty{}), nil
}

func (h *PlatformServiceConnectHandler) ListRuntimeProfiles(ctx context.Context, req *connect.Request[platform.ListRuntimeProfilesRequest]) (*connect.Response[platform.ListRuntimeProfilesResponse], error) {
	resp, err := h.srv.ListRuntimeProfiles(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) CreateRuntimeProfile(ctx context.Context, req *connect.Request[platform.CreateRuntimeProfileRequest]) (*connect.Response[platform.RuntimeProfile], error) {
	resp, err := h.srv.CreateRuntimeProfile(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) UpdateRuntimeProfile(ctx context.Context, req *connect.Request[platform.UpdateRuntimeProfileRequest]) (*connect.Response[platform.RuntimeProfile], error) {
	resp, err := h.srv.UpdateRuntimeProfile(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) DeleteRuntimeProfile(ctx context.Context, req *connect.Request[platform.DeleteRuntimeProfileRequest]) (*connect.Response[emptypb.Empty], error) {
	if err := h.srv.DeleteRuntimeProfile(ctx, req.Msg); err != nil {
		return nil, err
	}
	return connect.NewResponse(&emptypb.Empty{}), nil
}

func (h *PlatformServiceConnectHandler) ListMCPPolicies(ctx context.Context, req *connect.Request[platform.ListMCPPoliciesRequest]) (*connect.Response[platform.ListMCPPoliciesResponse], error) {
	resp, err := h.srv.ListMCPPolicies(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) CreateMCPPolicy(ctx context.Context, req *connect.Request[platform.CreateMCPPolicyRequest]) (*connect.Response[platform.MCPPolicy], error) {
	resp, err := h.srv.CreateMCPPolicy(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) UpdateMCPPolicy(ctx context.Context, req *connect.Request[platform.UpdateMCPPolicyRequest]) (*connect.Response[platform.MCPPolicy], error) {
	resp, err := h.srv.UpdateMCPPolicy(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) DeleteMCPPolicy(ctx context.Context, req *connect.Request[platform.DeleteMCPPolicyRequest]) (*connect.Response[emptypb.Empty], error) {
	if err := h.srv.DeleteMCPPolicy(ctx, req.Msg); err != nil {
		return nil, err
	}
	return connect.NewResponse(&emptypb.Empty{}), nil
}

func (h *PlatformServiceConnectHandler) ListGuardrailPolicies(ctx context.Context, req *connect.Request[platform.ListGuardrailPoliciesRequest]) (*connect.Response[platform.ListGuardrailPoliciesResponse], error) {
	resp, err := h.srv.ListGuardrailPolicies(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) CreateGuardrailPolicy(ctx context.Context, req *connect.Request[platform.CreateGuardrailPolicyRequest]) (*connect.Response[platform.GuardrailPolicy], error) {
	resp, err := h.srv.CreateGuardrailPolicy(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) UpdateGuardrailPolicy(ctx context.Context, req *connect.Request[platform.UpdateGuardrailPolicyRequest]) (*connect.Response[platform.GuardrailPolicy], error) {
	resp, err := h.srv.UpdateGuardrailPolicy(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) DeleteGuardrailPolicy(ctx context.Context, req *connect.Request[platform.DeleteGuardrailPolicyRequest]) (*connect.Response[emptypb.Empty], error) {
	if err := h.srv.DeleteGuardrailPolicy(ctx, req.Msg); err != nil {
		return nil, err
	}
	return connect.NewResponse(&emptypb.Empty{}), nil
}

func (h *PlatformServiceConnectHandler) ListModeTemplates(ctx context.Context, req *connect.Request[platform.ListModeTemplatesRequest]) (*connect.Response[platform.ListModeTemplatesResponse], error) {
	resp, err := h.srv.ListModeTemplates(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) CreateModeTemplate(ctx context.Context, req *connect.Request[platform.CreateModeTemplateRequest]) (*connect.Response[platform.ModeTemplate], error) {
	resp, err := h.srv.CreateModeTemplate(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) UpdateModeTemplate(ctx context.Context, req *connect.Request[platform.UpdateModeTemplateRequest]) (*connect.Response[platform.ModeTemplate], error) {
	resp, err := h.srv.UpdateModeTemplate(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) DeleteModeTemplate(ctx context.Context, req *connect.Request[platform.DeleteModeTemplateRequest]) (*connect.Response[emptypb.Empty], error) {
	if err := h.srv.DeleteModeTemplate(ctx, req.Msg); err != nil {
		return nil, err
	}
	return connect.NewResponse(&emptypb.Empty{}), nil
}

func (h *PlatformServiceConnectHandler) ListRoleInstructions(ctx context.Context, req *connect.Request[platform.ListRoleInstructionsRequest]) (*connect.Response[platform.ListRoleInstructionsResponse], error) {
	resp, err := h.srv.ListRoleInstructions(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) CreateRoleInstruction(ctx context.Context, req *connect.Request[platform.CreateRoleInstructionRequest]) (*connect.Response[platform.RoleInstruction], error) {
	resp, err := h.srv.CreateRoleInstruction(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) UpdateRoleInstruction(ctx context.Context, req *connect.Request[platform.UpdateRoleInstructionRequest]) (*connect.Response[platform.RoleInstruction], error) {
	resp, err := h.srv.UpdateRoleInstruction(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) DeleteRoleInstruction(ctx context.Context, req *connect.Request[platform.DeleteRoleInstructionRequest]) (*connect.Response[emptypb.Empty], error) {
	if err := h.srv.DeleteRoleInstruction(ctx, req.Msg); err != nil {
		return nil, err
	}
	return connect.NewResponse(&emptypb.Empty{}), nil
}

func (h *PlatformServiceConnectHandler) ListRuntimeImages(ctx context.Context, req *connect.Request[platform.ListRuntimeImagesRequest]) (*connect.Response[platform.ListRuntimeImagesResponse], error) {
	resp, err := h.srv.ListRuntimeImages(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) UpdateSlackAgent(ctx context.Context, req *connect.Request[platform.UpdateSlackAgentRequest]) (*connect.Response[platform.SlackAgent], error) {
	resp, err := h.srv.UpdateSlackAgent(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) DeleteSlackAgent(ctx context.Context, req *connect.Request[platform.DeleteSlackAgentRequest]) (*connect.Response[emptypb.Empty], error) {
	resp, err := h.srv.DeleteSlackAgent(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) ListSlackWorkspaces(ctx context.Context, req *connect.Request[platform.ListSlackWorkspacesRequest]) (*connect.Response[platform.ListSlackWorkspacesResponse], error) {
	resp, err := h.srv.ListSlackWorkspaces(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) UpdateSlackWorkspace(ctx context.Context, req *connect.Request[platform.UpdateSlackWorkspaceRequest]) (*connect.Response[platform.SlackWorkspace], error) {
	resp, err := h.srv.UpdateSlackWorkspace(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) DeleteSlackWorkspace(ctx context.Context, req *connect.Request[platform.DeleteSlackWorkspaceRequest]) (*connect.Response[emptypb.Empty], error) {
	resp, err := h.srv.DeleteSlackWorkspace(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) ListSlackDrafts(ctx context.Context, req *connect.Request[platform.ListSlackDraftsRequest]) (*connect.Response[platform.ListSlackDraftsResponse], error) {
	resp, err := h.srv.ListSlackDrafts(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) GetMySoul(ctx context.Context, req *connect.Request[platform.GetMySoulRequest]) (*connect.Response[platform.Soul], error) {
	resp, err := h.srv.GetMySoul(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) UpdateMySoul(ctx context.Context, req *connect.Request[platform.UpdateMySoulRequest]) (*connect.Response[platform.Soul], error) {
	resp, err := h.srv.UpdateMySoul(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) GetMyRoleModelPreferences(ctx context.Context, req *connect.Request[platform.GetMyRoleModelPreferencesRequest]) (*connect.Response[platform.RoleModelPreferences], error) {
	resp, err := h.srv.GetMyRoleModelPreferences(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) UpdateMyRoleModelPreferences(ctx context.Context, req *connect.Request[platform.UpdateMyRoleModelPreferencesRequest]) (*connect.Response[platform.RoleModelPreferences], error) {
	resp, err := h.srv.UpdateMyRoleModelPreferences(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) GetMyGitIdentity(ctx context.Context, req *connect.Request[platform.GetMyGitIdentityRequest]) (*connect.Response[platform.GitIdentity], error) {
	resp, err := h.srv.GetMyGitIdentity(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) UpdateMyGitIdentity(ctx context.Context, req *connect.Request[platform.UpdateMyGitIdentityRequest]) (*connect.Response[platform.GitIdentity], error) {
	resp, err := h.srv.UpdateMyGitIdentity(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) DeleteAgentRun(ctx context.Context, req *connect.Request[platform.DeleteAgentRunRequest]) (*connect.Response[emptypb.Empty], error) {
	resp, err := h.srv.DeleteAgentRun(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) CancelAgentRun(ctx context.Context, req *connect.Request[platform.CancelAgentRunRequest]) (*connect.Response[platform.AgentRun], error) {
	resp, err := h.srv.CancelAgentRun(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) PromoteAgentRun(ctx context.Context, req *connect.Request[platform.PromoteAgentRunRequest]) (*connect.Response[platform.AgentRun], error) {
	resp, err := h.srv.PromoteAgentRun(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) InterruptAgentRun(ctx context.Context, req *connect.Request[platform.InterruptAgentRunRequest]) (*connect.Response[platform.InterruptAgentRunResponse], error) {
	resp, err := h.srv.InterruptAgentRun(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) RetryAgentRun(ctx context.Context, req *connect.Request[platform.RetryAgentRunRequest]) (*connect.Response[platform.AgentRun], error) {
	resp, err := h.srv.RetryAgentRun(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) RenameAgentRun(ctx context.Context, req *connect.Request[platform.RenameAgentRunRequest]) (*connect.Response[platform.AgentRun], error) {
	resp, err := h.srv.RenameAgentRun(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) UpdateAgentRunRuntimeConfig(ctx context.Context, req *connect.Request[platform.UpdateAgentRunRuntimeConfigRequest]) (*connect.Response[platform.AgentRun], error) {
	resp, err := h.srv.UpdateAgentRunRuntimeConfig(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) ExtendAgentRunRuntime(ctx context.Context, req *connect.Request[platform.ExtendAgentRunRuntimeRequest]) (*connect.Response[platform.AgentRun], error) {
	resp, err := h.srv.ExtendAgentRunRuntime(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) CreateTeamChildRun(ctx context.Context, req *connect.Request[platform.CreateTeamChildRunRequest]) (*connect.Response[platform.TeamChildRunStatus], error) {
	resp, err := h.srv.CreateTeamChildRun(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) ListTeamChildRuns(ctx context.Context, req *connect.Request[platform.ListTeamChildRunsRequest]) (*connect.Response[platform.ListTeamChildRunsResponse], error) {
	resp, err := h.srv.ListTeamChildRuns(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) GetTeamChildRunStatus(ctx context.Context, req *connect.Request[platform.GetTeamChildRunStatusRequest]) (*connect.Response[platform.TeamChildRunStatus], error) {
	resp, err := h.srv.GetTeamChildRunStatus(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) GetTeamChildRunLogs(ctx context.Context, req *connect.Request[platform.GetTeamChildRunLogsRequest]) (*connect.Response[platform.TeamChildRunLogs], error) {
	resp, err := h.srv.GetTeamChildRunLogs(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) GetTeamChildRunArtifact(ctx context.Context, req *connect.Request[platform.GetTeamChildRunArtifactRequest]) (*connect.Response[platform.TeamChildRunArtifact], error) {
	resp, err := h.srv.GetTeamChildRunArtifact(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) SendTeamChildMessage(ctx context.Context, req *connect.Request[platform.SendTeamChildMessageRequest]) (*connect.Response[platform.TeamChildRunStatus], error) {
	resp, err := h.srv.SendTeamChildMessage(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) GetAgentRunTeamStatus(ctx context.Context, req *connect.Request[platform.GetAgentRunTeamStatusRequest]) (*connect.Response[platform.AgentRunTeamSummary], error) {
	resp, err := h.srv.GetAgentRunTeamStatus(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) WaitForTeamRunChange(ctx context.Context, req *connect.Request[platform.WaitForTeamRunChangeRequest]) (*connect.Response[platform.WaitForTeamRunChangeResponse], error) {
	resp, err := h.srv.WaitForTeamRunChange(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) CancelTeamChildRun(ctx context.Context, req *connect.Request[platform.CancelTeamChildRunRequest]) (*connect.Response[platform.TeamChildRunStatus], error) {
	resp, err := h.srv.CancelTeamChildRun(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) RetryTeamChildRun(ctx context.Context, req *connect.Request[platform.RetryTeamChildRunRequest]) (*connect.Response[platform.TeamChildRunStatus], error) {
	resp, err := h.srv.RetryTeamChildRun(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) GetTeamApprovalStatus(ctx context.Context, req *connect.Request[platform.GetTeamApprovalStatusRequest]) (*connect.Response[platform.TeamApprovalStatus], error) {
	resp, err := h.srv.GetTeamApprovalStatus(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) GetLinearProject(ctx context.Context, req *connect.Request[platform.GetLinearProjectRequest]) (*connect.Response[platform.LinearProject], error) {
	resp, err := h.srv.GetLinearProject(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) ListAvailableModels(ctx context.Context, req *connect.Request[platform.ListAvailableModelsRequest]) (*connect.Response[platform.ListAvailableModelsResponse], error) {
	resp, err := h.srv.ListAvailableModels(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) GetDiff(ctx context.Context, req *connect.Request[platform.GetDiffRequest]) (*connect.Response[platform.GetDiffResponse], error) {
	resp, err := h.srv.GetDiff(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) WatchDiff(ctx context.Context, req *connect.Request[platform.GetDiffRequest], stream *connect.ServerStream[platform.GetDiffResponse]) error {
	return h.srv.WatchDiff(ctx, req.Msg, stream)
}

func (h *PlatformServiceConnectHandler) ListFiles(ctx context.Context, req *connect.Request[platform.ListFilesRequest]) (*connect.Response[platform.ListFilesResponse], error) {
	resp, err := h.srv.ListFiles(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) ListWorkspaceFiles(ctx context.Context, req *connect.Request[platform.ListWorkspaceFilesRequest]) (*connect.Response[platform.ListWorkspaceFilesResponse], error) {
	resp, err := h.srv.ListWorkspaceFiles(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) ListRepositories(ctx context.Context, req *connect.Request[platform.ListRepositoriesRequest]) (*connect.Response[platform.ListRepositoriesResponse], error) {
	resp, err := h.srv.ListRepositories(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) CloneRepository(ctx context.Context, req *connect.Request[platform.CloneRepositoryRequest]) (*connect.Response[platform.CloneRepositoryResponse], error) {
	resp, err := h.srv.CloneRepository(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) ReadFile(ctx context.Context, req *connect.Request[platform.ReadFileRequest]) (*connect.Response[platform.ReadFileResponse], error) {
	resp, err := h.srv.ReadFile(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) UpdateLinearProjectInstructions(ctx context.Context, req *connect.Request[platform.UpdateLinearProjectInstructionsRequest]) (*connect.Response[platform.UpdateLinearProjectInstructionsResponse], error) {
	resp, err := h.srv.UpdateLinearProjectInstructions(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) GetTeamRuntime(ctx context.Context, req *connect.Request[platform.GetTeamRuntimeRequest]) (*connect.Response[platform.TeamRuntime], error) {
	resp, err := h.srv.GetTeamRuntime(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) WatchTeamRuntime(ctx context.Context, req *connect.Request[platform.WatchTeamRuntimeRequest], stream *connect.ServerStream[platform.TeamRuntime]) error {
	return h.srv.WatchTeamRuntime(ctx, req.Msg, stream)
}

func (h *PlatformServiceConnectHandler) ListAvailableModes(ctx context.Context, req *connect.Request[platform.ListAvailableModesRequest]) (*connect.Response[platform.ListAvailableModesResponse], error) {
	resp, err := h.srv.ListAvailableModes(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) GetModeTemplate(ctx context.Context, req *connect.Request[platform.GetModeTemplateRequest]) (*connect.Response[platform.ModeTemplate], error) {
	resp, err := h.srv.GetModeTemplate(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) SwitchAgentRunMode(ctx context.Context, req *connect.Request[platform.SwitchAgentRunModeRequest]) (*connect.Response[platform.SwitchAgentRunModeResponse], error) {
	resp, err := h.srv.SwitchAgentRunMode(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) ListProjects(ctx context.Context, req *connect.Request[platform.ListProjectsRequest]) (*connect.Response[platform.ListProjectsResponse], error) {
	resp, err := h.srv.ListProjects(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) GetProject(ctx context.Context, req *connect.Request[platform.GetProjectRequest]) (*connect.Response[platform.Project], error) {
	resp, err := h.srv.GetProject(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) WatchProjects(ctx context.Context, req *connect.Request[platform.WatchProjectsRequest], stream *connect.ServerStream[platform.ProjectEvent]) error {
	return h.srv.WatchProjects(ctx, req.Msg, stream)
}

func (h *PlatformServiceConnectHandler) CreateProject(ctx context.Context, req *connect.Request[platform.CreateProjectRequest]) (*connect.Response[platform.Project], error) {
	resp, err := h.srv.CreateProject(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) UpdateProject(ctx context.Context, req *connect.Request[platform.UpdateProjectRequest]) (*connect.Response[platform.Project], error) {
	resp, err := h.srv.UpdateProject(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) CreateProjectTrigger(ctx context.Context, req *connect.Request[platform.CreateProjectTriggerRequest]) (*connect.Response[platform.Project], error) {
	resp, err := h.srv.CreateProjectTrigger(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) UpdateProjectTrigger(ctx context.Context, req *connect.Request[platform.UpdateProjectTriggerRequest]) (*connect.Response[platform.Project], error) {
	resp, err := h.srv.UpdateProjectTrigger(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) DeleteProjectTrigger(ctx context.Context, req *connect.Request[platform.DeleteProjectTriggerRequest]) (*connect.Response[emptypb.Empty], error) {
	resp, err := h.srv.DeleteProjectTrigger(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) SetProjectTriggerEnabled(ctx context.Context, req *connect.Request[platform.SetProjectTriggerEnabledRequest]) (*connect.Response[platform.Project], error) {
	resp, err := h.srv.SetProjectTriggerEnabled(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) DeleteProject(ctx context.Context, req *connect.Request[platform.DeleteProjectRequest]) (*connect.Response[emptypb.Empty], error) {
	resp, err := h.srv.DeleteProject(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) ListConnections(ctx context.Context, req *connect.Request[platform.ListConnectionsRequest]) (*connect.Response[platform.ListConnectionsResponse], error) {
	resp, err := h.srv.ListConnections(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) CreateConnection(ctx context.Context, req *connect.Request[platform.CreateConnectionRequest]) (*connect.Response[platform.Connection], error) {
	resp, err := h.srv.CreateConnection(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) UpdateConnection(ctx context.Context, req *connect.Request[platform.UpdateConnectionRequest]) (*connect.Response[platform.Connection], error) {
	resp, err := h.srv.UpdateConnection(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) DeleteConnection(ctx context.Context, req *connect.Request[platform.DeleteConnectionRequest]) (*connect.Response[emptypb.Empty], error) {
	resp, err := h.srv.DeleteConnection(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) ListProjectContent(ctx context.Context, req *connect.Request[platform.ListProjectContentRequest]) (*connect.Response[platform.ListProjectContentResponse], error) {
	resp, err := h.srv.ListProjectContent(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) GetProjectContent(ctx context.Context, req *connect.Request[platform.GetProjectContentRequest]) (*connect.Response[platform.GetProjectContentResponse], error) {
	resp, err := h.srv.GetProjectContent(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) CreateProjectContent(ctx context.Context, req *connect.Request[platform.CreateProjectContentRequest]) (*connect.Response[platform.ProjectContent], error) {
	resp, err := h.srv.CreateProjectContent(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) UpdateProjectContent(ctx context.Context, req *connect.Request[platform.UpdateProjectContentRequest]) (*connect.Response[platform.ProjectContent], error) {
	resp, err := h.srv.UpdateProjectContent(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) DuplicateProjectContent(ctx context.Context, req *connect.Request[platform.DuplicateProjectContentRequest]) (*connect.Response[platform.ProjectContent], error) {
	resp, err := h.srv.DuplicateProjectContent(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) ListProjectContentVersions(ctx context.Context, req *connect.Request[platform.ListProjectContentVersionsRequest]) (*connect.Response[platform.ListProjectContentVersionsResponse], error) {
	resp, err := h.srv.ListProjectContentVersions(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) RestoreProjectContentVersion(ctx context.Context, req *connect.Request[platform.RestoreProjectContentVersionRequest]) (*connect.Response[platform.ProjectContent], error) {
	resp, err := h.srv.RestoreProjectContentVersion(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) DeleteProjectContent(ctx context.Context, req *connect.Request[platform.DeleteProjectContentRequest]) (*connect.Response[emptypb.Empty], error) {
	resp, err := h.srv.DeleteProjectContent(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) ListGitHubRepositories(ctx context.Context, req *connect.Request[platform.ListGitHubRepositoriesRequest]) (*connect.Response[platform.ListGitHubRepositoriesResponse], error) {
	resp, err := h.srv.ListGitHubRepositories(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) GetGitHubRepository(ctx context.Context, req *connect.Request[platform.GetGitHubRepositoryRequest]) (*connect.Response[platform.GitHubRepository], error) {
	resp, err := h.srv.GetGitHubRepository(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) ListMaintainerWorkItems(ctx context.Context, req *connect.Request[platform.ListMaintainerWorkItemsRequest]) (*connect.Response[platform.ListMaintainerWorkItemsResponse], error) {
	resp, err := h.srv.ListMaintainerWorkItems(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) WatchGitHubRepositories(ctx context.Context, req *connect.Request[platform.WatchGitHubRepositoriesRequest], stream *connect.ServerStream[platform.GitHubRepositoryEvent]) error {
	return h.srv.WatchGitHubRepositories(ctx, req.Msg, stream)
}

func (h *PlatformServiceConnectHandler) GetGitHubAppConfig(ctx context.Context, req *connect.Request[emptypb.Empty]) (*connect.Response[platform.GitHubAppConfig], error) {
	resp, err := h.srv.GetGitHubAppConfig(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) ListGitHubAppInstallations(ctx context.Context, req *connect.Request[emptypb.Empty]) (*connect.Response[platform.ListGitHubAppInstallationsResponse], error) {
	resp, err := h.srv.ListGitHubAppInstallations(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) ListGitHubAppInstallationRepositories(ctx context.Context, req *connect.Request[platform.ListGitHubAppInstallationRepositoriesRequest]) (*connect.Response[platform.ListGitHubAppInstallationRepositoriesResponse], error) {
	resp, err := h.srv.ListGitHubAppInstallationRepositories(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) CreateGitHubRepositoryFromInstallation(ctx context.Context, req *connect.Request[platform.CreateGitHubRepositoryFromInstallationRequest]) (*connect.Response[platform.GitHubRepository], error) {
	resp, err := h.srv.CreateGitHubRepositoryFromInstallation(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) CreateGitHubRepositoryFromToken(ctx context.Context, req *connect.Request[platform.CreateGitHubRepositoryFromTokenRequest]) (*connect.Response[platform.GitHubRepository], error) {
	resp, err := h.srv.CreateGitHubRepositoryFromToken(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) ListCrons(ctx context.Context, req *connect.Request[platform.ListCronsRequest]) (*connect.Response[platform.ListCronsResponse], error) {
	resp, err := h.srv.ListCrons(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) GetCron(ctx context.Context, req *connect.Request[platform.GetCronRequest]) (*connect.Response[platform.Cron], error) {
	resp, err := h.srv.GetCron(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) CreateCron(ctx context.Context, req *connect.Request[platform.CreateCronRequest]) (*connect.Response[platform.Cron], error) {
	resp, err := h.srv.CreateCron(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) UpdateCron(ctx context.Context, req *connect.Request[platform.UpdateCronRequest]) (*connect.Response[platform.Cron], error) {
	resp, err := h.srv.UpdateCron(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) UpdateGitHubRepository(ctx context.Context, req *connect.Request[platform.UpdateGitHubRepositoryRequest]) (*connect.Response[platform.GitHubRepository], error) {
	resp, err := h.srv.UpdateGitHubRepository(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) CreateLinearProject(ctx context.Context, req *connect.Request[platform.CreateLinearProjectRequest]) (*connect.Response[platform.LinearProject], error) {
	resp, err := h.srv.CreateLinearProject(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) UpdateLinearProject(ctx context.Context, req *connect.Request[platform.UpdateLinearProjectRequest]) (*connect.Response[platform.LinearProject], error) {
	resp, err := h.srv.UpdateLinearProject(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) DeleteCron(ctx context.Context, req *connect.Request[platform.DeleteCronRequest]) (*connect.Response[emptypb.Empty], error) {
	resp, err := h.srv.DeleteCron(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) WatchCrons(ctx context.Context, req *connect.Request[platform.WatchCronsRequest], stream *connect.ServerStream[platform.CronEvent]) error {
	return h.srv.WatchCrons(ctx, req.Msg, stream)
}

func (h *PlatformServiceConnectHandler) GetAgentTrace(ctx context.Context, req *connect.Request[platform.GetAgentTraceRequest]) (*connect.Response[platform.GetAgentTraceResponse], error) {
	resp, err := h.srv.GetAgentTrace(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) WatchAgentTrace(ctx context.Context, req *connect.Request[platform.GetAgentTraceRequest], stream *connect.ServerStream[platform.GetAgentTraceResponse]) error {
	return h.srv.WatchAgentTrace(ctx, req.Msg, stream)
}

func (h *PlatformServiceConnectHandler) GetAgentRunErrors(ctx context.Context, req *connect.Request[platform.GetAgentRunErrorsRequest]) (*connect.Response[platform.GetAgentRunErrorsResponse], error) {
	resp, err := h.srv.GetAgentRunErrors(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) GetAgentRunLogs(ctx context.Context, req *connect.Request[platform.GetAgentRunLogsRequest]) (*connect.Response[platform.GetAgentRunLogsResponse], error) {
	resp, err := h.srv.GetAgentRunLogs(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) ExportAgentRunArchive(ctx context.Context, req *connect.Request[platform.ExportAgentRunArchiveRequest]) (*connect.Response[platform.ExportAgentRunArchiveResponse], error) {
	resp, err := h.srv.ExportAgentRunArchive(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

// --- Collaboration: sharing ---

func (h *PlatformServiceConnectHandler) ShareResource(ctx context.Context, req *connect.Request[platform.ShareResourceRequest]) (*connect.Response[platform.ShareResourceResponse], error) {
	resp, err := h.srv.ShareResource(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) RevokeShare(ctx context.Context, req *connect.Request[platform.RevokeShareRequest]) (*connect.Response[emptypb.Empty], error) {
	if err := h.srv.RevokeShare(ctx, req.Msg); err != nil {
		return nil, err
	}
	return connect.NewResponse(&emptypb.Empty{}), nil
}

func (h *PlatformServiceConnectHandler) UpdateSharePermission(ctx context.Context, req *connect.Request[platform.UpdateSharePermissionRequest]) (*connect.Response[platform.ResourceShareInfo], error) {
	resp, err := h.srv.UpdateSharePermission(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) ListShares(ctx context.Context, req *connect.Request[platform.ListSharesRequest]) (*connect.Response[platform.ListSharesResponse], error) {
	resp, err := h.srv.ListShares(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) ListSharedWithMe(ctx context.Context, req *connect.Request[platform.ListSharedWithMeRequest]) (*connect.Response[platform.ListSharedWithMeResponse], error) {
	resp, err := h.srv.ListSharedWithMe(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

// --- Collaboration: notifications ---

func (h *PlatformServiceConnectHandler) ListNotifications(ctx context.Context, req *connect.Request[platform.ListNotificationsRequest]) (*connect.Response[platform.ListNotificationsResponse], error) {
	resp, err := h.srv.ListNotifications(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) MarkNotificationRead(ctx context.Context, req *connect.Request[platform.MarkNotificationReadRequest]) (*connect.Response[emptypb.Empty], error) {
	if err := h.srv.MarkNotificationRead(ctx, req.Msg); err != nil {
		return nil, err
	}
	return connect.NewResponse(&emptypb.Empty{}), nil
}

// --- Collaboration: presence ---

func (h *PlatformServiceConnectHandler) SendPresenceHeartbeat(ctx context.Context, req *connect.Request[platform.PresenceHeartbeatRequest]) (*connect.Response[emptypb.Empty], error) {
	if err := h.srv.SendPresenceHeartbeat(ctx, req.Msg); err != nil {
		return nil, err
	}
	return connect.NewResponse(&emptypb.Empty{}), nil
}

func (h *PlatformServiceConnectHandler) GetPresence(ctx context.Context, req *connect.Request[platform.GetPresenceRequest]) (*connect.Response[platform.GetPresenceResponse], error) {
	resp, err := h.srv.GetPresence(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}
