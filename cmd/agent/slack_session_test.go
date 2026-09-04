package main

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
	"time"

	"github.com/slack-go/slack/slackevents"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	internalslack "github.com/gratefulagents/gratefulagents/internal/slack"
)

func TestSlackGoParsesAgentSessionStopped(t *testing.T) {
	payload := json.RawMessage(`{
		"token":"verification-token",
		"team_id":"T123",
		"api_app_id":"A123",
		"type":"event_callback",
		"event":{"type":"agent_session_stopped","channel":"C1","thread_ts":"1.1","user":"U1","event_ts":"2.2","streaming_message_ts":["3.3"]}
	}`)
	event, err := slackevents.ParseEvent(payload, slackevents.OptionNoVerifyToken())
	if err != nil {
		t.Fatalf("ParseEvent(agent_session_stopped): %v", err)
	}
	stop, ok := event.InnerEvent.Data.(*slackSessionStoppedEvent)
	if !ok {
		t.Fatalf("inner event type = %T, want *slackSessionStoppedEvent", event.InnerEvent.Data)
	}
	if stop.Channel != "C1" || stop.ThreadTS != "1.1" || stop.User != "U1" || len(stop.StreamTSs) != 1 {
		t.Fatalf("unexpected payload: %+v", stop)
	}
}

func TestSessionTitleFor(t *testing.T) {
	tests := []struct {
		name string
		d    internalslack.Decision
		want string
	}{
		{"text", internalslack.Decision{Text: "  Fix the flaky test\nand more"}, "Fix the flaky test"},
		{"single file", internalslack.Decision{Files: []internalslack.File{{Name: "log.txt"}}}, "Shared file: log.txt"},
		{"many files", internalslack.Decision{Files: []internalslack.File{{Name: "a"}, {Name: "b"}}}, "Shared 2 files"},
		{"empty", internalslack.Decision{}, ""},
	}
	for _, tt := range tests {
		if got := sessionTitleFor(tt.d); got != tt.want {
			t.Errorf("%s: sessionTitleFor = %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestMayStop(t *testing.T) {
	o := &slackOrchestrator{ownerUserID: "UOWNER", commanders: []string{" UCMD "}}
	if !o.mayStop("UOWNER") {
		t.Error("owner should be able to stop")
	}
	if !o.mayStop("UCMD") {
		t.Error("commander should be able to stop")
	}
	if o.mayStop("USTRANGER") {
		t.Error("stranger must not stop")
	}
	if (&slackOrchestrator{}).mayStop("UOWNER") {
		t.Error("unknown owner must fail closed")
	}
}

func TestStopSignalLifecycle(t *testing.T) {
	o := &slackOrchestrator{}
	if o.signalStop("run-1") {
		t.Fatal("signalStop without a watcher should report false")
	}
	ch := o.registerStop("run-1")
	if again := o.registerStop("run-1"); again != ch {
		t.Fatal("registerStop should return the same channel for the same run")
	}
	if !o.signalStop("run-1") {
		t.Fatal("signalStop with a watcher should report true")
	}
	select {
	case <-ch:
	default:
		t.Fatal("stop channel should be closed")
	}
	if o.signalStop("run-1") {
		t.Fatal("a consumed signal should not fire twice")
	}
	o.releaseStop("run-1") // must be safe after signalStop already removed it
}

func TestStreamTargetNamesRecipientOutsideDM(t *testing.T) {
	o := &slackOrchestrator{teamID: "T1"}
	dm := o.streamTarget(replyWatch{channelID: "D1", threadTS: "1.1", requester: "U1", channelType: "im"})
	if dm.RecipientUserID != "" || dm.RecipientTeamID != "" {
		t.Fatalf("DM target should omit recipient: %+v", dm)
	}
	ch := o.streamTarget(replyWatch{channelID: "C1", threadTS: "1.1", requester: "U1", channelType: "channel"})
	if ch.RecipientUserID != "U1" || ch.RecipientTeamID != "T1" {
		t.Fatalf("channel target should name recipient: %+v", ch)
	}
}

func TestInterruptRunAnnotatesActiveRun(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}
	running := &platformv1alpha1.AgentRun{}
	running.Name = "run-active"
	running.Namespace = "ns"
	running.Status.Phase = platformv1alpha1.AgentRunPhaseRunning
	done := &platformv1alpha1.AgentRun{}
	done.Name = "run-done"
	done.Namespace = "ns"
	done.Status.Phase = platformv1alpha1.AgentRunPhaseSucceeded
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(running, done).Build()
	o := &slackOrchestrator{crdClient: c, namespace: "ns", agentName: "me"}

	o.interruptRun(context.Background(), "run-active", "U1")
	o.interruptRun(context.Background(), "run-done", "U1")
	o.interruptRun(context.Background(), "run-missing", "U1")

	got := &platformv1alpha1.AgentRun{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "ns", Name: "run-active"}, got); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if _, ok := got.Annotations[platformv1alpha1.InterruptRequestedAnnotation]; !ok {
		t.Fatal("active run should carry the interrupt annotation")
	}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "ns", Name: "run-done"}, got); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if _, ok := got.Annotations[platformv1alpha1.InterruptRequestedAnnotation]; ok {
		t.Fatal("terminal run must not be interrupted")
	}
}

// fakeSlackAPI records Web API calls made by the connector's client.
type fakeSlackAPI struct {
	mu    sync.Mutex
	calls []string
	form  []url.Values
	fail  map[string]string
}

func newFakeSlackAPI(t *testing.T) (*fakeSlackAPI, *internalslack.Client) {
	t.Helper()
	f := &fakeSlackAPI{fail: map[string]string{}}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	c, err := internalslack.New(internalslack.Tokens{BotToken: "xoxb-test"},
		internalslack.WithAPIURL(srv.URL+"/api/"), internalslack.WithHTTPClient(srv.Client()))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return f, c
}

func (f *fakeSlackAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	method := strings.TrimPrefix(r.URL.Path, "/api/")
	body, _ := io.ReadAll(r.Body)
	values := url.Values{}
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		var raw map[string]any
		_ = json.Unmarshal(body, &raw)
		for k, v := range raw {
			if s, ok := v.(string); ok {
				values.Set(k, s)
			}
		}
	} else {
		values, _ = url.ParseQuery(string(body))
	}
	f.mu.Lock()
	f.calls = append(f.calls, method)
	f.form = append(f.form, values)
	failure := f.fail[method]
	f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if failure != "" {
		_, _ = w.Write([]byte(`{"ok":false,"error":"` + failure + `"}`))
		return
	}
	_, _ = w.Write([]byte(`{"ok":true,"channel":"C1","ts":"1700000000.000100"}`))
}

func (f *fakeSlackAPI) methods() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func TestDeliverReplyStreamsMessagesAsOne(t *testing.T) {
	f, web := newFakeSlackAPI(t)
	o := &slackOrchestrator{web: web, teamID: "T1", agentName: "me"}
	w := replyWatch{runName: "run-1", channelID: "C1", threadTS: "1.1", requester: "U1", channelType: "channel"}
	if !o.deliverReply(context.Background(), w, []string{"# First", "Second"}) {
		t.Fatal("deliverReply should succeed")
	}
	want := []string{"chat.startStream", "chat.appendStream", "chat.stopStream"}
	if got := f.methods(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("calls = %v, want %v", got, want)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.form[0].Get("markdown_text") != "# First" || f.form[0].Get("recipient_user_id") != "U1" || f.form[0].Get("recipient_team_id") != "T1" {
		t.Fatalf("unexpected startStream form: %v", f.form[0])
	}
	if !strings.Contains(f.form[1].Get("markdown_text"), "Second") {
		t.Fatalf("unexpected appendStream form: %v", f.form[1])
	}
	if f.form[2].Get("session_status") != "active" || !strings.Contains(f.form[2].Get("blocks"), "feedback_buttons") {
		t.Fatalf("unexpected stopStream form: %v", f.form[2])
	}
}

func TestDeliverReplyFallsBackToMarkdownPosts(t *testing.T) {
	f, web := newFakeSlackAPI(t)
	f.fail["chat.startStream"] = "missing_scope"
	o := &slackOrchestrator{web: web, agentName: "me"}
	w := replyWatch{runName: "run-1", channelID: "D1", threadTS: "1.1", requester: "U1", channelType: "im"}
	if !o.deliverReply(context.Background(), w, []string{"one", "two"}) {
		t.Fatal("deliverReply should succeed via fallback")
	}
	want := []string{"chat.startStream", "chat.postMessage", "chat.postMessage", "agents.sessions.setStatus"}
	if got := f.methods(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("calls = %v, want %v", got, want)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.form[1].Get("markdown_text") != "one" || f.form[1].Get("thread_ts") != "1.1" {
		t.Fatalf("fallback post should use markdown_text in thread: %v", f.form[1])
	}
	if f.form[3].Get("status") != "active" {
		t.Fatalf("fallback must return the session to active: %v", f.form[3])
	}
}

func TestDeliverReplyNothingToPost(t *testing.T) {
	f, web := newFakeSlackAPI(t)
	o := &slackOrchestrator{web: web}
	if o.deliverReply(context.Background(), replyWatch{channelID: "C1", threadTS: "1.1"}, nil) {
		t.Fatal("no texts should not count as delivered")
	}
	if len(f.methods()) != 0 {
		t.Fatal("no Slack calls expected")
	}
}

func TestHandleSessionStoppedWithoutRunClearsIndicator(t *testing.T) {
	f, web := newFakeSlackAPI(t)
	o := &slackOrchestrator{web: web, ownerUserID: "U1"}
	o.handleSessionStopped(context.Background(), &slackSessionStoppedEvent{Channel: "D1", ThreadTS: "1.1", User: "U1"})
	got := f.methods()
	if len(got) != 1 || got[0] != "agents.sessions.setStatus" {
		t.Fatalf("calls = %v, want a single setStatus", got)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.form[0].Get("status") != "active" {
		t.Fatalf("status = %q, want active", f.form[0].Get("status"))
	}
}

func TestHandleSessionStoppedIgnoresStrangers(t *testing.T) {
	f, web := newFakeSlackAPI(t)
	o := &slackOrchestrator{web: web, ownerUserID: "U1"}
	o.handleSessionStopped(context.Background(), &slackSessionStoppedEvent{Channel: "D1", ThreadTS: "1.1", User: "U2"})
	if len(f.methods()) != 0 {
		t.Fatal("strangers must not trigger Slack calls")
	}
}

func TestStreamRepliesConfirmsStop(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}
	run := &platformv1alpha1.AgentRun{}
	run.Name = "run-1"
	run.Namespace = "ns"
	run.Status.Phase = platformv1alpha1.AgentRunPhaseRunning
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run).Build()
	f, web := newFakeSlackAPI(t)
	o := &slackOrchestrator{web: web, crdClient: c, namespace: "ns", agentName: "me"}

	done := make(chan struct{})
	go func() {
		defer close(done)
		o.streamReplies(context.Background(), replyWatch{
			runName: "run-1", channelID: "C1", threadTS: "1.1", messageTS: "1.0", channelType: "channel",
		})
	}()
	deadline := time.Now().Add(5 * time.Second)
	for !o.signalStop("run-1") {
		if time.Now().After(deadline) {
			t.Fatal("watcher never registered a stop channel")
		}
		time.Sleep(10 * time.Millisecond)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("watcher did not exit after stop")
	}
	got := strings.Join(f.methods(), ",")
	for _, want := range []string{"chat.postMessage", "agents.sessions.setStatus", "reactions.remove", "reactions.add"} {
		if !strings.Contains(got, want) {
			t.Errorf("calls %q missing %s", got, want)
		}
	}
}
