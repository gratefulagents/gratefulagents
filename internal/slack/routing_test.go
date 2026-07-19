package slack

import "testing"

func TestRoute(t *testing.T) {
	cfg := RouterConfig{
		OwnerUserID:    "UOWNER",
		BotUserID:      "UBOT",
		BotDMChannelID: "DBOTDM",
	}

	tests := []struct {
		name     string
		msg      InboundMessage
		cfg      RouterConfig
		wantKind RouteKind
	}{
		{
			name:     "owner DM to bot is a command",
			msg:      InboundMessage{ChannelType: "im", ChannelID: "DBOTDM", UserID: "UOWNER", Text: "do a thing", TS: "1.1"},
			cfg:      cfg,
			wantKind: RouteCommand,
		},
		{
			// Owner DMing the bot stays a command via the bot event even when
			// the control-DM channel could not be resolved at startup.
			name:     "bot-event DM from owner is a command",
			msg:      InboundMessage{ChannelType: "im", ChannelID: "DUNKNOWN", UserID: "UOWNER", Text: "do a thing", TS: "2.1", ViaBotEvent: true},
			cfg:      RouterConfig{OwnerUserID: "UOWNER", BotUserID: "UBOT"},
			wantKind: RouteCommand,
		},
		{
			// Any workspace member can open a DM with the bot. Fail closed.
			name:     "bot-event DM from non-owner is declined",
			msg:      InboundMessage{ChannelType: "im", ChannelID: "DBOBBOT", UserID: "UBOB", Text: "do a thing", TS: "2.2", ViaBotEvent: true},
			cfg:      cfg,
			wantKind: RouteDecline,
		},
		{
			// No owner identity to check against → nobody commands via DM.
			name:     "bot-event DM is declined when owner unknown",
			msg:      InboundMessage{ChannelType: "im", ChannelID: "DSOMEONE", UserID: "USOMEONE", Text: "hi", TS: "2.3", ViaBotEvent: true},
			cfg:      RouterConfig{BotUserID: "UBOT"},
			wantKind: RouteDecline,
		},
		{
			// A legacy app manifest may still subscribe to the owner's user-token
			// im events (the removed inbox-monitoring surface). Those are the
			// owner's DMs with other people and must never be routed.
			name:     "user-token subscription DM from third party is ignored",
			msg:      InboundMessage{ChannelType: "im", ChannelID: "DALICE", UserID: "UBOB", Text: "hey alice", TS: "3.1"},
			cfg:      cfg,
			wantKind: RouteIgnore,
		},
		{
			name:     "user-token subscription DM from owner is ignored",
			msg:      InboundMessage{ChannelType: "im", ChannelID: "DALICE", UserID: "UOWNER", Text: "hi bob", TS: "3.2"},
			cfg:      cfg,
			wantKind: RouteIgnore,
		},
		{
			name:     "self message ignored",
			msg:      InboundMessage{ChannelType: "im", ChannelID: "DBOTDM", UserID: "UBOT", Text: "hi", TS: "5.1"},
			cfg:      cfg,
			wantKind: RouteIgnore,
		},
		{
			name:     "bot message ignored",
			msg:      InboundMessage{ChannelType: "im", ChannelID: "DBOTDM", BotID: "B123", Text: "auto", TS: "6.1"},
			cfg:      cfg,
			wantKind: RouteIgnore,
		},
		{
			name:     "message edit subtype ignored",
			msg:      InboundMessage{ChannelType: "im", ChannelID: "DBOTDM", UserID: "UOWNER", SubType: "message_changed", Text: "edited", TS: "7.1", ViaBotEvent: true},
			cfg:      cfg,
			wantKind: RouteIgnore,
		},
		{
			name:     "app mention from owner is a command",
			msg:      InboundMessage{ChannelType: "channel", ChannelID: "CTEAM", UserID: "UOWNER", Text: "<@UBOT> ship it", TS: "8.1", IsAppMention: true},
			cfg:      cfg,
			wantKind: RouteCommand,
		},
		{
			name:     "app mention from non-owner is declined (fail closed)",
			msg:      InboundMessage{ChannelType: "channel", ChannelID: "CTEAM", UserID: "UBOB", Text: "<@UBOT> ship it", TS: "8.2", IsAppMention: true},
			cfg:      cfg,
			wantKind: RouteDecline,
		},
		{
			name:     "plain channel message without mention ignored",
			msg:      InboundMessage{ChannelType: "channel", ChannelID: "CTEAM", UserID: "UBOB", Text: "chatter", TS: "9.1"},
			cfg:      cfg,
			wantKind: RouteIgnore,
		},
		{
			name:     "file_share subtype in owner DM is a command",
			msg:      InboundMessage{ChannelType: "im", ChannelID: "DBOTDM", UserID: "UOWNER", SubType: "file_share", Text: "look at this", TS: "16.1", Files: []File{{ID: "F1", Name: "log.txt"}}},
			cfg:      cfg,
			wantKind: RouteCommand,
		},
		{
			name:     "file_share via user-token subscription is ignored",
			msg:      InboundMessage{ChannelType: "im", ChannelID: "DALICE", UserID: "UBOB", SubType: "file_share", Text: "see attached", TS: "16.2"},
			cfg:      cfg,
			wantKind: RouteIgnore,
		},
		{
			name:     "mention from commander is a command",
			msg:      InboundMessage{ChannelType: "channel", ChannelID: "CTEAM", UserID: "UCAROL", Text: "<@UBOT> go", TS: "17.1", IsAppMention: true},
			cfg:      RouterConfig{OwnerUserID: "UOWNER", BotUserID: "UBOT", Commanders: []string{"UCAROL"}},
			wantKind: RouteCommand,
		},
		{
			name:     "mention from non-commander is declined",
			msg:      InboundMessage{ChannelType: "channel", ChannelID: "CTEAM", UserID: "UBOB", Text: "<@UBOT> go", TS: "17.2", IsAppMention: true},
			cfg:      RouterConfig{OwnerUserID: "UOWNER", BotUserID: "UBOT", Commanders: []string{"UCAROL"}},
			wantKind: RouteDecline,
		},
		{
			name:     "owner always commands despite commanders list",
			msg:      InboundMessage{ChannelType: "channel", ChannelID: "CTEAM", UserID: "UOWNER", Text: "<@UBOT> go", TS: "17.3", IsAppMention: true},
			cfg:      RouterConfig{OwnerUserID: "UOWNER", BotUserID: "UBOT", Commanders: []string{"UCAROL"}},
			wantKind: RouteCommand,
		},
		{
			name:     "empty commanders is owner-only (fail closed)",
			msg:      InboundMessage{ChannelType: "channel", ChannelID: "CTEAM", UserID: "UBOB", Text: "<@UBOT> go", TS: "17.4", IsAppMention: true},
			cfg:      RouterConfig{OwnerUserID: "UOWNER", BotUserID: "UBOT"},
			wantKind: RouteDecline,
		},
		{
			name:     "unresolved owner declines mentions (fail closed)",
			msg:      InboundMessage{ChannelType: "channel", ChannelID: "CTEAM", UserID: "UBOB", Text: "<@UBOT> go", TS: "17.5", IsAppMention: true},
			cfg:      RouterConfig{BotUserID: "UBOT"},
			wantKind: RouteDecline,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Route(tt.msg, tt.cfg)
			if got.Kind != tt.wantKind {
				t.Fatalf("Route() kind = %q (%s), want %q", got.Kind, got.Reason, tt.wantKind)
			}
		})
	}
}

func TestRouteResolvesThreadRoot(t *testing.T) {
	cfg := RouterConfig{OwnerUserID: "UOWNER", BotUserID: "UBOT", BotDMChannelID: "DBOTDM"}

	// A reply in an existing thread keeps the thread root.
	d := Route(InboundMessage{ChannelType: "im", ChannelID: "DBOTDM", UserID: "UOWNER", TS: "10.2", ThreadTS: "10.1"}, cfg)
	if d.ThreadTS != "10.1" {
		t.Fatalf("ThreadTS = %q, want 10.1", d.ThreadTS)
	}

	// A top-level message uses its own ts as the thread root.
	d = Route(InboundMessage{ChannelType: "im", ChannelID: "DBOTDM", UserID: "UOWNER", TS: "11.1"}, cfg)
	if d.ThreadTS != "11.1" {
		t.Fatalf("ThreadTS = %q, want 11.1", d.ThreadTS)
	}
	if d.MessageTS != "11.1" {
		t.Fatalf("MessageTS = %q, want 11.1", d.MessageTS)
	}
}

func TestStripBotMention(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"<@UBOT> ship it", "ship it"},
		{"  <@UBOT>   hello  ", "hello"},
		{"no mention here", "no mention here"},
		{"<@UBOT>", ""},
	}
	for _, tt := range tests {
		if got := stripBotMention(tt.in); got != tt.want {
			t.Errorf("stripBotMention(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestConversationThreadKey(t *testing.T) {
	tests := []struct {
		channelType, threadTS, want string
	}{
		{"im", "1700.1", ""},            // DM: one run per conversation, threads ignored
		{"mpim", "1700.1", ""},          // group DM: same
		{"channel", "1700.1", "1700.1"}, // channel: one run per thread
		{"group", "1700.1", "1700.1"},   // legacy private channel: per thread
		{"channel", "", ""},             // non-threaded channel message keys to itself upstream
	}
	for _, tt := range tests {
		if got := ConversationThreadKey(tt.channelType, tt.threadTS); got != tt.want {
			t.Errorf("ConversationThreadKey(%q,%q) = %q, want %q", tt.channelType, tt.threadTS, got, tt.want)
		}
	}
}

func TestRouteCarriesChannelType(t *testing.T) {
	cfg := RouterConfig{OwnerUserID: "UOWNER", BotUserID: "UBOT", BotDMChannelID: "DBOTDM"}
	d := Route(InboundMessage{ChannelType: "im", ChannelID: "DBOTDM", UserID: "UOWNER", Text: "hi", TS: "1.1"}, cfg)
	if d.ChannelType != "im" {
		t.Fatalf("Decision.ChannelType = %q, want im", d.ChannelType)
	}
}

// Owner-unknown DM declines must carry ReasonOwnerUnknown so the dispatcher
// logs the setup problem instead of the generic decline.
func TestRouteDeclineReasonOwnerUnknown(t *testing.T) {
	d := Route(
		InboundMessage{ChannelType: "im", ChannelID: "D1", UserID: "U1", Text: "hi", TS: "1.1", ViaBotEvent: true},
		RouterConfig{BotUserID: "UBOT"},
	)
	if d.Kind != RouteDecline || d.Reason != ReasonOwnerUnknown {
		t.Fatalf("got %s/%q, want %s/%q", d.Kind, d.Reason, RouteDecline, ReasonOwnerUnknown)
	}
}
