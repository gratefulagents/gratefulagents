package dashboard

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestCreateGitHubRemoteRepository(t *testing.T) {
	for _, tc := range []struct {
		name, org string
		public    bool
		status    int
		code      connect.Code
	}{
		{name: "private personal", status: 201},
		{name: "public organization", org: "acme", public: true, status: 201},
		{name: "duplicate", status: 422, code: connect.CodeInvalidArgument},
		{name: "invalid token", status: 401, code: connect.CodeFailedPrecondition},
		{name: "forbidden", status: 403, code: connect.CodePermissionDenied},
		{name: "unknown result", status: 500, code: connect.CodeUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				path := "/user/repos"
				if tc.org != "" {
					path = "/orgs/" + tc.org + "/repos"
				}
				if r.Method != "POST" || r.URL.Path != path || r.Header.Get("Authorization") != "Bearer personal-token" {
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
				}
				var body struct {
					Name     string `json:"name"`
					Private  bool   `json:"private"`
					AutoInit bool   `json:"auto_init"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if body.Name != "payments" || body.Private == tc.public || !body.AutoInit {
					t.Errorf("unexpected payload: %+v", body)
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				if tc.status == 201 {
					_, _ = w.Write([]byte(`{"html_url":"https://github.com/acme/payments","default_branch":"trunk"}`))
				} else {
					_, _ = w.Write([]byte(`{"message":"failure"}`))
				}
			}))
			defer api.Close()
			scheme := testProjectScheme(t)
			c := fake.NewClientBuilder().WithScheme(scheme).Build()
			srv := NewServer(c, scheme, nil, nil, false, WithGitHubAppAPIBaseURL(api.URL+"/"))
			ctx := triggerActorCtx("alice", "member")
			ns, err := srv.ensureUserNamespace(ctx, requestActorFromContext(ctx))
			if err != nil {
				t.Fatal(err)
			}
			if err := c.Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "usercred-github", Namespace: ns}, Data: map[string][]byte{"token": []byte("personal-token")}}); err != nil {
				t.Fatal(err)
			}
			resp, err := srv.CreateGitHubRemoteRepository(ctx, &platform.CreateGitHubRemoteRepositoryRequest{Name: " payments ", Organization: tc.org, Public: tc.public})
			if tc.code != 0 {
				if connect.CodeOf(err) != tc.code {
					t.Fatalf("error = %v, want %v", err, tc.code)
				}
			} else if err != nil {
				t.Fatal(err)
			} else if resp.RepoUrl != "https://github.com/acme/payments" || resp.DefaultBranch != "trunk" {
				t.Fatalf("response = %v", resp)
			}
			if calls != 1 {
				t.Fatalf("API calls = %d", calls)
			}
		})
	}
}

func TestCreateGitHubRemoteRepositoryRejectsBeforeGitHub(t *testing.T) {
	scheme := testProjectScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("unexpected GitHub request"); w.WriteHeader(500) }))
	defer api.Close()
	srv := NewServer(c, scheme, nil, nil, false, WithGitHubAppAPIBaseURL(api.URL+"/"))
	for _, tc := range []struct {
		name    string
		ctx     context.Context
		request *platform.CreateGitHubRemoteRepositoryRequest
		code    connect.Code
	}{
		{"anonymous", context.Background(), &platform.CreateGitHubRemoteRepositoryRequest{Name: "repo"}, connect.CodeUnauthenticated},
		{"viewer", triggerActorCtx("alice", "viewer"), &platform.CreateGitHubRemoteRepositoryRequest{Name: "repo"}, connect.CodePermissionDenied},
		{"invalid name", triggerActorCtx("alice", "member"), &platform.CreateGitHubRemoteRepositoryRequest{Name: "../repo"}, connect.CodeInvalidArgument},
		{"invalid organization", triggerActorCtx("alice", "member"), &platform.CreateGitHubRemoteRepositoryRequest{Name: "repo", Organization: "../acme"}, connect.CodeInvalidArgument},
		{"missing token", triggerActorCtx("bob", "member"), &platform.CreateGitHubRemoteRepositoryRequest{Name: "repo"}, connect.CodeFailedPrecondition},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := srv.CreateGitHubRemoteRepository(tc.ctx, tc.request)
			if connect.CodeOf(err) != tc.code {
				t.Fatalf("error = %v, want %v", err, tc.code)
			}
		})
	}
}
