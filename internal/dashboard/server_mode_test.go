package dashboard

import "testing"

func TestParseModeCommand(t *testing.T) {
	tests := []struct {
		input string
		want  string
		ok    bool
	}{
		{"/mode eco", "eco", true},
		{"/mode autopilot", "autopilot", true},
		{"/MODE ECO", "eco", true},
		{" /mode debug ", "debug", true},
		{"/plan", "", false},
		{"hello", "", false},
		{"/mode ", "", false},
		{"/mode", "", false},
	}
	for _, tt := range tests {
		got, ok := parseModeCommand(tt.input)
		if ok != tt.ok || got != tt.want {
			t.Errorf("parseModeCommand(%q) = (%q, %v), want (%q, %v)", tt.input, got, ok, tt.want, tt.ok)
		}
	}
}

func TestParseAutopilotCommand(t *testing.T) {
	tests := []struct {
		input string
		want  string
		ok    bool
	}{
		{"/autopilot", "autopilot", true},
		{"/AUTOPILOT", "autopilot", true},
		{" /autopilot ", "autopilot", true},
		{"/stop", "", false},
		{"/STOP", "", false},
		{"/plan", "", false},
		{"/mode autopilot", "", false},
		{"hello", "", false},
	}
	for _, tt := range tests {
		got, ok := parseAutopilotCommand(tt.input)
		if ok != tt.ok || got != tt.want {
			t.Errorf("parseAutopilotCommand(%q) = (%q, %v), want (%q, %v)", tt.input, got, ok, tt.want, tt.ok)
		}
	}
}
