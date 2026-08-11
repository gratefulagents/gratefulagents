package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
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
	// The deterministic scan engine gives every task run its own AgentRun,
	// so these identify the execution a run belongs to and the task it executes.
	SecurityScanExecutionIDAnnotation       = triggersv1alpha1.SecurityScanExecutionIDAnnotation
	SecurityScanRecordNameAnnotation        = triggersv1alpha1.SecurityScanRecordNameAnnotation
	SecurityScanTaskNameAnnotation          = triggersv1alpha1.SecurityScanTaskNameAnnotation
	SecurityScanPostScriptAnnotation        = triggersv1alpha1.SecurityScanPostScriptAnnotation
	SecurityScanPostScriptFindingAnnotation = triggersv1alpha1.SecurityScanPostScriptFindingAnnotation
)

// Session artifact kinds written by submit_security_scan_report, aliased from
// the security package so the dashboard's report reader cannot drift.
const (
	SecurityReportArtifactKind = security.ReportArtifactKind
	SecuritySARIFArtifactKind  = security.SARIFArtifactKind
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
	// ExecutionID groups every run of one deterministic execution and
	// TaskName names the task this run executes; both are empty for
	// coordinator-mode (single-run) and legacy scans. Findings aggregate
	// per execution so a sink task and the final report can see what
	// sibling task runs persisted.
	ExecutionID string
	// RecordRunName, when set, is the run-name key of the persisted scan
	// record this run reports into. Every task run of one deterministic
	// execution carries the same value so the execution surfaces as ONE
	// scans-list row; empty (coordinator and legacy runs) falls back to the
	// run's own name.
	RecordRunName         string
	TaskName              string
	PostScripts           []string
	PostScriptFingerprint string
	SessionID             uuid.UUID
}

// RecordKey is the run-name key of the security_scans row this run reports
// into: the controller-stamped shared execution record when present,
// otherwise the run's own name. Every record read/write in these tools must
// use it so a deterministic execution stays ONE scans-list row.
func (c SecurityScanContext) RecordKey() string {
	if c.RecordRunName != "" {
		return c.RecordRunName
	}
	return c.RunName
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
		ScanName:              scanName,
		Namespace:             namespace,
		RunName:               runName,
		Repository:            strings.TrimSpace(run.Annotations[SecurityScanRepositoryAnnotation]),
		Revision:              strings.TrimSpace(run.Annotations[SecurityScanRevisionAnnotation]),
		MinSeverity:           minSeverity,
		DedupePermille:        dedupePermille,
		ExecutionID:           strings.TrimSpace(run.Annotations[SecurityScanExecutionIDAnnotation]),
		RecordRunName:         strings.TrimSpace(run.Annotations[SecurityScanRecordNameAnnotation]),
		TaskName:              strings.TrimSpace(run.Annotations[SecurityScanTaskNameAnnotation]),
		PostScripts:           splitTrimmedNonEmpty(run.Annotations[SecurityScanPostScriptAnnotation]),
		PostScriptFingerprint: strings.TrimSpace(run.Annotations[SecurityScanPostScriptFindingAnnotation]),
		SessionID:             sessionID,
	}, true
}

func splitTrimmedNonEmpty(value string) []string {
	var out []string
	for _, item := range strings.Split(value, ",") {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}

// RegisterSecurityScanTools registers the security finding tools for a scan
// run. findingStore may be nil (no Postgres); the tools then keep findings in
// an in-memory buffer scoped to this process so the scan still works, minus
// cross-run persistence.
// It returns the shared scan state so callers can register additional tools
// (run_security_tool) that must ingest through the very same pipeline.
func RegisterSecurityScanTools(registry *Registry, findingStore store.SecurityFindingStore, stateStore store.StateStore, scanCtx SecurityScanContext) *securityScanState {
	if registry == nil {
		return nil
	}
	state := &securityScanState{
		findingStore: findingStore,
		stateStore:   stateStore,
		scanCtx:      scanCtx,
	}
	registry.Register(&reportSecurityFindingTool{state: state})
	registry.Register(&listSecurityFindingsTool{state: state})
	registry.Register(&updateSecurityFindingTool{state: state})
	registry.Register(&ingestScannerResultsTool{state: state})
	registry.Register(&submitSecurityScanReportTool{state: state})
	return state
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
	if existing, err := s.findingStore.GetSecurityScan(ctx, s.scanCtx.Namespace, s.scanCtx.RecordKey()); err != nil {
		return uuid.Nil, err
	} else if existing != nil {
		s.scanID = existing.ID
		return s.scanID, nil
	}
	started := time.Now().UTC()
	created, err := s.findingStore.UpsertSecurityScan(ctx, &store.SecurityScanRecord{
		Namespace:  s.scanCtx.Namespace,
		ScanName:   s.scanCtx.ScanName,
		RunName:    s.scanCtx.RecordKey(),
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

// scopeFilter is the single definition of "the findings this run may see".
// Findings are aggregated per deterministic EXECUTION, not per run: the
// engine gives every task (and every fan-out instance) its own AgentRun, so
// run-scoped filtering would hide from the terminal sink task and the final
// report everything its sibling runs persisted. Scan-name-only aggregation is
// deliberately never used: it would fold historical and resumed executions of
// the same SecurityScan into one report. When no execution id is stamped
// (coordinator-mode single-run scans, and runs predating the annotation) the
// run itself is the whole execution, so run scoping is both correct and the
// safe fallback.
func (s *securityScanState) scopeFilter() store.SecurityFindingFilter {
	f := store.SecurityFindingFilter{
		Namespace: s.scanCtx.Namespace,
		ScanName:  s.scanCtx.ScanName,
	}
	if s.scanCtx.ExecutionID != "" {
		f.ExecutionID = s.scanCtx.ExecutionID
	} else {
		f.RunName = s.scanCtx.RunName
	}
	return f
}

func (s *securityScanState) scopeSummary(includeSuppressed bool) store.SecurityFindingSummaryScope {
	f := s.scopeFilter()
	return store.SecurityFindingSummaryScope{
		Namespace:         f.Namespace,
		ScanName:          f.ScanName,
		RunName:           f.RunName,
		ExecutionID:       f.ExecutionID,
		IncludeSuppressed: includeSuppressed,
	}
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
		// CorrelatedFingerprints is deliberately preserved: correlations
		// change only via recordCorrelation, mirroring the Postgres merge.
		existing.SourceKind = rec.SourceKind
		existing.Tool = rec.Tool
		existing.ToolVersion = rec.ToolVersion
		existing.RuleID = rec.RuleID
		// Attribution mirrors the Postgres merge: a reobservation moves the
		// finding into the reporting run's execution (it belongs in THAT
		// execution's report), while the task that first reported it keeps
		// the attribution as long as the execution is unchanged, so a
		// re-report does not change the task that first reported it.
		if rec.ExecutionID != "" {
			if existing.ExecutionID != rec.ExecutionID {
				existing.TaskName = rec.TaskName
			} else if existing.TaskName == "" {
				existing.TaskName = rec.TaskName
			}
			existing.ExecutionID = rec.ExecutionID
		} else if existing.TaskName == "" {
			existing.TaskName = rec.TaskName
		}
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

// memFindingMatches applies a SecurityFindingFilter to an in-memory record,
// mirroring the SQL filtering in the Postgres store.
func memFindingMatches(rec *store.SecurityFindingRecord, f store.SecurityFindingFilter) bool {
	if f.Namespace != "" && rec.Namespace != f.Namespace {
		return false
	}
	if f.ScanName != "" && rec.ScanName != f.ScanName {
		return false
	}
	if f.RunName != "" && rec.RunName != f.RunName {
		return false
	}
	if f.ExecutionID != "" && rec.ExecutionID != f.ExecutionID {
		return false
	}
	if f.TaskName != "" && rec.TaskName != f.TaskName {
		return false
	}
	if f.Repository != "" && rec.Repository != f.Repository {
		return false
	}
	if f.Severity != "" && rec.Severity != f.Severity {
		return false
	}
	if f.Category != "" && rec.Category != f.Category {
		return false
	}
	if f.Status != "" && rec.Status != f.Status {
		return false
	}
	if f.MinScore > 0 && rec.Score < f.MinScore {
		return false
	}
	if !f.IncludeDuplicates && rec.DuplicateOf != nil {
		return false
	}
	switch f.Suppressed {
	case store.SecuritySuppressedInclude:
	case store.SecuritySuppressedOnly:
		if rec.SuppressedBy == "" {
			return false
		}
	default:
		if rec.SuppressedBy != "" {
			return false
		}
	}
	if f.Search != "" {
		needle := strings.ToLower(f.Search)
		if !strings.Contains(strings.ToLower(rec.Title), needle) &&
			!strings.Contains(strings.ToLower(rec.Description), needle) &&
			!strings.Contains(strings.ToLower(rec.FilePath), needle) {
			return false
		}
	}
	return true
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
		if memFindingMatches(rec, f) {
			out = append(out, *rec)
		}
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
	offset := max(f.Offset, 0)
	if int(offset) >= len(out) {
		return nil, nil
	}
	out = out[offset:]
	if int32(len(out)) > limit {
		out = out[:limit]
	}
	return out, nil
}

const (
	// securityFindingPageSize is the store's hard cap on rows per
	// ListSecurityFindings call, so it is also the largest useful page.
	securityFindingPageSize = 1000
	// securityFindingScopeMax bounds a whole-execution load: an execution's
	// findings are the sum of every sibling run's, so one page no longer
	// covers them, but an unbounded loop would let a runaway scan pull an
	// arbitrary number of rows into this run's memory.
	securityFindingScopeMax = 20000
)

// listAllFindings pages listFindings over the whole scope until the result
// set is exhausted. It reports whether securityFindingScopeMax truncated the
// result; callers that render findings must disclose that rather than drop
// rows silently. Results stay ordered by score DESC, so a truncated load
// keeps the highest-scoring findings.
func (s *securityScanState) listAllFindings(ctx context.Context, f store.SecurityFindingFilter) ([]store.SecurityFindingRecord, bool, error) {
	f.Limit = securityFindingPageSize
	f.Offset = 0
	var out []store.SecurityFindingRecord
	for {
		page, err := s.listFindings(ctx, f)
		if err != nil {
			return nil, false, err
		}
		out = append(out, page...)
		if len(out) >= securityFindingScopeMax {
			return out[:securityFindingScopeMax], true, nil
		}
		if len(page) < securityFindingPageSize {
			return out, false, nil
		}
		f.Offset += int32(len(page))
	}
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
		return s.findingStore.SetSecurityFindingStatus(ctx, s.scanCtx.Namespace, id, status, actor, note, nil)
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
// total, open, open_<severity>, source_<kind>, and correlated keys. Caller
// must hold s.mu.
func (s *securityScanState) summarizeMemLocked() map[string]int32 {
	counts := map[string]int32{
		"total": 0, "open": 0,
		"open_critical": 0, "open_high": 0, "open_medium": 0, "open_low": 0, "open_info": 0,
		"source_agent": 0, "source_scanner": 0, "correlated": 0,
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
		if rec.SourceKind == security.SourceKindScanner {
			counts["source_scanner"]++
		} else {
			counts["source_agent"]++
		}
		if len(rec.CorrelatedFingerprints) > 0 {
			counts["correlated"]++
		}
	}
	return counts
}

// recordCorrelation records a two-way correlation between two findings of
// this scan identified by fingerprint, mirroring
// store.CorrelateSecurityFindings when no Postgres store is available.
func (s *securityScanState) recordCorrelation(ctx context.Context, repository, fpA, fpB, reason string) (bool, error) {
	if s.findingStore != nil {
		return s.findingStore.CorrelateSecurityFindings(ctx, s.scanCtx.Namespace, s.scanCtx.ScanName,
			repository, fpA, fpB, reason, s.scanCtx.RunName)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	find := func(fp string) *store.SecurityFindingRecord {
		for _, rec := range s.mem {
			if rec.Repository == repository && rec.Fingerprint == fp {
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
		detail, _ := json.Marshal(map[string]string{"correlated_fingerprint": other, "reason": reason})
		s.memEvents = append(s.memEvents, store.SecurityFindingEvent{
			FindingID: rec.ID,
			EventType: "correlated",
			Actor:     s.scanCtx.RunName,
			Note:      reason,
			Detail:    detail,
			CreatedAt: time.Now().UTC(),
		})
		changed = true
	}
	link(a, fpB)
	link(b, fpA)
	return changed, nil
}

// correlateScanFindings loads this scan's findings, computes agent↔scanner
// correlations, and records every new pair on both sides. It returns the
// number of pairs newly recorded.
func (s *securityScanState) correlateScanFindings(ctx context.Context) (int, error) {
	filter := s.scopeFilter()
	filter.Repository = s.scanCtx.Repository
	filter.IncludeDuplicates = true
	records, _, err := s.listAllFindings(ctx, filter)
	if err != nil {
		return 0, err
	}
	findings := make([]security.Finding, 0, len(records))
	for _, rec := range records {
		findings = append(findings, securityFindingFromRecord(rec))
	}
	recorded := 0
	for _, c := range security.Correlate(findings) {
		changed, err := s.recordCorrelation(ctx, s.scanCtx.Repository, c.AgentFingerprint, c.ScannerFingerprint, c.Reason)
		if err != nil {
			return recorded, err
		}
		if changed {
			recorded++
		}
	}
	return recorded, nil
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
		// Ownership is checked against namespace, scan, and — once the
		// controller stamps one — the execution, so a model-supplied id can
		// never re-triage findings of another namespace, scan, or an earlier
		// execution whose findings this run cannot even see. Run name is
		// deliberately not required: post-script and sibling runs of the same
		// scan legitimately triage findings they did not report.
		scope := s.scopeFilter()
		if rec.Namespace != scope.Namespace || rec.ScanName != scope.ScanName ||
			(scope.ExecutionID != "" && rec.ExecutionID != scope.ExecutionID) {
			return nil, fmt.Errorf("finding %s does not belong to security scan %q's current execution; this run can only update findings of its own execution", parsed, s.scanCtx.ScanName)
		}
		return rec, nil
	}
	if fingerprint == "" {
		return nil, fmt.Errorf("either id or fingerprint is required")
	}
	filter := s.scopeFilter()
	filter.IncludeDuplicates = true
	records, _, err := s.listAllFindings(ctx, filter)
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
		Namespace: scanCtx.Namespace,
		ScanName:  scanCtx.ScanName,
		RunName:   scanCtx.RunName,
		// Execution and task attribution makes findings aggregate per
		// execution; both are stamped from controller annotations, never from
		// the model.
		ExecutionID:  scanCtx.ExecutionID,
		TaskName:     scanCtx.TaskName,
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
		SourceKind:   f.SourceKind,
		Tool:         f.Tool,
		ToolVersion:  f.ToolVersion,
		RuleID:       f.RuleID,
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
	f.SourceKind = rec.SourceKind
	f.Tool = rec.Tool
	f.ToolVersion = rec.ToolVersion
	f.RuleID = rec.RuleID
	f.CorrelatedFingerprints = rec.CorrelatedFingerprints
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
	// Provenance is stamped, never model-supplied: scanner provenance (and
	// correlations) can only come from ingest_scanner_results, so an agent
	// finding cannot masquerade as a deterministic tool's output.
	finding.SourceKind = security.SourceKindAgent
	finding.Tool = ""
	finding.ToolVersion = ""
	finding.RuleID = ""
	finding.CorrelatedFingerprints = nil
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
	filter := t.state.scopeFilter()
	filter.Severity = strings.ToLower(strings.TrimSpace(in.Severity))
	filter.Category = strings.ToLower(strings.TrimSpace(in.Category))
	filter.Status = strings.ToLower(strings.TrimSpace(in.Status))
	filter.Search = strings.TrimSpace(in.Search)
	filter.Limit = in.Limit
	records, err := t.state.listFindings(ctx, filter)
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
	if t.state.scanCtx.PostScriptFingerprint != "" &&
		(rec.Status == store.SecurityFindingStatusFalsePositive || rec.Status == store.SecurityFindingStatusAcceptedRisk || rec.Status == store.SecurityFindingStatusFixed) && status != rec.Status {
		return Result{Content: fmt.Sprintf("terminal finding status %s is preserved during finding post-processing", rec.Status), IsError: true}, nil
	}
	if err := t.state.setFindingStatus(ctx, rec.ID, status, note); err != nil {
		if errors.Is(err, store.ErrSecurityFindingNotFound) {
			return Result{Content: fmt.Sprintf("no finding with id %s in this scan (use list_security_findings to see recorded findings)", rec.ID), IsError: true}, nil
		}
		return Result{Content: fmt.Sprintf("failed to update finding: %v", err), IsError: true}, nil
	}
	return Result{Content: fmt.Sprintf("Finding %s status set to %s.", rec.ID, status)}, nil
}

// --- ingest_scanner_results ---

type ingestScannerResultsTool struct {
	state *securityScanState
}

// Batch bounds for one ingest_scanner_results call: a hard record cap and a
// hard payload-size cap so a scanner dump cannot balloon a single tool call.
const (
	maxScannerBatchRecords = 500
	maxScannerBatchBytes   = 4 << 20 // 4 MiB
)

type ingestScannerResultsInput struct {
	Records []security.ScannerRecord `json:"records"`
}

// scannerFindingRaw builds the finding's raw JSON payload: the normalized
// finding plus the original scanner record preserved verbatim except for
// secret redaction. securityFindingFromRecord ignores the extra key.
type scannerFindingRaw struct {
	security.Finding
	ScannerRecord security.ScannerRecord `json:"scanner_record"`
}

func (t *ingestScannerResultsTool) Name() string { return "ingest_scanner_results" }

func (t *ingestScannerResultsTool) Description() string {
	return fmt.Sprintf("Ingest a batch of results from a deterministic scanner tool (semgrep, gosec, "+
		"trivy, ...) you ran in the workspace, normalized into the scanner record contract. "+
		"At most %d records (and %d bytes of input) per call; split larger outputs into "+
		"multiple calls. The whole batch is rejected with per-record errors when any record "+
		"is invalid. Accepted records are normalized, secret-redacted, persisted with "+
		"scanner provenance (tool, version, rule id), deduplicated by deterministic "+
		"fingerprint so re-running the same tool converges, and automatically correlated "+
		"with agent findings at the same location — correlation cross-references both "+
		"findings without merging them, so use it to prioritize validation rather than "+
		"trusting either source alone. The scanned repository and revision are stamped from "+
		"the scan configuration. Only records platform scan state; safe on read-only scan runs.",
		maxScannerBatchRecords, maxScannerBatchBytes)
}

func (t *ingestScannerResultsTool) InputSchema() json.RawMessage {
	return json.RawMessage(fmt.Sprintf(`{
		"type": "object",
		"properties": {
			"records": {
				"type": "array",
				"minItems": 1,
				"maxItems": %d,
				"description": "Scanner results normalized into the canonical scanner record contract",
				"items": {
					"type": "object",
					"additionalProperties": false,
					"required": ["tool", "rule_id", "message", "severity", "file_path"],
					"properties": {
						"tool": {"type": "string", "description": "Scanner name, e.g. gosec"},
						"tool_version": {"type": "string", "description": "Scanner version, e.g. 2.18.2"},
						"rule_id": {"type": "string", "description": "The tool's rule identifier, e.g. G401"},
						"rule_name": {"type": "string", "description": "Human-readable rule name"},
						"message": {"type": "string", "description": "The tool's finding message"},
						"severity": {"type": "string", "description": "The tool's severity (ERROR, WARNING, HIGH, moderate, ...); mapped onto critical/high/medium/low/info"},
						"category": {"type": "string", "description": "Optional platform category; derived from cwe when omitted"},
						"file_path": {"type": "string", "description": "Repository-relative path, forward slashes"},
						"start_line": {"type": "integer", "minimum": 0, "description": "First matched line (1-based, 0 = unknown)"},
						"end_line": {"type": "integer", "minimum": 0, "description": "Last matched line (1-based, 0 = unknown)"},
						"symbol": {"type": "string", "description": "Enclosing function/method/symbol when the tool reports one"},
						"cwe": {"type": "string", "description": "CWE identifier, e.g. CWE-798"},
						"references": {"type": "array", "items": {"type": "string"}, "description": "Rule documentation / advisory URLs"},
						"raw_evidence": {"type": "string", "description": "Verbatim matched snippet (secret-redacted before storage)"},
						"extra": {"type": "object", "additionalProperties": {"type": "string"}, "description": "Tool-specific metadata preserved in the raw payload"}
					}
				}
			}
		},
		"required": ["records"]
	}`, maxScannerBatchRecords))
}

func (t *ingestScannerResultsTool) IsReadOnly() bool                      { return true }
func (t *ingestScannerResultsTool) IsEnabled(_ *agentsdk.RunContext) bool { return true }
func (t *ingestScannerResultsTool) NeedsApproval() bool                   { return false }
func (t *ingestScannerResultsTool) TimeoutSeconds() int                   { return 0 }

func (t *ingestScannerResultsTool) Execute(ctx context.Context, input json.RawMessage, _ string) (Result, error) {
	if len(input) > maxScannerBatchBytes {
		return Result{Content: fmt.Sprintf("input is %d bytes, exceeding the %d byte batch limit; split the scanner output into smaller batches", len(input), maxScannerBatchBytes), IsError: true}, nil
	}
	var in ingestScannerResultsInput
	dec := json.NewDecoder(bytes.NewReader(input))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		return Result{Content: fmt.Sprintf("invalid input: %v (see the tool schema for the scanner record contract)", err), IsError: true}, nil
	}
	if len(in.Records) == 0 {
		return Result{Content: "records is required: supply at least one scanner record", IsError: true}, nil
	}
	if len(in.Records) > maxScannerBatchRecords {
		return Result{Content: fmt.Sprintf("%d records exceed the %d record batch limit; split the scanner output into smaller batches", len(in.Records), maxScannerBatchRecords), IsError: true}, nil
	}

	scanCtx := t.state.scanCtx
	findings := make([]security.Finding, len(in.Records))
	var problems []string
	for i, rec := range in.Records {
		// Repository and revision are stamped from the scan context (they
		// are part of the finding identity), exactly like agent findings.
		f, err := security.NormalizeScannerRecord(rec, scanCtx.Repository, scanCtx.Revision)
		if err != nil {
			problems = append(problems, fmt.Sprintf("records[%d]: %v", i, err))
			continue
		}
		f.SourceAgent = scanCtx.RunName
		findings[i] = f
	}
	if len(problems) > 0 {
		return Result{Content: fmt.Sprintf("batch rejected, no records ingested; fix these records and resubmit:\n%s", strings.Join(problems, "\n")), IsError: true}, nil
	}

	scanID, err := t.state.ensureScan(ctx)
	if err != nil {
		return Result{Content: fmt.Sprintf("failed to open the scan record: %v", err), IsError: true}, nil
	}
	created, merged, stoppedAt, err := ingestNormalizedScannerFindings(ctx, t.state, scanID, findings, in.Records)
	if err != nil {
		return Result{Content: fmt.Sprintf("failed to persist scanner finding (records[%d]): %v", stoppedAt, err), IsError: true}, nil
	}
	tools := map[string]bool{}
	for _, f := range findings {
		tools[f.Tool] = true
	}

	correlated, err := t.state.correlateScanFindings(ctx)
	if err != nil {
		return Result{Content: fmt.Sprintf("scanner findings ingested (%d new, %d merged) but correlation failed: %v", created, merged, err), IsError: true}, nil
	}

	toolNames := make([]string, 0, len(tools))
	for name := range tools {
		toolNames = append(toolNames, name)
	}
	sort.Strings(toolNames)
	var b strings.Builder
	fmt.Fprintf(&b, "Ingested %d scanner record(s) from %s: %d new finding(s), %d merged into existing findings by fingerprint.",
		len(in.Records), strings.Join(toolNames, ", "), created, merged)
	if correlated > 0 {
		fmt.Fprintf(&b, "\n%d new agent↔scanner correlation(s) recorded; use list_security_findings and validate correlated findings first.", correlated)
	}
	return Result{Content: b.String()}, nil
}

// ingestNormalizedScannerFindings persists normalized scanner findings,
// preserving each original record (minus secrets) in the raw payload. It
// stops at the first failure and returns the index it stopped at.
func ingestNormalizedScannerFindings(ctx context.Context, state *securityScanState, scanID uuid.UUID, findings []security.Finding, records []security.ScannerRecord) (created, merged, stoppedAt int, err error) {
	for i, f := range findings {
		rec := securityFindingRecord(f, state.scanCtx, state.sessionIDPtr())
		rec.ScanID = scanID
		if raw, marshalErr := json.Marshal(scannerFindingRaw{Finding: f, ScannerRecord: records[i].Redacted()}); marshalErr == nil {
			rec.Raw = raw
		}
		_, isNew, upsertErr := state.upsertFinding(ctx, rec)
		if upsertErr != nil {
			return created, merged, i, upsertErr
		}
		if isNew {
			created++
		} else {
			merged++
		}
	}
	return created, merged, len(findings), nil
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
		"dedupe policy are always enforced, and findings that are no longer actionable — " +
		"triaged as false positive, accepted risk, or fixed, collapsed as duplicates, or " +
		"suppressed by policy — are left out of the report and SARIF (their stored rows " +
		"are kept). Call this exactly " +
		"once, after every finding " +
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

// submitScanPolicy resolves the effective ranking rules and the human-readable
// policy note for a report submission. Scan policy from the SecurityScan CRD
// (via run annotations) always applies: the dedupe threshold comes from the
// scan, and the effective minimum severity is the stricter of the scan's and
// the model's.
func submitScanPolicy(in submitSecurityScanReportInput, scanCtx SecurityScanContext, dedupeDesc string) (security.RankRules, string) {
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
	minDesc := "no minimum severity"
	if rules.MinSeverity != "" {
		minDesc = fmt.Sprintf("minimum severity %s from %s", rules.MinSeverity, minSource)
	}
	return rules, fmt.Sprintf("Policy applied: %s; %s.", minDesc, dedupeDesc)
}

// Exclusion reasons reported in the submit policy note, in the order they
// are rendered.
var securityReportExclusionReasons = []string{
	store.SecurityFindingStatusFalsePositive,
	store.SecurityFindingStatusAcceptedRisk,
	store.SecurityFindingStatusFixed,
	"duplicate",
	"suppressed",
}

var securityReportExclusionLabels = map[string]string{
	store.SecurityFindingStatusFalsePositive: "%d finding(s) triaged as false positives",
	store.SecurityFindingStatusAcceptedRisk:  "%d finding(s) triaged as accepted risk",
	store.SecurityFindingStatusFixed:         "%d finding(s) triaged as fixed",
	"duplicate":                              "%d finding(s) collapsed as duplicates",
	"suppressed":                             "%d governed-suppressed finding(s)",
}

// securityReportExclusionReason returns why a stored finding must not reach
// the report, or "" when it is report-eligible.
func securityReportExclusionReason(rec store.SecurityFindingRecord) string {
	switch {
	case rec.DuplicateOf != nil:
		return "duplicate"
	case rec.SuppressedBy != "":
		return "suppressed"
	case rec.Status == store.SecurityFindingStatusFalsePositive,
		rec.Status == store.SecurityFindingStatusAcceptedRisk,
		rec.Status == store.SecurityFindingStatusFixed:
		return rec.Status
	}
	return ""
}

// securityExclusionNote renders the per-reason exclusion counts for the
// policy note, so the operator can tell a quiet report from a triaged one.
func securityExclusionNote(excluded map[string]int) string {
	var parts []string
	for _, reason := range securityReportExclusionReasons {
		if n := excluded[reason]; n > 0 {
			parts = append(parts, fmt.Sprintf(securityReportExclusionLabels[reason], n))
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, ", ") + " were excluded from the report"
}

// dedupeSubmittedFindings collapses findings per the scan's dedupe policy and
// returns the canonical findings, the cluster count, and a policy description.
func dedupeSubmittedFindings(findings []security.Finding, scanCtx SecurityScanContext) ([]security.Finding, int, string) {
	if scanCtx.DedupePermille == 0 {
		return findings, len(findings), "dedupe disabled by scan policy"
	}
	threshold := float64(0)
	desc := "default dedupe threshold"
	if scanCtx.DedupePermille > 0 {
		threshold = float64(scanCtx.DedupePermille) / 1000
		desc = fmt.Sprintf("dedupe threshold %.3f from scan policy", threshold)
	}
	clusters := security.Dedupe(findings, threshold)
	canonical := make([]security.Finding, 0, len(clusters))
	for _, c := range clusters {
		canonical = append(canonical, c.Canonical)
	}
	return canonical, len(clusters), desc
}

// alreadyFinalizedResult renders the response for a repeat submit whose
// findings are unchanged since the scan was completed.
func alreadyFinalizedResult(scanName, priorSummary, policyNote string, counts map[string]int32) Result {
	var b strings.Builder
	fmt.Fprintf(&b, "Security scan %q was already finalized and the findings are unchanged; keeping the existing report and completion time.\n", scanName)
	fmt.Fprintf(&b, "Existing summary: %s\n", priorSummary)
	securitySeverityBreakdown(&b, counts)
	b.WriteString(policyNote)
	return Result{Content: b.String()}
}

// loadPriorScanState fetches the prior scan record and current finding counts
// from the persistent store, returning a non-nil done Result when the scan was
// already finalized with unchanged findings.
func (t *submitSecurityScanReportTool) loadPriorScanState(ctx context.Context, policyNote string) (priorScan *store.SecurityScanRecord, counts map[string]int32, done *Result, err error) {
	scanCtx := t.state.scanCtx
	if _, err := t.state.ensureScan(ctx); err != nil {
		return nil, nil, nil, fmt.Errorf("failed to open the scan record: %v", err)
	}
	priorScan, err = t.state.findingStore.GetSecurityScan(ctx, scanCtx.Namespace, scanCtx.RecordKey())
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to load scan record: %v", err)
	}
	counts, err = t.state.findingStore.SummarizeSecurityFindingsScoped(ctx, t.state.scopeSummary(false))
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to summarize findings: %v", err)
	}
	if priorScan != nil && priorScan.Status == "completed" && securityCountsEqual(counts, priorScan.Counts) {
		res := alreadyFinalizedResult(scanCtx.ScanName, priorScan.Summary, policyNote, priorScan.Counts)
		return priorScan, counts, &res, nil
	}
	return priorScan, counts, nil, nil
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
	filter := t.state.scopeFilter()
	filter.IncludeDuplicates = true
	filter.Suppressed = store.SecuritySuppressedInclude
	records, truncated, err := t.state.listAllFindings(ctx, filter)
	if err != nil {
		return Result{Content: fmt.Sprintf("failed to load findings: %v", err), IsError: true}, nil
	}
	findings := make([]security.Finding, 0, len(records))
	excluded := map[string]int{}
	for _, rec := range records {
		// Report-ineligible findings are never deleted (the store keeps
		// every row and its audit trail), but they must not be correlated,
		// deduped, ranked, or rendered: a disproved, accepted, or
		// already-fixed finding, a row collapsed into another as a
		// duplicate, and a governed-suppressed row all describe risk the
		// operator has already decided about, so re-surfacing them in the
		// report would defeat triage and suppression policy alike.
		if reason := securityReportExclusionReason(rec); reason != "" {
			excluded[reason]++
			continue
		}
		findings = append(findings, securityFindingFromRecord(rec))
	}

	// Correlate agent and scanner findings one final time so anything
	// reported after the last ingest batch is cross-referenced (on both
	// stored rows and in the rendered report) before dedupe and ranking.
	if correlations := security.Correlate(findings); len(correlations) > 0 {
		for _, c := range correlations {
			if _, err := t.state.recordCorrelation(ctx, scanCtx.Repository, c.AgentFingerprint, c.ScannerFingerprint, c.Reason); err != nil {
				return Result{Content: fmt.Sprintf("failed to record finding correlation: %v", err), IsError: true}, nil
			}
		}
		security.ApplyCorrelations(findings, correlations)
	}

	canonical, clusterCount, dedupeDesc := dedupeSubmittedFindings(findings, scanCtx)
	if note := securityExclusionNote(excluded); note != "" {
		dedupeDesc = dedupeDesc + "; " + note
	}
	if truncated {
		dedupeDesc = dedupeDesc + fmt.Sprintf("; truncated to the %d highest-scoring findings of this execution — lower-scoring findings are stored but absent from this report and its SARIF artifact", securityFindingScopeMax)
	}
	rules, policyNote := submitScanPolicy(in, scanCtx, dedupeDesc)

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
	//
	// The persisted counts deliberately stay whole-scan (every stored,
	// non-duplicate row, including false positives) rather than mirroring
	// the filtered report: findings are marked, never erased, and the
	// counts carry both total and open_<severity> keys. Consumers that must
	// ignore triaged-away findings already read the open keys — the
	// controller's failOnSeverity evaluation and the GitHub check summary,
	// which lists only status=open findings — so filtering the report does
	// not change any number those surfaces show. Rewriting the stored totals
	// here would instead break the unchanged-findings comparison above,
	// which compares them against SummarizeSecurityFindings output.
	var priorScan *store.SecurityScanRecord
	var counts map[string]int32
	if t.state.findingStore != nil {
		var done *Result
		priorScan, counts, done, err = t.loadPriorScanState(ctx, policyNote)
		if err != nil {
			return Result{Content: err.Error(), IsError: true}, nil
		}
		if done != nil {
			return *done, nil
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
			return alreadyFinalizedResult(scanCtx.ScanName, priorSummary, policyNote, counts), nil
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
				RunName:    scanCtx.RecordKey(),
				SessionID:  t.state.sessionIDPtr(),
				Repository: scanCtx.Repository,
				Revision:   scanCtx.Revision,
			}
		}
		scan.Status = "completed"
		scan.Summary = summary
		scan.Counts = counts
		scan.CompletedAt = &now
		// The record's session locates the report/SARIF artifacts. On a
		// shared execution record the row was opened by an earlier sibling
		// task run, so point it at the submitting (sink) run's session —
		// that is where these artifacts were just stored.
		if sess := t.state.sessionIDPtr(); sess != nil {
			scan.SessionID = sess
		}
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
		scanCtx.ScanName, len(records), clusterCount, len(ranked))
	b.WriteString(policyNote + "\n")
	securitySeverityBreakdown(&b, counts)
	b.WriteString(artifactNote)
	if t.state.findingStore == nil {
		b.WriteString("\nIn-memory mode (no Postgres): findings, counts, and status events are scoped to this process and not persisted across runs; counts cover stored (fingerprint-merged) non-duplicate findings, matching the Postgres counting.")
	}
	return Result{Content: b.String()}, nil
}
