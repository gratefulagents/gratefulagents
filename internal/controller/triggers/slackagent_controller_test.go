package triggers

import (
	"context"
	"testing"

	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func slackAgentTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{
		triggersv1alpha1.AddToScheme,
		corev1.AddToScheme,
		appsv1.AddToScheme,
		rbacv1.AddToScheme,
	} {
		if err := add(scheme); err != nil {
			t.Fatalf("AddToScheme: %v", err)
		}
	}
	return scheme
}

func slackAgentTestCR() *triggersv1alpha1.SlackAgent {
	return &triggersv1alpha1.SlackAgent{
		ObjectMeta: metav1.ObjectMeta{Name: "alice", Namespace: "user-alice"},
		Spec: triggersv1alpha1.SlackAgentSpec{
			TokensSecret: "alice-slack",
			Defaults: triggersv1alpha1.AgentRunDefaults{
				Model: "claude-sonnet-4-6",
				Secrets: triggersv1alpha1.AgentRunSecrets{
					ClaudeApiKey: "anthropic-key",
				},
			},
		},
	}
}

func slackTokensSecret() *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "alice-slack", Namespace: "user-alice"},
		Data: map[string][]byte{
			triggersv1alpha1.SlackBotTokenKey:  []byte("xoxb-1"),
			triggersv1alpha1.SlackUserTokenKey: []byte("xoxp-1"),
			triggersv1alpha1.SlackAppTokenKey:  []byte("xapp-1"),
		},
	}
}

func TestSlackAgentReconcileMissingTokensSetsCondition(t *testing.T) {
	scheme := slackAgentTestScheme(t)
	cr := slackAgentTestCR()
	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&triggersv1alpha1.SlackAgent{}).
		WithObjects(cr).
		Build()

	r := &SlackAgentReconciler{Client: k8sClient, Scheme: scheme}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(cr)}); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	updated := &triggersv1alpha1.SlackAgent{}
	if err := k8sClient.Get(context.Background(), client.ObjectKeyFromObject(cr), updated); err != nil {
		t.Fatalf("Get(SlackAgent) error = %v", err)
	}
	cond := meta.FindStatusCondition(updated.Status.Conditions, triggersv1alpha1.ConditionSlackAgentTokenValid)
	if cond == nil || cond.Status != metav1.ConditionFalse {
		t.Fatalf("TokenValid condition = %+v, want False", cond)
	}

	// No Deployment should be created without tokens.
	deps := &appsv1.DeploymentList{}
	if err := k8sClient.List(context.Background(), deps, client.InNamespace("user-alice")); err != nil {
		t.Fatalf("List(Deployment) error = %v", err)
	}
	if len(deps.Items) != 0 {
		t.Fatalf("Deployments len = %d, want 0", len(deps.Items))
	}
}

func TestSlackAgentReconcileProvisionsConnector(t *testing.T) {
	scheme := slackAgentTestScheme(t)
	cr := slackAgentTestCR()
	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&triggersv1alpha1.SlackAgent{}).
		WithObjects(cr, slackTokensSecret()).
		Build()

	r := &SlackAgentReconciler{Client: k8sClient, Scheme: scheme}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(cr)}); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	name := slackResourceName(cr.Name)

	dep := &appsv1.Deployment{}
	if err := k8sClient.Get(context.Background(), client.ObjectKey{Namespace: "user-alice", Name: name}, dep); err != nil {
		t.Fatalf("Get(Deployment) error = %v", err)
	}
	if got := dep.Spec.Template.Spec.Containers[0].Command; len(got) != 2 || got[0] != "/opt/gratefulagents/bin/agent" || got[1] != "slack" {
		t.Fatalf("connector command = %v, want [/opt/gratefulagents/bin/agent slack]", got)
	}
	if dep.Spec.Replicas == nil || *dep.Spec.Replicas != 1 {
		t.Fatalf("replicas = %v, want 1", dep.Spec.Replicas)
	}

	sa := &corev1.ServiceAccount{}
	if err := k8sClient.Get(context.Background(), client.ObjectKey{Namespace: "user-alice", Name: name}, sa); err != nil {
		t.Fatalf("Get(ServiceAccount) error = %v", err)
	}
	role := &rbacv1.Role{}
	if err := k8sClient.Get(context.Background(), client.ObjectKey{Namespace: "user-alice", Name: name + "-role"}, role); err != nil {
		t.Fatalf("Get(Role) error = %v", err)
	}
	rb := &rbacv1.RoleBinding{}
	if err := k8sClient.Get(context.Background(), client.ObjectKey{Namespace: "user-alice", Name: name + "-binding"}, rb); err != nil {
		t.Fatalf("Get(RoleBinding) error = %v", err)
	}

	// The connector command path streams replies via Postgres, so the run name
	// must be a stable DNS label and the env wiring must include DATABASE_URL.
	connector := dep.Spec.Template.Spec.Containers[0]
	var hasDBURL bool
	for _, e := range connector.Env {
		if e.Name == "DATABASE_URL" {
			hasDBURL = true
		}
	}
	if !hasDBURL {
		t.Error("connector env missing DATABASE_URL")
	}

	updated := &triggersv1alpha1.SlackAgent{}
	if err := k8sClient.Get(context.Background(), client.ObjectKeyFromObject(cr), updated); err != nil {
		t.Fatalf("Get(SlackAgent) error = %v", err)
	}
	if updated.Status.DeploymentName != name {
		t.Fatalf("DeploymentName = %q, want %q", updated.Status.DeploymentName, name)
	}
	cond := meta.FindStatusCondition(updated.Status.Conditions, triggersv1alpha1.ConditionSlackAgentTokenValid)
	if cond == nil || cond.Status != metav1.ConditionTrue {
		t.Fatalf("TokenValid condition = %+v, want True", cond)
	}
}

func TestSlackAgentReconcileSuspendScalesToZero(t *testing.T) {
	scheme := slackAgentTestScheme(t)
	cr := slackAgentTestCR()
	cr.Spec.Suspend = true
	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&triggersv1alpha1.SlackAgent{}).
		WithObjects(cr, slackTokensSecret()).
		Build()

	r := &SlackAgentReconciler{Client: k8sClient, Scheme: scheme}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(cr)}); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	dep := &appsv1.Deployment{}
	if err := k8sClient.Get(context.Background(), client.ObjectKey{Namespace: "user-alice", Name: slackResourceName(cr.Name)}, dep); err != nil {
		t.Fatalf("Get(Deployment) error = %v", err)
	}
	if dep.Spec.Replicas == nil || *dep.Spec.Replicas != 0 {
		t.Fatalf("replicas = %v, want 0 when suspended", dep.Spec.Replicas)
	}
}

func TestSlackAgentConnectorEnvCarriesCommanders(t *testing.T) {
	scheme := slackAgentTestScheme(t)
	cr := slackAgentTestCR()
	cr.Spec.Commanders = []string{"U1", "U2"}
	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&triggersv1alpha1.SlackAgent{}).
		WithObjects(cr, slackTokensSecret()).
		Build()

	r := &SlackAgentReconciler{Client: k8sClient, Scheme: scheme}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(cr)}); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	dep := &appsv1.Deployment{}
	if err := k8sClient.Get(context.Background(), client.ObjectKey{Namespace: "user-alice", Name: slackResourceName(cr.Name)}, dep); err != nil {
		t.Fatalf("Get(Deployment) error = %v", err)
	}
	env := map[string]string{}
	for _, e := range dep.Spec.Template.Spec.Containers[0].Env {
		env[e.Name] = e.Value
	}
	if got := env["SLACK_COMMANDERS"]; got != "U1,U2" {
		t.Errorf("SLACK_COMMANDERS = %q, want U1,U2", got)
	}
}
