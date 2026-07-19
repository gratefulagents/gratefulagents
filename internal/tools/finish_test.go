package tools

import (
	"context"
	"testing"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func newFinishSink(t *testing.T) (agentRunFinishSink, *FinishSummaryHolder, client.Client) {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}
	run := &platformv1alpha1.AgentRun{ObjectMeta: metav1.ObjectMeta{Name: "fin-run", Namespace: "default"}}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&platformv1alpha1.AgentRun{}).
		WithObjects(run).
		Build()
	holder := &FinishSummaryHolder{}
	return agentRunFinishSink{k8sClient: c, taskName: "fin-run", namespace: "default", holder: holder}, holder, c
}

func TestFinishSinkCapturesSummaryAndCompletes(t *testing.T) {
	t.Parallel()
	sink, holder, c := newFinishSink(t)

	if err := sink.Finish(context.Background(), "  All weather gathered.  "); err != nil {
		t.Fatalf("Finish() error = %v", err)
	}

	if got := holder.Summary(); got != "All weather gathered." {
		t.Fatalf("holder.Summary() = %q, want %q", got, "All weather gathered.")
	}

	run := &platformv1alpha1.AgentRun{}
	if err := c.Get(context.Background(), client.ObjectKey{Name: "fin-run", Namespace: "default"}, run); err != nil {
		t.Fatalf("Get(run): %v", err)
	}
	if !run.Status.CompletionRequested {
		t.Fatal("expected CompletionRequested to be true")
	}
}

func TestFinishSummaryHolderEmptyByDefault(t *testing.T) {
	t.Parallel()
	holder := &FinishSummaryHolder{}
	if got := holder.Summary(); got != "" {
		t.Fatalf("Summary() = %q, want empty", got)
	}
}
