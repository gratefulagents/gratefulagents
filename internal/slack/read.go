package slack

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	slackgo "github.com/slack-go/slack"
)

// Message is a transport-agnostic view of one Slack message returned by the
// read APIs (history, replies, search). Kept minimal on purpose: it is rendered
// into agent tool output, not consumed programmatically.
type Message struct {
	UserID   string
	BotID    string
	Text     string
	TS       string
	ThreadTS string
	// ReplyCount is non-zero on a channel message that roots a thread.
	ReplyCount int
}

// Channel is a transport-agnostic view of one Slack conversation.
type Channel struct {
	ID        string
	Name      string
	IsPrivate bool
	IsIM      bool
	IsMember  bool
	Topic     string
}

// UserInfo is a transport-agnostic view of a Slack user profile.
type UserInfo struct {
	ID          string
	Name        string
	RealName    string
	DisplayName string
	Title       string
	TimeZone    string
	IsBot       bool
}

// readClients returns the API clients to try for read operations, bot first
// (channels the bot is in) then user (the owner's own DMs and private
// conversations). Callers try each in order until one succeeds.
func (c *Client) readClients() []*slackgo.Client {
	var apis []*slackgo.Client
	if c.bot != nil {
		apis = append(apis, c.bot)
	}
	if c.user != nil {
		apis = append(apis, c.user)
	}
	return apis
}

// GetConversationHistory reads recent messages from a conversation, newest
// first, trying the bot identity first and falling back to the owner identity.
// oldest optionally bounds the window (a Slack ts like "1719900000.000000").
func (c *Client) GetConversationHistory(ctx context.Context, channelID, oldest string, limit int) ([]Message, error) {
	apis := c.readClients()
	if len(apis) == 0 {
		return nil, errors.New("slack: no token configured")
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	var lastErr error
	for _, api := range apis {
		resp, err := api.GetConversationHistoryContext(ctx, &slackgo.GetConversationHistoryParameters{
			ChannelID: channelID,
			Limit:     limit,
			Oldest:    oldest,
		})
		if err != nil {
			lastErr = err
			continue
		}
		out := make([]Message, 0, len(resp.Messages))
		for _, m := range resp.Messages {
			out = append(out, messageFromSlack(m.Msg))
		}
		return out, nil
	}
	return nil, fmt.Errorf("slack conversations.history: %w", lastErr)
}

// GetConversationReplies reads a thread's messages (root first), trying the bot
// identity first and falling back to the owner identity.
func (c *Client) GetConversationReplies(ctx context.Context, channelID, threadTS string, limit int) ([]Message, error) {
	apis := c.readClients()
	if len(apis) == 0 {
		return nil, errors.New("slack: no token configured")
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	var lastErr error
	for _, api := range apis {
		msgs, _, _, err := api.GetConversationRepliesContext(ctx, &slackgo.GetConversationRepliesParameters{
			ChannelID: channelID,
			Timestamp: threadTS,
			Limit:     limit,
		})
		if err != nil {
			lastErr = err
			continue
		}
		out := make([]Message, 0, len(msgs))
		for _, m := range msgs {
			out = append(out, messageFromSlack(m.Msg))
		}
		return out, nil
	}
	return nil, fmt.Errorf("slack conversations.replies: %w", lastErr)
}

// SearchMessages searches the workspace and returns matches with their channel
// IDs aligned by index. Slack only permits user tokens on search.messages, so
// this requires the owner identity.
func (c *Client) SearchMessages(ctx context.Context, query string, count int) ([]Message, []string, error) {
	api, err := c.requireUser()
	if err != nil {
		return nil, nil, fmt.Errorf("slack search requires a user token: %w", err)
	}
	if count <= 0 || count > 100 {
		count = 20
	}
	params := slackgo.NewSearchParameters()
	params.Count = count
	res, err := api.SearchMessagesContext(ctx, query, params)
	if err != nil {
		return nil, nil, fmt.Errorf("slack search.messages: %w", err)
	}
	msgs := make([]Message, 0, len(res.Matches))
	channels := make([]string, 0, len(res.Matches))
	for _, m := range res.Matches {
		msgs = append(msgs, Message{UserID: m.User, Text: m.Text, TS: m.Timestamp})
		channels = append(channels, m.Channel.ID)
	}
	return msgs, channels, nil
}

// ListConversations lists channels visible to the agent (public + private the
// bot/owner is in), trying bot then user identity.
func (c *Client) ListConversations(ctx context.Context, limit int) ([]Channel, error) {
	apis := c.readClients()
	if len(apis) == 0 {
		return nil, errors.New("slack: no token configured")
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	var lastErr error
	for _, api := range apis {
		chans, _, err := api.GetConversationsContext(ctx, &slackgo.GetConversationsParameters{
			ExcludeArchived: true,
			Limit:           limit,
			Types:           []string{"public_channel", "private_channel"},
		})
		if err != nil {
			lastErr = err
			continue
		}
		out := make([]Channel, 0, len(chans))
		for _, ch := range chans {
			out = append(out, Channel{
				ID:        ch.ID,
				Name:      ch.Name,
				IsPrivate: ch.IsPrivate,
				IsIM:      ch.IsIM,
				IsMember:  ch.IsMember,
				Topic:     ch.Topic.Value,
			})
		}
		return out, nil
	}
	return nil, fmt.Errorf("slack conversations.list: %w", lastErr)
}

// GetUserInfo resolves a Slack user ID to profile details, trying bot then
// user identity.
func (c *Client) GetUserInfo(ctx context.Context, userID string) (UserInfo, error) {
	apis := c.readClients()
	if len(apis) == 0 {
		return UserInfo{}, errors.New("slack: no token configured")
	}
	var lastErr error
	for _, api := range apis {
		u, err := api.GetUserInfoContext(ctx, userID)
		if err != nil {
			lastErr = err
			continue
		}
		return UserInfo{
			ID:          u.ID,
			Name:        u.Name,
			RealName:    u.RealName,
			DisplayName: u.Profile.DisplayName,
			Title:       u.Profile.Title,
			TimeZone:    u.TZ,
			IsBot:       u.IsBot,
		}, nil
	}
	return UserInfo{}, fmt.Errorf("slack users.info: %w", lastErr)
}

// RemoveReactionAsBot removes an emoji reaction the bot previously added.
// Removing a reaction that is already gone is not an error to callers.
func (c *Client) RemoveReactionAsBot(ctx context.Context, channelID, ts, name string) error {
	api, err := c.requireBot()
	if err != nil {
		return err
	}
	if err := api.RemoveReactionContext(ctx, name, slackgo.NewRefToMessage(channelID, ts)); err != nil {
		if strings.Contains(err.Error(), "no_reaction") {
			return nil
		}
		return fmt.Errorf("slack reactions.remove: %w", err)
	}
	return nil
}

// OpenModal opens a Block Kit modal in response to an interaction. triggerID
// comes from the interaction callback and is only valid for a few seconds.
func (c *Client) OpenModal(ctx context.Context, triggerID string, view slackgo.ModalViewRequest) error {
	api, err := c.requireBot()
	if err != nil {
		return err
	}
	if _, err := api.OpenViewContext(ctx, triggerID, view); err != nil {
		return fmt.Errorf("slack views.open: %w", err)
	}
	return nil
}

// DownloadFile fetches a file's content from its authenticated url_private,
// trying bot then user identity, and caps the read at maxBytes.
func (c *Client) DownloadFile(ctx context.Context, urlPrivate string, maxBytes int64) ([]byte, error) {
	apis := c.readClients()
	if len(apis) == 0 {
		return nil, errors.New("slack: no token configured")
	}
	if maxBytes <= 0 {
		maxBytes = 256 * 1024
	}
	var lastErr error
	for _, api := range apis {
		var buf bytes.Buffer
		// +1 so a truncated read is detectable by size.
		w := &limitWriter{w: &buf, n: maxBytes + 1}
		if err := api.GetFileContext(ctx, urlPrivate, w); err != nil && buf.Len() == 0 {
			lastErr = err
			continue
		}
		data := buf.Bytes()
		if int64(len(data)) > maxBytes {
			data = data[:maxBytes]
		}
		return data, nil
	}
	return nil, fmt.Errorf("slack file download: %w", lastErr)
}

// limitWriter writes at most n bytes, silently discarding the rest so large
// downloads never buffer unbounded.
type limitWriter struct {
	w io.Writer
	n int64
}

func (lw *limitWriter) Write(p []byte) (int, error) {
	if lw.n <= 0 {
		return len(p), nil // pretend success, discard
	}
	take := min(int64(len(p)), lw.n)
	written, err := lw.w.Write(p[:take])
	lw.n -= int64(written)
	if err != nil {
		return written, err
	}
	return len(p), nil
}

func messageFromSlack(m slackgo.Msg) Message {
	return Message{
		UserID:     m.User,
		BotID:      m.BotID,
		Text:       m.Text,
		TS:         m.Timestamp,
		ThreadTS:   m.ThreadTimestamp,
		ReplyCount: m.ReplyCount,
	}
}
