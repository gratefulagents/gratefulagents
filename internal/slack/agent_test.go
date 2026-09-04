package slack

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	slackgo "github.com/slack-go/slack"
)

// fakeSlack is a minimal Web API stand-in recording every call it receives.
type fakeSlack struct {
	t *testing.T

	mu    sync.Mutex
	calls []fakeCall
	// fail maps a method name to an error string returned as {"ok":false}.
	fail map[string]string
}

type fakeCall struct {
	Method string
	Auth   string
	// Params merges JSON-body and form-encoded parameters into one view.
	Params map[string]string
}

func newFakeSlack(t *testing.T) (*fakeSlack, *Client) {
	t.Helper()
	f := &fakeSlack{t: t, fail: map[string]string{}}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	c, err := New(Tokens{BotToken: "xoxb-test"}, WithAPIURL(srv.URL+"/api/"), WithHTTPClient(srv.Client()))
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	return f, c
}

func (f *fakeSlack) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	method := strings.TrimPrefix(r.URL.Path, "/api/")
	body, _ := io.ReadAll(r.Body)
	params := map[string]string{}
	ct := r.Header.Get("Content-Type")
	switch {
	case strings.HasPrefix(ct, "application/json"):
		var raw map[string]any
		if err := json.Unmarshal(body, &raw); err != nil {
			f.t.Errorf("%s: invalid JSON body: %v", method, err)
		}
		for k, v := range raw {
			switch tv := v.(type) {
			case string:
				params[k] = tv
			default:
				b, _ := json.Marshal(tv)
				params[k] = string(b)
			}
		}
	default:
		values, err := url.ParseQuery(string(body))
		if err != nil {
			f.t.Errorf("%s: invalid form body: %v", method, err)
		}
		for k := range values {
			params[k] = values.Get(k)
		}
	}
	auth := r.Header.Get("Authorization")
	if auth == "" && params["token"] != "" {
		auth = "Bearer " + params["token"]
	}
	f.mu.Lock()
	f.calls = append(f.calls, fakeCall{Method: method, Auth: auth, Params: params})
	failure := f.fail[method]
	f.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	if failure != "" {
		_, _ = w.Write([]byte(`{"ok":false,"error":"` + failure + `"}`))
		return
	}
	_, _ = w.Write([]byte(`{"ok":true,"channel":"C1","ts":"1700000000.000100","status":"processing","agent_status":"processing"}`))
}

func (f *fakeSlack) last() fakeCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) == 0 {
		f.t.Fatal("no Slack calls recorded")
	}
	return f.calls[len(f.calls)-1]
}

func TestSetSessionStatusPostsJSON(t *testing.T) {
	f, c := newFakeSlack(t)
	err := c.SetSessionStatus(context.Background(), SessionParams{
		ChannelID: "C1", ThreadTS: "1.1", Status: SessionProcessing,
		Title: "  Fix the   flaky test\nmore detail", InitiatorUserID: "U1",
	})
	if err != nil {
		t.Fatalf("SetSessionStatus() error: %v", err)
	}
	call := f.last()
	if call.Method != "agents.sessions.setStatus" {
		t.Fatalf("method = %q", call.Method)
	}
	if call.Auth != "Bearer xoxb-test" {
		t.Errorf("auth = %q", call.Auth)
	}
	want := map[string]string{
		"channel_id": "C1", "thread_ts": "1.1", "status": "processing",
		"title": "Fix the flaky test", "initiator_user_id": "U1",
	}
	for k, v := range want {
		if call.Params[k] != v {
			t.Errorf("%s = %q, want %q", k, call.Params[k], v)
		}
	}
}

func TestSetSessionStatusOmitsEmptyOptionals(t *testing.T) {
	f, c := newFakeSlack(t)
	if err := c.SetSessionStatus(context.Background(), SessionParams{ChannelID: "C1", ThreadTS: "1.1", Status: SessionActive}); err != nil {
		t.Fatalf("SetSessionStatus() error: %v", err)
	}
	call := f.last()
	for _, k := range []string{"title", "initiator_user_id"} {
		if _, ok := call.Params[k]; ok {
			t.Errorf("%s should be omitted when empty", k)
		}
	}
}

func TestSetSessionStatusSurfacesSlackError(t *testing.T) {
	f, c := newFakeSlack(t)
	f.fail["agents.sessions.setStatus"] = "thread_ts_required"
	err := c.SetSessionStatus(context.Background(), SessionParams{ChannelID: "C1", ThreadTS: "1.1", Status: SessionActive})
	if err == nil || !strings.Contains(err.Error(), "thread_ts_required") {
		t.Fatalf("error = %v, want thread_ts_required", err)
	}
}

func TestSetSessionStatusValidatesInput(t *testing.T) {
	_, c := newFakeSlack(t)
	if err := c.SetSessionStatus(context.Background(), SessionParams{ChannelID: "C1", Status: SessionActive}); err == nil {
		t.Error("missing thread should error")
	}
	if err := c.SetSessionStatus(context.Background(), SessionParams{ChannelID: "C1", ThreadTS: "1.1"}); err == nil {
		t.Error("missing status should error")
	}
}

func TestRenameSession(t *testing.T) {
	f, c := newFakeSlack(t)
	if err := c.RenameSession(context.Background(), "C1", "1.1", "New title"); err != nil {
		t.Fatalf("RenameSession() error: %v", err)
	}
	call := f.last()
	if call.Method != "agents.sessions.rename" || call.Params["title"] != "New title" || call.Params["thread_ts"] != "1.1" {
		t.Fatalf("unexpected call: %+v", call)
	}
}

func TestSessionTitle(t *testing.T) {
	if got := SessionTitle("\n\n  hello    world \nsecond"); got != "hello world" {
		t.Errorf("SessionTitle = %q", got)
	}
	long := strings.Repeat("é", MaxSessionTitleChars+50)
	if got := SessionTitle(long); len([]rune(got)) != MaxSessionTitleChars {
		t.Errorf("SessionTitle length = %d, want %d", len([]rune(got)), MaxSessionTitleChars)
	}
	if got := SessionTitle("   \n "); got != "" {
		t.Errorf("SessionTitle(blank) = %q", got)
	}
}

func TestStreamLifecycle(t *testing.T) {
	f, c := newFakeSlack(t)
	ctx := context.Background()
	ts, err := c.StartStream(ctx, StreamTarget{ChannelID: "C1", ThreadTS: "1.1", RecipientUserID: "U9", RecipientTeamID: "T9"}, "# Hello")
	if err != nil {
		t.Fatalf("StartStream() error: %v", err)
	}
	if ts != "1700000000.000100" {
		t.Fatalf("ts = %q", ts)
	}
	start := f.last()
	if start.Method != "chat.startStream" {
		t.Fatalf("method = %q", start.Method)
	}
	for k, v := range map[string]string{
		"channel": "C1", "thread_ts": "1.1", "markdown_text": "# Hello",
		"recipient_user_id": "U9", "recipient_team_id": "T9",
	} {
		if start.Params[k] != v {
			t.Errorf("startStream %s = %q, want %q", k, start.Params[k], v)
		}
	}

	if err := c.AppendStream(ctx, "C1", ts, "more **text**"); err != nil {
		t.Fatalf("AppendStream() error: %v", err)
	}
	app := f.last()
	if app.Method != "chat.appendStream" || app.Params["ts"] != ts || app.Params["markdown_text"] != "more **text**" {
		t.Fatalf("unexpected appendStream call: %+v", app)
	}

	if err := c.StopStream(ctx, "C1", ts, "done", SessionActive, BuildReplyFeedbackBlocks("run-1")...); err != nil {
		t.Fatalf("StopStream() error: %v", err)
	}
	stop := f.last()
	if stop.Method != "chat.stopStream" {
		t.Fatalf("method = %q", stop.Method)
	}
	if stop.Params["ts"] != ts || stop.Params["session_status"] != "active" || stop.Params["markdown_text"] != "done" {
		t.Fatalf("unexpected stopStream params: %+v", stop.Params)
	}
	if !strings.Contains(stop.Params["blocks"], `"feedback_buttons"`) || !strings.Contains(stop.Params["blocks"], "reply_feedback:run-1") {
		t.Fatalf("stopStream blocks missing feedback buttons: %s", stop.Params["blocks"])
	}
}

func TestStartStreamOmitsRecipientInDM(t *testing.T) {
	f, c := newFakeSlack(t)
	if _, err := c.StartStream(context.Background(), StreamTarget{ChannelID: "D1", ThreadTS: "1.1"}, "hi"); err != nil {
		t.Fatalf("StartStream() error: %v", err)
	}
	call := f.last()
	for _, k := range []string{"recipient_user_id", "recipient_team_id"} {
		if _, ok := call.Params[k]; ok {
			t.Errorf("%s should be omitted when empty", k)
		}
	}
}

func TestAppendStreamSkipsEmptyChunk(t *testing.T) {
	f, c := newFakeSlack(t)
	if err := c.AppendStream(context.Background(), "C1", "1.1", "   "); err != nil {
		t.Fatalf("AppendStream() error: %v", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) != 0 {
		t.Fatalf("empty chunk should not call Slack; got %d calls", len(f.calls))
	}
}

func TestPostMarkdownAsBot(t *testing.T) {
	f, c := newFakeSlack(t)
	if _, err := c.PostMarkdownAsBot(context.Background(), "C1", "**bold**", "1.1"); err != nil {
		t.Fatalf("PostMarkdownAsBot() error: %v", err)
	}
	call := f.last()
	if call.Method != "chat.postMessage" || call.Params["markdown_text"] != "**bold**" || call.Params["thread_ts"] != "1.1" {
		t.Fatalf("unexpected call: %+v", call)
	}
	if _, ok := call.Params["text"]; ok {
		t.Error("markdown post should not send mrkdwn text")
	}
}

func TestTruncateMarkdown(t *testing.T) {
	long := strings.Repeat("ü", MaxMarkdownChars+10)
	got := TruncateMarkdown(long)
	if n := len([]rune(got)); n != MaxMarkdownChars {
		t.Fatalf("rune length = %d, want %d", n, MaxMarkdownChars)
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatal("truncated markdown should end with an ellipsis")
	}
	if TruncateMarkdown("  short  ") != "short" {
		t.Fatal("short markdown should be trimmed only")
	}
}

func TestReplyFeedbackRun(t *testing.T) {
	blocks := BuildReplyFeedbackBlocks("slack-abc-1")
	ca, ok := blocks[0].(*slackgo.ContextActionsBlock)
	if !ok {
		t.Fatalf("block type = %T", blocks[0])
	}
	if got := ReplyFeedbackRun(ca.BlockID); got != "slack-abc-1" {
		t.Fatalf("ReplyFeedbackRun = %q", got)
	}
	if ReplyFeedbackRun("other") != "" {
		t.Fatal("non-feedback block ID should yield empty run")
	}
}
