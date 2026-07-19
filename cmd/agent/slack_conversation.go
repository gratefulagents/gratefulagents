package main

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"log"
	"strconv"
	"time"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	internalslack "github.com/gratefulagents/gratefulagents/internal/slack"
	"github.com/gratefulagents/gratefulagents/internal/store/postgres/sqlc"
	"github.com/jackc/pgx/v5"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// resolveConversationRun maps an inbound message to the AgentRun for its
// conversation. A conversation is a DM/group-DM (keyed by channel, threads
// ignored) or a channel thread (keyed by thread), so follow-ups continue the
// same run and its memory. The mapped run is reused while the conversation is
// active — last activity within the idle window and the run still resumable —
// otherwise a fresh run starts (idle rollover) so context and cost stay bounded.
// It returns the run name and whether an existing run is being continued.
func (o *slackOrchestrator) resolveConversationRun(
	ctx context.Context, d internalslack.Decision,
) (runName string, reused bool) {
	threadKey := internalslack.ConversationThreadKey(d.ChannelType, d.ThreadTS)

	mapped, err := o.queries.GetSlackThread(ctx, sqlc.GetSlackThreadParams{
		SlackAgent: o.slackStoreKey(),
		ChannelID:  d.ChannelID,
		ThreadTs:   threadKey,
	})
	if err == nil && mapped.RunName != "" {
		if time.Since(mapped.UpdatedAt) < o.idleWindow() && o.runIsResumable(ctx, mapped.RunName) {
			return mapped.RunName, true
		}
	} else if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		// Unexpected lookup failure: fall through and start a fresh run rather
		// than drop the message.
		log.Printf("slack connector %s: conversation lookup failed (%v); starting a new run", o.agentName, err)
	}

	return conversationRunName(o.agentName, d.ChannelID, threadKey, time.Now()), false
}

// recordConversationActivity persists (or refreshes) the conversation→run
// mapping and bumps its last-activity timestamp, extending the idle window.
func (o *slackOrchestrator) recordConversationActivity(ctx context.Context, d internalslack.Decision, runName, kind string) {
	threadKey := internalslack.ConversationThreadKey(d.ChannelType, d.ThreadTS)
	if err := o.queries.UpsertSlackThread(ctx, sqlc.UpsertSlackThreadParams{
		SlackAgent:   o.slackStoreKey(),
		ChannelID:    d.ChannelID,
		ThreadTs:     threadKey,
		RunNamespace: o.namespace,
		RunName:      runName,
		Kind:         kind,
	}); err != nil {
		log.Printf("slack connector %s: recording conversation mapping: %v", o.agentName, err)
	}
}

// runIsResumable reports whether an AgentRun exists and can still take another
// turn (not terminal). A rolled-over or terminated run is not resumable.
func (o *slackOrchestrator) runIsResumable(ctx context.Context, runName string) bool {
	run := &platformv1alpha1.AgentRun{}
	if err := o.crdClient.Get(ctx, client.ObjectKey{Namespace: o.namespace, Name: runName}, run); err != nil {
		return false
	}
	return !isTerminalPhase(run.Status.Phase)
}

// idleWindow returns the configured conversation idle window, defaulting when
// unset so a zero-value orchestrator (tests) still behaves sanely.
func (o *slackOrchestrator) idleWindow() time.Duration {
	if o.sessionIdle > 0 {
		return o.sessionIdle
	}
	return defaultSlackSessionIdle
}

// slackStoreKey scopes rows in the shared Slack tables, defaulting to the bare
// agent name so zero-value orchestrators (tests) and pre-existing dedicated
// rows keep working.
func (o *slackOrchestrator) slackStoreKey() string {
	if o.storeKey != "" {
		return o.storeKey
	}
	return o.agentName
}

// claimEvent records that a message has been handled, returning true only the
// first time. It dedupes Slack redeliveries of the same message (keyed by
// channel + message ts, robust to differing envelope ids). Events are currently
// ACKed before dispatch, so a transient claim error must fail open rather than
// silently discard an already-ACKed user command.
func (o *slackOrchestrator) claimEvent(ctx context.Context, channelID, messageTS string) bool {
	if messageTS == "" {
		return true
	}
	key := "msg:" + channelID + ":" + messageTS
	_, err := o.queries.MarkSlackEventSeen(ctx, sqlc.MarkSlackEventSeenParams{
		SlackAgent: o.slackStoreKey(),
		EnvelopeID: key,
	})
	if !shouldProcessSlackEventClaim(err) {
		return false // already handled
	}
	if err != nil {
		log.Printf("slack connector %s: event dedup claim failed (%v); processing already-ACKed message", o.agentName, err)
	}
	return true
}

func shouldProcessSlackEventClaim(err error) bool {
	return !errors.Is(err, pgx.ErrNoRows)
}

// conversationRunName builds a DNS-safe AgentRun name for a conversation epoch.
// The channel/thread hash groups a conversation; the time suffix makes each
// idle-rollover epoch a distinct run (the mapping table records which epoch is
// current), so a fresh run never collides with the previous, still-present one.
func conversationRunName(agentName, channelID, threadKey string, now time.Time) string {
	sum := sha1.Sum([]byte(agentName + "|" + channelID + "|" + threadKey))
	return "slack-" + hex.EncodeToString(sum[:])[:12] + "-" + strconv.FormatInt(now.UnixNano(), 36)
}

// isSlackRun reports whether an AgentRun was triggered by a SlackAgent.
func isSlackRun(run *platformv1alpha1.AgentRun) bool {
	return run != nil && run.Spec.Trigger.Kind == slackTriggerKind
}
