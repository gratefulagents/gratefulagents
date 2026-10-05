package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	internalslack "github.com/gratefulagents/gratefulagents/internal/slack"
	"github.com/gratefulagents/gratefulagents/internal/store"
	"github.com/gratefulagents/gratefulagents/internal/store/postgres/sqlc"
	"github.com/jackc/pgx/v5"
	slackgo "github.com/slack-go/slack"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func operationalTestOrchestrator(t *testing.T, objects ...client.Object) (*slackOrchestrator, *fakeSlackAPI) {
	t.Helper()
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{
		platformv1alpha1.AddToScheme, triggersv1alpha1.AddToScheme, corev1.AddToScheme,
	} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	f, web := newFakeSlackAPI(t)
	return &slackOrchestrator{
		web:         web,
		crdClient:   fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build(),
		namespace:   "ns",
		agentName:   "me",
		ownerUserID: "UOWNER",
		commanders:  []string{"UCMD"},
		turnGates:   map[string]*sync.Mutex{},
	}, f
}

func operationalTestRun() *platformv1alpha1.AgentRun {
	return &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "run-one",
			Namespace: "ns",
			Labels:    map[string]string{slackAgentLabel: "me"},
			Annotations: map[string]string{
				"triggers.gratefulagents.dev/slack-channel": "C1",
				slackRequesterAnnotation:                    "UCMD",
			},
		},
		Spec: platformv1alpha1.AgentRunSpec{
			Trigger: platformv1alpha1.TriggerRef{Kind: slackTriggerKind},
			Repository: platformv1alpha1.RepositoryContext{
				URL:        "https://github.com/acme/one",
				BaseBranch: "main",
			},
		},
		Status: platformv1alpha1.AgentRunStatus{Phase: platformv1alpha1.AgentRunPhasePaused},
	}
}

func TestSlackOperationalAuthorization(t *testing.T) {
	run := operationalTestRun()
	foreign := run.DeepCopy()
	foreign.Name = "foreign"
	foreign.Labels[slackAgentLabel] = "other"
	otherNS := run.DeepCopy()
	otherNS.Name = "other-ns"
	otherNS.Namespace = "elsewhere"
	wrongTrigger := run.DeepCopy()
	wrongTrigger.Name = "cron"
	wrongTrigger.Spec.Trigger.Kind = "Cron"
	o, f := operationalTestOrchestrator(t, run, foreign, otherNS, wrongTrigger)
	for _, user := range []string{"UOWNER", "UCMD"} {
		if _, err := o.operationalRun(context.Background(), user, run.Name); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"foreign", "other-ns", "cron", "missing"} {
		if _, err := o.operationalRun(context.Background(), "UOWNER", name); err == nil {
			t.Fatalf("exposed %s", name)
		}
	}
	if _, err := o.operationalRun(context.Background(), "stranger", run.Name); err == nil {
		t.Fatal("exposed run to stranger")
	}
	o.handleOperationalAction(context.Background(), operationalAction("stranger", "slack_ops_stop", run.Name))
	if len(f.methods()) != 0 {
		t.Fatal("unauthorized action made API calls")
	}
	runs, err := o.operationalRuns(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].Name != "run-one" {
		t.Fatalf("runs leaked across agent scope: %+v", runs)
	}
}

func TestSlackSelectedDefaults(t *testing.T) {
	defaults := triggersv1alpha1.AgentRunDefaults{
		RepoURL:         "https://github.com/acme/one",
		BaseBranch:      "main",
		AdditionalRepos: []string{"https://github.com/acme/two"},
	}
	selected, err := slackSelectedDefaults(defaults, defaults.AdditionalRepos[0], "release/v2")
	if err != nil {
		t.Fatal(err)
	}
	if selected.RepoURL != defaults.AdditionalRepos[0] || selected.BaseBranch != "release/v2" ||
		len(selected.AdditionalRepos) != 1 ||
		selected.AdditionalRepos[0] != defaults.RepoURL {
		t.Fatalf("wrong selection: %+v", selected)
	}
	if defaults.AdditionalRepos[0] != "https://github.com/acme/two" {
		t.Fatal("mutated defaults")
	}
	for _, branch := range []string{
		"-evil", "a..b", "a//b", "a/", "a/.hidden", "foo.lock", "foo.lock/bar", "a@{b",
		"a b", "a;ls", "a\\b", strings.Repeat("a", 256),
	} {
		if _, err := slackSelectedDefaults(defaults, "", branch); err == nil {
			t.Errorf("accepted branch %q", branch)
		}
	}
	if _, err := slackSelectedDefaults(defaults, "https://github.com/private/other", "main"); err == nil {
		t.Fatal("accepted unconfigured repository")
	}
}

func TestSlackHomePrivacyAndControls(t *testing.T) {
	run := operationalTestRun()
	run.Status.Phase = platformv1alpha1.AgentRunPhaseRunning
	o, f := operationalTestOrchestrator(t, run)
	o.handleAppHome(context.Background(), "stranger")
	if got := f.form[0].Get(
		"view",
	); strings.Contains(got, "run-one") ||
		strings.Contains(got, "slack_ops_stop") {
		t.Fatalf("Home leaked state: %s", got)
	}
	o.handleAppHome(context.Background(), "UOWNER")
	got := f.form[1].Get("view")
	for _, want := range []string{"run-one", "Running", "slack_ops_stop", "slack_ops_resume", "slack_ops_refresh"} {
		if !strings.Contains(got, want) {
			t.Errorf("Home missing %s: %s", want, got)
		}
	}
	o.ownerUserID = ""
	o.handleAppHome(context.Background(), "UOWNER")
	if strings.Contains(f.form[2].Get("view"), "run-one") {
		t.Fatal("unknown owner did not clear operational view")
	}
}

type slackResumeStore struct {
	workspaceSnapshotMetadataStore
	appended []string
}

func (s *slackResumeStore) GetMessages(context.Context, uuid.UUID) ([]store.Message, error) {
	return nil, nil
}

func (s *slackResumeStore) AppendMessage(
	_ context.Context,
	_ uuid.UUID,
	role, content string,
	_ json.RawMessage,
) (*store.Message, error) {
	s.appended = append(s.appended, content)
	return &store.Message{Role: role, Content: content}, nil
}

func TestSlackCreateRunUsesSelectedTarget(t *testing.T) {
	agent := &triggersv1alpha1.SlackAgent{ObjectMeta: metav1.ObjectMeta{Name: "me", Namespace: "ns"}}
	agent.Spec.Defaults = triggersv1alpha1.AgentRunDefaults{
		RepoURL:    "https://github.com/acme/one",
		BaseBranch: "main",
		Provider:   triggersv1alpha1.ProviderCopilot,
		AuthMode:   platformv1alpha1.AgentRunAuthModeOAuth,
		Secrets:    triggersv1alpha1.AgentRunSecrets{OpenAIOAuthSecret: "usercred-copilot"},
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: slackSavedGitHubSecretName, Namespace: "ns"},
		Data:       map[string][]byte{slackSavedGitHubTokenKey: []byte("token")},
	}
	o, _ := operationalTestOrchestrator(t, agent, secret)
	selected, err := slackSelectedDefaults(agent.Spec.Defaults, agent.Spec.Defaults.RepoURL, "release/v2")
	if err != nil {
		t.Fatal(err)
	}
	if err := o.createRunWithDefaults(
		context.Background(),
		"selected",
		internalslack.Decision{ChannelID: "D1", UserID: "UCMD"},
		"task",
		selected,
	); err != nil {
		t.Fatal(err)
	}
	run := &platformv1alpha1.AgentRun{}
	if err := o.crdClient.Get(
		context.Background(),
		client.ObjectKey{Namespace: "ns", Name: "selected"},
		run,
	); err != nil {
		t.Fatal(err)
	}
	if run.Spec.Repository.BaseBranch != "release/v2" || run.Annotations[slackRequesterAnnotation] != "UCMD" {
		t.Fatalf("target/requester not persisted: %+v", run)
	}
}

func operationalAction(user, action, value string) slackgo.InteractionCallback {
	return slackgo.InteractionCallback{
		User:      slackgo.User{ID: user},
		TriggerID: "trigger",
		Type:      slackgo.InteractionTypeBlockActions,
		ActionCallback: slackgo.ActionCallbacks{
			BlockActions: []*slackgo.BlockAction{{ActionID: action, Value: value}},
		},
	}
}

func operationalSubmission(
	user, callbackID, metadata, repo, branch, task string,
) slackgo.InteractionCallback {
	return slackgo.InteractionCallback{
		Type: slackgo.InteractionTypeViewSubmission,
		User: slackgo.User{ID: user},
		View: slackgo.View{
			ID:              "V1",
			CallbackID:      callbackID,
			PrivateMetadata: metadata,
			State: &slackgo.ViewState{Values: map[string]map[string]slackgo.BlockAction{
				internalslack.BlockRunRepository: {
					internalslack.ActionRunInput: {
						SelectedOption: slackgo.OptionBlockObject{
							Value: internalslack.RepositoryOptionValue(repo),
						},
					},
				},
				internalslack.BlockRunBranch: {internalslack.ActionRunInput: {Value: branch}},
				internalslack.BlockRunTask:   {internalslack.ActionRunInput: {Value: task}},
			}},
		},
	}
}

func TestSlackUIStop(t *testing.T) {
	run := operationalTestRun()
	run.Status.Phase = platformv1alpha1.AgentRunPhaseRunning
	o, f := operationalTestOrchestrator(t, run)
	ss := &interruptActivityStore{
		workspaceSnapshotMetadataStore: workspaceSnapshotMetadataStore{
			session: &store.Session{ID: uuid.New()},
		},
	}
	o.store = ss
	stopped := o.registerStop("run-one")
	o.handleInteraction(context.Background(), operationalAction("UCMD", "slack_ops_stop", "run-one"))
	if !strings.Contains(string(ss.session.Metadata), "interrupt") {
		t.Fatal("stop not persisted")
	}
	select {
	case <-stopped:
	default:
		t.Fatal("watcher not stopped")
	}
	if !strings.Contains(f.form[1].Get("text"), "Stop requested") || f.form[1].Get("channel") != "D1" {
		t.Fatal("missing private confirmation")
	}
}

func TestSlackUIResume(t *testing.T) {
	run := operationalTestRun()
	o, _ := operationalTestOrchestrator(t, run)
	o.store = &slackResumeStore{
		workspaceSnapshotMetadataStore: workspaceSnapshotMetadataStore{
			session: &store.Session{ID: uuid.New()},
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		o.handleInteraction(
			ctx,
			operationalSubmission(
				"UOWNER",
				internalslack.CallbackRunResume,
				"ns/me/run-one",
				"",
				"",
				"finish tests",
			),
		)
	}()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		got := &platformv1alpha1.AgentRun{}
		if err := o.crdClient.Get(ctx, client.ObjectKeyFromObject(run), got); err != nil {
			t.Fatal(err)
		}
		if got.Spec.WakeRequests == 1 {
			cancel()
			<-done
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done
	t.Fatal("resume did not wake run")
}

func TestSlackUIStartValidatesLiveTargets(t *testing.T) {
	agent := &triggersv1alpha1.SlackAgent{ObjectMeta: metav1.ObjectMeta{Name: "me", Namespace: "ns"}}
	agent.Spec.Defaults = triggersv1alpha1.AgentRunDefaults{
		RepoURL:    "https://github.com/acme/one",
		BaseBranch: "main",
		Provider:   triggersv1alpha1.ProviderCopilot,
		AuthMode:   platformv1alpha1.AgentRunAuthModeOAuth,
		Secrets:    triggersv1alpha1.AgentRunSecrets{OpenAIOAuthSecret: "usercred-copilot"},
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: slackSavedGitHubSecretName, Namespace: "ns"},
		Data:       map[string][]byte{slackSavedGitHubTokenKey: []byte("token")},
	}
	o, f := operationalTestOrchestrator(t, agent, secret)
	o.handleInteraction(context.Background(), operationalAction("UCMD", "slack_ops_start", "start"))
	if f.calls[0] != "views.open" || !strings.Contains(f.form[0].Get("view"), "ops_repository") {
		t.Fatal("New Run did not open a repository form")
	}
	for _, input := range []struct{ user, key, repo, branch, task string }{
		{"stranger", "ns/me", agent.Spec.Defaults.RepoURL, "main", "task"},
		{"UOWNER", "ns/other", agent.Spec.Defaults.RepoURL, "main", "task"},
		{"UOWNER", "ns/me", "https://github.com/private/other", "main", "task"},
		{"UOWNER", "ns/me", agent.Spec.Defaults.RepoURL, "../bad", "task"},
		{"UOWNER", "ns/me", agent.Spec.Defaults.RepoURL, "main", ""},
	} {
		o.handleInteraction(
			context.Background(),
			operationalSubmission(
				input.user,
				internalslack.CallbackRunStart,
				input.key,
				input.repo,
				input.branch,
				input.task,
			),
		)
	}
	list := &platformv1alpha1.AgentRunList{}
	if err := o.crdClient.List(context.Background(), list); err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 0 {
		t.Fatal("invalid submission created a run")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	o.handleInteraction(
		ctx,
		operationalSubmission(
			"UOWNER",
			internalslack.CallbackRunStart,
			"ns/me",
			agent.Spec.Defaults.RepoURL,
			"release/v2",
			"do work",
		),
	)
	if err := o.crdClient.List(context.Background(), list); err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 1 || list.Items[0].Spec.Repository.BaseBranch != "release/v2" {
		t.Fatalf("target not persisted: %+v", list.Items)
	}
}

func TestSlackUIWorkspaceRouting(t *testing.T) {
	o, f := operationalTestOrchestrator(t, operationalTestRun())
	member := &workspaceMember{
		orch:       o,
		userID:     "UOWNER",
		commanders: []string{"UCMD"},
		ctx:        context.Background(),
	}
	workspace := &workspaceSlackBackend{members: map[string]*workspaceMember{"UOWNER": member}}
	workspace.handleInteraction(
		context.Background(),
		operationalAction("UCMD", "slack_ops_resume", "run-one"),
	)
	if len(f.methods()) != 1 || f.calls[0] != "views.open" {
		t.Fatal("unambiguous commander not routed")
	}
	workspace.members["OTHER"] = &workspaceMember{
		commanders: []string{"UCMD"},
		orch:       o,
		ctx:        context.Background(),
	}
	workspace.handleInteraction(
		context.Background(),
		operationalAction("UCMD", "slack_ops_resume", "run-one"),
	)
	if len(f.methods()) != 1 {
		t.Fatal("ambiguous commander executed an action")
	}
}

func TestSlackUIStopDoesNotQueueInterruptForInactiveRun(t *testing.T) {
	o, f := operationalTestOrchestrator(t, operationalTestRun())
	o.handleInteraction(context.Background(), operationalAction("UOWNER", "slack_ops_stop", "run-one"))
	if len(f.methods()) != 2 || !strings.Contains(f.form[1].Get("text"), "No running turn") {
		t.Fatal("inactive stop should report no running turn without touching the store")
	}
}

type homeQueryRecorder struct {
	sqlc.DBTX
	args []any
}

func (r *homeQueryRecorder) Query(_ context.Context, _ string, args ...any) (pgx.Rows, error) {
	r.args = args
	return nil, errors.New("query recorded")
}
func TestSlackHomeApprovalsAreNamespaceScopedAndOwnerOnly(t *testing.T) {
	o, _ := operationalTestOrchestrator(t, operationalTestRun())
	recorder := &homeQueryRecorder{}
	o.queries = sqlc.New(recorder)
	o.operationalHomeBlocks(context.Background(), "UOWNER")
	if len(recorder.args) != 4 || recorder.args[0] != "ns" || recorder.args[1] != "me" ||
		recorder.args[2] != slackDraftPending {
		t.Fatalf("approvals query not scoped: %v", recorder.args)
	}
	recorder.args = nil
	o.operationalHomeBlocks(context.Background(), "UCMD")
	if recorder.args != nil {
		t.Fatal("commander could query owner approvals")
	}
}

func TestSlackHomeRendersRunDetailsAndPullRequests(t *testing.T) {
	run := operationalTestRun()
	run.Status.Phase = platformv1alpha1.AgentRunPhaseRunning
	m := operationalTestMonitor()
	m.Status.Title = "Fix login"
	o, f := operationalTestOrchestrator(t, run, m)
	o.handleAppHome(context.Background(), "UCMD")
	view := f.form[0].Get("view")
	for _, want := range []string{
		"`acme/one`", "`main`", "acme/one#1 — Fix login", "checks passed", "Stop this run?",
	} {
		if !strings.Contains(view, want) {
			t.Errorf("Home missing %q: %s", want, view)
		}
	}
	if strings.Contains(view, "waiting for your approval") {
		t.Error("commander Home must not show the owner's approval backlog")
	}
}

func TestSlackValidateOperationalSubmissionReportsFieldErrorsInline(t *testing.T) {
	agent := &triggersv1alpha1.SlackAgent{ObjectMeta: metav1.ObjectMeta{Name: "me", Namespace: "ns"}}
	agent.Spec.Defaults = triggersv1alpha1.AgentRunDefaults{RepoURL: "https://github.com/acme/one", BaseBranch: "main"}
	o, f := operationalTestOrchestrator(t, agent)
	ctx := context.Background()

	bad := operationalSubmission("UOWNER", internalslack.CallbackRunStart, "ns/me",
		"https://github.com/private/other", "../bad", "")
	errs := o.validateOperationalSubmission(ctx, bad)
	for _, block := range []string{
		internalslack.BlockRunRepository, internalslack.BlockRunBranch, internalslack.BlockRunTask,
	} {
		if errs[block] == "" {
			t.Errorf("missing inline error for %s: %v", block, errs)
		}
	}
	if len(f.methods()) != 0 {
		t.Fatal("validation must not call Slack")
	}

	good := operationalSubmission("UOWNER", internalslack.CallbackRunStart, "ns/me",
		agent.Spec.Defaults.RepoURL, "release/v2", "do work")
	if errs := o.validateOperationalSubmission(ctx, good); len(errs) != 0 {
		t.Fatalf("valid submission rejected: %v", errs)
	}
	for _, skipped := range []slackgo.InteractionCallback{
		operationalSubmission("stranger", internalslack.CallbackRunStart, "ns/me", "", "../bad", ""),
		operationalSubmission("UOWNER", internalslack.CallbackRunStart, "ns/other", "", "../bad", ""),
		operationalSubmission("UOWNER", internalslack.CallbackRunResume, "ns/me/run-one", "", "../bad", ""),
	} {
		if errs := o.validateOperationalSubmission(ctx, skipped); len(errs) != 0 {
			t.Fatalf("validation leaked to an unbound submission: %v", errs)
		}
	}
	payload := viewSubmissionErrors(map[string]string{internalslack.BlockRunBranch: "bad"})
	raw, _ := json.Marshal(payload)
	if string(raw) != `{"errors":{"ops_branch":"bad"},"response_action":"errors"}` {
		t.Fatalf("ack payload = %s", raw)
	}
}

func TestSlackUIResumeBlankInstructionsUsesMessageFreeWake(t *testing.T) {
	run := operationalTestRun()
	run.Status.Phase = platformv1alpha1.AgentRunPhaseFailed
	o, f := operationalTestOrchestrator(t, run)
	resumeStore := &slackResumeStore{
		workspaceSnapshotMetadataStore: workspaceSnapshotMetadataStore{
			session: &store.Session{ID: uuid.New()},
		},
	}
	o.store = resumeStore
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	o.handleInteraction(ctx, operationalSubmission(
		"UCMD", internalslack.CallbackRunResume, "ns/me/run-one", "", "", "   "))
	got := &platformv1alpha1.AgentRun{}
	if err := o.crdClient.Get(context.Background(), client.ObjectKeyFromObject(run), got); err != nil {
		t.Fatal(err)
	}
	if got.Spec.WakeRequests != 1 {
		t.Fatalf("wakeRequests = %d, want 1", got.Spec.WakeRequests)
	}
	if len(resumeStore.appended) != 0 {
		t.Fatalf("blank resume queued a synthetic user message: %v", resumeStore.appended)
	}
	if !strings.Contains(string(resumeStore.session.Metadata), "resume_request") {
		t.Fatalf("blank resume did not record a resume request: %s", resumeStore.session.Metadata)
	}
	var confirmed bool
	for i, method := range f.calls {
		if method == "chat.postMessage" && strings.Contains(f.form[i].Get("text"), "continuing the previous task") {
			confirmed = true
		}
	}
	if !confirmed {
		t.Fatalf("missing resume confirmation: %v", f.methods())
	}
}
