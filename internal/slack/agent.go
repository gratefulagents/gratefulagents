package slack

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"unicode/utf8"

	slackgo "github.com/slack-go/slack"
)

// SessionStatus is an Agent Sessions API lifecycle status. Slack renders it in
// the thread ("Working…" with a stop button while processing) and in the user's
// agent sessions sidebar.
type SessionStatus string

const (
	// SessionActive: the agent is idle and ready for the next prompt.
	SessionActive SessionStatus = "active"
	// SessionProcessing: the agent is working. Slack shows the loading UX and,
	// when the app subscribes to agent_session_stopped, a stop button. It times
	// out to active after one hour unless re-sent.
	SessionProcessing SessionStatus = "processing"
	// SessionSuspended: the agent needs the user (or owner) to act before it
	// can continue, e.g. a reply held for approval.
	SessionSuspended SessionStatus = "suspended"
	// SessionClosed: the agent will no longer respond on this session.
	SessionClosed SessionStatus = "closed"
)

// MaxMarkdownChars is Slack's limit for markdown_text on chat.postMessage and
// the chat.*Stream methods.
const MaxMarkdownChars = 12000

// MaxSessionTitleChars is Slack's limit for an agent session title.
const MaxSessionTitleChars = 200

// SessionParams identifies an agent session and, on creation, seeds its title
// and initiator.
type SessionParams struct {
	ChannelID string
	ThreadTS  string
	Status    SessionStatus
	// Title is applied only when the session is created; use RenameSession to
	// change it afterwards.
	Title string
	// InitiatorUserID is applied only on creation and must be a member of the
	// channel (Slack silently falls back to the bot user otherwise).
	InitiatorUserID string
}

// SetSessionStatus creates or updates the agent session for a thread via
// agents.sessions.setStatus. Requires chat:write and the app to be declared as
// an agent (assistant:write). Slack replaces the assistant.threads.* methods
// with this API; unlike them, "processing" does not clear when a message posts,
// so callers must set active/suspended/closed when the turn ends.
func (c *Client) SetSessionStatus(ctx context.Context, p SessionParams) error {
	if strings.TrimSpace(p.ChannelID) == "" || strings.TrimSpace(p.ThreadTS) == "" {
		return errors.New("slack: channel and thread are required to set a session status")
	}
	if p.Status == "" {
		return errors.New("slack: session status is required")
	}
	body := map[string]any{
		"channel_id": p.ChannelID,
		"thread_ts":  p.ThreadTS,
		"status":     string(p.Status),
	}
	if t := SessionTitle(p.Title); t != "" {
		body["title"] = t
	}
	if u := strings.TrimSpace(p.InitiatorUserID); u != "" {
		body["initiator_user_id"] = u
	}
	return c.postJSON(ctx, "agents.sessions.setStatus", body)
}

// RenameSession changes an existing agent session's title.
func (c *Client) RenameSession(ctx context.Context, channelID, threadTS, title string) error {
	if strings.TrimSpace(channelID) == "" || strings.TrimSpace(threadTS) == "" {
		return errors.New("slack: channel and thread are required to rename a session")
	}
	title = SessionTitle(title)
	if title == "" {
		return errors.New("slack: session title is required")
	}
	return c.postJSON(ctx, "agents.sessions.rename", map[string]any{
		"channel_id": channelID,
		"thread_ts":  threadTS,
		"title":      title,
	})
}

// SessionTitle derives a single-line session title from free text: the first
// non-empty line, whitespace-collapsed and truncated to Slack's limit.
func SessionTitle(text string) string {
	for _, line := range strings.Split(text, "\n") {
		line = strings.Join(strings.Fields(line), " ")
		if line == "" {
			continue
		}
		return truncateRunes(line, MaxSessionTitleChars)
	}
	return ""
}

// StreamTarget addresses a streamed reply. RecipientUserID and RecipientTeamID
// are required by Slack when streaming into a channel (rather than the app DM);
// they identify the user the stream is answering.
type StreamTarget struct {
	ChannelID       string
	ThreadTS        string
	RecipientUserID string
	RecipientTeamID string
}

// StartStream opens a streaming message in a thread with an initial markdown
// chunk and returns the message ts. Starting a stream also creates the thread's
// agent session (if needed) and marks it processing. Requires chat:write.
func (c *Client) StartStream(ctx context.Context, t StreamTarget, markdown string) (ts string, err error) {
	api, err := c.requireBot()
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(t.ChannelID) == "" || strings.TrimSpace(t.ThreadTS) == "" {
		return "", errors.New("slack: channel and thread are required to start a stream")
	}
	opts := []slackgo.MsgOption{slackgo.MsgOptionTS(t.ThreadTS)}
	if md := TruncateMarkdown(markdown); md != "" {
		opts = append(opts, slackgo.MsgOptionMarkdownText(md))
	}
	if u := strings.TrimSpace(t.RecipientUserID); u != "" {
		opts = append(opts, slackgo.MsgOptionRecipientUserID(u))
	}
	if team := strings.TrimSpace(t.RecipientTeamID); team != "" {
		opts = append(opts, slackgo.MsgOptionRecipientTeamID(team))
	}
	_, ts, err = api.StartStreamContext(ctx, t.ChannelID, opts...)
	if err != nil {
		return "", fmt.Errorf("slack chat.startStream: %w", err)
	}
	return ts, nil
}

// AppendStream appends a markdown chunk to an open stream.
func (c *Client) AppendStream(ctx context.Context, channelID, ts, markdown string) error {
	api, err := c.requireBot()
	if err != nil {
		return err
	}
	md := TruncateMarkdown(markdown)
	if md == "" {
		return nil
	}
	if _, _, err := api.AppendStreamContext(ctx, channelID, ts, slackgo.MsgOptionMarkdownText(md)); err != nil {
		return fmt.Errorf("slack chat.appendStream: %w", err)
	}
	return nil
}

// StopStream finalizes an open stream, optionally appending a last markdown
// chunk and trailing blocks (e.g. feedback buttons), and sets the thread's
// session status (Slack defaults to active when empty).
func (c *Client) StopStream(ctx context.Context, channelID, ts, markdown string, status SessionStatus, blocks ...slackgo.Block) error {
	api, err := c.requireBot()
	if err != nil {
		return err
	}
	opts := []slackgo.MsgOption{
		// slack-go's StopStream sets the endpoint and ts; this option adds the
		// session_status parameter it does not model yet.
		slackgo.UnsafeMsgOptionEndpoint(c.apiURL+"chat.stopStream", func(v url.Values) {
			v.Set("ts", ts)
			if status != "" {
				v.Set("session_status", string(status))
			}
		}),
	}
	if md := TruncateMarkdown(markdown); md != "" {
		opts = append(opts, slackgo.MsgOptionMarkdownText(md))
	}
	if len(blocks) > 0 {
		opts = append(opts, slackgo.MsgOptionBlocks(blocks...))
	}
	if _, _, err := api.StopStreamContext(ctx, channelID, ts, opts...); err != nil {
		return fmt.Errorf("slack chat.stopStream: %w", err)
	}
	return nil
}

// TruncateMarkdown bounds markdown to Slack's markdown_text limit on a rune
// boundary, marking the cut with an ellipsis.
func TruncateMarkdown(s string) string {
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) <= MaxMarkdownChars {
		return s
	}
	return truncateRunes(s, MaxMarkdownChars-1) + "…"
}

// apiResponse is the envelope every Web API method returns.
type apiResponse struct {
	OK      bool   `json:"ok"`
	Error   string `json:"error"`
	Warning string `json:"warning"`
}

// postJSON calls a Web API method as the bot with a JSON body. It exists for
// methods slack-go has no wrapper for; every Web API method accepts
// application/json with a bearer token.
func (c *Client) postJSON(ctx context.Context, method string, body any) error {
	if c.botToken == "" {
		return errors.New("slack: bot token required")
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("slack %s: encoding request: %w", method, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.apiURL+method, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("slack %s: building request: %w", method, err)
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("Authorization", "Bearer "+c.botToken)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("slack %s: %w", method, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("slack %s: reading response: %w", method, err)
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		return fmt.Errorf("slack %s: rate limited (retry-after %s)", method, resp.Header.Get("Retry-After"))
	}
	var out apiResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return fmt.Errorf("slack %s: HTTP %d: %w", method, resp.StatusCode, err)
	}
	if !out.OK {
		if out.Error == "" {
			out.Error = fmt.Sprintf("HTTP %d", resp.StatusCode)
		}
		return fmt.Errorf("slack %s: %s", method, out.Error)
	}
	return nil
}
