package triggers

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const slackAgentKind = "SlackAgent"

// SlackAgentReconciler provisions, per SlackAgent, a long-lived connector
// Deployment (plus ServiceAccount + RBAC) that opens an outbound Socket Mode
// WebSocket and runs the agent loop in-process. The operator never holds the
// socket; it only reconciles the workload.
type SlackAgentReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=triggers.gratefulagents.dev,resources=slackagents,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=triggers.gratefulagents.dev,resources=slackagents/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=serviceaccounts,verbs=get;list;watch;create
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=roles;rolebindings,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;delete

func (r *SlackAgentReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	agent := &triggersv1alpha1.SlackAgent{}
	if err := r.Get(ctx, req.NamespacedName, agent); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if agent.UsesWorkspace() {
		return r.reconcileWorkspaceMember(ctx, req, agent)
	}

	tokensErr := r.validateTokensSecret(ctx, agent)
	if tokensErr != nil {
		if statusErr := r.updateStatus(ctx, req.NamespacedName, func(fresh *triggersv1alpha1.SlackAgent) {
			fresh.Status.LastError = tokensErr.Error()
			setSlackAgentCondition(fresh, triggersv1alpha1.ConditionSlackAgentTokenValid, metav1.ConditionFalse, "TokensMissing", tokensErr.Error())
			setSlackAgentCondition(fresh, triggersv1alpha1.ConditionSlackAgentReady, metav1.ConditionFalse, "TokensMissing", "Slack tokens are not available")
		}); statusErr != nil && !apierrors.IsNotFound(statusErr) {
			return ctrl.Result{}, fmt.Errorf("updating SlackAgent status: %w", statusErr)
		}
		// Missing tokens is a user-config problem; requeue is driven by Secret
		// changes, so don't return an error that would hot-loop.
		return ctrl.Result{}, nil
	}

	saName := slackResourceName(agent.Name)
	if err := r.ensureRBAC(ctx, agent, saName); err != nil {
		return ctrl.Result{}, fmt.Errorf("ensuring connector RBAC: %w", err)
	}

	if err := r.ensureInfraSecret(ctx, agent.Namespace); err != nil {
		return ctrl.Result{}, fmt.Errorf("syncing connector infra secret: %w", err)
	}

	deploymentName, err := r.ensureConnectorDeployment(ctx, agent, saName)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("ensuring connector Deployment: %w", err)
	}

	ready, replicas := r.deploymentAvailability(ctx, agent.Namespace, deploymentName)
	if statusErr := r.updateStatus(ctx, req.NamespacedName, func(fresh *triggersv1alpha1.SlackAgent) {
		fresh.Status.DeploymentName = deploymentName
		fresh.Status.LastError = ""
		setSlackAgentCondition(fresh, triggersv1alpha1.ConditionSlackAgentTokenValid, metav1.ConditionTrue, "TokensPresent", "Slack tokens are available")
		switch {
		case fresh.Spec.Suspend:
			setSlackAgentCondition(fresh, triggersv1alpha1.ConditionSlackAgentReady, metav1.ConditionFalse, "Suspended", "SlackAgent is suspended")
		case ready:
			setSlackAgentCondition(fresh, triggersv1alpha1.ConditionSlackAgentReady, metav1.ConditionTrue, "ConnectorReady", "Connector Deployment is available")
		default:
			setSlackAgentCondition(fresh, triggersv1alpha1.ConditionSlackAgentReady, metav1.ConditionFalse, "ConnectorPending", fmt.Sprintf("Connector has %d ready replicas", replicas))
		}
	}); statusErr != nil && !apierrors.IsNotFound(statusErr) {
		return ctrl.Result{}, fmt.Errorf("updating SlackAgent status: %w", statusErr)
	}

	log.V(1).Info("reconciled SlackAgent", "deployment", deploymentName, "ready", ready)
	return ctrl.Result{}, nil
}

// reconcileWorkspaceMember handles a SlackAgent bound to a shared
// SlackWorkspace: no dedicated Deployment is provisioned (the workspace's
// connector serves it), any stale dedicated Deployment from a mode switch is
// removed, and readiness mirrors the workspace's.
func (r *SlackAgentReconciler) reconcileWorkspaceMember(ctx context.Context, req ctrl.Request, agent *triggersv1alpha1.SlackAgent) (ctrl.Result, error) {
	// A mode switch (dedicated → workspace) leaves the old connector running;
	// delete it so two connectors never serve the same owner.
	staleName := slackResourceName(agent.Name)
	stale := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: staleName, Namespace: agent.Namespace}}
	if err := r.Delete(ctx, stale); err != nil && !apierrors.IsNotFound(err) {
		return ctrl.Result{}, fmt.Errorf("deleting stale dedicated connector Deployment: %w", err)
	}

	wsNamespace, wsName := agent.ResolvedWorkspaceRef()
	ws := &triggersv1alpha1.SlackWorkspace{}
	wsErr := r.Get(ctx, types.NamespacedName{Namespace: wsNamespace, Name: wsName}, ws)

	if statusErr := r.updateStatus(ctx, req.NamespacedName, func(fresh *triggersv1alpha1.SlackAgent) {
		fresh.Status.DeploymentName = ""
		setSlackAgentCondition(fresh, triggersv1alpha1.ConditionSlackAgentTokenValid, metav1.ConditionTrue, "DelegatedToWorkspace", "Tokens are managed by the referenced SlackWorkspace")
		switch {
		case wsErr != nil:
			fresh.Status.LastError = fmt.Sprintf("SlackWorkspace %s/%s not found", wsNamespace, wsName)
			setSlackAgentCondition(fresh, triggersv1alpha1.ConditionSlackAgentReady, metav1.ConditionFalse, "WorkspaceMissing", fresh.Status.LastError)
		case ws.Spec.Suspend:
			fresh.Status.LastError = ""
			setSlackAgentCondition(fresh, triggersv1alpha1.ConditionSlackAgentReady, metav1.ConditionFalse, "WorkspaceSuspended", "Referenced SlackWorkspace is suspended")
		case meta.IsStatusConditionTrue(ws.Status.Conditions, triggersv1alpha1.ConditionSlackWorkspaceReady):
			fresh.Status.LastError = ""
			fresh.Status.TeamID = ws.Status.TeamID
			fresh.Status.BotUserID = ws.Status.BotUserID
			setSlackAgentCondition(fresh, triggersv1alpha1.ConditionSlackAgentReady, metav1.ConditionTrue, "WorkspaceReady", "Served by the referenced SlackWorkspace connector")
		default:
			fresh.Status.LastError = ""
			setSlackAgentCondition(fresh, triggersv1alpha1.ConditionSlackAgentReady, metav1.ConditionFalse, "WorkspacePending", "Referenced SlackWorkspace connector is not ready")
		}
	}); statusErr != nil && !apierrors.IsNotFound(statusErr) {
		return ctrl.Result{}, fmt.Errorf("updating SlackAgent status: %w", statusErr)
	}
	return ctrl.Result{}, nil
}

// validateTokensSecret loads the Slack tokens Secret and validates that the
// minimum set (bot + app for Socket Mode) is present. Token values are consumed
// by the connector pod via secretKeyRef, so the controller only checks presence;
// the connector performs live auth.test.
func (r *SlackAgentReconciler) validateTokensSecret(ctx context.Context, agent *triggersv1alpha1.SlackAgent) error {
	secretName := strings.TrimSpace(agent.Spec.TokensSecret)
	if secretName == "" {
		return fmt.Errorf("spec.tokensSecret is required")
	}
	app, _ := ReadSecretValue(ctx, r.Client, agent.Namespace, secretName, triggersv1alpha1.SlackAppTokenKey)
	bot, _ := ReadSecretValue(ctx, r.Client, agent.Namespace, secretName, triggersv1alpha1.SlackBotTokenKey)
	if strings.TrimSpace(app) == "" {
		return fmt.Errorf("secret %q is missing key %q (app-level token for Socket Mode)", secretName, triggersv1alpha1.SlackAppTokenKey)
	}
	if strings.TrimSpace(bot) == "" {
		return fmt.Errorf("secret %q is missing key %q (bot token)", secretName, triggersv1alpha1.SlackBotTokenKey)
	}
	return nil
}

func (r *SlackAgentReconciler) ownerRef(agent *triggersv1alpha1.SlackAgent) metav1.OwnerReference {
	return metav1.OwnerReference{
		APIVersion:         triggersv1alpha1.GroupVersion.String(),
		Kind:               slackAgentKind,
		Name:               agent.Name,
		UID:                agent.UID,
		Controller:         new(true),
		BlockOwnerDeletion: new(true),
	}
}

// ensureRBAC provisions the connector's ServiceAccount + Role + RoleBinding so
// the connector pod can create and watch child AgentRuns and read the resources
// it needs, scoped to the agent's namespace.
func (r *SlackAgentReconciler) ensureRBAC(ctx context.Context, agent *triggersv1alpha1.SlackAgent, saName string) error {
	ownerRef := r.ownerRef(agent)

	sa := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: saName, Namespace: agent.Namespace, OwnerReferences: []metav1.OwnerReference{ownerRef}}}
	if err := r.Create(ctx, sa); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("creating ServiceAccount %s: %w", saName, err)
	}

	roleName := saName + "-role"
	role := &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{Name: roleName, Namespace: agent.Namespace, OwnerReferences: []metav1.OwnerReference{ownerRef}},
		Rules:      slackConnectorRBACRules(),
	}
	if err := r.Create(ctx, role); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("creating Role %s: %w", roleName, err)
		}
		existing := &rbacv1.Role{}
		if getErr := r.Get(ctx, client.ObjectKeyFromObject(role), existing); getErr != nil {
			return fmt.Errorf("getting existing Role %s: %w", roleName, getErr)
		}
		existing.Rules = role.Rules
		if updateErr := r.Update(ctx, existing); updateErr != nil {
			return fmt.Errorf("updating Role %s: %w", roleName, updateErr)
		}
	}

	rbName := saName + "-binding"
	rb := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: rbName, Namespace: agent.Namespace, OwnerReferences: []metav1.OwnerReference{ownerRef}},
		RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: roleName},
		Subjects:   []rbacv1.Subject{{Kind: rbacv1.ServiceAccountKind, Name: saName, Namespace: agent.Namespace}},
	}
	if err := r.Create(ctx, rb); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("creating RoleBinding %s: %w", rbName, err)
	}
	return nil
}

// ensureInfraSecret syncs the operator's infra credentials into the agent
// namespace so the connector reads thread/draft state and its runs reach
// object storage.
func (r *SlackAgentReconciler) ensureInfraSecret(ctx context.Context, namespace string) error {
	return ensureSlackWorkerInfraSecret(ctx, r.Client, namespace)
}

func slackConnectorRBACRules() []rbacv1.PolicyRule {
	return []rbacv1.PolicyRule{
		{
			APIGroups: []string{"triggers.gratefulagents.dev"},
			Resources: []string{"slackagents"},
			Verbs:     []string{"get", "list", "watch"},
		},
		{
			APIGroups: []string{"platform.gratefulagents.dev"},
			Resources: []string{"agentruns"},
			Verbs:     []string{"create", "get", "list", "watch", "patch"},
		},
		{
			APIGroups: []string{"platform.gratefulagents.dev"},
			Resources: []string{"agentruns/status"},
			Verbs:     []string{"get", "patch", "update"},
		},
		{
			APIGroups: []string{""},
			Resources: []string{"secrets", "configmaps"},
			Verbs:     []string{"get", "list", "watch"},
		},
		{
			APIGroups: []string{""},
			Resources: []string{"events"},
			Verbs:     []string{"create"},
		},
	}
}

// ensureConnectorDeployment creates or updates the connector Deployment running
// `agent slack`. Replicas drop to zero while the SlackAgent is suspended.
func (r *SlackAgentReconciler) ensureConnectorDeployment(ctx context.Context, agent *triggersv1alpha1.SlackAgent, saName string) (string, error) {
	name := slackResourceName(agent.Name)
	replicas := int32(1)
	if agent.Spec.Suspend {
		replicas = 0
	}
	desired := buildSlackConnectorDeployment(
		name, agent.Namespace, "slack-connector",
		"triggers.gratefulagents.dev/slack-agent", agent.Name,
		r.ownerRef(agent), replicas, r.connectorPodSpec(agent, saName),
	)
	if err := applySlackConnectorDeployment(ctx, r.Client, desired); err != nil {
		return "", err
	}
	return name, nil
}

func (r *SlackAgentReconciler) connectorPodSpec(agent *triggersv1alpha1.SlackAgent, saName string) corev1.PodSpec {
	secretName := strings.TrimSpace(agent.Spec.TokensSecret)
	env := []corev1.EnvVar{
		{Name: "SLACK_AGENT_NAME", Value: agent.Name},
		{Name: "POD_NAMESPACE", ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.namespace"}}},
		{Name: "SLACK_HEALTH_ADDR", Value: ":8080"},
		slackSecretEnv("SLACK_BOT_TOKEN", secretName, triggersv1alpha1.SlackBotTokenKey),
		slackSecretEnv("SLACK_USER_TOKEN", secretName, triggersv1alpha1.SlackUserTokenKey),
		slackSecretEnv("SLACK_APP_TOKEN", secretName, triggersv1alpha1.SlackAppTokenKey),
		// Connector reaches Postgres for thread/draft state via the per-namespace
		// infra secret, mirroring worker pods.
		workerInfraSecretEnvRef("DATABASE_URL", "database-url"),
	}
	if uid := strings.TrimSpace(agent.Spec.SlackUserID); uid != "" {
		env = append(env, corev1.EnvVar{Name: "SLACK_USER_ID", Value: uid})
	}
	if teamID := strings.TrimSpace(agent.Annotations[projectSlackTeamIDAnnotation]); teamID != "" {
		env = append(env, corev1.EnvVar{Name: "SLACK_TEAM_ID", Value: teamID})
	}
	if commanders := strings.Join(agent.Spec.Commanders, ","); commanders != "" {
		env = append(env, corev1.EnvVar{Name: "SLACK_COMMANDERS", Value: commanders})
	}
	if m := agent.Spec.SessionIdleMinutes; m != nil && *m > 0 {
		env = append(env, corev1.EnvVar{Name: "SLACK_SESSION_IDLE_MINUTES", Value: strconv.FormatInt(int64(*m), 10)})
	}

	return slackConnectorPodSpec(saName, agent.Spec.Image, env)
}

func (r *SlackAgentReconciler) deploymentAvailability(ctx context.Context, namespace, name string) (bool, int32) {
	return slackDeploymentAvailability(ctx, r.Client, namespace, name)
}

func (r *SlackAgentReconciler) updateStatus(ctx context.Context, key types.NamespacedName, mutate func(*triggersv1alpha1.SlackAgent)) error {
	fresh := &triggersv1alpha1.SlackAgent{}
	if err := r.Get(ctx, key, fresh); err != nil {
		return err
	}
	mutate(fresh)
	return r.Status().Update(ctx, fresh)
}

func setSlackAgentCondition(agent *triggersv1alpha1.SlackAgent, condType string, status metav1.ConditionStatus, reason, message string) {
	meta.SetStatusCondition(&agent.Status.Conditions, metav1.Condition{
		Type:               condType,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: agent.Generation,
	})
}

func (r *SlackAgentReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&triggersv1alpha1.SlackAgent{}).
		Owns(&appsv1.Deployment{}).
		Watches(&triggersv1alpha1.SlackWorkspace{}, handler.EnqueueRequestsFromMapFunc(r.mapWorkspaceToMembers)).
		Named("slackagent").
		WithOptions(controller.Options{MaxConcurrentReconciles: 1}).
		Complete(r)
}

// mapWorkspaceToMembers re-reconciles every member SlackAgent when its
// workspace changes so member readiness tracks the shared connector.
func (r *SlackAgentReconciler) mapWorkspaceToMembers(ctx context.Context, obj client.Object) []reconcile.Request {
	ws, ok := obj.(*triggersv1alpha1.SlackWorkspace)
	if !ok {
		return nil
	}
	agents := &triggersv1alpha1.SlackAgentList{}
	if err := r.List(ctx, agents); err != nil {
		return nil
	}
	var reqs []reconcile.Request
	for _, agent := range agents.Items {
		ns, name := agent.ResolvedWorkspaceRef()
		if ns == ws.Namespace && name == ws.Name {
			reqs = append(reqs, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: agent.Namespace, Name: agent.Name}})
		}
	}
	return reqs
}
