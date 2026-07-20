package dashboard

import (
	"context"
	"fmt"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/gratefulagents/gratefulagents/internal/store"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
)

const (
	observabilityMaxBuckets      = 2000
	observabilityMaxRangeSeconds = int64(90 * 24 * 60 * 60)
)

type observabilityStore interface {
	GetObservabilityOverview(context.Context, store.ObservabilityQuery) (*store.ObservabilityOverview, error)
}

func (s *Server) GetObservabilityOverview(ctx context.Context, req *platform.GetObservabilityOverviewRequest) (*platform.ObservabilityOverviewResponse, error) {
	analytics, ok := s.stateStore.(observabilityStore)
	if !ok {
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("historical observability is unavailable"))
	}
	namespace, err := s.authorizeRequestNamespace(ctx, req.GetNamespace(), nil)
	if err != nil {
		return nil, err
	}
	if req.GetStart() == nil || req.GetEnd() == nil || !req.GetStart().IsValid() || !req.GetEnd().IsValid() {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("valid start and end timestamps are required"))
	}
	start, end := req.GetStart().AsTime().UTC(), req.GetEnd().AsTime().UTC()
	bucketSeconds := req.GetBucketSeconds()
	if !start.Before(end) || bucketSeconds < 60 || bucketSeconds > observabilityMaxRangeSeconds || end.Sub(start) > 90*24*time.Hour || (end.Unix()-start.Unix()+bucketSeconds-1)/bucketSeconds > observabilityMaxBuckets {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("range must be positive, at most 90 days, with 60-second or larger buckets and at most %d buckets", observabilityMaxBuckets))
	}

	visible, err := s.ListAgentRuns(ctx, &platform.ListAgentRunsRequest{Namespace: namespace})
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(visible.Runs))
	for _, run := range visible.Runs {
		names = append(names, run.GetName())
	}
	overview, err := analytics.GetObservabilityOverview(ctx, store.ObservabilityQuery{Namespace: namespace, Start: start, End: end, BucketSeconds: bucketSeconds, AgentRunNames: names})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("querying observability: %w", err))
	}
	return observabilityOverviewProto(overview), nil
}

func observabilityOverviewProto(in *store.ObservabilityOverview) *platform.ObservabilityOverviewResponse {
	out := &platform.ObservabilityOverviewResponse{
		Totals: observabilityTotalsProto(in.Totals),
		DataCompleteness: &platform.ObservabilityDataCompleteness{
			Sessions: in.Completeness.Sessions, SessionsWithMetrics: in.Completeness.SessionsWithMetrics,
			SessionsWithActivity: in.Completeness.SessionsWithActivity,
			MetricsComplete:      in.Completeness.MetricsComplete, ActivityComplete: in.Completeness.ActivityComplete,
			ActivityTruncated: in.Completeness.ActivityTruncated,
		},
		CoverageWarnings: []string{
			"Activity-derived counts and generation-attributed usage are best-effort because the Postgres event tee may omit events.",
			"Only currently visible AgentRuns are included; deleted historical runs are excluded.",
			"Run snapshot totals include runs created in the selected range; generation series show when model usage was recorded.",
			"Run metadata totals may overlap when parent and child AgentRuns both contain rolled-up usage.",
			"Model usage recorded outside generation attempts (for example compaction summarization) appears in run snapshot totals but not in generation series.",
		},
	}
	if in.Completeness.ActivityTruncated {
		out.CoverageWarnings = append(out.CoverageWarnings, "Metric activity exceeded the per-query event limit; charts use the most recent 50,000 metric events.")
	}
	for _, b := range in.Buckets {
		out.Buckets = append(out.Buckets, &platform.ObservabilityBucket{Start: timestamppb.New(b.Start), Totals: observabilityTotalsProto(b.Totals)})
	}
	convert := func(values []store.ObservabilityBreakdown) []*platform.ObservabilityBreakdown {
		if len(values) > 100 {
			values = values[:100]
		}
		result := make([]*platform.ObservabilityBreakdown, 0, len(values))
		for _, v := range values {
			result = append(result, &platform.ObservabilityBreakdown{Name: v.Name, Count: v.Count, Errors: v.Errors, CostUsd: v.CostUSD, InputTokens: v.InputTokens, OutputTokens: v.OutputTokens, AverageDurationMs: v.AverageDurationMS, P95DurationMs: v.P95DurationMS})
		}
		return result
	}
	out.Tools = convert(in.Tools)
	out.Subagents = convert(in.Subagents)
	out.Models = convert(in.Models)
	return out
}

func observabilityTotalsProto(v store.ObservabilityTotals) *platform.ObservabilityTotals {
	return &platform.ObservabilityTotals{
		Runs: v.Runs, CostUsd: v.CostUSD, InputTokens: v.InputTokens, OutputTokens: v.OutputTokens,
		ToolCalls: v.ToolCalls, ToolErrors: v.ToolErrors, Subagents: v.Subagents,
		SubagentFailures: v.SubagentFailures, LlmAttempts: v.LLMAttempts, LlmFailures: v.LLMFailures,
		Compactions: v.Compactions, TokensReclaimed: v.TokensReclaimed,
		GenerationCostUsd: v.GenerationCostUSD, GenerationInputTokens: v.GenerationInputTokens,
		GenerationOutputTokens: v.GenerationOutputTokens,
	}
}
