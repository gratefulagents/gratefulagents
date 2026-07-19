package dashboard

import (
	"context"
	"slices"
	"testing"
	"time"

	"connectrpc.com/connect"
	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/store"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
	"google.golang.org/protobuf/types/known/timestamppb"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestGetObservabilityOverviewScopesQueryToVisibleRuns(t *testing.T) {
	scheme := newDashboardTestScheme(t)
	runA := &platformv1alpha1.AgentRun{ObjectMeta: metav1.ObjectMeta{Name: "run-a", Namespace: "default"}}
	runB := &platformv1alpha1.AgentRun{ObjectMeta: metav1.ObjectMeta{Name: "run-b", Namespace: "default"}}
	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(runA, runB).Build()
	state := newMockStateStore()
	if err := state.SetResourceOwner(context.Background(), "agent_run", "run-a", "default", "alice"); err != nil {
		t.Fatalf("own run-a: %v", err)
	}
	if err := state.SetResourceOwner(context.Background(), "agent_run", "run-b", "default", "bob"); err != nil {
		t.Fatalf("own run-b: %v", err)
	}
	state.observabilityResult = &store.ObservabilityOverview{Totals: store.ObservabilityTotals{Runs: 1}}
	srv := &Server{k8sClient: client, scheme: scheme, stateStore: state}

	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	resp, err := srv.GetObservabilityOverview(actorContext("alice", "member", "", ""), &platform.GetObservabilityOverviewRequest{
		Namespace: "default", Start: timestamppb.New(start), End: timestamppb.New(start.Add(24 * time.Hour)), BucketSeconds: 3600,
	})
	if err != nil {
		t.Fatalf("GetObservabilityOverview() error = %v", err)
	}
	if resp.GetTotals().GetRuns() != 1 {
		t.Fatalf("response runs = %d, want 1", resp.GetTotals().GetRuns())
	}
	if state.observabilityQuery.Namespace != "default" || !state.observabilityQuery.Start.Equal(start) || state.observabilityQuery.BucketSeconds != 3600 {
		t.Fatalf("query metadata = %+v", state.observabilityQuery)
	}
	slices.Sort(state.observabilityQuery.AgentRunNames)
	if !slices.Equal(state.observabilityQuery.AgentRunNames, []string{"run-a"}) {
		t.Fatalf("query run names = %v, want only alice's visible run", state.observabilityQuery.AgentRunNames)
	}
}

func TestGetObservabilityOverviewValidatesResourceBounds(t *testing.T) {
	scheme := newDashboardTestScheme(t)
	srv := &Server{k8sClient: fake.NewClientBuilder().WithScheme(scheme).Build(), scheme: scheme, stateStore: newMockStateStore()}
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	tests := []struct {
		name   string
		start  *timestamppb.Timestamp
		end    *timestamppb.Timestamp
		bucket int64
	}{
		{name: "missing timestamps", bucket: 60},
		{name: "non-positive range", start: timestamppb.New(start), end: timestamppb.New(start), bucket: 60},
		{name: "range over 90 days", start: timestamppb.New(start), end: timestamppb.New(start.Add(90*24*time.Hour + time.Second)), bucket: 86400},
		{name: "bucket below one minute", start: timestamppb.New(start), end: timestamppb.New(start.Add(time.Hour)), bucket: 59},
		{name: "bucket above maximum range", start: timestamppb.New(start), end: timestamppb.New(start.Add(time.Hour)), bucket: observabilityMaxRangeSeconds + 1},
		{name: "too many buckets", start: timestamppb.New(start), end: timestamppb.New(start.Add(2001 * time.Minute)), bucket: 60},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := srv.GetObservabilityOverview(context.Background(), &platform.GetObservabilityOverviewRequest{
				Namespace: "default", Start: tc.start, End: tc.end, BucketSeconds: tc.bucket,
			})
			if connect.CodeOf(err) != connect.CodeInvalidArgument {
				t.Fatalf("code = %v, error = %v", connect.CodeOf(err), err)
			}
		})
	}

	_, err := srv.GetObservabilityOverview(context.Background(), &platform.GetObservabilityOverviewRequest{
		Namespace: "default", Start: timestamppb.New(start), End: timestamppb.New(start.Add(90 * 24 * time.Hour)), BucketSeconds: 86400,
	})
	if err != nil {
		t.Fatalf("exact 90-day range should be accepted: %v", err)
	}
}
