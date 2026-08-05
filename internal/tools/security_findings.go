package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/gratefulagents/sdk/pkg/agentsdk"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/security"
	"github.com/gratefulagents/gratefulagents/internal/store"
)

// AgentRun annotations set by the SecurityScan controller on scan runs.
const (
	SecurityScanNameAnnotation       = "security.gratefulagents.dev/scan-name"
	SecurityScanRepositoryAnnotation = "security.gratefulagents.dev/repository"
	SecurityScanRevisionAnnotation   = "security.gratefulagents.dev/revision"
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
	SessionID  uuid.UUID
}

// SecurityScanContextFromRun extracts scan context from AgentRun annotations
// set by the SecurityScan controller (security.gratefulagents.dev/scan-name,
// .../repository, .../revision). It returns (ctx, true) only when the
// scan-name annotation is present, i.e. the run is a security scan run.
func SecurityScanContextFromRun(run *platformv1alpha1.AgentRun, namespace, runName string, sessionID uuid.UUID) (SecurityScanContext, bool) {
	if run == nil {
		return SecurityScanContext{}, false
	}
	scanName := strings.TrimSpace(run.Annotations[SecurityScanNameAnnotation])
	if scanName == "" {
		return SecurityScanContext{}, false
	}
	return SecurityScanContext{
		ScanName:   scanName,
		Namespace:  namespace,
		RunName:    runName,
		Repository: strings.TrimSpace(run.Annotations[SecurityScanRepositoryAnnotation]),
		Revision:   strings.TrimSpace(run.Annotations[SecurityScanRevisionAnnotation]),
		SessionID:  sessionID,
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

	mu     sync.Mutex
	mem    []*store.SecurityFindingRecord
	scanID uuid.UUID
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
		if f.Severity != "" && rec.Severity != f.Severity {
			continue
		}
		if f.Category != "" && rec.Category != f.Category {
			continue
		}
		if f.Status != "" && rec.Status != f.Status {
			continue
		}
		if f.Search != "" {
			needle := strings.ToLower(f.Search)
			if !strings.Contains(strings.ToLower(rec.FilePath), needle) &&
				!strings.Contains(strings.ToLower(rec.Title), needle) {
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
		return out[i].Title < out[j].Title
	})
	if int32(len(out)) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *securityScanState) setFindingStatus(ctx context.Context, id uuid.UUID, status, actor, note string) error {
	if s.findingStore != nil {
		return s.findingStore.SetSecurityFindingStatus(ctx, id, status, actor, note)
	}
	if !store.ValidSecurityFindingStatus(status) {
		return fmt.Errorf("invalid status %q", status)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, rec := range s.mem {
		if rec.ID == id {
			rec.Status = status
			return nil
		}
	}
	return fmt.Errorf("finding %s not found", id)
}

// resolveFindingID resolves an update target from an explicit id or a
// fingerprint returned by report_security_finding / list_security_findings.
func (s *securityScanState) resolveFindingID(ctx context.Context, id, fingerprint string) (uuid.UUID, error) {
	if id != "" {
		parsed, err := uuid.Parse(id)
		if err != nil {
			return uuid.Nil, fmt.Errorf("invalid finding id %q: %v", id, err)
		}
		return parsed, nil
	}
	if fingerprint == "" {
		return uuid.Nil, fmt.Errorf("either id or fingerprint is required")
	}
	records, err := s.listFindings(ctx, store.SecurityFindingFilter{
		Namespace:         s.scanCtx.Namespace,
		RunName:           s.scanCtx.RunName,
		IncludeDuplicates: true,
		Limit:             1000,
	})
	if err != nil {
		return uuid.Nil, fmt.Errorf("failed to look up finding: %v", err)
	}
	for _, rec := range records {
		if rec.Fingerprint == fingerprint {
			return rec.ID, nil
		}
	}
	return uuid.Nil, fmt.Errorf("no finding with fingerprint %q in this scan (use list_security_findings to see recorded findings)", fingerprint)
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
		"The finding is normalized, validated, stamped with this scan's repository/revision, " +
		"and deduplicated by fingerprint: reporting the same issue twice merges into the " +
		"existing finding instead of creating a duplicate. Returns the finding fingerprint. " +
		"This tool only records platform scan state — it never mutates the workspace, the " +
		"repository, or the network — so it is safe and available on read-only scan runs."
}

func (t *reportSecurityFindingTool) InputSchema() json.RawMessage {
	// Flat option: the tool input is the finding object itself, so the model
	// does not need a {"finding": {...}} wrapper.
	return json.RawMessage(security.FindingJSONSchema)
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
	if finding.Repository == "" {
		finding.Repository = t.state.scanCtx.Repository
	}
	if finding.Revision == "" {
		finding.Revision = t.state.scanCtx.Revision
	}
	if finding.SourceAgent == "" {
		finding.SourceAgent = t.state.scanCtx.RunName
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
			"search": {"type": "string", "description": "Substring match on file path or title"},
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
	Actor       string `json:"actor"`
}

func (t *updateSecurityFindingTool) Name() string { return "update_security_finding" }

func (t *updateSecurityFindingTool) Description() string {
	return "Set the status of a recorded security finding with an audit note: confirmed " +
		"(you built a PoC or proved exploitability), false_positive (you disproved it), " +
		"triaged, fixed, accepted_risk, or open. Identify the finding by the fingerprint " +
		"returned from report_security_finding / list_security_findings (or its id). " +
		"Only updates platform scan state; safe on read-only scan runs."
}

func (t *updateSecurityFindingTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"fingerprint": {"type": "string", "description": "Fingerprint of the finding to update"},
			"id": {"type": "string", "description": "Finding UUID, as an alternative to fingerprint"},
			"status": {"type": "string", "enum": ["open", "triaged", "confirmed", "false_positive", "fixed", "accepted_risk"], "description": "New status"},
			"note": {"type": "string", "description": "Why the status changed, e.g. the PoC that confirmed it or the reasoning that disproved it"},
			"actor": {"type": "string", "description": "Who/what changed the status; defaults to this run's name"}
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
	actor := strings.TrimSpace(in.Actor)
	if actor == "" {
		actor = t.state.scanCtx.RunName
	}
	id, err := t.state.resolveFindingID(ctx, strings.TrimSpace(in.ID), strings.TrimSpace(in.Fingerprint))
	if err != nil {
		return Result{Content: err.Error(), IsError: true}, nil
	}
	if err := t.state.setFindingStatus(ctx, id, status, actor, note); err != nil {
		return Result{Content: fmt.Sprintf("failed to update finding: %v", err), IsError: true}, nil
	}
	return Result{Content: fmt.Sprintf("Finding %s status set to %s.", id, status)}, nil
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
		"completed with a severity breakdown. Call this exactly once, after every finding " +
		"has been reported and triaged. Only writes platform scan state (no workspace, " +
		"repository, or network mutation), so it is available on read-only scan runs."
}

func (t *submitSecurityScanReportTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"summary": {"type": "string", "description": "One-paragraph executive summary of the scan outcome"},
			"ranker_rules": {"type": "string", "description": "Optional ranker rules text (severity-floor:, severity-ceiling:, exclude:, min-severity:, weight: directives plus prose)"},
			"min_severity": {"type": "string", "enum": ["critical", "high", "medium", "low", "info"], "description": "Drop findings below this severity from the report"}
		},
		"required": ["summary"]
	}`)
}

func (t *submitSecurityScanReportTool) IsReadOnly() bool                      { return true }
func (t *submitSecurityScanReportTool) IsEnabled(_ *agentsdk.RunContext) bool { return true }
func (t *submitSecurityScanReportTool) NeedsApproval() bool                   { return false }
func (t *submitSecurityScanReportTool) TimeoutSeconds() int                   { return 0 }

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

	clusters := security.Dedupe(findings, 0)
	canonical := make([]security.Finding, 0, len(clusters))
	for _, c := range clusters {
		canonical = append(canonical, c.Canonical)
	}
	rules := security.ParseRankRules(in.RankerRules)
	if min := strings.ToLower(strings.TrimSpace(in.MinSeverity)); min != "" {
		rules.MinSeverity = min
	}
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
	var priorScan *store.SecurityScanRecord
	if t.state.findingStore != nil {
		if _, err := t.state.ensureScan(ctx); err != nil {
			return Result{Content: fmt.Sprintf("failed to open the scan record: %v", err), IsError: true}, nil
		}
		priorScan, err = t.state.findingStore.GetSecurityScan(ctx, scanCtx.Namespace, scanCtx.RunName)
		if err != nil {
			return Result{Content: fmt.Sprintf("failed to load scan record: %v", err), IsError: true}, nil
		}
		if priorScan != nil && priorScan.StartedAt != nil {
			reportInput.StartedAt = *priorScan.StartedAt
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

	var counts map[string]int32
	if t.state.findingStore != nil {
		counts, err = t.state.findingStore.SummarizeSecurityFindings(ctx, scanCtx.Namespace, scanCtx.ScanName, scanCtx.RunName)
		if err != nil {
			return Result{Content: fmt.Sprintf("failed to summarize findings: %v", err), IsError: true}, nil
		}
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
		counts = make(map[string]int32, len(security.Severities)+1)
		for sev, n := range security.Summarize(findings) {
			counts[sev] = int32(n)
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Security scan %q finalized: %d finding(s) recorded, %d after dedupe, %d in the report.\n",
		scanCtx.ScanName, len(findings), len(clusters), len(ranked))
	b.WriteString("Severity breakdown:\n")
	for _, sev := range security.Severities {
		fmt.Fprintf(&b, "  %-8s %d\n", sev, counts[sev])
	}
	fmt.Fprintf(&b, "  total    %d\n", counts["total"])
	b.WriteString(artifactNote)
	return Result{Content: b.String()}, nil
}
