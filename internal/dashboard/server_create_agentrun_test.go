package dashboard

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/projectstate"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestProjectStateIDForAgentRunUsesSharedHashedIdentity(t *testing.T) {
	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Namespace: "Team-A"},
		Spec: platformv1alpha1.AgentRunSpec{Repository: platformv1alpha1.RepositoryContext{
			URL: "https://github.com/Acme/Widgets.git",
		}},
	}
	want := projectstate.ProjectID(run.Namespace, run.Spec.Repository.URL)
	if got := projectStateIDForAgentRun(run); got != want {
		t.Fatalf("projectStateIDForAgentRun() = %q, want %q", got, want)
	}
	if got := projectStateIDForAgentRun(nil); got != "" {
		t.Fatalf("projectStateIDForAgentRun(nil) = %q, want empty", got)
	}
}

func TestCreateAgentRunDefaultsToInteractiveMode(t *testing.T) {
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
				Model:      "gpt-4.1",
				Secrets: triggersv1alpha1.AgentRunSecrets{
					ClaudeApiKey: "claude-key",
					GithubToken:  "github-token",
				},
			},
		},
	}

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&platformv1alpha1.AgentRun{}).
		WithObjects(project).
		Build()
	srv := &Server{k8sClient: c, scheme: scheme}

	resp, err := srv.CreateAgentRun(context.Background(), &platform.CreateAgentRunRequest{
		Namespace: "default",
		Source: &platform.SourceRef{
			Kind: "LinearProject",
			Name: "payments-app",
		},
		UserRequest: "Implement usage-based billing",
	})
	if err != nil {
		t.Fatalf("CreateAgentRun() error = %v", err)
	}
	if resp.WorkflowMode != "auto" {
		t.Fatalf("WorkflowMode = %q, want auto", resp.WorkflowMode)
	}
	if resp.CurrentStep != "starting" {
		t.Fatalf("CurrentStep = %q, want starting", resp.CurrentStep)
	}

	run := &platformv1alpha1.AgentRun{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: resp.Name}, run); err != nil {
		t.Fatalf("Get(AgentRun) error = %v", err)
	}
	if run.Annotations["platform.gratefulagents.dev/direct-ingress"] != "true" {
		t.Fatalf("direct-ingress annotation missing")
	}
	if run.Status.Phase != platformv1alpha1.AgentRunPhasePending {
		t.Fatalf("Phase = %q, want Pending", run.Status.Phase)
	}
	if run.Spec.WorkflowMode != platformv1alpha1.WorkflowModeAuto {
		t.Fatalf("Spec.WorkflowMode = %q, want auto", run.Spec.WorkflowMode)
	}
	if run.Spec.ModeRef == nil || run.Spec.ModeRef.Name != "interactive" {
		t.Fatalf("Spec.ModeRef = %#v, want interactive", run.Spec.ModeRef)
	}
	if run.Spec.SpecArtifactRef != nil {
		t.Fatalf("SpecArtifactRef = %#v, want nil", run.Spec.SpecArtifactRef)
	}
	if run.Spec.Repository.BranchName != "" {
		t.Fatalf("Repository.BranchName = %q, want empty", run.Spec.Repository.BranchName)
	}
	if run.Status.CurrentStep != "starting" {
		t.Fatalf("CurrentStep = %q, want starting", run.Status.CurrentStep)
	}
	if run.Status.Queue == nil || run.Status.Queue.State != "Queued" {
		t.Fatalf("Queue = %#v, want Queued state", run.Status.Queue)
	}

	configMaps := &corev1.ConfigMapList{}
	if err := c.List(context.Background(), configMaps, client.InNamespace("default")); err != nil {
		t.Fatalf("List(ConfigMaps) error = %v", err)
	}
	if len(configMaps.Items) != 0 {
		t.Fatalf("len(ConfigMaps) = %d, want 0 (no pre-materialized execute spec)", len(configMaps.Items))
	}

	runs := &platformv1alpha1.AgentRunList{}
	if err := c.List(context.Background(), runs, client.InNamespace("default")); err != nil {
		t.Fatalf("List(AgentRuns) error = %v", err)
	}
	if len(runs.Items) != 1 {
		t.Fatalf("len(AgentRuns) = %d, want 1 same-run record", len(runs.Items))
	}
}

func TestCreateAgentRunInheritsSourceReasoningLevel(t *testing.T) {
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

	project := &triggersv1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{Name: "payments", Namespace: "default"},
		Spec: triggersv1alpha1.ProjectSpec{
			DisplayName: "Payments",
			Defaults: triggersv1alpha1.AgentRunDefaults{
				RepoURL:        "https://github.com/example/repo.git",
				BaseBranch:     "main",
				Model:          "gpt-4.1",
				ReasoningLevel: platformv1alpha1.ReasoningHigh,
				Secrets: triggersv1alpha1.AgentRunSecrets{
					ClaudeApiKey: "claude-key",
					GithubToken:  "github-token",
				},
			},
		},
	}

	newServer := func(t *testing.T) *Server {
		c := fake.NewClientBuilder().
			WithScheme(scheme).
			WithStatusSubresource(&platformv1alpha1.AgentRun{}).
			WithObjects(project.DeepCopy()).
			Build()
		return &Server{k8sClient: c, scheme: scheme}
	}
	source := &platform.SourceRef{Kind: "Project", Name: "payments"}

	t.Run("inherits project default", func(t *testing.T) {
		srv := newServer(t)
		resp, err := srv.CreateAgentRun(context.Background(), &platform.CreateAgentRunRequest{
			Namespace:   "default",
			Source:      source,
			UserRequest: "Implement billing",
		})
		if err != nil {
			t.Fatalf("CreateAgentRun() error = %v", err)
		}
		run := &platformv1alpha1.AgentRun{}
		if err := srv.k8sClient.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: resp.Name}, run); err != nil {
			t.Fatalf("Get(AgentRun) error = %v", err)
		}
		if run.Spec.ReasoningLevel != platformv1alpha1.ReasoningHigh {
			t.Fatalf("ReasoningLevel = %q, want high (inherited)", run.Spec.ReasoningLevel)
		}
	})

	t.Run("request override wins", func(t *testing.T) {
		srv := newServer(t)
		resp, err := srv.CreateAgentRun(context.Background(), &platform.CreateAgentRunRequest{
			Namespace:      "default",
			Source:         source,
			UserRequest:    "Implement billing",
			ReasoningLevel: "low",
		})
		if err != nil {
			t.Fatalf("CreateAgentRun() error = %v", err)
		}
		run := &platformv1alpha1.AgentRun{}
		if err := srv.k8sClient.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: resp.Name}, run); err != nil {
			t.Fatalf("Get(AgentRun) error = %v", err)
		}
		if run.Spec.ReasoningLevel != platformv1alpha1.ReasoningLow {
			t.Fatalf("ReasoningLevel = %q, want low (request override)", run.Spec.ReasoningLevel)
		}
	})
}

func TestCreateAgentRunInheritsSourceDisableCommandSandbox(t *testing.T) {
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

	project := &triggersv1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{Name: "payments", Namespace: "default"},
		Spec: triggersv1alpha1.ProjectSpec{
			DisplayName: "Payments",
			Defaults: triggersv1alpha1.AgentRunDefaults{
				RepoURL:               "https://github.com/example/repo.git",
				BaseBranch:            "main",
				Model:                 "gpt-4.1",
				DisableCommandSandbox: true,
				Secrets: triggersv1alpha1.AgentRunSecrets{
					ClaudeApiKey: "claude-key",
					GithubToken:  "github-token",
				},
			},
		},
	}

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&platformv1alpha1.AgentRun{}).
		WithObjects(project.DeepCopy()).
		Build()
	srv := &Server{k8sClient: c, scheme: scheme}

	resp, err := srv.CreateAgentRun(context.Background(), &platform.CreateAgentRunRequest{
		Namespace:   "default",
		Source:      &platform.SourceRef{Kind: "Project", Name: "payments"},
		UserRequest: "Implement billing",
	})
	if err != nil {
		t.Fatalf("CreateAgentRun() error = %v", err)
	}
	run := &platformv1alpha1.AgentRun{}
	if err := srv.k8sClient.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: resp.Name}, run); err != nil {
		t.Fatalf("Get(AgentRun) error = %v", err)
	}
	if !run.Spec.DisableCommandSandbox {
		t.Fatal("Spec.DisableCommandSandbox = false, want true (inherited from source trigger defaults)")
	}
}

func TestCreateAgentRunInheritsProjectKubernetesAdmin(t *testing.T) {
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

	project := &triggersv1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{Name: "payments", Namespace: "default"},
		Spec: triggersv1alpha1.ProjectSpec{
			DisplayName:     "Payments",
			KubernetesAdmin: true,
			Defaults: triggersv1alpha1.AgentRunDefaults{
				RepoURL:    "https://github.com/example/repo.git",
				BaseBranch: "main",
				Model:      "gpt-4.1",
				Provider:   triggersv1alpha1.ProviderOpenAI,
				AuthMode:   platformv1alpha1.AgentRunAuthModeAPIKey,
				Secrets:    triggersv1alpha1.AgentRunSecrets{ProviderKeys: []platformv1alpha1.ProviderKeyRef{{Provider: triggersv1alpha1.ProviderOpenAI, SecretName: "openai-secret"}}},
			},
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&platformv1alpha1.AgentRun{}).WithObjects(project).Build()
	srv := &Server{k8sClient: c, scheme: scheme}

	resp, err := srv.CreateAgentRun(context.Background(), &platform.CreateAgentRunRequest{
		Namespace:   "default",
		Name:        "run-admin",
		Source:      &platform.SourceRef{Kind: "Project", Name: "payments"},
		UserRequest: "do it",
	})
	if err != nil {
		t.Fatalf("CreateAgentRun() error = %v", err)
	}
	if !resp.KubernetesAdmin {
		t.Fatalf("response KubernetesAdmin = false, want true")
	}
	run := &platformv1alpha1.AgentRun{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: "run-admin"}, run); err != nil {
		t.Fatalf("Get(AgentRun) error = %v", err)
	}
	if !run.Spec.KubernetesAdmin {
		t.Fatalf("Spec.KubernetesAdmin = false, want true")
	}
}

func TestCreateAgentRunDefaultsNamespaceToUserNamespace(t *testing.T) {
	scheme := testProjectScheme(t)
	ns := testUserNS()

	project := &triggersv1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{Name: "payments", Namespace: ns},
		Spec: triggersv1alpha1.ProjectSpec{
			DisplayName: "Payments",
			Defaults: triggersv1alpha1.AgentRunDefaults{
				Model:    "gpt-4.1",
				Provider: triggersv1alpha1.ProviderOpenAI,
				AuthMode: platformv1alpha1.AgentRunAuthModeAPIKey,
				Secrets: triggersv1alpha1.AgentRunSecrets{
					ProviderKeys: []platformv1alpha1.ProviderKeyRef{{
						Provider:   triggersv1alpha1.ProviderOpenAI,
						SecretName: "openai-secret",
						SecretKey:  "api-key",
					}},
				},
			},
		},
	}

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&platformv1alpha1.AgentRun{}).
		WithObjects(project).
		Build()
	srv := &Server{k8sClient: c, scheme: scheme}

	resp, err := srv.CreateAgentRun(projectActorCtx(), &platform.CreateAgentRunRequest{
		Source: &platform.SourceRef{
			Kind: "Project",
			Name: "payments",
		},
		UserRequest: "Add a checkout endpoint",
	})
	if err != nil {
		t.Fatalf("CreateAgentRun() error = %v", err)
	}
	if resp.Namespace != ns {
		t.Fatalf("run namespace = %q, want %q", resp.Namespace, ns)
	}

	run := &platformv1alpha1.AgentRun{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: resp.Name}, run); err != nil {
		t.Fatalf("Get(AgentRun) error = %v", err)
	}
	if run.Namespace != ns {
		t.Fatalf("stored run namespace = %q, want %q", run.Namespace, ns)
	}
}

func TestCreateAgentRunChatWithoutUserRequestDoesNotInjectDefaultImplementationPrompt(t *testing.T) {
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
				Model:      "gpt-4.1",
				Secrets: triggersv1alpha1.AgentRunSecrets{
					ClaudeApiKey: "claude-key",
					GithubToken:  "github-token",
				},
			},
		},
	}

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&platformv1alpha1.AgentRun{}).
		WithObjects(project).
		Build()
	srv := &Server{k8sClient: c, scheme: scheme}

	resp, err := srv.CreateAgentRun(context.Background(), &platform.CreateAgentRunRequest{
		Namespace: "default",
		Source: &platform.SourceRef{
			Kind: "LinearProject",
			Name: "payments-app",
		},
	})
	if err != nil {
		t.Fatalf("CreateAgentRun() error = %v", err)
	}

	run := &platformv1alpha1.AgentRun{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: resp.Name}, run); err != nil {
		t.Fatalf("Get(AgentRun) error = %v", err)
	}
	if run.Spec.SpecArtifactRef != nil {
		t.Fatalf("SpecArtifactRef = %#v, want nil when chat starts without a request", run.Spec.SpecArtifactRef)
	}

	configMaps := &corev1.ConfigMapList{}
	if err := c.List(context.Background(), configMaps, client.InNamespace("default")); err != nil {
		t.Fatalf("List(ConfigMaps) error = %v", err)
	}
	if len(configMaps.Items) != 0 {
		t.Fatalf("len(ConfigMaps) = %d, want 0 (no bootstrap spec in live chat mode)", len(configMaps.Items))
	}
}

func TestCreateAgentRunDirectIngressDoesNotInjectBuiltInExplorationChildren(t *testing.T) {
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
				Model:      "gpt-4.1",
				Secrets: triggersv1alpha1.AgentRunSecrets{
					ClaudeApiKey: "claude-key",
					GithubToken:  "github-token",
				},
			},
		},
	}

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&platformv1alpha1.AgentRun{}).
		WithObjects(project).
		Build()
	srv := &Server{k8sClient: c, scheme: scheme}

	resp, err := srv.CreateAgentRun(context.Background(), &platform.CreateAgentRunRequest{
		Namespace: "default",
		Source: &platform.SourceRef{
			Kind: "LinearProject",
			Name: "payments-app",
		},
		UserRequest: "Implement this with a team",
	})
	if err != nil {
		t.Fatalf("CreateAgentRun() error = %v", err)
	}
	if resp.ExecutionMode != "linear" {
		t.Fatalf("ExecutionMode = %q, want linear", resp.ExecutionMode)
	}

	run := &platformv1alpha1.AgentRun{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: resp.Name}, run); err != nil {
		t.Fatalf("Get(AgentRun) error = %v", err)
	}
	if run.Spec.ExecutionMode != platformv1alpha1.ExecutionModeLinear {
		t.Fatalf("Spec.ExecutionMode = %q, want linear", run.Spec.ExecutionMode)
	}
	if run.Spec.Team != nil {
		t.Fatalf("Spec.Team = %#v, want nil (no injected exploration-child team)", run.Spec.Team)
	}
	if run.Spec.WorkflowMode != platformv1alpha1.WorkflowModeAuto {
		t.Fatalf("Spec.WorkflowMode = %q, want auto", run.Spec.WorkflowMode)
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

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&platformv1alpha1.AgentRun{}).
		WithObjects(project).
		Build()
	srv := &Server{k8sClient: c, scheme: scheme}

	resp, err := srv.CreateAgentRun(context.Background(), &platform.CreateAgentRunRequest{
		Namespace: "default",
		Source: &platform.SourceRef{
			Kind: "LinearProject",
			Name: "payments-app",
		},
	})
	if err != nil {
		t.Fatalf("CreateAgentRun() default model error = %v", err)
	}
	run := &platformv1alpha1.AgentRun{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: resp.Name}, run); err != nil {
		t.Fatalf("Get(AgentRun): %v", err)
	}
	if run.Spec.Model != "gpt-5.6-sol" || run.Spec.ReasoningLevel != platformv1alpha1.ReasoningMax {
		t.Fatalf("default main routing = model %q reasoning %q", run.Spec.Model, run.Spec.ReasoningLevel)
	}
}

func TestCreateAgentRunAppliesRuntimeProfileDefaults(t *testing.T) {
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
		ObjectMeta: metav1.ObjectMeta{
			Name:      "payments-app",
			Namespace: "default",
		},
		Spec: triggersv1alpha1.LinearProjectSpec{
			LinearAPIKeySecret: "linear-secret",
			ProjectID:          "proj",
			TeamID:             "team",
			Defaults: triggersv1alpha1.AgentRunDefaults{
				RepoURL:    "https://github.com/example/repo.git",
				BaseBranch: "main",
				Image:      "ghcr.io/example/worker:latest",
				Model:      "gpt-4.1",
				Secrets: triggersv1alpha1.AgentRunSecrets{
					ClaudeApiKey: "claude-key",
					GithubToken:  "github-token",
				},
				RuntimeProfileRef: &platformv1alpha1.NamedRef{Name: "interactive-readonly"},
			},
		},
	}
	profile := &platformv1alpha1.RuntimeProfile{
		ObjectMeta: metav1.ObjectMeta{Name: "interactive-readonly", Namespace: "default"},
		Spec: platformv1alpha1.RuntimeProfileSpec{
			Security: &platformv1alpha1.RuntimeProfileSecurity{
				PermissionMode: platformv1alpha1.PermissionMode("read-only"),
				DefaultTimeout: metav1.Duration{Duration: 45 * time.Minute},
			},
		},
	}

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&platformv1alpha1.AgentRun{}).
		WithObjects(project, profile).
		Build()
	srv := &Server{k8sClient: c, scheme: scheme}

	resp, err := srv.CreateAgentRun(context.Background(), &platform.CreateAgentRunRequest{
		Namespace: "default",
		Source: &platform.SourceRef{
			Kind: "LinearProject",
			Name: "payments-app",
		},
		UserRequest: "Can we safely inspect this repo?",
	})
	if err != nil {
		t.Fatalf("CreateAgentRun() error = %v", err)
	}
	if resp.RuntimeProfileRef != "interactive-readonly" {
		t.Fatalf("RuntimeProfileRef = %q, want interactive-readonly", resp.RuntimeProfileRef)
	}
	if resp.ResolvedPermissionMode != "read-only" {
		t.Fatalf("ResolvedPermissionMode = %q, want read-only", resp.ResolvedPermissionMode)
	}
	if resp.MaxRuntime != "45m0s" {
		t.Fatalf("MaxRuntime = %q, want 45m0s", resp.MaxRuntime)
	}
	if resp.Phase != "Pending" {
		t.Fatalf("Phase = %q, want Pending", resp.Phase)
	}

	run := &platformv1alpha1.AgentRun{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: resp.Name}, run); err != nil {
		t.Fatalf("Get(AgentRun) error = %v", err)
	}
	if run.Status.CurrentStep != "starting" {
		t.Fatalf("CurrentStep = %q, want starting", run.Status.CurrentStep)
	}
}

func TestCreateAgentRunPropagatesOpenAIAPIMode(t *testing.T) {
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
				Model:      "gpt-5.3-codex",
				Provider:   "openai",
				OpenAIAPI:  triggersv1alpha1.OpenAIAPIChatCompletions,
				Secrets: triggersv1alpha1.AgentRunSecrets{
					ClaudeApiKey: "claude-key",
					GithubToken:  "github-token",
				},
			},
		},
	}

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&platformv1alpha1.AgentRun{}).
		WithObjects(project).
		Build()
	srv := &Server{k8sClient: c, scheme: scheme}

	resp, err := srv.CreateAgentRun(context.Background(), &platform.CreateAgentRunRequest{
		Namespace: "default",
		Source: &platform.SourceRef{
			Kind: "LinearProject",
			Name: "payments-app",
		},
		UserRequest: "Implement usage-based billing",
	})
	if err != nil {
		t.Fatalf("CreateAgentRun() error = %v", err)
	}

	run := &platformv1alpha1.AgentRun{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: resp.Name}, run); err != nil {
		t.Fatalf("Get(AgentRun) error = %v", err)
	}
	if got := run.Annotations[openAIApiModeAnnotation]; got != triggersv1alpha1.OpenAIAPIChatCompletions {
		t.Fatalf("openai api mode annotation = %q, want %q", got, triggersv1alpha1.OpenAIAPIChatCompletions)
	}
}

func TestCreateAgentRunAppliesMCPPolicyDefaults(t *testing.T) {
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
		ObjectMeta: metav1.ObjectMeta{
			Name:      "payments-app",
			Namespace: "default",
		},
		Spec: triggersv1alpha1.LinearProjectSpec{
			LinearAPIKeySecret: "linear-secret",
			ProjectID:          "proj",
			TeamID:             "team",
			Defaults: triggersv1alpha1.AgentRunDefaults{
				RepoURL:    "https://github.com/example/repo.git",
				BaseBranch: "main",
				Image:      "ghcr.io/example/worker:latest",
				Model:      "gpt-4.1",
				Secrets: triggersv1alpha1.AgentRunSecrets{
					ClaudeApiKey: "claude-key",
					GithubToken:  "github-token",
				},
				MCPPolicyRef: &platformv1alpha1.NamedRef{Name: "repo-default"},
			},
		},
	}
	policy := &platformv1alpha1.MCPPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "repo-default", Namespace: "default"},
		Spec: platformv1alpha1.MCPPolicySpec{
			DefaultAction: platformv1alpha1.MCPDefaultActionDeny,
			AllowedServers: []platformv1alpha1.MCPAllowedServer{
				{Name: "context7"},
				{Name: "github"},
			},
		},
	}

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&platformv1alpha1.AgentRun{}).
		WithObjects(project, policy).
		Build()
	srv := &Server{k8sClient: c, scheme: scheme}

	resp, err := srv.CreateAgentRun(context.Background(), &platform.CreateAgentRunRequest{
		Namespace: "default",
		Source: &platform.SourceRef{
			Kind: "LinearProject",
			Name: "payments-app",
		},
		UserRequest: "Use allowed MCP servers only",
	})
	if err != nil {
		t.Fatalf("CreateAgentRun() error = %v", err)
	}
	if resp.McpPolicyRef != "repo-default" {
		t.Fatalf("McpPolicyRef = %q, want repo-default", resp.McpPolicyRef)
	}
	if len(resp.ResolvedMcpServers) != 2 || resp.ResolvedMcpServers[0] != "context7" || resp.ResolvedMcpServers[1] != "github" {
		t.Fatalf("ResolvedMcpServers = %v, want [context7 github]", resp.ResolvedMcpServers)
	}
}

func TestCreateAgentRunValidatesOAuthProviderScope(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(platform): %v", err)
	}
	if err := triggersv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(triggers): %v", err)
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
				Model:      "gpt-5.3-codex",
				Provider:   triggersv1alpha1.ProviderGemini,
				AuthMode:   platformv1alpha1.AgentRunAuthModeOAuth,
				Secrets: triggersv1alpha1.AgentRunSecrets{
					ClaudeApiKey: "claude-key",
					GithubToken:  "github-token",
				},
			},
		},
	}

	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&platformv1alpha1.AgentRun{}).WithObjects(project).Build()
	srv := &Server{k8sClient: c, scheme: scheme}

	_, err := srv.CreateAgentRun(context.Background(), &platform.CreateAgentRunRequest{
		Namespace:   "default",
		Source:      &platform.SourceRef{Kind: "LinearProject", Name: "payments-app"},
		UserRequest: "try oauth",
	})
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("connect.CodeOf(err) = %v, want %v (err=%v)", connect.CodeOf(err), connect.CodeInvalidArgument, err)
	}
}

func TestCreateAgentRunPropagatesOpenAIOAuthDefaults(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(platform): %v", err)
	}
	if err := triggersv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(triggers): %v", err)
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
				Model:      "gpt-5.3-codex",
				Provider:   triggersv1alpha1.ProviderOpenAI,
				AuthMode:   platformv1alpha1.AgentRunAuthModeOAuth,
				Secrets: triggersv1alpha1.AgentRunSecrets{
					OpenAIOAuthSecret: "openai-oauth",
					GithubToken:       "github-token",
				},
			},
		},
	}

	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&platformv1alpha1.AgentRun{}).WithObjects(project).Build()
	srv := &Server{k8sClient: c, scheme: scheme}

	resp, err := srv.CreateAgentRun(context.Background(), &platform.CreateAgentRunRequest{
		Namespace:   "default",
		Source:      &platform.SourceRef{Kind: "LinearProject", Name: "payments-app"},
		UserRequest: "ship oauth mode",
	})
	if err != nil {
		t.Fatalf("CreateAgentRun() error = %v", err)
	}

	run := &platformv1alpha1.AgentRun{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: resp.Name}, run); err != nil {
		t.Fatalf("Get(AgentRun) error = %v", err)
	}
	if run.Spec.AuthMode != platformv1alpha1.AgentRunAuthModeOAuth {
		t.Fatalf("Spec.AuthMode = %q, want oauth", run.Spec.AuthMode)
	}
	if run.Spec.Secrets == nil || run.Spec.Secrets.OpenAIOAuthSecret != "openai-oauth" {
		t.Fatalf("Spec.Secrets = %#v, want OpenAIOAuthSecret=openai-oauth", run.Spec.Secrets)
	}
}

func TestCreateAgentRunFailsWhenSessionCreationFails(t *testing.T) {
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
				Model:      "gpt-4.1",
				Secrets: triggersv1alpha1.AgentRunSecrets{
					ClaudeApiKey: "claude-key",
					GithubToken:  "github-token",
				},
			},
		},
	}

	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&platformv1alpha1.AgentRun{}).WithObjects(project).Build()
	ms := newMockStateStore()
	ms.createSessionErr = errors.New("session store unavailable")
	srv := &Server{k8sClient: c, scheme: scheme, stateStore: ms}

	_, err := srv.CreateAgentRun(context.Background(), &platform.CreateAgentRunRequest{
		Namespace:   "default",
		Source:      &platform.SourceRef{Kind: "LinearProject", Name: "payments-app"},
		UserRequest: "ship it",
	})
	if connect.CodeOf(err) != connect.CodeInternal {
		t.Fatalf("connect.CodeOf(err) = %v, want %v (err=%v)", connect.CodeOf(err), connect.CodeInternal, err)
	}
}

func TestCreateAgentRunFailsWhenSessionSeedFails(t *testing.T) {
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
				Model:      "gpt-4.1",
				Secrets: triggersv1alpha1.AgentRunSecrets{
					ClaudeApiKey: "claude-key",
					GithubToken:  "github-token",
				},
			},
		},
	}

	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&platformv1alpha1.AgentRun{}).WithObjects(project).Build()
	ms := newMockStateStore()
	ms.appendMessageErr = errors.New("message store unavailable")
	srv := &Server{k8sClient: c, scheme: scheme, stateStore: ms}

	_, err := srv.CreateAgentRun(context.Background(), &platform.CreateAgentRunRequest{
		Namespace:   "default",
		Source:      &platform.SourceRef{Kind: "LinearProject", Name: "payments-app"},
		UserRequest: "ship it",
	})
	if connect.CodeOf(err) != connect.CodeInternal {
		t.Fatalf("connect.CodeOf(err) = %v, want %v (err=%v)", connect.CodeOf(err), connect.CodeInternal, err)
	}
}

func TestCreateAgentRunInheritsProjectAutopilotMode(t *testing.T) {
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

	project := &triggersv1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{Name: "burn", Namespace: "default"},
		Spec: triggersv1alpha1.ProjectSpec{
			DisplayName: "Burn",
			Defaults: triggersv1alpha1.AgentRunDefaults{
				Model:        "gpt-4.1",
				WorkflowMode: platformv1alpha1.WorkflowModeAuto,
				ModeRef:      &platformv1alpha1.ModeRef{Name: "autopilot"},
				Secrets: triggersv1alpha1.AgentRunSecrets{
					ClaudeApiKey: "claude-key",
					GithubToken:  "github-token",
				},
			},
		},
	}

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&platformv1alpha1.AgentRun{}).
		WithObjects(project).
		Build()
	srv := &Server{k8sClient: c, scheme: scheme}

	resp, err := srv.CreateAgentRun(context.Background(), &platform.CreateAgentRunRequest{
		Namespace:   "default",
		Source:      &platform.SourceRef{Kind: "Project", Name: "burn"},
		UserRequest: "burn tokens",
	})
	if err != nil {
		t.Fatalf("CreateAgentRun() error = %v", err)
	}
	if resp.WorkflowMode != "auto" {
		t.Fatalf("WorkflowMode = %q, want auto (inherited from project defaults)", resp.WorkflowMode)
	}

	run := &platformv1alpha1.AgentRun{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: resp.Name}, run); err != nil {
		t.Fatalf("Get(AgentRun) error = %v", err)
	}
	if run.Spec.WorkflowMode != platformv1alpha1.WorkflowModeAuto {
		t.Fatalf("Spec.WorkflowMode = %q, want auto", run.Spec.WorkflowMode)
	}
	if run.Spec.ModeRef == nil || run.Spec.ModeRef.Name != "autopilot" {
		t.Fatalf("Spec.ModeRef = %#v, want autopilot (inherited from project defaults)", run.Spec.ModeRef)
	}
}

func TestCreateAgentRunProjectReviewLoopPolicy(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(platform): %v", err)
	}
	if err := triggersv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(triggers): %v", err)
	}

	projects := []client.Object{
		&triggersv1alpha1.Project{
			ObjectMeta: metav1.ObjectMeta{Name: "default-policy", Namespace: "default"},
			Spec: triggersv1alpha1.ProjectSpec{DisplayName: "Default", Defaults: triggersv1alpha1.AgentRunDefaults{
				Model: "gpt-4.1", Secrets: triggersv1alpha1.AgentRunSecrets{ClaudeApiKey: "test-key"},
			}},
		},
		&triggersv1alpha1.Project{
			ObjectMeta: metav1.ObjectMeta{Name: "enabled-policy", Namespace: "default"},
			Spec: triggersv1alpha1.ProjectSpec{DisplayName: "Enabled", ReviewLoop: &triggersv1alpha1.ProjectReviewLoopSpec{}, Defaults: triggersv1alpha1.AgentRunDefaults{
				Model: "gpt-4.1", Secrets: triggersv1alpha1.AgentRunSecrets{ClaudeApiKey: "test-key"},
			}},
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&platformv1alpha1.AgentRun{}).WithObjects(projects...).Build()
	srv := &Server{k8sClient: c, scheme: scheme}

	for _, tc := range []struct{ project, want string }{
		{project: "default-policy", want: projectReviewLoopDisabled},
		{project: "enabled-policy", want: projectReviewLoopEnabled},
	} {
		t.Run(tc.project, func(t *testing.T) {
			resp, err := srv.CreateAgentRun(context.Background(), &platform.CreateAgentRunRequest{
				Namespace: "default", Source: &platform.SourceRef{Kind: "Project", Name: tc.project}, UserRequest: "test policy",
			})
			if err != nil {
				t.Fatalf("CreateAgentRun() error = %v", err)
			}
			run := fetchCreatedRun(t, c, resp.Name)
			if got := run.Annotations[projectReviewLoopAnnotation]; got != tc.want {
				t.Fatalf("review-loop annotation = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestCreateAgentRunAdditionalRepos(t *testing.T) {
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

	project := &triggersv1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{Name: "multi", Namespace: "default"},
		Spec: triggersv1alpha1.ProjectSpec{
			DisplayName: "Multi",
			ReviewLoop:  &triggersv1alpha1.ProjectReviewLoopSpec{Disabled: true},
			Defaults: triggersv1alpha1.AgentRunDefaults{
				RepoURL:         "https://github.com/example/app.git",
				BaseBranch:      "main",
				Model:           "gpt-4.1",
				AdditionalRepos: []string{"https://github.com/example/lib.git"},
				Secrets: triggersv1alpha1.AgentRunSecrets{
					ClaudeApiKey: "claude-key",
					GithubToken:  "github-token",
				},
			},
		},
	}

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&platformv1alpha1.AgentRun{}).
		WithObjects(project).
		Build()
	srv := &Server{k8sClient: c, scheme: scheme}

	t.Run("inherits project defaults", func(t *testing.T) {
		resp, err := srv.CreateAgentRun(context.Background(), &platform.CreateAgentRunRequest{
			Namespace:   "default",
			Source:      &platform.SourceRef{Kind: "Project", Name: "multi"},
			UserRequest: "wire the lib into the app",
		})
		if err != nil {
			t.Fatalf("CreateAgentRun() error = %v", err)
		}
		want := []string{"https://github.com/example/lib.git"}
		if !reflect.DeepEqual(resp.AdditionalRepoUrls, want) {
			t.Fatalf("resp.AdditionalRepoUrls = %#v, want %#v", resp.AdditionalRepoUrls, want)
		}
		run := fetchCreatedRun(t, c, resp.Name)
		if !reflect.DeepEqual(run.Spec.Repository.AdditionalRepos, want) {
			t.Fatalf("Repository.AdditionalRepos = %#v, want %#v", run.Spec.Repository.AdditionalRepos, want)
		}
		if got := run.Annotations[projectReviewLoopAnnotation]; got != projectReviewLoopDisabled {
			t.Fatalf("review-loop annotation = %q, want %q", got, projectReviewLoopDisabled)
		}
	})

	t.Run("request overrides project defaults", func(t *testing.T) {
		resp, err := srv.CreateAgentRun(context.Background(), &platform.CreateAgentRunRequest{
			Namespace:          "default",
			Source:             &platform.SourceRef{Kind: "Project", Name: "multi"},
			AdditionalRepoUrls: []string{"https://github.com/example/tools.git"},
			UserRequest:        "use tools instead",
		})
		if err != nil {
			t.Fatalf("CreateAgentRun() error = %v", err)
		}
		run := fetchCreatedRun(t, c, resp.Name)
		want := []string{"https://github.com/example/tools.git"}
		if !reflect.DeepEqual(run.Spec.Repository.AdditionalRepos, want) {
			t.Fatalf("Repository.AdditionalRepos = %#v, want %#v", run.Spec.Repository.AdditionalRepos, want)
		}
	})

	t.Run("rejects invalid additional repo", func(t *testing.T) {
		_, err := srv.CreateAgentRun(context.Background(), &platform.CreateAgentRunRequest{
			Namespace:          "default",
			Source:             &platform.SourceRef{Kind: "Project", Name: "multi"},
			AdditionalRepoUrls: []string{"nonsense"},
			UserRequest:        "should fail",
		})
		if err == nil || connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Fatalf("CreateAgentRun() error = %v, want InvalidArgument", err)
		}
	})
}

// copilotDefaultsLinearProject returns a LinearProject in namespace "default"
// configured for copilot OAuth, used to exercise provider-override credential
// remapping.
func copilotDefaultsLinearProject() *triggersv1alpha1.LinearProject {
	return &triggersv1alpha1.LinearProject{
		ObjectMeta: metav1.ObjectMeta{Name: "payments-app", Namespace: "default"},
		Spec: triggersv1alpha1.LinearProjectSpec{
			LinearAPIKeySecret: "linear-secret",
			ProjectID:          "proj",
			TeamID:             "team",
			Defaults: triggersv1alpha1.AgentRunDefaults{
				RepoURL:    "https://github.com/example/repo.git",
				BaseBranch: "main",
				Image:      "ghcr.io/example/worker:latest",
				Model:      "gpt-5",
				Provider:   triggersv1alpha1.ProviderCopilot,
				AuthMode:   platformv1alpha1.AgentRunAuthModeOAuth,
				Secrets: triggersv1alpha1.AgentRunSecrets{
					OpenAIOAuthSecret: "usercred-copilot",
					GithubToken:       "github-token",
				},
			},
		},
	}
}

func fetchCreatedRun(t *testing.T, c client.Client, name string) *platformv1alpha1.AgentRun {
	t.Helper()
	run := &platformv1alpha1.AgentRun{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: name}, run); err != nil {
		t.Fatalf("Get(AgentRun) error = %v", err)
	}
	return run
}

func TestCreateAgentRunProviderPrefixRemapsToSavedOAuthCredentials(t *testing.T) {
	scheme := testProjectScheme(t)
	openaiCred := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "usercred-openai", Namespace: "default"},
		Data:       map[string][]byte{"auth.json": []byte("{}")},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&platformv1alpha1.AgentRun{}).
		WithObjects(copilotDefaultsLinearProject(), openaiCred).Build()
	srv := &Server{k8sClient: c, scheme: scheme}

	resp, err := srv.CreateAgentRun(context.Background(), &platform.CreateAgentRunRequest{
		Namespace:   "default",
		Source:      &platform.SourceRef{Kind: "LinearProject", Name: "payments-app"},
		Model:       "openai/gpt-5.3-codex",
		UserRequest: "use openai instead",
	})
	if err != nil {
		t.Fatalf("CreateAgentRun() error = %v", err)
	}

	run := fetchCreatedRun(t, c, resp.Name)
	if run.Spec.AuthMode != platformv1alpha1.AgentRunAuthModeOAuth {
		t.Fatalf("Spec.AuthMode = %q, want oauth", run.Spec.AuthMode)
	}
	if run.Spec.Secrets == nil || run.Spec.Secrets.OpenAIOAuthSecret != "usercred-openai" {
		t.Fatalf("Spec.Secrets = %#v, want OpenAIOAuthSecret=usercred-openai", run.Spec.Secrets)
	}
	if run.Spec.Secrets.GitHubTokenSecret != "github-token" {
		t.Fatalf("GitHubTokenSecret = %q, want project github-token preserved", run.Spec.Secrets.GitHubTokenSecret)
	}
	if run.Spec.Model != "gpt-5.3-codex" {
		t.Fatalf("Spec.Model = %q, want bare gpt-5.3-codex", run.Spec.Model)
	}
	if run.Spec.OpenAIBaseURL != triggersv1alpha1.DefaultOpenAIOAuthBaseURL {
		t.Fatalf("Spec.OpenAIBaseURL = %q, want %q", run.Spec.OpenAIBaseURL, triggersv1alpha1.DefaultOpenAIOAuthBaseURL)
	}
}

func TestCreateAgentRunProviderPrefixRemapsToSavedAPIKey(t *testing.T) {
	scheme := testProjectScheme(t)
	openaiCred := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "usercred-openai", Namespace: "default"},
		Data:       map[string][]byte{"api-key": []byte("sk-openai")},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&platformv1alpha1.AgentRun{}).
		WithObjects(copilotDefaultsLinearProject(), openaiCred).Build()
	srv := &Server{k8sClient: c, scheme: scheme}

	resp, err := srv.CreateAgentRun(context.Background(), &platform.CreateAgentRunRequest{
		Namespace:   "default",
		Source:      &platform.SourceRef{Kind: "LinearProject", Name: "payments-app"},
		Model:       "gemini/gemini-2.5-pro",
		UserRequest: "use gemini instead",
	})
	if err != nil {
		t.Fatalf("CreateAgentRun() error = %v", err)
	}

	run := fetchCreatedRun(t, c, resp.Name)
	if run.Spec.AuthMode != platformv1alpha1.AgentRunAuthModeAPIKey {
		t.Fatalf("Spec.AuthMode = %q, want api-key", run.Spec.AuthMode)
	}
	if run.Spec.Secrets == nil || run.Spec.Secrets.OpenAIOAuthSecret != "" {
		t.Fatalf("Spec.Secrets = %#v, want the copilot OAuth secret cleared", run.Spec.Secrets)
	}
	keys := run.Spec.Secrets.ProviderKeys
	keyFor := map[string]string{}
	for _, k := range keys {
		keyFor[k.Provider] = k.SecretName
	}
	if keyFor["gemini"] != "usercred-openai" {
		t.Fatalf("ProviderKeys = %#v, want gemini key backed by usercred-openai", keys)
	}
	// Every other saved credential is mounted too so the run can switch
	// providers mid-run without a compute restart.
	if keyFor["openai"] != "usercred-openai" {
		t.Fatalf("ProviderKeys = %#v, want saved openai key mounted for live switching", keys)
	}
	if run.Spec.Model != "gemini/gemini-2.5-pro" {
		t.Fatalf("Spec.Model = %q, want gemini/gemini-2.5-pro", run.Spec.Model)
	}
	if run.Spec.OpenAIBaseURL != triggersv1alpha1.DefaultGeminiBaseURL {
		t.Fatalf("Spec.OpenAIBaseURL = %q, want %q", run.Spec.OpenAIBaseURL, triggersv1alpha1.DefaultGeminiBaseURL)
	}
}

func TestCreateAgentRunProviderPrefixExplicitSecretWins(t *testing.T) {
	scheme := testProjectScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&platformv1alpha1.AgentRun{}).
		WithObjects(copilotDefaultsLinearProject()).Build()
	srv := &Server{k8sClient: c, scheme: scheme}

	resp, err := srv.CreateAgentRun(context.Background(), &platform.CreateAgentRunRequest{
		Namespace:         "default",
		Source:            &platform.SourceRef{Kind: "LinearProject", Name: "payments-app"},
		Model:             "openai/gpt-5.3-codex",
		OpenaiOauthSecret: "my-openai-oauth",
		UserRequest:       "explicit secret",
	})
	if err != nil {
		t.Fatalf("CreateAgentRun() error = %v", err)
	}

	run := fetchCreatedRun(t, c, resp.Name)
	if run.Spec.Secrets == nil || run.Spec.Secrets.OpenAIOAuthSecret != "my-openai-oauth" {
		t.Fatalf("Spec.Secrets = %#v, want explicit OpenAIOAuthSecret=my-openai-oauth", run.Spec.Secrets)
	}
	if run.Spec.AuthMode != platformv1alpha1.AgentRunAuthModeOAuth {
		t.Fatalf("Spec.AuthMode = %q, want oauth", run.Spec.AuthMode)
	}
}

func TestCreateAgentRunProviderPrefixPrefersSourceProviderKey(t *testing.T) {
	scheme := testProjectScheme(t)
	project := copilotDefaultsLinearProject()
	project.Spec.Defaults.Provider = triggersv1alpha1.ProviderAnthropic //nolint:staticcheck // exercising the legacy provider field
	project.Spec.Defaults.AuthMode = platformv1alpha1.AgentRunAuthModeAPIKey
	project.Spec.Defaults.Secrets = triggersv1alpha1.AgentRunSecrets{
		ClaudeApiKey: "legacy-claude",
		GithubToken:  "github-token",
		ProviderKeys: []platformv1alpha1.ProviderKeyRef{
			{Provider: "anthropic", SecretName: "proj-anthropic", SecretKey: "api-key"},
			{Provider: "openai", SecretName: "proj-openai", SecretKey: "api-key"},
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&platformv1alpha1.AgentRun{}).
		WithObjects(project).Build()
	srv := &Server{k8sClient: c, scheme: scheme}

	resp, err := srv.CreateAgentRun(context.Background(), &platform.CreateAgentRunRequest{
		Namespace:   "default",
		Source:      &platform.SourceRef{Kind: "LinearProject", Name: "payments-app"},
		Model:       "openai/gpt-5.3-codex",
		UserRequest: "use the project's openai key",
	})
	if err != nil {
		t.Fatalf("CreateAgentRun() error = %v", err)
	}

	run := fetchCreatedRun(t, c, resp.Name)
	if run.Spec.AuthMode != platformv1alpha1.AgentRunAuthModeAPIKey {
		t.Fatalf("Spec.AuthMode = %q, want api-key", run.Spec.AuthMode)
	}
	if run.Spec.Secrets == nil || run.Spec.Secrets.ClaudeAPIKeySecret != "" { //nolint:staticcheck // asserting the legacy field is cleared
		t.Fatalf("Spec.Secrets = %#v, want the anthropic legacy key cleared", run.Spec.Secrets)
	}
	found := false
	for _, key := range run.Spec.Secrets.ProviderKeys {
		if key.Provider == "openai" && key.SecretName == "proj-openai" {
			found = true
		}
	}
	if !found {
		t.Fatalf("ProviderKeys = %#v, want the source's openai key preserved", run.Spec.Secrets.ProviderKeys)
	}
}

func TestCreateAgentRunProviderPrefixMissingSavedCredsFails(t *testing.T) {
	scheme := testProjectScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&platformv1alpha1.AgentRun{}).
		WithObjects(copilotDefaultsLinearProject()).Build()
	srv := &Server{k8sClient: c, scheme: scheme}

	_, err := srv.CreateAgentRun(context.Background(), &platform.CreateAgentRunRequest{
		Namespace:   "default",
		Source:      &platform.SourceRef{Kind: "LinearProject", Name: "payments-app"},
		Model:       "openai/gpt-5.3-codex",
		UserRequest: "no creds",
	})
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("error code = %v (err=%v), want failed precondition", connect.CodeOf(err), err)
	}
	if !strings.Contains(err.Error(), "no saved OpenAI") {
		t.Fatalf("error = %q, want missing saved OpenAI credentials message", err.Error())
	}
}

func TestCreateAgentRunInheritsSourceDefaultsKubernetesAdmin(t *testing.T) {
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

	// The Kubernetes-admin grant on the defaults level is kubectl/GitOps-only
	// and shared with trigger-created runs; dashboard runs from such a source
	// must inherit it even without the Project-level admin toggle.
	project := &triggersv1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{Name: "payments", Namespace: "default"},
		Spec: triggersv1alpha1.ProjectSpec{
			DisplayName: "Payments",
			Defaults: triggersv1alpha1.AgentRunDefaults{
				RepoURL:         "https://github.com/example/repo.git",
				BaseBranch:      "main",
				Model:           "gpt-4.1",
				Provider:        triggersv1alpha1.ProviderOpenAI,
				AuthMode:        platformv1alpha1.AgentRunAuthModeAPIKey,
				KubernetesAdmin: true,
				Secrets:         triggersv1alpha1.AgentRunSecrets{ProviderKeys: []platformv1alpha1.ProviderKeyRef{{Provider: triggersv1alpha1.ProviderOpenAI, SecretName: "openai-secret"}}},
			},
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&platformv1alpha1.AgentRun{}).WithObjects(project).Build()
	srv := &Server{k8sClient: c, scheme: scheme}

	resp, err := srv.CreateAgentRun(context.Background(), &platform.CreateAgentRunRequest{
		Namespace:   "default",
		Name:        "run-admin-defaults",
		Source:      &platform.SourceRef{Kind: "Project", Name: "payments"},
		UserRequest: "do it",
	})
	if err != nil {
		t.Fatalf("CreateAgentRun() error = %v", err)
	}
	if !resp.KubernetesAdmin {
		t.Fatalf("response KubernetesAdmin = false, want true")
	}
	run := &platformv1alpha1.AgentRun{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: "run-admin-defaults"}, run); err != nil {
		t.Fatalf("Get(AgentRun) error = %v", err)
	}
	if !run.Spec.KubernetesAdmin {
		t.Fatalf("Spec.KubernetesAdmin = false, want true (inherited from source defaults)")
	}
}
