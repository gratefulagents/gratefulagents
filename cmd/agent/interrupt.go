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

// turnInterruptMinPollGap bounds how often push wake-ups may trigger a
// ConsumeInterrupt query. Every session UPDATE (metrics, activity, working
// state — many of them this pod's own writes) fans out as a wake-up; without
// the gap a chatty turn turns the watcher into a tight query loop. Wake-ups
// arriving inside the gap collapse into one poll at its end, so a stop is
// still honored within the gap plus one query.
const turnInterruptMinPollGap = 250 * time.Millisecond

// turnInterruptPollTimeout bounds one ConsumeInterrupt query so a stalled
// store connection cannot wedge the watcher (and with it the user's ability
// to stop the turn) indefinitely.
const turnInterruptPollTimeout = 5 * time.Second

// turnInterruptWatcher polls the Postgres session for a user interrupt request
// while a turn is in flight and cancels the turn context when one arrives,
// aborting the in-flight model call and any running tools.
type turnInterruptWatcher struct {
	interrupted atomic.Bool
	stopOnce    sync.Once
	stop        chan struct{}
	done        chan struct{}
}

// startTurnInterruptWatcher launches the watcher goroutine. ctx must be the
// run's root context (pod lifetime), not the turn context, so polling
// survives the turn cancellation it triggers.
func startTurnInterruptWatcher(
	ctx context.Context, sc *sessionclient.Client, cancelTurn context.CancelFunc,
) *turnInterruptWatcher {
	w := &turnInterruptWatcher{
		stop: make(chan struct{}),
		done: make(chan struct{}),
	}
	go func() {
		defer close(w.done)
		ticker := time.NewTicker(turnInterruptPollInterval)
		defer ticker.Stop()
		var lastPoll time.Time
		for {
			// Debounce: a burst of wake-ups yields a single poll once the gap
			// has elapsed. The ticker is unaffected (it fires at 1s ≫ gap).
			if wait := interruptPollDelay(lastPoll, time.Now()); wait > 0 {
				timer := time.NewTimer(wait)
				select {
				case <-ctx.Done():
					timer.Stop()
					return
				case <-w.stop:
					timer.Stop()
					return
				case <-timer.C:
				}
			}
			// Subscribe before the consume check so a stop landing in between
			// still wakes the next iteration instantly. With a push-capable
			// store (Postgres LISTEN/NOTIFY) interrupts cancel the turn within
			// milliseconds; the 1s ticker remains the correctness backstop.
			wake := sc.SubscribeSessionEvents()
			lastPoll = time.Now()
			pollCtx, cancelPoll := context.WithTimeout(ctx, turnInterruptPollTimeout)
			req, err := sc.ConsumeInterrupt(pollCtx)
			cancelPoll()
			if err != nil {
				log.Printf("WARN: interrupt watcher poll failed: %v", err)
			} else if req != nil {
				log.Printf("Interrupt requested by %q — cancelling in-flight turn", req.RequestedBy)
				w.interrupted.Store(true)
				cancelTurn()
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-w.stop:
				return
			case <-ticker.C:
			case <-wake: // nil channel blocks forever: pure polling
			}
		}
	}()
	return w
}

// interruptPollDelay is how long the watcher must still wait before its next
// ConsumeInterrupt query so consecutive polls are at least
// turnInterruptMinPollGap apart. Zero when no poll has happened yet.
func interruptPollDelay(lastPoll, now time.Time) time.Duration {
	if lastPoll.IsZero() {
		return 0
	}
	if wait := turnInterruptMinPollGap - now.Sub(lastPoll); wait > 0 {
		return wait
	}
	return 0
}

// Finish stops the watcher, waits for it to exit, and reports whether it
// claimed an interrupt request for this turn.
func (w *turnInterruptWatcher) Finish() bool {
	w.stopOnce.Do(func() { close(w.stop) })
	<-w.done
	return w.interrupted.Load()
}

// interruptAppliesToMessage reports whether a durable stop request targets
// this message: a stop requested at or after the message was sent means
// "stop before starting this turn". A newer user message is an explicit
// resume and makes an older idle-gap stop stale. Callers drain the pending
// stops through now (not through the message time) and then decide here —
// draining only through the message time would consume exactly the stale
// stops and leave the applicable ones pending.
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
