package slack

import (
	"context"
	"fmt"
	"strings"

	slackgo "github.com/slack-go/slack"
)

// PublishHomeView publishes a Home tab view for the given user as the bot. An
// empty block list clears the tab.
func (c *Client) PublishHomeView(ctx context.Context, userID string, blocks ...slackgo.Block) error {
	api, err := c.requireBot()
	if err != nil {
		return err
	}
	if strings.TrimSpace(userID) == "" {
		return fmt.Errorf("slack: user id is required to publish a home view")
	}
	view := slackgo.HomeTabViewRequest{
		Type:   slackgo.VTHomeTab,
		Blocks: slackgo.Blocks{BlockSet: blocks},
	}
	if _, err := api.PublishViewContext(ctx, slackgo.PublishViewContextRequest{UserID: userID, View: view}); err != nil {
		return fmt.Errorf("slack views.publish: %w", err)
	}
	return nil
}

// Slack block text limits enforced on the App Home copy so a long override
// never turns into an invalid_blocks API error.
const (
	homeHeaderMaxRunes = 150  // header blocks are capped by Slack at 150 chars
	homeTextMaxRunes   = 3000 // context/mrkdwn text object limit
)

// defaultHomeText is the info line rendered when the owner has not configured
// custom App Home copy.
const defaultHomeText = "DM me to get things done. This agent is managed from its owner's dashboard."

// BuildHomePlaceholderView renders the static App Home tab: a header (custom,
// or the agent name) plus one short info line (custom, or a generic pointer at
// the owner's dashboard). It deliberately carries no agent state: connection
// status, configuration, and held replies are dashboard-only,
// because the App Home is visible to any workspace member who opens the app.
// Publishing this placeholder also replaces (clears) any richer view published
// by older builds.
func BuildHomePlaceholderView(agentName, header, text string) []slackgo.Block {
	header = strings.TrimSpace(header)
	if header == "" {
		header = robotName(agentName)
	}
	text = strings.TrimSpace(text)
	if text == "" {
		text = defaultHomeText
	}
	return []slackgo.Block{
		slackgo.NewHeaderBlock(slackgo.NewTextBlockObject(
			slackgo.PlainTextType, truncateRunes(header, homeHeaderMaxRunes), false, false)),
		slackgo.NewContextBlock("home-info", slackgo.NewTextBlockObject(
			slackgo.MarkdownType, truncateRunes(text, homeTextMaxRunes), false, false)),
	}
}

func robotName(agentName string) string {
	agentName = strings.TrimSpace(agentName)
	if agentName == "" {
		agentName = "Slack agent"
	}
	return "🤖 " + agentName
}

// truncateRunes caps s at n runes, appending an ellipsis when it was cut.
func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
