package dashboard

import (
	"context"
	"fmt"
	"strings"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/security"
	"github.com/gratefulagents/gratefulagents/internal/store"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
)

const (
	// securityOverviewDefaultRecent is how many recent scans the overview
	// returns when the request does not name a limit.
	securityOverviewDefaultRecent = int32(10)
	// securityOverviewScanFetch bounds the persisted scan rows fetched to
	// split active from recent runs.
	securityOverviewScanFetch = int32(100)
)

// GetSecurityOverview aggregates a namespace's security posture: active and
// recent persisted scan runs plus open finding counts from the state store,
// and failing/blocked SecurityScan configurations from the cluster. A state
// store without security support degrades to store_supported=false instead of
// failing, so the configuration half of the page keeps working; individual
// aggregation failures degrade to warnings (partial result).
func (s *Server) GetSecurityOverview(ctx context.Context, req *platform.GetSecurityOverviewRequest) (*platform.GetSecurityOverviewResponse, error) {
	namespace, err := s.authorizeRequestNamespace(ctx, req.GetNamespace(), nil)
	if err != nil {
		return nil, err
	}

	resp := &platform.GetSecurityOverviewResponse{}

	if sec, ok := s.stateStore.(store.SecurityFindingStore); ok {
		resp.StoreSupported = true
		s.sweepExpiredAcceptedRisks(ctx, sec, namespace)
		recentLimit := req.GetRecentLimit()
		if recentLimit <= 0 {
			recentLimit = securityOverviewDefaultRecent
		}
		scans, err := sec.ListSecurityScans(ctx, namespace, "", securityOverviewScanFetch)
		if err != nil {
			resp.Warnings = append(resp.Warnings, fmt.Sprintf("listing security scans: %v", err))
		} else {
			for i := range scans {
				pb := securityScanProto(&scans[i])
				if scans[i].CompletedAt == nil {
					resp.ActiveScans = append(resp.ActiveScans, pb)
					continue
				}
				if int32(len(resp.RecentScans)) < recentLimit {
					resp.RecentScans = append(resp.RecentScans, pb)
				}
			}
		}
		counts, err := sec.SummarizeSecurityFindings(ctx, namespace, "", "", false)
		if err != nil {
			resp.Warnings = append(resp.Warnings, fmt.Sprintf("summarizing security findings: %v", err))
		} else {
			resp.FindingCounts = counts
			// Baseline states are classified at write time; any tracked
			// finding means observation data exists and the counters are
			// meaningful.
			resp.BaselineAvailable = counts["baseline_tracked"] > 0
			resp.NewFindings = counts["baseline_new"]
			resp.RecurringFindings = counts["baseline_recurring"]
			resp.ResolvedFindings = counts["baseline_resolved"]
			resp.RegressedFindings = counts["baseline_regressed"]
			resp.ReopenedFindings = counts["baseline_reopened"]
		}
		trends, err := sec.GetSecurityFindingTrends(ctx, namespace, "")
		if err != nil {
			resp.Warnings = append(resp.Warnings, fmt.Sprintf("aggregating security finding trends: %v", err))
		} else {
			resp.Trends = securityFindingTrendsProto(trends)
		}
	}

	configs := &triggersv1alpha1.SecurityScanList{}
	if err := s.k8sClient.List(ctx, configs, client.InNamespace(namespace)); err != nil {
		resp.Warnings = append(resp.Warnings, fmt.Sprintf("listing security scan configurations: %v", err))
		return resp, nil
	}
	visible := s.resourceVisibilityFilter(ctx, securityScanResourceType, false)
	for i := range configs.Items {
		cr := &configs.Items[i]
		if !visible(cr.Namespace, cr.Name) {
			continue
		}
		resp.ConfigCount++
		if issue := securityScanConfigIssue(cr); issue != nil {
			resp.ConfigIssues = append(resp.ConfigIssues, issue)
		}
	}
	return resp, nil
}

// securityScanConfigIssue reports why a SecurityScan configuration needs
// attention, or nil when it is healthy. A configuration is an issue when its
// Ready condition is False (failed, blocked, suspended, insecure defaults,
// findings over threshold) or when it records a last error.
func securityScanConfigIssue(cr *triggersv1alpha1.SecurityScan) *platform.SecurityScanConfigIssue {
	var ready *metav1.Condition
	for i := range cr.Status.Conditions {
		if cr.Status.Conditions[i].Type == triggersv1alpha1.ConditionSecurityScanReady {
			ready = &cr.Status.Conditions[i]
			break
		}
	}
	notReady := ready != nil && ready.Status == metav1.ConditionFalse
	if !notReady && strings.TrimSpace(cr.Status.LastError) == "" {
		return nil
	}
	issue := &platform.SecurityScanConfigIssue{
		Namespace: cr.Namespace,
		Name:      cr.Name,
		Phase:     cr.Status.Phase,
		Message:   cr.Status.LastError,
		Suspended: cr.Spec.Suspend,
	}
	if notReady {
		issue.ReadyReason = ready.Reason
		if issue.Message == "" {
			issue.Message = ready.Message
		}
	}
	return issue
}

// GetSecurityScanReport returns the Markdown or SARIF report artifact written
// by a scan run's submit_security_scan_report call.
func (s *Server) GetSecurityScanReport(ctx context.Context, req *platform.GetSecurityScanReportRequest) (*platform.GetSecurityScanReportResponse, error) {
	sec, err := s.securityStore()
	if err != nil {
		return nil, err
	}
	namespace, err := s.authorizeRequestNamespace(ctx, req.GetNamespace(), nil)
	if err != nil {
		return nil, err
	}

	var kind, format, ext string
	switch strings.ToLower(strings.TrimSpace(req.GetFormat())) {
	case "", "markdown":
		kind, format, ext = security.ReportArtifactKind, "markdown", "md"
	case "sarif":
		kind, format, ext = security.SARIFArtifactKind, "sarif", "sarif"
	default:
		return nil, connect.NewError(connect.CodeInvalidArgument,
			fmt.Errorf("unknown report format %q (want \"markdown\" or \"sarif\")", req.GetFormat()))
	}

	scan, err := sec.GetSecurityScan(ctx, namespace, req.GetRunName())
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("getting security scan: %w", err))
	}
	if scan == nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("security scan %s/%s not found", namespace, req.GetRunName()))
	}
	if scan.SessionID == nil {
		return nil, connect.NewError(connect.CodeNotFound,
			fmt.Errorf("security scan %s/%s has no report yet: the report is written when the scan run submits its results", namespace, req.GetRunName()))
	}
	art, err := s.stateStore.GetArtifact(ctx, *scan.SessionID, kind)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("getting security scan report artifact: %w", err))
	}
	if art == nil || strings.TrimSpace(art.Content) == "" {
		return nil, connect.NewError(connect.CodeNotFound,
			fmt.Errorf("security scan %s/%s has no %s report yet: the report is written when the scan run submits its results", namespace, req.GetRunName(), format))
	}

	name := scan.ScanName
	if name == "" {
		name = scan.RunName
	}
	resp := &platform.GetSecurityScanReportResponse{
		Content:  art.Content,
		Format:   format,
		Filename: fmt.Sprintf("%s-%s.%s", name, scan.RunName, ext),
	}
	if !art.UpdatedAt.IsZero() {
		resp.UpdatedAt = timestamppb.New(art.UpdatedAt)
	}
	return resp, nil
}

// Connect adapter methods.

func (h *PlatformServiceConnectHandler) GetSecurityOverview(ctx context.Context, req *connect.Request[platform.GetSecurityOverviewRequest]) (*connect.Response[platform.GetSecurityOverviewResponse], error) {
	resp, err := h.srv.GetSecurityOverview(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

func (h *PlatformServiceConnectHandler) GetSecurityScanReport(ctx context.Context, req *connect.Request[platform.GetSecurityScanReportRequest]) (*connect.Response[platform.GetSecurityScanReportResponse], error) {
	resp, err := h.srv.GetSecurityScanReport(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}
