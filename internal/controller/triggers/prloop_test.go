package triggers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/store"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const verdictHandledTrue = "true"

type prLoopStateStore struct {
	store.StateStore
	mu             sync.Mutex
	sessions       map[string]*store.Session
	messages       map[uuid.UUID][]string
	messageRecords map[uuid.UUID][]store.Message
}

func newPRLoopStateStore() *prLoopStateStore {
	return &prLoopStateStore{
		sessions:       map[string]*store.Session{},
		messages:       map[uuid.UUID][]string{},
		messageRecords: map[uuid.UUID][]store.Message{},
	}
}

func (s *prLoopStateStore) key(name, ns string) string { return ns + "/" + name }

func (s *prLoopStateStore) CreateSession(_ context.Context, name, ns, phase, step string) (*store.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sess, ok := s.sessions[s.key(name, ns)]; ok {
		return sess, nil
	}
	sess := &store.Session{ID: uuid.New(), AgentRunName: name, AgentRunNS: ns, Phase: phase, CurrentStep: step}
	s.sessions[s.key(name, ns)] = sess
	return sess, nil
}

func (s *prLoopStateStore) GetSessionByRun(_ context.Context, name, ns string) (*store.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[s.key(name, ns)]
	if !ok {
		return nil, errors.New("session not found")
	}
	return sess, nil
}

func (s *prLoopStateStore) AppendMessage(_ context.Context, sessionID uuid.UUID, role, content string, metadata json.RawMessage) (*store.Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if role != "user" {
		return nil, fmt.Errorf("unexpected role %q", role)
	}
	if eventKey := testMessageEventKey(metadata); eventKey != "" {
		for _, existing := range s.messageRecords[sessionID] {
			if testMessageEventKey(existing.Metadata) == eventKey {
				return nil, store.ErrMessageAlreadyExists
			}
		}
	}
	s.messages[sessionID] = append(s.messages[sessionID], content)
	message := store.Message{ID: int64(len(s.messageRecords[sessionID]) + 1), SessionID: sessionID, Role: role, Content: content, Metadata: append(json.RawMessage(nil), metadata...)}
	s.messageRecords[sessionID] = append(s.messageRecords[sessionID], message)
	return &message, nil
}

func testMessageEventKey(metadata json.RawMessage) string {
	var values map[string]json.RawMessage
	if json.Unmarshal(metadata, &values) != nil {
		return ""
	}
	var eventKey string
	_ = json.Unmarshal(values[githubEventMetadataKey], &eventKey)
	return eventKey
}

func (s *prLoopStateStore) GetMessages(_ context.Context, sessionID uuid.UUID) ([]store.Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]store.Message(nil), s.messageRecords[sessionID]...), nil
}

func (s *prLoopStateStore) SetPendingQuestion(_ context.Context, sessionID uuid.UUID, phase, question, inputType string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, sess := range s.sessions {
		if sess.ID == sessionID {
			sess.Phase = phase
			sess.PendingQuestion = question
			sess.PendingInputType = inputType
			return nil
		}
	}
	return errors.New("session not found")
}

func (s *prLoopStateStore) messagesFor(name, ns string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[ns+"/"+name]
	if !ok {
		return nil
	}
	return append([]string(nil), s.messages[sess.ID]...)
}

func prLoopTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(platform): %v", err)
	}
	if err := triggersv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(triggers): %v", err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(core): %v", err)
	}
	return scheme
}

func prLoopTestRepo() *triggersv1alpha1.GitHubRepository {
	return &triggersv1alpha1.GitHubRepository{
		ObjectMeta: metav1.ObjectMeta{Name: "repo", Namespace: "default"},
		Spec: triggersv1alpha1.GitHubRepositorySpec{
			GitHubTokenSecret: "gh-token",
			Owner:             "acme",
			Repo:              "widgets",
			ReviewLoop:        &triggersv1alpha1.ReviewLoopSpec{},
			Defaults: triggersv1alpha1.AgentRunDefaults{
				RepoURL:    "https://github.com/acme/widgets.git",
				BaseBranch: "main",
				Model:      "gpt-5.5",
				Secrets:    triggersv1alpha1.AgentRunSecrets{GithubToken: "gh-token", ClaudeApiKey: "claude-key"},
			},
		},
	}
}

func prLoopImplementerRun(state string) *platformv1alpha1.AgentRun {
	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "gh-acme-widgets-7",
			Namespace:   "default",
			Labels:      map[string]string{},
			Annotations: map[string]string{},
		},
		Spec: platformv1alpha1.AgentRunSpec{
			Trigger:    platformv1alpha1.TriggerRef{Kind: "GitHubRepository", Name: "repo"},
			Repository: platformv1alpha1.RepositoryContext{URL: "https://github.com/acme/widgets.git", BaseBranch: "main"},
		},
	}
	if state != "" {
		run.Labels[PRLoopStateLabel] = state
		run.Labels[PRLoopNumberLabel] = "42"
		run.Annotations[PRLoopURLAnnotation] = "https://github.com/acme/widgets/pull/42"
		run.Annotations[PRLoopBaseRefAnnotation] = "main"
		run.Annotations[PRLoopRoundAnnotation] = "1"
		run.Annotations[PRLoopRepositoryAnnotation] = "repo"
	}
	return run
}

func newPRLoopEngine(t *testing.T, objs ...client.Object) (*PRLoopEngine, client.Client, *prLoopStateStore) {
	t.Helper()
	scheme := prLoopTestScheme(t)
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&platformv1alpha1.AgentRun{}, &triggersv1alpha1.PullRequestMonitor{}).
		WithObjects(objs...).
		Build()
	ss := newPRLoopStateStore()
	return &PRLoopEngine{Client: c, Scheme: scheme, StateStore: ss}, c, ss
}

func prOpenedEvent() PullRequestEvent {
	return PullRequestEvent{
		Type:        PREventOpened,
		Number:      42,
		Title:       "Add widget pagination",
		URL:         "https://github.com/acme/widgets/pull/42",
		HeadRef:     "gh-acme-widgets-7",
		BaseRef:     "main",
		AuthorLogin: "agent-bot",
		SenderLogin: "agent-bot",
	}
}

func TestPRLoopOpenedStartsReviewRound(t *testing.T) {
	t.Parallel()
	gh := prLoopTestRepo()
	impl := prLoopImplementerRun("")
	engine, c, ss := newPRLoopEngine(t, gh, impl)

	handled, err := engine.HandlePullRequestEvent(context.Background(), gh, prOpenedEvent())
	if err != nil {
		t.Fatalf("HandlePullRequestEvent() error = %v", err)
	}
	if !handled {
		t.Fatal("handled = false, want true")
	}

	updated := &platformv1alpha1.AgentRun{}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(impl), updated); err != nil {
		t.Fatalf("Get(implementer): %v", err)
	}
	if got := updated.Labels[PRLoopStateLabel]; got != PRLoopStateInReview {
		t.Fatalf("implementer loop state = %q, want in_review", got)
	}
	if got := updated.Labels[PRLoopNumberLabel]; got != "42" {
		t.Fatalf("pr-number label = %q, want 42", got)
	}
	if got := updated.Annotations[PRLoopRoundAnnotation]; got != "1" {
		t.Fatalf("round annotation = %q, want 1", got)
	}

	reviewerName := reviewerRunName(impl.Name, prLoopKey("acme/widgets", 42), 1)
	reviewer := &platformv1alpha1.AgentRun{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: reviewerName}, reviewer); err != nil {
		t.Fatalf("Get(reviewer %s): %v", reviewerName, err)
	}
	if reviewer.Labels[PRLoopRoleLabel] != PRLoopRoleReviewer {
		t.Fatalf("reviewer role label = %q, want reviewer", reviewer.Labels[PRLoopRoleLabel])
	}
	if reviewer.Spec.WorkflowMode != platformv1alpha1.WorkflowModeAuto {
		t.Fatalf("reviewer workflow mode = %q, want auto", reviewer.Spec.WorkflowMode)
	}
	if reviewer.Annotations[PRLoopImplementerAnnotation] != impl.Name {
		t.Fatalf("reviewer implementer annotation = %q, want %s", reviewer.Annotations[PRLoopImplementerAnnotation], impl.Name)
	}
	if len(reviewer.OwnerReferences) != 1 || reviewer.OwnerReferences[0].Kind != "GitHubRepository" || reviewer.OwnerReferences[0].Name != gh.Name {
		t.Fatalf("reviewer ownerReferences = %#v, want GitHubRepository %s", reviewer.OwnerReferences, gh.Name)
	}
	msgs := ss.messagesFor(reviewerName, "default")
	if len(msgs) != 1 || !strings.Contains(msgs[0], "pull request #42") {
		t.Fatalf("reviewer seeded messages = %#v, want one PR review task", msgs)
	}
	if !strings.Contains(msgs[0], "submit_review_verdict") {
		t.Fatalf("reviewer task does not mention submit_review_verdict: %q", msgs[0])
	}
}

func TestPRLoopOpenedIgnoresHumanPRs(t *testing.T) {
	t.Parallel()
	gh := prLoopTestRepo()
	engine, c, _ := newPRLoopEngine(t, gh)

	event := prOpenedEvent()
	event.HeadRef = "feature/human-branch" // no AgentRun with this name
	handled, err := engine.HandlePullRequestEvent(context.Background(), gh, event)
	if err != nil {
		t.Fatalf("HandlePullRequestEvent() error = %v", err)
	}
	if handled {
		t.Fatal("handled = true, want false for human PR")
	}
	runs := &platformv1alpha1.AgentRunList{}
	if err := c.List(context.Background(), runs); err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(runs.Items) != 0 {
		t.Fatalf("AgentRuns created = %d, want 0", len(runs.Items))
	}
}

func TestPRLoopOpenedDefaultsToDisabled(t *testing.T) {
	t.Parallel()
	gh := prLoopTestRepo()
	gh.Spec.ReviewLoop = nil
	impl := prLoopImplementerRun("")
	engine, c, _ := newPRLoopEngine(t, gh, impl)

	handled, err := engine.HandlePullRequestEvent(context.Background(), gh, prOpenedEvent())
	if err != nil || handled {
		t.Fatalf("HandlePullRequestEvent() = (%v, %v), want (false, nil)", handled, err)
	}
	updated := &platformv1alpha1.AgentRun{}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(impl), updated); err != nil {
		t.Fatalf("Get(implementer): %v", err)
	}
	if updated.Labels[PRLoopStateLabel] != "" {
		t.Fatalf("loop state = %q, want empty by default", updated.Labels[PRLoopStateLabel])
	}
}

func TestPRLoopOpenedRespectsDisabled(t *testing.T) {
	t.Parallel()
	gh := prLoopTestRepo()
	gh.Spec.ReviewLoop = &triggersv1alpha1.ReviewLoopSpec{Disabled: true}
	impl := prLoopImplementerRun("")
	engine, c, _ := newPRLoopEngine(t, gh, impl)

	handled, err := engine.HandlePullRequestEvent(context.Background(), gh, prOpenedEvent())
	if err != nil || handled {
		t.Fatalf("HandlePullRequestEvent() = (%v, %v), want (false, nil)", handled, err)
	}
	updated := &platformv1alpha1.AgentRun{}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(impl), updated); err != nil {
		t.Fatalf("Get(implementer): %v", err)
	}
	if updated.Labels[PRLoopStateLabel] != "" {
		t.Fatalf("loop state = %q, want empty when disabled", updated.Labels[PRLoopStateLabel])
	}
}

func TestPRLoopOpenedIsIdempotent(t *testing.T) {
	t.Parallel()
	gh := prLoopTestRepo()
	impl := prLoopImplementerRun("in_review")
	engine, c, _ := newPRLoopEngine(t, gh, impl)

	handled, err := engine.HandlePullRequestEvent(context.Background(), gh, prOpenedEvent())
	if err != nil || !handled {
		t.Fatalf("HandlePullRequestEvent() = (%v, %v), want (true, nil)", handled, err)
	}
	runs := &platformv1alpha1.AgentRunList{}
	if err := c.List(context.Background(), runs); err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(runs.Items) != 1 { // only the implementer; no reviewer spawned again
		t.Fatalf("AgentRuns = %d, want 1 (no duplicate reviewer)", len(runs.Items))
	}
}

func TestPRLoopHumanApprovalEndsLoop(t *testing.T) {
	t.Parallel()
	gh := prLoopTestRepo()
	impl := prLoopImplementerRun("in_review")
	engine, c, _ := newPRLoopEngine(t, gh, impl)

	handled, err := engine.HandlePullRequestEvent(context.Background(), gh, PullRequestEvent{
		Type:                    PREventReviewSubmitted,
		Number:                  42,
		URL:                     "https://github.com/acme/widgets/pull/42",
		AuthorLogin:             "agent-bot",
		SenderLogin:             "human-reviewer",
		SenderAuthorAssociation: "MEMBER",
		ReviewState:             "approved",
	})
	if err != nil || !handled {
		t.Fatalf("HandlePullRequestEvent() = (%v, %v), want (true, nil)", handled, err)
	}
	updated := &platformv1alpha1.AgentRun{}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(impl), updated); err != nil {
		t.Fatalf("Get(implementer): %v", err)
	}
	if got := updated.Labels[PRLoopStateLabel]; got != PRLoopStateApproved {
		t.Fatalf("loop state = %q, want approved", got)
	}
}

func TestPRLoopIgnoresSelfReviews(t *testing.T) {
	t.Parallel()
	gh := prLoopTestRepo()
	impl := prLoopImplementerRun("in_review")
	engine, _, _ := newPRLoopEngine(t, gh, impl)

	handled, err := engine.HandlePullRequestEvent(context.Background(), gh, PullRequestEvent{
		Type:        PREventReviewSubmitted,
		Number:      42,
		AuthorLogin: "agent-bot",
		SenderLogin: "agent-bot", // same identity: reviewer-run COMMENT review
		ReviewState: "commented",
		Body:        "loop bait",
	})
	if err != nil || handled {
		t.Fatalf("HandlePullRequestEvent() = (%v, %v), want (false, nil) for self review", handled, err)
	}
}

func TestPRLoopHumanChangesRequestedWakesSucceededImplementer(t *testing.T) {
	t.Parallel()
	gh := prLoopTestRepo()
	impl := prLoopImplementerRun("in_review")
	engine, c, ss := newPRLoopEngine(t, gh, impl)
	if _, err := ss.CreateSession(context.Background(), impl.Name, impl.Namespace, "succeeded", "done"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	markSucceeded(t, c, impl)

	handled, err := engine.HandlePullRequestEvent(context.Background(), gh, PullRequestEvent{
		Type:                    PREventReviewSubmitted,
		Number:                  42,
		URL:                     "https://github.com/acme/widgets/pull/42",
		AuthorLogin:             "agent-bot",
		SenderLogin:             "human-reviewer",
		SenderAuthorAssociation: "MEMBER",
		ReviewState:             "changes_requested",
		Body:                    "The pagination is off by one.",
	})
	if err != nil || !handled {
		t.Fatalf("HandlePullRequestEvent() = (%v, %v), want (true, nil)", handled, err)
	}

	updated := &platformv1alpha1.AgentRun{}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(impl), updated); err != nil {
		t.Fatalf("Get(implementer): %v", err)
	}
	if got := updated.Labels[PRLoopStateLabel]; got != PRLoopStateResolving {
		t.Fatalf("loop state = %q, want resolving", got)
	}
	if updated.Spec.WakeRequests != 1 {
		t.Fatalf("WakeRequests = %d, want 1", updated.Spec.WakeRequests)
	}
	msgs := ss.messagesFor(impl.Name, impl.Namespace)
	if len(msgs) != 1 || !strings.Contains(msgs[0], "off by one") {
		t.Fatalf("implementer messages = %#v, want review feedback", msgs)
	}
}

func TestPRLoopConcurrentDuplicateFeedbackAppendsOneMessage(t *testing.T) {
	gh := prLoopTestRepo()
	impl := prLoopImplementerRun(PRLoopStateInReview)
	engine, c, stateStore := newPRLoopEngine(t, gh, impl)
	if _, err := stateStore.CreateSession(context.Background(), impl.Name, impl.Namespace, "succeeded", "done"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	markSucceeded(t, c, impl)

	event := PullRequestEvent{
		Type:                    PREventReviewSubmitted,
		Repository:              "acme/widgets",
		Number:                  42,
		URL:                     "https://github.com/acme/widgets/pull/42",
		AuthorLogin:             "agent-bot",
		SenderLogin:             "human-reviewer",
		SenderAuthorAssociation: "MEMBER",
		ReviewState:             "changes_requested",
		Body:                    "Please fix the race.",
		SourceID:                8101,
		SourceCreatedAt:         time.Date(2026, 7, 12, 6, 0, 0, 0, time.UTC),
	}

	type result struct {
		handled bool
		err     error
	}
	start := make(chan struct{})
	results := make(chan result, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			handled, err := engine.HandlePullRequestEvent(context.Background(), gh, event)
			results <- result{handled: handled, err: err}
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	for result := range results {
		if result.err != nil || !result.handled {
			t.Fatalf("concurrent delivery = (%v, %v), want (true, nil)", result.handled, result.err)
		}
	}

	updated := &platformv1alpha1.AgentRun{}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(impl), updated); err != nil {
		t.Fatalf("Get(implementer): %v", err)
	}
	messages := stateStore.messagesFor(impl.Name, impl.Namespace)
	if len(messages) != 1 {
		t.Fatalf("feedback messages = %d, want exactly 1", len(messages))
	}
	if updated.Spec.WakeRequests != 1 {
		t.Fatalf("WakeRequests = %d, want 1", updated.Spec.WakeRequests)
	}
}

func TestPRLoopCommentWithKeywordWakesImplementer(t *testing.T) {
	t.Parallel()
	gh := prLoopTestRepo()
	impl := prLoopImplementerRun("in_review")
	engine, c, ss := newPRLoopEngine(t, gh, impl)
	if _, err := ss.CreateSession(context.Background(), impl.Name, impl.Namespace, "succeeded", "done"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	markSucceeded(t, c, impl)

	handled, err := engine.HandlePullRequestEvent(context.Background(), gh, PullRequestEvent{
		Type:                    PREventComment,
		Number:                  42,
		URL:                     "https://github.com/acme/widgets/pull/42",
		AuthorLogin:             "agent-bot",
		SenderLogin:             "human",
		SenderAuthorAssociation: "MEMBER",
		Body:                    "@agent please also update the changelog",
	})
	if err != nil || !handled {
		t.Fatalf("HandlePullRequestEvent() = (%v, %v), want (true, nil)", handled, err)
	}
	msgs := ss.messagesFor(impl.Name, impl.Namespace)
	if len(msgs) != 1 || !strings.Contains(msgs[0], "update the changelog") {
		t.Fatalf("implementer messages = %#v, want comment body", msgs)
	}
}

func TestPRLoopCommentGitHubActorAuth(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		auth              *triggersv1alpha1.TriggerAuth
		login             string
		authorAssociation string
		wantHandled       bool
		wantMessage       bool
		wantEvent         bool
	}{
		{
			name:              "org member allowed with empty list",
			login:             "member",
			authorAssociation: "MEMBER",
			wantHandled:       true,
			wantMessage:       true,
		},
		{
			name:              "random user rejected with empty list",
			login:             "random",
			authorAssociation: "NONE",
			wantEvent:         true,
		},
		{
			name:              "explicit allowlist allows none association",
			auth:              &triggersv1alpha1.TriggerAuth{AllowedUsers: []string{"random"}},
			login:             "random",
			authorAssociation: "NONE",
			wantHandled:       true,
			wantMessage:       true,
		},
		{
			name:              "deny beats trusted association",
			auth:              &triggersv1alpha1.TriggerAuth{DenyUsers: []string{"member"}},
			login:             "member",
			authorAssociation: "MEMBER",
			wantEvent:         true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			gh := prLoopTestRepo()
			gh.Spec.Auth = tt.auth
			impl := prLoopImplementerRun("in_review")
			engine, c, ss := newPRLoopEngine(t, gh, impl)
			recorder := record.NewFakeRecorder(4)
			engine.Recorder = recorder
			if _, err := ss.CreateSession(context.Background(), impl.Name, impl.Namespace, "succeeded", "done"); err != nil {
				t.Fatalf("CreateSession: %v", err)
			}
			markSucceeded(t, c, impl)

			handled, err := engine.HandlePullRequestEvent(context.Background(), gh, PullRequestEvent{
				Type:                    PREventComment,
				Number:                  42,
				URL:                     "https://github.com/acme/widgets/pull/42",
				AuthorLogin:             "agent-bot",
				SenderLogin:             tt.login,
				SenderAuthorAssociation: tt.authorAssociation,
				Body:                    "@agent please check this",
			})
			if err != nil {
				t.Fatalf("HandlePullRequestEvent() error = %v", err)
			}
			if handled != tt.wantHandled {
				t.Fatalf("handled = %v, want %v", handled, tt.wantHandled)
			}
			if got := len(ss.messagesFor(impl.Name, impl.Namespace)); (got > 0) != tt.wantMessage {
				t.Fatalf("messages = %d, want message presence %v", got, tt.wantMessage)
			}
			select {
			case event := <-recorder.Events:
				if !tt.wantEvent {
					t.Fatalf("unexpected event: %s", event)
				}
				if !strings.Contains(event, "TriggerActorRejected") || !strings.Contains(event, tt.login) {
					t.Fatalf("event = %q, want TriggerActorRejected for %s", event, tt.login)
				}
			default:
				if tt.wantEvent {
					t.Fatal("missing TriggerActorRejected event")
				}
			}
		})
	}
}

func TestPRLoopCommentWithoutKeywordFallsThrough(t *testing.T) {
	t.Parallel()
	gh := prLoopTestRepo()
	impl := prLoopImplementerRun("in_review")
	engine, _, _ := newPRLoopEngine(t, gh, impl)

	handled, err := engine.HandlePullRequestEvent(context.Background(), gh, PullRequestEvent{
		Type:        PREventComment,
		Number:      42,
		SenderLogin: "human",
		Body:        "nice work!",
	})
	if err != nil || handled {
		t.Fatalf("HandlePullRequestEvent() = (%v, %v), want (false, nil)", handled, err)
	}
}

func markSucceeded(t *testing.T, c client.Client, run *platformv1alpha1.AgentRun) {
	t.Helper()
	markPhase(t, c, run, platformv1alpha1.AgentRunPhaseSucceeded)
}

func markPhase(t *testing.T, c client.Client, run *platformv1alpha1.AgentRun, phase platformv1alpha1.AgentRunPhase) {
	t.Helper()
	fresh := &platformv1alpha1.AgentRun{}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(run), fresh); err != nil {
		t.Fatalf("Get(%s): %v", run.Name, err)
	}
	fresh.Status.Phase = phase
	if err := c.Status().Update(context.Background(), fresh); err != nil {
		t.Fatalf("Status().Update(%s): %v", run.Name, err)
	}
}

func TestPRLoopWakeImplementerPinsCurrentPhaseBehavior(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                string
		phase               platformv1alpha1.AgentRunPhase
		wantWakeRequests    int64
		wantMessage         bool
		wantBlockedQuestion bool
	}{
		{
			name:             "succeeded implementer bumps wake requests",
			phase:            platformv1alpha1.AgentRunPhaseSucceeded,
			wantWakeRequests: 1,
			wantMessage:      true,
		},
		{
			name:             "failed implementer bumps wake requests",
			phase:            platformv1alpha1.AgentRunPhaseFailed,
			wantWakeRequests: 1,
			wantMessage:      true,
		},
		{
			name:             "paused implementer bumps wake requests",
			phase:            platformv1alpha1.AgentRunPhasePaused,
			wantWakeRequests: 1,
			wantMessage:      true,
		},
		{
			name:             "running implementer receives session message only",
			phase:            platformv1alpha1.AgentRunPhaseRunning,
			wantWakeRequests: 0,
			wantMessage:      true,
		},
		{
			name:                "cancelled implementer escalates blocked question",
			phase:               platformv1alpha1.AgentRunPhaseCancelled,
			wantWakeRequests:    0,
			wantBlockedQuestion: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			impl := prLoopImplementerRun(PRLoopStateInReview)
			impl.Name = "impl-" + strings.ReplaceAll(string(tc.phase), " ", "-")
			engine, c, ss := newPRLoopEngine(t, prLoopTestRepo(), impl)
			sess, err := ss.CreateSession(context.Background(), impl.Name, impl.Namespace, "ready", "done")
			if err != nil {
				t.Fatalf("CreateSession: %v", err)
			}
			markPhase(t, c, impl, tc.phase)

			fresh := &platformv1alpha1.AgentRun{}
			if err := c.Get(context.Background(), client.ObjectKeyFromObject(impl), fresh); err != nil {
				t.Fatalf("Get(implementer): %v", err)
			}
			if err := engine.wakeImplementer(context.Background(), fresh, "", PRLoopStateResolving, "please address reviewer feedback"); err != nil {
				t.Fatalf("wakeImplementer() error = %v", err)
			}

			updated := &platformv1alpha1.AgentRun{}
			if err := c.Get(context.Background(), client.ObjectKeyFromObject(impl), updated); err != nil {
				t.Fatalf("Get(updated implementer): %v", err)
			}
			if got := updated.Labels[PRLoopStateLabel]; got != PRLoopStateResolving {
				t.Fatalf("loop state = %q, want resolving", got)
			}
			if updated.Spec.WakeRequests != tc.wantWakeRequests {
				t.Fatalf("WakeRequests = %d, want %d", updated.Spec.WakeRequests, tc.wantWakeRequests)
			}
			msgs := ss.messagesFor(impl.Name, impl.Namespace)
			if tc.wantMessage {
				if len(msgs) != 1 || !strings.Contains(msgs[0], "reviewer feedback") {
					t.Fatalf("implementer messages = %#v, want reviewer feedback", msgs)
				}
			} else if len(msgs) != 0 {
				t.Fatalf("implementer messages = %#v, want none", msgs)
			}
			if tc.wantBlockedQuestion {
				if !strings.Contains(sess.PendingQuestion, "cancelled and cannot be woken") {
					t.Fatalf("PendingQuestion = %q, want cancelled escalation", sess.PendingQuestion)
				}
			} else if sess.PendingQuestion != "" {
				t.Fatalf("PendingQuestion = %q, want empty", sess.PendingQuestion)
			}
		})
	}
}

func TestPRLoopReviewerApproveVerdict(t *testing.T) {
	t.Parallel()
	gh := prLoopTestRepo()
	impl := prLoopImplementerRun("in_review")
	reviewer := &platformv1alpha1.AgentRun{ObjectMeta: metav1.ObjectMeta{
		Name:      reviewerRunName(impl.Name, prLoopKey("acme/widgets", 42), 1),
		Namespace: "default",
		Labels:    map[string]string{PRLoopRoleLabel: PRLoopRoleReviewer, PRLoopNumberLabel: "42"},
		Annotations: map[string]string{
			PRLoopImplementerAnnotation:              impl.Name,
			PRLoopRoundAnnotation:                    "1",
			platformv1alpha1.ReviewVerdictAnnotation: platformv1alpha1.ReviewVerdictApprove,
			platformv1alpha1.ReviewSummaryAnnotation: "Clean change, tests included.",
		},
	}}
	engine, c, _ := newPRLoopEngine(t, gh, impl, reviewer)
	markSucceeded(t, c, reviewer)

	fresh := &platformv1alpha1.AgentRun{}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(reviewer), fresh); err != nil {
		t.Fatalf("Get(reviewer): %v", err)
	}
	if err := engine.OnReviewerRunCompleted(context.Background(), fresh); err != nil {
		t.Fatalf("OnReviewerRunCompleted() error = %v", err)
	}

	updatedImpl := &platformv1alpha1.AgentRun{}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(impl), updatedImpl); err != nil {
		t.Fatalf("Get(implementer): %v", err)
	}
	if got := updatedImpl.Labels[PRLoopStateLabel]; got != PRLoopStateApproved {
		t.Fatalf("loop state = %q, want approved", got)
	}
	updatedRev := &platformv1alpha1.AgentRun{}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(reviewer), updatedRev); err != nil {
		t.Fatalf("Get(reviewer): %v", err)
	}
	if updatedRev.Annotations[PRLoopVerdictHandledAnnotation] != verdictHandledTrue {
		t.Fatal("verdict-handled annotation missing")
	}
}

func TestPRLoopReviewerRequestChangesWakesImplementer(t *testing.T) {
	t.Parallel()
	gh := prLoopTestRepo()
	impl := prLoopImplementerRun("in_review")
	reviewer := &platformv1alpha1.AgentRun{ObjectMeta: metav1.ObjectMeta{
		Name:      reviewerRunName(impl.Name, prLoopKey("acme/widgets", 42), 1),
		Namespace: "default",
		Labels:    map[string]string{PRLoopRoleLabel: PRLoopRoleReviewer, PRLoopNumberLabel: "42"},
		Annotations: map[string]string{
			PRLoopImplementerAnnotation:              impl.Name,
			PRLoopRoundAnnotation:                    "1",
			platformv1alpha1.ReviewVerdictAnnotation: platformv1alpha1.ReviewVerdictRequestChanges,
			platformv1alpha1.ReviewSummaryAnnotation: "HIGH: missing input validation on the new endpoint.",
		},
	}}
	engine, c, ss := newPRLoopEngine(t, gh, impl, reviewer)
	if _, err := ss.CreateSession(context.Background(), impl.Name, impl.Namespace, "succeeded", "done"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	markSucceeded(t, c, impl)
	markSucceeded(t, c, reviewer)

	fresh := &platformv1alpha1.AgentRun{}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(reviewer), fresh); err != nil {
		t.Fatalf("Get(reviewer): %v", err)
	}
	if err := engine.OnReviewerRunCompleted(context.Background(), fresh); err != nil {
		t.Fatalf("OnReviewerRunCompleted() error = %v", err)
	}

	updatedImpl := &platformv1alpha1.AgentRun{}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(impl), updatedImpl); err != nil {
		t.Fatalf("Get(implementer): %v", err)
	}
	if got := updatedImpl.Labels[PRLoopStateLabel]; got != PRLoopStateResolving {
		t.Fatalf("loop state = %q, want resolving", got)
	}
	if updatedImpl.Spec.WakeRequests != 1 {
		t.Fatalf("WakeRequests = %d, want 1", updatedImpl.Spec.WakeRequests)
	}
	msgs := ss.messagesFor(impl.Name, impl.Namespace)
	if len(msgs) != 1 || !strings.Contains(msgs[0], "missing input validation") {
		t.Fatalf("implementer messages = %#v, want reviewer summary", msgs)
	}
}

func TestPRLoopRoundCapBlocks(t *testing.T) {
	t.Parallel()
	gh := prLoopTestRepo()
	impl := prLoopImplementerRun("in_review")
	impl.Annotations[PRLoopRoundAnnotation] = "3" // at default cap
	reviewer := &platformv1alpha1.AgentRun{ObjectMeta: metav1.ObjectMeta{
		Name:      reviewerRunName(impl.Name, prLoopKey("acme/widgets", 42), 3),
		Namespace: "default",
		Labels:    map[string]string{PRLoopRoleLabel: PRLoopRoleReviewer, PRLoopNumberLabel: "42"},
		Annotations: map[string]string{
			PRLoopImplementerAnnotation:              impl.Name,
			PRLoopRoundAnnotation:                    "3",
			platformv1alpha1.ReviewVerdictAnnotation: platformv1alpha1.ReviewVerdictRequestChanges,
			platformv1alpha1.ReviewSummaryAnnotation: "Still broken.",
		},
	}}
	engine, c, ss := newPRLoopEngine(t, gh, impl, reviewer)
	sess, err := ss.CreateSession(context.Background(), impl.Name, impl.Namespace, "succeeded", "done")
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	markSucceeded(t, c, reviewer)

	fresh := &platformv1alpha1.AgentRun{}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(reviewer), fresh); err != nil {
		t.Fatalf("Get(reviewer): %v", err)
	}
	if err := engine.OnReviewerRunCompleted(context.Background(), fresh); err != nil {
		t.Fatalf("OnReviewerRunCompleted() error = %v", err)
	}

	updatedImpl := &platformv1alpha1.AgentRun{}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(impl), updatedImpl); err != nil {
		t.Fatalf("Get(implementer): %v", err)
	}
	if got := updatedImpl.Labels[PRLoopStateLabel]; got != PRLoopStateBlocked {
		t.Fatalf("loop state = %q, want blocked at round cap", got)
	}
	if updatedImpl.Spec.WakeRequests != 0 {
		t.Fatalf("WakeRequests = %d, want 0 (no wake past cap)", updatedImpl.Spec.WakeRequests)
	}
	if sess.Phase != "blocked" || !strings.Contains(sess.PendingQuestion, "blocked after 3 review rounds") {
		t.Fatalf("blocked escalation = phase %q question %q, want blocked pending question", sess.Phase, sess.PendingQuestion)
	}
	runs := &platformv1alpha1.AgentRunList{}
	if err := c.List(context.Background(), runs); err != nil {
		t.Fatalf("List(AgentRuns): %v", err)
	}
	for _, run := range runs.Items {
		if run.Name == reviewerRunName(impl.Name, prLoopKey("acme/widgets", 42), 4) {
			t.Fatalf("unexpected reviewer run %q created past round cap", run.Name)
		}
	}
}

func TestPRLoopImplementerCompletionStartsNextRound(t *testing.T) {
	t.Parallel()
	gh := prLoopTestRepo()
	impl := prLoopImplementerRun(PRLoopStateResolving)
	engine, c, ss := newPRLoopEngine(t, gh, impl)
	markSucceeded(t, c, impl)

	fresh := &platformv1alpha1.AgentRun{}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(impl), fresh); err != nil {
		t.Fatalf("Get(implementer): %v", err)
	}
	if err := engine.OnImplementerRunCompleted(context.Background(), fresh); err != nil {
		t.Fatalf("OnImplementerRunCompleted() error = %v", err)
	}

	updatedImpl := &platformv1alpha1.AgentRun{}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(impl), updatedImpl); err != nil {
		t.Fatalf("Get(implementer): %v", err)
	}
	if got := updatedImpl.Labels[PRLoopStateLabel]; got != PRLoopStateInReview {
		t.Fatalf("loop state = %q, want in_review", got)
	}
	if got := updatedImpl.Annotations[PRLoopRoundAnnotation]; got != "2" {
		t.Fatalf("round = %q, want 2", got)
	}

	reviewerName := reviewerRunName(impl.Name, prLoopKey("acme/widgets", 42), 2)
	reviewer := &platformv1alpha1.AgentRun{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: reviewerName}, reviewer); err != nil {
		t.Fatalf("Get(reviewer round 2): %v", err)
	}
	msgs := ss.messagesFor(reviewerName, "default")
	if len(msgs) != 1 || !strings.Contains(msgs[0], "round 2") {
		t.Fatalf("reviewer messages = %#v, want round 2 task", msgs)
	}
}

func TestPRLoopReconcileRoutesReviewerAndImplementer(t *testing.T) {
	t.Parallel()
	gh := prLoopTestRepo()
	impl := prLoopImplementerRun(PRLoopStateResolving)
	engine, c, _ := newPRLoopEngine(t, gh, impl)
	markSucceeded(t, c, impl)

	r := &PRLoopReconciler{Client: c, Engine: engine}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(impl)}); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	updated := &platformv1alpha1.AgentRun{}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(impl), updated); err != nil {
		t.Fatalf("Get(implementer): %v", err)
	}
	if got := updated.Labels[PRLoopStateLabel]; got != PRLoopStateInReview {
		t.Fatalf("loop state after reconcile = %q, want in_review", got)
	}
}

func TestAnnotationInt(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in       string
		fallback int
		want     int
	}{
		{"", 7, 7},
		{"3", 1, 3},
		{"42", 1, 42},
		{"x", 5, 5},
		{"-1", 5, 5},
	}
	for _, tc := range cases {
		if got := annotationInt(tc.in, tc.fallback); got != tc.want {
			t.Fatalf("annotationInt(%q, %d) = %d, want %d", tc.in, tc.fallback, got, tc.want)
		}
	}
}

// TestPRLoopReviewerInheritsTriggerDefaults ensures reviewer runs honor the
// trigger settings that apply per run: reasoning level, additional repos, the
// Kubernetes-admin grant, and the run timeout.
func TestPRLoopDashboardRunInOnboardedRepoStartsReview(t *testing.T) {
	t.Parallel()
	gh := prLoopTestRepo()
	impl := prLoopImplementerRun("")
	impl.Spec.Trigger = platformv1alpha1.TriggerRef{Kind: "Dashboard", Name: "chat"}
	impl.Spec.WorkflowMode = platformv1alpha1.WorkflowModeChat
	engine, c, _ := newPRLoopEngine(t, gh, impl)

	handled, err := engine.HandlePullRequestEvent(context.Background(), gh, prOpenedEvent())
	if err != nil || !handled {
		t.Fatalf("HandlePullRequestEvent() = (%v, %v), want (true, nil)", handled, err)
	}
	reviewer := &platformv1alpha1.AgentRun{}
	name := reviewerRunName(impl.Name, prLoopKey("acme/widgets", 42), 1)
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: impl.Namespace, Name: name}, reviewer); err != nil {
		t.Fatalf("Get(reviewer): %v", err)
	}
	if reviewer.Spec.WorkflowMode != platformv1alpha1.WorkflowModeAuto {
		t.Fatalf("reviewer workflow mode = %q, want auto", reviewer.Spec.WorkflowMode)
	}
}

func TestPRLoopUnconfiguredRepoUsesImplementerSpec(t *testing.T) {
	t.Parallel()
	impl := prLoopImplementerRun("")
	impl.Annotations[PRLoopOptAnnotation] = PRLoopOptEnabled
	impl.Namespace = "cron-team"
	impl.Spec.Trigger = platformv1alpha1.TriggerRef{Kind: "Cron", Name: "nightly"}
	impl.Spec.Model = "anthropic/claude-sonnet-4-6"
	impl.Spec.AuthMode = platformv1alpha1.AgentRunAuthModeOAuth
	impl.Spec.Image = "example/reviewer-worker:v2"
	impl.Spec.ReasoningLevel = platformv1alpha1.ReasoningHigh
	impl.Spec.Secrets = &platformv1alpha1.AgentRunSecrets{
		OpenAIOAuthSecret: "provider-oauth",
		GitHubTokenSecret: "github-token",
	}
	impl.Spec.RuntimeProfileRef = &platformv1alpha1.NamedRef{Name: "team-runtime"}
	impl.Annotations[openAIAPIModeAnnotation] = "responses"
	impl.OwnerReferences = []metav1.OwnerReference{{
		APIVersion: triggersv1alpha1.GroupVersion.String(), Kind: "Cron", Name: "nightly", UID: "cron-uid",
	}}
	engine, c, _ := newPRLoopEngine(t, impl)

	event := prOpenedEvent()
	event.Repository = "acme/widgets"
	handled, err := engine.HandlePullRequestEvent(context.Background(), nil, event)
	if err != nil || !handled {
		t.Fatalf("HandlePullRequestEvent() = (%v, %v), want (true, nil)", handled, err)
	}
	reviewer := &platformv1alpha1.AgentRun{}
	name := reviewerRunName(impl.Name, prLoopKey("acme/widgets", 42), 1)
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: impl.Namespace, Name: name}, reviewer); err != nil {
		t.Fatalf("Get(reviewer): %v", err)
	}
	if reviewer.Spec.Model != impl.Spec.Model || reviewer.Spec.AuthMode != impl.Spec.AuthMode || reviewer.Spec.Image != impl.Spec.Image {
		t.Fatalf("reviewer execution config = model %q auth %q image %q, want implementer values", reviewer.Spec.Model, reviewer.Spec.AuthMode, reviewer.Spec.Image)
	}
	if reviewer.Spec.Secrets == nil || reviewer.Spec.Secrets.GitHubTokenSecret != "github-token" || reviewer.Spec.RuntimeProfileRef == nil || reviewer.Spec.RuntimeProfileRef.Name != "team-runtime" {
		t.Fatalf("reviewer inherited config = secrets %+v runtime %+v", reviewer.Spec.Secrets, reviewer.Spec.RuntimeProfileRef)
	}
	if reviewer.Spec.Trigger.Kind != "PRReviewLoop" || reviewer.Spec.Trigger.Name != impl.Name {
		t.Fatalf("reviewer trigger = %+v, want PRReviewLoop/%s", reviewer.Spec.Trigger, impl.Name)
	}
	if len(reviewer.OwnerReferences) != 1 || reviewer.OwnerReferences[0].Kind != "Cron" || reviewer.OwnerReferences[0].Name != "nightly" {
		t.Fatalf("reviewer ownerReferences = %#v, want implementer's Cron owner", reviewer.OwnerReferences)
	}
}

func TestPRLoopUnconfiguredRepoHumanReviewWakesCronImplementer(t *testing.T) {
	t.Parallel()
	impl := prLoopImplementerRun("")
	impl.Annotations[PRLoopOptAnnotation] = PRLoopOptEnabled
	impl.Spec.Trigger = platformv1alpha1.TriggerRef{Kind: "Cron", Name: "nightly"}
	impl.Spec.Model = "openai/gpt-5.5"
	impl.Spec.Secrets = &platformv1alpha1.AgentRunSecrets{GitHubTokenSecret: "github-token"}
	engine, c, ss := newPRLoopEngine(t, impl)

	opened := prOpenedEvent()
	opened.Repository = "acme/widgets"
	if handled, err := engine.HandlePullRequestEvent(context.Background(), nil, opened); err != nil || !handled {
		t.Fatalf("opened event = (%v, %v), want (true, nil)", handled, err)
	}
	if _, err := ss.CreateSession(context.Background(), impl.Name, impl.Namespace, "succeeded", "done"); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	markSucceeded(t, c, impl)

	review := PullRequestEvent{
		Type:                    PREventReviewSubmitted,
		Repository:              "acme/widgets",
		Number:                  42,
		URL:                     opened.URL,
		AuthorLogin:             "agent-bot",
		SenderLogin:             "human-reviewer",
		SenderAuthorAssociation: "MEMBER",
		ReviewState:             "changes_requested",
		Body:                    "Please add a regression test.",
	}
	handled, err := engine.HandlePullRequestEvent(context.Background(), nil, review)
	if err != nil || !handled {
		t.Fatalf("review event = (%v, %v), want (true, nil)", handled, err)
	}
	updated := &platformv1alpha1.AgentRun{}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(impl), updated); err != nil {
		t.Fatalf("Get(implementer): %v", err)
	}
	if updated.Spec.WakeRequests != 1 || loopState(updated, prLoopKey("acme/widgets", 42), PRLoopStateLabel) != PRLoopStateResolving {
		t.Fatalf("wakeRequests/state = %d/%q, want 1/resolving", updated.Spec.WakeRequests, loopState(updated, prLoopKey("acme/widgets", 42), PRLoopStateLabel))
	}
}

func TestPRLoopRunAnnotationEnablesLoopWithoutRepositoryPolicy(t *testing.T) {
	t.Parallel()
	impl := prLoopImplementerRun("")
	impl.Spec.Trigger = platformv1alpha1.TriggerRef{Kind: "SlackAgent", Name: "support"}
	impl.Annotations[PRLoopOptAnnotation] = PRLoopOptEnabled
	engine, _, _ := newPRLoopEngine(t, impl)

	event := prOpenedEvent()
	event.Repository = "acme/widgets"
	handled, err := engine.HandlePullRequestEvent(context.Background(), nil, event)
	if err != nil || !handled {
		t.Fatalf("HandlePullRequestEvent() = (%v, %v), want (true, nil)", handled, err)
	}
}

func TestPRLoopRunAnnotationDisablesLoop(t *testing.T) {
	t.Parallel()
	impl := prLoopImplementerRun("")
	impl.Spec.Trigger = platformv1alpha1.TriggerRef{Kind: "SlackAgent", Name: "support"}
	impl.Annotations[PRLoopOptAnnotation] = PRLoopOptDisabled
	engine, c, _ := newPRLoopEngine(t, impl)

	event := prOpenedEvent()
	event.Repository = "acme/widgets"
	handled, err := engine.HandlePullRequestEvent(context.Background(), nil, event)
	if err != nil || handled {
		t.Fatalf("HandlePullRequestEvent() = (%v, %v), want (false, nil)", handled, err)
	}
	runs := &platformv1alpha1.AgentRunList{}
	if err := c.List(context.Background(), runs); err != nil {
		t.Fatalf("List(AgentRuns): %v", err)
	}
	if len(runs.Items) != 1 {
		t.Fatalf("AgentRuns = %d, want only implementer", len(runs.Items))
	}
}

func TestPRLoopCrossNamespaceDeliveryUsesImplementerNamespace(t *testing.T) {
	t.Parallel()
	gh := prLoopTestRepo()
	impl := prLoopImplementerRun("")
	impl.Annotations[PRLoopOptAnnotation] = PRLoopOptEnabled
	impl.Namespace = "other-team"
	impl.Spec.Trigger = platformv1alpha1.TriggerRef{Kind: "SlackAgent", Name: "support"}
	impl.Spec.Model = "openai/gpt-5.5"
	impl.Spec.Image = "cross-namespace-worker:v1"
	impl.Spec.Secrets = &platformv1alpha1.AgentRunSecrets{GitHubTokenSecret: "team-github"}
	impl.Status.Artifacts = &platformv1alpha1.AgentRunArtifacts{PullRequestURLs: []string{prOpenedEvent().URL}}
	// AgentRun names are only namespace-unique. An otherwise identical local
	// run must not steal a cross-namespace delivery.
	decoy := prLoopImplementerRun("")
	decoy.Spec.Model = "openai/wrong-run"
	engine, c, _ := newPRLoopEngine(t, gh, impl, decoy)

	handled, err := engine.HandlePullRequestEvent(context.Background(), gh, prOpenedEvent())
	if err != nil || !handled {
		t.Fatalf("HandlePullRequestEvent() = (%v, %v), want (true, nil)", handled, err)
	}
	reviewer := &platformv1alpha1.AgentRun{}
	name := reviewerRunName(impl.Name, prLoopKey("acme/widgets", 42), 1)
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: impl.Namespace, Name: name}, reviewer); err != nil {
		t.Fatalf("Get(cross-namespace reviewer): %v", err)
	}
	if reviewer.Spec.Image != impl.Spec.Image {
		t.Fatalf("reviewer image = %q, want cross-namespace implementer image %q", reviewer.Spec.Image, impl.Spec.Image)
	}
}

func TestPRLoopMultiRepoStateAndReviewerNamesArePerPR(t *testing.T) {
	t.Parallel()
	impl := prLoopImplementerRun("")
	impl.Annotations[PRLoopOptAnnotation] = PRLoopOptEnabled
	impl.Spec.Trigger = platformv1alpha1.TriggerRef{Kind: "Cron", Name: "multi-repo"}
	impl.Spec.Repository.AdditionalRepos = []string{"https://github.com/acme/docs.git"}
	impl.Spec.Model = "openai/gpt-5.5"
	impl.Spec.Secrets = &platformv1alpha1.AgentRunSecrets{GitHubTokenSecret: "github-token"}
	engine, c, _ := newPRLoopEngine(t, impl)

	widgets := prOpenedEvent()
	widgets.Repository = "acme/widgets"
	docs := prOpenedEvent()
	docs.Repository = "acme/docs"
	docs.URL = "https://github.com/acme/docs/pull/42"
	docs.Title = "Update docs"
	for _, event := range []PullRequestEvent{widgets, docs} {
		handled, err := engine.HandlePullRequestEvent(context.Background(), nil, event)
		if err != nil || !handled {
			t.Fatalf("HandlePullRequestEvent(%s) = (%v, %v)", event.Repository, handled, err)
		}
	}
	for _, repository := range []string{"acme/widgets", "acme/docs"} {
		name := reviewerRunName(impl.Name, prLoopKey(repository, 42), 1)
		reviewer := &platformv1alpha1.AgentRun{}
		if err := c.Get(context.Background(), client.ObjectKey{Namespace: impl.Namespace, Name: name}, reviewer); err != nil {
			t.Fatalf("Get(%s reviewer): %v", repository, err)
		}
		if got := repositoryFromCloneURL(reviewer.Spec.Repository.URL); got != repository {
			t.Fatalf("%s reviewer repository = %q, want %q", repository, got, repository)
		}
		for _, additional := range reviewer.Spec.Repository.AdditionalRepos {
			if repositoryFromCloneURL(additional) == repository {
				t.Fatalf("%s reviewer duplicates its primary URL in additional repos: %v", repository, reviewer.Spec.Repository.AdditionalRepos)
			}
		}
	}
	updated := &platformv1alpha1.AgentRun{}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(impl), updated); err != nil {
		t.Fatalf("Get(implementer): %v", err)
	}
	if got := len(loopRecords(updated)); got != 2 {
		t.Fatalf("loop records = %d, want 2", got)
	}
}

func TestPRLoopAdditionalRepoWithoutConfigDoesNotUsePrimaryRepoDefaults(t *testing.T) {
	t.Parallel()
	gh := prLoopTestRepo() // configures only acme/widgets (the primary repo)
	gh.Spec.Defaults.Model = "primary-repo-model"
	gh.Spec.Defaults.Secrets.GithubToken = "primary-repo-token"
	impl := prLoopImplementerRun("")
	impl.Annotations[PRLoopOptAnnotation] = PRLoopOptEnabled
	impl.Spec.Repository.AdditionalRepos = []string{"https://github.com/acme/docs.git"}
	impl.Spec.Model = "openai/implementer-model"
	impl.Spec.Secrets = &platformv1alpha1.AgentRunSecrets{GitHubTokenSecret: "implementer-token"}
	engine, c, _ := newPRLoopEngine(t, gh, impl)

	event := prOpenedEvent()
	event.Repository = "acme/docs"
	event.URL = "https://github.com/acme/docs/pull/42"
	handled, err := engine.HandlePullRequestEvent(context.Background(), nil, event)
	if err != nil || !handled {
		t.Fatalf("HandlePullRequestEvent() = (%v, %v), want (true, nil)", handled, err)
	}
	reviewer := &platformv1alpha1.AgentRun{}
	name := reviewerRunName(impl.Name, prLoopKey("acme/docs", 42), 1)
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: impl.Namespace, Name: name}, reviewer); err != nil {
		t.Fatalf("Get(reviewer): %v", err)
	}
	if reviewer.Spec.Model != impl.Spec.Model {
		t.Fatalf("reviewer model = %q, want implementer model %q (not primary repo defaults)", reviewer.Spec.Model, impl.Spec.Model)
	}
	if reviewer.Spec.Secrets == nil || reviewer.Spec.Secrets.GitHubTokenSecret != "implementer-token" {
		t.Fatalf("reviewer GitHub token ref = %+v, want implementer-token", reviewer.Spec.Secrets)
	}
	if reviewer.Spec.Trigger.Kind != "PRReviewLoop" {
		t.Fatalf("reviewer trigger kind = %q, want fallback PRReviewLoop", reviewer.Spec.Trigger.Kind)
	}
	if got := repositoryFromCloneURL(reviewer.Spec.Repository.URL); got != "acme/docs" {
		t.Fatalf("reviewer repository = %q, want acme/docs", got)
	}
}

func TestPRLoopReviewerUsesSeparateDefaults(t *testing.T) {
	t.Parallel()
	gh := prLoopTestRepo()
	gh.Spec.ReviewLoop = &triggersv1alpha1.ReviewLoopSpec{
		ReviewerDefaults: &triggersv1alpha1.AgentRunDefaults{
			RepoURL:            "https://github.com/other/wrong.git",
			Image:              "reviewer-worker:v2",
			Model:              "gpt-5.5",
			Provider:           triggersv1alpha1.ProviderOpenAI,
			AuthMode:           platformv1alpha1.AgentRunAuthModeAPIKey,
			ReasoningLevel:     platformv1alpha1.ReasoningHigh,
			CustomInstructions: "Focus on security boundaries.",
			Timeout:            metav1.Duration{Duration: 20 * time.Minute},
			RuntimeProfileRef:  &platformv1alpha1.NamedRef{Name: "reviewer-runtime"},
			MCPServerRefs:      []platformv1alpha1.NamedRef{{Name: "github-review"}},
			Secrets: triggersv1alpha1.AgentRunSecrets{
				GithubToken:  "wrong-token",
				ProviderKeys: []platformv1alpha1.ProviderKeyRef{{Provider: "openai", SecretName: "reviewer-openai", SecretKey: "api-key"}},
			},
		},
	}
	impl := prLoopImplementerRun("")
	name := reviewerRunName(impl.Name, prLoopKey("acme/widgets", 42), 1)
	staleInstructions := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: name + "-instructions", Namespace: "default"},
		Data:       map[string]string{"instructions.md": "stale instructions"},
	}
	engine, c, _ := newPRLoopEngine(t, gh, impl, staleInstructions)

	handled, err := engine.HandlePullRequestEvent(context.Background(), gh, prOpenedEvent())
	if err != nil || !handled {
		t.Fatalf("HandlePullRequestEvent() = (%v, %v), want (true, nil)", handled, err)
	}
	reviewer := &platformv1alpha1.AgentRun{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: name}, reviewer); err != nil {
		t.Fatalf("Get(reviewer): %v", err)
	}
	if reviewer.Spec.Model != "gpt-5.5" || reviewer.Spec.Image != "reviewer-worker:v2" || reviewer.Spec.ReasoningLevel != platformv1alpha1.ReasoningHigh {
		t.Fatalf("reviewer model/image/reasoning = %q/%q/%q", reviewer.Spec.Model, reviewer.Spec.Image, reviewer.Spec.ReasoningLevel)
	}
	if reviewer.Spec.Repository.URL != gh.Spec.Defaults.RepoURL || reviewer.Spec.Secrets == nil || reviewer.Spec.Secrets.GitHubTokenSecret != gh.Spec.Defaults.Secrets.GithubToken {
		t.Fatalf("reviewer repository/auth = %+v/%+v, want repository-owned values", reviewer.Spec.Repository, reviewer.Spec.Secrets)
	}
	if reviewer.Spec.RuntimeProfileRef == nil || reviewer.Spec.RuntimeProfileRef.Name != "reviewer-runtime" || len(reviewer.Spec.MCPServerRefs) != 1 {
		t.Fatalf("reviewer runtime/tools = %+v/%+v", reviewer.Spec.RuntimeProfileRef, reviewer.Spec.MCPServerRefs)
	}
	if reviewer.Spec.Limits == nil || reviewer.Spec.Limits.MaxRuntime.Duration != 20*time.Minute {
		t.Fatalf("reviewer limits = %+v, want 20m", reviewer.Spec.Limits)
	}
	instructions := &corev1.ConfigMap{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: name + "-instructions"}, instructions); err != nil {
		t.Fatalf("Get(reviewer instructions): %v", err)
	}
	if instructions.Data["instructions.md"] != "Focus on security boundaries." {
		t.Fatalf("reviewer instructions = %#v", instructions.Data)
	}
	if len(instructions.OwnerReferences) != 1 || instructions.OwnerReferences[0].Kind != "AgentRun" || instructions.OwnerReferences[0].Name != reviewer.Name {
		t.Fatalf("reviewer instructions ownerReferences = %#v", instructions.OwnerReferences)
	}
}

func TestPRLoopReviewerInheritsTriggerDefaults(t *testing.T) {
	t.Parallel()
	gh := prLoopTestRepo()
	gh.Spec.Defaults.ReasoningLevel = platformv1alpha1.ReasoningHigh
	gh.Spec.Defaults.AdditionalRepos = []string{"https://github.com/acme/docs.git"}
	gh.Spec.Defaults.KubernetesAdmin = true
	gh.Spec.Defaults.Timeout = metav1.Duration{Duration: 45 * time.Minute}
	impl := prLoopImplementerRun("")
	engine, c, _ := newPRLoopEngine(t, gh, impl)

	handled, err := engine.HandlePullRequestEvent(context.Background(), gh, prOpenedEvent())
	if err != nil {
		t.Fatalf("HandlePullRequestEvent() error = %v", err)
	}
	if !handled {
		t.Fatal("handled = false, want true")
	}

	reviewer := &platformv1alpha1.AgentRun{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: reviewerRunName(impl.Name, prLoopKey("acme/widgets", 42), 1)}, reviewer); err != nil {
		t.Fatalf("Get(reviewer): %v", err)
	}
	if reviewer.Spec.ReasoningLevel != platformv1alpha1.ReasoningHigh {
		t.Fatalf("reviewer ReasoningLevel = %q, want high", reviewer.Spec.ReasoningLevel)
	}
	if len(reviewer.Spec.Repository.AdditionalRepos) != 1 || reviewer.Spec.Repository.AdditionalRepos[0] != "https://github.com/acme/docs.git" {
		t.Fatalf("reviewer AdditionalRepos = %v, want trigger defaults", reviewer.Spec.Repository.AdditionalRepos)
	}
	if !reviewer.Spec.KubernetesAdmin {
		t.Fatal("reviewer KubernetesAdmin = false, want true (copied from trigger defaults)")
	}
	if reviewer.Spec.Limits == nil || reviewer.Spec.Limits.MaxRuntime.Duration != 45*time.Minute {
		t.Fatalf("reviewer Limits = %+v, want MaxRuntime 45m", reviewer.Spec.Limits)
	}
}
