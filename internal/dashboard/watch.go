package dashboard

import (
	"context"
	"fmt"
	"hash/fnv"
	"reflect"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func pollStream(ctx context.Context, interval time.Duration, tick func() error) error {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err := tick(); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func (s *Server) listNamespaced(ctx context.Context, namespace string, list client.ObjectList, label string) (skip bool, err error) {
	var opts []client.ListOption
	if namespace != "" {
		opts = append(opts, client.InNamespace(namespace))
	}
	if err := s.k8sClient.List(ctx, list, opts...); err != nil {
		if k8serrors.IsNotFound(err) || k8serrors.IsServiceUnavailable(err) {
			return true, nil
		}
		return false, mapK8sError(label, err)
	}
	return false, nil
}

func emitIfChanged[T any](versions map[string]string, key, version string, convert func() (T, error), send func(T) error) error {
	if versions[key] == version {
		return nil
	}
	versions[key] = version
	msg, err := convert()
	if err != nil {
		return err
	}
	return send(msg)
}

func streamSnapshots[T any](ctx context.Context, interval time.Duration, load func(context.Context) (T, bool, error), send func(T) error) error {
	var last T
	var sent bool
	timer := time.NewTimer(0)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-timer.C:
		}

		msg, active, err := load(ctx)
		if err != nil {
			return err
		}
		if !sent || !reflect.DeepEqual(last, msg) {
			if err := send(msg); err != nil {
				return err
			}
			last = msg
			sent = true
		}
		if !active {
			<-ctx.Done()
			return nil
		}

		timer.Reset(interval)
	}
}

// streamVersionedSnapshots polls like streamSnapshots but uses a cheap
// version probe to skip building (and DeepEqual-walking) the snapshot when
// nothing changed. probe returns an opaque version string; an empty version
// means "unknown", which falls back to building the snapshot every tick and
// comparing with DeepEqual before sending. build returns the snapshot and
// whether the stream should keep polling.
func streamVersionedSnapshots[T any](
	ctx context.Context,
	interval time.Duration,
	probe func(context.Context) (string, error),
	build func(context.Context) (T, bool, error),
	send func(T) error,
) error {
	var last T
	var sent bool
	var lastVersion string
	timer := time.NewTimer(0)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-timer.C:
		}

		version, err := probe(ctx)
		if err != nil {
			return err
		}
		if !sent || version == "" || version != lastVersion {
			msg, active, err := build(ctx)
			if err != nil {
				return err
			}
			if !sent || !reflect.DeepEqual(last, msg) {
				if err := send(msg); err != nil {
					return err
				}
				last = msg
				sent = true
			}
			lastVersion = version
			if !active {
				<-ctx.Done()
				return nil
			}
		}

		timer.Reset(interval)
	}
}

// WatchAgentRuns streams AgentRun updates. It sends the initial list immediately,
// then polls every 2 seconds and emits updates when resource versions change.
func (s *Server) WatchAgentRuns(ctx context.Context, req *platform.WatchAgentRunsRequest, stream *connect.ServerStream[platform.AgentRunEvent]) error {
	versions := make(map[string]string)
	return pollStream(ctx, 2*time.Second, func() error {
		runs := &platformv1alpha1.AgentRunList{}
		if skip, err := s.listNamespaced(ctx, req.Namespace, runs, "watch AgentRuns"); skip || err != nil {
			return err
		}
		visible := s.agentRunVisibilityFilter(ctx, true)
		// Session-backed summary fields (pending input, metrics, latest
		// activity) can change without a Kubernetes resourceVersion bump. The
		// production store returns every run's cheap summary version in one
		// query, keeping fleet rows live without an N+1 fingerprint loop.
		// The result is shared across all fleet streams watching the same
		// namespace for probeSummaryVersionsTTL.
		summaryVersions := map[string]string{}
		if versionStore, ok := s.stateStore.(interface {
			GetAgentRunSummaryVersions(context.Context, string) (map[string]string, error)
		}); ok {
			current, err := probeCacheDo(ctx, &s.probes, "sv|"+req.Namespace, probeSummaryVersionsTTL,
				func(ctx context.Context) (map[string]string, error) {
					return versionStore.GetAgentRunSummaryVersions(ctx, req.Namespace)
				})
			if err == nil {
				summaryVersions = current
			}
		}
		// Bulk enrichment state is built lazily on the first changed run of
		// this tick and discarded at tick end, so unchanged ticks avoid the
		// richer ownership/session loading path.
		var batch *agentRunEnrichBatch
		var batchBuilt bool
		seen := make(map[string]struct{}, len(runs.Items))
		for _, run := range runs.Items {
			run := run
			if !visible(&run) {
				continue
			}
			key := run.Namespace + "/" + run.Name
			seen[key] = struct{}{}
			version := run.ResourceVersion + "|" + summaryVersions[key]
			if err := emitIfChanged(versions, key, version,
				func() (*platform.AgentRunEvent, error) {
					if !batchBuilt {
						batch = s.newAgentRunEnrichBatch(ctx, req.Namespace, true)
						batchBuilt = true
					}
					pb, err := s.enrichAgentRunSummaryProto(ctx, k8sAgentRunToProto(&run), batch)
					if err != nil {
						return nil, err
					}
					return &platform.AgentRunEvent{Type: "MODIFIED", Run: pb}, nil
				},
				stream.Send,
			); err != nil {
				return err
			}
		}
		for key := range versions {
			if _, ok := seen[key]; ok {
				continue
			}
			delete(versions, key)
			parts := strings.SplitN(key, "/", 2)
			if len(parts) != 2 {
				continue
			}
			if err := stream.Send(&platform.AgentRunEvent{Type: "DELETED", Run: &platform.AgentRun{Namespace: parts[0], Name: parts[1]}}); err != nil {
				return err
			}
		}
		return nil
	})
}

// WatchAgentRun streams updates for a single AgentRun. Authorization runs
// once at stream start; each tick probes a cheap version (CRD resourceVersion
// plus a one-query Postgres session fingerprint) and only runs the full
// enrichment when it changed.
func (s *Server) WatchAgentRun(ctx context.Context, req *platform.WatchAgentRunRequest, stream *connect.ServerStream[platform.AgentRun]) error {
	if err := s.requireAgentRunViewer(ctx, req.Namespace, req.Name); err != nil {
		return err
	}
	var sessID uuid.UUID
	var haveSess bool
	probe := func(ctx context.Context) (string, error) {
		run := &platformv1alpha1.AgentRun{}
		if err := s.k8sClient.Get(ctx, client.ObjectKey{Namespace: req.Namespace, Name: req.Name}, run); err != nil {
			return "", mapK8sError("watch AgentRun", err)
		}
		if s.stateStore == nil {
			return "", nil
		}
		if !haveSess {
			sess, err := s.stateStore.GetSessionByRun(ctx, run.Name, run.Namespace)
			if err != nil {
				return "", nil
			}
			sessID = sess.ID
			haveSess = true
		}
		// Fingerprints are shared across every stream watching this run for
		// probeFingerprintTTL, so extra open tabs do not add query load.
		fp, err := probeCacheDo(ctx, &s.probes, "fp|"+sessID.String(), probeFingerprintTTL,
			func(ctx context.Context) (string, error) {
				return s.stateStore.GetSessionFingerprint(ctx, sessID)
			})
		if err != nil {
			return "", nil
		}
		return run.ResourceVersion + "|" + fp, nil
	}
	build := func(ctx context.Context) (*platform.AgentRun, bool, error) {
		run := &platformv1alpha1.AgentRun{}
		if err := s.k8sClient.Get(ctx, client.ObjectKey{Namespace: req.Namespace, Name: req.Name}, run); err != nil {
			return nil, false, mapK8sError("watch AgentRun", err)
		}
		pb, err := s.enrichAgentRunProto(ctx, k8sAgentRunToProto(run))
		if err != nil {
			return nil, false, err
		}
		return pb, shouldContinueAgentRunWatch(run), nil
	}
	return streamVersionedSnapshots(ctx, 500*time.Millisecond, probe, build, stream.Send)
}

// Succeeded and failed runs normally end their detail stream, but an attached
// overseer can still inspect and wake them (for example, after rejecting a
// completion). Keep the stream open through that lifecycle, including the
// asynchronous detach cleanup, so dashboard configuration and status do not
// freeze on the first terminal phase.
func shouldContinueAgentRunWatch(run *platformv1alpha1.AgentRun) bool {
	if run == nil {
		return false
	}
	if !isTerminalAgentRunPhase(run.Status.Phase) {
		return true
	}
	if run.Status.Phase == platformv1alpha1.AgentRunPhaseCancelled {
		return false
	}
	return run.Spec.Overseer != nil || run.Status.OverseerSummary != nil || strings.TrimSpace(run.Annotations[platformv1alpha1.OverseerDetachingAnnotation]) != ""
}

// activityLogVersion returns a cheap fingerprint of the activity-log source
// for a run: the immutable S3 artifact URL for terminal runs, or the latest
// Postgres event ID. Empty means the version cannot be determined cheaply
// (e.g. pod-exec fallback) and the caller must rebuild every tick.
func (s *Server) activityLogVersion(ctx context.Context, run *platformv1alpha1.AgentRun) string {
	isTerminal := isTerminalAgentRunPhase(run.Status.Phase)
	if isTerminal && s.s3Reader != nil && run.Status.Artifacts != nil && run.Status.Artifacts.EventsLogURL != "" {
		return "s3|" + run.Status.Artifacts.EventsLogURL
	}
	if s.stateStore != nil {
		if sess, err := s.cachedSessionByRun(ctx, run.Name, run.Namespace); err == nil {
			if latestID, err := s.latestActivityEventID(ctx, sess.ID); err == nil && latestID > 0 {
				return fmt.Sprintf("pg|%d|%t", latestID, isTerminal)
			}
		}
	}
	return ""
}

// WatchActivityLog streams the activity log. Authorization runs once at
// stream start; each tick probes the latest event ID and only rebuilds the
// full log when it changed. When req.Delta is set the stream sends an initial
// full snapshot (reset=true) followed by delta frames carrying only appended
// entries, falling back to full reset frames for sources without durable
// event IDs (S3/pod-exec).
func (s *Server) WatchActivityLog(ctx context.Context, req *platform.GetActivityLogRequest, stream *connect.ServerStream[platform.GetActivityLogResponse]) error {
	if err := s.requireAgentRunViewer(ctx, req.Namespace, req.Name); err != nil {
		return err
	}
	getRun := func(ctx context.Context) (*platformv1alpha1.AgentRun, error) {
		run := &platformv1alpha1.AgentRun{}
		if err := s.k8sClient.Get(ctx, client.ObjectKey{Namespace: req.Namespace, Name: req.Name}, run); err != nil {
			return nil, mapK8sError("watch activity log", err)
		}
		return run, nil
	}
	probe := func(ctx context.Context) (string, error) {
		run, err := getRun(ctx)
		if err != nil {
			return "", err
		}
		return s.activityLogVersion(ctx, run), nil
	}
	if req.Delta {
		buildSourced := func(ctx context.Context) (*platform.GetActivityLogResponse, activityLogSource, error) {
			run, err := getRun(ctx)
			if err != nil {
				return nil, activityLogSourceNone, err
			}
			resp, source := s.getAgentRunActivityLogSourced(ctx, run)
			return resp, source, nil
		}
		return watchActivityLogDelta(ctx, 500*time.Millisecond, req, probe, buildSourced, stream.Send)
	}
	previewReq := &platform.GetActivityLogRequest{PayloadPreviewBytes: req.PayloadPreviewBytes}
	build := func(ctx context.Context) (*platform.GetActivityLogResponse, bool, error) {
		run, err := getRun(ctx)
		if err != nil {
			return nil, false, err
		}
		resp := s.getAgentRunActivityLog(ctx, run)
		return applyActivityLogRequestOptions(resp, previewReq), !resp.IsComplete, nil
	}
	return streamVersionedSnapshots(ctx, 500*time.Millisecond, probe, build, stream.Send)
}

// subagentGraphFingerprint cheaply fingerprints a subagent graph so delta
// frames only carry it when it changed. The deterministic proto encoding
// covers every field — node progress (current_step, last_tool, waiting_on,
// files_written), edge endpoints, metrics — not just node IDs/statuses. The
// root node's entry_count is normalized away: it grows with every transcript
// entry and would otherwise force a graph frame per appended event (clients
// track their own entry counts).
func subagentGraphFingerprint(g *platform.SubagentGraph) string {
	if g == nil {
		return ""
	}
	norm := g
	for i, n := range g.Nodes {
		if n.Id != g.RootId || n.EntryCount == 0 {
			continue
		}
		if norm == g {
			norm = proto.Clone(g).(*platform.SubagentGraph)
		}
		norm.Nodes[i].EntryCount = 0
	}
	b, err := proto.MarshalOptions{Deterministic: true}.Marshal(norm)
	if err != nil {
		// Marshal of a generated message only fails in pathological cases;
		// degrade to a shape-only fingerprint rather than dropping frames.
		return fmt.Sprintf("shape|%d|%d", len(g.Nodes), len(g.Edges))
	}
	h := fnv.New64a()
	h.Write(b)
	return fmt.Sprintf("%d|%d|%x", len(g.Nodes), len(g.Edges), h.Sum64())
}

// watchActivityLogDelta is the delta-mode poll loop for WatchActivityLog. It
// mirrors streamVersionedSnapshots' version-probe skip, but tracks a
// per-stream event-ID cursor so unchanged history is never resent for
// delta-capable (Postgres) sources.
func watchActivityLogDelta(
	ctx context.Context,
	interval time.Duration,
	req *platform.GetActivityLogRequest,
	probe func(context.Context) (string, error),
	build func(context.Context) (*platform.GetActivityLogResponse, activityLogSource, error),
	send func(*platform.GetActivityLogResponse) error,
) error {
	var (
		sentInitial     bool
		lastSentEventID int64
		lastGraphFP     string
		lastSource      activityLogSource
		lastBuilt       *platform.GetActivityLogResponse
		lastVersion     string
	)
	timer := time.NewTimer(0)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-timer.C:
		}

		version, err := probe(ctx)
		if err != nil {
			return err
		}
		if sentInitial && version != "" && version == lastVersion {
			timer.Reset(interval)
			continue
		}
		resp, source, err := build(ctx)
		if err != nil {
			return err
		}
		// Coalescing keeps entries ordered by EventId, but compute the max
		// over all entries anyway so cursor correctness never depends on
		// upstream ordering invariants.
		var maxID int64
		for _, e := range resp.Entries {
			if e.EventId > maxID {
				maxID = e.EventId
			}
		}
		deltaCapable := source == activityLogSourcePostgres

		if !sentInitial || !deltaCapable || source != lastSource || maxID < lastSentEventID {
			// Full snapshot with reset: first frame, non-delta-capable source
			// (synthetic IDs), source flip, or event-ID regression.
			if !sentInitial || !reflect.DeepEqual(lastBuilt, resp) {
				opts := &platform.GetActivityLogRequest{
					Limit:               req.Limit,
					PayloadPreviewBytes: req.PayloadPreviewBytes,
				}
				if !sentInitial && deltaCapable {
					opts.SinceEventId = req.SinceEventId
				}
				applied := applyActivityLogRequestOptions(resp, opts)
				frame := &platform.GetActivityLogResponse{
					Entries:       applied.Entries,
					IsComplete:    resp.IsComplete,
					SubagentGraph: applied.SubagentGraph,
					LastEventId:   maxID,
					Delta:         true,
					Reset_:        true,
					FirstEventId:  applied.FirstEventId,
					HasMoreBefore: applied.HasMoreBefore,
				}
				if err := send(frame); err != nil {
					return err
				}
				lastSentEventID = maxID
				lastGraphFP = subagentGraphFingerprint(resp.SubagentGraph)
				lastSource = source
				lastBuilt = resp
				sentInitial = true
			}
		} else {
			// Delta frame: every entry whose EventId advanced past the
			// cursor, in slice order. Selected by id rather than by a
			// positional tail: a coalesced thinking entry is re-sent with a
			// grown EventId each time new deltas merge into it, and resend
			// correctness must not depend on where it sits in the slice.
			// Advancing the cursor to the max EventId keeps unchanged
			// entries from being re-sent.
			var appended []*platform.ActivityEntry
			for _, e := range resp.Entries {
				if e.EventId > lastSentEventID {
					appended = append(appended, e)
				}
			}
			if n := int(req.PayloadPreviewBytes); n > 0 {
				appended = truncateActivityEntries(appended, n)
			}
			graphFP := subagentGraphFingerprint(resp.SubagentGraph)
			if len(appended) > 0 || graphFP != lastGraphFP || !reflect.DeepEqual(lastBuilt, resp) {
				frame := &platform.GetActivityLogResponse{
					Entries:     appended,
					IsComplete:  resp.IsComplete,
					LastEventId: maxID,
					Delta:       true,
				}
				if graphFP != lastGraphFP {
					frame.SubagentGraph = resp.SubagentGraph
				}
				if err := send(frame); err != nil {
					return err
				}
				lastSentEventID = maxID
				lastGraphFP = graphFP
				lastBuilt = resp
			}
		}
		lastVersion = version
		if resp.IsComplete {
			<-ctx.Done()
			return nil
		}
		timer.Reset(interval)
	}
}

// WatchDiff streams the working-tree diff. Authorization runs once at stream
// start (matching WatchActivityLog); each tick rebuilds through the coalesced
// diff builder, so concurrent streams for the same run share one pod exec or
// S3 fetch instead of issuing one each.
func (s *Server) WatchDiff(ctx context.Context, req *platform.GetDiffRequest, stream *connect.ServerStream[platform.GetDiffResponse]) error {
	if err := s.requireAgentRunViewer(ctx, req.Namespace, req.Name); err != nil {
		return err
	}
	return streamSnapshots(ctx, 750*time.Millisecond, func(ctx context.Context) (*platform.GetDiffResponse, bool, error) {
		resp, err := s.buildDiff(ctx, req)
		if err != nil {
			return nil, false, err
		}
		return resp, !resp.IsComplete, nil
	}, stream.Send)
}

// WatchAgentTrace streams the agent's OTel trace. Authorization runs once at
// stream start; each tick rebuilds through the coalesced trace builder, so
// concurrent streams for the same run share one Jaeger fetch per TTL window.
func (s *Server) WatchAgentTrace(ctx context.Context, req *platform.GetAgentTraceRequest, stream *connect.ServerStream[platform.GetAgentTraceResponse]) error {
	if err := s.requireAgentRunViewer(ctx, req.Namespace, req.Name); err != nil {
		return err
	}
	if s.jaeger == nil {
		return connect.NewError(connect.CodeUnavailable, fmt.Errorf("Jaeger not configured (set JAEGER_QUERY_URL or OTEL_EXPORTER_OTLP_ENDPOINT)"))
	}
	return streamSnapshots(ctx, 750*time.Millisecond, func(ctx context.Context) (*platform.GetAgentTraceResponse, bool, error) {
		resp, err := s.buildAgentTrace(ctx, req)
		if err != nil {
			return nil, false, err
		}
		return resp, !resp.IsComplete, nil
	}, stream.Send)
}

// watchTriggerList factors the shared poll-list-emit loop used by the
// trigger-resource watchers (LinearProject, Project, GitHubRepository, Cron):
// list the resources, join run metrics, emit a MODIFIED event per item whose
// resourceVersion, metrics, or caller ACL changed since the previous poll, and
// a DELETED event per item that disappeared (resource deleted or the caller's
// access revoked). resourceType names the collaboration resource type used for
// visibility filtering and ACL fingerprints; empty means the resource type is
// not ACL-scoped.
func watchTriggerList[T any, E any](
	ctx context.Context,
	s *Server,
	namespace string,
	metricsKind string,
	resourceType string,
	fetch func(ctx context.Context) ([]T, bool, error),
	objMeta func(*T) (ns, name, resourceVersion string),
	makeEvent func(*T, map[resourceMetricsKey]*platform.ProjectMetrics) (*E, error),
	makeDeletedEvent func(namespace, name string) *E,
	send func(*E) error,
) error {
	return pollStream(ctx, 5*time.Second, triggerListTick(ctx, s, namespace, metricsKind, resourceType, fetch, objMeta, makeEvent, makeDeletedEvent, send))
}

// triggerListTick returns the per-poll body of watchTriggerList, factored out
// so tests can drive polls directly.
func triggerListTick[T any, E any](
	ctx context.Context,
	s *Server,
	namespace string,
	metricsKind string,
	resourceType string,
	fetch func(ctx context.Context) ([]T, bool, error),
	objMeta func(*T) (ns, name, resourceVersion string),
	makeEvent func(*T, map[resourceMetricsKey]*platform.ProjectMetrics) (*E, error),
	makeDeletedEvent func(namespace, name string) *E,
	send func(*E) error,
) func() error {
	versions := make(map[string]string)
	return func() error {
		items, skip, err := fetch(ctx)
		if skip || err != nil {
			return err
		}
		visible := func(string, string) bool { return true }
		aclKey := func(string, string) string { return "" }
		if resourceType != "" {
			visible, aclKey = s.resourceACLView(ctx, resourceType, true)
		}
		metrics, err := s.listResourceMetrics(ctx, namespace)
		if err != nil {
			return err
		}
		seen := make(map[string]struct{}, len(items))
		for i := range items {
			item := &items[i]
			ns, name, rv := objMeta(item)
			if !visible(ns, name) {
				continue
			}
			key := ns + "/" + name
			seen[key] = struct{}{}
			currentMetrics := resourceMetrics(metrics, ns, metricsKind, name)
			if err := emitIfChanged(versions, key, resourceMetricsVersion(rv, currentMetrics)+"\x1f"+aclKey(ns, name),
				func() (*E, error) { return makeEvent(item, metrics) },
				send,
			); err != nil {
				return err
			}
		}
		// Anything tracked but no longer visible was deleted or had the
		// caller's access revoked: emit a removal so clients drop it.
		for key := range versions {
			if _, ok := seen[key]; ok {
				continue
			}
			delete(versions, key)
			parts := strings.SplitN(key, "/", 2)
			if len(parts) != 2 {
				continue
			}
			if err := send(makeDeletedEvent(parts[0], parts[1])); err != nil {
				return err
			}
		}
		return nil
	}
}

// WatchLinearProjects streams LinearProject updates.
//
//nolint:dupl // irreducible type-plumbing wrapper; shared logic lives in watchTriggerList
func (s *Server) WatchLinearProjects(ctx context.Context, req *platform.WatchLinearProjectsRequest, stream *connect.ServerStream[platform.LinearProjectEvent]) error {
	return watchTriggerList(ctx, s, req.Namespace, "LinearProject", linearProjectResourceType,
		func(ctx context.Context) ([]triggersv1alpha1.LinearProject, bool, error) {
			list := &triggersv1alpha1.LinearProjectList{}
			skip, err := s.listNamespaced(ctx, req.Namespace, list, "watch LinearProjects")
			return list.Items, skip, err
		},
		func(p *triggersv1alpha1.LinearProject) (string, string, string) {
			return p.Namespace, p.Name, p.ResourceVersion
		},
		func(p *triggersv1alpha1.LinearProject, metrics map[resourceMetricsKey]*platform.ProjectMetrics) (*platform.LinearProjectEvent, error) {
			pb := s.linearProjectProto(ctx, p, metrics)
			pb.Owner, pb.MyPermission = s.resourceACL(ctx, linearProjectResourceType, p.Name, p.Namespace)
			return &platform.LinearProjectEvent{Type: "MODIFIED", Project: pb}, nil
		},
		func(namespace, name string) *platform.LinearProjectEvent {
			return &platform.LinearProjectEvent{Type: "DELETED", Project: &platform.LinearProject{Namespace: namespace, Name: name}}
		},
		stream.Send,
	)
}

// WatchProjects streams Project updates.
//
//nolint:dupl // irreducible type-plumbing wrapper; shared logic lives in watchTriggerList
func (s *Server) WatchProjects(ctx context.Context, req *platform.WatchProjectsRequest, stream *connect.ServerStream[platform.ProjectEvent]) error {
	return watchTriggerList(ctx, s, req.Namespace, "Project", projectResourceType,
		func(ctx context.Context) ([]triggersv1alpha1.Project, bool, error) {
			list := &triggersv1alpha1.ProjectList{}
			skip, err := s.listNamespaced(ctx, req.Namespace, list, "watch Projects")
			return list.Items, skip, err
		},
		func(p *triggersv1alpha1.Project) (string, string, string) {
			return p.Namespace, p.Name, p.ResourceVersion
		},
		func(p *triggersv1alpha1.Project, metrics map[resourceMetricsKey]*platform.ProjectMetrics) (*platform.ProjectEvent, error) {
			return &platform.ProjectEvent{Type: "MODIFIED", Project: s.projectProto(ctx, p, metrics)}, nil
		},
		func(namespace, name string) *platform.ProjectEvent {
			return &platform.ProjectEvent{Type: "DELETED", Project: &platform.Project{Namespace: namespace, Name: name}}
		},
		stream.Send,
	)
}

// WatchGitHubRepositories streams GitHubRepository updates.
//
//nolint:dupl // irreducible type-plumbing wrapper; shared logic lives in watchTriggerList
func (s *Server) WatchGitHubRepositories(ctx context.Context, req *platform.WatchGitHubRepositoriesRequest, stream *connect.ServerStream[platform.GitHubRepositoryEvent]) error {
	return watchTriggerList(ctx, s, req.Namespace, "GitHubRepository", githubRepositoryResourceType,
		func(ctx context.Context) ([]triggersv1alpha1.GitHubRepository, bool, error) {
			list := &triggersv1alpha1.GitHubRepositoryList{}
			skip, err := s.listNamespaced(ctx, req.Namespace, list, "watch GitHubRepositories")
			return list.Items, skip, err
		},
		func(r *triggersv1alpha1.GitHubRepository) (string, string, string) {
			return r.Namespace, r.Name, r.ResourceVersion
		},
		func(r *triggersv1alpha1.GitHubRepository, metrics map[resourceMetricsKey]*platform.ProjectMetrics) (*platform.GitHubRepositoryEvent, error) {
			pb := s.githubRepositoryProto(ctx, r, metrics)
			pb.ResourceOwner, pb.MyPermission = s.resourceACL(ctx, githubRepositoryResourceType, r.Name, r.Namespace)
			return &platform.GitHubRepositoryEvent{Type: "MODIFIED", Repository: pb}, nil
		},
		func(namespace, name string) *platform.GitHubRepositoryEvent {
			return &platform.GitHubRepositoryEvent{Type: "DELETED", Repository: &platform.GitHubRepository{Namespace: namespace, Name: name}}
		},
		stream.Send,
	)
}

// WatchCrons streams Cron trigger updates.
//
//nolint:dupl // irreducible type-plumbing wrapper; shared logic lives in watchTriggerList
func (s *Server) WatchCrons(ctx context.Context, req *platform.WatchCronsRequest, stream *connect.ServerStream[platform.CronEvent]) error {
	return watchTriggerList(ctx, s, req.Namespace, "Cron", cronResourceType,
		func(ctx context.Context) ([]triggersv1alpha1.Cron, bool, error) {
			list := &triggersv1alpha1.CronList{}
			skip, err := s.listNamespaced(ctx, req.Namespace, list, "watch Crons")
			return list.Items, skip, err
		},
		func(cr *triggersv1alpha1.Cron) (string, string, string) {
			return cr.Namespace, cr.Name, cr.ResourceVersion
		},
		func(cr *triggersv1alpha1.Cron, metrics map[resourceMetricsKey]*platform.ProjectMetrics) (*platform.CronEvent, error) {
			pb := s.cronProto(ctx, cr, metrics)
			pb.Owner, pb.MyPermission = s.resourceACL(ctx, cronResourceType, cr.Name, cr.Namespace)
			return &platform.CronEvent{Type: "MODIFIED", Cron: pb}, nil
		},
		func(namespace, name string) *platform.CronEvent {
			return &platform.CronEvent{Type: "DELETED", Cron: &platform.Cron{Namespace: namespace, Name: name}}
		},
		stream.Send,
	)
}

// WatchTeamRuntime streams TeamRuntime updates for a specific parent AgentRun.
func (s *Server) WatchTeamRuntime(ctx context.Context, req *platform.WatchTeamRuntimeRequest, stream *connect.ServerStream[platform.TeamRuntime]) error {
	if err := s.requireAgentRunViewer(ctx, req.Namespace, req.ParentName); err != nil {
		return err
	}
	var lastVersion string
	return pollStream(ctx, 2*time.Second, func() error {
		list := &platformv1alpha1.AgentRunTeamRuntimeList{}
		if err := s.k8sClient.List(ctx, list,
			client.InNamespace(req.Namespace),
			client.MatchingLabels{"platform.gratefulagents.dev/team-parent": req.ParentName},
		); err != nil {
			if k8serrors.IsNotFound(err) || k8serrors.IsServiceUnavailable(err) {
				return nil
			}
			return mapK8sError("watch TeamRuntime", err)
		}
		if len(list.Items) > 0 {
			rt := &list.Items[0]
			if rt.ResourceVersion != lastVersion {
				lastVersion = rt.ResourceVersion
				return stream.Send(k8sTeamRuntimeToProto(rt))
			}
		}
		return nil
	})
}
