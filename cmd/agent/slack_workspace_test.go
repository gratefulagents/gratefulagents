package main

import (
	"context"
	"testing"
	"time"

	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func workspaceTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := triggersv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}
	return scheme
}

func workspaceMemberAgent(name, namespace, userID string, commanders ...string) *triggersv1alpha1.SlackAgent {
	return &triggersv1alpha1.SlackAgent{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, Generation: 1},
		Spec: triggersv1alpha1.SlackAgentSpec{
			WorkspaceRef: &triggersv1alpha1.SlackWorkspaceRef{Name: "acme", Namespace: "user-admin"},
			SlackUserID:  userID,
			Commanders:   commanders,
		},
	}
}

func testWorkspaceBackend(t *testing.T, agents ...*triggersv1alpha1.SlackAgent) *workspaceSlackBackend {
	t.Helper()
	builder := fake.NewClientBuilder().WithScheme(workspaceTestScheme(t))
	for _, a := range agents {
		builder = builder.WithObjects(a)
	}
	b := &workspaceSlackBackend{
		cfg:         slackWorkspaceConfig{WorkspaceName: "acme", Namespace: "user-admin"},
		botUserID:   "B0BOT",
		teamID:      "T0123",
		deps:        &slackDeps{crdClient: builder.Build()},
		members:     map[string]*workspaceMember{},
		onboardedAt: map[string]time.Time{},
	}
	b.syncMembers(context.Background())
	return b
}

func TestWorkspaceBackendAllowTeam(t *testing.T) {
	b := &workspaceSlackBackend{teamID: "T0123"}
	for _, tc := range []struct {
		team string
		want bool
	}{
		{"T0123", true},
		{"", true}, // some payloads omit team; don't drop them
		{"T0999", false},
	} {
		if got := b.allowTeam(tc.team); got != tc.want {
			t.Errorf("allowTeam(%q) = %v, want %v", tc.team, got, tc.want)
		}
	}
}

func TestWorkspaceBackendSyncMembers(t *testing.T) {
	other := workspaceMemberAgent("carol", "user-carol", "U03")
	other.Spec.WorkspaceRef = &triggersv1alpha1.SlackWorkspaceRef{Name: "different", Namespace: "user-admin"}
	suspended := workspaceMemberAgent("dan", "user-dan", "U04")
	suspended.Spec.Suspend = true
	noUser := workspaceMemberAgent("erin", "user-erin", "")

	b := testWorkspaceBackend(t,
		workspaceMemberAgent("alice", "user-alice", "U01"),
		workspaceMemberAgent("bob", "user-bob", "U02"),
		other, suspended, noUser,
	)

	if m := b.memberByUser("U01"); m == nil || m.namespace != "user-alice" {
		t.Errorf("expected U01 → user-alice member, got %+v", m)
	}
	if m := b.memberByUser("U02"); m == nil || m.namespace != "user-bob" {
		t.Errorf("expected U02 → user-bob member, got %+v", m)
	}
	for _, id := range []string{"U03", "U04", ""} {
		if m := b.memberByUser(id); m != nil {
			t.Errorf("expected no member for %q, got %s/%s", id, m.namespace, m.name)
		}
	}

	// Members keyed to per-namespace store keys so same-named agents can't collide.
	if m := b.memberByUser("U01"); m.orch == nil || m.orch.slackStoreKey() != "user-alice/alice" {
		t.Errorf("expected namespace-qualified store key, got %q", m.orch.slackStoreKey())
	}

	// Unchanged generation keeps the same orchestrator across syncs.
	before := b.memberByUser("U01").orch
	b.syncMembers(context.Background())
	if after := b.memberByUser("U01").orch; after != before {
		t.Errorf("expected orchestrator reuse for unchanged member spec")
	}
}

func TestWorkspaceBackendMemberForCommander(t *testing.T) {
	b := testWorkspaceBackend(t,
		workspaceMemberAgent("alice", "user-alice", "U01", "UCMD"),
		workspaceMemberAgent("bob", "user-bob", "U02"),
	)

	if m := b.memberForCommander("UCMD"); m == nil || m.name != "alice" {
		t.Fatalf("expected UCMD to resolve to alice, got %+v", m)
	}
	if m := b.memberForCommander("USTRANGER"); m != nil {
		t.Errorf("expected no member for unknown commander")
	}

	// Ambiguous: two members list the same commander → refuse to guess.
	b2 := testWorkspaceBackend(t,
		workspaceMemberAgent("alice", "user-alice", "U01", "UCMD"),
		workspaceMemberAgent("bob", "user-bob", "U02", "UCMD"),
	)
	if m := b2.memberForCommander("UCMD"); m != nil {
		t.Errorf("expected ambiguous commander to resolve to nil, got %s", m.name)
	}
}
