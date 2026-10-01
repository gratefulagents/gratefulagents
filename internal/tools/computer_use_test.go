package tools

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/jpeg"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gratefulagents/gratefulagents/internal/computeruse"
	"github.com/gratefulagents/sdk/pkg/agentsdk"
)

func testJPEG(t testing.TB, w, h int) *computeruse.Screenshot {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, image.NewRGBA(image.Rect(0, 0, w, h)), nil); err != nil {
		t.Fatal(err)
	}
	return &computeruse.Screenshot{MediaType: "image/jpeg", Data: base64.StdEncoding.EncodeToString(buf.Bytes()), Width: w, Height: h}
}

// fakeDesktop drives a broker the way the desktop controller loop does.
type fakeDesktop struct {
	mu      sync.Mutex
	actions []computeruse.Action
}

func startFakeDesktop(t *testing.T, b *computeruse.Broker, handle func(computeruse.Action) computeruse.Result) *fakeDesktop {
	t.Helper()
	d := &fakeDesktop{}
	e := computeruse.Exchange{Namespace: "ns", Run: "run", Owner: "alice", SessionID: "desk"}
	e.Operation = "connect"
	if r, err := b.Exchange(context.Background(), e); err != nil || !r.Active {
		t.Fatalf("connect: %+v %v", r, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	t.Cleanup(func() { cancel(); <-done })
	go func() {
		defer close(done)
		for ctx.Err() == nil {
			next := e
			next.Operation = "next"
			r, err := b.Exchange(ctx, next)
			if err != nil || !r.Active {
				return
			}
			if r.Request == nil {
				continue
			}
			d.mu.Lock()
			d.actions = append(d.actions, r.Request.Action)
			d.mu.Unlock()
			res := handle(r.Request.Action)
			res.RequestID = r.Request.ID
			result := e
			result.Operation, result.RequestID, result.Result = "result", r.Request.ID, &res
			if _, err := b.Exchange(ctx, result); err != nil {
				t.Errorf("result rejected: %v", err)
				return
			}
		}
	}()
	return d
}

func (d *fakeDesktop) seen() []computeruse.Action {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]computeruse.Action(nil), d.actions...)
}

func newComputerUseTool(t *testing.T) (*ComputerUseTool, *computeruse.Broker) {
	t.Helper()
	b := computeruse.New("ns", "run")
	t.Cleanup(func() { b.Close() })
	r := NewRegistry(t.TempDir())
	tool := RegisterComputerUseTool(r, b)
	if tool == nil || r.Get("computer_use") == nil {
		t.Fatal("tool not registered")
	}
	return tool, b
}

func runComputerUse(t *testing.T, tool *ComputerUseTool, input string) Result {
	t.Helper()
	res, err := tool.Execute(context.Background(), json.RawMessage(input), "")
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestComputerUseToolMetadata(t *testing.T) {
	if RegisterComputerUseTool(NewRegistry(t.TempDir()), nil) != nil {
		t.Fatal("registered without broker")
	}
	tool, b := newComputerUseTool(t)
	var schema map[string]any
	if err := json.Unmarshal(tool.InputSchema(), &schema); err != nil {
		t.Fatalf("schema: %v", err)
	}
	enum := schema["properties"].(map[string]any)["action"].(map[string]any)["enum"].([]any)
	if len(enum) != len(computeruse.ActionNames) {
		t.Fatalf("enum %v", enum)
	}
	for _, field := range []string{"coordinate", "start_coordinate", "text", "scroll_direction", "scroll_amount", "repeat", "duration", "region", "url"} {
		if schema["properties"].(map[string]any)[field] == nil {
			t.Errorf("schema missing %s", field)
		}
	}
	desc := tool.Description()
	for _, want := range []string{"macOS", "screenshot first", "latest screenshot", "fresh screenshot", "cmd", "open_url", "zoom", "not clickable", "untrusted", "denied, do not retry"} {
		if !strings.Contains(desc, want) {
			t.Errorf("description missing %q", want)
		}
	}
	if tool.TimeoutSeconds() != 330 || tool.IsReadOnly() {
		t.Fatal("metadata")
	}
	if tool.IsEnabled(nil) {
		t.Fatal("enabled without desktop")
	}
	startFakeDesktop(t, b, func(computeruse.Action) computeruse.Result { return computeruse.Result{Error: "unused"} })
	if !tool.IsEnabled(&agentsdk.RunContext{}) {
		t.Fatal("disabled with desktop")
	}
	if tool.IsEnabled(&agentsdk.RunContext{ToolAccessLevel: agentsdk.ToolAccessLevelReadOnly}) {
		t.Fatal("enabled for read-only access")
	}
}

func TestComputerUseScreenshotAttachedAsImage(t *testing.T) {
	tool, b := newComputerUseTool(t)
	shot := testJPEG(t, 1183, 768)
	desk := startFakeDesktop(t, b, func(a computeruse.Action) computeruse.Result {
		res := computeruse.Result{OK: true, Screenshot: shot}
		if a.Action == "cursor_position" {
			res.Cursor = &computeruse.Point{X: 10, Y: 20}
		}
		if a.Action == "zoom" {
			res.Screenshot = testJPEG(t, 400, 300)
		}
		return res
	})
	res := runComputerUse(t, tool, `{"action":"screenshot"}`)
	if res.IsError || res.Content != "Screenshot taken. Screen 1183x768." || len(res.Images) != 1 {
		t.Fatalf("screenshot: %+v", res.Content)
	}
	if img := res.Images[0]; img.MediaType != "image/jpeg" || img.Detail != "high" || img.Data != shot.Data {
		t.Fatalf("image: %s %s", img.MediaType, img.Detail)
	}
	for input, want := range map[string]string{
		`{"action":"left_click","coordinate":[512,300]}`:                           "left_click at (512, 300) done. Screen 1183x768.",
		`{"action":"left_click_drag","start_coordinate":[1,2],"coordinate":[3,4]}`: "left_click_drag from (1, 2) to (3, 4) done. Screen 1183x768.",
		`{"action":"scroll","scroll_direction":"down","coordinate":[5,6]}`:         "scroll down by 3 at (5, 6) done. Screen 1183x768.",
		`{"action":"key","text":"cmd+c","repeat":2}`:                               "key cmd+c x2 done. Screen 1183x768.",
		`{"action":"type","text":"héllo"}`:                                         "type of 5 characters done. Screen 1183x768.",
		`{"action":"cursor_position"}`:                                             "Cursor at (10, 20). Screen 1183x768.",
		`{"action":"open_url","url":"https://example.com"}`:                        "open_url https://example.com done. Screen 1183x768.",
		`{"action":"zoom","region":[0,0,100,75]}`:                                  "Zoomed region [0, 0, 100, 75] (image 400x300). Zoom image coordinates are not clickable; use coordinates from a full screenshot.",
	} {
		res := runComputerUse(t, tool, input)
		if res.IsError || res.Content != want || len(res.Images) != 1 {
			t.Errorf("%s: %q images=%d", input, res.Content, len(res.Images))
		}
	}
	// Coordinates outside the latest screenshot never reach the desktop.
	before := len(desk.seen())
	res = runComputerUse(t, tool, `{"action":"left_click","coordinate":[1183,10]}`)
	if !res.IsError || !strings.Contains(res.Content, "outside the latest screenshot") || len(desk.seen()) != before {
		t.Fatalf("out of bounds: %+v", res.Content)
	}
}

func TestComputerUseWaitIsLocal(t *testing.T) {
	tool, b := newComputerUseTool(t)
	desk := startFakeDesktop(t, b, func(computeruse.Action) computeruse.Result {
		return computeruse.Result{OK: true, Screenshot: testJPEG(t, 32, 16)}
	})
	start := time.Now()
	res := runComputerUse(t, tool, `{"action":"wait","duration":0.2}`)
	if res.IsError || res.Content != "Waited 0.2s. Screen 32x16." || len(res.Images) != 1 {
		t.Fatalf("wait: %+v", res.Content)
	}
	if time.Since(start) < 200*time.Millisecond {
		t.Fatal("did not wait")
	}
	if seen := desk.seen(); len(seen) != 1 || seen[0].Action != "screenshot" {
		t.Fatalf("desktop saw %+v", seen)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if res, _ := tool.Execute(ctx, json.RawMessage(`{"action":"wait","duration":5}`), ""); !res.IsError {
		t.Fatal("canceled wait succeeded")
	}
}

func TestComputerUseDeniedAndFailure(t *testing.T) {
	tool, b := newComputerUseTool(t)
	shot := testJPEG(t, 20, 10)
	startFakeDesktop(t, b, func(a computeruse.Action) computeruse.Result {
		switch a.Action {
		case "left_click":
			return computeruse.Result{Error: "user denied", Denied: true}
		case "type":
			return computeruse.Result{Error: "secure input\u202e active\n" + strings.Repeat("x", 900), Screenshot: shot}
		}
		return computeruse.Result{OK: true, Screenshot: shot}
	})
	runComputerUse(t, tool, `{"action":"screenshot"}`)
	res := runComputerUse(t, tool, `{"action":"left_click","coordinate":[1,1]}`)
	if !res.IsError || !strings.Contains(res.Content, "denied") || !strings.Contains(res.Content, "Do not retry") || len(res.Images) != 0 {
		t.Fatalf("denied: %+v", res)
	}
	res = runComputerUse(t, tool, `{"action":"type","text":"x"}`)
	if !res.IsError || !strings.Contains(res.Content, "type failed") || !strings.Contains(res.Content, "secure input active") || strings.ContainsAny(res.Content, "\u202e\n") || len(res.Images) != 1 {
		t.Fatalf("failure: %q images=%d", res.Content, len(res.Images))
	}
	if len(res.Content) > 800 {
		t.Fatalf("desktop error not bounded: %d", len(res.Content))
	}
}

func TestComputerUseErrors(t *testing.T) {
	tool, b := newComputerUseTool(t)
	res := runComputerUse(t, tool, `{"action":"screenshot"}`)
	if !res.IsError || !strings.Contains(res.Content, "Computer tab") || !strings.Contains(res.Content, "Start") {
		t.Fatalf("no desktop: %q", res.Content)
	}
	for _, input := range []string{`{"action":"observe"}`, `{"action":"left_click"}`, `{"action":"screenshot","frameId":"x"}`, `not json`, `{"action":"key","text":"cmd+alt+escape"}`} {
		if res := runComputerUse(t, tool, input); !res.IsError || !strings.Contains(res.Content, "Invalid computer_use input") {
			t.Errorf("%s: %q", input, res.Content)
		}
	}
	// A desktop that connects but never polls lets the request time out.
	e := computeruse.Exchange{Namespace: "ns", Run: "run", Owner: "alice", SessionID: "idle", Operation: "connect"}
	if _, err := b.Exchange(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	old := computeruse.PickupTimeout
	computeruse.PickupTimeout = 100 * time.Millisecond
	defer func() { computeruse.PickupTimeout = old }()
	busy := make(chan Result, 1)
	go func() { busy <- runComputerUse(t, tool, `{"action":"screenshot"}`) }()
	time.Sleep(30 * time.Millisecond)
	if res := runComputerUse(t, tool, `{"action":"screenshot"}`); !res.IsError || !strings.Contains(res.Content, "still in progress") {
		t.Fatalf("busy: %q", res.Content)
	}
	if res := <-busy; !res.IsError || !strings.Contains(res.Content, "did not pick up") {
		t.Fatalf("timeout: %q", res.Content)
	}
	go func() { busy <- runComputerUse(t, tool, `{"action":"screenshot"}`) }()
	time.Sleep(30 * time.Millisecond)
	e.Operation = "disconnect"
	if _, err := b.Exchange(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	if res := <-busy; !res.IsError || !strings.Contains(res.Content, "disconnected") || !strings.Contains(res.Content, "reconnect") {
		t.Fatalf("disconnect: %q", res.Content)
	}
}
