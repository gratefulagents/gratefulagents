package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	internalslack "github.com/gratefulagents/gratefulagents/internal/slack"
	"github.com/gratefulagents/gratefulagents/internal/store/postgres/sqlc"
	"github.com/gratefulagents/gratefulagents/internal/store/sessionclient"
)

// slackSessionKeepalive is how often a long-running turn re-sends the
// "processing" status: Slack times a processing session out to active after
// one hour, which would hide the stop button and the working indicator while
// the run is still busy.
const slackSessionKeepalive = 30 * time.Minute

// slackStoppedMessage confirms a stop-button click in the thread.
const slackStoppedMessage = ":octagonal_sign: Stopped. Send another message when you want me to continue."

// setSession updates the thread's agent session status. Best-effort: the
// session is a presentation layer (working indicator, stop button, sidebar
// title), so failures are logged and never block a turn. Apps that predate the
// Agent messaging experience simply get no session UI.
func (o *slackOrchestrator) setSession(ctx context.Context, channelID, threadTS string, status internalslack.SessionStatus) {
	o.setSessionWithTitle(ctx, channelID, threadTS, status, "", "")
}

// setSessionWithTitle is setSession for the first turn of a conversation: the
// title and initiator only apply when Slack creates the session.
func (o *slackOrchestrator) setSessionWithTitle(
	ctx context.Context, channelID, threadTS string, status internalslack.SessionStatus, title, initiator string,
) {
	if o.web == nil || channelID == "" || threadTS == "" {
		return
	}
	err := o.web.SetSessionStatus(ctx, internalslack.SessionParams{
		ChannelID: channelID, ThreadTS: threadTS, Status: status, Title: title, InitiatorUserID: initiator,
	})
	if err != nil {
		log.Printf("slack connector %s: session %s/%s -> %s: %v", o.agentName, channelID, threadTS, status, err)
	}
}

// sessionTitleFor derives the session title Slack shows in the user's agent
// sidebar from the first message of a conversation.
func sessionTitleFor(d internalslack.Decision) string {
	if title := internalslack.SessionTitle(d.Text); title != "" {
		return title
	}
	if len(d.Files) == 1 {
		return "Shared file: " + d.Files[0].Name
	}
	if len(d.Files) > 1 {
		return fmt.Sprintf("Shared %d files", len(d.Files))
	}
	return ""
}

// streamTarget addresses the streamed reply for a turn. Slack requires the
// recipient identity when streaming anywhere but the app DM.
func (o *slackOrchestrator) streamTarget(w replyWatch) internalslack.StreamTarget {
	t := internalslack.StreamTarget{ChannelID: w.channelID, ThreadTS: w.threadTS}
	if w.channelType != "im" {
		t.RecipientUserID = w.requester
		t.RecipientTeamID = o.teamID
	}
	return t
}

// deliverReply posts a turn's reply (one or more assistant messages, oldest
// first) into the thread as a single streamed message rendered from the
// agent's native markdown, finishing with feedback buttons and the session
// back in the active state. When streaming is unavailable (missing scope,
// legacy app) it falls back to one markdown chat.postMessage per message and
// sets the session status explicitly. Returns false when nothing could be
// delivered.
func (o *slackOrchestrator) deliverReply(ctx context.Context, w replyWatch, texts []string) bool {
	if len(texts) == 0 {
		return false
	}
	if o.streamReply(ctx, w, texts) {
		return true
	}
	posted := 0
	for _, text := range texts {
		if _, err := o.web.PostMarkdownAsBot(ctx, w.channelID, text, w.threadTS); err != nil {
			log.Printf("slack connector %s: posting reply for %s: %v", o.agentName, w.runName, err)
			break
		}
		posted++
	}
	o.setSession(ctx, w.channelID, w.threadTS, internalslack.SessionActive)
	return posted > 0
}

// streamReply delivers texts through chat.startStream/appendStream/stopStream.
// A failure before the stream opens reports false so the caller can fall back;
// a failure after it opened is closed out best-effort (Slack keeps whatever was
// streamed) and still counts as delivered.
func (o *slackOrchestrator) streamReply(ctx context.Context, w replyWatch, texts []string) bool {
	ts, err := o.web.StartStream(ctx, o.streamTarget(w), texts[0])
	if err != nil {
		log.Printf("slack connector %s: streaming reply for %s unavailable (%v); posting instead", o.agentName, w.runName, err)
		return false
	}
	for _, text := range texts[1:] {
		if err := o.web.AppendStream(ctx, w.channelID, ts, "\n\n"+text); err != nil {
			log.Printf("slack connector %s: appending reply for %s: %v", o.agentName, w.runName, err)
			break
		}
	}
	feedback := internalslack.BuildReplyFeedbackBlocks(w.runName)
	if err := o.web.StopStream(ctx, w.channelID, ts, "", internalslack.SessionActive, feedback...); err != nil {
		log.Printf("slack connector %s: finishing reply stream for %s: %v", o.agentName, w.runName, err)
		// Retry without trailing blocks so the message is at least finalized.
		if err := o.web.StopStream(ctx, w.channelID, ts, "", internalslack.SessionActive); err != nil {
			log.Printf("slack connector %s: finalizing reply stream for %s: %v", o.agentName, w.runName, err)
			o.setSession(ctx, w.channelID, w.threadTS, internalslack.SessionActive)
		}
	}
	return true
}

// collectNewAssistantMessages returns assistant messages newer than *cursor
// (oldest first, blanks skipped) and advances the cursor past them.
func (o *slackOrchestrator) collectNewAssistantMessages(ctx context.Context, runName string, cursor *int64) []string {
	sess, err := o.store.GetSessionByRun(ctx, runName, o.namespace)
	if err != nil {
		return nil
	}
	msgs, err := o.store.GetMessages(ctx, sess.ID)
	if err != nil {
		return nil
	}
	var texts []string
	for _, m := range msgs {
		if m.Role != roleAssistant || m.ID <= *cursor {
			continue
		}
		*cursor = m.ID
		if text := strings.TrimSpace(m.Content); text != "" {
			texts = append(texts, text)
		}
	}
	return texts
}

// registerStop returns a channel closed when the user stops the run's current
// turn from Slack. The caller must releaseStop when the turn ends.
func (o *slackOrchestrator) registerStop(runName string) <-chan struct{} {
	o.stopMu.Lock()
	defer o.stopMu.Unlock()
	if o.stopSignals == nil {
		o.stopSignals = map[string]chan struct{}{}
	}
	if ch, ok := o.stopSignals[runName]; ok {
		return ch
	}
	ch := make(chan struct{})
	o.stopSignals[runName] = ch
	return ch
}

// releaseStop forgets the stop channel for a run once its turn has ended.
func (o *slackOrchestrator) releaseStop(runName string) {
	o.stopMu.Lock()
	defer o.stopMu.Unlock()
	delete(o.stopSignals, runName)
}

// signalStop wakes the reply watcher for runName, if any, so it can confirm
// the stop in the thread instead of waiting for a reply that will not come.
// Returns whether a watcher was active.
func (o *slackOrchestrator) signalStop(runName string) bool {
	o.stopMu.Lock()
	defer o.stopMu.Unlock()
	ch, ok := o.stopSignals[runName]
	if !ok {
		return false
	}
	close(ch)
	delete(o.stopSignals, runName)
	return true
}

// conversationRunFor resolves the run currently mapped to a Slack thread. The
// stop event does not carry the channel type, so a DM (keyed without a thread)
// is tried after the thread-keyed lookup.
func (o *slackOrchestrator) conversationRunFor(ctx context.Context, channelID, threadTS string) string {
	if o.queries == nil {
		return ""
	}
	for _, key := range []string{threadTS, ""} {
		mapped, err := o.queries.GetSlackThread(ctx, sqlc.GetSlackThreadParams{
			SlackAgent: o.slackStoreKey(), ChannelID: channelID, ThreadTs: key,
		})
		if err == nil && mapped.RunName != "" {
			return mapped.RunName
		}
		if err != nil && err != pgx.ErrNoRows {
			log.Printf("slack connector %s: stop lookup for %s/%s: %v", o.agentName, channelID, key, err)
		}
	}
	return ""
}

// handleSessionStopped acts on Slack's native stop button: the run's current
// turn is interrupted (the same dual-channel stop the dashboard uses, so the
// conversation stays resumable), the reply watcher is told to confirm the
// stop, and the session leaves the processing state. Only the owner or a
// commander may stop the agent; other clicks are ignored silently.
func (o *slackOrchestrator) handleSessionStopped(ctx context.Context, e *slackSessionStoppedEvent) {
	if e == nil || e.Channel == "" || e.ThreadTS == "" {
		return
	}
	if !o.mayStop(e.User) {
		log.Printf("slack connector %s: ignoring stop from %s in %s", o.agentName, e.User, e.Channel)
		return
	}
	runName := o.conversationRunFor(ctx, e.Channel, e.ThreadTS)
	if runName == "" {
		// Nothing of ours is running here; just clear the working indicator.
		o.setSession(ctx, e.Channel, e.ThreadTS, internalslack.SessionActive)
		return
	}
	o.interruptRun(ctx, runName, e.User)
	if !o.signalStop(runName) {
		// No watcher is waiting (the turn already finished): report directly.
		_, _ = o.web.PostMessageAsBot(ctx, e.Channel, slackStoppedMessage, e.ThreadTS)
		o.setSession(ctx, e.Channel, e.ThreadTS, internalslack.SessionActive)
	}
}

// mayStop reports whether a Slack user may stop this agent's work: the owner
// or a configured commander. An unknown owner fails closed.
func (o *slackOrchestrator) mayStop(userID string) bool {
	userID = strings.TrimSpace(userID)
	if userID == "" || o.ownerUserID == "" {
		return false
	}
	if userID == o.ownerUserID {
		return true
	}
	for _, c := range o.commanders {
		if strings.TrimSpace(c) == userID {
			return true
		}
	}
	return false
}

// interruptRun requests the run's current turn to stop on both channels the
// runner watches: the CRD annotation and the session store.
func (o *slackOrchestrator) interruptRun(ctx context.Context, runName, requestedBy string) {
	if o.crdClient != nil {
		run := &platformv1alpha1.AgentRun{}
		key := client.ObjectKey{Namespace: o.namespace, Name: runName}
		if err := o.crdClient.Get(ctx, key, run); err != nil {
			if !apierrors.IsNotFound(err) {
				log.Printf("slack connector %s: reading run %s for stop: %v", o.agentName, runName, err)
			}
		} else if !isTerminalPhase(run.Status.Phase) {
			patch := client.MergeFrom(run.DeepCopy())
			if run.Annotations == nil {
				run.Annotations = map[string]string{}
			}
			if _, exists := run.Annotations[platformv1alpha1.InterruptRequestedAnnotation]; !exists {
				run.Annotations[platformv1alpha1.InterruptRequestedAnnotation] = time.Now().UTC().Format(time.RFC3339)
				if err := o.crdClient.Patch(ctx, run, patch); err != nil {
					log.Printf("slack connector %s: recording interrupt on %s: %v", o.agentName, runName, err)
				}
			}
		}
	}
	if o.store == nil {
		return
	}
	sess, err := o.store.GetSessionByRun(ctx, runName, o.namespace)
	if err != nil {
		return
	}
	actor := "slack:" + requestedBy
	if err := sessionclient.RequestInterrupt(ctx, o.store, sess.ID, actor); err != nil {
		log.Printf("slack connector %s: interrupt request for %s: %v", o.agentName, runName, err)
		return
	}
	if _, err := o.store.WriteActivityEvent(ctx, sess.ID, "interrupt_requested",
		fmt.Sprintf("Stop requested by %s from Slack — interrupting the current turn", actor), nil); err != nil {
		log.Printf("slack connector %s: recording interrupt activity for %s: %v", o.agentName, runName, err)
	}
}

// handleReplyFeedback records a thumbs up/down on a streamed reply as an
// activity event on the run's session (visible in the dashboard timeline) and
// thanks the user privately. Feedback is advisory and never alters the run.
func (o *slackOrchestrator) handleReplyFeedback(ctx context.Context, runName, value, channelID, userID string) {
	runName = strings.TrimSpace(runName)
	if runName == "" || o.store == nil {
		return
	}
	summary := "Slack reader marked the reply as helpful"
	if value == internalslack.ReplyFeedbackNegative {
		summary = "Slack reader marked the reply as not helpful"
	}
	if sess, err := o.store.GetSessionByRun(ctx, runName, o.namespace); err == nil {
		detail, _ := json.Marshal(map[string]string{"source": "slack", "user": userID, "feedback": value})
		if _, err := o.store.WriteActivityEvent(ctx, sess.ID, "reply_feedback", summary, detail); err != nil {
			log.Printf("slack connector %s: recording feedback for %s: %v", o.agentName, runName, err)
		}
	}
	if channelID != "" && userID != "" {
		_ = o.web.PostEphemeralAsBot(ctx, channelID, userID, "Thanks for the feedback!")
	}
}
