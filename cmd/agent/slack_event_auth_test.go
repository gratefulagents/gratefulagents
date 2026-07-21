package main

import (
	"encoding/json"
	"testing"

	"github.com/slack-go/slack/slackevents"
)

// slackEventViaBot distinguishes the bot's event subscription (DMs with the
// bot: a command surface) from a user-token subscription a legacy app manifest
// may still carry (the owner's own DMs, which are never routed). It must fail
// closed — report a bot event — whenever the envelope carries no usable
// authorization.
func TestSlackEventViaBot(t *testing.T) {
	tests := []struct {
		name    string
		payload string
		want    bool
	}{
		{
			name:    "bot authorization is a bot event",
			payload: `{"authorizations":[{"enterprise_id":null,"team_id":"T1","user_id":"UBOT","is_bot":true}],"event":{"type":"message"}}`,
			want:    true,
		},
		{
			name:    "user-token authorization is not a bot event",
			payload: `{"authorizations":[{"team_id":"T1","user_id":"UOWNER","is_bot":false}],"event":{"type":"message"}}`,
			want:    false,
		},
		{
			name:    "missing authorizations fails closed as bot event",
			payload: `{"event":{"type":"message"}}`,
			want:    true,
		},
		{
			name:    "empty authorizations fails closed as bot event",
			payload: `{"authorizations":[],"event":{"type":"message"}}`,
			want:    true,
		},
		{
			name:    "malformed payload fails closed as bot event",
			payload: `{"authorizations":`,
			want:    true,
		},
		{
			name:    "empty payload fails closed as bot event",
			payload: "",
			want:    true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := slackEventViaBot(json.RawMessage(tc.payload)); got != tc.want {
				t.Fatalf("slackEventViaBot(%s) = %v, want %v", tc.payload, got, tc.want)
			}
		})
	}
}

func TestSlackEventContextChannel(t *testing.T) {
	tests := []struct {
		name    string
		payload string
		want    string
	}{
		{
			name: "direct channel entity",
			payload: `{"event":{"type":"message","app_context":{"entities":[` +
				`{"type":"slack#/types/channel_id","value":"C123","team_id":"T1"}]}}}`,
			want: "C123",
		},
		{
			name: "message context entity",
			payload: `{"event":{"type":"message","app_context":{"entities":[` +
				`{"type":"slack#/types/message_context","value":{"message_ts":"1.2","channel_id":"C456"}}]}}}`,
			want: "C456",
		},
		{
			name: "first relevant channel wins",
			payload: `{"event":{"type":"message","app_context":{"entities":[` +
				`{"type":"slack#/types/canvas_id","value":"F1"},` +
				`{"type":"slack#/types/channel_id","value":"C789"},` +
				`{"type":"slack#/types/channel_id","value":"C999"}]}}}`,
			want: "C789",
		},
		{name: "missing context", payload: `{"event":{"type":"message"}}`},
		{name: "malformed payload", payload: `{"event":`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := slackEventContextChannel(json.RawMessage(tc.payload)); got != tc.want {
				t.Fatalf("slackEventContextChannel() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSlackGoParsesAppContextChanged(t *testing.T) {
	payload := json.RawMessage(`{
		"token":"verification-token",
		"team_id":"T123",
		"api_app_id":"A123",
		"type":"event_callback",
		"event":{"type":"app_context_changed","context":{"entities":[]}}
	}`)
	event, err := slackevents.ParseEvent(payload, slackevents.OptionNoVerifyToken())
	if err != nil {
		t.Fatalf("ParseEvent(app_context_changed): %v", err)
	}
	if _, ok := event.InnerEvent.Data.(*slackAppContextChangedEvent); !ok {
		t.Fatalf("inner event type = %T, want *slackAppContextChangedEvent", event.InnerEvent.Data)
	}
}
