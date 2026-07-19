package triggers

import (
	"context"
	"testing"

	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const testWorkspaceTeamID = "T0123"

func slackWorkspaceTestCR() *triggersv1alpha1.SlackWorkspace {
	return &triggersv1alpha1.SlackWorkspace{
		ObjectMeta: metav1.ObjectMeta{Name: "acme", Namespace: "user-admin"},
		Spec: triggersv1alpha1.SlackWorkspaceSpec{
			TokensSecret: "acme-slack-ws",
			TeamID:       testWorkspaceTeamID,
		},
	}
}

func slackWorkspaceTokensSecret() *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "acme-slack-ws", Namespace: "user-admin"},
		Data: map[string][]byte{
			triggersv1alpha1.SlackBotTokenKey: []byte("xoxb-shared"),
			triggersv1alpha1.SlackAppTokenKey: []byte("xapp-shared"),
		},
	}
}

func workspaceMemberCR(name, namespace, slackUser string) *triggersv1alpha1.SlackAgent {
	return &triggersv1alpha1.SlackAgent{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: triggersv1alpha1.SlackAgentSpec{
			WorkspaceRef: &triggersv1alpha1.SlackWorkspaceRef{Name: "acme", Namespace: "user-admin"},
			SlackUserID:  slackUser,
			Defaults: triggersv1alpha1.AgentRunDefaults{
				Model:   "claude-sonnet-4-6",
				Secrets: triggersv1alpha1.AgentRunSecrets{ClaudeApiKey: "anthropic-key"},
			},
		},
	}
}

func reconcileSlackWorkspace(t *testing.T, c client.Client) {
	t.Helper()
	r := &SlackWorkspaceReconciler{Client: c, Scheme: slackAgentTestScheme(t)}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: "user-admin", Name: "acme"},
	}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
}

func TestSlackWorkspaceReconcileProvisionsConnector(t *testing.T) {
	scheme := slackAgentTestScheme(t)
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&triggersv1alpha1.SlackWorkspace{}).
		WithObjects(slackWorkspaceTestCR(), slackWorkspaceTokensSecret(),
			workspaceMemberCR("alice", "user-alice", "U01"),
			workspaceMemberCR("bob", "user-bob", "U02")).
		Build()

	reconcileSlackWorkspace(t, c)

	name := slackWorkspaceResourceName("acme")
	dep := &appsv1.Deployment{}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "user-admin", Name: name}, dep); err != nil {
		t.Fatalf("expected connector Deployment: %v", err)
	}
	env := map[string]string{}
	for _, e := range dep.Spec.Template.Spec.Containers[0].Env {
		env[e.Name] = e.Value
	}
	if env["SLACK_WORKSPACE_NAME"] != "acme" {
		t.Errorf("SLACK_WORKSPACE_NAME = %q, want acme", env["SLACK_WORKSPACE_NAME"])
	}
	if env["SLACK_TEAM_ID"] != testWorkspaceTeamID {
		t.Errorf("SLACK_TEAM_ID = %q, want T0123", env["SLACK_TEAM_ID"])
	}

	// Member namespaces get connector RBAC + the synced bot-token secret.
	for _, ns := range []string{"user-alice", "user-bob"} {
		rb := &rbacv1.RoleBinding{}
		if err := c.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: name + "-member-binding"}, rb); err != nil {
			t.Fatalf("expected member RoleBinding in %s: %v", ns, err)
		}
		if rb.Subjects[0].Namespace != "user-admin" || rb.Subjects[0].Name != name {
			t.Errorf("member RoleBinding subject = %+v, want workspace SA", rb.Subjects[0])
		}
		secret := &corev1.Secret{}
		if err := c.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: SlackWorkspaceBotSecretName("acme")}, secret); err != nil {
			t.Fatalf("expected synced bot secret in %s: %v", ns, err)
		}
		if string(secret.Data[triggersv1alpha1.SlackBotTokenKey]) != "xoxb-shared" {
			t.Errorf("synced bot token mismatch in %s", ns)
		}
		if _, hasApp := secret.Data[triggersv1alpha1.SlackAppTokenKey]; hasApp {
			t.Errorf("app-level token must never be synced to member namespaces")
		}
	}

	ws := &triggersv1alpha1.SlackWorkspace{}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "user-admin", Name: "acme"}, ws); err != nil {
		t.Fatalf("get workspace: %v", err)
	}
	if ws.Status.MemberCount != 2 {
		t.Errorf("memberCount = %d, want 2", ws.Status.MemberCount)
	}
	if !meta.IsStatusConditionTrue(ws.Status.Conditions, triggersv1alpha1.ConditionSlackWorkspaceTokenValid) {
		t.Errorf("expected TokenValid=True")
	}
}

func TestSlackWorkspaceReconcileMissingTokens(t *testing.T) {
	scheme := slackAgentTestScheme(t)
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&triggersv1alpha1.SlackWorkspace{}).
		WithObjects(slackWorkspaceTestCR()).
		Build()

	reconcileSlackWorkspace(t, c)

	dep := &appsv1.Deployment{}
	err := c.Get(context.Background(), types.NamespacedName{Namespace: "user-admin", Name: slackWorkspaceResourceName("acme")}, dep)
	if !k8serrors.IsNotFound(err) {
		t.Fatalf("expected no Deployment without tokens, got err=%v", err)
	}
	ws := &triggersv1alpha1.SlackWorkspace{}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "user-admin", Name: "acme"}, ws); err != nil {
		t.Fatalf("get workspace: %v", err)
	}
	if meta.IsStatusConditionTrue(ws.Status.Conditions, triggersv1alpha1.ConditionSlackWorkspaceTokenValid) {
		t.Errorf("expected TokenValid=False without tokens secret")
	}
}

func TestSlackAgentWorkspaceMemberSkipsDedicatedConnector(t *testing.T) {
	scheme := slackAgentTestScheme(t)
	member := workspaceMemberCR("alice", "user-alice", "U01")
	ws := slackWorkspaceTestCR()
	setSlackWorkspaceCondition(ws, triggersv1alpha1.ConditionSlackWorkspaceReady, metav1.ConditionTrue, "ConnectorReady", "ok")
	ws.Status.TeamID = testWorkspaceTeamID
	ws.Status.BotUserID = "B01"

	// A stale dedicated Deployment from a mode switch must be removed.
	stale := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{
		Name: slackResourceName("alice"), Namespace: "user-alice",
	}}

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&triggersv1alpha1.SlackAgent{}, &triggersv1alpha1.SlackWorkspace{}).
		WithObjects(member, ws, stale).
		Build()

	r := &SlackAgentReconciler{Client: c, Scheme: scheme}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: "user-alice", Name: "alice"},
	}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	dep := &appsv1.Deployment{}
	err := c.Get(context.Background(), types.NamespacedName{Namespace: "user-alice", Name: slackResourceName("alice")}, dep)
	if !k8serrors.IsNotFound(err) {
		t.Fatalf("expected stale dedicated Deployment deleted, got err=%v", err)
	}

	fresh := &triggersv1alpha1.SlackAgent{}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "user-alice", Name: "alice"}, fresh); err != nil {
		t.Fatalf("get agent: %v", err)
	}
	if !meta.IsStatusConditionTrue(fresh.Status.Conditions, triggersv1alpha1.ConditionSlackAgentReady) {
		t.Errorf("expected member Ready=True when workspace is ready")
	}
	if fresh.Status.TeamID != testWorkspaceTeamID || fresh.Status.BotUserID != "B01" {
		t.Errorf("expected member status to mirror workspace identity, got team=%q bot=%q", fresh.Status.TeamID, fresh.Status.BotUserID)
	}
}

func TestSlackAgentWorkspaceMemberMissingWorkspace(t *testing.T) {
	scheme := slackAgentTestScheme(t)
	member := workspaceMemberCR("alice", "user-alice", "U01")
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&triggersv1alpha1.SlackAgent{}).
		WithObjects(member).
		Build()

	r := &SlackAgentReconciler{Client: c, Scheme: scheme}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Namespace: "user-alice", Name: "alice"},
	}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	fresh := &triggersv1alpha1.SlackAgent{}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "user-alice", Name: "alice"}, fresh); err != nil {
		t.Fatalf("get agent: %v", err)
	}
	if meta.IsStatusConditionTrue(fresh.Status.Conditions, triggersv1alpha1.ConditionSlackAgentReady) {
		t.Errorf("expected Ready=False when workspace is missing")
	}
	if fresh.Status.LastError == "" {
		t.Errorf("expected lastError to name the missing workspace")
	}
}
