package main

import (
	"context"
	"fmt"
	"log"
	"strings"
	"unicode/utf8"

	internalslack "github.com/gratefulagents/gratefulagents/internal/slack"
)

// Limits for inbound attachment ingestion. Small text files are inlined into
// the turn's context; everything else is described by name/type so the agent
// knows it exists even when the content is not readable.
const (
	slackMaxInlineFiles    = 3
	slackMaxInlineBytes    = 64 * 1024 // per file cap actually inlined
	slackMaxDownloadBytes  = 256 * 1024
	slackInlineTruncNotice = "\n… (truncated)"
)

// describeFiles renders a message's attachments for the agent: small text-like
// files are downloaded and inlined verbatim; images and other binaries are
// described. Returns "" when there are no attachments.
func (o *slackOrchestrator) describeFiles(ctx context.Context, files []internalslack.File) string {
	if len(files) == 0 {
		return ""
	}
	var b strings.Builder
	inlined := 0
	for _, f := range files {
		name := f.Name
		if name == "" {
			name = f.ID
		}
		switch {
		case isTextualSlackFile(f) && inlined < slackMaxInlineFiles:
			content, err := o.downloadTextFile(ctx, f)
			if err != nil {
				log.Printf("slack connector %s: downloading %s: %v", o.agentName, name, err)
				fmt.Fprintf(&b, "Attached file %q (%s, %s) — could not be read.\n", name, f.Mimetype, humanSize(f.Size))
				continue
			}
			inlined++
			fmt.Fprintf(&b, "Attached file %q (%s):\n```\n%s\n```\n", name, f.Mimetype, content)
		case strings.HasPrefix(f.Mimetype, "image/"):
			fmt.Fprintf(&b, "Attached image %q (%s, %s) — not visible to you; ask the user to describe it if needed.\n",
				name, f.Mimetype, humanSize(f.Size))
		default:
			fmt.Fprintf(&b, "Attached file %q (%s, %s) — content not ingested.\n", name, f.Mimetype, humanSize(f.Size))
		}
	}
	return strings.TrimSpace(b.String())
}

// downloadTextFile fetches a text-like attachment and returns printable,
// size-capped content.
func (o *slackOrchestrator) downloadTextFile(ctx context.Context, f internalslack.File) (string, error) {
	data, err := o.web.DownloadFile(ctx, f.URLPrivate, slackMaxDownloadBytes)
	if err != nil {
		return "", err
	}
	if !utf8.Valid(data) {
		return "", fmt.Errorf("file %s is not valid UTF-8 text", f.Name)
	}
	return truncateUTF8(string(data), slackMaxInlineBytes, slackInlineTruncNotice), nil
}

func truncateUTF8(s string, maxBytes int, suffix string) string {
	if maxBytes < 0 || len(s) <= maxBytes {
		return s
	}
	cut := maxBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + suffix
}

// isTextualSlackFile reports whether an attachment looks like readable text the
// agent can ingest (source code, logs, config, plain text).
func isTextualSlackFile(f internalslack.File) bool {
	if f.URLPrivate == "" || f.Size <= 0 || f.Size > slackMaxDownloadBytes {
		return false
	}
	if strings.HasPrefix(f.Mimetype, "text/") {
		return true
	}
	switch f.Mimetype {
	case "application/json", "application/xml", "application/x-yaml", "application/javascript", "application/x-sh":
		return true
	}
	// Slack tags snippets/code by filetype even when mimetype is generic.
	switch strings.ToLower(f.Filetype) {
	case "text", "go", "python", "javascript", "typescript", "java", "c", "cpp", "csharp",
		"ruby", "rust", "php", "shell", "yaml", "json", "xml", "html", "css", "sql",
		"markdown", "diff", "patch", "log", "csv", "toml", "makefile", "dockerfile":
		return true
	}
	return false
}

// humanSize renders a byte count compactly (e.g. "12 KB").
func humanSize(n int) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%d KB", n/(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}
