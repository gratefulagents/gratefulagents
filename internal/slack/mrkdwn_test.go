package slack

import "testing"

func TestToMrkdwn(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"plain", "hello world", "hello world"},
		{"bold", "this is **bold** text", "this is *bold* text"},
		{"bold underscores", "this is __bold__ text", "this is *bold* text"},
		{"two bolds", "**a** and **b**", "*a* and *b*"},
		{"strike", "~~gone~~", "~gone~"},
		{"link", "see [docs](https://example.com/x) now", "see <https://example.com/x|docs> now"},
		{"heading", "## Summary", "*Summary*"},
		{"heading with bold", "# The **plan**", "*The *plan**"},
		{"bullet dash", "- item one", "• item one"},
		{"bullet star", "* item one", "• item one"},
		{"nested bullet", "  - sub", "  • sub"},
		{"numbered list untouched", "1. first", "1. first"},
		{
			"fence language stripped",
			"```go\nfmt.Println(\"**hi**\")\n```",
			"```\nfmt.Println(\"**hi**\")\n```",
		},
		{
			"fence content preserved",
			"before **b**\n```\n- not a bullet\n**not bold**\n```\nafter **b**",
			"before *b*\n```\n- not a bullet\n**not bold**\n```\nafter *b*",
		},
		{"inline code preserved", "run `x **y**` now **z**", "run `x **y**` now *z*"},
		{"italic single star untouched", "*emphasis*", "*emphasis*"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ToMrkdwn(tc.in); got != tc.want {
				t.Fatalf("ToMrkdwn(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
