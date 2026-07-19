package main

import (
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
