package tools

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
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

func (s *fakeSecurityFindingStore) ListSecurityScans(context.Context, string, string, int32, []string) ([]store.SecurityScanRecord, error) {
	return nil, nil
}

func (s *fakeSecurityFindingStore) UpsertSecurityFinding(_ context.Context, rec *store.SecurityFindingRecord) (*store.SecurityFindingRecord, bool, error) {
	for _, existing := range s.findings {
		if existing.Namespace == rec.Namespace && existing.ScanName == rec.ScanName &&
			existing.Repository == rec.Repository && existing.Fingerprint == rec.Fingerprint {
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

func (s *fakeSecurityFindingStore) CorrelateSecurityFindings(_ context.Context, namespace, scanName, repository, fpA, fpB, reason, actor string) (bool, error) {
	if namespace == "" {
		return false, fmt.Errorf("namespace is required")
	}
	find := func(fp string) *store.SecurityFindingRecord {
		for _, rec := range s.findings {
			if rec.Namespace == namespace && rec.ScanName == scanName &&
				rec.Repository == repository && rec.Fingerprint == fp {
				return rec
			}
		}
		return nil
	}
	a, b := find(fpA), find(fpB)
	if a == nil || b == nil {
		return false, store.ErrSecurityFindingNotFound
	}
	changed := false
	link := func(rec *store.SecurityFindingRecord, other string) {
		if slices.Contains(rec.CorrelatedFingerprints, other) {
			return
		}
		rec.CorrelatedFingerprints = append(rec.CorrelatedFingerprints, other)
		s.events = append(s.events, store.SecurityFindingEvent{FindingID: rec.ID, EventType: "correlated", Actor: actor, Note: reason})
		changed = true
	}
	link(a, fpB)
	link(b, fpA)
	return changed, nil
}

func (s *fakeSecurityFindingStore) ListSecurityFindings(_ context.Context, f store.SecurityFindingFilter) ([]store.SecurityFindingRecord, error) {
	out := make([]store.SecurityFindingRecord, 0, len(s.findings))
	for _, rec := range s.findings {
		if f.Namespace != "" && rec.Namespace != f.Namespace {
			continue
		}
		if f.ScanName != "" && rec.ScanName != f.ScanName {
			continue
		}
		if f.RunName != "" && rec.RunName != f.RunName {
			continue
		}
		if f.ExecutionID != "" && rec.ExecutionID != f.ExecutionID {
			continue
		}
		if f.TaskName != "" && rec.TaskName != f.TaskName {
			continue
		}
		if !f.IncludeDuplicates && rec.DuplicateOf != nil {
			continue
		}
		switch f.Suppressed {
		case store.SecuritySuppressedInclude:
		case store.SecuritySuppressedOnly:
			if rec.SuppressedBy == "" {
				continue
			}
		default:
			if rec.SuppressedBy != "" {
				continue
			}
		}
		if f.Repository != "" && rec.Repository != f.Repository {
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
	// Mirror the Postgres store's ordering and its hard row cap so tests
	// exercise the same paging the agent tools must do.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].Fingerprint < out[j].Fingerprint
	})
	limit := f.Limit
	if limit <= 0 {
		limit = 200
	}
	if limit > 1000 {
		limit = 1000
	}
	if int(f.Offset) >= len(out) {
		return nil, nil
	}
	out = out[f.Offset:]
	if int32(len(out)) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *fakeSecurityFindingStore) GetSecurityFinding(_ context.Context, namespace string, id uuid.UUID) (*store.SecurityFindingRecord, error) {
	if namespace == "" {
		return nil, fmt.Errorf("namespace is required")
	}
	for _, rec := range s.findings {
		if rec.Namespace == namespace && rec.ID == id {
			copied := *rec
			return &copied, nil
		}
	}
	return nil, nil
}

func (s *fakeSecurityFindingStore) SetSecurityFindingStatus(_ context.Context, namespace string, id uuid.UUID, status, actor, note string, _ *time.Time) error {
	if namespace == "" {
		return fmt.Errorf("namespace is required")
	}
	if !store.ValidSecurityFindingStatus(status) {
		return fmt.Errorf("invalid security finding status %q", status)
	}
	for _, rec := range s.findings {
		if rec.Namespace == namespace && rec.ID == id {
			rec.Status = status
			s.events = append(s.events, store.SecurityFindingEvent{FindingID: id, EventType: "status_changed", Actor: actor, Note: note})
			return nil
		}
	}
	return store.ErrSecurityFindingNotFound
}

func (s *fakeSecurityFindingStore) RecordSecurityFindingPolicyDisposition(_ context.Context, namespace string, id uuid.UUID, actor, executionID, check, disposition, note string) (*store.SecurityFindingEvent, error) {
	for _, rec := range s.findings {
		if rec.Namespace == namespace && rec.ID == id {
			event := store.SecurityFindingEvent{FindingID: id, EventType: "policy_disposition", Actor: actor, Note: note, Detail: json.RawMessage(`{"execution_id":"` + executionID + `","policy_check":"` + check + `","policy_disposition":"` + disposition + `"}`)}
			s.events = append(s.events, event)
			return &event, nil
		}
	}
	return nil, store.ErrSecurityFindingNotFound
}

func (s *fakeSecurityFindingStore) ListSecurityFindingEvents(_ context.Context, namespace string, id uuid.UUID, _ int32) ([]store.SecurityFindingEvent, error) {
	if namespace == "" {
		return nil, fmt.Errorf("namespace is required")
	}
	var out []store.SecurityFindingEvent
	for _, ev := range s.events {
		if ev.FindingID == id {
			out = append(out, ev)
		}
	}
	return out, nil
}

func (s *fakeSecurityFindingStore) SummarizeSecurityFindings(_ context.Context, namespace, scanName, runName string, _ bool) (map[string]int32, error) {
	out := map[string]int32{
		"total": 0, "open": 0,
		"open_critical": 0, "open_high": 0, "open_medium": 0, "open_low": 0, "open_info": 0,
	}
	for _, rec := range s.findings {
		if rec.Namespace != namespace || rec.DuplicateOf != nil {
			continue
		}
		if scanName != "" && rec.ScanName != scanName {
			continue
		}
		if runName != "" && rec.RunName != runName {
			continue
		}
		out[rec.Severity]++
		out["total"]++
		if rec.Status == store.SecurityFindingStatusOpen {
			out["open"]++
			out["open_"+rec.Severity]++
		}
	}
	return out, nil
}

func (s *fakeSecurityFindingStore) ListSecurityConfigPostures(context.Context, string, int32, []string) ([]store.SecurityConfigPosture, error) {
	return nil, nil
}

func (s *fakeSecurityFindingStore) SummarizeSecurityFindingsScoped(_ context.Context, scope store.SecurityFindingSummaryScope) (map[string]int32, error) {
	out := map[string]int32{
		"total": 0, "open": 0,
		"open_critical": 0, "open_high": 0, "open_medium": 0, "open_low": 0, "open_info": 0,
		"suppressed": 0,
	}
	for _, rec := range s.findings {
		if rec.Namespace != scope.Namespace || rec.DuplicateOf != nil {
			continue
		}
		if scope.ScanName != "" && rec.ScanName != scope.ScanName {
			continue
		}
		if scope.RunName != "" && rec.RunName != scope.RunName {
			continue
		}
		if scope.ExecutionID != "" && rec.ExecutionID != scope.ExecutionID {
			continue
		}
		if scope.TaskName != "" && rec.TaskName != scope.TaskName {
			continue
		}
		if rec.SuppressedBy != "" {
			out["suppressed"]++
			if !scope.IncludeSuppressed {
				continue
			}
		}
		out[rec.Severity]++
		out["total"]++
		if rec.Status == store.SecurityFindingStatusOpen {
			out["open"]++
			out["open_"+rec.Severity]++
		}
	}
	return out, nil
}

func (s *fakeSecurityFindingStore) AddSecurityFindingComment(_ context.Context, namespace string, id uuid.UUID, actor, body string) (*store.SecurityFindingEvent, error) {
	event := store.SecurityFindingEvent{
		ID: int64(len(s.events) + 1), FindingID: id, EventType: "comment",
		Actor: actor, Note: body, CreatedAt: time.Now(),
	}
	for _, rec := range s.findings {
		if rec.Namespace == namespace && rec.ID == id {
			s.events = append(s.events, event)
			return &event, nil
		}
	}
	return nil, store.ErrSecurityFindingNotFound
}

func (s *fakeSecurityFindingStore) DeleteSecurityScanData(context.Context, string, string) error {
	return nil
}

func (s *fakeSecurityFindingStore) PurgeExpiredSecurityData(context.Context, string, store.SecurityRetentionPolicy, int) (store.SecurityRetentionCounts, bool, error) {
	return store.SecurityRetentionCounts{}, false, nil
}

func (s *fakeSecurityFindingStore) ClaimSecurityNotifications(_ context.Context, _, _, _ string, fingerprints []string) ([]string, error) {
	return fingerprints, nil
}

func (s *fakeSecurityFindingStore) ReleaseSecurityNotifications(context.Context, string, string, string, []string) error {
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
		ScanName:       "nightly-scan",
		Namespace:      "default",
		RunName:        "nightly-scan-run-1",
		Repository:     "github.com/acme/widget",
		Revision:       "abc1234",
		DedupePermille: -1,
		SessionID:      uuid.New(),
	}
}

func newSecurityTestRegistryWithCtx(t *testing.T, findingStore store.SecurityFindingStore, stateStore store.StateStore, scanCtx SecurityScanContext) *Registry {
	t.Helper()
	registry := &Registry{tools: map[string]Tool{}}
	RegisterSecurityScanTools(registry, findingStore, stateStore, scanCtx)
	for _, name := range []string{"report_security_finding", "list_security_findings", "update_security_finding", "ingest_scanner_results", "submit_security_scan_report"} {
		if registry.Get(name) == nil {
			t.Fatalf("tool %s not registered", name)
		}
		if !registry.Get(name).IsReadOnly() {
			t.Fatalf("tool %s must be read-only", name)
		}
	}
	return registry
}

func newSecurityTestRegistry(t *testing.T, findingStore store.SecurityFindingStore, stateStore store.StateStore) *Registry {
	t.Helper()
	return newSecurityTestRegistryWithCtx(t, findingStore, stateStore, testScanContext())
}

func securityTestState(t *testing.T, registry *Registry) *securityScanState {
	t.Helper()
	tool, ok := registry.Get("report_security_finding").(*reportSecurityFindingTool)
	if !ok {
		t.Fatalf("report_security_finding has unexpected type")
	}
	return tool.state
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
			scanCtx, ok := SecurityScanContextFromRun(tt.run, "ns1", "run1", ".", sessionID)
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

func TestSecurityScanContextFromRunResolvesUnpinnedCheckout(t *testing.T) {
	checkoutDir := t.TempDir()
	for _, args := range [][]string{
		{"init", checkoutDir},
		{"-C", checkoutDir, "config", "user.email", "security-test@example.invalid"},
		{"-C", checkoutDir, "config", "user.name", "Security Test"},
	} {
		if output, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	if err := os.WriteFile(filepath.Join(checkoutDir, "target.txt"), []byte("target checkout\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"-C", checkoutDir, "add", "target.txt"}, {"-C", checkoutDir, "commit", "-m", "target"}} {
		if output, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	expectedOutput, err := exec.Command("git", "-C", checkoutDir, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	originalDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(originalDir) })

	run := &platformv1alpha1.AgentRun{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
		SecurityScanNameAnnotation:       "scan-a",
		SecurityScanRepositoryAnnotation: "github.com/acme/widget",
	}}}
	scanCtx, ok := SecurityScanContextFromRun(run, "ns1", "run1", checkoutDir, uuid.New())
	if !ok {
		t.Fatal("SecurityScanContextFromRun returned ok=false")
	}
	if scanCtx.Revision != strings.TrimSpace(string(expectedOutput)) {
		t.Fatalf("resolved revision = %q, want checkout HEAD %q", scanCtx.Revision, strings.TrimSpace(string(expectedOutput)))
	}
	decoded, err := hex.DecodeString(scanCtx.Revision)
	if err != nil || len(decoded) < 20 {
		t.Fatalf("resolved revision %q is not an immutable commit hash", scanCtx.Revision)
	}
}

func TestSecurityScanContextPolicyAnnotations(t *testing.T) {
	sessionID := uuid.New()
	tests := []struct {
		name         string
		annotations  map[string]string
		wantMinSev   string
		wantPermille int32
	}{
		{
			name:         "absent means unset",
			annotations:  map[string]string{SecurityScanNameAnnotation: "scan-a"},
			wantMinSev:   "",
			wantPermille: -1,
		},
		{
			name: "policy annotations parsed",
			annotations: map[string]string{
				SecurityScanNameAnnotation:           "scan-a",
				SecurityScanMinSeverityAnnotation:    "High",
				SecurityScanDedupePermilleAnnotation: "820",
			},
			wantMinSev:   "high",
			wantPermille: 820,
		},
		{
			name: "zero permille means dedupe disabled",
			annotations: map[string]string{
				SecurityScanNameAnnotation:           "scan-a",
				SecurityScanDedupePermilleAnnotation: "0",
			},
			wantMinSev:   "",
			wantPermille: 0,
		},
		{
			name: "invalid values treated as unset",
			annotations: map[string]string{
				SecurityScanNameAnnotation:           "scan-a",
				SecurityScanMinSeverityAnnotation:    "apocalyptic",
				SecurityScanDedupePermilleAnnotation: "not-a-number",
			},
			wantMinSev:   "",
			wantPermille: -1,
		},
		{
			name: "negative permille treated as unset",
			annotations: map[string]string{
				SecurityScanNameAnnotation:           "scan-a",
				SecurityScanDedupePermilleAnnotation: "-5",
			},
			wantMinSev:   "",
			wantPermille: -1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			run := &platformv1alpha1.AgentRun{ObjectMeta: metav1.ObjectMeta{Annotations: tt.annotations}}
			scanCtx, ok := SecurityScanContextFromRun(run, "ns1", "run1", ".", sessionID)
			if !ok {
				t.Fatalf("expected scan context")
			}
			if scanCtx.MinSeverity != tt.wantMinSev {
				t.Errorf("MinSeverity = %q, want %q", scanCtx.MinSeverity, tt.wantMinSev)
			}
			if scanCtx.DedupePermille != tt.wantPermille {
				t.Errorf("DedupePermille = %d, want %d", scanCtx.DedupePermille, tt.wantPermille)
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
}

// A model-supplied repository/revision is part of the dedupe key
// (namespace, scan_name, repository, fingerprint), so forging it could
// overwrite another scan's findings. It must always be overwritten with the
// scan context; source_agent keeps the model label but anchored to the run.
func TestReportSecurityFindingOverwritesForgedIdentity(t *testing.T) {
	findingStore := newFakeSecurityFindingStore()
	registry := newSecurityTestRegistry(t, findingStore, nil)
	scanCtx := testScanContext()

	result := execTool(t, registry, "report_security_finding",
		`{"title":"XSS in search","category":"xss","severity":"medium","description":"d","repository":"github.com/other/repo","revision":"fff","source_agent":"custom-agent"}`)
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Content)
	}
	if len(findingStore.findings) != 1 {
		t.Fatalf("expected 1 stored finding, got %d", len(findingStore.findings))
	}
	rec := findingStore.findings[0]
	if rec.Repository != scanCtx.Repository {
		t.Errorf("forged repository must be overwritten: got %q, want %q", rec.Repository, scanCtx.Repository)
	}
	if rec.Revision != scanCtx.Revision {
		t.Errorf("forged revision must be overwritten: got %q, want %q", rec.Revision, scanCtx.Revision)
	}
	if want := scanCtx.RunName + "/custom-agent"; rec.SourceAgent != want {
		t.Errorf("source agent = %q, want run-anchored %q", rec.SourceAgent, want)
	}
}

func TestReportSecurityFindingSchemaOmitsScanIdentity(t *testing.T) {
	registry := newSecurityTestRegistry(t, nil, nil)
	schema := string(registry.Get("report_security_finding").InputSchema())
	var parsed map[string]any
	if err := json.Unmarshal([]byte(schema), &parsed); err != nil {
		t.Fatalf("schema is not valid JSON: %v", err)
	}
	props, ok := parsed["properties"].(map[string]any)
	if !ok {
		t.Fatalf("schema has no properties object")
	}
	for _, forbidden := range []string{"repository", "revision"} {
		if _, present := props[forbidden]; present {
			t.Errorf("schema must not advertise %q; it is stamped from the scan context", forbidden)
		}
	}
	for _, required := range []string{"title", "category", "severity", "description"} {
		if _, present := props[required]; !present {
			t.Errorf("schema lost property %q", required)
		}
	}

	updateSchema := string(registry.Get("update_security_finding").InputSchema())
	if strings.Contains(updateSchema, `"actor"`) {
		t.Errorf("update_security_finding must not accept a model-supplied actor: %s", updateSchema)
	}
	if !strings.Contains(updateSchema, `"accepted_risk"`) {
		t.Errorf("update_security_finding must allow agents to record accepted risk: %s", updateSchema)
	}
}

func TestUpdateSecurityFindingRejectsForeignFindings(t *testing.T) {
	findingStore := newFakeSecurityFindingStore()
	registry := newSecurityTestRegistry(t, findingStore, nil)
	scanCtx := testScanContext()

	otherNamespace := &store.SecurityFindingRecord{
		ID: uuid.New(), Namespace: "other-ns", ScanName: scanCtx.ScanName, RunName: "other-run",
		Fingerprint: "aaaaaaaaaaaaaaaa", Severity: "high", Status: store.SecurityFindingStatusOpen,
	}
	otherScan := &store.SecurityFindingRecord{
		ID: uuid.New(), Namespace: scanCtx.Namespace, ScanName: "other-scan", RunName: "other-scan-run-1",
		Fingerprint: "bbbbbbbbbbbbbbbb", Severity: "high", Status: store.SecurityFindingStatusOpen,
	}
	findingStore.findings = append(findingStore.findings, otherNamespace, otherScan)

	result := execTool(t, registry, "update_security_finding",
		`{"id":"`+otherNamespace.ID.String()+`","status":"false_positive","note":"prompt-injected re-triage"}`)
	if !result.IsError || !strings.Contains(result.Content, "in this scan") {
		t.Errorf("cross-namespace id must be rejected: %s", result.Content)
	}
	if otherNamespace.Status != store.SecurityFindingStatusOpen {
		t.Errorf("cross-namespace finding was mutated: %+v", otherNamespace)
	}

	result = execTool(t, registry, "update_security_finding",
		`{"id":"`+otherScan.ID.String()+`","status":"false_positive","note":"prompt-injected re-triage"}`)
	if !result.IsError || !strings.Contains(result.Content, "does not belong to security scan") {
		t.Errorf("cross-scan id must be rejected: %s", result.Content)
	}
	if otherScan.Status != store.SecurityFindingStatusOpen {
		t.Errorf("cross-scan finding was mutated: %+v", otherScan)
	}

	result = execTool(t, registry, "update_security_finding",
		`{"fingerprint":"bbbbbbbbbbbbbbbb","status":"false_positive","note":"prompt-injected re-triage"}`)
	if !result.IsError || !strings.Contains(result.Content, "no finding with fingerprint") {
		t.Errorf("fingerprint lookup must stay scan-scoped: %s", result.Content)
	}

	result = execTool(t, registry, "update_security_finding",
		`{"id":"`+uuid.New().String()+`","status":"fixed","note":"n"}`)
	if !result.IsError || !strings.Contains(result.Content, "no finding with id") {
		t.Errorf("unknown id must map to a clear not-found error: %s", result.Content)
	}

	if report := execTool(t, registry, "report_security_finding",
		`{"title":"SQL injection in login","category":"injection","severity":"high","description":"d"}`); report.IsError {
		t.Fatalf("report failed: %s", report.Content)
	}
	var own *store.SecurityFindingRecord
	for _, rec := range findingStore.findings {
		if rec.ScanName == scanCtx.ScanName && rec.Namespace == scanCtx.Namespace {
			own = rec
		}
	}
	if own == nil {
		t.Fatalf("own finding not stored")
	}
	result = execTool(t, registry, "update_security_finding",
		`{"id":"`+own.ID.String()+`","status":"confirmed","note":"PoC built"}`)
	if result.IsError {
		t.Fatalf("own-scan update must succeed: %s", result.Content)
	}
	if own.Status != store.SecurityFindingStatusConfirmed {
		t.Errorf("own finding status = %q, want confirmed", own.Status)
	}
	result = execTool(t, registry, "update_security_finding",
		`{"id":"`+own.ID.String()+`","status":"accepted_risk","note":"agent chose to accept it"}`)
	if result.IsError {
		t.Errorf("scan agent risk acceptance must be allowed: %s", result.Content)
	}
	if own.Status != store.SecurityFindingStatusAcceptedRisk {
		t.Errorf("risk acceptance left status at %q", own.Status)
	}
	result = execTool(t, registry, "update_security_finding",
		`{"id":"`+own.ID.String()+`","status":"confirmed","policy_disposition":"known_issue","note":"missing provenance"}`)
	if !result.IsError || !strings.Contains(result.Content, "policy_check") {
		t.Fatalf("policy disposition without its check must fail: %s", result.Content)
	}
	result = execTool(t, registry, "update_security_finding",
		`{"id":"`+own.ID.String()+`","status":"confirmed","policy_check":"bounty","policy_disposition":"novel","note":"invalid pair"}`)
	if !result.IsError || !strings.Contains(result.Content, "not valid") {
		t.Fatalf("mismatched policy check and disposition must fail: %s", result.Content)
	}
	result = execTool(t, registry, "update_security_finding",
		`{"id":"`+own.ID.String()+`","status":"confirmed","policy_check":"prior_art","policy_disposition":"known_issue","note":"matches published audit"}`)
	if result.IsError {
		t.Fatalf("policy disposition update must succeed: %s", result.Content)
	}
	if got := store.SecurityFindingBlockingPolicyDisposition(findingStore.events, scanCtx.ExecutionID); got != "known_issue" {
		t.Fatalf("stored blocking policy disposition = %q, want known_issue", got)
	}
}

func TestUpdateSecurityFindingActorIsAlwaysRunName(t *testing.T) {
	findingStore := newFakeSecurityFindingStore()
	registry := newSecurityTestRegistry(t, findingStore, nil)
	scanCtx := testScanContext()

	if report := execTool(t, registry, "report_security_finding",
		`{"title":"SQL injection in login","category":"injection","severity":"high","description":"d"}`); report.IsError {
		t.Fatalf("report failed: %s", report.Content)
	}
	fingerprint := findingStore.findings[0].Fingerprint

	// The "actor" field was removed from the input; a model still sending it
	// must not be able to forge the audit trail.
	result := execTool(t, registry, "update_security_finding",
		`{"fingerprint":"`+fingerprint+`","status":"confirmed","note":"PoC built","actor":"cluster-admin"}`)
	if result.IsError {
		t.Fatalf("update failed: %s", result.Content)
	}
	if len(findingStore.events) != 1 {
		t.Fatalf("expected 1 audit event, got %d", len(findingStore.events))
	}
	ev := findingStore.events[0]
	if ev.Actor != scanCtx.RunName {
		t.Errorf("audit actor = %q, want run name %q", ev.Actor, scanCtx.RunName)
	}
	if ev.Note != "PoC built" {
		t.Errorf("audit note = %q, want the model's explanation", ev.Note)
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
	for line := range strings.SplitSeq(list.Content, "\n") {
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

// The in-memory fallback must apply the same filter and audit semantics as
// the Postgres path: namespace/scan/run/repository scoping, offset paging,
// description search, status events with the run as actor, and counts over
// stored non-duplicate rows.
func TestSecurityFindingInMemoryMatchesPostgresSemantics(t *testing.T) {
	registry := newSecurityTestRegistry(t, nil, nil)
	state := securityTestState(t, registry)
	scanCtx := testScanContext()
	ctx := context.Background()

	for _, finding := range []string{
		`{"title":"SQL injection in login","category":"injection","severity":"critical","description":"query concatenation in handler","file_path":"a.go"}`,
		`{"title":"XSS in search","category":"xss","severity":"medium","description":"unescaped output","file_path":"b.go"}`,
		`{"title":"Verbose header","category":"info-leak","severity":"info","description":"server header leaks version","file_path":"c.go"}`,
	} {
		if result := execTool(t, registry, "report_security_finding", finding); result.IsError {
			t.Fatalf("report failed: %s", result.Content)
		}
	}

	for name, filter := range map[string]store.SecurityFindingFilter{
		"namespace":  {Namespace: "other-ns"},
		"scan name":  {ScanName: "other-scan"},
		"run name":   {RunName: "other-run"},
		"repository": {Repository: "github.com/other/repo"},
		"min score":  {MinScore: 0.5},
	} {
		records, err := state.listFindings(ctx, filter)
		if err != nil {
			t.Fatalf("listFindings(%s): %v", name, err)
		}
		if len(records) != 0 {
			t.Errorf("%s filter must exclude all findings, got %d", name, len(records))
		}
	}

	all, err := state.listFindings(ctx, store.SecurityFindingFilter{
		Namespace: scanCtx.Namespace, ScanName: scanCtx.ScanName,
		RunName: scanCtx.RunName, Repository: scanCtx.Repository,
	})
	if err != nil {
		t.Fatalf("listFindings: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("full-scope filter must match all findings, got %d", len(all))
	}

	paged, err := state.listFindings(ctx, store.SecurityFindingFilter{Offset: 1, Limit: 1})
	if err != nil {
		t.Fatalf("listFindings offset: %v", err)
	}
	if len(paged) != 1 || paged[0].Fingerprint != all[1].Fingerprint {
		t.Errorf("offset/limit paging mismatch: got %d records", len(paged))
	}

	// Postgres search covers title, description, and file path.
	byDescription, err := state.listFindings(ctx, store.SecurityFindingFilter{Search: "unescaped output"})
	if err != nil {
		t.Fatalf("listFindings search: %v", err)
	}
	if len(byDescription) != 1 || byDescription[0].Title != "XSS in search" {
		t.Errorf("description search must match like Postgres, got %d records", len(byDescription))
	}

	update := execTool(t, registry, "update_security_finding",
		`{"fingerprint":"`+all[2].Fingerprint+`","status":"false_positive","note":"header is stripped by the proxy"}`)
	if update.IsError {
		t.Fatalf("update failed: %s", update.Content)
	}
	review := execTool(t, registry, "update_security_finding",
		`{"fingerprint":"`+all[2].Fingerprint+`","status":"false_positive","note":"rechecked against the proxy"}`)
	if review.IsError {
		t.Fatalf("same-status review failed: %s", review.Content)
	}
	state.mu.Lock()
	events := append([]store.SecurityFindingEvent(nil), state.memEvents...)
	counts := state.summarizeMemLocked()
	state.mu.Unlock()
	if len(events) != 2 || events[0].EventType != "status_changed" || events[1].EventType != "status_reviewed" || events[1].Actor != scanCtx.RunName {
		t.Errorf("in-memory fallback must record status events with the run as actor: %+v", events)
	}
	want := map[string]int32{"critical": 1, "medium": 1, "info": 1, "total": 3, "open": 2, "open_critical": 1, "open_medium": 1}
	for k, v := range want {
		if counts[k] != v {
			t.Errorf("counts[%q] = %d, want %d (all: %v)", k, counts[k], v, counts)
		}
	}
	if counts["open_info"] != 0 {
		t.Errorf("false_positive finding must not count as open: %v", counts)
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
	for _, want := range []string{"critical 1", "medium   1", "total    2", "Report artifacts saved", "Policy applied", "default dedupe threshold"} {
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
	if scan.Counts["open"] != 2 || scan.Counts["open_critical"] != 1 {
		t.Errorf("scan counts missing open keys: %v", scan.Counts)
	}
	if scan.CompletedAt == nil {
		t.Errorf("scan completed_at not set")
	}
}

// A finding a post-script disproved is kept in the store but must not reach
// the rendered report or the SARIF artifact.
func TestSubmitSecurityScanReportExcludesFalsePositives(t *testing.T) {
	findingStore := newFakeSecurityFindingStore()
	stateStore := &securityArtifactTestStore{}
	registry := newSecurityTestRegistry(t, findingStore, stateStore)

	for _, finding := range []string{
		`{"title":"SQL injection in login","category":"injection","severity":"critical","description":"query concat","file_path":"a.go","start_line":10}`,
		`{"title":"Weak hash for passwords","category":"crypto","severity":"medium","description":"md5 used","file_path":"b.go","start_line":5}`,
	} {
		if result := execTool(t, registry, "report_security_finding", finding); result.IsError {
			t.Fatalf("report failed: %s", result.Content)
		}
	}

	var fingerprint string
	for _, rec := range findingStore.findings {
		if rec.Title == "Weak hash for passwords" {
			fingerprint = rec.Fingerprint
		}
	}
	if fingerprint == "" {
		t.Fatalf("finding to disprove not stored: %v", findingStore.findings)
	}
	update := execTool(t, registry, "update_security_finding",
		`{"fingerprint":"`+fingerprint+`","status":"false_positive","note":"md5 only guards cache keys"}`)
	if update.IsError {
		t.Fatalf("update failed: %s", update.Content)
	}

	result := execTool(t, registry, "submit_security_scan_report", `{"summary":"One real issue."}`)
	if result.IsError {
		t.Fatalf("submit failed: %s", result.Content)
	}
	if !strings.Contains(result.Content, "2 finding(s) recorded") || !strings.Contains(result.Content, "1 in the report") {
		t.Errorf("false positive must be excluded from the report counts: %s", result.Content)
	}
	if !strings.Contains(result.Content, "1 finding(s) triaged as false positives were excluded") {
		t.Errorf("policy note must report the excluded count: %s", result.Content)
	}

	report := stateStore.artifacts[SecurityReportArtifactKind]
	if !strings.Contains(report, "SQL injection in login") {
		t.Errorf("report missing the open finding:\n%s", report)
	}
	if strings.Contains(report, "Weak hash for passwords") {
		t.Errorf("report must not include the disproved finding:\n%s", report)
	}
	sarif := stateStore.artifacts[SecuritySARIFArtifactKind]
	if !strings.Contains(sarif, "SQL injection in login") {
		t.Errorf("SARIF missing the open finding:\n%s", sarif)
	}
	if strings.Contains(sarif, "Weak hash for passwords") {
		t.Errorf("SARIF must not include the disproved finding:\n%s", sarif)
	}

	// The disproved row is marked, never erased, and the persisted counts
	// keep covering every stored row (open keys carry the triage outcome).
	if len(findingStore.findings) != 2 {
		t.Fatalf("stored findings = %d, want 2 (findings are marked, not deleted)", len(findingStore.findings))
	}
	for _, rec := range findingStore.findings {
		if rec.Title == "Weak hash for passwords" && rec.Status != store.SecurityFindingStatusFalsePositive {
			t.Errorf("disproved finding status = %q, want false_positive", rec.Status)
		}
	}
	scanCtx := testScanContext()
	scan := findingStore.scans[scanCtx.Namespace+"/"+scanCtx.RunName]
	if scan == nil {
		t.Fatalf("scan record not upserted")
	}
	if scan.Counts["total"] != 2 || scan.Counts["open"] != 1 || scan.Counts["open_critical"] != 1 {
		t.Errorf("scan counts = %v, want total 2 with 1 open critical", scan.Counts)
	}
}

// Every finding disproved: the report still submits cleanly, with a
// zero-finding summary.
func TestSubmitSecurityScanReportAllFindingsFalsePositive(t *testing.T) {
	findingStore := newFakeSecurityFindingStore()
	stateStore := &securityArtifactTestStore{}
	registry := newSecurityTestRegistry(t, findingStore, stateStore)

	if result := execTool(t, registry, "report_security_finding",
		`{"title":"SQL injection in login","category":"injection","severity":"critical","description":"query concat","file_path":"a.go"}`); result.IsError {
		t.Fatalf("report failed: %s", result.Content)
	}
	update := execTool(t, registry, "update_security_finding",
		`{"fingerprint":"`+findingStore.findings[0].Fingerprint+`","status":"false_positive","note":"the query is parameterized upstream"}`)
	if update.IsError {
		t.Fatalf("update failed: %s", update.Content)
	}

	result := execTool(t, registry, "submit_security_scan_report", `{"summary":"Nothing held up under triage."}`)
	if result.IsError {
		t.Fatalf("submit failed: %s", result.Content)
	}
	if !strings.Contains(result.Content, "1 finding(s) recorded, 0 after dedupe, 0 in the report") {
		t.Errorf("all-excluded scan must report zero findings: %s", result.Content)
	}
	if !strings.Contains(result.Content, "1 finding(s) triaged as false positives were excluded") {
		t.Errorf("policy note must report the excluded count: %s", result.Content)
	}
	report := stateStore.artifacts[SecurityReportArtifactKind]
	if !strings.Contains(report, "No findings.") || strings.Contains(report, "SQL injection in login") {
		t.Errorf("all-excluded report must render as empty:\n%s", report)
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(stateStore.artifacts[SecuritySARIFArtifactKind]), &parsed); err != nil {
		t.Fatalf("SARIF artifact is not valid JSON: %v", err)
	}
	scanCtx := testScanContext()
	if scan := findingStore.scans[scanCtx.Namespace+"/"+scanCtx.RunName]; scan == nil || scan.Status != "completed" {
		t.Errorf("scan record = %+v, want completed", scan)
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
	if !strings.Contains(result.Content, "minimum severity high from tool input") {
		t.Errorf("result must say which policy was applied: %s", result.Content)
	}
	if !strings.Contains(result.Content, "In-memory mode") {
		t.Errorf("in-memory divergence must be noted: %s", result.Content)
	}
	report := stateStore.artifacts[SecurityReportArtifactKind]
	if !strings.Contains(report, "SQL injection in login") {
		t.Errorf("report missing critical finding:\n%s", report)
	}
	if strings.Contains(report, "Verbose header leaks version") {
		t.Errorf("report should not include below-min-severity finding:\n%s", report)
	}
}

// The SecurityScan CRD's min-severity policy (via run annotations) must win
// whenever it is stricter than what the model asks for.
func TestSubmitSecurityScanReportEnforcesScanMinSeverity(t *testing.T) {
	stateStore := &securityArtifactTestStore{}
	scanCtx := testScanContext()
	scanCtx.MinSeverity = "high"
	registry := newSecurityTestRegistryWithCtx(t, nil, stateStore, scanCtx)

	for _, finding := range []string{
		`{"title":"SQL injection in login","category":"injection","severity":"critical","description":"query concat","file_path":"a.go"}`,
		`{"title":"Verbose header leaks version","category":"info-leak","severity":"info","description":"server header","file_path":"c.go"}`,
	} {
		if result := execTool(t, registry, "report_security_finding", finding); result.IsError {
			t.Fatalf("report failed: %s", result.Content)
		}
	}

	result := execTool(t, registry, "submit_security_scan_report",
		`{"summary":"Everything is fine.","min_severity":"info"}`)
	if result.IsError {
		t.Fatalf("submit failed: %s", result.Content)
	}
	if !strings.Contains(result.Content, "1 in the report") {
		t.Errorf("scan policy min severity must drop the info finding despite the looser tool input: %s", result.Content)
	}
	if !strings.Contains(result.Content, "minimum severity high from scan policy") {
		t.Errorf("result must attribute the enforced policy to the scan: %s", result.Content)
	}
	if strings.Contains(stateStore.artifacts[SecurityReportArtifactKind], "Verbose header leaks version") {
		t.Errorf("report must not include findings below the scan's min severity")
	}
}

func TestSubmitSecurityScanReportDedupePolicy(t *testing.T) {
	similar := []string{
		`{"title":"SQL injection in login handler","category":"injection","severity":"high","description":"user input concatenated into sql query string","file_path":"a.go"}`,
		`{"title":"SQL injection in login flow","category":"injection","severity":"high","description":"user input concatenated into sql query text","file_path":"b.go"}`,
	}

	t.Run("annotation threshold merges similar findings", func(t *testing.T) {
		scanCtx := testScanContext()
		scanCtx.DedupePermille = 1 // 0.001: merges anything with token overlap
		registry := newSecurityTestRegistryWithCtx(t, nil, &securityArtifactTestStore{}, scanCtx)
		for _, finding := range similar {
			if result := execTool(t, registry, "report_security_finding", finding); result.IsError {
				t.Fatalf("report failed: %s", result.Content)
			}
		}
		result := execTool(t, registry, "submit_security_scan_report", `{"summary":"s"}`)
		if result.IsError {
			t.Fatalf("submit failed: %s", result.Content)
		}
		if !strings.Contains(result.Content, "2 finding(s) recorded, 1 after dedupe") {
			t.Errorf("annotation dedupe threshold not applied: %s", result.Content)
		}
		if !strings.Contains(result.Content, "dedupe threshold 0.001 from scan policy") {
			t.Errorf("result must say the dedupe policy came from the scan: %s", result.Content)
		}
	})

	t.Run("zero permille disables dedupe", func(t *testing.T) {
		scanCtx := testScanContext()
		scanCtx.DedupePermille = 0
		registry := newSecurityTestRegistryWithCtx(t, nil, &securityArtifactTestStore{}, scanCtx)
		for _, finding := range similar {
			if result := execTool(t, registry, "report_security_finding", finding); result.IsError {
				t.Fatalf("report failed: %s", result.Content)
			}
		}
		result := execTool(t, registry, "submit_security_scan_report", `{"summary":"s"}`)
		if result.IsError {
			t.Fatalf("submit failed: %s", result.Content)
		}
		if !strings.Contains(result.Content, "2 finding(s) recorded, 2 after dedupe") {
			t.Errorf("dedupe must be skipped when the scan disables it: %s", result.Content)
		}
		if !strings.Contains(result.Content, "dedupe disabled by scan policy") {
			t.Errorf("result must say dedupe was disabled: %s", result.Content)
		}
	})
}

func TestSubmitSecurityScanReportDoubleSubmit(t *testing.T) {
	findingStore := newFakeSecurityFindingStore()
	stateStore := &securityArtifactTestStore{}
	registry := newSecurityTestRegistry(t, findingStore, stateStore)
	scanCtx := testScanContext()
	scanKey := scanCtx.Namespace + "/" + scanCtx.RunName

	if result := execTool(t, registry, "report_security_finding",
		`{"title":"SQL injection in login","category":"injection","severity":"critical","description":"query concat","file_path":"a.go"}`); result.IsError {
		t.Fatalf("report failed: %s", result.Content)
	}
	first := execTool(t, registry, "submit_security_scan_report", `{"summary":"One issue."}`)
	if first.IsError {
		t.Fatalf("first submit failed: %s", first.Content)
	}
	scan := findingStore.scans[scanKey]
	if scan == nil || scan.CompletedAt == nil {
		t.Fatalf("first submit must complete the scan record")
	}
	firstCompletedAt := *scan.CompletedAt

	second := execTool(t, registry, "submit_security_scan_report", `{"summary":"Different summary."}`)
	if second.IsError {
		t.Fatalf("second submit must be a safe no-op: %s", second.Content)
	}
	if !strings.Contains(second.Content, "already finalized") || !strings.Contains(second.Content, "One issue.") {
		t.Errorf("second submit must return the existing result: %s", second.Content)
	}
	scan = findingStore.scans[scanKey]
	if scan.Summary != "One issue." {
		t.Errorf("second submit must not rewrite the summary: %q", scan.Summary)
	}
	if !scan.CompletedAt.Equal(firstCompletedAt) {
		t.Errorf("second submit must not rewrite completed_at: %v vs %v", scan.CompletedAt, firstCompletedAt)
	}

	if result := execTool(t, registry, "report_security_finding",
		`{"title":"Weak hash for passwords","category":"crypto","severity":"medium","description":"md5 used","file_path":"b.go"}`); result.IsError {
		t.Fatalf("report failed: %s", result.Content)
	}
	third := execTool(t, registry, "submit_security_scan_report", `{"summary":"Two issues."}`)
	if third.IsError {
		t.Fatalf("third submit failed: %s", third.Content)
	}
	if !strings.Contains(third.Content, "finalized: 2 finding(s) recorded") {
		t.Errorf("changed findings must re-finalize the scan: %s", third.Content)
	}
	scan = findingStore.scans[scanKey]
	if scan.Summary != "Two issues." || scan.Counts["total"] != 2 {
		t.Errorf("re-finalize must update summary and counts: %+v", scan)
	}
}

func TestSubmitSecurityScanReportDoubleSubmitInMemory(t *testing.T) {
	registry := newSecurityTestRegistry(t, nil, &securityArtifactTestStore{})

	if result := execTool(t, registry, "report_security_finding",
		`{"title":"SQL injection in login","category":"injection","severity":"critical","description":"query concat","file_path":"a.go"}`); result.IsError {
		t.Fatalf("report failed: %s", result.Content)
	}
	if first := execTool(t, registry, "submit_security_scan_report", `{"summary":"One issue."}`); first.IsError {
		t.Fatalf("first submit failed: %s", first.Content)
	}
	second := execTool(t, registry, "submit_security_scan_report", `{"summary":"Other summary."}`)
	if second.IsError || !strings.Contains(second.Content, "already finalized") || !strings.Contains(second.Content, "One issue.") {
		t.Errorf("in-memory double submit must return the existing result: %s", second.Content)
	}

	if result := execTool(t, registry, "report_security_finding",
		`{"title":"Weak hash for passwords","category":"crypto","severity":"medium","description":"md5 used","file_path":"b.go"}`); result.IsError {
		t.Fatalf("report failed: %s", result.Content)
	}
	third := execTool(t, registry, "submit_security_scan_report", `{"summary":"Two issues."}`)
	if third.IsError || !strings.Contains(third.Content, "finalized: 2 finding(s) recorded") {
		t.Errorf("changed findings must re-finalize in memory: %s", third.Content)
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

// Collaboration methods are not exercised by the agent tools; stubs satisfy
// the store.SecurityFindingStore interface.
func (s *fakeSecurityFindingStore) SetSecurityFindingAssignee(context.Context, string, uuid.UUID, string, string) error {
	return nil
}

func (s *fakeSecurityFindingStore) SetSecurityFindingTicket(context.Context, string, uuid.UUID, string, string, string) error {
	return nil
}

func (s *fakeSecurityFindingStore) ExpireAcceptedRisks(context.Context, string) (int32, error) {
	return 0, nil
}

func (s *fakeSecurityFindingStore) ApplySecuritySuppressions(context.Context, string, string, []store.SecuritySuppressionRule) (int32, error) {
	return 0, nil
}

func (s *fakeSecurityFindingStore) ExpireSecuritySuppressions(context.Context, string) (int32, error) {
	return 0, nil
}

func (s *fakeSecurityFindingStore) RevokeSecuritySuppressions(context.Context, string, string, []store.SecuritySuppressionRule) (int32, error) {
	return 0, nil
}

func (s *fakeSecurityFindingStore) BulkUpdateSecurityFindings(context.Context, string, string, []uuid.UUID, store.SecurityFindingBulkUpdate) error {
	return nil
}

func (s *fakeSecurityFindingStore) FinalizeSecurityScanBaseline(context.Context, string, string) (int32, error) {
	return 0, nil
}

func (s *fakeSecurityFindingStore) ListSecuritySavedFilters(context.Context, string, string) ([]store.SecuritySavedFilter, error) {
	return nil, nil
}

func (s *fakeSecurityFindingStore) SaveSecuritySavedFilter(_ context.Context, rec *store.SecuritySavedFilter) (*store.SecuritySavedFilter, error) {
	return rec, nil
}

func (s *fakeSecurityFindingStore) DeleteSecuritySavedFilter(context.Context, string, string, string) error {
	return nil
}

func (s *fakeSecurityFindingStore) GetSecurityFindingTrends(context.Context, string, string, []string) (*store.SecurityFindingTrends, error) {
	return &store.SecurityFindingTrends{}, nil
}

func (s *fakeSecurityFindingStore) ExportSecurityFindingEvents(context.Context, string, string, int32) ([]store.SecurityFindingAuditRecord, error) {
	return nil, nil
}

const testAgentCryptoFinding = `{"title":"Weak password hashing with MD5","category":"crypto","severity":"high","description":"Passwords are hashed with MD5 which is broken.","file_path":"internal/crypto/hash.go","start_line":40,"end_line":46,"cwe":["CWE-327"]}`

const testScannerCryptoBatch = `{"records":[{"tool":"gosec","tool_version":"2.18.2","rule_id":"G401","rule_name":"Use of weak cryptographic primitive","message":"Use of weak cryptographic primitive md5","severity":"HIGH","file_path":"internal/crypto/hash.go","start_line":42,"end_line":44,"symbol":"hashPassword","cwe":"CWE-327","raw_evidence":"sum := md5.Sum(password)"}]}`

func TestIngestScannerResultsRejectsBatchWithPerRecordErrors(t *testing.T) {
	findingStore := newFakeSecurityFindingStore()
	registry := newSecurityTestRegistry(t, findingStore, nil)

	result := execTool(t, registry, "ingest_scanner_results", `{"records":[
		{"tool":"gosec","rule_id":"G401","message":"ok","severity":"HIGH","file_path":"a.go"},
		{"tool":"","rule_id":"","message":"m","severity":"HIGH","file_path":"a.go"},
		{"tool":"gosec","rule_id":"G1","message":"m","severity":"apocalyptic","file_path":""}
	]}`)
	if !result.IsError {
		t.Fatalf("expected batch rejection, got: %s", result.Content)
	}
	for _, want := range []string{"records[1]: ", "tool is required", "rule_id is required", "records[2]: ", `severity "apocalyptic" does not map`, "file_path is required"} {
		if !strings.Contains(result.Content, want) {
			t.Errorf("error %q missing %q", result.Content, want)
		}
	}
	if strings.Contains(result.Content, "records[0]") {
		t.Errorf("valid record must not be reported as invalid: %s", result.Content)
	}
	if len(findingStore.findings) != 0 {
		t.Errorf("rejected batch must persist nothing, got %d findings", len(findingStore.findings))
	}
}

func TestIngestScannerResultsBatchBounds(t *testing.T) {
	findingStore := newFakeSecurityFindingStore()
	registry := newSecurityTestRegistry(t, findingStore, nil)

	if result := execTool(t, registry, "ingest_scanner_results", `{"records":[]}`); !result.IsError || !strings.Contains(result.Content, "records is required") {
		t.Errorf("empty batch: %+v", result)
	}
	if result := execTool(t, registry, "ingest_scanner_results", `{"records":[{}],"extra_field":1}`); !result.IsError || !strings.Contains(result.Content, "invalid input") {
		t.Errorf("unknown field: %+v", result)
	}

	rec := `{"tool":"gosec","rule_id":"G1","message":"m","severity":"HIGH","file_path":"a.go","start_line":1}`
	over := `{"records":[` + rec + strings.Repeat(","+rec, maxScannerBatchRecords) + `]}`
	if result := execTool(t, registry, "ingest_scanner_results", over); !result.IsError || !strings.Contains(result.Content, "record batch limit") {
		t.Errorf("record cap: IsError=%v content=%.120s", result.IsError, result.Content)
	}

	huge := `{"records":[{"tool":"gosec","rule_id":"G1","message":"` + strings.Repeat("x", maxScannerBatchBytes) + `","severity":"HIGH","file_path":"a.go"}]}`
	if result := execTool(t, registry, "ingest_scanner_results", huge); !result.IsError || !strings.Contains(result.Content, "byte batch limit") {
		t.Errorf("byte cap: IsError=%v content=%.120s", result.IsError, result.Content)
	}
	if len(findingStore.findings) != 0 {
		t.Errorf("bounded-out batches must persist nothing, got %d findings", len(findingStore.findings))
	}
}

func assertScannerRawPayload(t *testing.T, scannerRec *store.SecurityFindingRecord) {
	t.Helper()
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(scannerRec.Raw, &raw); err != nil {
		t.Fatalf("raw payload: %v", err)
	}
	if _, ok := raw["scanner_record"]; !ok {
		t.Errorf("raw payload missing scanner_record: %s", scannerRec.Raw)
	}
}

func TestIngestScannerResultsPersistsProvenanceAndCorrelates(t *testing.T) {
	findingStore := newFakeSecurityFindingStore()
	registry := newSecurityTestRegistry(t, findingStore, nil)
	scanCtx := testScanContext()

	if result := execTool(t, registry, "report_security_finding", testAgentCryptoFinding); result.IsError {
		t.Fatalf("report_security_finding: %s", result.Content)
	}
	result := execTool(t, registry, "ingest_scanner_results", testScannerCryptoBatch)
	if result.IsError {
		t.Fatalf("ingest_scanner_results: %s", result.Content)
	}
	if !strings.Contains(result.Content, "1 new finding(s)") || !strings.Contains(result.Content, "gosec") {
		t.Errorf("result = %q", result.Content)
	}
	if !strings.Contains(result.Content, "1 new agent↔scanner correlation(s)") {
		t.Errorf("result missing correlation note: %q", result.Content)
	}

	if len(findingStore.findings) != 2 {
		t.Fatalf("findings = %d, want 2 (agent + scanner, never merged)", len(findingStore.findings))
	}
	var agentRec, scannerRec *store.SecurityFindingRecord
	for _, rec := range findingStore.findings {
		if rec.SourceKind == "scanner" {
			scannerRec = rec
		} else {
			agentRec = rec
		}
	}
	if agentRec == nil || scannerRec == nil {
		t.Fatalf("missing a source kind: %+v", findingStore.findings)
	}
	if agentRec.SourceKind != "agent" || agentRec.Tool != "" || agentRec.RuleID != "" {
		t.Errorf("agent provenance = %q/%q/%q", agentRec.SourceKind, agentRec.Tool, agentRec.RuleID)
	}
	if agentRec.Confidence != "tentative" {
		t.Errorf("agent confidence semantics lost: %q", agentRec.Confidence)
	}
	if scannerRec.SourceKind != "scanner" || scannerRec.Tool != "gosec" || scannerRec.ToolVersion != "2.18.2" || scannerRec.RuleID != "G401" {
		t.Errorf("scanner provenance = %q/%q/%q/%q", scannerRec.SourceKind, scannerRec.Tool, scannerRec.ToolVersion, scannerRec.RuleID)
	}
	if scannerRec.Repository != scanCtx.Repository || scannerRec.Revision != scanCtx.Revision {
		t.Errorf("scanner repo/revision not stamped: %q/%q", scannerRec.Repository, scannerRec.Revision)
	}
	if scannerRec.SourceAgent != scanCtx.RunName {
		t.Errorf("scanner source agent = %q, want run name", scannerRec.SourceAgent)
	}

	// Correlation recorded on BOTH rows, neither deleted nor rewritten.
	if len(agentRec.CorrelatedFingerprints) != 1 || agentRec.CorrelatedFingerprints[0] != scannerRec.Fingerprint {
		t.Errorf("agent correlations = %v, want [%s]", agentRec.CorrelatedFingerprints, scannerRec.Fingerprint)
	}
	if len(scannerRec.CorrelatedFingerprints) != 1 || scannerRec.CorrelatedFingerprints[0] != agentRec.Fingerprint {
		t.Errorf("scanner correlations = %v, want [%s]", scannerRec.CorrelatedFingerprints, agentRec.Fingerprint)
	}
	correlatedEvents := 0
	for _, ev := range findingStore.events {
		if ev.EventType == "correlated" {
			correlatedEvents++
		}
	}
	if correlatedEvents != 2 {
		t.Errorf("correlated audit events = %d, want 2 (one per side)", correlatedEvents)
	}

	// The raw payload preserves the scanner record verbatim.
	assertScannerRawPayload(t, scannerRec)
}

func TestIngestScannerResultsRerunConvergesAndKeepsCorrelation(t *testing.T) {
	findingStore := newFakeSecurityFindingStore()
	registry := newSecurityTestRegistry(t, findingStore, nil)

	execTool(t, registry, "report_security_finding", testAgentCryptoFinding)
	if result := execTool(t, registry, "ingest_scanner_results", testScannerCryptoBatch); result.IsError {
		t.Fatalf("first ingest: %s", result.Content)
	}
	result := execTool(t, registry, "ingest_scanner_results", testScannerCryptoBatch)
	if result.IsError {
		t.Fatalf("second ingest: %s", result.Content)
	}
	if !strings.Contains(result.Content, "0 new finding(s), 1 merged") {
		t.Errorf("re-run must merge by fingerprint: %q", result.Content)
	}
	if strings.Contains(result.Content, "new agent↔scanner correlation") {
		t.Errorf("re-run must not re-correlate: %q", result.Content)
	}
	if len(findingStore.findings) != 2 {
		t.Errorf("findings = %d, want 2", len(findingStore.findings))
	}
	for _, rec := range findingStore.findings {
		if len(rec.CorrelatedFingerprints) != 1 {
			t.Errorf("correlation lost on re-run: %+v", rec)
		}
	}
}

func TestIngestScannerResultsRedactsRawEvidence(t *testing.T) {
	findingStore := newFakeSecurityFindingStore()
	registry := newSecurityTestRegistry(t, findingStore, nil)
	awsKey := "AKIA" + "IOSFODNN7EXAMPLE"

	batch := `{"records":[{"tool":"gitleaks","rule_id":"aws-access-key","message":"AWS key ` + awsKey + ` committed","severity":"HIGH","file_path":"config/prod.env","start_line":3,"cwe":"CWE-798","raw_evidence":"AWS_KEY=` + awsKey + `","extra":{"match":"` + awsKey + `"}}]}`
	if result := execTool(t, registry, "ingest_scanner_results", batch); result.IsError {
		t.Fatalf("ingest: %s", result.Content)
	}
	rec := findingStore.findings[0]
	if strings.Contains(rec.Description, awsKey) {
		t.Errorf("description not redacted: %q", rec.Description)
	}
	if strings.Contains(string(rec.Raw), awsKey) {
		t.Errorf("raw payload not redacted: %s", rec.Raw)
	}
	if !strings.Contains(string(rec.Raw), "[REDACTED]") {
		t.Errorf("raw payload missing redaction marker: %s", rec.Raw)
	}
}

func TestIngestScannerResultsInMemoryFallback(t *testing.T) {
	registry := newSecurityTestRegistry(t, nil, nil)

	execTool(t, registry, "report_security_finding", testAgentCryptoFinding)
	result := execTool(t, registry, "ingest_scanner_results", testScannerCryptoBatch)
	if result.IsError {
		t.Fatalf("ingest (in-memory): %s", result.Content)
	}
	if !strings.Contains(result.Content, "1 new agent↔scanner correlation(s)") {
		t.Errorf("in-memory correlation missing: %q", result.Content)
	}
	state := securityTestState(t, registry)
	state.mu.Lock()
	defer state.mu.Unlock()
	if len(state.mem) != 2 {
		t.Fatalf("mem findings = %d, want 2", len(state.mem))
	}
	counts := state.summarizeMemLocked()
	if counts["source_agent"] != 1 || counts["source_scanner"] != 1 || counts["correlated"] != 2 {
		t.Errorf("summary = %v", counts)
	}
	correlatedEvents := 0
	for _, ev := range state.memEvents {
		if ev.EventType == "correlated" {
			correlatedEvents++
		}
	}
	if correlatedEvents != 2 {
		t.Errorf("in-memory correlated events = %d, want 2", correlatedEvents)
	}
}

func TestSubmitSecurityScanReportIncludesScannerFindings(t *testing.T) {
	findingStore := newFakeSecurityFindingStore()
	stateStore := &securityArtifactTestStore{}
	registry := newSecurityTestRegistry(t, findingStore, stateStore)

	execTool(t, registry, "report_security_finding", testAgentCryptoFinding)
	if result := execTool(t, registry, "ingest_scanner_results", testScannerCryptoBatch); result.IsError {
		t.Fatalf("ingest: %s", result.Content)
	}
	result := execTool(t, registry, "submit_security_scan_report", `{"summary":"Mixed agent and scanner findings."}`)
	if result.IsError {
		t.Fatalf("submit: %s", result.Content)
	}

	markdown := stateStore.artifacts[SecurityReportArtifactKind]
	for _, want := range []string{
		"- **Source:** scanner gosec 2.18.2, rule G401",
		"- **Correlated with:**",
	} {
		if !strings.Contains(markdown, want) {
			t.Errorf("markdown report missing %q", want)
		}
	}
	sarif := stateStore.artifacts[SecuritySARIFArtifactKind]
	for _, want := range []string{`"name": "gosec"`, `"G401"`, `"sourceKind": "scanner"`} {
		if !strings.Contains(sarif, want) {
			t.Errorf("SARIF report missing %q", want)
		}
	}
	// Both findings survive dedupe (cross-source merge is forbidden) and
	// are ranked into the report.
	if !strings.Contains(result.Content, "2 finding(s) recorded, 2 after dedupe, 2 in the report") {
		t.Errorf("submit result = %q", result.Content)
	}
}

func TestReportSecurityFindingCannotForgeScannerProvenance(t *testing.T) {
	findingStore := newFakeSecurityFindingStore()
	registry := newSecurityTestRegistry(t, findingStore, nil)

	result := execTool(t, registry, "report_security_finding",
		`{"title":"x","category":"crypto","severity":"high","description":"d","file_path":"a.go","source_kind":"scanner","tool":"gosec","tool_version":"1.0","rule_id":"G401","correlated_fingerprints":["deadbeefdeadbeef"]}`)
	if result.IsError {
		t.Fatalf("unexpected error: %s", result.Content)
	}
	rec := findingStore.findings[0]
	if rec.SourceKind != "agent" || rec.Tool != "" || rec.ToolVersion != "" || rec.RuleID != "" || len(rec.CorrelatedFingerprints) != 0 {
		t.Errorf("forged scanner provenance must be stamped away: %+v", rec)
	}
}

func securityTestFinding(n int) string {
	return fmt.Sprintf(`{"title":"Issue %d","category":"injection","severity":"high","description":"finding %d","file_path":"a/b%d.go","start_line":%d}`, n, n, n, n+1)
}

func TestReportSecurityFindingRecordsWithoutLimit(t *testing.T) {
	findingStore := newFakeSecurityFindingStore()
	registry := newSecurityTestRegistry(t, findingStore, nil)

	for n := 1; n <= 20; n++ {
		if result := execTool(t, registry, "report_security_finding", securityTestFinding(n)); result.IsError {
			t.Fatalf("finding %d must persist: %s", n, result.Content)
		}
	}
	if len(findingStore.findings) != 20 {
		t.Fatalf("stored findings = %d, want 20", len(findingStore.findings))
	}
}

func TestReportSecurityFindingRecordsWithoutLimitInMemory(t *testing.T) {
	registry := newSecurityTestRegistry(t, nil, nil)

	for n := 1; n <= 20; n++ {
		if result := execTool(t, registry, "report_security_finding", securityTestFinding(n)); result.IsError {
			t.Fatalf("finding %d must persist: %s", n, result.Content)
		}
	}
	if state := securityTestState(t, registry); len(state.mem) != 20 {
		t.Fatalf("in-memory findings = %d, want 20", len(state.mem))
	}
}

func TestIngestScannerResultsRecordsEntireBatch(t *testing.T) {
	findingStore := newFakeSecurityFindingStore()
	registry := newSecurityTestRegistry(t, findingStore, nil)
	record := func(n int) string {
		return fmt.Sprintf(`{"tool":"gosec","rule_id":"G%d","message":"issue %d","severity":"HIGH","file_path":"a/b%d.go","start_line":%d}`, n, n, n, n+1)
	}

	result := execTool(t, registry, "ingest_scanner_results",
		fmt.Sprintf(`{"records":[%s,%s,%s]}`, record(1), record(2), record(3)))
	if result.IsError {
		t.Fatalf("scanner batch must persist: %s", result.Content)
	}
	if len(findingStore.findings) != 3 {
		t.Fatalf("stored findings = %d, want 3", len(findingStore.findings))
	}
}

// executionScanContext is testScanContext for one task run of a
// deterministic execution: every task (and fan-out instance) gets its own
// AgentRun name but shares the execution id.
func executionScanContext(runName, executionID, taskName string) SecurityScanContext {
	scanCtx := testScanContext()
	scanCtx.RunName = runName
	scanCtx.ExecutionID = executionID
	scanCtx.TaskName = taskName
	return scanCtx
}

// Every task run of one deterministic execution reports into the SAME scan
// record (the controller-stamped record key), so the dashboard scans list
// shows one row per execution instead of one per reporting task run — and
// the sink's report submission completes that shared row, pointing its
// session at the submitting run so the report artifacts resolve.
func TestSecurityScanSiblingRunsShareOneScanRecord(t *testing.T) {
	const recordName = "secscan-nightly-scan-exec-1"
	findingStore := newFakeSecurityFindingStore()
	ctxA := executionScanContext("run-a", "exec-1", "recon")
	ctxA.RecordRunName = recordName
	ctxB := executionScanContext("run-b", "exec-1", "exploit")
	ctxB.RecordRunName = recordName
	ctxB.SessionID = uuid.New()
	runA := newSecurityTestRegistryWithCtx(t, findingStore, nil, ctxA)
	runB := newSecurityTestRegistryWithCtx(t, findingStore, nil, ctxB)

	if result := execTool(t, runA, "report_security_finding",
		`{"title":"SQLi","category":"injection","severity":"high","description":"x","file_path":"a.go"}`); result.IsError {
		t.Fatalf("run A report failed: %s", result.Content)
	}
	if result := execTool(t, runB, "report_security_finding",
		`{"title":"XSS","category":"injection","severity":"medium","description":"y","file_path":"b.go"}`); result.IsError {
		t.Fatalf("run B report failed: %s", result.Content)
	}
	if len(findingStore.scans) != 1 {
		t.Fatalf("scan records = %d (%v), want the siblings to share one record", len(findingStore.scans), findingStore.scans)
	}
	shared := findingStore.scans["default/"+recordName]
	if shared == nil {
		t.Fatalf("scans = %v, want the record keyed by %q", findingStore.scans, recordName)
	}
	for _, f := range findingStore.findings {
		if f.ScanID != shared.ID {
			t.Fatalf("finding %q scanID = %s, want the shared record %s", f.Title, f.ScanID, shared.ID)
		}
	}

	if result := execTool(t, runB, "submit_security_scan_report", `{"summary":"done"}`); result.IsError {
		t.Fatalf("submit failed: %s", result.Content)
	}
	if len(findingStore.scans) != 1 {
		t.Fatalf("scan records after submit = %d, want still one", len(findingStore.scans))
	}
	final := findingStore.scans["default/"+recordName]
	if final.Status != "completed" || final.CompletedAt == nil {
		t.Fatalf("shared record = %+v, want completed with completedAt", final)
	}
	if final.SessionID == nil || *final.SessionID != ctxB.SessionID {
		t.Fatalf("record session = %v, want the submitting run's session %s", final.SessionID, ctxB.SessionID)
	}
}

func TestSecurityScanContextFromRunParsesExecutionAnnotations(t *testing.T) {
	run := &platformv1alpha1.AgentRun{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{
		SecurityScanNameAnnotation:        "nightly-scan",
		SecurityScanExecutionIDAnnotation: "  exec-1  ",
		SecurityScanRecordNameAnnotation:  " secscan-nightly-scan-exec-1 ",
		SecurityScanTaskNameAnnotation:    " recon ",
	}}}
	scanCtx, ok := SecurityScanContextFromRun(run, "default", "nightly-scan-recon-0", ".", uuid.New())
	if !ok {
		t.Fatalf("scan context not recognized")
	}
	if scanCtx.ExecutionID != "exec-1" || scanCtx.TaskName != "recon" {
		t.Fatalf("parsed context = %+v", scanCtx)
	}
	// The shared record key aims every task run's record writes at ONE row.
	if scanCtx.RecordRunName != "secscan-nightly-scan-exec-1" || scanCtx.RecordKey() != "secscan-nightly-scan-exec-1" {
		t.Fatalf("record key = %q (RecordRunName %q), want shared execution record name", scanCtx.RecordKey(), scanCtx.RecordRunName)
	}
	delete(run.Annotations, SecurityScanRecordNameAnnotation)
	if scanCtx, _ := SecurityScanContextFromRun(run, "default", "r", ".", uuid.Nil); scanCtx.RecordKey() != "r" {
		t.Errorf("record key without annotation = %q, want the run's own name", scanCtx.RecordKey())
	}

}

// The deterministic engine runs each task in its own AgentRun, so a sibling
// task (and the terminal sink task that submits the report) must see every
// finding of its execution — and nothing from another execution of the same
// scan.
func TestSecurityFindingsAggregatePerExecution(t *testing.T) {
	findingStore := newFakeSecurityFindingStore()
	stateStore := &securityArtifactTestStore{}
	runA := newSecurityTestRegistryWithCtx(t, findingStore, nil, executionScanContext("run-a", "exec-1", "recon"))
	runB := newSecurityTestRegistryWithCtx(t, findingStore, stateStore, executionScanContext("run-b", "exec-1", "sink"))
	otherExec := newSecurityTestRegistryWithCtx(t, findingStore, nil, executionScanContext("run-c", "exec-2", "recon"))

	if result := execTool(t, runA, "report_security_finding",
		`{"title":"SQL injection in login","category":"injection","severity":"critical","description":"query concat","file_path":"a.go"}`); result.IsError {
		t.Fatalf("run A report failed: %s", result.Content)
	}
	if result := execTool(t, otherExec, "report_security_finding",
		`{"title":"Stale execution finding","category":"crypto","severity":"high","description":"md5","file_path":"b.go"}`); result.IsError {
		t.Fatalf("other execution report failed: %s", result.Content)
	}

	list := execTool(t, runB, "list_security_findings", `{}`)
	if !strings.Contains(list.Content, "SQL injection in login") {
		t.Errorf("sibling run must see its execution's findings: %s", list.Content)
	}
	if strings.Contains(list.Content, "Stale execution finding") {
		t.Errorf("another execution's findings must never leak in: %s", list.Content)
	}

	result := execTool(t, runB, "submit_security_scan_report", `{"summary":"Fan-out execution."}`)
	if result.IsError {
		t.Fatalf("submit failed: %s", result.Content)
	}
	report := stateStore.artifacts[SecurityReportArtifactKind]
	if !strings.Contains(report, "SQL injection in login") {
		t.Errorf("report must contain the sibling run's finding:\n%s", report)
	}
	if strings.Contains(report, "Stale execution finding") {
		t.Errorf("report must not contain another execution's finding:\n%s", report)
	}
}

// Everything the operator already decided about — disproved, accepted,
// fixed, duplicate, suppressed — stays out of the report while its row and
// the whole-scan stored counts survive untouched.
func TestSubmitSecurityScanReportExcludesIneligibleFindings(t *testing.T) {
	findingStore := newFakeSecurityFindingStore()
	stateStore := &securityArtifactTestStore{}
	scanCtx := testScanContext()
	registry := newSecurityTestRegistryWithCtx(t, findingStore, stateStore, scanCtx)

	reported := []struct{ title, status string }{
		{"SQL injection in login", store.SecurityFindingStatusOpen},
		{"Weak hash for passwords", store.SecurityFindingStatusFalsePositive},
		{"Verbose error page", store.SecurityFindingStatusAcceptedRisk},
		{"Missing CSRF token", store.SecurityFindingStatusFixed},
	}
	for i, f := range reported {
		result := execTool(t, registry, "report_security_finding",
			fmt.Sprintf(`{"title":%q,"category":"injection","severity":"high","description":"d %d","file_path":"a/b%d.go","start_line":%d}`, f.title, i, i, i+1))
		if result.IsError {
			t.Fatalf("report %q failed: %s", f.title, result.Content)
		}
		if f.status == store.SecurityFindingStatusOpen {
			continue
		}
		update := execTool(t, registry, "update_security_finding",
			fmt.Sprintf(`{"fingerprint":%q,"status":%q,"note":"triaged"}`, findingStore.findings[i].Fingerprint, f.status))
		if update.IsError {
			t.Fatalf("update %q failed: %s", f.title, update.Content)
		}
	}
	// A duplicate row and a governed-suppressed row are written directly:
	// neither can be produced through the tools.
	canonical := findingStore.findings[0].ID
	findingStore.findings = append(findingStore.findings,
		&store.SecurityFindingRecord{
			ID: uuid.New(), Namespace: scanCtx.Namespace, ScanName: scanCtx.ScanName, RunName: scanCtx.RunName,
			Repository: scanCtx.Repository, Fingerprint: "dup00000000000000", Title: "Duplicate of the injection",
			Category: "injection", Severity: "high", Status: store.SecurityFindingStatusOpen, DuplicateOf: &canonical,
		},
		&store.SecurityFindingRecord{
			ID: uuid.New(), Namespace: scanCtx.Namespace, ScanName: scanCtx.ScanName, RunName: scanCtx.RunName,
			Repository: scanCtx.Repository, Fingerprint: "sup00000000000000", Title: "Suppressed by policy pack",
			Category: "injection", Severity: "high", Status: store.SecurityFindingStatusOpen,
			SuppressedBy: "baseline-pack/test-fixtures",
		})

	result := execTool(t, registry, "submit_security_scan_report", `{"summary":"One real issue."}`)
	if result.IsError {
		t.Fatalf("submit failed: %s", result.Content)
	}
	for _, want := range []string{
		"1 finding(s) triaged as false positives",
		"1 finding(s) triaged as accepted risk",
		"1 finding(s) triaged as fixed",
		"1 finding(s) collapsed as duplicates",
		"1 governed-suppressed finding(s)",
		"were excluded from the report",
	} {
		if !strings.Contains(result.Content, want) {
			t.Errorf("policy note %q missing %q", result.Content, want)
		}
	}
	report := stateStore.artifacts[SecurityReportArtifactKind]
	sarif := stateStore.artifacts[SecuritySARIFArtifactKind]
	if !strings.Contains(report, "SQL injection in login") || !strings.Contains(sarif, "SQL injection in login") {
		t.Errorf("the actionable finding must be reported:\n%s\n%s", report, sarif)
	}
	for _, title := range []string{"Weak hash for passwords", "Verbose error page", "Missing CSRF token", "Duplicate of the injection", "Suppressed by policy pack"} {
		if strings.Contains(report, title) {
			t.Errorf("report must not contain %q:\n%s", title, report)
		}
		if strings.Contains(sarif, title) {
			t.Errorf("SARIF must not contain %q:\n%s", title, sarif)
		}
	}

	if len(findingStore.findings) != 6 {
		t.Fatalf("stored findings = %d, want 6 (rows are marked, never erased)", len(findingStore.findings))
	}
	scan := findingStore.scans[scanCtx.Namespace+"/"+scanCtx.RunName]
	if scan == nil {
		t.Fatalf("scan record not upserted")
	}
	// Whole-scan counts: the four tool-reported rows (duplicates and
	// suppressed rows are excluded from summaries by contract), of which
	// only one is still open.
	if scan.Counts["total"] != 4 || scan.Counts["open"] != 1 || scan.Counts["open_high"] != 1 {
		t.Errorf("scan counts = %v, want total 4 with 1 open high", scan.Counts)
	}
	if scan.Counts["suppressed"] != 1 {
		t.Errorf("suppressed count = %d, want 1", scan.Counts["suppressed"])
	}
}

// An execution's findings are the sum of every sibling run's, so they can
// exceed the store's per-call row cap: the report must page over them instead
// of silently dropping the lowest-scoring ones.
func TestSubmitSecurityScanReportPagesPastStoreRowCap(t *testing.T) {
	findingStore := newFakeSecurityFindingStore()
	stateStore := &securityArtifactTestStore{}
	scanCtx := testScanContext()
	scanCtx.ExecutionID = "exec-1"
	scanCtx.DedupePermille = 0
	registry := newSecurityTestRegistryWithCtx(t, findingStore, stateStore, scanCtx)

	const total = securityFindingPageSize + 250
	for i := range total {
		findingStore.findings = append(findingStore.findings, seedSecurityFinding(scanCtx, i, float64(total-i)))
	}

	result := execTool(t, registry, "submit_security_scan_report", `{"summary":"Big execution."}`)
	if result.IsError {
		t.Fatalf("submit failed: %s", result.Content)
	}
	if !strings.Contains(result.Content, fmt.Sprintf("%d finding(s) recorded", total)) ||
		!strings.Contains(result.Content, fmt.Sprintf("%d in the report", total)) {
		t.Errorf("report must cover every finding of the execution: %s", result.Content)
	}
	if strings.Contains(result.Content, "truncated to the") {
		t.Errorf("no truncation expected below the ceiling: %s", result.Content)
	}
	report := stateStore.artifacts[SecurityReportArtifactKind]
	// The lowest-scoring finding is exactly the one a single 1000-row load
	// would have dropped.
	for _, want := range []string{"Finding 0000", "Finding 0999", fmt.Sprintf("Finding %04d", total-1)} {
		if !strings.Contains(report, want) {
			t.Errorf("markdown report missing %q", want)
		}
	}
	sarif := stateStore.artifacts[SecuritySARIFArtifactKind]
	if !strings.Contains(sarif, fmt.Sprintf("Finding %04d", total-1)) {
		t.Errorf("SARIF artifact missing the lowest-scoring finding")
	}
}

// Paging is bounded, and hitting the ceiling must be disclosed rather than
// silently truncating the result.
func TestListAllFindingsCeilingIsDisclosed(t *testing.T) {
	findingStore := newFakeSecurityFindingStore()
	scanCtx := testScanContext()
	registry := newSecurityTestRegistryWithCtx(t, findingStore, &securityArtifactTestStore{}, scanCtx)
	state := securityTestState(t, registry)

	for i := range securityFindingScopeMax + 10 {
		findingStore.findings = append(findingStore.findings, seedSecurityFinding(scanCtx, i, float64(securityFindingScopeMax+10-i)))
	}

	records, truncated, err := state.listAllFindings(context.Background(), state.scopeFilter())
	if err != nil {
		t.Fatalf("listAllFindings: %v", err)
	}
	if !truncated {
		t.Errorf("truncated = false, want true past the ceiling")
	}
	if len(records) != securityFindingScopeMax {
		t.Errorf("records = %d, want %d", len(records), securityFindingScopeMax)
	}
	if records[0].Fingerprint != "fp-00000" {
		t.Errorf("truncation must keep the highest-scoring findings, got %q first", records[0].Fingerprint)
	}
}

func seedSecurityFinding(scanCtx SecurityScanContext, i int, score float64) *store.SecurityFindingRecord {
	return &store.SecurityFindingRecord{
		ID:          uuid.New(),
		Namespace:   scanCtx.Namespace,
		ScanName:    scanCtx.ScanName,
		RunName:     scanCtx.RunName,
		ExecutionID: scanCtx.ExecutionID,
		Repository:  scanCtx.Repository,
		Revision:    scanCtx.Revision,
		Fingerprint: fmt.Sprintf("fp-%05d", i),
		Title:       fmt.Sprintf("Finding %04d", i),
		Category:    "injection",
		Severity:    "medium",
		Confidence:  "high",
		Description: fmt.Sprintf("issue number %04d", i),
		FilePath:    fmt.Sprintf("pkg/f%04d.go", i),
		SourceKind:  "agent",
		Status:      store.SecurityFindingStatusOpen,
		Score:       score,
		Occurrences: 1,
		LastSeenAt:  time.Now().UTC(),
	}
}

// Without Postgres the in-memory filter must hide governed-suppressed
// findings exactly like the SQL one, or the no-store path shows findings the
// store would have hidden.
func TestMemFindingMatchesSuppressedSemantics(t *testing.T) {
	registry := newSecurityTestRegistry(t, nil, &securityArtifactTestStore{})
	state := securityTestState(t, registry)

	for _, finding := range []string{
		`{"title":"SQL injection in login","category":"injection","severity":"critical","description":"query concat","file_path":"a.go"}`,
		`{"title":"Weak hash for passwords","category":"crypto","severity":"medium","description":"md5 used","file_path":"b.go"}`,
	} {
		if result := execTool(t, registry, "report_security_finding", finding); result.IsError {
			t.Fatalf("report failed: %s", result.Content)
		}
	}
	state.mu.Lock()
	suppressed := state.mem[1]
	suppressed.SuppressedBy = "policy-pack/known-md5"
	suppressed.SuppressedReason = "accepted by policy"
	state.mu.Unlock()

	list := func(mode string) []string {
		t.Helper()
		filter := state.scopeFilter()
		filter.Suppressed = mode
		records, err := state.listFindings(context.Background(), filter)
		if err != nil {
			t.Fatalf("listFindings(%q): %v", mode, err)
		}
		titles := make([]string, 0, len(records))
		for _, rec := range records {
			titles = append(titles, rec.Title)
		}
		slices.Sort(titles)
		return titles
	}

	for _, tc := range []struct {
		mode string
		want []string
	}{
		{"", []string{"SQL injection in login"}},
		{store.SecuritySuppressedExclude, []string{"SQL injection in login"}},
		{store.SecuritySuppressedInclude, []string{"SQL injection in login", "Weak hash for passwords"}},
		{store.SecuritySuppressedOnly, []string{"Weak hash for passwords"}},
	} {
		if got := list(tc.mode); !slices.Equal(got, tc.want) {
			t.Errorf("suppressed=%q listed %v, want %v", tc.mode, got, tc.want)
		}
	}

	result := execTool(t, registry, "list_security_findings", `{}`)
	if result.IsError {
		t.Fatalf("list failed: %s", result.Content)
	}
	if strings.Contains(result.Content, "Weak hash for passwords") {
		t.Errorf("list_security_findings must hide suppressed findings by default: %s", result.Content)
	}
}
