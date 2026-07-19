package slack

import (
	"encoding/json"
	"strings"
	"testing"

	slackgo "github.com/slack-go/slack"
)

func TestBuildHomePlaceholderView(t *testing.T) {
	blocks := BuildHomePlaceholderView("my-agent", "", "")
	if len(blocks) != 2 {
		t.Fatalf("len(blocks) = %d, want 2", len(blocks))
	}
	header, ok := blocks[0].(*slackgo.HeaderBlock)
	if !ok {
		t.Fatalf("first block = %T, want *HeaderBlock", blocks[0])
	}
	if !strings.Contains(header.Text.Text, "my-agent") {
		t.Fatalf("header %q does not mention the agent name", header.Text.Text)
	}
	if !hasContextContaining(blocks, "dashboard") {
		t.Fatal("expected a context block pointing at the dashboard")
	}
}

func TestBuildHomePlaceholderViewDefaultsName(t *testing.T) {
	blocks := BuildHomePlaceholderView("  ", "", "")
	header, ok := blocks[0].(*slackgo.HeaderBlock)
	if !ok {
		t.Fatalf("first block = %T, want *HeaderBlock", blocks[0])
	}
	if !strings.Contains(header.Text.Text, "Slack agent") {
		t.Fatalf("header %q missing default name", header.Text.Text)
	}
}

func TestBuildHomePlaceholderViewCustomCopy(t *testing.T) {
	blocks := BuildHomePlaceholderView("my-agent", "Ops Butler", "Ping *#ops* for anything urgent.")
	header, ok := blocks[0].(*slackgo.HeaderBlock)
	if !ok {
		t.Fatalf("first block = %T, want *HeaderBlock", blocks[0])
	}
	if header.Text.Text != "Ops Butler" {
		t.Fatalf("header = %q, want custom header", header.Text.Text)
	}
	if !hasContextContaining(blocks, "Ping *#ops* for anything urgent.") {
		t.Fatal("expected the custom info line")
	}
	if hasContextContaining(blocks, "dashboard") {
		t.Fatal("default info line should be replaced by the custom one")
	}
}

func TestBuildHomePlaceholderViewTruncatesLongCopy(t *testing.T) {
	blocks := BuildHomePlaceholderView("my-agent", strings.Repeat("h", 200), strings.Repeat("t", 4000))
	header := blocks[0].(*slackgo.HeaderBlock)
	if n := len([]rune(header.Text.Text)); n != homeHeaderMaxRunes {
		t.Fatalf("header runes = %d, want %d", n, homeHeaderMaxRunes)
	}
	if !strings.HasSuffix(header.Text.Text, "…") {
		t.Fatalf("truncated header %q missing ellipsis", header.Text.Text)
	}
	ctx := blocks[1].(*slackgo.ContextBlock)
	txt := ctx.ContextElements.Elements[0].(*slackgo.TextBlockObject)
	if n := len([]rune(txt.Text)); n != homeTextMaxRunes {
		t.Fatalf("text runes = %d, want %d", n, homeTextMaxRunes)
	}
}

// The App Home is visible to any workspace member who opens the app, so the
// default placeholder must stay static: no status, monitoring state, or draft
// content.
func TestBuildHomePlaceholderViewCarriesNoAgentState(t *testing.T) {
	raw, err := json.Marshal(BuildHomePlaceholderView("my-agent", "", ""))
	if err != nil {
		t.Fatalf("marshal blocks: %v", err)
	}
	view := strings.ToLower(string(raw))
	for _, banned := range []string{"pending", "sent", "dismissed", "draft", "monitoring", "connected", "disconnected", "quiet hours"} {
		if strings.Contains(view, banned) {
			t.Fatalf("placeholder view leaks agent state: contains %q in %s", banned, raw)
		}
	}
}

func hasContextContaining(blocks []slackgo.Block, want string) bool {
	for _, b := range blocks {
		if cb, ok := b.(*slackgo.ContextBlock); ok {
			for _, el := range cb.ContextElements.Elements {
				if txt, ok := el.(*slackgo.TextBlockObject); ok && strings.Contains(txt.Text, want) {
					return true
				}
			}
		}
	}
	return false
}
