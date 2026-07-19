package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func newOverseerVerdictTool(t *testing.T) (*overseerVerdictTool, client.Client) {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}
	run := &platformv1alpha1.AgentRun{ObjectMeta: metav1.ObjectMeta{Name: "overseer", Namespace: "default"}}
	k8sClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run).Build()
	return &overseerVerdictTool{k8sClient: k8sClient, taskName: run.Name, namespace: run.Namespace}, k8sClient
}

func TestOverseerVerdictToolRecordsStructuredVerdict(t *testing.T) {
	t.Parallel()
	tool, k8sClient := newOverseerVerdictTool(t)

	result, err := tool.Execute(context.Background(), json.RawMessage(`{"verdict":" STEER ","summary":"The run is looping.","guidance":"Use the existing parser instead of retrying the regex."}`), "")
	if err != nil || result.IsError {
		t.Fatalf("Execute() = (%#v, %v), want success", result, err)
	}
	run := &platformv1alpha1.AgentRun{}
	if err := k8sClient.Get(context.Background(), client.ObjectKey{Name: "overseer", Namespace: "default"}, run); err != nil {
		t.Fatal(err)
	}
	if got := run.Annotations[platformv1alpha1.OverseerVerdictAnnotation]; got != platformv1alpha1.OverseerVerdictSteer {
		t.Fatalf("verdict = %q", got)
	}
	if got := run.Annotations[platformv1alpha1.OverseerSummaryAnnotation]; got != "The run is looping." {
		t.Fatalf("summary = %q", got)
	}
	if got := run.Annotations[platformv1alpha1.OverseerGuidanceAnnotation]; got != "Use the existing parser instead of retrying the regex." {
		t.Fatalf("guidance = %q", got)
	}
}

func TestOverseerVerdictToolValidatesGuidance(t *testing.T) {
	t.Parallel()
	tool, _ := newOverseerVerdictTool(t)
	for _, input := range []string{
		`{"verdict":"steer","summary":"drift"}`,
		`{"verdict":"reject_completion","summary":"tests missing","guidance":""}`,
		`{"verdict":"merge","summary":"invalid"}`,
		`{"verdict":"all_clear","summary":""}`,
		`{`,
	} {
		result, err := tool.Execute(context.Background(), json.RawMessage(input), "")
		if err != nil {
			t.Fatalf("Execute(%q) transport error: %v", input, err)
		}
		if !result.IsError {
			t.Fatalf("Execute(%q) succeeded: %#v", input, result)
		}
	}
}

func TestOverseerVerdictToolTruncatesUTF8Safely(t *testing.T) {
	t.Parallel()
	tool, k8sClient := newOverseerVerdictTool(t)
	long := strings.Repeat("界", 2000)
	input, err := json.Marshal(overseerVerdictInput{Verdict: platformv1alpha1.OverseerVerdictAllClear, Summary: long})
	if err != nil {
		t.Fatal(err)
	}
	result, err := tool.Execute(context.Background(), input, "")
	if err != nil || result.IsError {
		t.Fatalf("Execute() = (%#v, %v)", result, err)
	}
	run := &platformv1alpha1.AgentRun{}
	if err := k8sClient.Get(context.Background(), client.ObjectKey{Name: "overseer", Namespace: "default"}, run); err != nil {
		t.Fatal(err)
	}
	summary := run.Annotations[platformv1alpha1.OverseerSummaryAnnotation]
	if len(summary) > 4000 || !utf8.ValidString(summary) {
		t.Fatalf("truncated summary bytes=%d valid=%v", len(summary), utf8.ValidString(summary))
	}
}

func TestOverseerVerdictToolRecordsResolveInput(t *testing.T) {
	t.Parallel()
	tool, k8sClient := newOverseerVerdictTool(t)

	result, err := tool.Execute(context.Background(), json.RawMessage(`{"verdict":"resolve_input","summary":"Input supplied.","request_id":" request-1 ","action_id":" approve ","response":" proceed "}`), "")
	if err != nil || result.IsError {
		t.Fatalf("Execute() = (%#v, %v), want success", result, err)
	}
	run := &platformv1alpha1.AgentRun{}
	if err := k8sClient.Get(context.Background(), client.ObjectKey{Name: "overseer", Namespace: "default"}, run); err != nil {
		t.Fatal(err)
	}
	var response platformv1alpha1.OverseerInputResponse
	if err := json.Unmarshal([]byte(run.Annotations[platformv1alpha1.OverseerInputResponseAnnotation]), &response); err != nil {
		t.Fatalf("input response annotation: %v", err)
	}
	want := platformv1alpha1.OverseerInputResponse{RequestID: "request-1", ActionID: "approve", Response: "proceed"}
	if response != want {
		t.Fatalf("input response = %#v, want %#v", response, want)
	}
	if _, ok := run.Annotations[platformv1alpha1.OverseerGuidanceAnnotation]; ok {
		t.Fatal("guidance annotation should be absent")
	}
}

func TestOverseerVerdictToolValidatesResolveInput(t *testing.T) {
	t.Parallel()
	tool, _ := newOverseerVerdictTool(t)
	for _, input := range []string{
		`{"verdict":"resolve_input","summary":"answer","action_id":"approve"}`,
		`{"verdict":"resolve_input","summary":"answer","request_id":"request-1"}`,
		`{"verdict":"resolve_input","summary":"","request_id":"request-1","response":"answer"}`,
	} {
		result, err := tool.Execute(context.Background(), json.RawMessage(input), "")
		if err != nil {
			t.Fatalf("Execute(%q) transport error: %v", input, err)
		}
		if !result.IsError {
			t.Fatalf("Execute(%q) succeeded: %#v", input, result)
		}
	}
}

func TestOverseerVerdictToolClearsStaleInputResponse(t *testing.T) {
	t.Parallel()
	tool, k8sClient := newOverseerVerdictTool(t)
	run := &platformv1alpha1.AgentRun{}
	key := client.ObjectKey{Name: "overseer", Namespace: "default"}
	if err := k8sClient.Get(context.Background(), key, run); err != nil {
		t.Fatal(err)
	}
	run.Annotations = map[string]string{
		platformv1alpha1.OverseerInputResponseAnnotation: `{"request_id":"stale","response":"stale"}`,
		platformv1alpha1.OverseerGuidanceAnnotation:      "stale",
	}
	if err := k8sClient.Update(context.Background(), run); err != nil {
		t.Fatal(err)
	}

	result, err := tool.Execute(context.Background(), json.RawMessage(`{"verdict":"all_clear","summary":"Done."}`), "")
	if err != nil || result.IsError {
		t.Fatalf("Execute() = (%#v, %v), want success", result, err)
	}
	if err := k8sClient.Get(context.Background(), key, run); err != nil {
		t.Fatal(err)
	}
	if _, ok := run.Annotations[platformv1alpha1.OverseerInputResponseAnnotation]; ok {
		t.Fatal("input response annotation should be absent")
	}
	if _, ok := run.Annotations[platformv1alpha1.OverseerGuidanceAnnotation]; ok {
		t.Fatal("guidance annotation should be absent")
	}
}
