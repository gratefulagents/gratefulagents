package main

import (
	"context"
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"

	agent "github.com/gratefulagents/sdk/pkg/agentsdk"
)

const (
	// budgetWatchInterval is how often the hard-stop watcher re-checks spend
	// while a turn is in flight. It exists for the windows with no LLM-start
	// boundary: a single long model call, or the parent parked in
	// subagent_wait while sub-agents keep spending into the shared tracker.
	budgetWatchInterval = 5 * time.Second

	// budgetHardStopMargin is the overshoot fraction past the configured cost
	// cap at which the watcher hard-cancels the in-flight turn. The soft stop
	// (OnLLMStart, exactly at the cap) is preferred because it fires before
	// the next paid call; the hard stop bounds the worst case when no such
	// boundary arrives.
	budgetHardStopMargin = 0.10
)

// turnBudgetGuard enforces spec.limits.maxCostUsd inside a turn. The pre-turn
// check alone lets one long turn overshoot the cap arbitrarily; this guard
// closes that hole in two layers:
//
//  1. Soft stop: as a RunHooks OnLLMStart hook it cancels the turn at the
//     next model-call boundary once spend reaches the cap — nothing already
//     paid for is discarded.
//  2. Hard stop: a watcher goroutine cancels mid-call once spend exceeds the
//     cap by budgetHardStopMargin, bounding runaway sub-agent fan-out.
type turnBudgetGuard struct {
	agent.NoOpRunHooks

	baselineUSD float64
	capUSD      float64
	tracker     *agent.RunProgress
	cancelTurn  context.CancelFunc

	tripped  atomic.Bool
	spentUSD atomic.Value // float64 recorded when tripped

	stopOnce sync.Once
	stop     chan struct{}
	done     chan struct{}
}

// startTurnBudgetGuard launches the hard-stop watcher and returns the guard,
// which must also be registered as a run hook for the soft stop. ctx must be
// the run's root context so the watcher survives the turn cancellation it
// triggers.
func startTurnBudgetGuard(
	ctx context.Context, baselineUSD, capUSD float64, tracker *agent.RunProgress, cancelTurn context.CancelFunc,
) *turnBudgetGuard {
	return startTurnBudgetGuardWithInterval(ctx, baselineUSD, capUSD, tracker, cancelTurn, budgetWatchInterval)
}

func startTurnBudgetGuardWithInterval(
	ctx context.Context, baselineUSD, capUSD float64, tracker *agent.RunProgress,
	cancelTurn context.CancelFunc, interval time.Duration,
) *turnBudgetGuard {
	g := &turnBudgetGuard{
		baselineUSD: baselineUSD,
		capUSD:      capUSD,
		tracker:     tracker,
		cancelTurn:  cancelTurn,
		stop:        make(chan struct{}),
		done:        make(chan struct{}),
	}
	go func() {
		defer close(g.done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		hardCap := capUSD * (1 + budgetHardStopMargin)
		for {
			select {
			case <-ctx.Done():
				return
			case <-g.stop:
				return
			case <-ticker.C:
				if spent := g.spent(); spent >= hardCap {
					g.trip(spent, "hard")
					return
				}
			}
		}
	}()
	return g
}

func (g *turnBudgetGuard) spent() float64 {
	if g.tracker == nil {
		return g.baselineUSD
	}
	return g.baselineUSD + g.tracker.Snapshot().CostUsd
}

func (g *turnBudgetGuard) trip(spentUSD float64, layer string) {
	if g.tripped.CompareAndSwap(false, true) {
		g.spentUSD.Store(spentUSD)
		log.Printf("Cost cap exceeded mid-turn ($%.4f spent of the $%.2f limit, %s stop) — cancelling in-flight turn",
			spentUSD, g.capUSD, layer)
		g.cancelTurn()
	}
}

// OnLLMStart is the soft stop: it runs immediately before each model call, so
// cancelling here spends nothing beyond what the tracker already recorded.
func (g *turnBudgetGuard) OnLLMStart(_ *agent.RunContext, _ *agent.Agent) {
	if g.tripped.Load() {
		return
	}
	if spent := g.spent(); spent >= g.capUSD {
		g.trip(spent, "soft")
	}
}

// Finish stops the watcher, waits for it to exit, and reports whether the
// guard cancelled the turn for exceeding the cost cap.
func (g *turnBudgetGuard) Finish() bool {
	g.stopOnce.Do(func() { close(g.stop) })
	<-g.done
	return g.tripped.Load()
}

// notice is the user-facing message for a turn stopped by the budget guard.
func (g *turnBudgetGuard) notice() string {
	spent := g.capUSD
	if v, ok := g.spentUSD.Load().(float64); ok {
		spent = v
	}
	return fmt.Sprintf("Cost cap reached mid-turn: $%.4f spent of the $%.2f limit — "+
		"the turn was stopped and its progress preserved. Increase spec.limits.maxCostUsd to resume.", spent, g.capUSD)
}
