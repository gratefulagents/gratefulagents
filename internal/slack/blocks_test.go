package slack

import (
	"strings"
	"testing"

	slackgo "github.com/slack-go/slack"
)

func TestBuildDraftEditModalCarriesDraft(t *testing.T) {
	view := BuildDraftEditModal("draft-42", "original text")
	if view.CallbackID != CallbackDraftEditModal {
		t.Fatalf("callback = %q, want %q", view.CallbackID, CallbackDraftEditModal)
	}
	if view.PrivateMetadata != "draft-42" {
		t.Fatalf("private metadata = %q, want draft-42", view.PrivateMetadata)
	}
	if len(view.Blocks.BlockSet) != 1 {
		t.Fatalf("got %d blocks, want 1", len(view.Blocks.BlockSet))
	}
	input, ok := view.Blocks.BlockSet[0].(*slackgo.InputBlock)
	if !ok {
		t.Fatalf("block is %T, want *slack.InputBlock", view.Blocks.BlockSet[0])
	}
	el, ok := input.Element.(*slackgo.PlainTextInputBlockElement)
	if !ok {
		t.Fatalf("element is %T, want *slack.PlainTextInputBlockElement", input.Element)
	}
	if el.InitialValue != "original text" {
		t.Fatalf("initial value = %q, want prefilled draft", el.InitialValue)
	}
}

func TestTruncateForBlock(t *testing.T) {
	short := "hello"
	if got := truncateForBlock(short); got != short {
		t.Errorf("truncateForBlock(short) = %q, want unchanged", got)
	}
	long := strings.Repeat("x", 5000)
	got := truncateForBlock(long)
	if len(got) > 2900+len("…") {
		t.Errorf("truncateForBlock(long) len = %d, want <= %d", len(got), 2900+len("…"))
	}
	if len(got) >= 3000 {
		t.Errorf("truncateForBlock(long) len = %d, exceeds Slack block limit", len(got))
	}
	if !strings.HasSuffix(got, "…") {
		t.Error("truncated text should end with ellipsis")
	}
}

func TestBuildChannelReplyApprovalBlocks(t *testing.T) {
	blocks := BuildChannelReplyApprovalBlocks(
		"draft-456", "<@UBOB>", "C0TEAM", "what changed in the release?", "We shipped the new deploy pipeline.")
	if len(blocks) != 4 {
		t.Fatalf("got %d blocks, want 4", len(blocks))
	}

	header, ok := blocks[0].(*slackgo.SectionBlock)
	if !ok || header.Text == nil {
		t.Fatalf("first block is %T, want *slack.SectionBlock with text", blocks[0])
	}
	for _, want := range []string{"<@UBOB>", "<#C0TEAM>"} {
		if !strings.Contains(header.Text.Text, want) {
			t.Errorf("header %q missing %q", header.Text.Text, want)
		}
	}

	// Approval actions must carry the draft ID and reuse the shared action IDs
	// so the connector's one dispatcher handles both draft kinds.
	action, ok := blocks[len(blocks)-1].(*slackgo.ActionBlock)
	if !ok {
		t.Fatalf("last block is %T, want *slack.ActionBlock", blocks[len(blocks)-1])
	}
	if action.Elements == nil || len(action.Elements.ElementSet) != 4 {
		t.Fatalf("want 4 action elements, got %v", action.Elements)
	}
	found := map[string]bool{}
	for _, el := range action.Elements.ElementSet {
		btn, ok := el.(*slackgo.ButtonBlockElement)
		if !ok {
			t.Fatalf("action element is %T, want *slack.ButtonBlockElement", el)
		}
		if btn.Value != "draft-456" {
			t.Errorf("button value = %q, want draft-456", btn.Value)
		}
		found[btn.ActionID] = true
	}
	for _, id := range []string{ActionDraftApprove, ActionDraftEdit, ActionDraftRegen, ActionDraftDismiss} {
		if !found[id] {
			t.Errorf("missing action %s", id)
		}
	}
}
