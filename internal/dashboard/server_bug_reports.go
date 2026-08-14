package dashboard

import (
	"context"
	"errors"
	"fmt"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/gratefulagents/gratefulagents/internal/store"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
)

// bugReportStore returns the state store's agent bug report capability, or a
// FailedPrecondition error when the configured store does not support it.
func (s *Server) bugReportStore() (store.AgentBugReportStore, error) {
	br, ok := s.stateStore.(store.AgentBugReportStore)
	if !ok {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("agent bug reports are not supported by the configured state store"))
	}
	return br, nil
}

// bugReportVisibility returns the standard resource-visibility predicate for
// bug reports, derived from the ownership of the run that (most recently)
// filed each report. Namespaces can be shared between users, so namespace
// authorization alone is not enough: admins (and internal calls) see
// everything, other callers see reports whose run is unowned, owned by them,
// or shared with them.
func (s *Server) bugReportVisibility(ctx context.Context) func(rec *store.AgentBugReportRecord) bool {
	runVisible := s.resourceVisibilityFilter(ctx, "agent_run", false)
	return func(rec *store.AgentBugReportRecord) bool {
		if rec.RunName == "" {
			return true
		}
		return runVisible(rec.Namespace, rec.RunName)
	}
}

// ListBugReports lists agent-filed platform bug reports in a namespace, most
// recently seen first.
func (s *Server) ListBugReports(ctx context.Context, req *platform.ListBugReportsRequest) (*platform.ListBugReportsResponse, error) {
	br, err := s.bugReportStore()
	if err != nil {
		return nil, err
	}
	namespace, err := s.authorizeRequestNamespace(ctx, req.GetNamespace(), nil)
	if err != nil {
		return nil, err
	}
	if status := req.GetStatus(); status != "" && !store.ValidAgentBugReportStatus(status) {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid bug report status %q", status))
	}
	if category := req.GetCategory(); category != "" && !store.ValidAgentBugReportCategory(category) {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid bug report category %q", category))
	}
	reports, err := br.ListAgentBugReports(ctx, store.AgentBugReportFilter{
		Namespace: namespace,
		Status:    req.GetStatus(),
		Category:  req.GetCategory(),
		Limit:     req.GetLimit(),
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("listing bug reports: %w", err))
	}
	visible := s.bugReportVisibility(ctx)
	resp := &platform.ListBugReportsResponse{}
	for i := range reports {
		if !visible(&reports[i]) {
			continue
		}
		resp.Reports = append(resp.Reports, bugReportProto(&reports[i]))
	}
	return resp, nil
}

// UpdateBugReportStatus sets the triage status of one agent bug report,
// attributed to the authenticated caller.
func (s *Server) UpdateBugReportStatus(ctx context.Context, req *platform.UpdateBugReportStatusRequest) (*platform.BugReport, error) {
	br, err := s.bugReportStore()
	if err != nil {
		return nil, err
	}
	actor := requestActorFromContext(ctx)
	if actor.Subject == "" {
		return nil, connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("authentication required"))
	}
	namespace, err := s.authorizeRequestNamespace(ctx, req.GetNamespace(), nil)
	if err != nil {
		return nil, err
	}
	if !store.ValidAgentBugReportStatus(req.GetStatus()) {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid bug report status %q", req.GetStatus()))
	}
	id, err := uuid.Parse(req.GetId())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid bug report id %q", req.GetId()))
	}
	// A report the caller must not see reports the same NotFound as a missing
	// one, so this endpoint cannot be used to probe or triage another user's
	// reports in a shared namespace.
	existing, err := br.GetAgentBugReport(ctx, namespace, id)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("getting bug report: %w", err))
	}
	if existing == nil || !s.bugReportVisibility(ctx)(existing) {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("bug report %s not found", id))
	}
	if err := br.SetAgentBugReportStatus(ctx, namespace, id, req.GetStatus(), actor.Subject, req.GetNote()); err != nil {
		if errors.Is(err, store.ErrAgentBugReportNotFound) {
			return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("bug report %s not found", id))
		}
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("updating bug report status: %w", err))
	}
	updated, err := br.GetAgentBugReport(ctx, namespace, id)
	if err != nil || updated == nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("reloading bug report: %w", err))
	}
	return bugReportProto(updated), nil
}

func bugReportProto(in *store.AgentBugReportRecord) *platform.BugReport {
	return &platform.BugReport{
		Id:          in.ID.String(),
		Namespace:   in.Namespace,
		RunName:     in.RunName,
		Category:    in.Category,
		ToolName:    in.ToolName,
		Title:       in.Title,
		Body:        in.Body,
		Occurrences: in.Occurrences,
		Status:      in.Status,
		StatusNote:  in.StatusNote,
		StatusActor: in.StatusActor,
		FirstSeenAt: timestamppb.New(in.FirstSeenAt),
		LastSeenAt:  timestamppb.New(in.LastSeenAt),
	}
}
