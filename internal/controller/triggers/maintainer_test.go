package triggers

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/go-github/v68/github"
	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/githubapp"
	"github.com/gratefulagents/gratefulagents/internal/orchestration"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

type maintainerTokenMinter struct{}

func (maintainerTokenMinter) MintInstallationToken(context.Context, int64, int64, []byte) (string, error) {
	return "maintainer-installation-token", nil
}

func newMaintainerEngine(t *testing.T, now *time.Time, objects ...client.Object) (*MaintainerEngine, client.Client, *prLoopStateStore) {
	t.Helper()
	scheme := prLoopTestScheme(t)
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&platformv1alpha1.AgentRun{}, &triggersv1alpha1.GitHubRepository{}).
		WithObjects(objects...).
		Build()
	store := newPRLoopStateStore()
	engine := &MaintainerEngine{Client: c, Scheme: scheme, StateStore: store}
	if now != nil {
		engine.Now = func() time.Time { return *now }
	}
	return engine, c, store
}

func maintainerRepository() *triggersv1alpha1.GitHubRepository {
	repo := prLoopTestRepo()
	repo.Spec.ReviewLoop = nil
	repo.Spec.Maintainer = &triggersv1alpha1.MaintainerSpec{}
	return repo
}

func maintainerMode() *platformv1alpha1.ModeTemplate {
	return &platformv1alpha1.ModeTemplate{ObjectMeta: metav1.ObjectMeta{Name: defaultMaintainerModeName}}
}

func standingMaintainer(t *testing.T, c client.Client, namespace, repositoryName string) *platformv1alpha1.AgentRun {
	t.Helper()
	run := &platformv1alpha1.AgentRun{}
	key := client.ObjectKey{Namespace: namespace, Name: orchestration.StandingRunName(repositoryName, orchestration.StandingRunRoleMaintainer)}
	if err := c.Get(context.Background(), key, run); err != nil {
		t.Fatalf("Get(maintainer run): %v", err)
	}
	return run
}

func setMaintainerPhase(t *testing.T, c client.Client, run *platformv1alpha1.AgentRun, phase platformv1alpha1.AgentRunPhase) {
	t.Helper()
	run.Status.Phase = phase
	if err := c.Status().Update(context.Background(), run); err != nil {
		t.Fatalf("Update(maintainer phase): %v", err)
	}
}

func TestMaintainerCreatesStandingRunOnceWithFencesAndSeed(t *testing.T) {
	t.Parallel()
	repository := maintainerRepository()
	repository.Spec.Defaults.Secrets.GithubToken = "different-default-token"
	engine, c, stateStore := newMaintainerEngine(t, nil, repository, maintainerMode())

	if _, err := engine.Reconcile(context.Background(), repository, nil, true); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	run := standingMaintainer(t, c, repository.Namespace, repository.Name)
	if run.Labels[orchestration.StandingRunRoleLabel] != orchestration.StandingRunRoleMaintainer || run.Labels[orchestration.SupervisedRunLabel] != repository.Name {
		t.Fatalf("standing labels = %#v", run.Labels)
	}
	if run.Spec.Trigger.Kind != gitHubRepositoryTriggerKind || run.Spec.Trigger.Name != repository.Name || run.Spec.Trigger.ExternalRef != nil {
		t.Fatalf("trigger = %#v, want GitHubRepository repository without external reference", run.Spec.Trigger)
	}
	if run.Spec.Secrets == nil || run.Spec.Secrets.GitHubTokenSecret != repository.Spec.GitHubTokenSecret {
		t.Fatalf("GitHub token secret = %#v, want repository connection secret %q", run.Spec.Secrets, repository.Spec.GitHubTokenSecret)
	}
	if run.Spec.Overseer != nil || run.Labels[PRLoopRoleLabel] != "" || run.Annotations[PRLoopOptAnnotation] != "" {
		t.Fatalf("maintainer recursion fences missing: spec=%#v labels=%#v annotations=%#v", run.Spec.Overseer, run.Labels, run.Annotations)
	}
	if got := stateStore.messagesFor(run.Name, run.Namespace); len(got) != 1 || !strings.Contains(got[0], "Use wait_for_repo_events with long timeouts") || !strings.Contains(got[0], "a blocked wait costs nothing") {
		t.Fatalf("seed messages = %#v, want one continuous-loop dossier", got)
	}

	if _, err := engine.Reconcile(context.Background(), repository, nil, true); err != nil {
		t.Fatalf("second Reconcile() error = %v", err)
	}
	if got := stateStore.messagesFor(run.Name, run.Namespace); len(got) != 1 {
		t.Fatalf("seed messages after second reconcile = %#v, want one", got)
	}
}

func TestMaintainerCountsUnresolvedPullRequests(t *testing.T) {
	t.Parallel()
	repository := maintainerRepository()
	monitor := func(name string, state triggersv1alpha1.PullRequestMonitorState, repositoryName string) *triggersv1alpha1.PullRequestMonitor {
		return &triggersv1alpha1.PullRequestMonitor{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: repository.Namespace},
			Spec: triggersv1alpha1.PullRequestMonitorSpec{
				GitHubRepositoryRef: &corev1.LocalObjectReference{Name: repositoryName},
			},
			Status: triggersv1alpha1.PullRequestMonitorStatus{State: state},
		}
	}
	engine, _, _ := newMaintainerEngine(t, nil,
		repository,
		monitor("open", triggersv1alpha1.PullRequestMonitorStateOpen, repository.Name),
		monitor("approved", triggersv1alpha1.PullRequestMonitorStateApproved, repository.Name),
		monitor("merged", triggersv1alpha1.PullRequestMonitorStateMerged, repository.Name),
		monitor("foreign", triggersv1alpha1.PullRequestMonitorStateOpen, "other"),
	)
	got, err := engine.countActivePullRequests(context.Background(), repository)
	if err != nil {
		t.Fatal(err)
	}
	if got != 2 {
		t.Fatalf("active pull requests = %d, want 2", got)
	}
}

func TestMaintainerDoesNotRunWhenDisabledOrNotEnabled(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		enabled bool
		config  *triggersv1alpha1.MaintainerSpec
	}{
		{name: "flag off", config: &triggersv1alpha1.MaintainerSpec{}},
		{name: "configuration absent", enabled: true},
		{name: "configuration disabled", enabled: true, config: &triggersv1alpha1.MaintainerSpec{Disabled: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repository := maintainerRepository()
			repository.Spec.Maintainer = tc.config
			engine, c, _ := newMaintainerEngine(t, nil, repository, maintainerMode())
			reconciler := &GitHubRepositoryReconciler{Client: c, MaintainerEnabled: tc.enabled, MaintainerEngine: engine}
			if _, err := reconciler.reconcileMaintainer(context.Background(), repository, nil, true); err != nil {
				t.Fatalf("reconcileMaintainer() error = %v", err)
			}
			runs := &platformv1alpha1.AgentRunList{}
			if err := c.List(context.Background(), runs, client.InNamespace(repository.Namespace)); err != nil {
				t.Fatalf("List(AgentRuns): %v", err)
			}
			if len(runs.Items) != 0 {
				t.Fatalf("AgentRuns = %#v, want none", runs.Items)
			}
		})
	}
}

func TestMaintainerResumeNudges(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name        string
		phase       platformv1alpha1.AgentRunPhase
		issues      []*github.Issue
		lastResume  time.Duration
		standupDue  bool
		wantNudge   bool
		wantRequeue time.Duration
	}{
		{name: "terminal run with open work after cooldown", phase: platformv1alpha1.AgentRunPhaseSucceeded, issues: []*github.Issue{{Number: github.Int(42)}}, wantNudge: true, wantRequeue: 5 * time.Minute},
		{name: "running run", phase: platformv1alpha1.AgentRunPhaseRunning, issues: []*github.Issue{{Number: github.Int(42)}}, wantRequeue: defaultMaintainerStandupInterval},
		{name: "terminal run without work", phase: platformv1alpha1.AgentRunPhaseSucceeded, wantRequeue: defaultMaintainerStandupInterval},
		{name: "within cooldown", phase: platformv1alpha1.AgentRunPhaseSucceeded, issues: []*github.Issue{{Number: github.Int(42)}}, lastResume: -9 * time.Minute, wantRequeue: 5 * time.Minute},
		{name: "standup", phase: platformv1alpha1.AgentRunPhasePaused, standupDue: true, wantNudge: true, wantRequeue: defaultMaintainerStandupInterval},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Date(2026, 3, 8, 12, 0, 0, 0, time.UTC)
			repository := maintainerRepository()
			engine, c, stateStore := newMaintainerEngine(t, &now, repository, maintainerMode())
			if _, err := engine.Reconcile(context.Background(), repository, nil, true); err != nil {
				t.Fatalf("initial Reconcile() error = %v", err)
			}
			standing := standingMaintainer(t, c, repository.Namespace, repository.Name)
			setMaintainerPhase(t, c, standing, tc.phase)
			if tc.lastResume != 0 {
				standing = standingMaintainer(t, c, repository.Namespace, repository.Name)
				standing.Annotations[maintainerLastResumeAnnotation] = now.Add(tc.lastResume).Format(time.RFC3339)
				if err := c.Update(context.Background(), standing); err != nil {
					t.Fatalf("Update(last resume): %v", err)
				}
			}
			updatedRepository := &triggersv1alpha1.GitHubRepository{}
			if err := c.Get(context.Background(), client.ObjectKeyFromObject(repository), updatedRepository); err != nil {
				t.Fatalf("Get(repository): %v", err)
			}
			if tc.standupDue {
				wakeTime := metav1.NewTime(now.Add(-maintainerStandupInterval(updatedRepository)))
				updatedRepository.Status.Maintainer.LastWakeTime = &wakeTime
				if err := c.Status().Update(context.Background(), updatedRepository); err != nil {
					t.Fatalf("Update(last wake): %v", err)
				}
			}
			result, err := engine.Reconcile(context.Background(), updatedRepository, tc.issues, true)
			if err != nil {
				t.Fatalf("Reconcile() error = %v", err)
			}
			if result.RequeueAfter != tc.wantRequeue {
				t.Fatalf("RequeueAfter = %s, want %s", result.RequeueAfter, tc.wantRequeue)
			}
			standing = standingMaintainer(t, c, repository.Namespace, repository.Name)
			if got := standing.Spec.WakeRequests; (got == 1) != tc.wantNudge {
				t.Fatalf("WakeRequests = %d, want nudge=%t", got, tc.wantNudge)
			}
			messages := stateStore.messagesFor(standing.Name, standing.Namespace)
			wantMessages := 1
			if tc.wantNudge {
				wantMessages++
			}
			if len(messages) != wantMessages {
				t.Fatalf("messages = %#v, want nudge=%t", messages, tc.wantNudge)
			}
			if tc.wantNudge {
				if got := standing.Annotations[maintainerLastResumeAnnotation]; got != now.Format(time.RFC3339) {
					t.Fatalf("last resume = %q, want %q", got, now.Format(time.RFC3339))
				}
				if !strings.Contains(messages[1], "Maintainer resume:") {
					t.Fatalf("nudge message = %q", messages[1])
				}
			}
		})
	}
}

func TestMaintainerRoutesReportOnceAndMirrorsLedger(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 3, 8, 12, 0, 0, 0, time.UTC)
	repository := maintainerRepository()
	engine, c, _ := newMaintainerEngine(t, &now, repository, maintainerMode())
	if _, err := engine.Reconcile(context.Background(), repository, nil, true); err != nil {
		t.Fatalf("initial Reconcile() error = %v", err)
	}
	standing := standingMaintainer(t, c, repository.Namespace, repository.Name)
	standing.Annotations[triggersv1alpha1.MaintainerReportAnnotation] = `{"summary":"backlog is healthy","state":"healthy","time":"2026-03-08T12:00:00Z"}`
	standing.Annotations[triggersv1alpha1.MaintainerDispatchLedgerAnnotation] = `{"day":"2026-03-08","count":3,"issues":[1,2,3]}`
	if err := c.Update(context.Background(), standing); err != nil {
		t.Fatalf("Update(maintainer annotations): %v", err)
	}
	if _, err := engine.Reconcile(context.Background(), repository, nil, true); err != nil {
		t.Fatalf("report Reconcile() error = %v", err)
	}
	updatedRepository := &triggersv1alpha1.GitHubRepository{}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(repository), updatedRepository); err != nil {
		t.Fatalf("Get(repository): %v", err)
	}
	status := updatedRepository.Status.Maintainer
	if status == nil || status.LastReportState != triggersv1alpha1.MaintainerReportStateHealthy || status.LastReportSummary != "backlog is healthy" || status.DispatchesToday != 3 {
		t.Fatalf("maintainer status = %#v", status)
	}
	standing = standingMaintainer(t, c, repository.Namespace, repository.Name)
	handled := standing.Annotations[maintainerReportHandledAnnotation]
	if handled == "" {
		t.Fatal("report was not marked handled")
	}
	if _, err := engine.Reconcile(context.Background(), updatedRepository, nil, true); err != nil {
		t.Fatalf("second report Reconcile() error = %v", err)
	}
	standing = standingMaintainer(t, c, repository.Namespace, repository.Name)
	if got := standing.Annotations[maintainerReportHandledAnnotation]; got != handled {
		t.Fatalf("handled report changed from %q to %q", handled, got)
	}
}

func TestMaintainerEnsuresGitHubAppSecret(t *testing.T) {
	t.Parallel()
	repository := maintainerRepository()
	repository.Spec.GitHubTokenSecret = ""
	repository.Spec.GitHubApp = &triggersv1alpha1.GitHubAppAuth{AppID: 1, InstallationID: 2, PrivateKeySecret: "app-key"}
	privateKey := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "app-key", Namespace: repository.Namespace}, Data: map[string][]byte{githubapp.PrivateKeySecretKey: []byte("private key")}}
	engine, c, _ := newMaintainerEngine(t, nil, repository, maintainerMode(), privateKey)
	engine.GitHubAppMinter = maintainerTokenMinter{}
	if _, err := engine.Reconcile(context.Background(), repository, nil, true); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	run := standingMaintainer(t, c, repository.Namespace, repository.Name)
	secret := &corev1.Secret{}
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: run.Namespace, Name: run.Name + "-gh-token"}, secret); err != nil {
		t.Fatalf("Get(GitHub App token Secret): %v", err)
	}
	if got := string(secret.Data[githubapp.TokenSecretKey]); got != "maintainer-installation-token" {
		t.Fatalf("token = %q, want minted token", got)
	}
}

func TestMaintainerLedgerParsing(t *testing.T) {
	t.Parallel()
	ledger, err := parseMaintainerDispatchLedger(`{"day":"2026-03-08","count":2,"issues":[1,2]}`)
	if err != nil || ledger.Count != 2 || len(ledger.Issues) != 2 {
		t.Fatalf("parseMaintainerDispatchLedger() = %#v, %v", ledger, err)
	}
	if _, err := parseMaintainerDispatchLedger(`{"day":"not-a-day","count":-1}`); err == nil {
		t.Fatal("parseMaintainerDispatchLedger() accepted invalid ledger")
	}
}
