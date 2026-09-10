package computeruse

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"
)

func TestClaimTimeoutFor(t *testing.T) {
	for kind, want := range map[string]time.Duration{"observe": ObserveClaimTimeout, "type": TypeClaimTimeout, "click": ClaimTimeout, "scroll": ClaimTimeout, "key": ClaimTimeout, "activate": ClaimTimeout} {
		if got := ClaimTimeoutFor(Action{Kind: kind}); got != want {
			t.Errorf("%s: %v, want %v", kind, got, want)
		}
	}
	if ObserveClaimTimeout >= RequestTimeout || TypeClaimTimeout >= RequestTimeout || ClaimTimeout >= ObserveClaimTimeout {
		t.Fatal("claim timeouts must stay bounded below the request timeout")
	}
}

func TestProposedTextValidation(t *testing.T) {
	for _, tc := range []struct {
		name  string
		text  string
		valid bool
	}{
		{"missing", "", false},
		{"one", "a", true},
		{"spaces", " ", true},
		{"ascii-limit", strings.Repeat("a", 1000), true},
		{"ascii-over", strings.Repeat("a", 1001), false},
		{"bmp-limit", strings.Repeat("界", 1000), true},
		{"bmp-over", strings.Repeat("界", 1001), false},
		{"supplementary-limit", strings.Repeat("😀", 500), true},
		{"supplementary-over", strings.Repeat("😀", 501), false},
		{"mixed-limit", "ab" + strings.Repeat("😀", 499), true},
		{"mixed-over", "abc" + strings.Repeat("😀", 499), false},
		{"invalid-utf8", "\xff", false},
		{"wire-sized", strings.Repeat("a", 4001), false},
		{"nbsp", "a\u00a0b", true},
		{"ideographic-space", "a\u3000b", true},
		{"zero-width-joiner", "a\u200db", true},
		{"zero-width-non-joiner", "a\u200cb", true},
		{"emoji-zwj-sequence", "\U0001F468\u200d\U0001F469\u200d\U0001F467", true},
		{"zero-width-space", "a\u200bb", false},
		{"bom", "\ufeffa", false},
		{"rtl-override", "abc\u202efed", false},
		{"isolate", "a\u2067b\u2069", false},
		{"line-separator", "a\u2028b", false},
		{"paragraph-separator", "a\u2029b", false},
		{"soft-hyphen", "a\u00adb", false},
		{"private-use", "a\ue000b", false},
		{"unassigned", "a\U000E0080b", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := (Action{Kind: "type", Text: tc.text}).Validate(); (got == nil) != tc.valid {
				t.Fatalf("valid=%v: %v", tc.valid, got)
			}
		})
	}
	for r := rune(0); r <= 0x9f; r++ {
		if r > 0x1f && r < 0x7f {
			continue
		}
		if (Action{Kind: "type", Text: "a" + string(r)}).Validate() == nil {
			t.Errorf("accepted control U+%04X", r)
		}
	}
	n := 1.0
	for _, a := range []Action{
		{Kind: "observe", Text: "private"},
		{Kind: "activate", Text: "private"},
		{Kind: "click", X: &n, Y: &n, Text: "private"},
		{Kind: "scroll", DeltaX: &n, DeltaY: &n, Text: "private"},
		{Kind: "key", Key: "Tab", Text: "private"},
		{Kind: "type", Text: "private", X: &n},
		{Kind: "type", Text: "private", Y: &n},
		{Kind: "type", Text: "private", DeltaX: &n},
		{Kind: "type", Text: "private", DeltaY: &n},
		{Kind: "type", Text: "private", Key: "Tab"},
		{Kind: "type", Text: "private", Question: "question"},
	} {
		if a.Validate() == nil {
			t.Errorf("accepted incompatible fields for %s", a.Kind)
		}
	}
}

func TestPendingProposedTextRoundTrip(t *testing.T) {
	want := Response{Active: true, Pending: &Request{RequestID: "request", FrameID: "frame", Action: Action{Kind: "type", Text: "proposed 界😀"}}}
	raw, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	var got Response
	if err := Decode(strings.NewReader(string(raw)), &got); err != nil {
		t.Fatal(err)
	}
	if got.Validate() != nil || got.Pending == nil || got.Pending.Action.Text != want.Pending.Action.Text {
		t.Fatal("pending action did not preserve valid proposed text")
	}
	got.Pending.Action.Text = ""
	if got.Validate() == nil {
		t.Fatal("accepted pending type without text")
	}
}

func TestNativeKeyAndScrollValidation(t *testing.T) {
	for _, key := range []string{"Tab", "Shift+Tab", "Shift+Enter", "Space", "Cmd+A", "Cmd+Shift+Z", "Shift+Cmd+z", "Option+ArrowLeft", "Ctrl+A", "Alt+Backspace", "Command+S", "Cmd+1", "Cmd+Shift+7"} {
		if (Action{Kind: "key", Key: key}).Validate() != nil {
			t.Errorf("rejected %s", key)
		}
	}
	for _, key := range []string{
		"a", "A", "1", "Shift+A", "Shift+1", // bare letters/digits would bypass proposed-text review
		"Cmd+Cmd+A", "Cmd+", "+A", "Cmd+Shift", "Cmd+AB", "Cmd+F1", "Cmd+,", "Cmd+`", "Meta+Space", "Fn+A", "cmd+a",
		"Cmd+Q", "Cmd+Shift+Q", "Control+Cmd+Q", "Cmd+W", "Cmd+Shift+W", "Cmd+H", "Cmd+Option+H", "Cmd+M", "Cmd+Tab", "Cmd+Shift+Tab", "Cmd+Space", "Cmd+Option+Escape", "Control+Option+Cmd+Escape",
		"Cmd+Shift+3", "Cmd+Shift+4", "Cmd+Shift+5", "Cmd+Shift+6", "Cmd+Option+D", "Control+Cmd+F", "Control+ArrowLeft", "Control+Shift+ArrowUp", "Control+Space",
	} {
		if (Action{Kind: "key", Key: key}).Validate() == nil {
			t.Errorf("accepted %s", key)
		}
	}
	if h, err := ParseHotkey("Shift+Cmd+z"); err != nil || h.String() != "Shift+Cmd+Z" || !h.Shift || !h.Cmd || h.Control || h.Option {
		t.Fatalf("hotkey did not canonicalize: %+v %v", h, err)
	}
	for _, tc := range []struct {
		x, y  float64
		valid bool
	}{
		{0, 1, true}, {1, 0, true}, {-1000, 1000, true},
		{0, 0, false}, {1001, 0, false}, {0, -1001, false},
		{0.5, 1, false}, {1, -0.5, false}, {math.NaN(), 1, false}, {1, math.Inf(1), false},
	} {
		if got := (Action{Kind: "scroll", DeltaX: &tc.x, DeltaY: &tc.y}).Validate(); (got == nil) != tc.valid {
			t.Errorf("scroll (%v,%v): valid=%v, err=%v", tc.x, tc.y, tc.valid, got)
		}
	}
	n := 1.0
	for _, a := range []Action{{Kind: "scroll"}, {Kind: "scroll", DeltaX: &n}, {Kind: "scroll", DeltaY: &n}} {
		if a.Validate() == nil {
			t.Error("accepted missing scroll axis")
		}
	}
}

func TestPointerActionValidation(t *testing.T) {
	n, z, big := 10.0, 0.0, 100001.0
	one, two, three, four := 1, 2, 3, 4
	for _, tc := range []struct {
		name  string
		a     Action
		valid bool
	}{
		{"left-click", Action{Kind: "click", X: &n, Y: &n}, true},
		{"right-click", Action{Kind: "click", X: &n, Y: &n, Button: "right"}, true},
		{"middle-click", Action{Kind: "click", X: &n, Y: &n, Button: "middle", Count: &one}, true},
		{"double-click", Action{Kind: "click", X: &n, Y: &n, Count: &two}, true},
		{"triple-click", Action{Kind: "click", X: &n, Y: &n, Button: "left", Count: &three}, true},
		{"quadruple-click", Action{Kind: "click", X: &n, Y: &n, Count: &four}, false},
		{"zero-click", Action{Kind: "click", X: &n, Y: &n, Count: new(int)}, false},
		{"unknown-button", Action{Kind: "click", X: &n, Y: &n, Button: "back"}, false},
		{"click-missing-y", Action{Kind: "click", X: &n}, false},
		{"click-out-of-range", Action{Kind: "click", X: &big, Y: &n}, false},
		{"click-with-question", Action{Kind: "click", X: &n, Y: &n, Question: "done?"}, false},
		{"move", Action{Kind: "move", X: &n, Y: &n}, true},
		{"move-with-button", Action{Kind: "move", X: &n, Y: &n, Button: "left"}, false},
		{"move-missing-point", Action{Kind: "move"}, false},
		{"drag", Action{Kind: "drag", X: &n, Y: &n, ToX: &z, ToY: &n}, true},
		{"drag-same-point", Action{Kind: "drag", X: &n, Y: &n, ToX: &n, ToY: &n}, false},
		{"drag-missing-destination", Action{Kind: "drag", X: &n, Y: &n}, false},
		{"drag-half-destination", Action{Kind: "drag", X: &n, Y: &n, ToX: &z}, false},
		{"scroll-at-point", Action{Kind: "scroll", DeltaX: &z, DeltaY: &n, X: &n, Y: &n}, true},
		{"scroll-half-point", Action{Kind: "scroll", DeltaX: &z, DeltaY: &n, X: &n}, false},
		{"scroll-with-destination", Action{Kind: "scroll", DeltaX: &z, DeltaY: &n, ToX: &n, ToY: &n}, false},
		{"wait", Action{Kind: "wait", Seconds: &one}, true},
		{"wait-max", Action{Kind: "wait", Seconds: func() *int { s := 10; return &s }()}, true},
		{"wait-too-long", Action{Kind: "wait", Seconds: func() *int { s := 11; return &s }()}, false},
		{"wait-zero", Action{Kind: "wait", Seconds: new(int)}, false},
		{"wait-missing", Action{Kind: "wait"}, false},
		{"wait-with-point", Action{Kind: "wait", Seconds: &one, X: &n, Y: &n}, false},
		{"seconds-on-observe", Action{Kind: "observe", Seconds: &one}, false},
		{"open-url", Action{Kind: "open_url", URL: "https://example.com/a?b=c#d"}, true},
		{"open-url-http", Action{Kind: "open_url", URL: "http://localhost:8080/"}, true},
		{"open-url-file", Action{Kind: "open_url", URL: "file:///etc/passwd"}, false},
		{"open-url-javascript", Action{Kind: "open_url", URL: "javascript:alert(1)"}, false},
		{"open-url-relative", Action{Kind: "open_url", URL: "example.com"}, false},
		{"open-url-credentials", Action{Kind: "open_url", URL: "https://user:pw@example.com"}, false},
		{"open-url-space", Action{Kind: "open_url", URL: "https://example.com/a b"}, false},
		{"open-url-control", Action{Kind: "open_url", URL: "https://example.com/\n"}, false},
		{"open-url-empty", Action{Kind: "open_url"}, false},
		{"open-url-long", Action{Kind: "open_url", URL: "https://example.com/" + strings.Repeat("a", 2048)}, false},
		{"open-url-with-point", Action{Kind: "open_url", URL: "https://example.com", X: &n, Y: &n}, false},
		{"url-on-click", Action{Kind: "click", X: &n, Y: &n, URL: "https://example.com"}, false},
		{"destination-on-click", Action{Kind: "click", X: &n, Y: &n, ToX: &z, ToY: &z}, false},
		{"key-with-point", Action{Kind: "key", Key: "Enter", X: &n, Y: &n}, false},
		{"activate-with-point", Action{Kind: "activate", X: &n, Y: &n}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.a.Validate(); (got == nil) != tc.valid {
				t.Fatalf("valid=%v: %v", tc.valid, got)
			}
		})
	}
	for _, kind := range []string{"observe", "wait"} {
		if (Action{Kind: kind}).IsInput() {
			t.Errorf("%s reported as input", kind)
		}
	}
	for _, kind := range []string{"click", "move", "drag", "scroll", "key", "type", "activate", "open_url"} {
		if !(Action{Kind: kind}).IsInput() {
			t.Errorf("%s not reported as input", kind)
		}
	}
	for _, kind := range []string{"observe", "wait", "open_url"} {
		if (Action{Kind: kind}).NeedsFrame() {
			t.Errorf("%s should not need a frame", kind)
		}
	}
	for _, kind := range []string{"click", "move", "drag", "scroll", "key", "type", "activate"} {
		if !(Action{Kind: kind}).NeedsFrame() {
			t.Errorf("%s should need a frame", kind)
		}
	}
	one = 1
	raw, _ := json.Marshal(Response{Active: true, Pending: &Request{RequestID: "r", Action: Action{Kind: "wait", Seconds: &one}}})
	var got Response
	if Decode(strings.NewReader(string(raw)), &got) != nil || got.Validate() == nil {
		t.Fatal("accepted a pending wait on the wire")
	}
	var count Action
	if json.Unmarshal([]byte(`{"kind":"click","x":1,"y":1,"count":1.5}`), &count) == nil {
		t.Fatal("accepted fractional click count")
	}
}
