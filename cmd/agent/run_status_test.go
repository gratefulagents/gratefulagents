package main

import (
	"testing"

	"github.com/gratefulagents/gratefulagents/internal/store/sessionclient"
)

// The progress loop must not rewrite unchanged metrics every tick: each
// write is a session UPDATE + NOTIFY that wakes this pod's own pollers. Only
// changed payloads, failed-write retries, and the final write go through.
func TestProgressMetricsGateSkipsUnchangedWrites(t *testing.T) {
	var gate progressMetricsGate
	first := sessionclient.SessionMetrics{CostUSD: 0.5, InputTokens: 100, OutputTokens: 20, ToolCallCount: 3}

	if !gate.shouldWrite(first, false) {
		t.Fatal("first write must go through")
	}
	gate.recordWritten(first)
	if gate.shouldWrite(first, false) {
		t.Fatal("identical metrics must be skipped")
	}
	if !gate.shouldWrite(first, true) {
		t.Fatal("final write must go through even when unchanged")
	}

	changed := first
	changed.ContextTokens = 4096
	if !gate.shouldWrite(changed, false) {
		t.Fatal("changed context usage must be written")
	}
	// A failed write is not recorded, so the same payload is retried.
	if !gate.shouldWrite(changed, false) {
		t.Fatal("unrecorded (failed) write must be retried")
	}
	gate.recordWritten(changed)
	if gate.shouldWrite(changed, false) {
		t.Fatal("recorded metrics must be skipped")
	}
}
