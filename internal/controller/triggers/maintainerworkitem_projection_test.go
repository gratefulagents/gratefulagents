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

func TestReconcileMaintainerExecutionProjectionIgnoresCacheOnlyDeletedItems(t *testing.T) {
	t.Parallel()

	scheme := maintainerWorkItemScheme(t)
	repository := testMaintainerRepository()
	staleItem := testMaintainerWorkItem(repository, 7)
	cachedClient := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&triggersv1alpha1.MaintainerWorkItem{}).
		WithObjects(repository, staleItem).
		Build()
	apiReader := fake.NewClientBuilder().WithScheme(scheme).WithObjects(repository).Build()
	reconciler := &GitHubRepositoryReconciler{Client: cachedClient, APIReader: apiReader, Scheme: scheme}

	if err := reconciler.reconcileMaintainerExecutionProjection(context.Background(), repository); err != nil {
		t.Fatalf("projection returned an error for a cache-only deleted item: %v", err)
	}
	stored := &triggersv1alpha1.MaintainerWorkItem{}
	if err := cachedClient.Get(context.Background(), client.ObjectKeyFromObject(staleItem), stored); err != nil {
		t.Fatal(err)
	}
	if stored.Status.ProjectionSequence != staleItem.Status.ProjectionSequence {
		t.Fatalf("cache-only item was projected: sequence = %d, want %d", stored.Status.ProjectionSequence, staleItem.Status.ProjectionSequence)
	}
}

// Two monitors can observe the same pull request (for example a manually
// created recovery monitor next to the canonical controller-created one).
// Informer-cache list order is arbitrary, so the tied entries must get a total
// order and the semantic comparison must ignore map-list ordering; otherwise
// every projection pass advances the sequence and waiter v2 spins on an
// unchanged delivered item (issue #132).
func TestMaintainerProjectionDuplicatePullRequestMonitorsDoNotChurnSequence(t *testing.T) {
	t.Parallel()

	now := metav1.Now()
	canonical := triggersv1alpha1.MaintainerWorkItemPullRequestProjection{IntentName: "pr-monitor-aaa", MonitorRef: &corev1.LocalObjectReference{Name: "pr-monitor-aaa"}, Repository: projectionTestRepository, Number: 29, State: triggersv1alpha1.MaintainerWorkItemPullRequestStateMerged, MergedAt: &now, HeadSHA: "head"}
	recovery := canonical
	recovery.IntentName = "recovery-gateway-7-pr29"
	recovery.MonitorRef = &corev1.LocalObjectReference{Name: "recovery-gateway-7-pr29"}

	stored := &triggersv1alpha1.MaintainerWorkItemStatus{PullRequests: []triggersv1alpha1.MaintainerWorkItemPullRequestProjection{recovery, canonical}}
	recomputed := &triggersv1alpha1.MaintainerWorkItemStatus{PullRequests: []triggersv1alpha1.MaintainerWorkItemPullRequestProjection{canonical, recovery}}
	if !maintainerWorkItemStatusSemanticallyEqual(stored, recomputed) {
		t.Fatal("map-list order flip of duplicate pull-request projections would advance the projection sequence")
	}
	substantive := recomputed.DeepCopy()
	substantive.PullRequests[0].State = triggersv1alpha1.MaintainerWorkItemPullRequestStateOpen
	if maintainerWorkItemStatusSemanticallyEqual(stored, substantive) {
		t.Fatal("a substantive pull-request change must advance the projection sequence")
	}
}

// projectMaintainerRunsAndPRs must emit the same pull-request order regardless
// of the (arbitrary) monitor list order, including when two monitors observe
// the same repository and number.
func TestMaintainerProjectionSortsTiedPullRequestsByIntentName(t *testing.T) {
	t.Parallel()

	itemName, itemUID := "mwi-item-7", types.UID("item-uid")
	run := platformv1alpha1.AgentRun{ObjectMeta: metav1.ObjectMeta{Name: "impl-run", UID: types.UID("run-uid"), Labels: map[string]string{triggersv1alpha1.MaintainerWorkItemNameLabelKey: itemName, triggersv1alpha1.MaintainerWorkItemUIDLabelKey: string(itemUID)}}, Status: platformv1alpha1.AgentRunStatus{Phase: platformv1alpha1.AgentRunPhaseSucceeded}}
	newMonitor := func(name string) triggersv1alpha1.PullRequestMonitor {
		return triggersv1alpha1.PullRequestMonitor{ObjectMeta: metav1.ObjectMeta{Name: name}, Spec: triggersv1alpha1.PullRequestMonitorSpec{Repository: projectionTestRepository, Number: 29, ImplementerRef: corev1.LocalObjectReference{Name: run.Name}}, Status: triggersv1alpha1.PullRequestMonitorStatus{Lifecycle: triggersv1alpha1.PullRequestLifecycleMerged}}
	}
	first, second := newMonitor("pr-monitor-aaa"), newMonitor("recovery-gateway-7-pr29")

	var orders [][]string
	for _, monitors := range [][]triggersv1alpha1.PullRequestMonitor{{first, second}, {second, first}} {
		item := &triggersv1alpha1.MaintainerWorkItem{ObjectMeta: metav1.ObjectMeta{Name: itemName, UID: itemUID}}
		projectMaintainerRunsAndPRs(item, []platformv1alpha1.AgentRun{run}, monitors, time.Now())
		names := make([]string, 0, len(item.Status.PullRequests))
		for _, pr := range item.Status.PullRequests {
			names = append(names, pr.IntentName)
		}
		orders = append(orders, names)
	}
	want := []string{"pr-monitor-aaa", "recovery-gateway-7-pr29"}
	for _, order := range orders {
		if len(order) != 2 || order[0] != want[0] || order[1] != want[1] {
			t.Fatalf("pull-request order = %v, want %v regardless of monitor list order", order, want)
		}
	}
}

func TestMaintainerProjectionTreatsCheckAndReviewChangesAsWaiterEvents(t *testing.T) {
	now := metav1.Now()
	before := &triggersv1alpha1.MaintainerWorkItemStatus{PullRequests: []triggersv1alpha1.MaintainerWorkItemPullRequestProjection{{IntentName: "monitor", ReviewDecision: string(triggersv1alpha1.PullRequestReviewDecisionUnknown), CheckState: triggersv1alpha1.MaintainerWorkItemCheckStatePending, ReviewObservedAt: &now, ChecksObservedAt: &now}}}
	reviewed := before.DeepCopy()
	reviewed.PullRequests[0].ReviewDecision = string(triggersv1alpha1.PullRequestReviewDecisionApproved)
	if maintainerWorkItemStatusSemanticallyEqual(before, reviewed) {
		t.Fatal("review decision change would not advance the waiter projection sequence")
	}
	checked := before.DeepCopy()
	checked.PullRequests[0].CheckState = triggersv1alpha1.MaintainerWorkItemCheckStatePassing
	if maintainerWorkItemStatusSemanticallyEqual(before, checked) {
		t.Fatal("check state change would not advance the waiter projection sequence")
	}
	heartbeat := before.DeepCopy()
	later := metav1.NewTime(now.Add(time.Minute))
	heartbeat.PullRequests[0].ReviewObservedAt = &later
	heartbeat.PullRequests[0].ChecksObservedAt = &later
	if !maintainerWorkItemStatusSemanticallyEqual(before, heartbeat) {
		t.Fatal("timestamp-only heartbeat would churn the waiter projection sequence")
	}
	feedback := before.DeepCopy()
	feedback.PullRequests[0].LastReviewID = 41
	if maintainerWorkItemStatusSemanticallyEqual(before, feedback) {
		t.Fatal("new review with unchanged aggregate decision would not advance the waiter projection sequence")
	}
	feedback = before.DeepCopy()
	feedback.PullRequests[0].LastCommentID = 42
	if maintainerWorkItemStatusSemanticallyEqual(before, feedback) {
		t.Fatal("new PR comment would not advance the waiter projection sequence")
	}
}

func TestMaintainerPRProjectionIncludesFeedbackCursors(t *testing.T) {
	monitor := &triggersv1alpha1.PullRequestMonitor{
		ObjectMeta: metav1.ObjectMeta{Name: projectionTestMonitorName},
		Spec:       triggersv1alpha1.PullRequestMonitorSpec{Repository: projectionTestRepository, Number: 7, URL: "https://github.com/octo/widgets/pull/7"},
		Status: triggersv1alpha1.PullRequestMonitorStatus{
			LastReviewCursor:       &triggersv1alpha1.GitHubObjectCursor{ID: 41},
			LastIssueCommentCursor: &triggersv1alpha1.GitHubObjectCursor{ID: 42},
		},
	}

	projection := maintainerPRProjection(monitor)
	if projection.LastReviewID != 41 || projection.LastCommentID != 42 {
		t.Fatalf("feedback cursors = (%d, %d), want (41, 42)", projection.LastReviewID, projection.LastCommentID)
	}
}

func TestEvaluateMaintainerReadinessFailsClosedForHeadBoundCI(t *testing.T) {
	now := time.Now()
	observed := metav1.NewTime(now)
	item := &triggersv1alpha1.MaintainerWorkItem{Spec: triggersv1alpha1.MaintainerWorkItemSpec{Disposition: triggersv1alpha1.MaintainerWorkItemDispositionBounded}, Status: triggersv1alpha1.MaintainerWorkItemStatus{PullRequests: []triggersv1alpha1.MaintainerWorkItemPullRequestProjection{{IntentName: projectionTestMonitorName, Repository: projectionTestRepository, Number: 7, MonitorRef: &coreLocalRef, State: triggersv1alpha1.MaintainerWorkItemPullRequestStateOpen, HeadSHA: "new-head", Mergeable: new(true), ReviewDecision: string(triggersv1alpha1.PullRequestReviewDecisionApproved), CheckState: triggersv1alpha1.MaintainerWorkItemCheckStateUnknown, Fresh: true, HeadObservedAt: &observed, ReviewObservedAt: &observed, ChecksObservedAt: &observed, StatusesObservedAt: &observed}}}}
	evaluateMaintainerReadiness(item, now, false)
	if item.Status.Readiness.ReadyToMerge {
		t.Fatal("head change without fresh head-bound CI was merge-ready")
	}
	item.Status.PullRequests[0].CheckState = triggersv1alpha1.MaintainerWorkItemCheckStatePassing
	evaluateMaintainerReadiness(item, now, false)
	if !item.Status.Readiness.ReadyToMerge || item.Status.Phase != triggersv1alpha1.MaintainerWorkItemPhaseReadyToMerge {
		t.Fatalf("fresh exact-head facts not ready: %#v", item.Status)
	}
	item.Status.PullRequests[0].ReviewDecision = ""
	evaluateMaintainerReadiness(item, now, false)
	if item.Status.Readiness.ReadyToMerge {
		t.Fatal("unapproved pull request was merge-ready without full control")
	}
	evaluateMaintainerReadiness(item, now, true)
	if !item.Status.Readiness.ReadyToMerge {
		t.Fatal("full control still required human approval")
	}
	item.Status.PullRequests[0].CheckState = triggersv1alpha1.MaintainerWorkItemCheckStateNone
	evaluateMaintainerReadiness(item, now, true)
	if item.Status.Readiness.ReadyToMerge {
		t.Fatal("zero-check observation became ready without synchronous policy proof")
	}
	if !projectedPullRequestsReadyForPolicyVerification(item.Status.PullRequests, now) {
		t.Fatal("full-control zero-check candidate could not advance to policy verification")
	}
	evaluateMaintainerReadiness(item, now, false)
	if item.Status.Readiness.ReadyToMerge {
		t.Fatal("no-checks projection bypassed required human approval")
	}
	item.Status.PullRequests[0].ReviewDecision = string(triggersv1alpha1.PullRequestReviewDecisionChangesRequested)
	evaluateMaintainerReadiness(item, now, true)
	if item.Status.Readiness.ReadyToMerge {
		t.Fatal("full control ignored an explicit changes-requested review")
	}
	item.Status.PullRequests[0].ReviewDecision = string(triggersv1alpha1.PullRequestReviewDecisionApproved)
	stale := metav1.NewTime(now.Add(-maintainerProjectionFreshness - time.Second))
	item.Status.PullRequests[0].ChecksObservedAt = &stale
	evaluateMaintainerReadiness(item, now, false)
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

func TestEvaluateMaintainerReadinessRequiresExplicitCompleteGraphProjection(t *testing.T) {
	item := &triggersv1alpha1.MaintainerWorkItem{Spec: triggersv1alpha1.MaintainerWorkItemSpec{Disposition: triggersv1alpha1.MaintainerWorkItemDispositionBounded}}
	evaluateMaintainerReadiness(item, time.Now(), false)
	if item.Status.Readiness.ReadyToDispatch {
		t.Fatal("unreviewed work-item graph became ready")
	}

	item.Spec.GraphConfiguredByCommand = &corev1.LocalObjectReference{Name: "graph-command"}
	item.Spec.Dependencies = []triggersv1alpha1.MaintainerWorkItemReference{{Name: "dependency"}}
	evaluateMaintainerReadiness(item, time.Now(), false)
	if item.Status.Readiness.ReadyToDispatch {
		t.Fatal("dependency-bearing work item became ready before its link was projected")
	}

	item.Status.Dependencies = []triggersv1alpha1.MaintainerWorkItemDependencyProjection{{Name: "dependency", Delivered: false}}
	evaluateMaintainerReadiness(item, time.Now(), false)
	if item.Status.Readiness.ReadyToDispatch {
		t.Fatal("undelivered dependency became ready")
	}
	item.Status.Dependencies[0].Delivered = true
	evaluateMaintainerReadiness(item, time.Now(), false)
	if !item.Status.Readiness.ReadyToDispatch {
		t.Fatalf("explicit graph with delivered dependency did not become ready: %#v", item.Status.Readiness)
	}
}

func TestEvaluateMaintainerReadinessDoesNotRedispatchReservedItem(t *testing.T) {
	item := &triggersv1alpha1.MaintainerWorkItem{Spec: triggersv1alpha1.MaintainerWorkItemSpec{Disposition: triggersv1alpha1.MaintainerWorkItemDispositionBounded}, Status: triggersv1alpha1.MaintainerWorkItemStatus{DispatchReservation: &triggersv1alpha1.MaintainerDispatchReservation{ID: "once"}}}
	evaluateMaintainerReadiness(item, time.Now(), false)
	if item.Status.Readiness.ReadyToDispatch {
		t.Fatal("reserved item remained ready to dispatch")
	}
}

func TestEvaluateMaintainerReadinessBlocksDeliveryOnGraphPrerequisites(t *testing.T) {
	item := &triggersv1alpha1.MaintainerWorkItem{Spec: triggersv1alpha1.MaintainerWorkItemSpec{Disposition: triggersv1alpha1.MaintainerWorkItemDispositionDecomposable}, Status: triggersv1alpha1.MaintainerWorkItemStatus{Children: []triggersv1alpha1.MaintainerWorkItemChildProjection{{Name: "child", Delivered: false}}, Dependencies: []triggersv1alpha1.MaintainerWorkItemDependencyProjection{{Name: "dependency", Delivered: true}}, PullRequests: []triggersv1alpha1.MaintainerWorkItemPullRequestProjection{{IntentName: projectionTestMonitorName, Repository: projectionTestRepository, Number: 7, State: triggersv1alpha1.MaintainerWorkItemPullRequestStateMerged}}}}
	evaluateMaintainerReadiness(item, time.Now(), false)
	if item.Status.Phase == triggersv1alpha1.MaintainerWorkItemPhaseDelivered {
		t.Fatal("undelivered child allowed delivery")
	}
	item.Status.Children[0].Delivered = true
	item.Status.Dependencies[0].Delivered = false
	evaluateMaintainerReadiness(item, time.Now(), false)
	if item.Status.Phase == triggersv1alpha1.MaintainerWorkItemPhaseDelivered {
		t.Fatal("undelivered dependency allowed delivery")
	}
}

func TestEvaluateMaintainerReadinessRequiresFinalizationAfterMerge(t *testing.T) {
	item := &triggersv1alpha1.MaintainerWorkItem{Spec: triggersv1alpha1.MaintainerWorkItemSpec{Disposition: triggersv1alpha1.MaintainerWorkItemDispositionBounded}, Status: triggersv1alpha1.MaintainerWorkItemStatus{PullRequests: []triggersv1alpha1.MaintainerWorkItemPullRequestProjection{{IntentName: projectionTestMonitorName, Repository: projectionTestRepository, Number: 7, State: triggersv1alpha1.MaintainerWorkItemPullRequestStateMerged}}}}
	evaluateMaintainerReadiness(item, time.Now(), false)
	if item.Status.Phase == triggersv1alpha1.MaintainerWorkItemPhaseDelivered || item.Status.Readiness.ReadyToMerge {
		t.Fatalf("merge alone must not deliver = %#v", item.Status)
	}
	now := metav1.Now()
	item.Status.DeliveryAttestation = &triggersv1alpha1.MaintainerDeliveryAttestation{CompletedAt: &now}
	evaluateMaintainerReadiness(item, time.Now(), false)
	if item.Status.Phase != triggersv1alpha1.MaintainerWorkItemPhaseDelivered {
		t.Fatalf("finalized merged item phase = %s", item.Status.Phase)
	}
}

func TestProjectMaintainerPRsIgnoresLegacyRequiredIntents(t *testing.T) {
	// A pre-dispatch intent can never match a generated monitor name, so it must
	// neither hide a real monitor nor add an unsatisfiable placeholder.
	item := &triggersv1alpha1.MaintainerWorkItem{ObjectMeta: metav1.ObjectMeta{Name: "item", UID: types.UID("item-uid")}, Spec: triggersv1alpha1.MaintainerWorkItemSpec{RequiredPullRequests: []triggersv1alpha1.MaintainerRequiredPullRequestIntent{{Name: projectionTestRequired}}}}
	run := platformv1alpha1.AgentRun{ObjectMeta: metav1.ObjectMeta{Name: "implementer", Labels: map[string]string{triggersv1alpha1.MaintainerWorkItemNameLabelKey: item.Name, triggersv1alpha1.MaintainerWorkItemUIDLabelKey: string(item.UID)}}}
	monitors := []triggersv1alpha1.PullRequestMonitor{
		{ObjectMeta: metav1.ObjectMeta{Name: "pr-monitor-aaa"}, Spec: triggersv1alpha1.PullRequestMonitorSpec{Repository: projectionTestRepository, Number: 25, ImplementerRef: corev1.LocalObjectReference{Name: run.Name}}},
		{ObjectMeta: metav1.ObjectMeta{Name: "pr-monitor-bbb"}, Spec: triggersv1alpha1.PullRequestMonitorSpec{Repository: projectionTestRepository, Number: 26, ImplementerRef: corev1.LocalObjectReference{Name: run.Name}}},
		{ObjectMeta: metav1.ObjectMeta{Name: "pr-monitor-other"}, Spec: triggersv1alpha1.PullRequestMonitorSpec{Repository: projectionTestRepository, Number: 99, ImplementerRef: corev1.LocalObjectReference{Name: "another-run"}}},
	}
	projectMaintainerRunsAndPRs(item, []platformv1alpha1.AgentRun{run}, monitors, time.Now())
	if len(item.Status.PullRequests) != 2 {
		t.Fatalf("projected PRs = %#v", item.Status.PullRequests)
	}
	for _, pr := range item.Status.PullRequests {
		if pr.MonitorRef == nil || pr.Number < 1 {
			t.Fatalf("phantom projection %#v", pr)
		}
	}
}

func TestMigrateLegacyMaintainerWorkItemUnblocksPreGateItems(t *testing.T) {
	item := &triggersv1alpha1.MaintainerWorkItem{
		ObjectMeta: metav1.ObjectMeta{Name: "item"},
		Spec: triggersv1alpha1.MaintainerWorkItemSpec{
			Disposition:          triggersv1alpha1.MaintainerWorkItemDispositionBounded,
			TriagedByCommand:     &corev1.LocalObjectReference{Name: "triage-command"},
			RequiredPullRequests: []triggersv1alpha1.MaintainerRequiredPullRequestIntent{{Name: projectionTestRequired}},
		},
	}
	migrateLegacyMaintainerWorkItem(item)
	if item.Spec.GraphConfiguredByCommand == nil || item.Spec.GraphConfiguredByCommand.Name != "triage-command" {
		t.Fatalf("graph configuration = %#v", item.Spec.GraphConfiguredByCommand)
	}
	if item.Spec.RequiredPullRequests != nil {
		t.Fatalf("legacy required pull requests survived migration: %#v", item.Spec.RequiredPullRequests)
	}
	if item.Annotations[triggersv1alpha1.MaintainerWorkItemGraphGateAnnotation] != triggersv1alpha1.MaintainerWorkItemGraphGateVersion {
		t.Fatalf("graph gate annotation = %q", item.Annotations[triggersv1alpha1.MaintainerWorkItemGraphGateAnnotation])
	}
	evaluateMaintainerReadiness(item, time.Now(), false)
	if !item.Status.Readiness.ReadyToDispatch {
		t.Fatalf("migrated item is still not dispatchable: %#v", item.Status.Readiness.UnmetRequirements)
	}
	// A work item already configured under the current gate is never rewritten.
	configured := &triggersv1alpha1.MaintainerWorkItem{
		ObjectMeta: metav1.ObjectMeta{Name: "item", Annotations: map[string]string{triggersv1alpha1.MaintainerWorkItemGraphGateAnnotation: triggersv1alpha1.MaintainerWorkItemGraphGateVersion}},
		Spec:       triggersv1alpha1.MaintainerWorkItemSpec{TriagedByCommand: &corev1.LocalObjectReference{Name: "triage-command"}},
	}
	migrateLegacyMaintainerWorkItem(configured)
	if configured.Spec.GraphConfiguredByCommand != nil {
		t.Fatalf("gated item was migrated: %#v", configured.Spec.GraphConfiguredByCommand)
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

func TestEvaluateMaintainerReadinessIgnoresSupersededPullRequests(t *testing.T) {
	now := metav1.Now()
	monitorRef := corev1.LocalObjectReference{Name: projectionTestMonitorName}
	item := &triggersv1alpha1.MaintainerWorkItem{
		Spec: triggersv1alpha1.MaintainerWorkItemSpec{Disposition: triggersv1alpha1.MaintainerWorkItemDispositionBounded, GraphConfiguredByCommand: &corev1.LocalObjectReference{Name: "graph"}},
		Status: triggersv1alpha1.MaintainerWorkItemStatus{
			DeliveryAttestation: &triggersv1alpha1.MaintainerDeliveryAttestation{CompletedAt: &now},
			PullRequests: []triggersv1alpha1.MaintainerWorkItemPullRequestProjection{
				{IntentName: "pr-monitor-aaa", Repository: projectionTestRepository, Number: 24, MonitorRef: &monitorRef, State: triggersv1alpha1.MaintainerWorkItemPullRequestStateClosed},
				{IntentName: "pr-monitor-bbb", Repository: projectionTestRepository, Number: 25, MonitorRef: &monitorRef, State: triggersv1alpha1.MaintainerWorkItemPullRequestStateMerged},
			},
		},
	}
	evaluateMaintainerReadiness(item, time.Now(), false)
	if item.Status.Phase != triggersv1alpha1.MaintainerWorkItemPhaseDelivered {
		t.Fatalf("phase = %s, unmet = %#v", item.Status.Phase, item.Status.Readiness.UnmetRequirements)
	}
	if item.Status.Readiness.ReadyToMerge {
		t.Fatal("a fully shipped work item must not stay merge-ready")
	}
}
