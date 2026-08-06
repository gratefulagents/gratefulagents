package triggers

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func securityScanEventTestScan() *triggersv1alpha1.SecurityScan {
	scan := securityScanTestScan()
	scan.Spec.Triggers = &triggersv1alpha1.SecurityScanTriggers{
		RepositoryRef: &triggersv1alpha1.SecurityResourceRef{Name: "widget-repo"},
		OnPullRequest: true,
		OnPush:        true,
	}
	return scan
}

func securityScanEventTestRepo(namespace string) *triggersv1alpha1.GitHubRepository {
	return &triggersv1alpha1.GitHubRepository{
		ObjectMeta: metav1.ObjectMeta{Name: "widget-repo", Namespace: namespace},
		Spec: triggersv1alpha1.GitHubRepositorySpec{
			Owner:             "acme",
			Repo:              "widget",
			GitHubTokenSecret: "gh-token",
		},
	}
}

func stampScanEvent(t *testing.T, k8sClient client.Client, scan *triggersv1alpha1.SecurityScan, ev SecurityScanTriggerEvent) {
	t.Helper()
	payload, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}
	if err := retrySecurityScanPatch(context.Background(), k8sClient, client.ObjectKeyFromObject(scan), func(fresh *triggersv1alpha1.SecurityScan) {
		if fresh.Annotations == nil {
			fresh.Annotations = map[string]string{}
		}
		fresh.Annotations[triggersv1alpha1.SecurityScanEventAnnotation] = string(payload)
	}); err != nil {
		t.Fatalf("stamp event: %v", err)
	}
}

func testPushEvent() SecurityScanTriggerEvent {
	return SecurityScanTriggerEvent{
		Token:        securityScanEventToken("acme/widget", "push", "abc1234def"),
		Source:       "push",
		Repository:   "acme/widget",
		Revision:     "abc1234def",
		BaseRevision: "0ldbase",
		Branch:       "main",
	}
}

func TestSecurityScanWebhookPushStampsEventAnnotation(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	scan := securityScanEventTestScan()
	scan.Spec.Triggers.Branches = []string{"main", "release/*"}
	gh := securityScanEventTestRepo(scan.Namespace)
	_, k8sClient, _ := newSecurityScanReconciler(t, now, scan, gh)
	handler := &GitHubWebhookHandler{Client: k8sClient}

	payload := []byte(`{
		"ref": "refs/heads/main",
		"before": "1111111111111111111111111111111111111111",
		"after": "2222222222222222222222222222222222222222",
		"repository": {"full_name": "acme/widget"}
	}`)
	if err := handler.dispatchEvent(context.Background(), gh, "push", payload); err != nil {
		t.Fatalf("dispatchEvent(push) error = %v", err)
	}

	updated := getSecurityScan(t, k8sClient, scan)
	raw := updated.Annotations[triggersv1alpha1.SecurityScanEventAnnotation]
	if raw == "" {
		t.Fatal("scan-event annotation not stamped")
	}
	var ev SecurityScanTriggerEvent
	if err := json.Unmarshal([]byte(raw), &ev); err != nil {
		t.Fatalf("annotation payload invalid: %v", err)
	}
	if ev.Source != "push" || ev.Revision != "2222222222222222222222222222222222222222" || ev.Branch != "main" {
		t.Fatalf("event = %+v, want push revision/branch from payload", ev)
	}
	if ev.Token != securityScanEventToken("acme/widget", "push", ev.Revision) {
		t.Fatalf("token = %q, want deterministic token", ev.Token)
	}

	// Redelivery of the same payload stamps the same token (idempotent).
	if err := handler.dispatchEvent(context.Background(), gh, "push", payload); err != nil {
		t.Fatalf("redelivery error = %v", err)
	}
	if again := getSecurityScan(t, k8sClient, scan).Annotations[triggersv1alpha1.SecurityScanEventAnnotation]; again != raw {
		t.Fatalf("redelivery changed annotation: %q vs %q", again, raw)
	}
}

func TestSecurityScanWebhookPushRespectsBranchFilterAndDeletions(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	scan := securityScanEventTestScan()
	scan.Spec.Triggers.Branches = []string{"main"}
	gh := securityScanEventTestRepo(scan.Namespace)
	_, k8sClient, _ := newSecurityScanReconciler(t, now, scan, gh)
	handler := &GitHubWebhookHandler{Client: k8sClient}

	otherBranch := []byte(`{"ref": "refs/heads/dev", "after": "3333333333333333333333333333333333333333", "repository": {"full_name": "acme/widget"}}`)
	deletion := []byte(`{"ref": "refs/heads/main", "deleted": true, "after": "0000000000000000000000000000000000000000", "repository": {"full_name": "acme/widget"}}`)
	for _, payload := range [][]byte{otherBranch, deletion} {
		if err := handler.dispatchEvent(context.Background(), gh, "push", payload); err != nil {
			t.Fatalf("dispatchEvent error = %v", err)
		}
	}
	if raw := getSecurityScan(t, k8sClient, scan).Annotations[triggersv1alpha1.SecurityScanEventAnnotation]; raw != "" {
		t.Fatalf("annotation stamped for filtered branch or deletion: %q", raw)
	}
}

func TestSecurityScanWebhookPullRequestStampsEventAndDetectsFork(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	scan := securityScanEventTestScan()
	gh := securityScanEventTestRepo(scan.Namespace)
	_, k8sClient, _ := newSecurityScanReconciler(t, now, scan, gh)
	handler := &GitHubWebhookHandler{Client: k8sClient}

	payload := []byte(`{
		"action": "opened",
		"repository": {"full_name": "acme/widget"},
		"pull_request": {
			"number": 42,
			"title": "add feature",
			"html_url": "https://github.com/acme/widget/pull/42",
			"head": {"ref": "feature", "sha": "feedbeef01", "repo": {"full_name": "mallory/widget"}},
			"base": {"ref": "main", "sha": "ba5eba5e01", "repo": {"full_name": "acme/widget"}}
		}
	}`)
	if err := handler.handlePullRequestEvent(context.Background(), gh, payload); err != nil {
		t.Fatalf("handlePullRequestEvent error = %v", err)
	}

	raw := getSecurityScan(t, k8sClient, scan).Annotations[triggersv1alpha1.SecurityScanEventAnnotation]
	if raw == "" {
		t.Fatal("scan-event annotation not stamped for PR")
	}
	var ev SecurityScanTriggerEvent
	if err := json.Unmarshal([]byte(raw), &ev); err != nil {
		t.Fatalf("annotation payload invalid: %v", err)
	}
	if ev.Source != "pull_request" || ev.Revision != "feedbeef01" || ev.BaseRevision != "ba5eba5e01" || ev.PRNumber != 42 {
		t.Fatalf("event = %+v, want PR head/base SHAs from payload", ev)
	}
	if !ev.Fork || ev.HeadRepo != "mallory/widget" {
		t.Fatalf("event = %+v, want fork detected", ev)
	}
}

func TestSecurityScanEventReconcileCreatesRunWithStampedRevision(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	scan := securityScanEventTestScan()
	reconciler, k8sClient, stateStore := newSecurityScanReconciler(t, now, scan)
	ev := testPushEvent()
	stampScanEvent(t, k8sClient, scan, ev)

	if _, err := reconciler.Reconcile(context.Background(), securityScanRequest(scan)); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	runs := securityScanRuns(t, k8sClient, scan.Namespace)
	if len(runs) != 1 {
		t.Fatalf("AgentRuns = %d, want 1", len(runs))
	}
	if got := runs[0].Annotations[triggersv1alpha1.SecurityScanRevisionAnnotation]; got != ev.Revision {
		t.Fatalf("run revision annotation = %q, want platform-stamped %q", got, ev.Revision)
	}
	if runs[0].Spec.Repository.Revision != ev.Revision {
		t.Fatalf("run spec revision = %q, want %q", runs[0].Spec.Repository.Revision, ev.Revision)
	}
	updated := getSecurityScan(t, k8sClient, scan)
	if updated.Status.LastEventToken != ev.Token || updated.Status.LastEventRevision != ev.Revision {
		t.Fatalf("status token/revision = %q/%q, want %q/%q",
			updated.Status.LastEventToken, updated.Status.LastEventRevision, ev.Token, ev.Revision)
	}
	if updated.Status.EventRunsCreated != 1 || updated.Status.RunsCreated != 1 {
		t.Fatalf("EventRunsCreated/RunsCreated = %d/%d, want 1/1", updated.Status.EventRunsCreated, updated.Status.RunsCreated)
	}
	assertSecurityScanCondition(t, updated, metav1.ConditionTrue, "EventRunStarted")

	sessionID := stateStore.sessions[scan.Namespace+"/"+runs[0].Name]
	messages := stateStore.messages[sessionID]
	if len(messages) != 1 || !strings.Contains(messages[0].Content, "Trigger event") || !strings.Contains(messages[0].Content, ev.Revision) {
		t.Fatalf("seed prompt missing trigger-event section: %#v", messages)
	}
}

func TestSecurityScanEventReconcileIsIdempotentAcrossReplays(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	scan := securityScanEventTestScan()
	reconciler, k8sClient, _ := newSecurityScanReconciler(t, now, scan)
	ev := testPushEvent()
	stampScanEvent(t, k8sClient, scan, ev)

	for i := range 3 {
		if _, err := reconciler.Reconcile(context.Background(), securityScanRequest(scan)); err != nil {
			t.Fatalf("Reconcile() #%d error = %v", i, err)
		}
	}
	// A webhook redelivery re-stamps the identical event after the token was
	// consumed; it must not create a second run either.
	stampScanEvent(t, k8sClient, scan, ev)
	if _, err := reconciler.Reconcile(context.Background(), securityScanRequest(scan)); err != nil {
		t.Fatalf("Reconcile() after re-stamp error = %v", err)
	}

	if runs := securityScanRuns(t, k8sClient, scan.Namespace); len(runs) != 1 {
		t.Fatalf("AgentRuns = %d, want exactly 1 across replays", len(runs))
	}
	if updated := getSecurityScan(t, k8sClient, scan); updated.Status.EventRunsCreated != 1 {
		t.Fatalf("EventRunsCreated = %d, want 1", updated.Status.EventRunsCreated)
	}
}

func TestSecurityScanEventReconcileSkipsForkWithoutAllowForks(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	scan := securityScanEventTestScan()
	// The one-shot generation run already happened; only the event is pending.
	scan.Status.ObservedGeneration = scan.Generation
	scan.Status.LastRunName = "secscan-prior-run"
	reconciler, k8sClient, _ := newSecurityScanReconciler(t, now, scan)
	ev := testPushEvent()
	ev.Source = "pull_request"
	ev.Fork = true
	ev.HeadRepo = "mallory/widget"
	stampScanEvent(t, k8sClient, scan, ev)

	if _, err := reconciler.Reconcile(context.Background(), securityScanRequest(scan)); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	if runs := securityScanRuns(t, k8sClient, scan.Namespace); len(runs) != 0 {
		t.Fatalf("AgentRuns = %d, want 0 for fork PR", len(runs))
	}
	updated := getSecurityScan(t, k8sClient, scan)
	assertSecurityScanCondition(t, updated, metav1.ConditionFalse, "ForkPullRequestSkipped")
	if updated.Status.LastEventToken != ev.Token {
		t.Fatalf("fork skip must consume the token, got %q", updated.Status.LastEventToken)
	}

	// The consumed token never re-processes.
	if _, err := reconciler.Reconcile(context.Background(), securityScanRequest(scan)); err != nil {
		t.Fatalf("second Reconcile() error = %v", err)
	}
	if runs := securityScanRuns(t, k8sClient, scan.Namespace); len(runs) != 0 {
		t.Fatalf("AgentRuns = %d, want 0", len(runs))
	}
}

func TestSecurityScanEventAllowForksStripsGitHubCredential(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	scan := securityScanEventTestScan()
	scan.Spec.Triggers.AllowForks = true
	scan.Spec.Defaults.Secrets.GithubToken = "repo-write-token"
	reconciler, k8sClient, _ := newSecurityScanReconciler(t, now, scan)
	ev := testPushEvent()
	ev.Source = "pull_request"
	ev.Fork = true
	ev.HeadRepo = "mallory/widget"
	stampScanEvent(t, k8sClient, scan, ev)

	if _, err := reconciler.Reconcile(context.Background(), securityScanRequest(scan)); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	runs := securityScanRuns(t, k8sClient, scan.Namespace)
	if len(runs) != 1 {
		t.Fatalf("AgentRuns = %d, want 1", len(runs))
	}
	if runs[0].Spec.Secrets == nil || runs[0].Spec.Secrets.GitHubTokenSecret != "" {
		t.Fatalf("fork run GitHubTokenSecret = %q, want stripped", runs[0].Spec.Secrets.GitHubTokenSecret)
	}
}

func TestSecurityScanEventSuspendedScanConsumesNothing(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	scan := securityScanEventTestScan()
	scan.Spec.Suspend = true
	reconciler, k8sClient, _ := newSecurityScanReconciler(t, now, scan)
	ev := testPushEvent()
	stampScanEvent(t, k8sClient, scan, ev)

	if _, err := reconciler.Reconcile(context.Background(), securityScanRequest(scan)); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if runs := securityScanRuns(t, k8sClient, scan.Namespace); len(runs) != 0 {
		t.Fatalf("AgentRuns = %d, want 0 while suspended", len(runs))
	}
	updated := getSecurityScan(t, k8sClient, scan)
	if updated.Status.LastEventToken != "" {
		t.Fatalf("suspended scan consumed the event token %q", updated.Status.LastEventToken)
	}

	// Resuming processes the pending event.
	if err := retrySecurityScanPatch(context.Background(), k8sClient, client.ObjectKeyFromObject(scan), func(fresh *triggersv1alpha1.SecurityScan) {
		fresh.Spec.Suspend = false
	}); err != nil {
		t.Fatalf("resume: %v", err)
	}
	if _, err := reconciler.Reconcile(context.Background(), securityScanRequest(scan)); err != nil {
		t.Fatalf("Reconcile() after resume error = %v", err)
	}
	if runs := securityScanRuns(t, k8sClient, scan.Namespace); len(runs) != 1 {
		t.Fatalf("AgentRuns = %d, want 1 after resume", len(runs))
	}
}

func TestSecurityScanEventRespectsConcurrencyForbid(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	scan := securityScanEventTestScan()
	active := securityScanPriorRun(scan, platformv1alpha1.AgentRunPhaseRunning)
	reconciler, k8sClient, _ := newSecurityScanReconciler(t, now, scan, active)
	ev := testPushEvent()
	stampScanEvent(t, k8sClient, scan, ev)

	if _, err := reconciler.Reconcile(context.Background(), securityScanRequest(scan)); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	runs := securityScanRuns(t, k8sClient, scan.Namespace)
	if len(runs) != 1 { // only the pre-existing active run
		t.Fatalf("AgentRuns = %d, want 1 (no new run under Forbid)", len(runs))
	}
	updated := getSecurityScan(t, k8sClient, scan)
	assertSecurityScanCondition(t, updated, metav1.ConditionFalse, "ConcurrencyBlocked")
	if updated.Status.LastEventToken != ev.Token {
		t.Fatalf("blocked event must consume its token, got %q", updated.Status.LastEventToken)
	}
}

func TestSecurityScanEventConcurrencyAllowCreatesRun(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	scan := securityScanEventTestScan()
	scan.Spec.ConcurrencyPolicy = triggersv1alpha1.SecurityScanConcurrencyAllow
	active := securityScanPriorRun(scan, platformv1alpha1.AgentRunPhaseRunning)
	reconciler, k8sClient, _ := newSecurityScanReconciler(t, now, scan, active)
	stampScanEvent(t, k8sClient, scan, testPushEvent())

	if _, err := reconciler.Reconcile(context.Background(), securityScanRequest(scan)); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if runs := securityScanRuns(t, k8sClient, scan.Namespace); len(runs) != 2 {
		t.Fatalf("AgentRuns = %d, want 2 under Allow", len(runs))
	}
}

type fakeSecurityScanDiffLister struct {
	files []string
	err   error
	base  string
	head  string
}

func (f *fakeSecurityScanDiffLister) ListChangedFiles(_ context.Context, _ *triggersv1alpha1.GitHubRepository, base, head string) ([]string, error) {
	f.base, f.head = base, head
	return f.files, f.err
}

func TestSecurityScanEventDiffScopeListsChangedFiles(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	scan := securityScanEventTestScan()
	scan.Spec.Triggers.DiffScope = true
	gh := securityScanEventTestRepo(scan.Namespace)
	reconciler, k8sClient, stateStore := newSecurityScanReconciler(t, now, scan, gh)
	lister := &fakeSecurityScanDiffLister{files: []string{"internal/auth/login.go", "web/form.tsx"}}
	reconciler.DiffLister = lister
	ev := testPushEvent()
	stampScanEvent(t, k8sClient, scan, ev)

	if _, err := reconciler.Reconcile(context.Background(), securityScanRequest(scan)); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if lister.base != ev.BaseRevision || lister.head != ev.Revision {
		t.Fatalf("diff compared %s..%s, want %s..%s", lister.base, lister.head, ev.BaseRevision, ev.Revision)
	}
	runs := securityScanRuns(t, k8sClient, scan.Namespace)
	if len(runs) != 1 {
		t.Fatalf("AgentRuns = %d, want 1", len(runs))
	}
	prompt := stateStore.messages[stateStore.sessions[scan.Namespace+"/"+runs[0].Name]][0].Content
	if !strings.Contains(prompt, "internal/auth/login.go") || !strings.Contains(prompt, "Diff scope") {
		t.Fatalf("prompt missing diff-scope file list:\n%s", prompt)
	}
}

func TestSecurityScanEventDiffScopeFallsBackToFullScan(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	scan := securityScanEventTestScan()
	scan.Spec.Triggers.DiffScope = true
	gh := securityScanEventTestRepo(scan.Namespace)
	reconciler, k8sClient, stateStore := newSecurityScanReconciler(t, now, scan, gh)
	reconciler.DiffLister = &fakeSecurityScanDiffLister{err: fmt.Errorf("merge base unavailable")}
	ev := testPushEvent()
	stampScanEvent(t, k8sClient, scan, ev)

	if _, err := reconciler.Reconcile(context.Background(), securityScanRequest(scan)); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	runs := securityScanRuns(t, k8sClient, scan.Namespace)
	if len(runs) != 1 {
		t.Fatalf("AgentRuns = %d, want 1 (fallback still scans)", len(runs))
	}
	prompt := stateStore.messages[stateStore.sessions[scan.Namespace+"/"+runs[0].Name]][0].Content
	if !strings.Contains(prompt, "FALLBACK: scan the FULL repository") {
		t.Fatalf("prompt missing explicit fallback statement:\n%s", prompt)
	}
	updated := getSecurityScan(t, k8sClient, scan)
	ready := findSecurityScanReadyCondition(updated)
	if ready == nil || !strings.Contains(ready.Message, "fell back to a full-repository scan") {
		t.Fatalf("Ready condition = %+v, want fallback stated in message", ready)
	}
}

func findSecurityScanReadyCondition(scan *triggersv1alpha1.SecurityScan) *metav1.Condition {
	for i := range scan.Status.Conditions {
		if scan.Status.Conditions[i].Type == triggersv1alpha1.ConditionSecurityScanReady {
			return &scan.Status.Conditions[i]
		}
	}
	return nil
}

func TestSecurityScanInvalidTriggersReportInvalidSpec(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	scan := securityScanTestScan()
	scan.Spec.Triggers = &triggersv1alpha1.SecurityScanTriggers{OnPullRequest: true}
	reconciler, k8sClient, _ := newSecurityScanReconciler(t, now, scan)

	if _, err := reconciler.Reconcile(context.Background(), securityScanRequest(scan)); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if runs := securityScanRuns(t, k8sClient, scan.Namespace); len(runs) != 0 {
		t.Fatalf("AgentRuns = %d, want 0", len(runs))
	}
	assertSecurityScanCondition(t, getSecurityScan(t, k8sClient, scan), metav1.ConditionFalse, securityScanReasonInvalidSpec)
}

func TestSecurityScanBranchMatches(t *testing.T) {
	cases := []struct {
		patterns []string
		branch   string
		want     bool
	}{
		{nil, "anything", true},
		{[]string{"main"}, "main", true},
		{[]string{"main"}, "dev", false},
		{[]string{"release/*"}, "release/v1", true},
		{[]string{"release/*"}, "release/v1/hotfix", true}, // prefix match
		{[]string{"feature-*"}, "feature-login", true},
		{[]string{"feature-*"}, "bugfix-login", false},
	}
	for _, tc := range cases {
		if got := securityScanBranchMatches(tc.patterns, tc.branch); got != tc.want {
			t.Errorf("securityScanBranchMatches(%v, %q) = %t, want %t", tc.patterns, tc.branch, got, tc.want)
		}
	}
}
