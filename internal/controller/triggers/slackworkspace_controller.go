package triggers

import (
	"context"
	"fmt"
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

const slackWorkspaceKind = "SlackWorkspace"

// SlackWorkspaceReconciler provisions, per SlackWorkspace, ONE shared connector
// Deployment serving every member SlackAgent (spec.workspaceRef). Socket Mode
// load-balances an app's events across its open sockets, so a shared app must
// be consumed by a single process; the connector routes events to members by
// the sending Slack user. For each member namespace the reconciler grants the
// connector the standard per-namespace connector RBAC and syncs the shared bot
// token so member child runs can use Slack read tools.
type SlackWorkspaceReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=triggers.gratefulagents.dev,resources=slackworkspaces,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=triggers.gratefulagents.dev,resources=slackworkspaces/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=clusterroles;clusterrolebindings,verbs=get;list;watch;create;update;patch

func (r *SlackWorkspaceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	ws := &triggersv1alpha1.SlackWorkspace{}
	if err := r.Get(ctx, req.NamespacedName, ws); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if tokensErr := r.validateTokensSecret(ctx, ws); tokensErr != nil {
		if statusErr := r.updateStatus(ctx, req.NamespacedName, func(fresh *triggersv1alpha1.SlackWorkspace) {
			fresh.Status.LastError = tokensErr.Error()
			setSlackWorkspaceCondition(fresh, triggersv1alpha1.ConditionSlackWorkspaceTokenValid, metav1.ConditionFalse, "TokensMissing", tokensErr.Error())
			setSlackWorkspaceCondition(fresh, triggersv1alpha1.ConditionSlackWorkspaceReady, metav1.ConditionFalse, "TokensMissing", "Slack tokens are not available")
		}); statusErr != nil && !apierrors.IsNotFound(statusErr) {
			return ctrl.Result{}, fmt.Errorf("updating SlackWorkspace status: %w", statusErr)
		}
		return ctrl.Result{}, nil
	}

	saName := slackWorkspaceResourceName(ws.Name)
	if err := r.ensureRBAC(ctx, ws, saName); err != nil {
		return ctrl.Result{}, fmt.Errorf("ensuring workspace connector RBAC: %w", err)
	}
	if err := r.ensureWorkspaceInfraSecret(ctx, ws.Namespace); err != nil {
		return ctrl.Result{}, fmt.Errorf("syncing connector infra secret: %w", err)
	}

	members, err := r.listMembers(ctx, ws)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("listing workspace members: %w", err)
	}
	if err := r.ensureMemberNamespaces(ctx, ws, saName, members); err != nil {
		return ctrl.Result{}, fmt.Errorf("provisioning member namespaces: %w", err)
	}

	deploymentName, err := r.ensureConnectorDeployment(ctx, ws, saName)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("ensuring workspace connector Deployment: %w", err)
	}

	ready, replicas := r.deploymentAvailability(ctx, ws.Namespace, deploymentName)
	if statusErr := r.updateStatus(ctx, req.NamespacedName, func(fresh *triggersv1alpha1.SlackWorkspace) {
		fresh.Status.DeploymentName = deploymentName
		fresh.Status.MemberCount = int32(len(members)) //nolint:gosec // member count is tiny
		fresh.Status.LastError = ""
		setSlackWorkspaceCondition(fresh, triggersv1alpha1.ConditionSlackWorkspaceTokenValid, metav1.ConditionTrue, "TokensPresent", "Slack tokens are available")
		switch {
		case fresh.Spec.Suspend:
			setSlackWorkspaceCondition(fresh, triggersv1alpha1.ConditionSlackWorkspaceReady, metav1.ConditionFalse, "Suspended", "SlackWorkspace is suspended")
		case ready:
			setSlackWorkspaceCondition(fresh, triggersv1alpha1.ConditionSlackWorkspaceReady, metav1.ConditionTrue, "ConnectorReady", "Connector Deployment is available")
		default:
			setSlackWorkspaceCondition(fresh, triggersv1alpha1.ConditionSlackWorkspaceReady, metav1.ConditionFalse, "ConnectorPending", fmt.Sprintf("Connector has %d ready replicas", replicas))
		}
	}); statusErr != nil && !apierrors.IsNotFound(statusErr) {
		return ctrl.Result{}, fmt.Errorf("updating SlackWorkspace status: %w", statusErr)
	}

	log.V(1).Info("reconciled SlackWorkspace", "deployment", deploymentName, "members", len(members), "ready", ready)
	return ctrl.Result{}, nil
}

// validateTokensSecret presence-checks the shared app + bot tokens; the
// connector performs live auth.test.
func (r *SlackWorkspaceReconciler) validateTokensSecret(ctx context.Context, ws *triggersv1alpha1.SlackWorkspace) error {
	secretName := strings.TrimSpace(ws.Spec.TokensSecret)
	if secretName == "" {
		return fmt.Errorf("spec.tokensSecret is required")
	}
	app, _ := ReadSecretValue(ctx, r.Client, ws.Namespace, secretName, triggersv1alpha1.SlackAppTokenKey)
	bot, _ := ReadSecretValue(ctx, r.Client, ws.Namespace, secretName, triggersv1alpha1.SlackBotTokenKey)
	if strings.TrimSpace(app) == "" {
		return fmt.Errorf("secret %q is missing key %q (app-level token for Socket Mode)", secretName, triggersv1alpha1.SlackAppTokenKey)
	}
	if strings.TrimSpace(bot) == "" {
		return fmt.Errorf("secret %q is missing key %q (bot token)", secretName, triggersv1alpha1.SlackBotTokenKey)
	}
	return nil
}

func (r *SlackWorkspaceReconciler) ownerRef(ws *triggersv1alpha1.SlackWorkspace) metav1.OwnerReference {
	return metav1.OwnerReference{
		APIVersion:         triggersv1alpha1.GroupVersion.String(),
		Kind:               slackWorkspaceKind,
		Name:               ws.Name,
		UID:                ws.UID,
		Controller:         new(true),
		BlockOwnerDeletion: new(true),
	}
}

// ensureRBAC provisions the connector's ServiceAccount + namespace Role/Binding
// (workspace namespace) and a ClusterRole/ClusterRoleBinding so the connector
// can discover member SlackAgents across namespaces. Member-namespace write
// access is granted per namespace by ensureMemberNamespaces.
func (r *SlackWorkspaceReconciler) ensureRBAC(ctx context.Context, ws *triggersv1alpha1.SlackWorkspace, saName string) error {
	ownerRef := r.ownerRef(ws)

	sa := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: saName, Namespace: ws.Namespace, OwnerReferences: []metav1.OwnerReference{ownerRef}}}
	if err := r.Create(ctx, sa); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("creating ServiceAccount %s: %w", saName, err)
	}

	if err := r.upsertRole(ctx, ws.Namespace, saName+"-role", &ownerRef, slackConnectorRBACRules()); err != nil {
		return err
	}
	if err := r.upsertRoleBinding(ctx, ws.Namespace, saName+"-binding", &ownerRef, saName, ws.Namespace, saName+"-role"); err != nil {
		return err
	}

	// Cluster-scoped read of SlackAgents: membership discovery must see every
	// namespace. Cluster-scoped resources cannot own-ref a namespaced CR, so
	// name them uniquely per workspace.
	crName := fmt.Sprintf("gratefulagents-slack-ws-%s-%s", ws.Namespace, ws.Name)
	cr := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{Name: crName},
		Rules: []rbacv1.PolicyRule{{
			APIGroups: []string{"triggers.gratefulagents.dev"},
			Resources: []string{"slackagents", "slackworkspaces"},
			Verbs:     []string{"get", "list", "watch"},
		}},
	}
	if err := r.Create(ctx, cr); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("creating ClusterRole %s: %w", crName, err)
		}
		existing := &rbacv1.ClusterRole{}
		if getErr := r.Get(ctx, client.ObjectKeyFromObject(cr), existing); getErr != nil {
			return fmt.Errorf("getting ClusterRole %s: %w", crName, getErr)
		}
		existing.Rules = cr.Rules
		if updateErr := r.Update(ctx, existing); updateErr != nil {
			return fmt.Errorf("updating ClusterRole %s: %w", crName, updateErr)
		}
	}
	crb := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: crName},
		RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: crName},
		Subjects:   []rbacv1.Subject{{Kind: rbacv1.ServiceAccountKind, Name: saName, Namespace: ws.Namespace}},
	}
	if err := r.Create(ctx, crb); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("creating ClusterRoleBinding %s: %w", crName, err)
	}
	return nil
}

func (r *SlackWorkspaceReconciler) upsertRole(ctx context.Context, namespace, name string, ownerRef *metav1.OwnerReference, rules []rbacv1.PolicyRule) error {
	role := &rbacv1.Role{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}, Rules: rules}
	if ownerRef != nil {
		role.OwnerReferences = []metav1.OwnerReference{*ownerRef}
	}
	if err := r.Create(ctx, role); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("creating Role %s/%s: %w", namespace, name, err)
		}
		existing := &rbacv1.Role{}
		if getErr := r.Get(ctx, client.ObjectKeyFromObject(role), existing); getErr != nil {
			return fmt.Errorf("getting existing Role %s/%s: %w", namespace, name, getErr)
		}
		existing.Rules = rules
		if updateErr := r.Update(ctx, existing); updateErr != nil {
			return fmt.Errorf("updating Role %s/%s: %w", namespace, name, updateErr)
		}
	}
	return nil
}

func (r *SlackWorkspaceReconciler) upsertRoleBinding(ctx context.Context, namespace, name string, ownerRef *metav1.OwnerReference, saName, saNamespace, roleName string) error {
	rb := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: roleName},
		Subjects:   []rbacv1.Subject{{Kind: rbacv1.ServiceAccountKind, Name: saName, Namespace: saNamespace}},
	}
	if ownerRef != nil {
		rb.OwnerReferences = []metav1.OwnerReference{*ownerRef}
	}
	if err := r.Create(ctx, rb); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("creating RoleBinding %s/%s: %w", namespace, name, err)
	}
	return nil
}

// listMembers returns the SlackAgents bound to this workspace.
func (r *SlackWorkspaceReconciler) listMembers(ctx context.Context, ws *triggersv1alpha1.SlackWorkspace) ([]triggersv1alpha1.SlackAgent, error) {
	agents := &triggersv1alpha1.SlackAgentList{}
	if err := r.List(ctx, agents); err != nil {
		return nil, err
	}
	var members []triggersv1alpha1.SlackAgent
	for _, agent := range agents.Items {
		ns, name := agent.ResolvedWorkspaceRef()
		if ns == ws.Namespace && name == ws.Name {
			members = append(members, agent)
		}
	}
	return members, nil
}

// ensureMemberNamespaces grants the workspace connector SA the standard
// connector permissions in each member namespace (create/watch runs, read
// secrets) and syncs the shared bot token + infra secret there so member child
// runs work exactly like dedicated-agent runs.
func (r *SlackWorkspaceReconciler) ensureMemberNamespaces(ctx context.Context, ws *triggersv1alpha1.SlackWorkspace, saName string, members []triggersv1alpha1.SlackAgent) error {
	botToken, _ := ReadSecretValue(ctx, r.Client, ws.Namespace, strings.TrimSpace(ws.Spec.TokensSecret), triggersv1alpha1.SlackBotTokenKey)

	done := map[string]bool{}
	for _, member := range members {
		ns := member.Namespace
		if done[ns] {
			continue
		}
		done[ns] = true

		roleName := slackWorkspaceResourceName(ws.Name) + "-member-role"
		if err := r.upsertRole(ctx, ns, roleName, nil, slackConnectorRBACRules()); err != nil {
			return err
		}
		if err := r.upsertRoleBinding(ctx, ns, slackWorkspaceResourceName(ws.Name)+"-member-binding", nil, saName, ws.Namespace, roleName); err != nil {
			return err
		}
		if err := r.ensureWorkspaceInfraSecret(ctx, ns); err != nil {
			return err
		}
		if strings.TrimSpace(botToken) != "" {
			if err := r.syncBotTokenSecret(ctx, ns, ws.Name, botToken); err != nil {
				return err
			}
		}
	}
	return nil
}

// syncBotTokenSecret mirrors the shared bot token (bot token only — never the
// app-level token) into a member namespace for agent-side Slack read tools.
func (r *SlackWorkspaceReconciler) syncBotTokenSecret(ctx context.Context, namespace, workspaceName, botToken string) error {
	return upsertSecretData(ctx, r.Client, namespace, SlackWorkspaceBotSecretName(workspaceName),
		nil, map[string][]byte{triggersv1alpha1.SlackBotTokenKey: []byte(botToken)})
}

// ensureWorkspaceInfraSecret mirrors the per-namespace worker infra Secret.
func (r *SlackWorkspaceReconciler) ensureWorkspaceInfraSecret(ctx context.Context, namespace string) error {
	return ensureSlackWorkerInfraSecret(ctx, r.Client, namespace)
}

// ensureConnectorDeployment creates or updates the shared connector Deployment
// running `agent slack` in workspace mode. Replicas stay at exactly 1: Socket
// Mode splits events across sockets, so scaling out would misroute messages.
func (r *SlackWorkspaceReconciler) ensureConnectorDeployment(ctx context.Context, ws *triggersv1alpha1.SlackWorkspace, saName string) (string, error) {
	name := slackWorkspaceResourceName(ws.Name)
	replicas := int32(1)
	if ws.Spec.Suspend {
		replicas = 0
	}
	desired := buildSlackConnectorDeployment(
		name, ws.Namespace, "slack-workspace-connector",
		"triggers.gratefulagents.dev/slack-workspace", ws.Name,
		r.ownerRef(ws), replicas, r.connectorPodSpec(ws, saName),
	)
	if err := applySlackConnectorDeployment(ctx, r.Client, desired); err != nil {
		return "", err
	}
	return name, nil
}

func (r *SlackWorkspaceReconciler) connectorPodSpec(ws *triggersv1alpha1.SlackWorkspace, saName string) corev1.PodSpec {
	secretName := strings.TrimSpace(ws.Spec.TokensSecret)
	env := []corev1.EnvVar{
		{Name: "SLACK_WORKSPACE_NAME", Value: ws.Name},
		{Name: "POD_NAMESPACE", ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.namespace"}}},
		{Name: "SLACK_HEALTH_ADDR", Value: ":8080"},
		slackSecretEnv("SLACK_BOT_TOKEN", secretName, triggersv1alpha1.SlackBotTokenKey),
		slackSecretEnv("SLACK_APP_TOKEN", secretName, triggersv1alpha1.SlackAppTokenKey),
		workerInfraSecretEnvRef("DATABASE_URL", "database-url"),
	}
	if teamID := strings.TrimSpace(ws.Spec.TeamID); teamID != "" {
		env = append(env, corev1.EnvVar{Name: "SLACK_TEAM_ID", Value: teamID})
	}
	return slackConnectorPodSpec(saName, ws.Spec.Image, env)
}

func (r *SlackWorkspaceReconciler) deploymentAvailability(ctx context.Context, namespace, name string) (bool, int32) {
	return slackDeploymentAvailability(ctx, r.Client, namespace, name)
}

func (r *SlackWorkspaceReconciler) updateStatus(ctx context.Context, key types.NamespacedName, mutate func(*triggersv1alpha1.SlackWorkspace)) error {
	fresh := &triggersv1alpha1.SlackWorkspace{}
	if err := r.Get(ctx, key, fresh); err != nil {
		return err
	}
	mutate(fresh)
	return r.Status().Update(ctx, fresh)
}

func setSlackWorkspaceCondition(ws *triggersv1alpha1.SlackWorkspace, condType string, status metav1.ConditionStatus, reason, message string) {
	meta.SetStatusCondition(&ws.Status.Conditions, metav1.Condition{
		Type:               condType,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: ws.Generation,
	})
}

func (r *SlackWorkspaceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&triggersv1alpha1.SlackWorkspace{}).
		Owns(&appsv1.Deployment{}).
		Watches(&triggersv1alpha1.SlackAgent{}, handler.EnqueueRequestsFromMapFunc(r.mapAgentToWorkspace)).
		Named("slackworkspace").
		WithOptions(controller.Options{MaxConcurrentReconciles: 1}).
		Complete(r)
}

// mapAgentToWorkspace re-reconciles the workspace a member (de)binds from so
// membership counts, RBAC, and secret syncs stay current.
func (r *SlackWorkspaceReconciler) mapAgentToWorkspace(_ context.Context, obj client.Object) []reconcile.Request {
	agent, ok := obj.(*triggersv1alpha1.SlackAgent)
	if !ok {
		return nil
	}
	ns, name := agent.ResolvedWorkspaceRef()
	if name == "" {
		return nil
	}
	return []reconcile.Request{{NamespacedName: types.NamespacedName{Namespace: ns, Name: name}}}
}
