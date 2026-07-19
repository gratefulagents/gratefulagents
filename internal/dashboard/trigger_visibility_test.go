package dashboard

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/store"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/gratefulagents/gratefulagents/internal/githubapp"
)

// triggerActorCtx returns a context carrying an authenticated actor with the
// given subject and role, as recorded by the RPC interceptor.
func triggerActorCtx(subject, role string) context.Context {
	return context.WithValue(context.Background(), requestActorContextKey{}, requestActor{Subject: subject, Role: role})
}

// triggerVisibilityFixture builds a server whose namespace "team-a" holds, for
// each trigger type, one resource owned by user-1 ("mine-*"), one owned by
// user-2 ("theirs-*"), and one without an ownership record ("legacy-*").
func triggerVisibilityFixture(t *testing.T) (*Server, *collaborationStateStore) {
	t.Helper()
	const ns = "team-a"
	srv, _ := newCronTestServer(t,
		&triggersv1alpha1.Cron{ObjectMeta: metav1.ObjectMeta{Name: "mine-cron", Namespace: ns}, Spec: triggersv1alpha1.CronSpec{Schedule: "0 6 * * *"}},
		&triggersv1alpha1.Cron{ObjectMeta: metav1.ObjectMeta{Name: "theirs-cron", Namespace: ns}, Spec: triggersv1alpha1.CronSpec{Schedule: "0 7 * * *"}},
		&triggersv1alpha1.Cron{ObjectMeta: metav1.ObjectMeta{Name: "legacy-cron", Namespace: ns}, Spec: triggersv1alpha1.CronSpec{Schedule: "0 8 * * *"}},
		&triggersv1alpha1.GitHubRepository{ObjectMeta: metav1.ObjectMeta{Name: "mine-repo", Namespace: ns}, Spec: triggersv1alpha1.GitHubRepositorySpec{Owner: "acme", Repo: "mine"}},
		&triggersv1alpha1.GitHubRepository{ObjectMeta: metav1.ObjectMeta{Name: "theirs-repo", Namespace: ns}, Spec: triggersv1alpha1.GitHubRepositorySpec{Owner: "acme", Repo: "theirs"}},
		&triggersv1alpha1.GitHubRepository{ObjectMeta: metav1.ObjectMeta{Name: "legacy-repo", Namespace: ns}, Spec: triggersv1alpha1.GitHubRepositorySpec{Owner: "acme", Repo: "legacy"}},
		&triggersv1alpha1.LinearProject{ObjectMeta: metav1.ObjectMeta{Name: "mine-lp", Namespace: ns}},
		&triggersv1alpha1.LinearProject{ObjectMeta: metav1.ObjectMeta{Name: "theirs-lp", Namespace: ns}},
		&triggersv1alpha1.LinearProject{ObjectMeta: metav1.ObjectMeta{Name: "legacy-lp", Namespace: ns}},
		&triggersv1alpha1.Project{ObjectMeta: metav1.ObjectMeta{Name: "mine-project", Namespace: ns}, Spec: triggersv1alpha1.ProjectSpec{DisplayName: "Mine"}},
		&triggersv1alpha1.Project{ObjectMeta: metav1.ObjectMeta{Name: "theirs-project", Namespace: ns}, Spec: triggersv1alpha1.ProjectSpec{DisplayName: "Theirs"}},
		&triggersv1alpha1.Project{ObjectMeta: metav1.ObjectMeta{Name: "legacy-project", Namespace: ns}, Spec: triggersv1alpha1.ProjectSpec{DisplayName: "Legacy"}},
	)
	ms := newCollaborationStateStore()
	srv.stateStore = ms
	ctx := context.Background()
	for resourceType, suffix := range map[string]string{
		cronResourceType:             "cron",
		githubRepositoryResourceType: "repo",
		linearProjectResourceType:    "lp",
		projectResourceType:          "project",
	} {
		if err := ms.SetResourceOwner(ctx, resourceType, "mine-"+suffix, ns, "user-1"); err != nil {
			t.Fatalf("SetResourceOwner(mine-%s): %v", suffix, err)
		}
		if err := ms.SetResourceOwner(ctx, resourceType, "theirs-"+suffix, ns, "user-2"); err != nil {
			t.Fatalf("SetResourceOwner(theirs-%s): %v", suffix, err)
		}
	}
	return srv, ms
}

func shareTrigger(t *testing.T, ms *collaborationStateStore, resourceType, name, withUser, permission string) string {
	t.Helper()
	share, err := ms.ShareResource(context.Background(), &store.ResourceShare{
		ID:                resourceType + "/" + name + "/" + withUser,
		ResourceType:      resourceType,
		ResourceID:        name,
		ResourceNamespace: "team-a",
		SharedWithUserID:  withUser,
		SharedByUserID:    "user-2",
		Permission:        permission,
	})
	if err != nil {
		t.Fatalf("ShareResource(%s/%s): %v", resourceType, name, err)
	}
	return share.ID
}

func TestListCronsVisibility(t *testing.T) {
	srv, ms := triggerVisibilityFixture(t)

	names := func(resp *platform.ListCronsResponse) map[string]*platform.Cron {
		out := map[string]*platform.Cron{}
		for _, c := range resp.Crons {
			out[c.Name] = c
		}
		return out
	}

	// user-1 sees own + legacy, not user-2's.
	resp, err := srv.ListCrons(triggerActorCtx("user-1", ""), &platform.ListCronsRequest{Namespace: "team-a"})
	if err != nil {
		t.Fatalf("ListCrons() error = %v", err)
	}
	got := names(resp)
	if len(got) != 2 || got["mine-cron"] == nil || got["legacy-cron"] == nil {
		t.Fatalf("ListCrons(user-1) = %v, want mine-cron and legacy-cron", resp.Crons)
	}
	if got["mine-cron"].MyPermission != "owner" || got["mine-cron"].Owner.GetUserId() != "user-1" {
		t.Fatalf("mine-cron ACL = (%q, %v), want owner/user-1", got["mine-cron"].MyPermission, got["mine-cron"].Owner)
	}
	if got["legacy-cron"].MyPermission != "" || got["legacy-cron"].Owner != nil {
		t.Fatalf("legacy-cron ACL = (%q, %v), want unowned", got["legacy-cron"].MyPermission, got["legacy-cron"].Owner)
	}

	// After sharing, user-1 also sees user-2's cron with viewer permission.
	shareTrigger(t, ms, cronResourceType, "theirs-cron", "user-1", "viewer")
	resp, err = srv.ListCrons(triggerActorCtx("user-1", ""), &platform.ListCronsRequest{Namespace: "team-a"})
	if err != nil {
		t.Fatalf("ListCrons() after share error = %v", err)
	}
	got = names(resp)
	if len(got) != 3 || got["theirs-cron"] == nil {
		t.Fatalf("ListCrons(user-1, shared) = %v, want all three crons", resp.Crons)
	}
	if got["theirs-cron"].MyPermission != "viewer" {
		t.Fatalf("theirs-cron MyPermission = %q, want viewer", got["theirs-cron"].MyPermission)
	}

	// user-3 sees only the legacy cron.
	resp, err = srv.ListCrons(triggerActorCtx("user-3", ""), &platform.ListCronsRequest{Namespace: "team-a"})
	if err != nil {
		t.Fatalf("ListCrons(user-3) error = %v", err)
	}
	if len(resp.Crons) != 1 || resp.Crons[0].Name != "legacy-cron" {
		t.Fatalf("ListCrons(user-3) = %v, want only legacy-cron", resp.Crons)
	}

	// Admins see everything.
	resp, err = srv.ListCrons(triggerActorCtx("admin-1", "admin"), &platform.ListCronsRequest{Namespace: "team-a"})
	if err != nil {
		t.Fatalf("ListCrons(admin) error = %v", err)
	}
	if len(resp.Crons) != 3 {
		t.Fatalf("ListCrons(admin) = %d crons, want 3", len(resp.Crons))
	}
}

func TestGetCronAccess(t *testing.T) {
	srv, ms := triggerVisibilityFixture(t)

	// Owner reads own cron.
	pb, err := srv.GetCron(triggerActorCtx("user-1", ""), &platform.GetCronRequest{Namespace: "team-a", Name: "mine-cron"})
	if err != nil {
		t.Fatalf("GetCron(owner) error = %v", err)
	}
	if pb.MyPermission != "owner" {
		t.Fatalf("MyPermission = %q, want owner", pb.MyPermission)
	}

	// Non-owner is denied someone else's cron.
	_, err = srv.GetCron(triggerActorCtx("user-1", ""), &platform.GetCronRequest{Namespace: "team-a", Name: "theirs-cron"})
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("GetCron(other's) error = %v, want PermissionDenied", err)
	}

	// A share grants read access.
	shareTrigger(t, ms, cronResourceType, "theirs-cron", "user-1", "viewer")
	pb, err = srv.GetCron(triggerActorCtx("user-1", ""), &platform.GetCronRequest{Namespace: "team-a", Name: "theirs-cron"})
	if err != nil {
		t.Fatalf("GetCron(shared) error = %v", err)
	}
	if pb.MyPermission != "viewer" || pb.Owner.GetUserId() != "user-2" {
		t.Fatalf("shared ACL = (%q, %v), want viewer/user-2", pb.MyPermission, pb.Owner)
	}

	// Unowned (legacy) crons stay readable by any authenticated user.
	if _, err := srv.GetCron(triggerActorCtx("user-3", ""), &platform.GetCronRequest{Namespace: "team-a", Name: "legacy-cron"}); err != nil {
		t.Fatalf("GetCron(legacy) error = %v", err)
	}

	// Admins can read anything.
	pb, err = srv.GetCron(triggerActorCtx("admin-1", "admin"), &platform.GetCronRequest{Namespace: "team-a", Name: "theirs-cron"})
	if err != nil {
		t.Fatalf("GetCron(admin) error = %v", err)
	}
	if pb.MyPermission != "admin" {
		t.Fatalf("admin MyPermission = %q, want admin", pb.MyPermission)
	}
}

func TestListGitHubRepositoriesVisibility(t *testing.T) {
	srv, ms := triggerVisibilityFixture(t)

	resp, err := srv.ListGitHubRepositories(triggerActorCtx("user-1", ""), &platform.ListGitHubRepositoriesRequest{Namespace: "team-a"})
	if err != nil {
		t.Fatalf("ListGitHubRepositories() error = %v", err)
	}
	got := map[string]*platform.GitHubRepository{}
	for _, r := range resp.Repositories {
		got[r.Name] = r
	}
	if len(got) != 2 || got["mine-repo"] == nil || got["legacy-repo"] == nil {
		t.Fatalf("ListGitHubRepositories(user-1) = %v, want mine-repo and legacy-repo", resp.Repositories)
	}
	if got["mine-repo"].MyPermission != "owner" || got["mine-repo"].ResourceOwner.GetUserId() != "user-1" {
		t.Fatalf("mine-repo ACL = (%q, %v), want owner/user-1", got["mine-repo"].MyPermission, got["mine-repo"].ResourceOwner)
	}

	shareTrigger(t, ms, githubRepositoryResourceType, "theirs-repo", "user-1", "collaborator")
	resp, err = srv.ListGitHubRepositories(triggerActorCtx("user-1", ""), &platform.ListGitHubRepositoriesRequest{Namespace: "team-a"})
	if err != nil {
		t.Fatalf("ListGitHubRepositories() after share error = %v", err)
	}
	if len(resp.Repositories) != 3 {
		t.Fatalf("ListGitHubRepositories(user-1, shared) = %d repos, want 3", len(resp.Repositories))
	}
}

func TestGetGitHubRepositoryAccess(t *testing.T) {
	srv, ms := triggerVisibilityFixture(t)

	_, err := srv.GetGitHubRepository(triggerActorCtx("user-1", ""), &platform.GetGitHubRepositoryRequest{Namespace: "team-a", Name: "theirs-repo"})
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("GetGitHubRepository(other's) error = %v, want PermissionDenied", err)
	}

	shareTrigger(t, ms, githubRepositoryResourceType, "theirs-repo", "user-1", "collaborator")
	pb, err := srv.GetGitHubRepository(triggerActorCtx("user-1", ""), &platform.GetGitHubRepositoryRequest{Namespace: "team-a", Name: "theirs-repo"})
	if err != nil {
		t.Fatalf("GetGitHubRepository(shared) error = %v", err)
	}
	if pb.MyPermission != "collaborator" || pb.ResourceOwner.GetUserId() != "user-2" {
		t.Fatalf("shared ACL = (%q, %v), want collaborator/user-2", pb.MyPermission, pb.ResourceOwner)
	}

	if _, err := srv.GetGitHubRepository(triggerActorCtx("user-3", ""), &platform.GetGitHubRepositoryRequest{Namespace: "team-a", Name: "legacy-repo"}); err != nil {
		t.Fatalf("GetGitHubRepository(legacy) error = %v", err)
	}
}

func TestListLinearProjectsVisibility(t *testing.T) {
	srv, ms := triggerVisibilityFixture(t)

	resp, err := srv.ListLinearProjects(triggerActorCtx("user-1", ""), &platform.ListLinearProjectsRequest{Namespace: "team-a"})
	if err != nil {
		t.Fatalf("ListLinearProjects() error = %v", err)
	}
	got := map[string]*platform.LinearProject{}
	for _, p := range resp.Projects {
		got[p.Name] = p
	}
	if len(got) != 2 || got["mine-lp"] == nil || got["legacy-lp"] == nil {
		t.Fatalf("ListLinearProjects(user-1) = %v, want mine-lp and legacy-lp", resp.Projects)
	}
	if got["mine-lp"].MyPermission != "owner" {
		t.Fatalf("mine-lp MyPermission = %q, want owner", got["mine-lp"].MyPermission)
	}

	shareTrigger(t, ms, linearProjectResourceType, "theirs-lp", "user-1", "viewer")
	resp, err = srv.ListLinearProjects(triggerActorCtx("user-1", ""), &platform.ListLinearProjectsRequest{Namespace: "team-a"})
	if err != nil {
		t.Fatalf("ListLinearProjects() after share error = %v", err)
	}
	if len(resp.Projects) != 3 {
		t.Fatalf("ListLinearProjects(user-1, shared) = %d projects, want 3", len(resp.Projects))
	}
}

func TestListProjectsVisibility(t *testing.T) {
	srv, ms := triggerVisibilityFixture(t)

	names := func(resp *platform.ListProjectsResponse) map[string]*platform.Project {
		out := map[string]*platform.Project{}
		for _, p := range resp.Projects {
			out[p.Name] = p
		}
		return out
	}

	// user-1 sees own + legacy, not user-2's.
	resp, err := srv.ListProjects(triggerActorCtx("user-1", ""), &platform.ListProjectsRequest{Namespace: "team-a"})
	if err != nil {
		t.Fatalf("ListProjects() error = %v", err)
	}
	got := names(resp)
	if len(got) != 2 || got["mine-project"] == nil || got["legacy-project"] == nil {
		t.Fatalf("ListProjects(user-1) = %v, want mine-project and legacy-project", resp.Projects)
	}
	if got["mine-project"].MyPermission != "owner" || got["mine-project"].Owner.GetUserId() != "user-1" {
		t.Fatalf("mine-project ACL = (%q, %v), want owner/user-1", got["mine-project"].MyPermission, got["mine-project"].Owner)
	}

	// After sharing, user-1 also sees user-2's project.
	shareTrigger(t, ms, projectResourceType, "theirs-project", "user-1", "viewer")
	resp, err = srv.ListProjects(triggerActorCtx("user-1", ""), &platform.ListProjectsRequest{Namespace: "team-a"})
	if err != nil {
		t.Fatalf("ListProjects() after share error = %v", err)
	}
	got = names(resp)
	if len(got) != 3 || got["theirs-project"] == nil {
		t.Fatalf("ListProjects(user-1, shared) = %v, want all three projects", resp.Projects)
	}
	if got["theirs-project"].MyPermission != "viewer" {
		t.Fatalf("theirs-project MyPermission = %q, want viewer", got["theirs-project"].MyPermission)
	}

	// user-3 sees only the legacy project.
	resp, err = srv.ListProjects(triggerActorCtx("user-3", ""), &platform.ListProjectsRequest{Namespace: "team-a"})
	if err != nil {
		t.Fatalf("ListProjects(user-3) error = %v", err)
	}
	if len(resp.Projects) != 1 || resp.Projects[0].Name != "legacy-project" {
		t.Fatalf("ListProjects(user-3) = %v, want only legacy-project", resp.Projects)
	}

	// Admins see everything.
	resp, err = srv.ListProjects(triggerActorCtx("admin-1", "admin"), &platform.ListProjectsRequest{Namespace: "team-a"})
	if err != nil {
		t.Fatalf("ListProjects(admin) error = %v", err)
	}
	if len(resp.Projects) != 3 {
		t.Fatalf("ListProjects(admin) = %d projects, want 3", len(resp.Projects))
	}
}

func TestGetProjectAccess(t *testing.T) {
	srv, ms := triggerVisibilityFixture(t)

	// Owner reads own project.
	pb, err := srv.GetProject(triggerActorCtx("user-1", ""), &platform.GetProjectRequest{Namespace: "team-a", Name: "mine-project"})
	if err != nil {
		t.Fatalf("GetProject(owner) error = %v", err)
	}
	if pb.MyPermission != "owner" {
		t.Fatalf("MyPermission = %q, want owner", pb.MyPermission)
	}

	// Non-owner is denied someone else's project.
	_, err = srv.GetProject(triggerActorCtx("user-1", ""), &platform.GetProjectRequest{Namespace: "team-a", Name: "theirs-project"})
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("GetProject(other's) error = %v, want PermissionDenied", err)
	}

	// A share grants read access.
	shareTrigger(t, ms, projectResourceType, "theirs-project", "user-1", "viewer")
	pb, err = srv.GetProject(triggerActorCtx("user-1", ""), &platform.GetProjectRequest{Namespace: "team-a", Name: "theirs-project"})
	if err != nil {
		t.Fatalf("GetProject(shared) error = %v", err)
	}
	if pb.MyPermission != "viewer" || pb.Owner.GetUserId() != "user-2" {
		t.Fatalf("shared ACL = (%q, %v), want viewer/user-2", pb.MyPermission, pb.Owner)
	}

	// Unowned (legacy) projects stay readable by any authenticated user.
	if _, err := srv.GetProject(triggerActorCtx("user-3", ""), &platform.GetProjectRequest{Namespace: "team-a", Name: "legacy-project"}); err != nil {
		t.Fatalf("GetProject(legacy) error = %v", err)
	}

	// Admins can read anything.
	pb, err = srv.GetProject(triggerActorCtx("admin-1", "admin"), &platform.GetProjectRequest{Namespace: "team-a", Name: "theirs-project"})
	if err != nil {
		t.Fatalf("GetProject(admin) error = %v", err)
	}
	if pb.MyPermission != "admin" {
		t.Fatalf("admin MyPermission = %q, want admin", pb.MyPermission)
	}
}

func TestWatchProjectsVisibility(t *testing.T) {
	srv, _ := triggerVisibilityFixture(t)
	ctx := triggerActorCtx("user-1", "")

	var events []*platform.ProjectEvent
	tick := triggerListTick(ctx, srv, "team-a", "Project", projectResourceType,
		func(ctx context.Context) ([]triggersv1alpha1.Project, bool, error) {
			list := &triggersv1alpha1.ProjectList{}
			skip, err := srv.listNamespaced(ctx, "team-a", list, "watch Projects")
			return list.Items, skip, err
		},
		func(p *triggersv1alpha1.Project) (string, string, string) {
			return p.Namespace, p.Name, p.ResourceVersion
		},
		func(p *triggersv1alpha1.Project, metrics map[resourceMetricsKey]*platform.ProjectMetrics) (*platform.ProjectEvent, error) {
			return &platform.ProjectEvent{Type: "MODIFIED", Project: srv.projectProto(ctx, p, metrics)}, nil
		},
		func(namespace, name string) *platform.ProjectEvent {
			return &platform.ProjectEvent{Type: "DELETED", Project: &platform.Project{Namespace: namespace, Name: name}}
		},
		func(e *platform.ProjectEvent) error { events = append(events, e); return nil },
	)

	// The caller sees their own project and the unowned one, not user-2's.
	if err := tick(); err != nil {
		t.Fatalf("tick: %v", err)
	}
	got := map[string]string{}
	for _, e := range events {
		got[e.Project.Name] = e.Type
	}
	if len(events) != 2 || got["mine-project"] != "MODIFIED" || got["legacy-project"] != "MODIFIED" {
		t.Fatalf("watch events = %v, want MODIFIED mine-project and legacy-project only", got)
	}
}

func TestGetLinearProjectAccess(t *testing.T) {
	srv, ms := triggerVisibilityFixture(t)

	_, err := srv.GetLinearProject(triggerActorCtx("user-1", ""), &platform.GetLinearProjectRequest{Namespace: "team-a", Name: "theirs-lp"})
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("GetLinearProject(other's) error = %v, want PermissionDenied", err)
	}

	shareTrigger(t, ms, linearProjectResourceType, "theirs-lp", "user-1", "viewer")
	pb, err := srv.GetLinearProject(triggerActorCtx("user-1", ""), &platform.GetLinearProjectRequest{Namespace: "team-a", Name: "theirs-lp"})
	if err != nil {
		t.Fatalf("GetLinearProject(shared) error = %v", err)
	}
	if pb.MyPermission != "viewer" {
		t.Fatalf("MyPermission = %q, want viewer", pb.MyPermission)
	}

	if _, err := srv.GetLinearProject(triggerActorCtx("user-3", ""), &platform.GetLinearProjectRequest{Namespace: "team-a", Name: "legacy-lp"}); err != nil {
		t.Fatalf("GetLinearProject(legacy) error = %v", err)
	}
}

func TestCreateGitHubRepositoryFromInstallationRecordsOwnership(t *testing.T) {
	privateKey := testGitHubAppPrivateKey(t)
	scheme := testProjectScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "github-app-key", Namespace: "platform"}, Data: map[string][]byte{githubapp.PrivateKeySecretKey: privateKey}},
	).Build()
	srv := NewServer(c, scheme, nil, nil, false, WithGitHubAppConfig(99, "gratefulagents", "github-app-key", "platform"))
	ms := newCollaborationStateStore()
	srv.stateStore = ms

	resp, err := srv.CreateGitHubRepositoryFromInstallation(triggerActorCtx("user-1", "admin"), &platform.CreateGitHubRepositoryFromInstallationRequest{
		InstallationId:     123,
		Owner:              "acme",
		Repo:               "payments",
		Namespace:          "team-a",
		Provider:           "anthropic",
		ClaudeApiKeySecret: "anthropic-key",
	})
	if err != nil {
		t.Fatalf("CreateGitHubRepositoryFromInstallation() error = %v", err)
	}
	if resp.MyPermission != "admin" || resp.ResourceOwner.GetUserId() != "user-1" {
		t.Fatalf("response ACL = (%q, %v), want admin/user-1", resp.MyPermission, resp.ResourceOwner)
	}

	ownership, err := ms.GetResourceOwner(context.Background(), githubRepositoryResourceType, resp.Name, "team-a")
	if err != nil || ownership == nil || ownership.OwnerID != "user-1" {
		t.Fatalf("GetResourceOwner() = %v, %v, want user-1", ownership, err)
	}

	// The creator sees it; another member does not.
	gh := &triggersv1alpha1.GitHubRepository{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "team-a", Name: resp.Name}, gh); err != nil {
		t.Fatalf("Get(created repo) error = %v", err)
	}
	_, err = srv.GetGitHubRepository(triggerActorCtx("user-2", ""), &platform.GetGitHubRepositoryRequest{Namespace: "team-a", Name: resp.Name})
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("GetGitHubRepository(non-owner) error = %v, want PermissionDenied", err)
	}
}

func TestResolveSourceDefaultsEnforcesACL(t *testing.T) {
	srv, ms := triggerVisibilityFixture(t)

	cases := []struct {
		kind, resourceType   string
		mine, theirs, legacy string
	}{
		{"Cron", cronResourceType, "mine-cron", "theirs-cron", "legacy-cron"},
		{"GitHubRepository", githubRepositoryResourceType, "mine-repo", "theirs-repo", "legacy-repo"},
		{"LinearProject", linearProjectResourceType, "mine-lp", "theirs-lp", "legacy-lp"},
		{"Project", projectResourceType, "mine-project", "theirs-project", "legacy-project"},
	}
	for _, tc := range cases {
		// Own and legacy (unowned) triggers resolve fine.
		if _, _, err := srv.resolveSourceDefaults(triggerActorCtx("user-1", ""), "team-a", &platform.SourceRef{Kind: tc.kind, Name: tc.mine}); err != nil {
			t.Fatalf("resolveSourceDefaults(%s, mine) error = %v", tc.kind, err)
		}
		if _, _, err := srv.resolveSourceDefaults(triggerActorCtx("user-1", ""), "team-a", &platform.SourceRef{Kind: tc.kind, Name: tc.legacy}); err != nil {
			t.Fatalf("resolveSourceDefaults(%s, legacy) error = %v", tc.kind, err)
		}

		// Another user's private trigger cannot be used as a run source.
		_, _, err := srv.resolveSourceDefaults(triggerActorCtx("user-1", ""), "team-a", &platform.SourceRef{Kind: tc.kind, Name: tc.theirs})
		if connect.CodeOf(err) != connect.CodePermissionDenied {
			t.Fatalf("resolveSourceDefaults(%s, theirs) error = %v, want PermissionDenied", tc.kind, err)
		}

		// A share unlocks it.
		shareTrigger(t, ms, tc.resourceType, tc.theirs, "user-1", "viewer")
		if _, _, err := srv.resolveSourceDefaults(triggerActorCtx("user-1", ""), "team-a", &platform.SourceRef{Kind: tc.kind, Name: tc.theirs}); err != nil {
			t.Fatalf("resolveSourceDefaults(%s, shared) error = %v", tc.kind, err)
		}

		// Admins may use anything.
		if _, _, err := srv.resolveSourceDefaults(triggerActorCtx("admin-1", "admin"), "team-a", &platform.SourceRef{Kind: tc.kind, Name: tc.theirs}); err != nil {
			t.Fatalf("resolveSourceDefaults(%s, admin) error = %v", tc.kind, err)
		}
	}
}

func TestTriggerWatchEmitsRemovalsAndACLChanges(t *testing.T) {
	srv, ms := triggerVisibilityFixture(t)
	ctx := triggerActorCtx("user-1", "")

	var events []*platform.CronEvent
	tick := triggerListTick(ctx, srv, "team-a", "Cron", cronResourceType,
		func(ctx context.Context) ([]triggersv1alpha1.Cron, bool, error) {
			list := &triggersv1alpha1.CronList{}
			skip, err := srv.listNamespaced(ctx, "team-a", list, "watch Crons")
			return list.Items, skip, err
		},
		func(cr *triggersv1alpha1.Cron) (string, string, string) {
			return cr.Namespace, cr.Name, cr.ResourceVersion
		},
		func(cr *triggersv1alpha1.Cron, metrics map[resourceMetricsKey]*platform.ProjectMetrics) (*platform.CronEvent, error) {
			pb := srv.cronProto(context.Background(), cr, metrics)
			pb.Owner, pb.MyPermission = srv.resourceACL(ctx, cronResourceType, cr.Name, cr.Namespace)
			return &platform.CronEvent{Type: "MODIFIED", Cron: pb}, nil
		},
		func(namespace, name string) *platform.CronEvent {
			return &platform.CronEvent{Type: "DELETED", Cron: &platform.Cron{Namespace: namespace, Name: name}}
		},
		func(e *platform.CronEvent) error { events = append(events, e); return nil },
	)

	// Poll 1: the caller sees their own cron and the unowned one.
	if err := tick(); err != nil {
		t.Fatalf("tick 1: %v", err)
	}
	got := map[string]string{}
	for _, e := range events {
		got[e.Cron.Name] = e.Type
	}
	if len(events) != 2 || got["mine-cron"] != "MODIFIED" || got["legacy-cron"] != "MODIFIED" {
		t.Fatalf("poll 1 events = %v, want MODIFIED mine-cron and legacy-cron", got)
	}

	// Poll 2: a new share surfaces the other user's cron. Watch ticks read
	// ACL state through coalescing probes, so expire them to observe the
	// direct store mutation now instead of after probeACLTTL.
	events = nil
	shareID := shareTrigger(t, ms, cronResourceType, "theirs-cron", "user-1", "viewer")
	srv.probes.reset()
	if err := tick(); err != nil {
		t.Fatalf("tick 2: %v", err)
	}
	if len(events) != 1 || events[0].Type != "MODIFIED" || events[0].Cron.Name != "theirs-cron" || events[0].Cron.MyPermission != "viewer" {
		t.Fatalf("poll 2 events = %v, want MODIFIED theirs-cron as viewer", events)
	}

	// Poll 3: a permission change re-emits even though the CR is unchanged.
	events = nil
	if err := ms.UpdateSharePermission(context.Background(), shareID, "collaborator"); err != nil {
		t.Fatalf("UpdateSharePermission: %v", err)
	}
	srv.probes.reset()
	if err := tick(); err != nil {
		t.Fatalf("tick 3: %v", err)
	}
	if len(events) != 1 || events[0].Type != "MODIFIED" || events[0].Cron.MyPermission != "collaborator" {
		t.Fatalf("poll 3 events = %v, want MODIFIED theirs-cron as collaborator", events)
	}

	// Poll 4: revoking the share emits a removal.
	events = nil
	if err := ms.RevokeShare(context.Background(), shareID); err != nil {
		t.Fatalf("RevokeShare: %v", err)
	}
	srv.probes.reset()
	if err := tick(); err != nil {
		t.Fatalf("tick 4: %v", err)
	}
	if len(events) != 1 || events[0].Type != "DELETED" || events[0].Cron.Name != "theirs-cron" {
		t.Fatalf("poll 4 events = %v, want DELETED theirs-cron", events)
	}

	// Poll 5: deleting the resource emits a removal.
	events = nil
	if err := srv.k8sClient.Delete(context.Background(), &triggersv1alpha1.Cron{ObjectMeta: metav1.ObjectMeta{Name: "mine-cron", Namespace: "team-a"}}); err != nil {
		t.Fatalf("Delete(mine-cron): %v", err)
	}
	if err := tick(); err != nil {
		t.Fatalf("tick 5: %v", err)
	}
	if len(events) != 1 || events[0].Type != "DELETED" || events[0].Cron.Name != "mine-cron" {
		t.Fatalf("poll 5 events = %v, want DELETED mine-cron", events)
	}

	// Poll 6: steady state emits nothing.
	events = nil
	if err := tick(); err != nil {
		t.Fatalf("tick 6: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("poll 6 events = %v, want none", events)
	}
}
