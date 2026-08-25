package main

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestBudgetGuardSoftStopsAtCapOnLLMStart(t *testing.T) {
	t.Parallel()
	var cancelled atomic.Bool
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	g := startTurnBudgetGuardWithInterval(ctx, 5.0, 5.0, nil, func() { cancelled.Store(true) }, time.Hour)
	defer g.Finish()

	g.OnLLMStart(nil, nil)
	if !cancelled.Load() {
		t.Fatal("OnLLMStart at the cap must cancel the turn")
	}
	if !g.Finish() {
		t.Fatal("guard should report the budget trip")
	}
	if notice := g.notice(); !strings.Contains(notice, "$5.00 limit") {
		t.Fatalf("notice = %q, want it to mention the $5.00 limit", notice)
	}
}

func TestBudgetGuardUnderCapDoesNotTrip(t *testing.T) {
	t.Parallel()
	var cancelled atomic.Bool
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	g := startTurnBudgetGuardWithInterval(ctx, 1.0, 5.0, nil, func() { cancelled.Store(true) }, time.Hour)

	g.OnLLMStart(nil, nil)
	if cancelled.Load() {
		t.Fatal("OnLLMStart under the cap must not cancel the turn")
	}
	if g.Finish() {
		t.Fatal("guard must not report a trip under the cap")
	}
}

func TestBudgetGuardHardStopsPastOvershootMargin(t *testing.T) {
	t.Parallel()
	cancelledCh := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Baseline 10 vs cap 5: well past cap*(1+margin); the watcher alone must
	// trip without any LLM-start boundary.
	g := startTurnBudgetGuardWithInterval(ctx, 10.0, 5.0, nil, func() { close(cancelledCh) }, 5*time.Millisecond)

	select {
	case <-cancelledCh:
	case <-time.After(5 * time.Second):
		t.Fatal("hard-stop watcher did not cancel the turn past the overshoot margin")
	}
	if !g.Finish() {
		t.Fatal("guard should report the hard-stop trip")
	}
}

func TestBudgetGuardWatcherStaysQuietWithinMargin(t *testing.T) {
	t.Parallel()
	var cancelled atomic.Bool
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Spend is exactly at the cap: soft-stop territory, but below the hard
	// threshold — the watcher must not fire.
	g := startTurnBudgetGuardWithInterval(ctx, 5.0, 5.0, nil, func() { cancelled.Store(true) }, time.Millisecond)
	time.Sleep(50 * time.Millisecond)
	if g.Finish() || cancelled.Load() {
		t.Fatal("watcher must not hard-stop at spend == cap (within the overshoot margin)")
	}
}
