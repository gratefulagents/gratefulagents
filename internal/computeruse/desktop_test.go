package computeruse

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestDesktopMigrationRejectsLegacyConsent(t *testing.T) {
	b := New("ns", "run")
	t.Cleanup(func() {
		if err := b.Close(); err != nil {
			t.Errorf("close broker: %v", err)
		}
	})
	for _, op := range []string{"attach", "attach_agent"} {
		_, err := b.Exchange(Exchange{Namespace: "ns", Run: "run", Owner: "alice", SessionID: "old", Operation: op})
		if !errors.Is(err, ErrLegacyScope) || b.Active() {
			t.Fatalf("legacy %s: %v", op, err)
		}
	}
	for _, mode := range []string{"", "selected_window", "agent_choice"} {
		if !errors.Is((Response{Mode: mode}).Validate(), ErrLegacyScope) {
			t.Fatal(mode)
		}
	}
	for _, kind := range []string{"list_windows", "select_window", "activate"} {
		if !errors.Is((Action{Kind: kind}).Validate(), ErrLegacyScope) {
			t.Fatal(kind)
		}
	}
	var scope Exchange
	if Decode(strings.NewReader(`{"namespace":"ns","run":"run","owner":"alice","sessionId":"s","operation":"attach_desktop","windowId":1}`), &scope) == nil {
		t.Fatal("accepted legacy payload")
	}
}
func TestDesktopInputRequiresLatestObservation(t *testing.T) {
	b, _ := attached(t)
	for _, action := range []Action{{Kind: "key", Key: "Cmd+Tab"}, {Kind: "type", Text: "hello"}, {Kind: "open_url", URL: "https://example.com"}} {
		if _, err := b.Request(context.Background(), action, "missing"); !errors.Is(err, ErrStaleFrame) {
			t.Fatal(err)
		}
	}
}
func TestDesktopKeyboardPermitsAppSwitchingButReservesStop(t *testing.T) {
	for _, key := range []string{"Cmd+Tab", "Cmd+W", "Cmd+Q", "Cmd+Space", "Control+ArrowLeft"} {
		if _, err := ParseHotkey(key); err != nil {
			t.Fatal(key, err)
		}
	}
	for _, key := range []string{"Control+Option+Cmd+Escape", "Option+Cmd+Escape"} {
		if _, err := ParseHotkey(key); err == nil {
			t.Fatal(key)
		}
	}
}
