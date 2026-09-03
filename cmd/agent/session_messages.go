package main

import (
	"strings"

	"github.com/gratefulagents/gratefulagents/internal/store/sessionclient"
	agent "github.com/gratefulagents/sdk/pkg/agentsdk"
)

func nextPendingUserMessage(messages []sessionclient.UserMessage, consumedImmediate map[int64]struct{}) (sessionclient.UserMessage, bool, int64, bool) {
	msg, ok, skipCursor, immediate := agent.SelectNextUserMessage(toSDKUserMessages(messages), consumedImmediate)
	if !ok {
		return sessionclient.UserMessage{}, false, skipCursor, immediate
	}
	for _, original := range messages {
		if original.ID == msg.ID {
			return original, true, skipCursor, immediate
		}
	}
	return sessionclient.UserMessage{}, false, skipCursor, immediate
}

// nextTurnStartingUserMessage selects the message that opens a new turn when
// no turn is running: the oldest pending message with content, in creation
// order. Unlike nextPendingUserMessage it gives immediate (steering) messages
// no priority — steering only means "interrupt the work in progress", and at a
// turn boundary there is nothing to interrupt. Letting an immediate message
// jump ahead here inverts the conversation at startup: a steer typed while the
// sandbox was still provisioning would become the turn prompt, leaving the
// seeded kickoff request pending until the agent finished and then re-running
// it as a second turn. Any immediate message queued behind the selected prompt
// is left untouched; the runner's ImmediateInputPoller folds it into the same
// turn before the first model call.
//
// Immediate messages already folded into a previous turn (handledImmediate)
// are skipped. The returned bool reports whether the selection is immediate so
// callers can record it in handledImmediate once the claim succeeds.
func nextTurnStartingUserMessage(messages []sessionclient.UserMessage, handledImmediate map[int64]struct{}) (sessionclient.UserMessage, bool, bool) {
	for _, msg := range messages {
		if strings.TrimSpace(msg.Content) == "" && len(msg.Images) == 0 {
			continue
		}
		immediate := msg.Mode == sessionclient.UserMessageModeImmediate
		if immediate {
			if _, seen := handledImmediate[msg.ID]; seen {
				continue
			}
		}
		return msg, true, immediate
	}
	return sessionclient.UserMessage{}, false, false
}

// withoutStoppedMessage drops the prompt the user explicitly stopped
// (WorkingState.LastStoppedUserMessageID) from a queue snapshot. Every stop
// path completes that message's claim, but if the completion was lost the
// replacement pod's RecoverClaimedUserMessages hands the prompt back as
// pending — and re-running a stopped prompt is exactly what the user asked
// not to happen. Only that one ID is removed: an earlier queued message can
// legitimately still be pending (a later immediate steer is selected ahead
// of it), so this is deliberately not an ID floor.
func withoutStoppedMessage(messages []sessionclient.UserMessage, stoppedMessageID int64) []sessionclient.UserMessage {
	if stoppedMessageID == 0 {
		return messages
	}
	out := make([]sessionclient.UserMessage, 0, len(messages))
	for _, msg := range messages {
		if msg.ID == stoppedMessageID {
			continue
		}
		out = append(out, msg)
	}
	return out
}

// collectImmediateRunItems converts unconsumed immediate user messages into run
// items. Alongside the SDK result it reports which message IDs were newly
// consumed so the caller can record their delivery.
func collectImmediateRunItems(messages []sessionclient.UserMessage, consumedImmediate map[int64]struct{}) ([]agent.RunItem, []int64, int64) {
	probe := make(map[int64]struct{}, len(consumedImmediate))
	for id := range consumedImmediate {
		probe[id] = struct{}{}
	}
	items, cursor := agent.CollectImmediateRunItems(toSDKUserMessages(messages), probe)
	var consumedIDs []int64
	for _, msg := range messages {
		if _, already := consumedImmediate[msg.ID]; already {
			continue
		}
		if _, selected := probe[msg.ID]; selected {
			consumedIDs = append(consumedIDs, msg.ID)
		}
	}
	return items, consumedIDs, cursor
}

func toSDKUserMessages(messages []sessionclient.UserMessage) []agent.UserMessage {
	out := make([]agent.UserMessage, 0, len(messages))
	for _, msg := range messages {
		out = append(out, agent.UserMessage{
			ID:      msg.ID,
			Content: msg.Content,
			Mode:    string(msg.Mode),
			Images:  toSDKImageAttachments(msg.Images),
		})
	}
	return out
}

// toSDKImageAttachments converts stored message images into SDK image
// attachments for delivery to vision-capable models.
func toSDKImageAttachments(images []sessionclient.MessageImage) []agent.ImageAttachment {
	if len(images) == 0 {
		return nil
	}
	out := make([]agent.ImageAttachment, 0, len(images))
	for _, img := range images {
		if img.Data == "" {
			continue
		}
		out = append(out, agent.ImageAttachment{
			MediaType: img.MediaType,
			Data:      img.Data,
		})
	}
	return out
}
