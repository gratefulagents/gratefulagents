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
	for _, key := range []string{"Tab", "Shift+Tab"} {
		if (Action{Kind: "key", Key: key}).Validate() != nil {
			t.Errorf("rejected %s", key)
		}
	}
	for _, key := range []string{"Shift+Enter", "Ctrl+Tab", "Cmd+A", "a"} {
		if (Action{Kind: "key", Key: key}).Validate() == nil {
			t.Errorf("accepted %s", key)
		}
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
