package main

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/google/uuid"
	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/orchestration"
	internalslack "github.com/gratefulagents/gratefulagents/internal/slack"
	"github.com/gratefulagents/gratefulagents/internal/store/postgres/sqlc"
	slackgo "github.com/slack-go/slack"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// handleInteraction dispatches Block Kit interactions (button clicks, modal
// submissions) to their handlers by action/callback ID.
func (o *slackOrchestrator) handleInteraction(ctx context.Context, callback slackgo.InteractionCallback) {
	switch callback.Type {
	case slackgo.InteractionTypeBlockActions:
		o.handleBlockAction(ctx, callback)
	case slackgo.InteractionTypeViewSubmission:
		o.handleViewSubmission(ctx, callback)
	}
}

// handleBlockAction routes a button click by its action ID.
func (o *slackOrchestrator) handleBlockAction(ctx context.Context, callback slackgo.InteractionCallback) {
	actions := callback.ActionCallback.BlockActions
	if len(actions) == 0 {
		return
	}
	action := actions[0]
	draft, ok := o.loadDraft(ctx, action.Value, callback.User.ID)
	if !ok {
		return
	}
	notifyCh := callback.Container.ChannelID
	notifyTS := callback.Container.MessageTs

	switch action.ActionID {
	case internalslack.ActionDraftApprove:
		o.approveDraft(ctx, draft, notifyCh, notifyTS)
	case internalslack.ActionDraftDismiss:
		_ = o.resolveDraft(ctx, draft.ID, slackDraftDismissed, draft.DraftText)
		if draft.Kind == slackDraftKindChannelReply {
			o.resolveTurnReaction(ctx, draft.ChannelID, draft.OriginMsgTs, "x")
		}
		_ = o.web.UpdateMessageAsBot(ctx, notifyCh, notifyTS, ":wastebasket: Dismissed — nothing was posted.")
	case internalslack.ActionDraftEdit:
		modal := internalslack.BuildDraftEditModal(draft.ID.String(), draft.DraftText)
		if err := o.web.OpenModal(ctx, callback.TriggerID, modal); err != nil {
			log.Printf("slack connector %s: opening edit modal: %v", o.agentName, err)
		}
	case internalslack.ActionDraftRegen:
		modal := internalslack.BuildDraftRegenModal(draft.ID.String())
		if err := o.web.OpenModal(ctx, callback.TriggerID, modal); err != nil {
			log.Printf("slack connector %s: opening regenerate modal: %v", o.agentName, err)
		}
	}
}

// approveDraft posts an approved channel reply as the bot into the originating
// thread and resolves the card. Drafts of any other kind are stale rows from
// the removed inbox-monitoring feature and are closed out without sending.
func (o *slackOrchestrator) approveDraft(ctx context.Context, draft sqlc.SlackDraft, notifyCh, notifyTS string) {
	if draft.Status != slackDraftPending {
		return
	}
	if draft.Kind != slackDraftKindChannelReply {
		o.dismissLegacyDraft(ctx, draft, notifyCh, notifyTS)
		return
	}
	claimed, err := o.claimDraftTransition(ctx, draft, slackDraftSending)
	if err != nil {
		log.Printf("slack connector %s: claiming draft %s: %v", o.agentName, draft.ID, err)
		return
	}
	o.postApprovedChannelReply(ctx, claimed, claimed.DraftText, false)
}

// dismissLegacyDraft resolves a draft row left over from the removed inbox
// monitoring feature: it would have been sent as the owner, which the
// connector can no longer do, so the card is closed out without sending.
func (o *slackOrchestrator) dismissLegacyDraft(ctx context.Context, draft sqlc.SlackDraft, notifyCh, notifyTS string) {
	_ = o.resolveDraft(ctx, draft.ID, slackDraftDismissed, draft.DraftText)
	const msg = ":no_entry: Inbox monitoring has been removed — this draft can no longer be sent."
	if notifyCh != "" && notifyTS != "" {
		_ = o.web.UpdateMessageAsBot(ctx, notifyCh, notifyTS, msg)
		return
	}
	o.updateDraftCard(ctx, draft, msg)
}

// postApprovedChannelReply posts an approved (possibly owner-edited) channel
// reply as the bot into its originating thread, resolves the draft and the
// trigger reaction, and refreshes the approval card (via its stored notify ts,
// since modal submissions carry no message container). The agent's reply is
// markdown, so it is converted for Slack at post time (edits included).
func (o *slackOrchestrator) claimDraftTransition(ctx context.Context, draft sqlc.SlackDraft, nextStatus string) (sqlc.SlackDraft, error) {
	return o.queries.ClaimSlackDraft(ctx, sqlc.ClaimSlackDraftParams{
		NextStatus: nextStatus, ID: draft.ID, Namespace: o.namespace,
		SlackAgent: o.agentName, OwnerSubject: o.ownerUserID, Kind: slackDraftKindChannelReply,
	})
}

func (o *slackOrchestrator) postApprovedChannelReply(
	ctx context.Context, draft sqlc.SlackDraft, text string, edited bool,
) {
	if _, err := o.web.PostMessageAsBot(ctx, draft.ChannelID, internalslack.ToMrkdwn(text), draft.ThreadTs); err != nil {
		log.Printf("slack connector %s: posting approved channel reply: %v", o.agentName, err)
		// Return to pending so a confirmed failed attempt can be retried. The
		// sending claim prevents concurrent clicks from posting twice.
		_ = o.queries.UpdateSlackDraftText(ctx, sqlc.UpdateSlackDraftTextParams{
			ID: draft.ID, DraftText: text, ExpectedStatus: slackDraftSending, NextStatus: slackDraftPending,
		})
		o.updateDraftCard(ctx, draft, ":warning: I couldn't post that reply; it remains pending for retry.")
		return
	}
	if edited {
		if err := o.queries.ResolveSlackDraftEdited(ctx, sqlc.ResolveSlackDraftEditedParams{
			ID:             draft.ID,
			Status:         slackDraftSent,
			EditedText:     text,
			ExpectedStatus: slackDraftSending,
		}); err != nil {
			log.Printf("slack connector %s: resolving edited channel reply: %v", o.agentName, err)
		}
	} else if err := o.resolveDraft(ctx, draft.ID, slackDraftSent, text); err != nil {
		log.Printf("slack connector %s: resolving channel reply: %v", o.agentName, err)
	}
	o.resolveTurnReaction(ctx, draft.ChannelID, draft.OriginMsgTs, "white_check_mark")
	suffix := ""
	if edited {
		suffix = " (edited)"
	}
	o.updateDraftCard(ctx, draft,
		":white_check_mark: Posted"+suffix+" in <#"+draft.ChannelID+">:\n>"+strings.ReplaceAll(text, "\n", "\n>"))
}

// handleViewSubmission routes a modal submit by its callback ID.
func (o *slackOrchestrator) handleViewSubmission(ctx context.Context, callback slackgo.InteractionCallback) {
	draft, ok := o.loadDraft(ctx, callback.View.PrivateMetadata, callback.User.ID)
	if !ok {
		return
	}
	switch callback.View.CallbackID {
	case internalslack.CallbackDraftEditModal:
		text := viewInputValue(callback, internalslack.BlockDraftReplyInput)
		o.sendEditedDraft(ctx, draft, text)
	case internalslack.CallbackDraftRegenModal:
		feedback := viewInputValue(callback, internalslack.BlockDraftFeedbackInput)
		o.regenerateDraft(ctx, draft, feedback)
	}
}

// sendEditedDraft posts the owner's edited channel reply and resolves the card,
// keeping the original draft for the audit trail.
func (o *slackOrchestrator) sendEditedDraft(ctx context.Context, draft sqlc.SlackDraft, text string) {
	text = strings.TrimSpace(text)
	if text == "" || draft.Status != slackDraftPending {
		return
	}
	if draft.Kind != slackDraftKindChannelReply {
		o.dismissLegacyDraft(ctx, draft, "", "")
		return
	}
	claimed, err := o.claimDraftTransition(ctx, draft, slackDraftSending)
	if err != nil {
		log.Printf("slack connector %s: claiming edited draft %s: %v", o.agentName, draft.ID, err)
		return
	}
	o.postApprovedChannelReply(ctx, claimed, text, true)
}

// regenerateDraft wakes the command run that proposed a held channel reply with
// the owner's feedback, waits for the revised reply, and refreshes the approval
// card.
func (o *slackOrchestrator) regenerateDraft(ctx context.Context, draft sqlc.SlackDraft, feedback string) {
	feedback = strings.TrimSpace(feedback)
	if feedback == "" || draft.Status != slackDraftPending {
		return
	}
	if draft.Kind != slackDraftKindChannelReply {
		o.dismissLegacyDraft(ctx, draft, "", "")
		return
	}
	claimed, err := o.claimDraftTransition(ctx, draft, slackDraftRegenerating)
	if err != nil {
		log.Printf("slack connector %s: claiming draft regeneration %s: %v", o.agentName, draft.ID, err)
		return
	}
	o.regenerateChannelReply(ctx, claimed, feedback)
}

// regenerateChannelReply wakes the command run that proposed a held channel
// reply with the owner's feedback, waits for the revised reply, and refreshes
// the approval card. The pending draft (and the thread's :eyes: reaction) stay
// as they are until the owner decides on the new text.
func (o *slackOrchestrator) regenerateChannelReply(ctx context.Context, draft sqlc.SlackDraft, feedback string) {
	o.updateDraftCard(ctx, draft, ":writing_hand: Regenerating the reply for <#"+draft.ChannelID+">…")

	fail := func(summary string, err error) {
		log.Printf("slack connector %s: %s: %v", o.agentName, summary, err)
		_ = o.queries.UpdateSlackDraftText(ctx, sqlc.UpdateSlackDraftTextParams{
			ID: draft.ID, DraftText: draft.DraftText, ExpectedStatus: slackDraftRegenerating, NextStatus: slackDraftPending,
		})
		blocks := internalslack.BuildChannelReplyApprovalBlocks(
			draft.ID.String(), slackMention(draft.TargetUser), draft.ChannelID, draft.IncomingText, draft.DraftText)
		o.updateDraftCardBlocks(ctx, draft, ":warning: I couldn't regenerate the reply.", blocks...)
	}

	runName := strings.TrimSpace(draft.RunName)
	if runName == "" {
		fail("regenerating channel reply", fmt.Errorf("draft %s has no source run", draft.ID))
		return
	}
	gate := o.turnGate(runName)
	gate.Lock()
	defer gate.Unlock()
	baseline := o.maxAssistantMessageID(ctx, runName)
	if err := orchestration.WakeAgentRun(ctx, o.crdClient, o.store, o.namespace, runName,
		channelReplyReviseInstruction(draft.DraftText, feedback)); err != nil {
		fail("waking run for channel-reply regeneration", err)
		return
	}
	text := o.waitForFinalReply(ctx, runName, baseline)
	if strings.TrimSpace(text) == "" {
		fail("regenerating channel reply", fmt.Errorf("run %s produced no revised reply", runName))
		return
	}
	if err := o.queries.UpdateSlackDraftText(ctx, sqlc.UpdateSlackDraftTextParams{
		ID: draft.ID, DraftText: text, ExpectedStatus: slackDraftRegenerating, NextStatus: slackDraftPending,
	}); err != nil {
		log.Printf("slack connector %s: updating regenerated channel reply: %v", o.agentName, err)
		return
	}
	blocks := internalslack.BuildChannelReplyApprovalBlocks(
		draft.ID.String(), slackMention(draft.TargetUser), draft.ChannelID, draft.IncomingText, text)
	o.updateDraftCardBlocks(ctx, draft, "A reply is ready for your approval.", blocks...)
}

// channelReplyReviseInstruction is the wake prompt for regenerating a held
// channel reply with the owner's feedback. The revised text is captured from
// the run's next assistant message, so it must be the message content only.
func channelReplyReviseInstruction(prev, feedback string) string {
	return "The owner reviewed the reply you proposed and wants changes before it is posted: " + feedback +
		"\n\nYour previous proposed reply:\n" + prev +
		"\n\nRespond with ONLY the revised message to post — no preamble, no commentary, no quotes."
}

// waitForFinalReply polls a run until the reply for the current turn is ready
// and returns it: the newest assistant message with an id past baseline. The
// phase can't be the signal because a persistent-pod chat run stays Running
// while idle between turns; the baseline keeps a reused run from returning a
// previous turn's reply. Bails out early if the run ends before replying.
func (o *slackOrchestrator) waitForFinalReply(ctx context.Context, runName string, baseline int64) string {
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	deadline := time.Now().Add(15 * time.Minute)

	for {
		select {
		case <-ctx.Done():
			return ""
		case <-ticker.C:
		}

		if text := o.latestAssistantTextAfter(ctx, runName, baseline); text != "" {
			return text
		}

		run := &platformv1alpha1.AgentRun{}
		if err := o.crdClient.Get(ctx, client.ObjectKey{Namespace: o.namespace, Name: runName}, run); err != nil {
			if apierrors.IsNotFound(err) {
				return ""
			}
		} else if isTerminalPhase(run.Status.Phase) {
			return "" // run ended without producing a reply
		}

		if time.Now().After(deadline) {
			return ""
		}
	}
}

// latestAssistantTextAfter returns the newest assistant message with an id past
// afterID, or "" when none. Used to capture only the current turn's reply.
func (o *slackOrchestrator) latestAssistantTextAfter(ctx context.Context, runName string, afterID int64) string {
	sess, err := o.store.GetSessionByRun(ctx, runName, o.namespace)
	if err != nil {
		return ""
	}
	msgs, err := o.store.GetMessages(ctx, sess.ID)
	if err != nil {
		return ""
	}
	var latest string
	for _, m := range msgs {
		if m.Role == roleAssistant && m.ID > afterID && strings.TrimSpace(m.Content) != "" {
			latest = strings.TrimSpace(m.Content)
		}
	}
	return latest
}

// updateDraftCard refreshes the draft's control-DM message using the stored
// notify ts (modal submissions carry no message container).
func (o *slackOrchestrator) updateDraftCard(ctx context.Context, draft sqlc.SlackDraft, text string) {
	o.updateDraftCardBlocks(ctx, draft, text)
}

func (o *slackOrchestrator) updateDraftCardBlocks(ctx context.Context, draft sqlc.SlackDraft, text string, blocks ...slackgo.Block) {
	botDMChannelID := o.botDMChannel()
	if botDMChannelID == "" || draft.NotifyMsgTs == "" {
		return
	}
	if err := o.web.UpdateMessageAsBot(ctx, botDMChannelID, draft.NotifyMsgTs, text, blocks...); err != nil {
		log.Printf("slack connector %s: updating draft card: %v", o.agentName, err)
	}
}

// loadDraft parses and authorizes a pending draft. The opaque UUID alone is
// never authority: actor, owner, namespace, agent, kind, and state must match.
func (o *slackOrchestrator) loadDraft(ctx context.Context, raw, actor string) (sqlc.SlackDraft, bool) {
	draftID, err := uuid.Parse(strings.TrimSpace(raw))
	if err != nil {
		log.Printf("slack connector %s: bad draft id %q: %v", o.agentName, raw, err)
		return sqlc.SlackDraft{}, false
	}
	draft, err := o.queries.GetSlackDraft(ctx, draftID)
	if err != nil {
		log.Printf("slack connector %s: loading draft %s: %v", o.agentName, draftID, err)
		return sqlc.SlackDraft{}, false
	}
	if strings.TrimSpace(actor) == "" || actor != o.ownerUserID ||
		draft.OwnerSubject != o.ownerUserID || draft.Namespace != o.namespace ||
		draft.SlackAgent != o.agentName || draft.Kind != slackDraftKindChannelReply ||
		draft.Status != slackDraftPending {
		log.Printf("WARN: slack connector %s: rejected unauthorized or stale draft action %s by %q", o.agentName, draftID, actor)
		return sqlc.SlackDraft{}, false
	}
	return draft, true
}

// viewInputValue extracts a plain-text input's value from a modal submission.
func viewInputValue(callback slackgo.InteractionCallback, blockID string) string {
	if callback.View.State == nil {
		return ""
	}
	block, ok := callback.View.State.Values[blockID]
	if !ok {
		return ""
	}
	return block[internalslack.ActionDraftInputValue].Value
}

// resolveDraft finalizes a draft row with its outcome status.
func (o *slackOrchestrator) resolveDraft(ctx context.Context, id uuid.UUID, status, draftText string) error {
	expected := slackDraftPending
	if status == slackDraftSent {
		expected = slackDraftSending
	}
	return o.queries.ResolveSlackDraft(ctx, sqlc.ResolveSlackDraftParams{
		ID: id, Status: status, DraftText: draftText, ExpectedStatus: expected,
	})
}
