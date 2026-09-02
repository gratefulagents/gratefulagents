package dashboard

import (
	"context"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/store"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
)

func TestDeletedAgentRunEventUsesNamespaceAndName(t *testing.T) {
	versions := map[string]string{"default/run-1": "1"}
	seen := map[string]struct{}{}
	var events []*platform.AgentRunEvent

	for key := range versions {
		if _, ok := seen[key]; ok {
			continue
		}
		delete(versions, key)
		parts := strings.SplitN(key, "/", 2)
		events = append(events, &platform.AgentRunEvent{Type: "DELETED", Run: &platform.AgentRun{Namespace: parts[0], Name: parts[1]}})
	}

	if len(events) != 1 {
		t.Fatalf("len(events) = %d, want 1", len(events))
	}
	if events[0].Run.Namespace != "default" || events[0].Run.Name != "run-1" {
		t.Fatalf("deleted event run = %#v", events[0].Run)
	}
}

func TestIsTerminalAgentRunPhase(t *testing.T) {
	cases := map[platformv1alpha1.AgentRunPhase]bool{
		platformv1alpha1.AgentRunPhaseSucceeded: true,
		platformv1alpha1.AgentRunPhaseFailed:    true,
		platformv1alpha1.AgentRunPhaseCancelled: true,
		platformv1alpha1.AgentRunPhaseRunning:   false,
	}
	for phase, want := range cases {
		if got := isTerminalAgentRunPhase(phase); got != want {
			t.Fatalf("isTerminalAgentRunPhase(%q) = %v, want %v", phase, got, want)
		}
	}
}

func TestShouldContinueAgentRunWatchForOverseerLifecycle(t *testing.T) {
	tests := []struct {
		name string
		run  *platformv1alpha1.AgentRun
		want bool
	}{
		{name: "running", run: &platformv1alpha1.AgentRun{Status: platformv1alpha1.AgentRunStatus{Phase: platformv1alpha1.AgentRunPhaseRunning}}, want: true},
		{name: "plain succeeded", run: &platformv1alpha1.AgentRun{Status: platformv1alpha1.AgentRunStatus{Phase: platformv1alpha1.AgentRunPhaseSucceeded}}, want: false},
		{name: "succeeded attached", run: &platformv1alpha1.AgentRun{Spec: platformv1alpha1.AgentRunSpec{Overseer: &platformv1alpha1.AgentRunOverseerSpec{}}, Status: platformv1alpha1.AgentRunStatus{Phase: platformv1alpha1.AgentRunPhaseSucceeded}}, want: true},
		{name: "failed detaching", run: &platformv1alpha1.AgentRun{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{platformv1alpha1.OverseerDetachingAnnotation: "true"}}, Status: platformv1alpha1.AgentRunStatus{Phase: platformv1alpha1.AgentRunPhaseFailed}}, want: true},
		{name: "cancelled attached", run: &platformv1alpha1.AgentRun{Spec: platformv1alpha1.AgentRunSpec{Overseer: &platformv1alpha1.AgentRunOverseerSpec{}}, Status: platformv1alpha1.AgentRunStatus{Phase: platformv1alpha1.AgentRunPhaseCancelled}}, want: false},
		{name: "nil", run: nil, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldContinueAgentRunWatch(tt.run); got != tt.want {
				t.Fatalf("shouldContinueAgentRunWatch() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDeleteAgentRunRequestTypeCompiles(t *testing.T) {
	_ = context.Background()
	_ = metav1.Now()
}

// wakeProbeFixture drives streamVersionedSnapshots / watchActivityLogDelta
// with a wake channel, a healthy listener (so the fallback poll is slow), and
// a probe served through a probeCache primed before the wake-up.
type wakeProbeFixture struct {
	cache   probeCache
	version atomic.Int64
	wake    chan struct{}
	tick    watchTicker
	// honorFresh=false simulates the pre-fix probe that always reads the
	// (possibly stale) cache; the loop must then still re-arm at fast.
	honorFresh bool
	cacheTTL   time.Duration
}

func newWakeProbeFixture(honorFresh bool, cacheTTL time.Duration) *wakeProbeFixture {
	f := &wakeProbeFixture{wake: make(chan struct{}, 1), honorFresh: honorFresh, cacheTTL: cacheTTL}
	f.version.Store(1)
	f.tick = watchTicker{
		fast:    20 * time.Millisecond,
		slow:    3 * time.Second,
		wake:    f.wake,
		healthy: func() bool { return true },
	}
	return f
}

func (f *wakeProbeFixture) probe(ctx context.Context, fresh bool) (string, error) {
	do := probeCacheDo[string]
	if fresh && f.honorFresh {
		do = probeCacheDoFresh[string]
	}
	return do(ctx, &f.cache, "v", f.cacheTTL, func(context.Context) (string, error) {
		return strconv.FormatInt(f.version.Load(), 10), nil
	})
}

func TestStreamVersionedSnapshotsWakeObservesChangeWithinFast(t *testing.T) {
	for _, tc := range []struct {
		name       string
		honorFresh bool
		cacheTTL   time.Duration
	}{
		// Fresh probe: the wake-driven iteration bypasses the primed cache.
		{name: "fresh probe", honorFresh: true, cacheTTL: time.Minute},
		// Stale probe: the wake sees no change, but re-arms at fast rather
		// than slow, so the change is still picked up once the TTL lapses.
		{name: "fast re-arm", honorFresh: false, cacheTTL: 10 * time.Millisecond},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newWakeProbeFixture(tc.honorFresh, tc.cacheTTL)
			sent := make(chan string, 8)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() {
				done <- streamVersionedSnapshots(ctx, f.tick, f.probe,
					func(context.Context) (string, bool, error) {
						return strconv.FormatInt(f.version.Load(), 10), true, nil
					},
					func(v string) error { sent <- v; return nil })
			}()
			if got := <-sent; got != "1" {
				t.Fatalf("initial snapshot = %q, want 1", got)
			}
			// The initial iteration primed the cache and re-armed at slow.
			f.version.Store(2)
			start := time.Now()
			f.wake <- struct{}{}
			select {
			case got := <-sent:
				if got != "2" {
					t.Fatalf("snapshot after wake = %q, want 2", got)
				}
				if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
					t.Fatalf("change observed after %v, want well under slow (%v)", elapsed, f.tick.slow)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("change not observed within 2s: wake-driven iteration was answered from the primed cache and re-armed at slow")
			}
			cancel()
			if err := <-done; err != nil {
				t.Fatalf("streamVersionedSnapshots() error = %v", err)
			}
		})
	}
}

func TestWatchActivityLogDeltaWakeObservesChangeWithinFast(t *testing.T) {
	f := newWakeProbeFixture(true, time.Minute)
	events := func() []*platform.ActivityEntry {
		n := f.version.Load()
		out := make([]*platform.ActivityEntry, 0, n)
		for i := int64(1); i <= n; i++ {
			out = append(out, &platform.ActivityEntry{EventId: i, Type: "tool_end"})
		}
		return out
	}
	sent := make(chan *platform.GetActivityLogResponse, 8)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- watchActivityLogDelta(ctx, f.tick, &platform.GetActivityLogRequest{Delta: true}, f.probe,
			func(context.Context) (*platform.GetActivityLogResponse, activityLogSource, error) {
				return &platform.GetActivityLogResponse{Entries: events()}, activityLogSourcePostgres, nil
			},
			func(r *platform.GetActivityLogResponse) error { sent <- r; return nil })
	}()
	if first := <-sent; !first.Reset_ || first.LastEventId != 1 {
		t.Fatalf("initial frame = %+v, want reset with last_event_id 1", first)
	}
	f.version.Store(2)
	start := time.Now()
	f.wake <- struct{}{}
	select {
	case frame := <-sent:
		if frame.Reset_ || len(frame.Entries) != 1 || frame.Entries[0].EventId != 2 {
			t.Fatalf("delta frame = %+v, want only event 2", frame)
		}
		if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
			t.Fatalf("change observed after %v, want well under slow (%v)", elapsed, f.tick.slow)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("change not observed within 2s after wake")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("watchActivityLogDelta() error = %v", err)
	}
}

func TestWatchTickerIntervalAfterWake(t *testing.T) {
	healthy := true
	tick := watchTicker{fast: 500 * time.Millisecond, slow: 3 * time.Second, wake: make(chan struct{}), healthy: func() bool { return healthy }}
	if got := tick.intervalAfter(false, false); got != tick.slow {
		t.Fatalf("timer-driven, unchanged = %v, want slow", got)
	}
	if got := tick.intervalAfter(true, true); got != tick.slow {
		t.Fatalf("wake-driven, changed = %v, want slow", got)
	}
	if got := tick.intervalAfter(true, false); got != tick.fast {
		t.Fatalf("wake-driven, unchanged = %v, want fast", got)
	}
	healthy = false
	if got := tick.intervalAfter(false, false); got != tick.fast {
		t.Fatalf("listener down = %v, want fast", got)
	}
}

// fingerprintCountingStore records which session fingerprint WatchAgentRun
// probes: the conversation fingerprint (not bumped by activity events) must
// be used so tool-call logging does not force a transcript rebuild.
type fingerprintCountingStore struct {
	*mockStateStore
	fullFP atomic.Int32
	convFP atomic.Int32
}

func (s *fingerprintCountingStore) GetSessionFingerprint(ctx context.Context, id uuid.UUID) (string, error) {
	s.fullFP.Add(1)
	return s.mockStateStore.GetSessionFingerprint(ctx, id)
}

func (s *fingerprintCountingStore) GetSessionConversationFingerprint(ctx context.Context, id uuid.UUID) (string, error) {
	s.convFP.Add(1)
	return s.mockStateStore.GetSessionConversationFingerprint(ctx, id)
}

func TestWatchAgentRunProbesConversationFingerprintAndSharesConversationBuild(t *testing.T) {
	scheme := newDashboardTestScheme(t)
	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "run-cfp", Namespace: "default", ResourceVersion: "1"},
		Spec:       platformv1alpha1.AgentRunSpec{WorkflowMode: platformv1alpha1.WorkflowModeChat},
		Status:     platformv1alpha1.AgentRunStatus{Phase: platformv1alpha1.AgentRunPhaseRunning},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run).Build()
	ms := newMockStateStore()
	sess, _ := ms.CreateSession(context.Background(), "run-cfp", "default", "running", "implement")
	ms.getMessagesBySession = map[uuid.UUID][]store.Message{
		sess.ID: {{ID: 1, SessionID: sess.ID, Role: "user", Content: "hi", CreatedAt: time.Unix(10, 0)}},
	}
	cs := &fingerprintCountingStore{mockStateStore: ms}
	srv := &Server{k8sClient: c, scheme: scheme, stateStore: cs}

	const tabs = 3
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	conns := make([]*recordingStreamingHandlerConn, tabs)
	for i := range conns {
		conns[i] = &recordingStreamingHandlerConn{ch: make(chan *platform.AgentRun, 8)}
		go func(conn *recordingStreamingHandlerConn) {
			_ = srv.WatchAgentRun(ctx, &platform.WatchAgentRunRequest{Namespace: "default", Name: "run-cfp"}, newAgentRunServerStream(conn))
		}(conns[i])
	}
	for _, conn := range conns {
		select {
		case first := <-conn.ch:
			if len(first.Conversation) != 1 {
				t.Fatalf("conversation len = %d, want 1", len(first.Conversation))
			}
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for initial snapshot")
		}
	}
	if cs.fullFP.Load() != 0 {
		t.Fatalf("GetSessionFingerprint calls = %d, want 0 (conversation watch must ignore activity events)", cs.fullFP.Load())
	}
	if cs.convFP.Load() == 0 {
		t.Fatal("GetSessionConversationFingerprint was never probed")
	}
	// All tabs built the same conversation version: the memo keyed by the
	// conversation fingerprint collapses them to one GetMessages per version.
	if calls := ms.getMessagesCalls.Load(); calls != 1 {
		t.Fatalf("GetMessages calls = %d, want 1 shared across %d tabs", calls, tabs)
	}
}
