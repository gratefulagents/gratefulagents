package dashboard

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/githubapp"
	"github.com/gratefulagents/gratefulagents/internal/store"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// fakeGitHubRepoAPI serves GET /repos/{owner}/{repo}, recording the
// Authorization header it saw.
func fakeGitHubRepoAPI(t *testing.T, status int, defaultBranch string, sawAuth *string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if sawAuth != nil {
			*sawAuth = r.Header.Get("Authorization")
		}
		if r.URL.Path != "/repos/acme/payments" {
			http.NotFound(w, r)
			return
		}
		if status != http.StatusOK {
			w.WriteHeader(status)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"name":           "payments",
			"full_name":      "acme/payments",
			"default_branch": defaultBranch,
			"owner":          map[string]any{"login": "acme"},
		})
	}))
}

func TestCreateGitHubRepositoryFromTokenUsesSavedToken(t *testing.T) {
	var sawAuth string
	api := fakeGitHubRepoAPI(t, http.StatusOK, "develop", &sawAuth)
	defer api.Close()

	scheme := testProjectScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	// No GitHub App configured: the token path must work without one.
	srv := NewServer(c, scheme, nil, nil, false, WithGitHubAppAPIBaseURL(api.URL+"/"))

	ctx := triggerActorCtx("user-1", "member")
	namespace, err := srv.ensureUserNamespace(ctx, requestActorFromContext(ctx))
	if err != nil {
		t.Fatalf("ensureUserNamespace() error = %v", err)
	}
	if err := c.Create(context.Background(), &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "usercred-github", Namespace: namespace},
		Data:       map[string][]byte{"token": []byte("saved-token")},
	}); err != nil {
		t.Fatalf("create saved token secret: %v", err)
	}

	resp, err := srv.CreateGitHubRepositoryFromToken(ctx, &platform.CreateGitHubRepositoryFromTokenRequest{
		Owner:              "acme",
		Repo:               "payments",
		Provider:           "anthropic",
		ClaudeApiKeySecret: "anthropic-key",
		Model:              "claude-sonnet-4-6",
	})
	if err != nil {
		t.Fatalf("CreateGitHubRepositoryFromToken() error = %v", err)
	}
	if resp.Namespace != namespace {
		t.Fatalf("Namespace = %q, want personal namespace %q", resp.Namespace, namespace)
	}
	if resp.BaseBranch != "develop" {
		t.Fatalf("BaseBranch = %q, want develop (from GitHub API)", resp.BaseBranch)
	}
	if sawAuth != "Bearer saved-token" {
		t.Fatalf("GitHub API saw Authorization %q, want the saved token", sawAuth)
	}

	gh := &triggersv1alpha1.GitHubRepository{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: namespace, Name: "acme-payments"}, gh); err != nil {
		t.Fatalf("Get(GitHubRepository) error = %v", err)
	}
	if gh.Spec.GitHubTokenSecret != "usercred-github" {
		t.Fatalf("GitHubTokenSecret = %q, want usercred-github", gh.Spec.GitHubTokenSecret)
	}
	if gh.Spec.GitHubApp != nil {
		t.Fatalf("GitHubApp = %#v, want nil for the token path", gh.Spec.GitHubApp)
	}
	if gh.Spec.Defaults.Secrets.GithubToken != "usercred-github" {
		t.Fatalf("Defaults.Secrets.GithubToken = %q, want usercred-github", gh.Spec.Defaults.Secrets.GithubToken)
	}
	if gh.Spec.Maintainer != nil {
		t.Fatalf("Maintainer = %+v, want nil by default", gh.Spec.Maintainer)
	}
}

func TestCreateGitHubRepositoryFromTokenInlineTokenCreatesSecret(t *testing.T) {
	var sawAuth string
	api := fakeGitHubRepoAPI(t, http.StatusOK, "main", &sawAuth)
	defer api.Close()

	scheme := testProjectScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	srv := NewServer(c, scheme, nil, nil, false, WithGitHubAppAPIBaseURL(api.URL+"/"))

	ctx := triggerActorCtx("user-1", "member")
	resp, err := srv.CreateGitHubRepositoryFromToken(ctx, &platform.CreateGitHubRepositoryFromTokenRequest{
		Owner:              "acme",
		Repo:               "payments",
		DefaultBranch:      "trunk",
		Provider:           "anthropic",
		ClaudeApiKeySecret: "anthropic-key",
		GithubToken:        "ghp-inline",
	})
	if err != nil {
		t.Fatalf("CreateGitHubRepositoryFromToken() error = %v", err)
	}
	if resp.BaseBranch != "trunk" {
		t.Fatalf("BaseBranch = %q, want explicit trunk", resp.BaseBranch)
	}
	if sawAuth != "Bearer ghp-inline" {
		t.Fatalf("GitHub API saw Authorization %q, want the inline token", sawAuth)
	}

	gh := &triggersv1alpha1.GitHubRepository{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: resp.Namespace, Name: "acme-payments"}, gh); err != nil {
		t.Fatalf("Get(GitHubRepository) error = %v", err)
	}
	wantSecret := "acme-payments-github-token"
	if gh.Spec.GitHubTokenSecret != wantSecret {
		t.Fatalf("GitHubTokenSecret = %q, want %q", gh.Spec.GitHubTokenSecret, wantSecret)
	}
	if gh.Spec.Defaults.Secrets.GithubToken != wantSecret {
		t.Fatalf("Defaults.Secrets.GithubToken = %q, want %q", gh.Spec.Defaults.Secrets.GithubToken, wantSecret)
	}
	secret := &corev1.Secret{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: resp.Namespace, Name: wantSecret}, secret); err != nil {
		t.Fatalf("Get(token Secret) error = %v", err)
	}
	if string(secret.StringData["token"])+string(secret.Data["token"]) != "ghp-inline" {
		t.Fatalf("token Secret data = %#v, want inline token", secret)
	}
	if len(secret.OwnerReferences) != 1 || secret.OwnerReferences[0].Kind != "GitHubRepository" || secret.OwnerReferences[0].Name != "acme-payments" {
		t.Fatalf("token Secret owner refs = %#v, want GitHubRepository/acme-payments", secret.OwnerReferences)
	}
}

func TestCreateGitHubRepositoryFromTokenMissingToken(t *testing.T) {
	api := fakeGitHubRepoAPI(t, http.StatusOK, "main", nil)
	defer api.Close()

	scheme := testProjectScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	srv := NewServer(c, scheme, nil, nil, false, WithGitHubAppAPIBaseURL(api.URL+"/"))

	_, err := srv.CreateGitHubRepositoryFromToken(triggerActorCtx("user-1", "member"), &platform.CreateGitHubRepositoryFromTokenRequest{
		Owner:              "acme",
		Repo:               "payments",
		Provider:           "anthropic",
		ClaudeApiKeySecret: "anthropic-key",
	})
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("error = %v, want FailedPrecondition (no saved token)", err)
	}
}

func TestCreateGitHubRepositoryFromTokenInaccessibleRepo(t *testing.T) {
	api := fakeGitHubRepoAPI(t, http.StatusNotFound, "", nil)
	defer api.Close()

	scheme := testProjectScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	srv := NewServer(c, scheme, nil, nil, false, WithGitHubAppAPIBaseURL(api.URL+"/"))

	ctx := triggerActorCtx("user-1", "member")
	resp, err := srv.CreateGitHubRepositoryFromToken(ctx, &platform.CreateGitHubRepositoryFromTokenRequest{
		Owner:              "acme",
		Repo:               "payments",
		Provider:           "anthropic",
		ClaudeApiKeySecret: "anthropic-key",
		GithubToken:        "ghp-inline",
	})
	if connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("error = %v (resp=%v), want NotFound", err, resp)
	}
	// Nothing may be left behind: no trigger, no token Secret.
	namespace, err := srv.ensureUserNamespace(ctx, requestActorFromContext(ctx))
	if err != nil {
		t.Fatalf("ensureUserNamespace() error = %v", err)
	}
	gh := &triggersv1alpha1.GitHubRepository{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: namespace, Name: "acme-payments"}, gh); !k8serrors.IsNotFound(err) {
		t.Fatalf("GitHubRepository should not exist, got err=%v", err)
	}
	secret := &corev1.Secret{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: namespace, Name: "acme-payments-github-token"}, secret); !k8serrors.IsNotFound(err) {
		t.Fatalf("token Secret should not exist, got err=%v", err)
	}
}

func TestCreateGitHubRepositoryFromTokenSavedCredentials(t *testing.T) {
	api := fakeGitHubRepoAPI(t, http.StatusOK, "main", nil)
	defer api.Close()

	scheme := testProjectScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	srv := NewServer(c, scheme, nil, nil, false, WithGitHubAppAPIBaseURL(api.URL+"/"))

	ctx := triggerActorCtx("user-1", "member")
	namespace, err := srv.ensureUserNamespace(ctx, requestActorFromContext(ctx))
	if err != nil {
		t.Fatalf("ensureUserNamespace() error = %v", err)
	}
	for name, data := range map[string]map[string][]byte{
		"usercred-github":    {"token": []byte("saved-token")},
		"usercred-anthropic": {"api-key": []byte("sk-ant")},
	} {
		if err := c.Create(context.Background(), &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
			Data:       data,
		}); err != nil {
			t.Fatalf("create secret %s: %v", name, err)
		}
	}

	_, err = srv.CreateGitHubRepositoryFromToken(ctx, &platform.CreateGitHubRepositoryFromTokenRequest{
		Owner:               "acme",
		Repo:                "payments",
		Provider:            "anthropic",
		AuthMode:            "api-key",
		UseSavedCredentials: true,
	})
	if err != nil {
		t.Fatalf("CreateGitHubRepositoryFromToken() error = %v", err)
	}

	gh := &triggersv1alpha1.GitHubRepository{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: namespace, Name: "acme-payments"}, gh); err != nil {
		t.Fatalf("Get(GitHubRepository) error = %v", err)
	}
	keys := gh.Spec.Defaults.Secrets.ProviderKeys
	if len(keys) != 1 || keys[0].SecretName != "usercred-anthropic" || keys[0].Provider != "anthropic" {
		t.Fatalf("ProviderKeys = %#v, want saved anthropic key", keys)
	}
	if gh.Spec.Defaults.Secrets.GithubToken != "usercred-github" {
		t.Fatalf("Defaults.Secrets.GithubToken = %q, want usercred-github", gh.Spec.Defaults.Secrets.GithubToken)
	}
}

func TestCreateGitHubRepositoryFromInstallationDefaultsToUserNamespace(t *testing.T) {
	privateKey := testGitHubAppPrivateKey(t)
	scheme := testProjectScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "github-app-key", Namespace: "platform"}, Data: map[string][]byte{githubapp.PrivateKeySecretKey: privateKey}},
	).Build()
	srv := NewServer(c, scheme, nil, nil, false, WithGitHubAppConfig(99, "gratefulagents", "github-app-key", "platform"))

	ctx := triggerActorCtx("user-1", "admin")
	namespace, err := srv.ensureUserNamespace(ctx, requestActorFromContext(ctx))
	if err != nil {
		t.Fatalf("ensureUserNamespace() error = %v", err)
	}

	resp, err := srv.CreateGitHubRepositoryFromInstallation(ctx, &platform.CreateGitHubRepositoryFromInstallationRequest{
		InstallationId:     123,
		Owner:              "acme",
		Repo:               "payments",
		Provider:           "anthropic",
		ClaudeApiKeySecret: "anthropic-key",
	})
	if err != nil {
		t.Fatalf("CreateGitHubRepositoryFromInstallation() error = %v", err)
	}
	if resp.Namespace != namespace {
		t.Fatalf("Namespace = %q, want personal namespace %q", resp.Namespace, namespace)
	}
	copied := &corev1.Secret{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: namespace, Name: "github-app-key"}, copied); err != nil {
		t.Fatalf("Get(copied private key Secret) error = %v", err)
	}
	gh := &triggersv1alpha1.GitHubRepository{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: namespace, Name: "acme-payments"}, gh); err != nil {
		t.Fatalf("Get(GitHubRepository) error = %v", err)
	}
	if gh.Spec.Maintainer != nil {
		t.Fatalf("Maintainer = %+v, want nil by default", gh.Spec.Maintainer)
	}
}

func TestCreateGitHubRepositoryFromTokenDeniesForeignPersonalNamespace(t *testing.T) {
	api := fakeGitHubRepoAPI(t, http.StatusOK, "main", nil)
	defer api.Close()

	scheme := testProjectScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
			Name:   "victim-ns",
			Labels: map[string]string{userNamespaceLabel: "true"},
		}},
	).Build()
	srv := NewServer(c, scheme, nil, nil, false, WithGitHubAppAPIBaseURL(api.URL+"/"))

	_, err := srv.CreateGitHubRepositoryFromToken(triggerActorCtx("user-1", "member"), &platform.CreateGitHubRepositoryFromTokenRequest{
		Owner:              "acme",
		Repo:               "payments",
		Namespace:          "victim-ns",
		Provider:           "anthropic",
		ClaudeApiKeySecret: "anthropic-key",
		GithubToken:        "ghp-inline",
	})
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("error = %v, want PermissionDenied for another user's personal namespace", err)
	}
}

// failingOwnerStore makes SetResourceOwner fail so ownership recording's
// fail-closed rollback can be exercised.
type failingOwnerStore struct{ store.StateStore }

func (failingOwnerStore) SetResourceOwner(context.Context, string, string, string, string) error {
	return errors.New("boom")
}

func TestCreateGitHubRepositoryFromTokenOwnershipFailClosed(t *testing.T) {
	api := fakeGitHubRepoAPI(t, http.StatusOK, "main", nil)
	defer api.Close()

	scheme := testProjectScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	srv := NewServer(c, scheme, nil, nil, false, WithGitHubAppAPIBaseURL(api.URL+"/"))
	srv.stateStore = failingOwnerStore{newCollaborationStateStore()}

	ctx := triggerActorCtx("user-1", "member")
	_, err := srv.CreateGitHubRepositoryFromToken(ctx, &platform.CreateGitHubRepositoryFromTokenRequest{
		Owner:              "acme",
		Repo:               "payments",
		Provider:           "anthropic",
		ClaudeApiKeySecret: "anthropic-key",
		GithubToken:        "ghp-inline",
	})
	if connect.CodeOf(err) != connect.CodeInternal {
		t.Fatalf("error = %v, want Internal when ownership recording fails", err)
	}
	namespace, nsErr := srv.ensureUserNamespace(ctx, requestActorFromContext(ctx))
	if nsErr != nil {
		t.Fatalf("ensureUserNamespace() error = %v", nsErr)
	}
	gh := &triggersv1alpha1.GitHubRepository{}
	if getErr := c.Get(context.Background(), client.ObjectKey{Namespace: namespace, Name: "acme-payments"}, gh); !k8serrors.IsNotFound(getErr) {
		t.Fatalf("GitHubRepository should have been rolled back, got err=%v", getErr)
	}
	secret := &corev1.Secret{}
	if getErr := c.Get(context.Background(), client.ObjectKey{Namespace: namespace, Name: "acme-payments-github-token"}, secret); !k8serrors.IsNotFound(getErr) {
		t.Fatalf("token Secret should have been rolled back, got err=%v", getErr)
	}
}
