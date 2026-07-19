package main

import (
	"strings"

	"github.com/gratefulagents/gratefulagents/internal/store"
	"github.com/gratefulagents/gratefulagents/internal/store/sessionclient"
	agent "github.com/gratefulagents/sdk/pkg/agentsdk"
)

const recentConversationMessageLimit = agent.DefaultRecentConversationLimit

func buildConversationTail(messages []store.Message, state sessionclient.WorkingState, excludeMessageID int64, limit int) []agent.RunItem {
	return agent.BuildConversationTail(toSDKConversationMessages(messages), toSDKWorkingState(state), excludeMessageID, limit)
}

// buildTurnInput selects the base conversation context for a turn: when the
// in-memory session transcript is non-empty it is replayed verbatim
// (full-transcript replay) so tool calls, outputs,
// and mid-run compaction summaries survive turn boundaries; otherwise (pod
// restart, external context clear, interrupted run) it falls back to the
// durable conversation tail. Durable messages recorded out-of-band since the
// transcript was captured are folded into the transcript by the loop before
// this replay — see outOfBandMessageItems.
func buildTurnInput(transcript []agent.RunItem, messages []store.Message, state sessionclient.WorkingState, excludeMessageID int64, limit int) []agent.RunItem {
	if len(transcript) > 0 {
		return append([]agent.RunItem(nil), transcript...)
	}
	if len(messages) == 0 {
		return nil
	}
	return buildConversationTail(messages, state, excludeMessageID, limit)
}

// transcriptAfterRun returns the in-memory session transcript to carry into
// the next turn: the runner's post-run conversation state (the input as last
// used after any mid-run compaction, plus every generated item). Interrupted
// runs end with an unresolved tool approval — an unpaired tool_use providers
// reject on replay — so they reset the transcript to the durable-tail
// fallback.
func transcriptAfterRun(result *agent.RunResult) []agent.RunItem {
	if result == nil || result.IsInterrupted() {
		return nil
	}
	return result.FinalHistory
}

// outOfBandMessageItems converts durable messages recorded after the
// in-memory transcript was last captured (ID > seenThroughID) into run
// items for transcript replay. Some UI actions record context only as
// durable messages without starting a turn — rejecting a plan appends a
// system "Plan rejected…" message, a mode switch appends an assistant
// note — so they appear in neither FinalHistory nor the current user item.
// Skipped:
//   - user messages: each is consumed by the reply queue as a turn prompt
//     or an immediate mid-run injection, so folding would duplicate it;
//   - selfAssistantMessageID: the loop's own durable append of the previous
//     turn's reply, whose content is already in FinalHistory.
func outOfBandMessageItems(messages []store.Message, seenThroughID, selfAssistantMessageID int64, state sessionclient.WorkingState) []agent.RunItem {
	newer := make([]store.Message, 0, len(messages))
	for _, msg := range messages {
		if msg.ID <= seenThroughID || msg.ID == selfAssistantMessageID || msg.Role == "user" {
			continue
		}
		newer = append(newer, msg)
	}
	if len(newer) == 0 {
		return nil
	}
	return buildConversationTail(newer, state, 0, len(newer))
}

// maxSeenMessageID advances the transcript watermark over every durable
// message visible this turn: after the input is built they are all
// represented in the model's context (transcript fold, durable tail, or the
// current user item), so future turns must not fold them again.
func maxSeenMessageID(current int64, messages []store.Message) int64 {
	for _, msg := range messages {
		if msg.ID > current {
			current = msg.ID
		}
	}
	return current
}

func buildWorkingStateContext(state sessionclient.WorkingState) string {
	return agent.BuildWorkingStateContext(toSDKWorkingState(state))
}

func deriveWorkingStateGoal(rawReply, effectivePrompt string) string {
	return agent.DeriveWorkingStateGoal(rawReply, effectivePrompt)
}

func buildAssistantTurnSummary(items []agent.RunItem) string {
	return agent.BuildAssistantTurnSummary(items)
}

func toSDKConversationMessages(messages []store.Message) []agent.ConversationMessage {
	out := make([]agent.ConversationMessage, 0, len(messages))
	for _, msg := range messages {
		out = append(out, agent.ConversationMessage{
			ID:      msg.ID,
			Role:    msg.Role,
			Content: msg.Content,
		})
	}
	return out
}

func toSDKWorkingState(state sessionclient.WorkingState) agent.WorkingState {
	return agent.WorkingState{
		Goal:                  state.Goal,
		CurrentMode:           state.CurrentMode,
		LastUserMessage:       state.LastUserMessage,
		LastAssistantSummary:  state.LastAssistantSummary,
		RecentTurnSummaries:   append([]string(nil), state.RecentTurnSummaries...),
		HistoryFloorMessageID: state.HistoryFloorMessageID,
		LastResponseID:        state.LastResponseID,
	}
}

func dedupeNonEmptyStrings(values []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}
