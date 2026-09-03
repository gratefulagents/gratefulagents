package dashboard

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/gratefulagents/gratefulagents/internal/store"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
)

// mockSecurityStore layers an in-memory store.SecurityFindingStore on top of
// the shared mockStateStore.
type mockSecurityStore struct {
	*mockStateStore
	scans      []store.SecurityScanRecord
	findings   map[uuid.UUID]*store.SecurityFindingRecord
	events     map[uuid.UUID][]store.SecurityFindingEvent
	lastFilter store.SecurityFindingFilter

	listScansErr error
	summaryErr   error

	lastGetNamespace     string
	lastEventsNamespace  string
	lastStatusNamespace  string
	lastCommentNamespace string

	summaryNamespace string
	summaryScanID    uuid.UUID
	summaryScanName  string
	summaryRunName   string
	summary          map[string]int32

	summaryIncludeSuppressed bool

	lastStatusExpiry     *time.Time
	lastStatusHumanActor bool
	expireSweeps         []string
	bulkErr              error
	lastBulkUpdate       *store.SecurityFindingBulkUpdate
	savedFilters         []store.SecuritySavedFilter
	trends               *store.SecurityFindingTrends

	// Visibility plumbing: shares feeds ListSharedWithMe, and the exclusion
	// lists the handlers pushed into the last summary/trends call.
	shares              []store.ResourceShare
	lastSummaryExcluded []string
	lastTrendsExcluded  []string

	postures             []store.SecurityConfigPosture
	posturesErr          error
	lastPosturesExcluded []string
	lastActivityLimit    int32

	appliedSuppressions     []store.SecuritySuppressionRule
	suppressionExpirySweeps []string
}

func newMockSecurityStore() *mockSecurityStore {
	return &mockSecurityStore{
		mockStateStore: newMockStateStore(),
		findings:       make(map[uuid.UUID]*store.SecurityFindingRecord),
		events:         make(map[uuid.UUID][]store.SecurityFindingEvent),
	}
}

func (m *mockSecurityStore) UpsertSecurityScan(_ context.Context, rec *store.SecurityScanRecord) (*store.SecurityScanRecord, error) {
	return rec, nil
}

func (m *mockSecurityStore) GetSecurityScan(_ context.Context, namespace, runName string) (*store.SecurityScanRecord, error) {
	for i := range m.scans {
		if m.scans[i].Namespace == namespace && m.scans[i].RunName == runName {
			return &m.scans[i], nil
		}
	}
	return nil, nil
}

func (m *mockSecurityStore) ListSecurityScans(_ context.Context, namespace, scanName string, _ int32, excludedScanNames []string) ([]store.SecurityScanRecord, error) {
	if m.listScansErr != nil {
		return nil, m.listScansErr
	}
	excluded := make(map[string]bool, len(excludedScanNames))
	for _, name := range excludedScanNames {
		excluded[name] = true
	}
	var out []store.SecurityScanRecord
	for _, scan := range m.scans {
		if scan.Namespace == namespace && (scanName == "" || scan.ScanName == scanName) && !excluded[scan.ScanName] {
			out = append(out, scan)
		}
	}
	return out, nil
}

func (m *mockSecurityStore) UpsertSecurityFinding(_ context.Context, rec *store.SecurityFindingRecord) (*store.SecurityFindingRecord, bool, error) {
	return rec, true, nil
}

func (m *mockSecurityStore) CorrelateSecurityFindings(context.Context, string, string, string, string, string, string, string) (bool, error) {
	return false, nil
}

func (m *mockSecurityStore) ListSecurityFindings(_ context.Context, f store.SecurityFindingFilter) ([]store.SecurityFindingRecord, error) {
	m.lastFilter = f
	var out []store.SecurityFindingRecord
	for _, finding := range m.findings {
		if finding.Namespace != f.Namespace {
			continue
		}
		if f.ScanID != uuid.Nil && finding.ScanID != f.ScanID {
			continue
		}
		if f.ScanName != "" && finding.ScanName != f.ScanName {
			continue
		}
		if f.RunName != "" && finding.RunName != f.RunName {
			continue
		}
		if slices.Contains(f.ExcludedScanNames, finding.ScanName) {
			continue
		}
		out = append(out, *finding)
	}
	return out, nil
}

func (m *mockSecurityStore) GetSecurityFinding(_ context.Context, namespace string, id uuid.UUID) (*store.SecurityFindingRecord, error) {
	if namespace == "" {
		return nil, errors.New("namespace is required")
	}
	m.lastGetNamespace = namespace
	finding := m.findings[id]
	if finding == nil || finding.Namespace != namespace {
		return nil, nil
	}
	return finding, nil
}

func (m *mockSecurityStore) SetSecurityFindingStatus(_ context.Context, namespace string, id uuid.UUID, status, actor, note string, opts store.SetSecurityFindingStatusOpts) error {
	m.lastStatusExpiry = opts.AcceptedRiskExpiresAt
	m.lastStatusHumanActor = opts.HumanActor
	if namespace == "" {
		return errors.New("namespace is required")
	}
	m.lastStatusNamespace = namespace
	finding, ok := m.findings[id]
	if !ok || finding.Namespace != namespace {
		return store.ErrSecurityFindingNotFound
	}
	if !store.ValidSecurityFindingStatus(status) {
		return context.Canceled
	}
	finding.Status = status
	m.events[id] = append([]store.SecurityFindingEvent{{
		ID: int64(len(m.events[id]) + 1), FindingID: id, EventType: "status_changed",
		Actor: actor, Note: note, CreatedAt: time.Now(),
	}}, m.events[id]...)
	return nil
}

func (m *mockSecurityStore) ListSecurityFindingEvents(_ context.Context, namespace string, id uuid.UUID, _ int32) ([]store.SecurityFindingEvent, error) {
	if namespace == "" {
		return nil, errors.New("namespace is required")
	}
	m.lastEventsNamespace = namespace
	finding := m.findings[id]
	if finding == nil || finding.Namespace != namespace {
		return nil, nil
	}
	return m.events[id], nil
}

func (m *mockSecurityStore) AddSecurityFindingComment(_ context.Context, namespace string, id uuid.UUID, actor, body string) (*store.SecurityFindingEvent, error) {
	if namespace == "" {
		return nil, errors.New("namespace is required")
	}
	m.lastCommentNamespace = namespace
	finding := m.findings[id]
	if finding == nil || finding.Namespace != namespace {
		return nil, store.ErrSecurityFindingNotFound
	}
	event := store.SecurityFindingEvent{
		ID: int64(len(m.events[id]) + 1), FindingID: id, EventType: "comment",
		Actor: actor, Note: body, Detail: []byte(`{}`), CreatedAt: time.Now(),
	}
	m.events[id] = append([]store.SecurityFindingEvent{event}, m.events[id]...)
	return &event, nil
}

func (m *mockSecurityStore) SummarizeSecurityFindings(_ context.Context, namespace, scanName, runName string, includeSuppressed bool) (map[string]int32, error) {
	if m.summaryErr != nil {
		return nil, m.summaryErr
	}
	m.summaryNamespace, m.summaryScanName, m.summaryRunName = namespace, scanName, runName
	m.summaryIncludeSuppressed = includeSuppressed
	return m.summary, nil
}

func (m *mockSecurityStore) SummarizeSecurityFindingsScoped(_ context.Context, scope store.SecurityFindingSummaryScope) (map[string]int32, error) {
	if m.summaryErr != nil {
		return nil, m.summaryErr
	}
	m.summaryNamespace, m.summaryScanName, m.summaryRunName = scope.Namespace, scope.ScanName, scope.RunName
	m.summaryScanID = scope.ScanID
	m.summaryIncludeSuppressed = scope.IncludeSuppressed
	m.lastSummaryExcluded = scope.ExcludedScanNames
	return m.summary, nil
}

func (m *mockSecurityStore) ListSecurityConfigPostures(_ context.Context, namespace string, activityLimit int32, excludedScanNames []string) ([]store.SecurityConfigPosture, error) {
	if m.posturesErr != nil {
		return nil, m.posturesErr
	}
	m.lastActivityLimit = activityLimit
	m.lastPosturesExcluded = excludedScanNames
	excluded := make(map[string]bool, len(excludedScanNames))
	for _, name := range excludedScanNames {
		excluded[name] = true
	}
	var out []store.SecurityConfigPosture
	for _, p := range m.postures {
		if !excluded[p.ScanName] {
			out = append(out, p)
		}
	}
	_ = namespace
	return out, nil
}

func (m *mockSecurityStore) DeleteSecurityScanData(context.Context, string, string) error {
	return nil
}

func (m *mockSecurityStore) PurgeExpiredSecurityData(context.Context, string, store.SecurityRetentionPolicy, int) (store.SecurityRetentionCounts, bool, error) {
	return store.SecurityRetentionCounts{}, false, nil
}

func (m *mockSecurityStore) ClaimSecurityNotifications(_ context.Context, _, _, _ string, fingerprints []string) ([]string, error) {
	return fingerprints, nil
}

func (m *mockSecurityStore) ReleaseSecurityNotifications(context.Context, string, string, string, []string) error {
	return nil
}

func (m *mockSecurityStore) SetSecurityFindingAssignee(_ context.Context, namespace string, id uuid.UUID, assignee, actor string) error {
	if namespace == "" {
		return errors.New("namespace is required")
	}
	finding, ok := m.findings[id]
	if !ok || finding.Namespace != namespace {
		return store.ErrSecurityFindingNotFound
	}
	finding.Assignee = assignee
	m.events[id] = append([]store.SecurityFindingEvent{{
		ID: int64(len(m.events[id]) + 1), FindingID: id, EventType: "assignee_changed",
		Actor: actor, Detail: []byte(`{}`), CreatedAt: time.Now(),
	}}, m.events[id]...)
	return nil
}

func (m *mockSecurityStore) SetSecurityFindingTicket(_ context.Context, namespace string, id uuid.UUID, ticketURL, provider, actor string) error {
	if namespace == "" {
		return errors.New("namespace is required")
	}
	if ticketURL != "" {
		if err := store.ValidateSecurityTicketURL(ticketURL); err != nil {
			return err
		}
	}
	finding, ok := m.findings[id]
	if !ok || finding.Namespace != namespace {
		return store.ErrSecurityFindingNotFound
	}
	finding.TicketURL = ticketURL
	finding.TicketProvider = provider
	eventType := "ticket_linked"
	if ticketURL == "" {
		eventType = "ticket_unlinked"
	}
	m.events[id] = append([]store.SecurityFindingEvent{{
		ID: int64(len(m.events[id]) + 1), FindingID: id, EventType: eventType,
		Actor: actor, Detail: []byte(`{}`), CreatedAt: time.Now(),
	}}, m.events[id]...)
	return nil
}

func (m *mockSecurityStore) ExpireAcceptedRisks(_ context.Context, namespace string) (int32, error) {
	if namespace == "" {
		return 0, errors.New("namespace is required")
	}
	m.expireSweeps = append(m.expireSweeps, namespace)
	return 0, nil
}

func (m *mockSecurityStore) ApplySecuritySuppressions(_ context.Context, namespace, scanName string, rules []store.SecuritySuppressionRule) (int32, error) {
	if namespace == "" {
		return 0, errors.New("namespace is required")
	}
	m.appliedSuppressions = append(m.appliedSuppressions, rules...)
	return int32(len(rules)), nil
}

func (m *mockSecurityStore) ExpireSecuritySuppressions(_ context.Context, namespace string) (int32, error) {
	if namespace == "" {
		return 0, errors.New("namespace is required")
	}
	m.suppressionExpirySweeps = append(m.suppressionExpirySweeps, namespace)
	return 0, nil
}

func (m *mockSecurityStore) RevokeSecuritySuppressions(_ context.Context, namespace, scanName string, activeRules []store.SecuritySuppressionRule) (int32, error) {
	if namespace == "" {
		return 0, errors.New("namespace is required")
	}
	return 0, nil
}

func (m *mockSecurityStore) BulkUpdateSecurityFindings(_ context.Context, namespace, scanName string, ids []uuid.UUID, upd store.SecurityFindingBulkUpdate) error {
	if namespace == "" {
		return errors.New("namespace is required")
	}
	if m.bulkErr != nil {
		return m.bulkErr
	}
	// Atomic: validate every id before applying anything.
	for _, id := range ids {
		finding, ok := m.findings[id]
		if !ok || finding.Namespace != namespace || (scanName != "" && finding.ScanName != scanName) {
			return &store.BulkSecurityFindingError{FindingID: id, Err: store.ErrSecurityFindingNotFound}
		}
	}
	for _, id := range ids {
		finding := m.findings[id]
		if upd.Status != nil {
			finding.Status = *upd.Status
		}
		if upd.Assignee != nil {
			finding.Assignee = *upd.Assignee
		}
	}
	m.lastBulkUpdate = &upd
	return nil
}

func (m *mockSecurityStore) FinalizeSecurityScanBaseline(_ context.Context, namespace, runName string) (int32, error) {
	if namespace == "" {
		return 0, errors.New("namespace is required")
	}
	return 0, nil
}

func (m *mockSecurityStore) ListSecuritySavedFilters(_ context.Context, namespace, owner string) ([]store.SecuritySavedFilter, error) {
	if namespace == "" || owner == "" {
		return nil, errors.New("namespace and owner are required")
	}
	var out []store.SecuritySavedFilter
	for _, f := range m.savedFilters {
		if f.Namespace == namespace && f.Owner == owner {
			out = append(out, f)
		}
	}
	return out, nil
}

func (m *mockSecurityStore) SaveSecuritySavedFilter(_ context.Context, rec *store.SecuritySavedFilter) (*store.SecuritySavedFilter, error) {
	if rec.Namespace == "" || rec.Owner == "" {
		return nil, errors.New("namespace and owner are required")
	}
	stored := *rec
	stored.ID = uuid.New()
	stored.CreatedAt = time.Now()
	stored.UpdatedAt = stored.CreatedAt
	for i := range m.savedFilters {
		f := &m.savedFilters[i]
		if f.Namespace == rec.Namespace && f.Owner == rec.Owner && f.Name == rec.Name {
			f.Query = rec.Query
			f.UpdatedAt = time.Now()
			out := *f
			return &out, nil
		}
	}
	m.savedFilters = append(m.savedFilters, stored)
	out := stored
	return &out, nil
}

func (m *mockSecurityStore) DeleteSecuritySavedFilter(_ context.Context, namespace, owner, name string) error {
	if namespace == "" || owner == "" {
		return errors.New("namespace and owner are required")
	}
	kept := m.savedFilters[:0]
	for _, f := range m.savedFilters {
		if f.Namespace == namespace && f.Owner == owner && f.Name == name {
			continue
		}
		kept = append(kept, f)
	}
	m.savedFilters = kept
	return nil
}

func (m *mockSecurityStore) GetSecurityFindingTrends(_ context.Context, namespace, scanName string, excludedScanNames []string) (*store.SecurityFindingTrends, error) {
	if namespace == "" {
		return nil, errors.New("namespace is required")
	}
	m.lastTrendsExcluded = excludedScanNames
	if m.trends != nil {
		return m.trends, nil
	}
	return &store.SecurityFindingTrends{}, nil
}

func (m *mockSecurityStore) ExportSecurityFindingEvents(_ context.Context, namespace, scanName string, _ int32) ([]store.SecurityFindingAuditRecord, error) {
	if namespace == "" {
		return nil, errors.New("namespace is required")
	}
	if scanName == "" {
		return nil, errors.New("scan name is required")
	}
	var out []store.SecurityFindingAuditRecord
	for id, finding := range m.findings {
		if finding.Namespace != namespace || finding.ScanName != scanName {
			continue
		}
		for _, ev := range m.events[id] {
			out = append(out, store.SecurityFindingAuditRecord{
				Event: ev, FindingID: id, Fingerprint: finding.Fingerprint,
				Title: finding.Title, Severity: finding.Severity, Status: finding.Status,
			})
		}
	}
	return out, nil
}

var _ store.SecurityFindingStore = (*mockSecurityStore)(nil)

// ListResourceOwnersByType exposes the embedded mockStateStore's ownership
// records in bulk so the security visibility helper exercises the same
// bulk-load path the postgres store provides.
func (m *mockSecurityStore) ListResourceOwnersByType(_ context.Context, resourceType string) ([]store.ResourceOwnership, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []store.ResourceOwnership
	for _, o := range m.owners {
		if o.ResourceType == resourceType {
			out = append(out, *o)
		}
	}
	return out, nil
}

func (m *mockSecurityStore) ListSharedWithMe(_ context.Context, subject, resourceType string) ([]store.ResourceShare, error) {
	var out []store.ResourceShare
	for _, sh := range m.shares {
		if sh.SharedWithUserID == subject && sh.ResourceType == resourceType {
			out = append(out, sh)
		}
	}
	return out, nil
}

func newSecurityTestServer(t *testing.T, sec store.StateStore) *Server {
	t.Helper()
	scheme := newDashboardTestScheme(t)
	return &Server{
		k8sClient:  fake.NewClientBuilder().WithScheme(scheme).Build(),
		scheme:     scheme,
		stateStore: sec,
	}
}

func TestSecurityHandlersRequireCapableStore(t *testing.T) {
	// The base mock state store does not implement store.SecurityFindingStore.
	srv := newSecurityTestServer(t, newMockStateStore())
	ctx := actorContext("alice", "admin", "", "")

	calls := map[string]func() error{
		"ListSecurityScans": func() error {
			_, err := srv.ListSecurityScans(ctx, &platform.ListSecurityScansRequest{Namespace: "default"})
			return err
		},
		"GetSecurityScan": func() error {
			_, err := srv.GetSecurityScan(ctx, &platform.GetSecurityScanRequest{Namespace: "default", RunName: "scan-1"})
			return err
		},
		"ListSecurityFindings": func() error {
			_, err := srv.ListSecurityFindings(ctx, &platform.ListSecurityFindingsRequest{Namespace: "default"})
			return err
		},
		"GetSecurityFinding": func() error {
			_, err := srv.GetSecurityFinding(ctx, &platform.GetSecurityFindingRequest{Id: uuid.NewString()})
			return err
		},
		"UpdateSecurityFindingStatus": func() error {
			_, err := srv.UpdateSecurityFindingStatus(ctx, &platform.UpdateSecurityFindingStatusRequest{Id: uuid.NewString(), Status: "triaged"})
			return err
		},
		"GetSecurityFindingSummary": func() error {
			_, err := srv.GetSecurityFindingSummary(ctx, &platform.GetSecurityFindingSummaryRequest{Namespace: "default"})
			return err
		},
		"ListSecurityFindingEvents": func() error {
			_, err := srv.ListSecurityFindingEvents(ctx, &platform.ListSecurityFindingEventsRequest{Id: uuid.NewString()})
			return err
		},
		"AddSecurityFindingComment": func() error {
			_, err := srv.AddSecurityFindingComment(ctx, &platform.AddSecurityFindingCommentRequest{Id: uuid.NewString(), Body: "hi"})
			return err
		},
		"GetSecurityScanReport": func() error {
			_, err := srv.GetSecurityScanReport(ctx, &platform.GetSecurityScanReportRequest{Namespace: "default", RunName: "scan-1"})
			return err
		},
	}
	for name, call := range calls {
		if code := connect.CodeOf(call()); code != connect.CodeFailedPrecondition {
			t.Errorf("%s code = %v, want FailedPrecondition", name, code)
		}
	}
}

func TestListAndGetSecurityScans(t *testing.T) {
	sec := newMockSecurityStore()
	started := time.Date(2026, 2, 1, 10, 0, 0, 0, time.UTC)
	completed := started.Add(30 * time.Minute)
	sec.scans = []store.SecurityScanRecord{
		{
			ID: uuid.New(), Namespace: "default", ScanName: "nightly", RunName: "nightly-1",
			Repository: "github.com/acme/payments", Revision: "abc123", Status: "completed",
			Summary: "2 findings", StartedAt: &started, CompletedAt: &completed,
			Counts: map[string]int32{"critical": 1, "low": 1, "total": 2},
		},
		{ID: uuid.New(), Namespace: "other", ScanName: "nightly", RunName: "nightly-2"},
	}
	srv := newSecurityTestServer(t, sec)
	ctx := actorContext("alice", "admin", "", "")

	resp, err := srv.ListSecurityScans(ctx, &platform.ListSecurityScansRequest{Namespace: "default"})
	if err != nil {
		t.Fatalf("ListSecurityScans() error = %v", err)
	}
	if len(resp.Scans) != 1 {
		t.Fatalf("scans = %d, want 1", len(resp.Scans))
	}
	scan := resp.Scans[0]
	if scan.GetRunName() != "nightly-1" || scan.GetRepository() != "github.com/acme/payments" ||
		scan.GetCounts()["critical"] != 1 || !scan.GetStartedAt().AsTime().Equal(started) ||
		!scan.GetCompletedAt().AsTime().Equal(completed) {
		t.Fatalf("scan proto = %+v", scan)
	}

	got, err := srv.GetSecurityScan(ctx, &platform.GetSecurityScanRequest{Namespace: "default", RunName: "nightly-1"})
	if err != nil {
		t.Fatalf("GetSecurityScan() error = %v", err)
	}
	if got.GetId() != sec.scans[0].ID.String() {
		t.Fatalf("scan id = %q, want %q", got.GetId(), sec.scans[0].ID.String())
	}

	if _, err := srv.GetSecurityScan(ctx, &platform.GetSecurityScanRequest{Namespace: "default", RunName: "missing"}); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("missing scan code = %v, want NotFound", connect.CodeOf(err))
	}
}

func TestListSecurityFindingsPassesFilter(t *testing.T) {
	sec := newMockSecurityStore()
	srv := newSecurityTestServer(t, sec)
	ctx := actorContext("alice", "admin", "", "")

	_, err := srv.ListSecurityFindings(ctx, &platform.ListSecurityFindingsRequest{
		Namespace: "default", ScanName: "nightly", RunName: "nightly-1",
		Repository: "github.com/acme/payments", Severity: "high", Status: "open",
		Category: "injection", Search: "sql", MinScore: 7.5, IncludeDuplicates: true,
		Limit: 50, Offset: 10,
	})
	if err != nil {
		t.Fatalf("ListSecurityFindings() error = %v", err)
	}
	want := store.SecurityFindingFilter{
		Namespace: "default", ScanName: "nightly", RunName: "nightly-1",
		Repository: "github.com/acme/payments", Severity: "high", Status: "open",
		Category: "injection", Search: "sql", MinScore: 7.5, IncludeDuplicates: true,
		Limit: 50, Offset: 10,
	}
	if !reflect.DeepEqual(sec.lastFilter, want) {
		t.Fatalf("filter = %+v, want %+v", sec.lastFilter, want)
	}
}

func TestRunScopedFindingReadsUsePersistedScanID(t *testing.T) {
	sec := newMockSecurityStore()
	scanID := uuid.New()
	sec.scans = []store.SecurityScanRecord{{
		ID: scanID, Namespace: "default", ScanName: "nightly", RunName: "nightly-execution-1",
	}}
	for _, runName := range []string{"nightly-hunt-1", "nightly-review-1"} {
		finding := newTestFinding("default")
		finding.ScanID = scanID
		finding.RunName = runName
		sec.findings[finding.ID] = finding
	}
	sec.summary = map[string]int32{"high": 1, "medium": 1, "total": 2}
	srv := newSecurityTestServer(t, sec)
	ctx := actorContext("alice", "admin", "", "")

	list, err := srv.ListSecurityFindings(ctx, &platform.ListSecurityFindingsRequest{
		Namespace: "default", RunName: "nightly-execution-1",
	})
	if err != nil {
		t.Fatalf("ListSecurityFindings() error = %v", err)
	}
	if len(list.Findings) != 2 {
		t.Fatalf("findings = %d, want both sibling task findings", len(list.Findings))
	}
	if sec.lastFilter.ScanID != scanID || sec.lastFilter.RunName != "" {
		t.Fatalf("list scope = scan ID %s / run %q, want %s / empty", sec.lastFilter.ScanID, sec.lastFilter.RunName, scanID)
	}

	resp, err := srv.GetSecurityFindingSummary(ctx, &platform.GetSecurityFindingSummaryRequest{
		Namespace: "default", RunName: "nightly-execution-1",
	})
	if err != nil {
		t.Fatalf("GetSecurityFindingSummary() error = %v", err)
	}
	if resp.GetCounts()["total"] != 2 {
		t.Fatalf("summary counts = %v, want total 2", resp.GetCounts())
	}
	if sec.summaryScanID != scanID || sec.summaryRunName != "" {
		t.Fatalf("summary scope = scan ID %s / run %q, want %s / empty", sec.summaryScanID, sec.summaryRunName, scanID)
	}
}

func newTestFinding(namespace string) *store.SecurityFindingRecord {
	return &store.SecurityFindingRecord{
		ID: uuid.New(), ScanID: uuid.New(), Namespace: namespace, ScanName: "nightly",
		RunName: "nightly-1", Fingerprint: "fp-1", Title: "SQL injection",
		Category: "injection", Severity: "critical", Confidence: "high",
		Repository: "github.com/acme/payments", Revision: "abc123",
		FilePath: "internal/db/query.go", StartLine: 10, EndLine: 20, Symbol: "Query",
		CWE: []string{"CWE-89"}, Description: "desc", Impact: "impact",
		AttackVector: "vector", Remediation: "fix it", References: []string{"https://example.com"},
		SourceAgent: "scanner", ScanStep: "step-1", Score: 9.5, Status: "open",
		SourceKind: "scanner", Tool: "semgrep", ToolVersion: "1.50.0", RuleID: "go.sqli",
		CorrelatedFingerprints: []string{"fp-agent-1"},
		Occurrences:            2, Raw: []byte(`{"k":"v"}`),
		FirstSeenAt: time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC),
		LastSeenAt:  time.Date(2026, 2, 2, 0, 0, 0, 0, time.UTC),
	}
}

func TestGetSecurityFinding(t *testing.T) {
	sec := newMockSecurityStore()
	// Finding-by-ID lookups are scoped to the caller's authorized namespace.
	callerNS := deriveUserNamespaceName("", "alice")
	finding := newTestFinding(callerNS)
	sec.findings[finding.ID] = finding
	sec.events[finding.ID] = []store.SecurityFindingEvent{{
		ID: 1, FindingID: finding.ID, EventType: "created", Actor: "scanner",
		Detail: []byte(`{}`), CreatedAt: time.Now(),
	}}
	srv := newSecurityTestServer(t, sec)
	ctx := actorContext("alice", "admin", "", "")

	resp, err := srv.GetSecurityFinding(ctx, &platform.GetSecurityFindingRequest{Id: finding.ID.String()})
	if err != nil {
		t.Fatalf("GetSecurityFinding() error = %v", err)
	}
	pb := resp.GetFinding()
	if pb.GetId() != finding.ID.String() || pb.GetTitle() != "SQL injection" ||
		pb.GetSeverity() != "critical" || pb.GetScore() != 9.5 ||
		len(pb.GetCwe()) != 1 || pb.GetCwe()[0] != "CWE-89" ||
		pb.GetRaw() != `{"k":"v"}` || !pb.GetFirstSeenAt().AsTime().Equal(finding.FirstSeenAt) {
		t.Fatalf("finding proto = %+v", pb)
	}
	if len(resp.GetEvents()) != 1 || resp.GetEvents()[0].GetEventType() != "created" {
		t.Fatalf("events = %+v", resp.GetEvents())
	}
	if pb.GetSourceKind() != "scanner" || pb.GetTool() != "semgrep" || pb.GetToolVersion() != "1.50.0" ||
		pb.GetRuleId() != "go.sqli" || len(pb.GetCorrelatedFingerprints()) != 1 ||
		pb.GetCorrelatedFingerprints()[0] != "fp-agent-1" {
		t.Fatalf("finding provenance = %+v", pb)
	}
	if sec.lastGetNamespace != callerNS || sec.lastEventsNamespace != callerNS {
		t.Fatalf("store namespaces = %q/%q, want %q", sec.lastGetNamespace, sec.lastEventsNamespace, callerNS)
	}

	if _, err := srv.GetSecurityFinding(ctx, &platform.GetSecurityFindingRequest{Id: uuid.NewString()}); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("missing finding code = %v, want NotFound", connect.CodeOf(err))
	}
	if _, err := srv.GetSecurityFinding(ctx, &platform.GetSecurityFindingRequest{Id: "not-a-uuid"}); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("bad id code = %v, want InvalidArgument", connect.CodeOf(err))
	}
}

func TestUpdateSecurityFindingStatus(t *testing.T) {
	sec := newMockSecurityStore()
	callerNS := deriveUserNamespaceName("", "alice")
	finding := newTestFinding(callerNS)
	sec.findings[finding.ID] = finding
	srv := newSecurityTestServer(t, sec)
	ctx := actorContext("alice", "admin", "", "")

	updated, err := srv.UpdateSecurityFindingStatus(ctx, &platform.UpdateSecurityFindingStatusRequest{
		Id: finding.ID.String(), Status: store.SecurityFindingStatusTriaged, Note: "looked at it",
	})
	if err != nil {
		t.Fatalf("UpdateSecurityFindingStatus() error = %v", err)
	}
	if updated.GetStatus() != "triaged" {
		t.Fatalf("status = %q, want triaged", updated.GetStatus())
	}
	if sec.lastStatusNamespace != callerNS {
		t.Fatalf("status namespace = %q, want %q", sec.lastStatusNamespace, callerNS)
	}
	if !sec.lastStatusHumanActor {
		t.Fatal("dashboard status changes must be recorded as human decisions so accepted_risk stays available to reviewers")
	}
	events := sec.events[finding.ID]
	if len(events) != 1 || events[0].Actor != "alice" || events[0].Note != "looked at it" {
		t.Fatalf("events = %+v, want one status event with actor alice", events)
	}

	if _, err := srv.UpdateSecurityFindingStatus(ctx, &platform.UpdateSecurityFindingStatusRequest{
		Id: finding.ID.String(), Status: "bogus",
	}); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("invalid status code = %v, want InvalidArgument", connect.CodeOf(err))
	}

	if _, err := srv.UpdateSecurityFindingStatus(actorContext("", "", "", ""), &platform.UpdateSecurityFindingStatusRequest{
		Id: finding.ID.String(), Status: store.SecurityFindingStatusFixed,
	}); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("unauthenticated code = %v, want Unauthenticated", connect.CodeOf(err))
	}
}

func TestGetSecurityFindingSummary(t *testing.T) {
	sec := newMockSecurityStore()
	sec.summary = map[string]int32{"critical": 1, "total": 3, "open": 2}
	srv := newSecurityTestServer(t, sec)
	ctx := actorContext("alice", "admin", "", "")

	resp, err := srv.GetSecurityFindingSummary(ctx, &platform.GetSecurityFindingSummaryRequest{
		Namespace: "default", ScanName: "nightly", RunName: "nightly-1",
	})
	if err != nil {
		t.Fatalf("GetSecurityFindingSummary() error = %v", err)
	}
	if resp.GetCounts()["critical"] != 1 || resp.GetCounts()["total"] != 3 {
		t.Fatalf("counts = %+v", resp.GetCounts())
	}
	if sec.summaryNamespace != "default" || sec.summaryScanName != "nightly" || sec.summaryRunName != "nightly-1" {
		t.Fatalf("summary scope = %q/%q/%q", sec.summaryNamespace, sec.summaryScanName, sec.summaryRunName)
	}
}

func TestSecurityReadsScopeToAuthorizedNamespace(t *testing.T) {
	// A non-admin member requesting another user's personal namespace is
	// denied by authorizeRequestNamespace before the store is consulted.
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

	if _, err := srv.ListSecurityScans(ctx, &platform.ListSecurityScansRequest{Namespace: "user-bob"}); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("ListSecurityScans code = %v, want PermissionDenied", connect.CodeOf(err))
	}
	// A finding in another user's namespace is reported as NotFound — not
	// PermissionDenied — so the endpoint is no UUID-existence oracle.
	if _, err := srv.GetSecurityFinding(ctx, &platform.GetSecurityFindingRequest{Id: finding.ID.String()}); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("GetSecurityFinding code = %v, want NotFound", connect.CodeOf(err))
	}
	if callerNS := deriveUserNamespaceName("", "alice"); sec.lastGetNamespace != callerNS {
		t.Fatalf("store queried namespace %q, want caller namespace %q", sec.lastGetNamespace, callerNS)
	}
	if _, err := srv.UpdateSecurityFindingStatus(ctx, &platform.UpdateSecurityFindingStatusRequest{
		Id: finding.ID.String(), Status: store.SecurityFindingStatusTriaged,
	}); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("UpdateSecurityFindingStatus code = %v, want NotFound", connect.CodeOf(err))
	}
	if finding.Status != "open" {
		t.Fatalf("foreign finding status = %q, want untouched", finding.Status)
	}
}

func TestSecurityFindingForeignAndMissingAreIndistinguishable(t *testing.T) {
	sec := newMockSecurityStore()
	foreign := newTestFinding("user-bob")
	sec.findings[foreign.ID] = foreign
	scheme := newDashboardTestScheme(t)
	srv := &Server{
		k8sClient:  fake.NewClientBuilder().WithScheme(scheme).WithObjects(userNamespaceObj("user-bob")).Build(),
		scheme:     scheme,
		stateStore: sec,
	}
	ctx := actorContext("alice", "member", "", "")

	_, foreignErr := srv.GetSecurityFinding(ctx, &platform.GetSecurityFindingRequest{Id: foreign.ID.String()})
	_, missingErr := srv.GetSecurityFinding(ctx, &platform.GetSecurityFindingRequest{Id: uuid.NewString()})
	if connect.CodeOf(foreignErr) != connect.CodeNotFound || connect.CodeOf(missingErr) != connect.CodeNotFound {
		t.Fatalf("codes = %v/%v, want NotFound/NotFound", connect.CodeOf(foreignErr), connect.CodeOf(missingErr))
	}
}

func TestSecurityFindingRPCsHonorRequestedSharedNamespace(t *testing.T) {
	// Findings listed from a shared (non-personal) namespace must stay
	// reachable by ID when the client sends the namespace it is routing on,
	// instead of silently resolving to the caller's personal namespace.
	sec := newMockSecurityStore()
	finding := newTestFinding("team-shared")
	sec.findings[finding.ID] = finding
	scheme := newDashboardTestScheme(t)
	srv := &Server{
		k8sClient:  fake.NewClientBuilder().WithScheme(scheme).WithObjects(sharedNamespaceObj("team-shared")).Build(),
		scheme:     scheme,
		stateStore: sec,
	}
	ctx := actorContext("alice", "member", "", "")

	got, err := srv.GetSecurityFinding(ctx, &platform.GetSecurityFindingRequest{
		Id: finding.ID.String(), Namespace: "team-shared",
	})
	if err != nil {
		t.Fatalf("GetSecurityFinding() error = %v", err)
	}
	if got.GetFinding().GetId() != finding.ID.String() {
		t.Fatalf("finding id = %q, want %q", got.GetFinding().GetId(), finding.ID.String())
	}
	if sec.lastGetNamespace != "team-shared" || sec.lastEventsNamespace != "team-shared" {
		t.Fatalf("store queried namespaces %q/%q, want team-shared", sec.lastGetNamespace, sec.lastEventsNamespace)
	}

	updated, err := srv.UpdateSecurityFindingStatus(ctx, &platform.UpdateSecurityFindingStatusRequest{
		Id: finding.ID.String(), Namespace: "team-shared", Status: store.SecurityFindingStatusTriaged,
	})
	if err != nil {
		t.Fatalf("UpdateSecurityFindingStatus() error = %v", err)
	}
	if updated.GetStatus() != store.SecurityFindingStatusTriaged {
		t.Fatalf("status = %q, want triaged", updated.GetStatus())
	}
	if sec.lastStatusNamespace != "team-shared" {
		t.Fatalf("status updated in namespace %q, want team-shared", sec.lastStatusNamespace)
	}

	// An explicitly requested foreign personal namespace stays denied.
	if _, err := srv.GetSecurityFinding(ctx, &platform.GetSecurityFindingRequest{
		Id: finding.ID.String(), Namespace: "user-bob",
	}); connect.CodeOf(err) == 0 {
		t.Fatal("GetSecurityFinding(foreign personal namespace) succeeded, want error")
	}
}

func TestGetSecurityFindingScanOwnership(t *testing.T) {
	sec := newMockSecurityStore()
	callerNS := deriveUserNamespaceName("", "alice")
	finding := newTestFinding(callerNS) // ScanName: "nightly"
	sec.findings[finding.ID] = finding
	srv := newSecurityTestServer(t, sec)
	ctx := actorContext("alice", "admin", "", "")

	got, err := srv.GetSecurityFinding(ctx, &platform.GetSecurityFindingRequest{
		Id: finding.ID.String(), ScanName: "nightly",
	})
	if err != nil {
		t.Fatalf("GetSecurityFinding(matching scan) error = %v", err)
	}
	if got.GetFinding().GetId() != finding.ID.String() {
		t.Fatalf("finding id = %q, want %q", got.GetFinding().GetId(), finding.ID.String())
	}

	// A finding reached through another scan's route is NotFound, exactly
	// like a missing finding, so scan routes cannot leak foreign findings.
	if _, err := srv.GetSecurityFinding(ctx, &platform.GetSecurityFindingRequest{
		Id: finding.ID.String(), ScanName: "weekly",
	}); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("scan mismatch code = %v, want NotFound", connect.CodeOf(err))
	}
}

func TestListSecurityFindingEvents(t *testing.T) {
	sec := newMockSecurityStore()
	callerNS := deriveUserNamespaceName("", "alice")
	finding := newTestFinding(callerNS)
	sec.findings[finding.ID] = finding
	sec.events[finding.ID] = []store.SecurityFindingEvent{
		{ID: 2, FindingID: finding.ID, EventType: "status_changed", Actor: "alice",
			Note: "checked", Detail: []byte(`{"from":"open","to":"triaged"}`), CreatedAt: time.Now()},
		{ID: 1, FindingID: finding.ID, EventType: "reobserved", Actor: "scanner",
			Detail: []byte(`{}`), CreatedAt: time.Now().Add(-time.Hour)},
	}
	srv := newSecurityTestServer(t, sec)
	ctx := actorContext("alice", "admin", "", "")

	resp, err := srv.ListSecurityFindingEvents(ctx, &platform.ListSecurityFindingEventsRequest{
		Id: finding.ID.String(), ScanName: "nightly",
	})
	if err != nil {
		t.Fatalf("ListSecurityFindingEvents() error = %v", err)
	}
	if len(resp.Events) != 2 {
		t.Fatalf("events = %d, want 2", len(resp.Events))
	}
	first := resp.Events[0]
	if first.GetEventType() != "status_changed" || first.GetActor() != "alice" ||
		first.GetNote() != "checked" || first.GetDetail() != `{"from":"open","to":"triaged"}` {
		t.Fatalf("first event = %+v", first)
	}
	if sec.lastEventsNamespace != callerNS {
		t.Fatalf("events namespace = %q, want %q", sec.lastEventsNamespace, callerNS)
	}

	if _, err := srv.ListSecurityFindingEvents(ctx, &platform.ListSecurityFindingEventsRequest{
		Id: finding.ID.String(), ScanName: "weekly",
	}); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("scan mismatch code = %v, want NotFound", connect.CodeOf(err))
	}
	if _, err := srv.ListSecurityFindingEvents(ctx, &platform.ListSecurityFindingEventsRequest{
		Id: uuid.NewString(),
	}); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("unknown finding code = %v, want NotFound", connect.CodeOf(err))
	}
}

func TestListSecurityFindingEventsCrossNamespaceDenied(t *testing.T) {
	sec := newMockSecurityStore()
	foreign := newTestFinding("user-bob")
	sec.findings[foreign.ID] = foreign
	sec.events[foreign.ID] = []store.SecurityFindingEvent{{ID: 1, FindingID: foreign.ID, EventType: "comment"}}
	scheme := newDashboardTestScheme(t)
	srv := &Server{
		k8sClient:  fake.NewClientBuilder().WithScheme(scheme).WithObjects(userNamespaceObj("user-bob")).Build(),
		scheme:     scheme,
		stateStore: sec,
	}
	ctx := actorContext("alice", "member", "", "")

	// Without an explicit namespace the lookup resolves to alice's personal
	// namespace and misses; requesting bob's namespace outright is denied.
	if _, err := srv.ListSecurityFindingEvents(ctx, &platform.ListSecurityFindingEventsRequest{
		Id: foreign.ID.String(),
	}); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("implicit namespace code = %v, want NotFound", connect.CodeOf(err))
	}
	if _, err := srv.ListSecurityFindingEvents(ctx, &platform.ListSecurityFindingEventsRequest{
		Id: foreign.ID.String(), Namespace: "user-bob",
	}); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("explicit foreign namespace code = %v, want PermissionDenied", connect.CodeOf(err))
	}
}

func TestAddSecurityFindingComment(t *testing.T) {
	sec := newMockSecurityStore()
	callerNS := deriveUserNamespaceName("", "alice")
	finding := newTestFinding(callerNS)
	sec.findings[finding.ID] = finding
	srv := newSecurityTestServer(t, sec)
	ctx := actorContext("alice", "admin", "", "")

	event, err := srv.AddSecurityFindingComment(ctx, &platform.AddSecurityFindingCommentRequest{
		Id: finding.ID.String(), ScanName: "nightly", Body: "  looks exploitable, needs a fix  ",
	})
	if err != nil {
		t.Fatalf("AddSecurityFindingComment() error = %v", err)
	}
	// The comment is stamped with the authenticated actor and trimmed.
	if event.GetEventType() != "comment" || event.GetActor() != "alice" ||
		event.GetNote() != "looks exploitable, needs a fix" {
		t.Fatalf("comment event = %+v", event)
	}
	if sec.lastCommentNamespace != callerNS {
		t.Fatalf("comment namespace = %q, want %q", sec.lastCommentNamespace, callerNS)
	}
	if events := sec.events[finding.ID]; len(events) != 1 || events[0].EventType != "comment" {
		t.Fatalf("stored events = %+v, want one comment", events)
	}
}

func TestAddSecurityFindingCommentValidation(t *testing.T) {
	sec := newMockSecurityStore()
	callerNS := deriveUserNamespaceName("", "alice")
	finding := newTestFinding(callerNS)
	sec.findings[finding.ID] = finding
	srv := newSecurityTestServer(t, sec)
	ctx := actorContext("alice", "admin", "", "")

	if _, err := srv.AddSecurityFindingComment(ctx, &platform.AddSecurityFindingCommentRequest{
		Id: finding.ID.String(), Body: "   ",
	}); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("empty body code = %v, want InvalidArgument", connect.CodeOf(err))
	}
	long := strings.Repeat("x", maxSecurityFindingCommentLen+1)
	if _, err := srv.AddSecurityFindingComment(ctx, &platform.AddSecurityFindingCommentRequest{
		Id: finding.ID.String(), Body: long,
	}); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("oversized body code = %v, want InvalidArgument", connect.CodeOf(err))
	}
	// Exactly at the limit is accepted.
	if _, err := srv.AddSecurityFindingComment(ctx, &platform.AddSecurityFindingCommentRequest{
		Id: finding.ID.String(), Body: strings.Repeat("x", maxSecurityFindingCommentLen),
	}); err != nil {
		t.Fatalf("at-limit body error = %v", err)
	}
	if _, err := srv.AddSecurityFindingComment(actorContext("", "", "", ""), &platform.AddSecurityFindingCommentRequest{
		Id: finding.ID.String(), Body: "hi",
	}); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("unauthenticated code = %v, want Unauthenticated", connect.CodeOf(err))
	}
	if _, err := srv.AddSecurityFindingComment(ctx, &platform.AddSecurityFindingCommentRequest{
		Id: uuid.NewString(), Body: "hi",
	}); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("unknown finding code = %v, want NotFound", connect.CodeOf(err))
	}
	if _, err := srv.AddSecurityFindingComment(ctx, &platform.AddSecurityFindingCommentRequest{
		Id: finding.ID.String(), ScanName: "weekly", Body: "hi",
	}); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("scan mismatch code = %v, want NotFound", connect.CodeOf(err))
	}
}

func TestAddSecurityFindingCommentCrossNamespaceDenied(t *testing.T) {
	sec := newMockSecurityStore()
	foreign := newTestFinding("user-bob")
	sec.findings[foreign.ID] = foreign
	scheme := newDashboardTestScheme(t)
	srv := &Server{
		k8sClient:  fake.NewClientBuilder().WithScheme(scheme).WithObjects(userNamespaceObj("user-bob")).Build(),
		scheme:     scheme,
		stateStore: sec,
	}
	ctx := actorContext("alice", "member", "", "")

	if _, err := srv.AddSecurityFindingComment(ctx, &platform.AddSecurityFindingCommentRequest{
		Id: foreign.ID.String(), Body: "hi",
	}); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("implicit namespace code = %v, want NotFound", connect.CodeOf(err))
	}
	if _, err := srv.AddSecurityFindingComment(ctx, &platform.AddSecurityFindingCommentRequest{
		Id: foreign.ID.String(), Namespace: "user-bob", Body: "hi",
	}); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("explicit foreign namespace code = %v, want PermissionDenied", connect.CodeOf(err))
	}
	if len(sec.events[foreign.ID]) != 0 {
		t.Fatalf("foreign finding gained events: %+v", sec.events[foreign.ID])
	}
}

// newScanVisibilityStore seeds a mock store with three scans in "default":
// one owned by alice, one owned by bob, one unowned — each with one finding.
// The returned findings map is keyed by scan name.
func newScanVisibilityStore(t *testing.T) (*mockSecurityStore, map[string]*store.SecurityFindingRecord) {
	t.Helper()
	sec := newMockSecurityStore()
	ctx := context.Background()
	if err := sec.SetResourceOwner(ctx, securityScanResourceType, "alice-scan", "default", "alice"); err != nil {
		t.Fatalf("SetResourceOwner(alice-scan): %v", err)
	}
	if err := sec.SetResourceOwner(ctx, securityScanResourceType, "bob-scan", "default", "bob"); err != nil {
		t.Fatalf("SetResourceOwner(bob-scan): %v", err)
	}
	findings := map[string]*store.SecurityFindingRecord{}
	for _, scanName := range []string{"alice-scan", "bob-scan", "unowned-scan"} {
		runName := scanName + "-run-1"
		sec.scans = append(sec.scans, store.SecurityScanRecord{
			ID: uuid.New(), Namespace: "default", ScanName: scanName, RunName: runName,
		})
		finding := newTestFinding("default")
		finding.ScanName = scanName
		finding.RunName = runName
		sec.findings[finding.ID] = finding
		findings[scanName] = finding
	}
	return sec, findings
}

func TestSecurityScanRecordsHiddenFromNonOwner(t *testing.T) {
	sec, findings := newScanVisibilityStore(t)
	srv := newSecurityTestServer(t, sec)
	bob := actorContext("bob", "member", "", "")

	scans, err := srv.ListSecurityScans(bob, &platform.ListSecurityScansRequest{Namespace: "default"})
	if err != nil {
		t.Fatalf("ListSecurityScans() error = %v", err)
	}
	if len(scans.Scans) != 2 {
		t.Fatalf("scans = %d, want bob-scan and unowned-scan only", len(scans.Scans))
	}
	for _, scan := range scans.Scans {
		if scan.GetScanName() == "alice-scan" {
			t.Fatalf("alice-scan leaked into bob's scan list")
		}
	}

	if _, err := srv.GetSecurityScan(bob, &platform.GetSecurityScanRequest{Namespace: "default", RunName: "alice-scan-run-1"}); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("hidden GetSecurityScan code = %v, want NotFound", connect.CodeOf(err))
	}
	if _, err := srv.GetSecurityScan(bob, &platform.GetSecurityScanRequest{Namespace: "default", RunName: "unowned-scan-run-1"}); err != nil {
		t.Fatalf("unowned GetSecurityScan error = %v, want visible", err)
	}

	list, err := srv.ListSecurityFindings(bob, &platform.ListSecurityFindingsRequest{Namespace: "default"})
	if err != nil {
		t.Fatalf("ListSecurityFindings() error = %v", err)
	}
	if !slices.Equal(sec.lastFilter.ExcludedScanNames, []string{"alice-scan"}) {
		t.Fatalf("ExcludedScanNames = %v, want [alice-scan]", sec.lastFilter.ExcludedScanNames)
	}
	if len(list.Findings) != 2 {
		t.Fatalf("findings = %d, want alice's excluded", len(list.Findings))
	}
	for _, f := range list.Findings {
		if f.GetScanName() == "alice-scan" {
			t.Fatalf("alice-scan finding leaked into bob's list")
		}
	}
	if _, err := srv.ListSecurityFindings(bob, &platform.ListSecurityFindingsRequest{Namespace: "default", ScanName: "alice-scan"}); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("hidden scan-scoped list code = %v, want NotFound", connect.CodeOf(err))
	}
	if _, err := srv.ListSecurityFindings(bob, &platform.ListSecurityFindingsRequest{Namespace: "default", RunName: "alice-scan-run-1"}); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("hidden run-scoped list code = %v, want NotFound", connect.CodeOf(err))
	}

	aliceFinding := findings["alice-scan"]
	if _, err := srv.GetSecurityFinding(bob, &platform.GetSecurityFindingRequest{Id: aliceFinding.ID.String(), Namespace: "default"}); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("hidden GetSecurityFinding code = %v, want NotFound", connect.CodeOf(err))
	}
	if _, err := srv.UpdateSecurityFindingStatus(bob, &platform.UpdateSecurityFindingStatusRequest{
		Id: aliceFinding.ID.String(), Namespace: "default", Status: store.SecurityFindingStatusTriaged,
	}); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("hidden UpdateSecurityFindingStatus code = %v, want NotFound", connect.CodeOf(err))
	}
	if aliceFinding.Status != "open" {
		t.Fatalf("hidden finding status = %q, want untouched", aliceFinding.Status)
	}
	if _, err := srv.AddSecurityFindingComment(bob, &platform.AddSecurityFindingCommentRequest{
		Id: aliceFinding.ID.String(), Namespace: "default", Body: "hi",
	}); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("hidden AddSecurityFindingComment code = %v, want NotFound", connect.CodeOf(err))
	}
	if _, err := srv.BulkUpdateSecurityFindingStatus(bob, &platform.BulkUpdateSecurityFindingStatusRequest{
		Namespace: "default", Ids: []string{aliceFinding.ID.String()}, Status: store.SecurityFindingStatusTriaged,
	}); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("hidden bulk update code = %v, want NotFound", connect.CodeOf(err))
	}
	if _, err := srv.BulkUpdateSecurityFindingStatus(bob, &platform.BulkUpdateSecurityFindingStatusRequest{
		Namespace: "default", ScanName: "alice-scan", Ids: []string{aliceFinding.ID.String()}, Status: store.SecurityFindingStatusTriaged,
	}); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("hidden scan-scoped bulk update code = %v, want NotFound", connect.CodeOf(err))
	}
	if _, err := srv.ExportSecurityFindingAuditLog(bob, &platform.ExportSecurityFindingAuditLogRequest{
		Namespace: "default", ScanName: "alice-scan",
	}); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("hidden audit export code = %v, want NotFound", connect.CodeOf(err))
	}
	if _, err := srv.GetSecurityScanReport(bob, &platform.GetSecurityScanReportRequest{
		Namespace: "default", RunName: "alice-scan-run-1",
	}); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("hidden report code = %v, want NotFound", connect.CodeOf(err))
	}

	// A visible finding stays writable: viewer-level visibility is enough.
	bobFinding := findings["bob-scan"]
	if _, err := srv.UpdateSecurityFindingStatus(bob, &platform.UpdateSecurityFindingStatusRequest{
		Id: bobFinding.ID.String(), Namespace: "default", Status: store.SecurityFindingStatusTriaged,
	}); err != nil {
		t.Fatalf("own finding update error = %v", err)
	}
	unownedFinding := findings["unowned-scan"]
	if _, err := srv.GetSecurityFinding(bob, &platform.GetSecurityFindingRequest{Id: unownedFinding.ID.String(), Namespace: "default"}); err != nil {
		t.Fatalf("unowned finding read error = %v, want visible", err)
	}
}

func TestSecurityFindingSummaryExcludesHiddenScans(t *testing.T) {
	sec, _ := newScanVisibilityStore(t)
	sec.summary = map[string]int32{"total": 2}
	srv := newSecurityTestServer(t, sec)
	bob := actorContext("bob", "member", "", "")

	if _, err := srv.GetSecurityFindingSummary(bob, &platform.GetSecurityFindingSummaryRequest{Namespace: "default"}); err != nil {
		t.Fatalf("GetSecurityFindingSummary() error = %v", err)
	}
	if !slices.Equal(sec.lastSummaryExcluded, []string{"alice-scan"}) {
		t.Fatalf("summary ExcludedScanNames = %v, want [alice-scan]", sec.lastSummaryExcluded)
	}
	if !slices.Equal(sec.lastTrendsExcluded, []string{"alice-scan"}) {
		t.Fatalf("trends ExcludedScanNames = %v, want [alice-scan]", sec.lastTrendsExcluded)
	}
	if _, err := srv.GetSecurityFindingSummary(bob, &platform.GetSecurityFindingSummaryRequest{Namespace: "default", ScanName: "alice-scan"}); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("hidden scan summary code = %v, want NotFound", connect.CodeOf(err))
	}
	if _, err := srv.GetSecurityFindingSummary(bob, &platform.GetSecurityFindingSummaryRequest{Namespace: "default", RunName: "alice-scan-run-1"}); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("hidden run summary code = %v, want NotFound", connect.CodeOf(err))
	}
}

func TestSecurityScanRecordsVisibleToSharedUserAndAdmin(t *testing.T) {
	sec, findings := newScanVisibilityStore(t)
	sec.shares = []store.ResourceShare{{
		ResourceType: securityScanResourceType, ResourceID: "alice-scan",
		ResourceNamespace: "default", SharedWithUserID: "bob", Permission: "viewer",
	}}
	srv := newSecurityTestServer(t, sec)

	bob := actorContext("bob", "member", "", "")
	scans, err := srv.ListSecurityScans(bob, &platform.ListSecurityScansRequest{Namespace: "default"})
	if err != nil {
		t.Fatalf("ListSecurityScans() error = %v", err)
	}
	if len(scans.Scans) != 3 {
		t.Fatalf("shared scans = %d, want all 3", len(scans.Scans))
	}
	if _, err := srv.GetSecurityScan(bob, &platform.GetSecurityScanRequest{Namespace: "default", RunName: "alice-scan-run-1"}); err != nil {
		t.Fatalf("shared GetSecurityScan error = %v", err)
	}
	if _, err := srv.GetSecurityFinding(bob, &platform.GetSecurityFindingRequest{Id: findings["alice-scan"].ID.String(), Namespace: "default"}); err != nil {
		t.Fatalf("shared GetSecurityFinding error = %v", err)
	}
	if _, err := srv.GetSecurityFindingSummary(bob, &platform.GetSecurityFindingSummaryRequest{Namespace: "default"}); err != nil {
		t.Fatalf("GetSecurityFindingSummary() error = %v", err)
	}
	if len(sec.lastSummaryExcluded) != 0 {
		t.Fatalf("shared summary exclusions = %v, want none", sec.lastSummaryExcluded)
	}

	sec.shares = nil
	admin := actorContext("carol", "admin", "", "")
	scans, err = srv.ListSecurityScans(admin, &platform.ListSecurityScansRequest{Namespace: "default"})
	if err != nil {
		t.Fatalf("admin ListSecurityScans() error = %v", err)
	}
	if len(scans.Scans) != 3 {
		t.Fatalf("admin scans = %d, want all 3", len(scans.Scans))
	}
	if _, err := srv.GetSecurityFindingSummary(admin, &platform.GetSecurityFindingSummaryRequest{Namespace: "default"}); err != nil {
		t.Fatalf("admin GetSecurityFindingSummary() error = %v", err)
	}
	if len(sec.lastSummaryExcluded) != 0 {
		t.Fatalf("admin summary exclusions = %v, want none", sec.lastSummaryExcluded)
	}
}

func TestSecurityOverviewExcludesHiddenScans(t *testing.T) {
	sec, _ := newScanVisibilityStore(t)
	sec.summary = map[string]int32{"total": 2}
	srv := newSecurityOverviewTestServer(t, sec)
	bob := actorContext("bob", "member", "", "")

	resp, err := srv.GetSecurityOverview(bob, &platform.GetSecurityOverviewRequest{Namespace: "default"})
	if err != nil {
		t.Fatalf("GetSecurityOverview() error = %v", err)
	}
	if len(resp.Warnings) != 0 {
		t.Fatalf("warnings = %v, want none", resp.Warnings)
	}
	for _, scan := range append(append([]*platform.SecurityScan{}, resp.ActiveScans...), resp.RecentScans...) {
		if scan.GetScanName() == "alice-scan" {
			t.Fatalf("alice-scan leaked into bob's overview")
		}
	}
	if got := len(resp.ActiveScans) + len(resp.RecentScans); got != 2 {
		t.Fatalf("overview scans = %d, want 2", got)
	}
	if !slices.Equal(sec.lastSummaryExcluded, []string{"alice-scan"}) {
		t.Fatalf("overview summary exclusions = %v, want [alice-scan]", sec.lastSummaryExcluded)
	}
	if !slices.Equal(sec.lastTrendsExcluded, []string{"alice-scan"}) {
		t.Fatalf("overview trends exclusions = %v, want [alice-scan]", sec.lastTrendsExcluded)
	}
}
