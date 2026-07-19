package slack

import (
	"fmt"

	slackgo "github.com/slack-go/slack"
)

// Block Kit action IDs for the draft-approval prompt. The connector matches on
// these when an interaction callback arrives.
const (
	ActionDraftApprove = "slack_draft_approve"
	ActionDraftDismiss = "slack_draft_dismiss"
	// ActionDraftEdit opens a modal to edit the draft before posting.
	ActionDraftEdit = "slack_draft_edit"
	// ActionDraftRegen opens a modal asking what to change; the run that
	// proposed the reply is woken with the feedback and the card is refreshed
	// with the new draft.
	ActionDraftRegen = "slack_draft_regen"
)

// View callback IDs and input block/action IDs for draft modals.
const (
	CallbackDraftEditModal  = "slack_draft_edit_modal"
	CallbackDraftRegenModal = "slack_draft_regen_modal"
	BlockDraftReplyInput    = "draft_reply_input"
	BlockDraftFeedbackInput = "draft_feedback_input"
	ActionDraftInputValue   = "value"
)

// truncateForBlock keeps section text within Slack's 3000-char block limit.
func truncateForBlock(s string) string {
	const max = 2900
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}

// BuildChannelReplyApprovalBlocks renders the owner-facing approval prompt for
// the agent's own reply into a public conversation (channel thread, group DM):
// who asked and where, the proposed reply, and Approve / Edit / Regenerate /
// Dismiss actions carrying the draft ID.
func BuildChannelReplyApprovalBlocks(draftID, requesterMention, channelID, incomingText, draftText string) []slackgo.Block {
	header := slackgo.NewSectionBlock(
		slackgo.NewTextBlockObject(slackgo.MarkdownType,
			fmt.Sprintf(":speech_balloon: *%s* asked in <#%s>:", requesterMention, channelID), false, false),
		nil, nil,
	)
	return draftApprovalBlocks(header, draftID, incomingText, draftText)
}

// draftApprovalBlocks assembles the shared body of an approval card: the quoted
// incoming message, the proposed reply, and Approve / Edit / Regenerate /
// Dismiss buttons carrying the draft ID.
func draftApprovalBlocks(header *slackgo.SectionBlock, draftID, incomingText, draftText string) []slackgo.Block {
	incoming := slackgo.NewSectionBlock(
		slackgo.NewTextBlockObject(slackgo.MarkdownType, "> "+truncateForBlock(incomingText), false, false),
		nil, nil,
	)
	draft := slackgo.NewSectionBlock(
		slackgo.NewTextBlockObject(slackgo.MarkdownType,
			"*Proposed reply:*\n"+truncateForBlock(draftText), false, false),
		nil, nil,
	)
	approve := slackgo.NewButtonBlockElement(ActionDraftApprove, draftID,
		slackgo.NewTextBlockObject(slackgo.PlainTextType, "Approve & send", true, false))
	approve.Style = slackgo.StylePrimary
	edit := slackgo.NewButtonBlockElement(ActionDraftEdit, draftID,
		slackgo.NewTextBlockObject(slackgo.PlainTextType, "Edit & send", true, false))
	regen := slackgo.NewButtonBlockElement(ActionDraftRegen, draftID,
		slackgo.NewTextBlockObject(slackgo.PlainTextType, "Regenerate", true, false))
	dismiss := slackgo.NewButtonBlockElement(ActionDraftDismiss, draftID,
		slackgo.NewTextBlockObject(slackgo.PlainTextType, "Dismiss", true, false))
	dismiss.Style = slackgo.StyleDanger
	actions := slackgo.NewActionBlock("slack_draft_actions", approve, edit, regen, dismiss)

	return []slackgo.Block{header, incoming, draft, actions}
}

// BuildDraftEditModal is the "Edit & send" modal, prefilled with the draft.
// private_metadata carries the draft ID through to the submission callback.
func BuildDraftEditModal(draftID, draftText string) slackgo.ModalViewRequest {
	input := slackgo.NewPlainTextInputBlockElement(
		slackgo.NewTextBlockObject(slackgo.PlainTextType, "Reply", false, false), ActionDraftInputValue)
	input.Multiline = true
	input.InitialValue = draftText
	block := slackgo.NewInputBlock(BlockDraftReplyInput,
		slackgo.NewTextBlockObject(slackgo.PlainTextType, "Edit the reply to send", false, false),
		nil, input)

	return slackgo.ModalViewRequest{
		Type:            slackgo.VTModal,
		CallbackID:      CallbackDraftEditModal,
		PrivateMetadata: draftID,
		Title:           slackgo.NewTextBlockObject(slackgo.PlainTextType, "Edit reply", false, false),
		Submit:          slackgo.NewTextBlockObject(slackgo.PlainTextType, "Send", false, false),
		Close:           slackgo.NewTextBlockObject(slackgo.PlainTextType, "Cancel", false, false),
		Blocks:          slackgo.Blocks{BlockSet: []slackgo.Block{block}},
	}
}

// BuildDraftRegenModal is the "Regenerate" modal asking what should change.
func BuildDraftRegenModal(draftID string) slackgo.ModalViewRequest {
	input := slackgo.NewPlainTextInputBlockElement(
		slackgo.NewTextBlockObject(slackgo.PlainTextType, "e.g. shorter, more formal, mention the deadline", false, false),
		ActionDraftInputValue)
	input.Multiline = true
	block := slackgo.NewInputBlock(BlockDraftFeedbackInput,
		slackgo.NewTextBlockObject(slackgo.PlainTextType, "What should change?", false, false),
		nil, input)

	return slackgo.ModalViewRequest{
		Type:            slackgo.VTModal,
		CallbackID:      CallbackDraftRegenModal,
		PrivateMetadata: draftID,
		Title:           slackgo.NewTextBlockObject(slackgo.PlainTextType, "Regenerate reply", false, false),
		Submit:          slackgo.NewTextBlockObject(slackgo.PlainTextType, "Regenerate", false, false),
		Close:           slackgo.NewTextBlockObject(slackgo.PlainTextType, "Cancel", false, false),
		Blocks:          slackgo.Blocks{BlockSet: []slackgo.Block{block}},
	}
}
