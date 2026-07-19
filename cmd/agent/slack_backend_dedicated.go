package main

import (
	"context"
	"log"

	internalslack "github.com/gratefulagents/gratefulagents/internal/slack"
	slackgo "github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
)

// dedicatedSlackBackend serves a single SlackAgent with its own dedicated Slack
// app: every event on the socket belongs to this one owner.
type dedicatedSlackBackend struct {
	cfg         slackConnectorConfig
	web         *internalslack.Client
	botUserID   string
	ownerUserID string
	// botDMChannelID is the owner<->bot control DM, resolved at startup.
	botDMChannelID string
	// orch handles owner commands (create/wake AgentRuns, stream replies). It is
	// nil when its dependencies (Postgres/CRD client) are unavailable.
	orch *slackOrchestrator
}

// allowTeam accepts all teams: a dedicated app is single-workspace by
// construction (non-distributed), so no pinning is needed.
func (b *dedicatedSlackBackend) allowTeam(string) bool { return true }

// routerConfig snapshots the identities the router needs for the current event.
func (b *dedicatedSlackBackend) routerConfig() internalslack.RouterConfig {
	return internalslack.RouterConfig{
		OwnerUserID:    b.ownerUserID,
		BotUserID:      b.botUserID,
		BotDMChannelID: b.botDMChannelID,
		Commanders:     b.cfg.Commanders,
	}
}

func (b *dedicatedSlackBackend) handleMessage(ctx context.Context, msg internalslack.InboundMessage) {
	dispatchSlackDecision(ctx, b.web, b.cfg.AgentName, b.orch, internalslack.Route(msg, b.routerConfig()))
}

func (b *dedicatedSlackBackend) handleInteraction(ctx context.Context, callback slackgo.InteractionCallback) {
	if b.orch == nil {
		return
	}
	b.orch.handleInteraction(ctx, callback)
}

// handleAssistantContextChanged remembers which channel the user is viewing
// alongside an assistant-pane thread so the orchestrator can inject it into run
// context.
func (b *dedicatedSlackBackend) handleAssistantContextChanged(thread slackevents.AssistantThread) {
	if b.orch == nil || thread.ChannelID == "" || thread.ThreadTimeStamp == "" {
		return
	}
	b.orch.setAssistantContext(thread.ChannelID, thread.ThreadTimeStamp, thread.Context.ChannelID)
}

// handleAssistantStarted greets the user when they open the assistant pane and
// offers suggested prompts.
func (b *dedicatedSlackBackend) handleAssistantStarted(
	ctx context.Context, e *slackevents.AssistantThreadStartedEvent,
) {
	const greeting = "Hi! I'm your agent. Ask me to do something, or pick a prompt below."
	postAssistantGreeting(ctx, b.web, b.cfg.AgentName, e, greeting)
}

func (b *dedicatedSlackBackend) handleAppHome(ctx context.Context, userID string) {
	if b.orch == nil {
		return
	}
	b.orch.handleAppHome(ctx, userID)
}

// dispatchSlackDecision acts on a routing decision by enqueuing it to the
// owning orchestrator's conversation worker, which serializes and coalesces the
// conversation's turns. Shared by the dedicated and workspace backends.
func dispatchSlackDecision(
	ctx context.Context, web *internalslack.Client, name string, orch *slackOrchestrator, d internalslack.Decision,
) {
	switch d.Kind {
	case internalslack.RouteCommand:
		if orch == nil {
			log.Printf("slack connector %s: %s in %s ignored (handling disabled)", name, d.Kind, d.ChannelID)
			return
		}
		// Dedupe Slack redeliveries of the same message so a retry doesn't spawn
		// a second run.
		if !orch.claimEvent(ctx, d.ChannelID, d.MessageTS) {
			log.Printf("slack connector %s: skipping already-handled message %s/%s", name, d.ChannelID, d.MessageTS)
			return
		}
		orch.enqueueConversation(ctx, d)
	case internalslack.RouteDecline:
		// Explicit invocation by someone not allowed to command this agent.
		// Stay completely silent — no reply, no ephemeral — so the agent never
		// reveals itself or its policy to unauthorized senders. The decline is
		// only logged (including ReasonOwnerUnknown, which means the connector
		// has no owner Slack user ID configured and declines everyone).
		log.Printf("slack connector %s: silently declined message from %s in %s (%s)",
			name, d.UserID, d.ChannelID, d.Reason)
	default:
		log.Printf("slack connector %s: ignored message in %s (%s)", name, d.ChannelID, d.Reason)
	}
}

// postAssistantGreeting posts the assistant-pane greeting plus suggested
// prompts. Shared by both backends.
func postAssistantGreeting(
	ctx context.Context, web *internalslack.Client, name string,
	e *slackevents.AssistantThreadStartedEvent, greeting string,
) {
	channelID := e.AssistantThread.ChannelID
	threadTS := e.AssistantThread.ThreadTimeStamp
	if channelID == "" || threadTS == "" {
		return
	}
	if _, err := web.PostMessageAsBot(ctx, channelID, greeting, threadTS); err != nil {
		log.Printf("slack connector %s: assistant greeting: %v", name, err)
	}
	prompts := []internalslack.AssistantPrompt{
		{Title: "What needs my attention?", Message: "What in my Slack needs my attention right now?"},
		{Title: "Draft a reply", Message: "Help me draft a reply to my most recent DM."},
		{Title: "Summarize a channel", Message: "Summarize the recent activity in the channel I'm viewing."},
	}
	if err := web.SetAssistantSuggestedPrompts(ctx, channelID, threadTS, "Try one of these", prompts); err != nil {
		log.Printf("slack connector %s: set suggested prompts: %v", name, err)
	}
}
