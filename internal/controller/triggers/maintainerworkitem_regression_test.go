package triggers

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/go-github/v68/github"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

type stubTriageClient struct {
	getIssue     func(ctx context.Context, owner, repo string, number int) (*github.Issue, *github.Response, error)
	getIssueHits int
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

func (s *stubTriageClient) AddLabelsToIssue(context.Context, string, string, int, []string) ([]*github.Label, *github.Response, error) {
	return nil, nil, nil
}

func (s *stubTriageClient) EditIssue(context.Context, string, string, int, *github.IssueRequest) (*github.Issue, *github.Response, error) {
	return nil, nil, nil
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
		"deleted-item":        {CommandName: "a", ReservedAt: recent},
		"finished-item":       {CommandName: "b", ReservedAt: recent},
		"expired-item":        {CommandName: "c", ReservedAt: expired},
		"pending-item":        {CommandName: "d", ReservedAt: recent},
		"running-item":        {CommandName: "e", ReservedAt: recent},
		"protected-item":      {CommandName: "f", ReservedAt: expired},
		"materialized-active": {CommandName: "g", ReservedAt: recent},
	}}
	workItemUIDs := map[string]string{
		"finished-item":       "uid-b",
		"expired-item":        "uid-c",
		"pending-item":        "uid-d",
		"running-item":        "uid-e",
		"protected-item":      "uid-f",
		"materialized-active": "uid-g",
	}
	materialized := map[string]bool{"finished-item": true, "materialized-active": true}
	activeItems := map[string]bool{"running-item": true, "materialized-active": true}

	if !pruneMaintainerDispatchReservations(ledger, "protected-item", workItemUIDs, materialized, activeItems, now) {
		t.Fatal("expected pruning to report changes")
	}
	for _, gone := range []string{"deleted-item", "finished-item", "expired-item"} {
		if _, ok := ledger.Reservations[gone]; ok {
			t.Fatalf("reservation %q was not pruned", gone)
		}
	}
	for _, kept := range []string{"pending-item", "running-item", "protected-item", "materialized-active"} {
		if _, ok := ledger.Reservations[kept]; !ok {
			t.Fatalf("reservation %q was wrongly pruned", kept)
		}
	}
	if pruneMaintainerDispatchReservations(ledger, "protected-item", workItemUIDs, materialized, activeItems, now) {
		t.Fatal("second prune must be a no-op")
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
