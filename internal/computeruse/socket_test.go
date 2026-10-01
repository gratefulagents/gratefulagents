package computeruse

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func listenTest(t *testing.T) (*Broker, string) {
	t.Helper()
	uid := uuid.NewString()
	path, err := SocketPath(uid)
	if err != nil {
		t.Fatal(err)
	}
	b := New("ns", "run")
	ctx, cancel := context.WithCancel(context.Background())
	l, err := Listen(ctx, b, uid)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); b.Close(); l.Close(); os.Remove(path); os.Remove(filepath.Dir(path)) })
	return b, uid
}

func bridge(t *testing.T, uid string, e Exchange) (Response, error) {
	t.Helper()
	raw, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := Bridge(context.Background(), uid, bytes.NewReader(raw), &out); err != nil {
		return Response{}, err
	}
	var r Response
	if err := Decode(&out, &r); err != nil {
		t.Fatal(err)
	}
	return r, nil
}

func TestBridgeSocketRoundTrip(t *testing.T) {
	b, uid := listenTest(t)
	path, _ := SocketPath(uid)
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("socket permissions: %v %v", info, err)
	}
	dir, err := os.Stat(filepath.Dir(path))
	if err != nil || dir.Mode().Perm() != 0700 {
		t.Fatalf("dir permissions: %v %v", dir, err)
	}
	e := Exchange{Namespace: "ns", Run: "run", Owner: "alice", SessionID: "s1", Operation: "connect"}
	if r, err := bridge(t, uid, e); err != nil || !r.Active || r.Protocol != Protocol {
		t.Fatalf("connect: %+v %v", r, err)
	}
	done := make(chan requestOutcome, 1)
	go func() {
		time.Sleep(50 * time.Millisecond)
		res, err := b.Request(context.Background(), Action{Action: "key", Text: "cmd+c"})
		done <- requestOutcome{res, err}
	}()
	// The long-poll outlives the old 3s connection deadline design.
	e.Operation = "next"
	r, err := bridge(t, uid, e)
	if err != nil || r.Request == nil || r.Request.Action.Text != "cmd+c" {
		t.Fatalf("next: %+v %v", r, err)
	}
	e.Operation, e.RequestID, e.Result = "result", r.Request.ID, &Result{RequestID: r.Request.ID, OK: true, Screenshot: shot(t, 64, 48)}
	if r, err := bridge(t, uid, e); err != nil || !r.Active || r.Reason != "" {
		t.Fatalf("result: %+v %v", r, err)
	}
	o := await(t, done)
	if o.err != nil || o.res.Screenshot.Width != 64 {
		t.Fatalf("outcome: %+v", o)
	}
	e = Exchange{Namespace: "ns", Run: "run", Owner: "mallory", SessionID: "s2", Operation: "connect"}
	if r, err := bridge(t, uid, e); err != nil || r.Active || r.Reason != ReasonForeignOwner {
		t.Fatalf("foreign owner: %+v %v", r, err)
	}
}

func TestBridgeLongPollOverSocket(t *testing.T) {
	shortTimeouts(t, 5*time.Second, 4*time.Second, 5*time.Second, 5*time.Second)
	_, uid := listenTest(t)
	e := Exchange{Namespace: "ns", Run: "run", Owner: "alice", SessionID: "s1", Operation: "connect"}
	bridge(t, uid, e)
	e.Operation = "next"
	start := time.Now()
	r, err := bridge(t, uid, e)
	if err != nil || !r.Active || r.Request != nil || time.Since(start) < 3500*time.Millisecond {
		t.Fatalf("idle long-poll over socket: %+v %v after %v", r, err, time.Since(start))
	}
}

func TestBridgeRejectsInvalidInput(t *testing.T) {
	_, uid := listenTest(t)
	var out bytes.Buffer
	for _, in := range []string{
		strings.Repeat("x", MaxWire+1),
		`{"namespace":"ns","run":"run","owner":"alice","sessionId":"s1","operation":"connect","extra":1}`,
		`{"namespace":"ns","run":"run","owner":"alice","sessionId":"s1","operation":"attach_desktop"}`,
	} {
		if Bridge(context.Background(), uid, strings.NewReader(in), &out) == nil || out.Len() != 0 {
			t.Fatal("bridge accepted invalid input")
		}
	}
	// A wrong-run exchange is refused by the broker and the bridge reports failure.
	if _, err := bridge(t, uid, Exchange{Namespace: "ns", Run: "other", Owner: "alice", SessionID: "s1", Operation: "connect"}); err == nil {
		t.Fatal("wrong run accepted")
	}
	if _, err := bridge(t, uuid.NewString(), Exchange{Namespace: "ns", Run: "run", Owner: "alice", SessionID: "s1", Operation: "connect"}); err == nil {
		t.Fatal("bridge reached a socket that does not exist")
	}
}

func TestSocketOwnershipRestartAndShutdown(t *testing.T) {
	uid := uuid.NewString()
	path, _ := SocketPath(uid)
	if !strings.HasPrefix(path, "/tmp/gratefulagents-desktop-") || strings.Contains(path, uid) {
		t.Fatalf("unsafe path %q", path)
	}
	if _, err := SocketPath(""); err == nil {
		t.Fatal("accepted empty uid")
	}
	b := New("ns", "run")
	ctx, cancel := context.WithCancel(context.Background())
	l, err := Listen(ctx, b, uid)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); b.Close(); l.Close(); os.Remove(filepath.Dir(path)) })
	if _, err := Listen(ctx, b, uid); err == nil {
		t.Fatal("replaced live socket")
	}
	if _, err := b.Exchange(context.Background(), Exchange{Namespace: "ns", Run: "run", Owner: "a", SessionID: "s", Operation: "connect"}); err != nil {
		t.Fatal(err)
	}
	cancel()
	deadline := time.Now().Add(time.Second)
	for b.Active() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if b.Active() {
		t.Fatal("context cancel did not close broker")
	}
	l.Close()
	stale, err := net.ListenUnix("unix", &net.UnixAddr{Net: "unix", Name: path})
	if err != nil {
		t.Fatal(err)
	}
	stale.SetUnlinkOnClose(false)
	stale.Close()
	fresh := New("ns", "run")
	defer fresh.Close()
	restarted, err := Listen(context.Background(), fresh, uid)
	if err != nil {
		t.Fatalf("stale socket recovery: %v", err)
	}
	restarted.Close()
	if err := os.WriteFile(path, []byte("do not replace"), 0600); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(path)
	if _, err := Listen(context.Background(), fresh, uid); err == nil {
		t.Fatal("replaced regular file")
	}
}
