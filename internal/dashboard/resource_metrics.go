package dashboard

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/store"
	"github.com/gratefulagents/gratefulagents/internal/store/sessionclient"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
	"google.golang.org/protobuf/proto"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type resourceMetricsKey struct {
	Namespace string
	Kind      string
	Name      string
}

const resourceMetricsCacheTTL = 5 * time.Second

func (s *Server) listResourceMetrics(ctx context.Context, namespace string) (map[resourceMetricsKey]*platform.ProjectMetrics, error) {
	s.metricsCacheMu.Lock()
	if entry, ok := s.metricsCache[namespace]; ok && time.Since(entry.at) < resourceMetricsCacheTTL {
		data := entry.data
		s.metricsCacheMu.Unlock()
		return data, nil
	}
	s.metricsCacheMu.Unlock()

	runs := &platformv1alpha1.AgentRunList{}
	var opts []client.ListOption
	if namespace != "" {
		opts = append(opts, client.InNamespace(namespace))
	}
	if err := s.k8sClient.List(ctx, runs, opts...); err != nil {
		return nil, mapK8sError("list AgentRuns for resource metrics", err)
	}

	// Load metrics from Postgres (source of truth) to overlay on CRD data.
	var pgMetrics map[string]store.SessionMetricsEntry
	if s.stateStore != nil {
		if entries, err := s.stateStore.ListAllSessionMetrics(ctx); err == nil {
			pgMetrics = make(map[string]store.SessionMetricsEntry, len(entries))
			for _, e := range entries {
				pgMetrics[e.AgentRunNS+"/"+e.AgentRunName] = e
			}
		}
	}

	result := buildResourceMetricsMap(runs.Items, pgMetrics)

	// The cached map is shared read-only: resourceMetrics() clones entries
	// before handing them to callers.
	s.metricsCacheMu.Lock()
	if s.metricsCache == nil {
		s.metricsCache = make(map[string]*resourceMetricsCacheEntry)
	}
	s.metricsCache[namespace] = &resourceMetricsCacheEntry{at: time.Now(), data: result}
	s.metricsCacheMu.Unlock()

	return result, nil
}

func buildResourceMetricsMap(runs []platformv1alpha1.AgentRun, pgMetrics map[string]store.SessionMetricsEntry) map[resourceMetricsKey]*platform.ProjectMetrics {
	byResource := make(map[resourceMetricsKey]*platform.ProjectMetrics)

	for i := range runs {
		run := &runs[i]
		key, ok := resourceMetricsKeyForRun(run)
		if !ok {
			continue
		}

		m, exists := byResource[key]
		if !exists {
			m = &platform.ProjectMetrics{}
			byResource[key] = m
		}

		m.TotalRuns++

		switch run.Status.Phase {
		case platformv1alpha1.AgentRunPhaseSucceeded:
			m.SuccessfulRuns++
		case platformv1alpha1.AgentRunPhaseFailed:
			m.FailedRuns++
		case platformv1alpha1.AgentRunPhaseRunning, platformv1alpha1.AgentRunPhaseProvisioning,
			platformv1alpha1.AgentRunPhaseQuestion, platformv1alpha1.AgentRunPhaseBlocked:
			m.RunningRuns++
		}

		// Prefer Postgres metrics (source of truth); fall back to CRD.
		pgKey := run.Namespace + "/" + run.Name
		if pg, ok := pgMetrics[pgKey]; ok {
			m.TotalCostUsd += pg.CostUSD
			m.TotalInputTokens += pg.InputTokens
			m.TotalOutputTokens += pg.OutputTokens
			m.TotalToolCalls += pg.ToolCallCount
		} else if run.Status.Metrics != nil {
			if cost, err := strconv.ParseFloat(run.Status.Metrics.CostUsd, 64); err == nil {
				m.TotalCostUsd += cost
			}
			m.TotalInputTokens += run.Status.Metrics.InputTokens
			m.TotalOutputTokens += run.Status.Metrics.OutputTokens
			m.TotalToolCalls += run.Status.Metrics.ToolCallCount
		}

		if run.Status.StartedAt != nil {
			ts := run.Status.StartedAt.Unix()
			if ts > m.LastRunAtUnix {
				m.LastRunAtUnix = ts
			}
		} else if ts := run.CreationTimestamp.Unix(); ts > m.LastRunAtUnix {
			m.LastRunAtUnix = ts
		}
	}

	for _, m := range byResource {
		if m.TotalRuns > 0 {
			m.AverageCostPerRun = m.TotalCostUsd / float64(m.TotalRuns)
		}
	}

	return byResource
}

func resourceMetricsKeyForRun(run *platformv1alpha1.AgentRun) (resourceMetricsKey, bool) {
	if run == nil || run.Spec.Context == nil || run.Spec.Context.ProjectRef == nil {
		return resourceMetricsKey{}, false
	}
	ref := run.Spec.Context.ProjectRef
	if strings.TrimSpace(ref.Kind) == "" || strings.TrimSpace(ref.Name) == "" {
		return resourceMetricsKey{}, false
	}
	return resourceMetricsKey{
		Namespace: run.Namespace,
		Kind:      ref.Kind,
		Name:      ref.Name,
	}, true
}

func resourceMetrics(metrics map[resourceMetricsKey]*platform.ProjectMetrics, namespace, kind, name string) *platform.ProjectMetrics {
	key := resourceMetricsKey{Namespace: namespace, Kind: kind, Name: name}
	if m, ok := metrics[key]; ok {
		return cloneProjectMetrics(m)
	}
	return &platform.ProjectMetrics{}
}

func cloneProjectMetrics(m *platform.ProjectMetrics) *platform.ProjectMetrics {
	if m == nil {
		return nil
	}
	return proto.Clone(m).(*platform.ProjectMetrics)
}

func resourceMetricsVersion(resourceVersion string, metrics *platform.ProjectMetrics) string {
	if metrics == nil {
		return resourceVersion
	}
	parts := []string{
		resourceVersion,
		strconv.Itoa(int(metrics.TotalRuns)),
		strconv.Itoa(int(metrics.SuccessfulRuns)),
		strconv.Itoa(int(metrics.FailedRuns)),
		strconv.Itoa(int(metrics.RunningRuns)),
		strconv.FormatFloat(metrics.TotalCostUsd, 'f', 6, 64),
		strconv.FormatFloat(metrics.AverageCostPerRun, 'f', 6, 64),
		strconv.FormatInt(metrics.TotalInputTokens, 10),
		strconv.FormatInt(metrics.TotalOutputTokens, 10),
		strconv.Itoa(int(metrics.TotalToolCalls)),
		strconv.FormatInt(metrics.LastRunAtUnix, 10),
	}
	return strings.Join(parts, "/")
}

func (s *Server) projectProto(ctx context.Context, p *triggersv1alpha1.Project, metrics map[resourceMetricsKey]*platform.ProjectMetrics) *platform.Project {
	pb := k8sProjectToProto(p)
	pb.Metrics = resourceMetrics(metrics, p.Namespace, "Project", p.Name)
	return s.enrichProjectProto(ctx, pb)
}

func (s *Server) linearProjectProto(ctx context.Context, lp *triggersv1alpha1.LinearProject, metrics map[resourceMetricsKey]*platform.ProjectMetrics) *platform.LinearProject {
	pb := k8sLinearProjectToProto(lp)
	pb.Metrics = resourceMetrics(metrics, lp.Namespace, "LinearProject", lp.Name)
	pb.PermissionMode, pb.EgressMode, pb.McpPolicyDefaultAction, pb.McpPolicyAllowedServers =
		s.resolveTriggerPolicyModes(ctx, lp.Namespace, lp.Spec.Defaults)
	return pb
}

func (s *Server) githubRepositoryProto(ctx context.Context, gh *triggersv1alpha1.GitHubRepository, metrics map[resourceMetricsKey]*platform.ProjectMetrics) *platform.GitHubRepository {
	pb := k8sGitHubRepositoryToProto(gh)
	pb.Metrics = resourceMetrics(metrics, gh.Namespace, "GitHubRepository", gh.Name)
	pb.PermissionMode, pb.EgressMode, pb.McpPolicyDefaultAction, pb.McpPolicyAllowedServers =
		s.resolveTriggerPolicyModes(ctx, gh.Namespace, gh.Spec.Defaults)
	if gh.Spec.ReviewLoop != nil && gh.Spec.ReviewLoop.ReviewerDefaults != nil {
		pb.ReviewerPermissionMode, pb.ReviewerEgressMode, pb.ReviewerMcpPolicyDefaultAction, pb.ReviewerMcpPolicyAllowedServers =
			s.resolveTriggerPolicyModes(ctx, gh.Namespace, *gh.Spec.ReviewLoop.ReviewerDefaults)
	}
	return pb
}

func (s *Server) cronProto(ctx context.Context, cr *triggersv1alpha1.Cron, metrics map[resourceMetricsKey]*platform.ProjectMetrics) *platform.Cron {
	pb := k8sCronToProto(cr)
	pb.Metrics = resourceMetrics(metrics, cr.Namespace, "Cron", cr.Name)
	pb.PermissionMode, pb.EgressMode, pb.McpPolicyDefaultAction, pb.McpPolicyAllowedServers =
		s.resolveTriggerPolicyModes(ctx, cr.Namespace, cr.Spec.Defaults)
	return pb
}

// parseSessionMetrics extracts SessionMetrics from the session's JSONB metadata.
func parseSessionMetrics(sess *store.Session) (sessionclient.SessionMetrics, bool) {
	if sess == nil || len(sess.Metadata) == 0 {
		return sessionclient.SessionMetrics{}, false
	}
	var envelope struct {
		Metrics sessionclient.SessionMetrics `json:"metrics"`
	}
	if err := json.Unmarshal(sess.Metadata, &envelope); err != nil {
		return sessionclient.SessionMetrics{}, false
	}
	// Reject only a fully zero-valued record: cost can legitimately be zero
	// (unknown pricing) while tokens/context data are present, and vice versa.
	if envelope.Metrics == (sessionclient.SessionMetrics{}) {
		return sessionclient.SessionMetrics{}, false
	}
	return envelope.Metrics, true
}
