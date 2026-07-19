package main

import (
	"context"
	"strings"
	"time"

	internalslack "github.com/gratefulagents/gratefulagents/internal/slack"
)

// convQueue is the buffered channel feeding a conversation's serial worker.
type convQueue struct {
	ch chan internalslack.Decision
}

// convQueueBuffer bounds how many un-started messages a conversation may hold.
const convQueueBuffer = 64

// enqueueConversation hands a routed message to its conversation's serial worker,
// starting the worker on first use. Serializing per conversation prevents two
// turns from racing on the same AgentRun (which drafted duplicate replies for a
// rapid burst), and lets the worker coalesce a burst into a single turn.
func (o *slackOrchestrator) enqueueConversation(ctx context.Context, d internalslack.Decision) {
	key := conversationQueueKey(o.agentName, d)

	o.convMu.Lock()
	q, ok := o.convQueues[key]
	if !ok {
		q = &convQueue{ch: make(chan internalslack.Decision, convQueueBuffer)}
		o.convQueues[key] = q
		go o.conversationWorker(ctx, key, q)
	}
	// Send under the lock so an idle-exiting worker (which re-checks the channel
	// under the same lock before deleting itself) can never miss this message.
	select {
	case q.ch <- d:
		o.convMu.Unlock()
	default:
		// Never bypass the queue: inline execution would race the worker and
		// violate per-conversation turn ordering. Reject visibly under overload.
		o.convMu.Unlock()
		o.postErr(ctx, d, "I'm handling too many messages in this conversation; please retry shortly", context.DeadlineExceeded)
	}
}

// conversationWorker processes a conversation's messages strictly one turn at a
// time, coalescing rapid bursts. It exits after a long idle period, re-checking
// the queue under the lock so a concurrent enqueue is never lost.
func (o *slackOrchestrator) conversationWorker(ctx context.Context, key string, q *convQueue) {
	const idleExit = 15 * time.Minute
	idle := time.NewTimer(idleExit)
	defer idle.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case first := <-q.ch:
			o.processConversationBatch(ctx, o.coalesce(ctx, q, first))
			resetTimer(idle, idleExit)
		case <-idle.C:
			o.convMu.Lock()
			select {
			case d := <-q.ch:
				o.convMu.Unlock()
				o.processConversationBatch(ctx, o.coalesce(ctx, q, d))
				resetTimer(idle, idleExit)
			default:
				delete(o.convQueues, key)
				o.convMu.Unlock()
				return
			}
		}
	}
}

// coalesce collects the messages that arrive within the batch window after the
// first, so a burst is answered together. The window restarts on each new
// message, ending once the sender pauses.
func (o *slackOrchestrator) coalesce(
	ctx context.Context, q *convQueue, first internalslack.Decision,
) []internalslack.Decision {
	window := o.batchWindow
	if window <= 0 {
		window = defaultSlackBatchWindow
	}
	decisions := []internalslack.Decision{first}

	timer := time.NewTimer(window)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return decisions
		case d := <-q.ch:
			decisions = append(decisions, d)
			resetTimer(timer, window)
		case <-timer.C:
			return decisions
		}
	}
}

// processConversationBatch merges a burst into one decision and handles it.
func (o *slackOrchestrator) processConversationBatch(ctx context.Context, decisions []internalslack.Decision) {
	if len(decisions) == 0 {
		return
	}
	o.handleCommand(ctx, mergeDecisions(decisions))
}

// mergeDecisions combines a burst of messages into one: the reply/reaction
// targets come from the most recent message, and the texts are joined in order
// so the agent answers the whole burst in a single turn. Attachments from every
// message in the burst are carried through.
func mergeDecisions(decisions []internalslack.Decision) internalslack.Decision {
	merged := decisions[len(decisions)-1]
	if len(decisions) == 1 {
		return merged
	}
	texts := make([]string, 0, len(decisions))
	var files []internalslack.File
	for _, d := range decisions {
		if t := strings.TrimSpace(d.Text); t != "" {
			texts = append(texts, t)
		}
		files = append(files, d.Files...)
	}
	merged.Text = strings.Join(texts, "\n")
	merged.Files = files
	return merged
}

// conversationQueueKey groups messages that must be serialized together: the
// same key as the conversation's AgentRun (DM/group by channel, channel by
// thread).
func conversationQueueKey(agentName string, d internalslack.Decision) string {
	return agentName + "|" + d.ChannelID + "|" + internalslack.ConversationThreadKey(d.ChannelType, d.ThreadTS)
}

// resetTimer safely resets a timer that may have already fired.
func resetTimer(t *time.Timer, d time.Duration) {
	if !t.Stop() {
		select {
		case <-t.C:
		default:
		}
	}
	t.Reset(d)
}
