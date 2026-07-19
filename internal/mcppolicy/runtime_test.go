package mcppolicy

import (
	"testing"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestEvaluatorExplicitToolSubsetOverridesDefaultAllow(t *testing.T) {
	t.Parallel()

	policy := &platformv1alpha1.MCPPolicy{
		Spec: platformv1alpha1.MCPPolicySpec{
			DefaultAction: platformv1alpha1.MCPDefaultActionAllow,
			AllowedServers: []platformv1alpha1.MCPAllowedServer{
				{Name: "github", Tools: []string{"get_issue"}},
			},
		},
	}

	evaluator := NewEvaluator(nil, policy)
	if !evaluator.AllowsTool("github", "get_issue") {
		t.Fatal("AllowsTool(github, get_issue) = false, want true")
	}
	if evaluator.AllowsTool("github", "create_issue") {
		t.Fatal("AllowsTool(github, create_issue) = true, want false")
	}
	if !evaluator.AllowsTool("context7", "search") {
		t.Fatal("AllowsTool(context7, search) = false, want true via default allow")
	}
}

func TestEvaluatorGrantedBreakGlassOverridesDeny(t *testing.T) {
	t.Parallel()

	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{},
		},
	}
	if err := SetGrantedGrants(run.Annotations, []BreakGlassGrant{{
		Server: "github",
		Tool:   "create_issue",
		Reason: "Need to open the tracked issue",
	}}); err != nil {
		t.Fatalf("SetGrantedGrants() error = %v", err)
	}
	policy := &platformv1alpha1.MCPPolicy{
		Spec: platformv1alpha1.MCPPolicySpec{
			DefaultAction: platformv1alpha1.MCPDefaultActionDeny,
			BreakGlass:    &platformv1alpha1.MCPBreakGlass{Enabled: true},
		},
	}

	evaluator := NewEvaluator(run, policy)
	if !evaluator.AllowsTool("github", "create_issue") {
		t.Fatal("AllowsTool(github, create_issue) = false, want true from grant")
	}
	if evaluator.AllowsTool("github", "delete_issue") {
		t.Fatal("AllowsTool(github, delete_issue) = true, want false")
	}
}

func TestPendingRequestRoundTrip(t *testing.T) {
	t.Parallel()

	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{},
		},
	}
	want := BreakGlassRequest{
		Server:      "github",
		Tool:        "create_issue",
		Reason:      "Need to create the issue",
		RequestedAt: "2026-04-19T12:00:00Z",
	}
	if err := SetPendingRequest(run.Annotations, want); err != nil {
		t.Fatalf("SetPendingRequest() error = %v", err)
	}

	got, err := PendingRequest(run)
	if err != nil {
		t.Fatalf("PendingRequest() error = %v", err)
	}
	if got == nil {
		t.Fatal("PendingRequest() = nil, want request")
	}
	if *got != want {
		t.Fatalf("PendingRequest() = %#v, want %#v", *got, want)
	}

	ClearPendingRequest(run.Annotations)
	got, err = PendingRequest(run)
	if err != nil {
		t.Fatalf("PendingRequest() after clear error = %v", err)
	}
	if got != nil {
		t.Fatalf("PendingRequest() after clear = %#v, want nil", *got)
	}
}
