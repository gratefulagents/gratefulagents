package dashboard

import (
	"errors"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/store"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
)

func newSecurityOverviewTestServer(t *testing.T, stateStore store.StateStore, objs ...client.Object) *Server {
	t.Helper()
	scheme := testProjectScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
	return &Server{k8sClient: c, scheme: scheme, stateStore: stateStore}
}

func securityScanCR(namespace, name string, mutate func(*triggersv1alpha1.SecurityScan)) *triggersv1alpha1.SecurityScan {
	cr := &triggersv1alpha1.SecurityScan{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
		Spec:       triggersv1alpha1.SecurityScanSpec{RepoURL: "https://github.com/acme/payments.git"},
	}
	if mutate != nil {
		mutate(cr)
	}
	return cr
}

func TestGetSecurityOverviewAggregates(t *testing.T) {
	sec := newMockSecurityStore()
	started := time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)
	completed := started.Add(20 * time.Minute)
	sec.scans = []store.SecurityScanRecord{
		{ID: uuid.New(), Namespace: "default", ScanName: "nightly", RunName: "nightly-2",
			Status: "running", StartedAt: &started},
		{ID: uuid.New(), Namespace: "default", ScanName: "nightly", RunName: "nightly-1",
			Status: "completed", StartedAt: &started, CompletedAt: &completed,
			Counts: map[string]int32{"critical": 1, "total": 1}},
		{ID: uuid.New(), Namespace: "other", ScanName: "foreign", RunName: "foreign-1", CompletedAt: &completed},
	}
	sec.summary = map[string]int32{"total": 4, "open": 3, "open_critical": 1, "open_high": 2}

	healthy := securityScanCR("default", "healthy", func(cr *triggersv1alpha1.SecurityScan) {
		cr.Status.Phase = "Scheduled"
		cr.Status.Conditions = []metav1.Condition{{
			Type: triggersv1alpha1.ConditionSecurityScanReady, Status: metav1.ConditionTrue,
			Reason: "Scheduled", LastTransitionTime: metav1.Now(),
		}}
	})
	failing := securityScanCR("default", "failing", func(cr *triggersv1alpha1.SecurityScan) {
		cr.Status.Phase = "Error"
		cr.Status.LastError = "run creation failed"
		cr.Status.Conditions = []metav1.Condition{{
			Type: triggersv1alpha1.ConditionSecurityScanReady, Status: metav1.ConditionFalse,
			Reason: "RunCreationFailed", Message: "run creation failed", LastTransitionTime: metav1.Now(),
		}}
	})
	suspended := securityScanCR("default", "paused", func(cr *triggersv1alpha1.SecurityScan) {
		cr.Spec.Suspend = true
		cr.Status.Phase = "Suspended"
		cr.Status.Conditions = []metav1.Condition{{
			Type: triggersv1alpha1.ConditionSecurityScanReady, Status: metav1.ConditionFalse,
			Reason: "Suspended", Message: "SecurityScan trigger is suspended", LastTransitionTime: metav1.Now(),
		}}
	})
	foreign := securityScanCR("other", "foreign", nil)

	srv := newSecurityOverviewTestServer(t, sec, healthy, failing, suspended, foreign)
	ctx := actorContext("alice", "admin", "", "")

	resp, err := srv.GetSecurityOverview(ctx, &platform.GetSecurityOverviewRequest{Namespace: "default"})
	if err != nil {
		t.Fatalf("GetSecurityOverview() error = %v", err)
	}
	if !resp.GetStoreSupported() {
		t.Fatal("StoreSupported = false, want true")
	}
	if len(resp.GetActiveScans()) != 1 || resp.GetActiveScans()[0].GetRunName() != "nightly-2" {
		t.Fatalf("active scans = %+v, want [nightly-2]", resp.GetActiveScans())
	}
	if len(resp.GetRecentScans()) != 1 || resp.GetRecentScans()[0].GetRunName() != "nightly-1" {
		t.Fatalf("recent scans = %+v, want [nightly-1]", resp.GetRecentScans())
	}
	if resp.GetFindingCounts()["open_critical"] != 1 || resp.GetFindingCounts()["open_high"] != 2 {
		t.Fatalf("finding counts = %+v", resp.GetFindingCounts())
	}
	if resp.GetConfigCount() != 3 {
		t.Fatalf("config count = %d, want 3", resp.GetConfigCount())
	}
	if len(resp.GetConfigIssues()) != 2 {
		t.Fatalf("config issues = %+v, want 2 issues", resp.GetConfigIssues())
	}
	byName := map[string]*platform.SecurityScanConfigIssue{}
	for _, issue := range resp.GetConfigIssues() {
		byName[issue.GetName()] = issue
	}
	if issue := byName["failing"]; issue == nil || issue.GetReadyReason() != "RunCreationFailed" ||
		issue.GetMessage() != "run creation failed" || issue.GetSuspended() {
		t.Fatalf("failing issue = %+v", issue)
	}
	if issue := byName["paused"]; issue == nil || issue.GetReadyReason() != "Suspended" || !issue.GetSuspended() {
		t.Fatalf("paused issue = %+v", issue)
	}
	if resp.GetBaselineAvailable() || resp.GetNewFindings() != 0 || resp.GetRecurringFindings() != 0 || resp.GetResolvedFindings() != 0 {
		t.Fatalf("baseline fields should be absent, got %+v", resp)
	}
	if len(resp.GetWarnings()) != 0 {
		t.Fatalf("warnings = %v, want none", resp.GetWarnings())
	}
}

func TestGetSecurityOverviewWithoutSecurityStore(t *testing.T) {
	cr := securityScanCR("default", "healthy", nil)
	srv := newSecurityOverviewTestServer(t, newMockStateStore(), cr)
	ctx := actorContext("alice", "admin", "", "")

	resp, err := srv.GetSecurityOverview(ctx, &platform.GetSecurityOverviewRequest{Namespace: "default"})
	if err != nil {
		t.Fatalf("GetSecurityOverview() error = %v", err)
	}
	if resp.GetStoreSupported() {
		t.Fatal("StoreSupported = true, want false")
	}
	if len(resp.GetActiveScans()) != 0 || len(resp.GetRecentScans()) != 0 {
		t.Fatalf("scans should be empty without a capable store: %+v", resp)
	}
	if resp.GetConfigCount() != 1 {
		t.Fatalf("config count = %d, want 1", resp.GetConfigCount())
	}
}

func TestGetSecurityOverviewPartialFailureWarns(t *testing.T) {
	sec := newMockSecurityStore()
	sec.listScansErr = errors.New("scan table offline")
	sec.summary = map[string]int32{"open": 1}
	srv := newSecurityOverviewTestServer(t, sec)
	ctx := actorContext("alice", "admin", "", "")

	resp, err := srv.GetSecurityOverview(ctx, &platform.GetSecurityOverviewRequest{Namespace: "default"})
	if err != nil {
		t.Fatalf("GetSecurityOverview() error = %v", err)
	}
	if len(resp.GetWarnings()) != 1 || !strings.Contains(resp.GetWarnings()[0], "scan table offline") {
		t.Fatalf("warnings = %v", resp.GetWarnings())
	}
	if resp.GetFindingCounts()["open"] != 1 {
		t.Fatalf("finding counts = %+v, want open=1 despite scan warning", resp.GetFindingCounts())
	}
}

func TestGetSecurityScanReport(t *testing.T) {
	sec := newMockSecurityStore()
	sessionID := uuid.New()
	completed := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	sec.scans = []store.SecurityScanRecord{
		{ID: uuid.New(), Namespace: "default", ScanName: "nightly", RunName: "nightly-1",
			SessionID: &sessionID, Status: "completed", CompletedAt: &completed},
		{ID: uuid.New(), Namespace: "default", ScanName: "nightly", RunName: "nightly-2", Status: "running"},
	}
	updated := completed.Add(time.Minute)
	sec.getArtifact = &store.Artifact{
		SessionID: sessionID, Kind: "security_report",
		Content: "# Security Scan Report", UpdatedAt: updated,
	}
	srv := newSecurityOverviewTestServer(t, sec)
	ctx := actorContext("alice", "admin", "", "")

	resp, err := srv.GetSecurityScanReport(ctx, &platform.GetSecurityScanReportRequest{
		Namespace: "default", RunName: "nightly-1",
	})
	if err != nil {
		t.Fatalf("GetSecurityScanReport() error = %v", err)
	}
	if resp.GetContent() != "# Security Scan Report" || resp.GetFormat() != "markdown" {
		t.Fatalf("report = %+v", resp)
	}
	if resp.GetFilename() != "nightly-nightly-1.md" {
		t.Fatalf("filename = %q", resp.GetFilename())
	}
	if !resp.GetUpdatedAt().AsTime().Equal(updated) {
		t.Fatalf("updated at = %v, want %v", resp.GetUpdatedAt().AsTime(), updated)
	}

	sarif, err := srv.GetSecurityScanReport(ctx, &platform.GetSecurityScanReportRequest{
		Namespace: "default", RunName: "nightly-1", Format: "sarif",
	})
	if err != nil {
		t.Fatalf("GetSecurityScanReport(sarif) error = %v", err)
	}
	if sarif.GetFormat() != "sarif" || sarif.GetFilename() != "nightly-nightly-1.sarif" {
		t.Fatalf("sarif report = %+v", sarif)
	}

	if _, err := srv.GetSecurityScanReport(ctx, &platform.GetSecurityScanReportRequest{
		Namespace: "default", RunName: "nightly-1", Format: "pdf",
	}); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("bad format code = %v, want InvalidArgument", connect.CodeOf(err))
	}

	// A scan without a recorded session (run still in progress) is NotFound
	// with actionable copy.
	_, err = srv.GetSecurityScanReport(ctx, &platform.GetSecurityScanReportRequest{
		Namespace: "default", RunName: "nightly-2",
	})
	if connect.CodeOf(err) != connect.CodeNotFound || !strings.Contains(err.Error(), "report is written") {
		t.Fatalf("in-progress scan error = %v, want NotFound with copy", err)
	}

	if _, err := srv.GetSecurityScanReport(ctx, &platform.GetSecurityScanReportRequest{
		Namespace: "default", RunName: "missing",
	}); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("missing scan code = %v, want NotFound", connect.CodeOf(err))
	}

	// Session present but the artifact was never written.
	sec.getArtifact = nil
	if _, err := srv.GetSecurityScanReport(ctx, &platform.GetSecurityScanReportRequest{
		Namespace: "default", RunName: "nightly-1",
	}); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("missing artifact code = %v, want NotFound", connect.CodeOf(err))
	}
}

func TestGetSecurityScanReportScopedToAuthorizedNamespace(t *testing.T) {
	sec := newMockSecurityStore()
	sessionID := uuid.New()
	sec.scans = []store.SecurityScanRecord{
		{ID: uuid.New(), Namespace: "user-bob", ScanName: "private", RunName: "private-1", SessionID: &sessionID},
	}
	sec.getArtifact = &store.Artifact{SessionID: sessionID, Content: "secret"}
	srv := newSecurityOverviewTestServer(t, sec, userNamespaceObj("user-bob"))
	ctx := actorContext("alice", "member", "", "")

	_, err := srv.GetSecurityScanReport(ctx, &platform.GetSecurityScanReportRequest{
		Namespace: "user-bob", RunName: "private-1",
	})
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("foreign namespace code = %v, want PermissionDenied", connect.CodeOf(err))
	}
}
