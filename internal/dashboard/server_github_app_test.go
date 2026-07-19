package dashboard

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/githubapp"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func testGitHubAppPrivateKey(t *testing.T) []byte {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
}

func TestGitHubAppConfigReportsUnconfigured(t *testing.T) {
	srv := &Server{}
	resp, err := srv.GetGitHubAppConfig(context.Background(), nil)
	if err != nil {
		t.Fatalf("GetGitHubAppConfig() error = %v", err)
	}
	if resp.Configured {
		t.Fatalf("Configured = true, want false")
	}
	if _, err := srv.ListGitHubAppInstallations(context.Background(), nil); err == nil {
		t.Fatalf("ListGitHubAppInstallations() error = nil, want GitHub App not configured")
	}
}

func TestListGitHubAppInstallationsAndRepositories(t *testing.T) {
	privateKey := testGitHubAppPrivateKey(t)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/app/installations":
			_ = json.NewEncoder(w).Encode([]map[string]any{{
				"id": int64(123),
				"account": map[string]any{
					"login": "acme",
					"type":  "Organization",
				},
			}})
		case "/app/installations/123/access_tokens":
			_ = json.NewEncoder(w).Encode(map[string]any{"token": "installation-token"})
		case "/installation/repositories":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"repositories": []map[string]any{{
					"name":           "payments",
					"full_name":      "acme/payments",
					"default_branch": "main",
					"private":        true,
					"owner": map[string]any{
						"login": "acme",
					},
				}},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer api.Close()

	scheme := testProjectScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "github-app-key", Namespace: "platform"}, Data: map[string][]byte{githubapp.PrivateKeySecretKey: privateKey}},
		&triggersv1alpha1.GitHubRepository{ObjectMeta: metav1.ObjectMeta{Name: "payments", Namespace: "team-a"}, Spec: triggersv1alpha1.GitHubRepositorySpec{Owner: "acme", Repo: "payments"}},
	).Build()
	srv := NewServer(c, scheme, nil, nil, false,
		WithGitHubAppConfig(99, "gratefulagents", "github-app-key", "platform"),
		WithGitHubAppAPIBaseURL(api.URL+"/"),
	)

	installations, err := srv.ListGitHubAppInstallations(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListGitHubAppInstallations() error = %v", err)
	}
	if len(installations.Installations) != 1 || installations.Installations[0].AccountLogin != "acme" {
		t.Fatalf("Installations = %#v, want acme", installations.Installations)
	}

	repos, err := srv.ListGitHubAppInstallationRepositories(context.Background(), &platform.ListGitHubAppInstallationRepositoriesRequest{InstallationId: 123})
	if err != nil {
		t.Fatalf("ListGitHubAppInstallationRepositories() error = %v", err)
	}
	if len(repos.Repositories) != 1 || !repos.Repositories[0].AlreadyOnboarded || repos.Repositories[0].DefaultBranch != "main" {
		t.Fatalf("Repositories = %#v, want onboarded acme/payments", repos.Repositories)
	}
}

func TestGitHubAppInstallationOperationsDenyMembers(t *testing.T) {
	srv := &Server{}
	ctx := actorContext("mallory", "member", "", "")
	if _, err := srv.ListGitHubAppInstallations(ctx, nil); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("ListGitHubAppInstallations(member): want PermissionDenied, got %v", err)
	}
	if _, err := srv.ListGitHubAppInstallationRepositories(ctx, &platform.ListGitHubAppInstallationRepositoriesRequest{InstallationId: 123}); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("ListGitHubAppInstallationRepositories(member): want PermissionDenied, got %v", err)
	}
	if _, err := srv.CreateGitHubRepositoryFromInstallation(ctx, &platform.CreateGitHubRepositoryFromInstallationRequest{InstallationId: 123}); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("CreateGitHubRepositoryFromInstallation(member): want PermissionDenied, got %v", err)
	}
}

func TestCreateGitHubRepositoryFromInstallationCopiesPrivateKeySecret(t *testing.T) {
	privateKey := testGitHubAppPrivateKey(t)
	scheme := testProjectScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "github-app-key", Namespace: "platform"}, Data: map[string][]byte{githubapp.PrivateKeySecretKey: privateKey}},
	).Build()
	srv := NewServer(c, scheme, nil, nil, false, WithGitHubAppConfig(99, "gratefulagents", "github-app-key", "platform"))

	resp, err := srv.CreateGitHubRepositoryFromInstallation(context.Background(), &platform.CreateGitHubRepositoryFromInstallationRequest{
		InstallationId:     123,
		Owner:              "acme",
		Repo:               "payments",
		Namespace:          "team-a",
		DefaultBranch:      "trunk",
		Provider:           "anthropic",
		ClaudeApiKeySecret: "anthropic-key",
		Model:              "claude-sonnet-4-6",
	})
	if err != nil {
		t.Fatalf("CreateGitHubRepositoryFromInstallation() error = %v", err)
	}
	if resp.Namespace != "team-a" || resp.Name != "acme-payments" || resp.BaseBranch != "trunk" {
		t.Fatalf("response = %#v, want team-a/acme-payments trunk", resp)
	}

	copied := &corev1.Secret{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "team-a", Name: "github-app-key"}, copied); err != nil {
		t.Fatalf("Get(copied Secret) error = %v", err)
	}
	if string(copied.Data[githubapp.PrivateKeySecretKey]) != string(privateKey) {
		t.Fatalf("copied private key mismatch")
	}
	gh := &triggersv1alpha1.GitHubRepository{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "team-a", Name: "acme-payments"}, gh); err != nil {
		t.Fatalf("Get(GitHubRepository) error = %v", err)
	}
	if gh.Spec.GitHubApp == nil || gh.Spec.GitHubApp.PrivateKeySecret != "github-app-key" || gh.Spec.GitHubApp.InstallationID != 123 {
		t.Fatalf("GitHubApp = %#v, want copied app auth", gh.Spec.GitHubApp)
	}
}
