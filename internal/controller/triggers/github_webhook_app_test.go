package triggers

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func newAppWebhookHandler(t *testing.T, sink PullRequestEventSink, secretName string, objs ...client.Object) *GitHubAppWebhookHandler {
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
	return &GitHubAppWebhookHandler{
		Inner: &GitHubWebhookHandler{
			Client:     c,
			Reconciler: &GitHubRepositoryReconciler{Client: c, Scheme: scheme},
			PRSink:     sink,
		},
		WebhookSecretNamespace: "platform-ns",
		WebhookSecretName:      secretName,
		AllowUnauthenticated:   secretName == "",
	}
}

func postAppWebhook(t *testing.T, h *GitHubAppWebhookHandler, event, delivery string, body []byte, sign []byte) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	RegisterGitHubAppWebhookRoutes(mux, h)
	req := httptest.NewRequest(http.MethodPost, "/webhooks/github/app", bytes.NewReader(body))
	req.Header.Set("X-GitHub-Event", event)
	if delivery != "" {
		req.Header.Set("X-GitHub-Delivery", delivery)
	}
	if sign != nil {
		mac := hmac.New(sha256.New, sign)
		mac.Write(body)
		req.Header.Set("X-Hub-Signature-256", fmt.Sprintf("sha256=%s", hex.EncodeToString(mac.Sum(nil))))
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func appPROpenedBody(repoFullName string) []byte {
	return fmt.Appendf(nil, `{
		"action": "opened",
		"repository": {"full_name": %q},
		"pull_request": {
			"number": 42,
			"title": "Add pagination",
			"html_url": "https://github.com/%s/pull/42",
			"user": {"login": "agent-bot"},
			"head": {"ref": "gh-acme-widgets-7"},
			"base": {"ref": "main"}
		},
		"sender": {"login": "agent-bot"}
	}`, repoFullName, repoFullName)
}

func appWebhookWithEngine(t *testing.T, objs ...client.Object) (*GitHubAppWebhookHandler, client.Client) {
	t.Helper()
	scheme := prLoopTestScheme(t)
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(core): %v", err)
	}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&platformv1alpha1.AgentRun{}, &triggersv1alpha1.GitHubRepository{}).
		WithObjects(objs...).
		Build()
	engine := &PRLoopEngine{Client: c, Scheme: scheme}
	return &GitHubAppWebhookHandler{
		Inner: &GitHubWebhookHandler{
			Client:     c,
			Reconciler: &GitHubRepositoryReconciler{Client: c, Scheme: scheme},
			PRSink:     engine,
		},
		AllowUnauthenticated: true,
	}, c
}

func TestAppWebhookDashboardRunStartsReviewEndToEnd(t *testing.T) {
	t.Parallel()
	gh := prLoopTestRepo()
	impl := prLoopImplementerRun("")
	impl.Spec.Trigger = platformv1alpha1.TriggerRef{Kind: "Dashboard", Name: "chat"}
	h, c := appWebhookWithEngine(t, gh, impl)

	rec := postAppWebhook(t, h, "pull_request", "dashboard-e2e", appPROpenedBody("acme/widgets"), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	reviewer := &platformv1alpha1.AgentRun{}
	name := reviewerRunName(impl.Name, prLoopKey("acme/widgets", 42), 1)
	if err := c.Get(t.Context(), client.ObjectKey{Namespace: impl.Namespace, Name: name}, reviewer); err != nil {
		t.Fatalf("Get(reviewer): %v", err)
	}
}

func TestAppWebhookUnconfiguredRepoStartsFallbackReviewEndToEnd(t *testing.T) {
	t.Parallel()
	impl := prLoopImplementerRun("")
	impl.Annotations[PRLoopOptAnnotation] = PRLoopOptEnabled
	impl.Spec.Trigger = platformv1alpha1.TriggerRef{Kind: "Cron", Name: "nightly"}
	impl.Spec.Model = "openai/gpt-5.5"
	impl.Spec.Image = "cron-worker:v1"
	impl.Spec.Secrets = &platformv1alpha1.AgentRunSecrets{GitHubTokenSecret: "github-token"}
	h, c := appWebhookWithEngine(t, impl)

	rec := postAppWebhook(t, h, "pull_request", "cron-e2e", appPROpenedBody("acme/widgets"), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	reviewer := &platformv1alpha1.AgentRun{}
	name := reviewerRunName(impl.Name, prLoopKey("acme/widgets", 42), 1)
	if err := c.Get(t.Context(), client.ObjectKey{Namespace: impl.Namespace, Name: name}, reviewer); err != nil {
		t.Fatalf("Get(reviewer): %v", err)
	}
	if reviewer.Spec.Model != impl.Spec.Model || reviewer.Spec.Image != impl.Spec.Image {
		t.Fatalf("reviewer model/image = %q/%q, want %q/%q", reviewer.Spec.Model, reviewer.Spec.Image, impl.Spec.Model, impl.Spec.Image)
	}
}

func TestAppWebhookRoutesByRepositoryFullName(t *testing.T) {
	t.Parallel()
	sink := &recordingPRSink{handled: true}
	h := newAppWebhookHandler(t, sink, "", prLoopTestRepo())

	// Case-insensitive match against spec.owner/spec.repo (acme/widgets).
	rec := postAppWebhook(t, h, "pull_request", "ad-1", appPROpenedBody("Acme/Widgets"), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	if len(sink.events) != 1 || sink.events[0].Type != PREventOpened || sink.events[0].Number != 42 {
		t.Fatalf("sink events = %#v, want one opened event", sink.events)
	}
}

func TestAppWebhookRejectsMissingSecretByDefault(t *testing.T) {
	t.Parallel()
	sink := &recordingPRSink{handled: true}
	h := newAppWebhookHandler(t, sink, "", prLoopTestRepo())
	h.AllowUnauthenticated = false

	rec := postAppWebhook(t, h, "pull_request", "missing-app-secret-default", appPROpenedBody("acme/widgets"), nil)
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

func TestAppWebhookAllowsMissingSecretWithOptOut(t *testing.T) {
	t.Parallel()
	sink := &recordingPRSink{handled: true}
	h := newAppWebhookHandler(t, sink, "", prLoopTestRepo())

	rec := postAppWebhook(t, h, "pull_request", "missing-app-secret-opt-out", appPROpenedBody("acme/widgets"), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	if len(sink.events) != 1 {
		t.Fatalf("sink events = %d, want 1", len(sink.events))
	}
}

func TestAppWebhookDispatchesUnonboardedRepoPRsToLoop(t *testing.T) {
	t.Parallel()
	sink := &recordingPRSink{handled: true}
	h := newAppWebhookHandler(t, sink, "", prLoopTestRepo())

	rec := postAppWebhook(t, h, "pull_request", "ad-2", appPROpenedBody("other/repo"), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 for unonboarded repo", rec.Code)
	}
	if len(sink.events) != 1 || sink.events[0].Repository != "other/repo" {
		t.Fatalf("sink events = %#v, want one fallback PR event for other/repo", sink.events)
	}
}

func TestAppWebhookFansOutToAllMatchingCRs(t *testing.T) {
	t.Parallel()
	sink := &recordingPRSink{handled: true}
	second := prLoopTestRepo()
	second.Namespace = "team-b"
	h := newAppWebhookHandler(t, sink, "", prLoopTestRepo(), second)

	rec := postAppWebhook(t, h, "pull_request", "ad-3", appPROpenedBody("acme/widgets"), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	if len(sink.events) != 2 {
		t.Fatalf("sink events = %d, want 2 (one per matching CR)", len(sink.events))
	}
}

func TestAppWebhookIgnoresNonRepositoryEvents(t *testing.T) {
	t.Parallel()
	sink := &recordingPRSink{handled: true}
	h := newAppWebhookHandler(t, sink, "", prLoopTestRepo())

	rec := postAppWebhook(t, h, "ping", "ad-4", []byte(`{"zen":"Keep it logically awesome."}`), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 for ping", rec.Code)
	}
	if len(sink.events) != 0 {
		t.Fatalf("sink events = %d, want 0", len(sink.events))
	}
}

func TestAppWebhookValidatesAppSecret(t *testing.T) {
	t.Parallel()
	secret := []byte("app-hush")
	secretObj := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "gh-app-webhook", Namespace: "platform-ns"},
		Data:       map[string][]byte{"secret": secret},
	}
	sink := &recordingPRSink{handled: true}
	h := newAppWebhookHandler(t, sink, "gh-app-webhook", prLoopTestRepo(), secretObj)

	body := appPROpenedBody("acme/widgets")
	if rec := postAppWebhook(t, h, "pull_request", "ad-5", body, nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unsigned status = %d, want 401", rec.Code)
	}
	if rec := postAppWebhook(t, h, "pull_request", "ad-6", body, []byte("wrong")); rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong-key status = %d, want 401", rec.Code)
	}
	if rec := postAppWebhook(t, h, "pull_request", "ad-7", body, secret); rec.Code != http.StatusOK {
		t.Fatalf("signed status = %d, want 200", rec.Code)
	}
	if len(sink.events) != 1 {
		t.Fatalf("sink events = %d, want 1 (only the signed delivery)", len(sink.events))
	}
}

func TestAppWebhookDeduplicatesDeliveries(t *testing.T) {
	t.Parallel()
	sink := &recordingPRSink{handled: true}
	h := newAppWebhookHandler(t, sink, "", prLoopTestRepo())

	body := appPROpenedBody("acme/widgets")
	for range 2 {
		if rec := postAppWebhook(t, h, "pull_request", "same-app-guid", body, nil); rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
	}
	if len(sink.events) != 1 {
		t.Fatalf("sink events = %d, want 1 (redelivery dropped)", len(sink.events))
	}
}
