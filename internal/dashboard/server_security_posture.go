package dashboard

import (
	"context"
	"fmt"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/gratefulagents/gratefulagents/internal/store"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
)

// GetSecurityConfigPostures aggregates the namespace's persisted security
// posture grouped per scan configuration: current finding counts, the latest
// run, and recent completed-run activity for trend visualization. A state
// store without security support degrades to store_supported=false instead
// of failing; aggregation failures degrade to warnings (partial result).
// Postures follow the SecurityScan CR's ownership: configurations hidden
// from the caller are excluded from the response.
func (s *Server) GetSecurityConfigPostures(ctx context.Context, req *platform.GetSecurityConfigPosturesRequest) (*platform.GetSecurityConfigPosturesResponse, error) {
	namespace, err := s.authorizeRequestNamespace(ctx, req.GetNamespace(), nil)
	if err != nil {
		return nil, err
	}

	resp := &platform.GetSecurityConfigPosturesResponse{}
	sec, ok := s.stateStore.(store.SecurityFindingStore)
	if !ok {
		return resp, nil
	}
	resp.StoreSupported = true

	scanVisible, hiddenScans, verr := s.securityScanVisibility(ctx, namespace)
	if verr != nil {
		resp.Warnings = append(resp.Warnings, fmt.Sprintf("resolving security scan visibility: %v", verr))
		return resp, nil
	}
	postures, err := sec.ListSecurityConfigPostures(ctx, namespace, req.GetActivityLimit(), hiddenScans)
	if err != nil {
		resp.Warnings = append(resp.Warnings, fmt.Sprintf("aggregating security configuration postures: %v", err))
		return resp, nil
	}
	for i := range postures {
		if !scanVisible(postures[i].ScanName) {
			continue
		}
		resp.Postures = append(resp.Postures, securityConfigPostureProto(&postures[i]))
	}
	return resp, nil
}

func securityConfigPostureProto(p *store.SecurityConfigPosture) *platform.SecurityConfigPosture {
	pb := &platform.SecurityConfigPosture{
		ScanName:      p.ScanName,
		FindingCounts: p.Counts,
		Repository:    p.Repository,
		LastRunName:   p.LastRunName,
		LastRunStatus: p.LastRunStatus,
	}
	if p.LastStartedAt != nil {
		pb.LastStartedAt = timestamppb.New(*p.LastStartedAt)
	}
	if p.LastCompletedAt != nil {
		pb.LastCompletedAt = timestamppb.New(*p.LastCompletedAt)
	}
	for i := range p.Activity {
		point := &p.Activity[i]
		pb.Activity = append(pb.Activity, &platform.SecurityRunActivityPoint{
			RunName:        point.RunName,
			CompletedAt:    timestamppb.New(point.CompletedAt),
			SeverityCounts: point.SeverityCounts,
			Total:          point.Total,
		})
	}
	return pb
}

// Connect adapter method.

func (h *PlatformServiceConnectHandler) GetSecurityConfigPostures(ctx context.Context, req *connect.Request[platform.GetSecurityConfigPosturesRequest]) (*connect.Response[platform.GetSecurityConfigPosturesResponse], error) {
	resp, err := h.srv.GetSecurityConfigPostures(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}
