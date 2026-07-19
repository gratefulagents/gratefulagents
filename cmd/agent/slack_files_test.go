package main

import (
	"strings"
	"testing"
	"unicode/utf8"

	internalslack "github.com/gratefulagents/gratefulagents/internal/slack"
)

func TestIsTextualSlackFile(t *testing.T) {
	cases := []struct {
		name string
		f    internalslack.File
		want bool
	}{
		{"plain text", internalslack.File{URLPrivate: "u", Size: 100, Mimetype: "text/plain"}, true},
		{"json", internalslack.File{URLPrivate: "u", Size: 100, Mimetype: "application/json"}, true},
		{
			"go snippet by filetype",
			internalslack.File{URLPrivate: "u", Size: 100, Mimetype: "application/octet-stream", Filetype: "go"},
			true,
		},
		{"image", internalslack.File{URLPrivate: "u", Size: 100, Mimetype: "image/png", Filetype: "png"}, false},
		{"too large", internalslack.File{URLPrivate: "u", Size: 10 << 20, Mimetype: "text/plain"}, false},
		{"no url", internalslack.File{Size: 100, Mimetype: "text/plain"}, false},
		{"binary", internalslack.File{URLPrivate: "u", Size: 100, Mimetype: "application/zip", Filetype: "zip"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isTextualSlackFile(tc.f); got != tc.want {
				t.Fatalf("isTextualSlackFile(%+v) = %v, want %v", tc.f, got, tc.want)
			}
		})
	}
}

func TestTruncateUTF8PreservesRuneBoundary(t *testing.T) {
	input := strings.Repeat("a", 4) + "🙂tail"
	got := truncateUTF8(input, 6, "…") // cuts through the four-byte emoji unless adjusted
	if !utf8.ValidString(got) {
		t.Fatalf("truncateUTF8() produced invalid UTF-8: %q", got)
	}
	if got != "aaaa…" {
		t.Fatalf("truncateUTF8() = %q, want %q", got, "aaaa…")
	}
}

func TestHumanSize(t *testing.T) {
	if got := humanSize(512); got != "512 B" {
		t.Fatalf("humanSize(512) = %q", got)
	}
	if got := humanSize(4096); got != "4 KB" {
		t.Fatalf("humanSize(4096) = %q", got)
	}
	if got := humanSize(3 << 20); got != "3.0 MB" {
		t.Fatalf("humanSize(3MB) = %q", got)
	}
}
