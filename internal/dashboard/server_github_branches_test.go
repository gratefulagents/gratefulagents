package dashboard

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
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

func TestListGitHubBranchesVisibility(t *testing.T) {
	for _, tc := range []struct {
		name, actor, role, resource, namespace string
		shared, authenticated                  bool
	}{
		{name: "owner", actor: "user-1", resource: "mine", authenticated: true},
		{name: "hidden", actor: "user-1", resource: "theirs"},
		{name: "shared viewer", actor: "user-1", resource: "theirs", shared: true, authenticated: true},
		{name: "admin", actor: "admin", role: "admin", resource: "theirs", authenticated: true},
		{name: "legacy", actor: "user-1", resource: "legacy", authenticated: true},
		{name: "unrelated", actor: "user-1", resource: "unknown"},
		{name: "other namespace", actor: "user-1", resource: "mine", namespace: "elsewhere"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, ms := triggerVisibilityFixture(t)
			for _, name := range []string{"mine", "theirs", "legacy"} {
				r := &triggersv1alpha1.GitHubRepository{}
				if err := srv.k8sClient.Get(context.Background(), client.ObjectKey{Namespace: "team-a", Name: name + "-repo"}, r); err != nil {
					t.Fatal(err)
				}
				r.Spec.GitHubTokenSecret = "github-token"
				if err := srv.k8sClient.Update(context.Background(), r); err != nil {
					t.Fatal(err)
				}
			}
			if err := srv.k8sClient.Create(context.Background(), &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Namespace: "team-a", Name: "github-token"},
				Data:       map[string][]byte{"token": []byte("repo-token")},
			}); err != nil {
				t.Fatal(err)
			}
			if tc.shared {
				shareTrigger(t, ms, githubRepositoryResourceType, "theirs-repo", "user-1", "viewer")
			}
			calls := 0
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.URL.Path != "/repos/acme/"+tc.resource+"/branches" {
					t.Errorf("unexpected GitHub request: %s", r.URL)
				}
				wantAuth := ""
				if tc.authenticated {
					wantAuth = "Bearer repo-token"
				}
				if got := r.Header.Get("Authorization"); got != wantAuth {
					t.Errorf("Authorization = %q, want %q", got, wantAuth)
				}
				w.Header().Set("Content-Type", "application/json")
				if !tc.authenticated {
					w.WriteHeader(http.StatusNotFound)
					fmt.Fprint(w, `{"message":"Not Found"}`)
					return
				}
				fmt.Fprint(w, `[{"name":"main"},{"name":"release/next"}]`)
			}))
			defer api.Close()
			srv.githubAPIBase = api.URL + "/"
			srv.githubHTTP = api.Client()
			ns := tc.namespace
			if ns == "" {
				ns = "team-a"
			}
			resp, err := srv.ListGitHubBranches(triggerActorCtx(tc.actor, tc.role), &platform.ListGitHubBranchesRequest{
				Namespace: ns, RepoUrl: "https://github.com/acme/" + tc.resource + ".git/",
			})
			if tc.authenticated {
				if err != nil || !reflect.DeepEqual(resp.GetBranches(), []string{"main", "release/next"}) {
					t.Fatalf("response = %v, error = %v", resp, err)
				}
			} else if connect.CodeOf(err) != connect.CodeNotFound {
				t.Fatalf("error = %v, want NotFound", err)
			}
			if calls != 1 {
				t.Fatalf("GitHub calls = %d, want only branch listing", calls)
			}
		})
	}
}

func TestListGitHubBranchesPublicPagination(t *testing.T) {
	srv, _ := newCronTestServer(t)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/acme/public/branches" || r.URL.Query().Get("per_page") != "100" || r.Header.Get("Authorization") != "" {
			t.Errorf("unexpected request: %s, auth %q", r.URL, r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("page") == "1" {
			w.Header().Set("Link", fmt.Sprintf(`<http://%s/repos/acme/public/branches?page=2>; rel="next"`, r.Host))
			fmt.Fprint(w, `[{"name":"main"}]`)
		} else {
			fmt.Fprint(w, `[{"name":"feature/a"}]`)
		}
	}))
	defer api.Close()
	srv.githubAPIBase = api.URL + "/"
	handler := &PlatformServiceConnectHandler{srv: srv}
	resp, err := handler.ListGitHubBranches(triggerActorCtx("user-1", "member"), connect.NewRequest(&platform.ListGitHubBranchesRequest{RepoUrl: "https://github.com/acme/public"}))
	if err != nil || resp.Msg.NextPage != 2 || !reflect.DeepEqual(resp.Msg.Branches, []string{"main"}) {
		t.Fatalf("first page = %v, error = %v", resp, err)
	}
	last, err := srv.ListGitHubBranches(context.Background(), &platform.ListGitHubBranchesRequest{RepoUrl: "https://github.com/acme/public", Page: resp.Msg.NextPage})
	if err != nil || last.NextPage != 0 || !reflect.DeepEqual(last.Branches, []string{"feature/a"}) {
		t.Fatalf("last page = %v, error = %v", last, err)
	}
}

func TestListGitHubBranchesValidationAndErrors(t *testing.T) {
	srv, _ := newCronTestServer(t)
	for _, url := range []string{"", "https://gitlab.com/acme/repo", "http://github.com/acme/repo", "https://github.com/acme/repo/tree/main", "https://token@github.com/acme/repo", "https://github.com/acme/repo?token=secret"} {
		if _, err := srv.ListGitHubBranches(context.Background(), &platform.ListGitHubBranchesRequest{RepoUrl: url}); connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Errorf("URL %q error = %v, want InvalidArgument", url, err)
		}
	}
	if _, err := srv.ListGitHubBranches(context.Background(), &platform.ListGitHubBranchesRequest{RepoUrl: "https://github.com/acme/repo", Page: -1}); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("negative page error = %v", err)
	}
	for _, tc := range []struct {
		status int
		code   connect.Code
	}{{404, connect.CodeNotFound}, {403, connect.CodeResourceExhausted}, {429, connect.CodeResourceExhausted}, {500, connect.CodeUnavailable}} {
		t.Run(fmt.Sprint(tc.status), func(t *testing.T) {
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				fmt.Fprint(w, "sensitive upstream detail")
			}))
			defer api.Close()
			srv.githubAPIBase = api.URL + "/"
			_, err := srv.ListGitHubBranches(context.Background(), &platform.ListGitHubBranchesRequest{RepoUrl: "https://github.com/acme/repo"})
			if connect.CodeOf(err) != tc.code || strings.Contains(err.Error(), "sensitive") {
				t.Fatalf("error = %v, want sanitized %s", err, tc.code)
			}
		})
	}
}

func TestListGitHubBranchesAppCredentials(t *testing.T) {
	privateKey := testGitHubAppPrivateKey(t)
	minted := false
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/app/installations/123/access_tokens":
			minted = true
			fmt.Fprint(w, `{"token":"installation-token"}`)
		case "/repos/acme/payments/branches":
			if r.Header.Get("Authorization") != "Bearer installation-token" {
				t.Errorf("missing installation token")
			}
			fmt.Fprint(w, `[{"name":"main"}]`)
		default:
			t.Errorf("unexpected GitHub call: %s", r.URL)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer api.Close()
	scheme := testProjectScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "github-app-key", Namespace: "platform"}, Data: map[string][]byte{githubapp.PrivateKeySecretKey: privateKey}},
		&triggersv1alpha1.GitHubRepository{ObjectMeta: metav1.ObjectMeta{Name: "payments", Namespace: "team-a"}, Spec: triggersv1alpha1.GitHubRepositorySpec{Owner: "acme", Repo: "payments", GitHubApp: &triggersv1alpha1.GitHubAppAuth{InstallationID: 123}}},
	).Build()
	srv := NewServer(c, scheme, nil, nil, false,
		WithGitHubAppConfig(99, "gratefulagents", "github-app-key", "platform"), WithGitHubAppAPIBaseURL(api.URL+"/"))
	resp, err := srv.ListGitHubBranches(triggerActorCtx("user-1", "member"), &platform.ListGitHubBranchesRequest{RepoUrl: "https://github.com/acme/payments"})
	if err != nil || !minted || !reflect.DeepEqual(resp.GetBranches(), []string{"main"}) {
		t.Fatalf("response = %v, minted = %v, error = %v", resp, minted, err)
	}
}
