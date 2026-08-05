package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/store"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type fakeSecurityFindingStore struct {
	scans    map[string]*store.SecurityScanRecord
	findings []*store.SecurityFindingRecord
	events   []store.SecurityFindingEvent
}

func newFakeSecurityFindingStore() *fakeSecurityFindingStore {
	return &fakeSecurityFindingStore{scans: map[string]*store.SecurityScanRecord{}}
}

func (s *fakeSecurityFindingStore) UpsertSecurityScan(_ context.Context, rec *store.SecurityScanRecord) (*store.SecurityScanRecord, error) {
	copied := *rec
	if copied.ID == uuid.Nil {
		copied.ID = uuid.New()
	}
	s.scans[rec.Namespace+"/"+rec.RunName] = &copied
	out := copied
	return &out, nil
}

func (s *fakeSecurityFindingStore) GetSecurityScan(_ context.Context, namespace, runName string) (*store.SecurityScanRecord, error) {
	rec, ok := s.scans[namespace+"/"+runName]
	if !ok {
		return nil, nil
	}
	copied := *rec
	return &copied, nil
}

func (s *fakeSecurityFindingStore) ListSecurityScans(context.Context, string, string, int32) ([]store.SecurityScanRecord, error) {
	return nil, nil
}

func (s *fakeSecurityFindingStore) UpsertSecurityFinding(_ context.Context, rec *store.SecurityFindingRecord) (*store.SecurityFindingRecord, bool, error) {
	for _, existing := range s.findings {
		if existing.Namespace == rec.Namespace && existing.Repository == rec.Repository && existing.Fingerprint == rec.Fingerprint {
			existing.Occurrences++
			existing.LastSeenAt = time.Now().UTC()
			copied := *existing
			return &copied, false, nil
		}
	}
	stored := *rec
	stored.ID = uuid.New()
	if stored.Status == "" {
		stored.Status = store.SecurityFindingStatusOpen
	}
	stored.Occurrences = 1
	s.findings = append(s.findings, &stored)
	copied := stored
	return &copied, true, nil
}

func (s *fakeSecurityFindingStore) ListSecurityFindings(_ context.Context, f store.SecurityFindingFilter) ([]store.SecurityFindingRecord, error) {
	var out []store.SecurityFindingRecord
	for _, rec := range s.findings {
		if f.Namespace != "" && rec.Namespace != f.Namespace {
			continue
		}
		if f.RunName != "" && rec.RunName != f.RunName {
			continue
		}
		if f.Severity != "" && rec.Severity != f.Severity {
			continue
		}
		if f.Category != "" && rec.Category != f.Category {
			continue
		}
		if f.Status != "" && rec.Status != f.Status {
			continue
		}
		if f.Search != "" && !strings.Contains(rec.FilePath, f.Search) && !strings.Contains(rec.Title, f.Search) {
			continue
		}
		out = append(out, *rec)
	}
	return out, nil
}

func (s *fakeSecurityFindingStore) GetSecurityFinding(_ context.Context, id uuid.UUID) (*store.SecurityFindingRecord, error) {
	for _, rec := range s.findings {
		if rec.ID == id {
			copied := *rec
			return &copied, nil
		}
	}
	return nil, nil
}

func (s *fakeSecurityFindingStore) SetSecurityFindingStatus(_ context.Context, id uuid.UUID, status, actor, note string) error {
	if !store.ValidSecurityFindingStatus(status) {
		return context.Canceled
	}
	for _, rec := range s.findings {
		if rec.ID == id {
			rec.Status = status
			s.events = append(s.events, store.SecurityFindingEvent{FindingID: id, EventType: "status_changed", Actor: actor, Note: note})
			return nil
		}
	}
	return context.Canceled
}

func (s *fakeSecurityFindingStore) ListSecurityFindingEvents(context.Context, uuid.UUID, int32) ([]store.SecurityFindingEvent, error) {
	return s.events, nil
}

func (s *fakeSecurityFindingStore) SummarizeSecurityFindings(_ context.Context, namespace, _, runName string) (map[string]int32, error) {
	out := map[string]int32{"critical": 0, "high": 0, "medium": 0, "low": 0, "info": 0, "total": 0, "open": 0}
	for _, rec := range s.findings {
		if rec.Namespace != namespace || rec.RunName != runName {
			continue
		}
		out[rec.Severity]++
		out["total"]++
		if rec.Status == store.SecurityFindingStatusOpen {
			out["open"]++
		}
	}
	return out, nil
}

func (s *fakeSecurityFindingStore) DeleteSecurityScanData(context.Context, string, string) error {
	return nil
}

type securityArtifactTestStore struct {
	store.StateStore
	artifacts map[string]string
}

func (s *securityArtifactTestStore) UpsertArtifact(_ context.Context, _ uuid.UUID, kind, content, _, _ string, _ json.RawMessage) (*store.Artifact, error) {
	if s.artifacts == nil {
		s.artifacts = map[string]string{}
	}
	s.artifacts[kind] = content
	return &store.Artifact{Kind: kind, Content: content}, nil
}

func testScanContext() SecurityScanContext {
	return SecurityScanContext{
		ScanName:   "nightly-scan",
		Namespace:  "default",
		RunName:    "nightly-scan-run-1",
		Repository: "github.com/acme/widget",
		Revision:   "abc1234",
		SessionID:  uuid.New(),
	}
}

func newSecurityTestRegistry(t *testing.T, findingStore store.SecurityFindingStore, stateStore store.StateStore) *Registry {
	t.Helper()
	registry := &Registry{tools: map[string]Tool{}}
	RegisterSecurityScanTools(registry, findingStore, stateStore, testScanContext())
	for _, name := range []string{"report_security_finding", "list_security_findings", "update_security_finding", "submit_security_scan_report"} {
		if registry.Get(name) == nil {
			t.Fatalf("tool %s not registered", name)
		}
		if !registry.Get(name).IsReadOnly() {
			t.Fatalf("tool %s must be read-only", name)
		}
	}
	return registry
}

func execTool(t *testing.T, registry *Registry, name, input string) Result {
	t.Helper()
	result, err := registry.Get(name).Execute(context.Background(), json.RawMessage(input), "")
	if err != nil {
		t.Fatalf("%s returned error: %v", name, err)
	}
	return result
}

func TestSecurityScanContextFromRun(t *testing.T) {
	sessionID := uuid.New()
	tests := []struct {
		name     string
		run      *platformv1alpha1.AgentRun
		wantOK   bool
		wantScan string
		wantRepo string
		wantRev  string
	}{
		{name: "nil run", run: nil, wantOK: false},
		{name: "no annotations", run: &platformv1alpha1.AgentRun{}, wantOK: false},
		{
			name: "scan-name blank",
			run: &platformv1alpha1.AgentRun{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
				SecurityScanNameAnnotation: "  ",
			}}},
			wantOK: false,
		},
		{
			name: "scan-name only",
			run: &platformv1alpha1.AgentRun{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
				SecurityScanNameAnnotation: "scan-a",
			}}},
			wantOK:   true,
			wantScan: "scan-a",
		},
		{
			name: "all annotations",
			run: &platformv1alpha1.AgentRun{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
				SecurityScanNameAnnotation:       "scan-a",
				SecurityScanRepositoryAnnotation: "github.com/acme/widget",
				SecurityScanRevisionAnnotation:   "deadbeef",
			}}},
			wantOK:   true,
			wantScan: "scan-a",
			wantRepo: "github.com/acme/widget",
			wantRev:  "deadbeef",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scanCtx, ok := SecurityScanContextFromRun(tt.run, "ns1", "run1", sessionID)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if !ok {
				return
			}
			if scanCtx.ScanName != tt.wantScan || scanCtx.Repository != tt.wantRepo || scanCtx.Revision != tt.wantRev {
				t.Errorf("got %+v, want scan=%q repo=%q rev=%q", scanCtx, tt.wantScan, tt.wantRepo, tt.wantRev)
			}
			if scanCtx.Namespace != "ns1" || scanCtx.RunName != "run1" || scanCtx.SessionID != sessionID {
				t.Errorf("namespace/run/session not propagated: %+v", scanCtx)
			}
		})
	}
}

func TestReportSecurityFindingValidation(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		wantErrText []string
	}{
		{
			name:        "not json",
			input:       `{`,
			wantErrText: []string{"invalid input"},
		},
		{
			name:        "unknown field",
			input:       `{"title":"x","category":"injection","severity":"high","description":"d","sev":"high"}`,
			wantErrText: []string{"invalid input", "sev"},
		},
		{
			name:        "missing title and description",
			input:       `{"category":"injection","severity":"high"}`,
			wantErrText: []string{"title is required", "description is required"},
		},
		{
			name:        "unknown category",
			input:       `{"title":"x","category":"nonsense","severity":"high","description":"d"}`,
			wantErrText: []string{`unknown category "nonsense"`},
		},
		{
			name:        "unknown severity",
			input:       `{"title":"x","category":"injection","severity":"apocalyptic","description":"d"}`,
			wantErrText: []string{`unknown severity "apocalyptic"`},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			registry := newSecurityTestRegistry(t, newFakeSecurityFindingStore(), nil)
			result := execTool(t, registry, "report_security_finding", tt.input)
			if !result.IsError {
				t.Fatalf("expected error result, got: %s", result.Content)
			}
			for _, want := range tt.wantErrText {
				if !strings.Contains(result.Content, want) {
					t.Errorf("error %q missing %q", result.Content, want)
				}
			}
		})
	}
}

func TestReportSecurityFindingStampsScanContext(t *testing.T) {
	findingStore := newFakeSecurityFindingStore()
	registry := newSecurityTestRegistry(t, findingStore, nil)

	result := execTool(t, registry, "report_security_finding",
		`{"title":"SQL injection in login","category":"sql-injection","severity":"HIGH","description":"user input concatenated into query","file_path":"./internal/db/login.go","start_line":42}`)
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Content)
	}
	if len(findingStore.findings) != 1 {
		t.Fatalf("expected 1 stored finding, got %d", len(findingStore.findings))
	}
	rec := findingStore.findings[0]
	scanCtx := testScanContext()
	if rec.Repository != scanCtx.Repository {
		t.Errorf("repository = %q, want stamped %q", rec.Repository, scanCtx.Repository)
	}
	if rec.Revision != scanCtx.Revision {
		t.Errorf("revision = %q, want stamped %q", rec.Revision, scanCtx.Revision)
	}
	if rec.SourceAgent != scanCtx.RunName {
		t.Errorf("source agent = %q, want stamped %q", rec.SourceAgent, scanCtx.RunName)
	}
	if rec.Namespace != scanCtx.Namespace || rec.ScanName != scanCtx.ScanName || rec.RunName != scanCtx.RunName {
		t.Errorf("scan scope not stamped: %+v", rec)
	}
	if rec.Category != "injection" {
		t.Errorf("category = %q, want normalized %q", rec.Category, "injection")
	}
	if rec.Severity != "high" {
		t.Errorf("severity = %q, want normalized %q", rec.Severity, "high")
	}
	if rec.FilePath != "internal/db/login.go" {
		t.Errorf("file path = %q, want normalized %q", rec.FilePath, "internal/db/login.go")
	}
	if rec.Fingerprint == "" || !strings.Contains(result.Content, rec.Fingerprint) {
		t.Errorf("result %q must include fingerprint %q", result.Content, rec.Fingerprint)
	}

	explicit := execTool(t, registry, "report_security_finding",
		`{"title":"XSS in search","category":"xss","severity":"medium","description":"d","repository":"github.com/other/repo","revision":"fff","source_agent":"custom-agent"}`)
	if explicit.IsError {
		t.Fatalf("unexpected error: %s", explicit.Content)
	}
	rec = findingStore.findings[1]
	if rec.Repository != "github.com/other/repo" || rec.Revision != "fff" || rec.SourceAgent != "custom-agent" {
		t.Errorf("explicit values must not be overwritten: %+v", rec)
	}
}

func TestSecurityFindingInMemoryFallback(t *testing.T) {
	registry := newSecurityTestRegistry(t, nil, nil)
	finding := `{"title":"SQL injection in login","category":"injection","severity":"high","description":"bad","file_path":"a/b.go"}`

	first := execTool(t, registry, "report_security_finding", finding)
	if first.IsError {
		t.Fatalf("unexpected error: %s", first.Content)
	}
	if !strings.Contains(first.Content, "Finding recorded") {
		t.Errorf("first report should create: %s", first.Content)
	}

	second := execTool(t, registry, "report_security_finding", finding)
	if second.IsError {
		t.Fatalf("unexpected error: %s", second.Content)
	}
	if !strings.Contains(second.Content, "merged into existing finding") {
		t.Errorf("second report should merge: %s", second.Content)
	}

	list := execTool(t, registry, "list_security_findings", `{}`)
	if list.IsError {
		t.Fatalf("unexpected error: %s", list.Content)
	}
	if !strings.Contains(list.Content, "1 finding(s)") {
		t.Errorf("list should show one finding: %s", list.Content)
	}
	if !strings.Contains(list.Content, "SQL injection in login") {
		t.Errorf("list missing finding title: %s", list.Content)
	}

	fingerprint := ""
	for _, line := range strings.Split(list.Content, "\n") {
		fields := strings.Fields(line)
		if len(fields) > 0 && len(fields[0]) == 16 && fields[0] != "FINGERPRINT" {
			fingerprint = fields[0]
		}
	}
	if fingerprint == "" {
		t.Fatalf("no fingerprint found in list output: %s", list.Content)
	}

	update := execTool(t, registry, "update_security_finding",
		`{"fingerprint":"`+fingerprint+`","status":"false_positive","note":"input is parameterized upstream"}`)
	if update.IsError {
		t.Fatalf("unexpected error: %s", update.Content)
	}

	filtered := execTool(t, registry, "list_security_findings", `{"status":"false_positive"}`)
	if !strings.Contains(filtered.Content, fingerprint) {
		t.Errorf("status filter should match updated finding: %s", filtered.Content)
	}
	open := execTool(t, registry, "list_security_findings", `{"status":"open"}`)
	if !strings.Contains(open.Content, "No findings") {
		t.Errorf("no open findings expected: %s", open.Content)
	}

	missing := execTool(t, registry, "update_security_finding",
		`{"fingerprint":"ffffffffffffffff","status":"fixed","note":"n"}`)
	if !missing.IsError {
		t.Errorf("updating unknown fingerprint must fail: %s", missing.Content)
	}
	badStatus := execTool(t, registry, "update_security_finding",
		`{"fingerprint":"`+fingerprint+`","status":"bogus","note":"n"}`)
	if !badStatus.IsError || !strings.Contains(badStatus.Content, "invalid status") {
		t.Errorf("invalid status must fail: %s", badStatus.Content)
	}
}

func TestSubmitSecurityScanReport(t *testing.T) {
	findingStore := newFakeSecurityFindingStore()
	stateStore := &securityArtifactTestStore{}
	registry := newSecurityTestRegistry(t, findingStore, stateStore)

	for _, finding := range []string{
		`{"title":"SQL injection in login","category":"injection","severity":"critical","description":"query concat","file_path":"a.go","start_line":10}`,
		`{"title":"Weak hash for passwords","category":"crypto","severity":"medium","description":"md5 used","file_path":"b.go","start_line":5}`,
	} {
		result := execTool(t, registry, "report_security_finding", finding)
		if result.IsError {
			t.Fatalf("report failed: %s", result.Content)
		}
	}

	result := execTool(t, registry, "submit_security_scan_report", `{"summary":"Two issues found."}`)
	if result.IsError {
		t.Fatalf("submit failed: %s", result.Content)
	}
	for _, want := range []string{"critical 1", "medium   1", "total    2", "Report artifacts saved"} {
		if !strings.Contains(result.Content, want) {
			t.Errorf("output %q missing %q", result.Content, want)
		}
	}

	report, ok := stateStore.artifacts[SecurityReportArtifactKind]
	if !ok {
		t.Fatalf("markdown report artifact missing; artifacts: %v", stateStore.artifacts)
	}
	if !strings.Contains(report, "SQL injection in login") || !strings.Contains(report, "Two issues found.") {
		t.Errorf("markdown report incomplete:\n%s", report)
	}
	sarif, ok := stateStore.artifacts[SecuritySARIFArtifactKind]
	if !ok {
		t.Fatalf("SARIF artifact missing; artifacts: %v", stateStore.artifacts)
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(sarif), &parsed); err != nil {
		t.Fatalf("SARIF artifact is not valid JSON: %v", err)
	}

	scanCtx := testScanContext()
	scan := findingStore.scans[scanCtx.Namespace+"/"+scanCtx.RunName]
	if scan == nil {
		t.Fatalf("scan record not upserted")
	}
	if scan.Status != "completed" {
		t.Errorf("scan status = %q, want completed", scan.Status)
	}
	if scan.Summary != "Two issues found." {
		t.Errorf("scan summary = %q", scan.Summary)
	}
	if scan.Counts["total"] != 2 || scan.Counts["critical"] != 1 || scan.Counts["medium"] != 1 {
		t.Errorf("scan counts = %v", scan.Counts)
	}
	if scan.CompletedAt == nil {
		t.Errorf("scan completed_at not set")
	}
}

func TestSubmitSecurityScanReportMinSeverityAndInMemory(t *testing.T) {
	stateStore := &securityArtifactTestStore{}
	registry := newSecurityTestRegistry(t, nil, stateStore)

	for _, finding := range []string{
		`{"title":"SQL injection in login","category":"injection","severity":"critical","description":"query concat","file_path":"a.go"}`,
		`{"title":"Verbose header leaks version","category":"info-leak","severity":"info","description":"server header","file_path":"c.go"}`,
	} {
		if result := execTool(t, registry, "report_security_finding", finding); result.IsError {
			t.Fatalf("report failed: %s", result.Content)
		}
	}

	result := execTool(t, registry, "submit_security_scan_report",
		`{"summary":"One real issue.","min_severity":"high"}`)
	if result.IsError {
		t.Fatalf("submit failed: %s", result.Content)
	}
	if !strings.Contains(result.Content, "2 finding(s) recorded") || !strings.Contains(result.Content, "1 in the report") {
		t.Errorf("min_severity should drop the info finding from the report: %s", result.Content)
	}
	report := stateStore.artifacts[SecurityReportArtifactKind]
	if !strings.Contains(report, "SQL injection in login") {
		t.Errorf("report missing critical finding:\n%s", report)
	}
	if strings.Contains(report, "Verbose header leaks version") {
		t.Errorf("report should not include below-min-severity finding:\n%s", report)
	}
}

func TestSubmitSecurityScanReportRequiresSummary(t *testing.T) {
	registry := newSecurityTestRegistry(t, nil, nil)
	result := execTool(t, registry, "submit_security_scan_report", `{"summary":"  "}`)
	if !result.IsError || !strings.Contains(result.Content, "summary is required") {
		t.Errorf("blank summary must fail: %s", result.Content)
	}
}

// Findings carry a non-null scan_id foreign key, so reporting a finding must
// first open (or reuse) the scan record and bind every finding to it.
func TestReportSecurityFindingBindsScanRecord(t *testing.T) {
	findingStore := newFakeSecurityFindingStore()
	registry := newSecurityTestRegistry(t, findingStore, nil)
	scanCtx := testScanContext()

	for _, input := range []string{
		`{"title":"SQL injection in login","category":"injection","severity":"high","description":"d","file_path":"a.go","start_line":1}`,
		`{"title":"XSS in search","category":"xss","severity":"medium","description":"d","file_path":"b.go","start_line":2}`,
	} {
		if result := execTool(t, registry, "report_security_finding", input); result.IsError {
			t.Fatalf("unexpected error: %s", result.Content)
		}
	}

	scan, err := findingStore.GetSecurityScan(context.Background(), scanCtx.Namespace, scanCtx.RunName)
	if err != nil {
		t.Fatalf("GetSecurityScan: %v", err)
	}
	if scan == nil {
		t.Fatal("reporting a finding must open a scan record")
	}
	if scan.Status != "running" || scan.StartedAt == nil {
		t.Errorf("scan record = %+v, want status running with startedAt", scan)
	}
	if scan.Repository != scanCtx.Repository || scan.ScanName != scanCtx.ScanName {
		t.Errorf("scan record scope = %+v, want scan context %+v", scan, scanCtx)
	}
	if len(findingStore.findings) != 2 {
		t.Fatalf("expected 2 findings, got %d", len(findingStore.findings))
	}
	for _, rec := range findingStore.findings {
		if rec.ScanID != scan.ID {
			t.Errorf("finding %q scanID = %s, want %s", rec.Title, rec.ScanID, scan.ID)
		}
	}
	if len(findingStore.scans) != 1 {
		t.Errorf("expected exactly one scan record, got %d", len(findingStore.scans))
	}
}
