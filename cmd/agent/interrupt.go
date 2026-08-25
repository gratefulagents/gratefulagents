package main

import (
	"context"
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/store/sessionclient"
	agent "github.com/gratefulagents/sdk/pkg/agentsdk"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// turnInterruptPollInterval is how often the per-turn watcher checks the
// session for a user stop request while a turn is in flight.
const turnInterruptPollInterval = time.Second

// crdInterruptPollEvery is how many session polls pass between checks of the
// CRD fallback interrupt annotation. The annotation is the degraded-mode
// channel for a session-store outage, so it is polled at a fraction of the
// primary channel's rate.
const crdInterruptPollEvery = 5

// turnInterruptWatcher polls the Postgres session (primary) and the AgentRun
// interrupt annotation (CRD fallback) for a user interrupt request while a
// turn is in flight and cancels the turn context when one arrives, aborting
// the in-flight model call and any running tools.
type turnInterruptWatcher struct {
	interrupted atomic.Bool
	stopOnce    sync.Once
	stop        chan struct{}
	done        chan struct{}
}

// startTurnInterruptWatcher launches the watcher goroutine. ctx must be the
// run's root context (pod lifetime), not the turn context, so polling
// survives the turn cancellation it triggers. crdClient may be nil, which
// disables the CRD fallback channel.
func startTurnInterruptWatcher(ctx context.Context, sc *sessionclient.Client, crdClient client.Client, runName, namespace string, cancelTurn context.CancelFunc) *turnInterruptWatcher {
	w := &turnInterruptWatcher{
		stop: make(chan struct{}),
		done: make(chan struct{}),
	}
	go func() {
		defer close(w.done)
		ticker := time.NewTicker(turnInterruptPollInterval)
		defer ticker.Stop()
		polls := 0
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
				} else if req != nil {
					log.Printf("Interrupt requested by %q — cancelling in-flight turn", req.RequestedBy)
					w.interrupted.Store(true)
					cancelTurn()
					return
				}
				polls++
				if crdClient == nil || polls%crdInterruptPollEvery != 0 {
					continue
				}
				// CRD fallback: a stop recorded on the AgentRun because the
				// session store was unreachable. Deleting the annotation is
				// the consume-and-acknowledge step.
				if _, found, crdErr := consumeCRDInterrupt(ctx, crdClient, runName, namespace); crdErr != nil {
					log.Printf("WARN: CRD interrupt watcher poll failed: %v", crdErr)
				} else if found {
					log.Printf("Interrupt requested via AgentRun annotation — cancelling in-flight turn")
					w.interrupted.Store(true)
					cancelTurn()
					return
				}
			}
		}
	}()
	return w
}

// consumeCRDInterrupt claims a pending interrupt annotation on the AgentRun:
// it returns the recorded request time and deletes the annotation so the
// request is honored exactly once. Annotation removal is the runner's
// acknowledgment; the dashboard treats a lingering annotation as an unacked
// stop it can escalate (force-stop via cancel).
func consumeCRDInterrupt(ctx context.Context, c client.Client, runName, namespace string) (time.Time, bool, error) {
	run := getAgentRun(ctx, c, runName, namespace)
	if run == nil {
		return time.Time{}, false, nil
	}
	raw, ok := run.Annotations[platformv1alpha1.InterruptRequestedAnnotation]
	if !ok {
		return time.Time{}, false, nil
	}
	requestedAt := time.Now().UTC()
	if parsed, err := time.Parse(time.RFC3339, raw); err == nil {
		requestedAt = parsed
	}
	if err := patchAgentRunSpec(ctx, c, runName, namespace, func(fresh *platformv1alpha1.AgentRun) {
		delete(fresh.Annotations, platformv1alpha1.InterruptRequestedAnnotation)
	}); err != nil {
		return time.Time{}, false, err
	}
	return requestedAt, true, nil
}

// drainCRDInterruptThrough consumes a pending CRD interrupt annotation not
// newer than cutoff and reports whether one was claimed. It mirrors the
// session channel's DrainInterruptsThrough: a request newer than cutoff is
// left pending for the in-turn watcher.
func drainCRDInterruptThrough(ctx context.Context, c client.Client, runName, namespace string, cutoff time.Time) bool {
	if c == nil {
		return false
	}
	run := getAgentRun(ctx, c, runName, namespace)
	if run == nil {
		return false
	}
	raw, ok := run.Annotations[platformv1alpha1.InterruptRequestedAnnotation]
	if !ok {
		return false
	}
	requestedAt := time.Now().UTC()
	if parsed, err := time.Parse(time.RFC3339, raw); err == nil {
		requestedAt = parsed
	}
	if requestedAt.After(cutoff) {
		return false
	}
	if err := patchAgentRunSpec(ctx, c, runName, namespace, func(fresh *platformv1alpha1.AgentRun) {
		delete(fresh.Annotations, platformv1alpha1.InterruptRequestedAnnotation)
	}); err != nil {
		log.Printf("WARN: failed to consume CRD interrupt annotation: %v", err)
		return false
	}
	return true
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
