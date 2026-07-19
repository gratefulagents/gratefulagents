package slack

import "strings"

// RouteKind classifies how the connector should act on an inbound Slack message.
type RouteKind string

const (
	// RouteIgnore means the message must not trigger any action (self messages,
	// edits/deletes, bot messages, user-token subscription events, etc.).
	RouteIgnore RouteKind = "ignore"

	// RouteCommand means the owner is talking to their agent (control channel:
	// a DM to the bot, or an @mention). The connector handles it conversationally
	// and may spawn a child AgentRun for heavy work.
	RouteCommand RouteKind = "command"

	// RouteDecline means the message explicitly invoked the agent but the sender
	// is not allowed to command it (not the owner or a listed commander). The
	// connector stays completely silent — declines are logged, never answered —
	// so the agent is invisible to unauthorized senders.
	RouteDecline RouteKind = "decline"
)

// ReasonOwnerUnknown marks a declined bot DM when the connector has no owner
// identity to check against (no spec.slackUserId and no user token to resolve
// it). Fail closed: without a known owner, nobody may command via DM. The
// reason appears only in connector logs so operators can spot the
// misconfiguration; nothing is ever posted back to Slack.
const ReasonOwnerUnknown = "owner identity unknown"

// File is a transport-agnostic view of a Slack file attached to a message. Only
// the fields the connector needs to decide whether/how to ingest the file.
type File struct {
	ID       string
	Name     string
	Mimetype string
	Filetype string
	Size     int
	// URLPrivate is the authenticated download URL (requires a bot/user token).
	URLPrivate string
}

// InboundMessage is a transport-agnostic view of a Slack message event, decoded
// from either a message.* event or an app_mention event. Keeping routing decoupled
// from slack-go's concrete structs makes the decision logic easy to unit-test.
type InboundMessage struct {
	ChannelType  string // "im", "channel", "group", "mpim"
	ChannelID    string
	UserID       string // author of the message
	BotID        string // non-empty when the message was posted by a bot/app
	Text         string
	TS           string
	ThreadTS     string
	SubType      string // message subtype: edits/deletes/joins/bot_message/...
	IsAppMention bool
	// ViaBotEvent reports whether the event was delivered for the bot's own
	// event subscription (envelope authorizations[].is_bot) rather than a
	// user-token subscription a legacy app manifest may still carry. Bot im
	// events are DMs with the bot (a command surface); user-token im events
	// are the owner's own DMs with other people and are never routed.
	ViaBotEvent bool
	// Files lists any attachments on the message (subtype file_share carries
	// them); the connector may ingest small text files into the run's context.
	Files []File
}

// RouterConfig carries the identities the router needs to classify a message.
type RouterConfig struct {
	// OwnerUserID is the Slack user ID of the human who owns this agent.
	OwnerUserID string
	// BotUserID is our bot's Slack user ID, used to ignore the bot's own messages.
	BotUserID string
	// BotDMChannelID is the DM channel between the owner and our bot (the control
	// channel). Messages here from the owner are commands; it distinguishes the
	// control channel from the owner's DMs with other people.
	BotDMChannelID string
	// Commanders lists who may command the agent via channel @mentions besides
	// the owner. Fail-closed: with an empty list only the owner is allowed.
	Commanders []string
}

// Decision is the routing outcome plus the resolved targets for a reply.
type Decision struct {
	Kind        RouteKind
	ChannelType string // "im", "channel", "group", "mpim" — used to key the run
	ChannelID   string
	ThreadTS    string // resolved thread root to reply in (ThreadTS or TS)
	MessageTS   string // ts of the triggering message (for reactions)
	UserID      string
	Text        string
	Reason      string // human-readable explanation, useful for logs and tests
	Files       []File // attachments carried through from the inbound message
}

// Route classifies an inbound message. It is a pure function: same inputs always
// produce the same decision, with no I/O.
func Route(msg InboundMessage, cfg RouterConfig) Decision {
	threadTS := firstNonEmptyStr(msg.ThreadTS, msg.TS)
	base := Decision{ChannelType: msg.ChannelType, ChannelID: msg.ChannelID, ThreadTS: threadTS, MessageTS: msg.TS, UserID: msg.UserID, Text: msg.Text, Files: msg.Files}

	ignore := func(reason string) Decision {
		d := base
		d.Kind = RouteIgnore
		d.Reason = reason
		return d
	}

	// Never react to our own bot, nor to any bot/app message: prevents loops.
	if msg.UserID != "" && msg.UserID == cfg.BotUserID {
		return ignore("self message")
	}
	if strings.TrimSpace(msg.BotID) != "" {
		return ignore("bot message")
	}

	// app_mention is an explicit invocation of the agent → a command, but only
	// from allowed senders. Fail-closed: the owner and listed commanders may
	// command; everyone else (including when the owner is unresolved) is
	// declined.
	if msg.IsAppMention {
		d := base
		d.Text = stripBotMention(msg.Text)
		allowed := (cfg.OwnerUserID != "" && msg.UserID == cfg.OwnerUserID) ||
			containsUser(cfg.Commanders, msg.UserID)
		if !allowed {
			d.Kind = RouteDecline
			d.Reason = "sender not owner or commander"
			return d
		}
		d.Kind = RouteCommand
		d.Reason = "app mention"
		return d
	}

	// Only act on brand-new messages; ignore edits/deletes/joins/etc. File
	// uploads arrive as subtype file_share and are regular new messages.
	if st := strings.TrimSpace(msg.SubType); st != "" && st != "file_share" {
		return ignore("message subtype " + msg.SubType)
	}

	switch msg.ChannelType {
	case "im":
		// Control channel: the owner DMing our bot.
		if cfg.BotDMChannelID != "" && msg.ChannelID == cfg.BotDMChannelID {
			d := base
			d.Kind = RouteCommand
			d.Reason = "owner DM to bot"
			return d
		}
		// An im event that was NOT delivered for the bot's own subscription can
		// only come from a user-token subscription a legacy app manifest still
		// carries: one of the owner's DMs with another person. Those are never
		// routed anywhere (inbox monitoring was removed).
		if !msg.ViaBotEvent {
			return ignore("user-token subscription event")
		}
		// A DM with the bot itself: a command surface. Any workspace member can
		// open a DM with the bot, so gate commands on the sender being the owner
		// (fail closed, mirroring app_mention). When the owner identity is
		// unknown (no slackUserId configured and no user token to resolve it)
		// decline everything rather than take commands from anyone.
		d := base
		if cfg.OwnerUserID != "" && msg.UserID == cfg.OwnerUserID {
			d.Kind = RouteCommand
			d.Reason = "owner DM to bot"
			return d
		}
		d.Kind = RouteDecline
		d.Reason = "im sender not owner"
		if cfg.OwnerUserID == "" {
			d.Reason = ReasonOwnerUnknown
		}
		return d
	default:
		// Channel/group messages without an explicit mention are not for us.
		return ignore("non-DM without mention")
	}
}

// stripBotMention removes a leading <@BOTID> mention token from app_mention text.
func stripBotMention(text string) string {
	trimmed := strings.TrimSpace(text)
	if !strings.HasPrefix(trimmed, "<@") {
		return trimmed
	}
	if end := strings.IndexByte(trimmed, '>'); end >= 0 {
		return strings.TrimSpace(trimmed[end+1:])
	}
	return trimmed
}

func firstNonEmptyStr(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// containsUser reports whether the given Slack user ID appears in the list.
// Comparison is exact after trimming; Slack user IDs are canonical.
func containsUser(list []string, userID string) bool {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return false
	}
	for _, u := range list {
		if strings.TrimSpace(u) == userID {
			return true
		}
	}
	return false
}

// ConversationThreadKey returns the thread component used to key an AgentRun to a
// conversation. Direct messages (im) and group DMs (mpim) are treated as a single
// ongoing conversation regardless of threading — many people never use threads,
// so keying a DM by thread would spawn a new run per message. Channels host many
// parallel topics, so there each thread is its own conversation.
func ConversationThreadKey(channelType, threadTS string) string {
	switch channelType {
	case "im", "mpim":
		return "" // one run per DM/group-DM conversation
	default:
		return threadTS // one run per channel thread
	}
}
