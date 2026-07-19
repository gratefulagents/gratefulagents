package dashboard

import (
	"context"
	"fmt"
	"strings"

	"connectrpc.com/connect"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const linearProjectAPIKeySecretSuffix = "-linear-api-key"

func (s *Server) CreateLinearProject(ctx context.Context, req *platform.CreateLinearProjectRequest) (*platform.LinearProject, error) {
	actor := requestActorFromContext(ctx)
	namespace, err := s.ensureUserNamespace(ctx, actor)
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(req.GetName())
	if name == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("name is required"))
	}
	if problems := validation.IsDNS1123Subdomain(name); len(problems) != 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("name must be a valid DNS-1123 subdomain: %s", strings.Join(problems, "; ")))
	}
	secretName := name + linearProjectAPIKeySecretSuffix
	if problems := validation.IsDNS1123Subdomain(secretName); len(problems) != 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("name is too long for the managed Linear credential Secret: %s", strings.Join(problems, "; ")))
	}
	apiKey := strings.TrimSpace(req.GetLinearApiKey())
	projectID := strings.TrimSpace(req.GetProjectId())
	teamID := strings.TrimSpace(req.GetTeamId())
	if apiKey == "" || projectID == "" || teamID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("linear_api_key, project_id, and team_id are required"))
	}

	var pollInterval metav1.Duration
	if value := strings.TrimSpace(req.GetPollInterval()); value != "" {
		if err := pollInterval.UnmarshalJSON([]byte(`"` + value + `"`)); err != nil || pollInterval.Duration <= 0 {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("poll_interval must be a positive Go duration"))
		}
	}
	defaults, provider, authMode, err := protoDefaultsToCRD(req.GetDefaults())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	if strings.TrimSpace(defaults.Model) == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("defaults.model is required"))
	}
	if req.GetUseSavedCredentials() {
		secrets := triggersv1alpha1.AgentRunSecrets{}
		if err := s.applyProjectSavedCredentials(ctx, namespace, provider, authMode, &secrets); err != nil {
			return nil, err
		}
		defaults.Secrets = secrets
	} else if err := validateProviderAuthConfiguration(provider, authMode, defaults.Secrets.ClaudeApiKey, defaults.Secrets.OpenAIOAuthSecret, defaults.Secrets.ProviderKeys); err != nil { //nolint:staticcheck
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}

	key := client.ObjectKey{Namespace: namespace, Name: name}
	if err := s.k8sClient.Get(ctx, key, &triggersv1alpha1.LinearProject{}); err == nil {
		return nil, connect.NewError(connect.CodeAlreadyExists, fmt.Errorf("LinearProject %s/%s already exists", namespace, name))
	} else if !k8serrors.IsNotFound(err) {
		return nil, mapK8sError("read LinearProject", err)
	}

	policyCleanup, err := s.applyTriggerPolicies(ctx, namespace, name, req.GetPolicies(), &defaults)
	if err != nil {
		return nil, err
	}
	rollback := func() {
		for _, fn := range policyCleanup {
			fn()
		}
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: namespace, Labels: map[string]string{"app.kubernetes.io/managed-by": "gratefulagents-dashboard"}},
		Type:       corev1.SecretTypeOpaque,
		Data:       map[string][]byte{"api-key": []byte(apiKey)},
	}
	if err := s.k8sClient.Create(ctx, secret); err != nil {
		rollback()
		if k8serrors.IsAlreadyExists(err) {
			return nil, connect.NewError(connect.CodeAlreadyExists, fmt.Errorf("Secret %s/%s already exists", namespace, secretName))
		}
		return nil, mapK8sError("create Linear API key Secret", err)
	}

	approvedLabel := strings.TrimSpace(req.GetApprovedLabel())
	if approvedLabel == "" {
		approvedLabel = "ai-approved"
	}
	lp := &triggersv1alpha1.LinearProject{
		TypeMeta:   metav1.TypeMeta{APIVersion: triggersv1alpha1.GroupVersion.String(), Kind: "LinearProject"},
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: triggersv1alpha1.LinearProjectSpec{
			LinearAPIKeySecret: secretName, ProjectID: projectID, TeamID: teamID,
			PollInterval: pollInterval, ApprovedLabel: approvedLabel,
			AutoCreateTasks: req.GetAutoCreateTasks(), Defaults: defaults,
		},
	}
	cleanupCtx := context.WithoutCancel(ctx)
	rollbackResources := func(deleteProject bool) error {
		var failures []string
		if err := s.k8sClient.Delete(cleanupCtx, secret); err != nil && !k8serrors.IsNotFound(err) {
			failures = append(failures, "delete Secret: "+err.Error())
		}
		if deleteProject {
			if err := s.k8sClient.Delete(cleanupCtx, lp); err != nil && !k8serrors.IsNotFound(err) {
				failures = append(failures, "delete LinearProject: "+err.Error())
			}
		}
		rollback()
		if len(failures) != 0 {
			return fmt.Errorf("rollback failed: %s", strings.Join(failures, "; "))
		}
		return nil
	}

	if err := s.k8sClient.Create(ctx, lp); err != nil {
		if cleanupErr := rollbackResources(false); cleanupErr != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("create LinearProject: %v; %w", err, cleanupErr))
		}
		if k8serrors.IsAlreadyExists(err) {
			return nil, connect.NewError(connect.CodeAlreadyExists, fmt.Errorf("LinearProject %s/%s already exists", namespace, name))
		}
		return nil, mapK8sError("create LinearProject", err)
	}

	controller, blockOwnerDeletion := true, true
	secret.OwnerReferences = []metav1.OwnerReference{{APIVersion: triggersv1alpha1.GroupVersion.String(), Kind: "LinearProject", Name: name, UID: lp.UID, Controller: &controller, BlockOwnerDeletion: &blockOwnerDeletion}}
	if err := s.k8sClient.Update(ctx, secret); err != nil {
		if cleanupErr := rollbackResources(true); cleanupErr != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("attach ownership to Linear API key Secret: %v; %w", err, cleanupErr))
		}
		return nil, mapK8sError("attach ownership to Linear API key Secret", err)
	}

	if s.stateStore != nil && actor.Subject != "" {
		if err := s.stateStore.SetResourceOwner(ctx, linearProjectResourceType, name, namespace, actor.Subject); err != nil {
			if cleanupErr := rollbackResources(true); cleanupErr != nil {
				return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("record ownership for LinearProject %s/%s: %v; %w", namespace, name, err, cleanupErr))
			}
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("record ownership for LinearProject %s/%s: %w", namespace, name, err))
		}
	}
	pb := s.linearProjectProto(ctx, lp, nil)
	pb.Owner, pb.MyPermission = s.resourceACL(ctx, linearProjectResourceType, name, namespace)
	return pb, nil
}
