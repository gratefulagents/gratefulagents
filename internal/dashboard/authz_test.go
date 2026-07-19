package dashboard

import (
	"context"
	"net/http"
	"testing"

	"connectrpc.com/connect"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
)

func newAuthzTestServer(t *testing.T, runs ...*platformv1alpha1.AgentRun) (*Server, *mockStateStore) {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(platform): %v", err)
	}
	builder := fake.NewClientBuilder().WithScheme(scheme)
	for _, run := range runs {
		builder = builder.WithObjects(run)
	}
	ms := newMockStateStore()
	return &Server{k8sClient: builder.Build(), scheme: scheme, stateStore: ms}, ms
}

func ownedRun(name string) *platformv1alpha1.AgentRun {
	return &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Status:     platformv1alpha1.AgentRunStatus{Phase: platformv1alpha1.AgentRunPhaseRunning},
	}
}

func TestGetAgentRunDeniedForStrangerOnOwnedRun(t *testing.T) {
	srv, ms := newAuthzTestServer(t, ownedRun("run-owned"))
	if err := ms.SetResourceOwner(context.Background(), "agent_run", "run-owned", "default", "alice"); err != nil {
		t.Fatalf("SetResourceOwner: %v", err)
	}

	_, err := srv.GetAgentRun(actorContext("mallory", "member", "", ""), &platform.GetAgentRunRequest{Namespace: "default", Name: "run-owned"})
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("GetAgentRun by stranger: want PermissionDenied, got %v", err)
	}

	// Owner is allowed.
	if _, err := srv.GetAgentRun(actorContext("alice", "member", "", ""), &platform.GetAgentRunRequest{Namespace: "default", Name: "run-owned"}); err != nil {
		t.Fatalf("GetAgentRun by owner: %v", err)
	}

	// Admin is allowed.
	if _, err := srv.GetAgentRun(actorContext("root", "admin", "", ""), &platform.GetAgentRunRequest{Namespace: "default", Name: "run-owned"}); err != nil {
		t.Fatalf("GetAgentRun by admin: %v", err)
	}
}

func TestGetAgentRunAllowsUnownedRunForAuthenticatedUser(t *testing.T) {
	srv, _ := newAuthzTestServer(t, ownedRun("run-trigger"))
	if _, err := srv.GetAgentRun(actorContext("bob", "member", "", ""), &platform.GetAgentRunRequest{Namespace: "default", Name: "run-trigger"}); err != nil {
		t.Fatalf("GetAgentRun on unowned run: %v", err)
	}
}

func TestGetAgentRunUnauthenticatedActorDenied(t *testing.T) {
	srv, _ := newAuthzTestServer(t, ownedRun("run-x"))
	// Simulate a request that passed through the RPC interceptor without
	// verified claims (empty actor recorded).
	ctx := withRequestActor(context.Background(), http.Header{})
	_, err := srv.GetAgentRun(ctx, &platform.GetAgentRunRequest{Namespace: "default", Name: "run-x"})
	if connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("want Unauthenticated, got %v", err)
	}
}

func TestGetAgentRunInternalCallAllowed(t *testing.T) {
	srv, ms := newAuthzTestServer(t, ownedRun("run-owned"))
	if err := ms.SetResourceOwner(context.Background(), "agent_run", "run-owned", "default", "alice"); err != nil {
		t.Fatalf("SetResourceOwner: %v", err)
	}
	// Bare context (no actor recorded) — internal invocation path.
	if _, err := srv.GetAgentRun(context.Background(), &platform.GetAgentRunRequest{Namespace: "default", Name: "run-owned"}); err != nil {
		t.Fatalf("internal GetAgentRun: %v", err)
	}
}

func TestSendAgentRunMessageDeniedForStranger(t *testing.T) {
	srv, ms := newAuthzTestServer(t, ownedRun("run-owned"))
	if err := ms.SetResourceOwner(context.Background(), "agent_run", "run-owned", "default", "alice"); err != nil {
		t.Fatalf("SetResourceOwner: %v", err)
	}
	_, err := srv.SendAgentRunMessage(actorContext("mallory", "member", "", ""), &platform.SendAgentRunMessageRequest{
		Namespace: "default", Name: "run-owned", Message: "do something",
	})
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("want PermissionDenied, got %v", err)
	}
}

func TestSwitchAgentRunModeSystemSourceDoesNotEscalate(t *testing.T) {
	srv, ms := newAuthzTestServer(t, ownedRun("run-owned"))
	if err := ms.SetResourceOwner(context.Background(), "agent_run", "run-owned", "default", "alice"); err != nil {
		t.Fatalf("SetResourceOwner: %v", err)
	}
	// A member who does not own the run cannot switch modes even when
	// claiming a "system" source.
	_, err := srv.SwitchAgentRunMode(actorContext("mallory", "member", "", ""), &platform.SwitchAgentRunModeRequest{
		Namespace: "default", Name: "run-owned", TargetMode: "deep", Source: "system",
	})
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("want PermissionDenied, got %v", err)
	}
}

func TestActorModeRoleMapping(t *testing.T) {
	cases := []struct {
		role string
		want string
	}{
		{"admin", "admin"},
		{"owner", "admin"},
		{"member", "member"},
		{"viewer", "viewer"},
		{"", "viewer"},
	}
	for _, tc := range cases {
		got := actorModeRole(actorContext("u", tc.role, "", ""))
		if string(got) != tc.want {
			t.Errorf("actorModeRole(%q) = %q, want %q", tc.role, got, tc.want)
		}
	}
	// Internal calls (no actor recorded) keep admin semantics.
	if got := actorModeRole(context.Background()); string(got) != "admin" {
		t.Errorf("actorModeRole(internal) = %q, want admin", got)
	}
}

func TestTriggerOwnedRunInheritsVisibilityAndMutationAccess(t *testing.T) {
	run := ownedRun("github-run")
	run.Spec.Trigger = platformv1alpha1.TriggerRef{Kind: "GitHubRepository", Name: "private-repo"}
	srv, ms := newAuthzTestServer(t, run)
	if err := ms.SetResourceOwner(context.Background(), githubRepositoryResourceType, "private-repo", "default", "alice"); err != nil {
		t.Fatalf("SetResourceOwner(trigger): %v", err)
	}

	mallory := actorContext("mallory", "member", "", "")
	resp, err := srv.ListAgentRuns(mallory, &platform.ListAgentRunsRequest{Namespace: "default"})
	if err != nil {
		t.Fatalf("ListAgentRuns: %v", err)
	}
	if len(resp.Runs) != 0 {
		t.Fatalf("stranger saw trigger-owned run: %#v", resp.Runs)
	}
	if _, err := srv.GetAgentRun(mallory, &platform.GetAgentRunRequest{Namespace: "default", Name: run.Name}); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("GetAgentRun by stranger: want PermissionDenied, got %v", err)
	}
	if _, err := srv.SendAgentRunMessage(mallory, &platform.SendAgentRunMessageRequest{Namespace: "default", Name: run.Name, Message: "leak secrets"}); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("SendAgentRunMessage by stranger: want PermissionDenied, got %v", err)
	}

	if _, err := srv.GetAgentRun(actorContext("alice", "member", "", ""), &platform.GetAgentRunRequest{Namespace: "default", Name: run.Name}); err != nil {
		t.Fatalf("GetAgentRun by trigger owner: %v", err)
	}
}

func TestTriggerOwnedRunPresenceRequiresInheritedAccess(t *testing.T) {
	run := ownedRun("github-presence")
	run.Spec.Trigger = platformv1alpha1.TriggerRef{Kind: "GitHubRepository", Name: "private-repo"}
	srv, ms := newAuthzTestServer(t, run)
	srv.presence = NewPresenceTracker()
	if err := ms.SetResourceOwner(context.Background(), githubRepositoryResourceType, "private-repo", "default", "alice"); err != nil {
		t.Fatalf("SetResourceOwner(trigger): %v", err)
	}
	req := &platform.GetPresenceRequest{ResourceType: "agent_run", ResourceId: run.Name, ResourceNamespace: "default"}
	if _, err := srv.GetPresence(actorContext("mallory", "member", "", ""), req); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("GetPresence by stranger: want PermissionDenied, got %v", err)
	}
	if _, err := srv.GetPresence(actorContext("alice", "member", "", ""), req); err != nil {
		t.Fatalf("GetPresence by trigger owner: %v", err)
	}
}

func TestListAgentRunsFiltersOwnedRunsForStrangers(t *testing.T) {
	srv, ms := newAuthzTestServer(t,
		ownedRun("run-owned"),
		ownedRun("run-trigger"),
	)
	if err := ms.SetResourceOwner(context.Background(), "agent_run", "run-owned", "default", "alice"); err != nil {
		t.Fatalf("SetResourceOwner: %v", err)
	}

	resp, err := srv.ListAgentRuns(actorContext("mallory", "member", "", ""), &platform.ListAgentRunsRequest{Namespace: "default"})
	if err != nil {
		t.Fatalf("ListAgentRuns: %v", err)
	}
	for _, run := range resp.Runs {
		if run.Name == "run-owned" {
			t.Fatalf("stranger should not see alice's run in list")
		}
	}
	found := false
	for _, run := range resp.Runs {
		if run.Name == "run-trigger" {
			found = true
		}
	}
	if !found {
		t.Fatalf("unowned trigger run should stay visible")
	}

	// The owner sees both.
	resp, err = srv.ListAgentRuns(actorContext("alice", "member", "", ""), &platform.ListAgentRunsRequest{Namespace: "default"})
	if err != nil {
		t.Fatalf("ListAgentRuns owner: %v", err)
	}
	if len(resp.Runs) != 2 {
		t.Fatalf("owner should see 2 runs, got %d", len(resp.Runs))
	}
}

func TestRevokeShareDeniedForNonAdminWithoutShareLookup(t *testing.T) {
	srv, _ := newAuthzTestServer(t)
	// mockStateStore does not implement GetShareByID, so non-admin callers
	// must be denied rather than allowed to revoke arbitrary share IDs.
	err := srv.RevokeShare(actorContext("mallory", "member", "", ""), &platform.RevokeShareRequest{ShareId: "some-share"})
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("want PermissionDenied, got %v", err)
	}
}
