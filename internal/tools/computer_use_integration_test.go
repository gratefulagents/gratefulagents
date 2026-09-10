package tools

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/gratefulagents/gratefulagents/internal/computeruse"
	sdkvision "github.com/gratefulagents/sdk/pkg/agentsdk/tools/vision"
)

// This harness uses the production tool, broker, socket and Bridge. Only the
// desktop executor and vision provider are synthetic; no OS input is generated.
type desktopWorkflowHarness struct {
	t        *testing.T
	ctx      context.Context
	uid      string
	exchange computeruse.Exchange
	tool     *ComputerUseTool
}

func newDesktopWorkflowHarness(t *testing.T) *desktopWorkflowHarness {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	b := computeruse.New("ci", "desktop-workflow")
	uid := uuid.NewString()
	listener, err := computeruse.Listen(ctx, b, uid)
	if err != nil {
		cancel()
		_ = b.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		_ = listener.Close()
		_ = b.Close()
		path, _ := computeruse.SocketPath(uid)
		_ = os.Remove(path)
		_ = os.Remove(filepath.Dir(path))
	})
	h := &desktopWorkflowHarness{
		t: t, ctx: ctx, uid: uid,
		exchange: computeruse.Exchange{Namespace: "ci", Run: "desktop-workflow", Owner: "ci-owner", SessionID: "ci-session"},
		tool: &ComputerUseTool{broker: b, vision: &sdkvision.Tool{AnalyzeFn: func(_ context.Context, data []byte, mime, _ string) (string, error) {
			if mime != "image/png" {
				t.Error("vision did not receive PNG")
			}
			img, err := png.Decode(bytes.NewReader(data))
			if err != nil {
				return "", err
			}
			r, _, _, _ := img.At(0, 0).RGBA()
			if r != 0 {
				return "The synthetic field contains hello", nil
			}
			return "The synthetic field is empty", nil
		}}},
	}
	if !h.relay("attach", "", nil).Active {
		t.Fatal("relay did not attach")
	}
	return h
}

func (h *desktopWorkflowHarness) relay(operation, requestID string, outcome *computeruse.Outcome) computeruse.Response {
	h.t.Helper()
	e := h.exchange
	e.Operation, e.RequestID, e.Outcome = operation, requestID, outcome
	input, err := json.Marshal(e)
	if err != nil {
		h.t.Fatal(err)
	}
	var output bytes.Buffer
	if err := computeruse.Bridge(h.ctx, h.uid, bytes.NewReader(input), &output); err != nil {
		h.t.Fatal(err)
	}
	var response computeruse.Response
	if err := json.Unmarshal(output.Bytes(), &response); err != nil {
		h.t.Fatal(err)
	}
	return response
}

func (h *desktopWorkflowHarness) start(raw string) <-chan Result {
	done := make(chan Result, 1)
	go func() {
		result, err := h.tool.Execute(h.ctx, json.RawMessage(raw), "")
		if err != nil {
			h.t.Error(err)
		}
		done <- result
	}()
	return done
}

func (h *desktopWorkflowHarness) next(kind string) *computeruse.Request {
	h.t.Helper()
	timer := time.NewTicker(time.Millisecond)
	defer timer.Stop()
	for {
		response := h.relay("poll", "", nil)
		if response.Pending != nil {
			if response.Pending.Action.Kind != kind {
				h.t.Fatalf("wanted %s, got %s", kind, response.Pending.Action.Kind)
			}
			return response.Pending
		}
		select {
		case <-h.ctx.Done():
			h.t.Fatal("timed out waiting for desktop request")
		case <-timer.C:
		}
	}
}

func (h *desktopWorkflowHarness) complete(request *computeruse.Request, status string, capture *computeruse.Capture) {
	h.t.Helper()
	h.relay("claim", request.RequestID, nil)
	h.relay("resolve", request.RequestID, &computeruse.Outcome{RequestID: request.RequestID, Status: status, Capture: capture})
}

func (h *desktopWorkflowHarness) result(done <-chan Result) Result {
	h.t.Helper()
	select {
	case result := <-done:
		return result
	case <-h.ctx.Done():
		h.t.Fatal("tool did not finish")
		return Result{}
	}
}

func syntheticDesktopCapture(t *testing.T, frame string, filled bool) *computeruse.Capture {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	if filled {
		img.SetRGBA(0, 0, color.RGBA{R: 255, A: 255})
	}
	var data bytes.Buffer
	if err := png.Encode(&data, img); err != nil {
		t.Fatal(err)
	}
	return &computeruse.Capture{FrameID: frame, DataURL: "data:image/png;base64," + base64.StdEncoding.EncodeToString(data.Bytes()), PixelWidth: 2, PixelHeight: 2, Geometry: computeruse.Geometry{Width: 2, Height: 2}}
}

func TestComputerUseWorkflowOverSocket(t *testing.T) {
	for _, scenario := range []string{"completed", "observation-denied", "disconnected"} {
		t.Run(scenario, func(t *testing.T) {
			h := newDesktopWorkflowHarness(t)
			initial := h.start(`{"action":{"kind":"observe"}}`)
			h.complete(h.next("observe"), "completed", syntheticDesktopCapture(t, "before-input", false))
			assertWorkflowObservation(t, h.result(initial), "before-input", "The synthetic field is empty", "")

			input := h.start(`{"frameId":"before-input","action":{"kind":"type","text":"hello"}}`)
			request := h.next("type")
			if request.FrameID != "before-input" || request.Action.Text != "hello" {
				t.Fatal("input lost observed frame or proposed text")
			}
			// The simulated executor changes its field only on approved input.
			select {
			case <-input:
				t.Fatal("input completed before approval")
			default:
			}
			h.complete(request, "completed", nil)
			observation := h.next("observe")
			if observation.FrameID != "" || observation.RequestID == request.RequestID {
				t.Fatal("post-action observation reused the input request or stale frame")
			}
			finishWorkflowScenario(t, h, scenario, observation)
			result := h.result(input)
			assertWorkflowResult(t, scenario, result)
			if h.relay("poll", "", nil).Pending != nil {
				t.Fatal("workflow queued an unexpected retry")
			}
		})
	}
}

func finishWorkflowScenario(t *testing.T, h *desktopWorkflowHarness, scenario string, observation *computeruse.Request) {
	t.Helper()
	switch scenario {
	case "completed":
		h.complete(observation, "completed", syntheticDesktopCapture(t, "after-input", true))
	case "observation-denied":
		h.complete(observation, "denied", nil)
	case "disconnected":
		h.relay("stop", "", nil)
	}
}

func assertWorkflowResult(t *testing.T, scenario string, result Result) {
	t.Helper()
	if scenario == "completed" {
		assertWorkflowObservation(t, result, "after-input", "The synthetic field contains hello", "completed")
		return
	}
	if !result.IsError || !bytes.Contains([]byte(result.Content), []byte("Input completed, but its effect is unverified. Do not repeat the input.")) {
		t.Fatalf("lost input outcome after %s: %+v", scenario, result)
	}
}

func assertWorkflowObservation(t *testing.T, result Result, frame, analysis, actionStatus string) {
	t.Helper()
	var out struct {
		FrameID      string `json:"frameId"`
		Analysis     string `json:"analysis"`
		ActionStatus string `json:"actionStatus"`
	}
	if result.IsError || json.Unmarshal([]byte(result.Content), &out) != nil || out.FrameID != frame || out.Analysis != analysis || out.ActionStatus != actionStatus {
		t.Fatalf("unexpected observation: %+v", result)
	}
	if bytes.Contains([]byte(result.Content), []byte("data:image")) {
		t.Fatal("screenshot leaked into ordinary tool result")
	}
}

// The extended action surface (click variants, hover, drag, positioned scroll,
// hotkeys) must reach the desktop with its fields intact and without the
// model's verification question, which belongs to the follow-up observation.
func TestComputerUseExtendedActionsReachDesktop(t *testing.T) {
	for _, tc := range []struct{ name, raw, wantKind, wantJSON string }{
		{"right-click", `{"kind":"click","x":10,"y":20,"button":"right","question":"Did the context menu open?"}`, "click", `"button":"right"`},
		{"triple-click", `{"kind":"click","x":10,"y":20,"count":3}`, "click", `"count":3`},
		{"hover", `{"kind":"move","x":5,"y":6}`, "move", `"x":5,"y":6`},
		{"drag", `{"kind":"drag","x":1,"y":2,"toX":30,"toY":40}`, "drag", `"toX":30,"toY":40`},
		{"scroll-at", `{"kind":"scroll","deltaX":0,"deltaY":120,"x":7,"y":8}`, "scroll", `"x":7,"y":8`},
		{"hotkey", `{"kind":"key","key":"Cmd+Shift+Z","question":"Was the edit redone?"}`, "key", `"key":"Cmd+Shift+Z"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newDesktopWorkflowHarness(t)
			var seenPrompt string
			h.tool.vision.AnalyzeFn = func(_ context.Context, _ []byte, _, prompt string) (string, error) {
				seenPrompt = prompt
				return "Visible result", nil
			}
			done := h.start(`{"frameId":"before-input","action":` + tc.raw + `}`)
			request := h.next(tc.wantKind)
			wire, _ := json.Marshal(request.Action)
			if !bytes.Contains(wire, []byte(tc.wantJSON)) || bytes.Contains(wire, []byte("question")) || request.FrameID != "before-input" {
				t.Fatalf("desktop request lost fields or carried the question: %s", wire)
			}
			h.complete(request, "completed", nil)
			observation := h.next("observe")
			h.complete(observation, "completed", syntheticDesktopCapture(t, "after-input", true))
			result := h.result(done)
			if result.IsError || !bytes.Contains([]byte(result.Content), []byte(`"actionStatus":"completed"`)) {
				t.Fatalf("unexpected result: %+v", result)
			}
			var in struct {
				Action struct{ Question string }
			}
			_ = json.Unmarshal([]byte(tc.raw), &in)
			if in.Action.Question != "" && !bytes.Contains([]byte(seenPrompt), []byte(in.Action.Question)) {
				t.Fatalf("follow-up observation ignored the verification question: %q", seenPrompt)
			}
		})
	}
}

// wait pauses agent-side, never queues a desktop request during the pause,
// then requests an ordinary approved observation.
func TestComputerUseWaitThenObserve(t *testing.T) {
	h := newDesktopWorkflowHarness(t)
	started := time.Now()
	done := h.start(`{"action":{"kind":"wait","seconds":1,"question":"Has the spinner gone?"}}`)
	deadline := started.Add(700 * time.Millisecond)
	for time.Now().Before(deadline) {
		if h.relay("poll", "", nil).Pending != nil {
			t.Fatal("wait queued a desktop request before the pause elapsed")
		}
		time.Sleep(50 * time.Millisecond)
	}
	observation := h.next("observe")
	if time.Since(started) < time.Second || observation.FrameID != "" || !bytes.Contains([]byte(observation.Action.Question), []byte("Has the spinner gone?")) {
		t.Fatalf("wait did not pause before a fresh observation: %+v after %v", observation, time.Since(started))
	}
	h.complete(observation, "completed", syntheticDesktopCapture(t, "after-wait", false))
	result := h.result(done)
	assertWorkflowObservation(t, result, "after-wait", "The synthetic field is empty", "")
}
