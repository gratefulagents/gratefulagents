// Package slack provides a thin wrapper around the slack-go client tailored to
// the SlackAgent connector. It separates the bot identity (xoxb-, used for the
// owner<->agent control DM and Block Kit interactions) from the optional user
// identity (xoxp-, used only for the slack_search read tool and to resolve the
// owner's Slack user ID).
//
// Socket Mode (the inbound transport) is handled by the connector entrypoint in
// cmd/agent; this package covers the Web API calls used to post results and
// manage assistant threads.
package slack

import (
	"context"
	"errors"
	"fmt"
	"strings"

	slackgo "github.com/slack-go/slack"
)

// Tokens carries the Slack credentials for a single SlackAgent. AppToken is
// only required by the Socket Mode connector, not by the Web API client.
type Tokens struct {
	BotToken  string
	UserToken string
	AppToken  string
}

// Identity is the resolved Slack identity from auth.test.
type Identity struct {
	UserID       string
	BotID        string
	TeamID       string
	Team         string
	EnterpriseID string
}

// Client wraps two slack-go clients: one authenticated as the bot and one as the
// owner user. Either may be nil if the corresponding token is absent.
type Client struct {
	bot  *slackgo.Client
	user *slackgo.Client
}

// New builds a Client from the provided tokens. At least a bot token is
// required; the user token is optional and only needed for the slack_search
// tool (Slack permits search.messages with user tokens only).
func New(tokens Tokens) (*Client, error) {
	c := &Client{}
	if t := strings.TrimSpace(tokens.BotToken); t != "" {
		c.bot = slackgo.New(t)
	}
	if t := strings.TrimSpace(tokens.UserToken); t != "" {
		c.user = slackgo.New(t)
	}
	if c.bot == nil && c.user == nil {
		return nil, errors.New("slack: at least one of bot-token or user-token is required")
	}
	return c, nil
}

// HasUser reports whether a user token is configured (slack_search / owner
// identity resolution).
func (c *Client) HasUser() bool { return c.user != nil }

// HasBot reports whether a bot token is configured.
func (c *Client) HasBot() bool { return c.bot != nil }

func (c *Client) requireBot() (*slackgo.Client, error) {
	if c.bot == nil {
		return nil, errors.New("slack: bot token not configured")
	}
	return c.bot, nil
}

func (c *Client) requireUser() (*slackgo.Client, error) {
	if c.user == nil {
		return nil, errors.New("slack: user token not configured")
	}
	return c.user, nil
}

// AuthTestBot validates the bot token and returns the bot identity.
func (c *Client) AuthTestBot(ctx context.Context) (Identity, error) {
	api, err := c.requireBot()
	if err != nil {
		return Identity{}, err
	}
	return authTest(ctx, api)
}

// AuthTestUser validates the user token and returns the owner identity.
func (c *Client) AuthTestUser(ctx context.Context) (Identity, error) {
	api, err := c.requireUser()
	if err != nil {
		return Identity{}, err
	}
	return authTest(ctx, api)
}

func authTest(ctx context.Context, api *slackgo.Client) (Identity, error) {
	resp, err := api.AuthTestContext(ctx)
	if err != nil {
		return Identity{}, fmt.Errorf("slack auth.test: %w", err)
	}
	return Identity{
		UserID:       resp.UserID,
		BotID:        resp.BotID,
		TeamID:       resp.TeamID,
		Team:         resp.Team,
		EnterpriseID: resp.EnterpriseID,
	}, nil
}

// PostMessageAsBot posts a message as the bot. When threadTS is non-empty the
// message is posted in that thread.
func (c *Client) PostMessageAsBot(ctx context.Context, channelID, text, threadTS string) (ts string, err error) {
	api, err := c.requireBot()
	if err != nil {
		return "", err
	}
	return postMessage(ctx, api, channelID, text, threadTS)
}

// PostMessageAsBotBlocks posts Block Kit blocks as the bot (e.g. approval
// prompts). text is the notification fallback.
func (c *Client) PostMessageAsBotBlocks(ctx context.Context, channelID, text, threadTS string, blocks ...slackgo.Block) (ts string, err error) {
	api, err := c.requireBot()
	if err != nil {
		return "", err
	}
	opts := []slackgo.MsgOption{slackgo.MsgOptionText(text, false), slackgo.MsgOptionBlocks(blocks...)}
	if strings.TrimSpace(threadTS) != "" {
		opts = append(opts, slackgo.MsgOptionTS(threadTS))
	}
	_, ts, err = api.PostMessageContext(ctx, channelID, opts...)
	if err != nil {
		return "", fmt.Errorf("slack chat.postMessage (blocks): %w", err)
	}
	return ts, nil
}

func postMessage(ctx context.Context, api *slackgo.Client, channelID, text, threadTS string) (string, error) {
	opts := []slackgo.MsgOption{slackgo.MsgOptionText(text, false)}
	if strings.TrimSpace(threadTS) != "" {
		opts = append(opts, slackgo.MsgOptionTS(threadTS))
	}
	_, ts, err := api.PostMessageContext(ctx, channelID, opts...)
	if err != nil {
		return "", fmt.Errorf("slack chat.postMessage: %w", err)
	}
	return ts, nil
}

// UpdateMessageAsBot edits a previously posted bot message (e.g. resolving an
// approval prompt after the owner clicks a button).
func (c *Client) UpdateMessageAsBot(ctx context.Context, channelID, ts, text string, blocks ...slackgo.Block) error {
	api, err := c.requireBot()
	if err != nil {
		return err
	}
	opts := []slackgo.MsgOption{slackgo.MsgOptionText(text, false)}
	if len(blocks) > 0 {
		opts = append(opts, slackgo.MsgOptionBlocks(blocks...))
	}
	if _, _, _, err := api.UpdateMessageContext(ctx, channelID, ts, opts...); err != nil {
		return fmt.Errorf("slack chat.update: %w", err)
	}
	return nil
}

// PostEphemeralAsBot posts a message visible only to the given user.
func (c *Client) PostEphemeralAsBot(ctx context.Context, channelID, userID, text string) error {
	api, err := c.requireBot()
	if err != nil {
		return err
	}
	if _, err := api.PostEphemeralContext(ctx, channelID, userID, slackgo.MsgOptionText(text, false)); err != nil {
		return fmt.Errorf("slack chat.postEphemeral: %w", err)
	}
	return nil
}

// AddReactionAsBot adds an emoji reaction (name without colons) to a message as
// the bot.
func (c *Client) AddReactionAsBot(ctx context.Context, channelID, ts, name string) error {
	api, err := c.requireBot()
	if err != nil {
		return err
	}
	if err := api.AddReactionContext(ctx, name, slackgo.NewRefToMessage(channelID, ts)); err != nil {
		return fmt.Errorf("slack reactions.add: %w", err)
	}
	return nil
}

// OpenIMWithUser opens (or returns) the bot's direct-message channel with a user
// and returns its channel ID. Used to resolve the owner<->bot control channel.
func (c *Client) OpenIMWithUser(ctx context.Context, userID string) (string, error) {
	api, err := c.requireBot()
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(userID) == "" {
		return "", errors.New("slack: user id is required to open IM")
	}
	channel, _, _, err := api.OpenConversationContext(ctx, &slackgo.OpenConversationParameters{
		Users:    []string{userID},
		ReturnIM: true,
	})
	if err != nil {
		return "", fmt.Errorf("slack conversations.open: %w", err)
	}
	if channel == nil {
		return "", errors.New("slack conversations.open returned no channel")
	}
	return channel.ID, nil
}

// AssistantPrompt is a suggested prompt chip shown in the assistant pane.
type AssistantPrompt struct {
	Title   string
	Message string
}

// SetAssistantStatus sets the assistant pane's "is thinking…" status for a
// thread. The status auto-clears when the next message is posted. Requires the
// assistant:write scope; best-effort for non-assistant threads.
func (c *Client) SetAssistantStatus(ctx context.Context, channelID, threadTS, status string) error {
	api, err := c.requireBot()
	if err != nil {
		return err
	}
	return api.SetAssistantThreadsStatusContext(ctx, slackgo.AssistantThreadsSetStatusParameters{
		ChannelID: channelID,
		ThreadTS:  threadTS,
		Status:    status,
	})
}

// SetAssistantSuggestedPrompts shows up to four clickable prompt chips in the
// assistant pane.
func (c *Client) SetAssistantSuggestedPrompts(ctx context.Context, channelID, threadTS, title string, prompts []AssistantPrompt) error {
	api, err := c.requireBot()
	if err != nil {
		return err
	}
	params := slackgo.AssistantThreadsSetSuggestedPromptsParameters{
		ChannelID: channelID,
		ThreadTS:  threadTS,
		Title:     title,
	}
	for _, p := range prompts {
		params.AddPrompt(p.Title, p.Message)
	}
	return api.SetAssistantThreadsSuggestedPromptsContext(ctx, params)
}

// SetAssistantTitle sets the assistant thread's title (shown in the user's DM
// history with the app).
func (c *Client) SetAssistantTitle(ctx context.Context, channelID, threadTS, title string) error {
	api, err := c.requireBot()
	if err != nil {
		return err
	}
	return api.SetAssistantThreadsTitleContext(ctx, slackgo.AssistantThreadsSetTitleParameters{
		ChannelID: channelID,
		ThreadTS:  threadTS,
		Title:     title,
	})
}
