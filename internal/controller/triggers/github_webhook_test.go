package triggers

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

type recordingPRSink struct {
	events  []PullRequestEvent
	handled bool
	err     error
}

func (s *recordingPRSink) HandlePullRequestEvent(_ context.Context, _ *triggersv1alpha1.GitHubRepository, event PullRequestEvent) (bool, error) {
	s.events = append(s.events, event)
	return s.handled, s.err
}

func newWebhookHandler(t *testing.T, sink PullRequestEventSink, objs ...client.Object) *GitHubWebhookHandler {
	t.Helper()
	scheme := prLoopTestScheme(t)
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(core): %v", err)
	}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&triggersv1alpha1.GitHubRepository{}).
		WithObjects(objs...).
		Build()
	return &GitHubWebhookHandler{
		Client:               c,
		Reconciler:           &GitHubRepositoryReconciler{Client: c, Scheme: scheme},
		AllowUnauthenticated: true,
		PRSink:               sink,
	}
}

func postWebhook(t *testing.T, h *GitHubWebhookHandler, event, delivery string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	RegisterGitHubWebhookRoutes(mux, h)
	req := httptest.NewRequest(http.MethodPost, "/webhooks/github/default/repo", bytes.NewReader(body))
	req.Header.Set("X-GitHub-Event", event)
	if delivery != "" {
		req.Header.Set("X-GitHub-Delivery", delivery)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestWebhookRoutesPullRequestOpenedToSink(t *testing.T) {
	t.Parallel()
	sink := &recordingPRSink{handled: true}
	h := newWebhookHandler(t, sink, prLoopTestRepo())

	body := []byte(`{
		"action": "opened",
		"pull_request": {
			"number": 42,
			"title": "Add pagination",
			"html_url": "https://github.com/acme/widgets/pull/42",
			"user": {"login": "agent-bot"},
			"head": {"ref": "gh-acme-widgets-7"},
			"base": {"ref": "main"}
		},
		"sender": {"login": "agent-bot"}
	}`)
	rec := postWebhook(t, h, "pull_request", "d-1", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	if len(sink.events) != 1 {
		t.Fatalf("sink events = %d, want 1", len(sink.events))
	}
	got := sink.events[0]
	if got.Type != PREventOpened || got.Number != 42 || got.HeadRef != "gh-acme-widgets-7" || got.BaseRef != "main" {
		t.Fatalf("event = %#v, want normalized opened event", got)
	}
}

func TestWebhookRejectsMissingSecretByDefault(t *testing.T) {
	t.Parallel()
	sink := &recordingPRSink{handled: true}
	h := newWebhookHandler(t, sink, prLoopTestRepo())
	h.AllowUnauthenticated = false

	rec := postWebhook(t, h, "pull_request", "missing-secret-default", []byte(`{"action":"opened","pull_request":{"number":1}}`))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (%s)", rec.Code, rec.Body.String())
	}
	if got := rec.Body.String(); got != "webhook secret not configured\n" {
		t.Fatalf("body = %q, want webhook secret not configured", got)
	}
	if len(sink.events) != 0 {
		t.Fatalf("sink events = %d, want 0", len(sink.events))
	}
}

func TestWebhookAllowsMissingSecretWithOptOut(t *testing.T) {
	t.Parallel()
	sink := &recordingPRSink{handled: true}
	h := newWebhookHandler(t, sink, prLoopTestRepo())

	body := []byte(`{"action":"opened","pull_request":{"number":1,"head":{"ref":"x"},"base":{"ref":"main"}}}`)
	rec := postWebhook(t, h, "pull_request", "missing-secret-opt-out", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	if len(sink.events) != 1 {
		t.Fatalf("sink events = %d, want 1", len(sink.events))
	}
}

func TestWebhookIgnoresUninterestingPRActions(t *testing.T) {
	t.Parallel()
	sink := &recordingPRSink{}
	h := newWebhookHandler(t, sink, prLoopTestRepo())

	rec := postWebhook(t, h, "pull_request", "d-2", []byte(`{"action":"labeled","pull_request":{"number":1}}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if len(sink.events) != 0 {
		t.Fatalf("sink events = %d, want 0", len(sink.events))
	}
}

func TestWebhookRoutesReviewSubmittedToSink(t *testing.T) {
	t.Parallel()
	sink := &recordingPRSink{handled: true}
	h := newWebhookHandler(t, sink, prLoopTestRepo())

	body := []byte(`{
		"action": "submitted",
		"pull_request": {
			"number": 42,
			"html_url": "https://github.com/acme/widgets/pull/42",
			"user": {"login": "agent-bot"},
			"head": {"ref": "gh-acme-widgets-7"},
			"base": {"ref": "main"}
		},
		"review": {"state": "changes_requested", "body": "off by one", "user": {"login": "human"}},
		"sender": {"login": "human"}
	}`)
	rec := postWebhook(t, h, "pull_request_review", "d-3", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	if len(sink.events) != 1 {
		t.Fatalf("sink events = %d, want 1", len(sink.events))
	}
	got := sink.events[0]
	if got.Type != PREventReviewSubmitted || got.ReviewState != "changes_requested" || got.Body != "off by one" || got.SenderLogin != "human" {
		t.Fatalf("event = %#v, want normalized review event", got)
	}
}

func TestWebhookDeduplicatesDeliveries(t *testing.T) {
	t.Parallel()
	sink := &recordingPRSink{handled: true}
	h := newWebhookHandler(t, sink, prLoopTestRepo())

	body := []byte(`{"action":"opened","pull_request":{"number":42,"head":{"ref":"x"},"base":{"ref":"main"}}}`)
	for range 2 {
		rec := postWebhook(t, h, "pull_request", "same-guid", body)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
	}
	if len(sink.events) != 1 {
		t.Fatalf("sink events = %d, want 1 (redelivery dropped)", len(sink.events))
	}

	// A different delivery ID is processed.
	rec := postWebhook(t, h, "pull_request", "other-guid", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if len(sink.events) != 2 {
		t.Fatalf("sink events = %d, want 2", len(sink.events))
	}
}

func TestWebhookPRCommentFallsThroughWhenUnhandled(t *testing.T) {
	t.Parallel()
	sink := &recordingPRSink{handled: false}
	h := newWebhookHandler(t, sink, prLoopTestRepo())

	// PR comment without a loop-linked run: sink reports unhandled, the
	// legacy keyword path runs (and silently ignores the missing keyword).
	body := []byte(`{
		"action": "created",
		"issue": {
			"number": 42,
			"title": "Add pagination",
			"html_url": "https://github.com/acme/widgets/pull/42",
			"user": {"login": "human"},
			"pull_request": {"url": "https://api.github.com/repos/acme/widgets/pulls/42"}
		},
		"comment": {"body": "no keyword here", "author_association": "MEMBER", "user": {"login": "human"}}
	}`)
	rec := postWebhook(t, h, "issue_comment", "d-5", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	if len(sink.events) != 1 || sink.events[0].Type != PREventComment {
		t.Fatalf("sink events = %#v, want one PREventComment", sink.events)
	}
	if got := sink.events[0].SenderAuthorAssociation; got != "MEMBER" {
		t.Fatalf("SenderAuthorAssociation = %q, want MEMBER", got)
	}
}

func TestWebhookIssueCommentRejectsUntrustedGitHubActor(t *testing.T) {
	t.Parallel()
	recorder := record.NewFakeRecorder(4)
	h := newWebhookHandler(t, nil, prLoopTestRepo())
	h.Recorder = recorder
	h.Reconciler.Recorder = recorder

	body := []byte(`{
		"action": "created",
		"issue": {
			"number": 42,
			"title": "Add pagination",
			"html_url": "https://github.com/acme/widgets/issues/42",
			"user": {"login": "human"}
		},
		"comment": {
			"body": "@agent please run this",
			"author_association": "NONE",
			"user": {"login": "random"}
		}
	}`)
	rec := postWebhook(t, h, "issue_comment", "d-reject-comment", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	for range 2 {
		select {
		case event := <-recorder.Events:
			if strings.Contains(event, "TriggerActorRejected") && strings.Contains(event, "random") {
				return
			}
		default:
			t.Fatal("missing TriggerActorRejected event")
		}
	}
	t.Fatal("missing TriggerActorRejected event")
}

func TestWebhookClosedIssueCancelsActiveRunWhenEnabled(t *testing.T) {
	t.Parallel()
	gh := prLoopTestRepo()
	gh.Spec.CancelRunsOnIssueClose = true
	run := issueTriggeredRun("run-42", "42", platformv1alpha1.AgentRunPhaseRunning)
	recorder := record.NewFakeRecorder(4)
	h := newWebhookHandler(t, nil, gh, run)
	h.Recorder = recorder

	rec := postWebhook(t, h, "issues", "issue-closed-cancel", []byte(`{"action":"closed","issue":{"number":42}}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}

	got := &platformv1alpha1.AgentRun{}
	if err := h.Client.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: "run-42"}, got); err != nil {
		t.Fatalf("get AgentRun: %v", err)
	}
	if got.Annotations[cancelRequestedAnnotation] != "true" {
		t.Fatalf("cancel annotation = %q, want true", got.Annotations[cancelRequestedAnnotation])
	}
	for range 2 {
		select {
		case event := <-recorder.Events:
			if strings.Contains(event, "IssueClosedRunCancelled") {
				return
			}
		default:
			t.Fatal("missing IssueClosedRunCancelled event")
		}
	}
	t.Fatal("missing IssueClosedRunCancelled event")
}

func TestWebhookClosedIssueDoesNotCancelWhenDisabled(t *testing.T) {
	t.Parallel()
	gh := prLoopTestRepo()
	run := issueTriggeredRun("run-42", "42", platformv1alpha1.AgentRunPhaseRunning)
	h := newWebhookHandler(t, nil, gh, run)

	rec := postWebhook(t, h, "issues", "issue-closed-disabled", []byte(`{"action":"closed","issue":{"number":42}}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}

	got := &platformv1alpha1.AgentRun{}
	if err := h.Client.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: "run-42"}, got); err != nil {
		t.Fatalf("get AgentRun: %v", err)
	}
	if got.Annotations[cancelRequestedAnnotation] != "" {
		t.Fatalf("cancel annotation = %q, want empty", got.Annotations[cancelRequestedAnnotation])
	}
}

func TestWebhookClosedIssueDoesNotCancelTerminalRun(t *testing.T) {
	t.Parallel()
	gh := prLoopTestRepo()
	gh.Spec.CancelRunsOnIssueClose = true
	run := issueTriggeredRun("run-42", "42", platformv1alpha1.AgentRunPhaseSucceeded)
	h := newWebhookHandler(t, nil, gh, run)

	rec := postWebhook(t, h, "issues", "issue-closed-terminal", []byte(`{"action":"closed","issue":{"number":42}}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}

	got := &platformv1alpha1.AgentRun{}
	if err := h.Client.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: "run-42"}, got); err != nil {
		t.Fatalf("get AgentRun: %v", err)
	}
	if got.Annotations[cancelRequestedAnnotation] != "" {
		t.Fatalf("cancel annotation = %q, want empty", got.Annotations[cancelRequestedAnnotation])
	}
}

func TestWebhookIssueCommentBypassesProcessedIssueGuard(t *testing.T) {
	t.Parallel()
	gh := prLoopTestRepo()
	gh.Status.ProcessedIssueIDs = []string{"42"}
	h := newWebhookHandler(t, nil, gh)

	body := []byte(`{
		"action": "created",
		"issue": {
			"number": 42,
			"title": "Add pagination",
			"html_url": "https://github.com/acme/widgets/issues/42",
			"user": {"login": "human"}
		},
		"comment": {
			"id": 9001,
			"body": "@agent please rerun this",
			"author_association": "MEMBER",
			"user": {"login": "human"}
		}
	}`)
	rec := postWebhook(t, h, "issue_comment", "comment-bypass-processed", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}

	run := &platformv1alpha1.AgentRun{}
	if err := h.Client.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: ghIssueName("acme", "widgets", "42-9001")}, run); err != nil {
		t.Fatalf("get comment AgentRun: %v", err)
	}
	if run.Spec.Trigger.ExternalRef == nil || run.Spec.Trigger.ExternalRef.ID != "42-9001" {
		t.Fatalf("ExternalRef = %#v, want ID 42-9001", run.Spec.Trigger.ExternalRef)
	}
}

func issueTriggeredRun(name, issueID string, phase platformv1alpha1.AgentRunPhase) *platformv1alpha1.AgentRun {
	return &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default", Annotations: map[string]string{}},
		Spec: platformv1alpha1.AgentRunSpec{
			Trigger: platformv1alpha1.TriggerRef{
				Kind: gitHubRepositoryTriggerKind,
				Name: "repo",
				ExternalRef: &platformv1alpha1.ExternalRef{
					ID: issueID,
				},
			},
		},
		Status: platformv1alpha1.AgentRunStatus{Phase: phase},
	}
}

func TestWebhookValidatesSignature(t *testing.T) {
	t.Parallel()
	secret := []byte("hush")
	gh := prLoopTestRepo()
	gh.Spec.WebhookSecret = "hook-secret"
	secretObj := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "hook-secret", Namespace: "default"},
		Data:       map[string][]byte{"secret": secret},
	}

	scheme := prLoopTestScheme(t)
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(core): %v", err)
	}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&triggersv1alpha1.GitHubRepository{}).
		WithObjects(gh, secretObj).
		Build()
	sink := &recordingPRSink{handled: true}
	h := &GitHubWebhookHandler{Client: c, Reconciler: &GitHubRepositoryReconciler{Client: c, Scheme: scheme}, PRSink: sink}

	mux := http.NewServeMux()
	RegisterGitHubWebhookRoutes(mux, h)
	body := []byte(`{"action":"opened","pull_request":{"number":1,"head":{"ref":"x"},"base":{"ref":"main"}}}`)

	send := func(signature string) int {
		req := httptest.NewRequest(http.MethodPost, "/webhooks/github/default/repo", bytes.NewReader(body))
		req.Header.Set("X-GitHub-Event", "pull_request")
		if signature != "" {
			req.Header.Set("X-Hub-Signature-256", signature)
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec.Code
	}

	if code := send("sha256=deadbeef"); code != http.StatusUnauthorized {
		t.Fatalf("invalid signature status = %d, want 401", code)
	}
	if code := send(""); code != http.StatusUnauthorized {
		t.Fatalf("missing signature status = %d, want 401", code)
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	if code := send(fmt.Sprintf("sha256=%s", hex.EncodeToString(mac.Sum(nil)))); code != http.StatusOK {
		t.Fatalf("valid signature status = %d, want 200", code)
	}
	if len(sink.events) != 1 {
		t.Fatalf("sink events = %d, want 1 (only the signed delivery)", len(sink.events))
	}
}

func TestDeliveryDeduperBounds(t *testing.T) {
	t.Parallel()
	d := newDeliveryDeduper()
	if d.isDuplicate("") {
		t.Fatal("empty delivery ID must never be a duplicate")
	}
	if d.isDuplicate("a") {
		t.Fatal("first sighting must not be a duplicate")
	}
	if !d.isDuplicate("a") {
		t.Fatal("second sighting must be a duplicate")
	}
}
