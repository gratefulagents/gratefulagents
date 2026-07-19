package dashboard

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
	"unsafe"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/store"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
)

// newActivityLogTestServer builds a Server backed by a fake k8s client and a
// mock Postgres store holding the given activity events for one running run.
func newActivityLogTestServer(t *testing.T, runName string, events []store.ActivityEvent) (*Server, *mockStateStore, uuid.UUID) {
	t.Helper()
	scheme := newDashboardTestScheme(t)
	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: runName, Namespace: "default"},
		Status:     platformv1alpha1.AgentRunStatus{Phase: platformv1alpha1.AgentRunPhaseRunning},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run).Build()
	ms := newMockStateStore()
	sess, err := ms.CreateSession(context.Background(), runName, "default", "running", "implement")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	setMockActivity(ms, sess.ID, events)
	return &Server{k8sClient: c, scheme: scheme, stateStore: ms}, ms, sess.ID
}

func setMockActivity(ms *mockStateStore, sessID uuid.UUID, events []store.ActivityEvent) {
	ms.mu.Lock()
	defer ms.mu.Unlock()
	if ms.allActivityBySession == nil {
		ms.allActivityBySession = map[uuid.UUID][]store.ActivityEvent{}
	}
	if ms.getRecentActivityBySession == nil {
		ms.getRecentActivityBySession = map[uuid.UUID][]store.ActivityEvent{}
	}
	ms.allActivityBySession[sessID] = events
	if len(events) > 0 {
		ms.getRecentActivityBySession[sessID] = []store.ActivityEvent{events[len(events)-1]}
	} else {
		ms.getRecentActivityBySession[sessID] = nil
	}
}

func toolResultEvent(id int64, toolUseID, inputRaw, output string) store.ActivityEvent {
	detail := fmt.Sprintf(`{"type":"tool_end","tool":"Bash","tool_use_id":%q,"input_raw":%q,"output":%q}`, toolUseID, inputRaw, output)
	return store.ActivityEvent{ID: id, EventType: "tool_end", Detail: []byte(detail)}
}

func TestGetActivityLogPayloadPreviewTruncatesCopiesOnly(t *testing.T) {
	longInput := strings.Repeat("i", 100)
	longOutput := strings.Repeat("o", 100)
	srv, _, _ := newActivityLogTestServer(t, "run-trunc", []store.ActivityEvent{
		toolResultEvent(1, "t1", longInput, longOutput),
		toolResultEvent(2, "t2", "tiny", "tiny"),
	})

	req := &platform.GetActivityLogRequest{Namespace: "default", Name: "run-trunc", PayloadPreviewBytes: 10}
	resp, err := srv.GetActivityLog(context.Background(), req)
	if err != nil {
		t.Fatalf("GetActivityLog() error = %v", err)
	}
	if len(resp.Entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(resp.Entries))
	}
	e := resp.Entries[0]
	if e.InputRaw != strings.Repeat("i", 10) || !e.InputTruncated {
		t.Fatalf("entry 0 InputRaw = %q (truncated=%t), want 10 bytes + flag", e.InputRaw, e.InputTruncated)
	}
	if e.Output != strings.Repeat("o", 10) || !e.OutputTruncated {
		t.Fatalf("entry 0 Output = %q (truncated=%t), want 10 bytes + flag", e.Output, e.OutputTruncated)
	}
	if small := resp.Entries[1]; small.InputTruncated || small.OutputTruncated || small.InputRaw != "tiny" {
		t.Fatalf("small entry must be untouched, got %+v", small)
	}

	// The memoized response must retain the full payloads.
	full, err := srv.GetActivityLog(context.Background(), &platform.GetActivityLogRequest{Namespace: "default", Name: "run-trunc"})
	if err != nil {
		t.Fatalf("GetActivityLog() full error = %v", err)
	}
	if full.Entries[0].InputRaw != longInput || full.Entries[0].Output != longOutput {
		t.Fatal("memoized entries were mutated by truncation")
	}
	if full.Entries[0].InputTruncated || full.Entries[0].OutputTruncated {
		t.Fatal("memoized entries must not carry truncation flags")
	}
}

func TestGetActivityLogPagination(t *testing.T) {
	var events []store.ActivityEvent
	for i := int64(1); i <= 5; i++ {
		events = append(events, toolResultEvent(i, fmt.Sprintf("t%d", i), "in", "out"))
	}
	srv, _, _ := newActivityLogTestServer(t, "run-page", events)
	ctx := context.Background()

	resp, err := srv.GetActivityLog(ctx, &platform.GetActivityLogRequest{Namespace: "default", Name: "run-page", Limit: 2})
	if err != nil {
		t.Fatalf("GetActivityLog(limit=2) error = %v", err)
	}
	if len(resp.Entries) != 2 || resp.Entries[0].EventId != 4 || resp.Entries[1].EventId != 5 {
		t.Fatalf("limit=2 entries = %+v, want event ids 4,5", resp.Entries)
	}
	if !resp.HasMoreBefore || resp.FirstEventId != 4 || resp.LastEventId != 5 {
		t.Fatalf("limit=2 window meta = first=%d last=%d more=%t, want 4/5/true", resp.FirstEventId, resp.LastEventId, resp.HasMoreBefore)
	}

	resp, err = srv.GetActivityLog(ctx, &platform.GetActivityLogRequest{Namespace: "default", Name: "run-page", Limit: 2, BeforeEventId: 4})
	if err != nil {
		t.Fatalf("GetActivityLog(before=4) error = %v", err)
	}
	if len(resp.Entries) != 2 || resp.Entries[0].EventId != 2 || resp.Entries[1].EventId != 3 {
		t.Fatalf("before=4 entries = %+v, want event ids 2,3", resp.Entries)
	}
	if !resp.HasMoreBefore || resp.FirstEventId != 2 || resp.LastEventId != 3 {
		t.Fatalf("before=4 window meta = first=%d last=%d more=%t, want 2/3/true", resp.FirstEventId, resp.LastEventId, resp.HasMoreBefore)
	}

	resp, err = srv.GetActivityLog(ctx, &platform.GetActivityLogRequest{Namespace: "default", Name: "run-page", Limit: 5, BeforeEventId: 3})
	if err != nil {
		t.Fatalf("GetActivityLog(before=3) error = %v", err)
	}
	if len(resp.Entries) != 2 || resp.Entries[0].EventId != 1 || resp.Entries[1].EventId != 2 {
		t.Fatalf("before=3 entries = %+v, want event ids 1,2", resp.Entries)
	}
	if resp.HasMoreBefore {
		t.Fatal("before=3 limit=5 must not report more entries before the window")
	}

	resp, err = srv.GetActivityLog(ctx, &platform.GetActivityLogRequest{Namespace: "default", Name: "run-page", SinceEventId: 3})
	if err != nil {
		t.Fatalf("GetActivityLog(since=3) error = %v", err)
	}
	if len(resp.Entries) != 2 || resp.Entries[0].EventId != 4 || resp.Entries[1].EventId != 5 {
		t.Fatalf("since=3 entries = %+v, want event ids 4,5", resp.Entries)
	}
}

func TestGetActivityLogLegacyRequestUnchanged(t *testing.T) {
	srv, _, _ := newActivityLogTestServer(t, "run-legacy", []store.ActivityEvent{
		toolResultEvent(1, "t1", strings.Repeat("i", 100), strings.Repeat("o", 100)),
		toolResultEvent(2, "t2", "in", "out"),
	})
	resp, err := srv.GetActivityLog(context.Background(), &platform.GetActivityLogRequest{Namespace: "default", Name: "run-legacy"})
	if err != nil {
		t.Fatalf("GetActivityLog() error = %v", err)
	}
	if len(resp.Entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(resp.Entries))
	}
	for i, e := range resp.Entries {
		if e.InputTruncated || e.OutputTruncated {
			t.Fatalf("entry %d has truncation flags on legacy request", i)
		}
	}
	if resp.Entries[0].InputRaw != strings.Repeat("i", 100) {
		t.Fatal("legacy request must return full payloads")
	}
	if resp.Delta || resp.Reset_ || resp.HasMoreBefore || resp.FirstEventId != 0 || resp.LastEventId != 0 {
		t.Fatalf("legacy response must not set new fields, got %+v", resp)
	}
}

func TestGetActivityEntryDetail(t *testing.T) {
	longInput := strings.Repeat("i", 100)
	longOutput := strings.Repeat("o", 100)
	srv, _, _ := newActivityLogTestServer(t, "run-detail", []store.ActivityEvent{
		toolResultEvent(1, "t1", "in1", "out1"),
		toolResultEvent(2, "t2", longInput, longOutput),
	})
	ctx := context.Background()

	got, err := srv.GetActivityEntryDetail(ctx, &platform.GetActivityEntryDetailRequest{Namespace: "default", Name: "run-detail", EventId: 2})
	if err != nil {
		t.Fatalf("GetActivityEntryDetail(event_id=2) error = %v", err)
	}
	if got.InputRaw != longInput || got.Output != longOutput {
		t.Fatalf("detail = %q/%q, want full payloads", got.InputRaw, got.Output)
	}

	got, err = srv.GetActivityEntryDetail(ctx, &platform.GetActivityEntryDetailRequest{Namespace: "default", Name: "run-detail", ToolUseId: "t1"})
	if err != nil {
		t.Fatalf("GetActivityEntryDetail(tool_use_id=t1) error = %v", err)
	}
	if got.InputRaw != "in1" || got.Output != "out1" {
		t.Fatalf("detail by tool_use_id = %q/%q, want in1/out1", got.InputRaw, got.Output)
	}

	_, err = srv.GetActivityEntryDetail(ctx, &platform.GetActivityEntryDetailRequest{Namespace: "default", Name: "run-detail", EventId: 99})
	if connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("GetActivityEntryDetail(event_id=99) = %v, want NotFound", err)
	}
}

type recordingActivityLogConn struct {
	ch chan *platform.GetActivityLogResponse
}

func (c *recordingActivityLogConn) Spec() connect.Spec           { return connect.Spec{} }
func (c *recordingActivityLogConn) Peer() connect.Peer           { return connect.Peer{} }
func (c *recordingActivityLogConn) Receive(any) error            { return errors.New("not implemented") }
func (c *recordingActivityLogConn) RequestHeader() http.Header   { return http.Header{} }
func (c *recordingActivityLogConn) ResponseHeader() http.Header  { return http.Header{} }
func (c *recordingActivityLogConn) ResponseTrailer() http.Header { return http.Header{} }
func (c *recordingActivityLogConn) Send(msg any) error {
	pb, ok := msg.(*platform.GetActivityLogResponse)
	if !ok {
		return errors.New("unexpected message type")
	}
	c.ch <- proto.Clone(pb).(*platform.GetActivityLogResponse)
	return nil
}

func newActivityLogServerStream(conn connect.StreamingHandlerConn) *connect.ServerStream[platform.GetActivityLogResponse] {
	stream := &connect.ServerStream[platform.GetActivityLogResponse]{}
	streamPtr := (*struct{ Conn connect.StreamingHandlerConn })(unsafe.Pointer(stream))
	streamPtr.Conn = conn
	return stream
}

func TestWatchActivityLogDeltaSendsResetThenAppendOnly(t *testing.T) {
	srv, ms, sessID := newActivityLogTestServer(t, "run-delta", []store.ActivityEvent{
		toolResultEvent(1, "t1", "in1", "out1"),
		toolResultEvent(2, "t2", "in2", "out2"),
	})

	conn := &recordingActivityLogConn{ch: make(chan *platform.GetActivityLogResponse, 8)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.WatchActivityLog(ctx, &platform.GetActivityLogRequest{
			Namespace: "default", Name: "run-delta", Delta: true,
		}, newActivityLogServerStream(conn))
	}()

	var first *platform.GetActivityLogResponse
	select {
	case first = <-conn.ch:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for initial frame")
	}
	if !first.Reset_ || !first.Delta {
		t.Fatalf("initial frame reset=%t delta=%t, want true/true", first.Reset_, first.Delta)
	}
	if len(first.Entries) != 2 || first.LastEventId != 2 {
		t.Fatalf("initial frame entries=%d last=%d, want 2/2", len(first.Entries), first.LastEventId)
	}
	if first.SubagentGraph == nil {
		t.Fatal("initial frame must carry the subagent graph")
	}

	events := append([]store.ActivityEvent{
		toolResultEvent(1, "t1", "in1", "out1"),
		toolResultEvent(2, "t2", "in2", "out2"),
	}, toolResultEvent(3, "t3", "in3", "out3"))
	setMockActivity(ms, sessID, events)

	var second *platform.GetActivityLogResponse
	select {
	case second = <-conn.ch:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for delta frame")
	}
	if second.Reset_ {
		t.Fatal("delta frame must not set reset")
	}
	if !second.Delta {
		t.Fatal("delta frame must set delta")
	}
	if len(second.Entries) != 1 || second.Entries[0].EventId != 3 {
		t.Fatalf("delta frame entries = %+v, want only event 3", second.Entries)
	}
	if second.LastEventId != 3 {
		t.Fatalf("delta frame last_event_id = %d, want 3", second.LastEventId)
	}
	if second.SubagentGraph != nil {
		t.Fatal("unchanged subagent graph must be omitted from delta frames")
	}

	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("WatchActivityLog returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for watch to stop")
	}
}

// TestApplyActivityLogOptionsRemapsSubagentGraph verifies that slicing a
// response (limit/cursor pagination) remaps the graph's positional
// detail_entry_indices onto the sliced entries via the durable event IDs,
// drops out-of-page indices, and never mutates the memoized graph.
func TestApplyActivityLogOptionsRemapsSubagentGraph(t *testing.T) {
	entries := []*platform.ActivityEntry{
		{EventId: 1, Type: "text"},
		{EventId: 2, Type: "subagent_progress", TaskId: "t1"},
		{EventId: 3, Type: "text"},
		{EventId: 4, Type: "subagent_progress", TaskId: "t1"},
	}
	node := &platform.SubagentGraphNode{
		Id:                  "t1",
		Status:              "running",
		DetailEntryIndices:  []int32{1, 3},
		DetailEntryEventIds: []int64{2, 4},
	}
	resp := &platform.GetActivityLogResponse{
		Entries:       entries,
		SubagentGraph: &platform.SubagentGraph{RootId: "root", Nodes: []*platform.SubagentGraphNode{node}},
	}

	out := applyActivityLogRequestOptions(resp, &platform.GetActivityLogRequest{Limit: 2})

	if len(out.Entries) != 2 || out.Entries[0].EventId != 3 || out.Entries[1].EventId != 4 {
		t.Fatalf("limit slice = %+v, want events 3,4", out.Entries)
	}
	got := out.SubagentGraph.Nodes[0]
	if len(got.DetailEntryIndices) != 1 || got.DetailEntryIndices[0] != 1 {
		t.Fatalf("remapped indices = %v, want [1] (event 4 at sliced position 1)", got.DetailEntryIndices)
	}
	if len(got.DetailEntryEventIds) != 2 || got.DetailEntryEventIds[0] != 2 || got.DetailEntryEventIds[1] != 4 {
		t.Fatalf("event ids must pass through unchanged, got %v", got.DetailEntryEventIds)
	}
	// The original (memoized) graph must not have been mutated.
	if len(node.DetailEntryIndices) != 2 || node.DetailEntryIndices[0] != 1 || node.DetailEntryIndices[1] != 3 {
		t.Fatalf("original graph node mutated: %v", node.DetailEntryIndices)
	}
	// An unsliced request keeps the original graph pointer (no clone cost).
	same := applyActivityLogRequestOptions(resp, &platform.GetActivityLogRequest{PayloadPreviewBytes: 1 << 20})
	if same.SubagentGraph != resp.SubagentGraph {
		t.Fatal("unsliced response must reuse the original graph")
	}
}

// TestSubagentGraphFingerprintTracksProgressFields verifies the delta-frame
// suppression fingerprint reacts to node progress and edge changes, not just
// node IDs/statuses.
func TestSubagentGraphFingerprintTracksProgressFields(t *testing.T) {
	mk := func() *platform.SubagentGraph {
		return &platform.SubagentGraph{
			RootId: "root",
			Nodes: []*platform.SubagentGraphNode{{
				Id:          "t1",
				Status:      "running",
				CurrentStep: "exploring",
				LastTool:    "Bash",
				WaitingOn:   []string{"t0"},
			}},
			Edges: []*platform.SubagentGraphEdge{{Id: "e1", From: "root", To: "t1", Kind: "spawned"}},
		}
	}

	base := subagentGraphFingerprint(mk())
	if got := subagentGraphFingerprint(mk()); got != base {
		t.Fatalf("identical graphs must fingerprint equally: %q vs %q", got, base)
	}

	step := mk()
	step.Nodes[0].CurrentStep = "implementing"
	tool := mk()
	tool.Nodes[0].LastTool = "Edit"
	waiting := mk()
	waiting.Nodes[0].WaitingOn = nil
	files := mk()
	files.Nodes[0].FilesWritten = 3
	edge := mk()
	edge.Edges[0].To = "t2"

	for name, g := range map[string]*platform.SubagentGraph{
		"current_step": step, "last_tool": tool, "waiting_on": waiting, "files_written": files, "edge_endpoint": edge,
	} {
		if subagentGraphFingerprint(g) == base {
			t.Fatalf("fingerprint must change when %s changes", name)
		}
	}

	// Root entry_count grows with every transcript entry; it alone must NOT
	// force a graph re-send.
	rootGrowth := mk()
	rootGrowth.Nodes = append([]*platform.SubagentGraphNode{{Id: "root", Kind: "root", Status: "running", EntryCount: 7}}, rootGrowth.Nodes...)
	rootBase := subagentGraphFingerprint(rootGrowth)
	moreEntries := mk()
	moreEntries.Nodes = append([]*platform.SubagentGraphNode{{Id: "root", Kind: "root", Status: "running", EntryCount: 8}}, moreEntries.Nodes...)
	if subagentGraphFingerprint(moreEntries) != rootBase {
		t.Fatal("root entry_count growth alone must not change the fingerprint")
	}
}
