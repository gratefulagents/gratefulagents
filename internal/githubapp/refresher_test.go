package githubapp

import (
	"context"
	"testing"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

type fakeInstallationMinter struct{ token string }

func (f fakeInstallationMinter) MintInstallationToken(context.Context, int64, int64, []byte) (string, error) {
	return f.token, nil
}

func TestRefresherRotatesActiveRunTokenSecrets(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	_ = platformv1alpha1.AddToScheme(scheme)
	_ = triggersv1alpha1.AddToScheme(scheme)
	gh := &triggersv1alpha1.GitHubRepository{ObjectMeta: metav1.ObjectMeta{Name: "repo", Namespace: "ns"}, Spec: triggersv1alpha1.GitHubRepositorySpec{GitHubApp: &triggersv1alpha1.GitHubAppAuth{AppID: 1, InstallationID: 2, PrivateKeySecret: "app-key"}}}
	run := &platformv1alpha1.AgentRun{ObjectMeta: metav1.ObjectMeta{Name: "run", Namespace: "ns"}, Spec: platformv1alpha1.AgentRunSpec{Trigger: platformv1alpha1.TriggerRef{Kind: "GitHubRepository", Name: "repo"}, Secrets: &platformv1alpha1.AgentRunSecrets{GitHubTokenSecret: "run-gh-token"}}, Status: platformv1alpha1.AgentRunStatus{Phase: platformv1alpha1.AgentRunPhaseRunning}}
	terminal := &platformv1alpha1.AgentRun{ObjectMeta: metav1.ObjectMeta{Name: "done", Namespace: "ns"}, Spec: platformv1alpha1.AgentRunSpec{Trigger: platformv1alpha1.TriggerRef{Kind: "GitHubRepository", Name: "repo"}, Secrets: &platformv1alpha1.AgentRunSecrets{GitHubTokenSecret: "done-gh-token"}}, Status: platformv1alpha1.AgentRunStatus{Phase: platformv1alpha1.AgentRunPhaseSucceeded}}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		gh,
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "app-key", Namespace: "ns"}, Data: map[string][]byte{PrivateKeySecretKey: []byte("key")}},
		run,
		terminal,
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "run-gh-token", Namespace: "ns"}, Data: map[string][]byte{TokenSecretKey: []byte("old")}},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "done-gh-token", Namespace: "ns"}, Data: map[string][]byte{TokenSecretKey: []byte("old")}},
	).Build()

	r := NewRefresher(c, fakeInstallationMinter{token: "rotated"}, scheme)
	r.refreshAll(context.Background())

	updated := &corev1.Secret{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "ns", Name: "run-gh-token"}, updated); err != nil {
		t.Fatalf("get active secret: %v", err)
	}
	if got := string(updated.Data[TokenSecretKey]); got != "rotated" {
		t.Fatalf("active token = %q, want rotated", got)
	}
	if len(updated.OwnerReferences) == 0 || updated.OwnerReferences[0].Kind != "AgentRun" {
		t.Fatalf("ownerReferences = %#v, want AgentRun owner", updated.OwnerReferences)
	}
	done := &corev1.Secret{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "ns", Name: "done-gh-token"}, done); err != nil {
		t.Fatalf("get terminal secret: %v", err)
	}
	if got := string(done.Data[TokenSecretKey]); got != "old" {
		t.Fatalf("terminal token = %q, want unchanged old", got)
	}
}
