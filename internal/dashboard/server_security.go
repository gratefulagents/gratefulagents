package dashboard

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"

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
	scans, err := sec.ListSecurityScans(ctx, namespace, req.GetScanName(), req.GetLimit())
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("listing security scans: %w", err))
	}
	resp := &platform.ListSecurityScansResponse{}
	for i := range scans {
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
	findings, err := sec.ListSecurityFindings(ctx, store.SecurityFindingFilter{
		Namespace:         namespace,
		ScanName:          req.GetScanName(),
		RunName:           req.GetRunName(),
		Repository:        req.GetRepository(),
		Severity:          req.GetSeverity(),
		Status:            req.GetStatus(),
		Category:          req.GetCategory(),
		Search:            req.GetSearch(),
		MinScore:          req.GetMinScore(),
		IncludeDuplicates: req.GetIncludeDuplicates(),
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
	finding, err := s.authorizedSecurityFinding(ctx, sec, req.GetId())
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
	finding, err := s.authorizedSecurityFinding(ctx, sec, req.GetId())
	if err != nil {
		return nil, err
	}
	if err := sec.SetSecurityFindingStatus(ctx, finding.Namespace, finding.ID, req.GetStatus(), actor.Subject, req.GetNote()); err != nil {
		if errors.Is(err, store.ErrSecurityFindingNotFound) {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("security finding %s not found", finding.ID))
		}
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("updating security finding status: %w", err))
	}
	updated, err := sec.GetSecurityFinding(ctx, finding.Namespace, finding.ID)
	if err != nil || updated == nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("reloading security finding: %w", err))
	}
	return securityFindingProto(updated), nil
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
	counts, err := sec.SummarizeSecurityFindings(ctx, namespace, req.GetScanName(), req.GetRunName())
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("summarizing security findings: %w", err))
	}
	return &platform.GetSecurityFindingSummaryResponse{Counts: counts}, nil
}

// authorizedSecurityFinding loads a finding by request ID, scoped to the
// namespace the caller is authorized to act in. A finding that does not exist
// and one that lives in a namespace the caller may not read are both reported
// as NotFound so the endpoint cannot be used as a UUID-existence oracle.
func (s *Server) authorizedSecurityFinding(ctx context.Context, sec store.SecurityFindingStore, rawID string) (*store.SecurityFindingRecord, error) {
	id, err := uuid.Parse(strings.TrimSpace(rawID))
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid finding id %q", rawID))
	}
	namespace, err := s.authorizeRequestNamespace(ctx, "", nil)
	if err != nil {
		return nil, err
	}
	finding, err := sec.GetSecurityFinding(ctx, namespace, id)
	if err != nil && !errors.Is(err, store.ErrSecurityFindingNotFound) {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("getting security finding: %w", err))
	}
	if err != nil || finding == nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("security finding %s not found", id))
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
		Id:           in.ID.String(),
		ScanId:       in.ScanID.String(),
		Namespace:    in.Namespace,
		ScanName:     in.ScanName,
		RunName:      in.RunName,
		Fingerprint:  in.Fingerprint,
		Title:        in.Title,
		Category:     in.Category,
		Severity:     in.Severity,
		Confidence:   in.Confidence,
		Repository:   in.Repository,
		Revision:     in.Revision,
		FilePath:     in.FilePath,
		StartLine:    in.StartLine,
		EndLine:      in.EndLine,
		Symbol:       in.Symbol,
		Cwe:          in.CWE,
		Description:  in.Description,
		Impact:       in.Impact,
		AttackVector: in.AttackVector,
		Remediation:  in.Remediation,
		References:   in.References,
		SourceAgent:  in.SourceAgent,
		ScanStep:     in.ScanStep,
		Score:        in.Score,
		Status:       in.Status,
		Occurrences:  in.Occurrences,
		Raw:          string(in.Raw),
		FirstSeenAt:  timestamppb.New(in.FirstSeenAt),
		LastSeenAt:   timestamppb.New(in.LastSeenAt),
	}
	if in.SessionID != nil {
		out.SessionId = in.SessionID.String()
	}
	if in.DuplicateOf != nil {
		out.DuplicateOf = in.DuplicateOf.String()
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
