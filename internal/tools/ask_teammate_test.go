package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"
)

type fakeSoulResolver struct {
	canonical string
	content   string
	found     bool
	err       error
	gotName   string
}

func (f *fakeSoulResolver) ResolveSoul(_ context.Context, name string) (string, string, bool, error) {
	f.gotName = name
	return f.canonical, f.content, f.found, f.err
}

func newAskTeammateTool(resolver SoulResolver, persona PersonaRunner) *AskTeammateTool {
	t := &AskTeammateTool{resolver: resolver}
	if persona != nil {
		t.SetPersonaRunner(persona)
	}
	return t
}

func TestNormalizeTeammateName(t *testing.T) {
	cases := map[string]string{
		"alice":        "alice",
		"@alice":       "alice",
		"agent_alice":  "alice",
		"agent-alice":  "alice",
		"  agent_bob ": "bob",
		"AGENT_carol":  "carol",
		"agent_":       "agent_", // too short to be a prefix-only handle
	}
	for in, want := range cases {
		if got := normalizeTeammateName(in); got != want {
			t.Errorf("normalizeTeammateName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAskTeammateRequiresFields(t *testing.T) {
	tool := newAskTeammateTool(&fakeSoulResolver{found: true, content: "soul"},
		func(context.Context, string, string) (string, error) { return "ok", nil })

	res, _ := tool.Execute(context.Background(), json.RawMessage(`{"question":"hi"}`), "")
	if !res.IsError || !strings.Contains(res.Content, "name is required") {
		t.Fatalf("missing name: got %+v", res)
	}

	res, _ = tool.Execute(context.Background(), json.RawMessage(`{"name":"alice"}`), "")
	if !res.IsError || !strings.Contains(res.Content, "question is required") {
		t.Fatalf("missing question: got %+v", res)
	}
}

func TestAskTeammateUnknownTeammateIsNonFatal(t *testing.T) {
	tool := newAskTeammateTool(&fakeSoulResolver{found: false},
		func(context.Context, string, string) (string, error) { return "ok", nil })
	res, err := tool.Execute(context.Background(), json.RawMessage(`{"name":"ghost","question":"hi"}`), "")
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if res.IsError {
		t.Fatalf("unknown teammate should be non-fatal, got error: %s", res.Content)
	}
	if !strings.Contains(res.Content, "No teammate persona found") {
		t.Fatalf("unexpected content: %s", res.Content)
	}
}

func TestAskTeammateEmptySoulIsNonFatal(t *testing.T) {
	tool := newAskTeammateTool(&fakeSoulResolver{found: true, canonical: "alice", content: "   "},
		func(context.Context, string, string) (string, error) { return "should not run", nil })
	res, err := tool.Execute(context.Background(), json.RawMessage(`{"name":"agent_alice","question":"hi"}`), "")
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if res.IsError || !strings.Contains(res.Content, "has not written a SOUL") {
		t.Fatalf("expected non-fatal no-soul message, got %+v", res)
	}
}

func TestAskTeammateResolverErrorIsReported(t *testing.T) {
	tool := newAskTeammateTool(&fakeSoulResolver{err: errors.New("db down")},
		func(context.Context, string, string) (string, error) { return "ok", nil })
	res, _ := tool.Execute(context.Background(), json.RawMessage(`{"name":"alice","question":"hi"}`), "")
	if !res.IsError || !strings.Contains(res.Content, "failed to look up teammate") {
		t.Fatalf("expected resolver error reported, got %+v", res)
	}
}

func TestAskTeammateUnavailableWithoutPersonaRunner(t *testing.T) {
	tool := newAskTeammateTool(&fakeSoulResolver{found: true, content: "soul"}, nil)
	res, _ := tool.Execute(context.Background(), json.RawMessage(`{"name":"alice","question":"hi"}`), "")
	if !res.IsError || !strings.Contains(res.Content, "unavailable") {
		t.Fatalf("expected unavailable error, got %+v", res)
	}
}

func TestAskTeammateSuccessFramesPersonaReply(t *testing.T) {
	var gotSoul, gotPrompt string
	resolver := &fakeSoulResolver{found: true, canonical: "Alice", content: "I am Alice, I value tests."}
	tool := newAskTeammateTool(resolver, func(_ context.Context, soul, prompt string) (string, error) {
		gotSoul = soul
		gotPrompt = prompt
		return "I'd add a regression test first.", nil
	})

	input := `{"name":"agent_alice","question":"What about this plan?","material":"step 1\nstep 2"}`
	res, err := tool.Execute(context.Background(), json.RawMessage(input), "")
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if res.IsError {
		t.Fatalf("unexpected error: %s", res.Content)
	}
	if resolver.gotName != "alice" {
		t.Fatalf("resolver got name %q, want normalized 'alice'", resolver.gotName)
	}
	if gotSoul != "I am Alice, I value tests." {
		t.Fatalf("persona soul = %q", gotSoul)
	}
	if !strings.Contains(gotPrompt, "What about this plan?") || !strings.Contains(gotPrompt, "step 1") {
		t.Fatalf("prompt missing question/material: %q", gotPrompt)
	}
	if !strings.Contains(res.Content, "Alice's perspective:") || !strings.Contains(res.Content, "regression test") {
		t.Fatalf("reply not framed correctly: %s", res.Content)
	}
}

func TestAskTeammatePersonaErrorIsReported(t *testing.T) {
	tool := newAskTeammateTool(&fakeSoulResolver{found: true, canonical: "alice", content: "soul"},
		func(context.Context, string, string) (string, error) { return "", errors.New("model timeout") })
	res, _ := tool.Execute(context.Background(), json.RawMessage(`{"name":"alice","question":"hi"}`), "")
	if !res.IsError || !strings.Contains(res.Content, "failed to consult teammate") {
		t.Fatalf("expected persona error reported, got %+v", res)
	}
}

func TestAskTeammateIsReadOnly(t *testing.T) {
	tool := &AskTeammateTool{}
	if !tool.IsReadOnly() {
		t.Fatal("ask_teammate must be read-only so it is available in plan/read-only modes")
	}
}

func TestTruncateRunesIsUTF8Safe(t *testing.T) {
	// "é" is two bytes; cutting at an odd boundary must not split it.
	s := strings.Repeat("é", 100) // 200 bytes
	got := truncateRunes(s, 51)
	if !utf8.ValidString(got) {
		t.Fatalf("truncateRunes produced invalid UTF-8: %q", got)
	}
	if len(got) > 51 {
		t.Fatalf("truncateRunes len = %d, want <= 51", len(got))
	}
	if truncateRunes("short", 100) != "short" {
		t.Fatal("truncateRunes must return short strings unchanged")
	}
}
