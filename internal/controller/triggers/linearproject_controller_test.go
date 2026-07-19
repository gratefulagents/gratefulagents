package triggers

import (
	"context"
	"errors"
	"testing"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/linear"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

type fakeLinearClient struct {
	issues         []linear.Issue
	removed        []string
	added          []string
	labelIDsByName map[string]string
}

func (f *fakeLinearClient) FetchIssuesByLabel(ctx context.Context, projectID, labelName string) ([]linear.Issue, error) {
	return f.issues, nil
}

func (f *fakeLinearClient) GetLabelID(ctx context.Context, teamID, labelName string) (string, error) {
	return f.labelIDsByName[labelName], nil
}

func (f *fakeLinearClient) AddLabel(ctx context.Context, issueID, labelID string) error {
	f.added = append(f.added, issueID+":"+labelID)
	return nil
}

func (f *fakeLinearClient) RemoveLabel(ctx context.Context, issueID, labelID string) error {
	f.removed = append(f.removed, issueID+":"+labelID)
	return nil
}

func (f *fakeLinearClient) AddComment(ctx context.Context, issueID, body string) error {
	return nil
}

func (f *fakeLinearClient) CreateIssue(ctx context.Context, input linear.CreateIssueInput) (*linear.CreatedIssue, error) {
	return nil, nil
}

type failingCreateClient struct {
	client.Client
}

func (c *failingCreateClient) Create(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
	if _, ok := obj.(*platformv1alpha1.AgentRun); ok {
		return errors.New("transient create failure")
	}
	return c.Client.Create(ctx, obj, opts...)
}

func TestCreateAgentRunAppliesTeamDefaults(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(platform): %v", err)
	}
	if err := triggersv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(triggers): %v", err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(core): %v", err)
	}

	project := &triggersv1alpha1.LinearProject{
		ObjectMeta: metav1.ObjectMeta{Name: "payments-app", Namespace: "default"},
		Spec: triggersv1alpha1.LinearProjectSpec{
			LinearAPIKeySecret: "linear-secret",
			ProjectID:          "proj",
			TeamID:             "team",
			Defaults: triggersv1alpha1.AgentRunDefaults{
				RepoURL:       "https://github.com/example/repo.git",
				BaseBranch:    "main",
				Image:         "ghcr.io/example/worker:latest",
				Model:         "gpt-4.1",
				ExecutionMode: platformv1alpha1.ExecutionModeTeam,
				Team: &platformv1alpha1.AgentRunTeamSpec{
					Steps: []platformv1alpha1.AgentRunTeamStep{
						{
							Name: "parallel-implementers",
							Type: platformv1alpha1.TeamStepTypeParallel,
							Tasks: []platformv1alpha1.AgentRunTeamTask{
								{Name: "worker-a", Role: "executor", Objective: "Implement the requested change"},
								{Name: "worker-b", Role: "code-reviewer", Objective: "Review the requested change"},
							},
						},
					},
				},
				Secrets: triggersv1alpha1.AgentRunSecrets{
					ClaudeApiKey: "claude-key",
					GithubToken:  "github-token",
				},
			},
		},
	}

	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&platformv1alpha1.AgentRun{}).
		WithObjects(project).
		Build()

	reconciler := &LinearProjectReconciler{Client: k8sClient, Scheme: scheme}
	issue := linear.Issue{ID: "issue-123", Identifier: "ENG-123", Title: "Implement with team", Description: "Use team mode"}
	if err := reconciler.createAgentRun(context.Background(), project, issue); err != nil {
		t.Fatalf("createAgentRun() error = %v", err)
	}

	run := &platformv1alpha1.AgentRun{}
	if err := k8sClient.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: issueName(issue.ID)}, run); err != nil {
		t.Fatalf("Get(AgentRun) error = %v", err)
	}
	if run.Spec.ExecutionMode != platformv1alpha1.ExecutionModeTeam {
		t.Fatalf("Spec.ExecutionMode = %q, want team", run.Spec.ExecutionMode)
	}
	if run.Spec.WorkflowMode != platformv1alpha1.WorkflowModeAuto {
		t.Fatalf("Spec.WorkflowMode = %q, want auto (autonomous default)", run.Spec.WorkflowMode)
	}
	if got := run.Annotations["platform.gratefulagents.dev/run-mode"]; got != "auto" {
		t.Fatalf("run-mode annotation = %q, want auto", got)
	}
	if run.Spec.Team == nil || len(run.Spec.Team.Steps) != 1 || len(run.Spec.Team.Steps[0].Tasks) != 2 {
		t.Fatalf("Spec.Team = %#v, want one step with two tasks", run.Spec.Team)
	}
}

func TestCreateAgentRunUsesProviderDefaultModel(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(platform): %v", err)
	}
	if err := triggersv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(triggers): %v", err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(core): %v", err)
	}

	project := &triggersv1alpha1.LinearProject{
		ObjectMeta: metav1.ObjectMeta{Name: "payments-app", Namespace: "default"},
		Spec: triggersv1alpha1.LinearProjectSpec{
			LinearAPIKeySecret: "linear-secret",
			ProjectID:          "proj",
			TeamID:             "team",
			Defaults: triggersv1alpha1.AgentRunDefaults{
				RepoURL:    "https://github.com/example/repo.git",
				BaseBranch: "main",
				Image:      "ghcr.io/example/worker:latest",
				Provider:   "openai",
				Secrets: triggersv1alpha1.AgentRunSecrets{
					ClaudeApiKey: "claude-key",
					GithubToken:  "github-token",
				},
			},
		},
	}

	k8sClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&platformv1alpha1.AgentRun{}).
		WithObjects(project).
		Build()

	reconciler := &LinearProjectReconciler{Client: k8sClient, Scheme: scheme}
	issue := linear.Issue{ID: "issue-123", Identifier: "ENG-123", Title: "Implement with team", Description: "Use team mode"}
	err := reconciler.createAgentRun(context.Background(), project, issue)
	if err != nil {
		t.Fatalf("createAgentRun() default model error = %v", err)
	}
	var runs platformv1alpha1.AgentRunList
	if err := k8sClient.List(context.Background(), &runs); err != nil {
		t.Fatalf("List(AgentRun): %v", err)
	}
	if len(runs.Items) != 1 || runs.Items[0].Spec.Model != "gpt-5.6-sol" || runs.Items[0].Spec.ReasoningLevel != platformv1alpha1.ReasoningMax {
		t.Fatalf("default trigger routing = %#v", runs.Items)
	}
}

func TestLinearReconcileKeepsApprovedLabelWhenAgentRunCreateFails(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(platform): %v", err)
	}
	if err := triggersv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(triggers): %v", err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(core): %v", err)
	}

	project := &triggersv1alpha1.LinearProject{
		ObjectMeta: metav1.ObjectMeta{Name: "payments-app", Namespace: "default"},
		Spec: triggersv1alpha1.LinearProjectSpec{
			LinearAPIKeySecret: "linear-secret",
			ProjectID:          "proj",
			TeamID:             "team",
			AutoCreateTasks:    true,
			Defaults: triggersv1alpha1.AgentRunDefaults{
				RepoURL:    "https://github.com/example/repo.git",
				BaseBranch: "main",
				Image:      "ghcr.io/example/worker:latest",
				Model:      "gpt-4.1",
				Secrets: triggersv1alpha1.AgentRunSecrets{
					ClaudeApiKey: "claude-key",
				},
			},
		},
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "linear-secret", Namespace: "default"},
		Data:       map[string][]byte{"api-key": []byte("linear-token")},
	}
	baseClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&triggersv1alpha1.LinearProject{}).
		WithObjects(project, secret).
		Build()
	issue := linear.Issue{
		ID:         "issue-123",
		Identifier: "ENG-123",
		Title:      "Implement",
	}
	issue.Labels.Nodes = []linear.LabelNode{{Name: defaultApprovedLabel}}
	linearClient := &fakeLinearClient{
		issues: []linear.Issue{issue},
		labelIDsByName: map[string]string{
			defaultApprovedLabel: "approved-id",
			labelInProgress:      "in-progress-id",
		},
	}

	reconciler := &LinearProjectReconciler{
		Client: &failingCreateClient{Client: baseClient},
		Scheme: scheme,
		LinearClientFactory: func(apiKey string) linear.LinearClient {
			return linearClient
		},
	}
	if _, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(project)}); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if len(linearClient.removed) != 0 {
		t.Fatalf("removed labels = %#v, want none", linearClient.removed)
	}
	if len(linearClient.added) != 0 {
		t.Fatalf("added labels = %#v, want none", linearClient.added)
	}
}
