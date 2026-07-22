package dashboard

import (
	"context"
	"errors"
	"testing"
	"time"

	"connectrpc.com/connect"
	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestUpdateProjectEditsDefaultsWithoutAutoCreatingPolicies(t *testing.T) {
	scheme := testProjectScheme(t)
	ns := testUserNS()
	existing := &triggersv1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{Name: "payments", Namespace: ns},
		Spec: triggersv1alpha1.ProjectSpec{
			DisplayName: "Payments",
			Defaults: triggersv1alpha1.AgentRunDefaults{
				Provider: triggersv1alpha1.ProviderOpenAI,
				AuthMode: platformv1alpha1.AgentRunAuthModeAPIKey,
				Secrets: triggersv1alpha1.AgentRunSecrets{
					GithubToken: "github-secret",
					ProviderKeys: []platformv1alpha1.ProviderKeyRef{{
						Provider:   triggersv1alpha1.ProviderOpenAI,
						SecretName: "openai-secret",
						SecretKey:  "api-key",
					}},
				},
			},
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(existing).Build()
	srv := &Server{k8sClient: c, scheme: scheme}
	disableReviewLoop := true
	modeRef := "review"

	resp, err := srv.UpdateProject(projectActorCtx(), &platform.UpdateProjectRequest{
		Namespace:          ns,
		Name:               "payments",
		DisplayName:        "Payments API",
		RepoUrl:            "https://github.com/example/payments.git",
		AdditionalRepoUrls: []string{"https://github.com/example/payments-lib.git"},
		BaseBranch:         "develop",
		Model:              "gpt-5",
		Timeout:            "1h",
		CustomInstructions: "prefer small diffs",
		Provider:           "openai",
		AllowedModels:      []string{"gpt-5", "gpt-5-mini"},
		AuthMode:           "api-key",
		ReasoningLevel:     "medium",
		ModeRef:            &modeRef,
		ReviewLoopDisabled: &disableReviewLoop,
		GithubTokenSecret:  "github-secret",
		ProviderKeys: []*platform.ProviderKeyRef{{
			Provider:   "openai",
			SecretName: "openai-secret",
			SecretKey:  "api-key",
		}},
		RuntimeProfileRef: "shared-runtime",
		McpPolicyRef:      "shared-policy",
	})
	if err != nil {
		t.Fatalf("UpdateProject() error = %v", err)
	}
	if resp.DisplayName != "Payments API" || resp.RuntimeProfileRef != "shared-runtime" || resp.McpPolicyRef != "shared-policy" || resp.ModeRef != "review" {
		t.Fatalf("response = %#v, want updated display/runtime/mcp/mode refs", resp)
	}
	if !resp.ReviewLoopDisabled {
		t.Fatalf("ReviewLoopDisabled = false, want true")
	}

	project := &triggersv1alpha1.Project{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: "payments"}, project); err != nil {
		t.Fatalf("Get(Project) error = %v", err)
	}
	if project.Spec.ReviewLoop == nil || !project.Spec.ReviewLoop.Disabled {
		t.Fatalf("ReviewLoop = %#v, want disabled", project.Spec.ReviewLoop)
	}
	defaults := project.Spec.Defaults
	if defaults.RepoURL != "https://github.com/example/payments.git" || defaults.BaseBranch != "develop" {
		t.Fatalf("repository defaults = %q/%q, want updated repo/develop", defaults.RepoURL, defaults.BaseBranch)
	}
	if len(defaults.AdditionalRepos) != 1 || defaults.AdditionalRepos[0] != "https://github.com/example/payments-lib.git" {
		t.Fatalf("AdditionalRepos = %#v, want payments-lib", defaults.AdditionalRepos)
	}
	if defaults.Timeout.Duration != time.Hour {
		t.Fatalf("Timeout = %s, want 1h", defaults.Timeout.Duration)
	}
	if defaults.RuntimeProfileRef == nil || defaults.RuntimeProfileRef.Name != "shared-runtime" {
		t.Fatalf("RuntimeProfileRef = %#v, want shared-runtime", defaults.RuntimeProfileRef)
	}
	if defaults.MCPPolicyRef == nil || defaults.MCPPolicyRef.Name != "shared-policy" {
		t.Fatalf("MCPPolicyRef = %#v, want shared-policy", defaults.MCPPolicyRef)
	}
	if defaults.ReasoningLevel != platformv1alpha1.ReasoningMedium {
		t.Fatalf("ReasoningLevel = %q, want medium", defaults.ReasoningLevel)
	}
	if defaults.ModeRef == nil || defaults.ModeRef.Name != "review" {
		t.Fatalf("ModeRef = %#v, want review", defaults.ModeRef)
	}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: projectRuntimeProfileName("payments")}, &platformv1alpha1.RuntimeProfile{}); !apierrors.IsNotFound(err) {
		t.Fatalf("default RuntimeProfile lookup err = %v, want not found", err)
	}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: projectMCPPolicyName("payments")}, &platformv1alpha1.MCPPolicy{}); !apierrors.IsNotFound(err) {
		t.Fatalf("default MCPPolicy lookup err = %v, want not found", err)
	}
}

func TestUpdateProjectKubernetesAdminPresenceAndAdminGate(t *testing.T) {
	scheme := testProjectScheme(t)
	ns := testUserNS()
	existing := &triggersv1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{Name: "payments", Namespace: ns},
		Spec: triggersv1alpha1.ProjectSpec{
			DisplayName:     "Payments",
			ReviewLoop:      &triggersv1alpha1.ProjectReviewLoopSpec{Disabled: true},
			KubernetesAdmin: true,
			Defaults: triggersv1alpha1.AgentRunDefaults{
				Provider: triggersv1alpha1.ProviderOpenAI,
				AuthMode: platformv1alpha1.AgentRunAuthModeAPIKey,
				ModeRef:  &platformv1alpha1.ModeRef{Name: "autopilot", Version: "v2", Channel: "stable"},
				Secrets:  triggersv1alpha1.AgentRunSecrets{ProviderKeys: []platformv1alpha1.ProviderKeyRef{{Provider: triggersv1alpha1.ProviderOpenAI, SecretName: "openai-secret"}}},
			},
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(existing).Build()
	srv := &Server{k8sClient: c, scheme: scheme}

	resp, err := srv.UpdateProject(projectActorCtx(), &platform.UpdateProjectRequest{
		Namespace:    ns,
		Name:         "payments",
		DisplayName:  "Payments",
		Provider:     "openai",
		AuthMode:     "api-key",
		ProviderKeys: []*platform.ProviderKeyRef{{Provider: "openai", SecretName: "openai-secret"}},
	})
	if err != nil {
		t.Fatalf("UpdateProject() preserve error = %v", err)
	}
	if !resp.KubernetesAdmin {
		t.Fatalf("KubernetesAdmin after omitted update = false, want preserved true")
	}
	if !resp.ReviewLoopDisabled {
		t.Fatalf("ReviewLoopDisabled after omitted update = false, want preserved true")
	}
	if resp.ModeRef != "autopilot" {
		t.Fatalf("ModeRef after omitted update = %q, want preserved autopilot", resp.ModeRef)
	}
	preserved := &triggersv1alpha1.Project{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: "payments"}, preserved); err != nil {
		t.Fatalf("Get(Project) after omitted mode update: %v", err)
	}
	if got := preserved.Spec.Defaults.ModeRef; got == nil || got.Name != "autopilot" || got.Version != "v2" || got.Channel != "stable" {
		t.Fatalf("ModeRef after omitted update = %#v, want autopilot@v2 (stable)", got)
	}

	clear := false
	_, err = srv.UpdateProject(projectActorCtx(), &platform.UpdateProjectRequest{
		Namespace:       ns,
		Name:            "payments",
		DisplayName:     "Payments",
		Provider:        "openai",
		AuthMode:        "api-key",
		ProviderKeys:    []*platform.ProviderKeyRef{{Provider: "openai", SecretName: "openai-secret"}},
		KubernetesAdmin: &clear,
	})
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("UpdateProject() non-admin change error = %v, want PermissionDenied", err)
	}

	clearMode := ""
	resp, err = srv.UpdateProject(actorContext("admin-1", "admin", "", ""), &platform.UpdateProjectRequest{
		Namespace:       ns,
		Name:            "payments",
		DisplayName:     "Payments",
		Provider:        "openai",
		AuthMode:        "api-key",
		ProviderKeys:    []*platform.ProviderKeyRef{{Provider: "openai", SecretName: "openai-secret"}},
		KubernetesAdmin: &clear,
		ModeRef:         &clearMode,
	})
	if err != nil {
		t.Fatalf("UpdateProject() admin clear error = %v", err)
	}
	if resp.KubernetesAdmin {
		t.Fatalf("KubernetesAdmin after admin clear = true, want false")
	}
	if resp.ModeRef != "" {
		t.Fatalf("ModeRef after explicit clear = %q, want empty", resp.ModeRef)
	}
}

func TestUpdateProjectCanConfigureRuntimeProfileAndMCPPolicy(t *testing.T) {
	scheme := testProjectScheme(t)
	ns := testUserNS()
	existing := &triggersv1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{Name: "payments", Namespace: ns},
		Spec: triggersv1alpha1.ProjectSpec{
			DisplayName: "Payments",
			Defaults: triggersv1alpha1.AgentRunDefaults{
				Provider: triggersv1alpha1.ProviderOpenAI,
				AuthMode: platformv1alpha1.AgentRunAuthModeAPIKey,
				Secrets: triggersv1alpha1.AgentRunSecrets{ProviderKeys: []platformv1alpha1.ProviderKeyRef{{
					Provider:   triggersv1alpha1.ProviderOpenAI,
					SecretName: "openai-secret",
				}}},
			},
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(existing).Build()
	srv := &Server{k8sClient: c, scheme: scheme}

	resp, err := srv.UpdateProject(projectActorCtx(), &platform.UpdateProjectRequest{
		Namespace:               ns,
		Name:                    "payments",
		DisplayName:             "Payments",
		Provider:                "openai",
		AuthMode:                "api-key",
		ProviderKeys:            []*platform.ProviderKeyRef{{Provider: "openai", SecretName: "openai-secret"}},
		ConfigureRuntimeProfile: true,
		PermissionMode:          "read-only",
		EgressMode:              "disabled",
		ConfigureMcpPolicy:      true,
		McpPolicyDefaultAction:  "Deny",
		McpPolicyAllowedServers: []string{"fetch", "github"},
	})
	if err != nil {
		t.Fatalf("UpdateProject() error = %v", err)
	}
	if resp.RuntimeProfileRef != "payments-runtime" || resp.PermissionMode != "read-only" || resp.EgressMode != "disabled" {
		t.Fatalf("runtime response = %q/%q/%q, want payments-runtime/read-only/disabled", resp.RuntimeProfileRef, resp.PermissionMode, resp.EgressMode)
	}
	if resp.McpPolicyRef != "payments-mcp-policy" || resp.McpPolicyDefaultAction != "Deny" {
		t.Fatalf("mcp response = %q/%q, want payments-mcp-policy/Deny", resp.McpPolicyRef, resp.McpPolicyDefaultAction)
	}

	profile := &platformv1alpha1.RuntimeProfile{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: "payments-runtime"}, profile); err != nil {
		t.Fatalf("Get(RuntimeProfile) error = %v", err)
	}
	if profile.Spec.Security == nil || profile.Spec.Security.PermissionMode != platformv1alpha1.PermissionModeReadOnly || profile.Spec.Security.EgressMode != platformv1alpha1.EgressMode("disabled") {
		t.Fatalf("RuntimeProfile security = %#v, want read-only/disabled", profile.Spec.Security)
	}
	policy := &platformv1alpha1.MCPPolicy{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: "payments-mcp-policy"}, policy); err != nil {
		t.Fatalf("Get(MCPPolicy) error = %v", err)
	}
	if policy.Spec.DefaultAction != platformv1alpha1.MCPDefaultActionDeny || len(policy.Spec.AllowedServers) != 2 {
		t.Fatalf("MCPPolicy spec = %#v, want Deny with two allowed servers", policy.Spec)
	}
}

func TestUpdateProjectUsesSavedCredentials(t *testing.T) {
	scheme := testProjectScheme(t)
	ns := testUserNS()
	existing := &triggersv1alpha1.Project{ObjectMeta: metav1.ObjectMeta{Name: "saved", Namespace: ns}, Spec: triggersv1alpha1.ProjectSpec{DisplayName: "Saved"}}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(existing).Build()
	srv := &Server{k8sClient: c, scheme: scheme}

	for _, secret := range []*corev1.Secret{
		{
			ObjectMeta: metav1.ObjectMeta{Name: userCredentialSecretName(triggersv1alpha1.ProviderAnthropic), Namespace: ns},
			Data:       map[string][]byte{userCredAPIKeyKey: []byte("sk-ant-saved")},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: userCredentialSecretName(credentialGitHub), Namespace: ns},
			Data:       map[string][]byte{userCredGithubTokenKey: []byte("gh-saved")},
		},
	} {
		if err := c.Create(context.Background(), secret); err != nil {
			t.Fatalf("seed secret: %v", err)
		}
	}

	_, err := srv.UpdateProject(projectActorCtx(), &platform.UpdateProjectRequest{
		Namespace:               ns,
		Name:                    "saved",
		DisplayName:             "Saved",
		Provider:                "anthropic",
		AuthMode:                "api-key",
		UseSavedCredentials:     true,
		ConfigureMcpPolicy:      false,
		ConfigureRuntimeProfile: false,
	})
	if err != nil {
		t.Fatalf("UpdateProject() error = %v", err)
	}

	project := &triggersv1alpha1.Project{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: "saved"}, project); err != nil {
		t.Fatalf("Get(Project) error = %v", err)
	}
	keys := project.Spec.Defaults.Secrets.ProviderKeys
	if len(keys) != 1 || keys[0].Provider != triggersv1alpha1.ProviderAnthropic || keys[0].SecretName != userCredentialSecretName(triggersv1alpha1.ProviderAnthropic) {
		t.Fatalf("ProviderKeys = %#v, want saved anthropic key", keys)
	}
	if project.Spec.Defaults.Secrets.GithubToken != userCredentialSecretName(credentialGitHub) {
		t.Fatalf("GithubToken = %q, want saved github token", project.Spec.Defaults.Secrets.GithubToken)
	}
}

func TestUpdateProjectRejectsProviderWithoutMatchingSecretRef(t *testing.T) {
	scheme := testProjectScheme(t)
	ns := testUserNS()
	existing := &triggersv1alpha1.Project{ObjectMeta: metav1.ObjectMeta{Name: "payments", Namespace: ns}, Spec: triggersv1alpha1.ProjectSpec{DisplayName: "Payments"}}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(existing).Build()
	srv := &Server{k8sClient: c, scheme: scheme}

	_, err := srv.UpdateProject(projectActorCtx(), &platform.UpdateProjectRequest{
		Namespace:   ns,
		Name:        "payments",
		DisplayName: "Payments",
		Provider:    "openrouter",
		AuthMode:    "api-key",
		ProviderKeys: []*platform.ProviderKeyRef{{
			Provider:   "openai",
			SecretName: "openai-secret",
		}},
	})
	if err == nil {
		t.Fatalf("UpdateProject() error = nil, want invalid argument")
	}
	var connectErr *connect.Error
	if !errors.As(err, &connectErr) || connectErr.Code() != connect.CodeInvalidArgument {
		t.Fatalf("UpdateProject() error = %v, want invalid argument", err)
	}
}

func TestUpdateProjectRoundtripsMCPServerAndSkillRefs(t *testing.T) {
	scheme := testProjectScheme(t)
	ns := testUserNS()
	existing := &triggersv1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{Name: "payments", Namespace: ns},
		Spec: triggersv1alpha1.ProjectSpec{
			DisplayName: "Payments",
			Defaults: triggersv1alpha1.AgentRunDefaults{
				Provider: triggersv1alpha1.ProviderOpenAI,
				AuthMode: platformv1alpha1.AgentRunAuthModeAPIKey,
				Secrets: triggersv1alpha1.AgentRunSecrets{ProviderKeys: []platformv1alpha1.ProviderKeyRef{{
					Provider:   triggersv1alpha1.ProviderOpenAI,
					SecretName: "openai-secret",
				}}},
			},
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(existing).Build()
	srv := &Server{k8sClient: c, scheme: scheme}

	resp, err := srv.UpdateProject(projectActorCtx(), &platform.UpdateProjectRequest{
		Namespace:     ns,
		Name:          "payments",
		DisplayName:   "Payments",
		Provider:      "openai",
		AuthMode:      "api-key",
		ProviderKeys:  []*platform.ProviderKeyRef{{Provider: "openai", SecretName: "openai-secret"}},
		McpServerRefs: []string{"grafana", " grafana ", "", "browser-playwright"},
		SkillRefs:     []string{"pdf", " pdf ", ""},
	})
	if err != nil {
		t.Fatalf("UpdateProject() error = %v", err)
	}
	if got := resp.McpServerRefs; len(got) != 2 || got[0] != "grafana" || got[1] != "browser-playwright" {
		t.Fatalf("McpServerRefs = %v, want deduped [grafana browser-playwright]", got)
	}
	if got := resp.SkillRefs; len(got) != 1 || got[0] != "pdf" {
		t.Fatalf("SkillRefs = %v, want deduped [pdf]", got)
	}

	stored := &triggersv1alpha1.Project{}
	if err := c.Get(projectActorCtx(), client.ObjectKey{Namespace: ns, Name: "payments"}, stored); err != nil {
		t.Fatalf("Get(Project) error = %v", err)
	}
	if refs := stored.Spec.Defaults.MCPServerRefs; len(refs) != 2 || refs[0].Name != "grafana" {
		t.Fatalf("stored refs = %+v", refs)
	}
	if refs := stored.Spec.Defaults.SkillRefs; len(refs) != 1 || refs[0].Name != "pdf" {
		t.Fatalf("stored skill refs = %+v", refs)
	}
}
