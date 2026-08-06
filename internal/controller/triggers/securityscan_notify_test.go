package triggers

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/store"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type notifyTestFindingStore struct {
	store.SecurityFindingStore
	findings   []store.SecurityFindingRecord
	markers    map[string]bool
	claimErr   error
	releaseErr error
	released   []string
}

func newNotifyTestFindingStore(findings ...store.SecurityFindingRecord) *notifyTestFindingStore {
	return &notifyTestFindingStore{findings: findings, markers: map[string]bool{}}
}

func (s *notifyTestFindingStore) ListSecurityFindings(context.Context, store.SecurityFindingFilter) ([]store.SecurityFindingRecord, error) {
	return s.findings, nil
}

func (s *notifyTestFindingStore) SummarizeSecurityFindings(context.Context, string, string, string, bool) (map[string]int32, error) {
	return map[string]int32{}, nil
}

func (s *notifyTestFindingStore) ClaimSecurityNotifications(_ context.Context, namespace, scanName, ruleKey string, fingerprints []string) ([]string, error) {
	if s.claimErr != nil {
		return nil, s.claimErr
	}
	claimed := make([]string, 0, len(fingerprints))
	for _, fp := range fingerprints {
		key := namespace + "/" + scanName + "/" + ruleKey + "/" + fp
		if s.markers[key] {
			continue
		}
		s.markers[key] = true
		claimed = append(claimed, fp)
	}
	return claimed, nil
}

func (s *notifyTestFindingStore) ReleaseSecurityNotifications(_ context.Context, namespace, scanName, ruleKey string, fingerprints []string) error {
	if s.releaseErr != nil {
		return s.releaseErr
	}
	for _, fp := range fingerprints {
		delete(s.markers, namespace+"/"+scanName+"/"+ruleKey+"/"+fp)
		s.released = append(s.released, fp)
	}
	return nil
}

type fakeSecurityScanNotifier struct {
	slackTexts   []string
	slackErr     error
	githubIssues []string
	githubErr    error
	// githubFailAfter delays githubErr until this many issues were created.
	githubFailAfter int
	linearIssues    []string
	linearErr       error
}

func (n *fakeSecurityScanNotifier) SendSlack(_ context.Context, _, text string) error {
	if n.slackErr != nil {
		return n.slackErr
	}
	n.slackTexts = append(n.slackTexts, text)
	return nil
}

func (n *fakeSecurityScanNotifier) CreateGitHubIssue(_ context.Context, _ *triggersv1alpha1.GitHubRepository, title, body string) (string, error) {
	if n.githubErr != nil && len(n.githubIssues) >= n.githubFailAfter {
		return "", n.githubErr
	}
	n.githubIssues = append(n.githubIssues, title+"\n"+body)
	return "https://github.com/acme/widget/issues/1", nil
}

func (n *fakeSecurityScanNotifier) CreateLinearIssue(_ context.Context, _, _, title, body string) (string, error) {
	if n.linearErr != nil {
		return "", n.linearErr
	}
	n.linearIssues = append(n.linearIssues, title+"\n"+body)
	return "https://linear.app/acme/issue/ENG-1", nil
}

func notifyTestFinding(fingerprint, severity, baseline string) store.SecurityFindingRecord {
	return store.SecurityFindingRecord{
		Fingerprint:   fingerprint,
		Title:         "finding " + fingerprint,
		Severity:      severity,
		Status:        store.SecurityFindingStatusOpen,
		BaselineState: baseline,
		FilePath:      "internal/auth/login.go",
		StartLine:     7,
		Description:   "EVIDENCE: secret detail",
	}
}

func securityScanNotifyTestFixture(t *testing.T, findings *notifyTestFindingStore, rules ...triggersv1alpha1.SecurityScanNotificationRule) (*SecurityScanReconciler, *triggersv1alpha1.SecurityScan, *fakeSecurityScanNotifier) {
	t.Helper()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	scan := securityScanEventTestScan()
	scan.Spec.Notifications = rules
	scan.Status.LastRunName = "secscan-nightly-security-ev-abc"
	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: scan.Status.LastRunName, Namespace: scan.Namespace},
		Status:     platformv1alpha1.AgentRunStatus{Phase: platformv1alpha1.AgentRunPhaseSucceeded},
	}
	gh := securityScanEventTestRepo(scan.Namespace)
	webhookSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "slack-webhook", Namespace: scan.Namespace},
		Data:       map[string][]byte{"url": []byte("https://hooks.slack.com/services/T/B/x")},
	}
	linearSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "linear-key", Namespace: scan.Namespace},
		Data:       map[string][]byte{"api-key": []byte("lin_api_key")},
	}
	reconciler, _, _ := newSecurityScanReconciler(t, now, scan, run, gh, webhookSecret, linearSecret)
	notifier := &fakeSecurityScanNotifier{}
	reconciler.Notifier = notifier
	reconciler.Findings = findings
	return reconciler, scan, notifier
}

func slackRule(name string) triggersv1alpha1.SecurityScanNotificationRule {
	return triggersv1alpha1.SecurityScanNotificationRule{
		Name:  name,
		Slack: &triggersv1alpha1.SecurityScanSlackNotification{WebhookSecretRef: "slack-webhook"},
	}
}

func TestNotifyRunFindingsSendsOnceAndSuppressesDuplicates(t *testing.T) {
	findings := newNotifyTestFindingStore(
		notifyTestFinding("fp-1", "critical", store.SecurityFindingBaselineNew),
		notifyTestFinding("fp-2", "high", store.SecurityFindingBaselineRegressed),
	)
	reconciler, scan, notifier := securityScanNotifyTestFixture(t, findings, slackRule("critical-alerts"))

	if retry := reconciler.notifyRunFindings(context.Background(), scan); retry {
		t.Fatal("notifyRunFindings retry = true, want false")
	}
	if len(notifier.slackTexts) != 1 {
		t.Fatalf("slack messages = %d, want 1", len(notifier.slackTexts))
	}
	text := notifier.slackTexts[0]
	if !strings.Contains(text, "finding fp-1") || !strings.Contains(text, "finding fp-2") {
		t.Fatalf("slack text missing findings:\n%s", text)
	}
	if strings.Contains(text, "EVIDENCE") {
		t.Fatalf("slack text leaks evidence:\n%s", text)
	}

	updated := getSecurityScan(t, reconciler.Client, scan)
	if updated.Status.LastNotifications == nil || updated.Status.LastNotifications.Sent != 2 || updated.Status.LastNotifications.LastError != "" {
		t.Fatalf("LastNotifications = %+v, want 2 sent", updated.Status.LastNotifications)
	}

	// Re-evaluating the same run never notifies the same finding twice, even
	// when the completed-run marker is wiped (e.g. after a controller
	// restart mid-update): the persisted claims suppress it.
	updated.Status.LastNotifications = nil
	if reconciler.notifyRunFindings(context.Background(), updated) {
		t.Fatal("re-evaluation retry = true, want false")
	}
	if len(notifier.slackTexts) != 1 {
		t.Fatalf("slack messages = %d after re-evaluation, want still 1", len(notifier.slackTexts))
	}
}

func TestNotifyRunFindingsReleasesClaimsOnFailureAndRetries(t *testing.T) {
	findings := newNotifyTestFindingStore(notifyTestFinding("fp-1", "critical", store.SecurityFindingBaselineNew))
	reconciler, scan, notifier := securityScanNotifyTestFixture(t, findings, slackRule("critical-alerts"))
	notifier.slackErr = fmt.Errorf("slack webhook returned 500")

	if !reconciler.notifyRunFindings(context.Background(), scan) {
		t.Fatal("notifyRunFindings retry = false, want true on delivery failure")
	}
	updated := getSecurityScan(t, reconciler.Client, scan)
	if updated.Status.LastNotifications == nil || !strings.Contains(updated.Status.LastNotifications.LastError, "slack webhook returned 500") {
		t.Fatalf("LastNotifications = %+v, want failure recorded", updated.Status.LastNotifications)
	}
	if len(findings.released) != 1 || findings.released[0] != "fp-1" {
		t.Fatalf("released claims = %v, want the failed fingerprint", findings.released)
	}

	// The retry after recovery sends exactly once.
	notifier.slackErr = nil
	if reconciler.notifyRunFindings(context.Background(), updated) {
		t.Fatal("retry after recovery should succeed")
	}
	if len(notifier.slackTexts) != 1 {
		t.Fatalf("slack messages = %d, want exactly 1 after retry", len(notifier.slackTexts))
	}
	updated = getSecurityScan(t, reconciler.Client, scan)
	if updated.Status.LastNotifications.LastError != "" || updated.Status.LastNotifications.Sent != 1 {
		t.Fatalf("LastNotifications = %+v, want clean retry", updated.Status.LastNotifications)
	}
}

func TestNotifyRunFindingsPartialDeliveryReleasesOnlyUndelivered(t *testing.T) {
	findings := newNotifyTestFindingStore(
		notifyTestFinding("fp-1", "critical", store.SecurityFindingBaselineNew),
		notifyTestFinding("fp-2", "critical", store.SecurityFindingBaselineNew),
	)
	rule := triggersv1alpha1.SecurityScanNotificationRule{
		Name:         "tickets",
		GitHubIssues: &triggersv1alpha1.SecurityScanGitHubIssueNotification{},
	}
	reconciler, scan, notifier := securityScanNotifyTestFixture(t, findings, rule)
	notifier.githubErr = fmt.Errorf("github returned 502")
	notifier.githubFailAfter = 1

	if !reconciler.notifyRunFindings(context.Background(), scan) {
		t.Fatal("notifyRunFindings retry = false, want true on partial delivery failure")
	}
	if len(notifier.githubIssues) != 1 || !strings.Contains(notifier.githubIssues[0], "finding fp-1") {
		t.Fatalf("github issues = %v, want exactly the fp-1 issue", notifier.githubIssues)
	}
	if len(findings.released) != 1 || findings.released[0] != "fp-2" {
		t.Fatalf("released claims = %v, want only the undelivered fp-2", findings.released)
	}

	// The retry after recovery notifies only the undelivered finding: the
	// already-created fp-1 issue is never duplicated.
	notifier.githubErr = nil
	updated := getSecurityScan(t, reconciler.Client, scan)
	if reconciler.notifyRunFindings(context.Background(), updated) {
		t.Fatal("retry after recovery should succeed")
	}
	if len(notifier.githubIssues) != 2 {
		t.Fatalf("github issues = %d after retry, want 2", len(notifier.githubIssues))
	}
	if !strings.Contains(notifier.githubIssues[1], "finding fp-2") || strings.Contains(notifier.githubIssues[1], "finding fp-1") {
		t.Fatalf("retry created the wrong issue:\n%s", notifier.githubIssues[1])
	}
	updated = getSecurityScan(t, reconciler.Client, scan)
	if updated.Status.LastNotifications.Sent != 2 || updated.Status.LastNotifications.LastError != "" {
		t.Fatalf("LastNotifications = %+v, want 2 sent and no error", updated.Status.LastNotifications)
	}
}

func TestNotifyRunFindingsFiltersBySeverityAndBaseline(t *testing.T) {
	findings := newNotifyTestFindingStore(
		notifyTestFinding("fp-low", "medium", store.SecurityFindingBaselineNew),         // below high threshold
		notifyTestFinding("fp-old", "critical", store.SecurityFindingBaselineRecurring), // recurring never notifies
		notifyTestFinding("fp-hit", "critical", store.SecurityFindingBaselineNew),
	)
	rule := slackRule("high-new")
	rule.MinSeverity = "high"
	rule.NotifyOn = "new"
	reconciler, scan, notifier := securityScanNotifyTestFixture(t, findings, rule)

	if reconciler.notifyRunFindings(context.Background(), scan) {
		t.Fatal("retry = true, want false")
	}
	if len(notifier.slackTexts) != 1 {
		t.Fatalf("slack messages = %d, want 1", len(notifier.slackTexts))
	}
	text := notifier.slackTexts[0]
	if !strings.Contains(text, "fp-hit") || strings.Contains(text, "fp-low") || strings.Contains(text, "fp-old") {
		t.Fatalf("wrong findings notified:\n%s", text)
	}
}

func TestNotifyRunFindingsCreatesGitHubAndLinearIssuesPerFinding(t *testing.T) {
	findings := newNotifyTestFindingStore(
		notifyTestFinding("fp-1", "critical", store.SecurityFindingBaselineNew),
		notifyTestFinding("fp-2", "critical", store.SecurityFindingBaselineNew),
	)
	rule := triggersv1alpha1.SecurityScanNotificationRule{
		Name:         "tickets",
		GitHubIssues: &triggersv1alpha1.SecurityScanGitHubIssueNotification{},
		Linear:       &triggersv1alpha1.SecurityScanLinearNotification{APIKeySecretRef: "linear-key", TeamID: "team-1"},
	}
	reconciler, scan, notifier := securityScanNotifyTestFixture(t, findings, rule)

	if reconciler.notifyRunFindings(context.Background(), scan) {
		t.Fatal("retry = true, want false")
	}
	if len(notifier.githubIssues) != 2 || len(notifier.linearIssues) != 2 {
		t.Fatalf("issues = %d github / %d linear, want 2/2", len(notifier.githubIssues), len(notifier.linearIssues))
	}
	for _, body := range append(notifier.githubIssues, notifier.linearIssues...) {
		if strings.Contains(body, "EVIDENCE") {
			t.Fatalf("issue body leaks evidence:\n%s", body)
		}
		if !strings.Contains(body, "internal/auth/login.go:7") {
			t.Fatalf("issue body missing location:\n%s", body)
		}
	}

	// The same run never re-creates issues.
	updated := getSecurityScan(t, reconciler.Client, scan)
	if reconciler.notifyRunFindings(context.Background(), updated) {
		t.Fatal("second evaluation retry = true")
	}
	if len(notifier.githubIssues) != 2 || len(notifier.linearIssues) != 2 {
		t.Fatalf("issues duplicated: %d github / %d linear", len(notifier.githubIssues), len(notifier.linearIssues))
	}
}

func TestNotifyRunFindingsSkipsUnsuccessfulRuns(t *testing.T) {
	findings := newNotifyTestFindingStore(notifyTestFinding("fp-1", "critical", store.SecurityFindingBaselineNew))
	reconciler, scan, notifier := securityScanNotifyTestFixture(t, findings, slackRule("alerts"))
	run := &platformv1alpha1.AgentRun{}
	if err := reconciler.Get(context.Background(), securityScanRunKey(scan), run); err != nil {
		t.Fatalf("get run: %v", err)
	}
	run.Status.Phase = platformv1alpha1.AgentRunPhaseFailed
	if err := reconciler.Status().Update(context.Background(), run); err != nil {
		t.Fatalf("update run: %v", err)
	}

	if reconciler.notifyRunFindings(context.Background(), scan) {
		t.Fatal("retry = true, want false")
	}
	if len(notifier.slackTexts) != 0 {
		t.Fatalf("slack messages = %d, want 0 for failed run", len(notifier.slackTexts))
	}
}

func securityScanRunKey(scan *triggersv1alpha1.SecurityScan) client.ObjectKey {
	return client.ObjectKey{Namespace: scan.Namespace, Name: scan.Status.LastRunName}
}

func (s *notifyTestFindingStore) ApplySecuritySuppressions(context.Context, string, string, []store.SecuritySuppressionRule) (int32, error) {
	return 0, nil
}

func (s *notifyTestFindingStore) ExpireSecuritySuppressions(context.Context, string) (int32, error) {
	return 0, nil
}
