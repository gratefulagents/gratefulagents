package main

import (
	"context"
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gratefulagents/gratefulagents/internal/store/sessionclient"
	agent "github.com/gratefulagents/sdk/pkg/agentsdk"
)

// turnInterruptPollInterval is how often the per-turn watcher checks the
// session for a user stop request while a turn is in flight.
const turnInterruptPollInterval = time.Second

// turnInterruptWatcher polls the Postgres session for a user interrupt
// request while a turn is in flight and cancels the turn context when one
// arrives, aborting the in-flight model call and any running tools.
type turnInterruptWatcher struct {
	interrupted atomic.Bool
	stopOnce    sync.Once
	stop        chan struct{}
	done        chan struct{}
}

// startTurnInterruptWatcher launches the watcher goroutine. ctx must be the
// run's root context (pod lifetime), not the turn context, so DB polling
// survives the turn cancellation it triggers.
func startTurnInterruptWatcher(ctx context.Context, sc *sessionclient.Client, cancelTurn context.CancelFunc) *turnInterruptWatcher {
	w := &turnInterruptWatcher{
		stop: make(chan struct{}),
		done: make(chan struct{}),
	}
	go func() {
		defer close(w.done)
		ticker := time.NewTicker(turnInterruptPollInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-w.stop:
				return
			case <-ticker.C:
				req, err := sc.ConsumeInterrupt(ctx)
				if err != nil {
					log.Printf("WARN: interrupt watcher poll failed: %v", err)
					continue
				}
				if req == nil {
					continue
				}
				log.Printf("Interrupt requested by %q — cancelling in-flight turn", req.RequestedBy)
				w.interrupted.Store(true)
				cancelTurn()
				return
			}
		}
	}()
	return w
}

// Finish stops the watcher, waits for it to exit, and reports whether it
// claimed an interrupt request for this turn.
func (w *turnInterruptWatcher) Finish() bool {
	w.stopOnce.Do(func() { close(w.stop) })
	<-w.done
	return w.interrupted.Load()
}

// interruptAppliesToMessage reports whether a durable stop request targets
// this message. A newer user message is an explicit resume and makes an older
// idle-gap stop stale.
func interruptAppliesToMessage(req *sessionclient.InterruptRequest, messageCreatedAt time.Time) bool {
	if req == nil {
		return false
	}
	if messageCreatedAt.IsZero() {
		return true
	}
	return !req.RequestedAt.Before(messageCreatedAt)
}

// turnInterruptNotice is the user-facing activity summary for an interrupted
// turn.
func turnInterruptNotice(cancelledSubAgents int) string {
	if cancelledSubAgents == 1 {
		return "Stopped by user — interrupted the current turn and 1 sub-agent task; send a message to continue."
	}
	if cancelledSubAgents > 1 {
		return fmt.Sprintf("Stopped by user — interrupted the current turn and %d sub-agent tasks; send a message to continue.", cancelledSubAgents)
	}
	return "Stopped by user — interrupted the current turn; send a message to continue."
}

// cancelActiveSubAgentTasks cancels every non-terminal managed sub-agent task
// so a user interrupt stops background workers along with the main turn.
// Sub-agent tasks run on independent contexts that survive turn cancellation,
// which is why they must be cancelled explicitly here.
func cancelActiveSubAgentTasks(registry *agent.SubAgentScheduler) int {
	if registry == nil {
		return 0
	}
	cancelled := 0
	for _, task := range registry.ListTasks() {
		if task == nil || task.IsTerminal() {
			continue
		}
		if err := registry.Cancel(task.ID); err != nil {
			log.Printf("WARN: failed to cancel sub-agent task %s: %v", task.ID, err)
			continue
		}
		cancelled++
	}
	return cancelled
}
