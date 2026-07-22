package dashboard

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"connectrpc.com/connect"
	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func testProjectScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
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
	return scheme
}

const (
	testProjectSubject = "user-1"
	testProjectUser    = "Test User"
)

// projectActorCtx returns a context carrying an authenticated actor, as required
// by CreateProject (which provisions the user's personal namespace).
func projectActorCtx() context.Context {
	return context.WithValue(context.Background(), requestActorContextKey{}, requestActor{Subject: testProjectSubject, Name: testProjectUser})
}

// testUserNS is the namespace CreateProject derives for the test actor.
func testUserNS() string {
	return deriveUserNamespaceName(testProjectUser, testProjectSubject)
}

func TestCreateProjectCreatesCRDAndCredentialsSecret(t *testing.T) {
	scheme := testProjectScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	srv := &Server{k8sClient: c, scheme: scheme}
	ns := testUserNS()
	modeRef := "autopilot"

	resp, err := srv.CreateProject(projectActorCtx(), &platform.CreateProjectRequest{
		Name:                    "payments",
		DisplayName:             "Payments",
		RepoUrl:                 "https://github.com/example/payments.git",
		BaseBranch:              "main",
		Model:                   "gpt-4.1",
		Image:                   "ghcr.io/example/worker:latest",
		Timeout:                 "45m",
		CustomInstructions:      "do the thing",
		Provider:                "openai",
		AllowedModels:           []string{"gpt-4.1", "gpt-4o"},
		AuthMode:                "api-key",
		ReasoningLevel:          "high",
		ModeRef:                 &modeRef,
		GithubToken:             "gh-token",
		OpenaiApiKey:            "sk-test",
		ConfigureRuntimeProfile: true,
		RuntimeProfileRef:       "payments-runtime",
		PermissionMode:          "workspace-write",
		EgressMode:              "unrestricted",
		ConfigureMcpPolicy:      true,
		McpPolicyRef:            "payments-policy",
		McpPolicyDefaultAction:  "Deny",
		McpPolicyAllowedServers: []string{"fetch", "github"},
	})
	if err != nil {
		t.Fatalf("CreateProject() error = %v", err)
	}
	if resp.Namespace != ns {
		t.Fatalf("project namespace = %q, want %q", resp.Namespace, ns)
	}
	if resp.CredentialStatus == nil || !resp.CredentialStatus.GithubTokenPresent || !resp.CredentialStatus.OpenaiApiKeyPresent {
		t.Fatalf("CredentialStatus = %#v, want github/openai present", resp.CredentialStatus)
	}
	if resp.CredentialStatus.AnthropicApiKeyPresent {
		t.Fatalf("CredentialStatus = %#v, want anthropic absent", resp.CredentialStatus)
	}
	if resp.RuntimeProfileRef != "payments-runtime" {
		t.Fatalf("RuntimeProfileRef = %q, want payments-runtime", resp.RuntimeProfileRef)
	}
	if resp.McpPolicyRef != "payments-policy" {
		t.Fatalf("McpPolicyRef = %q, want payments-policy", resp.McpPolicyRef)
	}
	if resp.ReasoningLevel != "high" {
		t.Fatalf("ReasoningLevel = %q, want high", resp.ReasoningLevel)
	}
	if resp.ModeRef != "autopilot" {
		t.Fatalf("ModeRef = %q, want autopilot", resp.ModeRef)
	}
	if !resp.ReviewLoopDisabled {
		t.Fatalf("ReviewLoopDisabled = false, want true")
	}

	project := &triggersv1alpha1.Project{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: "payments"}, project); err != nil {
		t.Fatalf("Get(Project) error = %v", err)
	}
	if project.Spec.DisplayName != "Payments" {
		t.Fatalf("DisplayName = %q, want Payments", project.Spec.DisplayName)
	}
	if project.Spec.ReviewLoop == nil || !project.Spec.ReviewLoop.Disabled {
		t.Fatalf("ReviewLoop = %#v, want disabled", project.Spec.ReviewLoop)
	}
	if project.Spec.Defaults.Secrets.GithubToken != "payments-credentials" {
		t.Fatalf("GithubToken secret = %q, want payments-credentials", project.Spec.Defaults.Secrets.GithubToken)
	}
	if len(project.Spec.Defaults.Secrets.ProviderKeys) != 1 {
		t.Fatalf("ProviderKeys = %#v, want 1 entry", project.Spec.Defaults.Secrets.ProviderKeys)
	}
	pk := project.Spec.Defaults.Secrets.ProviderKeys[0]
	if pk.Provider != triggersv1alpha1.ProviderOpenAI || pk.SecretName != "payments-credentials" || pk.SecretKey != openAIAPIKeySecretKey {
		t.Fatalf("ProviderKey = %#v, want openai/payments-credentials/%s", pk, openAIAPIKeySecretKey)
	}
	if project.Spec.Defaults.Secrets.OpenAIOAuthSecret != "" {
		t.Fatalf("OpenAIOAuthSecret = %q, want empty", project.Spec.Defaults.Secrets.OpenAIOAuthSecret)
	}
	if project.Spec.Defaults.Timeout.Duration != 45*time.Minute {
		t.Fatalf("Timeout = %s, want 45m", project.Spec.Defaults.Timeout.Duration)
	}
	if project.Spec.Defaults.RuntimeProfileRef == nil || project.Spec.Defaults.RuntimeProfileRef.Name != "payments-runtime" {
		t.Fatalf("RuntimeProfileRef = %#v, want payments-runtime", project.Spec.Defaults.RuntimeProfileRef)
	}
	if project.Spec.Defaults.MCPPolicyRef == nil || project.Spec.Defaults.MCPPolicyRef.Name != "payments-policy" {
		t.Fatalf("MCPPolicyRef = %#v, want payments-policy", project.Spec.Defaults.MCPPolicyRef)
	}
	if project.Spec.Defaults.ReasoningLevel != platformv1alpha1.ReasoningHigh {
		t.Fatalf("ReasoningLevel = %q, want high", project.Spec.Defaults.ReasoningLevel)
	}
	if project.Spec.Defaults.ModeRef == nil || project.Spec.Defaults.ModeRef.Name != "autopilot" {
		t.Fatalf("ModeRef = %#v, want autopilot", project.Spec.Defaults.ModeRef)
	}

	profile := &platformv1alpha1.RuntimeProfile{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: "payments-runtime"}, profile); err != nil {
		t.Fatalf("Get(RuntimeProfile) error = %v", err)
	}
	if profile.Spec.Security == nil {
		t.Fatal("RuntimeProfile.Spec.Security is nil")
	}
	if profile.Spec.Security.PermissionMode != platformv1alpha1.PermissionModeWorkspaceWrite {
		t.Fatalf("PermissionMode = %q, want workspace-write", profile.Spec.Security.PermissionMode)
	}
	if profile.Spec.Security.EgressMode != platformv1alpha1.EgressMode("unrestricted") {
		t.Fatalf("EgressMode = %q, want unrestricted", profile.Spec.Security.EgressMode)
	}

	policy := &platformv1alpha1.MCPPolicy{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: "payments-policy"}, policy); err != nil {
		t.Fatalf("Get(MCPPolicy) error = %v", err)
	}
	if policy.Spec.DefaultAction != platformv1alpha1.MCPDefaultActionDeny {
		t.Fatalf("DefaultAction = %q, want Deny", policy.Spec.DefaultAction)
	}
	if got := len(policy.Spec.AllowedServers); got != 2 {
		t.Fatalf("AllowedServers len = %d, want 2", got)
	}
	if policy.Spec.AllowedServers[0].Name != "fetch" || policy.Spec.AllowedServers[1].Name != "github" {
		t.Fatalf("AllowedServers = %#v, want fetch/github", policy.Spec.AllowedServers)
	}

	secret := &corev1.Secret{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: "payments-credentials"}, secret); err != nil {
		t.Fatalf("Get(Secret) error = %v", err)
	}
	gotGithub := string(secret.Data[githubTokenSecretKey])
	if gotGithub == "" {
		gotGithub = secret.StringData[githubTokenSecretKey]
	}
	if gotGithub != "gh-token" {
		t.Fatalf("github token = %q, want gh-token", gotGithub)
	}
	gotOpenAI := string(secret.Data[openAIAPIKeySecretKey])
	if gotOpenAI == "" {
		gotOpenAI = secret.StringData[openAIAPIKeySecretKey]
	}
	if gotOpenAI != "sk-test" {
		t.Fatalf("openai key = %q, want sk-test", gotOpenAI)
	}
}

func TestCreateGratefulAgentsBootstrapShapePinsIssueMode(t *testing.T) {
	scheme := testProjectScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	stateStore := newMockStateStore()
	srv := &Server{k8sClient: c, scheme: scheme, stateStore: stateStore}
	ns := testUserNS()

	_, err := srv.CreateProject(projectActorCtx(), &platform.CreateProjectRequest{
		Name:                    gratefulAgentsProjectName,
		DisplayName:             "Grateful Agents",
		RepoUrl:                 gratefulAgentsRepoURL,
		AdditionalRepoUrls:      []string{gratefulAgentsSDKRepoURL},
		BaseBranch:              "main",
		Provider:                "openai",
		AuthMode:                "api-key",
		OpenaiApiKey:            "test-provider-key",
		GithubToken:             "test-github-token",
		ConfigureRuntimeProfile: true,
		PermissionMode:          "workspace-write",
		EgressMode:              "unrestricted",
	})
	if err != nil {
		t.Fatalf("CreateProject() error = %v", err)
	}

	project := &triggersv1alpha1.Project{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: gratefulAgentsProjectName}, project); err != nil {
		t.Fatalf("Get(Grateful Agents project) error = %v", err)
	}
	if project.Spec.Defaults.ModeRef == nil || project.Spec.Defaults.ModeRef.Name != gratefulAgentsModeName {
		t.Fatalf("ModeRef = %#v, want %q", project.Spec.Defaults.ModeRef, gratefulAgentsModeName)
	}
	ownership, err := stateStore.GetResourceOwner(context.Background(), projectResourceType, gratefulAgentsProjectName, ns)
	if err != nil {
		t.Fatalf("GetResourceOwner(Grateful Agents project) error = %v", err)
	}
	if ownership == nil || ownership.OwnerID != testProjectSubject {
		t.Fatalf("Grateful Agents project ownership = %#v, want owner %q", ownership, testProjectSubject)
	}
}

func TestCreateGratefulAgentsBootstrapAllowsExplicitInteractiveMode(t *testing.T) {
	scheme := testProjectScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	srv := &Server{k8sClient: c, scheme: scheme}
	ns := testUserNS()
	interactiveDefault := ""

	_, err := srv.CreateProject(projectActorCtx(), &platform.CreateProjectRequest{
		Name:                    gratefulAgentsProjectName,
		DisplayName:             "Grateful Agents",
		RepoUrl:                 gratefulAgentsRepoURL,
		AdditionalRepoUrls:      []string{gratefulAgentsSDKRepoURL},
		BaseBranch:              "main",
		Provider:                "openai",
		AuthMode:                "api-key",
		OpenaiApiKey:            "test-openai-key",
		ModeRef:                 &interactiveDefault,
		ConfigureRuntimeProfile: true,
		PermissionMode:          "workspace-write",
		EgressMode:              "unrestricted",
	})
	if err != nil {
		t.Fatalf("CreateProject() error = %v", err)
	}

	project := &triggersv1alpha1.Project{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: gratefulAgentsProjectName}, project); err != nil {
		t.Fatalf("Get(Grateful Agents project) error = %v", err)
	}
	if project.Spec.Defaults.ModeRef != nil {
		t.Fatalf("ModeRef = %#v, want nil for explicit interactive default", project.Spec.Defaults.ModeRef)
	}
}

func TestGratefulAgentsBootstrapShapeRequiresCanonicalRepositories(t *testing.T) {
	tests := []struct {
		name       string
		project    string
		repoURL    string
		additional []string
		want       bool
	}{
		{name: "canonical", project: gratefulAgentsProjectName, repoURL: gratefulAgentsRepoURL, additional: []string{gratefulAgentsSDKRepoURL}, want: true},
		{name: "different name", project: "support", repoURL: gratefulAgentsRepoURL, additional: []string{gratefulAgentsSDKRepoURL}},
		{name: "different primary", project: gratefulAgentsProjectName, repoURL: "https://example.com/core.git", additional: []string{gratefulAgentsSDKRepoURL}},
		{name: "missing SDK", project: gratefulAgentsProjectName, repoURL: gratefulAgentsRepoURL},
		{name: "extra repository", project: gratefulAgentsProjectName, repoURL: gratefulAgentsRepoURL, additional: []string{gratefulAgentsSDKRepoURL, "https://example.com/extra.git"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isGratefulAgentsBootstrapProject(tc.project, tc.repoURL, tc.additional); got != tc.want {
				t.Fatalf("isGratefulAgentsBootstrapProject() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestCreateProjectKubernetesAdminRequiresAdmin(t *testing.T) {
	scheme := testProjectScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	srv := &Server{k8sClient: c, scheme: scheme}

	_, err := srv.CreateProject(projectActorCtx(), &platform.CreateProjectRequest{
		Name:            "admin-project",
		DisplayName:     "Admin Project",
		KubernetesAdmin: true,
	})
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("CreateProject() error = %v, want PermissionDenied", err)
	}
}

func TestCreateProjectKubernetesAdminAdminAllowed(t *testing.T) {
	scheme := testProjectScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	srv := &Server{k8sClient: c, scheme: scheme}
	resp, err := srv.CreateProject(actorContext("admin-1", "admin", "", ""), &platform.CreateProjectRequest{
		Name:            "admin-project",
		DisplayName:     "Admin Project",
		Provider:        "openai",
		AuthMode:        "api-key",
		OpenaiApiKey:    "sk-test",
		KubernetesAdmin: true,
	})
	if err != nil {
		t.Fatalf("CreateProject() error = %v", err)
	}
	if !resp.KubernetesAdmin {
		t.Fatalf("response KubernetesAdmin = false, want true")
	}
	project := &triggersv1alpha1.Project{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: resp.Namespace, Name: "admin-project"}, project); err != nil {
		t.Fatalf("Get(Project) error = %v", err)
	}
	if !project.Spec.KubernetesAdmin {
		t.Fatalf("Project.Spec.KubernetesAdmin = false, want true")
	}
}

func TestCreateProjectAllowsNoRepository(t *testing.T) {
	scheme := testProjectScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	srv := &Server{k8sClient: c, scheme: scheme}
	ns := testUserNS()

	_, err := srv.CreateProject(projectActorCtx(), &platform.CreateProjectRequest{
		Name:         "repoless",
		DisplayName:  "Repoless",
		Provider:     "openai",
		AuthMode:     "api-key",
		OpenaiApiKey: "sk-test",
	})
	if err != nil {
		t.Fatalf("CreateProject() with no repo error = %v", err)
	}

	project := &triggersv1alpha1.Project{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: "repoless"}, project); err != nil {
		t.Fatalf("Get(Project) error = %v", err)
	}
	if project.Spec.Defaults.RepoURL != "" {
		t.Fatalf("RepoURL = %q, want empty", project.Spec.Defaults.RepoURL)
	}
	if project.Spec.Defaults.BaseBranch != "" {
		t.Fatalf("BaseBranch = %q, want empty (no default branch without a repo)", project.Spec.Defaults.BaseBranch)
	}
	if project.Spec.Defaults.RuntimeProfileRef != nil {
		t.Fatalf("RuntimeProfileRef = %#v, want nil by default", project.Spec.Defaults.RuntimeProfileRef)
	}
	if project.Spec.Defaults.MCPPolicyRef != nil {
		t.Fatalf("MCPPolicyRef = %#v, want nil by default", project.Spec.Defaults.MCPPolicyRef)
	}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: projectRuntimeProfileName("repoless")}, &platformv1alpha1.RuntimeProfile{}); !apierrors.IsNotFound(err) {
		t.Fatalf("default RuntimeProfile lookup err = %v, want not found", err)
	}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: projectMCPPolicyName("repoless")}, &platformv1alpha1.MCPPolicy{}); !apierrors.IsNotFound(err) {
		t.Fatalf("default MCPPolicy lookup err = %v, want not found", err)
	}
}

func TestCreateProjectStoresAdditionalRepos(t *testing.T) {
	scheme := testProjectScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	srv := &Server{k8sClient: c, scheme: scheme}
	ns := testUserNS()

	resp, err := srv.CreateProject(projectActorCtx(), &platform.CreateProjectRequest{
		Name:               "multi-repo",
		DisplayName:        "Multi Repo",
		RepoUrl:            "https://github.com/example/app.git",
		BaseBranch:         "main",
		AdditionalRepoUrls: []string{"https://github.com/example/lib.git", "https://github.com/example/tools.git"},
		Provider:           "openai",
		AuthMode:           "api-key",
		OpenaiApiKey:       "sk-test",
	})
	if err != nil {
		t.Fatalf("CreateProject() error = %v", err)
	}
	wantRepos := []string{"https://github.com/example/lib.git", "https://github.com/example/tools.git"}
	if !reflect.DeepEqual(resp.AdditionalRepoUrls, wantRepos) {
		t.Fatalf("resp.AdditionalRepoUrls = %#v, want %#v", resp.AdditionalRepoUrls, wantRepos)
	}

	project := &triggersv1alpha1.Project{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: "multi-repo"}, project); err != nil {
		t.Fatalf("Get(Project) error = %v", err)
	}
	if !reflect.DeepEqual(project.Spec.Defaults.AdditionalRepos, wantRepos) {
		t.Fatalf("Defaults.AdditionalRepos = %#v, want %#v", project.Spec.Defaults.AdditionalRepos, wantRepos)
	}
}

func TestCreateProjectRejectsInvalidAdditionalRepo(t *testing.T) {
	scheme := testProjectScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	srv := &Server{k8sClient: c, scheme: scheme}

	_, err := srv.CreateProject(projectActorCtx(), &platform.CreateProjectRequest{
		Name:               "bad-extra-repo",
		DisplayName:        "Bad Extra Repo",
		RepoUrl:            "https://github.com/example/app.git",
		AdditionalRepoUrls: []string{"not-a-git-url"},
		Provider:           "openai",
		AuthMode:           "api-key",
		OpenaiApiKey:       "sk-test",
	})
	if err == nil || connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("CreateProject() error = %v, want InvalidArgument", err)
	}
}

func TestCreateProjectRequiresOpenAIProviderCredentialForAPIKeyMode(t *testing.T) {
	scheme := testProjectScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	srv := &Server{k8sClient: c, scheme: scheme}

	_, err := srv.CreateProject(projectActorCtx(), &platform.CreateProjectRequest{
		Name:        "openai-project",
		DisplayName: "OpenAI Project",
		RepoUrl:     "https://github.com/example/repo.git",
		BaseBranch:  "main",
		Provider:    "openai",
		AuthMode:    "api-key",
	})
	if err == nil {
		t.Fatalf("CreateProject() error = nil, want invalid argument")
	}
	var connectErr *connect.Error
	if !errors.As(err, &connectErr) || connectErr.Code() != connect.CodeInvalidArgument {
		t.Fatalf("CreateProject() error = %v, want invalid argument", err)
	}
}

func TestCreateProjectRequiresAnthropicProviderCredentialForAPIKeyMode(t *testing.T) {
	scheme := testProjectScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	srv := &Server{k8sClient: c, scheme: scheme}

	_, err := srv.CreateProject(projectActorCtx(), &platform.CreateProjectRequest{
		Name:        "anthropic-project",
		DisplayName: "Anthropic Project",
		RepoUrl:     "https://github.com/example/repo.git",
		BaseBranch:  "main",
		Provider:    "anthropic",
		AuthMode:    "api-key",
	})
	if err == nil {
		t.Fatalf("CreateProject() error = nil, want invalid argument")
	}
	var connectErr *connect.Error
	if !errors.As(err, &connectErr) || connectErr.Code() != connect.CodeInvalidArgument {
		t.Fatalf("CreateProject() error = %v, want invalid argument", err)
	}
}

func TestCreateProjectRequiresAuthentication(t *testing.T) {
	scheme := testProjectScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	srv := &Server{k8sClient: c, scheme: scheme}

	_, err := srv.CreateProject(context.Background(), &platform.CreateProjectRequest{
		Name:         "payments",
		DisplayName:  "Payments",
		Provider:     "openai",
		AuthMode:     "api-key",
		OpenaiApiKey: "sk-test",
	})
	var connectErr *connect.Error
	if !errors.As(err, &connectErr) || connectErr.Code() != connect.CodeUnauthenticated {
		t.Fatalf("CreateProject() error = %v, want unauthenticated", err)
	}
}

func TestCreateProjectUsesSavedCredentials(t *testing.T) {
	scheme := testProjectScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	srv := &Server{k8sClient: c, scheme: scheme}
	ns := testUserNS()

	// Seed the user's saved anthropic API key + github token in their namespace.
	saved := []*corev1.Secret{
		{
			ObjectMeta: metav1.ObjectMeta{Name: userCredentialSecretName(triggersv1alpha1.ProviderAnthropic), Namespace: ns},
			Data:       map[string][]byte{userCredAPIKeyKey: []byte("sk-ant-saved")},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: userCredentialSecretName(credentialGitHub), Namespace: ns},
			Data:       map[string][]byte{userCredGithubTokenKey: []byte("gh-saved")},
		},
	}
	for _, s := range saved {
		if err := c.Create(context.Background(), s); err != nil {
			t.Fatalf("seed secret: %v", err)
		}
	}

	_, err := srv.CreateProject(projectActorCtx(), &platform.CreateProjectRequest{
		Name:                "saved",
		DisplayName:         "Saved",
		Provider:            "anthropic",
		AuthMode:            "api-key",
		UseSavedCredentials: true,
	})
	if err != nil {
		t.Fatalf("CreateProject() error = %v", err)
	}

	project := &triggersv1alpha1.Project{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: "saved"}, project); err != nil {
		t.Fatalf("Get(Project) error = %v", err)
	}
	keys := project.Spec.Defaults.Secrets.ProviderKeys
	if len(keys) != 1 || keys[0].Provider != triggersv1alpha1.ProviderAnthropic ||
		keys[0].SecretName != userCredentialSecretName(triggersv1alpha1.ProviderAnthropic) || keys[0].SecretKey != userCredAPIKeyKey {
		t.Fatalf("ProviderKeys = %#v, want saved anthropic credential", keys)
	}
	if project.Spec.Defaults.Secrets.GithubToken != userCredentialSecretName(credentialGitHub) {
		t.Fatalf("GithubToken = %q, want saved github secret", project.Spec.Defaults.Secrets.GithubToken)
	}
	// No per-project credentials Secret should have been created.
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: projectCredentialsSecretName("saved")}, &corev1.Secret{}); !apierrors.IsNotFound(err) {
		t.Fatalf("unexpected per-project secret, err = %v", err)
	}
}

func TestCreateProjectUseSavedCredentialsMissingFails(t *testing.T) {
	scheme := testProjectScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	srv := &Server{k8sClient: c, scheme: scheme}

	_, err := srv.CreateProject(projectActorCtx(), &platform.CreateProjectRequest{
		Name:                "saved",
		DisplayName:         "Saved",
		Provider:            "anthropic",
		AuthMode:            "api-key",
		UseSavedCredentials: true,
	})
	var connectErr *connect.Error
	if !errors.As(err, &connectErr) || connectErr.Code() != connect.CodeFailedPrecondition {
		t.Fatalf("CreateProject() error = %v, want failed precondition", err)
	}
}

func TestCreateProjectReturnsAlreadyExistsWhenProjectExists(t *testing.T) {
	scheme := testProjectScheme(t)
	ns := testUserNS()
	existing := &triggersv1alpha1.Project{ObjectMeta: metav1.ObjectMeta{Name: "payments", Namespace: ns}}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(existing).Build()
	srv := &Server{k8sClient: c, scheme: scheme}

	_, err := srv.CreateProject(projectActorCtx(), &platform.CreateProjectRequest{
		Name:         "payments",
		DisplayName:  "Payments",
		RepoUrl:      "https://github.com/example/payments.git",
		BaseBranch:   "main",
		Provider:     "openai",
		AuthMode:     "api-key",
		OpenaiApiKey: "sk-test",
	})
	if err == nil {
		t.Fatalf("CreateProject() error = nil, want already exists")
	}
	var connectErr *connect.Error
	if !errors.As(err, &connectErr) || connectErr.Code() != connect.CodeAlreadyExists {
		t.Fatalf("CreateProject() error = %v, want already exists", err)
	}
}

func TestCreateProjectReturnsAlreadyExistsWhenCredentialSecretExists(t *testing.T) {
	scheme := testProjectScheme(t)
	ns := testUserNS()
	existingSecret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "payments-credentials", Namespace: ns}}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(existingSecret).Build()
	srv := &Server{k8sClient: c, scheme: scheme}

	_, err := srv.CreateProject(projectActorCtx(), &platform.CreateProjectRequest{
		Name:         "payments",
		DisplayName:  "Payments",
		RepoUrl:      "https://github.com/example/payments.git",
		BaseBranch:   "main",
		Provider:     "openai",
		AuthMode:     "api-key",
		GithubToken:  "gh-token",
		OpenaiApiKey: "sk-test",
	})
	if err == nil {
		t.Fatalf("CreateProject() error = nil, want already exists")
	}
	var connectErr *connect.Error
	if !errors.As(err, &connectErr) || connectErr.Code() != connect.CodeAlreadyExists {
		t.Fatalf("CreateProject() error = %v, want already exists", err)
	}
}

func TestCreateProjectDeletesCredentialSecretWhenProjectCreateFails(t *testing.T) {
	scheme := testProjectScheme(t)
	ns := testUserNS()
	failingClient := &createProjectFailingClient{
		Client: fake.NewClientBuilder().WithScheme(scheme).Build(),
		failOnCreate: map[schema.GroupVersionKind]error{
			triggersv1alpha1.GroupVersion.WithKind("Project"): apierrors.NewAlreadyExists(schema.GroupResource{Group: triggersv1alpha1.GroupVersion.Group, Resource: "projects"}, "payments"),
		},
	}
	srv := &Server{k8sClient: failingClient, scheme: scheme}

	_, err := srv.CreateProject(projectActorCtx(), &platform.CreateProjectRequest{
		Name:         "payments",
		DisplayName:  "Payments",
		RepoUrl:      "https://github.com/example/payments.git",
		BaseBranch:   "main",
		Provider:     "openai",
		AuthMode:     "api-key",
		GithubToken:  "gh-token",
		OpenaiApiKey: "sk-test",
	})
	if err == nil {
		t.Fatalf("CreateProject() error = nil, want already exists")
	}
	var connectErr *connect.Error
	if !errors.As(err, &connectErr) || connectErr.Code() != connect.CodeAlreadyExists {
		t.Fatalf("CreateProject() error = %v, want already exists", err)
	}

	secret := &corev1.Secret{}
	secretErr := failingClient.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: "payments-credentials"}, secret)
	if !apierrors.IsNotFound(secretErr) {
		t.Fatalf("Get(Secret) error = %v, want not found", secretErr)
	}
}

func TestProjectReadsAreEnrichedWithCredentialPresence(t *testing.T) {
	scheme := testProjectScheme(t)
	project := &triggersv1alpha1.Project{
		ObjectMeta: metav1.ObjectMeta{Name: "payments", Namespace: "default", ResourceVersion: "1"},
		Spec: triggersv1alpha1.ProjectSpec{
			DisplayName: "Payments",
			Defaults: triggersv1alpha1.AgentRunDefaults{
				RepoURL:    "https://github.com/example/payments.git",
				BaseBranch: "main",
				Provider:   "openai",
				AuthMode:   platformv1alpha1.AgentRunAuthModeAPIKey,
				Secrets: triggersv1alpha1.AgentRunSecrets{
					GithubToken:  "payments-credentials",
					ClaudeApiKey: "payments-credentials",
					ProviderKeys: []platformv1alpha1.ProviderKeyRef{{Provider: "openai", SecretName: "payments-credentials", SecretKey: openAIAPIKeySecretKey}},
				},
			},
		},
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "payments-credentials", Namespace: "default"},
		StringData: map[string]string{
			githubTokenSecretKey:     "gh-token",
			openAIAPIKeySecretKey:    "sk-test",
			anthropicAPIKeySecretKey: "",
		},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(project, secret).Build()
	srv := &Server{k8sClient: c, scheme: scheme}

	got, err := srv.GetProject(context.Background(), &platform.GetProjectRequest{Namespace: "default", Name: "payments"})
	if err != nil {
		t.Fatalf("GetProject() error = %v", err)
	}
	if got.GithubTokenSecret != "payments-credentials" || got.ClaudeApiKeySecret != "payments-credentials" || got.OpenaiOauthSecret != "" {
		t.Fatalf("GetProject() leaked unexpected secret refs = %#v", got)
	}
	if got.CredentialStatus == nil || !got.CredentialStatus.GithubTokenPresent || !got.CredentialStatus.OpenaiApiKeyPresent || got.CredentialStatus.AnthropicApiKeyPresent {
		t.Fatalf("GetProject credential status = %#v", got.CredentialStatus)
	}

	list, err := srv.ListProjects(context.Background(), &platform.ListProjectsRequest{Namespace: "default"})
	if err != nil {
		t.Fatalf("ListProjects() error = %v", err)
	}
	if len(list.Projects) != 1 || list.Projects[0].CredentialStatus == nil || !list.Projects[0].CredentialStatus.GithubTokenPresent || !list.Projects[0].CredentialStatus.OpenaiApiKeyPresent || list.Projects[0].CredentialStatus.AnthropicApiKeyPresent {
		t.Fatalf("ListProjects = %#v", list.Projects)
	}
}

type createProjectFailingClient struct {
	client.Client
	failOnCreate map[schema.GroupVersionKind]error
}

func (c *createProjectFailingClient) Create(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
	if err, ok := c.failOnCreate[obj.GetObjectKind().GroupVersionKind()]; ok {
		return err
	}
	return c.Client.Create(ctx, obj, opts...)
}
