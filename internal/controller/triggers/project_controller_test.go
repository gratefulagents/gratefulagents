//nolint:goconst // Repeated identifiers are intentional test fixtures.
package triggers

import (
	"context"
	"testing"
	"time"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestProjectReconcilerCompilesTriggers(t *testing.T) {
	t.Parallel()
	scheme := projectTestScheme(t)
	project := projectWithTriggers()
	githubConnection := &triggersv1alpha1.Connection{
		ObjectMeta: metav1.ObjectMeta{Name: "github", Namespace: project.Namespace},
		Spec: triggersv1alpha1.ConnectionSpec{
			Type:   triggersv1alpha1.ConnectionTypeGitHub,
			GitHub: &triggersv1alpha1.GitHubConnectionConfig{TokenSecret: "github-token"},
		},
	}
	slackConnection := &triggersv1alpha1.Connection{
		ObjectMeta: metav1.ObjectMeta{Name: "slack", Namespace: project.Namespace},
		Spec: triggersv1alpha1.ConnectionSpec{
			Type: triggersv1alpha1.ConnectionTypeSlack,
			Slack: &triggersv1alpha1.SlackConnectionConfig{
				TokensSecret: "slack-tokens",
				TeamID:       "T123",
				SlackUserID:  "UOWNER1",
			},
		},
	}
	linearConnection := &triggersv1alpha1.Connection{
		ObjectMeta: metav1.ObjectMeta{Name: "linear", Namespace: project.Namespace},
		Spec: triggersv1alpha1.ConnectionSpec{
			Type:   triggersv1alpha1.ConnectionTypeLinear,
			Linear: &triggersv1alpha1.LinearConnectionConfig{APIKeySecret: "linear-key"},
		},
	}
	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&triggersv1alpha1.Project{}).
		WithObjects(project, githubConnection, slackConnection, linearConnection).
		Build()

	reconciler := &ProjectReconciler{Client: k8sClient, Scheme: scheme}
	compiled, err := reconciler.compileTrigger(context.Background(), project, project.Spec.Triggers[0])
	if err != nil {
		t.Fatalf("compileTrigger() error = %v", err)
	}
	compiledGitHub := compiled.(*triggersv1alpha1.GitHubRepository)
	if compiledGitHub.Spec.Maintainer == project.Spec.Triggers[0].GitHub.Maintainer || compiledGitHub.Spec.Maintainer.ModeRef == project.Spec.Triggers[0].GitHub.Maintainer.ModeRef {
		t.Fatal("compiled GitHubRepository maintainer aliases Project trigger configuration")
	}
	if _, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(project)}); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}

	github := &triggersv1alpha1.GitHubRepository{}
	if err := k8sClient.Get(context.Background(), types.NamespacedName{Namespace: project.Namespace, Name: projectGeneratedChildName(project.Name, "issues")}, github); err != nil {
		t.Fatalf("Get(GitHubRepository) error = %v", err)
	}
	if github.Spec.GitHubTokenSecret != "github-token" || github.Spec.Owner != "acme" || github.Spec.Repo != "widgets" {
		t.Fatalf("GitHubRepository spec = %#v", github.Spec)
	}
	maintainer := github.Spec.Maintainer
	if maintainer == nil || maintainer.ModeRef == nil || maintainer.ModeRef.Name != "repository-maintainer" || maintainer.Model != "gpt-5" ||
		maintainer.MaxConcurrentDispatches != 3 || maintainer.MaxDispatchesPerDay != 12 || maintainer.StandupInterval == nil ||
		maintainer.StandupInterval.Duration != 6*time.Hour || !maintainer.AllowPullRequestMerge {
		t.Fatalf("GitHubRepository maintainer = %#v", maintainer)
	}
	if !github.Spec.Defaults.KubernetesAdmin || github.Labels[projectGeneratedRuntimeLabel] != "true" || github.Labels[projectTriggerNameLabel] != "issues" {
		t.Fatalf("GitHubRepository metadata/defaults = %#v/%#v", github.ObjectMeta, github.Spec.Defaults)
	}
	if owner := metav1.GetControllerOf(github); owner == nil || owner.UID != project.UID {
		t.Fatalf("GitHubRepository controller owner = %#v, want Project UID %q", owner, project.UID)
	}

	slack := &triggersv1alpha1.SlackAgent{}
	if err := k8sClient.Get(context.Background(), types.NamespacedName{Namespace: project.Namespace, Name: projectGeneratedChildName(project.Name, "team-chat")}, slack); err != nil {
		t.Fatalf("Get(SlackAgent) error = %v", err)
	}
	if slack.Spec.TokensSecret != "slack-tokens" || slack.Spec.SlackUserID != "UOWNER1" || slack.Annotations[projectSlackChannelAnnotation] != "C123" || slack.Annotations[projectSlackTeamIDAnnotation] != "T123" {
		t.Fatalf("SlackAgent identity/annotations = %#v/%#v", slack.Spec, slack.Annotations)
	}
	if slack.Spec.ChannelReplyMode != triggersv1alpha1.SlackChannelReplyAuto || len(slack.Spec.Commanders) != 1 || slack.Spec.Commanders[0] != "UHELPER1" || slack.Spec.SessionIdleMinutes == nil || *slack.Spec.SessionIdleMinutes != 90 {
		t.Fatalf("SlackAgent behavior = %#v", slack.Spec)
	}

	cron := &triggersv1alpha1.Cron{}
	if err := k8sClient.Get(context.Background(), types.NamespacedName{Namespace: project.Namespace, Name: projectGeneratedChildName(project.Name, "nightly")}, cron); err != nil {
		t.Fatalf("Get(Cron) error = %v", err)
	}
	if cron.Spec.Schedule != "0 2 * * *" || cron.Spec.Prompt != "Review the queue" || cron.Spec.Defaults.Model != project.Spec.Defaults.Model {
		t.Fatalf("Cron spec = %#v", cron.Spec)
	}

	linear := &triggersv1alpha1.LinearProject{}
	if err := k8sClient.Get(context.Background(), types.NamespacedName{Namespace: project.Namespace, Name: projectGeneratedChildName(project.Name, "linear")}, linear); err != nil {
		t.Fatalf("Get(LinearProject) error = %v", err)
	}
	if linear.Spec.LinearAPIKeySecret != "linear-key" || linear.Spec.ProjectID != "project-1" || !linear.Spec.AutoCreateTasks {
		t.Fatalf("LinearProject spec = %#v", linear.Spec)
	}
}

func TestProjectReconcilerRemovesDisabledAndStaleGeneratedChildren(t *testing.T) {
	t.Parallel()
	scheme := projectTestScheme(t)
	enabled := true
	project := &triggersv1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{Name: "project", Namespace: "default", UID: types.UID("project-uid")},
		Spec: triggersv1alpha1.ProjectSpec{Triggers: []triggersv1alpha1.ProjectTrigger{
			{Name: "disabled", Type: triggersv1alpha1.ProjectTriggerTypeCron, Enabled: &[]bool{false}[0], Cron: &triggersv1alpha1.CronProjectTriggerConfig{Schedule: "0 1 * * *", Prompt: "disabled"}},
			{Name: "active", Type: triggersv1alpha1.ProjectTriggerTypeCron, Enabled: &enabled, Cron: &triggersv1alpha1.CronProjectTriggerConfig{Schedule: "0 2 * * *", Prompt: "active"}},
		}},
	}
	stale := &triggersv1alpha1.Cron{
		ObjectMeta: metav1.ObjectMeta{
			Name:      projectGeneratedChildName(project.Name, "disabled"),
			Namespace: project.Namespace,
			Labels: map[string]string{
				projectGeneratedRuntimeLabel: "true",
				projectUIDLabel:              projectLabelValue(string(project.UID)),
			},
			OwnerReferences: []metav1.OwnerReference{{APIVersion: triggersv1alpha1.GroupVersion.String(), Kind: "Project", Name: project.Name, UID: project.UID, Controller: &[]bool{true}[0]}},
		},
	}
	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&triggersv1alpha1.Project{}).
		WithObjects(project, stale).
		Build()

	reconciler := &ProjectReconciler{Client: k8sClient, Scheme: scheme}
	if _, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(project)}); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if err := k8sClient.Get(context.Background(), client.ObjectKeyFromObject(stale), &triggersv1alpha1.Cron{}); err == nil {
		t.Fatal("stale disabled Cron still exists")
	}
	active := &triggersv1alpha1.Cron{}
	if err := k8sClient.Get(context.Background(), types.NamespacedName{Namespace: project.Namespace, Name: projectGeneratedChildName(project.Name, "active")}, active); err != nil {
		t.Fatalf("Get(active Cron) error = %v", err)
	}
}

func TestProjectReconcilerNormalizesChildStatus(t *testing.T) {
	t.Parallel()
	scheme := projectTestScheme(t)
	project := &triggersv1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{Name: "project", Namespace: "default", UID: types.UID("project-uid"), Generation: 4},
		Spec: triggersv1alpha1.ProjectSpec{Triggers: []triggersv1alpha1.ProjectTrigger{{
			Name: "nightly", Type: triggersv1alpha1.ProjectTriggerTypeCron, Cron: &triggersv1alpha1.CronProjectTriggerConfig{Schedule: "0 2 * * *", Prompt: "Review the queue"},
		}}},
	}
	child := &triggersv1alpha1.Cron{
		ObjectMeta: metav1.ObjectMeta{
			Name:      projectGeneratedChildName(project.Name, "nightly"),
			Namespace: project.Namespace,
			Labels: map[string]string{
				projectGeneratedRuntimeLabel: "true",
				projectUIDLabel:              projectLabelValue(string(project.UID)),
			},
			OwnerReferences: []metav1.OwnerReference{{APIVersion: triggersv1alpha1.GroupVersion.String(), Kind: "Project", Name: project.Name, UID: project.UID, Controller: &[]bool{true}[0]}},
		},
		Status: triggersv1alpha1.CronStatus{Conditions: []metav1.Condition{{Type: "Ready", Status: metav1.ConditionTrue, Reason: "Scheduled"}}},
	}
	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&triggersv1alpha1.Project{}, &triggersv1alpha1.Cron{}).
		WithObjects(project, child).
		Build()

	reconciler := &ProjectReconciler{Client: k8sClient, Scheme: scheme}
	if _, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(project)}); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	updated := &triggersv1alpha1.Project{}
	if err := k8sClient.Get(context.Background(), client.ObjectKeyFromObject(project), updated); err != nil {
		t.Fatalf("Get(Project) error = %v", err)
	}
	if len(updated.Status.Triggers) != 1 || updated.Status.Triggers[0].Name != "nightly" || updated.Status.Triggers[0].Conditions[0].Status != metav1.ConditionTrue {
		t.Fatalf("Project trigger status = %#v", updated.Status.Triggers)
	}
	if condition := meta.FindStatusCondition(updated.Status.Conditions, "Ready"); condition == nil || condition.Status != metav1.ConditionTrue {
		t.Fatalf("Project Ready condition = %#v", updated.Status.Conditions)
	}
}

func projectTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := triggersv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(triggers) error = %v", err)
	}
	return scheme
}

func projectWithTriggers() *triggersv1alpha1.Project {
	idle := int32(90)
	return &triggersv1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{Name: "Payments Service", Namespace: "default", UID: types.UID("project-uid")},
		Spec: triggersv1alpha1.ProjectSpec{
			KubernetesAdmin: true,
			Defaults:        triggersv1alpha1.AgentRunDefaults{Model: "gpt-4.1"},
			Triggers: []triggersv1alpha1.ProjectTrigger{
				{Name: "issues", Type: triggersv1alpha1.ProjectTriggerTypeGitHub, GitHub: &triggersv1alpha1.GitHubProjectTriggerConfig{
					ConnectionRef: triggersv1alpha1.ConnectionRef{Name: "github"}, Owner: "acme", Repo: "widgets", Issues: true, Comments: true,
					Maintainer: &triggersv1alpha1.MaintainerSpec{
						ModeRef: &platformv1alpha1.ModeRef{Name: "repository-maintainer"}, Model: "gpt-5",
						MaxConcurrentDispatches: 3, MaxDispatchesPerDay: 12,
						StandupInterval: &metav1.Duration{Duration: 6 * time.Hour}, AllowPullRequestMerge: true,
					},
				}},
				{Name: "team-chat", Type: triggersv1alpha1.ProjectTriggerTypeSlack, Slack: &triggersv1alpha1.SlackProjectTriggerConfig{ConnectionRef: triggersv1alpha1.ConnectionRef{Name: "slack"}, Channel: "C123", ChannelReplyMode: triggersv1alpha1.SlackChannelReplyAuto, Commanders: []string{"UHELPER1"}, SessionIdleMinutes: &idle}},
				{Name: "nightly", Type: triggersv1alpha1.ProjectTriggerTypeCron, Cron: &triggersv1alpha1.CronProjectTriggerConfig{Schedule: "0 2 * * *", Prompt: "Review the queue"}},
				{Name: "linear", Type: triggersv1alpha1.ProjectTriggerTypeLinear, Linear: &triggersv1alpha1.LinearProjectTriggerConfig{ConnectionRef: triggersv1alpha1.ConnectionRef{Name: "linear"}, ProjectID: "project-1", TeamID: "team-1", AutoCreate: true}},
			},
		},
	}
}
