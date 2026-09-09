package computeruse

import (
	"bytes"
	"context"
	"encoding/base64"
	"image"
	"image/png"
	"strings"
	"testing"
	"time"
)

func attached(t *testing.T) (*Broker, Exchange) {
	t.Helper()
	b := New("ns", "run")
	t.Cleanup(func() { b.Close() })
	e := Exchange{Namespace: "ns", Run: "run", Owner: "alice", SessionID: "session", Operation: "attach"}
	if r, err := b.Exchange(e); err != nil || !r.Active {
		t.Fatalf("attach: %+v %v", r, err)
	}
	return b, e
}

func queued(t *testing.T, b *Broker, ctx context.Context) (Request, <-chan delivery) {
	t.Helper()
	done := make(chan delivery, 1)
	go func() {
		o, err := b.Request(ctx, Action{Kind: "type", Text: "proposed text"}, "frame")
		done <- delivery{o, err}
	}()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		b.mu.Lock()
		p := b.pending
		b.mu.Unlock()
		if p != nil {
			return p.request, done
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("request did not queue")
	return Request{}, nil
}

func TestIdentityAndLease(t *testing.T) {
	b, e := attached(t)
	for _, field := range []string{"owner", "session", "namespace", "run"} {
		bad := e
		bad.Operation = "poll"
		switch field {
		case "owner":
			bad.Owner = "bob"
		case "session":
			bad.SessionID = "other"
		case "namespace":
			bad.Namespace = "other"
		case "run":
			bad.Run = "other"
		}
		if _, err := b.Exchange(bad); err == nil {
			t.Fatalf("accepted wrong %s", field)
		}
	}
	b.mu.Lock()
	b.leaseUntil = time.Now().Add(-time.Second)
	b.mu.Unlock()
	e.Operation = "poll"
	if r, err := b.Exchange(e); err != nil || r.Active {
		t.Fatalf("poll reattached: %+v %v", r, err)
	}
	e.Operation = "attach"
	b.SetVisionAvailable(func() bool { return true })
	if r, err := b.Exchange(e); err != nil || !r.Active || !r.VisionAvailable {
		t.Fatalf("explicit attach: %+v %v", r, err)
	}
}

func TestClaimResolveReplay(t *testing.T) {
	b, e := attached(t)
	request, done := queued(t, b, context.Background())
	if _, err := b.Request(context.Background(), Action{Kind: "type", Text: "proposed text"}, "frame"); err == nil {
		t.Fatal("accepted concurrent request")
	}
	e.Operation = "resolve"
	e.RequestID = request.RequestID
	e.Outcome = &Outcome{RequestID: request.RequestID, Status: "completed"}
	if _, err := b.Exchange(e); err == nil {
		t.Fatal("unclaimed resolve accepted")
	}
	e.Operation = "claim"
	e.Outcome = nil
	if _, err := b.Exchange(e); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Exchange(e); err == nil {
		t.Fatal("duplicate claim accepted")
	}
	b.mu.Lock()
	until := b.pending.deadline
	b.mu.Unlock()
	if time.Until(until) > TypeClaimTimeout {
		t.Fatal("unbounded claim")
	}
	e.Operation = "resolve"
	e.Outcome = &Outcome{RequestID: request.RequestID, Status: "completed"}
	if _, err := b.Exchange(e); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Exchange(e); err == nil {
		t.Fatal("duplicate resolve accepted")
	}
	select {
	case got := <-done:
		if got.err != nil || got.outcome.Status != "completed" {
			t.Fatalf("%+v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("request hung")
	}
}

func TestCancelStopExpiryRejectClaim(t *testing.T) {
	for _, mode := range []string{"cancel", "stop", "lease", "request", "claim", "close"} {
		t.Run(mode, func(t *testing.T) {
			b, e := attached(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			request, done := queued(t, b, ctx)
			b.mu.Lock()
			lease := b.leaseUntil
			b.mu.Unlock()
			switch mode {
			case "cancel":
				cancel()
			case "stop":
				e.Operation = "stop"
				if _, err := b.Exchange(e); err != nil {
					t.Fatal(err)
				}
			case "lease":
				b.mu.Lock()
				b.leaseUntil = time.Now().Add(-time.Second)
				b.mu.Unlock()
			case "request":
				b.mu.Lock()
				b.pending.deadline = time.Now().Add(-time.Second)
				b.mu.Unlock()
			case "claim":
				e.Operation = "claim"
				e.RequestID = request.RequestID
				if _, err := b.Exchange(e); err != nil {
					t.Fatal(err)
				}
				b.mu.Lock()
				if !b.leaseUntil.Equal(lease) {
					t.Error("claim renewed lease")
				}
				b.pending.deadline = time.Now().Add(-time.Second)
				b.mu.Unlock()
			case "close":
				b.Close()
			}
			e.Operation = "claim"
			e.RequestID = request.RequestID
			if _, err := b.Exchange(e); err == nil {
				t.Fatal("claim after cancellation accepted")
			}
			select {
			case got := <-done:
				if got.err == nil {
					t.Fatal("canceled request completed")
				}
			case <-time.After(time.Second):
				t.Fatal("request hung")
			}
		})
	}
}

func TestProtocolBoundsAndTextRestrictions(t *testing.T) {
	for _, raw := range []string{`{"kind":"activate","text":"secret"}`, `{"kind":"type","question":"secret"}`, `{"kind":"key","key":"secret"}`, `{"kind":"observe","question":"` + strings.Repeat("a", 2049) + `"}`} {
		var a Action
		if Decode(strings.NewReader(raw), &a) == nil && a.Validate() == nil {
			t.Fatal("accepted misplaced text or oversized input")
		}
	}
	var a Action
	if Decode(strings.NewReader(strings.Repeat(" ", MaxWire+1)), &a) == nil {
		t.Fatal("accepted oversized wire")
	}
	if Decode(strings.NewReader(`{"kind":"type"} {}`), &a) == nil {
		t.Fatal("accepted trailing JSON")
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 2, 3))); err != nil {
		t.Fatal(err)
	}
	c := Capture{FrameID: "frame", DataURL: "data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes()), PixelWidth: 2, PixelHeight: 3, Geometry: Geometry{Width: 2, Height: 3}}
	if _, err := c.PNG(); err != nil {
		t.Fatal(err)
	}
	c.PixelWidth = 4
	if _, err := c.PNG(); err == nil {
		t.Fatal("accepted dimensions mismatch")
	}
	c.DataURL = "data:image/png;base64," + strings.Repeat("a", base64.StdEncoding.EncodedLen(MaxScreenshot)+4)
	if _, err := c.PNG(); err == nil {
		t.Fatal("accepted oversized capture")
	}
}

func TestClaimedCancellationInvalidatesSession(t *testing.T) {
	for _, mode := range []string{"cancel", "deadline"} {
		t.Run(mode, func(t *testing.T) {
			b, e := attached(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			request, done := queued(t, b, ctx)
			e.Operation, e.RequestID = "claim", request.RequestID
			if _, err := b.Exchange(e); err != nil {
				t.Fatal(err)
			}
			if mode == "cancel" {
				cancel()
			} else {
				b.mu.Lock()
				b.pending.deadline = time.Now().Add(-time.Second)
				b.mu.Unlock()
			}
			e.Operation, e.RequestID = "poll", ""
			if r, err := b.Exchange(e); err != nil || r.Active || r.Pending != nil {
				t.Fatalf("poll: %+v %v", r, err)
			}
			select {
			case d := <-done:
				if d.err == nil {
					t.Fatal("canceled claim completed")
				}
			case <-time.After(time.Second):
				t.Fatal("request hung")
			}
			e.Operation, e.RequestID = "resolve", request.RequestID
			e.Outcome = &Outcome{RequestID: request.RequestID, Status: "completed"}
			if _, err := b.Exchange(e); err == nil {
				t.Fatal("late resolve accepted")
			}
		})
	}
}

func TestSessionContextSurvivesResolveNotInvalidation(t *testing.T) {
	for _, mode := range []string{"stop", "lease", "close"} {
		t.Run(mode, func(t *testing.T) {
			b, e := attached(t)
			ctx, cancel, err := b.SessionContext(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer cancel()
			request, done := queued(t, b, ctx)
			e.Operation, e.RequestID = "claim", request.RequestID
			if _, err := b.Exchange(e); err != nil {
				t.Fatal(err)
			}
			e.Operation = "resolve"
			e.Outcome = &Outcome{RequestID: request.RequestID, Status: "completed"}
			if _, err := b.Exchange(e); err != nil {
				t.Fatal(err)
			}
			select {
			case d := <-done:
				if d.err != nil {
					t.Fatal(d.err)
				}
			case <-time.After(time.Second):
				t.Fatal("resolve hung")
			}
			if !b.SessionValid(ctx) || !b.Active() {
				t.Fatal("successful resolve invalidated session")
			}
			e.RequestID, e.Outcome = "", nil
			switch mode {
			case "stop":
				e.Operation = "stop"
				if _, err := b.Exchange(e); err != nil {
					t.Fatal(err)
				}
			case "lease":
				b.mu.Lock()
				b.leaseUntil = time.Now().Add(-time.Second)
				b.mu.Unlock()
			case "close":
				b.Close()
			}
			if b.SessionValid(ctx) {
				t.Fatal("stale generation valid")
			}
			select {
			case <-ctx.Done():
			case <-time.After(time.Second):
				t.Fatal("session context not canceled")
			}
			if mode != "close" {
				e.Operation = "attach"
				if _, err := b.Exchange(e); err != nil {
					t.Fatal(err)
				}
				if b.SessionValid(ctx) {
					t.Fatal("same session ID resurrected generation")
				}
				stale := context.WithValue(context.Background(), sessionContextKey{}, ctx.Value(sessionContextKey{}))
				if b.SessionValid(stale) {
					t.Fatal("stale token valid without context cancellation")
				}
				if _, err := b.Request(stale, Action{Kind: "observe"}, ""); err == nil {
					t.Fatal("old generation queued in replacement")
				}
			}
		})
	}
}
