package dashboard

import (
	"context"
	"fmt"

	"connectrpc.com/connect"
	"sigs.k8s.io/controller-runtime/pkg/client"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/mcppolicy"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
)

const (
	githubRepositoryResourceType = "github_repository"
	linearProjectResourceType    = "linear_project"
	projectResourceType          = "project"
)

// ListLinearProjects returns all LinearProjects, optionally filtered by namespace.
func (s *Server) ListLinearProjects(ctx context.Context, req *platform.ListLinearProjectsRequest) (*platform.ListLinearProjectsResponse, error) {
	projects := &triggersv1alpha1.LinearProjectList{}
	var opts []client.ListOption
	if req.Namespace != "" {
		opts = append(opts, client.InNamespace(req.Namespace))
	}
	if err := s.k8sClient.List(ctx, projects, opts...); err != nil {
		return nil, mapK8sError("list LinearProjects", err)
	}
	metrics, err := s.listResourceMetrics(ctx, req.Namespace)
	if err != nil {
		return nil, err
	}

	var pbProjects []*platform.LinearProject
	visible := s.resourceVisibilityFilter(ctx, linearProjectResourceType, false)
	for _, p := range projects.Items {
		if !visible(p.Namespace, p.Name) {
			continue
		}
		pb := s.linearProjectProto(ctx, &p, metrics)
		pb.Owner, pb.MyPermission = s.resourceACL(ctx, linearProjectResourceType, p.Name, p.Namespace)
		pbProjects = append(pbProjects, pb)
	}
	return &platform.ListLinearProjectsResponse{Projects: pbProjects}, nil
}

// sourceKindResourceTypes maps SourceRef kinds to their collaboration resource
// types.
var sourceKindResourceTypes = map[string]string{
	"Cron":             cronResourceType,
	"GitHubRepository": githubRepositoryResourceType,
	"LinearProject":    linearProjectResourceType,
	"Project":          projectResourceType,
}

// resolveSourceDefaults looks up the trigger resource and returns its defaults and the
// owner object for setting controller references. Access to owned triggers is
// enforced so callers cannot consume a private trigger's defaults or secrets
// (e.g. via CreateAgentRun or ListAvailableModels) without at least a share.
func (s *Server) resolveSourceDefaults(ctx context.Context, namespace string, source *platform.SourceRef) (triggersv1alpha1.AgentRunDefaults, client.Object, error) {
	if resourceType, ok := sourceKindResourceTypes[source.Kind]; ok {
		if err := s.requireResourceAccess(ctx, resourceType, source.Name, namespace, AccessViewer, "use this source"); err != nil {
			return triggersv1alpha1.AgentRunDefaults{}, nil, err
		}
	}
	key := client.ObjectKey{Namespace: namespace, Name: source.Name}

	switch source.Kind {
	case "LinearProject":
		lp := &triggersv1alpha1.LinearProject{}
		if err := s.k8sClient.Get(ctx, key, lp); err != nil {
			return triggersv1alpha1.AgentRunDefaults{}, nil, mapK8sError(fmt.Sprintf("get LinearProject %s/%s", namespace, source.Name), err)
		}
		return lp.Spec.Defaults, lp, nil
	case "Project":
		p := &triggersv1alpha1.Project{}
		if err := s.k8sClient.Get(ctx, key, p); err != nil {
			return triggersv1alpha1.AgentRunDefaults{}, nil, mapK8sError(fmt.Sprintf("get Project %s/%s", namespace, source.Name), err)
		}
		return p.Spec.Defaults, p, nil
	case "GitHubRepository":
		gh := &triggersv1alpha1.GitHubRepository{}
		if err := s.k8sClient.Get(ctx, key, gh); err != nil {
			return triggersv1alpha1.AgentRunDefaults{}, nil, mapK8sError(fmt.Sprintf("get GitHubRepository %s/%s", namespace, source.Name), err)
		}
		return gh.Spec.Defaults, gh, nil
	case "Cron":
		cr := &triggersv1alpha1.Cron{}
		if err := s.k8sClient.Get(ctx, key, cr); err != nil {
			return triggersv1alpha1.AgentRunDefaults{}, nil, mapK8sError(fmt.Sprintf("get Cron %s/%s", namespace, source.Name), err)
		}
		return cr.Spec.Defaults, cr, nil
	default:
		return triggersv1alpha1.AgentRunDefaults{}, nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("unsupported source kind %q", source.Kind))
	}
}

func (s *Server) resolveRuntimeProfile(ctx context.Context, namespace string, ref *platformv1alpha1.NamedRef) (*platformv1alpha1.RuntimeProfile, *platformv1alpha1.NamedRef, error) {
	if ref == nil || ref.Name == "" {
		return nil, nil, nil
	}

	profile := &platformv1alpha1.RuntimeProfile{}
	if err := s.k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: ref.Name}, profile); err != nil {
		return nil, nil, mapK8sError(fmt.Sprintf("get RuntimeProfile %s/%s", namespace, ref.Name), err)
	}
	return profile, ref.DeepCopy(), nil
}

func (s *Server) resolveMCPPolicy(ctx context.Context, namespace string, ref *platformv1alpha1.NamedRef) (*platformv1alpha1.MCPPolicy, *platformv1alpha1.NamedRef, error) {
	if ref == nil || ref.Name == "" {
		return nil, nil, nil
	}

	policy := &platformv1alpha1.MCPPolicy{}
	if err := s.k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: ref.Name}, policy); err != nil {
		return nil, nil, mapK8sError(fmt.Sprintf("get MCPPolicy %s/%s", namespace, ref.Name), err)
	}
	return policy, ref.DeepCopy(), nil
}

func applyRuntimeProfileDefaultsToAgentRun(run *platformv1alpha1.AgentRun, profile *platformv1alpha1.RuntimeProfile, ref *platformv1alpha1.NamedRef) {
	if run == nil || ref == nil {
		return
	}
	run.Spec.RuntimeProfileRef = &platformv1alpha1.NamedRef{Name: ref.Name}
	if profile == nil || profile.Spec.Security == nil {
		return
	}
	if profile.Spec.Security.DefaultTimeout.Duration > 0 {
		if run.Spec.Limits == nil {
			run.Spec.Limits = &platformv1alpha1.AgentRunLimits{}
		}
		if run.Spec.Limits.MaxRuntime.Duration == 0 {
			run.Spec.Limits.MaxRuntime = profile.Spec.Security.DefaultTimeout
		}
	}

	if profile.Spec.Security.PermissionMode != "" {
		if run.Status.Policy == nil {
			run.Status.Policy = &platformv1alpha1.AgentRunResolvedPolicy{}
		}
		if run.Status.Policy.ResolvedPermissionMode == "" {
			run.Status.Policy.ResolvedPermissionMode = string(profile.Spec.Security.PermissionMode)
		}
	}
}

func applyMCPPolicyDefaultsToAgentRun(run *platformv1alpha1.AgentRun, policy *platformv1alpha1.MCPPolicy, ref *platformv1alpha1.NamedRef) {
	if run == nil || ref == nil {
		return
	}
	run.Spec.MCPPolicyRef = &platformv1alpha1.NamedRef{Name: ref.Name}
	if policy == nil {
		return
	}
	if run.Status.Policy == nil {
		run.Status.Policy = &platformv1alpha1.AgentRunResolvedPolicy{}
	}
	run.Status.Policy.ResolvedMCPServers = append(run.Status.Policy.ResolvedMCPServers[:0], mcppolicy.ExplicitAllowedServers(policy)...)
}

// GetLinearProject returns a single LinearProject by namespace and name.
func (s *Server) GetLinearProject(ctx context.Context, req *platform.GetLinearProjectRequest) (*platform.LinearProject, error) {
	if err := s.requireResourceAccess(ctx, linearProjectResourceType, req.Name, req.Namespace, AccessViewer, "view this Linear project"); err != nil {
		return nil, err
	}
	lp := &triggersv1alpha1.LinearProject{}
	if err := s.k8sClient.Get(ctx, client.ObjectKey{
		Namespace: req.Namespace,
		Name:      req.Name,
	}, lp); err != nil {
		return nil, mapK8sError(fmt.Sprintf("get LinearProject %s/%s", req.Namespace, req.Name), err)
	}
	metrics, err := s.listResourceMetrics(ctx, req.Namespace)
	if err != nil {
		return nil, err
	}
	pb := s.linearProjectProto(ctx, lp, metrics)
	pb.Owner, pb.MyPermission = s.resourceACL(ctx, linearProjectResourceType, lp.Name, lp.Namespace)
	return pb, nil
}

// ListProjects returns all Projects, optionally filtered by namespace.
func (s *Server) ListProjects(ctx context.Context, req *platform.ListProjectsRequest) (*platform.ListProjectsResponse, error) {
	projects := &triggersv1alpha1.ProjectList{}
	var opts []client.ListOption
	if req.Namespace != "" {
		opts = append(opts, client.InNamespace(req.Namespace))
	}
	if err := s.k8sClient.List(ctx, projects, opts...); err != nil {
		return nil, mapK8sError("list Projects", err)
	}
	metrics, err := s.listResourceMetrics(ctx, req.Namespace)
	if err != nil {
		return nil, err
	}
	var pbProjects []*platform.Project
	visible := s.resourceVisibilityFilter(ctx, projectResourceType, false)
	for _, p := range projects.Items {
		if !visible(p.Namespace, p.Name) {
			continue
		}
		pbProjects = append(pbProjects, s.projectProto(ctx, &p, metrics))
	}

	// Apply ownership/sharing filters if stateStore is available.
	pbProjects = filterListByAccess(ctx, s, projectResourceType, req.OwnedByMe, req.SharedWithMe, pbProjects,
		func(p *platform.Project) string { return p.Namespace + "/" + p.Name })

	return &platform.ListProjectsResponse{Projects: pbProjects}, nil
}

// GetProject returns a single Project by namespace and name.
func (s *Server) GetProject(ctx context.Context, req *platform.GetProjectRequest) (*platform.Project, error) {
	if err := s.requireResourceAccess(ctx, projectResourceType, req.Name, req.Namespace, AccessViewer, "view this project"); err != nil {
		return nil, err
	}
	p := &triggersv1alpha1.Project{}
	if err := s.k8sClient.Get(ctx, client.ObjectKey{
		Namespace: req.Namespace,
		Name:      req.Name,
	}, p); err != nil {
		return nil, mapK8sError(fmt.Sprintf("get Project %s/%s", req.Namespace, req.Name), err)
	}
	metrics, err := s.listResourceMetrics(ctx, req.Namespace)
	if err != nil {
		return nil, err
	}
	return s.projectProto(ctx, p, metrics), nil
}

// ListGitHubRepositories returns all GitHubRepositories, optionally filtered by namespace.
func (s *Server) ListGitHubRepositories(ctx context.Context, req *platform.ListGitHubRepositoriesRequest) (*platform.ListGitHubRepositoriesResponse, error) {
	repos := &triggersv1alpha1.GitHubRepositoryList{}
	var opts []client.ListOption
	if req.Namespace != "" {
		opts = append(opts, client.InNamespace(req.Namespace))
	}
	if err := s.k8sClient.List(ctx, repos, opts...); err != nil {
		return nil, mapK8sError("list GitHubRepositories", err)
	}
	metrics, err := s.listResourceMetrics(ctx, req.Namespace)
	if err != nil {
		return nil, err
	}
	var pbRepos []*platform.GitHubRepository
	visible := s.resourceVisibilityFilter(ctx, githubRepositoryResourceType, false)
	for _, r := range repos.Items {
		if !visible(r.Namespace, r.Name) {
			continue
		}
		pb := s.githubRepositoryProto(ctx, &r, metrics)
		pb.ResourceOwner, pb.MyPermission = s.resourceACL(ctx, githubRepositoryResourceType, r.Name, r.Namespace)
		pbRepos = append(pbRepos, pb)
	}
	return &platform.ListGitHubRepositoriesResponse{Repositories: pbRepos}, nil
}

// GetGitHubRepository returns a single GitHubRepository by namespace and name.
func (s *Server) GetGitHubRepository(ctx context.Context, req *platform.GetGitHubRepositoryRequest) (*platform.GitHubRepository, error) {
	if err := s.requireResourceAccess(ctx, githubRepositoryResourceType, req.Name, req.Namespace, AccessViewer, "view this repository"); err != nil {
		return nil, err
	}
	gh := &triggersv1alpha1.GitHubRepository{}
	if err := s.k8sClient.Get(ctx, client.ObjectKey{
		Namespace: req.Namespace,
		Name:      req.Name,
	}, gh); err != nil {
		return nil, mapK8sError(fmt.Sprintf("get GitHubRepository %s/%s", req.Namespace, req.Name), err)
	}
	metrics, err := s.listResourceMetrics(ctx, req.Namespace)
	if err != nil {
		return nil, err
	}
	pb := s.githubRepositoryProto(ctx, gh, metrics)
	pb.ResourceOwner, pb.MyPermission = s.resourceACL(ctx, githubRepositoryResourceType, gh.Name, gh.Namespace)
	return pb, nil
}

// ListCrons returns all Cron triggers, optionally filtered by namespace.
func (s *Server) ListCrons(ctx context.Context, req *platform.ListCronsRequest) (*platform.ListCronsResponse, error) {
	crons := &triggersv1alpha1.CronList{}
	var opts []client.ListOption
	if req.Namespace != "" {
		opts = append(opts, client.InNamespace(req.Namespace))
	}
	if err := s.k8sClient.List(ctx, crons, opts...); err != nil {
		return nil, mapK8sError("list Crons", err)
	}
	metrics, err := s.listResourceMetrics(ctx, req.Namespace)
	if err != nil {
		return nil, err
	}
	var pbCrons []*platform.Cron
	visible := s.resourceVisibilityFilter(ctx, cronResourceType, false)
	for _, cr := range crons.Items {
		if !visible(cr.Namespace, cr.Name) {
			continue
		}
		pb := s.cronProto(ctx, &cr, metrics)
		pb.Owner, pb.MyPermission = s.resourceACL(ctx, cronResourceType, cr.Name, cr.Namespace)
		pbCrons = append(pbCrons, pb)
	}
	return &platform.ListCronsResponse{Crons: pbCrons}, nil
}

// GetCron returns a single Cron trigger by namespace and name.
func (s *Server) GetCron(ctx context.Context, req *platform.GetCronRequest) (*platform.Cron, error) {
	if err := s.requireResourceAccess(ctx, cronResourceType, req.Name, req.Namespace, AccessViewer, "view this cron"); err != nil {
		return nil, err
	}
	cr := &triggersv1alpha1.Cron{}
	if err := s.k8sClient.Get(ctx, client.ObjectKey{
		Namespace: req.Namespace,
		Name:      req.Name,
	}, cr); err != nil {
		return nil, mapK8sError(fmt.Sprintf("get Cron %s/%s", req.Namespace, req.Name), err)
	}
	metrics, err := s.listResourceMetrics(ctx, req.Namespace)
	if err != nil {
		return nil, err
	}
	pb := s.cronProto(ctx, cr, metrics)
	pb.Owner, pb.MyPermission = s.resourceACL(ctx, cronResourceType, cr.Name, cr.Namespace)
	return pb, nil
}

// GetTeamRuntime returns the team runtime substrate for a given parent AgentRun.
func (s *Server) GetTeamRuntime(ctx context.Context, req *platform.GetTeamRuntimeRequest) (*platform.TeamRuntime, error) {
	if err := s.requireAgentRunViewer(ctx, req.Namespace, req.ParentName); err != nil {
		return nil, err
	}
	list := &platformv1alpha1.AgentRunTeamRuntimeList{}
	if err := s.k8sClient.List(ctx, list,
		client.InNamespace(req.Namespace),
		client.MatchingLabels{"platform.gratefulagents.dev/team-parent": req.ParentName},
	); err != nil {
		return nil, mapK8sError(fmt.Sprintf("list TeamRuntime for %s/%s", req.Namespace, req.ParentName), err)
	}
	if len(list.Items) == 0 {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("no TeamRuntime for parent %s/%s", req.Namespace, req.ParentName))
	}
	return k8sTeamRuntimeToProto(&list.Items[0]), nil
}

// UpdateLinearProjectInstructions patches the custom instructions on a LinearProject.
func (s *Server) UpdateLinearProjectInstructions(ctx context.Context, req *platform.UpdateLinearProjectInstructionsRequest) (*platform.UpdateLinearProjectInstructionsResponse, error) {
	if err := s.requireResourceAccess(ctx, linearProjectResourceType, req.Name, req.Namespace, AccessCollaborator, "update instructions for this project"); err != nil {
		return nil, err
	}
	lp := &triggersv1alpha1.LinearProject{}
	key := client.ObjectKey{Namespace: req.Namespace, Name: req.Name}
	if err := s.k8sClient.Get(ctx, key, lp); err != nil {
		return nil, mapK8sError(fmt.Sprintf("get LinearProject %s/%s", req.Namespace, req.Name), err)
	}

	patch := client.MergeFrom(lp.DeepCopy())
	lp.Spec.Defaults.CustomInstructions = req.CustomInstructions
	if err := s.k8sClient.Patch(ctx, lp, patch); err != nil {
		return nil, mapK8sError("patch LinearProject instructions", err)
	}
	return &platform.UpdateLinearProjectInstructionsResponse{}, nil
}

// preserveAdminOnlyTriggerDefaults carries the admin-only AgentRunDefaults
// flags from the stored trigger onto rebuilt defaults. Dashboard trigger
// forms replace Spec.Defaults wholesale, and these flags are intentionally
// not exposed through the dashboard trigger APIs (kubectl/GitOps only) — a
// regular dashboard save must not silently clear what an operator granted.
func preserveAdminOnlyTriggerDefaults(defaults *triggersv1alpha1.AgentRunDefaults, existing triggersv1alpha1.AgentRunDefaults) {
	if defaults == nil {
		return
	}
	defaults.DisableCommandSandbox = existing.DisableCommandSandbox
	defaults.KubernetesAdmin = existing.KubernetesAdmin
}
