package dashboard

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/gratefulagents/gratefulagents/internal/store"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
)

const (
	defaultSecurityResearchListLimit int32 = 100
	maxSecurityResearchListLimit     int32 = 200
	maxSecurityResearchJSONBytes           = 256 << 10
)

func (s *Server) securityResearchStore() (store.SecurityResearchStore, error) {
	research, ok := s.stateStore.(store.SecurityResearchStore)
	if !ok {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("security research is not supported by the configured state store"))
	}
	return research, nil
}

func (s *Server) securityResearchRevision(ctx context.Context, scope *platform.SecurityResearchScope) (store.SecurityResearchStore, string, string, *store.SecurityResearchRevision, error) {
	research, err := s.securityResearchStore()
	if err != nil {
		return nil, "", "", nil, err
	}
	if scope == nil || strings.TrimSpace(scope.GetTargetKey()) == "" || strings.TrimSpace(scope.GetRevision()) == "" {
		return nil, "", "", nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("target key and revision are required"))
	}
	namespace, err := s.authorizeRequestNamespace(ctx, scope.GetNamespace(), nil)
	if err != nil {
		return nil, "", "", nil, err
	}
	targetKey := strings.TrimSpace(scope.GetTargetKey())
	visible, _, err := s.securityScanVisibility(ctx, namespace)
	if err != nil {
		return nil, "", "", nil, connect.NewError(connect.CodeInternal, err)
	}
	if !visible(targetKey) {
		return nil, "", "", nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("security research target %s/%s not found", namespace, targetKey))
	}
	revision, err := research.GetSecurityResearchRevision(ctx, namespace, targetKey, strings.TrimSpace(scope.GetRevision()))
	if err != nil {
		return nil, "", "", nil, securityResearchError("getting security research revision", err)
	}
	if revision == nil {
		return nil, "", "", nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("security research revision %s/%s@%s not found", namespace, targetKey, scope.GetRevision()))
	}
	return research, namespace, targetKey, revision, nil
}

func (s *Server) authorizeSecurityResearchWrite(ctx context.Context, namespace, targetKey string) error {
	if err := s.requireResourceAccess(ctx, securityScanResourceType, targetKey, namespace, AccessCollaborator, "modify security research"); err != nil {
		return err
	}
	return nil
}

func securityResearchHypothesisForRevision(ctx context.Context, research store.SecurityResearchStore, namespace string, revisionID, hypothesisID uuid.UUID) error {
	values, err := research.ListSecurityResearchHypotheses(ctx, namespace, revisionID)
	if err != nil {
		return securityResearchError("checking hypothesis revision", err)
	}
	for i := range values {
		if values[i].ID == hypothesisID {
			return nil
		}
	}
	return connect.NewError(connect.CodeNotFound, store.ErrSecurityResearchHypothesisNotFound)
}

func securityResearchSweepForRevision(ctx context.Context, research store.SecurityResearchStore, namespace string, revisionID, sweepID uuid.UUID) (*store.SecurityResearchVariantSweep, error) {
	values, err := research.ListSecurityResearchVariantSweeps(ctx, namespace, revisionID)
	if err != nil {
		return nil, securityResearchError("checking variant sweep revision", err)
	}
	for i := range values {
		if values[i].ID == sweepID {
			return &values[i], nil
		}
	}
	return nil, connect.NewError(connect.CodeNotFound, store.ErrSecurityResearchSweepNotFound)
}

func securityResearchActor(ctx context.Context) (string, error) {
	actor, recorded := requestActorFromContextOK(ctx)
	if !recorded || strings.TrimSpace(actor.Subject) == "" {
		return "", connect.NewError(connect.CodeUnauthenticated, fmt.Errorf("authentication required"))
	}
	return actor.Subject, nil
}

func securityResearchError(action string, err error) error {
	switch {
	case errors.Is(err, store.ErrSecurityResearchTargetNotFound),
		errors.Is(err, store.ErrSecurityResearchRevisionNotFound),
		errors.Is(err, store.ErrSecurityResearchDossierNotFound),
		errors.Is(err, store.ErrSecurityResearchHypothesisNotFound),
		errors.Is(err, store.ErrSecurityResearchSweepNotFound),
		errors.Is(err, store.ErrSecurityResearchSubmissionNotFound):
		return connect.NewError(connect.CodeNotFound, err)
	case errors.Is(err, store.ErrSecurityResearchVersionConflict):
		return connect.NewError(connect.CodeAborted, err)
	case errors.Is(err, store.ErrSecurityResearchInvalidTransition), errors.Is(err, store.ErrSecurityResearchLineageCycle):
		return connect.NewError(connect.CodeFailedPrecondition, err)
	default:
		return connect.NewError(connect.CodeInternal, fmt.Errorf("%s: %w", action, err))
	}
}

func securityResearchLimit(limit int32) int {
	if limit <= 0 {
		return int(defaultSecurityResearchListLimit)
	}
	if limit > maxSecurityResearchListLimit {
		return int(maxSecurityResearchListLimit)
	}
	return int(limit)
}

func securityResearchJSON(value, fallback, field string) (json.RawMessage, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		value = fallback
	}
	if len(value) > maxSecurityResearchJSONBytes {
		return nil, connect.NewError(connect.CodeResourceExhausted, fmt.Errorf("%s exceeds %d bytes", field, maxSecurityResearchJSONBytes))
	}
	if !json.Valid([]byte(value)) {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("%s must be valid JSON", field))
	}
	return json.RawMessage(value), nil
}

func securityResearchID(value, field string) (uuid.UUID, error) {
	id, err := uuid.Parse(strings.TrimSpace(value))
	if err != nil {
		return uuid.Nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid %s", field))
	}
	return id, nil
}

func optionalSecurityResearchID(value, field string) (*uuid.UUID, error) {
	if strings.TrimSpace(value) == "" {
		return nil, nil
	}
	id, err := securityResearchID(value, field)
	if err != nil {
		return nil, err
	}
	return &id, nil
}

func (s *Server) GetSecurityCampaignResearchStatus(ctx context.Context, req *platform.GetSecurityCampaignResearchStatusRequest) (*platform.SecurityCampaignResearchStatus, error) {
	research, namespace, targetKey, revision, err := s.securityResearchRevision(ctx, req.GetScope())
	if err != nil {
		return nil, err
	}
	workflow := strings.TrimSpace(req.GetWorkflow())
	if workflow == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("workflow is required"))
	}
	hypotheses, err := research.ListSecurityResearchHypotheses(ctx, namespace, revision.ID)
	if err != nil {
		return nil, securityResearchError("listing security research hypotheses", err)
	}
	coverage, err := research.ListSecurityResearchCoverage(ctx, namespace, revision.ID)
	if err != nil {
		return nil, securityResearchError("listing security research coverage", err)
	}
	sweeps, err := research.ListSecurityResearchVariantSweeps(ctx, namespace, revision.ID)
	if err != nil {
		return nil, securityResearchError("listing security research variant sweeps", err)
	}
	precision, err := research.GetSecuritySubmissionPrecision(ctx, namespace, revision.TargetID, workflow, nil)
	if err != nil {
		return nil, securityResearchError("getting security submission precision", err)
	}
	status := &platform.SecurityCampaignResearchStatus{
		TargetId: revision.TargetID.String(), RevisionId: revision.ID.String(), TargetKey: targetKey,
		Revision: revision.Revision, Workflow: workflow, HypothesisStatusCounts: map[string]int32{},
		HypothesisResultCounts: map[string]int32{}, CoverageVerdictCounts: map[string]int32{},
		VariantSweepStatusCounts: map[string]int32{}, Precision: securitySubmissionPrecisionProto(precision),
	}
	if dossier, dossierErr := research.GetLatestSecurityResearchDossier(ctx, namespace, revision.ID); dossierErr != nil && !errors.Is(dossierErr, store.ErrSecurityResearchDossierNotFound) {
		return nil, securityResearchError("getting security research dossier", dossierErr)
	} else if dossier != nil {
		status.DossierVersion = dossier.Version
	}
	for i := range hypotheses {
		status.HypothesisStatusCounts[hypotheses[i].Status]++
		status.HypothesisResultCounts[hypotheses[i].Result]++
	}
	for i := range coverage {
		status.CoverageVerdictCounts[coverage[i].Verdict]++
	}
	for i := range sweeps {
		status.VariantSweepStatusCounts[sweeps[i].Status]++
	}
	return status, nil
}

func (s *Server) GetSecurityResearchDossier(ctx context.Context, req *platform.GetSecurityResearchDossierRequest) (*platform.SecurityResearchDossier, error) {
	research, namespace, _, revision, err := s.securityResearchRevision(ctx, req.GetScope())
	if err != nil {
		return nil, err
	}
	dossier, err := research.GetLatestSecurityResearchDossier(ctx, namespace, revision.ID)
	if err != nil {
		return nil, securityResearchError("getting security research dossier", err)
	}
	if dossier == nil {
		return nil, connect.NewError(connect.CodeNotFound, store.ErrSecurityResearchDossierNotFound)
	}
	return securityResearchDossierProto(dossier), nil
}

func (s *Server) AmendSecurityResearchDossier(ctx context.Context, req *platform.AmendSecurityResearchDossierRequest) (*platform.AmendSecurityResearchDossierResponse, error) {
	actor, err := securityResearchActor(ctx)
	if err != nil {
		return nil, err
	}
	research, namespace, targetKey, revision, err := s.securityResearchRevision(ctx, req.GetScope())
	if err != nil {
		return nil, err
	}
	if err := s.authorizeSecurityResearchWrite(ctx, namespace, targetKey); err != nil {
		return nil, err
	}
	content, err := securityResearchJSON(req.GetContentJson(), "{}", "content_json")
	if err != nil {
		return nil, err
	}
	parentID, err := optionalSecurityResearchID(req.GetExpectedParentId(), "expected_parent_id")
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.GetIdempotencyKey()) == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("idempotency key is required"))
	}
	dossier, created, err := research.AmendSecurityResearchDossier(ctx, namespace, &store.SecurityResearchDossier{
		RevisionID: revision.ID, Version: req.GetExpectedVersion(), ParentID: parentID, Content: content,
		ChangeSummary: req.GetChangeSummary(), Actor: actor, IdempotencyKey: req.GetIdempotencyKey(),
	})
	if err != nil {
		return nil, securityResearchError("amending security research dossier", err)
	}
	return &platform.AmendSecurityResearchDossierResponse{Dossier: securityResearchDossierProto(dossier), Created: created}, nil
}

func (s *Server) ListSecurityResearchHypotheses(ctx context.Context, req *platform.ListSecurityResearchHypothesesRequest) (*platform.ListSecurityResearchHypothesesResponse, error) {
	research, namespace, _, revision, err := s.securityResearchRevision(ctx, req.GetScope())
	if err != nil {
		return nil, err
	}
	values, err := research.ListSecurityResearchHypotheses(ctx, namespace, revision.ID)
	if err != nil {
		return nil, securityResearchError("listing security research hypotheses", err)
	}
	limit := securityResearchLimit(req.GetLimit())
	response := &platform.ListSecurityResearchHypothesesResponse{Truncated: len(values) > limit}
	if len(values) > limit {
		values = values[:limit]
	}
	for i := range values {
		response.Hypotheses = append(response.Hypotheses, securityResearchHypothesisProto(&values[i]))
	}
	return response, nil
}

func (s *Server) CreateSecurityResearchHypothesis(ctx context.Context, req *platform.CreateSecurityResearchHypothesisRequest) (*platform.CreateSecurityResearchHypothesisResponse, error) {
	actor, err := securityResearchActor(ctx)
	if err != nil {
		return nil, err
	}
	research, namespace, targetKey, revision, err := s.securityResearchRevision(ctx, req.GetScope())
	if err != nil {
		return nil, err
	}
	if err := s.authorizeSecurityResearchWrite(ctx, namespace, targetKey); err != nil {
		return nil, err
	}
	detail, err := securityResearchJSON(req.GetDetailJson(), "{}", "detail_json")
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.GetHypothesisKey()) == "" || strings.TrimSpace(req.GetTitle()) == "" || strings.TrimSpace(req.GetInvariant()) == "" || strings.TrimSpace(req.GetIdempotencyKey()) == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("hypothesis key, title, invariant, and idempotency key are required"))
	}
	value, created, err := research.CreateSecurityResearchHypothesis(ctx, namespace, &store.SecurityResearchHypothesis{
		RevisionID: revision.ID, HypothesisKey: req.GetHypothesisKey(), Title: req.GetTitle(), Invariant: req.GetInvariant(),
		Status: store.SecurityHypothesisProposed, Result: store.SecurityHypothesisResultPending, Detail: detail, Actor: actor, IdempotencyKey: req.GetIdempotencyKey(),
	})
	if err != nil {
		return nil, securityResearchError("creating security research hypothesis", err)
	}
	return &platform.CreateSecurityResearchHypothesisResponse{Hypothesis: securityResearchHypothesisProto(value), Created: created}, nil
}

func (s *Server) TransitionSecurityResearchHypothesis(ctx context.Context, req *platform.TransitionSecurityResearchHypothesisRequest) (*platform.SecurityResearchHypothesis, error) {
	actor, err := securityResearchActor(ctx)
	if err != nil {
		return nil, err
	}
	research, namespace, targetKey, revision, err := s.securityResearchRevision(ctx, req.GetScope())
	if err != nil {
		return nil, err
	}
	if err := s.authorizeSecurityResearchWrite(ctx, namespace, targetKey); err != nil {
		return nil, err
	}
	id, err := securityResearchID(req.GetHypothesisId(), "hypothesis_id")
	if err != nil {
		return nil, err
	}
	if err := securityResearchHypothesisForRevision(ctx, research, namespace, revision.ID, id); err != nil {
		return nil, err
	}
	detail, err := securityResearchJSON(req.GetDetailJson(), "{}", "detail_json")
	if err != nil {
		return nil, err
	}
	value, err := research.TransitionSecurityResearchHypothesis(ctx, namespace, id, store.SecurityHypothesisTransition{
		ExpectedVersion: req.GetExpectedVersion(), ToStatus: req.GetToStatus(), Result: req.GetResult(), Actor: actor,
		Rationale: req.GetRationale(), Detail: detail, IdempotencyKey: req.GetIdempotencyKey(),
	})
	if err != nil {
		return nil, securityResearchError("transitioning security research hypothesis", err)
	}
	return securityResearchHypothesisProto(value), nil
}

func (s *Server) ReopenSecurityResearchHypothesis(ctx context.Context, req *platform.ReopenSecurityResearchHypothesisRequest) (*platform.SecurityResearchHypothesis, error) {
	actor, err := securityResearchActor(ctx)
	if err != nil {
		return nil, err
	}
	research, namespace, targetKey, revision, err := s.securityResearchRevision(ctx, req.GetScope())
	if err != nil {
		return nil, err
	}
	if err := s.authorizeSecurityResearchWrite(ctx, namespace, targetKey); err != nil {
		return nil, err
	}
	id, err := securityResearchID(req.GetHypothesisId(), "hypothesis_id")
	if err != nil {
		return nil, err
	}
	if err := securityResearchHypothesisForRevision(ctx, research, namespace, revision.ID, id); err != nil {
		return nil, err
	}
	detail, err := securityResearchJSON(req.GetDetailJson(), "{}", "detail_json")
	if err != nil {
		return nil, err
	}
	value, err := research.ReopenSecurityResearchHypothesis(ctx, namespace, id, store.SecurityHypothesisTransition{
		ExpectedVersion: req.GetExpectedVersion(), Actor: actor, Rationale: req.GetRationale(), Detail: detail, IdempotencyKey: req.GetIdempotencyKey(),
	})
	if err != nil {
		return nil, securityResearchError("reopening security research hypothesis", err)
	}
	return securityResearchHypothesisProto(value), nil
}

func (s *Server) ListSecurityResearchCoverage(ctx context.Context, req *platform.ListSecurityResearchCoverageRequest) (*platform.ListSecurityResearchCoverageResponse, error) {
	research, namespace, _, revision, err := s.securityResearchRevision(ctx, req.GetScope())
	if err != nil {
		return nil, err
	}
	values, err := research.ListSecurityResearchCoverage(ctx, namespace, revision.ID)
	if err != nil {
		return nil, securityResearchError("listing security research coverage", err)
	}
	limit := securityResearchLimit(req.GetLimit())
	response := &platform.ListSecurityResearchCoverageResponse{Truncated: len(values) > limit}
	if len(values) > limit {
		values = values[:limit]
	}
	for i := range values {
		response.Coverage = append(response.Coverage, securityResearchCoverageProto(&values[i]))
	}
	return response, nil
}

func (s *Server) RecordSecurityResearchCoverage(ctx context.Context, req *platform.RecordSecurityResearchCoverageRequest) (*platform.RecordSecurityResearchCoverageResponse, error) {
	actor, err := securityResearchActor(ctx)
	if err != nil {
		return nil, err
	}
	research, namespace, targetKey, revision, err := s.securityResearchRevision(ctx, req.GetScope())
	if err != nil {
		return nil, err
	}
	if err := s.authorizeSecurityResearchWrite(ctx, namespace, targetKey); err != nil {
		return nil, err
	}
	hypothesisID, err := optionalSecurityResearchID(req.GetHypothesisId(), "hypothesis_id")
	if err != nil {
		return nil, err
	}
	if hypothesisID != nil {
		if err := securityResearchHypothesisForRevision(ctx, research, namespace, revision.ID, *hypothesisID); err != nil {
			return nil, err
		}
	}
	if !store.ValidSecurityCoverageDimension(req.GetDimension()) {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid coverage dimension %q", req.GetDimension()))
	}
	if !store.ValidSecurityCoverageVerdict(req.GetVerdict()) {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid coverage verdict %q", req.GetVerdict()))
	}
	bounds, err := securityResearchJSON(req.GetBoundsJson(), "{}", "bounds_json")
	if err != nil {
		return nil, err
	}
	evidence, err := securityResearchJSON(req.GetEvidenceJson(), "[]", "evidence_json")
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.GetSubjectKey()) == "" || strings.TrimSpace(req.GetIdempotencyKey()) == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("coverage subject and idempotency key are required"))
	}
	value, created, err := research.RecordSecurityResearchCoverage(ctx, namespace, &store.SecurityResearchCoverage{
		RevisionID: revision.ID, HypothesisID: hypothesisID, Dimension: req.GetDimension(), SubjectKey: req.GetSubjectKey(),
		Verdict: req.GetVerdict(), Bounds: bounds, Evidence: evidence, Actor: actor, IdempotencyKey: req.GetIdempotencyKey(),
	})
	if err != nil {
		return nil, securityResearchError("recording security research coverage", err)
	}
	return &platform.RecordSecurityResearchCoverageResponse{Coverage: securityResearchCoverageProto(value), Created: created}, nil
}

func (s *Server) ListSecurityResearchVariantSweeps(ctx context.Context, req *platform.ListSecurityResearchVariantSweepsRequest) (*platform.ListSecurityResearchVariantSweepsResponse, error) {
	research, namespace, _, revision, err := s.securityResearchRevision(ctx, req.GetScope())
	if err != nil {
		return nil, err
	}
	values, err := research.ListSecurityResearchVariantSweeps(ctx, namespace, revision.ID)
	if err != nil {
		return nil, securityResearchError("listing security research variant sweeps", err)
	}
	limit := securityResearchLimit(req.GetLimit())
	response := &platform.ListSecurityResearchVariantSweepsResponse{Truncated: len(values) > limit}
	if len(values) > limit {
		values = values[:limit]
	}
	for i := range values {
		response.Sweeps = append(response.Sweeps, securityResearchVariantSweepProto(&values[i]))
	}
	return response, nil
}

func (s *Server) CreateSecurityResearchVariantSweep(ctx context.Context, req *platform.CreateSecurityResearchVariantSweepRequest) (*platform.CreateSecurityResearchVariantSweepResponse, error) {
	actor, err := securityResearchActor(ctx)
	if err != nil {
		return nil, err
	}
	research, namespace, targetKey, revision, err := s.securityResearchRevision(ctx, req.GetScope())
	if err != nil {
		return nil, err
	}
	if err := s.authorizeSecurityResearchWrite(ctx, namespace, targetKey); err != nil {
		return nil, err
	}
	findingID, err := optionalSecurityResearchID(req.GetFindingId(), "finding_id")
	if err != nil {
		return nil, err
	}
	rootHypothesisID, err := optionalSecurityResearchID(req.GetRootHypothesisId(), "root_hypothesis_id")
	if err != nil {
		return nil, err
	}
	scopeJSON, err := securityResearchJSON(req.GetScopeJson(), "{}", "scope_json")
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.GetRootCause()) == "" || strings.TrimSpace(req.GetIdempotencyKey()) == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("root cause and idempotency key are required"))
	}
	status := req.GetStatus()
	if status == "" {
		status = store.SecurityVariantSweepPending
	}
	value, created, err := research.CreateSecurityResearchVariantSweep(ctx, namespace, &store.SecurityResearchVariantSweep{
		RevisionID: revision.ID, FindingID: findingID, RootHypothesisID: rootHypothesisID, RootCause: req.GetRootCause(),
		Scope: scopeJSON, Status: status, Result: json.RawMessage(`{}`), Actor: actor, IdempotencyKey: req.GetIdempotencyKey(),
	})
	if err != nil {
		return nil, securityResearchError("creating security research variant sweep", err)
	}
	return &platform.CreateSecurityResearchVariantSweepResponse{Sweep: securityResearchVariantSweepProto(value), Created: created}, nil
}

func (s *Server) CompleteSecurityResearchVariantSweep(ctx context.Context, req *platform.CompleteSecurityResearchVariantSweepRequest) (*platform.SecurityResearchVariantSweep, error) {
	actor, err := securityResearchActor(ctx)
	if err != nil {
		return nil, err
	}
	research, namespace, targetKey, revision, err := s.securityResearchRevision(ctx, req.GetScope())
	if err != nil {
		return nil, err
	}
	if err := s.authorizeSecurityResearchWrite(ctx, namespace, targetKey); err != nil {
		return nil, err
	}
	id, err := securityResearchID(req.GetSweepId(), "sweep_id")
	if err != nil {
		return nil, err
	}
	if _, err := securityResearchSweepForRevision(ctx, research, namespace, revision.ID, id); err != nil {
		return nil, err
	}
	result, err := securityResearchJSON(req.GetResultJson(), "{}", "result_json")
	if err != nil {
		return nil, err
	}
	value, err := research.CompleteSecurityResearchVariantSweep(ctx, namespace, id, req.GetStatus(), result, actor, req.GetIdempotencyKey())
	if err != nil {
		return nil, securityResearchError("completing security research variant sweep", err)
	}
	return securityResearchVariantSweepProto(value), nil
}

func (s *Server) ListSecurityResearchSubmissions(ctx context.Context, req *platform.ListSecurityResearchSubmissionsRequest) (*platform.ListSecurityResearchSubmissionsResponse, error) {
	research, namespace, _, revision, err := s.securityResearchRevision(ctx, req.GetScope())
	if err != nil {
		return nil, err
	}
	values, err := research.ListSecurityResearchSubmissions(ctx, namespace, revision.ID)
	if err != nil {
		return nil, securityResearchError("listing security research submissions", err)
	}
	limit := securityResearchLimit(req.GetLimit())
	response := &platform.ListSecurityResearchSubmissionsResponse{Truncated: len(values) > limit}
	if len(values) > limit {
		values = values[:limit]
	}
	for i := range values {
		response.Submissions = append(response.Submissions, securityResearchSubmissionProto(&values[i]))
	}
	return response, nil
}

func (s *Server) ListSecuritySubmissionOutcomeHistory(ctx context.Context, req *platform.ListSecuritySubmissionOutcomeHistoryRequest) (*platform.ListSecuritySubmissionOutcomeHistoryResponse, error) {
	research, namespace, _, revision, err := s.securityResearchRevision(ctx, req.GetScope())
	if err != nil {
		return nil, err
	}
	id, err := securityResearchID(req.GetSubmissionId(), "submission_id")
	if err != nil {
		return nil, err
	}
	values, err := research.ListSecuritySubmissionOutcomeEvents(ctx, namespace, revision.ID, id)
	if err != nil {
		return nil, securityResearchError("listing security submission outcome history", err)
	}
	limit := securityResearchLimit(req.GetLimit())
	response := &platform.ListSecuritySubmissionOutcomeHistoryResponse{Truncated: len(values) > limit}
	if len(values) > limit {
		values = values[:limit]
	}
	for i := range values {
		response.Events = append(response.Events, securitySubmissionOutcomeEventProto(&values[i]))
	}
	return response, nil
}

func (s *Server) RecordSecuritySubmissionOutcome(ctx context.Context, req *platform.RecordSecuritySubmissionOutcomeRequest) (*platform.RecordSecuritySubmissionOutcomeResponse, error) {
	return s.recordSecuritySubmissionOutcome(ctx, req.GetScope(), req.GetSubmissionId(), req.GetOutcome(), req.GetExternalReference(), req.GetRationale(), req.GetIdempotencyKey(), nil)
}

func (s *Server) CorrectSecuritySubmissionOutcome(ctx context.Context, req *platform.CorrectSecuritySubmissionOutcomeRequest) (*platform.RecordSecuritySubmissionOutcomeResponse, error) {
	if req.GetCorrectionOf() <= 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("correction_of is required"))
	}
	correctionOf := req.GetCorrectionOf()
	return s.recordSecuritySubmissionOutcome(ctx, req.GetScope(), req.GetSubmissionId(), req.GetOutcome(), req.GetExternalReference(), req.GetRationale(), req.GetIdempotencyKey(), &correctionOf)
}

func (s *Server) recordSecuritySubmissionOutcome(ctx context.Context, scope *platform.SecurityResearchScope, submissionID, outcome, externalReference, rationale, idempotencyKey string, correctionOf *int64) (*platform.RecordSecuritySubmissionOutcomeResponse, error) {
	actor, err := securityResearchActor(ctx)
	if err != nil {
		return nil, err
	}
	research, namespace, targetKey, revision, err := s.securityResearchRevision(ctx, scope)
	if err != nil {
		return nil, err
	}
	if err := s.authorizeSecurityResearchWrite(ctx, namespace, targetKey); err != nil {
		return nil, err
	}
	id, err := securityResearchID(submissionID, "submission_id")
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(outcome) == "" || strings.TrimSpace(idempotencyKey) == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("outcome and idempotency key are required"))
	}
	value, created, err := research.RecordSecuritySubmissionOutcome(ctx, namespace, id, store.SecuritySubmissionOutcomeInput{
		RevisionID: revision.ID, Outcome: outcome, ExternalReference: externalReference, Rationale: rationale, Actor: actor,
		CorrectionOf: correctionOf, IdempotencyKey: idempotencyKey,
	})
	if err != nil {
		return nil, securityResearchError("recording security submission outcome", err)
	}
	return &platform.RecordSecuritySubmissionOutcomeResponse{Outcome: securitySubmissionOutcomeProto(value), Created: created}, nil
}

func (s *Server) ListSecuritySubmissionQueue(ctx context.Context, req *platform.ListSecuritySubmissionQueueRequest) (*platform.ListSecuritySubmissionQueueResponse, error) {
	research, err := s.securityResearchStore()
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
	items, err := research.ListSecuritySubmissionQueue(ctx, namespace, hidden)
	if err != nil {
		return nil, securityResearchError("listing security submission queue", err)
	}
	limit := securityResearchLimit(req.GetLimit())
	response := &platform.ListSecuritySubmissionQueueResponse{}
	for i := range items {
		// Hidden scans are excluded in the store; the predicate stays as the
		// second layer for stores that cannot push the exclusion down.
		if !visible(items[i].ScanName) {
			continue
		}
		if len(response.Items) == limit {
			response.Truncated = true
			break
		}
		response.Items = append(response.Items, securitySubmissionQueueItemProto(&items[i]))
	}
	return response, nil
}

func (s *Server) MarkSecuritySubmissionSubmitted(ctx context.Context, req *platform.MarkSecuritySubmissionSubmittedRequest) (*platform.MarkSecuritySubmissionSubmittedResponse, error) {
	actor, err := securityResearchActor(ctx)
	if err != nil {
		return nil, err
	}
	research, err := s.securityResearchStore()
	if err != nil {
		return nil, err
	}
	namespace, err := s.authorizeRequestNamespace(ctx, req.GetNamespace(), nil)
	if err != nil {
		return nil, err
	}
	id, err := securityResearchID(req.GetSubmissionId(), "submission_id")
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.GetProgram()) == "" || strings.TrimSpace(req.GetIdempotencyKey()) == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("program and idempotency key are required"))
	}
	visible, _, err := s.securityScanVisibility(ctx, namespace)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	existing, err := research.GetSecurityResearchSubmission(ctx, namespace, id)
	if err != nil {
		return nil, securityResearchError("getting security research submission", err)
	}
	if !visible(existing.TargetKey) {
		return nil, connect.NewError(connect.CodeNotFound, store.ErrSecurityResearchSubmissionNotFound)
	}
	if err := s.authorizeSecurityResearchWrite(ctx, namespace, existing.TargetKey); err != nil {
		return nil, err
	}
	handoff := store.SecuritySubmissionHandoff{Program: req.GetProgram(), ExternalReference: req.GetExternalReference(), Actor: actor}
	if req.GetSubmittedAt() != nil {
		handoff.SubmittedAt = req.GetSubmittedAt().AsTime()
	}
	value, err := research.MarkSecurityResearchSubmissionSubmitted(ctx, namespace, id, handoff)
	if err != nil {
		return nil, securityResearchError("marking security submission submitted", err)
	}
	return &platform.MarkSecuritySubmissionSubmittedResponse{Submission: securityResearchSubmissionProto(value)}, nil
}

func (s *Server) GetSecuritySubmissionPrecisionRollup(ctx context.Context, req *platform.GetSecuritySubmissionPrecisionRollupRequest) (*platform.SecuritySubmissionPrecisionRollup, error) {
	research, err := s.securityResearchStore()
	if err != nil {
		return nil, err
	}
	namespace, err := s.authorizeRequestNamespace(ctx, req.GetNamespace(), nil)
	if err != nil {
		return nil, err
	}
	_, hidden, err := s.securityScanVisibility(ctx, namespace)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	var since *time.Time
	if req.GetSince() != nil {
		at := req.GetSince().AsTime()
		since = &at
	}
	rollup, err := research.GetSecuritySubmissionPrecisionRollup(ctx, namespace, since, hidden)
	if err != nil {
		return nil, securityResearchError("calculating security submission precision rollup", err)
	}
	response := &platform.SecuritySubmissionPrecisionRollup{Total: securitySubmissionPrecisionProto(&rollup.Total)}
	for i := range rollup.ByProgram {
		response.ByProgram = append(response.ByProgram, &platform.SecuritySubmissionPrecisionGroup{Key: rollup.ByProgram[i].Key, Precision: securitySubmissionPrecisionProto(&rollup.ByProgram[i].Precision)})
	}
	for i := range rollup.ByWorkflow {
		response.ByWorkflow = append(response.ByWorkflow, &platform.SecuritySubmissionPrecisionGroup{Key: rollup.ByWorkflow[i].Key, Precision: securitySubmissionPrecisionProto(&rollup.ByWorkflow[i].Precision)})
	}
	return response, nil
}

func securitySubmissionQueueItemProto(in *store.SecuritySubmissionQueueItem) *platform.SecuritySubmissionQueueItem {
	out := &platform.SecuritySubmissionQueueItem{
		FindingId: in.FindingID.String(), Namespace: in.Namespace, ScanName: in.ScanName, RunName: in.RunName, Title: in.Title, Severity: in.Severity,
		FindingStatus: in.FindingStatus, Fingerprint: in.Fingerprint, Repository: in.Repository, BundleReadyAt: timestamppb.New(in.BundleReadyAt), BundleFilename: in.BundleFilename,
		SubmissionStatus: in.SubmissionStatus, TargetKey: in.TargetKey, Revision: in.Revision, Workflow: in.Workflow, Program: in.Program, ExternalReference: in.ExternalReference,
		SubmittedBy: in.SubmittedBy, LatestOutcome: in.Outcome,
	}
	if in.SubmissionID != nil {
		out.SubmissionId = in.SubmissionID.String()
	}
	if in.SubmittedAt != nil {
		out.SubmittedAt = timestamppb.New(*in.SubmittedAt)
	}
	if in.OutcomeRecordedAt != nil {
		out.LatestOutcomeAt = timestamppb.New(*in.OutcomeRecordedAt)
	}
	return out
}

func securityResearchDossierProto(in *store.SecurityResearchDossier) *platform.SecurityResearchDossier {
	out := &platform.SecurityResearchDossier{Id: in.ID.String(), RevisionId: in.RevisionID.String(), Version: in.Version, ContentJson: string(in.Content), ChangeSummary: in.ChangeSummary, Actor: in.Actor, IdempotencyKey: in.IdempotencyKey, CreatedAt: timestamppb.New(in.CreatedAt)}
	if in.ParentID != nil {
		out.ParentId = in.ParentID.String()
	}
	return out
}

func securityResearchHypothesisProto(in *store.SecurityResearchHypothesis) *platform.SecurityResearchHypothesis {
	return &platform.SecurityResearchHypothesis{Id: in.ID.String(), RevisionId: in.RevisionID.String(), HypothesisKey: in.HypothesisKey, Title: in.Title, Invariant: in.Invariant, Status: in.Status, Result: in.Result, DetailJson: string(in.Detail), Version: in.Version, IdempotencyKey: in.IdempotencyKey, CreatedAt: timestamppb.New(in.CreatedAt), UpdatedAt: timestamppb.New(in.UpdatedAt)}
}

func securityResearchCoverageProto(in *store.SecurityResearchCoverage) *platform.SecurityResearchCoverage {
	out := &platform.SecurityResearchCoverage{Id: in.ID.String(), RevisionId: in.RevisionID.String(), Dimension: in.Dimension, SubjectKey: in.SubjectKey, Verdict: in.Verdict, BoundsJson: string(in.Bounds), EvidenceJson: string(in.Evidence), Actor: in.Actor, IdempotencyKey: in.IdempotencyKey, CreatedAt: timestamppb.New(in.CreatedAt)}
	if in.HypothesisID != nil {
		out.HypothesisId = in.HypothesisID.String()
	}
	return out
}

func securityResearchVariantSweepProto(in *store.SecurityResearchVariantSweep) *platform.SecurityResearchVariantSweep {
	out := &platform.SecurityResearchVariantSweep{Id: in.ID.String(), RevisionId: in.RevisionID.String(), RootCause: in.RootCause, ScopeJson: string(in.Scope), Status: in.Status, ResultJson: string(in.Result), IdempotencyKey: in.IdempotencyKey, CreatedAt: timestamppb.New(in.CreatedAt)}
	if in.FindingID != nil {
		out.FindingId = in.FindingID.String()
	}
	if in.RootHypothesisID != nil {
		out.RootHypothesisId = in.RootHypothesisID.String()
	}
	if in.CompletedAt != nil {
		out.CompletedAt = timestamppb.New(*in.CompletedAt)
	}
	return out
}

func securityResearchSubmissionProto(in *store.SecurityResearchSubmission) *platform.SecurityResearchSubmission {
	out := &platform.SecurityResearchSubmission{Id: in.ID.String(), FindingFingerprint: in.FindingFingerprint, FindingTitle: in.FindingTitle, Workflow: in.Workflow, CandidateKey: in.CandidateKey, Rank: in.Rank, Status: in.Status, CreatedAt: timestamppb.New(in.CreatedAt), LatestOutcome: in.Outcome, Program: in.Program, ExternalReference: in.ExternalReference, SubmittedBy: in.SubmittedBy}
	if in.FindingID != nil {
		out.FindingId = in.FindingID.String()
	}
	if in.PackagedAt != nil {
		out.PackagedAt = timestamppb.New(*in.PackagedAt)
	}
	if in.SubmittedAt != nil {
		out.SubmittedAt = timestamppb.New(*in.SubmittedAt)
	}
	if in.OutcomeRecordedAt != nil {
		out.LatestOutcomeAt = timestamppb.New(*in.OutcomeRecordedAt)
	}
	return out
}

func securitySubmissionOutcomeEventProto(in *store.SecuritySubmissionOutcomeEvent) *platform.SecuritySubmissionOutcomeEvent {
	out := &platform.SecuritySubmissionOutcomeEvent{Id: in.ID, SubmissionId: in.SubmissionID.String(), Outcome: in.Outcome, ExternalReference: in.ExternalReference, Rationale: in.Rationale, Actor: in.Actor, IdempotencyKey: in.IdempotencyKey, CreatedAt: timestamppb.New(in.CreatedAt)}
	if in.CorrectionOf != nil {
		out.CorrectionOf = *in.CorrectionOf
	}
	return out
}

func securitySubmissionOutcomeProto(in *store.SecuritySubmissionOutcome) *platform.SecuritySubmissionOutcome {
	return &platform.SecuritySubmissionOutcome{SubmissionId: in.SubmissionID.String(), EventId: in.EventID, Outcome: in.Outcome, ExternalReference: in.ExternalReference, RecordedAt: timestamppb.New(in.RecordedAt)}
}

func securitySubmissionPrecisionProto(in *store.SecuritySubmissionPrecision) *platform.SecuritySubmissionPrecision {
	if in == nil {
		return &platform.SecuritySubmissionPrecision{}
	}
	return &platform.SecuritySubmissionPrecision{Submitted: in.Submitted, Accepted: in.Accepted, Duplicate: in.Duplicate, Informative: in.Informative, Rejected: in.Rejected, Resolved: in.Resolved}
}

func (h *PlatformServiceConnectHandler) GetSecurityCampaignResearchStatus(ctx context.Context, req *connect.Request[platform.GetSecurityCampaignResearchStatusRequest]) (*connect.Response[platform.SecurityCampaignResearchStatus], error) {
	resp, err := h.srv.GetSecurityCampaignResearchStatus(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}
func (h *PlatformServiceConnectHandler) GetSecurityResearchDossier(ctx context.Context, req *connect.Request[platform.GetSecurityResearchDossierRequest]) (*connect.Response[platform.SecurityResearchDossier], error) {
	resp, err := h.srv.GetSecurityResearchDossier(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}
func (h *PlatformServiceConnectHandler) AmendSecurityResearchDossier(ctx context.Context, req *connect.Request[platform.AmendSecurityResearchDossierRequest]) (*connect.Response[platform.AmendSecurityResearchDossierResponse], error) {
	resp, err := h.srv.AmendSecurityResearchDossier(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}
func (h *PlatformServiceConnectHandler) ListSecurityResearchHypotheses(ctx context.Context, req *connect.Request[platform.ListSecurityResearchHypothesesRequest]) (*connect.Response[platform.ListSecurityResearchHypothesesResponse], error) {
	resp, err := h.srv.ListSecurityResearchHypotheses(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}
func (h *PlatformServiceConnectHandler) CreateSecurityResearchHypothesis(ctx context.Context, req *connect.Request[platform.CreateSecurityResearchHypothesisRequest]) (*connect.Response[platform.CreateSecurityResearchHypothesisResponse], error) {
	resp, err := h.srv.CreateSecurityResearchHypothesis(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}
func (h *PlatformServiceConnectHandler) TransitionSecurityResearchHypothesis(ctx context.Context, req *connect.Request[platform.TransitionSecurityResearchHypothesisRequest]) (*connect.Response[platform.SecurityResearchHypothesis], error) {
	resp, err := h.srv.TransitionSecurityResearchHypothesis(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}
func (h *PlatformServiceConnectHandler) ReopenSecurityResearchHypothesis(ctx context.Context, req *connect.Request[platform.ReopenSecurityResearchHypothesisRequest]) (*connect.Response[platform.SecurityResearchHypothesis], error) {
	resp, err := h.srv.ReopenSecurityResearchHypothesis(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}
func (h *PlatformServiceConnectHandler) ListSecurityResearchCoverage(ctx context.Context, req *connect.Request[platform.ListSecurityResearchCoverageRequest]) (*connect.Response[platform.ListSecurityResearchCoverageResponse], error) {
	resp, err := h.srv.ListSecurityResearchCoverage(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}
func (h *PlatformServiceConnectHandler) RecordSecurityResearchCoverage(ctx context.Context, req *connect.Request[platform.RecordSecurityResearchCoverageRequest]) (*connect.Response[platform.RecordSecurityResearchCoverageResponse], error) {
	resp, err := h.srv.RecordSecurityResearchCoverage(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}
func (h *PlatformServiceConnectHandler) ListSecurityResearchVariantSweeps(ctx context.Context, req *connect.Request[platform.ListSecurityResearchVariantSweepsRequest]) (*connect.Response[platform.ListSecurityResearchVariantSweepsResponse], error) {
	resp, err := h.srv.ListSecurityResearchVariantSweeps(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}
func (h *PlatformServiceConnectHandler) CreateSecurityResearchVariantSweep(ctx context.Context, req *connect.Request[platform.CreateSecurityResearchVariantSweepRequest]) (*connect.Response[platform.CreateSecurityResearchVariantSweepResponse], error) {
	resp, err := h.srv.CreateSecurityResearchVariantSweep(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}
func (h *PlatformServiceConnectHandler) CompleteSecurityResearchVariantSweep(ctx context.Context, req *connect.Request[platform.CompleteSecurityResearchVariantSweepRequest]) (*connect.Response[platform.SecurityResearchVariantSweep], error) {
	resp, err := h.srv.CompleteSecurityResearchVariantSweep(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}
func (h *PlatformServiceConnectHandler) ListSecurityResearchSubmissions(ctx context.Context, req *connect.Request[platform.ListSecurityResearchSubmissionsRequest]) (*connect.Response[platform.ListSecurityResearchSubmissionsResponse], error) {
	resp, err := h.srv.ListSecurityResearchSubmissions(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}
func (h *PlatformServiceConnectHandler) ListSecuritySubmissionOutcomeHistory(ctx context.Context, req *connect.Request[platform.ListSecuritySubmissionOutcomeHistoryRequest]) (*connect.Response[platform.ListSecuritySubmissionOutcomeHistoryResponse], error) {
	resp, err := h.srv.ListSecuritySubmissionOutcomeHistory(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}
func (h *PlatformServiceConnectHandler) RecordSecuritySubmissionOutcome(ctx context.Context, req *connect.Request[platform.RecordSecuritySubmissionOutcomeRequest]) (*connect.Response[platform.RecordSecuritySubmissionOutcomeResponse], error) {
	resp, err := h.srv.RecordSecuritySubmissionOutcome(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}
func (h *PlatformServiceConnectHandler) CorrectSecuritySubmissionOutcome(ctx context.Context, req *connect.Request[platform.CorrectSecuritySubmissionOutcomeRequest]) (*connect.Response[platform.RecordSecuritySubmissionOutcomeResponse], error) {
	resp, err := h.srv.CorrectSecuritySubmissionOutcome(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}
func (h *PlatformServiceConnectHandler) ListSecuritySubmissionQueue(ctx context.Context, req *connect.Request[platform.ListSecuritySubmissionQueueRequest]) (*connect.Response[platform.ListSecuritySubmissionQueueResponse], error) {
	resp, err := h.srv.ListSecuritySubmissionQueue(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}
func (h *PlatformServiceConnectHandler) MarkSecuritySubmissionSubmitted(ctx context.Context, req *connect.Request[platform.MarkSecuritySubmissionSubmittedRequest]) (*connect.Response[platform.MarkSecuritySubmissionSubmittedResponse], error) {
	resp, err := h.srv.MarkSecuritySubmissionSubmitted(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}
func (h *PlatformServiceConnectHandler) GetSecuritySubmissionPrecisionRollup(ctx context.Context, req *connect.Request[platform.GetSecuritySubmissionPrecisionRollupRequest]) (*connect.Response[platform.SecuritySubmissionPrecisionRollup], error) {
	resp, err := h.srv.GetSecuritySubmissionPrecisionRollup(ctx, req.Msg)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}
