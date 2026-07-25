package triggers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"testing"
	"time"

	"github.com/google/go-github/v68/github"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
)

func newGitHubPollTestClient(t *testing.T, handler http.Handler) (*goGitHubPullRequestPoller, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(handler)
	client := github.NewClient(server.Client())
	baseURL, err := url.Parse(server.URL + "/")
	if err != nil {
		t.Fatalf("parse test server URL: %v", err)
	}
	client.BaseURL = baseURL
	return &goGitHubPullRequestPoller{client: client}, server
}

func TestGetPullRequestSendsIfNoneMatchAndReturnsResponseETag(t *testing.T) {
	poller, server := newGitHubPollTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.URL.Path, "/repos/acme/widgets/pulls/42"; got != want {
			t.Errorf("path = %q, want %q", got, want)
		}
		if got, want := r.Header.Get("If-None-Match"), `"old"`; got != want {
			t.Errorf("If-None-Match = %q, want %q", got, want)
		}
		w.Header().Set("ETag", `"new"`)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"number":42,"title":"Fix it","html_url":"https://github.com/acme/widgets/pull/42","state":"open","head":{"ref":"fix"},"base":{"ref":"main"},"user":{"login":"agent"},"updated_at":"2026-01-02T03:04:05Z"}`)
	}))
	defer server.Close()

	pull, metadata, err := poller.GetPullRequest(context.Background(), "acme", "widgets", 42, `"old"`)
	if err != nil {
		t.Fatalf("GetPullRequest() error = %v", err)
	}
	if pull == nil || pull.Number != 42 || pull.Title != "Fix it" {
		t.Fatalf("GetPullRequest() pull = %#v", pull)
	}
	if got, want := metadata.ETag, `"new"`; got != want {
		t.Fatalf("ETag = %q, want %q", got, want)
	}
}

func TestGetPullRequestReturnsNilValueForNotModified(t *testing.T) {
	poller, server := newGitHubPollTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("ETag", `"same"`)
		w.WriteHeader(http.StatusNotModified)
	}))
	defer server.Close()

	pull, metadata, err := poller.GetPullRequest(context.Background(), "acme", "widgets", 42, `"same"`)
	if err != nil {
		t.Fatalf("GetPullRequest() error = %v", err)
	}
	if pull != nil {
		t.Fatalf("GetPullRequest() pull = %#v, want nil", pull)
	}
	if got, want := metadata.StatusCode, http.StatusNotModified; got != want {
		t.Fatalf("status = %d, want %d", got, want)
	}
}

func TestHeadRollupsAreBoundToRequestedSHA(t *testing.T) {
	const sha = "abc123"
	poller, server := newGitHubPollTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/repos/acme/widgets/commits/abc123/check-runs":
			_, _ = fmt.Fprint(w, `{"total_count":2,"check_runs":[{"status":"completed","conclusion":"success"},{"status":"in_progress"}]}`)
		case "/repos/acme/widgets/commits/abc123/status":
			_, _ = fmt.Fprint(w, `{"sha":"abc123","state":"success","statuses":[{"context":"legacy","state":"success"}]}`)
		default:
			t.Errorf("path = %q", r.URL.Path)
		}
	}))
	defer server.Close()

	checks, _, err := poller.ListCheckRuns(context.Background(), "acme", "widgets", sha)
	if err != nil {
		t.Fatalf("ListCheckRuns() error = %v", err)
	}
	if checks.HeadSHA != sha || checks.State != gitHubRollupPending {
		t.Fatalf("check rollup = %#v, want pending for %s", checks, sha)
	}
	statuses, _, err := poller.GetCommitStatus(context.Background(), "acme", "widgets", sha)
	if err != nil {
		t.Fatalf("GetCommitStatus() error = %v", err)
	}
	if statuses.HeadSHA != sha || statuses.State != gitHubRollupSuccess {
		t.Fatalf("status rollup = %#v, want success for %s", statuses, sha)
	}
}

func TestListIssueCommentsUsesSinceOverlapAndDoesNotRetainCollectionETag(t *testing.T) {
	after := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	poller, server := newGitHubPollTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.URL.Query().Get("since"), after.Add(-time.Second).Format(time.RFC3339); got != want {
			t.Errorf("since = %q, want %q", got, want)
		}
		if got := r.Header.Get("If-None-Match"); got != "" {
			t.Errorf("If-None-Match = %q, want empty", got)
		}
		w.Header().Set("ETag", `"collection"`)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `[{"id":9,"body":"later","user":{"login":"bob"},"author_association":"MEMBER","created_at":%q,"updated_at":%q},{"id":7,"body":"cursor duplicate","created_at":%q},{"id":8,"body":"earlier","created_at":%q}]`, after.Add(2*time.Second).Format(time.RFC3339), after.Add(3*time.Second).Format(time.RFC3339), after.Format(time.RFC3339), after.Add(time.Second).Format(time.RFC3339))
	}))
	defer server.Close()

	comments, metadata, err := poller.ListIssueComments(context.Background(), "acme", "widgets", 42, after)
	if err != nil {
		t.Fatalf("ListIssueComments() error = %v", err)
	}
	if got, want := []int64{comments[0].ID, comments[1].ID, comments[2].ID}, []int64{7, 8, 9}; !reflect.DeepEqual(got, want) {
		t.Fatalf("overlapping comment IDs = %v, want %v (monitor cursor removes ID 7)", got, want)
	}
	if metadata.ETag != "" {
		t.Fatalf("collection ETag = %q, want empty", metadata.ETag)
	}
}

func TestListReviewsReadsLaterPageAndSortsByTimestampThenID(t *testing.T) {
	after := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	poller, server := newGitHubPollTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Query().Get("page") {
		case "", "1":
			w.Header().Set("Link", fmt.Sprintf(`<%s/repos/acme/widgets/pulls/42/reviews?page=2&per_page=100>; rel="next", <%s/repos/acme/widgets/pulls/42/reviews?page=2&per_page=100>; rel="last"`, serverURL(r), serverURL(r)))
			fmt.Fprint(w, `[{"id":12,"submitted_at":"2026-01-02T03:04:08Z"}]`)
		case "2":
			fmt.Fprint(w, `[{"id":11,"submitted_at":"2026-01-02T03:04:07Z"},{"id":10,"submitted_at":"2026-01-02T03:04:07Z"}]`)
		default:
			t.Errorf("unexpected page %q", r.URL.Query().Get("page"))
		}
	}))
	defer server.Close()

	reviews, metadata, err := poller.ListReviews(context.Background(), "acme", "widgets", 42, after)
	if err != nil {
		t.Fatalf("ListReviews() error = %v", err)
	}
	ids := make([]int64, len(reviews))
	for i := range reviews {
		ids[i] = reviews[i].ID
	}
	if want := []int64{10, 11, 12}; !reflect.DeepEqual(ids, want) {
		t.Fatalf("review IDs = %v, want %v", ids, want)
	}
	if metadata.ETag != "" {
		t.Fatalf("collection ETag = %q, want empty", metadata.ETag)
	}
}

func TestGetReviewDecisionFallsBackToIndividualReviewsWhenGraphQLIsBlank(t *testing.T) {
	poller, server := newGitHubPollTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/graphql" && r.Method == http.MethodPost:
			fmt.Fprint(w, `{"data":{"repository":{"pullRequest":{"reviewDecision":null}}}}`)
		case r.URL.Path == "/repos/acme/widgets/pulls/42/reviews" && r.Method == http.MethodGet:
			fmt.Fprint(w, `[{"id":1,"state":"CHANGES_REQUESTED","user":{"login":"alice"},"submitted_at":"2026-01-02T03:04:05Z"},{"id":2,"state":"APPROVED","user":{"login":"alice"},"submitted_at":"2026-01-02T03:05:05Z"}]`)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.String())
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	decision, _, err := poller.GetReviewDecision(context.Background(), "acme", "widgets", 42)
	if err != nil {
		t.Fatalf("GetReviewDecision() error = %v", err)
	}
	if decision != triggersv1alpha1.PullRequestReviewDecisionApproved {
		t.Fatalf("decision = %q, want approved", decision)
	}
}

func TestIndividualReviewDecision(t *testing.T) {
	reviews := func(values ...polledPullRequestReview) []polledPullRequestReview { return values }
	review := func(author, state string) polledPullRequestReview {
		return polledPullRequestReview{AuthorLogin: author, State: state}
	}
	tests := []struct {
		name    string
		reviews []polledPullRequestReview
		want    triggersv1alpha1.PullRequestReviewDecision
	}{
		{name: "none", want: triggersv1alpha1.PullRequestReviewDecisionUnknown},
		{name: "approval", reviews: reviews(review("alice", "APPROVED")), want: triggersv1alpha1.PullRequestReviewDecisionApproved},
		{name: "changes requested wins", reviews: reviews(review("alice", "APPROVED"), review("bob", "CHANGES_REQUESTED")), want: triggersv1alpha1.PullRequestReviewDecisionChangesRequested},
		{name: "latest decisive review per author wins", reviews: reviews(review("alice", "CHANGES_REQUESTED"), review("alice", "APPROVED")), want: triggersv1alpha1.PullRequestReviewDecisionApproved},
		{name: "dismissal clears review", reviews: reviews(review("alice", "APPROVED"), review("alice", "DISMISSED")), want: triggersv1alpha1.PullRequestReviewDecisionUnknown},
		{name: "comments do not clear approval", reviews: reviews(review("alice", "APPROVED"), review("alice", "COMMENTED")), want: triggersv1alpha1.PullRequestReviewDecisionApproved},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := individualReviewDecision(tt.reviews); got != tt.want {
				t.Fatalf("individualReviewDecision() = %q, want %q", got, tt.want)
			}
		})
	}
}

func serverURL(r *http.Request) string {
	return "http://" + r.Host
}
