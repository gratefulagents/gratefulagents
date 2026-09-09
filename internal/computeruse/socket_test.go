package computeruse

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestSocketOwnershipRestartAndShutdown(t *testing.T) {
	uid := uuid.NewString()
	path, _ := SocketPath(uid)
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
	cancel()
	deadline := time.Now().Add(time.Second)
	for b.Active() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
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

func TestResponseRejectsUnboundedOrSensitiveFields(t *testing.T) {
	for _, r := range []Response{
		{Reason: "PRIVATE"},
		{Active: true, Pending: &Request{RequestID: "id", Action: Action{Kind: "observe", Question: strings.Repeat("a", 2049)}}},
		{Pending: &Request{RequestID: "id", Action: Action{Kind: "observe"}}},
	} {
		if r.Validate() == nil {
			t.Fatal("accepted unsafe response")
		}
	}
}
