package dashboard

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"
	"unicode/utf8"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/store"
	"github.com/gratefulagents/gratefulagents/internal/store/sessionclient"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
	agent "github.com/gratefulagents/sdk/pkg/agentsdk"
)

// ListAgentRuns returns all AgentRuns, optionally filtered by namespace.
func (s *Server) ListAgentRuns(ctx context.Context, req *platform.ListAgentRunsRequest) (*platform.ListAgentRunsResponse, error) {
	runs := &platformv1alpha1.AgentRunList{}
	var opts []client.ListOption
	if req.Namespace != "" {
		opts = append(opts, client.InNamespace(req.Namespace))
	}
	if err := s.k8sClient.List(ctx, runs, opts...); err != nil {
		return nil, mapK8sError("list AgentRuns", err)
	}

	var pbRuns []*platform.AgentRun
	visible := s.agentRunVisibilityFilter(ctx, false)
	batch := s.newAgentRunEnrichBatch(ctx, req.Namespace, false)
	for _, run := range runs.Items {
		if !visible(&run) {
			continue
		}
		pb, err := s.enrichAgentRunSummaryProto(ctx, k8sAgentRunToProto(&run), batch)
		if err != nil {
			return nil, err
		}
		pbRuns = append(pbRuns, pb)
	}

	// Apply ownership/sharing filters if stateStore is available.
	pbRuns = filterListByAccess(ctx, s, "agent_run", req.OwnedByMe, req.SharedWithMe, pbRuns,
		func(r *platform.AgentRun) string { return r.Namespace + "/" + r.Name })

	return &platform.ListAgentRunsResponse{Runs: pbRuns}, nil
}

// GetAgentRun returns a single AgentRun by namespace and name.
func (s *Server) GetAgentRun(ctx context.Context, req *platform.GetAgentRunRequest) (*platform.AgentRun, error) {
	if err := s.requireAgentRunViewer(ctx, req.Namespace, req.Name); err != nil {
		return nil, err
	}
	run := &platformv1alpha1.AgentRun{}
	if err := s.k8sClient.Get(ctx, client.ObjectKey{
		Namespace: req.Namespace,
		Name:      req.Name,
	}, run); err != nil {
		return nil, mapK8sError(fmt.Sprintf("get AgentRun %s/%s", req.Namespace, req.Name), err)
	}
	return s.enrichAgentRunProto(ctx, k8sAgentRunToProto(run))
}

// fetchSpecMarkdown reads the spec.md key from the referenced ConfigMap.
// Returns empty string when the ConfigMap or key is absent; unexpected
// errors are logged so enrichment failures are not invisible.
func (s *Server) fetchSpecMarkdown(ctx context.Context, namespace, configMapName string) string {
	if configMapName == "" {
		return ""
	}
	cm := &corev1.ConfigMap{}
	if err := s.k8sClient.Get(ctx, client.ObjectKey{
		Namespace: namespace,
		Name:      configMapName,
	}, cm); err != nil {
		if !k8serrors.IsNotFound(err) {
			log.Printf("WARN: reading spec ConfigMap %s/%s: %v", namespace, configMapName, err)
		}
		return ""
	}
	return cm.Data["spec.md"]
}

// GetActivityLog returns the full NDJSON activity log for the public AgentRun surface.
// Internal compatibility owners are resolved from the AgentRun itself.
func (s *Server) GetActivityLog(ctx context.Context, req *platform.GetActivityLogRequest) (*platform.GetActivityLogResponse, error) {
	if err := s.requireAgentRunViewer(ctx, req.Namespace, req.Name); err != nil {
		return nil, err
	}
	run := &platformv1alpha1.AgentRun{}
	if err := s.k8sClient.Get(ctx, client.ObjectKey{
		Namespace: req.Namespace,
		Name:      req.Name,
	}, run); err != nil {
		return nil, mapK8sError(fmt.Sprintf("get AgentRun %s/%s", req.Namespace, req.Name), err)
	}
	return applyActivityLogRequestOptions(s.getAgentRunActivityLog(ctx, run), req), nil
}

// GetActivityEntryDetail returns the full, untruncated tool payloads for a
// single activity entry, identified by event ID (preferred) or tool-use ID.
func (s *Server) GetActivityEntryDetail(ctx context.Context, req *platform.GetActivityEntryDetailRequest) (*platform.GetActivityEntryDetailResponse, error) {
	if err := s.requireAgentRunViewer(ctx, req.Namespace, req.Name); err != nil {
		return nil, err
	}
	run := &platformv1alpha1.AgentRun{}
	if err := s.k8sClient.Get(ctx, client.ObjectKey{
		Namespace: req.Namespace,
		Name:      req.Name,
	}, run); err != nil {
		return nil, mapK8sError(fmt.Sprintf("get AgentRun %s/%s", req.Namespace, req.Name), err)
	}
	// Prefer the already-built memo (a viewer asking for detail has almost
	// always just streamed the log) and, for Postgres-sourced live runs, a
	// single primary-key row: neither loads the run's full history.
	if resp, ok := s.activityEntryDetailFast(ctx, run, req); ok {
		return resp, nil
	}
	activity := s.getAgentRunActivityLog(ctx, run)
	if resp := findActivityEntryDetail(activity.Entries, req); resp != nil {
		return resp, nil
	}
	return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("activity entry not found in %s/%s", req.Namespace, req.Name))
}

// findActivityEntryDetail locates an entry by event ID (preferred) or
// tool-use ID in an already-built entry list.
func findActivityEntryDetail(entries []*platform.ActivityEntry, req *platform.GetActivityEntryDetailRequest) *platform.GetActivityEntryDetailResponse {
	if req.EventId > 0 {
		for _, e := range entries {
			if e.EventId == req.EventId {
				return &platform.GetActivityEntryDetailResponse{InputRaw: e.InputRaw, Output: e.Output}
			}
		}
	}
	if req.ToolUseId != "" {
		for _, e := range entries {
			if e.ToolUseId == req.ToolUseId {
				return &platform.GetActivityEntryDetailResponse{InputRaw: e.InputRaw, Output: e.Output}
			}
		}
	}
	return nil
}

// activityEntryDetailFast answers an entry-detail request without loading
// the run's history: from the memoized response when one exists, or with a
// single-row Postgres lookup when the run's log is served from Postgres and
// the request carries a durable event ID. Returns ok=false when neither
// applies and the caller must fall back to the full log.
func (s *Server) activityEntryDetailFast(ctx context.Context, run *platformv1alpha1.AgentRun, req *platform.GetActivityEntryDetailRequest) (*platform.GetActivityEntryDetailResponse, bool) {
	memoKey := run.Namespace + "/" + run.Name
	// Terminal runs with an S3 artifact carry synthetic ordinals, not
	// Postgres IDs: a memo built from another source must not answer them.
	var s3URL string
	if isTerminalAgentRunPhase(run.Status.Phase) && s.s3Reader != nil && run.Status.Artifacts != nil {
		s3URL = run.Status.Artifacts.EventsLogURL
	}
	s.activityMemoMu.Lock()
	memo, ok := s.activityMemo[memoKey]
	var resp *platform.GetActivityLogResponse
	if ok && memo.s3URL == s3URL {
		memo.lastAccess = time.Now()
		resp = memo.resp
	}
	s.activityMemoMu.Unlock()
	if resp != nil {
		if detail := findActivityEntryDetail(resp.Entries, req); detail != nil {
			return detail, true
		}
	}
	if req.EventId <= 0 || s.stateStore == nil {
		return nil, false
	}
	getter, ok := s.stateStore.(store.ActivityEventGetter)
	if !ok {
		return nil, false
	}
	// Only the live Postgres source can be answered by row.
	if s3URL != "" {
		return nil, false
	}
	sess, err := s.cachedSessionByRun(ctx, run.Name, run.Namespace)
	if err != nil {
		return nil, false
	}
	ev, err := getter.GetActivityEvent(ctx, sess.ID, req.EventId)
	if err != nil {
		return nil, false
	}
	e := activityEventToActivityEntry(*ev)
	return &platform.GetActivityEntryDetailResponse{InputRaw: e.InputRaw, Output: e.Output}, true
}

func (s *Server) GetAgentRunUsage(ctx context.Context, req *platform.GetAgentRunUsageRequest) (*platform.AgentRunUsageResponse, error) {
	if err := s.requireAgentRunViewer(ctx, req.Namespace, req.Name); err != nil {
		return nil, err
	}
	run := &platformv1alpha1.AgentRun{}
	if err := s.k8sClient.Get(ctx, client.ObjectKey{Namespace: req.Namespace, Name: req.Name}, run); err != nil {
		return nil, mapK8sError(fmt.Sprintf("get AgentRun %s/%s", req.Namespace, req.Name), err)
	}
	activity := s.getAgentRunActivityLog(ctx, run)
	usage := aggregateUsageFromEntries(activity.Entries)
	usage.IsComplete = activity.IsComplete
	return usage, nil
}

// activityLogSource identifies which backend produced an activity-log
// response. Only Postgres carries durable, monotonically increasing event IDs
// suitable for delta streaming; S3 and pod-exec entries get synthetic
// ordinals that are only stable within a single response.
type activityLogSource string

const (
	activityLogSourcePostgres activityLogSource = "pg"
	activityLogSourceS3       activityLogSource = "s3"
	activityLogSourceExec     activityLogSource = "exec"
	activityLogSourceNone     activityLogSource = ""
)

func activityEventToActivityEntry(ev store.ActivityEvent) *platform.ActivityEntry {
	var ce agent.ContentEvent
	if err := json.Unmarshal(ev.Detail, &ce); err == nil && ce.Type != "" {
		e := contentEventToActivityEntry(&ce)
		preserveEventUsageCacheSemantics(ev.Detail, e)
		e.EventId = ev.ID
		return e
	}
	return &platform.ActivityEntry{
		TimestampUnix: ev.CreatedAt.Unix(),
		Type:          ev.EventType,
		Message:       ev.Summary,
		InputRaw:      string(ev.Detail),
		EventId:       ev.ID,
	}
}

func (s *Server) getAgentRunActivityLog(ctx context.Context, run *platformv1alpha1.AgentRun) *platform.GetActivityLogResponse {
	resp, _ := s.getAgentRunActivityLogSourced(ctx, run)
	return resp
}

// buildActivityLogResponse is the single seam where raw entries from any
// source (S3, Postgres, pod exec) become a response: streamed thinking
// deltas are coalesced here, per request, so caches keep raw entries and
// the subagent graph is built from what the client actually sees.
func buildActivityLogResponse(entries []*platform.ActivityEntry, isComplete bool, runName string) *platform.GetActivityLogResponse {
	coalesced := coalesceThinkingEntries(entries)
	return &platform.GetActivityLogResponse{
		Entries:       coalesced,
		IsComplete:    isComplete,
		SubagentGraph: BuildSubagentGraph(coalesced, runName),
	}
}

// latestActivityIDStore is implemented by stores that can return just the
// newest event ID for a session without loading the event payload.
type latestActivityIDStore interface {
	GetLatestActivityEventID(ctx context.Context, sessionID uuid.UUID) (int64, error)
}

// cachedSessionByRun resolves a run's session with a short-TTL coalescing
// cache. Activity surfaces only need the session ID — created once per run
// and immutable — and every open view of the same run probes it on its own
// sub-second tick.
func (s *Server) cachedSessionByRun(ctx context.Context, name, namespace string) (*store.Session, error) {
	return probeCacheDo(ctx, &s.probes, "sess|"+namespace+"/"+name, probeSessionTTL, func(ctx context.Context) (*store.Session, error) {
		return s.stateStore.GetSessionByRun(ctx, name, namespace)
	})
}

// latestActivityEventID returns the newest activity event ID for a session
// (0 when none exist), coalesced across concurrent watch ticks. Prefers the
// index-only store probe and falls back to loading the newest event row for
// stores without it. fresh bypasses a value cached before the call (still
// coalescing with an in-flight probe); wake-driven watch iterations set it.
func (s *Server) latestActivityEventID(ctx context.Context, sessionID uuid.UUID, fresh bool) (int64, error) {
	do := probeCacheDo[int64]
	if fresh {
		do = probeCacheDoFresh[int64]
	}
	return do(ctx, &s.probes, "lastev|"+sessionID.String(), probeLatestEventTTL, func(ctx context.Context) (int64, error) {
		if ls, ok := s.stateStore.(latestActivityIDStore); ok {
			return ls.GetLatestActivityEventID(ctx, sessionID)
		}
		recent, err := s.stateStore.GetRecentActivity(ctx, sessionID, 1)
		if err != nil {
			return 0, err
		}
		if len(recent) == 0 {
			return 0, nil
		}
		return recent[0].ID, nil
	})
}

// conversationFingerprintKey is the probe-cache key for a session's
// conversation fingerprint (conversation_seq: messages, session row, plan
// artifact, interrupts — not activity events). Writers on this replica
// invalidate it after inserting a message.
func conversationFingerprintKey(sessionID uuid.UUID) string {
	return "cfp|" + sessionID.String()
}

// conversationFingerprint returns the session's conversation fingerprint,
// shared across concurrent streams for probeFingerprintTTL. fresh bypasses a
// value cached before the call (wake-driven watch iterations).
func (s *Server) conversationFingerprint(ctx context.Context, sessionID uuid.UUID, fresh bool) (string, error) {
	do := probeCacheDo[string]
	if fresh {
		do = probeCacheDoFresh[string]
	}
	return do(ctx, &s.probes, conversationFingerprintKey(sessionID), probeFingerprintTTL, func(ctx context.Context) (string, error) {
		return s.stateStore.GetSessionConversationFingerprint(ctx, sessionID)
	})
}

// invalidateConversationProbe drops this replica's cached conversation
// fingerprint after a message write so a same-process read-after-write is
// not answered from a probe cached just before the write.
func (s *Server) invalidateConversationProbe(sessionID uuid.UUID) {
	s.probes.invalidate(conversationFingerprintKey(sessionID))
}

type sharedConversationMemoKey struct{}

// withSharedConversationMemo marks a context as belonging to a watch stream
// build. Only watch builds share the memoized conversation: unary reads keep
// loading the transcript directly so a client never observes its own write
// missing because of a probe cached moments earlier.
func withSharedConversationMemo(ctx context.Context) context.Context {
	return context.WithValue(ctx, sharedConversationMemoKey{}, true)
}

// probeConversationTTL bounds how long a built conversation stays memoized.
// Every stream watching a run rebuilds within one probe window of a change,
// so a few seconds is enough for N tabs to share one GetMessages per version
// while keeping large transcripts (inline images) from lingering.
const probeConversationTTL = 5 * time.Second

// sessionConversation returns the chat transcript for a session. Watch
// builds (withSharedConversationMemo) memoize the built conversation keyed
// by the conversation fingerprint so N streams share one GetMessages per
// version. The returned slice and its messages are shared read-only across
// streams; consumers must not mutate them.
func (s *Server) sessionConversation(ctx context.Context, sessionID uuid.UUID, phase string) ([]*platform.ChatMessage, error) {
	load := func(ctx context.Context) ([]*platform.ChatMessage, error) {
		msgs, err := s.stateStore.GetMessages(ctx, sessionID)
		if err != nil {
			return nil, err
		}
		return conversationFromMessages(msgs, phase), nil
	}
	if ctx.Value(sharedConversationMemoKey{}) == nil {
		return load(ctx)
	}
	fp, err := s.conversationFingerprint(ctx, sessionID, false)
	if err != nil || fp == "" {
		return load(ctx)
	}
	return probeCacheDo(ctx, &s.probes, "conv|"+sessionID.String()+"|"+fp, probeConversationTTL, load)
}

func (s *Server) getAgentRunActivityLogSourced(ctx context.Context, run *platformv1alpha1.AgentRun) (*platform.GetActivityLogResponse, activityLogSource) {
	isTerminal := run.Status.Phase == platformv1alpha1.AgentRunPhaseSucceeded ||
		run.Status.Phase == platformv1alpha1.AgentRunPhaseFailed ||
		run.Status.Phase == platformv1alpha1.AgentRunPhaseCancelled
	sandboxMissing := false

	// For terminal runs, read events.jsonl from S3.
	if isTerminal && s.s3Reader != nil && run.Status.Artifacts != nil {
		if evURL := run.Status.Artifacts.EventsLogURL; evURL != "" {
			memoKey := run.Namespace + "/" + run.Name
			// The artifact is immutable, so the built response (coalesced
			// entries + subagent graph) is reused for every viewer of the
			// finished run instead of being rebuilt on each call or tick.
			s.activityMemoMu.Lock()
			memo, ok := s.activityMemo[memoKey]
			if ok && memo.s3URL == evURL {
				memo.lastAccess = time.Now()
			}
			s.activityMemoMu.Unlock()
			if ok && memo.s3URL == evURL {
				return memo.resp, activityLogSourceS3
			}
			entries, err := s.s3Reader.FetchEventStream(ctx, evURL)
			if err != nil {
				log.Printf("WARN: failed to fetch event stream from S3 (%s): %v", evURL, err)
			} else {
				resp := buildActivityLogResponse(entries, true, run.Name)
				approxBytes := 0
				for _, e := range entries {
					approxBytes += activityEntryApproxBytes(e)
				}
				// Replaces any Postgres memo for this run: the immutable
				// artifact is now authoritative and the Postgres memo would
				// never be read again.
				s.storeActivityMemo(memoKey, &activityMemoEntry{
					isTerminal:  true,
					s3URL:       evURL,
					entries:     entries,
					resp:        resp,
					approxBytes: approxBytes,
				})
				return resp, activityLogSourceS3
			}
		}
	}

	// Postgres is the preferred live source because it is durable and updates
	// faster than pod exec snapshots while the run is still active.
	if s.stateStore != nil {
		if sess, err := s.cachedSessionByRun(ctx, run.Name, run.Namespace); err == nil {
			memoKey := run.Namespace + "/" + run.Name
			// Cheap probe: when no new events arrived and the terminal state
			// is unchanged, reuse the previously built response instead of
			// reloading the full history and rebuilding the subagent graph
			// on every 500ms watch tick. When new events did arrive, fetch
			// only the delta and extend the cached entries.
			if latestID, err := s.latestActivityEventID(ctx, sess.ID, false); err == nil && latestID > 0 {
				s.activityMemoMu.Lock()
				memo, ok := s.activityMemo[memoKey]
				if ok && memo.s3URL != "" {
					// An S3-built memo has no Postgres cursor; when the
					// artifact became unreadable, rebuild from Postgres
					// instead of appending events onto S3 entries.
					ok = false
				}
				if ok {
					memo.lastAccess = time.Now()
				}
				s.activityMemoMu.Unlock()
				// The probe can be up to probeLatestEventTTL stale, so the
				// memo (advanced by whichever stream fetched a delta first)
				// may legitimately be ahead of it; events are append-only,
				// so the memo is current in both cases.
				if ok && memo.lastEventID >= latestID && memo.isTerminal == isTerminal {
					return memo.resp, activityLogSourcePostgres
				}
				if ok && memo.lastEventID <= latestID {
					if delta, err := s.stateStore.GetActivityEventsSince(ctx, sess.ID, memo.lastEventID); err == nil {
						// Never mutate the cached slice in place: previous
						// responses may still be referenced by streams. The
						// full-cap slice expression forces append to copy.
						entries := memo.entries[:len(memo.entries):len(memo.entries)]
						lastID := memo.lastEventID
						approxBytes := memo.approxBytes
						for _, ev := range delta {
							// The store returns IDs > afterID, but never trust
							// ordering or the cursor: an event at or below the
							// memo's high-water mark is already present.
							if ev.ID <= memo.lastEventID {
								continue
							}
							if ev.ID > lastID {
								lastID = ev.ID
							}
							approxBytes += activityEventApproxBytes(ev)
							entries = append(entries, activityEventToActivityEntry(ev))
						}
						resp := buildActivityLogResponse(entries, isTerminal, run.Name)
						s.storeActivityMemo(memoKey, &activityMemoEntry{
							lastEventID: lastID,
							isTerminal:  isTerminal,
							entries:     entries,
							resp:        resp,
							approxBytes: approxBytes,
						})
						return resp, activityLogSourcePostgres
					}
				}
			}
			events, err := s.stateStore.GetAllActivity(ctx, sess.ID)
			if err == nil && len(events) > 0 {
				entries := make([]*platform.ActivityEntry, 0, len(events))
				var lastID int64
				var approxBytes int
				for _, ev := range events {
					// The high-water mark is the max ID, not the last row: the
					// delta cursor must not depend on the load's sort order.
					if ev.ID > lastID {
						lastID = ev.ID
					}
					approxBytes += activityEventApproxBytes(ev)
					entries = append(entries, activityEventToActivityEntry(ev))
				}
				if len(entries) > 0 {
					resp := buildActivityLogResponse(entries, isTerminal, run.Name)
					s.storeActivityMemo(memoKey, &activityMemoEntry{
						lastEventID: lastID,
						isTerminal:  isTerminal,
						entries:     entries,
						resp:        resp,
						approxBytes: approxBytes,
					})
					return resp, activityLogSourcePostgres
				}
			}
		}
	}

	// For running pods, exec into sandbox (tries events.jsonl first internally).
	// A failed or empty exec is remembered briefly so 500ms watch ticks don't
	// hammer pods that have no activity data yet.
	if run.Status.Sandbox != nil && run.Status.Sandbox.SandboxRef != nil && s.clientset != nil {
		execKey := run.Namespace + "/" + run.Status.Sandbox.SandboxRef.Name
		s.execFailMu.Lock()
		failedAt, backingOff := s.execFailAt[execKey]
		s.execFailMu.Unlock()
		if !backingOff || time.Since(failedAt) >= execFailBackoff {
			entries, err := execReadActivityLog(ctx, s.clientset, s.restConfig, run.Status.Sandbox.SandboxRef.Name, run.Namespace)
			if err != nil {
				if isPodNotFoundExecError(err) {
					sandboxMissing = true
				} else if !isTransientPodStartupExecError(err) {
					log.Printf("WARN: failed to exec into AgentRun sandbox %s/%s for activity log: %v", run.Namespace, run.Status.Sandbox.SandboxRef.Name, err)
				}
			}
			if err != nil || len(entries) == 0 {
				s.execFailMu.Lock()
				if s.execFailAt == nil {
					s.execFailAt = make(map[string]time.Time)
				}
				s.execFailAt[execKey] = time.Now()
				s.execFailMu.Unlock()
			}
			if err == nil {
				for i, e := range entries {
					e.EventId = int64(i + 1)
				}
				return buildActivityLogResponse(entries, false, run.Name), activityLogSourceExec
			}
		}
	}

	return &platform.GetActivityLogResponse{
		Entries:    nil,
		IsComplete: isTerminal && (run.Status.Sandbox == nil || run.Status.Sandbox.SandboxRef == nil || s.clientset == nil || sandboxMissing),
	}, activityLogSourceNone
}

// execFailBackoff is how long the activity-log pod-exec fallback waits after
// a failed or empty exec before trying that sandbox pod again.
const execFailBackoff = 3 * time.Second

// Activity memo bounds. Entries are evicted least-recently-accessed first
// when either the count or the approximate byte budget (summed summary and
// detail payload sizes) is exceeded; a handful of runs with huge tool
// outputs must not pin gigabytes of built responses.
const (
	activityMemoMaxEntries = 128
	activityMemoMaxBytes   = 256 << 20
)

func activityEventApproxBytes(ev store.ActivityEvent) int {
	return len(ev.Summary) + len(ev.Detail)
}

// activityEntryApproxBytes estimates the retained size of an already-parsed
// entry (S3-sourced logs never pass through store.ActivityEvent).
func activityEntryApproxBytes(e *platform.ActivityEntry) int {
	if e == nil {
		return 0
	}
	return len(e.Message) + len(e.InputRaw) + len(e.Output)
}

// storeActivityMemo caches an activity-log response, evicting
// least-recently-accessed entries until the cache is within both its count
// and byte budgets. The new entry is always stored (it is what the caller is
// about to serve), even when it alone exceeds the byte budget.
func (s *Server) storeActivityMemo(key string, entry *activityMemoEntry) {
	s.activityMemoMu.Lock()
	defer s.activityMemoMu.Unlock()
	if s.activityMemo == nil {
		s.activityMemo = make(map[string]*activityMemoEntry)
	}
	entry.lastAccess = time.Now()
	s.activityMemo[key] = entry
	totalBytes := 0
	for _, m := range s.activityMemo {
		totalBytes += m.approxBytes
	}
	for len(s.activityMemo) > activityMemoMaxEntries || totalBytes > activityMemoMaxBytes {
		var oldestKey string
		var oldest time.Time
		for k, m := range s.activityMemo {
			if k == key {
				continue
			}
			if oldestKey == "" || m.lastAccess.Before(oldest) {
				oldestKey, oldest = k, m.lastAccess
			}
		}
		if oldestKey == "" {
			return
		}
		totalBytes -= s.activityMemo[oldestKey].approxBytes
		delete(s.activityMemo, oldestKey)
	}
}

// dropActivityMemo releases a run's memoized activity log.
func (s *Server) dropActivityMemo(key string) {
	s.activityMemoMu.Lock()
	delete(s.activityMemo, key)
	s.activityMemoMu.Unlock()
}

// fetchPlanMarkdown reads the plan.md key from the referenced ConfigMap.
// Returns empty string on any error (ConfigMap missing, key missing, etc.).
func (s *Server) fetchPlanMarkdown(ctx context.Context, namespace, configMapName string) string {
	if configMapName == "" {
		return ""
	}
	cm := &corev1.ConfigMap{}
	if err := s.k8sClient.Get(ctx, client.ObjectKey{
		Namespace: namespace,
		Name:      configMapName,
	}, cm); err != nil {
		log.Printf("WARN: failed to fetch plan ConfigMap %s/%s: %v", namespace, configMapName, err)
		return ""
	}
	return cm.Data["plan.md"]
}

func recentActivityFromStore(events []store.ActivityEvent) []*platform.AgentActivity {
	if len(events) == 0 {
		return nil
	}
	out := make([]*platform.AgentActivity, 0, len(events))
	for i := len(events) - 1; i >= 0; i-- {
		ev := events[i]
		out = append(out, &platform.AgentActivity{
			TimestampUnix: ev.CreatedAt.Unix(),
			EventType:     ev.EventType,
			Summary:       ev.Summary,
		})
	}
	return out
}

// conversationFromMessages maps durable session messages onto the API
// conversation. User messages additionally carry their queue/steering state:
// a message stays pending until the agent loop records its delivery, so the
// UI can render it as "queued"/"steering" instead of a transcript bubble.
//
// Pending is suppressed only for the run's kickoff message: it is the request
// that started the run and must render while the sandbox is provisioning.
// Every later user message remains pending until the runner durably stamps its
// delivery, including after the run becomes terminal. Terminal state changes
// whether the backlog is actionable, not whether the agent actually saw it.
func conversationFromMessages(msgs []store.Message, _ string) []*platform.ChatMessage {
	var firstUserID int64
	for _, msg := range msgs {
		if firstUserID == 0 && msg.Role == "user" {
			firstUserID = msg.ID
		}
	}

	out := make([]*platform.ChatMessage, 0, len(msgs))
	for _, msg := range msgs {
		// Cancelled messages were withdrawn by the user before the agent
		// consumed them; they are not part of the conversation. The typed
		// delivery_state column is authoritative (migration 036 backfilled
		// it and an insert trigger maintains it); the metadata check remains
		// as belt-and-braces for non-Postgres stores.
		if msg.Role == "user" && (msg.DeliveryState == "cancelled" || sessionclient.UserMessageCancelled(msg.Metadata)) {
			continue
		}
		cm := &platform.ChatMessage{
			Id:               msg.ID,
			Role:             msg.Role,
			Content:          msg.Content,
			TimestampUnix:    msg.CreatedAt.Unix(),
			DeliverySequence: msg.DeliverySequence,
			DeliveryState:    msg.DeliveryState,
		}
		for _, img := range sessionclient.ImagesFromMetadata(msg.Metadata) {
			cm.ImageDataUrls = append(cm.ImageDataUrls, img.DataURL())
		}
		if msg.Role == "user" {
			mode, deliveredAt := sessionclient.UserMessageStateFromMetadata(msg.Metadata)
			// The typed claimed_at column is the authoritative delivery
			// stamp; the metadata mirror is a legacy fallback slated for
			// removal once no pre-typed writers remain.
			if msg.ClaimedAt != nil {
				deliveredAt = msg.ClaimedAt.Unix()
			}
			cm.QueueMode = string(mode)
			cm.DeliveredAtUnix = deliveredAt
			cm.Pending = (msg.DeliveryState == "pending" || msg.DeliveryState == "" && deliveredAt == 0) && msg.ID != firstUserID
		}
		out = append(out, cm)
	}
	return out
}

// enrichAgentRunProto fully enriches a run for detail surfaces (GetAgentRun,
// WatchAgentRun): conversation, recent activity, plan/spec content, and live
// mode instructions, on top of everything the summary enrichment provides.
func (s *Server) enrichAgentRunProto(ctx context.Context, pb *platform.AgentRun) (*platform.AgentRun, error) {
	return s.enrichAgentRunProtoMode(ctx, pb, true, nil)
}

// enrichAgentRunSummaryProto enriches only the cheap, card-visible fields for
// list surfaces (ListAgentRuns, WatchAgentRuns): session metrics, send
// readiness, pending actions/input, latest activity, PR loop, and ownership
// ACLs. It skips conversation, plan/spec content, and mode-instruction lookups.
// A non-nil batch replaces per-run Postgres lookups with pre-loaded maps.
func (s *Server) enrichAgentRunSummaryProto(ctx context.Context, pb *platform.AgentRun, batch *agentRunEnrichBatch) (*platform.AgentRun, error) {
	return s.enrichAgentRunProtoMode(ctx, pb, false, batch)
}

// agentRunEnrichBatch holds pre-loaded state for enriching many runs in one
// request without per-run Postgres queries. Keys are namespace+"/"+name.
type agentRunEnrichBatch struct {
	sessions       map[string]*store.Session
	latestActivity map[uuid.UUID]store.ActivityEvent
	// owners maps "agent_run" resources to their direct owner IDs.
	owners map[string]string
	// triggerOwners maps trigger resource type -> resource key -> owner ID,
	// for the trigger-ownership fallback of runs without their own record.
	triggerOwners map[string]map[string]string
	// shares maps run keys to the calling actor's share permission.
	shares  map[string]string
	ownerOf func(userID string) *platform.ResourceOwner
}

// enrichSessionState is the session-derived half of an enrich batch: the
// session row and newest activity summary per run.
type enrichSessionState struct {
	sessions       map[string]*store.Session
	latestActivity map[uuid.UUID]store.ActivityEvent
}

// probeEnrichSessionsTTL caches the namespace-wide session/latest-activity
// load shared by fleet streams that need to enrich most of the fleet at once
// (initial ticks). Kept below the 2s fleet tick so a single stream still
// observes every tick's changes.
const probeEnrichSessionsTTL = 1 * time.Second

// loadEnrichSessionState loads sessions and their newest activity summary.
// With a non-nil keys set only those runs are loaded (one indexed query
// bounded by the changed set, the common fleet-tick case); with nil keys the
// whole namespace is loaded and, when shared is true, coalesced across
// concurrent streams for probeEnrichSessionsTTL. The returned maps are
// read-only when shared.
func (s *Server) loadEnrichSessionState(ctx context.Context, namespace string, keys []store.AgentRunKey, shared bool) (*enrichSessionState, error) {
	load := func(ctx context.Context) (*enrichSessionState, error) {
		var sessions []store.Session
		var err error
		if lister, ok := s.stateStore.(store.SessionsByRunsLister); ok && keys != nil {
			sessions, err = lister.ListSessionsByRuns(ctx, keys)
		} else {
			sessions, err = s.stateStore.ListSessionsByNamespace(ctx, namespace)
		}
		if err != nil {
			return nil, fmt.Errorf("bulk-listing sessions for run enrichment: %w", err)
		}
		st := &enrichSessionState{
			sessions:       make(map[string]*store.Session, len(sessions)),
			latestActivity: make(map[uuid.UUID]store.ActivityEvent),
		}
		sessionIDs := make([]uuid.UUID, 0, len(sessions))
		for i := range sessions {
			sess := &sessions[i]
			st.sessions[sess.AgentRunNS+"/"+sess.AgentRunName] = sess
			sessionIDs = append(sessionIDs, sess.ID)
		}
		if activityStore, ok := s.stateStore.(interface {
			GetLatestActivityBySessions(context.Context, []uuid.UUID) (map[uuid.UUID]store.ActivityEvent, error)
		}); ok && len(sessionIDs) > 0 {
			if latest, err := activityStore.GetLatestActivityBySessions(ctx, sessionIDs); err == nil {
				st.latestActivity = latest
			} else {
				log.Printf("WARN: bulk-listing latest AgentRun activity: %v", err)
			}
		}
		return st, nil
	}
	if keys != nil || !shared {
		return load(ctx)
	}
	return probeCacheDo(ctx, &s.probes, "enrichsess|"+namespace, probeEnrichSessionsTTL, load)
}

// newAgentRunEnrichBatch bulk-loads the session, ownership, and share state
// needed to enrich every run in a namespace ("" = all namespaces) with one
// query per state kind. Returns nil when the store cannot bulk-load (no state
// store, no bulk ownership support, or a query failed); callers pass the nil
// batch through and enrichment falls back to per-run queries.
// cachedACL selects the coalesced (watch-tick) ACL loads; unary paths pass
// false so ownership/share mutations are visible in the next request.
func (s *Server) newAgentRunEnrichBatch(ctx context.Context, namespace string, cachedACL bool) *agentRunEnrichBatch {
	return s.newAgentRunEnrichBatchForRuns(ctx, namespace, nil, cachedACL)
}

// newAgentRunEnrichBatchForRuns is newAgentRunEnrichBatch restricted to an
// explicit set of runs: session state is loaded only for keys (nil = the
// whole namespace), so a fleet tick that re-emits a handful of changed runs
// does not reload every session in the fleet. Ownership and share state are
// namespace-wide bulk loads either way (cached across ticks when cachedACL).
func (s *Server) newAgentRunEnrichBatchForRuns(ctx context.Context, namespace string, keys []store.AgentRunKey, cachedACL bool) *agentRunEnrichBatch {
	if s.stateStore == nil {
		return nil
	}
	bulk, ok := s.stateStore.(resourceOwnersByTypeStore)
	if !ok {
		return nil
	}
	sessionState, err := s.loadEnrichSessionState(ctx, namespace, keys, cachedACL)
	if err != nil {
		log.Printf("WARN: %v", err)
		return nil
	}
	owners, err := s.cachedResourceOwnersByType(ctx, bulk, "agent_run", cachedACL)
	if err != nil {
		log.Printf("WARN: bulk-listing agent_run owners for run enrichment: %v", err)
		return nil
	}
	b := &agentRunEnrichBatch{
		sessions:       sessionState.sessions,
		latestActivity: sessionState.latestActivity,
		owners:         make(map[string]string, len(owners)),
		triggerOwners:  make(map[string]map[string]string, len(agentRunTriggerResourceTypes)),
		shares:         make(map[string]string),
		ownerOf:        s.ownerEnricher(ctx),
	}
	for _, o := range owners {
		b.owners[o.ResourceNamespace+"/"+o.ResourceID] = o.OwnerID
	}
	for _, resourceType := range agentRunTriggerResourceTypes {
		if _, done := b.triggerOwners[resourceType]; done {
			continue
		}
		triggerOwners, err := s.cachedResourceOwnersByType(ctx, bulk, resourceType, cachedACL)
		if err != nil {
			log.Printf("WARN: bulk-listing %s owners for run enrichment: %v", resourceType, err)
			return nil
		}
		m := make(map[string]string, len(triggerOwners))
		for _, o := range triggerOwners {
			m[o.ResourceNamespace+"/"+o.ResourceID] = o.OwnerID
		}
		b.triggerOwners[resourceType] = m
	}
	if actor := requestActorFromContext(ctx); actor.Subject != "" {
		shares, err := s.cachedSharedWithMe(ctx, actor.Subject, "agent_run", cachedACL)
		if err != nil {
			log.Printf("WARN: bulk-listing agent_run shares for run enrichment: %v", err)
			return nil
		}
		for _, sh := range shares {
			b.shares[sh.ResourceNamespace+"/"+sh.ResourceID] = sh.Permission
		}
	}
	return b
}

// triggerOwnership mirrors Server.triggerResourceOwnership using the
// pre-loaded trigger owner maps.
func (b *agentRunEnrichBatch) triggerOwnership(pb *platform.AgentRun) *store.ResourceOwnership {
	if pb.Trigger == nil {
		return nil
	}
	resourceType := agentRunTriggerResourceTypes[pb.Trigger.Kind]
	triggerName := strings.TrimSpace(pb.Trigger.Name)
	if resourceType == "" || triggerName == "" {
		return nil
	}
	if ownerID, ok := b.triggerOwners[resourceType][pb.Namespace+"/"+triggerName]; ok {
		return &store.ResourceOwnership{OwnerID: ownerID}
	}
	return nil
}

func (s *Server) enrichAgentRunProtoMode(ctx context.Context, pb *platform.AgentRun, full bool, batch *agentRunEnrichBatch) (*platform.AgentRun, error) {
	if pb == nil {
		return nil, nil
	}

	// Enrich durable session data from Postgres. The CRD carries only
	// cluster-visible execution status and artifact pointers.
	if s.stateStore != nil {
		var sess *store.Session
		if batch != nil {
			sess = batch.sessions[pb.Namespace+"/"+pb.Name]
		} else if got, err := s.stateStore.GetSessionByRun(ctx, pb.Name, pb.Namespace); err == nil {
			sess = got
		}
		pb.SendReady, pb.SendReadinessReason = agentRunSendReadiness(pb.Phase, sess)
		if sess != nil {
			// List surfaces receive one bounded latest-activity item from the
			// batched query. Detail surfaces keep the fuller 20-item history below.
			if !full {
				if batch != nil {
					if event, ok := batch.latestActivity[sess.ID]; ok {
						pb.RecentActivity = recentActivityFromStore([]store.ActivityEvent{event})
					}
				} else if events, err := s.stateStore.GetRecentActivity(ctx, sess.ID, 1); err == nil {
					pb.RecentActivity = recentActivityFromStore(events)
				}
			}

			// Override metrics from Postgres (source of truth) instead of CRD.
			if m, ok := parseSessionMetrics(sess); ok {
				pb.CostUsd = fmt.Sprintf("%.4f", m.CostUSD)
				pb.InputTokens = m.InputTokens
				pb.OutputTokens = m.OutputTokens
				if m.ToolCallCount > 0 {
					pb.ToolCallCount = m.ToolCallCount
				}
				pb.ContextTriggerTokens = m.ContextTriggerTokens
				pb.ContextTargetTokens = m.ContextTargetTokens
				pb.ContextTokens = m.ContextTokens
			}

			if full {
				if conv, err := s.sessionConversation(ctx, sess.ID, pb.Phase); err == nil {
					pb.Conversation = conv
				}

				if events, err := s.stateStore.GetRecentActivity(ctx, sess.ID, 20); err == nil {
					pb.RecentActivity = recentActivityFromStore(events)
				}

				if planArt, planErr := s.stateStore.GetArtifact(ctx, sess.ID, "plan"); planErr == nil && planArt != nil {
					if len(planArt.Content) > 0 {
						pb.CurrentPlan = planArt.Content
					}
					if planArt.Metadata != nil {
						var meta map[string]string
						if json.Unmarshal(planArt.Metadata, &meta) == nil {
							if v := meta["summary"]; v != "" {
								pb.PlanSummary = v
							}
							if v := meta["updated_at"]; v != "" {
								pb.PlanUpdatedAt = v
							}
						}
					}
					if pb.PlanSummary == "" && len(planArt.Content) > 0 {
						if len(planArt.Content) > 200 {
							pb.PlanSummary = planArt.Content[:200] + "..."
						} else {
							pb.PlanSummary = planArt.Content
						}
					}
				}
			}

			pb.PendingActions = nil
			if len(sess.PendingActions) > 2 { // "[]" is empty
				var actions []struct {
					ID    string `json:"id"`
					Label string `json:"label"`
					Mode  string `json:"mode"`
					Style string `json:"style"`
				}
				if json.Unmarshal(sess.PendingActions, &actions) == nil {
					for _, a := range actions {
						pb.PendingActions = append(pb.PendingActions, &platform.QuickAction{
							Id:    a.ID,
							Label: a.Label,
							Mode:  a.Mode,
							Style: a.Style,
						})
					}
				}
			}

			pb.UserInputRequest = nil
			phase := platformv1alpha1.AgentRunPhase(pb.Phase)
			if pendingInputType := strings.TrimSpace(sess.PendingInputType); pendingInputType != "" && !isTerminalAgentRunPhase(phase) && phase != platformv1alpha1.AgentRunPhasePaused {
				if pb.UserInputRequest == nil {
					pb.UserInputRequest = &platform.UserInputRequest{}
				}
				pb.UserInputRequest.Type = pendingInputType
				pb.UserInputRequest.Message = sess.PendingQuestion
				pb.UserInputRequest.RequestId = sess.PendingRequestID
				pb.UserInputRequest.Actions = nil
				for _, qa := range pb.PendingActions {
					pb.UserInputRequest.Actions = append(pb.UserInputRequest.Actions, &platform.QuickAction{
						Id:    qa.Id,
						Label: qa.Label,
						Mode:  qa.Mode,
						Style: qa.Style,
					})
				}
			}
		}
	}

	if full && pb.CurrentPlan == "" && pb.PlanArtifactName != "" {
		pb.CurrentPlan = s.fetchPlanMarkdown(ctx, pb.Namespace, pb.PlanArtifactName)
	}
	if full && pb.SpecArtifactName != "" {
		key := pb.SpecArtifactKey
		if key == "" {
			key = "spec.md"
		}
		if key == "spec.md" {
			pb.SpecMarkdown = s.fetchSpecMarkdown(ctx, pb.Namespace, pb.SpecArtifactName)
		} else {
			cm := &corev1.ConfigMap{}
			if err := s.k8sClient.Get(ctx, client.ObjectKey{Namespace: pb.Namespace, Name: pb.SpecArtifactName}, cm); err == nil {
				pb.SpecMarkdown = cm.Data[key]
			}
		}
	}
	s.enrichPRLoopMaxRounds(ctx, pb)

	// Refresh mode instructions & phases from the live ModeTemplate CRD so the
	// dashboard always shows the latest template, not the stale snapshot pinned
	// at run-creation time. Try Spec.ModeRef first, fall back to Status fields
	// (which are updated on every mode switch).
	if full {
		modeName := pb.ModeRefName
		if modeName == "" {
			modeName = pb.ModeName
		}
		if modeName != "" {
			var liveTmpl platformv1alpha1.ModeTemplate
			if err := s.k8sClient.Get(ctx, client.ObjectKey{Name: modeName}, &liveTmpl); err == nil {
				pb.ModeInstructions = liveTmpl.Spec.Instructions
			}
		}
	}

	// Enrich owner and the caller's permission from the collaboration store.
	if s.stateStore != nil {
		actor := requestActorFromContext(ctx)
		var ownership *store.ResourceOwnership
		if batch != nil {
			if ownerID, ok := batch.owners[pb.Namespace+"/"+pb.Name]; ok {
				ownership = &store.ResourceOwnership{OwnerID: ownerID}
			} else {
				ownership = batch.triggerOwnership(pb)
			}
		} else {
			var err error
			ownership, err = s.stateStore.GetResourceOwner(ctx, "agent_run", pb.Name, pb.Namespace)
			if err != nil || ownership == nil {
				// Trigger-created runs (Slack/GitHub/cron/Linear) may have no
				// ownership record of their own (the connector's owner write is
				// best-effort, and legacy runs predate it). Fall back to the owner
				// of the trigger resource that created the run, so that user can
				// manage (stop, delete) the runs their agent spawned.
				ownership = s.triggerResourceOwnership(ctx, pb)
			}
		}
		if ownership != nil {
			if batch != nil {
				pb.Owner = batch.ownerOf(ownership.OwnerID)
			} else {
				pb.Owner = s.enrichOwner(ctx, ownership.OwnerID)
			}
		}
		switch {
		case actor.Role == "admin" || actor.Role == "owner":
			// Admins manage every run, including trigger-created runs
			// (Slack/GitHub/cron/Linear) that never recorded an owner.
			pb.MyPermission = "admin"
		case ownership != nil && actor.Subject != "" && ownership.OwnerID == actor.Subject:
			pb.MyPermission = "owner"
		case ownership != nil && actor.Subject != "":
			if batch != nil {
				if permission, ok := batch.shares[pb.Namespace+"/"+pb.Name]; ok {
					pb.MyPermission = permission
				}
			} else if share, _ := s.stateStore.GetSharePermission(ctx, "agent_run", pb.Name, pb.Namespace, actor.Subject); share != nil {
				pb.MyPermission = share.Permission
			}
		}
	}

	return pb, nil
}

// agentRunTriggerResourceTypes maps an AgentRun trigger kind to the
// collaboration-store resource type under which the dashboard records that
// trigger's owner.
var agentRunTriggerResourceTypes = map[string]string{
	slackTriggerKind:   slackResourceType,
	"Cron":             cronResourceType,
	"GitHubRepository": githubRepositoryResourceType,
	"LinearProject":    linearProjectResourceType,
	"SecurityScan":     securityScanResourceType,
}

// triggerResourceOwnership resolves the owner of the trigger resource that
// created a run (e.g. the SlackAgent behind a Slack conversation run). Used as
// the run's effective ownership when the run itself has no ownership record,
// so trigger-created runs are manageable by the trigger's owner. Returns nil
// when the run has no trigger, the trigger kind records no ownership, or no
// owner is recorded.
func (s *Server) triggerResourceOwnership(ctx context.Context, pb *platform.AgentRun) *store.ResourceOwnership {
	if s.stateStore == nil || pb.Trigger == nil {
		return nil
	}
	resourceType := agentRunTriggerResourceTypes[pb.Trigger.Kind]
	triggerName := strings.TrimSpace(pb.Trigger.Name)
	if resourceType == "" || triggerName == "" {
		return nil
	}
	ownership, err := s.stateStore.GetResourceOwner(ctx, resourceType, triggerName, pb.Namespace)
	if err != nil {
		return nil
	}
	return ownership
}

func (s *Server) enrichPRLoopMaxRounds(ctx context.Context, pb *platform.AgentRun) {
	if pb == nil || pb.PrLoop == nil || pb.Trigger == nil || pb.Trigger.Kind != "GitHubRepository" || pb.Trigger.Name == "" {
		return
	}
	var repo triggersv1alpha1.GitHubRepository
	if err := s.k8sClient.Get(ctx, client.ObjectKey{Namespace: pb.Namespace, Name: pb.Trigger.Name}, &repo); err != nil {
		return
	}
	if repo.Spec.ReviewLoop != nil && repo.Spec.ReviewLoop.MaxRounds > 0 {
		pb.PrLoop.MaxRounds = repo.Spec.ReviewLoop.MaxRounds
	}
}

// applyActivityLogRequestOptions applies the cursor/pagination/preview options
// of an activity-log request to a fully built response, returning a new
// response so the memoized one is never mutated. A request with all options
// zero returns resp unchanged.
func applyActivityLogRequestOptions(resp *platform.GetActivityLogResponse, req *platform.GetActivityLogRequest) *platform.GetActivityLogResponse {
	if req.GetSinceEventId() == 0 && req.GetBeforeEventId() == 0 && req.GetLimit() == 0 && req.GetPayloadPreviewBytes() == 0 {
		return resp
	}
	entries := resp.Entries
	if since := req.GetSinceEventId(); since > 0 {
		i := 0
		for i < len(entries) && entries[i].EventId <= since {
			i++
		}
		entries = entries[i:]
	}
	if before := req.GetBeforeEventId(); before > 0 {
		j := len(entries)
		for j > 0 && entries[j-1].EventId >= before {
			j--
		}
		entries = entries[:j]
	}
	hasMoreBefore := false
	if limit := int(req.GetLimit()); limit > 0 && len(entries) > limit {
		entries = entries[len(entries)-limit:]
		hasMoreBefore = true
	}
	// BuildSubagentGraph stores detail_entry_indices as positions in the
	// full entry slice; once the cursor/pagination options sliced it, those
	// positions no longer match the response and must be remapped (detected
	// before truncation, which clones entries but keeps positions).
	sliced := len(entries) != len(resp.Entries) ||
		(len(entries) > 0 && entries[0] != resp.Entries[0])
	if n := int(req.GetPayloadPreviewBytes()); n > 0 {
		entries = truncateActivityEntries(entries, n)
	}
	graph := resp.SubagentGraph
	if sliced {
		graph = remapSubagentGraph(graph, entries)
	}
	out := &platform.GetActivityLogResponse{
		Entries:       entries,
		IsComplete:    resp.IsComplete,
		SubagentGraph: graph,
		Delta:         resp.Delta,
		Reset_:        resp.Reset_,
		HasMoreBefore: hasMoreBefore,
	}
	if len(entries) > 0 {
		out.FirstEventId = entries[0].EventId
		out.LastEventId = entries[len(entries)-1].EventId
	}
	return out
}

// remapSubagentGraph returns a copy of graph whose detail_entry_indices are
// valid positions in entries (resolved through the durable event IDs);
// indices whose entries fell outside the slice are dropped. Nodes are cloned
// so memoized graphs are never mutated; detail_entry_event_ids pass through
// unchanged as the slice-independent reference.
func remapSubagentGraph(graph *platform.SubagentGraph, entries []*platform.ActivityEntry) *platform.SubagentGraph {
	if graph == nil {
		return nil
	}
	indexByID := make(map[int64]int32, len(entries))
	for i, e := range entries {
		if e.EventId != 0 {
			indexByID[e.EventId] = int32(i)
		}
	}
	out := &platform.SubagentGraph{
		RootId:       graph.RootId,
		Edges:        graph.Edges,
		OrphanIds:    graph.OrphanIds,
		HasSubagents: graph.HasSubagents,
		Nodes:        make([]*platform.SubagentGraphNode, len(graph.Nodes)),
	}
	for i, n := range graph.Nodes {
		c := proto.Clone(n).(*platform.SubagentGraphNode)
		var indices []int32
		for _, id := range c.DetailEntryEventIds {
			if idx, ok := indexByID[id]; ok {
				indices = append(indices, idx)
			}
		}
		c.DetailEntryIndices = indices
		out.Nodes[i] = c
	}
	return out
}

// truncateActivityEntries returns entries with any oversized tool payloads
// truncated to n bytes and flagged. Entries needing truncation are cloned so
// callers never mutate shared (memoized) entries; untouched entries are
// shared by reference.
func truncateActivityEntries(entries []*platform.ActivityEntry, n int) []*platform.ActivityEntry {
	out := entries
	copied := false
	for i, e := range entries {
		if len(e.InputRaw) <= n && len(e.Output) <= n {
			continue
		}
		if !copied {
			out = append([]*platform.ActivityEntry(nil), entries...)
			copied = true
		}
		c := proto.Clone(e).(*platform.ActivityEntry)
		if len(c.InputRaw) > n {
			c.InputRaw = truncateUTF8(c.InputRaw, n)
			c.InputTruncated = true
		}
		if len(c.Output) > n {
			c.Output = truncateUTF8(c.Output, n)
			c.OutputTruncated = true
		}
		out[i] = c
	}
	return out
}

// truncateUTF8 cuts s to at most n bytes, backing up over a partial trailing
// rune so the result stays valid UTF-8 (for valid input).
func truncateUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && cut > n-utf8.UTFMax && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}
