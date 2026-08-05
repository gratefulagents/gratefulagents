package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/gratefulagents/sdk/pkg/agentsdk"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/security"
	"github.com/gratefulagents/gratefulagents/internal/store"
)

// AgentRun annotations set by the SecurityScan controller on scan runs,
// aliased from the triggers API so the two cannot drift.
const (
	SecurityScanNameAnnotation           = triggersv1alpha1.SecurityScanNameAnnotation
	SecurityScanRepositoryAnnotation     = triggersv1alpha1.SecurityScanRepositoryAnnotation
	SecurityScanRevisionAnnotation       = triggersv1alpha1.SecurityScanRevisionAnnotation
	SecurityScanMinSeverityAnnotation    = triggersv1alpha1.SecurityScanMinSeverityAnnotation
	SecurityScanDedupePermilleAnnotation = triggersv1alpha1.SecurityScanDedupePermilleAnnotation
)

// Session artifact kinds written by submit_security_scan_report.
const (
	SecurityReportArtifactKind = "security_report"
	SecuritySARIFArtifactKind  = "security_sarif"
)

// SecurityScanContext identifies the security scan a run belongs to.
type SecurityScanContext struct {
	ScanName   string
	Namespace  string
	RunName    string
	Repository string
	Revision   string
	// MinSeverity is the scan's operator-set severity floor ("" = unset).
	MinSeverity string
	// DedupePermille is the scan's dedupe similarity threshold in permille:
	// 0 disables dedupe, negative means unset (use the built-in default).
	DedupePermille int32
	SessionID      uuid.UUID
}

// SecurityScanContextFromRun extracts scan context from AgentRun annotations
// set by the SecurityScan controller (security.gratefulagents.dev/scan-name,
// .../repository, .../revision, .../min-severity, .../dedupe-permille). It
// returns (ctx, true) only when the scan-name annotation is present, i.e.
// the run is a security scan run.
func SecurityScanContextFromRun(run *platformv1alpha1.AgentRun, namespace, runName string, sessionID uuid.UUID) (SecurityScanContext, bool) {
	if run == nil {
		return SecurityScanContext{}, false
	}
	scanName := strings.TrimSpace(run.Annotations[SecurityScanNameAnnotation])
	if scanName == "" {
		return SecurityScanContext{}, false
	}
	minSeverity := strings.ToLower(strings.TrimSpace(run.Annotations[SecurityScanMinSeverityAnnotation]))
	if security.SeverityRank(minSeverity) < 0 {
		minSeverity = ""
	}
	dedupePermille := int32(-1)
	if raw := strings.TrimSpace(run.Annotations[SecurityScanDedupePermilleAnnotation]); raw != "" {
		if v, err := strconv.ParseInt(raw, 10, 32); err == nil && v >= 0 {
			dedupePermille = int32(v)
		}
	}
	return SecurityScanContext{
		ScanName:       scanName,
		Namespace:      namespace,
		RunName:        runName,
		Repository:     strings.TrimSpace(run.Annotations[SecurityScanRepositoryAnnotation]),
		Revision:       strings.TrimSpace(run.Annotations[SecurityScanRevisionAnnotation]),
		MinSeverity:    minSeverity,
		DedupePermille: dedupePermille,
		SessionID:      sessionID,
	}, true
}

// RegisterSecurityScanTools registers the security finding tools for a scan
// run. findingStore may be nil (no Postgres); the tools then keep findings in
// an in-memory buffer scoped to this process so the scan still works, minus
// cross-run persistence.
func RegisterSecurityScanTools(registry *Registry, findingStore store.SecurityFindingStore, stateStore store.StateStore, scanCtx SecurityScanContext) {
	if registry == nil {
		return
	}
	state := &securityScanState{
		findingStore: findingStore,
		stateStore:   stateStore,
		scanCtx:      scanCtx,
	}
	registry.Register(&reportSecurityFindingTool{state: state})
	registry.Register(&listSecurityFindingsTool{state: state})
	registry.Register(&updateSecurityFindingTool{state: state})
	registry.Register(&submitSecurityScanReportTool{state: state})
}

// securityScanState is shared by the security scan tools. When findingStore
// is nil it degrades to an in-memory buffer with the same upsert/list/status
// semantics, so the tools work without Postgres.
type securityScanState struct {
	findingStore store.SecurityFindingStore
	stateStore   store.StateStore
	scanCtx      SecurityScanContext

	mu        sync.Mutex
	mem       []*store.SecurityFindingRecord
	memEvents []store.SecurityFindingEvent
	scanID    uuid.UUID

	memCompleted bool
	memSummary   string
	memCounts    map[string]int32
}

// ensureScan lazily creates (or reuses) the security_scans row this run's
// findings belong to and returns its id. Findings carry a non-null scan_id
// foreign key, so this must run before the first finding is persisted.
func (s *securityScanState) ensureScan(ctx context.Context) (uuid.UUID, error) {
	if s.findingStore == nil {
		return uuid.Nil, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.scanID != uuid.Nil {
		return s.scanID, nil
	}
	if existing, err := s.findingStore.GetSecurityScan(ctx, s.scanCtx.Namespace, s.scanCtx.RunName); err != nil {
		return uuid.Nil, err
	} else if existing != nil {
		s.scanID = existing.ID
		return s.scanID, nil
	}
	started := time.Now().UTC()
	created, err := s.findingStore.UpsertSecurityScan(ctx, &store.SecurityScanRecord{
		Namespace:  s.scanCtx.Namespace,
		ScanName:   s.scanCtx.ScanName,
		RunName:    s.scanCtx.RunName,
		SessionID:  s.sessionIDPtr(),
		Repository: s.scanCtx.Repository,
		Revision:   s.scanCtx.Revision,
		Status:     "running",
		StartedAt:  &started,
	})
	if err != nil {
		return uuid.Nil, err
	}
	s.scanID = created.ID
	return s.scanID, nil
}

func (s *securityScanState) sessionIDPtr() *uuid.UUID {
	if s.scanCtx.SessionID == uuid.Nil {
		return nil
	}
	id := s.scanCtx.SessionID
	return &id
}

// upsertFinding persists the finding, merging into an existing record with
// the same fingerprint. The bool reports whether a new record was created.
func (s *securityScanState) upsertFinding(ctx context.Context, rec *store.SecurityFindingRecord) (*store.SecurityFindingRecord, bool, error) {
	if s.findingStore != nil {
		return s.findingStore.UpsertSecurityFinding(ctx, rec)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	for _, existing := range s.mem {
		if existing.Fingerprint != rec.Fingerprint {
			continue
		}
		existing.Occurrences++
		existing.LastSeenAt = now
		if security.SeverityRank(rec.Severity) > security.SeverityRank(existing.Severity) {
			existing.Severity = rec.Severity
		}
		if rec.Score > existing.Score {
			existing.Score = rec.Score
		}
		existing.Title = rec.Title
		existing.Category = rec.Category
		existing.Confidence = rec.Confidence
		existing.FilePath = rec.FilePath
		existing.StartLine = rec.StartLine
		existing.EndLine = rec.EndLine
		existing.Symbol = rec.Symbol
		existing.CWE = rec.CWE
		existing.Description = rec.Description
		existing.Impact = rec.Impact
		existing.AttackVector = rec.AttackVector
		existing.Remediation = rec.Remediation
		existing.References = rec.References
		existing.Raw = rec.Raw
		copied := *existing
		return &copied, false, nil
	}
	stored := *rec
	stored.ID = uuid.New()
	if stored.Status == "" {
		stored.Status = store.SecurityFindingStatusOpen
	}
	stored.Occurrences = 1
	stored.FirstSeenAt = now
	stored.LastSeenAt = now
	s.mem = append(s.mem, &stored)
	copied := stored
	return &copied, true, nil
}

func (s *securityScanState) listFindings(ctx context.Context, f store.SecurityFindingFilter) ([]store.SecurityFindingRecord, error) {
	if s.findingStore != nil {
		return s.findingStore.ListSecurityFindings(ctx, f)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	limit := f.Limit
	if limit <= 0 {
		limit = 200
	}
	if limit > 1000 {
		limit = 1000
	}
	var out []store.SecurityFindingRecord
	for _, rec := range s.mem {
		if f.Namespace != "" && rec.Namespace != f.Namespace {
			continue
		}
		if f.ScanName != "" && rec.ScanName != f.ScanName {
			continue
		}
		if f.RunName != "" && rec.RunName != f.RunName {
			continue
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
		if f.MinScore > 0 && rec.Score < f.MinScore {
			continue
		}
		if !f.IncludeDuplicates && rec.DuplicateOf != nil {
			continue
		}
		if f.Search != "" {
			needle := strings.ToLower(f.Search)
			if !strings.Contains(strings.ToLower(rec.Title), needle) &&
				!strings.Contains(strings.ToLower(rec.Description), needle) &&
				!strings.Contains(strings.ToLower(rec.FilePath), needle) {
				continue
			}
		}
		out = append(out, *rec)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		ri, rj := security.SeverityRank(out[i].Severity), security.SeverityRank(out[j].Severity)
		if ri != rj {
			return ri > rj
		}
		if !out[i].LastSeenAt.Equal(out[j].LastSeenAt) {
			return out[i].LastSeenAt.After(out[j].LastSeenAt)
		}
		return out[i].Fingerprint < out[j].Fingerprint
	})
	offset := f.Offset
	if offset < 0 {
		offset = 0
	}
	if int(offset) >= len(out) {
		return nil, nil
	}
	out = out[offset:]
	if int32(len(out)) > limit {
		out = out[:limit]
	}
	return out, nil
}

// getFinding looks up a finding by id, scoped to this scan's namespace.
// It returns (nil, nil) when no finding matches.
func (s *securityScanState) getFinding(ctx context.Context, id uuid.UUID) (*store.SecurityFindingRecord, error) {
	if s.findingStore != nil {
		return s.findingStore.GetSecurityFinding(ctx, s.scanCtx.Namespace, id)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, rec := range s.mem {
		if rec.ID == id {
			copied := *rec
			return &copied, nil
		}
	}
	return nil, nil
}

// setFindingStatus updates the status of a finding in this scan's namespace.
// The audit actor is always this run's name; the model cannot supply it.
func (s *securityScanState) setFindingStatus(ctx context.Context, id uuid.UUID, status, note string) error {
	actor := s.scanCtx.RunName
	if s.findingStore != nil {
		return s.findingStore.SetSecurityFindingStatus(ctx, s.scanCtx.Namespace, id, status, actor, note)
	}
	if !store.ValidSecurityFindingStatus(status) {
		return fmt.Errorf("invalid status %q", status)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, rec := range s.mem {
		if rec.ID != id {
			continue
		}
		detail, _ := json.Marshal(map[string]string{"from": rec.Status, "to": status})
		rec.Status = status
		s.memEvents = append(s.memEvents, store.SecurityFindingEvent{
			FindingID: id,
			EventType: "status_changed",
			Actor:     actor,
			Note:      note,
			Detail:    detail,
			CreatedAt: time.Now().UTC(),
		})
		return nil
	}
	return store.ErrSecurityFindingNotFound
}

// summarizeMemLocked mirrors the Postgres SummarizeSecurityFindings contract
// over the in-memory buffer: non-duplicate rows counted per severity, plus
// total, open, and open_<severity> keys. Caller must hold s.mu.
func (s *securityScanState) summarizeMemLocked() map[string]int32 {
	counts := map[string]int32{
		"total": 0, "open": 0,
		"open_critical": 0, "open_high": 0, "open_medium": 0, "open_low": 0, "open_info": 0,
	}
	for _, rec := range s.mem {
		if rec.DuplicateOf != nil {
			continue
		}
		counts[rec.Severity]++
		counts["total"]++
		if rec.Status == store.SecurityFindingStatusOpen {
			counts["open"]++
			counts["open_"+rec.Severity]++
		}
	}
	return counts
}

// resolveFinding resolves an update target from an explicit id or a
// fingerprint and verifies it belongs to this run's scan (same namespace AND
// same scan name), so a model-supplied id cannot re-triage findings of other
// scans or namespaces.
func (s *securityScanState) resolveFinding(ctx context.Context, id, fingerprint string) (*store.SecurityFindingRecord, error) {
	if id != "" {
		parsed, err := uuid.Parse(id)
		if err != nil {
			return nil, fmt.Errorf("invalid finding id %q: %v", id, err)
		}
		rec, err := s.getFinding(ctx, parsed)
		if err != nil && !errors.Is(err, store.ErrSecurityFindingNotFound) {
			return nil, fmt.Errorf("failed to look up finding: %v", err)
		}
		if rec == nil || errors.Is(err, store.ErrSecurityFindingNotFound) {
			return nil, fmt.Errorf("no finding with id %s in this scan (use list_security_findings to see recorded findings)", parsed)
		}
		if rec.Namespace != s.scanCtx.Namespace || rec.ScanName != s.scanCtx.ScanName {
			return nil, fmt.Errorf("finding %s does not belong to security scan %q; this run can only update findings of its own scan", parsed, s.scanCtx.ScanName)
		}
		return rec, nil
	}
	if fingerprint == "" {
		return nil, fmt.Errorf("either id or fingerprint is required")
	}
	records, err := s.listFindings(ctx, store.SecurityFindingFilter{
		Namespace:         s.scanCtx.Namespace,
		ScanName:          s.scanCtx.ScanName,
		IncludeDuplicates: true,
		Limit:             1000,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to look up finding: %v", err)
	}
	for i := range records {
		if records[i].Fingerprint == fingerprint {
			return &records[i], nil
		}
	}
	return nil, fmt.Errorf("no finding with fingerprint %q in this scan (use list_security_findings to see recorded findings)", fingerprint)
}

// securityFindingRecord converts a normalized, validated finding into a store
// record scoped to the scan.
func securityFindingRecord(f security.Finding, scanCtx SecurityScanContext, sessionID *uuid.UUID) *store.SecurityFindingRecord {
	raw, err := json.Marshal(f)
	if err != nil {
		raw = nil
	}
	return &store.SecurityFindingRecord{
		Namespace:    scanCtx.Namespace,
		ScanName:     scanCtx.ScanName,
		RunName:      scanCtx.RunName,
		SessionID:    sessionID,
		Fingerprint:  f.Fingerprint,
		Title:        f.Title,
		Category:     f.Category,
		Severity:     f.Severity,
		Confidence:   f.Confidence,
		Repository:   f.Repository,
		Revision:     f.Revision,
		FilePath:     f.FilePath,
		StartLine:    int32(f.StartLine),
		EndLine:      int32(f.EndLine),
		Symbol:       f.Symbol,
		CWE:          f.CWE,
		Description:  f.Description,
		Impact:       f.Impact,
		AttackVector: f.AttackVector,
		Remediation:  f.Remediation,
		References:   f.References,
		SourceAgent:  f.SourceAgent,
		ScanStep:     f.ScanStep,
		Status:       store.SecurityFindingStatusOpen,
		Raw:          raw,
	}
}

// securityFindingFromRecord rebuilds a security.Finding from a stored record.
// The raw JSON (when present) restores fields without dedicated columns
// (evidence, tags); the columns then win for everything merge can change.
func securityFindingFromRecord(rec store.SecurityFindingRecord) security.Finding {
	var f security.Finding
	if len(rec.Raw) > 0 {
		_ = json.Unmarshal(rec.Raw, &f)
	}
	f.Title = rec.Title
	f.Category = rec.Category
	f.Severity = rec.Severity
	f.Confidence = rec.Confidence
	f.Repository = rec.Repository
	f.Revision = rec.Revision
	f.FilePath = rec.FilePath
	f.StartLine = int(rec.StartLine)
	f.EndLine = int(rec.EndLine)
	f.Symbol = rec.Symbol
	f.CWE = rec.CWE
	f.Description = rec.Description
	f.Impact = rec.Impact
	f.AttackVector = rec.AttackVector
	f.Remediation = rec.Remediation
	f.References = rec.References
	f.SourceAgent = rec.SourceAgent
	f.ScanStep = rec.ScanStep
	f.Fingerprint = rec.Fingerprint
	return f
}

// --- report_security_finding ---

type reportSecurityFindingTool struct {
	state *securityScanState
}

func (t *reportSecurityFindingTool) Name() string { return "report_security_finding" }

func (t *reportSecurityFindingTool) Description() string {
	return "Record one security finding for this scan. The input IS the finding object " +
		"itself (flat fields, no wrapper), matching the platform security finding schema. " +
		"The finding is normalized, validated, and deduplicated by fingerprint: reporting " +
		"the same issue twice merges into the existing finding instead of creating a " +
		"duplicate. The scanned repository and revision are stamped from the scan " +
		"configuration automatically; do not supply them. Returns the finding fingerprint. " +
		"This tool only records platform scan state — it never mutates the workspace, the " +
		"repository, or the network — so it is safe and available on read-only scan runs."
}

// reportFindingInputSchema is security.FindingJSONSchema minus the
// repository/revision properties: those are always stamped from the scan
// context (never model-supplied), so the model must not be told to send them.
var reportFindingInputSchema = buildReportFindingInputSchema()

func buildReportFindingInputSchema() json.RawMessage {
	var schema map[string]any
	if err := json.Unmarshal([]byte(security.FindingJSONSchema), &schema); err != nil {
		return json.RawMessage(security.FindingJSONSchema)
	}
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		return json.RawMessage(security.FindingJSONSchema)
	}
	delete(props, "repository")
	delete(props, "revision")
	if sa, ok := props["source_agent"].(map[string]any); ok {
		sa["description"] = "Optional sub-agent label; recorded as \"<run-name>/<label>\" so attribution always includes this run."
	}
	out, err := json.Marshal(schema)
	if err != nil {
		return json.RawMessage(security.FindingJSONSchema)
	}
	return out
}

func (t *reportSecurityFindingTool) InputSchema() json.RawMessage {
	// Flat option: the tool input is the finding object itself, so the model
	// does not need a {"finding": {...}} wrapper.
	return reportFindingInputSchema
}

func (t *reportSecurityFindingTool) IsReadOnly() bool                      { return true }
func (t *reportSecurityFindingTool) IsEnabled(_ *agentsdk.RunContext) bool { return true }
func (t *reportSecurityFindingTool) NeedsApproval() bool                   { return false }
func (t *reportSecurityFindingTool) TimeoutSeconds() int                   { return 0 }

func (t *reportSecurityFindingTool) Execute(ctx context.Context, input json.RawMessage, _ string) (Result, error) {
	var finding security.Finding
	dec := json.NewDecoder(bytes.NewReader(input))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&finding); err != nil {
		return Result{Content: fmt.Sprintf("invalid input: %v (the input must be a single flat finding object; see the tool schema for the allowed fields)", err), IsError: true}, nil
	}
	// Repository and revision are stamped unconditionally: they are part of
	// the finding dedupe key (namespace, scan_name, repository, fingerprint),
	// so a model-supplied value could forge another scan's identity and
	// overwrite its findings.
	finding.Repository = t.state.scanCtx.Repository
	finding.Revision = t.state.scanCtx.Revision
	// SourceAgent is always anchored to this run's name; a model-supplied
	// value is kept only as a sub-agent label suffix ("<run-name>/<label>")
	// so attribution cannot be forged.
	if label := strings.TrimSpace(finding.SourceAgent); label == "" || label == t.state.scanCtx.RunName {
		finding.SourceAgent = t.state.scanCtx.RunName
	} else {
		finding.SourceAgent = t.state.scanCtx.RunName + "/" + label
	}
	finding.Normalize()
	if err := finding.Validate(); err != nil {
		return Result{Content: fmt.Sprintf("%v — fix the listed fields and call report_security_finding again", err), IsError: true}, nil
	}

	rec := securityFindingRecord(finding, t.state.scanCtx, t.state.sessionIDPtr())
	scanID, err := t.state.ensureScan(ctx)
	if err != nil {
		return Result{Content: fmt.Sprintf("failed to open the scan record: %v", err), IsError: true}, nil
	}
	rec.ScanID = scanID
	stored, created, err := t.state.upsertFinding(ctx, rec)
	if err != nil {
		return Result{Content: fmt.Sprintf("failed to persist finding: %v", err), IsError: true}, nil
	}
	if created {
		return Result{Content: fmt.Sprintf("Finding recorded (fingerprint %s, severity %s, category %s).", stored.Fingerprint, stored.Severity, stored.Category)}, nil
	}
	return Result{Content: fmt.Sprintf("Finding merged into existing finding with fingerprint %s (now observed %d times, severity %s). It was already reported; do not report it again.", stored.Fingerprint, stored.Occurrences, stored.Severity)}, nil
}

// --- list_security_findings ---

type listSecurityFindingsTool struct {
	state *securityScanState
}

type listSecurityFindingsInput struct {
	Severity string `json:"severity"`
	Category string `json:"category"`
	Status   string `json:"status"`
	Search   string `json:"search"`
	Limit    int32  `json:"limit"`
}

func (t *listSecurityFindingsTool) Name() string { return "list_security_findings" }

func (t *listSecurityFindingsTool) Description() string {
	return "List the security findings recorded so far for this scan as a compact table " +
		"(fingerprint, severity, category, status, location, title). Use it before " +
		"reporting to avoid duplicates and when triaging or validating findings. Read-only."
}

func (t *listSecurityFindingsTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"severity": {"type": "string", "enum": ["critical", "high", "medium", "low", "info"], "description": "Only findings with this severity"},
			"category": {"type": "string", "description": "Only findings in this category, e.g. injection"},
			"status": {"type": "string", "enum": ["open", "triaged", "confirmed", "false_positive", "fixed", "accepted_risk"], "description": "Only findings with this status"},
			"search": {"type": "string", "description": "Substring match on title, description, or file path"},
			"limit": {"type": "integer", "minimum": 1, "maximum": 1000, "description": "Max findings to return (default 200)"}
		}
	}`)
}

func (t *listSecurityFindingsTool) IsReadOnly() bool                      { return true }
func (t *listSecurityFindingsTool) IsEnabled(_ *agentsdk.RunContext) bool { return true }
func (t *listSecurityFindingsTool) NeedsApproval() bool                   { return false }
func (t *listSecurityFindingsTool) TimeoutSeconds() int                   { return 0 }

func (t *listSecurityFindingsTool) Execute(ctx context.Context, input json.RawMessage, _ string) (Result, error) {
	var in listSecurityFindingsInput
	if err := json.Unmarshal(input, &in); err != nil {
		return Result{Content: fmt.Sprintf("invalid input: %v", err), IsError: true}, nil
	}
	records, err := t.state.listFindings(ctx, store.SecurityFindingFilter{
		Namespace: t.state.scanCtx.Namespace,
		RunName:   t.state.scanCtx.RunName,
		Severity:  strings.ToLower(strings.TrimSpace(in.Severity)),
		Category:  strings.ToLower(strings.TrimSpace(in.Category)),
		Status:    strings.ToLower(strings.TrimSpace(in.Status)),
		Search:    strings.TrimSpace(in.Search),
		Limit:     in.Limit,
	})
	if err != nil {
		return Result{Content: fmt.Sprintf("failed to list findings: %v", err), IsError: true}, nil
	}
	if len(records) == 0 {
		return Result{Content: "No findings recorded (matching the filters) yet."}, nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d finding(s):\n\n", len(records))
	b.WriteString("FINGERPRINT      | SEVERITY | CATEGORY         | STATUS         | LOCATION | TITLE\n")
	for _, rec := range records {
		location := rec.FilePath
		if location == "" {
			location = "-"
		} else if rec.StartLine > 0 {
			location = fmt.Sprintf("%s:%d", location, rec.StartLine)
			if rec.EndLine > rec.StartLine {
				location = fmt.Sprintf("%s-%d", location, rec.EndLine)
			}
		}
		title := rec.Title
		if len(title) > 90 {
			title = title[:87] + "..."
		}
		fmt.Fprintf(&b, "%-16s | %-8s | %-16s | %-14s | %s | %s\n",
			rec.Fingerprint, rec.Severity, rec.Category, rec.Status, location, title)
	}
	return Result{Content: strings.TrimRight(b.String(), "\n")}, nil
}

// --- update_security_finding ---

type updateSecurityFindingTool struct {
	state *securityScanState
}

type updateSecurityFindingInput struct {
	ID          string `json:"id"`
	Fingerprint string `json:"fingerprint"`
	Status      string `json:"status"`
	Note        string `json:"note"`
}

func (t *updateSecurityFindingTool) Name() string { return "update_security_finding" }

func (t *updateSecurityFindingTool) Description() string {
	return "Set the status of a security finding recorded by this scan with an audit note: " +
		"confirmed (you built a PoC or proved exploitability), false_positive (you disproved " +
		"it), triaged, fixed, accepted_risk, or open. Identify the finding by the fingerprint " +
		"returned from report_security_finding / list_security_findings (or its id). Only " +
		"findings belonging to this scan can be updated; the audit trail records this run as " +
		"the actor. Only updates platform scan state; safe on read-only scan runs."
}

func (t *updateSecurityFindingTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"fingerprint": {"type": "string", "description": "Fingerprint of the finding to update"},
			"id": {"type": "string", "description": "Finding UUID, as an alternative to fingerprint"},
			"status": {"type": "string", "enum": ["open", "triaged", "confirmed", "false_positive", "fixed", "accepted_risk"], "description": "New status"},
			"note": {"type": "string", "description": "Why the status changed, e.g. the PoC that confirmed it or the reasoning that disproved it"}
		},
		"required": ["status", "note"]
	}`)
}

func (t *updateSecurityFindingTool) IsReadOnly() bool                      { return true }
func (t *updateSecurityFindingTool) IsEnabled(_ *agentsdk.RunContext) bool { return true }
func (t *updateSecurityFindingTool) NeedsApproval() bool                   { return false }
func (t *updateSecurityFindingTool) TimeoutSeconds() int                   { return 0 }

func (t *updateSecurityFindingTool) Execute(ctx context.Context, input json.RawMessage, _ string) (Result, error) {
	var in updateSecurityFindingInput
	if err := json.Unmarshal(input, &in); err != nil {
		return Result{Content: fmt.Sprintf("invalid input: %v", err), IsError: true}, nil
	}
	status := strings.ToLower(strings.TrimSpace(in.Status))
	if !store.ValidSecurityFindingStatus(status) {
		return Result{Content: fmt.Sprintf("invalid status %q (valid: %s, %s, %s, %s, %s, %s)",
			in.Status,
			store.SecurityFindingStatusOpen, store.SecurityFindingStatusTriaged,
			store.SecurityFindingStatusConfirmed, store.SecurityFindingStatusFalsePositive,
			store.SecurityFindingStatusFixed, store.SecurityFindingStatusAcceptedRisk), IsError: true}, nil
	}
	note := strings.TrimSpace(in.Note)
	if note == "" {
		return Result{Content: "note is required: explain why the status changed", IsError: true}, nil
	}
	rec, err := t.state.resolveFinding(ctx, strings.TrimSpace(in.ID), strings.TrimSpace(in.Fingerprint))
	if err != nil {
		return Result{Content: err.Error(), IsError: true}, nil
	}
	if err := t.state.setFindingStatus(ctx, rec.ID, status, note); err != nil {
		if errors.Is(err, store.ErrSecurityFindingNotFound) {
			return Result{Content: fmt.Sprintf("no finding with id %s in this scan (use list_security_findings to see recorded findings)", rec.ID), IsError: true}, nil
		}
		return Result{Content: fmt.Sprintf("failed to update finding: %v", err), IsError: true}, nil
	}
	return Result{Content: fmt.Sprintf("Finding %s status set to %s.", rec.ID, status)}, nil
}

// --- submit_security_scan_report ---

type submitSecurityScanReportTool struct {
	state *securityScanState
}

type submitSecurityScanReportInput struct {
	Summary     string `json:"summary"`
	RankerRules string `json:"ranker_rules"`
	MinSeverity string `json:"min_severity"`
}

func (t *submitSecurityScanReportTool) Name() string { return "submit_security_scan_report" }

func (t *submitSecurityScanReportTool) Description() string {
	return "Finalize this security scan: deduplicate and rank all recorded findings, render " +
		"the markdown report and SARIF output as session artifacts, and mark the scan " +
		"completed with a severity breakdown. The scan's configured minimum severity and " +
		"dedupe policy are always enforced. Call this exactly once, after every finding " +
		"has been reported and triaged; calling it again returns the existing result " +
		"unless the findings changed. Only writes platform scan state (no workspace, " +
		"repository, or network mutation), so it is available on read-only scan runs."
}

func (t *submitSecurityScanReportTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"summary": {"type": "string", "description": "One-paragraph executive summary of the scan outcome"},
			"ranker_rules": {"type": "string", "description": "Optional ranker rules text (severity-floor:, severity-ceiling:, exclude:, min-severity:, weight: directives plus prose)"},
			"min_severity": {"type": "string", "enum": ["critical", "high", "medium", "low", "info"], "description": "Drop findings below this severity from the report; the scan's configured minimum severity still applies when stricter"}
		},
		"required": ["summary"]
	}`)
}

func (t *submitSecurityScanReportTool) IsReadOnly() bool                      { return true }
func (t *submitSecurityScanReportTool) IsEnabled(_ *agentsdk.RunContext) bool { return true }
func (t *submitSecurityScanReportTool) NeedsApproval() bool                   { return false }
func (t *submitSecurityScanReportTool) TimeoutSeconds() int                   { return 0 }

// securityCountsEqual reports whether two summary maps hold the same counts,
// treating missing keys as zero.
func securityCountsEqual(a, b map[string]int32) bool {
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	for k, v := range b {
		if a[k] != v {
			return false
		}
	}
	return true
}

func securitySeverityBreakdown(b *strings.Builder, counts map[string]int32) {
	b.WriteString("Severity breakdown:\n")
	for _, sev := range security.Severities {
		fmt.Fprintf(b, "  %-8s %d\n", sev, counts[sev])
	}
	fmt.Fprintf(b, "  total    %d\n", counts["total"])
}

func (t *submitSecurityScanReportTool) Execute(ctx context.Context, input json.RawMessage, _ string) (Result, error) {
	var in submitSecurityScanReportInput
	if err := json.Unmarshal(input, &in); err != nil {
		return Result{Content: fmt.Sprintf("invalid input: %v", err), IsError: true}, nil
	}
	summary := strings.TrimSpace(in.Summary)
	if summary == "" {
		return Result{Content: "summary is required", IsError: true}, nil
	}

	scanCtx := t.state.scanCtx
	records, err := t.state.listFindings(ctx, store.SecurityFindingFilter{
		Namespace:         scanCtx.Namespace,
		RunName:           scanCtx.RunName,
		IncludeDuplicates: true,
		Limit:             1000,
	})
	if err != nil {
		return Result{Content: fmt.Sprintf("failed to load findings: %v", err), IsError: true}, nil
	}
	findings := make([]security.Finding, 0, len(records))
	for _, rec := range records {
		findings = append(findings, securityFindingFromRecord(rec))
	}

	// Scan policy from the SecurityScan CRD (via run annotations) always
	// applies: the dedupe threshold comes from the scan, and the effective
	// minimum severity is the stricter of the scan's and the model's.
	rules := security.ParseRankRules(in.RankerRules)
	minSource := "ranker rules"
	if modelMin := strings.ToLower(strings.TrimSpace(in.MinSeverity)); modelMin != "" {
		rules.MinSeverity = modelMin
		minSource = "tool input"
	}
	if scanCtx.MinSeverity != "" && security.SeverityRank(scanCtx.MinSeverity) > security.SeverityRank(rules.MinSeverity) {
		rules.MinSeverity = scanCtx.MinSeverity
		minSource = "scan policy"
	}

	var canonical []security.Finding
	var clusterCount int
	var dedupeDesc string
	switch {
	case scanCtx.DedupePermille == 0:
		canonical = findings
		clusterCount = len(findings)
		dedupeDesc = "dedupe disabled by scan policy"
	case scanCtx.DedupePermille > 0:
		threshold := float64(scanCtx.DedupePermille) / 1000
		clusters := security.Dedupe(findings, threshold)
		for _, c := range clusters {
			canonical = append(canonical, c.Canonical)
		}
		clusterCount = len(clusters)
		dedupeDesc = fmt.Sprintf("dedupe threshold %.3f from scan policy", threshold)
	default:
		clusters := security.Dedupe(findings, 0)
		for _, c := range clusters {
			canonical = append(canonical, c.Canonical)
		}
		clusterCount = len(clusters)
		dedupeDesc = "default dedupe threshold"
	}
	minDesc := "no minimum severity"
	if rules.MinSeverity != "" {
		minDesc = fmt.Sprintf("minimum severity %s from %s", rules.MinSeverity, minSource)
	}
	policyNote := fmt.Sprintf("Policy applied: %s; %s.", minDesc, dedupeDesc)

	ranked := security.Rank(canonical, rules)

	now := time.Now().UTC()
	reportInput := security.ReportInput{
		ScanName:    scanCtx.ScanName,
		Namespace:   scanCtx.Namespace,
		Repository:  scanCtx.Repository,
		Revision:    scanCtx.Revision,
		Summary:     summary,
		CompletedAt: now,
		Ranked:      ranked,
	}

	// Second submit with unchanged findings: keep the existing completed
	// scan record (and its completed_at) instead of silently re-stamping it.
	var priorScan *store.SecurityScanRecord
	var counts map[string]int32
	if t.state.findingStore != nil {
		if _, err := t.state.ensureScan(ctx); err != nil {
			return Result{Content: fmt.Sprintf("failed to open the scan record: %v", err), IsError: true}, nil
		}
		priorScan, err = t.state.findingStore.GetSecurityScan(ctx, scanCtx.Namespace, scanCtx.RunName)
		if err != nil {
			return Result{Content: fmt.Sprintf("failed to load scan record: %v", err), IsError: true}, nil
		}
		counts, err = t.state.findingStore.SummarizeSecurityFindings(ctx, scanCtx.Namespace, scanCtx.ScanName, scanCtx.RunName)
		if err != nil {
			return Result{Content: fmt.Sprintf("failed to summarize findings: %v", err), IsError: true}, nil
		}
		if priorScan != nil && priorScan.Status == "completed" && securityCountsEqual(counts, priorScan.Counts) {
			var b strings.Builder
			fmt.Fprintf(&b, "Security scan %q was already finalized and the findings are unchanged; keeping the existing report and completion time.\n", scanCtx.ScanName)
			fmt.Fprintf(&b, "Existing summary: %s\n", priorScan.Summary)
			securitySeverityBreakdown(&b, priorScan.Counts)
			b.WriteString(policyNote)
			return Result{Content: b.String()}, nil
		}
		if priorScan != nil && priorScan.StartedAt != nil {
			reportInput.StartedAt = *priorScan.StartedAt
		}
	} else {
		t.state.mu.Lock()
		counts = t.state.summarizeMemLocked()
		alreadyDone := t.state.memCompleted && securityCountsEqual(counts, t.state.memCounts)
		priorSummary := t.state.memSummary
		t.state.mu.Unlock()
		if alreadyDone {
			var b strings.Builder
			fmt.Fprintf(&b, "Security scan %q was already finalized and the findings are unchanged; keeping the existing report and completion time.\n", scanCtx.ScanName)
			fmt.Fprintf(&b, "Existing summary: %s\n", priorSummary)
			securitySeverityBreakdown(&b, counts)
			b.WriteString(policyNote)
			return Result{Content: b.String()}, nil
		}
	}

	markdown := security.RenderMarkdown(reportInput)
	sarif, err := security.RenderSARIF(reportInput)
	if err != nil {
		return Result{Content: fmt.Sprintf("failed to render SARIF: %v", err), IsError: true}, nil
	}

	var artifactNote string
	if t.state.stateStore != nil {
		if _, err := t.state.stateStore.UpsertArtifact(ctx, scanCtx.SessionID, SecurityReportArtifactKind, markdown, "", "", nil); err != nil {
			return Result{Content: fmt.Sprintf("failed to save markdown report artifact: %v", err), IsError: true}, nil
		}
		if _, err := t.state.stateStore.UpsertArtifact(ctx, scanCtx.SessionID, SecuritySARIFArtifactKind, string(sarif), "", "", nil); err != nil {
			return Result{Content: fmt.Sprintf("failed to save SARIF artifact: %v", err), IsError: true}, nil
		}
		artifactNote = fmt.Sprintf("Report artifacts saved (%s, %s).", SecurityReportArtifactKind, SecuritySARIFArtifactKind)
	} else {
		artifactNote = "No artifact store available; report artifacts were not persisted."
	}

	if t.state.findingStore != nil {
		scan := priorScan
		if scan == nil {
			scan = &store.SecurityScanRecord{
				Namespace:  scanCtx.Namespace,
				ScanName:   scanCtx.ScanName,
				RunName:    scanCtx.RunName,
				SessionID:  t.state.sessionIDPtr(),
				Repository: scanCtx.Repository,
				Revision:   scanCtx.Revision,
			}
		}
		scan.Status = "completed"
		scan.Summary = summary
		scan.Counts = counts
		scan.CompletedAt = &now
		if _, err := t.state.findingStore.UpsertSecurityScan(ctx, scan); err != nil {
			return Result{Content: fmt.Sprintf("failed to update scan record: %v", err), IsError: true}, nil
		}
	} else {
		t.state.mu.Lock()
		t.state.memCompleted = true
		t.state.memSummary = summary
		t.state.memCounts = counts
		t.state.mu.Unlock()
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Security scan %q finalized: %d finding(s) recorded, %d after dedupe, %d in the report.\n",
		scanCtx.ScanName, len(findings), clusterCount, len(ranked))
	b.WriteString(policyNote + "\n")
	securitySeverityBreakdown(&b, counts)
	b.WriteString(artifactNote)
	if t.state.findingStore == nil {
		b.WriteString("\nIn-memory mode (no Postgres): findings, counts, and status events are scoped to this process and not persisted across runs; counts cover stored (fingerprint-merged) non-duplicate findings, matching the Postgres counting.")
	}
	return Result{Content: b.String()}, nil
}
