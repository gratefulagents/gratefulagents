package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/gratefulagents/gratefulagents/internal/computeruse"
)

// TestComputerUseOverRelay drives the production tool, broker, socket and
// Bridge; only the desktop executor is synthetic.
func TestComputerUseOverRelay(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b := computeruse.New("ci", "relay")
	uid := uuid.NewString()
	l, err := computeruse.Listen(ctx, b, uid)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		l.Close()
		b.Close()
		path, _ := computeruse.SocketPath(uid)
		os.Remove(path)
		os.Remove(filepath.Dir(path))
	})
	relay := func(e computeruse.Exchange) computeruse.Response {
		raw, _ := json.Marshal(e)
		var out bytes.Buffer
		if err := computeruse.Bridge(ctx, uid, bytes.NewReader(raw), &out); err != nil {
			t.Errorf("bridge %s: %v", e.Operation, err)
			return computeruse.Response{}
		}
		var r computeruse.Response
		if err := computeruse.Decode(&out, &r); err != nil {
			t.Errorf("decode: %v", err)
		}
		return r
	}
	e := computeruse.Exchange{Namespace: "ci", Run: "relay", Owner: "ci-owner", SessionID: "ci-session", Operation: "connect"}
	if !relay(e).Active {
		t.Fatal("relay did not connect")
	}
	tool := &ComputerUseTool{broker: b}
	shot := testJPEG(t, 300, 200)
	desktop := make(chan []string, 1)
	go func() {
		var seen []string
		for len(seen) < 2 {
			next := e
			next.Operation = "next"
			r := relay(next)
			if r.Request == nil {
				continue
			}
			seen = append(seen, r.Request.Action.Action+" "+r.Request.Action.Text)
			res := computeruse.Result{RequestID: r.Request.ID, OK: true, Screenshot: shot}
			result := e
			result.Operation, result.RequestID, result.Result = "result", r.Request.ID, &res
			relay(result)
		}
		desktop <- seen
	}()
	if res := runComputerUse(t, tool, `{"action":"screenshot"}`); res.IsError || len(res.Images) != 1 {
		t.Fatalf("screenshot: %q", res.Content)
	}
	if res := runComputerUse(t, tool, `{"action":"type","text":"hello"}`); res.IsError || !strings.Contains(res.Content, "Screen 300x200") {
		t.Fatalf("type: %q", res.Content)
	}
	if seen := <-desktop; strings.Join(seen, ",") != "screenshot ,type hello" {
		t.Fatalf("desktop saw %v", seen)
	}
}
