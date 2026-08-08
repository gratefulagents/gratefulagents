package triggers

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/store"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type fakeSecurityCheckPublisher struct {
	checks     []SecurityCheckRun
	publishErr error
	sarifs     []string
	sarifRefs  []string
	sarifErr   error
}

func (p *fakeSecurityCheckPublisher) PublishCheck(_ context.Context, _ *triggersv1alpha1.GitHubRepository, check SecurityCheckRun) (string, error) {
	if p.publishErr != nil {
		return "", p.publishErr
	}
	p.checks = append(p.checks, check)
	return "https://github.com/acme/widget/runs/1", nil
}

func (p *fakeSecurityCheckPublisher) UploadSARIF(_ context.Context, _ *triggersv1alpha1.GitHubRepository, _, ref, sarif string) (string, error) {
	if p.sarifErr != nil {
		return "", p.sarifErr
	}
	p.sarifs = append(p.sarifs, sarif)
	p.sarifRefs = append(p.sarifRefs, ref)
	return "sarif-1", nil
}

type checksTestFindingStore struct {
	securityScanRecordStubStore
	counts   map[string]int32
	findings []store.SecurityFindingRecord
	scanRec  *store.SecurityScanRecord
}

func (s *checksTestFindingStore) SummarizeSecurityFindings(context.Context, string, string, string, bool) (map[string]int32, error) {
	return s.counts, nil
}

func (s *checksTestFindingStore) ListSecurityFindings(context.Context, store.SecurityFindingFilter) ([]store.SecurityFindingRecord, error) {
	return s.findings, nil
}

func (s *checksTestFindingStore) GetSecurityScan(context.Context, string, string) (*store.SecurityScanRecord, error) {
	return s.scanRec, nil
}

type sarifArtifactStore struct {
	store.StateStore
	content string
}

func (s *sarifArtifactStore) GetArtifact(context.Context, uuid.UUID, string) (*store.Artifact, error) {
	return &store.Artifact{Content: s.content}, nil
}

func securityScanChecksTestFixture(t *testing.T) (*SecurityScanReconciler, *triggersv1alpha1.SecurityScan, *fakeSecurityCheckPublisher, *checksTestFindingStore) {
	t.Helper()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	scan := securityScanEventTestScan()
	scan.Spec.Checks = &triggersv1alpha1.SecurityScanChecks{Enabled: true}
	scan.Spec.FailOnSeverity = "critical"
	scan.Status.LastRunName = "secscan-nightly-security-ev-abc"
	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:      scan.Status.LastRunName,
			Namespace: scan.Namespace,
			Annotations: map[string]string{
				triggersv1alpha1.SecurityScanRevisionAnnotation: "abc1234def",
			},
		},
		Status: platformv1alpha1.AgentRunStatus{Phase: platformv1alpha1.AgentRunPhaseSucceeded},
	}
	gh := securityScanEventTestRepo(scan.Namespace)
	reconciler, _, _ := newSecurityScanReconciler(t, now, scan, run, gh)
	publisher := &fakeSecurityCheckPublisher{}
	findings := &checksTestFindingStore{
		counts: map[string]int32{"total": 1, "open": 1, "critical": 1, "open_critical": 1},
		findings: []store.SecurityFindingRecord{{
			Title:        "SQL injection in login handler",
			Severity:     "critical",
			Status:       store.SecurityFindingStatusOpen,
			FilePath:     "internal/auth/login.go",
			StartLine:    42,
			Description:  "EVIDENCE: raw SQL concatenation with attacker input",
			Impact:       "IMPACT: full database read",
			AttackVector: "POC: ' OR 1=1 --",
		}},
	}
	reconciler.CheckPublisher = publisher
	reconciler.Findings = findings
	return reconciler, scan, publisher, findings
}

func TestSecurityScanCheckConclusionMapping(t *testing.T) {
	open := map[string]int32{"critical": 1, "high": 2}
	cases := []struct {
		phase  platformv1alpha1.AgentRunPhase
		failOn string
		open   map[string]int32
		want   string
	}{
		{platformv1alpha1.AgentRunPhaseFailed, "critical", open, "neutral"},
		{platformv1alpha1.AgentRunPhaseCancelled, "", open, "neutral"},
		{platformv1alpha1.AgentRunPhaseSucceeded, "", open, "neutral"},
		{platformv1alpha1.AgentRunPhaseSucceeded, "critical", open, "failure"},
		{platformv1alpha1.AgentRunPhaseSucceeded, "high", map[string]int32{"medium": 3}, "success"},
		{platformv1alpha1.AgentRunPhaseSucceeded, "high", map[string]int32{}, "success"},
	}
	for _, tc := range cases {
		got, _ := securityScanCheckConclusion(tc.phase, tc.failOn, tc.open)
		if got != tc.want {
			t.Errorf("conclusion(%s, %q, %v) = %q, want %q", tc.phase, tc.failOn, tc.open, got, tc.want)
		}
	}
}

func TestPublishRunCheckDefaultSummaryContainsNoFindingDetails(t *testing.T) {
	reconciler, scan, publisher, _ := securityScanChecksTestFixture(t)

	if retry := reconciler.publishRunCheck(context.Background(), scan); retry {
		t.Fatal("publishRunCheck retry = true, want false on success")
	}
	if len(publisher.checks) != 1 {
		t.Fatalf("published checks = %d, want 1", len(publisher.checks))
	}
	check := publisher.checks[0]
	if check.Revision != "abc1234def" {
		t.Fatalf("check revision = %q, want the run's stamped revision", check.Revision)
	}
	if check.Conclusion != "failure" {
		t.Fatalf("check conclusion = %q, want failure (open critical with failOnSeverity=critical)", check.Conclusion)
	}
	for _, secret := range []string{"SQL injection", "login.go", "EVIDENCE", "IMPACT", "POC", "OR 1=1"} {
		if strings.Contains(check.Summary, secret) || strings.Contains(check.Title, secret) {
			t.Fatalf("default check output leaks finding detail %q:\n%s", secret, check.Summary)
		}
	}
	if !strings.Contains(check.Summary, "| critical | 1 |") {
		t.Fatalf("summary missing severity counts:\n%s", check.Summary)
	}
	if !strings.Contains(check.Summary, "/security/"+scan.Namespace) {
		t.Fatalf("summary missing dashboard link:\n%s", check.Summary)
	}

	updated := getSecurityScan(t, reconciler.Client, scan)
	lastCheck := updated.Status.LastCheck
	if lastCheck == nil || lastCheck.Error != "" || lastCheck.Conclusion != "failure" || lastCheck.URL == "" {
		t.Fatalf("LastCheck = %+v, want recorded successful publish", lastCheck)
	}
}

func TestPublishRunCheckOptInSummariesIncludeTitleAndLocationOnly(t *testing.T) {
	reconciler, scan, publisher, _ := securityScanChecksTestFixture(t)
	scan.Spec.Checks.IncludeFindingSummaries = true

	if reconciler.publishRunCheck(context.Background(), scan) {
		t.Fatal("publishRunCheck retry = true, want false")
	}
	summary := publisher.checks[0].Summary
	if !strings.Contains(summary, "SQL injection in login handler") || !strings.Contains(summary, "internal/auth/login.go:42") {
		t.Fatalf("opt-in summary missing title/location:\n%s", summary)
	}
	for _, secret := range []string{"EVIDENCE", "IMPACT", "POC", "OR 1=1"} {
		if strings.Contains(summary, secret) {
			t.Fatalf("opt-in summary leaks evidence %q:\n%s", secret, summary)
		}
	}
}

func TestPublishRunCheckRetriesFailuresWithoutCorruptingState(t *testing.T) {
	reconciler, scan, publisher, _ := securityScanChecksTestFixture(t)
	publisher.publishErr = fmt.Errorf("boom: 502 from api.github.com")

	if !reconciler.publishRunCheck(context.Background(), scan) {
		t.Fatal("publishRunCheck retry = false, want true on failure")
	}
	updated := getSecurityScan(t, reconciler.Client, scan)
	if updated.Status.LastCheck == nil || !strings.Contains(updated.Status.LastCheck.Error, "boom") {
		t.Fatalf("LastCheck = %+v, want error recorded", updated.Status.LastCheck)
	}
	if updated.Status.LastRunName != scan.Status.LastRunName {
		t.Fatalf("LastRunName corrupted: %q", updated.Status.LastRunName)
	}

	// The next reconcile retries and clears the error.
	publisher.publishErr = nil
	if reconciler.publishRunCheck(context.Background(), updated) {
		t.Fatal("publishRunCheck retry = true after publisher recovered")
	}
	updated = getSecurityScan(t, reconciler.Client, scan)
	if updated.Status.LastCheck.Error != "" || updated.Status.LastCheck.URL == "" {
		t.Fatalf("LastCheck = %+v, want successful retry", updated.Status.LastCheck)
	}
	if len(publisher.checks) != 1 {
		t.Fatalf("published checks = %d, want 1", len(publisher.checks))
	}
}

func TestPublishRunCheckIdempotentAndRepublishesAfterTriage(t *testing.T) {
	reconciler, scan, publisher, findings := securityScanChecksTestFixture(t)

	if reconciler.publishRunCheck(context.Background(), scan) {
		t.Fatal("first publish should succeed")
	}
	updated := getSecurityScan(t, reconciler.Client, scan)
	if reconciler.publishRunCheck(context.Background(), updated) {
		t.Fatal("second publish should be a no-op")
	}
	if len(publisher.checks) != 1 {
		t.Fatalf("published checks = %d, want 1 (identical state must not republish)", len(publisher.checks))
	}

	// Triage closes the critical finding: the conclusion flips and the check
	// is re-published.
	findings.counts = map[string]int32{"total": 1, "open": 0}
	updated = getSecurityScan(t, reconciler.Client, scan)
	if reconciler.publishRunCheck(context.Background(), updated) {
		t.Fatal("triage republish should succeed")
	}
	if len(publisher.checks) != 2 {
		t.Fatalf("published checks = %d, want 2 after triage", len(publisher.checks))
	}
	if publisher.checks[1].Conclusion != "success" {
		t.Fatalf("post-triage conclusion = %q, want success", publisher.checks[1].Conclusion)
	}
}

func TestPublishRunCheckSkipsRunsWithoutRevision(t *testing.T) {
	reconciler, scan, publisher, _ := securityScanChecksTestFixture(t)
	stored := &platformv1alpha1.AgentRun{}
	if err := reconciler.Get(context.Background(), client.ObjectKey{Namespace: scan.Namespace, Name: scan.Status.LastRunName}, stored); err != nil {
		t.Fatalf("get run: %v", err)
	}
	stored.Annotations = nil
	if err := reconciler.Update(context.Background(), stored); err != nil {
		t.Fatalf("update run: %v", err)
	}

	if reconciler.publishRunCheck(context.Background(), scan) {
		t.Fatal("retry = true, want false")
	}
	if len(publisher.checks) != 0 {
		t.Fatalf("published checks = %d, want 0 without a stamped revision", len(publisher.checks))
	}
}

func TestPublishRunCheckUploadsSARIFWhenOptedIn(t *testing.T) {
	reconciler, scan, publisher, findings := securityScanChecksTestFixture(t)
	scan.Spec.Checks.UploadSARIF = true
	sessionID := uuid.New()
	findings.scanRec = &store.SecurityScanRecord{SessionID: &sessionID}
	reconciler.StateStore = &sarifArtifactStore{content: `{"version":"2.1.0"}`}

	if reconciler.publishRunCheck(context.Background(), scan) {
		t.Fatal("publishRunCheck retry = true, want false")
	}
	if len(publisher.sarifs) != 1 || publisher.sarifs[0] != `{"version":"2.1.0"}` {
		t.Fatalf("SARIF uploads = %v, want the stored artifact", publisher.sarifs)
	}
	if publisher.sarifRefs[0] != "refs/heads/main" {
		t.Fatalf("SARIF ref = %q, want refs/heads/main", publisher.sarifRefs[0])
	}
	updated := getSecurityScan(t, reconciler.Client, scan)
	if updated.Status.LastCheck == nil || !updated.Status.LastCheck.SARIFUploaded {
		t.Fatalf("LastCheck = %+v, want SARIFUploaded", updated.Status.LastCheck)
	}
}

func TestPublishRunCheckUsesPolicyPackFailOnSeverity(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	scan := securityScanEventTestScan()
	scan.Spec.Checks = &triggersv1alpha1.SecurityScanChecks{Enabled: true}
	scan.Spec.FailOnSeverity = ""
	scan.Spec.PolicyPackRef = &triggersv1alpha1.SecurityResourceRef{Name: "org-policy"}
	scan.Status.LastRunName = "secscan-nightly-security-ev-abc"
	pack := &triggersv1alpha1.SecurityPolicyPack{
		ObjectMeta: metav1.ObjectMeta{Name: "org-policy", Namespace: scan.Namespace},
		Spec:       triggersv1alpha1.SecurityPolicyPackSpec{FailOnSeverity: "high"},
	}
	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:      scan.Status.LastRunName,
			Namespace: scan.Namespace,
			Annotations: map[string]string{
				triggersv1alpha1.SecurityScanRevisionAnnotation: "abc1234def",
			},
		},
		Status: platformv1alpha1.AgentRunStatus{Phase: platformv1alpha1.AgentRunPhaseSucceeded},
	}
	gh := securityScanEventTestRepo(scan.Namespace)
	reconciler, _, _ := newSecurityScanReconciler(t, now, scan, run, gh, pack)
	publisher := &fakeSecurityCheckPublisher{}
	reconciler.CheckPublisher = publisher
	reconciler.Findings = &checksTestFindingStore{
		counts: map[string]int32{"total": 1, "open": 1, "high": 1, "open_high": 1},
	}

	if reconciler.publishRunCheck(context.Background(), scan) {
		t.Fatal("publishRunCheck retry = true, want false")
	}
	if len(publisher.checks) != 1 {
		t.Fatalf("published checks = %d, want 1", len(publisher.checks))
	}
	check := publisher.checks[0]
	if check.Conclusion != "failure" {
		t.Fatalf("check conclusion = %q, want failure (open high with pack failOnSeverity=high)", check.Conclusion)
	}
	if !strings.Contains(check.Title, `"high"`) {
		t.Fatalf("check title = %q, want the pack threshold mentioned", check.Title)
	}
}

func TestPublishRunCheckLinksToRegisteredScanDetailRoute(t *testing.T) {
	reconciler, scan, publisher, _ := securityScanChecksTestFixture(t)
	reconciler.DashboardBaseURL = "https://dash.example.com/"

	if reconciler.publishRunCheck(context.Background(), scan) {
		t.Fatal("publishRunCheck retry = true, want false")
	}
	check := publisher.checks[0]
	// The frontend registers scan detail at /security/:namespace/:runName.
	wantURL := "https://dash.example.com/security/" + scan.Namespace + "/" + scan.Status.LastRunName
	if check.DetailsURL != wantURL {
		t.Fatalf("DetailsURL = %q, want %q", check.DetailsURL, wantURL)
	}
	if !strings.Contains(check.Summary, wantURL) {
		t.Fatalf("summary missing run detail link %q:\n%s", wantURL, check.Summary)
	}
	if strings.Contains(check.Summary, "?scan=") {
		t.Fatalf("summary uses a stale query-param path:\n%s", check.Summary)
	}

	bare := securityScanCheckSummary(scan, scan.Status.LastRunName, nil, nil, "")
	wantPath := "`/security/" + scan.Namespace + "/" + scan.Status.LastRunName + "`"
	if !strings.Contains(bare, wantPath) {
		t.Fatalf("summary without base URL missing %s:\n%s", wantPath, bare)
	}
}

func TestPublishRunCheckDisabledDoesNothing(t *testing.T) {
	reconciler, scan, publisher, _ := securityScanChecksTestFixture(t)
	scan.Spec.Checks = nil
	if reconciler.publishRunCheck(context.Background(), scan) {
		t.Fatal("retry = true, want false")
	}
	if len(publisher.checks) != 0 {
		t.Fatalf("published checks = %d, want 0 when disabled", len(publisher.checks))
	}
}

func (s *checksTestFindingStore) ApplySecuritySuppressions(context.Context, string, string, []store.SecuritySuppressionRule) (int32, error) {
	return 0, nil
}

func (s *checksTestFindingStore) ExpireSecuritySuppressions(context.Context, string) (int32, error) {
	return 0, nil
}

// multiRunFindingStore maps run names to sessions so multi-sink executions
// can resolve each run's own SARIF artifact.
type multiRunFindingStore struct {
	securityScanRecordStubStore
	sessions map[string]uuid.UUID
}

func (s *multiRunFindingStore) GetSecurityScan(_ context.Context, _, runName string) (*store.SecurityScanRecord, error) {
	id, ok := s.sessions[runName]
	if !ok {
		return nil, nil
	}
	return &store.SecurityScanRecord{SessionID: &id}, nil
}

func (s *multiRunFindingStore) SummarizeSecurityFindings(context.Context, string, string, string, bool) (map[string]int32, error) {
	return map[string]int32{}, nil
}

type multiSarifArtifactStore struct {
	store.StateStore
	bySession map[uuid.UUID]string
}

func (s *multiSarifArtifactStore) GetArtifact(_ context.Context, session uuid.UUID, _ string) (*store.Artifact, error) {
	content, ok := s.bySession[session]
	if !ok {
		return nil, nil
	}
	return &store.Artifact{Content: content}, nil
}

func TestPublishExecutionCheckUploadsSARIFFromEverySinkTask(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	scan := securityScanEventTestScan()
	scan.Spec.Checks = &triggersv1alpha1.SecurityScanChecks{Enabled: true, UploadSARIF: true}
	scan.Spec.Revision = "abc1234def"
	scan.Status.LastExecution = &triggersv1alpha1.SecurityScanExecutionStatus{
		ID:    "manual-1",
		Mode:  triggersv1alpha1.SecurityScanExecutionModeDeterministic,
		Phase: triggersv1alpha1.SecurityScanExecutionPhaseSucceeded,
		Tasks: []triggersv1alpha1.SecurityScanTaskExecutionStatus{
			{Name: "sink-a", State: triggersv1alpha1.SecurityScanTaskStateSucceeded, RunName: "run-a"},
			{Name: "sink-b", State: triggersv1alpha1.SecurityScanTaskStateSucceeded, RunName: "run-b"},
		},
	}
	gh := securityScanEventTestRepo(scan.Namespace)
	reconciler, _, _ := newSecurityScanReconciler(t, now, scan, gh)
	publisher := &fakeSecurityCheckPublisher{}
	sessionA, sessionB := uuid.New(), uuid.New()
	reconciler.CheckPublisher = publisher
	reconciler.Findings = &multiRunFindingStore{sessions: map[string]uuid.UUID{"run-a": sessionA, "run-b": sessionB}}
	reconciler.StateStore = &multiSarifArtifactStore{bySession: map[uuid.UUID]string{
		sessionA: `{"version":"2.1.0","runs":[{"tool":{"driver":{"name":"sink-a"}}}]}`,
		sessionB: `{"version":"2.1.0","runs":[{"tool":{"driver":{"name":"sink-b"}}}]}`,
	}}

	if retry := reconciler.publishExecutionCheck(context.Background(), scan, scan.Status.LastExecution); retry {
		t.Fatal("publishExecutionCheck retry = true, want false on success")
	}
	if len(publisher.sarifs) != 1 {
		t.Fatalf("SARIF uploads = %d, want one merged document", len(publisher.sarifs))
	}
	var merged struct {
		Version string `json:"version"`
		Runs    []struct {
			Tool struct {
				Driver struct {
					Name string `json:"name"`
				} `json:"driver"`
			} `json:"tool"`
		} `json:"runs"`
	}
	if err := json.Unmarshal([]byte(publisher.sarifs[0]), &merged); err != nil {
		t.Fatalf("merged SARIF is not valid JSON: %v", err)
	}
	if merged.Version != "2.1.0" || len(merged.Runs) != 2 ||
		merged.Runs[0].Tool.Driver.Name != "sink-a" || merged.Runs[1].Tool.Driver.Name != "sink-b" {
		t.Fatalf("merged SARIF = %s, want both sink runs' entries", publisher.sarifs[0])
	}
	updated := getSecurityScan(t, reconciler.Client, scan)
	if updated.Status.LastCheck == nil || !updated.Status.LastCheck.SARIFUploaded || updated.Status.LastCheck.RunName != "run-b" {
		t.Fatalf("LastCheck = %+v, want SARIFUploaded with the last succeeded run as primary", updated.Status.LastCheck)
	}
}

func TestSecurityScanExecutionReportRunsListsEverySucceededRun(t *testing.T) {
	exec := &triggersv1alpha1.SecurityScanExecutionStatus{Tasks: []triggersv1alpha1.SecurityScanTaskExecutionStatus{
		{Name: "a", State: triggersv1alpha1.SecurityScanTaskStateSucceeded, RunName: "run-a"},
		{Name: "vacuous", State: triggersv1alpha1.SecurityScanTaskStateSucceeded, RunName: ""},
		{Name: "b", State: triggersv1alpha1.SecurityScanTaskStateFailed, RunName: "run-b"},
		{Name: "c", State: triggersv1alpha1.SecurityScanTaskStateSucceeded, RunName: "run-c"},
	}}
	if got := securityScanExecutionReportRuns(exec); len(got) != 2 || got[0] != "run-a" || got[1] != "run-c" {
		t.Fatalf("report runs = %v, want [run-a run-c]", got)
	}
	exec.Tasks[0].State = triggersv1alpha1.SecurityScanTaskStateFailed
	exec.Tasks[3].State = triggersv1alpha1.SecurityScanTaskStateSkipped
	if got := securityScanExecutionReportRuns(exec); len(got) != 1 || got[0] != "run-c" {
		t.Fatalf("fallback report runs = %v, want the last recorded run [run-c]", got)
	}
}
