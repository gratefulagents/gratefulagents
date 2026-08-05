package dashboard

import (
	"context"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/store"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
)

// newSecurityCollabTestScheme extends the dashboard test scheme with the
// triggers group so GitHubRepository objects can back the fake client.
func newSecurityCollabTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := newDashboardTestScheme(t)
	if err := triggersv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(triggers): %v", err)
	}
	return scheme
}

func TestUpdateSecurityFindingAssignee(t *testing.T) {
	sec := newMockSecurityStore()
	finding := newTestFinding("default")
	sec.findings[finding.ID] = finding
	srv := newSecurityTestServer(t, sec)
	ctx := actorContext("alice", "admin", "", "")

	resp, err := srv.UpdateSecurityFindingAssignee(ctx, &platform.UpdateSecurityFindingAssigneeRequest{
		Id: finding.ID.String(), Namespace: "default", Assignee: "bob",
	})
	if err != nil {
		t.Fatalf("UpdateSecurityFindingAssignee: %v", err)
	}
	if resp.GetAssignee() != "bob" || finding.Assignee != "bob" {
		t.Errorf("assignee = %q / stored %q, want bob", resp.GetAssignee(), finding.Assignee)
	}
	// Clearing.
	resp, err = srv.UpdateSecurityFindingAssignee(ctx, &platform.UpdateSecurityFindingAssigneeRequest{
		Id: finding.ID.String(), Namespace: "default",
	})
	if err != nil || resp.GetAssignee() != "" {
		t.Errorf("clear assignee = %q, %v, want empty", resp.GetAssignee(), err)
	}
	// Unauthenticated.
	if _, err := srv.UpdateSecurityFindingAssignee(context.Background(), &platform.UpdateSecurityFindingAssigneeRequest{
		Id: finding.ID.String(), Namespace: "default", Assignee: "bob",
	}); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Errorf("unauthenticated code = %v", connect.CodeOf(err))
	}
}

func TestUpdateSecurityFindingAssigneeForeignNamespaceDenied(t *testing.T) {
	sec := newMockSecurityStore()
	finding := newTestFinding("user-bob")
	sec.findings[finding.ID] = finding
	scheme := newDashboardTestScheme(t)
	srv := &Server{
		k8sClient:  fake.NewClientBuilder().WithScheme(scheme).WithObjects(userNamespaceObj("user-bob")).Build(),
		scheme:     scheme,
		stateStore: sec,
	}
	ctx := actorContext("alice", "member", "", "")
	if _, err := srv.UpdateSecurityFindingAssignee(ctx, &platform.UpdateSecurityFindingAssigneeRequest{
		Id: finding.ID.String(), Assignee: "alice",
	}); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("foreign finding code = %v, want NotFound", connect.CodeOf(err))
	}
	if finding.Assignee != "" {
		t.Error("foreign finding was assigned")
	}
}

func TestUpdateSecurityFindingTicket(t *testing.T) {
	sec := newMockSecurityStore()
	finding := newTestFinding("default")
	sec.findings[finding.ID] = finding
	srv := newSecurityTestServer(t, sec)
	ctx := actorContext("alice", "admin", "", "")

	resp, err := srv.UpdateSecurityFindingTicket(ctx, &platform.UpdateSecurityFindingTicketRequest{
		Id: finding.ID.String(), Namespace: "default",
		TicketUrl: "https://github.com/acme/payments/issues/7", TicketProvider: "GitHub",
	})
	if err != nil {
		t.Fatalf("UpdateSecurityFindingTicket: %v", err)
	}
	if resp.GetTicketUrl() != "https://github.com/acme/payments/issues/7" || resp.GetTicketProvider() != "github" {
		t.Errorf("ticket = %q (%q)", resp.GetTicketUrl(), resp.GetTicketProvider())
	}
	// Invalid URLs are rejected before touching the store.
	for _, bad := range []string{"javascript:alert(1)", "notaurl", "ftp://x/y"} {
		if _, err := srv.UpdateSecurityFindingTicket(ctx, &platform.UpdateSecurityFindingTicketRequest{
			Id: finding.ID.String(), Namespace: "default", TicketUrl: bad,
		}); connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Errorf("ticket URL %q code = %v, want InvalidArgument", bad, connect.CodeOf(err))
		}
	}
	// Unlink.
	resp, err = srv.UpdateSecurityFindingTicket(ctx, &platform.UpdateSecurityFindingTicketRequest{
		Id: finding.ID.String(), Namespace: "default",
	})
	if err != nil || resp.GetTicketUrl() != "" {
		t.Errorf("unlink = %q, %v, want empty", resp.GetTicketUrl(), err)
	}
	if events := sec.events[finding.ID]; len(events) == 0 || events[0].EventType != "ticket_unlinked" {
		t.Errorf("newest event after unlink = %+v", sec.events[finding.ID])
	}
}

// fakeIssueCreator records the issue it was asked to create.
type fakeIssueCreator struct {
	gotRepo  *triggersv1alpha1.GitHubRepository
	gotTitle string
	gotBody  string
	url      string
	err      error
}

func (f *fakeIssueCreator) CreateIssue(_ context.Context, gh *triggersv1alpha1.GitHubRepository, title, body string) (string, error) {
	f.gotRepo, f.gotTitle, f.gotBody = gh, title, body
	if f.err != nil {
		return "", f.err
	}
	return f.url, nil
}

func TestCreateSecurityFindingTicketGitHub(t *testing.T) {
	sec := newMockSecurityStore()
	finding := newTestFinding("default")
	finding.Description = "SECRET-DESCRIPTION-TEXT"
	finding.Impact = "SECRET-IMPACT-TEXT"
	finding.AttackVector = "SECRET-VECTOR-TEXT"
	finding.Raw = []byte(`{"evidence":"SECRET-EVIDENCE"}`)
	sec.findings[finding.ID] = finding
	scheme := newSecurityCollabTestScheme(t)
	repo := &triggersv1alpha1.GitHubRepository{
		ObjectMeta: metav1.ObjectMeta{Name: "payments", Namespace: "default"},
		Spec:       triggersv1alpha1.GitHubRepositorySpec{Owner: "acme", Repo: "payments", GitHubTokenSecret: "gh-token"},
	}
	creator := &fakeIssueCreator{url: "https://github.com/acme/payments/issues/42"}
	srv := &Server{
		k8sClient:             fake.NewClientBuilder().WithScheme(scheme).WithObjects(repo).Build(),
		scheme:                scheme,
		stateStore:            sec,
		securityTicketCreator: creator,
	}
	ctx := actorContext("alice", "admin", "", "")

	resp, err := srv.CreateSecurityFindingTicket(ctx, &platform.CreateSecurityFindingTicketRequest{
		Id: finding.ID.String(), Namespace: "default", Provider: "github", RepositoryRef: "payments",
	})
	if err != nil {
		t.Fatalf("CreateSecurityFindingTicket: %v", err)
	}
	if resp.GetTicketUrl() != creator.url || resp.GetTicketProvider() != "github" {
		t.Errorf("linked ticket = %q (%q)", resp.GetTicketUrl(), resp.GetTicketProvider())
	}
	if creator.gotRepo == nil || creator.gotRepo.Name != "payments" {
		t.Fatalf("creator repo = %+v", creator.gotRepo)
	}
	if !strings.Contains(creator.gotTitle, finding.Title) {
		t.Errorf("title %q does not reference the finding", creator.gotTitle)
	}
	// The body must identify the finding but never leak evidence, impact,
	// or attack-vector text.
	for _, want := range []string{"critical", "internal/db/query.go:10", finding.ID.String()} {
		if !strings.Contains(creator.gotBody, want) {
			t.Errorf("body missing %q:\n%s", want, creator.gotBody)
		}
	}
	for _, leak := range []string{string(finding.Raw), finding.Impact, finding.AttackVector, finding.Description} {
		if leak != "" && strings.Contains(creator.gotBody, leak) {
			t.Errorf("body leaks %q:\n%s", leak, creator.gotBody)
		}
	}

	// A second creation is blocked while a ticket is linked.
	if _, err := srv.CreateSecurityFindingTicket(ctx, &platform.CreateSecurityFindingTicketRequest{
		Id: finding.ID.String(), Namespace: "default", Provider: "github", RepositoryRef: "payments",
	}); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Errorf("second create code = %v, want FailedPrecondition", connect.CodeOf(err))
	}
}

func TestCreateSecurityFindingTicketGuards(t *testing.T) {
	sec := newMockSecurityStore()
	finding := newTestFinding("default")
	sec.findings[finding.ID] = finding
	scheme := newSecurityCollabTestScheme(t)
	// GitHubRepository exists only in another namespace: same-namespace rule
	// must reject it.
	repo := &triggersv1alpha1.GitHubRepository{
		ObjectMeta: metav1.ObjectMeta{Name: "payments", Namespace: "other"},
		Spec:       triggersv1alpha1.GitHubRepositorySpec{Owner: "acme", Repo: "payments"},
	}
	srv := &Server{
		k8sClient:             fake.NewClientBuilder().WithScheme(scheme).WithObjects(repo).Build(),
		scheme:                scheme,
		stateStore:            sec,
		securityTicketCreator: &fakeIssueCreator{url: "https://example.com/1"},
	}
	ctx := actorContext("alice", "admin", "", "")

	if _, err := srv.CreateSecurityFindingTicket(ctx, &platform.CreateSecurityFindingTicketRequest{
		Id: finding.ID.String(), Namespace: "default", Provider: "linear", RepositoryRef: "payments",
	}); connect.CodeOf(err) != connect.CodeUnimplemented {
		t.Errorf("linear code = %v, want Unimplemented (link-only)", connect.CodeOf(err))
	}
	if _, err := srv.CreateSecurityFindingTicket(ctx, &platform.CreateSecurityFindingTicketRequest{
		Id: finding.ID.String(), Namespace: "default", Provider: "github", RepositoryRef: "payments",
	}); connect.CodeOf(err) != connect.CodeNotFound {
		t.Errorf("cross-namespace repo code = %v, want NotFound", connect.CodeOf(err))
	}
	if finding.TicketURL != "" {
		t.Error("guarded create linked a ticket")
	}
}

func TestBulkUpdateSecurityFindingStatus(t *testing.T) {
	sec := newMockSecurityStore()
	f1 := newTestFinding("default")
	f2 := newTestFinding("default")
	sec.findings[f1.ID] = f1
	sec.findings[f2.ID] = f2
	srv := newSecurityTestServer(t, sec)
	ctx := actorContext("alice", "admin", "", "")

	resp, err := srv.BulkUpdateSecurityFindingStatus(ctx, &platform.BulkUpdateSecurityFindingStatusRequest{
		Namespace: "default", ScanName: "nightly",
		Ids:    []string{f1.ID.String(), f2.ID.String()},
		Status: "triaged", SetAssignee: true, Assignee: "bob", Note: "sweep",
	})
	if err != nil {
		t.Fatalf("BulkUpdateSecurityFindingStatus: %v", err)
	}
	if resp.GetUpdated() != 2 || len(resp.GetResults()) != 2 {
		t.Fatalf("resp = %+v", resp)
	}
	for _, r := range resp.GetResults() {
		if !r.GetOk() || r.GetError() != "" {
			t.Errorf("result = %+v, want ok", r)
		}
	}
	if f1.Status != "triaged" || f1.Assignee != "bob" || f2.Status != "triaged" {
		t.Errorf("stored findings = %q/%q and %q", f1.Status, f1.Assignee, f2.Status)
	}
}

func TestBulkUpdateSecurityFindingStatusPartialFailureReportsAndAborts(t *testing.T) {
	sec := newMockSecurityStore()
	f1 := newTestFinding("default")
	sec.findings[f1.ID] = f1
	srv := newSecurityTestServer(t, sec)
	ctx := actorContext("alice", "admin", "", "")
	missing := uuid.New()

	resp, err := srv.BulkUpdateSecurityFindingStatus(ctx, &platform.BulkUpdateSecurityFindingStatusRequest{
		Namespace: "default",
		Ids:       []string{f1.ID.String(), missing.String()},
		Status:    "confirmed",
	})
	if err != nil {
		t.Fatalf("BulkUpdateSecurityFindingStatus: %v", err)
	}
	if resp.GetUpdated() != 0 {
		t.Errorf("updated = %d, want 0 (atomic abort)", resp.GetUpdated())
	}
	byID := map[string]*platform.BulkUpdateSecurityFindingOutcome{}
	for _, r := range resp.GetResults() {
		byID[r.GetId()] = r
	}
	if r := byID[missing.String()]; r == nil || r.GetOk() || !strings.Contains(r.GetError(), "not found") {
		t.Errorf("missing outcome = %+v", r)
	}
	if r := byID[f1.ID.String()]; r == nil || r.GetOk() || !strings.Contains(r.GetError(), "aborted") {
		t.Errorf("aborted outcome = %+v", r)
	}
	if f1.Status != "open" {
		t.Errorf("f1 status = %q, want rolled back to open", f1.Status)
	}
}

func TestBulkUpdateSecurityFindingStatusValidation(t *testing.T) {
	sec := newMockSecurityStore()
	f1 := newTestFinding("default")
	sec.findings[f1.ID] = f1
	srv := newSecurityTestServer(t, sec)
	ctx := actorContext("alice", "admin", "", "")

	cases := []*platform.BulkUpdateSecurityFindingStatusRequest{
		{Namespace: "default", Status: "triaged"},                              // no ids
		{Namespace: "default", Ids: []string{f1.ID.String()}},                  // no change
		{Namespace: "default", Ids: []string{f1.ID.String()}, Status: "bogus"}, // bad status
		{Namespace: "default", Ids: []string{"not-a-uuid"}, Status: "triaged"}, // bad id
		{Namespace: "default", Ids: []string{f1.ID.String()}, Status: "triaged", // expiry w/o accepted_risk
			AcceptedRiskExpiresAt: timestamppb.New(time.Now().Add(time.Hour))},
	}
	for i, req := range cases {
		if _, err := srv.BulkUpdateSecurityFindingStatus(ctx, req); connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Errorf("case %d code = %v, want InvalidArgument", i, connect.CodeOf(err))
		}
	}
}

func TestSecuritySavedFilterOwnershipIsolation(t *testing.T) {
	sec := newMockSecurityStore()
	srv := newSecurityTestServer(t, sec)
	alice := actorContext("alice", "admin", "", "")
	bob := actorContext("bob", "admin", "", "")

	if _, err := srv.SaveSecuritySavedFilter(alice, &platform.SaveSecuritySavedFilterRequest{
		Namespace: "default", Name: "criticals", Query: `{"severity":"critical"}`,
	}); err != nil {
		t.Fatalf("save: %v", err)
	}
	// Bob cannot see alice's filter.
	resp, err := srv.ListSecuritySavedFilters(bob, &platform.ListSecuritySavedFiltersRequest{Namespace: "default"})
	if err != nil || len(resp.GetFilters()) != 0 {
		t.Errorf("bob's list = %d filters, %v, want 0", len(resp.GetFilters()), err)
	}
	// Bob deleting alice's filter name removes nothing of hers.
	if _, err := srv.DeleteSecuritySavedFilter(bob, &platform.DeleteSecuritySavedFilterRequest{Namespace: "default", Name: "criticals"}); err != nil {
		t.Fatalf("bob delete: %v", err)
	}
	resp, err = srv.ListSecuritySavedFilters(alice, &platform.ListSecuritySavedFiltersRequest{Namespace: "default"})
	if err != nil || len(resp.GetFilters()) != 1 || resp.GetFilters()[0].GetName() != "criticals" {
		t.Fatalf("alice's list after bob delete = %+v, %v", resp.GetFilters(), err)
	}
	// Invalid query payloads are rejected.
	if _, err := srv.SaveSecuritySavedFilter(alice, &platform.SaveSecuritySavedFilterRequest{
		Namespace: "default", Name: "bad", Query: "not-json",
	}); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("bad query code = %v, want InvalidArgument", connect.CodeOf(err))
	}
	// Unauthenticated callers are rejected.
	if _, err := srv.ListSecuritySavedFilters(context.Background(), &platform.ListSecuritySavedFiltersRequest{Namespace: "default"}); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Errorf("unauthenticated code = %v", connect.CodeOf(err))
	}
}

func TestExportSecurityFindingAuditLog(t *testing.T) {
	sec := newMockSecurityStore()
	finding := newTestFinding("default")
	sec.findings[finding.ID] = finding
	sec.events[finding.ID] = []store.SecurityFindingEvent{{
		ID: 1, FindingID: finding.ID, EventType: "status_changed", Actor: "alice",
		Note: "triaged, needs \"review\"", Detail: []byte(`{"from":"open","to":"triaged"}`), CreatedAt: time.Now(),
	}}
	srv := newSecurityTestServer(t, sec)
	ctx := actorContext("alice", "admin", "", "")

	resp, err := srv.ExportSecurityFindingAuditLog(ctx, &platform.ExportSecurityFindingAuditLogRequest{
		Namespace: "default", ScanName: "nightly",
	})
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if resp.GetFilename() != "security-audit-nightly.csv" || resp.GetContentType() != "text/csv" || resp.GetEventCount() != 1 {
		t.Errorf("export meta = %q %q %d", resp.GetFilename(), resp.GetContentType(), resp.GetEventCount())
	}
	body := string(resp.GetContent())
	if !strings.Contains(body, "event_id,created_at,finding_id") || !strings.Contains(body, "status_changed") || !strings.Contains(body, finding.ID.String()) {
		t.Errorf("csv body:\n%s", body)
	}

	resp, err = srv.ExportSecurityFindingAuditLog(ctx, &platform.ExportSecurityFindingAuditLogRequest{
		Namespace: "default", ScanName: "nightly", Format: "json",
	})
	if err != nil || resp.GetContentType() != "application/json" || !strings.Contains(string(resp.GetContent()), `"event_type": "status_changed"`) {
		t.Errorf("json export = %q, %v", resp.GetContentType(), err)
	}

	if _, err := srv.ExportSecurityFindingAuditLog(ctx, &platform.ExportSecurityFindingAuditLogRequest{Namespace: "default"}); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("missing scan code = %v", connect.CodeOf(err))
	}
	if _, err := srv.ExportSecurityFindingAuditLog(ctx, &platform.ExportSecurityFindingAuditLogRequest{
		Namespace: "default", ScanName: "nightly", Format: "xml",
	}); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("bad format code = %v", connect.CodeOf(err))
	}
}

func TestSecurityCollabRPCsScopeToAuthorizedNamespace(t *testing.T) {
	sec := newMockSecurityStore()
	scheme := newDashboardTestScheme(t)
	srv := &Server{
		k8sClient:  fake.NewClientBuilder().WithScheme(scheme).WithObjects(userNamespaceObj("user-bob")).Build(),
		scheme:     scheme,
		stateStore: sec,
	}
	ctx := actorContext("alice", "member", "", "")

	if _, err := srv.BulkUpdateSecurityFindingStatus(ctx, &platform.BulkUpdateSecurityFindingStatusRequest{
		Namespace: "user-bob", Ids: []string{uuid.NewString()}, Status: "triaged",
	}); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Errorf("bulk update foreign ns code = %v, want PermissionDenied", connect.CodeOf(err))
	}
	if _, err := srv.ListSecuritySavedFilters(ctx, &platform.ListSecuritySavedFiltersRequest{Namespace: "user-bob"}); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Errorf("saved filters foreign ns code = %v, want PermissionDenied", connect.CodeOf(err))
	}
	if _, err := srv.ExportSecurityFindingAuditLog(ctx, &platform.ExportSecurityFindingAuditLogRequest{
		Namespace: "user-bob", ScanName: "nightly",
	}); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Errorf("export foreign ns code = %v, want PermissionDenied", connect.CodeOf(err))
	}
}
