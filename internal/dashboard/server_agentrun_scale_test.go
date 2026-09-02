package dashboard

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"
	"unsafe"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/store"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
)

// s3FixtureReader returns an S3 activity reader that serves canned entries
// per URL instead of downloading from S3, counting fetches per URL.
func s3FixtureReader(fixtures map[string][]*platform.ActivityEntry) *s3ActivityReader {
	return &s3ActivityReader{fetch: func(_ context.Context, url string) ([]*platform.ActivityEntry, error) {
		entries, ok := fixtures[url]
		if !ok {
			return nil, errors.New("fixture not found: " + url)
		}
		return entries, nil
	}}
}

// scaleStore adds the optional scale capabilities to countingBatchStore so the
// scoped fleet-tick paths can be exercised and counted.
type scaleStore struct {
	*countingBatchStore

	mu                     sync.Mutex
	listSessionsByRunsKeys [][]store.AgentRunKey
	metricsNamespaces      []string
	getActivityEventCalls  int
	getAllActivityCalls    int
}

func (s *scaleStore) ListSessionsByRuns(ctx context.Context, keys []store.AgentRunKey) ([]store.Session, error) {
	s.mu.Lock()
	s.listSessionsByRunsKeys = append(s.listSessionsByRunsKeys, append([]store.AgentRunKey(nil), keys...))
	s.mu.Unlock()
	all, err := s.collaborationStateStore.ListSessionsByNamespace(ctx, "")
	if err != nil {
		return nil, err
	}
	want := make(map[store.AgentRunKey]bool, len(keys))
	for _, k := range keys {
		want[k] = true
	}
	var out []store.Session
	for _, sess := range all {
		if want[store.AgentRunKey{Namespace: sess.AgentRunNS, Name: sess.AgentRunName}] {
			out = append(out, sess)
		}
	}
	return out, nil
}

func (s *scaleStore) ListSessionMetricsByNamespace(_ context.Context, namespace string) ([]store.SessionMetricsEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.metricsNamespaces = append(s.metricsNamespaces, namespace)
	return []store.SessionMetricsEntry{{AgentRunName: "run-a", AgentRunNS: "default", CostUSD: 1.5, InputTokens: 10}}, nil
}

func (s *scaleStore) GetActivityEvent(_ context.Context, sessionID uuid.UUID, eventID int64) (*store.ActivityEvent, error) {
	s.mu.Lock()
	s.getActivityEventCalls++
	s.mu.Unlock()
	for _, ev := range s.allActivityBySession[sessionID] {
		if ev.ID == eventID {
			return &ev, nil
		}
	}
	return nil, store.ErrActivityEventNotFound
}

func (s *scaleStore) GetAllActivity(ctx context.Context, sessionID uuid.UUID) ([]store.ActivityEvent, error) {
	s.mu.Lock()
	s.getAllActivityCalls++
	s.mu.Unlock()
	return s.countingBatchStore.GetAllActivity(ctx, sessionID)
}

type recordingAgentRunEventConn struct {
	mu   sync.Mutex
	sent []*platform.AgentRunEvent
	ch   chan *platform.AgentRunEvent
}

func (c *recordingAgentRunEventConn) Spec() connect.Spec           { return connect.Spec{} }
func (c *recordingAgentRunEventConn) Peer() connect.Peer           { return connect.Peer{} }
func (c *recordingAgentRunEventConn) Receive(any) error            { return errors.New("not implemented") }
func (c *recordingAgentRunEventConn) RequestHeader() http.Header   { return http.Header{} }
func (c *recordingAgentRunEventConn) ResponseHeader() http.Header  { return http.Header{} }
func (c *recordingAgentRunEventConn) ResponseTrailer() http.Header { return http.Header{} }
func (c *recordingAgentRunEventConn) Send(msg any) error {
	ev, ok := msg.(*platform.AgentRunEvent)
	if !ok {
		return errors.New("unexpected message type")
	}
	clone := proto.Clone(ev).(*platform.AgentRunEvent)
	c.mu.Lock()
	c.sent = append(c.sent, clone)
	c.mu.Unlock()
	if c.ch != nil {
		c.ch <- clone
	}
	return nil
}

func newAgentRunEventServerStream(conn connect.StreamingHandlerConn) *connect.ServerStream[platform.AgentRunEvent] {
	stream := &connect.ServerStream[platform.AgentRunEvent]{}
	streamPtr := (*struct{ Conn connect.StreamingHandlerConn })(unsafe.Pointer(stream))
	streamPtr.Conn = conn
	return stream
}

func newScaleStore(t *testing.T) *scaleStore {
	t.Helper()
	return &scaleStore{countingBatchStore: &countingBatchStore{collaborationStateStore: newBatchTestState(t)}}
}

func TestWatchAgentRunsLoadsSessionsOnlyForChangedRuns(t *testing.T) {
	ss := newScaleStore(t)
	srv := newBatchTestServer(t, ss)

	ctx, cancel := context.WithCancel(actorContext("admin", "admin", "", ""))
	defer cancel()
	conn := &recordingAgentRunEventConn{ch: make(chan *platform.AgentRunEvent, 16)}
	done := make(chan error, 1)
	go func() {
		done <- srv.WatchAgentRuns(ctx, &platform.WatchAgentRunsRequest{Namespace: "default"}, newAgentRunEventServerStream(conn))
	}()

	waitEvent := func(timeout time.Duration) *platform.AgentRunEvent {
		select {
		case ev := <-conn.ch:
			return ev
		case <-time.After(timeout):
			t.Fatal("timed out waiting for fleet event")
			return nil
		}
	}
	initial := map[string]bool{}
	for range 3 {
		ev := waitEvent(5 * time.Second)
		initial[ev.GetRun().GetName()] = true
	}
	if len(initial) != 3 {
		t.Fatalf("initial events = %v, want run-a, run-b, run-c", initial)
	}
	// The initial tick enriches the whole fleet: one namespace-wide load,
	// no per-key load.
	if ss.listSessionsCalls != 1 || len(ss.listSessionsByRunsKeys) != 0 {
		t.Fatalf("initial tick: namespace loads = %d, keyed loads = %v; want 1 and none", ss.listSessionsCalls, ss.listSessionsByRunsKeys)
	}

	// Change one run; the next tick must reload only that run's session.
	var run platformv1alpha1.AgentRun
	if err := srv.k8sClient.Get(ctx, client.ObjectKey{Namespace: "default", Name: "run-a"}, &run); err != nil {
		t.Fatalf("get run-a: %v", err)
	}
	run.Status.Phase = platformv1alpha1.AgentRunPhaseSucceeded
	if err := srv.k8sClient.Update(ctx, &run); err != nil {
		t.Fatalf("update run-a: %v", err)
	}
	ev := waitEvent(6 * time.Second)
	if ev.GetRun().GetName() != "run-a" || ev.GetType() != "MODIFIED" {
		t.Fatalf("event after update = %v, want MODIFIED run-a", ev)
	}
	ss.mu.Lock()
	keyed := ss.listSessionsByRunsKeys
	ss.mu.Unlock()
	if ss.listSessionsCalls != 1 {
		t.Errorf("namespace-wide session loads = %d, want 1 (initial tick only)", ss.listSessionsCalls)
	}
	if len(keyed) != 1 || len(keyed[0]) != 1 || keyed[0][0] != (store.AgentRunKey{Namespace: "default", Name: "run-a"}) {
		t.Errorf("keyed session loads = %v, want exactly [[default/run-a]]", keyed)
	}
	if ss.getSessionByRunCalls != 0 {
		t.Errorf("per-run GetSessionByRun calls = %d, want 0", ss.getSessionByRunCalls)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("WatchAgentRuns did not return after cancel")
	}
}

func TestEnrichBatchForRunsUsesKeyedSessionLoad(t *testing.T) {
	ss := newScaleStore(t)
	srv := newBatchTestServer(t, ss)
	ctx := actorContext("alice", "member", "", "")

	batch := srv.newAgentRunEnrichBatchForRuns(ctx, "default", []store.AgentRunKey{{Namespace: "default", Name: "run-a"}}, true)
	if batch == nil {
		t.Fatal("batch = nil, want bulk batch")
	}
	if _, ok := batch.sessions["default/run-a"]; !ok {
		t.Errorf("batch sessions = %v, want run-a", batch.sessions)
	}
	if _, ok := batch.sessions["default/run-b"]; ok {
		t.Errorf("batch sessions include run-b although only run-a was requested")
	}
	if ss.listSessionsCalls != 0 || len(ss.listSessionsByRunsKeys) != 1 {
		t.Errorf("namespace loads = %d, keyed loads = %d; want 0 and 1", ss.listSessionsCalls, len(ss.listSessionsByRunsKeys))
	}
	if ss.latestActivityCalls != 1 {
		t.Errorf("latest activity loads = %d, want 1", ss.latestActivityCalls)
	}

	// Shared namespace-wide loads are coalesced across concurrent callers.
	srv.newAgentRunEnrichBatchForRuns(ctx, "default", nil, true)
	srv.newAgentRunEnrichBatchForRuns(ctx, "default", nil, true)
	if ss.listSessionsCalls != 1 {
		t.Errorf("namespace loads after two shared batches = %d, want 1 (coalesced)", ss.listSessionsCalls)
	}
}

func TestAgentRunVisibilityFilterProjectRunsUseBulkLoads(t *testing.T) {
	ms := newCollaborationStateStore()
	addCollaborationOwner(t, ms, projectResourceType, "default", "proj-bob", "bob")
	addCollaborationOwner(t, ms, projectResourceType, "default", "proj-alice", "alice")
	addCollaborationOwner(t, ms, projectResourceType, "default", "proj-shared", "bob")
	addCollaborationShare(ms, "share-p", projectResourceType, "default", "proj-shared", "alice", "bob", "viewer")
	cs := &countingBatchStore{collaborationStateStore: ms}

	projectRun := func(name, project string) *platformv1alpha1.AgentRun {
		return &platformv1alpha1.AgentRun{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
			Spec: platformv1alpha1.AgentRunSpec{
				Trigger: platformv1alpha1.TriggerRef{Kind: "ProjectChat", Name: "chat"},
				Context: &platformv1alpha1.AgentRunContext{ProjectRef: &platformv1alpha1.ProjectRef{Kind: "Project", Name: project}},
			},
		}
	}
	srv := &Server{stateStore: cs}
	visible := srv.agentRunVisibilityFilter(actorContext("alice", "member", "", ""), false)

	cases := map[string]bool{
		"proj-bob":     false, // owned by bob, not shared
		"proj-alice":   true,  // owned by alice
		"proj-shared":  true,  // owned by bob, shared with alice
		"proj-unowned": true,  // no ownership record
	}
	for project, want := range cases {
		if got := visible(projectRun("run-"+project, project)); got != want {
			t.Errorf("visible(run in %s) = %v, want %v", project, got, want)
		}
	}
	if cs.getResourceOwnerCalls != 0 || cs.getSharePermissionCalls != 0 {
		t.Errorf("per-run lookups: GetResourceOwner=%d GetSharePermission=%d, want 0/0", cs.getResourceOwnerCalls, cs.getSharePermissionCalls)
	}

	// Parity with the per-resource authorization the filter replaces.
	for project, want := range cases {
		err := srv.requireAgentRunViewerForRun(actorContext("alice", "member", "", ""), projectRun("run-"+project, project))
		if got := err == nil; got != want {
			t.Errorf("requireAgentRunViewerForRun(run in %s) allowed=%v, want %v (err=%v)", project, got, want, err)
		}
	}
}

func TestGetActivityLogTerminalRunMemoizesS3Response(t *testing.T) {
	scheme := newDashboardTestScheme(t)
	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "run-s3", Namespace: "default"},
		Status: platformv1alpha1.AgentRunStatus{
			Phase:     platformv1alpha1.AgentRunPhaseSucceeded,
			Artifacts: &platformv1alpha1.AgentRunArtifacts{EventsLogURL: "s3://bucket/run-s3.events.jsonl"},
		},
	}
	fetches := 0
	reader := &s3ActivityReader{fetch: func(context.Context, string) ([]*platform.ActivityEntry, error) {
		fetches++
		return []*platform.ActivityEntry{
			{TimestampUnix: 1, Type: "tool_use", ToolUseId: "t1", InputRaw: "{\"cmd\":\"ls\"}", Output: "a b c"},
			{TimestampUnix: 2, Type: "assistant_text", Message: "done"},
		}, nil
	}}
	srv := &Server{k8sClient: fake.NewClientBuilder().WithScheme(scheme).WithObjects(run).Build(), scheme: scheme, s3Reader: reader}

	req := &platform.GetActivityLogRequest{Namespace: "default", Name: "run-s3"}
	first, err := srv.GetActivityLog(context.Background(), req)
	if err != nil {
		t.Fatalf("GetActivityLog #1: %v", err)
	}
	second, err := srv.GetActivityLog(context.Background(), req)
	if err != nil {
		t.Fatalf("GetActivityLog #2: %v", err)
	}
	if fetches != 1 {
		t.Fatalf("S3 fetches = %d, want 1 (memoized)", fetches)
	}
	if first != second {
		t.Fatal("second GetActivityLog rebuilt the response instead of serving the memo")
	}
	memo := srv.activityMemo["default/run-s3"]
	if memo == nil || memo.s3URL != "s3://bucket/run-s3.events.jsonl" || memo.approxBytes == 0 {
		t.Fatalf("memo = %+v, want S3-tagged entry with a byte estimate", memo)
	}

	// Entry detail and usage are answered from the same memo.
	detail, err := srv.GetActivityEntryDetail(context.Background(), &platform.GetActivityEntryDetailRequest{Namespace: "default", Name: "run-s3", ToolUseId: "t1"})
	if err != nil {
		t.Fatalf("GetActivityEntryDetail: %v", err)
	}
	if detail.Output != "a b c" {
		t.Fatalf("detail output = %q, want tool output", detail.Output)
	}
	if _, err := srv.GetAgentRunUsage(context.Background(), &platform.GetAgentRunUsageRequest{Namespace: "default", Name: "run-s3"}); err != nil {
		t.Fatalf("GetAgentRunUsage: %v", err)
	}
	if fetches != 1 {
		t.Fatalf("S3 fetches after detail/usage = %d, want 1", fetches)
	}
}

func TestS3FetchEventStreamCoalescesConcurrentFetches(t *testing.T) {
	var mu sync.Mutex
	fetches := 0
	release := make(chan struct{})
	reader := &s3ActivityReader{fetch: func(context.Context, string) ([]*platform.ActivityEntry, error) {
		mu.Lock()
		fetches++
		mu.Unlock()
		<-release
		return []*platform.ActivityEntry{{Type: "result"}}, nil
	}}
	var wg sync.WaitGroup
	results := make([]int, 8)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			entries, err := reader.FetchEventStream(context.Background(), "s3://bucket/x.jsonl")
			if err != nil {
				t.Errorf("FetchEventStream: %v", err)
				return
			}
			results[i] = len(entries)
		}(i)
	}
	// Let every goroutine register before the single download completes.
	deadline := time.Now().Add(2 * time.Second)
	for {
		mu.Lock()
		n := fetches
		mu.Unlock()
		if n >= 1 || time.Now().After(deadline) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(20 * time.Millisecond)
	close(release)
	wg.Wait()
	if fetches != 1 {
		t.Fatalf("underlying fetches = %d, want 1 for 8 concurrent callers", fetches)
	}
	for i, n := range results {
		if n != 1 {
			t.Errorf("caller %d got %d entries, want 1", i, n)
		}
	}
}

func TestGetActivityEntryDetailUsesSingleRowLookup(t *testing.T) {
	ss := newScaleStore(t)
	sess, err := ss.GetSessionByRun(context.Background(), "run-a", "default")
	if err != nil {
		t.Fatalf("GetSessionByRun: %v", err)
	}
	ss.allActivityBySession = map[uuid.UUID][]store.ActivityEvent{
		sess.ID: {
			{ID: 41, SessionID: sess.ID, EventType: "tool_use", Summary: "ls", Detail: []byte(`{"type":"tool_end","tool":"Bash","tool_use_id":"t41","output":"file-a file-b"}`)},
			{ID: 42, SessionID: sess.ID, EventType: "assistant_text", Summary: "done", Detail: []byte(`{"type":"assistant_text","message":"done"}`)},
		},
	}
	srv := newBatchTestServer(t, ss)

	resp, err := srv.GetActivityEntryDetail(actorContext("alice", "member", "", ""), &platform.GetActivityEntryDetailRequest{Namespace: "default", Name: "run-a", EventId: 41})
	if err != nil {
		t.Fatalf("GetActivityEntryDetail: %v", err)
	}
	if resp.Output != "file-a file-b" {
		t.Fatalf("output = %q, want tool output from the single row", resp.Output)
	}
	if ss.getActivityEventCalls != 1 || ss.getAllActivityCalls != 0 {
		t.Fatalf("GetActivityEvent=%d GetAllActivity=%d, want 1/0 (no history load)", ss.getActivityEventCalls, ss.getAllActivityCalls)
	}

	// Unknown IDs fall back to the full log and then report not found.
	_, err = srv.GetActivityEntryDetail(actorContext("alice", "member", "", ""), &platform.GetActivityEntryDetailRequest{Namespace: "default", Name: "run-a", EventId: 99})
	if connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("unknown event error = %v, want NotFound", err)
	}
}

func TestListResourceMetricsIsNamespaceScopedAndCoalesced(t *testing.T) {
	ss := newScaleStore(t)
	srv := newBatchTestServer(t, ss)
	ctx := context.Background()

	for range 3 {
		if _, err := srv.listResourceMetrics(ctx, "default"); err != nil {
			t.Fatalf("listResourceMetrics: %v", err)
		}
	}
	ss.mu.Lock()
	namespaces := ss.metricsNamespaces
	ss.mu.Unlock()
	if len(namespaces) != 1 || namespaces[0] != "default" {
		t.Fatalf("metrics loads = %v, want one load scoped to default", namespaces)
	}
}
