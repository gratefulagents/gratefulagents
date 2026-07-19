package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func newDisplayNameTool(t *testing.T) (*setDisplayNameTool, client.Client) {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}
	run := &platformv1alpha1.AgentRun{ObjectMeta: metav1.ObjectMeta{Name: "dn-run", Namespace: "default"}}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&platformv1alpha1.AgentRun{}).
		WithObjects(run).
		Build()
	return &setDisplayNameTool{k8sClient: c, taskName: "dn-run", namespace: "default"}, c
}

func TestSetDisplayNameToolSetsStatus(t *testing.T) {
	t.Parallel()
	tool, c := newDisplayNameTool(t)

	res, err := tool.Execute(context.Background(), json.RawMessage(`{"display_name":"  Fix retry race  "}`), "")
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if res.IsError {
		t.Fatalf("Execute() result error: %s", res.Content)
	}

	run := &platformv1alpha1.AgentRun{}
	if err := c.Get(context.Background(), client.ObjectKey{Name: "dn-run", Namespace: "default"}, run); err != nil {
		t.Fatalf("Get(run): %v", err)
	}
	if got := run.Status.DisplayName; got != "Fix retry race" {
		t.Fatalf("status.displayName = %q, want %q", got, "Fix retry race")
	}
}

func TestSetDisplayNameToolCapsLength(t *testing.T) {
	t.Parallel()
	tool, c := newDisplayNameTool(t)

	long := strings.Repeat("a", maxDisplayNameLen+50)
	in, _ := json.Marshal(setDisplayNameInput{DisplayName: long})
	res, err := tool.Execute(context.Background(), in, "")
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if res.IsError {
		t.Fatalf("Execute() result error: %s", res.Content)
	}

	run := &platformv1alpha1.AgentRun{}
	if err := c.Get(context.Background(), client.ObjectKey{Name: "dn-run", Namespace: "default"}, run); err != nil {
		t.Fatalf("Get(run): %v", err)
	}
	if got := len(run.Status.DisplayName); got > maxDisplayNameLen {
		t.Fatalf("status.displayName length = %d, want <= %d", got, maxDisplayNameLen)
	}
}

func TestSetDisplayNameToolKeepsExistingDisplayName(t *testing.T) {
	t.Parallel()
	tool, c := newDisplayNameTool(t)

	res, err := tool.Execute(context.Background(), json.RawMessage(`{"display_name":"Original title"}`), "")
	if err != nil {
		t.Fatalf("first Execute() error = %v", err)
	}
	if res.IsError {
		t.Fatalf("first Execute() result error: %s", res.Content)
	}

	res, err = tool.Execute(context.Background(), json.RawMessage(`{"display_name":"Updated title"}`), "")
	if err != nil {
		t.Fatalf("second Execute() error = %v", err)
	}
	if res.IsError {
		t.Fatalf("second Execute() = %q, want idempotent no-op, not a tool error", res.Content)
	}
	if got, want := res.Content, `Display name already set to "Original title"; keeping it (no action needed).`; got != want {
		t.Fatalf("second Execute() content = %q, want %q", got, want)
	}

	run := &platformv1alpha1.AgentRun{}
	if err := c.Get(context.Background(), client.ObjectKey{Name: "dn-run", Namespace: "default"}, run); err != nil {
		t.Fatalf("Get(run): %v", err)
	}
	if got := run.Status.DisplayName; got != "Original title" {
		t.Fatalf("status.displayName = %q, want %q", got, "Original title")
	}
}

func TestSetDisplayNameToolRejectsEmpty(t *testing.T) {
	t.Parallel()
	tool, _ := newDisplayNameTool(t)

	for _, in := range []string{`{"display_name":""}`, `{"display_name":"   "}`, `{"display_name"`} {
		res, err := tool.Execute(context.Background(), json.RawMessage(in), "")
		if err != nil {
			t.Fatalf("Execute(%s) transport error = %v", in, err)
		}
		if !res.IsError {
			t.Fatalf("Execute(%s) = %q, want tool error", in, res.Content)
		}
	}
}
