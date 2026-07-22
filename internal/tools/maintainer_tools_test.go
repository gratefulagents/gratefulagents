package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/orchestration"
	"github.com/gratefulagents/gratefulagents/internal/store"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const (
	maintainerTestNamespace      = "default"
	maintainerTestRepositoryName = "repo"
	maintainerTestRepositoryKind = "GitHubRepository"
	maintainerTestRunUID         = "maintainer"
	maintainerTestOther          = "other"
	maintainerTestRunName        = "repo-maintainer"
	maintainerTestFleetRunName   = "implementer"
)

type maintainerTestStore struct {
	store.StateStore
	sessions      map[string]*store.Session
	sessionErr    error
	beforeSession func()
	messages      []store.Message
	activity      []store.ActivityEvent
	activityErr   error
}

func (s *maintainerTestStore) GetSessionByRun(_ context.Context, name, namespace string) (*store.Session, error) {
	if s.beforeSession != nil {
		s.beforeSession()
		s.beforeSession = nil
	}
	if s.sessionErr != nil {
		return nil, s.sessionErr
	}
	return s.sessions[namespace+"/"+name], nil
}

func (s *maintainerTestStore) AppendMessage(_ context.Context, sessionID uuid.UUID, role, content string, metadata json.RawMessage) (*store.Message, error) {
	message := store.Message{ID: int64(len(s.messages) + 1), SessionID: sessionID, Role: role, Content: content, Metadata: metadata}
	s.messages = append(s.messages, message)
	return &message, nil
}

func (s *maintainerTestStore) WriteActivityEvent(_ context.Context, sessionID uuid.UUID, eventType, summary string, detail json.RawMessage) (*store.ActivityEvent, error) {
	if s.activityErr != nil {
		return nil, s.activityErr
	}
	event := store.ActivityEvent{ID: int64(len(s.activity) + 1), SessionID: sessionID, EventType: eventType, Summary: summary, Detail: detail}
	s.activity = append(s.activity, event)
	return &event, nil
}

func newMaintainerToolBase(t *testing.T, runs ...*platformv1alpha1.AgentRun) (maintainerToolBase, client.Client, *maintainerTestStore) {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := triggersv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	repository := &triggersv1alpha1.GitHubRepository{ObjectMeta: metav1.ObjectMeta{Name: maintainerTestRepositoryName, Namespace: maintainerTestNamespace, UID: types.UID("repo-uid")}}
	objects := make([]client.Object, 0, len(runs)+1)
	objects = append(objects, repository)
	for _, run := range runs {
		objects = append(objects, run)
		if run.Labels[orchestration.StandingRunRoleLabel] == orchestration.StandingRunRoleMaintainer {
			controller := true
			objects = append(objects, &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name: triggersv1alpha1.MaintainerCommandCapabilitySecretName(run.Name), Namespace: run.Namespace,
					OwnerReferences: []metav1.OwnerReference{{APIVersion: platformv1alpha1.GroupVersion.String(), Kind: "AgentRun", Name: run.Name, UID: run.UID, Controller: &controller}},
				},
				Data: map[string][]byte{
					triggersv1alpha1.MaintainerCommandCapabilitySecretKey:         []byte("01234567890123456789012345678901"),
					triggersv1alpha1.MaintainerCommandCapabilityRepositoryNameKey: []byte(repository.Name),
					triggersv1alpha1.MaintainerCommandCapabilityRepositoryUIDKey:  []byte(repository.UID),
				},
			})
		}
	}
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
	stateStore := &maintainerTestStore{sessions: map[string]*store.Session{}}
	for _, run := range runs {
		stateStore.sessions[run.Namespace+"/"+run.Name] = &store.Session{ID: uuid.New(), AgentRunName: run.Name, AgentRunNS: run.Namespace}
	}
	return maintainerToolBase{stateStore: stateStore, k8sClient: k8sClient, currentRunName: maintainerTestRunName, currentRunNamespace: maintainerTestNamespace, repositoryName: maintainerTestRepositoryName, repositoryNamespace: maintainerTestNamespace}, k8sClient, stateStore
}

func maintainerRun() *platformv1alpha1.AgentRun {
	controller := true
	return &platformv1alpha1.AgentRun{ObjectMeta: metav1.ObjectMeta{
		Name: maintainerTestRunName, Namespace: maintainerTestNamespace, UID: types.UID(maintainerTestRunUID),
		Labels:          map[string]string{orchestration.StandingRunRoleLabel: orchestration.StandingRunRoleMaintainer, orchestration.SupervisedRunLabel: maintainerTestRepositoryName},
		OwnerReferences: []metav1.OwnerReference{{Kind: maintainerTestRepositoryKind, Name: maintainerTestRepositoryName, Controller: &controller}},
	}}
}

func fleetRun(name string, phase platformv1alpha1.AgentRunPhase) *platformv1alpha1.AgentRun {
	return &platformv1alpha1.AgentRun{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: maintainerTestNamespace}, Spec: platformv1alpha1.AgentRunSpec{Trigger: platformv1alpha1.TriggerRef{Kind: maintainerTestRepositoryKind, Name: maintainerTestRepositoryName}}, Status: platformv1alpha1.AgentRunStatus{Phase: phase}}
}

func TestMaintainerAuthorizationAndFleetFiltering(t *testing.T) {
	t.Parallel()
	maintainer := maintainerRun()
	implementer := fleetRun("implementer", platformv1alpha1.AgentRunPhaseRunning)
	reviewer := fleetRun("reviewer", platformv1alpha1.AgentRunPhaseRunning)
	reviewer.Labels = map[string]string{triggersv1alpha1.PRLoopRoleLabelKey: triggersv1alpha1.PRLoopRoleReviewerValue}
	foreign := fleetRun("foreign", platformv1alpha1.AgentRunPhaseRunning)
	foreign.Spec.Trigger.Name = "other"
	standing := fleetRun("standing", platformv1alpha1.AgentRunPhaseRunning)
	standing.Labels = map[string]string{orchestration.StandingRunRoleLabel: "maintainer"}
	base, _, _ := newMaintainerToolBase(t, maintainer, implementer, reviewer, foreign, standing)

	fleet, err := base.fleetRuns(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(fleet) != 2 || fleet[0].Name != "implementer" || fleet[1].Name != "reviewer" {
		t.Fatalf("fleet = %#v", fleet)
	}

	for _, mutate := range []func(*platformv1alpha1.AgentRun){
		func(run *platformv1alpha1.AgentRun) { run.Labels[orchestration.StandingRunRoleLabel] = "overseer" },
		func(run *platformv1alpha1.AgentRun) { run.Labels[orchestration.SupervisedRunLabel] = "other" },
		func(run *platformv1alpha1.AgentRun) { run.OwnerReferences = nil },
	} {
		run := maintainer.DeepCopy()
		mutate(run)
		base, _, _ := newMaintainerToolBase(t, run)
		if _, err := base.currentRun(context.Background()); err == nil {
			t.Fatal("unauthorized maintainer was accepted")
		}
	}
}

func TestDescribeFleetRunExposesPRLoopStateAndRound(t *testing.T) {
	t.Parallel()
	maintainer := maintainerRun()
	implementer := fleetRun("implementer", platformv1alpha1.AgentRunPhaseRunning)
	implementer.Labels = map[string]string{
		maintainerPRLoopStateLabel:          "in_review",
		triggersv1alpha1.PRLoopRoleLabelKey: "unexpected-role-value",
	}
	implementer.Annotations = map[string]string{maintainerPRLoopRoundAnnotation: "2"}
	base, _, _ := newMaintainerToolBase(t, maintainer, implementer)
	tool := &getFleetRunsTool{maintainerToolBase: base}
	entry, err := tool.describeFleetRun(context.Background(), implementer)
	if err != nil {
		t.Fatal(err)
	}
	if entry.PRLoopState != "in_review" || entry.ReviewRound != "2" {
		t.Fatalf("PR loop state/round = %q/%q, want in_review/2", entry.PRLoopState, entry.ReviewRound)
	}
}

func TestConditionsMet(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		run     *platformv1alpha1.AgentRun
		session *store.Session
		want    []string
	}{
		{"terminal", fleetRun("terminal", platformv1alpha1.AgentRunPhaseSucceeded), nil, []string{"terminal", "idle"}},
		{"blocked", func() *platformv1alpha1.AgentRun {
			run := fleetRun("blocked", platformv1alpha1.AgentRunPhaseRunning)
			run.Status.Queue = &platformv1alpha1.AgentRunQueueStatus{BlockedReason: "capacity"}
			return run
		}(), nil, []string{"blocked", "idle"}},
		{"input", fleetRun("input", platformv1alpha1.AgentRunPhaseQuestion), &store.Session{PendingInputType: "question", PendingRequestID: "request"}, []string{"awaiting_user_input", "idle"}},
		{"pr", func() *platformv1alpha1.AgentRun {
			run := fleetRun("pr", platformv1alpha1.AgentRunPhaseRunning)
			run.Status.Artifacts = &platformv1alpha1.AgentRunArtifacts{PullRequestURLs: []string{"https://example.test/pr/1"}}
			return run
		}(), nil, []string{"pr_created"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := conditionsMet(tc.run, tc.session, map[string]bool{})
			if len(got) != len(tc.want) {
				t.Fatalf("conditionsMet() = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("conditionsMet() = %v, want %v", got, tc.want)
				}
			}
		})
	}
}

func TestLegacyDispatchRoutesThroughTypedCommandWhenWorkItemExists(t *testing.T) {
	maintainer := maintainerRun()
	base, k8sClient, _ := newMaintainerToolBase(t, maintainer)
	if err := k8sClient.Create(context.Background(), &platformv1alpha1.ModeTemplate{ObjectMeta: metav1.ObjectMeta{Name: "auto"}}); err != nil {
		t.Fatal(err)
	}
	workItem := createMaintainerWorkItem(t, k8sClient, maintainerTestRepositoryName, 2, 3)
	runner := &fakePRReviewRunner{}
	result, err := (&dispatchIssueTool{maintainerToolBase: base, runner: runner}).Execute(context.Background(), json.RawMessage(`{"issue_number":2,"mode":"auto"}`), "")
	if err != nil || result.IsError {
		t.Fatalf("Execute() = (%#v, %v)", result, err)
	}
	if len(runner.ghCalls) != 0 {
		t.Fatalf("legacy dispatch made direct GitHub calls: %#v", runner.ghCalls)
	}
	command := &triggersv1alpha1.MaintainerWorkItemCommand{}
	name := MaintainerWorkItemCommandName(maintainerTestRepositoryName, "legacy-dispatch-2-auto")
	if err := k8sClient.Get(context.Background(), client.ObjectKey{Name: name, Namespace: maintainerTestNamespace}, command); err != nil {
		t.Fatal(err)
	}
	if command.Spec.Type != triggersv1alpha1.MaintainerWorkItemCommandTypeDispatchWorkItem || command.Spec.Dispatch == nil || command.Spec.Preconditions.WorkItemName != workItem.Name {
		t.Fatalf("typed command = %#v", command.Spec)
	}
}

func TestDispatchCapsAndLedgerRollover(t *testing.T) {
	t.Parallel()
	maintainer := maintainerRun()
	maintainer.Annotations = map[string]string{triggersv1alpha1.MaintainerDispatchLedgerAnnotation: `{"day":"2000-01-01","count":10,"issues":[1]}`}
	active := fleetRun("active", platformv1alpha1.AgentRunPhaseRunning)
	base, k8sClient, _ := newMaintainerToolBase(t, maintainer, active)
	repository := &triggersv1alpha1.GitHubRepository{}
	if err := k8sClient.Get(context.Background(), client.ObjectKey{Name: maintainerTestRepositoryName, Namespace: maintainerTestNamespace}, repository); err != nil {
		t.Fatal(err)
	}
	repository.Spec.Maintainer = &triggersv1alpha1.MaintainerSpec{MaxConcurrentDispatches: 1}
	if err := k8sClient.Update(context.Background(), repository); err != nil {
		t.Fatal(err)
	}
	tool := &dispatchIssueTool{maintainerToolBase: base}
	result, err := tool.Execute(context.Background(), json.RawMessage(`{"issue_number":2,"mode":"auto"}`), "")
	if err != nil || !result.IsError || result.Content == "" {
		t.Fatalf("active cap result = (%#v, %v)", result, err)
	}

	fresh := &platformv1alpha1.AgentRun{}
	if err := k8sClient.Get(context.Background(), client.ObjectKey{Name: maintainer.Name, Namespace: maintainer.Namespace}, fresh); err != nil {
		t.Fatal(err)
	}
	ledger := parseMaintainerLedger(fresh, metav1.Now().Time)
	if ledger.Count != 0 || ledger.Day != metav1.Now().UTC().Format("2006-01-02") {
		t.Fatalf("rolled ledger = %#v", ledger)
	}
}

func TestMaintainerLedgerParsesLegacyDispatches(t *testing.T) {
	maintainer := maintainerRun()
	now := metav1.Now().Time
	maintainer.Annotations = map[string]string{triggersv1alpha1.MaintainerDispatchLedgerAnnotation: `{"day":"` + now.UTC().Format("2006-01-02") + `","count":1,"issues":[7]}`}

	ledger := parseMaintainerLedger(maintainer, now)
	if ledger.Count != 1 || len(ledger.Issues) != 1 || ledger.Issues[0] != 7 || len(ledger.Pending) != 0 {
		t.Fatalf("legacy ledger = %#v", ledger)
	}
}

func TestDispatchReservationConcurrentCapAndReplay(t *testing.T) {
	maintainer := maintainerRun()
	base, k8sClient, _ := newMaintainerToolBase(t, maintainer)
	tool := &dispatchIssueTool{maintainerToolBase: base}

	start := make(chan struct{})
	results := make(chan struct {
		issue int
		err   error
	}, 2)
	var wg sync.WaitGroup
	for _, issue := range []int{2, 3} {
		wg.Add(1)
		go func(issue int) {
			defer wg.Done()
			<-start
			results <- struct {
				issue int
				err   error
			}{issue: issue, err: tool.reserveDispatch(context.Background(), issue, "auto", nil, 1, 2)}
		}(issue)
	}
	close(start)
	wg.Wait()
	close(results)

	successes := 0
	reservedIssue := 0
	for result := range results {
		if result.err == nil {
			successes++
			reservedIssue = result.issue
		}
	}
	if successes != 1 {
		t.Fatalf("successful reservations = %d, want 1", successes)
	}
	if err := tool.reserveDispatch(context.Background(), reservedIssue, "auto", nil, 1, 2); err == nil || !strings.Contains(err.Error(), "do not replay") {
		t.Fatalf("duplicate reservation error = %v", err)
	}
	fresh := &platformv1alpha1.AgentRun{}
	if err := k8sClient.Get(context.Background(), client.ObjectKey{Name: maintainer.Name, Namespace: maintainer.Namespace}, fresh); err != nil {
		t.Fatal(err)
	}
	ledger := parseMaintainerLedger(fresh, metav1.Now().Time)
	if ledger.Count != 1 || len(ledger.Issues) != 1 || len(ledger.Pending) != 1 || ledger.Pending[0].Issue != reservedIssue {
		t.Fatalf("reserved ledger = %#v", ledger)
	}
}

func TestDispatchReservationFailureHandling(t *testing.T) {
	maintainer := maintainerRun()
	base, k8sClient, _ := newMaintainerToolBase(t, maintainer)
	if err := k8sClient.Create(context.Background(), &platformv1alpha1.ModeTemplate{ObjectMeta: metav1.ObjectMeta{Name: "auto"}}); err != nil {
		t.Fatal(err)
	}
	tool := &dispatchIssueTool{maintainerToolBase: base, runner: &fakePRReviewRunner{ghErr: map[string]error{
		"issue edit 2 --add-label auto": errors.New("HTTP 422: Validation Failed"),
	}}}
	result, err := tool.Execute(context.Background(), json.RawMessage(`{"issue_number":2,"mode":"auto"}`), testGitRepoDir(t))
	if err != nil || !result.IsError || !strings.Contains(result.Content, "without applying") {
		t.Fatalf("definite failure = (%#v, %v)", result, err)
	}
	fresh := &platformv1alpha1.AgentRun{}
	if err := k8sClient.Get(context.Background(), client.ObjectKey{Name: maintainer.Name, Namespace: maintainer.Namespace}, fresh); err != nil {
		t.Fatal(err)
	}
	ledger := parseMaintainerLedger(fresh, metav1.Now().Time)
	if ledger.Count != 0 || len(ledger.Issues) != 0 || len(ledger.Pending) != 0 {
		t.Fatalf("released ledger = %#v", ledger)
	}

	tool.runner = &fakePRReviewRunner{ghErr: map[string]error{
		"issue edit 3 --add-label auto": errors.New("connection reset"),
	}}
	result, err = tool.Execute(context.Background(), json.RawMessage(`{"issue_number":3,"mode":"auto"}`), testGitRepoDir(t))
	if err != nil || !result.IsError || !strings.Contains(result.Content, "reservation is retained") {
		t.Fatalf("ambiguous failure = (%#v, %v)", result, err)
	}
	if err := k8sClient.Get(context.Background(), client.ObjectKey{Name: maintainer.Name, Namespace: maintainer.Namespace}, fresh); err != nil {
		t.Fatal(err)
	}
	ledger = parseMaintainerLedger(fresh, metav1.Now().Time)
	if ledger.Count != 1 || len(ledger.Issues) != 1 || len(ledger.Pending) != 1 || ledger.Pending[0].Issue != 3 {
		t.Fatalf("retained ledger = %#v", ledger)
	}
}

func TestDispatchIssueNoteHasGitHubAppAuthorization(t *testing.T) {
	maintainer := maintainerRun()
	base, k8sClient, _ := newMaintainerToolBase(t, maintainer)
	if err := k8sClient.Create(context.Background(), &platformv1alpha1.ModeTemplate{ObjectMeta: metav1.ObjectMeta{Name: "auto"}}); err != nil {
		t.Fatal(err)
	}
	runner := &fakePRReviewRunner{}
	tool := &dispatchIssueTool{maintainerToolBase: base, runner: runner}

	result, err := tool.Execute(context.Background(), json.RawMessage(`{"issue_number":2,"mode":"auto","note":"Validated and dispatching."}`), testGitRepoDir(t))
	if err != nil || result.IsError {
		t.Fatalf("Execute() = (%#v, %v)", result, err)
	}
	if len(runner.ghInputs) != 1 {
		t.Fatalf("comment payloads = %#v, want one", runner.ghInputs)
	}
	var payload map[string]string
	if err := json.Unmarshal([]byte(runner.ghInputs[0]), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["body"] != "Validated and dispatching.\n\n"+githubAppAuthorizationFooter {
		t.Fatalf("comment payload = %#v", payload)
	}
}

func TestWakeAgentRunPhases(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name         string
		phase        platformv1alpha1.AgentRunPhase
		wantError    bool
		wantWake     int64
		wantMessages int
	}{
		{"running", platformv1alpha1.AgentRunPhaseRunning, false, 0, 1},
		{"paused", platformv1alpha1.AgentRunPhasePaused, false, 1, 1},
		{"cancelled", platformv1alpha1.AgentRunPhaseCancelled, true, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			maintainer, target := maintainerRun(), fleetRun("target", tc.phase)
			base, k8sClient, stateStore := newMaintainerToolBase(t, maintainer, target)
			tool := &wakeAgentRunTool{maintainerToolBase: base}
			result, err := tool.Execute(context.Background(), json.RawMessage(`{"run_name":"target","message":"continue with the existing fix"}`), "")
			if err != nil || result.IsError != tc.wantError {
				t.Fatalf("Execute() = (%#v, %v)", result, err)
			}
			updated := &platformv1alpha1.AgentRun{}
			if err := k8sClient.Get(context.Background(), client.ObjectKey{Name: target.Name, Namespace: target.Namespace}, updated); err != nil {
				t.Fatal(err)
			}
			if updated.Spec.WakeRequests != tc.wantWake || len(stateStore.messages) != tc.wantMessages {
				t.Fatalf("wake=%d messages=%d", updated.Spec.WakeRequests, len(stateStore.messages))
			}
		})
	}
}

func TestWakeAgentRunRevalidatesBeforeDelivery(t *testing.T) {
	maintainer, target := maintainerRun(), fleetRun("target", platformv1alpha1.AgentRunPhasePaused)
	base, k8sClient, stateStore := newMaintainerToolBase(t, maintainer, target)
	var mutateErr error
	stateStore.beforeSession = func() {
		fresh := &platformv1alpha1.AgentRun{}
		if mutateErr = k8sClient.Get(context.Background(), client.ObjectKey{Name: target.Name, Namespace: target.Namespace}, fresh); mutateErr != nil {
			return
		}
		fresh.Spec.Trigger.Name = "different-repository"
		mutateErr = k8sClient.Update(context.Background(), fresh)
	}

	result, err := (&wakeAgentRunTool{maintainerToolBase: base}).Execute(context.Background(), json.RawMessage(`{"run_name":"target","message":"continue"}`), "")
	if mutateErr != nil {
		t.Fatal(mutateErr)
	}
	if err != nil || !result.IsError || !strings.Contains(result.Content, "reverify") || len(stateStore.messages) != 0 {
		t.Fatalf("Execute() = (%#v, %v), messages = %d", result, err, len(stateStore.messages))
	}
}

func TestMaintainerReportValidation(t *testing.T) {
	t.Parallel()
	base, k8sClient, stateStore := newMaintainerToolBase(t, maintainerRun())
	tool := &submitMaintainerReportTool{maintainerToolBase: base}
	for _, input := range []string{`{`, `{"state":"unknown","summary":"x"}`, `{"state":"healthy","summary":""}`, `{"state":"healthy","summary":"x","decisions":"` + strings.Repeat("x", 4001) + `"}`} {
		result, err := tool.Execute(context.Background(), json.RawMessage(input), "")
		if err != nil || !result.IsError {
			t.Fatalf("Execute(%s) = (%#v, %v)", input, result, err)
		}
	}
	result, err := tool.Execute(context.Background(), json.RawMessage(`{"state":"healthy","summary":"fleet is clear","decisions":"triaged issue #1 and am waiting for run-a"}`), "")
	if err != nil || result.IsError {
		t.Fatalf("Execute() = (%#v, %v)", result, err)
	}
	run := &platformv1alpha1.AgentRun{}
	if err := k8sClient.Get(context.Background(), client.ObjectKey{Name: maintainerTestRunName, Namespace: maintainerTestNamespace}, run); err != nil {
		t.Fatal(err)
	}
	var report maintainerReport
	if err := json.Unmarshal([]byte(run.Annotations[triggersv1alpha1.MaintainerReportAnnotation]), &report); err != nil || report.State != "healthy" || report.Summary != "fleet is clear" || report.Decisions != "triaged issue #1 and am waiting for run-a" || report.Time == "" {
		t.Fatalf("report = %#v, err = %v", report, err)
	}
	if len(stateStore.activity) != 1 || stateStore.activity[0].EventType != "maintainer_report" || stateStore.activity[0].Summary != report.Summary || string(stateStore.activity[0].Detail) != run.Annotations[triggersv1alpha1.MaintainerReportAnnotation] {
		t.Fatalf("report activity = %#v", stateStore.activity)
	}
}

func TestMaintainerReportHistoryFailureWarns(t *testing.T) {
	t.Parallel()
	base, _, stateStore := newMaintainerToolBase(t, maintainerRun())
	stateStore.activityErr = errors.New("activity unavailable")
	result, err := (&submitMaintainerReportTool{maintainerToolBase: base}).Execute(context.Background(), json.RawMessage(`{"state":"healthy","summary":"fleet is clear"}`), "")
	if err != nil || result.IsError || !strings.Contains(result.Content, "warning: failed to write report history") {
		t.Fatalf("Execute() = (%#v, %v)", result, err)
	}
}
