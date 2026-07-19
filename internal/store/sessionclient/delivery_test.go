package sessionclient

import (
	"encoding/json"
	"testing"
)

func TestUserMessageStateFromMetadata(t *testing.T) {
	tests := []struct {
		name          string
		metadata      json.RawMessage
		wantMode      UserMessageMode
		wantDelivered int64
	}{
		{name: "nil metadata", metadata: nil, wantMode: UserMessageModeEnqueue, wantDelivered: 0},
		{name: "empty object", metadata: json.RawMessage(`{}`), wantMode: UserMessageModeEnqueue, wantDelivered: 0},
		{name: "invalid json", metadata: json.RawMessage(`{`), wantMode: UserMessageModeEnqueue, wantDelivered: 0},
		{
			name:          "undelivered immediate",
			metadata:      EncodeUserMessageMetadata(UserMessageModeImmediate),
			wantMode:      UserMessageModeImmediate,
			wantDelivered: 0,
		},
		{
			name:          "delivered enqueue",
			metadata:      json.RawMessage(`{"mode":"enqueue","delivered_at_unix":1700000000}`),
			wantMode:      UserMessageModeEnqueue,
			wantDelivered: 1700000000,
		},
		{
			name:          "delivered immediate with images",
			metadata:      json.RawMessage(`{"mode":"immediate","images":[{"media_type":"image/png","data":"AQID"}],"delivered_at_unix":42}`),
			wantMode:      UserMessageModeImmediate,
			wantDelivered: 42,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mode, delivered := UserMessageStateFromMetadata(tt.metadata)
			if mode != tt.wantMode {
				t.Errorf("mode = %q, want %q", mode, tt.wantMode)
			}
			if delivered != tt.wantDelivered {
				t.Errorf("delivered = %d, want %d", delivered, tt.wantDelivered)
			}
		})
	}
}
