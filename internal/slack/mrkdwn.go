package slack

import (
	"regexp"
	"strings"
)

// ToMrkdwn converts common Markdown (as produced by the agent) into Slack
// mrkdwn so replies render properly instead of showing literal markers:
//
//	**bold** / __bold__   → *bold*
//	~~strike~~            → ~strike~
//	[text](url)           → <url|text>
//	# Heading             → *Heading*
//	- item / * item       → • item
//	```lang               → ``` (Slack fences carry no language tag)
//
// Fenced code blocks and inline code spans are preserved untouched. The
// converter is a pure function and intentionally line-based: agent output is
// simple document markdown, not arbitrary HTML-grade input.
func ToMrkdwn(text string) string {
	if strings.TrimSpace(text) == "" {
		return text
	}
	out := make([]string, 0, strings.Count(text, "\n")+1)
	inFence := false
	for line := range strings.SplitSeq(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			if !inFence {
				// Opening fence: drop any language tag, Slack does not support it.
				indent := line[:strings.Index(line, "```")]
				out = append(out, indent+"```")
			} else {
				out = append(out, line)
			}
			inFence = !inFence
			continue
		}
		if inFence {
			out = append(out, line)
			continue
		}
		out = append(out, convertMrkdwnLine(line))
	}
	return strings.Join(out, "\n")
}

var (
	mrkdwnHeadingRe = regexp.MustCompile(`^(\s*)#{1,6}\s+(.*)$`)
	mrkdwnBulletRe  = regexp.MustCompile(`^(\s*)[-*]\s+`)
	mrkdwnBoldRe    = regexp.MustCompile(`\*\*(.+?)\*\*`)
	mrkdwnBoldUndRe = regexp.MustCompile(`__(.+?)__`)
	mrkdwnStrikeRe  = regexp.MustCompile(`~~(.+?)~~`)
	mrkdwnLinkRe    = regexp.MustCompile(`\[([^\]\n]+)\]\((https?://[^)\s]+)\)`)
)

// convertMrkdwnLine rewrites one non-code line, leaving inline code spans as-is.
func convertMrkdwnLine(line string) string {
	// Split around inline code spans so their contents are never rewritten.
	parts := strings.Split(line, "`")
	for i := 0; i < len(parts); i += 2 { // even indices are outside code spans
		parts[i] = convertMrkdwnSegment(parts[i], i == 0)
	}
	return strings.Join(parts, "`")
}

// convertMrkdwnSegment applies the inline conversions to a code-free segment.
// Line-anchored rules (headings, bullets) only apply to the first segment.
func convertMrkdwnSegment(seg string, lineStart bool) string {
	if lineStart {
		if m := mrkdwnHeadingRe.FindStringSubmatch(seg); m != nil {
			seg = m[1] + "*" + strings.TrimSpace(m[2]) + "*"
			// The heading text may still contain bold/link markers; fall through.
		}
		seg = mrkdwnBulletRe.ReplaceAllString(seg, "$1• ")
	}
	seg = mrkdwnLinkRe.ReplaceAllString(seg, "<$2|$1>")
	seg = mrkdwnBoldRe.ReplaceAllString(seg, "*$1*")
	seg = mrkdwnBoldUndRe.ReplaceAllString(seg, "*$1*")
	seg = mrkdwnStrikeRe.ReplaceAllString(seg, "~$1~")
	return seg
}
