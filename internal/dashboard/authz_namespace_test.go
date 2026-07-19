package dashboard

import (
	"context"
	"errors"
	"testing"

	"connectrpc.com/connect"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/store"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
)

// userNamespaceObj builds a Namespace provisioned as a user's personal
// namespace (as ensureUserNamespace labels them).
func userNamespaceObj(name string) *corev1.Namespace {
	return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name:   name,
		Labels: map[string]string{userNamespaceLabel: "true"},
	}}
}

// sharedNamespaceObj builds a plain (shared/system) Namespace.
func sharedNamespaceObj(name string) *corev1.Namespace {
	return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}
}

// modelDefaultsProject builds a Project source with enough defaults for
// createAgentRunFromRequest to succeed without any real credentials.
func modelDefaultsProject(namespace string) *triggersv1alpha1.Project {
	return &triggersv1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{Name: "payments", Namespace: namespace},
		Spec: triggersv1alpha1.ProjectSpec{
			DisplayName: "Payments",
			Defaults: triggersv1alpha1.AgentRunDefaults{
				Model:    "gpt-4.1",
				Provider: triggersv1alpha1.ProviderOpenAI,
				AuthMode: platformv1alpha1.AgentRunAuthModeAPIKey,
				Secrets: triggersv1alpha1.AgentRunSecrets{
					ProviderKeys: []platformv1alpha1.ProviderKeyRef{{
						Provider:   triggersv1alpha1.ProviderOpenAI,
						SecretName: "proj-openai",
						SecretKey:  "api-key",
					}},
				},
			},
		},
	}
}

func TestCreateAgentRunDeniesOtherUsersPersonalNamespace(t *testing.T) {
	scheme := testProjectScheme(t)
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&platformv1alpha1.AgentRun{}).
		WithObjects(userNamespaceObj("alice-ns")).
		Build()
	srv := &Server{k8sClient: c, scheme: scheme}

	_, err := srv.CreateAgentRun(actorContext("mallory", "member", "", ""), &platform.CreateAgentRunRequest{
		Namespace:   "alice-ns",
		Source:      &platform.SourceRef{Kind: "Project", Name: "payments"},
		UserRequest: "read alice's stuff",
	})
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("CreateAgentRun in another user's namespace: want PermissionDenied, got %v", err)
	}

	// Nothing may have been created in the foreign namespace.
	runs := &platformv1alpha1.AgentRunList{}
	if err := c.List(context.Background(), runs, client.InNamespace("alice-ns")); err != nil {
		t.Fatalf("List(AgentRun) error = %v", err)
	}
	if len(runs.Items) != 0 {
		t.Fatalf("expected no runs in alice-ns, got %d", len(runs.Items))
	}
}

func TestCreateAgentRunAllowsSharedNamespaceForMembers(t *testing.T) {
	scheme := testProjectScheme(t)
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&platformv1alpha1.AgentRun{}).
		WithObjects(sharedNamespaceObj("shared-tools"), modelDefaultsProject("shared-tools")).
		Build()
	srv := &Server{k8sClient: c, scheme: scheme}

	resp, err := srv.CreateAgentRun(projectActorCtx(), &platform.CreateAgentRunRequest{
		Namespace:   "shared-tools",
		Source:      &platform.SourceRef{Kind: "Project", Name: "payments"},
		UserRequest: "run from the shared source",
	})
	if err != nil {
		t.Fatalf("CreateAgentRun() in shared namespace error = %v", err)
	}
	if resp.Namespace != "shared-tools" {
		t.Fatalf("run namespace = %q, want shared-tools", resp.Namespace)
	}
}

func TestCreateAgentRunAdminMayTargetUserNamespace(t *testing.T) {
	scheme := testProjectScheme(t)
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&platformv1alpha1.AgentRun{}).
		WithObjects(userNamespaceObj("alice-ns"), modelDefaultsProject("alice-ns")).
		Build()
	srv := &Server{k8sClient: c, scheme: scheme}

	resp, err := srv.CreateAgentRun(actorContext("root", "admin", "", ""), &platform.CreateAgentRunRequest{
		Namespace:   "alice-ns",
		Source:      &platform.SourceRef{Kind: "Project", Name: "payments"},
		UserRequest: "admin assisting alice",
	})
	if err != nil {
		t.Fatalf("CreateAgentRun() by admin error = %v", err)
	}
	if resp.Namespace != "alice-ns" {
		t.Fatalf("run namespace = %q, want alice-ns", resp.Namespace)
	}
}

func TestCreateAgentRunOwnNamespaceStillWorks(t *testing.T) {
	scheme := testProjectScheme(t)
	ns := testUserNS()
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&platformv1alpha1.AgentRun{}).
		WithObjects(userNamespaceObj(ns), modelDefaultsProject(ns)).
		Build()
	srv := &Server{k8sClient: c, scheme: scheme}

	resp, err := srv.CreateAgentRun(projectActorCtx(), &platform.CreateAgentRunRequest{
		Namespace:   ns,
		Source:      &platform.SourceRef{Kind: "Project", Name: "payments"},
		UserRequest: "own namespace, explicitly named",
	})
	if err != nil {
		t.Fatalf("CreateAgentRun() in own namespace error = %v", err)
	}
	if resp.Namespace != ns {
		t.Fatalf("run namespace = %q, want %q", resp.Namespace, ns)
	}
}

func TestCreateAgentRunFailsClosedWhenOwnershipRecordingFails(t *testing.T) {
	scheme := testProjectScheme(t)
	ns := testUserNS()
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&platformv1alpha1.AgentRun{}).
		WithObjects(modelDefaultsProject(ns)).
		Build()
	ms := newMockStateStore()
	ms.setResourceOwnerErr = errors.New("db down")
	srv := &Server{k8sClient: c, scheme: scheme, stateStore: ms}

	_, err := srv.CreateAgentRun(projectActorCtx(), &platform.CreateAgentRunRequest{
		Source:      &platform.SourceRef{Kind: "Project", Name: "payments"},
		UserRequest: "must not become a public run",
	})
	if connect.CodeOf(err) != connect.CodeInternal {
		t.Fatalf("CreateAgentRun() with failing ownership store: want Internal, got %v", err)
	}

	// The run must have been rolled back rather than left unowned (unowned
	// resources are visible to every authenticated user).
	runs := &platformv1alpha1.AgentRunList{}
	if err := c.List(context.Background(), runs, client.InNamespace(ns)); err != nil {
		t.Fatalf("List(AgentRun) error = %v", err)
	}
	if len(runs.Items) != 0 {
		t.Fatalf("expected run rollback, found %d runs", len(runs.Items))
	}
	ms.mu.Lock()
	defer ms.mu.Unlock()
	if len(ms.sessions) != 0 {
		t.Fatalf("expected rollback to clean sessions, found %d", len(ms.sessions))
	}
	if len(ms.deletedAgentRunData) != 1 {
		t.Fatalf("DeleteAgentRunData calls = %d, want 1", len(ms.deletedAgentRunData))
	}
	if got := ms.deletedAgentRunData[0]; got.namespace != ns || got.projectID != ns+"-chat" || got.name == "" {
		t.Fatalf("DeleteAgentRunData call = %#v", got)
	}
}

func TestListAvailableModelsDeniesOtherUsersPersonalNamespace(t *testing.T) {
	scheme := testProjectScheme(t)
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(userNamespaceObj("alice-ns")).
		Build()
	srv := &Server{k8sClient: c, scheme: scheme}

	_, err := srv.ListAvailableModels(actorContext("mallory", "member", "", ""), &platform.ListAvailableModelsRequest{
		Namespace: "alice-ns",
		Provider:  "openai",
	})
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("ListAvailableModels in another user's namespace: want PermissionDenied, got %v", err)
	}
}

func TestGetTeamRuntimeRequiresParentRunAccess(t *testing.T) {
	scheme := testProjectScheme(t)
	runtime := &platformv1alpha1.AgentRunTeamRuntime{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "run-owned-team",
			Namespace: "default",
			Labels:    map[string]string{"platform.gratefulagents.dev/team-parent": "run-owned"},
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(runtime).Build()
	ms := newMockStateStore()
	srv := &Server{k8sClient: c, scheme: scheme, stateStore: ms}
	if err := ms.SetResourceOwner(context.Background(), "agent_run", "run-owned", "default", "alice"); err != nil {
		t.Fatalf("SetResourceOwner: %v", err)
	}

	req := &platform.GetTeamRuntimeRequest{Namespace: "default", ParentName: "run-owned"}
	_, err := srv.GetTeamRuntime(actorContext("mallory", "member", "", ""), req)
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("GetTeamRuntime by stranger: want PermissionDenied, got %v", err)
	}
	if _, err := srv.GetTeamRuntime(actorContext("alice", "member", "", ""), req); err != nil {
		t.Fatalf("GetTeamRuntime by owner: %v", err)
	}
}

func TestGetPresenceRequiresResourceAccess(t *testing.T) {
	scheme := testProjectScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	ms := newMockStateStore()
	srv := &Server{k8sClient: c, scheme: scheme, stateStore: ms, presence: NewPresenceTracker()}
	if err := ms.SetResourceOwner(context.Background(), "agent_run", "run-owned", "default", "alice"); err != nil {
		t.Fatalf("SetResourceOwner: %v", err)
	}

	req := &platform.GetPresenceRequest{ResourceType: "agent_run", ResourceId: "run-owned", ResourceNamespace: "default"}
	_, err := srv.GetPresence(actorContext("mallory", "member", "", ""), req)
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("GetPresence by stranger: want PermissionDenied, got %v", err)
	}
	if _, err := srv.GetPresence(actorContext("alice", "member", "", ""), req); err != nil {
		t.Fatalf("GetPresence by owner: %v", err)
	}
}

// TestAuthorizeRequestNamespaceMissingNamespaceObject documents the posture
// for namespaces with no Namespace object: they cannot be a provisioned
// personal namespace, so the request proceeds (and any write fails downstream
// against the missing namespace in a real cluster).
func TestAuthorizeRequestNamespaceMissingNamespaceObject(t *testing.T) {
	scheme := testProjectScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	srv := &Server{k8sClient: c, scheme: scheme}

	got, err := srv.authorizeRequestNamespace(actorContext("mallory", "member", "", ""), "no-such-ns", nil)
	if err != nil {
		t.Fatalf("authorizeRequestNamespace() error = %v", err)
	}
	if got != "no-such-ns" {
		t.Fatalf("namespace = %q, want no-such-ns", got)
	}
	// Sanity: a labeled personal namespace with the same setup is denied.
	if err := c.Create(context.Background(), userNamespaceObj("victim-ns")); err != nil && !k8serrors.IsAlreadyExists(err) {
		t.Fatalf("create namespace: %v", err)
	}
	if _, err := srv.authorizeRequestNamespace(actorContext("mallory", "member", "", ""), "victim-ns", nil); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("authorizeRequestNamespace(victim-ns): want PermissionDenied, got %v", err)
	}
}

// TestCreateAgentRunSharedSourceOpensOwnersNamespace covers the collaboration
// exception: a collaborator share on a source inside the owner's personal
// namespace lets the recipient create runs from it there; a viewer share (or
// no share) does not.
func TestCreateAgentRunSharedSourceOpensOwnersNamespace(t *testing.T) {
	scheme := testProjectScheme(t)
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&platformv1alpha1.AgentRun{}).
		WithObjects(userNamespaceObj("alice-ns"), modelDefaultsProject("alice-ns")).
		Build()
	ms := newCollaborationStateStore()
	srv := &Server{k8sClient: c, scheme: scheme, stateStore: ms}
	if err := ms.SetResourceOwner(context.Background(), projectResourceType, "payments", "alice-ns", "alice"); err != nil {
		t.Fatalf("SetResourceOwner: %v", err)
	}

	req := func() *platform.CreateAgentRunRequest {
		return &platform.CreateAgentRunRequest{
			Namespace:   "alice-ns",
			Source:      &platform.SourceRef{Kind: "Project", Name: "payments"},
			UserRequest: "collaborate on alice's project",
		}
	}

	// No share: denied.
	if _, err := srv.CreateAgentRun(actorContext("bob", "member", "", ""), req()); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("CreateAgentRun with no share: want PermissionDenied, got %v", err)
	}

	// Viewer share: still denied (viewing a project is not a grant to run in
	// the owner's namespace).
	if _, err := ms.ShareResource(context.Background(), &store.ResourceShare{
		ResourceType: projectResourceType, ResourceID: "payments", ResourceNamespace: "alice-ns",
		SharedWithUserID: "carol", SharedByUserID: "alice", Permission: "viewer",
	}); err != nil {
		t.Fatalf("ShareResource(viewer): %v", err)
	}
	if _, err := srv.CreateAgentRun(actorContext("carol", "member", "", ""), req()); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("CreateAgentRun with viewer share: want PermissionDenied, got %v", err)
	}

	// Collaborator share: allowed.
	if _, err := ms.ShareResource(context.Background(), &store.ResourceShare{
		ResourceType: projectResourceType, ResourceID: "payments", ResourceNamespace: "alice-ns",
		SharedWithUserID: "bob", SharedByUserID: "alice", Permission: "collaborator",
	}); err != nil {
		t.Fatalf("ShareResource(collaborator): %v", err)
	}
	resp, err := srv.CreateAgentRun(actorContext("bob", "member", "", ""), req())
	if err != nil {
		t.Fatalf("CreateAgentRun with collaborator share: %v", err)
	}
	if resp.Namespace != "alice-ns" {
		t.Fatalf("run namespace = %q, want alice-ns", resp.Namespace)
	}
}
