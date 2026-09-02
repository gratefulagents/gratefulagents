package main

import (
	"testing"
	"time"
)

// Push wake-ups can arrive far faster than the watcher should query the
// store; polls closer together than turnInterruptMinPollGap are deferred to
// the end of the gap, while the very first poll and any poll past the gap run
// immediately.
func TestInterruptPollDelayDebouncesWakeUps(t *testing.T) {
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)

	if got := interruptPollDelay(time.Time{}, now); got != 0 {
		t.Fatalf("first poll delay = %v, want 0", got)
	}
	if got := interruptPollDelay(now.Add(-50*time.Millisecond), now); got != turnInterruptMinPollGap-50*time.Millisecond {
		t.Fatalf("delay 50ms after a poll = %v, want %v", got, turnInterruptMinPollGap-50*time.Millisecond)
	}
	if got := interruptPollDelay(now.Add(-turnInterruptMinPollGap), now); got != 0 {
		t.Fatalf("delay exactly one gap after a poll = %v, want 0", got)
	}
	if got := interruptPollDelay(now.Add(-turnInterruptPollInterval), now); got != 0 {
		t.Fatalf("delay one ticker interval after a poll = %v, want 0 (ticker must not be throttled)", got)
	}
}
