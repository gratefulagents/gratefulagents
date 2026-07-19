package dashboard

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/robfig/cron/v3"
	"google.golang.org/protobuf/types/known/emptypb"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
)

const cronResourceType = "cron"

// CreateCron creates a Cron trigger in the caller's personal namespace (or an
// explicitly requested namespace for admins).
func (s *Server) CreateCron(ctx context.Context, req *platform.CreateCronRequest) (*platform.Cron, error) {
	actor := requestActorFromContext(ctx)
	namespace, err := s.authorizeCronNamespace(ctx, actor, req.GetNamespace())
	if err != nil {
		return nil, err
	}

	spec, provider, authMode, err := cronSpecFromRequest(
		req.GetSchedule(), req.GetTimeZone(), req.GetSuspend(),
		req.GetConcurrencyPolicy(), req.GetPrompt(), req.GetDefaults(),
	)
	if err != nil {
		return nil, err
	}
	if req.GetUseSavedCredentials() {
		secrets := triggersv1alpha1.AgentRunSecrets{}
		if err := s.applyProjectSavedCredentials(ctx, namespace, provider, authMode, &secrets); err != nil {
			return nil, err
		}
		spec.Defaults.Secrets = secrets
	}

	name := sanitizeDNSLabel(req.GetName())
	if name == "" {
		name = generateCronName()
	}
	if len(name) > maxDNSLabelLen {
		name = strings.Trim(name[:maxDNSLabelLen], "-")
	}

	policyCleanup, err := s.applyTriggerPolicies(ctx, namespace, name, req.GetPolicies(), &spec.Defaults)
	if err != nil {
		return nil, err
	}
	rollbackPolicies := func() {
		for _, fn := range policyCleanup {
			fn()
		}
	}

	cr := &triggersv1alpha1.Cron{
		TypeMeta: metav1.TypeMeta{
			APIVersion: triggersv1alpha1.GroupVersion.String(),
			Kind:       "Cron",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: *spec,
	}
	if err := s.k8sClient.Create(ctx, cr); err != nil {
		rollbackPolicies()
		if k8serrors.IsAlreadyExists(err) {
			return nil, connect.NewError(connect.CodeAlreadyExists, fmt.Errorf("Cron %s/%s already exists", namespace, name))
		}
		return nil, mapK8sError("create Cron", err)
	}

	// Record resource ownership. This must succeed: an unowned cron is treated
	// as system-created and becomes visible to every authenticated user, so a
	// silently dropped ownership record would leak the cron's prompt and
	// configuration.
	if s.stateStore != nil && actor.Subject != "" {
		if err := s.stateStore.SetResourceOwner(ctx, cronResourceType, cr.Name, cr.Namespace, actor.Subject); err != nil {
			_ = s.k8sClient.Delete(ctx, cr)
			rollbackPolicies()
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("record ownership for Cron %s/%s: %w", cr.Namespace, cr.Name, err))
		}
	}

	return s.cronProto(ctx, cr, nil), nil
}

// UpdateCron replaces the spec of an existing Cron from the request.
func (s *Server) UpdateCron(ctx context.Context, req *platform.UpdateCronRequest) (*platform.Cron, error) {
	namespace := strings.TrimSpace(req.GetNamespace())
	name := strings.TrimSpace(req.GetName())
	if namespace == "" || name == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("namespace and name are required"))
	}
	if err := s.requireResourceAccess(ctx, cronResourceType, name, namespace, AccessCollaborator, "update this cron"); err != nil {
		return nil, err
	}

	existing := &triggersv1alpha1.Cron{}
	if err := s.k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, existing); err != nil {
		return nil, mapK8sError(fmt.Sprintf("get Cron %s/%s", namespace, name), err)
	}

	spec, provider, authMode, err := cronSpecFromRequest(
		req.GetSchedule(), req.GetTimeZone(), req.GetSuspend(),
		req.GetConcurrencyPolicy(), req.GetPrompt(), req.GetDefaults(),
	)
	if err != nil {
		return nil, err
	}
	if req.GetUseSavedCredentials() {
		secrets := triggersv1alpha1.AgentRunSecrets{}
		if err := s.applyProjectSavedCredentials(ctx, namespace, provider, authMode, &secrets); err != nil {
			return nil, err
		}
		spec.Defaults.Secrets = secrets
	}

	policyCleanup, err := s.applyTriggerPolicies(ctx, namespace, name, req.GetPolicies(), &spec.Defaults)
	if err != nil {
		return nil, err
	}

	preserveAdminOnlyTriggerDefaults(&spec.Defaults, existing.Spec.Defaults)
	existing.Spec = *spec
	if err := s.k8sClient.Update(ctx, existing); err != nil {
		for _, fn := range policyCleanup {
			fn()
		}
		return nil, mapK8sError("update Cron", err)
	}
	return s.cronProto(ctx, existing, nil), nil
}

// DeleteCron deletes a Cron trigger.
func (s *Server) DeleteCron(ctx context.Context, req *platform.DeleteCronRequest) (*emptypb.Empty, error) {
	namespace := strings.TrimSpace(req.GetNamespace())
	name := strings.TrimSpace(req.GetName())
	if namespace == "" || name == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("namespace and name are required"))
	}
	if err := s.requireResourceAccess(ctx, cronResourceType, name, namespace, AccessCollaborator, "delete this cron"); err != nil {
		return nil, err
	}

	cr := &triggersv1alpha1.Cron{}
	if err := s.k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, cr); err != nil {
		return nil, mapK8sError(fmt.Sprintf("get Cron %s/%s", namespace, name), err)
	}
	if err := s.k8sClient.Delete(ctx, cr); err != nil && !k8serrors.IsNotFound(err) {
		return nil, mapK8sError("delete Cron", err)
	}
	return &emptypb.Empty{}, nil
}

// authorizeCronNamespace resolves the namespace a cron write targets: the
// caller's personal namespace by default; another namespace only for admins.
func (s *Server) authorizeCronNamespace(ctx context.Context, actor requestActor, requested string) (string, error) {
	namespace, err := s.ensureUserNamespace(ctx, actor)
	if err != nil {
		return "", err
	}
	if reqNS := strings.TrimSpace(requested); reqNS != "" && reqNS != namespace {
		if actor.Role != "admin" && actor.Role != "owner" {
			return "", connect.NewError(connect.CodePermissionDenied, fmt.Errorf("you do not have permission to manage crons in namespace %q", reqNS))
		}
		namespace = reqNS
	}
	return namespace, nil
}

// generateCronName derives a DNS-1123 name for an unnamed cron.
func generateCronName() string {
	suffix := strconv.FormatInt(time.Now().UnixNano(), 36)
	if len(suffix) > 6 {
		suffix = suffix[len(suffix)-6:]
	}
	return "cron-" + suffix
}

// cronSpecFromRequest validates the shared create/update fields and builds the
// CronSpec, also returning the resolved provider and auth mode so callers can
// wire saved credentials.
func cronSpecFromRequest(
	schedule, timeZone string,
	suspend bool,
	concurrencyPolicy, prompt string,
	pbDefaults *platform.AgentRunDefaults,
) (*triggersv1alpha1.CronSpec, string, platformv1alpha1.AgentRunAuthMode, error) {
	if err := validateCronSchedule(schedule, timeZone); err != nil {
		return nil, "", "", connect.NewError(connect.CodeInvalidArgument, err)
	}
	policy, err := cronConcurrencyPolicy(concurrencyPolicy)
	if err != nil {
		return nil, "", "", connect.NewError(connect.CodeInvalidArgument, err)
	}
	if strings.TrimSpace(prompt) == "" {
		return nil, "", "", connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("prompt is required"))
	}
	defaults, provider, authMode, err := protoDefaultsToCRD(pbDefaults)
	if err != nil {
		return nil, "", "", connect.NewError(connect.CodeInvalidArgument, err)
	}
	return &triggersv1alpha1.CronSpec{
		Schedule:          strings.TrimSpace(schedule),
		TimeZone:          strings.TrimSpace(timeZone),
		Suspend:           suspend,
		ConcurrencyPolicy: policy,
		Prompt:            prompt,
		Defaults:          defaults,
	}, provider, authMode, nil
}

// validateCronSchedule mirrors the Cron controller's parser (5-field
// expressions plus @hourly-style descriptors) so the dashboard rejects
// schedules the controller would refuse.
func validateCronSchedule(schedule, timeZone string) error {
	trimmed := strings.TrimSpace(schedule)
	if trimmed == "" {
		return fmt.Errorf("schedule is required")
	}
	upper := strings.ToUpper(trimmed)
	if strings.HasPrefix(upper, "TZ=") || strings.HasPrefix(upper, "CRON_TZ=") {
		return fmt.Errorf("inline cron time zones are not supported; use time_zone")
	}
	zone := strings.TrimSpace(timeZone)
	if zone == "" {
		zone = "UTC"
	}
	if _, err := time.LoadLocation(zone); err != nil {
		return fmt.Errorf("invalid time_zone %q: %w", zone, err)
	}
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)
	if _, err := parser.Parse("CRON_TZ=" + zone + " " + trimmed); err != nil {
		return fmt.Errorf("invalid schedule %q: %w", trimmed, err)
	}
	return nil
}

func cronConcurrencyPolicy(value string) (triggersv1alpha1.CronConcurrencyPolicy, error) {
	switch strings.TrimSpace(value) {
	case "":
		return "", nil
	case string(triggersv1alpha1.CronConcurrencyAllow):
		return triggersv1alpha1.CronConcurrencyAllow, nil
	case string(triggersv1alpha1.CronConcurrencyForbid):
		return triggersv1alpha1.CronConcurrencyForbid, nil
	default:
		return "", fmt.Errorf("invalid concurrency_policy %q (want Allow or Forbid)", value)
	}
}

// protoDefaultsToCRD validates and maps proto trigger defaults onto the shared
// CRD struct, returning the resolved provider and auth mode.
func protoDefaultsToCRD(pb *platform.AgentRunDefaults) (triggersv1alpha1.AgentRunDefaults, string, platformv1alpha1.AgentRunAuthMode, error) {
	if pb == nil {
		pb = &platform.AgentRunDefaults{}
	}
	provider, err := resolveProvider(pb.GetProvider(), "")
	if err != nil {
		return triggersv1alpha1.AgentRunDefaults{}, "", "", err
	}
	authMode := triggersv1alpha1.NormalizeAuthMode(pb.GetAuthMode())
	if err := triggersv1alpha1.ValidateProviderAuthMode(provider, authMode); err != nil {
		return triggersv1alpha1.AgentRunDefaults{}, "", "", err
	}
	reasoningLevel, err := resolveReasoningLevel(pb.GetReasoningLevel())
	if err != nil {
		return triggersv1alpha1.AgentRunDefaults{}, "", "", err
	}
	if reasoningLevel == "" {
		reasoningLevel = platformv1alpha1.ReasoningMax
	}
	model := strings.TrimSpace(pb.GetModel())
	if model == "" {
		model = triggersv1alpha1.DefaultMainModelForProvider(provider)
	}
	repoURL := strings.TrimSpace(pb.GetRepoUrl())
	additionalRepos, err := normalizeAdditionalRepoURLs(pb.GetAdditionalRepoUrls(), repoURL)
	if err != nil {
		return triggersv1alpha1.AgentRunDefaults{}, "", "", err
	}

	var timeout metav1.Duration
	if value := strings.TrimSpace(pb.GetTimeout()); value != "" {
		d, err := time.ParseDuration(value)
		if err != nil {
			return triggersv1alpha1.AgentRunDefaults{}, "", "", fmt.Errorf("invalid timeout %q: %w", value, err)
		}
		timeout = metav1.Duration{Duration: d}
	}

	var workflowMode platformv1alpha1.AgentRunWorkflowMode
	switch strings.ToLower(strings.TrimSpace(pb.GetWorkflowMode())) {
	case "", string(platformv1alpha1.WorkflowModeAuto), string(platformv1alpha1.WorkflowModeChat):
		workflowMode = platformv1alpha1.WorkflowModeAuto
	default:
		return triggersv1alpha1.AgentRunDefaults{}, "", "", fmt.Errorf("invalid workflow_mode %q (want auto)", pb.GetWorkflowMode())
	}

	var executionMode platformv1alpha1.AgentRunExecutionMode
	switch strings.ToLower(strings.TrimSpace(pb.GetExecutionMode())) {
	case "":
	case string(platformv1alpha1.ExecutionModeLinear):
		executionMode = platformv1alpha1.ExecutionModeLinear
	case string(platformv1alpha1.ExecutionModeTeam):
		executionMode = platformv1alpha1.ExecutionModeTeam
	default:
		return triggersv1alpha1.AgentRunDefaults{}, "", "", fmt.Errorf("invalid execution_mode %q (want linear or team)", pb.GetExecutionMode())
	}

	d := triggersv1alpha1.AgentRunDefaults{
		RepoURL:            repoURL,
		AdditionalRepos:    additionalRepos,
		BaseBranch:         strings.TrimSpace(pb.GetBaseBranch()),
		Image:              strings.TrimSpace(pb.GetImage()),
		Model:              model,
		AllowedModels:      append([]string(nil), pb.GetAllowedModels()...),
		Provider:           provider,
		AuthMode:           authMode,
		ReasoningLevel:     reasoningLevel,
		OpenAIBaseURL:      strings.TrimSpace(pb.GetOpenaiBaseUrl()),
		OpenAIAPI:          strings.TrimSpace(pb.GetOpenaiApi()),
		Timeout:            timeout,
		CustomInstructions: pb.GetCustomInstructions(),
		ExecutionMode:      executionMode,
		WorkflowMode:       workflowMode,
		MCPServerRefs:      namedRefsFromNames(pb.GetMcpServerRefs()),
		SkillRefs:          namedRefsFromNames(pb.GetSkillRefs()),
		Secrets: triggersv1alpha1.AgentRunSecrets{
			ClaudeApiKey:      strings.TrimSpace(pb.GetClaudeApiKeySecret()),
			OpenAIOAuthSecret: strings.TrimSpace(pb.GetOpenaiOauthSecret()),
			GithubToken:       strings.TrimSpace(pb.GetGithubTokenSecret()),
			ProviderKeys:      providerKeysFromProto(pb.GetProviderKeys()),
		},
	}
	if name := strings.TrimSpace(pb.GetRuntimeProfileRef()); name != "" {
		d.RuntimeProfileRef = &platformv1alpha1.NamedRef{Name: name}
	}
	if name := strings.TrimSpace(pb.GetMcpPolicyRef()); name != "" {
		d.MCPPolicyRef = &platformv1alpha1.NamedRef{Name: name}
	}
	if name := strings.TrimSpace(pb.GetModeRef()); name != "" {
		d.ModeRef = &platformv1alpha1.ModeRef{Name: name}
	}
	return d, provider, authMode, nil
}
