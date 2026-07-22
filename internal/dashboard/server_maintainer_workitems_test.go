package dashboard

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
)

func workItem(name, repo string, issue int32, phase triggersv1alpha1.MaintainerWorkItemPhase) *triggersv1alpha1.MaintainerWorkItem {
	return &triggersv1alpha1.MaintainerWorkItem{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec: triggersv1alpha1.MaintainerWorkItemSpec{
			RepositoryRef: corev1.LocalObjectReference{Name: repo},
			IssueNumber:   issue,
		},
		Status: triggersv1alpha1.MaintainerWorkItemStatus{Phase: phase},
	}
}

func newWorkItemTestServer(t *testing.T, items ...*triggersv1alpha1.MaintainerWorkItem) (*Server, *mockStateStore) {
	t.Helper()
	scheme := testProjectScheme(t)
	builder := fake.NewClientBuilder().WithScheme(scheme)
	for _, item := range items {
		builder = builder.WithObjects(item)
	}
	ms := newMockStateStore()
	return &Server{k8sClient: builder.Build(), scheme: scheme, stateStore: ms}, ms
}

func TestListMaintainerWorkItemsFiltersAndSorts(t *testing.T) {
	srv, _ := newWorkItemTestServer(t,
		workItem("acme-wi-11", "acme", 11, triggersv1alpha1.MaintainerWorkItemPhaseDelivered),
		workItem("acme-wi-12", "acme", 12, triggersv1alpha1.MaintainerWorkItemPhaseImplementing),
		workItem("acme-wi-13", "acme", 13, triggersv1alpha1.MaintainerWorkItemPhaseAwaitingDecision),
		workItem("other-wi-9", "other", 9, triggersv1alpha1.MaintainerWorkItemPhaseImplementing),
	)

	resp, err := srv.ListMaintainerWorkItems(context.Background(), &platform.ListMaintainerWorkItemsRequest{
		Namespace:      "default",
		RepositoryName: "acme",
	})
	if err != nil {
		t.Fatalf("ListMaintainerWorkItems: %v", err)
	}
	if len(resp.Items) != 3 {
		t.Fatalf("want 3 items for acme, got %d", len(resp.Items))
	}
	wantOrder := []string{"acme-wi-13", "acme-wi-12", "acme-wi-11"}
	for i, want := range wantOrder {
		if resp.Items[i].Name != want {
			t.Errorf("items[%d] = %q, want %q (active phases first)", i, resp.Items[i].Name, want)
		}
	}
}

func TestListMaintainerWorkItemsRequiresRepositoryAccess(t *testing.T) {
	srv, ms := newWorkItemTestServer(t, workItem("acme-wi-1", "acme", 1, triggersv1alpha1.MaintainerWorkItemPhaseTriaged))
	if err := ms.SetResourceOwner(context.Background(), githubRepositoryResourceType, "acme", "default", "alice"); err != nil {
		t.Fatalf("SetResourceOwner: %v", err)
	}

	req := &platform.ListMaintainerWorkItemsRequest{Namespace: "default", RepositoryName: "acme"}
	if _, err := srv.ListMaintainerWorkItems(actorContext("mallory", "member", "", ""), req); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("stranger listing work items: want PermissionDenied, got %v", err)
	}
	if _, err := srv.ListMaintainerWorkItems(actorContext("alice", "member", "", ""), req); err != nil {
		t.Fatalf("owner listing work items: %v", err)
	}
}

func TestListMaintainerWorkItemsValidatesRequest(t *testing.T) {
	srv, _ := newWorkItemTestServer(t)
	if _, err := srv.ListMaintainerWorkItems(context.Background(), &platform.ListMaintainerWorkItemsRequest{Namespace: "default"}); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("missing repository_name: want InvalidArgument, got %v", err)
	}
	if _, err := srv.ListMaintainerWorkItems(context.Background(), &platform.ListMaintainerWorkItemsRequest{RepositoryName: "acme"}); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("missing namespace: want InvalidArgument, got %v", err)
	}
}

func TestMaintainerWorkItemToProtoMapsStatus(t *testing.T) {
	now := metav1.NewTime(time.Unix(1_760_000_000, 0))
	closeReason := triggersv1alpha1.MaintainerWorkItemCloseReasonNotPlanned
	item := &triggersv1alpha1.MaintainerWorkItem{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "acme-wi-42",
			Namespace:         "default",
			CreationTimestamp: now,
		},
		Spec: triggersv1alpha1.MaintainerWorkItemSpec{
			RepositoryRef:   corev1.LocalObjectReference{Name: "acme"},
			IssueNumber:     42,
			Disposition:     triggersv1alpha1.MaintainerWorkItemDispositionBounded,
			CloseReason:     &closeReason,
			EvidenceSummary: "Reproduced the bug",
			Children:        []triggersv1alpha1.MaintainerWorkItemReference{{Name: "child-a"}},
		},
		Status: triggersv1alpha1.MaintainerWorkItemStatus{
			Phase: triggersv1alpha1.MaintainerWorkItemPhaseReadyToMerge,
			IssueObservation: &triggersv1alpha1.MaintainerIssueObservation{
				Number: 42,
				URL:    "https://github.com/acme/payments/issues/42",
				Title:  "Fix login bug",
				State:  triggersv1alpha1.MaintainerIssueStateOpen,
			},
			Readiness: &triggersv1alpha1.MaintainerWorkItemReadiness{
				ReadyToMerge:      true,
				UnmetRequirements: []string{"checks pending"},
			},
			PendingDecision: &triggersv1alpha1.MaintainerPendingDecision{
				ID:          "d-1",
				Question:    "Merge now?",
				Options:     []string{"yes", "no"},
				RequestedAt: now,
			},
			AgentRuns: []triggersv1alpha1.MaintainerWorkItemAgentRunProjection{{
				Name:        "acme-wi-42-impl",
				Role:        triggersv1alpha1.MaintainerWorkItemAgentRunRole("implementer"),
				Phase:       "Running",
				PRLoopState: "reviewing",
			}},
			PullRequests: []triggersv1alpha1.MaintainerWorkItemPullRequestProjection{{
				IntentName:     "pr-1",
				Repository:     "acme/payments",
				Number:         77,
				URL:            "https://github.com/acme/payments/pull/77",
				State:          triggersv1alpha1.MaintainerWorkItemPullRequestStateOpen,
				CheckState:     triggersv1alpha1.MaintainerWorkItemCheckStatePassing,
				ReviewDecision: "APPROVED",
			}},
			Children: []triggersv1alpha1.MaintainerWorkItemChildProjection{
				{Name: "child-a", Delivered: true},
				{Name: "child-b"},
			},
			Dependencies: []triggersv1alpha1.MaintainerWorkItemDependencyProjection{
				{Name: "dep-a", Delivered: true},
			},
			DeliveryAttestation: &triggersv1alpha1.MaintainerDeliveryAttestation{
				DeliverySummary: "Shipped",
				CompletedAt:     &now,
			},
			LatestCommand: &triggersv1alpha1.MaintainerWorkItemCommandObservation{
				Name:    "cmd-1",
				Type:    triggersv1alpha1.MaintainerWorkItemCommandTypeRequestMerge,
				Phase:   triggersv1alpha1.MaintainerWorkItemCommandPhaseRejected,
				Message: "capacity exhausted",
			},
		},
	}

	pb := maintainerWorkItemToProto(item)
	if pb.IssueNumber != 42 || pb.IssueTitle != "Fix login bug" || pb.IssueState != "open" {
		t.Errorf("issue mapping wrong: %+v", pb)
	}
	if pb.IssueUrl != "https://github.com/acme/payments/issues/42" {
		t.Errorf("IssueUrl = %q", pb.IssueUrl)
	}
	if pb.Phase != "ReadyToMerge" || pb.Disposition != "Bounded" || pb.CloseReason != "not_planned" {
		t.Errorf("phase/disposition mapping wrong: %+v", pb)
	}
	if !pb.ReadyToMerge || pb.ReadyToDispatch || len(pb.UnmetRequirements) != 1 {
		t.Errorf("readiness mapping wrong: %+v", pb)
	}
	if pb.PendingDecision == nil || pb.PendingDecision.Question != "Merge now?" || len(pb.PendingDecision.Options) != 2 {
		t.Errorf("pending decision mapping wrong: %+v", pb.PendingDecision)
	}
	if len(pb.AgentRuns) != 1 || pb.AgentRuns[0].Role != "implementer" || pb.AgentRuns[0].PrLoopState != "reviewing" {
		t.Errorf("agent run mapping wrong: %+v", pb.AgentRuns)
	}
	if len(pb.PullRequests) != 1 || pb.PullRequests[0].Number != 77 || pb.PullRequests[0].CheckState != "Passing" {
		t.Errorf("pull request mapping wrong: %+v", pb.PullRequests)
	}
	if pb.ChildrenTotal != 2 || pb.ChildrenDelivered != 1 {
		t.Errorf("children counts wrong: total=%d delivered=%d", pb.ChildrenTotal, pb.ChildrenDelivered)
	}
	if pb.DependenciesTotal != 1 || pb.DependenciesDelivered != 1 {
		t.Errorf("dependency counts wrong: total=%d delivered=%d", pb.DependenciesTotal, pb.DependenciesDelivered)
	}
	if pb.DeliverySummary != "Shipped" || pb.DeliveredAtUnix != now.Unix() {
		t.Errorf("delivery mapping wrong: %+v", pb)
	}
	if pb.LatestCommandType != "RequestMerge" || pb.LatestCommandPhase != "Rejected" || pb.LatestCommandMessage != "capacity exhausted" {
		t.Errorf("latest command mapping wrong: %+v", pb)
	}
	if pb.CreatedAtUnix != now.Unix() {
		t.Errorf("CreatedAtUnix = %d, want %d", pb.CreatedAtUnix, now.Unix())
	}

	// Empty status phase defaults to PendingTriage.
	bare := workItem("bare", "acme", 1, "")
	if got := maintainerWorkItemToProto(bare).Phase; got != "PendingTriage" {
		t.Errorf("empty phase = %q, want PendingTriage", got)
	}
}
