package dashboard

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/google/go-github/v68/github"
	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/githubapp"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestParsePullRequestURL(t *testing.T) {
	tests := []struct {
		url     string
		want    pullRequestRef
		wantErr bool
	}{
		{url: "https://github.com/acme/payments/pull/42", want: pullRequestRef{owner: "acme", repo: "payments", number: 42}},
		{url: "https://github.com/acme/payments/pull/42/files", want: pullRequestRef{owner: "acme", repo: "payments", number: 42}},
		{url: "https://github.com/acme/payments/pull/42?diff=split#discussion_r1", want: pullRequestRef{owner: "acme", repo: "payments", number: 42}},
		{url: "  https://www.github.com/acme/payments/pull/7 ", want: pullRequestRef{owner: "acme", repo: "payments", number: 7}},
		{url: "https://gitlab.com/acme/payments/-/merge_requests/1", wantErr: true},
		{url: "https://github.com/acme/payments", wantErr: true},
		{url: "https://github.com/acme/payments/issues/42", wantErr: true},
		{url: "https://github.com/acme/payments/pull/abc", wantErr: true},
		{url: "not a url at all ://", wantErr: true},
	}
	for _, tt := range tests {
		got, err := parsePullRequestURL(tt.url)
		if tt.wantErr {
			if err == nil {
				t.Errorf("parsePullRequestURL(%q) error = nil, want error", tt.url)
			}
			continue
		}
		if err != nil {
			t.Errorf("parsePullRequestURL(%q) error = %v", tt.url, err)
			continue
		}
		if got != tt.want {
			t.Errorf("parsePullRequestURL(%q) = %+v, want %+v", tt.url, got, tt.want)
		}
	}
}

func TestDedupePullRequestURLs(t *testing.T) {
	got := dedupePullRequestURLs(
		[]string{"https://github.com/a/b/pull/1", " https://github.com/a/b/pull/2", "https://github.com/a/b/pull/1", ""},
		"https://github.com/a/b/pull/1",
	)
	want := []string{"https://github.com/a/b/pull/1", "https://github.com/a/b/pull/2"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("dedupePullRequestURLs = %v, want %v", got, want)
	}

	got = dedupePullRequestURLs(nil, "https://github.com/a/b/pull/9")
	if !reflect.DeepEqual(got, []string{"https://github.com/a/b/pull/9"}) {
		t.Fatalf("legacy-only dedupe = %v", got)
	}

	if got := dedupePullRequestURLs(nil, ""); got != nil {
		t.Fatalf("empty dedupe = %v, want nil", got)
	}
}

func ghComment(id, inReplyTo int64, author, body, path string, line int) *github.PullRequestComment {
	c := &github.PullRequestComment{
		ID:   github.Ptr(id),
		Body: github.Ptr(body),
		User: &github.User{Login: github.Ptr(author)},
	}
	if inReplyTo != 0 {
		c.InReplyTo = github.Ptr(inReplyTo)
	}
	if path != "" {
		c.Path = github.Ptr(path)
		c.Line = github.Ptr(line)
	}
	return c
}

func TestGroupReviewThreads(t *testing.T) {
	comments := []*github.PullRequestComment{
		ghComment(1, 0, "alice", "root A", "main.go", 10),
		ghComment(2, 1, "bob", "reply A1", "", 0),
		ghComment(3, 0, "carol", "root B", "util.go", 5),
		ghComment(4, 2, "alice", "reply A2 (chained via reply)", "", 0),
	}
	threads := groupReviewThreads(comments)
	if len(threads) != 2 {
		t.Fatalf("threads = %d, want 2", len(threads))
	}
	a, b := threads[0], threads[1]
	if a.Id != "1" || a.Path != "main.go" || a.Line != 10 {
		t.Fatalf("thread A = %+v", a)
	}
	if len(a.Comments) != 3 || a.Comments[0].Body != "root A" || a.Comments[1].Body != "reply A1" || a.Comments[2].Body != "reply A2 (chained via reply)" {
		t.Fatalf("thread A comments = %+v", a.Comments)
	}
	if a.Resolved || a.Outdated {
		t.Fatalf("resolved/outdated should be false (REST API cannot report them)")
	}
	if b.Id != "3" || b.Path != "util.go" || len(b.Comments) != 1 || b.Comments[0].Author != "carol" {
		t.Fatalf("thread B = %+v", b)
	}
}

func TestGroupReviewThreadsMissingRoot(t *testing.T) {
	threads := groupReviewThreads([]*github.PullRequestComment{
		ghComment(2, 1, "bob", "orphan reply", "file.go", 3),
	})
	if len(threads) != 1 || threads[0].Id != "1" || threads[0].Path != "file.go" || threads[0].Line != 3 {
		t.Fatalf("threads = %+v", threads)
	}
}

func TestReviewDecision(t *testing.T) {
	review := func(author, state string) *github.PullRequestReview {
		return &github.PullRequestReview{User: &github.User{Login: github.Ptr(author)}, State: github.Ptr(state)}
	}
	if got := reviewDecision(nil); got != "" {
		t.Fatalf("no reviews = %q, want empty", got)
	}
	if got := reviewDecision([]*github.PullRequestReview{review("a", "COMMENTED")}); got != "" {
		t.Fatalf("comment only = %q, want empty", got)
	}
	if got := reviewDecision([]*github.PullRequestReview{review("a", "APPROVED")}); got != "APPROVED" {
		t.Fatalf("approved = %q", got)
	}
	if got := reviewDecision([]*github.PullRequestReview{review("a", "APPROVED"), review("b", "CHANGES_REQUESTED")}); got != "CHANGES_REQUESTED" {
		t.Fatalf("changes requested wins = %q", got)
	}
	if got := reviewDecision([]*github.PullRequestReview{review("a", "CHANGES_REQUESTED"), review("a", "APPROVED")}); got != "APPROVED" {
		t.Fatalf("latest per author wins = %q", got)
	}
	if got := reviewDecision([]*github.PullRequestReview{review("a", "CHANGES_REQUESTED"), review("a", "DISMISSED")}); got != "" {
		t.Fatalf("dismissed = %q, want empty", got)
	}
}

func TestGetAgentRunPullRequestsPartialErrors(t *testing.T) {
	privateKey := testGitHubAppPrivateKey(t)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/app/installations/55/access_tokens":
			_ = json.NewEncoder(w).Encode(map[string]any{"token": "installation-token"})
		case "/repos/acme/payments/pulls/7":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"number": 7,
				"title":  "Add refunds",
				"state":  "open",
				"head":   map[string]any{"ref": "feat/refunds", "sha": "abc123"},
				"base":   map[string]any{"ref": "main"},
			})
		case "/repos/acme/payments/pulls/7/reviews":
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"state": "APPROVED", "user": map[string]any{"login": "alice"}},
			})
		case "/repos/acme/payments/commits/abc123/check-runs":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"total_count": 1,
				"check_runs": []map[string]any{{
					"name":        "ci/test",
					"status":      "completed",
					"conclusion":  "success",
					"details_url": "https://ci.example.com/1",
				}},
			})
		case "/repos/acme/payments/commits/abc123/status":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"state": "failure",
				"statuses": []map[string]any{
					{"context": "legacy/deploy", "state": "error", "target_url": "https://legacy.example.com/1"},
					{"context": "legacy/e2e", "state": "pending", "target_url": "https://legacy.example.com/2"},
				},
			})
		case "/repos/acme/payments/pulls/7/comments":
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"id": 1, "body": "nit", "path": "main.go", "line": 4, "user": map[string]any{"login": "alice"}},
				{"id": 2, "body": "fixed", "in_reply_to_id": 1, "user": map[string]any{"login": "bob"}},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer api.Close()

	scheme := testProjectScheme(t)
	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "run-1", Namespace: "team-a"},
		Status: platformv1alpha1.AgentRunStatus{
			Artifacts: &platformv1alpha1.AgentRunArtifacts{
				PullRequestURL: "https://github.com/acme/payments/pull/7",
				PullRequestURLs: []string{
					"https://github.com/acme/payments/pull/7",
					"https://gitlab.com/acme/other/-/merge_requests/3",
					"https://github.com/acme/unonboarded/pull/9",
				},
			},
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		run,
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "github-app-key", Namespace: "platform"}, Data: map[string][]byte{githubapp.PrivateKeySecretKey: privateKey}},
		&triggersv1alpha1.GitHubRepository{
			ObjectMeta: metav1.ObjectMeta{Name: "payments", Namespace: "team-a"},
			Spec: triggersv1alpha1.GitHubRepositorySpec{
				Owner:     "acme",
				Repo:      "payments",
				GitHubApp: &triggersv1alpha1.GitHubAppAuth{InstallationID: 55},
			},
		},
	).Build()
	srv := NewServer(c, scheme, nil, nil, false,
		WithGitHubAppConfig(99, "gratefulagents", "github-app-key", "platform"),
		WithGitHubAppAPIBaseURL(api.URL+"/"),
	)

	resp, err := srv.GetAgentRunPullRequests(context.Background(), &platform.GetAgentRunPullRequestsRequest{Namespace: "team-a", Name: "run-1"})
	if err != nil {
		t.Fatalf("GetAgentRunPullRequests() error = %v", err)
	}
	if len(resp.PullRequests) != 3 {
		t.Fatalf("PullRequests = %d, want 3 (legacy URL deduped)", len(resp.PullRequests))
	}

	ok := resp.PullRequests[0]
	if ok.Error != "" {
		t.Fatalf("first PR error = %q, want none", ok.Error)
	}
	if ok.Repository != "acme/payments" || ok.Number != 7 || ok.Title != "Add refunds" || ok.State != "open" ||
		ok.HeadRef != "feat/refunds" || ok.BaseRef != "main" || ok.HeadSha != "abc123" || ok.ReviewDecision != "APPROVED" {
		t.Fatalf("first PR = %+v", ok)
	}
	if len(ok.Checks) != 3 || ok.Checks[0].Name != "ci/test" || ok.Checks[0].Conclusion != "success" {
		t.Fatalf("checks = %+v", ok.Checks)
	}
	// Legacy commit statuses are merged in after check runs.
	if ok.Checks[1].Name != "legacy/deploy" || ok.Checks[1].Status != "completed" || ok.Checks[1].Conclusion != "error" ||
		ok.Checks[1].DetailsUrl != "https://legacy.example.com/1" {
		t.Fatalf("legacy error status = %+v", ok.Checks[1])
	}
	if ok.Checks[2].Name != "legacy/e2e" || ok.Checks[2].Status != "pending" || ok.Checks[2].Conclusion != "" {
		t.Fatalf("legacy pending status = %+v", ok.Checks[2])
	}
	if len(ok.ReviewThreads) != 1 || len(ok.ReviewThreads[0].Comments) != 2 || ok.ReviewThreads[0].Path != "main.go" {
		t.Fatalf("review threads = %+v", ok.ReviewThreads)
	}

	if resp.PullRequests[1].Error == "" {
		t.Fatalf("gitlab URL should carry an error marker")
	}
	if resp.PullRequests[2].Error == "" || resp.PullRequests[2].Repository != "acme/unonboarded" {
		t.Fatalf("unonboarded repo entry = %+v", resp.PullRequests[2])
	}
}

func TestGetAgentRunPullRequestsTokenAuthRepository(t *testing.T) {
	var sawAuth string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/repos/acme/legacy/pulls/3":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"number": 3,
				"title":  "Token-auth PR",
				"state":  "open",
				"head":   map[string]any{"ref": "feat/x", "sha": "def456"},
				"base":   map[string]any{"ref": "main"},
			})
		case "/repos/acme/legacy/pulls/3/reviews", "/repos/acme/legacy/pulls/3/comments":
			_ = json.NewEncoder(w).Encode([]map[string]any{})
		case "/repos/acme/legacy/commits/def456/check-runs":
			_ = json.NewEncoder(w).Encode(map[string]any{"total_count": 0, "check_runs": []map[string]any{}})
		case "/repos/acme/legacy/commits/def456/status":
			_ = json.NewEncoder(w).Encode(map[string]any{"state": "pending", "statuses": []map[string]any{}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer api.Close()

	scheme := testProjectScheme(t)
	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "run-1", Namespace: "team-a"},
		Status: platformv1alpha1.AgentRunStatus{
			Artifacts: &platformv1alpha1.AgentRunArtifacts{
				PullRequestURLs: []string{"https://github.com/acme/legacy/pull/3"},
			},
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		run,
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "legacy-token", Namespace: "team-a"},
			Data:       map[string][]byte{githubapp.TokenSecretKey: []byte("repo-token\n")},
		},
		&triggersv1alpha1.GitHubRepository{
			ObjectMeta: metav1.ObjectMeta{Name: "legacy", Namespace: "team-a"},
			Spec: triggersv1alpha1.GitHubRepositorySpec{
				Owner:             "acme",
				Repo:              "legacy",
				GitHubTokenSecret: "legacy-token",
			},
		},
	).Build()
	// No GitHub App configured: token-secret repositories must still work.
	srv := NewServer(c, scheme, nil, nil, false, WithGitHubAppAPIBaseURL(api.URL+"/"))

	resp, err := srv.GetAgentRunPullRequests(context.Background(), &platform.GetAgentRunPullRequestsRequest{Namespace: "team-a", Name: "run-1"})
	if err != nil {
		t.Fatalf("GetAgentRunPullRequests() error = %v", err)
	}
	if len(resp.PullRequests) != 1 {
		t.Fatalf("PullRequests = %d, want 1", len(resp.PullRequests))
	}
	pr := resp.PullRequests[0]
	if pr.Error != "" {
		t.Fatalf("PR error = %q, want none", pr.Error)
	}
	if pr.Title != "Token-auth PR" || pr.Repository != "acme/legacy" {
		t.Fatalf("PR = %+v", pr)
	}
	if sawAuth != "Bearer repo-token" {
		t.Fatalf("Authorization = %q, want token from Secret", sawAuth)
	}
}

// A repository without a GitHubRepository resource must still render PR
// details when the platform GitHub App is installed on it: the handler
// discovers the installation live instead of requiring onboarding. Review
// threads authored by humans or other bots/harnesses are returned as-is.
func TestGetAgentRunPullRequestsAppInstallationFallback(t *testing.T) {
	privateKey := testGitHubAppPrivateKey(t)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/repos/acme/adhoc/installation":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": 77})
		case "/app/installations/77/access_tokens":
			_ = json.NewEncoder(w).Encode(map[string]any{"token": "discovered-token"})
		case "/repos/acme/adhoc/pulls/24":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"number": 24,
				"title":  "Attached repo PR",
				"state":  "open",
				"head":   map[string]any{"ref": "feat/y", "sha": "fff999"},
				"base":   map[string]any{"ref": "main"},
			})
		case "/repos/acme/adhoc/pulls/24/reviews":
			_ = json.NewEncoder(w).Encode([]map[string]any{})
		case "/repos/acme/adhoc/commits/fff999/check-runs":
			_ = json.NewEncoder(w).Encode(map[string]any{"total_count": 0, "check_runs": []map[string]any{}})
		case "/repos/acme/adhoc/commits/fff999/status":
			_ = json.NewEncoder(w).Encode(map[string]any{"state": "pending", "statuses": []map[string]any{}})
		case "/repos/acme/adhoc/pulls/24/comments":
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"id": 1, "body": "human comment", "path": "a.go", "line": 2, "user": map[string]any{"login": "human-reviewer"}},
				{"id": 2, "body": "other harness reply", "in_reply_to_id": 1, "user": map[string]any{"login": "other-bot[bot]"}},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer api.Close()

	scheme := testProjectScheme(t)
	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "run-1", Namespace: "team-a"},
		Status: platformv1alpha1.AgentRunStatus{
			Artifacts: &platformv1alpha1.AgentRunArtifacts{
				PullRequestURLs: []string{"https://github.com/acme/adhoc/pull/24"},
			},
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		run,
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "github-app-key", Namespace: "platform"}, Data: map[string][]byte{githubapp.PrivateKeySecretKey: privateKey}},
	).Build()
	srv := NewServer(c, scheme, nil, nil, false,
		WithGitHubAppConfig(99, "gratefulagents", "github-app-key", "platform"),
		WithGitHubAppAPIBaseURL(api.URL+"/"),
	)

	resp, err := srv.GetAgentRunPullRequests(context.Background(), &platform.GetAgentRunPullRequestsRequest{Namespace: "team-a", Name: "run-1"})
	if err != nil {
		t.Fatalf("GetAgentRunPullRequests() error = %v", err)
	}
	if len(resp.PullRequests) != 1 {
		t.Fatalf("PullRequests = %d, want 1", len(resp.PullRequests))
	}
	pr := resp.PullRequests[0]
	if pr.Error != "" {
		t.Fatalf("PR error = %q, want none (App installation fallback)", pr.Error)
	}
	if pr.Title != "Attached repo PR" || pr.Repository != "acme/adhoc" {
		t.Fatalf("PR = %+v", pr)
	}
	if len(pr.ReviewThreads) != 1 || len(pr.ReviewThreads[0].Comments) != 2 {
		t.Fatalf("review threads = %+v", pr.ReviewThreads)
	}
	if pr.ReviewThreads[0].Comments[0].Author != "human-reviewer" || pr.ReviewThreads[0].Comments[1].Author != "other-bot[bot]" {
		t.Fatalf("comment authors = %+v", pr.ReviewThreads[0].Comments)
	}
}

// When a repository is not onboarded and the GitHub App is not installed on
// it either, the handler falls back to the run's own GitHub token Secret —
// the credential that created the PR in the first place.
func TestGetAgentRunPullRequestsRunTokenFallback(t *testing.T) {
	privateKey := testGitHubAppPrivateKey(t)
	authByPath := map[string]string{}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authByPath[r.URL.Path] = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/repos/acme/attached/pulls/5":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"number": 5,
				"title":  "Run-token PR",
				"state":  "open",
				"head":   map[string]any{"ref": "feat/z", "sha": "0a0b0c"},
				"base":   map[string]any{"ref": "main"},
			})
		case "/repos/acme/attached/pulls/5/reviews", "/repos/acme/attached/pulls/5/comments":
			_ = json.NewEncoder(w).Encode([]map[string]any{})
		case "/repos/acme/attached/commits/0a0b0c/check-runs":
			_ = json.NewEncoder(w).Encode(map[string]any{"total_count": 0, "check_runs": []map[string]any{}})
		case "/repos/acme/attached/commits/0a0b0c/status":
			_ = json.NewEncoder(w).Encode(map[string]any{"state": "pending", "statuses": []map[string]any{}})
		default:
			// Includes /repos/acme/attached/installation → App not installed.
			http.NotFound(w, r)
		}
	}))
	defer api.Close()

	scheme := testProjectScheme(t)
	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "run-1", Namespace: "team-a"},
		Spec: platformv1alpha1.AgentRunSpec{
			Secrets: &platformv1alpha1.AgentRunSecrets{GitHubTokenSecret: "run-github-token"},
		},
		Status: platformv1alpha1.AgentRunStatus{
			Artifacts: &platformv1alpha1.AgentRunArtifacts{
				PullRequestURLs: []string{"https://github.com/acme/attached/pull/5"},
			},
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		run,
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "github-app-key", Namespace: "platform"}, Data: map[string][]byte{githubapp.PrivateKeySecretKey: privateKey}},
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "run-github-token", Namespace: "team-a"},
			Data:       map[string][]byte{githubapp.TokenSecretKey: []byte("run-token\n")},
		},
	).Build()
	srv := NewServer(c, scheme, nil, nil, false,
		WithGitHubAppConfig(99, "gratefulagents", "github-app-key", "platform"),
		WithGitHubAppAPIBaseURL(api.URL+"/"),
	)

	resp, err := srv.GetAgentRunPullRequests(context.Background(), &platform.GetAgentRunPullRequestsRequest{Namespace: "team-a", Name: "run-1"})
	if err != nil {
		t.Fatalf("GetAgentRunPullRequests() error = %v", err)
	}
	if len(resp.PullRequests) != 1 {
		t.Fatalf("PullRequests = %d, want 1", len(resp.PullRequests))
	}
	pr := resp.PullRequests[0]
	if pr.Error != "" {
		t.Fatalf("PR error = %q, want none (run token fallback)", pr.Error)
	}
	if pr.Title != "Run-token PR" || pr.Repository != "acme/attached" {
		t.Fatalf("PR = %+v", pr)
	}
	if got := authByPath["/repos/acme/attached/pulls/5"]; got != "Bearer run-token" {
		t.Fatalf("PR fetch Authorization = %q, want run token", got)
	}
}

func TestGetAgentRunPullRequestsNoArtifacts(t *testing.T) {
	scheme := testProjectScheme(t)
	run := &platformv1alpha1.AgentRun{ObjectMeta: metav1.ObjectMeta{Name: "run-1", Namespace: "team-a"}}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run).Build()
	srv := NewServer(c, scheme, nil, nil, false)

	resp, err := srv.GetAgentRunPullRequests(context.Background(), &platform.GetAgentRunPullRequestsRequest{Namespace: "team-a", Name: "run-1"})
	if err != nil {
		t.Fatalf("GetAgentRunPullRequests() error = %v", err)
	}
	if len(resp.PullRequests) != 0 {
		t.Fatalf("PullRequests = %+v, want empty", resp.PullRequests)
	}
}
