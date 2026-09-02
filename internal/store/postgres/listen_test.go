package postgres

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestSessionChangeChannelFor(t *testing.T) {
	id := uuid.MustParse("6ba7b810-9dad-11d1-80b4-00c04fd430c8")
	got := sessionChangeChannelFor(id)
	want := "session_change_6ba7b8109dad11d180b400c04fd430c8"
	if got != want {
		t.Fatalf("sessionChangeChannelFor() = %q, want %q", got, want)
	}
	if len(got) > 63 {
		t.Fatalf("channel name %q exceeds the 63-char identifier limit", got)
	}
	for _, r := range got {
		if !(r == '_' || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')) {
			t.Fatalf("channel name %q contains %q, which needs quoting in LISTEN", got, r)
		}
	}
	if sessionChangeChannelFor(uuid.New()) == got {
		t.Fatal("distinct sessions must map to distinct channels")
	}
}

func TestSessionChangeBrokerFanOut(t *testing.T) {
	var b sessionChangeBroker
	a, b1 := uuid.New(), uuid.New()

	wakeA1, cancelA1 := b.subscribe(a)
	wakeA2, cancelA2 := b.subscribe(a)
	wakeB, cancelB := b.subscribe(b1)
	defer cancelB()

	b.notify(a)
	for i, ch := range []<-chan struct{}{wakeA1, wakeA2} {
		select {
		case <-ch:
		default:
			t.Fatalf("subscriber %d of session A not woken", i)
		}
	}
	select {
	case <-wakeB:
		t.Fatal("session B subscriber woken by session A notification")
	default:
	}

	// Repeated notifications coalesce into the single buffered wake-up and
	// never block the notifier.
	done := make(chan struct{})
	go func() {
		for range 10 {
			b.notify(a)
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("notify blocked on a subscriber that already has a pending wake-up")
	}
	<-wakeA1
	select {
	case <-wakeA1:
		t.Fatal("more than one wake-up buffered per subscriber")
	default:
	}
	<-wakeA2

	cancelA1()
	b.notify(a)
	select {
	case <-wakeA1:
		t.Fatal("cancelled subscriber still receives wake-ups")
	default:
	}
	select {
	case <-wakeA2:
	default:
		t.Fatal("remaining subscriber missed a wake-up after a sibling cancelled")
	}

	cancelA2()
	b.mu.Lock()
	_, stillTracked := b.subs[a]
	b.mu.Unlock()
	if stillTracked {
		t.Fatal("session entry not removed after its last subscriber cancelled")
	}
	b.notify(a) // no subscribers: must not panic
	b.notify(uuid.New())
}

func TestSessionChangeRouters(t *testing.T) {
	sessionID := uuid.New()
	other := uuid.New()

	if id, ok := globalSessionChangeRouter(sessionChangeChannel, sessionID.String()); !ok || id != sessionID {
		t.Fatalf("global router: (%v, %v), want (%v, true)", id, ok, sessionID)
	}
	if _, ok := globalSessionChangeRouter(sessionChangeChannel, "not-a-uuid"); ok {
		t.Fatal("global router accepted a malformed payload")
	}
	if _, ok := globalSessionChangeRouter(sessionChangeChannelFor(sessionID), sessionID.String()); ok {
		t.Fatal("global router accepted a per-session channel")
	}

	route := perSessionChangeRouter(sessionID)
	if id, ok := route(sessionChangeChannelFor(sessionID), sessionID.String()); !ok || id != sessionID {
		t.Fatalf("per-session router: (%v, %v), want (%v, true)", id, ok, sessionID)
	}
	if _, ok := route(sessionChangeChannelFor(other), other.String()); ok {
		t.Fatal("per-session router accepted another session's channel")
	}
	if _, ok := route(sessionChangeChannel, sessionID.String()); ok {
		t.Fatal("per-session router accepted the global channel")
	}
}
