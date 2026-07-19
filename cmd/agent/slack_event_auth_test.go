package main

import (
	"encoding/json"
	"testing"
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
