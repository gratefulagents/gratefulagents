package computeruse

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func shortTimeouts(t *testing.T, lease, poll, pickup, delivered time.Duration) {
	t.Helper()
	old := []time.Duration{LeaseDuration, PollWait, PickupTimeout, DeliveredTimeout, expiryTick}
	LeaseDuration, PollWait, PickupTimeout, DeliveredTimeout, expiryTick = lease, poll, pickup, delivered, 5*time.Millisecond
	t.Cleanup(func() {
		LeaseDuration, PollWait, PickupTimeout, DeliveredTimeout, expiryTick = old[0], old[1], old[2], old[3], old[4]
	})
}

func newTestBroker(t *testing.T) *Broker {
	t.Helper()
	b := New("ns", "run")
	t.Cleanup(func() { b.Close() })
	return b
}

func desk(owner, session string) Exchange {
	return Exchange{Namespace: "ns", Run: "run", Owner: owner, SessionID: session}
}

func op(t *testing.T, b *Broker, e Exchange, operation string) Response {
	t.Helper()
	e.Operation = operation
	r, err := b.Exchange(context.Background(), e)
	if err != nil {
		t.Fatalf("%s: %v", operation, err)
	}
	if err := r.Validate(); err != nil {
		t.Fatalf("%s produced invalid response %+v: %v", operation, r, err)
	}
	return r
}

func send(t *testing.T, b *Broker, e Exchange, res Result) Response {
	t.Helper()
	e.Operation, e.RequestID, e.Result = "result", res.RequestID, &res
	r, err := b.Exchange(context.Background(), e)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

type requestOutcome struct {
	res Result
	err error
}

func request(b *Broker, a Action) chan requestOutcome {
	ch := make(chan requestOutcome, 1)
	go func() {
		res, err := b.Request(context.Background(), a)
		ch <- requestOutcome{res, err}
	}()
	return ch
}

func await(t *testing.T, ch chan requestOutcome) requestOutcome {
	t.Helper()
	select {
	case o := <-ch:
		return o
	case <-time.After(5 * time.Second):
		t.Fatal("request did not finish")
	}
	return requestOutcome{}
}

func TestBrokerRoundTripAndAvailability(t *testing.T) {
	b := newTestBroker(t)
	alice := desk("alice", "s1")
	if r := op(t, b, alice, "next"); r.Active || r.Reason != ReasonNoSession {
		t.Fatalf("next without session: %+v", r)
	}
	if _, err := b.Request(context.Background(), Action{Action: "screenshot"}); !errors.Is(err, ErrNoDesktop) {
		t.Fatalf("request without desktop: %v", err)
	}
	r := op(t, b, alice, "connect")
	if !r.Active || r.Available || r.Protocol != 2 {
		t.Fatalf("connect: %+v", r)
	}
	b.SetAvailable(func() bool { return true })
	if !b.Active() || !op(t, b, alice, "connect").Available {
		t.Fatal("availability not reported")
	}
	ch := request(b, Action{Action: "screenshot"})
	r = op(t, b, alice, "next")
	if r.Request == nil || r.Request.Action.Action != "screenshot" {
		t.Fatalf("next: %+v", r)
	}
	if again := op(t, b, alice, "connect"); again.Request != nil {
		t.Fatal("connect redelivered request")
	}
	if r := send(t, b, alice, Result{RequestID: "other", OK: true, Screenshot: shot(t, 100, 50)}); r.Reason != ReasonStaleResult || !r.Active {
		t.Fatalf("stale result: %+v", r)
	}
	if r := send(t, b, alice, Result{RequestID: r.Request.ID, OK: true, Screenshot: shot(t, 100, 50)}); !r.Active || r.Reason != "" {
		t.Fatalf("result: %+v", r)
	}
	o := await(t, ch)
	if o.err != nil || !o.res.OK || o.res.Screenshot.Width != 100 {
		t.Fatalf("outcome: %+v", o)
	}
	// The latest screenshot bounds later coordinates; zoom crops do not.
	if _, err := b.Request(context.Background(), Action{Action: "left_click", Coordinate: pt(100, 10)}); !errors.Is(err, ErrRejected) {
		t.Fatalf("out-of-bounds click: %v", err)
	}
	if _, err := b.Request(context.Background(), Action{Action: "wait"}); !errors.Is(err, ErrRejected) {
		t.Fatal("wait reached the desktop")
	}
	ch = request(b, Action{Action: "zoom", Region: region(0, 0, 50, 25)})
	r = op(t, b, alice, "next")
	send(t, b, alice, Result{RequestID: r.Request.ID, OK: true, Screenshot: shot(t, 400, 200)})
	await(t, ch)
	if _, err := b.Request(context.Background(), Action{Action: "left_click", Coordinate: pt(150, 10)}); !errors.Is(err, ErrRejected) {
		t.Fatal("zoom result changed the screen size")
	}
}

func TestBrokerSessionTakeover(t *testing.T) {
	b := newTestBroker(t)
	old, fresh := desk("alice", "s1"), desk("alice", "s2")
	op(t, b, old, "connect")
	ch := request(b, Action{Action: "screenshot"})
	op(t, b, old, "next")
	if r := op(t, b, fresh, "connect"); !r.Active {
		t.Fatalf("takeover refused: %+v", r)
	}
	o := await(t, ch)
	if !errors.Is(o.err, ErrDisconnected) || !strings.Contains(o.err.Error(), "desktop reconnected") {
		t.Fatalf("in-flight request: %v", o.err)
	}
	if r := op(t, b, old, "next"); r.Active {
		t.Fatalf("replaced session still active: %+v", r)
	}
	if r := op(t, b, old, "disconnect"); r.Active || !b.Active() {
		t.Fatal("replaced session disconnected the new one")
	}
}

func TestBrokerForeignOwnerRefused(t *testing.T) {
	b := newTestBroker(t)
	op(t, b, desk("alice", "s1"), "connect")
	for _, operation := range []string{"connect", "next", "disconnect"} {
		r := op(t, b, desk("mallory", "s1"), operation)
		if r.Active || r.Request != nil {
			t.Fatalf("%s by foreign owner: %+v", operation, r)
		}
		if operation == "connect" && r.Reason != ReasonForeignOwner {
			t.Fatalf("reason %q", r.Reason)
		}
	}
	if r := send(t, b, desk("mallory", "s1"), Result{RequestID: "r1", Error: "x"}); r.Active {
		t.Fatal("foreign result accepted")
	}
	if !b.Active() {
		t.Fatal("foreign owner ended session")
	}
	if _, err := b.Exchange(context.Background(), Exchange{Namespace: "ns", Run: "other", Owner: "alice", SessionID: "s1", Operation: "connect"}); err == nil {
		t.Fatal("accepted exchange for another run")
	}
}

func TestBrokerLongPollWake(t *testing.T) {
	shortTimeouts(t, time.Second, 3*time.Second, time.Second, time.Second)
	b := newTestBroker(t)
	alice := desk("alice", "s1")
	op(t, b, alice, "connect")
	got := make(chan Response, 1)
	go func() {
		e := alice
		e.Operation = "next"
		r, _ := b.Exchange(context.Background(), e)
		got <- r
	}()
	time.Sleep(50 * time.Millisecond)
	start := time.Now()
	request(b, Action{Action: "screenshot"})
	select {
	case r := <-got:
		if r.Request == nil {
			t.Fatalf("woke without request: %+v", r)
		}
		if d := time.Since(start); d > 200*time.Millisecond {
			t.Fatalf("wake latency %v", d)
		}
		send(t, b, alice, Result{RequestID: r.Request.ID, OK: true, Screenshot: shot(t, 100, 50)})
	case <-time.After(2 * time.Second):
		t.Fatal("long-poll not woken")
	}
	// An idle poll returns active with nothing after PollWait.
	PollWait = 30 * time.Millisecond
	if r := op(t, b, desk("alice", "s1"), "next"); !r.Active || r.Request != nil {
		t.Fatalf("idle poll: %+v", r)
	}
	// Ending the session during a poll returns inactive.
	PollWait = 3 * time.Second
	go func() {
		e := alice
		e.Operation = "next"
		r, _ := b.Exchange(context.Background(), e)
		got <- r
	}()
	time.Sleep(50 * time.Millisecond)
	op(t, b, alice, "disconnect")
	select {
	case r := <-got:
		if r.Active {
			t.Fatalf("poll after disconnect: %+v", r)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("poll not ended by disconnect")
	}
}

func TestBrokerLeaseExpiry(t *testing.T) {
	shortTimeouts(t, 100*time.Millisecond, 50*time.Millisecond, 5*time.Second, 5*time.Second)
	b := newTestBroker(t)
	alice := desk("alice", "s1")
	op(t, b, alice, "connect")
	// A delivered, unresolved request keeps the session alive past the lease.
	ch := request(b, Action{Action: "screenshot"})
	r := op(t, b, alice, "next")
	time.Sleep(300 * time.Millisecond)
	if !b.Active() {
		t.Fatal("lease enforced while a request was delivered")
	}
	send(t, b, alice, Result{RequestID: r.Request.ID, Error: "failed"})
	if o := await(t, ch); o.err != nil || o.res.OK {
		t.Fatalf("outcome %+v", o)
	}
	time.Sleep(300 * time.Millisecond)
	if b.Active() {
		t.Fatal("lease not enforced")
	}
	if r := op(t, b, alice, "next"); r.Active {
		t.Fatal("expired session still polled")
	}
	// An undelivered request fails when the lease lapses.
	op(t, b, alice, "connect")
	o := await(t, request(b, Action{Action: "screenshot"}))
	if !errors.Is(o.err, ErrDisconnected) {
		t.Fatalf("pending on expiry: %v", o.err)
	}
}

func TestBrokerDeadlines(t *testing.T) {
	shortTimeouts(t, 5*time.Second, 50*time.Millisecond, 100*time.Millisecond, 150*time.Millisecond)
	b := newTestBroker(t)
	alice := desk("alice", "s1")
	op(t, b, alice, "connect")
	o := await(t, request(b, Action{Action: "screenshot"}))
	if !errors.Is(o.err, ErrTimeout) || !strings.Contains(o.err.Error(), "desktop did not pick up the request") {
		t.Fatalf("undelivered: %v", o.err)
	}
	ch := request(b, Action{Action: "screenshot"})
	r := op(t, b, alice, "next")
	if r.Request == nil {
		t.Fatal("no request")
	}
	start := time.Now()
	o = await(t, ch)
	if !errors.Is(o.err, ErrTimeout) || time.Since(start) < 100*time.Millisecond {
		t.Fatalf("delivered: %v after %v", o.err, time.Since(start))
	}
	if r := send(t, b, alice, Result{RequestID: r.Request.ID, Error: "late"}); r.Reason != ReasonStaleResult {
		t.Fatalf("late result: %+v", r)
	}
	if !b.Active() {
		t.Fatal("session dropped by request deadline")
	}
}

func TestBrokerBusyAndCancel(t *testing.T) {
	b := newTestBroker(t)
	alice := desk("alice", "s1")
	op(t, b, alice, "connect")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := b.Request(ctx, Action{Action: "screenshot"}); done <- err }()
	deadline := time.Now().Add(time.Second)
	for {
		b.mu.Lock()
		p := b.pending
		b.mu.Unlock()
		if p != nil || time.Now().After(deadline) {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if _, err := b.Request(context.Background(), Action{Action: "screenshot"}); !errors.Is(err, ErrBusy) {
		t.Fatalf("concurrent request: %v", err)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled request: %v", err)
	}
	ch := request(b, Action{Action: "cursor_position"})
	r := op(t, b, alice, "next")
	if r.Request == nil || r.Request.Action.Action != "cursor_position" {
		t.Fatalf("canceled request still pending: %+v", r)
	}
	send(t, b, alice, Result{RequestID: r.Request.ID, OK: true, Screenshot: shot(t, 10, 10), Cursor: &Point{X: 1, Y: 2}})
	if o := await(t, ch); o.res.Cursor == nil || o.res.Cursor.Y != 2 {
		t.Fatalf("cursor: %+v", o)
	}
}

func TestBrokerDisconnectFailsPending(t *testing.T) {
	b := newTestBroker(t)
	alice := desk("alice", "s1")
	op(t, b, alice, "connect")
	ch := request(b, Action{Action: "screenshot"})
	op(t, b, alice, "next")
	if r := op(t, b, alice, "disconnect"); r.Active || r.Reason != ReasonDisconnected {
		t.Fatalf("disconnect: %+v", r)
	}
	if o := await(t, ch); !errors.Is(o.err, ErrDisconnected) || o.err.Error() != "desktop disconnected" {
		t.Fatalf("pending: %v", o.err)
	}
	if b.Active() {
		t.Fatal("still active")
	}
	op(t, b, alice, "connect")
	ch = request(b, Action{Action: "screenshot"})
	time.Sleep(20 * time.Millisecond)
	b.Close()
	if o := await(t, ch); !errors.Is(o.err, ErrDisconnected) {
		t.Fatalf("close: %v", o.err)
	}
	if r := op(t, b, alice, "connect"); r.Active || r.Reason != ReasonClosed {
		t.Fatalf("connect after close: %+v", r)
	}
}

func TestBrokerRedeliversUnresolvedRequestOnNextPoll(t *testing.T) {
	b := newTestBroker(t)
	alice := desk("alice", "s1")
	op(t, b, alice, "connect")
	ch := request(b, Action{Action: "screenshot"})
	first := op(t, b, alice, "next")
	if first.Request == nil {
		t.Fatalf("first next: %+v", first)
	}
	// The first response was lost in transit; the idle desktop polls again.
	second := op(t, b, alice, "next")
	if second.Request == nil || second.Request.ID != first.Request.ID {
		t.Fatalf("not redelivered: %+v", second)
	}
	send(t, b, alice, Result{RequestID: second.Request.ID, OK: true, Screenshot: shot(t, 100, 50)})
	if o := await(t, ch); o.err != nil || !o.res.OK {
		t.Fatalf("outcome: %+v", o)
	}
}
