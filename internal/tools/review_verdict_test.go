package tools

import (
	"context"
	"encoding/json"
	"testing"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func newVerdictTool(t *testing.T) (*reviewVerdictTool, client.Client) {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}
	run := &platformv1alpha1.AgentRun{ObjectMeta: metav1.ObjectMeta{Name: "rev-run", Namespace: "default"}}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run).Build()
	return &reviewVerdictTool{k8sClient: c, taskName: "rev-run", namespace: "default"}, c
}

func TestReviewVerdictToolRecordsAnnotations(t *testing.T) {
	t.Parallel()
	tool, c := newVerdictTool(t)

	res, err := tool.Execute(context.Background(), json.RawMessage(`{"verdict":"request_changes","summary":"HIGH: race in retry loop"}`), "")
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if res.IsError {
		t.Fatalf("Execute() result error: %s", res.Content)
	}

	run := &platformv1alpha1.AgentRun{}
	if err := c.Get(context.Background(), client.ObjectKey{Name: "rev-run", Namespace: "default"}, run); err != nil {
		t.Fatalf("Get(run): %v", err)
	}
	if got := run.Annotations[platformv1alpha1.ReviewVerdictAnnotation]; got != platformv1alpha1.ReviewVerdictRequestChanges {
		t.Fatalf("verdict annotation = %q, want request_changes", got)
	}
	if got := run.Annotations[platformv1alpha1.ReviewSummaryAnnotation]; got != "HIGH: race in retry loop" {
		t.Fatalf("summary annotation = %q", got)
	}
}

func TestReviewVerdictToolRejectsInvalidInput(t *testing.T) {
	t.Parallel()
	tool, _ := newVerdictTool(t)

	cases := []string{
		`{"verdict":"merge","summary":"x"}`,
		`{"verdict":"approve","summary":""}`,
		`{"verdict":"approve"`,
	}
	for _, in := range cases {
		res, err := tool.Execute(context.Background(), json.RawMessage(in), "")
		if err != nil {
			t.Fatalf("Execute(%s) transport error = %v", in, err)
		}
		if !res.IsError {
			t.Fatalf("Execute(%s) = %q, want tool error", in, res.Content)
		}
	}
}
