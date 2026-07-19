package triggers

import (
	"context"
	"testing"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/githubapp"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

type fakeGitHubAppMinter struct {
	token string
	key   []byte
}

func (f *fakeGitHubAppMinter) MintInstallationToken(_ context.Context, _, _ int64, privateKeyPEM []byte) (string, error) {
	f.key = append([]byte(nil), privateKeyPEM...)
	return f.token, nil
}

func TestResolveGitHubTokenReadsPATSecret(t *testing.T) {
	c := fake.NewClientBuilder().WithObjects(&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "pat", Namespace: "ns"}, Data: map[string][]byte{githubapp.TokenSecretKey: []byte("pat-token")}}).Build()
	gh := &triggersv1alpha1.GitHubRepository{ObjectMeta: metav1.ObjectMeta{Name: "repo", Namespace: "ns"}, Spec: triggersv1alpha1.GitHubRepositorySpec{GitHubTokenSecret: "pat"}}
	token, err := resolveGitHubToken(context.Background(), c, gh, nil)
	if err != nil {
		t.Fatalf("resolveGitHubToken() error = %v", err)
	}
	if token != "pat-token" {
		t.Fatalf("token = %q, want pat-token", token)
	}
}

func TestResolveGitHubTokenMintsGitHubAppToken(t *testing.T) {
	minter := &fakeGitHubAppMinter{token: "app-token"}
	c := fake.NewClientBuilder().WithObjects(&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "app-key", Namespace: "ns"}, Data: map[string][]byte{githubapp.PrivateKeySecretKey: []byte("private-key")}}).Build()
	gh := &triggersv1alpha1.GitHubRepository{ObjectMeta: metav1.ObjectMeta{Name: "repo", Namespace: "ns"}, Spec: triggersv1alpha1.GitHubRepositorySpec{GitHubApp: &triggersv1alpha1.GitHubAppAuth{AppID: 1, InstallationID: 2, PrivateKeySecret: "app-key"}}}
	token, err := resolveGitHubToken(context.Background(), c, gh, minter)
	if err != nil {
		t.Fatalf("resolveGitHubToken() error = %v", err)
	}
	if token != "app-token" {
		t.Fatalf("token = %q, want app-token", token)
	}
	if string(minter.key) != "private-key" {
		t.Fatalf("minter key = %q, want private-key", string(minter.key))
	}
}

func TestCreateAgentRunCreatesPerRunGitHubAppTokenSecret(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	_ = platformv1alpha1.AddToScheme(scheme)
	_ = triggersv1alpha1.AddToScheme(scheme)
	minter := &fakeGitHubAppMinter{token: "run-token"}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "app-key", Namespace: "ns"}, Data: map[string][]byte{githubapp.PrivateKeySecretKey: []byte("private-key")}}).Build()
	r := &GitHubRepositoryReconciler{Client: c, Scheme: scheme, GitHubAppMinter: minter}
	gh := &triggersv1alpha1.GitHubRepository{ObjectMeta: metav1.ObjectMeta{Name: "repo", Namespace: "ns"}, Spec: triggersv1alpha1.GitHubRepositorySpec{
		Owner: "owner", Repo: "repo", GitHubApp: &triggersv1alpha1.GitHubAppAuth{AppID: 1, InstallationID: 2, PrivateKeySecret: "app-key"},
		Defaults: triggersv1alpha1.AgentRunDefaults{Model: "claude-sonnet", RepoURL: "https://github.com/owner/repo.git", Secrets: triggersv1alpha1.AgentRunSecrets{ClaudeApiKey: "claude-key"}},
	}}
	if _, err := r.createAgentRun(context.Background(), gh, "7", 7, "https://github.com/owner/repo/issues/7", "body", "author", nil); err != nil {
		t.Fatalf("createAgentRun() error = %v", err)
	}
	runName := ghIssueName("owner", "repo", "7")
	run := &platformv1alpha1.AgentRun{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "ns", Name: runName}, run); err != nil {
		t.Fatalf("get AgentRun: %v", err)
	}
	wantSecret := runName + "-gh-token"
	if run.Spec.Secrets == nil || run.Spec.Secrets.GitHubTokenSecret != wantSecret {
		t.Fatalf("run GitHubTokenSecret = %#v, want %q", run.Spec.Secrets, wantSecret)
	}
	secret := &corev1.Secret{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "ns", Name: wantSecret}, secret); err != nil {
		t.Fatalf("get token Secret: %v", err)
	}
	if got := string(secret.Data[githubapp.TokenSecretKey]); got != "run-token" {
		t.Fatalf("secret token = %q, want run-token", got)
	}
	if len(secret.OwnerReferences) == 0 || secret.OwnerReferences[0].Kind != "AgentRun" {
		t.Fatalf("ownerReferences = %#v, want AgentRun owner", secret.OwnerReferences)
	}
}
