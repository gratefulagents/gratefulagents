package triggers

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/go-github/v68/github"
	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

type stubTriageClient struct {
	getIssue     func(ctx context.Context, owner, repo string, number int) (*github.Issue, *github.Response, error)
	getIssueHits int
}

type cacheOnlyDeletedMaintainerReader struct {
	client.Reader
	deletedKey  client.ObjectKey
	liveKey     client.ObjectKey
	deletedSeen bool
}

func (r *cacheOnlyDeletedMaintainerReader) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if key == r.deletedKey {
		r.deletedSeen = true
		return apierrors.NewNotFound(schema.GroupResource{Group: triggersv1alpha1.GroupVersion.Group, Resource: "maintainerworkitems"}, key.Name)
	}
	if key == r.liveKey && !r.deletedSeen {
		return errors.New("live item read before cache-only deleted item")
	}
	return r.Reader.Get(ctx, key, obj, opts...)
}

func (s *stubTriageClient) ListIssueComments(context.Context, string, string, int, *github.IssueListCommentsOptions) ([]*github.IssueComment, *github.Response, error) {
	return nil, nil, nil
}

func (s *stubTriageClient) CreateIssueComment(context.Context, string, string, int, *github.IssueComment) (*github.IssueComment, *github.Response, error) {
	return nil, nil, nil
}

func (s *stubTriageClient) EditIssueComment(context.Context, string, string, int64, *github.IssueComment) (*github.IssueComment, *github.Response, error) {
	return nil, nil, nil
}

func (s *stubTriageClient) GetIssue(ctx context.Context, owner, repo string, number int) (*github.Issue, *github.Response, error) {
	s.getIssueHits++
	return s.getIssue(ctx, owner, repo, number)
}

func (s *stubTriageClient) GetLabel(context.Context, string, string, string) (*github.Label, *github.Response, error) {
	return &github.Label{}, &github.Response{}, nil
}

func (s *stubTriageClient) CreateLabel(context.Context, string, string, *github.Label) (*github.Label, *github.Response, error) {
	return &github.Label{}, &github.Response{}, nil
}

func (s *stubTriageClient) AddLabelsToIssue(context.Context, string, string, int, []string) ([]*github.Label, *github.Response, error) {
	return nil, nil, nil
}

func (s *stubTriageClient) EditIssue(context.Context, string, string, int, *github.IssueRequest) (*github.Issue, *github.Response, error) {
	return nil, nil, nil
}

func TestMaintainerObservationPassesIgnoreCacheOnlyDeletedItems(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name   string
		reason string
		run    func(context.Context, *GitHubRepositoryReconciler, *triggersv1alpha1.GitHubRepository) error
	}{
		{
			name:   "complete issue list",
			reason: "NotInOpenIssueList",
			run: func(ctx context.Context, reconciler *GitHubRepositoryReconciler, repository *triggersv1alpha1.GitHubRepository) error {
				return reconciler.reconcileMaintainerWorkItems(ctx, repository, nil, true, nil)
			},
		},
		{
			name:   "observations unavailable",
			reason: "IssuePollUnavailable",
			run: func(ctx context.Context, reconciler *GitHubRepositoryReconciler, repository *triggersv1alpha1.GitHubRepository) error {
				return reconciler.markMaintainerWorkItemObservationsUnavailable(ctx, repository, "IssuePollUnavailable", "issue poll failed")
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			scheme := maintainerWorkItemScheme(t)
			repository := testMaintainerRepository()
			staleItem := testMaintainerWorkItem(repository, 7)
			staleItem.Labels = map[string]string{triggersv1alpha1.MaintainerWorkItemRepositoryLabelKey: repository.Name}
			liveItem := testMaintainerWorkItem(repository, 8)
			liveItem.Labels = map[string]string{triggersv1alpha1.MaintainerWorkItemRepositoryLabelKey: repository.Name}
			cachedClient := fake.NewClientBuilder().WithScheme(scheme).
				WithStatusSubresource(&triggersv1alpha1.MaintainerWorkItem{}).
				WithObjects(repository, staleItem, liveItem).
				Build()
			apiReader := &cacheOnlyDeletedMaintainerReader{
				Reader:     cachedClient,
				deletedKey: client.ObjectKeyFromObject(staleItem),
				liveKey:    client.ObjectKeyFromObject(liveItem),
			}
			reconciler := &GitHubRepositoryReconciler{Client: cachedClient, APIReader: apiReader, Scheme: scheme}

			if err := test.run(context.Background(), reconciler, repository); err != nil {
				t.Fatalf("observation pass returned an error for a cache-only deleted item: %v", err)
			}
			stored := &triggersv1alpha1.MaintainerWorkItem{}
			if err := cachedClient.Get(context.Background(), client.ObjectKeyFromObject(liveItem), stored); err != nil {
				t.Fatal(err)
			}
			condition := findMaintainerWorkItemCondition(stored, triggersv1alpha1.ConditionMaintainerWorkItemObservationFresh)
			if condition == nil || condition.Status != metav1.ConditionFalse || condition.Reason != test.reason {
				t.Fatalf("live item freshness condition = %#v, want false with reason %q", condition, test.reason)
			}
		})
	}
}

// A merged pull request auto-closes its issue; the issue leaves the open list
// but must be re-observed directly so finalize preconditions stay satisfiable.
func TestReconcileMaintainerWorkItemsObservesClosedIssueDirectly(t *testing.T) {
	t.Parallel()

	scheme := maintainerWorkItemScheme(t)
	repository := testMaintainerRepository()
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&triggersv1alpha1.MaintainerWorkItem{}).WithObjects(repository).Build()
	reconciler := &GitHubRepositoryReconciler{Client: c, Scheme: scheme}

	issue := testMaintainerIssue(7)
	if err := reconciler.reconcileMaintainerWorkItems(context.Background(), repository, []*github.Issue{issue}, true, nil); err != nil {
		t.Fatalf("open projection: %v", err)
	}

	closedIssue := testMaintainerIssue(7)
	closedIssue.State = new("closed")
	stub := &stubTriageClient{getIssue: func(context.Context, string, string, int) (*github.Issue, *github.Response, error) {
		return closedIssue, nil, nil
	}}
	if err := reconciler.reconcileMaintainerWorkItems(context.Background(), repository, nil, true, stub); err != nil {
		t.Fatalf("closed projection: %v", err)
	}
	item := getMaintainerWorkItem(t, c, repository, 7)
	if item.Status.IssueObservation.State != triggersv1alpha1.MaintainerIssueStateClosed {
		t.Fatalf("observation state = %q, want closed", item.Status.IssueObservation.State)
	}
	if !maintainerWorkItemObservationIsFresh(item) {
		t.Fatalf("closed observation is not fresh: %#v", item.Status.Conditions)
	}
	condition := findMaintainerWorkItemCondition(item, triggersv1alpha1.ConditionMaintainerWorkItemObservationFresh)
	if condition == nil || condition.Reason != "ObservedDirectly" {
		t.Fatalf("condition = %#v, want ObservedDirectly", condition)
	}

	// A fresh closed observation is stable; no further direct reads occur.
	if err := reconciler.reconcileMaintainerWorkItems(context.Background(), repository, nil, true, stub); err != nil {
		t.Fatalf("steady-state projection: %v", err)
	}
	if stub.getIssueHits != 1 {
		t.Fatalf("GetIssue hits = %d, want 1", stub.getIssueHits)
	}
}

// GitHub bumps updatedAt on every comment; that alone must not advance the
// semantic projection sequence or invalidate pending command preconditions.
func TestObserveMaintainerWorkItemIgnoresUpdatedAtOnlyChanges(t *testing.T) {
	t.Parallel()

	scheme := maintainerWorkItemScheme(t)
	repository := testMaintainerRepository()
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&triggersv1alpha1.MaintainerWorkItem{}).WithObjects(repository).Build()
	reconciler := &GitHubRepositoryReconciler{Client: c, Scheme: scheme}

	issue := testMaintainerIssue(7)
	if err := reconciler.reconcileMaintainerWorkItems(context.Background(), repository, []*github.Issue{issue}, true, nil); err != nil {
		t.Fatalf("first projection: %v", err)
	}
	item := getMaintainerWorkItem(t, c, repository, 7)
	sequence := item.Status.ProjectionSequence

	bumped := testMaintainerIssue(7)
	bumped.UpdatedAt = &github.Timestamp{Time: time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)}
	if err := reconciler.reconcileMaintainerWorkItems(context.Background(), repository, []*github.Issue{bumped}, true, nil); err != nil {
		t.Fatalf("updatedAt-only projection: %v", err)
	}
	item = getMaintainerWorkItem(t, c, repository, 7)
	if item.Status.ProjectionSequence != sequence {
		t.Fatalf("updatedAt-only change advanced sequence %d -> %d", sequence, item.Status.ProjectionSequence)
	}

	retitled := testMaintainerIssue(7)
	retitled.Title = new("substantive change")
	if err := reconciler.reconcileMaintainerWorkItems(context.Background(), repository, []*github.Issue{retitled}, true, nil); err != nil {
		t.Fatalf("substantive projection: %v", err)
	}
	item = getMaintainerWorkItem(t, c, repository, 7)
	if item.Status.ProjectionSequence != sequence+1 {
		t.Fatalf("substantive change sequence = %d, want %d", item.Status.ProjectionSequence, sequence+1)
	}
}

// Free-form error messages (rate-limit reset times and similar) must not
// advance every work item's sequence on each failed poll.
func TestObservationErrorMessageChangesDoNotAdvanceSequence(t *testing.T) {
	t.Parallel()

	scheme := maintainerWorkItemScheme(t)
	repository := testMaintainerRepository()
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&triggersv1alpha1.MaintainerWorkItem{}).WithObjects(repository).Build()
	reconciler := &GitHubRepositoryReconciler{Client: c, Scheme: scheme}

	issue := testMaintainerIssue(7)
	if err := reconciler.reconcileMaintainerWorkItems(context.Background(), repository, []*github.Issue{issue}, true, nil); err != nil {
		t.Fatalf("projection: %v", err)
	}
	item := getMaintainerWorkItem(t, c, repository, 7)
	key := client.ObjectKeyFromObject(item)

	if err := reconciler.markMaintainerWorkItemObservationNotFresh(context.Background(), key, "IssuePollUnavailable", "rate reset in 34m12s"); err != nil {
		t.Fatalf("first not-fresh: %v", err)
	}
	item = getMaintainerWorkItem(t, c, repository, 7)
	sequence := item.Status.ProjectionSequence

	if err := reconciler.markMaintainerWorkItemObservationNotFresh(context.Background(), key, "IssuePollUnavailable", "rate reset in 33m01s"); err != nil {
		t.Fatalf("second not-fresh: %v", err)
	}
	item = getMaintainerWorkItem(t, c, repository, 7)
	if item.Status.ProjectionSequence != sequence {
		t.Fatalf("message-only change advanced sequence %d -> %d", sequence, item.Status.ProjectionSequence)
	}
}

func TestPruneMaintainerDispatchReservations(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	recent := metav1.NewTime(now.Add(-time.Hour))
	expired := metav1.NewTime(now.Add(-25 * time.Hour))
	ledger := &maintainerRepositoryDispatchLedger{Day: now.Format("2006-01-02"), Count: 6, Reservations: map[string]maintainerRepositoryReservation{
		"deleted-item":         {CommandName: "a", ReservedAt: recent},
		"finished-item":        {CommandName: "b", ReservedAt: recent},
		"expired-item":         {CommandName: "c", ReservedAt: expired},
		"pending-item":         {CommandName: "d", ReservedAt: recent},
		"running-item":         {CommandName: "e", ReservedAt: recent},
		"protected-item":       {CommandName: "f", ReservedAt: expired},
		"materialized-active":  {CommandName: "g", ReservedAt: recent},
		"not-actionable-item":  {CommandName: "h", ReservedAt: recent},
		"not-actionable-still": {CommandName: "i", ReservedAt: recent},
	}}
	workItemUIDs := map[string]string{
		"finished-item":        "uid-b",
		"expired-item":         "uid-c",
		"pending-item":         "uid-d",
		"running-item":         "uid-e",
		"protected-item":       "uid-f",
		"materialized-active":  "uid-g",
		"not-actionable-item":  "uid-h",
		"not-actionable-still": "uid-i",
	}
	materialized := map[string]bool{"finished-item": true, "materialized-active": true}
	activeItems := map[string]bool{"running-item": true, "materialized-active": true, "not-actionable-still": true}
	notActionable := map[string]bool{"not-actionable-item": true, "not-actionable-still": true}

	if !pruneMaintainerDispatchReservations(ledger, "protected-item", workItemUIDs, materialized, activeItems, notActionable, now) {
		t.Fatal("expected pruning to report changes")
	}
	for _, gone := range []string{"deleted-item", "finished-item", "expired-item", "not-actionable-item"} {
		if _, ok := ledger.Reservations[gone]; ok {
			t.Fatalf("reservation %q was not pruned", gone)
		}
	}
	for _, kept := range []string{"pending-item", "running-item", "protected-item", "materialized-active", "not-actionable-still"} {
		if _, ok := ledger.Reservations[kept]; !ok {
			t.Fatalf("reservation %q was wrongly pruned", kept)
		}
	}
	if pruneMaintainerDispatchReservations(ledger, "protected-item", workItemUIDs, materialized, activeItems, notActionable, now) {
		t.Fatal("second prune must be a no-op")
	}
}

func TestAuthenticatedRedispatchReplacesOrphanAndRecreatesRun(t *testing.T) {
	t.Parallel()

	scheme := maintainerWorkItemScheme(t)
	repository := testMaintainerRepository()
	repository.Spec.Defaults = triggerRunTestDefaults()
	repository.Spec.Maintainer = &triggersv1alpha1.MaintainerSpec{DispatchModeRef: "implementer"}
	now := time.Now().UTC()
	oldCommandName := "old-dispatch"
	item := testMaintainerWorkItem(repository, 7)
	controller := true
	item.OwnerReferences = []metav1.OwnerReference{{APIVersion: triggersv1alpha1.GroupVersion.String(), Kind: gitHubRepositoryTriggerKind, Name: repository.Name, UID: repository.UID, Controller: &controller}}
	item.Labels = map[string]string{triggersv1alpha1.MaintainerWorkItemRepositoryLabelKey: repository.Name}
	item.Spec.Disposition = triggersv1alpha1.MaintainerWorkItemDispositionBounded
	item.Spec.GraphConfiguredByCommand = &corev1.LocalObjectReference{Name: "graph-command"}
	runName := ghIssueName(repository.Spec.Owner, repository.Spec.Repo, "7")
	item.Status.DispatchReservation = &triggersv1alpha1.MaintainerDispatchReservation{ID: "old-key", CommandRef: corev1.LocalObjectReference{Name: oldCommandName}, ReservedAt: metav1.NewTime(now), AgentRunRef: &corev1.LocalObjectReference{Name: runName}}
	item.Status.AuthorizedAgentRuns = []triggersv1alpha1.MaintainerAuthorizedAgentRunReference{{Name: runName, UID: "deleted-run-uid"}}
	repository.Annotations = map[string]string{triggersv1alpha1.MaintainerDispatchReservationsAnnotation: fmt.Sprintf(`{"day":%q,"count":1,"reservations":{%q:{"commandName":%q,"reservedAt":%q}}}`, now.Format("2006-01-02"), item.Name, oldCommandName, now.Format(time.RFC3339))}
	command := &triggersv1alpha1.MaintainerWorkItemCommand{
		ObjectMeta: metav1.ObjectMeta{Name: "recovery-dispatch", Namespace: repository.Namespace},
		Spec: triggersv1alpha1.MaintainerWorkItemCommandSpec{
			IdempotencyKey: "recovery-key",
			Type:           triggersv1alpha1.MaintainerWorkItemCommandTypeDispatchWorkItem,
			Dispatch:       &triggersv1alpha1.MaintainerDispatchWorkItemCommand{IssueNumber: 7, Mode: "implementer"},
		},
	}
	mode := &platformv1alpha1.ModeTemplate{ObjectMeta: metav1.ObjectMeta{Name: "implementer"}}
	c := buildTriggerFakeClient(fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&triggersv1alpha1.MaintainerWorkItem{}, &triggersv1alpha1.MaintainerWorkItemCommand{}).WithObjects(repository, item, command, mode))
	reconciler := &GitHubRepositoryReconciler{Client: c, APIReader: c, Scheme: scheme}

	if err := reconciler.replaceOrphanedMaintainerDispatch(context.Background(), repository, command, item); err != nil {
		t.Fatalf("replace orphaned dispatch: %v", err)
	}
	freshItem := &triggersv1alpha1.MaintainerWorkItem{}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(item), freshItem); err != nil {
		t.Fatal(err)
	}
	if freshItem.Status.DispatchReservation == nil || freshItem.Status.DispatchReservation.CommandRef.Name != command.Name || freshItem.Status.DispatchReservation.AgentRunRef != nil {
		t.Fatalf("replacement reservation = %#v", freshItem.Status.DispatchReservation)
	}
	freshRepository := &triggersv1alpha1.GitHubRepository{}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(repository), freshRepository); err != nil {
		t.Fatal(err)
	}
	if raw := freshRepository.Annotations[triggersv1alpha1.MaintainerDispatchReservationsAnnotation]; !strings.Contains(raw, `"count":1`) || !strings.Contains(raw, command.Name) {
		t.Fatalf("transferred dispatch ledger = %s", raw)
	}
	if command.Status.Result == nil || command.Status.Result.AgentRunRef == nil || command.Status.Result.AgentRunRef.Name != runName {
		t.Fatalf("validated recovery result = %#v", command.Status.Result)
	}
	// A crash after reservation transfer leaves the command Pending. Its replay
	// must cross the durable reservation boundary without consulting the false
	// ReadyToDispatch projection caused by that reservation.
	if err := reconciler.applyMaintainerExecutionIntent(context.Background(), repository, command, freshItem); err != nil {
		t.Fatalf("replay transferred recovery: %v", err)
	}
	if err := reconciler.setMaintainerWorkItemCommandAccepted(context.Background(), command, freshItem); err != nil {
		t.Fatalf("accept recovery command: %v", err)
	}
	freshCommand := &triggersv1alpha1.MaintainerWorkItemCommand{}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(command), freshCommand); err != nil {
		t.Fatal(err)
	}
	if freshCommand.Status.Result == nil || freshCommand.Status.Result.AgentRunRef == nil || freshCommand.Status.Result.AgentRunRef.Name != runName {
		t.Fatalf("accepted recovery result = %#v", freshCommand.Status.Result)
	}
	issue := testMaintainerIssue(7)
	if err := reconciler.recreateMaintainerDispatchRun(context.Background(), repository, freshCommand, freshItem, issue); err != nil {
		t.Fatalf("recreate implementer: %v", err)
	}
	run := &platformv1alpha1.AgentRun{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: repository.Namespace, Name: runName}, run); err != nil {
		t.Fatalf("get recreated AgentRun: %v", err)
	}
	if run.Spec.ModeRef == nil || run.Spec.ModeRef.Name != "implementer" {
		t.Fatalf("recreated mode = %#v", run.Spec.ModeRef)
	}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(item), freshItem); err != nil {
		t.Fatal(err)
	}
	if len(freshItem.Status.AuthorizedAgentRuns) != 1 || freshItem.Status.AuthorizedAgentRuns[0].UID != run.UID {
		t.Fatalf("replacement authorization = %#v, want UID %s", freshItem.Status.AuthorizedAgentRuns, run.UID)
	}
}

func TestRedispatchRejectsMissingCapacityReservation(t *testing.T) {
	t.Parallel()

	scheme := maintainerWorkItemScheme(t)
	repository := testMaintainerRepository()
	item := testMaintainerWorkItem(repository, 8)
	controller := true
	item.OwnerReferences = []metav1.OwnerReference{{APIVersion: triggersv1alpha1.GroupVersion.String(), Kind: gitHubRepositoryTriggerKind, Name: repository.Name, UID: repository.UID, Controller: &controller}}
	item.Labels = map[string]string{triggersv1alpha1.MaintainerWorkItemRepositoryLabelKey: repository.Name}
	item.Spec.Disposition = triggersv1alpha1.MaintainerWorkItemDispositionBounded
	item.Spec.GraphConfiguredByCommand = &corev1.LocalObjectReference{Name: "graph-command"}
	item.Status.DispatchReservation = &triggersv1alpha1.MaintainerDispatchReservation{ID: "old-key", CommandRef: corev1.LocalObjectReference{Name: "old-dispatch"}, ReservedAt: metav1.Now(), AgentRunRef: &corev1.LocalObjectReference{Name: ghIssueName(repository.Spec.Owner, repository.Spec.Repo, "8")}}
	command := &triggersv1alpha1.MaintainerWorkItemCommand{ObjectMeta: metav1.ObjectMeta{Name: "recovery-dispatch", Namespace: repository.Namespace}, Spec: triggersv1alpha1.MaintainerWorkItemCommandSpec{IdempotencyKey: "new-key", Dispatch: &triggersv1alpha1.MaintainerDispatchWorkItemCommand{IssueNumber: 8, Mode: defaultMaintainerDispatchModeName}}}
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&triggersv1alpha1.MaintainerWorkItem{}, &triggersv1alpha1.MaintainerWorkItemCommand{}).WithObjects(repository, item, command).Build()
	reconciler := &GitHubRepositoryReconciler{Client: c, APIReader: c, Scheme: scheme}

	if err := reconciler.replaceOrphanedMaintainerDispatch(context.Background(), repository, command, item); err == nil || !strings.Contains(err.Error(), "capacity reservation") {
		t.Fatalf("missing-ledger recovery error = %v", err)
	}
}

func TestRecoveryActiveImplementerCheckIncludesLegacyRepositoryRuns(t *testing.T) {
	t.Parallel()

	repository := testMaintainerRepository()
	item := testMaintainerWorkItem(repository, 9)
	controller := true
	legacy := platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{
			Name: "legacy-implementer", Namespace: repository.Namespace,
			OwnerReferences: []metav1.OwnerReference{{APIVersion: triggersv1alpha1.GroupVersion.String(), Kind: gitHubRepositoryTriggerKind, Name: repository.Name, UID: repository.UID, Controller: &controller}},
		},
		Spec: platformv1alpha1.AgentRunSpec{Trigger: platformv1alpha1.TriggerRef{
			Kind: gitHubRepositoryTriggerKind, Name: repository.Name,
			ExternalRef: &platformv1alpha1.ExternalRef{Identifier: "#9"},
		}},
		Status: platformv1alpha1.AgentRunStatus{Phase: platformv1alpha1.AgentRunPhaseRunning},
	}
	expectedRunName := ghIssueName(repository.Spec.Owner, repository.Spec.Repo, "9")
	if !hasOtherActiveMaintainerImplementer(repository, item, expectedRunName, []platformv1alpha1.AgentRun{legacy}) {
		t.Fatal("active legacy implementer was not correlated to the work item")
	}
	legacy.Labels = map[string]string{triggersv1alpha1.PRLoopRoleLabelKey: triggersv1alpha1.PRLoopRoleReviewerValue}
	if hasOtherActiveMaintainerImplementer(repository, item, expectedRunName, []platformv1alpha1.AgentRun{legacy}) {
		t.Fatal("legacy reviewer was treated as an active implementer")
	}
}

func TestCurrentProjectionMessageIncludesStaleObservationFacts(t *testing.T) {
	t.Parallel()

	item := testMaintainerWorkItem(testMaintainerRepository(), 7)
	item.Status.Conditions = []metav1.Condition{{Type: triggersv1alpha1.ConditionMaintainerWorkItemObservationFresh, Status: metav1.ConditionFalse, Reason: "NotInOpenIssueList"}}
	message := currentProjectionMessage(item)
	if !strings.Contains(message, "NotInOpenIssueList") || !strings.Contains(message, "observation not fresh") {
		t.Fatalf("message %q does not explain the failing observation", message)
	}
}

// Commands that already crossed a durable side-effect boundary must stay
// retryable so a later-visible outcome is still recorded.
func TestRetryBudgetExemptsDurableSideEffects(t *testing.T) {
	t.Parallel()

	scheme := maintainerWorkItemScheme(t)
	repository := testMaintainerRepository()
	item := testMaintainerWorkItem(repository, 7)
	attempted := metav1.Now()
	mergeCommand := &triggersv1alpha1.MaintainerWorkItemCommand{
		ObjectMeta: metav1.ObjectMeta{Name: "merge-cmd", Namespace: repository.Namespace},
		Spec:       triggersv1alpha1.MaintainerWorkItemCommandSpec{Type: triggersv1alpha1.MaintainerWorkItemCommandTypeRequestMerge, Preconditions: triggersv1alpha1.MaintainerWorkItemCommandPreconditions{WorkItemName: item.Name}},
		Status:     triggersv1alpha1.MaintainerWorkItemCommandStatus{Result: &triggersv1alpha1.MaintainerWorkItemCommandResult{MergeAttemptedAt: &attempted}},
	}
	finalizeCommand := &triggersv1alpha1.MaintainerWorkItemCommand{
		ObjectMeta: metav1.ObjectMeta{Name: "finalize-cmd", Namespace: repository.Namespace},
		Spec:       triggersv1alpha1.MaintainerWorkItemCommandSpec{Type: triggersv1alpha1.MaintainerWorkItemCommandTypeFinalizeWorkItem, Preconditions: triggersv1alpha1.MaintainerWorkItemCommandPreconditions{WorkItemName: item.Name}},
	}
	plainCommand := &triggersv1alpha1.MaintainerWorkItemCommand{
		ObjectMeta: metav1.ObjectMeta{Name: "dispatch-cmd", Namespace: repository.Namespace},
		Spec:       triggersv1alpha1.MaintainerWorkItemCommandSpec{Type: triggersv1alpha1.MaintainerWorkItemCommandTypeDispatchWorkItem, Preconditions: triggersv1alpha1.MaintainerWorkItemCommandPreconditions{WorkItemName: item.Name}},
	}
	item.Status.DeliveryAttestation = &triggersv1alpha1.MaintainerDeliveryAttestation{FinalizedByCommand: corev1.LocalObjectReference{Name: finalizeCommand.Name}}
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&triggersv1alpha1.MaintainerWorkItem{}).WithObjects(repository, item).Build()
	reconciler := &GitHubRepositoryReconciler{Client: c, Scheme: scheme}

	for _, tc := range []struct {
		command *triggersv1alpha1.MaintainerWorkItemCommand
		durable bool
	}{
		{mergeCommand, true},
		{finalizeCommand, true},
		{plainCommand, false},
	} {
		durable, err := reconciler.maintainerCommandHasDurableSideEffects(context.Background(), repository, tc.command)
		if err != nil {
			t.Fatalf("%s: %v", tc.command.Name, err)
		}
		if durable != tc.durable {
			t.Fatalf("%s durable = %v, want %v", tc.command.Name, durable, tc.durable)
		}
	}
}
