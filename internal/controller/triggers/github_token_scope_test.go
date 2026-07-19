package triggers

import (
	"context"
	"testing"

	"github.com/google/go-github/v68/github"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
)

const fullScopeTestToken = "full-token"

type fakeScopedMinter struct {
	scopedCalls int
	fullCalls   int
	lastPerms   *github.InstallationPermissions
}

func (f *fakeScopedMinter) MintInstallationToken(context.Context, int64, int64, []byte) (string, error) {
	f.fullCalls++
	return fullScopeTestToken, nil
}

func (f *fakeScopedMinter) MintScopedInstallationToken(_ context.Context, _, _ int64, _ []byte, perms *github.InstallationPermissions) (string, error) {
	f.scopedCalls++
	f.lastPerms = perms
	return "scoped-token", nil
}

func TestMintRunInstallationTokenDownscopesReviewerRuns(t *testing.T) {
	t.Parallel()
	gh := &triggersv1alpha1.GitHubRepository{Spec: triggersv1alpha1.GitHubRepositorySpec{
		GitHubApp: &triggersv1alpha1.GitHubAppAuth{AppID: 1, InstallationID: 2},
	}}

	reviewer := &platformv1alpha1.AgentRun{ObjectMeta: metav1.ObjectMeta{
		Name: "rev", Labels: map[string]string{triggersv1alpha1.PRLoopRoleLabelKey: triggersv1alpha1.PRLoopRoleReviewerValue},
	}}
	implementer := &platformv1alpha1.AgentRun{ObjectMeta: metav1.ObjectMeta{Name: "impl"}}

	m := &fakeScopedMinter{}
	token, err := mintRunInstallationToken(context.Background(), m, gh, reviewer, []byte("pem"))
	if err != nil || token != "scoped-token" {
		t.Fatalf("reviewer mint = (%q, %v), want scoped-token", token, err)
	}
	if m.scopedCalls != 1 || m.lastPerms == nil {
		t.Fatalf("scopedCalls = %d, perms = %v", m.scopedCalls, m.lastPerms)
	}
	if got := m.lastPerms.GetContents(); got != "read" {
		t.Fatalf("contents permission = %q, want read", got)
	}
	if got := m.lastPerms.GetPullRequests(); got != "write" {
		t.Fatalf("pull_requests permission = %q, want write", got)
	}

	token, err = mintRunInstallationToken(context.Background(), m, gh, implementer, []byte("pem"))
	if err != nil || token != fullScopeTestToken {
		t.Fatalf("implementer mint = (%q, %v), want full-token", token, err)
	}
	if m.fullCalls != 1 {
		t.Fatalf("fullCalls = %d, want 1", m.fullCalls)
	}
}

type fullOnlyMinter struct{ calls int }

func (f *fullOnlyMinter) MintInstallationToken(context.Context, int64, int64, []byte) (string, error) {
	f.calls++
	return fullScopeTestToken, nil
}

func TestMintRunInstallationTokenFailsClosedWithoutScopedSupport(t *testing.T) {
	t.Parallel()
	gh := &triggersv1alpha1.GitHubRepository{Spec: triggersv1alpha1.GitHubRepositorySpec{
		GitHubApp: &triggersv1alpha1.GitHubAppAuth{AppID: 1, InstallationID: 2},
	}}
	reviewer := &platformv1alpha1.AgentRun{ObjectMeta: metav1.ObjectMeta{
		Name: "rev", Labels: map[string]string{triggersv1alpha1.PRLoopRoleLabelKey: triggersv1alpha1.PRLoopRoleReviewerValue},
	}}
	m := &fullOnlyMinter{}
	token, err := mintRunInstallationToken(context.Background(), m, gh, reviewer, []byte("pem"))
	if err == nil || token != "" || m.calls != 0 {
		t.Fatalf("reviewer mint = (%q, %v, calls %d), want fail-closed without full-scope mint", token, err, m.calls)
	}
}
