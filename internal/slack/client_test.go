package slack

import (
	"context"
	"testing"
)

func TestNewRequiresAToken(t *testing.T) {
	if _, err := New(Tokens{}); err == nil {
		t.Fatal("New() with no tokens should error")
	}
}

func TestNewBotOnly(t *testing.T) {
	c, err := New(Tokens{BotToken: "xoxb-test"})
	if err != nil {
		t.Fatalf("New() bot-only error: %v", err)
	}
	if !c.HasBot() {
		t.Error("HasBot() = false, want true")
	}
	if c.HasUser() {
		t.Error("HasUser() = true, want false")
	}
}

func TestNewUserOnly(t *testing.T) {
	c, err := New(Tokens{UserToken: "xoxp-test"})
	if err != nil {
		t.Fatalf("New() user-only error: %v", err)
	}
	if c.HasBot() {
		t.Error("HasBot() = true, want false")
	}
	if !c.HasUser() {
		t.Error("HasUser() = false, want true")
	}
}

func TestPostAsBotRequiresBotToken(t *testing.T) {
	c, err := New(Tokens{UserToken: "xoxp-test"})
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	if _, err := c.PostMessageAsBot(context.Background(), "D1", "hi", ""); err == nil {
		t.Fatal("PostMessageAsBot without bot token should error")
	}
}

func TestAuthTestUserRequiresUserToken(t *testing.T) {
	c, err := New(Tokens{BotToken: "xoxb-test"})
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	if _, err := c.AuthTestUser(context.Background()); err == nil {
		t.Fatal("AuthTestUser without user token should error")
	}
}

func TestAssistantMethodsRequireBotToken(t *testing.T) {
	c, err := New(Tokens{UserToken: "xoxp-test"})
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	ctx := context.Background()
	if err := c.SetSessionStatus(ctx, SessionParams{ChannelID: "C1", ThreadTS: "1.1", Status: SessionProcessing}); err == nil {
		t.Error("SetSessionStatus without bot token should error")
	}
	if err := c.SetAssistantSuggestedPrompts(ctx, "C1", "1.1", "t", []AssistantPrompt{{Title: "a", Message: "b"}}); err == nil {
		t.Error("SetAssistantSuggestedPrompts without bot token should error")
	}
	if err := c.RenameSession(ctx, "C1", "1.1", "Title"); err == nil {
		t.Error("RenameSession without bot token should error")
	}
	if _, err := c.StartStream(ctx, StreamTarget{ChannelID: "C1", ThreadTS: "1.1"}, "hi"); err == nil {
		t.Error("StartStream without bot token should error")
	}
}
