package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/gratefulagents/sdk/pkg/agentsdk"

	internalslack "github.com/gratefulagents/gratefulagents/internal/slack"
)

// RegisterSlackReadTools registers read-only Slack tools (threads, history,
// search, channels, users) for runs triggered by a SlackAgent. Tokens come from
// the run pod's environment (SLACK_BOT_TOKEN / SLACK_USER_TOKEN, injected from
// the SlackAgent's tokens Secret). Registration is a no-op when no token is
// present so the tools never appear without credentials. These tools only read
// — sending always goes through the connector's approval flow.
func RegisterSlackReadTools(registry *Registry) {
	if registry == nil {
		return
	}
	botToken := strings.TrimSpace(os.Getenv("SLACK_BOT_TOKEN"))
	userToken := strings.TrimSpace(os.Getenv("SLACK_USER_TOKEN"))
	if botToken == "" && userToken == "" {
		return
	}
	shared := &slackReadClient{botToken: botToken, userToken: userToken}
	registry.Register(&slackReadThreadTool{c: shared})
	registry.Register(&slackReadChannelTool{c: shared})
	registry.Register(&slackSearchTool{c: shared})
	registry.Register(&slackListChannelsTool{c: shared})
	registry.Register(&slackUserInfoTool{c: shared})
}

// slackReadClient lazily builds the shared Slack client and caches user-ID →
// display-name lookups so rendered output shows names instead of raw IDs.
type slackReadClient struct {
	botToken  string
	userToken string

	mu     sync.Mutex
	client *internalslack.Client
	names  map[string]string
}

func (s *slackReadClient) get() (*internalslack.Client, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.client != nil {
		return s.client, nil
	}
	c, err := internalslack.New(internalslack.Tokens{BotToken: s.botToken, UserToken: s.userToken})
	if err != nil {
		return nil, err
	}
	s.client = c
	return c, nil
}

// displayName resolves a user ID to a human-readable name, caching results.
func (s *slackReadClient) displayName(ctx context.Context, userID string) string {
	if userID == "" {
		return "unknown"
	}
	s.mu.Lock()
	if s.names == nil {
		s.names = map[string]string{}
	}
	if name, ok := s.names[userID]; ok {
		s.mu.Unlock()
		return name
	}
	s.mu.Unlock()

	name := userID
	if c, err := s.get(); err == nil {
		if info, err := c.GetUserInfo(ctx, userID); err == nil {
			switch {
			case info.DisplayName != "":
				name = info.DisplayName
			case info.RealName != "":
				name = info.RealName
			case info.Name != "":
				name = info.Name
			}
		}
	}
	s.mu.Lock()
	s.names[userID] = name
	s.mu.Unlock()
	return name
}

// renderMessages renders messages one per line as "name: text" for tool output.
func (s *slackReadClient) renderMessages(ctx context.Context, msgs []internalslack.Message) string {
	var b strings.Builder
	for _, m := range msgs {
		who := s.displayName(ctx, m.UserID)
		if m.UserID == "" && m.BotID != "" {
			who = "bot:" + m.BotID
		}
		fmt.Fprintf(&b, "[%s] %s: %s\n", m.TS, who, strings.TrimSpace(m.Text))
		if m.ReplyCount > 0 {
			fmt.Fprintf(&b, "  (thread root with %d replies — use slack_read_thread with thread_ts=%s)\n", m.ReplyCount, m.TS)
		}
	}
	if b.Len() == 0 {
		return "(no messages)"
	}
	return b.String()
}

func errResult(err error) Result {
	return Result{Content: err.Error(), IsError: true}
}

// --- slack_read_thread ---

type slackReadThreadTool struct{ c *slackReadClient }

func (t *slackReadThreadTool) Name() string { return "slack_read_thread" }
func (t *slackReadThreadTool) Description() string {
	return "Read a Slack thread's messages (root first). Use for catching up on a " +
		"conversation the user referenced. channel is the channel ID (e.g. C0123); " +
		"thread_ts is the thread root timestamp."
}
func (t *slackReadThreadTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"channel": {"type": "string", "description": "Channel ID (C…/D…/G…)"},
			"thread_ts": {"type": "string", "description": "Thread root timestamp (e.g. 1719900000.000100)"},
			"limit": {"type": "integer", "description": "Max messages (default 50)"}
		},
		"required": ["channel", "thread_ts"]
	}`)
}
func (t *slackReadThreadTool) IsReadOnly() bool                      { return true }
func (t *slackReadThreadTool) IsEnabled(_ *agentsdk.RunContext) bool { return true }
func (t *slackReadThreadTool) NeedsApproval() bool                   { return false }
func (t *slackReadThreadTool) TimeoutSeconds() int                   { return 60 }

func (t *slackReadThreadTool) Execute(ctx context.Context, input json.RawMessage, _ string) (Result, error) {
	var in struct {
		Channel  string `json:"channel"`
		ThreadTS string `json:"thread_ts"`
		Limit    int    `json:"limit"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return errResult(fmt.Errorf("invalid input: %w", err)), nil
	}
	c, err := t.c.get()
	if err != nil {
		return errResult(err), nil
	}
	msgs, err := c.GetConversationReplies(ctx, strings.TrimSpace(in.Channel), strings.TrimSpace(in.ThreadTS), in.Limit)
	if err != nil {
		return errResult(err), nil
	}
	return Result{Content: t.c.renderMessages(ctx, msgs)}, nil
}

// --- slack_read_channel ---

type slackReadChannelTool struct{ c *slackReadClient }

func (t *slackReadChannelTool) Name() string { return "slack_read_channel" }
func (t *slackReadChannelTool) Description() string {
	return "Read recent messages from a Slack channel or DM (newest first). Use for " +
		"summarizing or catching up. channel is the channel ID (e.g. C0123); oldest " +
		"optionally bounds the window to messages after that Slack timestamp."
}
func (t *slackReadChannelTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"channel": {"type": "string", "description": "Channel ID (C…/D…/G…)"},
			"limit": {"type": "integer", "description": "Max messages (default 50, max 200)"},
			"oldest": {"type": "string", "description": "Only messages after this Slack ts (optional)"}
		},
		"required": ["channel"]
	}`)
}
func (t *slackReadChannelTool) IsReadOnly() bool                      { return true }
func (t *slackReadChannelTool) IsEnabled(_ *agentsdk.RunContext) bool { return true }
func (t *slackReadChannelTool) NeedsApproval() bool                   { return false }
func (t *slackReadChannelTool) TimeoutSeconds() int                   { return 60 }

func (t *slackReadChannelTool) Execute(ctx context.Context, input json.RawMessage, _ string) (Result, error) {
	var in struct {
		Channel string `json:"channel"`
		Limit   int    `json:"limit"`
		Oldest  string `json:"oldest"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return errResult(fmt.Errorf("invalid input: %w", err)), nil
	}
	c, err := t.c.get()
	if err != nil {
		return errResult(err), nil
	}
	msgs, err := c.GetConversationHistory(ctx, strings.TrimSpace(in.Channel), strings.TrimSpace(in.Oldest), in.Limit)
	if err != nil {
		return errResult(err), nil
	}
	return Result{Content: t.c.renderMessages(ctx, msgs)}, nil
}

// --- slack_search ---

type slackSearchTool struct{ c *slackReadClient }

func (t *slackSearchTool) Name() string { return "slack_search" }
func (t *slackSearchTool) Description() string {
	return "Search Slack messages across the workspace (requires the owner's user " +
		"token). Supports Slack search modifiers like from:@user, in:#channel, " +
		"before:/after:YYYY-MM-DD."
}
func (t *slackSearchTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"query": {"type": "string", "description": "Search query (Slack search syntax)"},
			"count": {"type": "integer", "description": "Max results (default 20)"}
		},
		"required": ["query"]
	}`)
}
func (t *slackSearchTool) IsReadOnly() bool                      { return true }
func (t *slackSearchTool) IsEnabled(_ *agentsdk.RunContext) bool { return true }
func (t *slackSearchTool) NeedsApproval() bool                   { return false }
func (t *slackSearchTool) TimeoutSeconds() int                   { return 60 }

func (t *slackSearchTool) Execute(ctx context.Context, input json.RawMessage, _ string) (Result, error) {
	var in struct {
		Query string `json:"query"`
		Count int    `json:"count"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return errResult(fmt.Errorf("invalid input: %w", err)), nil
	}
	if strings.TrimSpace(in.Query) == "" {
		return errResult(fmt.Errorf("query is required")), nil
	}
	c, err := t.c.get()
	if err != nil {
		return errResult(err), nil
	}
	msgs, channels, err := c.SearchMessages(ctx, in.Query, in.Count)
	if err != nil {
		return errResult(err), nil
	}
	if len(msgs) == 0 {
		return Result{Content: "(no matches)"}, nil
	}
	var b strings.Builder
	for i, m := range msgs {
		channel := ""
		if i < len(channels) {
			channel = channels[i]
		}
		fmt.Fprintf(&b, "[%s in %s] %s: %s\n", m.TS, channel, t.c.displayName(ctx, m.UserID), strings.TrimSpace(m.Text))
	}
	return Result{Content: b.String()}, nil
}

// --- slack_list_channels ---

type slackListChannelsTool struct{ c *slackReadClient }

func (t *slackListChannelsTool) Name() string { return "slack_list_channels" }
func (t *slackListChannelsTool) Description() string {
	return "List Slack channels visible to the agent (name, ID, topic). Use to " +
		"resolve a channel name like #eng to its ID before reading it."
}
func (t *slackListChannelsTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"limit": {"type": "integer", "description": "Max channels (default 100)"}
		}
	}`)
}
func (t *slackListChannelsTool) IsReadOnly() bool                      { return true }
func (t *slackListChannelsTool) IsEnabled(_ *agentsdk.RunContext) bool { return true }
func (t *slackListChannelsTool) NeedsApproval() bool                   { return false }
func (t *slackListChannelsTool) TimeoutSeconds() int                   { return 60 }

func (t *slackListChannelsTool) Execute(ctx context.Context, input json.RawMessage, _ string) (Result, error) {
	var in struct {
		Limit int `json:"limit"`
	}
	_ = json.Unmarshal(input, &in)
	c, err := t.c.get()
	if err != nil {
		return errResult(err), nil
	}
	channels, err := c.ListConversations(ctx, in.Limit)
	if err != nil {
		return errResult(err), nil
	}
	if len(channels) == 0 {
		return Result{Content: "(no channels)"}, nil
	}
	var b strings.Builder
	for _, ch := range channels {
		visibility := "public"
		if ch.IsPrivate {
			visibility = "private"
		}
		member := ""
		if ch.IsMember {
			member = ", member"
		}
		topic := ""
		if ch.Topic != "" {
			topic = " — " + ch.Topic
		}
		fmt.Fprintf(&b, "#%s (%s, %s%s)%s\n", ch.Name, ch.ID, visibility, member, topic)
	}
	return Result{Content: b.String()}, nil
}

// --- slack_user_info ---

type slackUserInfoTool struct{ c *slackReadClient }

func (t *slackUserInfoTool) Name() string { return "slack_user_info" }
func (t *slackUserInfoTool) Description() string {
	return "Look up a Slack user's profile (name, title, timezone) by user ID (U…)."
}
func (t *slackUserInfoTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"user": {"type": "string", "description": "Slack user ID (e.g. U0123ABC)"}
		},
		"required": ["user"]
	}`)
}
func (t *slackUserInfoTool) IsReadOnly() bool                      { return true }
func (t *slackUserInfoTool) IsEnabled(_ *agentsdk.RunContext) bool { return true }
func (t *slackUserInfoTool) NeedsApproval() bool                   { return false }
func (t *slackUserInfoTool) TimeoutSeconds() int                   { return 30 }

func (t *slackUserInfoTool) Execute(ctx context.Context, input json.RawMessage, _ string) (Result, error) {
	var in struct {
		User string `json:"user"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return errResult(fmt.Errorf("invalid input: %w", err)), nil
	}
	c, err := t.c.get()
	if err != nil {
		return errResult(err), nil
	}
	info, err := c.GetUserInfo(ctx, strings.TrimSpace(in.User))
	if err != nil {
		return errResult(err), nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "ID: %s\nName: %s\nReal name: %s\nDisplay name: %s\n", info.ID, info.Name, info.RealName, info.DisplayName)
	if info.Title != "" {
		fmt.Fprintf(&b, "Title: %s\n", info.Title)
	}
	if info.TimeZone != "" {
		fmt.Fprintf(&b, "Timezone: %s\n", info.TimeZone)
	}
	if info.IsBot {
		b.WriteString("(bot account)\n")
	}
	return Result{Content: b.String()}, nil
}
