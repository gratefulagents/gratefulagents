package tools

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gratefulagents/gratefulagents/internal/computeruse"
	"github.com/gratefulagents/sdk/pkg/agentsdk"
	sdkvision "github.com/gratefulagents/sdk/pkg/agentsdk/tools/vision"
)

func TestComputerUsePolicyAndVisionInjection(t *testing.T) {
	b := computeruse.New("ns", "run")
	defer b.Close()
	r := NewRegistry(t.TempDir(), WithVisionTools(nil))
	tool := RegisterComputerUseTool(r, b)
	if tool.IsEnabled(nil) {
		t.Fatal("enabled while detached")
	}
	e := computeruse.Exchange{Namespace: "ns", Run: "run", Owner: "alice", SessionID: "session", Operation: "attach"}
	if _, err := b.Exchange(e); err != nil {
		t.Fatal(err)
	}
	if tool.IsEnabled(nil) {
		t.Fatal("enabled without vision")
	}
	got, _ := tool.Execute(context.Background(), json.RawMessage(`{"action":{"kind":"observe"}}`), "")
	if !got.IsError || !strings.Contains(got.Content, "unsupported") {
		t.Fatalf("%+v", got)
	}
	v := r.Get("AnalyzeImage").(*sdkvision.Tool)
	v.AnalyzeFn = func(context.Context, []byte, string, string) (string, error) { return "analysis", nil }
	if !tool.IsEnabled(nil) {
		t.Fatal("did not pick up injected vision callback")
	}
	if tool.IsEnabled(&agentsdk.RunContext{ToolAccessLevel: agentsdk.ToolAccessLevelReadOnly}) {
		t.Fatal("enabled read-only")
	}
	readonly := NewRegistry(t.TempDir(), WithReadOnlyTools(), WithVisionTools(nil))
	RegisterComputerUseTool(readonly, b)
	if readonly.Get("computer_use") != nil {
		t.Fatal("registered read-only mutation")
	}
	for _, raw := range []string{`{"frameId":"frame","action":{"kind":"type","text":"PRIVATE\n"}}`, `{"frameId":"frame","action":{"kind":"type"}}`, `{"action":{"kind":"activate","text":"PRIVATE"}}`, `{"action":{"kind":"type","question":"PRIVATE"}}`, `{"text":"PRIVATE","action":{"kind":"type"}}`} {
		result, err := tool.Execute(context.Background(), json.RawMessage(raw), "")
		if err != nil || !result.IsError || strings.Contains(result.Content, "PRIVATE") {
			t.Fatalf("unsafe rejection: %+v %v", result, err)
		}
	}
	var schema struct {
		Properties struct {
			Action struct {
				Properties struct {
					Text struct {
						Type      string
						MinLength int
						MaxLength int
						Pattern   string
					}
					Key struct{ Enum []string }
				}
			}
		}
	}
	if err := json.Unmarshal(tool.InputSchema(), &schema); err != nil {
		t.Fatal(err)
	}
	text := schema.Properties.Action.Properties.Text
	if text.Type != "string" || text.MinLength != 1 || text.MaxLength != 1000 || text.Pattern == "" {
		t.Fatalf("missing proposed text schema bounds: %+v", text)
	}
	if !strings.Contains(strings.Join(schema.Properties.Action.Properties.Key.Enum, ","), "Shift+Tab") {
		t.Fatal("schema missing Shift+Tab")
	}
}

func TestComputerUseObservationMemoryOnly(t *testing.T) {
	for _, failure := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "provider-error"}[failure], func(t *testing.T) {
			b := computeruse.New("ns", "run")
			defer b.Close()
			e := computeruse.Exchange{Namespace: "ns", Run: "run", Owner: "alice", SessionID: "session", Operation: "attach"}
			if _, err := b.Exchange(e); err != nil {
				t.Fatal(err)
			}
			var imageBytes bytes.Buffer
			if err := png.Encode(&imageBytes, image.NewRGBA(image.Rect(0, 0, 2, 3))); err != nil {
				t.Fatal(err)
			}
			encoded := base64.StdEncoding.EncodeToString(imageBytes.Bytes())
			called := false
			v := &sdkvision.Tool{AnalyzeWithDetailFn: func(ctx context.Context, data []byte, mime, prompt, detail string) (string, error) {
				called = true
				if !bytes.Equal(data, imageBytes.Bytes()) || mime != "image/png" || detail != "high" || !strings.Contains(prompt, "untrusted") || !strings.Contains(prompt, "what is visible") || !strings.Contains(prompt, computerUseCoordinates) {
					t.Error("wrong vision input")
				}
				if failure {
					return "", errors.New("PRIVATE " + encoded)
				}
				return "A blank desktop", nil
			}}
			tool := &ComputerUseTool{broker: b, vision: v}
			done := make(chan Result, 1)
			go func() {
				result, _ := tool.Execute(context.Background(), json.RawMessage(`{"action":{"kind":"observe","question":"what is visible"}}`), t.TempDir())
				done <- result
			}()
			var request *computeruse.Request
			deadline := time.Now().Add(time.Second)
			for time.Now().Before(deadline) {
				e.Operation = "poll"
				r, err := b.Exchange(e)
				if err != nil {
					t.Fatal(err)
				}
				if r.Pending != nil {
					request = r.Pending
					break
				}
				time.Sleep(time.Millisecond)
			}
			if request == nil {
				t.Fatal("no pending observation")
			}
			e.Operation = "claim"
			e.RequestID = request.RequestID
			if _, err := b.Exchange(e); err != nil {
				t.Fatal(err)
			}
			e.Operation = "resolve"
			e.Outcome = &computeruse.Outcome{RequestID: request.RequestID, Status: "completed", Message: "PRIVATE", Capture: &computeruse.Capture{FrameID: "frame-1", DataURL: "data:image/png;base64," + encoded, PixelWidth: 2, PixelHeight: 3, Geometry: computeruse.Geometry{Width: 2, Height: 3}}}
			if _, err := b.Exchange(e); err != nil {
				t.Fatal(err)
			}
			select {
			case result := <-done:
				if !called || result.IsError != failure || strings.Contains(result.Content, encoded) || strings.Contains(result.Content, "PRIVATE") || strings.Contains(result.Content, "dataUrl") {
					t.Fatalf("unsafe result: %+v", result)
				}
				if !failure && (!strings.Contains(result.Content, `"frameId":"frame-1"`) || !strings.Contains(result.Content, `"pixelHeight":3`) || !strings.Contains(result.Content, `"coordinates":`) || !strings.Contains(result.Content, "top-left")) {
					t.Fatalf("missing metadata: %+v", result)
				}
			case <-time.After(time.Second):
				t.Fatal("observation hung")
			}
		})
	}
}

func TestComputerUseProposedTextApprovalAndOutcomes(t *testing.T) {
	for _, tc := range []struct{ name, raw, text, status string }{
		{"completed", `{"kind":"type","text":"PRIVATE proposed text"}`, "PRIVATE proposed text", "completed"},
		{"observation-denied", `{"kind":"key","key":"Enter"}`, "", "completed"},
		{"vision-failed", `{"kind":"key","key":"Enter"}`, "", "completed"},
		{"denied", `{"kind":"type","text":"PRIVATE proposed text"}`, "PRIVATE proposed text", "denied"},
		{"failed", `{"kind":"type","text":"PRIVATE proposed text"}`, "PRIVATE proposed text", "failed"},
		{"escaped-limit", `{"kind":"type","text":"` + strings.Repeat(`\u0061`, 1000) + `"}`, strings.Repeat("a", 1000), "completed"},
		{"shift-tab", `{"kind":"key","key":"Shift+Tab"}`, "", "completed"},
		{"scroll", `{"kind":"scroll","deltaX":0,"deltaY":1000}`, "", "completed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := computeruse.New("ns", "run")
			defer b.Close()
			e := computeruse.Exchange{Namespace: "ns", Run: "run", Owner: "alice", SessionID: "session", Operation: "attach"}
			if _, err := b.Exchange(e); err != nil {
				t.Fatal(err)
			}
			tool := &ComputerUseTool{broker: b, vision: &sdkvision.Tool{AnalyzeFn: func(context.Context, []byte, string, string) (string, error) {
				if tc.status != "completed" || tc.name == "observation-denied" {
					t.Error("vision invoked without completed observation")
				}
				if tc.name == "vision-failed" {
					return "", errors.New("provider failed")
				}
				return "Visible result after input", nil
			}}}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			done := make(chan Result, 1)
			dir := t.TempDir()
			go func() {
				result, err := tool.Execute(ctx, json.RawMessage(`{"frameId":"frame","action":`+tc.raw+`}`), dir)
				if err != nil {
					t.Error(err)
				}
				done <- result
			}()
			var request *computeruse.Request
			deadline := time.Now().Add(time.Second)
			for time.Now().Before(deadline) {
				e.Operation = "poll"
				r, err := b.Exchange(e)
				if err != nil {
					t.Fatal(err)
				}
				if r.Pending != nil {
					request = r.Pending
					break
				}
				time.Sleep(time.Millisecond)
			}
			if request == nil {
				t.Fatal("no pending approval")
			}
			if request.Action.Text != tc.text {
				t.Fatal("approval lost proposed text")
			}
			select {
			case <-done:
				t.Fatal("action completed before approval")
			default:
			}
			e.Operation, e.RequestID = "claim", request.RequestID
			if _, err := b.Exchange(e); err != nil {
				t.Fatal(err)
			}
			e.Operation = "resolve"
			e.Outcome = &computeruse.Outcome{RequestID: request.RequestID, Status: tc.status, Message: "PRIVATE " + tc.text}
			if _, err := b.Exchange(e); err != nil {
				t.Fatal(err)
			}
			if tc.status == "completed" {
				var observation *computeruse.Request
				deadline := time.Now().Add(time.Second)
				for time.Now().Before(deadline) {
					e.Operation, e.RequestID, e.Outcome = "poll", "", nil
					response, err := b.Exchange(e)
					if err != nil {
						t.Fatal(err)
					}
					if response.Pending != nil {
						observation = response.Pending
						break
					}
					time.Sleep(time.Millisecond)
				}
				if observation == nil || observation.Action.Kind != "observe" || observation.FrameID != "" {
					t.Fatalf("missing fresh post-action observation: %+v", observation)
				}
				select {
				case <-done:
					t.Fatal("returned before observation approval")
				default:
				}
				e.Operation, e.RequestID = "claim", observation.RequestID
				if _, err := b.Exchange(e); err != nil {
					t.Fatal(err)
				}
				e.Operation = "resolve"
				e.Outcome = &computeruse.Outcome{RequestID: observation.RequestID, Status: "completed"}
				if tc.name == "observation-denied" {
					e.Outcome.Status = "denied"
				} else {
					var pngBytes bytes.Buffer
					if err := png.Encode(&pngBytes, image.NewRGBA(image.Rect(0, 0, 2, 3))); err != nil {
						t.Fatal(err)
					}
					e.Outcome.Capture = &computeruse.Capture{FrameID: "after-input", DataURL: "data:image/png;base64," + base64.StdEncoding.EncodeToString(pngBytes.Bytes()), PixelWidth: 2, PixelHeight: 3, Geometry: computeruse.Geometry{Width: 2, Height: 3}}
				}
				if _, err := b.Exchange(e); err != nil {
					t.Fatal(err)
				}
			}
			select {
			case result := <-done:
				if tc.status != "completed" {
					if !result.IsError || result.Content != "Desktop action "+tc.status+computerUseNoRetry {
						t.Fatalf("unexpected failed input result: %+v", result)
					}
				} else if tc.name == "observation-denied" || tc.name == "vision-failed" {
					if !result.IsError || !strings.Contains(result.Content, "Input completed, but its effect is unverified. Do not repeat the input.") {
						t.Fatalf("lost completed-input status: %+v", result)
					}
				} else {
					if result.IsError || !strings.Contains(result.Content, `"actionStatus":"completed"`) || !strings.Contains(result.Content, `"frameId":"after-input"`) || !strings.Contains(result.Content, "Visible result after input") {
						t.Fatalf("missing post-action analysis: %+v", result)
					}
				}
				if strings.Contains(result.Content, "PRIVATE") || strings.Contains(result.Content, "data:image") {
					t.Fatal("leaked input or capture")
				}
				e.Operation, e.RequestID, e.Outcome = "poll", "", nil
				response, err := b.Exchange(e)
				if err != nil || response.Pending != nil {
					t.Fatal("unexpected retry", err)
				}
			case <-time.After(time.Second):
				t.Fatal("action hung")
			}
			files, err := os.ReadDir(dir)
			if err != nil || len(files) != 0 {
				t.Fatal("tool persisted action data", err)
			}
		})
	}
}

func TestComputerUseRejectsInvalidNativeActions(t *testing.T) {
	b := computeruse.New("ns", "run")
	defer b.Close()
	e := computeruse.Exchange{Namespace: "ns", Run: "run", Owner: "alice", SessionID: "session", Operation: "attach"}
	if _, err := b.Exchange(e); err != nil {
		t.Fatal(err)
	}
	tool := &ComputerUseTool{broker: b, vision: &sdkvision.Tool{AnalyzeFn: func(context.Context, []byte, string, string) (string, error) { return "", nil }}}
	for _, action := range []string{
		`{"kind":"type","text":""}`,
		`{"kind":"type","text":"` + strings.Repeat("😀", 501) + `"}`,
		`{"kind":"type","text":"PRIVATE\u0085"}`,
		`{"kind":"key","key":"Shift+Enter"}`,
		`{"kind":"scroll","deltaX":1}`,
		`{"kind":"scroll","deltaY":1}`,
		`{"kind":"scroll","deltaX":0,"deltaY":0}`,
		`{"kind":"scroll","deltaX":0.5,"deltaY":1}`,
		`{"kind":"scroll","deltaX":0,"deltaY":1001}`,
	} {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		result, err := tool.Execute(ctx, json.RawMessage(`{"frameId":"frame","action":`+action+`}`), "")
		cancel()
		if err != nil || !result.IsError || result.Content != "Invalid computer use action" {
			t.Fatal("invalid action not rejected generically", err)
		}
	}
}

func TestComputerUseVisionSessionInvalidation(t *testing.T) {
	for _, mode := range []string{"stop", "replacement", "lease", "caller-cancel"} {
		t.Run(mode, func(t *testing.T) {
			b := computeruse.New("ns", "run")
			defer b.Close()
			e := computeruse.Exchange{Namespace: "ns", Run: "run", Owner: "alice", SessionID: "session", Operation: "attach"}
			if _, err := b.Exchange(e); err != nil {
				t.Fatal(err)
			}
			started := make(chan context.Context, 1)
			release := make(chan struct{})
			defer close(release)
			tool := &ComputerUseTool{broker: b, vision: &sdkvision.Tool{AnalyzeFn: func(ctx context.Context, _ []byte, _, _ string) (string, error) {
				started <- ctx
				<-release
				return "PRIVATE late screen analysis", nil
			}}}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan Result, 1)
			go func() {
				result, _ := tool.Execute(ctx, json.RawMessage(`{"action":{"kind":"observe"}}`), "")
				done <- result
			}()
			var request *computeruse.Request
			deadline := time.Now().Add(time.Second)
			for time.Now().Before(deadline) {
				e.Operation = "poll"
				r, err := b.Exchange(e)
				if err != nil {
					t.Fatal(err)
				}
				if r.Pending != nil {
					request = r.Pending
					break
				}
				time.Sleep(time.Millisecond)
			}
			if request == nil {
				t.Fatal("observation not queued")
			}
			e.Operation, e.RequestID = "claim", request.RequestID
			if _, err := b.Exchange(e); err != nil {
				t.Fatal(err)
			}
			var imageBytes bytes.Buffer
			if err := png.Encode(&imageBytes, image.NewRGBA(image.Rect(0, 0, 2, 3))); err != nil {
				t.Fatal(err)
			}
			e.Operation = "resolve"
			e.Outcome = &computeruse.Outcome{RequestID: request.RequestID, Status: "completed", Capture: &computeruse.Capture{FrameID: "frame", DataURL: "data:image/png;base64," + base64.StdEncoding.EncodeToString(imageBytes.Bytes()), PixelWidth: 2, PixelHeight: 3, Geometry: computeruse.Geometry{Width: 2, Height: 3}}}
			if _, err := b.Exchange(e); err != nil {
				t.Fatal(err)
			}
			var visionContext context.Context
			select {
			case visionContext = <-started:
			case <-time.After(time.Second):
				t.Fatal("vision not started")
			}
			e.RequestID, e.Outcome = "", nil
			switch mode {
			case "stop", "replacement":
				e.Operation = "stop"
				if _, err := b.Exchange(e); err != nil {
					t.Fatal(err)
				}
				if mode == "replacement" {
					e.Operation = "attach"
					if _, err := b.Exchange(e); err != nil {
						t.Fatal(err)
					}
				}
			case "caller-cancel":
				cancel()
			}
			select {
			case <-visionContext.Done():
			case <-time.After(computeruse.Lease + time.Second):
				t.Fatal("vision context not canceled")
			}
			release <- struct{}{}
			select {
			case result := <-done:
				if !result.IsError || strings.Contains(result.Content, "PRIVATE") || strings.Contains(result.Content, "frameId") || !strings.Contains(result.Content, "Do not automatically retry") {
					t.Fatalf("unsafe result: %+v", result)
				}
			case <-time.After(time.Second):
				t.Fatal("vision result hung")
			}
		})
	}
}

func TestComputerUseSafetyGuidance(t *testing.T) {
	tool := &ComputerUseTool{}
	for _, text := range []string{"no keystroke logs", "may appear in model conversation/run history", "provider retention policies apply", "Do not automatically retry", "OS events may be partially applied", "fresh observation and approval", "top-left", "not desktop points", "Vision may be imperfect"} {
		if !strings.Contains(tool.Description(), text) {
			t.Fatalf("description missing %q", text)
		}
	}
}

func TestComputerUseClaimedCancellationWarnsAgainstRetry(t *testing.T) {
	b := computeruse.New("ns", "run")
	defer b.Close()
	e := computeruse.Exchange{Namespace: "ns", Run: "run", Owner: "alice", SessionID: "session", Operation: "attach"}
	if _, err := b.Exchange(e); err != nil {
		t.Fatal(err)
	}
	tool := &ComputerUseTool{broker: b, vision: &sdkvision.Tool{AnalyzeFn: func(context.Context, []byte, string, string) (string, error) {
		t.Error("input invoked vision")
		return "", nil
	}}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan Result, 1)
	go func() {
		result, _ := tool.Execute(ctx, json.RawMessage(`{"frameId":"frame","action":{"kind":"type","text":"PRIVATE"}}`), "")
		done <- result
	}()
	deadline := time.Now().Add(time.Second)
	var request *computeruse.Request
	for time.Now().Before(deadline) {
		e.Operation = "poll"
		r, err := b.Exchange(e)
		if err != nil {
			t.Fatal(err)
		}
		if r.Pending != nil {
			request = r.Pending
			break
		}
		time.Sleep(time.Millisecond)
	}
	if request == nil {
		t.Fatal("input not queued")
	}
	e.Operation, e.RequestID = "claim", request.RequestID
	if _, err := b.Exchange(e); err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case result := <-done:
		if !result.IsError || strings.Contains(result.Content, "PRIVATE") || !strings.Contains(result.Content, computerUseNoRetry) {
			t.Fatalf("unsafe cancellation: %+v", result)
		}
	case <-time.After(time.Second):
		t.Fatal("input hung")
	}
	e.Operation, e.RequestID = "poll", ""
	if r, err := b.Exchange(e); err != nil || r.Active {
		t.Fatalf("canceled claim remained active: %+v %v", r, err)
	}
}

func TestComputerUseWorkflowDescription(t *testing.T) {
	description := (&ComputerUseTool{}).Description()
	for _, requirement := range []string{"not a headless Browser session", "locally selected mode", "requests a fresh observation", "not that the task succeeded"} {
		if !strings.Contains(description, requirement) {
			t.Errorf("missing workflow guidance: %s", requirement)
		}
	}
	if strings.Contains(description, "Every action requires local human approval") {
		t.Fatal("description contradicts session approval modes")
	}
}
