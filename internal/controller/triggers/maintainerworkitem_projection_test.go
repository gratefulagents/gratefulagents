package triggers

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/google/go-github/v68/github"
	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestEvaluateMaintainerReadinessFailsClosedForHeadBoundCI(t *testing.T) {
	now := time.Now()
	observed := metav1.NewTime(now)
	item := &triggersv1alpha1.MaintainerWorkItem{Spec: triggersv1alpha1.MaintainerWorkItemSpec{Disposition: triggersv1alpha1.MaintainerWorkItemDispositionBounded}, Status: triggersv1alpha1.MaintainerWorkItemStatus{PullRequests: []triggersv1alpha1.MaintainerWorkItemPullRequestProjection{{IntentName: projectionTestMonitorName, Repository: projectionTestRepository, Number: 7, MonitorRef: &coreLocalRef, State: triggersv1alpha1.MaintainerWorkItemPullRequestStateOpen, HeadSHA: "new-head", Mergeable: new(true), ReviewDecision: string(triggersv1alpha1.PullRequestReviewDecisionApproved), CheckState: triggersv1alpha1.MaintainerWorkItemCheckStateUnknown, Fresh: true, HeadObservedAt: &observed, ReviewObservedAt: &observed, ChecksObservedAt: &observed, StatusesObservedAt: &observed}}}}
	evaluateMaintainerReadiness(item, now)
	if item.Status.Readiness.ReadyToMerge {
		t.Fatal("head change without fresh head-bound CI was merge-ready")
	}
	item.Status.PullRequests[0].CheckState = triggersv1alpha1.MaintainerWorkItemCheckStatePassing
	evaluateMaintainerReadiness(item, now)
	if !item.Status.Readiness.ReadyToMerge || item.Status.Phase != triggersv1alpha1.MaintainerWorkItemPhaseReadyToMerge {
		t.Fatalf("fresh exact-head facts not ready: %#v", item.Status)
	}
	stale := metav1.NewTime(now.Add(-maintainerProjectionFreshness - time.Second))
	item.Status.PullRequests[0].ChecksObservedAt = &stale
	evaluateMaintainerReadiness(item, now)
	if item.Status.Readiness.ReadyToMerge {
		t.Fatal("stale checks were merge-ready")
	}
}

const (
	projectionTestMonitorName = "monitor-7"
	projectionTestRequired    = "required"
	projectionTestRepository  = "octo/widgets"
)

var coreLocalRef = structLocalRef(projectionTestMonitorName)

func structLocalRef(name string) (ref corev1.LocalObjectReference) { ref.Name = name; return }

func TestEvaluateMaintainerReadinessDoesNotRedispatchReservedItem(t *testing.T) {
	item := &triggersv1alpha1.MaintainerWorkItem{Spec: triggersv1alpha1.MaintainerWorkItemSpec{Disposition: triggersv1alpha1.MaintainerWorkItemDispositionBounded}, Status: triggersv1alpha1.MaintainerWorkItemStatus{DispatchReservation: &triggersv1alpha1.MaintainerDispatchReservation{ID: "once"}}}
	evaluateMaintainerReadiness(item, time.Now())
	if item.Status.Readiness.ReadyToDispatch {
		t.Fatal("reserved item remained ready to dispatch")
	}
}

func TestEvaluateMaintainerReadinessBlocksDeliveryOnGraphPrerequisites(t *testing.T) {
	item := &triggersv1alpha1.MaintainerWorkItem{Spec: triggersv1alpha1.MaintainerWorkItemSpec{Disposition: triggersv1alpha1.MaintainerWorkItemDispositionDecomposable}, Status: triggersv1alpha1.MaintainerWorkItemStatus{Children: []triggersv1alpha1.MaintainerWorkItemChildProjection{{Name: "child", Delivered: false}}, Dependencies: []triggersv1alpha1.MaintainerWorkItemDependencyProjection{{Name: "dependency", Delivered: true}}, PullRequests: []triggersv1alpha1.MaintainerWorkItemPullRequestProjection{{IntentName: projectionTestMonitorName, Repository: projectionTestRepository, Number: 7, State: triggersv1alpha1.MaintainerWorkItemPullRequestStateMerged}}}}
	evaluateMaintainerReadiness(item, time.Now())
	if item.Status.Phase == triggersv1alpha1.MaintainerWorkItemPhaseDelivered {
		t.Fatal("undelivered child allowed delivery")
	}
	item.Status.Children[0].Delivered = true
	item.Status.Dependencies[0].Delivered = false
	evaluateMaintainerReadiness(item, time.Now())
	if item.Status.Phase == triggersv1alpha1.MaintainerWorkItemPhaseDelivered {
		t.Fatal("undelivered dependency allowed delivery")
	}
}

func TestEvaluateMaintainerReadinessRequiresFinalizationAfterMerge(t *testing.T) {
	item := &triggersv1alpha1.MaintainerWorkItem{Spec: triggersv1alpha1.MaintainerWorkItemSpec{Disposition: triggersv1alpha1.MaintainerWorkItemDispositionBounded}, Status: triggersv1alpha1.MaintainerWorkItemStatus{PullRequests: []triggersv1alpha1.MaintainerWorkItemPullRequestProjection{{IntentName: projectionTestMonitorName, Repository: projectionTestRepository, Number: 7, State: triggersv1alpha1.MaintainerWorkItemPullRequestStateMerged}}}}
	evaluateMaintainerReadiness(item, time.Now())
	if item.Status.Phase == triggersv1alpha1.MaintainerWorkItemPhaseDelivered || item.Status.Readiness.ReadyToMerge {
		t.Fatalf("merge alone must not deliver = %#v", item.Status)
	}
	now := metav1.Now()
	item.Status.DeliveryAttestation = &triggersv1alpha1.MaintainerDeliveryAttestation{CompletedAt: &now}
	evaluateMaintainerReadiness(item, time.Now())
	if item.Status.Phase != triggersv1alpha1.MaintainerWorkItemPhaseDelivered {
		t.Fatalf("finalized merged item phase = %s", item.Status.Phase)
	}
}

func TestProjectMaintainerPRsKeepsExplicitRequiredFilterAfterMatch(t *testing.T) {
	item := &triggersv1alpha1.MaintainerWorkItem{ObjectMeta: metav1.ObjectMeta{Name: "item", UID: types.UID("item-uid")}, Spec: triggersv1alpha1.MaintainerWorkItemSpec{RequiredPullRequests: []triggersv1alpha1.MaintainerRequiredPullRequestIntent{{Name: projectionTestRequired}}}}
	run := platformv1alpha1.AgentRun{ObjectMeta: metav1.ObjectMeta{Name: "implementer", Labels: map[string]string{triggersv1alpha1.MaintainerWorkItemNameLabelKey: item.Name, triggersv1alpha1.MaintainerWorkItemUIDLabelKey: string(item.UID)}}}
	monitors := []triggersv1alpha1.PullRequestMonitor{{ObjectMeta: metav1.ObjectMeta{Name: projectionTestRequired}, Spec: triggersv1alpha1.PullRequestMonitorSpec{ImplementerRef: corev1.LocalObjectReference{Name: run.Name}}}, {ObjectMeta: metav1.ObjectMeta{Name: "unrelated"}, Spec: triggersv1alpha1.PullRequestMonitorSpec{ImplementerRef: corev1.LocalObjectReference{Name: run.Name}}}}
	projectMaintainerRunsAndPRs(item, []platformv1alpha1.AgentRun{run}, monitors, time.Now())
	if len(item.Status.PullRequests) != 1 || item.Status.PullRequests[0].IntentName != projectionTestRequired {
		t.Fatalf("projected PRs = %#v", item.Status.PullRequests)
	}
}

func TestMaintainerGraphMutationLockSerializesCommands(t *testing.T) {
	scheme := maintainerWorkItemScheme(t)
	repository := testMaintainerRepository()
	repository.ResourceVersion = "1"
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(repository).Build()
	r := &GitHubRepositoryReconciler{Client: c, APIReader: c, Scheme: scheme}
	if err := r.acquireMaintainerCommandLock(context.Background(), repository, "first"); err != nil {
		t.Fatal(err)
	}
	if err := r.acquireMaintainerCommandLock(context.Background(), repository, "second"); err == nil {
		t.Fatal("concurrent graph command acquired lock")
	}
	if err := r.releaseMaintainerCommandLock(context.Background(), repository, "first"); err != nil {
		t.Fatal(err)
	}
	if err := r.acquireMaintainerCommandLock(context.Background(), repository, "second"); err != nil {
		t.Fatal(err)
	}
}

func TestDefiniteGitHubDispatchErrorsExcludeAmbiguousOutcomes(t *testing.T) {
	for status, want := range map[int]bool{404: true, 422: true, 408: false, 429: false, 500: false} {
		err := &github.ErrorResponse{Response: &http.Response{StatusCode: status}}
		if got := isDefiniteGitHubDispatchError(err); got != want {
			t.Fatalf("status %d definite = %t, want %t", status, got, want)
		}
	}
}

func TestReleaseMaintainerDispatchReturnsCapacity(t *testing.T) {
	scheme := maintainerWorkItemScheme(t)
	repository := testMaintainerRepository()
	item := testMaintainerWorkItem(repository, 23)
	command := &triggersv1alpha1.MaintainerWorkItemCommand{ObjectMeta: metav1.ObjectMeta{Name: "dispatch"}}
	item.Status.DispatchReservation = &triggersv1alpha1.MaintainerDispatchReservation{ID: "dispatch", CommandRef: corev1.LocalObjectReference{Name: command.Name}, ReservedAt: metav1.Now()}
	ledger := maintainerRepositoryDispatchLedger{Day: time.Now().UTC().Format("2006-01-02"), Count: 1, Reservations: map[string]maintainerRepositoryReservation{item.Name: {CommandName: command.Name, ReservedAt: metav1.Now()}}}
	encoded, err := json.Marshal(ledger)
	if err != nil {
		t.Fatal(err)
	}
	repository.Annotations = map[string]string{triggersv1alpha1.MaintainerDispatchReservationsAnnotation: string(encoded)}
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&triggersv1alpha1.MaintainerWorkItem{}).WithObjects(repository, item).Build()
	r := &GitHubRepositoryReconciler{Client: c, APIReader: c, Scheme: scheme}
	if err := r.releaseMaintainerDispatch(context.Background(), repository, command, item); err != nil {
		t.Fatal(err)
	}
	freshItem := &triggersv1alpha1.MaintainerWorkItem{}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(item), freshItem); err != nil {
		t.Fatal(err)
	}
	if freshItem.Status.DispatchReservation != nil {
		t.Fatalf("reservation = %#v", freshItem.Status.DispatchReservation)
	}
	freshRepository := &triggersv1alpha1.GitHubRepository{}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(repository), freshRepository); err != nil {
		t.Fatal(err)
	}
	var got maintainerRepositoryDispatchLedger
	if err := json.Unmarshal([]byte(freshRepository.Annotations[triggersv1alpha1.MaintainerDispatchReservationsAnnotation]), &got); err != nil {
		t.Fatal(err)
	}
	if got.Count != 0 || len(got.Reservations) != 0 {
		t.Fatalf("ledger = %#v", got)
	}
}

func TestRepositoryDispatchReservationSerializesConcurrentItems(t *testing.T) {
	scheme := maintainerWorkItemScheme(t)
	repository := testMaintainerRepository()
	repository.ResourceVersion = "1"
	repository.Spec.Maintainer = &triggersv1alpha1.MaintainerSpec{MaxConcurrentDispatches: 1, MaxDispatchesPerDay: 10}
	first, second := testMaintainerWorkItem(repository, 21), testMaintainerWorkItem(repository, 22)
	first.UID, second.UID = types.UID("first"), types.UID("second")
	for _, item := range []*triggersv1alpha1.MaintainerWorkItem{first, second} {
		item.Labels = map[string]string{triggersv1alpha1.MaintainerWorkItemRepositoryLabelKey: repository.Name}
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&triggersv1alpha1.MaintainerWorkItem{}).WithObjects(repository, first, second).Build()
	r := &GitHubRepositoryReconciler{Client: c, Scheme: scheme}
	commands := []*triggersv1alpha1.MaintainerWorkItemCommand{{ObjectMeta: metav1.ObjectMeta{Name: "dispatch-first"}, Spec: triggersv1alpha1.MaintainerWorkItemCommandSpec{IdempotencyKey: "first"}}, {ObjectMeta: metav1.ObjectMeta{Name: "dispatch-second"}, Spec: triggersv1alpha1.MaintainerWorkItemCommandSpec{IdempotencyKey: "second"}}}
	items := []*triggersv1alpha1.MaintainerWorkItem{first, second}
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for i := range items {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			<-start
			results <- r.reserveMaintainerDispatch(context.Background(), repository, commands[index], items[index])
		}(i)
	}
	close(start)
	wg.Wait()
	close(results)
	succeeded := 0
	for err := range results {
		if err == nil {
			succeeded++
		}
	}
	if succeeded != 1 {
		t.Fatalf("successful reservations = %d, want 1", succeeded)
	}
}

func TestValidateBreakdownRejectsDependencyCycle(t *testing.T) {
	scheme := maintainerWorkItemScheme(t)
	repository := testMaintainerRepository()
	a := testMaintainerWorkItem(repository, 1)
	b := testMaintainerWorkItem(repository, 2)
	a.UID, b.UID = types.UID("a-uid"), types.UID("b-uid")
	a.Labels = map[string]string{triggersv1alpha1.MaintainerWorkItemRepositoryLabelKey: repository.Name}
	b.Labels = map[string]string{triggersv1alpha1.MaintainerWorkItemRepositoryLabelKey: repository.Name}
	b.Spec.Dependencies = []triggersv1alpha1.MaintainerWorkItemReference{{Name: a.Name, UID: a.UID}}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(repository, a, b).Build()
	r := &GitHubRepositoryReconciler{Client: c, Scheme: scheme}
	err := r.validateBreakdown(context.Background(), repository, a.Name, []triggersv1alpha1.MaintainerWorkItemReference{{Name: b.Name, UID: b.UID}}, []triggersv1alpha1.MaintainerWorkItemReference{{Name: b.Name, UID: b.UID}})
	if err == nil {
		t.Fatal("dependency cycle was accepted")
	}
}
