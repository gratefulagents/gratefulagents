package dashboard

import (
	"context"
	"math"
	"testing"
	"time"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestResourceReadsIncludePerResourceMetrics(t *testing.T) {
	scheme := testProjectScheme(t)

	project := &triggersv1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{Name: "payments", Namespace: "default", ResourceVersion: "1"},
		Spec: triggersv1alpha1.ProjectSpec{
			DisplayName: "Payments",
			Defaults:    triggersv1alpha1.AgentRunDefaults{RepoURL: "https://github.com/example/payments.git"},
		},
	}
	projectOtherNamespace := &triggersv1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{Name: "payments", Namespace: "other", ResourceVersion: "1"},
		Spec: triggersv1alpha1.ProjectSpec{
			DisplayName: "Payments Other",
			Defaults:    triggersv1alpha1.AgentRunDefaults{RepoURL: "https://github.com/example/other.git"},
		},
	}
	repo := &triggersv1alpha1.GitHubRepository{
		ObjectMeta: metav1.ObjectMeta{Name: "payments", Namespace: "default", ResourceVersion: "1"},
		Spec: triggersv1alpha1.GitHubRepositorySpec{
			GitHubTokenSecret: "github-token",
			Owner:             "example",
			Repo:              "payments",
			Defaults:          triggersv1alpha1.AgentRunDefaults{RepoURL: "https://github.com/example/payments.git"},
		},
	}
	linear := &triggersv1alpha1.LinearProject{
		ObjectMeta: metav1.ObjectMeta{Name: "payments", Namespace: "default", ResourceVersion: "1"},
		Spec: triggersv1alpha1.LinearProjectSpec{
			LinearAPIKeySecret: "linear-api-key",
			ProjectID:          "ENG",
			TeamID:             "TEAM",
			Defaults:           triggersv1alpha1.AgentRunDefaults{RepoURL: "https://github.com/example/payments.git"},
		},
	}
	cron := &triggersv1alpha1.Cron{
		ObjectMeta: metav1.ObjectMeta{Name: "payments-nightly", Namespace: "default", ResourceVersion: "1"},
		Spec: triggersv1alpha1.CronSpec{
			Schedule: "0 1 * * *",
			Prompt:   "Run nightly maintenance.",
			Defaults: triggersv1alpha1.AgentRunDefaults{RepoURL: "https://github.com/example/payments.git"},
		},
	}

	startedProjectSuccess := metav1.NewTime(time.Unix(1_700_000_000, 0))
	startedProjectFailure := metav1.NewTime(time.Unix(1_700_000_060, 0))
	startedGitHub := metav1.NewTime(time.Unix(1_700_000_120, 0))
	startedLinear := metav1.NewTime(time.Unix(1_700_000_180, 0))
	startedCron := metav1.NewTime(time.Unix(1_700_000_210, 0))
	startedOtherNamespace := metav1.NewTime(time.Unix(1_700_000_240, 0))

	projectRunSucceeded := resourceMetricsTestRun(
		"default",
		"project-run-succeeded",
		"Project",
		"payments",
		platformv1alpha1.AgentRunPhaseSucceeded,
		"1.25",
		120,
		30,
		3,
		&startedProjectSuccess,
	)
	projectRunFailed := resourceMetricsTestRun(
		"default",
		"project-run-failed",
		"Project",
		"payments",
		platformv1alpha1.AgentRunPhaseFailed,
		"0.75",
		80,
		20,
		2,
		&startedProjectFailure,
	)
	githubRunRunning := resourceMetricsTestRun(
		"default",
		"github-run-running",
		"GitHubRepository",
		"payments",
		platformv1alpha1.AgentRunPhaseRunning,
		"2.50",
		200,
		50,
		4,
		&startedGitHub,
	)
	linearRunFailed := resourceMetricsTestRun(
		"default",
		"linear-run-failed",
		"LinearProject",
		"payments",
		platformv1alpha1.AgentRunPhaseFailed,
		"3.00",
		300,
		60,
		5,
		&startedLinear,
	)
	cronRunSucceeded := resourceMetricsTestRun(
		"default",
		"cron-run-succeeded",
		"Cron",
		"payments-nightly",
		platformv1alpha1.AgentRunPhaseSucceeded,
		"1.50",
		150,
		45,
		7,
		&startedCron,
	)
	otherNamespaceRun := resourceMetricsTestRun(
		"other",
		"project-run-other",
		"Project",
		"payments",
		platformv1alpha1.AgentRunPhaseSucceeded,
		"4.00",
		400,
		70,
		6,
		&startedOtherNamespace,
	)

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(
			project,
			projectOtherNamespace,
			repo,
			linear,
			cron,
			projectRunSucceeded,
			projectRunFailed,
			githubRunRunning,
			linearRunFailed,
			cronRunSucceeded,
			otherNamespaceRun,
		).
		Build()
	srv := &Server{k8sClient: c, scheme: scheme}

	gotProject, err := srv.GetProject(context.Background(), &platform.GetProjectRequest{Namespace: "default", Name: "payments"})
	if err != nil {
		t.Fatalf("GetProject() error = %v", err)
	}
	assertMetrics(t, "Project.Get", gotProject.Metrics, 2, 1, 1, 0, 2.00, 1.00, 200, 50, 5, startedProjectFailure.Unix())

	projectList, err := srv.ListProjects(context.Background(), &platform.ListProjectsRequest{})
	if err != nil {
		t.Fatalf("ListProjects() error = %v", err)
	}
	if len(projectList.Projects) != 2 {
		t.Fatalf("ListProjects() len = %d, want 2", len(projectList.Projects))
	}
	for _, item := range projectList.Projects {
		switch item.Namespace {
		case "default":
			assertMetrics(t, "Project.List/default", item.Metrics, 2, 1, 1, 0, 2.00, 1.00, 200, 50, 5, startedProjectFailure.Unix())
		case "other":
			assertMetrics(t, "Project.List/other", item.Metrics, 1, 1, 0, 0, 4.00, 4.00, 400, 70, 6, startedOtherNamespace.Unix())
		default:
			t.Fatalf("unexpected Project namespace %q", item.Namespace)
		}
	}

	gotRepo, err := srv.GetGitHubRepository(context.Background(), &platform.GetGitHubRepositoryRequest{Namespace: "default", Name: "payments"})
	if err != nil {
		t.Fatalf("GetGitHubRepository() error = %v", err)
	}
	assertMetrics(t, "GitHub.Get", gotRepo.Metrics, 1, 0, 0, 1, 2.50, 2.50, 200, 50, 4, startedGitHub.Unix())

	repoList, err := srv.ListGitHubRepositories(context.Background(), &platform.ListGitHubRepositoriesRequest{Namespace: "default"})
	if err != nil {
		t.Fatalf("ListGitHubRepositories() error = %v", err)
	}
	if len(repoList.Repositories) != 1 {
		t.Fatalf("ListGitHubRepositories() len = %d, want 1", len(repoList.Repositories))
	}
	assertMetrics(t, "GitHub.List", repoList.Repositories[0].Metrics, 1, 0, 0, 1, 2.50, 2.50, 200, 50, 4, startedGitHub.Unix())

	gotLinear, err := srv.GetLinearProject(context.Background(), &platform.GetLinearProjectRequest{Namespace: "default", Name: "payments"})
	if err != nil {
		t.Fatalf("GetLinearProject() error = %v", err)
	}
	assertMetrics(t, "Linear.Get", gotLinear.Metrics, 1, 0, 1, 0, 3.00, 3.00, 300, 60, 5, startedLinear.Unix())

	linearList, err := srv.ListLinearProjects(context.Background(), &platform.ListLinearProjectsRequest{Namespace: "default"})
	if err != nil {
		t.Fatalf("ListLinearProjects() error = %v", err)
	}
	if len(linearList.Projects) != 1 {
		t.Fatalf("ListLinearProjects() len = %d, want 1", len(linearList.Projects))
	}
	assertMetrics(t, "Linear.List", linearList.Projects[0].Metrics, 1, 0, 1, 0, 3.00, 3.00, 300, 60, 5, startedLinear.Unix())

	gotCron, err := srv.GetCron(context.Background(), &platform.GetCronRequest{Namespace: "default", Name: "payments-nightly"})
	if err != nil {
		t.Fatalf("GetCron() error = %v", err)
	}
	assertMetrics(t, "Cron.Get", gotCron.Metrics, 1, 1, 0, 0, 1.50, 1.50, 150, 45, 7, startedCron.Unix())

	cronList, err := srv.ListCrons(context.Background(), &platform.ListCronsRequest{Namespace: "default"})
	if err != nil {
		t.Fatalf("ListCrons() error = %v", err)
	}
	if len(cronList.Crons) != 1 {
		t.Fatalf("ListCrons() len = %d, want 1", len(cronList.Crons))
	}
	assertMetrics(t, "Cron.List", cronList.Crons[0].Metrics, 1, 1, 0, 0, 1.50, 1.50, 150, 45, 7, startedCron.Unix())
}

func resourceMetricsTestRun(namespace, name, kind, projectName string, phase platformv1alpha1.AgentRunPhase, cost string, inputTokens, outputTokens int64, toolCalls int32, startedAt *metav1.Time) *platformv1alpha1.AgentRun {
	return &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: platformv1alpha1.AgentRunSpec{
			Context: &platformv1alpha1.AgentRunContext{
				ProjectRef: &platformv1alpha1.ProjectRef{Kind: kind, Name: projectName},
			},
			Trigger: platformv1alpha1.TriggerRef{Kind: kind, Name: projectName},
		},
		Status: platformv1alpha1.AgentRunStatus{
			Phase:     phase,
			StartedAt: startedAt,
			Metrics: &platformv1alpha1.AgentRunMetrics{
				CostUsd:       cost,
				InputTokens:   inputTokens,
				OutputTokens:  outputTokens,
				ToolCallCount: toolCalls,
			},
		},
	}
}

func assertMetrics(t *testing.T, label string, got *platform.ProjectMetrics, totalRuns, successfulRuns, failedRuns, runningRuns int32, totalCost, avgCost float64, inputTokens, outputTokens int64, toolCalls int32, lastRunAt int64) {
	t.Helper()
	if got == nil {
		t.Fatalf("%s metrics = nil", label)
	}
	if got.TotalRuns != totalRuns || got.SuccessfulRuns != successfulRuns || got.FailedRuns != failedRuns || got.RunningRuns != runningRuns {
		t.Fatalf("%s counts = %#v, want runs=%d success=%d failed=%d running=%d", label, got, totalRuns, successfulRuns, failedRuns, runningRuns)
	}
	if math.Abs(got.TotalCostUsd-totalCost) > 1e-9 {
		t.Fatalf("%s TotalCostUsd = %v, want %v", label, got.TotalCostUsd, totalCost)
	}
	if math.Abs(got.AverageCostPerRun-avgCost) > 1e-9 {
		t.Fatalf("%s AverageCostPerRun = %v, want %v", label, got.AverageCostPerRun, avgCost)
	}
	if got.TotalInputTokens != inputTokens || got.TotalOutputTokens != outputTokens || got.TotalToolCalls != toolCalls || got.LastRunAtUnix != lastRunAt {
		t.Fatalf("%s usage = %#v, want input=%d output=%d toolCalls=%d lastRunAt=%d", label, got, inputTokens, outputTokens, toolCalls, lastRunAt)
	}
}
