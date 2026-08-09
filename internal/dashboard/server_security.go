package dashboard

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"
	"sigs.k8s.io/controller-runtime/pkg/client"

	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/store"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
)

const securityFindingEventsLimit = 200

// securityStore returns the state store's security-finding capability, or a
// FailedPrecondition error when the configured store does not support it.
func (s *Server) securityStore() (store.SecurityFindingStore, error) {
	sec, ok := s.stateStore.(store.SecurityFindingStore)
	if !ok {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("security findings are not supported by the configured state store"))
	}
	return sec, nil
}

// securityScanVisibility computes the caller's visibility over persisted
// security scan data in a namespace. Scan and finding records carry
// ScanName == the SecurityScan CR name, so record visibility follows the
// CR's ownership: admins (and internal calls without an RPC actor) see
// everything, unowned scans are visible to every authenticated user, and a
// scan owned by someone else is hidden unless shared with the caller.
//
// visible reports whether records of one scan name may be shown (an empty
// scan name has no owning CR and is always visible). hidden lists the scan
// names the caller must not see, for pushing exclusion into namespace-wide
// store queries and aggregates; it is nil for admins. A store that cannot
// bulk-list ownership falls back to per-name access checks with a nil
// hidden list.
func (s *Server) securityScanVisibility(ctx context.Context, namespace string) (visible func(scanName string) bool, hidden []string, err error) {
	allowAll := func(string) bool { return true }
	actor, recorded := requestActorFromContextOK(ctx)
	if !recorded || actor.Role == "admin" || actor.Role == "owner" || s.stateStore == nil {
		return allowAll, nil, nil
	}
	bulk, ok := s.stateStore.(resourceOwnersByTypeStore)
	if !ok {
		perName := s.resourceVisibilityFilter(ctx, securityScanResourceType, false)
		return func(scanName string) bool {
			return scanName == "" || perName(namespace, scanName)
		}, nil, nil
	}
	owners, err := bulk.ListResourceOwnersByType(ctx, securityScanResourceType)
	if err != nil {
		return nil, nil, fmt.Errorf("listing security scan owners: %w", err)
	}
	shares, err := s.stateStore.ListSharedWithMe(ctx, actor.Subject, securityScanResourceType)
	if err != nil {
		return nil, nil, fmt.Errorf("listing security scan shares: %w", err)
	}
	shared := make(map[string]bool, len(shares))
	for _, sh := range shares {
		if sh.ResourceNamespace == namespace {
			shared[sh.ResourceID] = true
		}
	}
	hiddenSet := map[string]bool{}
	for _, o := range owners {
		if o.ResourceNamespace != namespace || o.ResourceID == "" ||
			o.OwnerID == "" || o.OwnerID == actor.Subject || shared[o.ResourceID] {
			continue
		}
		if !hiddenSet[o.ResourceID] {
			hiddenSet[o.ResourceID] = true
			hidden = append(hidden, o.ResourceID)
		}
	}
	return func(scanName string) bool { return !hiddenSet[scanName] }, hidden, nil
}

// securityScanRecordForRun resolves a run-scoped request to its persisted
// scan record and reports whether that scan is hidden from the caller. An
// unknown run is not hidden and returns no record, preserving compatibility
// with legacy findings that may exist without a scan row.
func securityScanRecordForRun(ctx context.Context, sec store.SecurityFindingStore, visible func(string) bool, namespace, runName string) (*store.SecurityScanRecord, bool, error) {
	if runName == "" {
		return nil, false, nil
	}
	rec, err := sec.GetSecurityScan(ctx, namespace, runName)
	if err != nil {
		return nil, false, fmt.Errorf("getting security scan: %w", err)
	}
	return rec, rec != nil && !visible(rec.ScanName), nil
}

// ListSecurityScans lists security scans in a namespace, newest first.
func (s *Server) ListSecurityScans(ctx context.Context, req *platform.ListSecurityScansRequest) (*platform.ListSecurityScansResponse, error) {
	sec, err := s.securityStore()
	if err != nil {
		return nil, err
	}
	namespace, err := s.authorizeRequestNamespace(ctx, req.GetNamespace(), nil)
	if err != nil {
		return nil, err
	}
	visible, hidden, err := s.securityScanVisibility(ctx, namespace)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	// Hidden scans are excluded inside the store query so the limit counts
	// only rows the caller may see; the predicate remains as a second layer
	// for stores that cannot push the exclusion down.
	scans, err := sec.ListSecurityScans(ctx, namespace, req.GetScanName(), req.GetLimit(), hidden)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("listing security scans: %w", err))
	}
	resp := &platform.ListSecurityScansResponse{}
	for i := range scans {
		if !visible(scans[i].ScanName) {
			continue
		}
		resp.Scans = append(resp.Scans, securityScanProto(&scans[i]))
	}
	return resp, nil
}

// GetSecurityScan returns one security scan by namespace and run name.
func (s *Server) GetSecurityScan(ctx context.Context, req *platform.GetSecurityScanRequest) (*platform.SecurityScan, error) {
	sec, err := s.securityStore()
	if err != nil {
		return nil, err
	}
	namespace, err := s.authorizeRequestNamespace(ctx, req.GetNamespace(), nil)
	if err != nil {
		return nil, err
	}
	scan, err := sec.GetSecurityScan(ctx, namespace, req.GetRunName())
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("getting security scan: %w", err))
	}
	if scan != nil {
		visible, _, verr := s.securityScanVisibility(ctx, namespace)
		if verr != nil {
			return nil, connect.NewError(connect.CodeInternal, verr)
		}
		// A hidden scan reports the same NotFound as a missing one so the
		// endpoint cannot be used to probe another user's scan runs.
		if !visible(scan.ScanName) {
			scan = nil
		}
	}
	if scan == nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("security scan %s/%s not found", namespace, req.GetRunName()))
	}
	return securityScanProto(scan), nil
}

// ListSecurityFindings lists deduplicated security findings matching the
// request filter.
func (s *Server) ListSecurityFindings(ctx context.Context, req *platform.ListSecurityFindingsRequest) (*platform.ListSecurityFindingsResponse, error) {
	sec, err := s.securityStore()
	if err != nil {
		return nil, err
	}
	namespace, err := s.authorizeRequestNamespace(ctx, req.GetNamespace(), nil)
	if err != nil {
		return nil, err
	}
	s.sweepExpiredAcceptedRisks(ctx, sec, namespace)
	if !store.ValidSecuritySuppressedFilter(req.GetSuppressed()) {
		return nil, connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("invalid suppressed filter %q (want empty, exclude, include, or only)", req.GetSuppressed()))
	}
	visible, hidden, err := s.securityScanVisibility(ctx, namespace)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if scanName := req.GetScanName(); scanName != "" && !visible(scanName) {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("security scan %s/%s not found", namespace, scanName))
	}
	rec, runHidden, err := securityScanRecordForRun(ctx, sec, visible, namespace, req.GetRunName())
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if runHidden {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("security scan %s/%s not found", namespace, req.GetRunName()))
	}
	// A detail-page run name identifies the persisted scan row. Deterministic
	// executions store findings from sibling task AgentRuns under their own
	// run names, so scan_id is the only complete execution-wide scope.
	var scanID uuid.UUID
	runName := req.GetRunName()
	if rec != nil {
		scanID = rec.ID
		runName = ""
	}
	findings, err := sec.ListSecurityFindings(ctx, store.SecurityFindingFilter{
		Namespace:         namespace,
		ScanID:            scanID,
		ScanName:          req.GetScanName(),
		RunName:           runName,
		Repository:        req.GetRepository(),
		Severity:          req.GetSeverity(),
		Status:            req.GetStatus(),
		Category:          req.GetCategory(),
		Search:            req.GetSearch(),
		MinScore:          req.GetMinScore(),
		IncludeDuplicates: req.GetIncludeDuplicates(),
		BaselineState:     req.GetBaselineState(),
		Assignee:          req.GetAssignee(),
		Suppressed:        req.GetSuppressed(),
		ExcludedScanNames: hidden,
		Limit:             req.GetLimit(),
		Offset:            req.GetOffset(),
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("listing security findings: %w", err))
	}
	resp := &platform.ListSecurityFindingsResponse{}
	for i := range findings {
		resp.Findings = append(resp.Findings, securityFindingProto(&findings[i]))
	}
	return resp, nil
}

// GetSecurityFinding returns one finding by ID along with its audit events.
func (s *Server) GetSecurityFinding(ctx context.Context, req *platform.GetSecurityFindingRequest) (*platform.GetSecurityFindingResponse, error) {
	sec, err := s.securityStore()
	if err != nil {
		return nil, err
	}
	finding, err := s.authorizedSecurityFinding(ctx, sec, req.GetId(), req.GetNamespace(), req.GetScanName())
	if err != nil {
		return nil, err
	}
	events, err := sec.ListSecurityFindingEvents(ctx, finding.Namespace, finding.ID, securityFindingEventsLimit)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("listing security finding events: %w", err))
	}
	resp := &platform.GetSecurityFindingResponse{Finding: securityFindingProto(finding)}
	for i := range events {
		resp.Events = append(resp.Events, securityFindingEventProto(&events[i]))
	}
	return resp, nil
}

// UpdateSecurityFindingStatus sets a finding's triage status, recording the
// acting user on the audit event, and returns the updated finding.
func (s *Server) UpdateSecurityFindingStatus(ctx context.Context, req *platform.UpdateSecurityFindingStatusRequest) (*platform.SecurityFinding, error) {
	sec, err := s.securityStore()
	if err != nil {
		return nil, err
	}
	actor := requestActorFromContext(ctx)
	if actor.Subject == "" {
		return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("authentication required"))
	}
	if !store.ValidSecurityFindingStatus(req.GetStatus()) {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid finding status %q", req.GetStatus()))
	}
	finding, err := s.authorizedSecurityFinding(ctx, sec, req.GetId(), req.GetNamespace(), "")
	if err != nil {
		return nil, err
	}
	var expiry *time.Time
	if req.GetAcceptedRiskExpiresAt() != nil {
		if req.GetStatus() != store.SecurityFindingStatusAcceptedRisk {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("accepted_risk_expires_at is only valid with status %q", store.SecurityFindingStatusAcceptedRisk))
		}
		t := req.GetAcceptedRiskExpiresAt().AsTime()
		if !t.After(time.Now()) {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("accepted_risk_expires_at must be in the future"))
		}
		expiry = &t
	}
	if err := sec.SetSecurityFindingStatus(ctx, finding.Namespace, finding.ID, req.GetStatus(), actor.Subject, req.GetNote(), expiry); err != nil {
		if errors.Is(err, store.ErrSecurityFindingNotFound) {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("security finding %s not found", finding.ID))
		}
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("updating security finding status: %w", err))
	}
	updated, err := sec.GetSecurityFinding(ctx, finding.Namespace, finding.ID)
	if err != nil || updated == nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("reloading security finding: %w", err))
	}
	s.nudgeSecurityScanStatusRefresh(ctx, finding.Namespace, finding.ScanName)
	return securityFindingProto(updated), nil
}

// nudgeSecurityScanStatusRefresh stamps the status-refresh annotation on the
// SecurityScan CR after finding triage so the controller re-reconciles,
// refreshes finding counts, and re-publishes any GitHub check with the
// post-triage conclusion. Best-effort: a missing CR or update error only
// delays the refresh to the next reconcile.
func (s *Server) nudgeSecurityScanStatusRefresh(ctx context.Context, namespace, scanName string) {
	if s.k8sClient == nil || namespace == "" || scanName == "" {
		return
	}
	cr := &triggersv1alpha1.SecurityScan{}
	if err := s.k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: scanName}, cr); err != nil {
		return
	}
	if cr.Annotations == nil {
		cr.Annotations = map[string]string{}
	}
	cr.Annotations[triggersv1alpha1.SecurityScanStatusRefreshAnnotation] = time.Now().UTC().Format(time.RFC3339Nano)
	_ = s.k8sClient.Update(ctx, cr)
}

// GetSecurityFindingSummary returns severity -> count aggregates (plus
// "total" and "open") for a namespace, optionally scoped to one scan or run.
func (s *Server) GetSecurityFindingSummary(ctx context.Context, req *platform.GetSecurityFindingSummaryRequest) (*platform.GetSecurityFindingSummaryResponse, error) {
	sec, err := s.securityStore()
	if err != nil {
		return nil, err
	}
	namespace, err := s.authorizeRequestNamespace(ctx, req.GetNamespace(), nil)
	if err != nil {
		return nil, err
	}
	s.sweepExpiredAcceptedRisks(ctx, sec, namespace)
	visible, hidden, err := s.securityScanVisibility(ctx, namespace)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if scanName := req.GetScanName(); scanName != "" && !visible(scanName) {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("security scan %s/%s not found", namespace, scanName))
	}
	rec, runHidden, err := securityScanRecordForRun(ctx, sec, visible, namespace, req.GetRunName())
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if runHidden {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("security scan %s/%s not found", namespace, req.GetRunName()))
	}
	var scanID uuid.UUID
	runName := req.GetRunName()
	if rec != nil {
		scanID = rec.ID
		runName = ""
	}
	counts, err := sec.SummarizeSecurityFindingsScoped(ctx, store.SecurityFindingSummaryScope{
		Namespace:         namespace,
		ScanID:            scanID,
		ScanName:          req.GetScanName(),
		RunName:           runName,
		IncludeSuppressed: req.GetIncludeSuppressed(),
		ExcludedScanNames: hidden,
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("summarizing security findings: %w", err))
	}
	resp := &platform.GetSecurityFindingSummaryResponse{Counts: counts}
	// Trends are per scan (or namespace-wide); a run-scoped summary keeps
	// the same scope as its scan.
	if trends, err := sec.GetSecurityFindingTrends(ctx, namespace, req.GetScanName(), hidden); err == nil {
		resp.Trends = securityFindingTrendsProto(trends)
	}
	return resp, nil
}

// ListSecurityFindingEvents returns a finding's audit trail, newest first.
func (s *Server) ListSecurityFindingEvents(ctx context.Context, req *platform.ListSecurityFindingEventsRequest) (*platform.ListSecurityFindingEventsResponse, error) {
	sec, err := s.securityStore()
	if err != nil {
		return nil, err
	}
	finding, err := s.authorizedSecurityFinding(ctx, sec, req.GetId(), req.GetNamespace(), req.GetScanName())
	if err != nil {
		return nil, err
	}
	limit := req.GetLimit()
	if limit <= 0 {
		limit = securityFindingEventsLimit
	}
	events, err := sec.ListSecurityFindingEvents(ctx, finding.Namespace, finding.ID, limit)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("listing security finding events: %w", err))
	}
	resp := &platform.ListSecurityFindingEventsResponse{}
	for i := range events {
		resp.Events = append(resp.Events, securityFindingEventProto(&events[i]))
	}
	return resp, nil
}

// maxSecurityFindingCommentLen bounds comment bodies (in characters) so the
// append-only audit table cannot be flooded with arbitrarily large rows.
const maxSecurityFindingCommentLen = 10000

// AddSecurityFindingComment appends a comment event to a finding's audit
// trail, attributed to the authenticated caller.
func (s *Server) AddSecurityFindingComment(ctx context.Context, req *platform.AddSecurityFindingCommentRequest) (*platform.SecurityFindingEvent, error) {
	sec, err := s.securityStore()
	if err != nil {
		return nil, err
	}
	actor := requestActorFromContext(ctx)
	if actor.Subject == "" {
		return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("authentication required"))
	}
	body := strings.TrimSpace(req.GetBody())
	if body == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("comment body is required"))
	}
	if utf8.RuneCountInString(body) > maxSecurityFindingCommentLen {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("comment body exceeds %d characters", maxSecurityFindingCommentLen))
	}
	finding, err := s.authorizedSecurityFinding(ctx, sec, req.GetId(), req.GetNamespace(), req.GetScanName())
	if err != nil {
		return nil, err
	}
	event, err := sec.AddSecurityFindingComment(ctx, finding.Namespace, finding.ID, actor.Subject, body)
	if err != nil {
		if errors.Is(err, store.ErrSecurityFindingNotFound) {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("security finding %s not found", finding.ID))
		}
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("adding security finding comment: %w", err))
	}
	return securityFindingEventProto(event), nil
}

// authorizedSecurityFinding loads a finding by request ID, scoped to the
// namespace the caller is authorized to act in (the requested namespace when
// given, the caller's personal namespace otherwise). A finding that does not
// exist, one that lives in a namespace the caller may not read, and one that
// belongs to a scan hidden from the caller (owned by another user and not
// shared) are all reported as NotFound so the endpoint cannot be used as a
// UUID-existence oracle. When scanName is non-empty the finding must also
// belong to that scan; a mismatch is likewise reported as NotFound.
func (s *Server) authorizedSecurityFinding(ctx context.Context, sec store.SecurityFindingStore, rawID, requestedNamespace, scanName string) (*store.SecurityFindingRecord, error) {
	id, err := uuid.Parse(strings.TrimSpace(rawID))
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid finding id %q", rawID))
	}
	namespace, err := s.authorizeRequestNamespace(ctx, requestedNamespace, nil)
	if err != nil {
		return nil, err
	}
	finding, err := sec.GetSecurityFinding(ctx, namespace, id)
	if err != nil && !errors.Is(err, store.ErrSecurityFindingNotFound) {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("getting security finding: %w", err))
	}
	if err != nil || finding == nil || (scanName != "" && finding.ScanName != scanName) {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("security finding %s not found", id))
	}
	if finding.ScanName != "" {
		visible, _, verr := s.securityScanVisibility(ctx, namespace)
		if verr != nil {
			return nil, connect.NewError(connect.CodeInternal, verr)
		}
		if !visible(finding.ScanName) {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("security finding %s not found", id))
		}
	}
	return finding, nil
}

func securityScanProto(in *store.SecurityScanRecord) *platform.SecurityScan {
	out := &platform.SecurityScan{
		Id:         in.ID.String(),
		Namespace:  in.Namespace,
		ScanName:   in.ScanName,
		RunName:    in.RunName,
		Repository: in.Repository,
		Revision:   in.Revision,
		Status:     in.Status,
		Summary:    in.Summary,
		Counts:     in.Counts,
	}
	if in.StartedAt != nil {
		out.StartedAt = timestamppb.New(*in.StartedAt)
	}
	if in.CompletedAt != nil {
		out.CompletedAt = timestamppb.New(*in.CompletedAt)
	}
	return out
}

func securityFindingProto(in *store.SecurityFindingRecord) *platform.SecurityFinding {
	out := &platform.SecurityFinding{
		Id:             in.ID.String(),
		ScanId:         in.ScanID.String(),
		Namespace:      in.Namespace,
		ScanName:       in.ScanName,
		RunName:        in.RunName,
		Fingerprint:    in.Fingerprint,
		Title:          in.Title,
		Category:       in.Category,
		Severity:       in.Severity,
		Confidence:     in.Confidence,
		Repository:     in.Repository,
		Revision:       in.Revision,
		FilePath:       in.FilePath,
		StartLine:      in.StartLine,
		EndLine:        in.EndLine,
		Symbol:         in.Symbol,
		Cwe:            in.CWE,
		Description:    in.Description,
		Impact:         in.Impact,
		AttackVector:   in.AttackVector,
		Remediation:    in.Remediation,
		References:     in.References,
		SourceAgent:    in.SourceAgent,
		ScanStep:       in.ScanStep,
		Score:          in.Score,
		Status:         in.Status,
		Occurrences:    in.Occurrences,
		Raw:            string(in.Raw),
		FirstSeenAt:    timestamppb.New(in.FirstSeenAt),
		LastSeenAt:     timestamppb.New(in.LastSeenAt),
		Assignee:       in.Assignee,
		TicketUrl:      in.TicketURL,
		TicketProvider: in.TicketProvider,
		BaselineState:  in.BaselineState,
	}
	if in.SessionID != nil {
		out.SessionId = in.SessionID.String()
	}
	if in.DuplicateOf != nil {
		out.DuplicateOf = in.DuplicateOf.String()
	}
	if in.AcceptedRiskExpiresAt != nil {
		out.AcceptedRiskExpiresAt = timestamppb.New(*in.AcceptedRiskExpiresAt)
	}
	if in.ResolvedAt != nil {
		out.ResolvedAt = timestamppb.New(*in.ResolvedAt)
	}
	if in.TriagedAt != nil {
		out.TriagedAt = timestamppb.New(*in.TriagedAt)
	}
	out.SourceKind = in.SourceKind
	out.Tool = in.Tool
	out.ToolVersion = in.ToolVersion
	out.RuleId = in.RuleID
	out.CorrelatedFingerprints = in.CorrelatedFingerprints
	out.SuppressedBy = in.SuppressedBy
	out.SuppressedReason = in.SuppressedReason
	out.SuppressedOwner = in.SuppressedOwner
	if in.SuppressionExpiresAt != nil {
		out.SuppressionExpiresAt = timestamppb.New(*in.SuppressionExpiresAt)
	}
	if in.SuppressedAt != nil {
		out.SuppressedAt = timestamppb.New(*in.SuppressedAt)
	}
	return out
}

func securityFindingEventProto(in *store.SecurityFindingEvent) *platform.SecurityFindingEvent {
	return &platform.SecurityFindingEvent{
		Id:        in.ID,
		EventType: in.EventType,
		Actor:     in.Actor,
		Note:      in.Note,
		Detail:    string(in.Detail),
		CreatedAt: timestamppb.New(in.CreatedAt),
	}
}

// Connect adapter methods for the security scanning RPCs.

func (h *PlatformServiceConnectHandler) ListSecurityScans(ctx context.Context, req *connect.Request[platform.ListSecurityScansRequest]) (*connect.Response[platform.ListSecurityScansResponse], error) {
	resp, err := h.srv.ListSecurityScans(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) GetSecurityScan(ctx context.Context, req *connect.Request[platform.GetSecurityScanRequest]) (*connect.Response[platform.SecurityScan], error) {
	resp, err := h.srv.GetSecurityScan(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) ListSecurityFindings(ctx context.Context, req *connect.Request[platform.ListSecurityFindingsRequest]) (*connect.Response[platform.ListSecurityFindingsResponse], error) {
	resp, err := h.srv.ListSecurityFindings(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) GetSecurityFinding(ctx context.Context, req *connect.Request[platform.GetSecurityFindingRequest]) (*connect.Response[platform.GetSecurityFindingResponse], error) {
	resp, err := h.srv.GetSecurityFinding(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) UpdateSecurityFindingStatus(ctx context.Context, req *connect.Request[platform.UpdateSecurityFindingStatusRequest]) (*connect.Response[platform.SecurityFinding], error) {
	resp, err := h.srv.UpdateSecurityFindingStatus(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) GetSecurityFindingSummary(ctx context.Context, req *connect.Request[platform.GetSecurityFindingSummaryRequest]) (*connect.Response[platform.GetSecurityFindingSummaryResponse], error) {
	resp, err := h.srv.GetSecurityFindingSummary(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) ListSecurityFindingEvents(ctx context.Context, req *connect.Request[platform.ListSecurityFindingEventsRequest]) (*connect.Response[platform.ListSecurityFindingEventsResponse], error) {
	resp, err := h.srv.ListSecurityFindingEvents(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) AddSecurityFindingComment(ctx context.Context, req *connect.Request[platform.AddSecurityFindingCommentRequest]) (*connect.Response[platform.SecurityFindingEvent], error) {
	resp, err := h.srv.AddSecurityFindingComment(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}
