package main

import (
	"context"
	"testing"
	"time"

	internalslack "github.com/gratefulagents/gratefulagents/internal/slack"
)

func TestMergeDecisionsSingle(t *testing.T) {
	d := internalslack.Decision{ChannelID: "D1", Text: "just one", ThreadTS: "1.1", MessageTS: "1.1"}
	got := mergeDecisions([]internalslack.Decision{d})
	if got.Text != "just one" {
		t.Fatalf("single merge changed text: %q", got.Text)
	}
}

func TestMergeDecisionsBurst(t *testing.T) {
	burst := []internalslack.Decision{
		{ChannelID: "D1", ChannelType: "im", Text: "whats the time in japan", ThreadTS: "1.1", MessageTS: "1.1", UserID: "UA"},
		{ChannelID: "D1", ChannelType: "im", Text: "and in usa", ThreadTS: "1.1", MessageTS: "1.2", UserID: "UA"},
	}
	got := mergeDecisions(burst)
	if got.Text != "whats the time in japan\nand in usa" {
		t.Fatalf("merged text = %q", got.Text)
	}
	// Reply/reaction targets come from the most recent message.
	if got.MessageTS != "1.2" {
		t.Fatalf("merged MessageTS = %q, want 1.2 (last)", got.MessageTS)
	}
}

func TestMergeDecisionsSkipsEmpty(t *testing.T) {
	burst := []internalslack.Decision{
		{Text: "hello"},
		{Text: "   "},
		{Text: "world"},
	}
	if got := mergeDecisions(burst); got.Text != "hello\nworld" {
		t.Fatalf("merged text = %q, want hello\\nworld", got.Text)
	}
}

func TestConversationQueueKey(t *testing.T) {
	// DMs key by channel (threads ignored); channels key by thread.
	dm := internalslack.Decision{ChannelType: "im", ChannelID: "DALICE", ThreadTS: "9.9"}
	if got := conversationQueueKey("agent", dm); got != "agent|DALICE|" {
		t.Fatalf("dm key = %q, want agent|DALICE|", got)
	}
	ch := internalslack.Decision{ChannelType: "channel", ChannelID: "CTEAM", ThreadTS: "5.5"}
	if got := conversationQueueKey("agent", ch); got != "agent|CTEAM|5.5" {
		t.Fatalf("channel key = %q, want agent|CTEAM|5.5", got)
	}
}

func TestCoalesceDrainsBurst(t *testing.T) {
	o := &slackOrchestrator{batchWindow: 40 * time.Millisecond}
	q := &convQueue{ch: make(chan internalslack.Decision, convQueueBuffer)}
	// Two more messages already waiting when coalescing starts.
	q.ch <- internalslack.Decision{Text: "b"}
	q.ch <- internalslack.Decision{Text: "c"}

	batch := o.coalesce(context.Background(), q, internalslack.Decision{Text: "a"})
	if len(batch) != 3 {
		t.Fatalf("batch len = %d, want 3 (a,b,c coalesced)", len(batch))
	}
	if merged := mergeDecisions(batch); merged.Text != "a\nb\nc" {
		t.Fatalf("merged burst = %q, want a\\nb\\nc", merged.Text)
	}
}

func TestCoalesceStopsAfterWindow(t *testing.T) {
	o := &slackOrchestrator{batchWindow: 30 * time.Millisecond}
	q := &convQueue{ch: make(chan internalslack.Decision, convQueueBuffer)}

	start := time.Now()
	batch := o.coalesce(context.Background(), q, internalslack.Decision{Text: "solo"})
	if len(batch) != 1 {
		t.Fatalf("batch len = %d, want 1", len(batch))
	}
	if elapsed := time.Since(start); elapsed < 25*time.Millisecond {
		t.Fatalf("coalesce returned too early (%s); should wait ~the window", elapsed)
	}
}

func TestMergeDecisionsCarriesFiles(t *testing.T) {
	merged := mergeDecisions([]internalslack.Decision{
		{Text: "look at this", Files: []internalslack.File{{ID: "F1", Name: "a.txt"}}},
		{Text: "and this", Files: []internalslack.File{{ID: "F2", Name: "b.log"}}},
	})
	if merged.Text != "look at this\nand this" {
		t.Fatalf("merged text = %q", merged.Text)
	}
	if len(merged.Files) != 2 || merged.Files[0].ID != "F1" || merged.Files[1].ID != "F2" {
		t.Fatalf("merged files = %+v, want both files in order", merged.Files)
	}
}
