package triggers

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/auth"
	"github.com/gratefulagents/gratefulagents/internal/store"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	GeneratedRuntimeMarkerMetadataKey = "triggers.gratefulagents.dev/generated-runtime"
	ProjectNameMetadataKey            = "triggers.gratefulagents.dev/project-name"
	ProjectUIDMetadataKey             = "triggers.gratefulagents.dev/project-uid"
	ProjectTriggerNameMetadataKey     = "triggers.gratefulagents.dev/project-trigger-name"
	ProjectTriggerTypeMetadataKey     = "triggers.gratefulagents.dev/project-trigger-type"
	// RuntimeTriggerNameAnnotation preserves the generated adapter identity used
	// for deduplication and concurrency while Trigger.Name exposes Project-local provenance.
	RuntimeTriggerNameAnnotation = "triggers.gratefulagents.dev/runtime-trigger-name"
)

type TriggerRunSpec struct {
	RunName            string
	Namespace          string
	TriggerKind        string
	TriggerName        string
	ExternalID         string
	ExternalIdentifier string
	ExternalURL        string
	SeedMessage        string
	Defaults           triggersv1alpha1.AgentRunDefaults
	OwnerRef           client.Object
	Scheme             *runtime.Scheme
	Labels             map[string]string
	Annotations        map[string]string
	Context            *platformv1alpha1.AgentRunContext
	ModeRef            *platformv1alpha1.ModeRef
	GitHubTokenSecret  string
	// OwnerID, when set, is recorded in the collaboration store as the run's
	// owner so that user can manage the run (stop, delete, share) from the
	// dashboard. Trigger-created runs without an owner are only manageable by
	// admins.
	OwnerID string
	// SlackTokensSecret optionally grants the run pod read-only Slack
	// credentials (bot/user tokens) for agent-side Slack read tools.
	SlackTokensSecret string

	SeedLogPrefix                  string
	SeedOnAlreadyExists            bool
	FetchExistingOnAlreadyExists   bool
	DetailedCredentialErrorContext bool
	AfterCreate                    func(context.Context, *platformv1alpha1.AgentRun, bool) error
}

func BuildTriggerRun(spec TriggerRunSpec) *platformv1alpha1.AgentRun {
	d := spec.Defaults
	provider := triggersv1alpha1.NormalizeProvider(d.Provider)
	authMode := triggersv1alpha1.NormalizeAuthMode(string(d.AuthMode))
	model := strings.TrimSpace(d.Model)
	if model == "" {
		model = triggersv1alpha1.DefaultMainModelForProvider(provider)
	}
	reasoningLevel := d.ReasoningLevel
	if reasoningLevel == "" {
		reasoningLevel = platformv1alpha1.ReasoningMax
	}
	gitHubTokenSecret := spec.GitHubTokenSecret
	if gitHubTokenSecret == "" {
		gitHubTokenSecret = d.Secrets.GithubToken
	}
	triggerName := spec.TriggerName
	triggerType := ""
	runContext := spec.Context
	if projectName, generated := generatedProjectRuntime(spec.OwnerRef); generated {
		triggerName = metadataValue(spec.OwnerRef, ProjectTriggerNameMetadataKey)
		triggerType = strings.ToLower(metadataValue(spec.OwnerRef, ProjectTriggerTypeMetadataKey))
		runContext = &platformv1alpha1.AgentRunContext{ProjectRef: &platformv1alpha1.ProjectRef{Kind: "Project", Name: projectName}}
	}
	run := &platformv1alpha1.AgentRun{}
	run.Name = spec.RunName
	run.Namespace = spec.Namespace
	run.Finalizers = []string{platformv1alpha1.AgentRunCleanupFinalizer}
	run.Labels = copyStringMap(spec.Labels)
	run.Annotations = copyStringMap(spec.Annotations)
	if _, generated := generatedProjectRuntime(spec.OwnerRef); generated {
		if run.Annotations == nil {
			run.Annotations = map[string]string{}
		}
		run.Annotations[RuntimeTriggerNameAnnotation] = spec.TriggerName
	}
	run.Spec = platformv1alpha1.AgentRunSpec{
		Trigger: platformv1alpha1.TriggerRef{
			Kind: spec.TriggerKind,
			Name: triggerName,
			Type: triggerType,
			ExternalRef: &platformv1alpha1.ExternalRef{
				ID:         spec.ExternalID,
				Identifier: spec.ExternalIdentifier,
				URL:        spec.ExternalURL,
			},
		},
		Repository: platformv1alpha1.RepositoryContext{
			URL:             d.RepoURL,
			BaseBranch:      d.BaseBranch,
			AdditionalRepos: append([]string(nil), d.AdditionalRepos...),
		},
		Context:        runContext,
		ExecutionMode:  d.ResolveExecutionMode(),
		WorkflowMode:   d.ResolveWorkflowMode(),
		Model:          prefixModelWithProvider(model, provider),
		AuthMode:       authMode,
		ReasoningLevel: reasoningLevel,
		OpenAIBaseURL:  triggersv1alpha1.ResolveOpenAIBaseURLWithAuth(provider, d.OpenAIBaseURL, authMode),
		Image:          d.Image,
		Secrets: &platformv1alpha1.AgentRunSecrets{
			ClaudeAPIKeySecret: d.Secrets.ClaudeApiKey,
			OpenAIOAuthSecret:  d.Secrets.OpenAIOAuthSecret,
			GitHubTokenSecret:  gitHubTokenSecret,
			SlackTokensSecret:  spec.SlackTokensSecret,
			ProviderKeys:       d.Secrets.ProviderKeys,
		},
	}
	if d.Team != nil {
		run.Spec.Team = d.Team.DeepCopy()
	}
	applyPolicyRefs(&run.Spec, d)
	if spec.ModeRef != nil {
		run.Spec.ModeRef = spec.ModeRef.DeepCopy()
	} else if d.ModeRef != nil {
		run.Spec.ModeRef = d.ModeRef.DeepCopy()
	}
	return run
}

func CreateTriggerRun(ctx context.Context, c client.Client, stateStore store.StateStore, spec TriggerRunSpec) (bool, *platformv1alpha1.AgentRun, error) {
	if err := validateTriggerRunDefaults(spec); err != nil {
		return false, nil, err
	}
	if err := resolveGeneratedProjectOwner(ctx, stateStore, &spec); err != nil {
		return false, nil, err
	}
	run := BuildTriggerRun(spec)
	if err := snapshotTriggerOwnerRoleModels(ctx, stateStore, run, spec); err != nil {
		return false, run, err
	}
	if spec.OwnerRef != nil && !isGeneratedProjectRuntime(spec.OwnerRef) {
		if err := ctrl.SetControllerReference(spec.OwnerRef, run, spec.Scheme); err != nil {
			return false, run, fmt.Errorf("setting owner reference on AgentRun: %w", err)
		}
	}
	created := false
	if err := c.Create(ctx, run); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return false, run, fmt.Errorf("creating AgentRun: %w", err)
		}
		if spec.FetchExistingOnAlreadyExists {
			if getErr := c.Get(ctx, client.ObjectKey{Namespace: run.Namespace, Name: run.Name}, run); getErr != nil {
				return false, run, fmt.Errorf("getting existing AgentRun: %w", getErr)
			}
		}
	} else {
		created = true
	}
	if created {
		if err := retainRunInstructionConfigMap(ctx, c, spec.Scheme, run); err != nil {
			_ = c.Delete(ctx, run)
			return false, run, err
		}
		recordTriggerRunOwner(ctx, stateStore, run, spec)
	}
	if spec.AfterCreate != nil {
		if err := spec.AfterCreate(ctx, run, created); err != nil {
			return false, run, err
		}
	}
	if created || spec.SeedOnAlreadyExists {
		seedTriggerRunSession(ctx, stateStore, run, spec, created)
	}
	return created, run, nil
}

type userRoleModelPreferenceReader interface {
	ListUserRoleModelPreferences(context.Context, string) ([]*auth.UserRoleModelPreference, error)
}

func retainRunInstructionConfigMap(ctx context.Context, c client.Client, scheme *runtime.Scheme, run *platformv1alpha1.AgentRun) error {
	if c == nil || scheme == nil || run == nil {
		return nil
	}
	name := strings.TrimSpace(run.Annotations["platform.gratefulagents.dev/instructions-configmap-ref"])
	if name == "" {
		return nil
	}
	configMap := &corev1.ConfigMap{}
	if err := c.Get(ctx, client.ObjectKey{Namespace: run.Namespace, Name: name}, configMap); err != nil {
		return fmt.Errorf("getting instructions ConfigMap %s: %w", name, err)
	}
	configMap.OwnerReferences = nil
	if err := ctrl.SetControllerReference(run, configMap, scheme); err != nil {
		return fmt.Errorf("setting AgentRun owner on instructions ConfigMap: %w", err)
	}
	if err := c.Update(ctx, configMap); err != nil {
		return fmt.Errorf("retaining instructions ConfigMap for AgentRun: %w", err)
	}
	return nil
}

func resolveGeneratedProjectOwner(ctx context.Context, stateStore store.StateStore, spec *TriggerRunSpec) error {
	if stateStore == nil || spec == nil || strings.TrimSpace(spec.OwnerID) != "" {
		return nil
	}
	projectName, generated := generatedProjectRuntime(spec.OwnerRef)
	if !generated {
		return nil
	}
	owner, err := stateStore.GetResourceOwner(ctx, "project", projectName, spec.Namespace)
	if err != nil {
		return fmt.Errorf("resolve Project trigger owner: %w", err)
	}
	if owner != nil {
		spec.OwnerID = strings.TrimSpace(owner.OwnerID)
	}
	return nil
}

func snapshotTriggerOwnerRoleModels(ctx context.Context, stateStore store.StateStore, run *platformv1alpha1.AgentRun, spec TriggerRunSpec) error {
	if stateStore == nil || run == nil {
		return nil
	}
	ownerID := strings.TrimSpace(spec.OwnerID)
	if ownerID == "" {
		resourceTypes := map[string]string{"LinearProject": "linear_project", "GitHubRepository": "github_repository", "Cron": "cron"}
		resourceType := resourceTypes[spec.TriggerKind]
		if resourceType == "" {
			return nil
		}
		owner, err := stateStore.GetResourceOwner(ctx, resourceType, spec.TriggerName, spec.Namespace)
		if err != nil {
			return fmt.Errorf("resolve trigger owner role models: %w", err)
		}
		if owner == nil {
			return nil
		}
		ownerID = strings.TrimSpace(owner.OwnerID)
	}
	reader, ok := stateStore.(userRoleModelPreferenceReader)
	if !ok || ownerID == "" {
		return nil
	}
	preferences, err := reader.ListUserRoleModelPreferences(ctx, ownerID)
	if err != nil {
		return fmt.Errorf("load trigger owner role models: %w", err)
	}
	byRole := map[string]map[string]string{}
	for _, preference := range preferences {
		if preference == nil || strings.TrimSpace(preference.RoleName) == "" || strings.TrimSpace(preference.Provider) == "" || strings.TrimSpace(preference.Model) == "" {
			continue
		}
		if byRole[preference.RoleName] == nil {
			byRole[preference.RoleName] = map[string]string{}
		}
		byRole[preference.RoleName][preference.Provider] = preference.Model
	}
	roles := make([]string, 0, len(byRole))
	for role := range byRole {
		roles = append(roles, role)
	}
	sort.Strings(roles)
	for _, role := range roles {
		run.Spec.RoleModelOverrides = append(run.Spec.RoleModelOverrides, platformv1alpha1.AgentRunRoleModelOverride{Role: role, ModelsByProvider: byRole[role]})
	}
	return nil
}

func generatedProjectRuntime(owner client.Object) (string, bool) {
	if !isGeneratedProjectRuntime(owner) {
		return "", false
	}
	return metadataValue(owner, ProjectNameMetadataKey), true
}

func isGeneratedProjectRuntime(owner client.Object) bool {
	return owner != nil &&
		metadataValue(owner, GeneratedRuntimeMarkerMetadataKey) != "" &&
		metadataValue(owner, ProjectNameMetadataKey) != "" &&
		metadataValue(owner, ProjectUIDMetadataKey) != "" &&
		metadataValue(owner, ProjectTriggerNameMetadataKey) != "" &&
		metadataValue(owner, ProjectTriggerTypeMetadataKey) != ""
}

func metadataValue(owner client.Object, key string) string {
	if value := strings.TrimSpace(owner.GetAnnotations()[key]); value != "" {
		return value
	}
	return strings.TrimSpace(owner.GetLabels()[key])
}

// RuntimeTriggerName returns the concrete adapter identity used by source
// controllers. Project-generated runs expose the declaration name in
// Trigger.Name and retain the adapter name in an internal annotation.
func RuntimeTriggerName(run *platformv1alpha1.AgentRun) string {
	if run == nil {
		return ""
	}
	if name := strings.TrimSpace(run.Annotations[RuntimeTriggerNameAnnotation]); name != "" {
		return name
	}
	return strings.TrimSpace(run.Spec.Trigger.Name)
}

func TriggerRunMatches(run *platformv1alpha1.AgentRun, kind, runtimeName string) bool {
	return run != nil && run.Spec.Trigger.Kind == kind && RuntimeTriggerName(run) == runtimeName
}

func validateTriggerRunDefaults(spec TriggerRunSpec) error {
	d := spec.Defaults
	provider := triggersv1alpha1.NormalizeProvider(d.Provider)
	authMode := triggersv1alpha1.NormalizeAuthMode(string(d.AuthMode))
	model := strings.TrimSpace(d.Model)
	if model == "" {
		model = triggersv1alpha1.DefaultMainModelForProvider(provider)
	}
	if model == "" {
		return fmt.Errorf("%s %s/%s spec.defaults.model is required for provider %s", spec.TriggerKind, spec.Namespace, spec.TriggerName, provider)
	}
	if triggersv1alpha1.IsOpenAICompatibleProvider(provider) {
		switch strings.ToLower(model) {
		case "small", "medium", "large":
			return fmt.Errorf("%s %s/%s spec.defaults.model must be an explicit model id; aliases (%s) are not allowed", spec.TriggerKind, spec.Namespace, spec.TriggerName, model)
		}
	}
	if err := triggersv1alpha1.ValidateProviderAuthMode(provider, authMode); err != nil {
		return fmt.Errorf("%s %s/%s auth configuration is invalid: %w", spec.TriggerKind, spec.Namespace, spec.TriggerName, err)
	}
	if triggersv1alpha1.RequiresOpenAIOAuthSecret(provider, authMode) && strings.TrimSpace(d.Secrets.OpenAIOAuthSecret) == "" {
		if spec.DetailedCredentialErrorContext {
			return fmt.Errorf("%s %s/%s requires spec.defaults.secrets.openaiOAuthSecret for provider=%s authMode=%s", spec.TriggerKind, spec.Namespace, spec.TriggerName, provider, authMode)
		}
		return fmt.Errorf("%s %s/%s requires spec.defaults.secrets.openaiOAuthSecret", spec.TriggerKind, spec.Namespace, spec.TriggerName)
	}
	if !triggersv1alpha1.OAuthSupportedForProvider(provider) && strings.TrimSpace(d.Secrets.OpenAIOAuthSecret) != "" {
		return fmt.Errorf("%s %s/%s spec.defaults.secrets.openaiOAuthSecret is only supported for OAuth-capable providers", spec.TriggerKind, spec.Namespace, spec.TriggerName)
	}
	if !triggersv1alpha1.RequiresOpenAIOAuthSecret(provider, authMode) && strings.TrimSpace(d.Secrets.ClaudeApiKey) == "" && len(d.Secrets.ProviderKeys) == 0 {
		if spec.DetailedCredentialErrorContext {
			return fmt.Errorf("%s %s/%s requires spec.defaults.secrets.claudeApiKey or providerKeys for provider=%s authMode=%s", spec.TriggerKind, spec.Namespace, spec.TriggerName, provider, authMode)
		}
		return fmt.Errorf("%s %s/%s requires spec.defaults.secrets.claudeApiKey or providerKeys", spec.TriggerKind, spec.Namespace, spec.TriggerName)
	}
	return nil
}

// agentRunResourceType is the collaboration-store resource type for AgentRuns,
// matching the dashboard's ownership and permission checks.
const agentRunResourceType = "agent_run"

// recordTriggerRunOwner best-effort records the triggering user as the run's
// owner in the collaboration store so the run is manageable (stop/delete) from
// the dashboard. A failure never blocks the run: it only degrades dashboard
// permissions, which admins can still exercise.
func recordTriggerRunOwner(ctx context.Context, stateStore store.StateStore, run *platformv1alpha1.AgentRun, spec TriggerRunSpec) {
	ownerID := strings.TrimSpace(spec.OwnerID)
	if stateStore == nil || ownerID == "" {
		return
	}
	prefix := spec.SeedLogPrefix
	if prefix == "" {
		prefix = strings.ToLower(spec.TriggerKind)
	}
	if err := stateStore.SetResourceOwner(ctx, agentRunResourceType, run.Name, run.Namespace, ownerID); err != nil {
		log.Printf("%s: failed to record owner for %s: %v", prefix, run.Name, err)
	}
}

func seedTriggerRunSession(ctx context.Context, stateStore store.StateStore, run *platformv1alpha1.AgentRun, spec TriggerRunSpec, created bool) {
	if stateStore == nil {
		return
	}
	prefix := spec.SeedLogPrefix
	if prefix == "" {
		prefix = strings.ToLower(spec.TriggerKind)
	}
	sess, err := stateStore.CreateSession(ctx, run.Name, run.Namespace, "pending", "setup")
	if err != nil {
		log.Printf("%s: failed to create session for %s: %v", prefix, run.Name, err)
		return
	}
	if !created {
		// The run already existed, so seeding is recovery for a crash between
		// run creation and the first seed. If the session already carries any
		// message the seed was delivered; appending again would queue the same
		// trigger message once per reconcile.
		messages, err := stateStore.GetMessages(ctx, sess.ID)
		if err != nil {
			log.Printf("%s: failed to check existing seed for %s: %v", prefix, run.Name, err)
			return
		}
		if len(messages) > 0 {
			return
		}
	}
	if _, err := stateStore.AppendMessage(ctx, sess.ID, "user", spec.SeedMessage, nil); err != nil {
		log.Printf("%s: failed to seed message for %s: %v", prefix, run.Name, err)
	}
}

func copyStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
