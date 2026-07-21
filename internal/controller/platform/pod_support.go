package platform

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/mcpattach"
	"github.com/gratefulagents/gratefulagents/internal/orchestration"
	"github.com/gratefulagents/sdk/pkg/agentsdk/sandbox"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

const (
	defaultRunTimeout         = 60 * time.Minute
	awaitingUserStep          = "awaiting-user"
	teamParentLabelName       = "platform.gratefulagents.dev/team-parent"
	childAutonomousAnnotation = "platform.gratefulagents.dev/child-autonomous"
	openAIOAuthVolumeName     = "openai-oauth"
	openAIOAuthMountPath      = "/var/run/gratefulagents/openai-oauth"
	openAIOAuthAuthJSONPath   = openAIOAuthMountPath + "/auth.json"
	openAIOAuthAccountIDPath  = openAIOAuthMountPath + "/account-id"
	anthropicOAuthAuthJSONEnv = "ANTHROPIC_OAUTH_AUTH_JSON_PATH"
	copilotOAuthAuthJSONEnv   = "COPILOT_OAUTH_AUTH_JSON_PATH"
	// providerOAuthMountRoot is the base directory for the additional
	// per-provider OAuth mounts from spec.secrets.providerOAuthSecrets.
	providerOAuthMountRoot = "/var/run/gratefulagents/oauth"
	// workspaceScratchPath is a separate EmptyDir mount for large, disposable
	// toolchains and build caches. It deliberately sits outside the repository
	// checkout so workspace checkpoints never try to archive its contents.
	workspaceScratchVolumeName = "workspace-scratch"
	workspaceScratchPath       = "/workspace/scratch"
)

var errRunPodReplaced = errors.New("stale run pod replaced")

func boolPtr(v bool) *bool { return &v }

func int64Ptr(v int64) *int64 { return &v }

// Run pods require the bwrap command sandbox by default
// (GRATEFULAGENTS_COMMAND_SANDBOX=required; admins can disable it per trigger
// via spec.defaults.disableCommandSandbox). bwrap only works without
// CAP_SYS_ADMIN when it runs unprivileged (unpriv user-namespace path);
// as uid 0 it does CLONE_NEWNS directly and fails in a default k8s pod.
// Force every worker container — including arbitrary user images that
// default to root, e.g. elixir:1.17 — to the default worker image's
// non-root uid/gid.
const (
	workerRunAsUID int64 = 1100
	workerRunAsGID int64 = 1100
)

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func sanitizeDNSLabel(prefix, name string) string {
	combined := strings.ToLower(strings.TrimSpace(prefix + "-" + name))
	var b strings.Builder
	prevDash := false
	for _, r := range combined {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
			prevDash = false
		case r >= '0' && r <= '9':
			b.WriteRune(r)
			prevDash = false
		default:
			if !prevDash {
				b.WriteByte('-')
				prevDash = true
			}
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		out = prefix
	}
	if len(out) > 63 {
		out = strings.Trim(out[:63], "-")
	}
	if out == "" {
		out = prefix
	}
	return out
}

func annotatedRunMode(run *platformv1alpha1.AgentRun) string {
	if run == nil || run.Annotations == nil {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(run.Annotations[runModeAnnotation]))
}

func initialCurrentStepForRun(run *platformv1alpha1.AgentRun) string {
	if annotatedRunMode(run) == "chat" {
		return awaitingUserStep
	}
	if run == nil {
		return "pending"
	}
	// At init time, snapshot may not be set yet, so check spec.WorkflowMode as initial hint.
	if run.Spec.WorkflowMode == platformv1alpha1.WorkflowModeAuto {
		return "auto"
	}
	return awaitingUserStep
}

func initialPhaseForRun(run *platformv1alpha1.AgentRun) platformv1alpha1.AgentRunPhase {
	if annotatedRunMode(run) == "chat" {
		return platformv1alpha1.AgentRunPhaseBlocked
	}
	return platformv1alpha1.AgentRunPhasePending
}

func effectiveTimeout(run *platformv1alpha1.AgentRun) time.Duration {
	if run != nil && run.Spec.Limits != nil && run.Spec.Limits.MaxRuntime.Duration > 0 {
		return run.Spec.Limits.MaxRuntime.Duration
	}
	return defaultRunTimeout
}

func runRBACRules(run *platformv1alpha1.AgentRun, verifiedSupervisedName, verifiedMaintainedRepositoryName string) []rbacv1.PolicyRule {
	currentName := ""
	if run != nil {
		currentName = strings.TrimSpace(run.Name)
	}
	readNames := []string{currentName, strings.TrimSpace(verifiedSupervisedName)}
	rules := []rbacv1.PolicyRule{
		{
			APIGroups:     []string{"platform.gratefulagents.dev"},
			Resources:     []string{"agentruns"},
			ResourceNames: compactStrings(readNames),
			Verbs:         []string{"get"},
		},
		{
			APIGroups:     []string{"platform.gratefulagents.dev"},
			Resources:     []string{"agentruns"},
			ResourceNames: compactStrings([]string{currentName}),
			Verbs:         []string{"patch"},
		},
		{
			APIGroups:     []string{"platform.gratefulagents.dev"},
			Resources:     []string{"agentruns/status"},
			ResourceNames: compactStrings([]string{currentName}),
			Verbs:         []string{"get", "patch", "update"},
		},
		{
			APIGroups: []string{"platform.gratefulagents.dev"},
			// The agent bootstrap resolves the run's MCP servers and skills
			// (mcpServerRefs, skillRefs, and skill requires.mcpServers).
			Resources: []string{"mcpservers", "skills"},
			Verbs:     []string{"get"},
		},
		{
			APIGroups: []string{"platform.gratefulagents.dev"},
			// Run pods resolve policy from namespaced CRDs in their own
			// namespace: permission mode (runtimeprofiles — the zero-trust
			// read-only fallback depends on this read succeeding), MCP config
			// filtering (mcppolicies), and guardrails (guardrailpolicies).
			// Granted here, per run, and not only via the shared
			// gratefulagents-agent-reader ClusterRole: that ClusterRole is
			// rewritten to exactly match whichever operator binary last
			// reconciled a run and its per-run ClusterRoleBindings are
			// deleted on terminal transitions, so depending on it for these
			// reads lets version skew or cleanup races silently degrade a
			// running pod to a read-only workspace.
			Resources: []string{"runtimeprofiles", "mcppolicies", "guardrailpolicies"},
			Verbs:     []string{"get"},
		},
		{
			APIGroups: []string{""},
			Resources: []string{"configmaps"},
			Verbs:     []string{"create", "get", "patch", "update"},
		},
		{
			APIGroups: []string{""},
			Resources: []string{"events"},
			Verbs:     []string{"create"},
		},
	}
	if verifiedMaintainedRepositoryName == "" {
		return rules
	}
	return append(rules,
		rbacv1.PolicyRule{
			APIGroups: []string{"platform.gratefulagents.dev"},
			Resources: []string{"agentruns"},
			Verbs:     []string{"get", "list", "watch", "patch", "update"},
		},
		rbacv1.PolicyRule{
			APIGroups:     []string{"triggers.gratefulagents.dev"},
			Resources:     []string{"githubrepositories"},
			ResourceNames: []string{verifiedMaintainedRepositoryName},
			Verbs:         []string{"get"},
		},
		rbacv1.PolicyRule{
			APIGroups:     []string{""},
			Resources:     []string{"secrets"},
			ResourceNames: []string{triggersv1alpha1.MaintainerCommandCapabilitySecretName(currentName)},
			Verbs:         []string{"get"},
		},
		rbacv1.PolicyRule{
			APIGroups: []string{"triggers.gratefulagents.dev"},
			Resources: []string{"maintainerworkitems"},
			Verbs:     []string{"get", "list", "watch"},
		},
		rbacv1.PolicyRule{
			APIGroups: []string{"triggers.gratefulagents.dev"},
			Resources: []string{"maintainerworkitemcommands"},
			Verbs:     []string{"create", "get", "list", "watch"},
		},
		rbacv1.PolicyRule{
			APIGroups: []string{"platform.gratefulagents.dev"},
			Resources: []string{"modetemplates"},
			Verbs:     []string{"get", "list"},
		},
	)
}

func compactStrings(values []string) []string {
	out := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func ensureRunRBAC(ctx context.Context, c client.Client, run *platformv1alpha1.AgentRun, saName string) error {
	ownerRef := metav1.OwnerReference{
		APIVersion:         platformv1alpha1.GroupVersion.String(),
		Kind:               "AgentRun",
		Name:               run.Name,
		UID:                run.UID,
		Controller:         boolPtr(true),
		BlockOwnerDeletion: boolPtr(true),
	}

	sa := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: saName, Namespace: run.Namespace, OwnerReferences: []metav1.OwnerReference{ownerRef}}}
	if err := c.Create(ctx, sa); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("creating ServiceAccount %s: %w", saName, err)
	}

	if err := ensureWorkerInfraSecret(ctx, c, run.Namespace); err != nil {
		return fmt.Errorf("syncing worker infra secret: %w", err)
	}

	verifiedSupervisedName, err := verifiedSupervisedRunName(ctx, c, run)
	if err != nil {
		return fmt.Errorf("verifying supervised AgentRun RBAC scope: %w", err)
	}
	maintainedRepositoryName, err := verifiedMaintainedRepositoryName(ctx, c, run)
	if err != nil {
		return fmt.Errorf("verifying maintained GitHubRepository RBAC scope: %w", err)
	}
	if maintainedRepositoryName != "" {
		if err := ensureMaintainerCommandCapability(ctx, c, run, ownerRef, maintainedRepositoryName); err != nil {
			return fmt.Errorf("ensuring maintainer command capability: %w", err)
		}
	}
	roleName := saName + "-role"
	role := &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{Name: roleName, Namespace: run.Namespace, OwnerReferences: []metav1.OwnerReference{ownerRef}},
		Rules:      runRBACRules(run, verifiedSupervisedName, maintainedRepositoryName),
	}
	if err := c.Create(ctx, role); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("creating Role %s: %w", roleName, err)
		}
		existing := &rbacv1.Role{}
		if getErr := c.Get(ctx, client.ObjectKeyFromObject(role), existing); getErr != nil {
			return fmt.Errorf("getting existing Role %s: %w", roleName, getErr)
		}
		if !reflect.DeepEqual(existing.Rules, role.Rules) {
			existing.Rules = role.Rules
			if updateErr := c.Update(ctx, existing); updateErr != nil {
				return fmt.Errorf("updating Role %s: %w", roleName, updateErr)
			}
		}
	}

	rbName := saName + "-binding"
	rb := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: rbName, Namespace: run.Namespace, OwnerReferences: []metav1.OwnerReference{ownerRef}},
		RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: roleName},
		Subjects:   []rbacv1.Subject{{Kind: rbacv1.ServiceAccountKind, Name: saName, Namespace: run.Namespace}},
	}
	if err := c.Create(ctx, rb); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("creating RoleBinding %s: %w", rbName, err)
	}

	// Cluster-scoped RBAC: agent pods need read access to cluster-scoped
	// CRDs (RoleInstructions, ModeTemplates).
	if err := ensureClusterScopedRBAC(ctx, c, run, saName); err != nil {
		return fmt.Errorf("creating cluster-scoped RBAC: %w", err)
	}

	return nil
}

func ensureMaintainerCommandCapability(ctx context.Context, c client.Client, run *platformv1alpha1.AgentRun, ownerRef metav1.OwnerReference, repositoryName string) error {
	repository := &triggersv1alpha1.GitHubRepository{}
	if err := c.Get(ctx, client.ObjectKey{Namespace: run.Namespace, Name: repositoryName}, repository); err != nil {
		return fmt.Errorf("getting maintained GitHubRepository %s/%s: %w", run.Namespace, repositoryName, err)
	}
	name := triggersv1alpha1.MaintainerCommandCapabilitySecretName(run.Name)
	existing := &corev1.Secret{}
	if err := c.Get(ctx, client.ObjectKey{Namespace: run.Namespace, Name: name}, existing); err == nil {
		key := existing.Data[triggersv1alpha1.MaintainerCommandCapabilitySecretKey]
		if len(key) < 32 {
			return fmt.Errorf("secret %s/%s has a missing or invalid %q", run.Namespace, name, triggersv1alpha1.MaintainerCommandCapabilitySecretKey)
		}
		if !metav1.IsControlledBy(existing, run) {
			return fmt.Errorf("secret %s/%s is not controlled by AgentRun %s", run.Namespace, name, run.Name)
		}
		if string(existing.Data[triggersv1alpha1.MaintainerCommandCapabilityRepositoryNameKey]) != repository.Name || string(existing.Data[triggersv1alpha1.MaintainerCommandCapabilityRepositoryUIDKey]) != string(repository.UID) {
			return fmt.Errorf("secret %s/%s is bound to a different GitHubRepository", run.Namespace, name)
		}
		return nil
	} else if !apierrors.IsNotFound(err) {
		return err
	}

	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return fmt.Errorf("generating capability key: %w", err)
	}
	immutable := true
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: run.Namespace, OwnerReferences: []metav1.OwnerReference{ownerRef}},
		Immutable:  &immutable,
		Type:       corev1.SecretTypeOpaque,
		Data: map[string][]byte{
			triggersv1alpha1.MaintainerCommandCapabilitySecretKey:         key,
			triggersv1alpha1.MaintainerCommandCapabilityRepositoryNameKey: []byte(repository.Name),
			triggersv1alpha1.MaintainerCommandCapabilityRepositoryUIDKey:  []byte(repository.UID),
		},
	}
	if err := c.Create(ctx, secret); err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}
	return nil
}

const clusterReadRoleName = "gratefulagents-agent-reader"

// workerInfraSecretName is the per-namespace Secret carrying infra credentials
// (S3 access keys, database URL) for worker pods. Worker pods reference it via
// secretKeyRef so the values never appear inline in pod specs, where anyone
// with pod read access could see them.
const workerInfraSecretName = "gratefulagents-worker-infra"

const (
	workerInfraKeyAWSAccessKeyID     = "aws-access-key-id"
	workerInfraKeyAWSSecretAccessKey = "aws-secret-access-key"
	workerInfraKeyDatabaseURL        = "database-url"
)

// ensureWorkerInfraSecret syncs the operator's infra credentials into the
// run namespace so worker pods can consume them via secretKeyRef.
func ensureWorkerInfraSecret(ctx context.Context, c client.Client, namespace string) error {
	data := map[string][]byte{}
	if v := os.Getenv("AWS_ACCESS_KEY_ID"); v != "" {
		data[workerInfraKeyAWSAccessKeyID] = []byte(v)
	}
	if v := os.Getenv("AWS_SECRET_ACCESS_KEY"); v != "" {
		data[workerInfraKeyAWSSecretAccessKey] = []byte(v)
	}
	if v := os.Getenv("DATABASE_URL"); v != "" {
		data[workerInfraKeyDatabaseURL] = []byte(v)
	}
	if len(data) == 0 {
		return nil // nothing to sync; pod env refs are optional
	}

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: workerInfraSecretName, Namespace: namespace},
		Data:       data,
	}
	if err := c.Create(ctx, secret); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("creating Secret %s: %w", workerInfraSecretName, err)
		}
		existing := &corev1.Secret{}
		if getErr := c.Get(ctx, client.ObjectKeyFromObject(secret), existing); getErr != nil {
			return fmt.Errorf("getting existing Secret %s: %w", workerInfraSecretName, getErr)
		}
		if !reflect.DeepEqual(existing.Data, data) {
			existing.Data = data
			if updateErr := c.Update(ctx, existing); updateErr != nil {
				return fmt.Errorf("updating Secret %s: %w", workerInfraSecretName, updateErr)
			}
		}
	}
	return nil
}

// workerInfraSecretEnv returns an env var sourced from the synced infra
// Secret. Optional so pods start unchanged when a key is not configured.
func workerInfraSecretEnv(envName, secretKey string) corev1.EnvVar {
	optional := true
	return corev1.EnvVar{
		Name: envName,
		ValueFrom: &corev1.EnvVarSource{
			SecretKeyRef: &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: workerInfraSecretName},
				Key:                  secretKey,
				Optional:             &optional,
			},
		},
	}
}

// ensureClusterScopedRBAC creates a shared ClusterRole (if missing) granting
// read access to cluster-scoped CRDs and a per-run ClusterRoleBinding for the
// agent pod's service account.
func ensureClusterScopedRBAC(ctx context.Context, c client.Client, run *platformv1alpha1.AgentRun, saName string) error {
	// Shared ClusterRole — idempotent create.
	cr := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{
			Name: clusterReadRoleName,
			Labels: map[string]string{
				"app.kubernetes.io/name":      "gratefulagents",
				"app.kubernetes.io/component": "agent-runner",
			},
		},
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups: []string{"platform.gratefulagents.dev"},
				// roleinstructions and modetemplates are cluster-scoped and
				// genuinely need this ClusterRole. guardrailpolicies,
				// runtimeprofiles, and mcppolicies are namespaced and also
				// granted by the per-run namespaced Role (runRBACRules);
				// they stay listed here so pods provisioned by older
				// operator builds keep working during rollouts.
				Resources: []string{"roleinstructions", "modetemplates", "guardrailpolicies", "runtimeprofiles", "mcppolicies"},
				Verbs:     []string{"get", "list", "watch"},
			},
		},
	}
	if err := c.Create(ctx, cr); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("creating ClusterRole %s: %w", clusterReadRoleName, err)
		}
		// Update the existing ClusterRole to pick up any resource list changes.
		existing := &rbacv1.ClusterRole{}
		if err := c.Get(ctx, client.ObjectKeyFromObject(cr), existing); err == nil {
			if !reflect.DeepEqual(existing.Rules, cr.Rules) {
				existing.Rules = cr.Rules
				if updateErr := c.Update(ctx, existing); updateErr != nil {
					return fmt.Errorf("updating ClusterRole %s: %w", clusterReadRoleName, updateErr)
				}
			}
		}
	}

	// Per-run ClusterRoleBinding — labeled for cleanup.
	crbName := saName + "-cluster-binding"
	crb := clusterRoleBindingForRun(crbName, run, saName, clusterReadRoleName)
	if err := c.Create(ctx, crb); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("creating ClusterRoleBinding %s: %w", crbName, err)
	}

	adminCRBName := saName + "-admin-binding"
	if !run.Spec.KubernetesAdmin {
		if err := c.Delete(ctx, &rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: adminCRBName}}); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("deleting ClusterRoleBinding %s: %w", adminCRBName, err)
		}
		return nil
	}

	adminCRB := clusterRoleBindingForRun(adminCRBName, run, saName, "cluster-admin")
	if err := c.Create(ctx, adminCRB); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("creating ClusterRoleBinding %s: %w", adminCRBName, err)
		}
		existing := &rbacv1.ClusterRoleBinding{}
		if getErr := c.Get(ctx, client.ObjectKeyFromObject(adminCRB), existing); getErr != nil {
			return fmt.Errorf("getting existing ClusterRoleBinding %s: %w", adminCRBName, getErr)
		}
		if !reflect.DeepEqual(existing.Labels, adminCRB.Labels) || !reflect.DeepEqual(existing.RoleRef, adminCRB.RoleRef) || !reflect.DeepEqual(existing.Subjects, adminCRB.Subjects) {
			existing.Labels = adminCRB.Labels
			existing.RoleRef = adminCRB.RoleRef
			existing.Subjects = adminCRB.Subjects
			if updateErr := c.Update(ctx, existing); updateErr != nil {
				return fmt.Errorf("updating ClusterRoleBinding %s: %w", adminCRBName, updateErr)
			}
		}
	}

	return nil
}

func clusterRoleBindingForRun(name string, run *platformv1alpha1.AgentRun, saName, clusterRoleName string) *rbacv1.ClusterRoleBinding {
	return &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			Labels: map[string]string{
				"app.kubernetes.io/name":                "gratefulagents",
				"app.kubernetes.io/component":           "agent-runner",
				"platform.gratefulagents.dev/owner-run": run.Name,
				"platform.gratefulagents.dev/namespace": run.Namespace,
			},
		},
		RoleRef: rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: clusterRoleName},
		Subjects: []rbacv1.Subject{{
			Kind:      rbacv1.ServiceAccountKind,
			Name:      saName,
			Namespace: run.Namespace,
		}},
	}
}

// cleanupClusterRoleBindings removes the per-run ClusterRoleBinding when a run
// is deleted. Called from the reconciler's teardown path.
func cleanupClusterRoleBindings(ctx context.Context, c client.Client, run *platformv1alpha1.AgentRun) error {
	var crbList rbacv1.ClusterRoleBindingList
	if err := c.List(ctx, &crbList, client.MatchingLabels{
		"platform.gratefulagents.dev/owner-run": run.Name,
		"platform.gratefulagents.dev/namespace": run.Namespace,
	}); err != nil {
		return err
	}
	for i := range crbList.Items {
		if err := c.Delete(ctx, &crbList.Items[i]); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
	}
	return nil
}

// effectiveProvider derives the LLM provider for a run. If the model field
// contains a prefix (e.g. "anthropic/claude-sonnet-4-6"), the prefix is used.
// Otherwise defaults to "openai".
func effectiveProvider(run *platformv1alpha1.AgentRun) string {
	if run == nil {
		return "openai"
	}
	if i := strings.Index(run.Spec.Model, "/"); i > 0 {
		return strings.ToLower(run.Spec.Model[:i])
	}
	return "openai"
}

// providerEnvVarName returns the environment variable name for a provider's API key.
func providerEnvVarName(provider string) string {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "anthropic":
		return "ANTHROPIC_API_KEY"
	case "openai":
		return "OPENAI_API_KEY"
	case "openrouter":
		return "OPENROUTER_API_KEY"
	case "gemini":
		return "GEMINI_API_KEY"
	case "groq":
		return "GROQ_API_KEY"
	case "xai":
		return "XAI_API_KEY"
	case "copilot":
		return "COPILOT_API_KEY"
	default:
		return strings.ToUpper(strings.TrimSpace(provider)) + "_API_KEY"
	}
}

// providerAPIKeyEnvs returns env vars for all configured provider API keys.
// It first mounts all entries from secrets.providerKeys, then falls back to
// the legacy claudeApiKeySecret for backward compatibility (only if the
// provider isn't already covered by providerKeys).
func providerAPIKeyEnvs(run *platformv1alpha1.AgentRun) []corev1.EnvVar {
	if run == nil || run.Spec.Secrets == nil {
		return nil
	}
	var envs []corev1.EnvVar
	mounted := map[string]bool{}
	oauthProvider := ""
	if requiresProviderOAuth(run) {
		oauthProvider = strings.ToLower(strings.TrimSpace(effectiveProvider(run)))
	}

	// New: explicit per-provider keys.
	for _, pk := range run.Spec.Secrets.ProviderKeys {
		provider := strings.ToLower(strings.TrimSpace(pk.Provider))
		if oauthProvider != "" && provider == oauthProvider {
			continue
		}
		envName := providerEnvVarName(pk.Provider)
		if mounted[envName] {
			continue
		}
		secretKey := pk.SecretKey
		if secretKey == "" {
			secretKey = "api-key"
		}
		envs = append(envs, corev1.EnvVar{
			Name: envName,
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: pk.SecretName},
					Key:                  secretKey,
				},
			},
		})
		mounted[envName] = true
	}

	// Legacy fallback: claudeApiKeySecret → single provider env var.
	legacySecret := strings.TrimSpace(run.Spec.Secrets.ClaudeAPIKeySecret)
	if legacySecret != "" {
		envName := providerEnvVarName(effectiveProvider(run))
		if !mounted[envName] && !requiresProviderOAuth(run) {
			envs = append(envs, corev1.EnvVar{
				Name: envName,
				ValueFrom: &corev1.EnvVarSource{
					SecretKeyRef: &corev1.SecretKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{Name: legacySecret},
						Key:                  "api-key",
					},
				},
			})
		}
	}

	return envs
}

func openAIAuthMode(run *platformv1alpha1.AgentRun) platformv1alpha1.AgentRunAuthMode {
	if run == nil {
		return platformv1alpha1.AgentRunAuthModeAPIKey
	}
	mode := strings.ToLower(strings.TrimSpace(string(run.Spec.AuthMode)))
	switch mode {
	case string(platformv1alpha1.AgentRunAuthModeOAuth):
		return platformv1alpha1.AgentRunAuthModeOAuth
	default:
		return platformv1alpha1.AgentRunAuthModeAPIKey
	}
}

func requiresOpenAIOAuth(run *platformv1alpha1.AgentRun) bool {
	if run == nil {
		return false
	}
	if !strings.EqualFold(effectiveProvider(run), "openai") {
		return false
	}
	return openAIAuthMode(run) == platformv1alpha1.AgentRunAuthModeOAuth
}

func requiresAnthropicOAuth(run *platformv1alpha1.AgentRun) bool {
	if run == nil {
		return false
	}
	if !strings.EqualFold(effectiveProvider(run), "anthropic") {
		return false
	}
	return openAIAuthMode(run) == platformv1alpha1.AgentRunAuthModeOAuth
}

func requiresCopilotOAuth(run *platformv1alpha1.AgentRun) bool {
	if run == nil {
		return false
	}
	if !strings.EqualFold(effectiveProvider(run), "copilot") {
		return false
	}
	return openAIAuthMode(run) == platformv1alpha1.AgentRunAuthModeOAuth
}

func requiresProviderOAuth(run *platformv1alpha1.AgentRun) bool {
	return requiresOpenAIOAuth(run) || requiresAnthropicOAuth(run) || requiresCopilotOAuth(run)
}

func openAIAuthModeEnv(run *platformv1alpha1.AgentRun) *corev1.EnvVar {
	if run == nil {
		return nil
	}
	return &corev1.EnvVar{Name: "OPENAI_AUTH_MODE", Value: string(openAIAuthMode(run))}
}

func openAIOAuthEnvs(run *platformv1alpha1.AgentRun) []corev1.EnvVar {
	if !requiresProviderOAuth(run) {
		return nil
	}
	if requiresAnthropicOAuth(run) {
		return []corev1.EnvVar{{Name: anthropicOAuthAuthJSONEnv, Value: openAIOAuthAuthJSONPath}}
	}
	if requiresCopilotOAuth(run) {
		return []corev1.EnvVar{{Name: copilotOAuthAuthJSONEnv, Value: openAIOAuthAuthJSONPath}}
	}
	return []corev1.EnvVar{
		{Name: "OPENAI_OAUTH_AUTH_JSON_PATH", Value: openAIOAuthAuthJSONPath},
		{Name: "OPENAI_OAUTH_ACCOUNT_ID_PATH", Value: openAIOAuthAccountIDPath},
	}
}

func openAIOAuthVolume(run *platformv1alpha1.AgentRun) *corev1.Volume {
	if !requiresProviderOAuth(run) || run.Spec.Secrets == nil || strings.TrimSpace(run.Spec.Secrets.OpenAIOAuthSecret) == "" {
		return nil
	}
	// The names remain openai-* for compatibility with existing manifests, but
	// this projected secret is the shared provider OAuth material mount.
	return &corev1.Volume{
		Name: openAIOAuthVolumeName,
		VolumeSource: corev1.VolumeSource{
			Projected: &corev1.ProjectedVolumeSource{
				Sources: []corev1.VolumeProjection{
					{
						Secret: &corev1.SecretProjection{
							LocalObjectReference: corev1.LocalObjectReference{Name: run.Spec.Secrets.OpenAIOAuthSecret},
							Items:                []corev1.KeyToPath{{Key: "auth.json", Path: "auth.json"}},
						},
					},
					{
						Secret: &corev1.SecretProjection{
							LocalObjectReference: corev1.LocalObjectReference{Name: run.Spec.Secrets.OpenAIOAuthSecret},
							Items:                []corev1.KeyToPath{{Key: "account-id", Path: "account-id"}},
							Optional:             boolPtr(true),
						},
					},
				},
			},
		},
	}
}

func openAIOAuthVolumeMount(run *platformv1alpha1.AgentRun) *corev1.VolumeMount {
	if openAIOAuthVolume(run) == nil {
		return nil
	}
	return &corev1.VolumeMount{Name: openAIOAuthVolumeName, MountPath: openAIOAuthMountPath, ReadOnly: true}
}

// providerOAuthSecretEntries returns the normalized additional per-provider
// OAuth secret refs to mount: OAuth-capable providers only, deduped, skipping
// the provider already served by the legacy openaiOAuthSecret volume.
func providerOAuthSecretEntries(run *platformv1alpha1.AgentRun) []platformv1alpha1.ProviderOAuthSecretRef {
	if run == nil || run.Spec.Secrets == nil {
		return nil
	}
	legacyProvider := ""
	if requiresProviderOAuth(run) && strings.TrimSpace(run.Spec.Secrets.OpenAIOAuthSecret) != "" {
		legacyProvider = strings.ToLower(strings.TrimSpace(effectiveProvider(run)))
	}
	var entries []platformv1alpha1.ProviderOAuthSecretRef
	seen := map[string]bool{}
	for _, ref := range run.Spec.Secrets.ProviderOAuthSecrets {
		provider := strings.ToLower(strings.TrimSpace(ref.Provider))
		secretName := strings.TrimSpace(ref.SecretName)
		switch provider {
		case "openai", "anthropic", "copilot":
		default:
			continue
		}
		if secretName == "" || provider == legacyProvider || seen[provider] {
			continue
		}
		seen[provider] = true
		entries = append(entries, platformv1alpha1.ProviderOAuthSecretRef{Provider: provider, SecretName: secretName})
	}
	return entries
}

func providerOAuthMountPath(provider string) string {
	return providerOAuthMountRoot + "/" + provider
}

// providerOAuthVolumes mounts each additional provider OAuth secret so the
// agent can switch to that provider mid-run without a compute restart.
func providerOAuthVolumes(run *platformv1alpha1.AgentRun) []corev1.Volume {
	var volumes []corev1.Volume
	for _, ref := range providerOAuthSecretEntries(run) {
		volumes = append(volumes, corev1.Volume{
			Name: "provider-oauth-" + ref.Provider,
			VolumeSource: corev1.VolumeSource{
				Projected: &corev1.ProjectedVolumeSource{
					Sources: []corev1.VolumeProjection{
						{
							Secret: &corev1.SecretProjection{
								LocalObjectReference: corev1.LocalObjectReference{Name: ref.SecretName},
								Items:                []corev1.KeyToPath{{Key: "auth.json", Path: "auth.json"}},
							},
						},
						{
							Secret: &corev1.SecretProjection{
								LocalObjectReference: corev1.LocalObjectReference{Name: ref.SecretName},
								Items:                []corev1.KeyToPath{{Key: "account-id", Path: "account-id"}},
								Optional:             boolPtr(true),
							},
						},
					},
				},
			},
		})
	}
	return volumes
}

func providerOAuthVolumeMounts(run *platformv1alpha1.AgentRun) []corev1.VolumeMount {
	var mounts []corev1.VolumeMount
	for _, ref := range providerOAuthSecretEntries(run) {
		mounts = append(mounts, corev1.VolumeMount{
			Name:      "provider-oauth-" + ref.Provider,
			MountPath: providerOAuthMountPath(ref.Provider),
			ReadOnly:  true,
		})
	}
	return mounts
}

// providerOAuthEnvs points the agent at each additionally mounted provider's
// OAuth material. These never collide with the legacy single-provider envs
// (openAIOAuthEnvs): entries for the legacy provider are filtered out.
func providerOAuthEnvs(run *platformv1alpha1.AgentRun) []corev1.EnvVar {
	var envs []corev1.EnvVar
	for _, ref := range providerOAuthSecretEntries(run) {
		authJSONPath := providerOAuthMountPath(ref.Provider) + "/auth.json"
		switch ref.Provider {
		case "anthropic":
			envs = append(envs, corev1.EnvVar{Name: anthropicOAuthAuthJSONEnv, Value: authJSONPath})
		case "copilot":
			envs = append(envs, corev1.EnvVar{Name: copilotOAuthAuthJSONEnv, Value: authJSONPath})
		default: // openai
			envs = append(envs,
				corev1.EnvVar{Name: "OPENAI_OAUTH_AUTH_JSON_PATH", Value: authJSONPath},
				corev1.EnvVar{Name: "OPENAI_OAUTH_ACCOUNT_ID_PATH", Value: providerOAuthMountPath(ref.Provider) + "/account-id"},
			)
		}
	}
	return envs
}

func githubTokenEnv(run *platformv1alpha1.AgentRun) *corev1.EnvVar {
	if run == nil || run.Spec.Secrets == nil || strings.TrimSpace(run.Spec.Secrets.GitHubTokenSecret) == "" {
		return nil
	}
	secretRef := corev1.SecretKeySelector{
		LocalObjectReference: corev1.LocalObjectReference{Name: run.Spec.Secrets.GitHubTokenSecret},
		Key:                  "token",
	}
	return &corev1.EnvVar{Name: "GH_PAT", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &secretRef}}
}

// slackTokensEnvs exposes read-only Slack credentials to the run pod when the
// run references a Slack tokens Secret, powering agent-side Slack read tools.
// Keys are optional: a missing user token simply narrows what the tools can
// read; sends always stay with the connector's approval flow.
func slackTokensEnvs(run *platformv1alpha1.AgentRun) []corev1.EnvVar {
	if run == nil || run.Spec.Secrets == nil {
		return nil
	}
	secretName := strings.TrimSpace(run.Spec.Secrets.SlackTokensSecret)
	if secretName == "" {
		return nil
	}
	env := func(name, key string) corev1.EnvVar {
		return corev1.EnvVar{
			Name: name,
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: secretName},
					Key:                  key,
					Optional:             boolPtr(true),
				},
			},
		}
	}
	return []corev1.EnvVar{
		env("SLACK_BOT_TOKEN", "bot-token"),
		env("SLACK_USER_TOKEN", "user-token"),
	}
}

// resolveMCPServerSecretEnvs collects the secret-backed environment variables
// declared by the MCP servers a run references — explicitly via mcpServerRefs
// or pulled in by an attached skill's requires.mcpServers — as secretKeyRef
// envs for the run pod. This is how token-bearing MCP servers (e.g. Grafana)
// receive credentials without plaintext CRD values. Missing servers are
// skipped (the MCP layer already tolerates absent servers); duplicate env
// names keep the first declaration. Entries default to optional so a missing
// Secret never blocks pod startup unless the server demands it.
func resolveMCPServerSecretEnvs(ctx context.Context, c client.Client, run *platformv1alpha1.AgentRun) []corev1.EnvVar {
	if run == nil || c == nil {
		return nil
	}
	refs := mcpattach.EffectiveMCPServerRefs(ctx, c, run)
	if len(refs) == 0 {
		return nil
	}
	var envs []corev1.EnvVar
	seen := map[string]bool{}
	for _, ref := range refs {
		srv := &platformv1alpha1.MCPServer{}
		key := client.ObjectKey{Namespace: run.Namespace, Name: strings.TrimSpace(ref.Name)}
		if key.Name == "" {
			continue
		}
		if err := c.Get(ctx, key, srv); err != nil {
			log.Log.V(1).Info("skipping MCPServer secretEnv (fetch failed)", "server", key.Name, "error", err)
			continue
		}
		if srv.Spec.MCPServerConfig == nil {
			continue
		}
		for _, se := range srv.Spec.MCPServerConfig.SecretEnv {
			name := strings.TrimSpace(se.Name)
			if name == "" || seen[name] {
				continue
			}
			optional := true
			if se.Optional != nil {
				optional = *se.Optional
			}
			seen[name] = true
			envs = append(envs, corev1.EnvVar{
				Name: name,
				ValueFrom: &corev1.EnvVarSource{
					SecretKeyRef: &corev1.SecretKeySelector{
						LocalObjectReference: corev1.LocalObjectReference{Name: se.SecretName},
						Key:                  se.SecretKey,
						Optional:             boolPtr(optional),
					},
				},
			})
		}
	}
	return envs
}

func maybeAppendEnv(envs []corev1.EnvVar, env *corev1.EnvVar) []corev1.EnvVar {
	if env == nil {
		return envs
	}
	return append(envs, *env)
}

func maybeAppendVolumeMount(mounts []corev1.VolumeMount, mount *corev1.VolumeMount) []corev1.VolumeMount {
	if mount == nil {
		return mounts
	}
	return append(mounts, *mount)
}

func maybeAppendVolume(volumes []corev1.Volume, volume *corev1.Volume) []corev1.Volume {
	if volume == nil {
		return volumes
	}
	return append(volumes, *volume)
}

func selectOpenAIBaseURL(run *platformv1alpha1.AgentRun) string {
	if run == nil {
		return ""
	}
	return strings.TrimSpace(run.Spec.OpenAIBaseURL)
}

func instructionsConfigMapName(run *platformv1alpha1.AgentRun) string {
	if run == nil || run.Annotations == nil {
		return ""
	}
	return strings.TrimSpace(run.Annotations["platform.gratefulagents.dev/instructions-configmap-ref"])
}

func openAIApiModeFromAnnotations(run *platformv1alpha1.AgentRun) string {
	if run == nil || run.Annotations == nil {
		return ""
	}
	return strings.TrimSpace(run.Annotations["platform.gratefulagents.dev/openai-api-mode"])
}

// modelFallbacksForRun resolves the ordered model fallback list (used by
// OpenRouter-style providers) from the run annotation, falling back to the
// manager-level MODEL_FALLBACKS env.
func modelFallbacksForRun(run *platformv1alpha1.AgentRun) string {
	if run != nil && run.Annotations != nil {
		if value := strings.TrimSpace(run.Annotations["platform.gratefulagents.dev/model-fallbacks"]); value != "" {
			return value
		}
	}
	return strings.TrimSpace(os.Getenv("MODEL_FALLBACKS"))
}

func commandSandboxConfigEnvs(run *platformv1alpha1.AgentRun) []corev1.EnvVar {
	var envs []corev1.EnvVar
	for _, name := range sandbox.SandboxConfigEnvNames() {
		// When the run disables the command sandbox, commandSandboxModeEnvs
		// forces the allow-unsafe flag on; skip the operator-forwarded value
		// so the pod spec never carries a conflicting duplicate.
		if runDisablesCommandSandbox(run) && name == sandbox.SandboxAllowUnsafeReadOnlyLocalEnv {
			continue
		}
		if value := strings.TrimSpace(os.Getenv(name)); value != "" {
			envs = append(envs, corev1.EnvVar{Name: name, Value: value})
		}
	}
	return envs
}

// ensureWorkspaceScratchSandboxConfig grants the dedicated scratch mount to
// write-capable subprocess sandboxes and routes Go caches there without
// discarding operator- or RuntimeProfile-provided configuration.
func ensureWorkspaceScratchSandboxConfig(container *corev1.Container) {
	if container == nil {
		return
	}
	paths := []string{workspaceScratchPath}
	var extraEnvValue string
	for _, env := range container.Env {
		switch env.Name {
		case sandbox.SandboxExtraWritablePathsEnv:
			for _, path := range filepath.SplitList(env.Value) {
				path = strings.TrimSpace(path)
				if path != "" && path != workspaceScratchPath {
					paths = append(paths, path)
				}
			}
		case sandbox.SandboxExtraEnvEnv:
			extraEnvValue = env.Value
		}
	}
	upsertContainerEnv(container, corev1.EnvVar{
		Name:  sandbox.SandboxExtraWritablePathsEnv,
		Value: strings.Join(paths, string(os.PathListSeparator)),
	})

	// ConfigFromEnv only forwards explicitly granted variables into bwrap.
	// Normalize the legacy comma-separated form and remove stale cache entries
	// before appending the platform-owned values, making this merge idempotent.
	if !strings.Contains(extraEnvValue, "\n") {
		extraEnvValue = strings.ReplaceAll(extraEnvValue, ",", "\n")
	}
	var extraEnv []string
	for _, pair := range strings.Split(extraEnvValue, "\n") {
		pair = strings.TrimSpace(pair)
		key, _, ok := strings.Cut(pair, "=")
		if pair == "" || !ok || key == "GOPATH" || key == "GOMODCACHE" || key == "GOCACHE" {
			continue
		}
		extraEnv = append(extraEnv, pair)
	}
	extraEnv = append(extraEnv,
		"GOPATH="+workspaceScratchPath+"/go",
		"GOMODCACHE="+workspaceScratchPath+"/go/pkg/mod",
		"GOCACHE="+workspaceScratchPath+"/go-build",
	)
	upsertContainerEnv(container, corev1.EnvVar{
		Name:  sandbox.SandboxExtraEnvEnv,
		Value: strings.Join(extraEnv, "\n"),
	})
}

func runDisablesCommandSandbox(run *platformv1alpha1.AgentRun) bool {
	return run != nil && run.Spec.DisableCommandSandbox
}

// commandSandboxModeEnvs selects the subprocess sandbox posture for the run
// pod. Default: the enforcing bubblewrap sandbox is required. When the run
// (via an admin-set trigger option) disables the command sandbox, bubblewrap
// is bypassed entirely — including for read-only permission modes, which
// otherwise refuse to run outside the enforcing sandbox.
func commandSandboxModeEnvs(run *platformv1alpha1.AgentRun) []corev1.EnvVar {
	if runDisablesCommandSandbox(run) {
		return []corev1.EnvVar{
			{Name: sandbox.SandboxModeEnv, Value: "disabled"},
			{Name: sandbox.SandboxAllowUnsafeReadOnlyLocalEnv, Value: "1"},
		}
	}
	return []corev1.EnvVar{{Name: sandbox.SandboxModeEnv, Value: "required"}}
}

// gitIdentityEnvs exports the run's snapshotted author identity to the worker.
// Author variables override the image's default GitHub App identity.
func gitIdentityEnvs(run *platformv1alpha1.AgentRun) []corev1.EnvVar {
	if run == nil || run.Annotations == nil {
		return nil
	}
	var envs []corev1.EnvVar
	name := strings.TrimSpace(run.Annotations[platformv1alpha1.GitAuthorNameAnnotation])
	email := strings.TrimSpace(run.Annotations[platformv1alpha1.GitAuthorEmailAnnotation])
	if name != "" && email != "" {
		envs = append(envs,
			corev1.EnvVar{Name: "GIT_AUTHOR_NAME", Value: name},
			corev1.EnvVar{Name: "GIT_AUTHOR_EMAIL", Value: email},
			corev1.EnvVar{Name: "GIT_COMMITTER_NAME", Value: name},
			corev1.EnvVar{Name: "GIT_COMMITTER_EMAIL", Value: email},
		)
	}
	return envs
}

func runtimeProfileCommandSandboxConfigEnvs(runtimeProfile *platformv1alpha1.RuntimeProfile) []corev1.EnvVar {
	if runtimeProfile == nil || runtimeProfile.Spec.Sandbox == nil || runtimeProfile.Spec.Sandbox.CommandSandbox == nil {
		return nil
	}
	cfg := runtimeProfile.Spec.Sandbox.CommandSandbox
	var envs []corev1.EnvVar
	if value := cleanRuntimeProfilePathList(cfg.Path, false, false); value != "" {
		envs = append(envs, corev1.EnvVar{Name: sandbox.SandboxPathEnv, Value: value})
	}
	if value := cleanRuntimeProfilePathList(cfg.PathPrepend, false, false); value != "" {
		envs = append(envs, corev1.EnvVar{Name: sandbox.SandboxPathPrependEnv, Value: value})
	}
	if value := cleanRuntimeProfilePathList(cfg.PathAppend, true, false); value != "" {
		envs = append(envs, corev1.EnvVar{Name: sandbox.SandboxPathAppendEnv, Value: value})
	}
	if value := cleanRuntimeProfilePathList(cfg.ExtraReadOnlyPaths, false, false); value != "" {
		envs = append(envs, corev1.EnvVar{Name: sandbox.SandboxExtraReadOnlyPathsEnv, Value: value})
	}
	if value := cleanRuntimeProfilePathList(cfg.ExtraWritablePaths, false, true); value != "" {
		envs = append(envs, corev1.EnvVar{Name: sandbox.SandboxExtraWritablePathsEnv, Value: value})
	}
	if value := runtimeProfileCommandSandboxEnvValue(cfg.Env); value != "" {
		envs = append(envs, corev1.EnvVar{Name: sandbox.SandboxExtraEnvEnv, Value: value})
	}
	return envs
}

func cleanRuntimeProfilePathList(paths []string, allowWorkspace, forbidSystemPaths bool) string {
	var cleaned []string
	seen := map[string]struct{}{}
	for _, path := range paths {
		path = cleanRuntimeProfileAbsolutePath(path)
		if path == "" || isForbiddenRuntimeProfileCommandSandboxPath(path, allowWorkspace, forbidSystemPaths) {
			continue
		}
		if _, ok := seen[path]; ok {
			continue
		}
		seen[path] = struct{}{}
		cleaned = append(cleaned, path)
	}
	return strings.Join(cleaned, string(os.PathListSeparator))
}

func cleanRuntimeProfileAbsolutePath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" || !strings.HasPrefix(path, "/") {
		return ""
	}
	return filepath.Clean(path)
}

func isForbiddenRuntimeProfileCommandSandboxPath(path string, allowWorkspace, forbidSystemPaths bool) bool {
	workspacePathAllowed := allowWorkspace && isAllowedRuntimeProfileWorkspacePath(path)
	if path == "" || path == "/" || path == "/tmp" || path == "/proc" || path == "/dev" ||
		strings.HasPrefix(path, "/tmp/") || strings.HasPrefix(path, "/proc/") || strings.HasPrefix(path, "/dev/") ||
		path == "/workspace" || (strings.HasPrefix(path, "/workspace/") && !workspacePathAllowed) {
		return true
	}
	for _, prefix := range []string{"/home", "/root", "/run", "/var/lib", "/var/log", "/var/run", "/var/spool", "/var/tmp"} {
		if path == prefix || strings.HasPrefix(path, prefix+"/") {
			return true
		}
	}
	if !forbidSystemPaths {
		return false
	}
	for _, prefix := range []string{"/etc", "/usr", "/bin", "/sbin", "/lib", "/lib64"} {
		if path == prefix || strings.HasPrefix(path, prefix+"/") {
			return true
		}
	}
	return false
}

func isAllowedRuntimeProfileWorkspacePath(path string) bool {
	if path == workspaceScratchPath+"/go/bin" {
		return true
	}
	return strings.HasPrefix(path, "/workspace/repo/") && strings.HasSuffix(path, "/node_modules/.bin")
}

func runtimeProfileCommandSandboxEnvValue(values map[string]string) string {
	if len(values) == 0 {
		return ""
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		key = strings.TrimSpace(key)
		if validRuntimeProfileCommandSandboxEnvKey(key) && !isSensitiveRuntimeProfileCommandSandboxEnvKey(key) {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	var lines []string
	for _, key := range keys {
		lines = append(lines, key+"="+sanitizeRuntimeProfileCommandSandboxEnvValue(values[key]))
	}
	return strings.Join(lines, "\n")
}

func sanitizeRuntimeProfileCommandSandboxEnvValue(value string) string {
	value = strings.TrimSpace(value)
	value = strings.ReplaceAll(value, "\x00", "")
	value = strings.ReplaceAll(value, "\r", " ")
	value = strings.ReplaceAll(value, "\n", " ")
	return value
}

func validRuntimeProfileCommandSandboxEnvKey(key string) bool {
	if key == "" {
		return false
	}
	for i, r := range key {
		switch {
		case r == '_':
		case r >= 'A' && r <= 'Z':
		case r >= 'a' && r <= 'z':
		case i > 0 && r >= '0' && r <= '9':
		default:
			return false
		}
	}
	first := key[0]
	return first == '_' || (first >= 'A' && first <= 'Z') || (first >= 'a' && first <= 'z')
}

func isSensitiveRuntimeProfileCommandSandboxEnvKey(key string) bool {
	upper := strings.ToUpper(strings.TrimSpace(key))
	for _, marker := range []string{"TOKEN", "SECRET", "PASSWORD", "PASSWD", "API_KEY", "PRIVATE_KEY", "ACCESS_KEY", "CREDENTIAL"} {
		if strings.Contains(upper, marker) {
			return true
		}
	}
	return upper == "KUBECONFIG"
}

func buildCommonPodSpec(run *platformv1alpha1.AgentRun, saName string, command []string, extraEnv []corev1.EnvVar, extraVolumeMounts []corev1.VolumeMount, extraVolumes []corev1.Volume) corev1.PodSpec {
	envs := []corev1.EnvVar{
		// PATH is intentionally NOT set here: the agent assembles it at startup
		// (toolkit bin first, the image's own PATH, injected fallback tools
		// last) so arbitrary user images keep their PATH customizations.
		{Name: "GOPATH", Value: workspaceScratchPath + "/go"},
		{Name: "GOMODCACHE", Value: workspaceScratchPath + "/go/pkg/mod"},
		{Name: "GOCACHE", Value: workspaceScratchPath + "/go-build"},
		{Name: "REPO_URL", Value: run.Spec.Repository.URL},
		{Name: "ADDITIONAL_REPO_URLS", Value: strings.Join(run.Spec.Repository.AdditionalRepos, ",")},
		{Name: "BASE_BRANCH", Value: firstNonEmpty(run.Spec.Repository.BaseBranch, "main")},
		{Name: "MODEL", Value: run.Spec.Model},
		{Name: "AI_PROVIDER", Value: effectiveProvider(run)},
		{Name: "MODEL_FALLBACKS", Value: modelFallbacksForRun(run)},
		{Name: "OPENAI_BASE_URL", Value: selectOpenAIBaseURL(run)},
		{Name: "OPENAI_API_MODE", Value: openAIApiModeFromAnnotations(run)},
		{Name: "S3_BUCKET", Value: os.Getenv("S3_BUCKET")},
		{Name: "S3_ENDPOINT", Value: os.Getenv("S3_ENDPOINT")},
		{Name: "S3_REGION", Value: os.Getenv("S3_REGION")},
		// Sensitive infra credentials come from the synced per-namespace
		// Secret instead of being inlined into the pod spec.
		workerInfraSecretEnv("AWS_ACCESS_KEY_ID", workerInfraKeyAWSAccessKeyID),
		workerInfraSecretEnv("AWS_SECRET_ACCESS_KEY", workerInfraKeyAWSSecretAccessKey),
		workerInfraSecretEnv("DATABASE_URL", workerInfraKeyDatabaseURL),
		{Name: "ENABLE_MEMORY", Value: os.Getenv("ENABLE_MEMORY")},
		// Worker feature toggles forwarded from the manager environment.
		{Name: "ENABLE_BROWSER_TOOLS", Value: os.Getenv("ENABLE_BROWSER_TOOLS")},
		{Name: "ENABLE_TERMINAL_TOOL", Value: os.Getenv("ENABLE_TERMINAL_TOOL")},
		{Name: "ENABLE_CRITIC_VERIFIER", Value: os.Getenv("ENABLE_CRITIC_VERIFIER")},
		{Name: "OTEL_EXPORTER_OTLP_ENDPOINT", Value: os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")},
		{Name: "POD_NAMESPACE", ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.namespace"}}},
	}
	envs = append(envs, runtimeScopeEnvs(run)...)
	envs = append(envs, commandSandboxModeEnvs(run)...)
	envs = append(envs, commandSandboxConfigEnvs(run)...)
	envs = maybeAppendEnv(envs, githubTokenEnv(run))
	envs = append(envs, slackTokensEnvs(run)...)
	envs = maybeAppendEnv(envs, openAIAuthModeEnv(run))
	envs = append(envs, providerAPIKeyEnvs(run)...)
	envs = append(envs, openAIOAuthEnvs(run)...)
	envs = append(envs, providerOAuthEnvs(run)...)
	envs = append(envs, modeConstraintEnvs(run)...)
	envs = append(envs, gitIdentityEnvs(run)...)
	envs = append(envs, extraEnv...)

	volumeMounts := []corev1.VolumeMount{
		{Name: "gratefulagents-toolkit", MountPath: "/opt/gratefulagents", SubPath: "gratefulagents", ReadOnly: true},
		{Name: "workspace", MountPath: "/workspace"},
		{Name: workspaceScratchVolumeName, MountPath: workspaceScratchPath},
	}
	volumeMounts = maybeAppendVolumeMount(volumeMounts, openAIOAuthVolumeMount(run))
	volumeMounts = append(volumeMounts, providerOAuthVolumeMounts(run)...)
	volumeMounts = append(volumeMounts, extraVolumeMounts...)
	volumes := []corev1.Volume{
		{Name: "gratefulagents-toolkit", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
		{Name: "workspace", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
		{Name: workspaceScratchVolumeName, VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
	}
	volumes = maybeAppendVolume(volumes, openAIOAuthVolume(run))
	volumes = append(volumes, providerOAuthVolumes(run)...)
	volumes = append(volumes, extraVolumes...)

	if instructionsRef := instructionsConfigMapName(run); instructionsRef != "" {
		optional := true
		volumes = append(volumes, corev1.Volume{
			Name:         "operator-instructions",
			VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: instructionsRef}, Optional: &optional}},
		})
		volumeMounts = append(volumeMounts, corev1.VolumeMount{Name: "operator-instructions", MountPath: "/etc/operator-instructions", ReadOnly: true})
	}

	podSpec := corev1.PodSpec{
		RestartPolicy:      corev1.RestartPolicyNever,
		ServiceAccountName: saName,
		// Give the agent time to publish a final encrypted workspace checkpoint
		// to object storage before pause/wake pod deletion completes.
		TerminationGracePeriodSeconds: int64Ptr(60),
		ShareProcessNamespace:         boolPtr(false),
		InitContainers: []corev1.Container{{
			Name:            "inject-toolkit",
			Image:           firstNonEmpty(os.Getenv("INJECTOR_IMAGE"), "gratefulagents-injector:latest"),
			ImagePullPolicy: corev1.PullIfNotPresent,
			VolumeMounts:    []corev1.VolumeMount{{Name: "gratefulagents-toolkit", MountPath: "/shared"}},
		}},
		Containers: []corev1.Container{{
			Name:            "worker",
			Image:           firstNonEmpty(run.Spec.Image, os.Getenv("WORKER_IMAGE"), "worker:latest"),
			ImagePullPolicy: corev1.PullIfNotPresent,
			Command:         command,
			Env:             envs,
			VolumeMounts:    volumeMounts,
			// Container-level only: the inject-toolkit init container must
			// stay root so its cp -a preserves toolkit file ownership.
			SecurityContext: &corev1.SecurityContext{
				RunAsUser:    int64Ptr(workerRunAsUID),
				RunAsGroup:   int64Ptr(workerRunAsGID),
				RunAsNonRoot: boolPtr(true),
			},
		}},
		Volumes: volumes,
	}
	ensureWorkspaceScratchSandboxConfig(&podSpec.Containers[0])
	return podSpec
}

func createPlanPod(ctx context.Context, c client.Client, run *platformv1alpha1.AgentRun) (string, error) {
	podName := sanitizeDNSLabel("run", run.Name)
	saName := sanitizeDNSLabel("run", run.Name)
	if err := ensureRunRBAC(ctx, c, run, saName); err != nil {
		return "", err
	}

	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Name:      podName,
		Namespace: run.Namespace,
		Labels: map[string]string{
			"app.kubernetes.io/name":                    "gratefulagents",
			"app.kubernetes.io/component":               "agent-runner",
			"platform.gratefulagents.dev/owner-run":     run.Name,
			"platform.gratefulagents.dev/owner-run-uid": string(run.UID),
		},
		OwnerReferences: []metav1.OwnerReference{{
			APIVersion:         platformv1alpha1.GroupVersion.String(),
			Kind:               "AgentRun",
			Name:               run.Name,
			UID:                run.UID,
			Controller:         boolPtr(true),
			BlockOwnerDeletion: boolPtr(true),
		}},
	}}

	envs := runExecutionEnvVars(run)
	// Secret-backed env declared by attached MCP servers (incl. skill-required ones).
	envs = append(envs, resolveMCPServerSecretEnvs(ctx, c, run)...)
	pod.Spec = buildCommonPodSpec(ctxlessRun(run), saName, []string{"/opt/gratefulagents/bin/agent", "run"}, envs, nil, nil)
	if err := c.Create(ctx, pod); err != nil {
		if apierrors.IsAlreadyExists(err) {
			existing := &corev1.Pod{}
			key := client.ObjectKey{Name: podName, Namespace: run.Namespace}
			if getErr := c.Get(ctx, key, existing); getErr != nil {
				if apierrors.IsNotFound(getErr) {
					return "", errRunPodReplaced
				}
				return "", fmt.Errorf("getting existing run pod: %w", getErr)
			}
			if shouldReplaceExistingRunPod(run, existing) {
				if existing.DeletionTimestamp == nil {
					if delErr := c.Delete(ctx, existing); delErr != nil && !apierrors.IsNotFound(delErr) {
						return "", fmt.Errorf("deleting stale run pod: %w", delErr)
					}
				}
				return "", errRunPodReplaced
			}
			return podName, nil
		}
		return "", fmt.Errorf("creating run pod: %w", err)
	}
	return podName, nil
}

func shouldReplaceExistingRunPod(run *platformv1alpha1.AgentRun, pod *corev1.Pod) bool {
	if pod == nil {
		return false
	}
	if pod.DeletionTimestamp != nil {
		return true
	}
	switch pod.Status.Phase {
	case corev1.PodSucceeded, corev1.PodFailed:
		return true
	}
	// Pod is still running — don't replace it.
	// Mode transitions are handled in-process via Postgres polling, not pod replacement.
	return false
}

func triggerExternalIdentifier(run *platformv1alpha1.AgentRun) string {
	if run == nil || run.Spec.Trigger.ExternalRef == nil {
		return ""
	}
	return strings.TrimSpace(run.Spec.Trigger.ExternalRef.Identifier)
}

func ctxlessRun(run *platformv1alpha1.AgentRun) *platformv1alpha1.AgentRun {
	return run
}

func runtimeScopeEnvs(run *platformv1alpha1.AgentRun) []corev1.EnvVar {
	if run == nil {
		return nil
	}
	currentNamespace := strings.TrimSpace(run.Namespace)
	currentName := strings.TrimSpace(run.Name)
	currentUID := strings.TrimSpace(string(run.UID))

	parentNamespace, parentName, parentUID := parentIdentityForRun(run)
	envs := []corev1.EnvVar{
		{Name: "AGENTRUN_CURRENT_NAMESPACE", Value: currentNamespace},
		{Name: "AGENTRUN_CURRENT_NAME", Value: currentName},
		{Name: "AGENTRUN_CURRENT_UID", Value: currentUID},
		{Name: "AGENTRUN_PARENT_NAMESPACE", Value: parentNamespace},
		{Name: "AGENTRUN_PARENT_NAME", Value: parentName},
		{Name: "AGENTRUN_PARENT_UID", Value: parentUID},
		{Name: "RUN_NAMESPACE", Value: parentNamespace},
		{Name: "RUN_NAME", Value: parentName},
		{Name: "RUN_UID", Value: parentUID},
	}
	if supervisedNamespace, supervisedName, supervisedUID := supervisedIdentityForRun(run); supervisedName != "" {
		envs = append(envs,
			corev1.EnvVar{Name: "AGENTRUN_SUPERVISED_NAMESPACE", Value: supervisedNamespace},
			corev1.EnvVar{Name: "AGENTRUN_SUPERVISED_NAME", Value: supervisedName},
			corev1.EnvVar{Name: "AGENTRUN_SUPERVISED_UID", Value: supervisedUID},
		)
	}
	if maintainedNamespace, maintainedName := maintainedRepositoryForRun(run); maintainedName != "" {
		envs = append(envs,
			corev1.EnvVar{Name: "AGENTRUN_MAINTAINED_REPOSITORY_NAME", Value: maintainedName},
			corev1.EnvVar{Name: "AGENTRUN_MAINTAINED_REPOSITORY_NAMESPACE", Value: maintainedNamespace},
		)
	}
	return envs
}

// modeConstraintEnvs produces env vars from the mode snapshot's constraints.
func modeConstraintEnvs(run *platformv1alpha1.AgentRun) []corev1.EnvVar {
	if run == nil || run.Status.ModeSnapshot == nil {
		return nil
	}
	snap := run.Status.ModeSnapshot
	var envs []corev1.EnvVar

	if snap.Constraints != nil && snap.Constraints.MaxTurns > 0 {
		envs = append(envs, corev1.EnvVar{Name: "MODE_MAX_TURNS", Value: strconv.Itoa(int(snap.Constraints.MaxTurns))})
	}
	if snap.Constraints != nil && snap.Constraints.SubAgentMaxTurns > 0 {
		envs = append(envs, corev1.EnvVar{Name: "MODE_SUBAGENT_MAX_TURNS", Value: strconv.Itoa(int(snap.Constraints.SubAgentMaxTurns))})
	}
	if snap.Constraints != nil && snap.Constraints.MaxConcurrentSubAgents > 0 {
		envs = append(envs, corev1.EnvVar{Name: "MODE_MAX_CONCURRENT_CHILDREN", Value: strconv.Itoa(int(snap.Constraints.MaxConcurrentSubAgents))})
	}
	if run.Status.ModeName != "" {
		envs = append(envs, corev1.EnvVar{Name: "MODE_NAME", Value: run.Status.ModeName})
	}

	return envs
}

func parentIdentityForRun(run *platformv1alpha1.AgentRun) (namespace, name, uid string) {
	if run == nil {
		return "", "", ""
	}
	currentNamespace := strings.TrimSpace(run.Namespace)
	currentName := strings.TrimSpace(run.Name)
	currentUID := strings.TrimSpace(string(run.UID))

	namespace = currentNamespace
	name = currentName
	uid = currentUID

	if ownerRef := parentAgentRunOwnerRef(run); ownerRef != nil {
		if ownerName := strings.TrimSpace(ownerRef.Name); ownerName != "" {
			name = ownerName
		}
		if ownerUID := strings.TrimSpace(string(ownerRef.UID)); ownerUID != "" {
			uid = ownerUID
		}
	} else if run.Labels != nil {
		if labeledParent := strings.TrimSpace(run.Labels[teamParentLabelName]); labeledParent != "" {
			name = labeledParent
		}
	}

	if namespace == "" {
		namespace = currentNamespace
	}
	if name == "" {
		name = currentName
	}
	if uid == "" {
		uid = currentUID
	}
	return namespace, name, uid
}

func parentAgentRunOwnerRef(run *platformv1alpha1.AgentRun) *metav1.OwnerReference {
	if run == nil {
		return nil
	}
	for i := range run.OwnerReferences {
		ref := &run.OwnerReferences[i]
		if ref.APIVersion == platformv1alpha1.GroupVersion.String() && ref.Kind == "AgentRun" && strings.TrimSpace(ref.Name) != "" {
			return ref
		}
	}
	return nil
}

func verifiedSupervisedRunName(ctx context.Context, c client.Client, run *platformv1alpha1.AgentRun) (string, error) {
	namespace, name, _ := supervisedIdentityForRun(run)
	if name == "" {
		return "", nil
	}
	target := &platformv1alpha1.AgentRun{}
	if err := c.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, target); err != nil {
		if apierrors.IsNotFound(err) {
			return "", nil
		}
		return "", err
	}
	if !metav1.IsControlledBy(run, target) {
		return "", nil
	}
	return name, nil
}

// supervisedIdentityForRun exposes a target identity only to controller-created
// standing overseer runs. Both labels and the AgentRun owner reference must
// agree, so an arbitrary run cannot gain cross-session visibility by setting a
// single label.
func supervisedIdentityForRun(run *platformv1alpha1.AgentRun) (namespace, name, uid string) {
	if run == nil || run.Labels[orchestration.StandingRunRoleLabel] != orchestration.StandingRunRoleOverseer {
		return "", "", ""
	}
	name = strings.TrimSpace(run.Labels[orchestration.SupervisedRunLabel])
	if name == "" {
		return "", "", ""
	}
	owner := parentAgentRunOwnerRef(run)
	if owner == nil || strings.TrimSpace(owner.Name) != name {
		return "", "", ""
	}
	return strings.TrimSpace(run.Namespace), name, strings.TrimSpace(string(owner.UID))
}

func maintainedRepositoryForRun(run *platformv1alpha1.AgentRun) (namespace, name string) {
	if run == nil || run.Labels[orchestration.StandingRunRoleLabel] != orchestration.StandingRunRoleMaintainer {
		return "", ""
	}
	name = strings.TrimSpace(run.Labels[orchestration.SupervisedRunLabel])
	if name == "" {
		return "", ""
	}
	for i := range run.OwnerReferences {
		owner := &run.OwnerReferences[i]
		if owner.APIVersion == triggersv1alpha1.GroupVersion.String() && owner.Kind == "GitHubRepository" && owner.Controller != nil && *owner.Controller && strings.TrimSpace(owner.Name) == name {
			return strings.TrimSpace(run.Namespace), name
		}
	}
	return "", ""
}

func verifiedMaintainedRepositoryName(ctx context.Context, c client.Client, run *platformv1alpha1.AgentRun) (string, error) {
	namespace, name := maintainedRepositoryForRun(run)
	if name == "" {
		return "", nil
	}
	repository := &triggersv1alpha1.GitHubRepository{}
	if err := c.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, repository); err != nil {
		if apierrors.IsNotFound(err) {
			return "", nil
		}
		return "", err
	}
	if !metav1.IsControlledBy(run, repository) {
		return "", nil
	}
	return name, nil
}

func isDelegatedChildRun(run *platformv1alpha1.AgentRun) bool {
	if run == nil {
		return false
	}
	if strings.TrimSpace(run.Labels[teamParentLabelName]) != "" {
		return true
	}
	return parentAgentRunOwnerRef(run) != nil
}

func isAutonomousChildRun(run *platformv1alpha1.AgentRun) bool {
	if run == nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(run.Annotations[childAutonomousAnnotation]), "true")
}
