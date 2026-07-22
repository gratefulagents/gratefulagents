package dashboard

import (
	"context"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
)

func jsonModelResponse(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

// fullParityDefaults populates every AgentRunDefaults field the RPC surface
// exposes, so the round-trip test proves each one maps to a CRD field.
func fullParityDefaults() *platform.AgentRunDefaults {
	return &platform.AgentRunDefaults{
		RepoUrl:            "https://github.com/example/payments.git",
		AdditionalRepoUrls: []string{"https://github.com/example/lib.git"},
		BaseBranch:         "main",
		Image:              "ghcr.io/example/worker:latest",
		Model:              "claude-sonnet-4-6",
		AllowedModels:      []string{"claude-sonnet-4-6", "claude-haiku-4-5"},
		Provider:           "anthropic",
		AuthMode:           "api-key",
		ReasoningLevel:     "high",
		OpenaiBaseUrl:      "https://llm.example.com/v1",
		OpenaiApi:          "responses",
		Timeout:            "45m0s",
		CustomInstructions: "do the thing",
		ClaudeApiKeySecret: "anthropic-cred",
		OpenaiOauthSecret:  "oauth-cred",
		GithubTokenSecret:  "gh-cred",
		ProviderKeys: []*platform.ProviderKeyRef{
			{Provider: "anthropic", SecretName: "anthropic-cred", SecretKey: "api-key"},
		},
		RuntimeProfileRef: "custom-runtime",
		McpPolicyRef:      "custom-policy",
		McpServerRefs:     []string{"mcp-a", "mcp-b"},
		SkillRefs:         []string{"skill-a"},
		WorkflowMode:      "auto",
		ModeRef:           "deep-research",
		ExecutionMode:     "linear",
	}
}

func TestAgentRunDefaultsProtoCRDRoundTrip(t *testing.T) {
	in := fullParityDefaults()

	crd, provider, authMode, err := protoDefaultsToCRD(in)
	if err != nil {
		t.Fatalf("protoDefaultsToCRD() error = %v", err)
	}
	if provider != triggersv1alpha1.ProviderAnthropic || authMode != platformv1alpha1.AgentRunAuthModeAPIKey {
		t.Fatalf("provider/authMode = %q/%q", provider, authMode)
	}

	out := crdDefaultsToProto(crd)
	if !proto.Equal(in, out) {
		t.Fatalf("round trip mismatch:\n in = %+v\nout = %+v", in, out)
	}
}

func parityPolicies() *platform.TriggerPolicies {
	return &platform.TriggerPolicies{
		ConfigureRuntimeProfile: true,
		PermissionMode:          "read-only",
		EgressMode:              "restricted",
		ConfigureMcpPolicy:      true,
		McpPolicyDefaultAction:  "Allow",
		McpPolicyAllowedServers: []string{"github", "linear"},
	}
}

// assertPolicyObjects verifies the provisioned RuntimeProfile and MCPPolicy
// hold the request's policy options (the CRD mapping guarantee).
func assertPolicyObjects(t *testing.T, c client.Client, ns, runtimeName, policyName string) {
	t.Helper()
	profile := &platformv1alpha1.RuntimeProfile{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: runtimeName}, profile); err != nil {
		t.Fatalf("Get(RuntimeProfile %s) error = %v", runtimeName, err)
	}
	if profile.Spec.Security == nil ||
		profile.Spec.Security.PermissionMode != platformv1alpha1.PermissionModeReadOnly ||
		profile.Spec.Security.EgressMode != platformv1alpha1.EgressMode("restricted") {
		t.Fatalf("RuntimeProfile security = %+v", profile.Spec.Security)
	}
	policy := &platformv1alpha1.MCPPolicy{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: policyName}, policy); err != nil {
		t.Fatalf("Get(MCPPolicy %s) error = %v", policyName, err)
	}
	if policy.Spec.DefaultAction != platformv1alpha1.MCPDefaultActionAllow {
		t.Fatalf("MCPPolicy default action = %q", policy.Spec.DefaultAction)
	}
	if len(policy.Spec.AllowedServers) != 2 || policy.Spec.AllowedServers[0].Name != "github" || policy.Spec.AllowedServers[1].Name != "linear" {
		t.Fatalf("MCPPolicy allowed servers = %+v", policy.Spec.AllowedServers)
	}
}

func TestCreateCronPoliciesProvisionObjectsAndSetRefs(t *testing.T) {
	srv, c := newCronTestServer(t)
	ns := testUserNS()

	resp, err := srv.CreateCron(projectActorCtx(), &platform.CreateCronRequest{
		Name:     "nightly",
		Schedule: "0 6 * * *",
		Prompt:   "report",
		Defaults: &platform.AgentRunDefaults{Provider: "anthropic", ClaudeApiKeySecret: "k"},
		Policies: parityPolicies(),
	})
	if err != nil {
		t.Fatalf("CreateCron() error = %v", err)
	}

	assertPolicyObjects(t, c, ns, "nightly-runtime", "nightly-mcp-policy")

	cr := &triggersv1alpha1.Cron{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: "nightly"}, cr); err != nil {
		t.Fatalf("Get(Cron) error = %v", err)
	}
	if cr.Spec.Defaults.RuntimeProfileRef == nil || cr.Spec.Defaults.RuntimeProfileRef.Name != "nightly-runtime" {
		t.Fatalf("RuntimeProfileRef = %+v", cr.Spec.Defaults.RuntimeProfileRef)
	}
	if cr.Spec.Defaults.MCPPolicyRef == nil || cr.Spec.Defaults.MCPPolicyRef.Name != "nightly-mcp-policy" {
		t.Fatalf("MCPPolicyRef = %+v", cr.Spec.Defaults.MCPPolicyRef)
	}

	if resp.PermissionMode != "read-only" || resp.EgressMode != "restricted" ||
		resp.McpPolicyDefaultAction != "Allow" || !reflect.DeepEqual(resp.McpPolicyAllowedServers, []string{"github", "linear"}) {
		t.Fatalf("resolved policy fields = %q/%q/%q/%v", resp.PermissionMode, resp.EgressMode, resp.McpPolicyDefaultAction, resp.McpPolicyAllowedServers)
	}
}

func TestCreateCronNameCollisionRestoresPolicyObjects(t *testing.T) {
	ns := testUserNS()
	existingCron := &triggersv1alpha1.Cron{
		ObjectMeta: metav1.ObjectMeta{Name: "nightly", Namespace: ns},
		Spec:       triggersv1alpha1.CronSpec{Schedule: "0 6 * * *", Prompt: "old"},
	}
	profile := &platformv1alpha1.RuntimeProfile{
		ObjectMeta: metav1.ObjectMeta{Name: "nightly-runtime", Namespace: ns},
		Spec: platformv1alpha1.RuntimeProfileSpec{Security: &platformv1alpha1.RuntimeProfileSecurity{
			PermissionMode: platformv1alpha1.PermissionModeReadOnly,
			EgressMode:     platformv1alpha1.EgressMode("restricted"),
		}},
	}
	srv, c := newCronTestServer(t, existingCron, profile)

	// Policies are applied before the Cron CR write; when that write fails
	// (name collision) the rollback must restore the pre-existing profile's
	// spec instead of leaving it silently switched to the new modes.
	_, err := srv.CreateCron(projectActorCtx(), &platform.CreateCronRequest{
		Name:     "nightly",
		Schedule: "0 6 * * *",
		Prompt:   "new",
		Defaults: &platform.AgentRunDefaults{Provider: "anthropic", ClaudeApiKeySecret: "k"},
		Policies: &platform.TriggerPolicies{
			ConfigureRuntimeProfile: true,
			PermissionMode:          "danger-full-access",
			EgressMode:              "unrestricted",
		},
	})
	if err == nil {
		t.Fatalf("CreateCron() expected name-collision error, got nil")
	}

	got := &platformv1alpha1.RuntimeProfile{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: "nightly-runtime"}, got); err != nil {
		t.Fatalf("Get(RuntimeProfile) error = %v", err)
	}
	if got.Spec.Security == nil ||
		got.Spec.Security.PermissionMode != platformv1alpha1.PermissionModeReadOnly ||
		string(got.Spec.Security.EgressMode) != "restricted" {
		t.Fatalf("RuntimeProfile security = %+v, want restored read-only/restricted", got.Spec.Security)
	}
}

func TestUpdateCronPoliciesProvisionObjectsAndSetRefs(t *testing.T) {
	ns := testUserNS()
	existing := &triggersv1alpha1.Cron{
		ObjectMeta: metav1.ObjectMeta{Name: "nightly", Namespace: ns},
		Spec:       triggersv1alpha1.CronSpec{Schedule: "0 6 * * *", Prompt: "old"},
	}
	srv, c := newCronTestServer(t, existing)

	resp, err := srv.UpdateCron(projectActorCtx(), &platform.UpdateCronRequest{
		Namespace: ns,
		Name:      "nightly",
		Schedule:  "@daily",
		Prompt:    "new",
		Defaults:  &platform.AgentRunDefaults{Provider: "anthropic", ClaudeApiKeySecret: "k"},
		Policies:  parityPolicies(),
	})
	if err != nil {
		t.Fatalf("UpdateCron() error = %v", err)
	}
	assertPolicyObjects(t, c, ns, "nightly-runtime", "nightly-mcp-policy")
	if resp.Defaults.GetRuntimeProfileRef() != "nightly-runtime" || resp.Defaults.GetMcpPolicyRef() != "nightly-mcp-policy" {
		t.Fatalf("defaults refs = %q/%q", resp.Defaults.GetRuntimeProfileRef(), resp.Defaults.GetMcpPolicyRef())
	}
}

func TestCreateGitHubRepositoryFromTokenWithDefaultsMessage(t *testing.T) {
	api := fakeGitHubRepoAPI(t, http.StatusOK, "develop", nil)
	defer api.Close()

	scheme := testProjectScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	srv := NewServer(c, scheme, nil, nil, false, WithGitHubAppAPIBaseURL(api.URL+"/"))

	ctx := triggerActorCtx("user-1", "member")
	namespace, err := srv.ensureUserNamespace(ctx, requestActorFromContext(ctx))
	if err != nil {
		t.Fatalf("ensureUserNamespace() error = %v", err)
	}

	defaults := fullParityDefaults()
	defaults.RepoUrl = "https://github.com/evil/other.git" // must be ignored
	defaults.AdditionalRepoUrls = nil
	defaults.BaseBranch = "release"
	defaults.RuntimeProfileRef = ""
	defaults.McpPolicyRef = ""

	resp, err := srv.CreateGitHubRepositoryFromToken(ctx, &platform.CreateGitHubRepositoryFromTokenRequest{
		Owner:       "acme",
		Repo:        "payments",
		GithubToken: "inline-tok",
		Defaults:    defaults,
		Policies:    parityPolicies(),
	})
	if err != nil {
		t.Fatalf("CreateGitHubRepositoryFromToken() error = %v", err)
	}
	if resp.RepoUrl != "https://github.com/acme/payments.git" {
		t.Fatalf("RepoUrl = %q, want derived URL", resp.RepoUrl)
	}

	gh := &triggersv1alpha1.GitHubRepository{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: namespace, Name: "acme-payments"}, gh); err != nil {
		t.Fatalf("Get(GitHubRepository) error = %v", err)
	}
	d := gh.Spec.Defaults
	if d.RepoURL != "https://github.com/acme/payments.git" {
		t.Fatalf("RepoURL = %q, want derived URL", d.RepoURL)
	}
	if d.BaseBranch != "release" {
		t.Fatalf("BaseBranch = %q, want release (from defaults message)", d.BaseBranch)
	}
	if d.Model != "claude-sonnet-4-6" || d.Image != "ghcr.io/example/worker:latest" ||
		d.Provider != triggersv1alpha1.ProviderAnthropic || d.AuthMode != platformv1alpha1.AgentRunAuthModeAPIKey ||
		d.ReasoningLevel != platformv1alpha1.ReasoningHigh || d.OpenAIBaseURL != "https://llm.example.com/v1" ||
		d.OpenAIAPI != "responses" || d.CustomInstructions != "do the thing" ||
		string(d.WorkflowMode) != "auto" || string(d.ExecutionMode) != "linear" {
		t.Fatalf("defaults = %+v", d)
	}
	if len(d.MCPServerRefs) != 2 || len(d.SkillRefs) != 1 || d.ModeRef == nil || d.ModeRef.Name != "deep-research" {
		t.Fatalf("refs = %+v / %+v / %+v", d.MCPServerRefs, d.SkillRefs, d.ModeRef)
	}
	if d.Secrets.GithubToken != "acme-payments-github-token" {
		t.Fatalf("Secrets.GithubToken = %q, want per-trigger token secret", d.Secrets.GithubToken)
	}
	assertPolicyObjects(t, c, namespace, "acme-payments-runtime", "acme-payments-mcp-policy")
	if d.RuntimeProfileRef == nil || d.RuntimeProfileRef.Name != "acme-payments-runtime" ||
		d.MCPPolicyRef == nil || d.MCPPolicyRef.Name != "acme-payments-mcp-policy" {
		t.Fatalf("policy refs = %+v / %+v", d.RuntimeProfileRef, d.MCPPolicyRef)
	}
}

func TestUpdateGitHubRepositoryReplacesDefaultsPreservingRepoURLAndToken(t *testing.T) {
	ns := testUserNS()
	existing := &triggersv1alpha1.GitHubRepository{
		ObjectMeta: metav1.ObjectMeta{Name: "acme-payments", Namespace: ns},
		Spec: triggersv1alpha1.GitHubRepositorySpec{
			Owner:             "acme",
			Repo:              "payments",
			TriggerKeyword:    "@agent",
			GitHubTokenSecret: "trigger-token",
			Defaults: triggersv1alpha1.AgentRunDefaults{
				RepoURL:    "https://github.com/acme/payments.git",
				BaseBranch: "main",
				Provider:   triggersv1alpha1.ProviderOpenAI,
				Secrets:    triggersv1alpha1.AgentRunSecrets{GithubToken: "trigger-token"},
			},
		},
	}
	srv, c := newCronTestServer(t, existing)

	defaults := fullParityDefaults()
	defaults.RepoUrl = "https://github.com/evil/other.git" // must be ignored
	defaults.AdditionalRepoUrls = nil
	defaults.GithubTokenSecret = "" // empty keeps the trigger's token wiring
	defaults.RuntimeProfileRef = ""
	defaults.McpPolicyRef = ""

	resp, err := srv.UpdateGitHubRepository(projectActorCtx(), &platform.UpdateGitHubRepositoryRequest{
		Namespace: ns,
		Name:      "acme-payments",
		Defaults:  defaults,
		Policies:  parityPolicies(),
	})
	if err != nil {
		t.Fatalf("UpdateGitHubRepository() error = %v", err)
	}

	gh := &triggersv1alpha1.GitHubRepository{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: "acme-payments"}, gh); err != nil {
		t.Fatalf("Get(GitHubRepository) error = %v", err)
	}
	if gh.Spec.GitHubTokenSecret != "trigger-token" {
		t.Fatalf("spec GitHubTokenSecret = %q, want untouched", gh.Spec.GitHubTokenSecret)
	}
	d := gh.Spec.Defaults
	if d.RepoURL != "https://github.com/acme/payments.git" {
		t.Fatalf("RepoURL = %q, want preserved derived URL", d.RepoURL)
	}
	if d.Secrets.GithubToken != "trigger-token" {
		t.Fatalf("Secrets.GithubToken = %q, want preserved", d.Secrets.GithubToken)
	}
	if d.Model != "claude-sonnet-4-6" || d.Provider != triggersv1alpha1.ProviderAnthropic || d.BaseBranch != "main" {
		t.Fatalf("defaults = %+v", d)
	}
	assertPolicyObjects(t, c, ns, "acme-payments-runtime", "acme-payments-mcp-policy")
	if resp.Defaults.GetModel() != "claude-sonnet-4-6" || resp.PermissionMode != "read-only" {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestUpdateGitHubRepositorySavedCredentialsKeepTokenTriggerWiring(t *testing.T) {
	ns := testUserNS()
	existing := &triggersv1alpha1.GitHubRepository{
		ObjectMeta: metav1.ObjectMeta{Name: "acme-payments", Namespace: ns},
		Spec: triggersv1alpha1.GitHubRepositorySpec{
			Owner:             "acme",
			Repo:              "payments",
			GitHubTokenSecret: "trigger-token",
			Defaults: triggersv1alpha1.AgentRunDefaults{
				RepoURL:  "https://github.com/acme/payments.git",
				Provider: triggersv1alpha1.ProviderAnthropic,
				Secrets:  triggersv1alpha1.AgentRunSecrets{GithubToken: "trigger-token"},
			},
		},
	}
	savedAnthropic := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: userCredentialSecretName("anthropic"), Namespace: ns},
		Data:       map[string][]byte{userCredAPIKeyKey: []byte("saved-anthropic")},
	}
	savedGitHub := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: userCredentialSecretName(credentialGitHub), Namespace: ns},
		Data:       map[string][]byte{userCredGithubTokenKey: []byte("gh-personal")},
	}
	srv, c := newCronTestServer(t, existing, savedAnthropic, savedGitHub)

	// A token-authenticated trigger keeps its dedicated GitHub token even when
	// the caller rewires provider credentials to saved ones and has a saved
	// GitHub token of their own: swapping it would silently change the git
	// identity of runs (PR review P1).
	_, err := srv.UpdateGitHubRepository(projectActorCtx(), &platform.UpdateGitHubRepositoryRequest{
		Namespace:           ns,
		Name:                "acme-payments",
		Defaults:            &platform.AgentRunDefaults{Provider: "anthropic", AuthMode: "api-key", Model: "claude-sonnet-4-6"},
		UseSavedCredentials: true,
	})
	if err != nil {
		t.Fatalf("UpdateGitHubRepository() error = %v", err)
	}
	gh := &triggersv1alpha1.GitHubRepository{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: "acme-payments"}, gh); err != nil {
		t.Fatalf("Get(GitHubRepository) error = %v", err)
	}
	if gh.Spec.Defaults.Secrets.GithubToken != "trigger-token" {
		t.Fatalf("Secrets.GithubToken = %q, want dedicated trigger token preserved", gh.Spec.Defaults.Secrets.GithubToken)
	}
	if len(gh.Spec.Defaults.Secrets.ProviderKeys) == 0 {
		t.Fatalf("expected saved provider credentials to be wired, got %+v", gh.Spec.Defaults.Secrets)
	}
}

func TestUpdateGitHubRepositoryReplacesTriggerSettings(t *testing.T) {
	ns := testUserNS()
	existing := &triggersv1alpha1.GitHubRepository{
		ObjectMeta: metav1.ObjectMeta{Name: "acme-payments", Namespace: ns},
		Spec: triggersv1alpha1.GitHubRepositorySpec{
			Owner:             "acme",
			Repo:              "payments",
			GitHubTokenSecret: "trigger-token",
			Defaults: triggersv1alpha1.AgentRunDefaults{
				RepoURL:  "https://github.com/acme/payments.git",
				Provider: triggersv1alpha1.ProviderAnthropic,
				Secrets: triggersv1alpha1.AgentRunSecrets{
					GithubToken:  "trigger-token",
					ProviderKeys: []platformv1alpha1.ProviderKeyRef{{Provider: "anthropic", SecretName: "anthropic-key", SecretKey: "api-key"}},
				},
			},
		},
	}
	srv, c := newCronTestServer(t, existing)
	useReviewerDefaults := true

	resp, err := srv.UpdateGitHubRepository(projectActorCtx(), &platform.UpdateGitHubRepositoryRequest{
		Namespace:           ns,
		Name:                "acme-payments",
		Defaults:            &platform.AgentRunDefaults{Provider: "anthropic", AuthMode: "api-key", Model: "claude-sonnet-4-6", GithubTokenSecret: "attempted-override", ProviderKeys: []*platform.ProviderKeyRef{{Provider: "anthropic", SecretName: "anthropic-key", SecretKey: "api-key"}}},
		UseReviewerDefaults: &useReviewerDefaults,
		ReviewerDefaults:    fullParityDefaults(),
		TriggerSettings: &platform.GitHubRepositoryTriggerSettings{
			PollInterval:                      "2m",
			ReviewLoopDisabled:                func(v bool) *bool { return &v }(false),
			WebhookSecret:                     "github-webhook",
			TriggerKeyword:                    "@agents",
			CancelRunsOnIssueClose:            true,
			AuthAllowedUsers:                  []string{"alice", "", "bob"},
			AuthDenyUsers:                     []string{"mallory"},
			ReviewLoopMaxRounds:               5,
			ReviewerModeRef:                   "security-review",
			ReviewerModeVersion:               "v2",
			ReviewerModeChannel:               "stable",
			MaintainerEnabled:                 func(v bool) *bool { return &v }(true),
			MaintainerMaxConcurrentDispatches: 4,
			MaintainerMaxDispatchesPerDay:     12,
			MaintainerStandupInterval:         "6h",
			MaintainerModeRef:                 "repository-maintainer",
			MaintainerModel:                   "claude-opus-4-6",
			MaintainerAllowPrMerge:            true,
			MaintainerWorkItemCutover:         new("Legacy"),
		},
	})
	if err != nil {
		t.Fatalf("UpdateGitHubRepository() error = %v", err)
	}
	gh := &triggersv1alpha1.GitHubRepository{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: "acme-payments"}, gh); err != nil {
		t.Fatalf("Get(GitHubRepository) error = %v", err)
	}
	if got := gh.Spec.PollInterval.Duration.String(); got != "2m0s" {
		t.Fatalf("PollInterval = %q, want 2m0s", got)
	}
	if gh.Spec.WebhookSecret != "github-webhook" || gh.Spec.TriggerKeyword != "@agents" || !gh.Spec.CancelRunsOnIssueClose {
		t.Fatalf("trigger settings = %+v", gh.Spec)
	}
	if gh.Spec.Defaults.Secrets.GithubToken != "trigger-token" {
		t.Fatalf("defaults GitHub token = %q, want onboarding-owned trigger-token", gh.Spec.Defaults.Secrets.GithubToken)
	}
	if gh.Spec.Auth == nil || len(gh.Spec.Auth.AllowedUsers) != 2 || gh.Spec.Auth.AllowedUsers[1] != "bob" || len(gh.Spec.Auth.DenyUsers) != 1 {
		t.Fatalf("Auth = %+v", gh.Spec.Auth)
	}
	if gh.Spec.ReviewLoop == nil || gh.Spec.ReviewLoop.MaxRounds != 5 || gh.Spec.ReviewLoop.ReviewerModeRef == nil || gh.Spec.ReviewLoop.ReviewerModeRef.Name != "security-review" || gh.Spec.ReviewLoop.ReviewerModeRef.Version != "v2" || gh.Spec.ReviewLoop.ReviewerModeRef.Channel != "stable" {
		t.Fatalf("ReviewLoop = %+v", gh.Spec.ReviewLoop)
	}
	if gh.Spec.ReviewLoop.ReviewerDefaults == nil {
		t.Fatal("ReviewLoop.ReviewerDefaults = nil")
	}
	if got := gh.Spec.ReviewLoop.ReviewerDefaults; got.Model != "claude-sonnet-4-6" || got.Image != "ghcr.io/example/worker:latest" || got.CustomInstructions != "do the thing" {
		t.Fatalf("ReviewerDefaults = %+v", got)
	}
	if got := gh.Spec.ReviewLoop.ReviewerDefaults; got.WorkflowMode != "" || got.ExecutionMode != "" || got.ModeRef != nil || got.Team != nil {
		t.Fatalf("hidden reviewer orchestration fields were persisted: %+v", got)
	}
	if gh.Spec.ReviewLoop.ReviewerDefaults.RepoURL != existing.Spec.Defaults.RepoURL || gh.Spec.ReviewLoop.ReviewerDefaults.Secrets.GithubToken != "trigger-token" {
		t.Fatalf("reviewer repo/GitHub auth was not fixed to repository: %+v", gh.Spec.ReviewLoop.ReviewerDefaults)
	}
	if gh.Spec.Maintainer == nil || gh.Spec.Maintainer.Disabled ||
		gh.Spec.Maintainer.MaxConcurrentDispatches != 4 || gh.Spec.Maintainer.MaxDispatchesPerDay != 12 ||
		gh.Spec.Maintainer.StandupInterval == nil || gh.Spec.Maintainer.StandupInterval.Duration.String() != "6h0m0s" ||
		gh.Spec.Maintainer.ModeRef == nil || gh.Spec.Maintainer.ModeRef.Name != "repository-maintainer" ||
		gh.Spec.Maintainer.Model != "claude-opus-4-6" || !gh.Spec.Maintainer.AllowPullRequestMerge ||
		gh.Spec.Maintainer.WorkItemCutover != triggersv1alpha1.MaintainerWorkItemCutoverLegacy {
		t.Fatalf("Maintainer = %+v", gh.Spec.Maintainer)
	}
	if resp.ReviewerDefaults == nil || resp.ReviewerDefaults.Model != "claude-sonnet-4-6" {
		t.Fatalf("response ReviewerDefaults = %+v", resp.ReviewerDefaults)
	}
	if resp.TriggerSettings == nil || !resp.TriggerSettings.GetMaintainerEnabled() ||
		resp.TriggerSettings.MaintainerMaxConcurrentDispatches != 4 || resp.TriggerSettings.MaintainerMaxDispatchesPerDay != 12 ||
		resp.TriggerSettings.MaintainerStandupInterval != "6h0m0s" || resp.TriggerSettings.MaintainerModeRef != "repository-maintainer" ||
		resp.TriggerSettings.MaintainerModel != "claude-opus-4-6" || !resp.TriggerSettings.MaintainerAllowPrMerge ||
		resp.TriggerSettings.GetMaintainerWorkItemCutover() != string(triggersv1alpha1.MaintainerWorkItemCutoverLegacy) {
		t.Fatalf("response maintainer settings = %+v", resp.TriggerSettings)
	}
}

func TestUpdateGitHubRepositoryPreservesMaintainerCutoverWhenClientOmitsField(t *testing.T) {
	ns := testUserNS()
	existing := &triggersv1alpha1.GitHubRepository{
		ObjectMeta: metav1.ObjectMeta{Name: "acme-payments", Namespace: ns},
		Spec: triggersv1alpha1.GitHubRepositorySpec{
			Owner: "acme", Repo: "payments", GitHubTokenSecret: "trigger-token",
			Defaults:   triggersv1alpha1.AgentRunDefaults{RepoURL: "https://github.com/acme/payments.git", Provider: triggersv1alpha1.ProviderAnthropic, Secrets: triggersv1alpha1.AgentRunSecrets{GithubToken: "trigger-token", ProviderKeys: []platformv1alpha1.ProviderKeyRef{{Provider: "anthropic", SecretName: "anthropic-key", SecretKey: "api-key"}}}},
			Maintainer: &triggersv1alpha1.MaintainerSpec{WorkItemCutover: triggersv1alpha1.MaintainerWorkItemCutoverDualRead},
		},
	}
	srv, c := newCronTestServer(t, existing)
	enabled := true
	_, err := srv.UpdateGitHubRepository(projectActorCtx(), &platform.UpdateGitHubRepositoryRequest{
		Namespace:       ns,
		Name:            existing.Name,
		Defaults:        &platform.AgentRunDefaults{Provider: "anthropic", AuthMode: "api-key", Model: "claude-sonnet-4-6", ProviderKeys: []*platform.ProviderKeyRef{{Provider: "anthropic", SecretName: "anthropic-key", SecretKey: "api-key"}}},
		TriggerSettings: &platform.GitHubRepositoryTriggerSettings{MaintainerEnabled: &enabled, MaintainerModel: "claude-opus-4-6"},
	})
	if err != nil {
		t.Fatalf("UpdateGitHubRepository() error = %v", err)
	}
	updated := &triggersv1alpha1.GitHubRepository{}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(existing), updated); err != nil {
		t.Fatal(err)
	}
	if updated.Spec.Maintainer == nil || updated.Spec.Maintainer.WorkItemCutover != triggersv1alpha1.MaintainerWorkItemCutoverDualRead {
		t.Fatalf("maintainer cutover = %+v, want preserved DualRead", updated.Spec.Maintainer)
	}
}

func TestUpdateGitHubRepositoryPreservesTriggerSettingsWhenOmitted(t *testing.T) {
	ns := testUserNS()
	existing := &triggersv1alpha1.GitHubRepository{
		ObjectMeta: metav1.ObjectMeta{Name: "acme-payments", Namespace: ns},
		Spec: triggersv1alpha1.GitHubRepositorySpec{
			Owner:                  "acme",
			Repo:                   "payments",
			GitHubTokenSecret:      "trigger-token",
			TriggerKeyword:         "@agents",
			WebhookSecret:          "github-webhook",
			CancelRunsOnIssueClose: true,
			Auth:                   &triggersv1alpha1.TriggerAuth{AllowedUsers: []string{"alice"}},
			ReviewLoop: &triggersv1alpha1.ReviewLoopSpec{
				Disabled:         true,
				ReviewerDefaults: &triggersv1alpha1.AgentRunDefaults{Model: "dedicated-reviewer"},
			},
			Maintainer: &triggersv1alpha1.MaintainerSpec{
				Model: "dedicated-maintainer",
			},
			Defaults: triggersv1alpha1.AgentRunDefaults{
				RepoURL:  "https://github.com/acme/payments.git",
				Provider: triggersv1alpha1.ProviderAnthropic,
				Secrets: triggersv1alpha1.AgentRunSecrets{
					GithubToken:  "trigger-token",
					ProviderKeys: []platformv1alpha1.ProviderKeyRef{{Provider: "anthropic", SecretName: "anthropic-key", SecretKey: "api-key"}},
				},
			},
		},
	}
	srv, c := newCronTestServer(t, existing)

	_, err := srv.UpdateGitHubRepository(projectActorCtx(), &platform.UpdateGitHubRepositoryRequest{
		Namespace: ns,
		Name:      "acme-payments",
		Defaults:  &platform.AgentRunDefaults{Provider: "anthropic", AuthMode: "api-key", Model: "claude-sonnet-4-6", ProviderKeys: []*platform.ProviderKeyRef{{Provider: "anthropic", SecretName: "anthropic-key", SecretKey: "api-key"}}},
	})
	if err != nil {
		t.Fatalf("UpdateGitHubRepository() error = %v", err)
	}
	gh := &triggersv1alpha1.GitHubRepository{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: "acme-payments"}, gh); err != nil {
		t.Fatalf("Get(GitHubRepository) error = %v", err)
	}
	if gh.Spec.TriggerKeyword != "@agents" || gh.Spec.WebhookSecret != "github-webhook" || !gh.Spec.CancelRunsOnIssueClose || gh.Spec.Auth == nil || gh.Spec.ReviewLoop == nil || !gh.Spec.ReviewLoop.Disabled {
		t.Fatalf("trigger settings were not preserved: %+v", gh.Spec)
	}
	if gh.Spec.ReviewLoop.ReviewerDefaults == nil || gh.Spec.ReviewLoop.ReviewerDefaults.Model != "dedicated-reviewer" {
		t.Fatalf("reviewer defaults were not preserved: %+v", gh.Spec.ReviewLoop)
	}
	if gh.Spec.Maintainer == nil || gh.Spec.Maintainer.Model != "dedicated-maintainer" {
		t.Fatalf("maintainer settings were not preserved: %+v", gh.Spec.Maintainer)
	}
}

func TestUpdateGitHubRepositoryClearsReviewerDefaults(t *testing.T) {
	ns := testUserNS()
	existing := &triggersv1alpha1.GitHubRepository{
		ObjectMeta: metav1.ObjectMeta{Name: "acme-payments", Namespace: ns},
		Spec: triggersv1alpha1.GitHubRepositorySpec{
			Owner: "acme", Repo: "payments",
			Defaults: triggersv1alpha1.AgentRunDefaults{
				RepoURL: "https://github.com/acme/payments.git", Provider: triggersv1alpha1.ProviderAnthropic,
				Secrets: triggersv1alpha1.AgentRunSecrets{ProviderKeys: []platformv1alpha1.ProviderKeyRef{{Provider: "anthropic", SecretName: "anthropic-key", SecretKey: "api-key"}}},
			},
			ReviewLoop: &triggersv1alpha1.ReviewLoopSpec{
				MaxRounds:        5,
				ReviewerDefaults: &triggersv1alpha1.AgentRunDefaults{Model: "dedicated-reviewer"},
			},
		},
	}
	srv, c := newCronTestServer(t, existing)
	useReviewerDefaults := false
	_, err := srv.UpdateGitHubRepository(projectActorCtx(), &platform.UpdateGitHubRepositoryRequest{
		Namespace:           ns,
		Name:                "acme-payments",
		Defaults:            &platform.AgentRunDefaults{Provider: "anthropic", AuthMode: "api-key", Model: "claude-sonnet-4-6", ProviderKeys: []*platform.ProviderKeyRef{{Provider: "anthropic", SecretName: "anthropic-key", SecretKey: "api-key"}}},
		TriggerSettings:     &platform.GitHubRepositoryTriggerSettings{ReviewLoopMaxRounds: 5},
		UseReviewerDefaults: &useReviewerDefaults,
	})
	if err != nil {
		t.Fatalf("UpdateGitHubRepository() error = %v", err)
	}
	gh := &triggersv1alpha1.GitHubRepository{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: "acme-payments"}, gh); err != nil {
		t.Fatalf("Get(GitHubRepository) error = %v", err)
	}
	if gh.Spec.ReviewLoop == nil || !gh.Spec.ReviewLoop.Disabled || gh.Spec.ReviewLoop.MaxRounds != 5 || gh.Spec.ReviewLoop.ReviewerDefaults != nil {
		t.Fatalf("ReviewLoop after clear = %+v, want disabled with 5 dormant rounds", gh.Spec.ReviewLoop)
	}
}

func TestGitHubReviewSettingsRemainDisabledWithoutExplicitOptIn(t *testing.T) {
	for _, tc := range []struct {
		name     string
		settings *platform.GitHubRepositoryTriggerSettings
	}{
		{name: "max rounds", settings: &platform.GitHubRepositoryTriggerSettings{ReviewLoopMaxRounds: 5}},
		{name: "reviewer mode", settings: &platform.GitHubRepositoryTriggerSettings{ReviewerModeRef: "security-review"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, _, _, _, reviewLoop, _, err := protoGitHubTriggerSettingsToCRD(tc.settings)
			if err != nil {
				t.Fatalf("protoGitHubTriggerSettingsToCRD() error = %v", err)
			}
			if reviewLoop == nil || !reviewLoop.Disabled {
				t.Fatalf("ReviewLoop = %+v, want disabled without explicit opt-in", reviewLoop)
			}
		})
	}
}

func TestGitHubMaintainerSettingsRequireExplicitOptIn(t *testing.T) {
	_, _, _, _, _, _, maintainer, err := protoGitHubTriggerSettingsToCRD(&platform.GitHubRepositoryTriggerSettings{
		MaintainerMaxConcurrentDispatches: 4,
		MaintainerMaxDispatchesPerDay:     12,
		MaintainerStandupInterval:         "6h",
		MaintainerModeRef:                 "repository-maintainer",
		MaintainerModel:                   "claude-opus-4-6",
		MaintainerAllowPrMerge:            true,
	})
	if err != nil {
		t.Fatalf("protoGitHubTriggerSettingsToCRD() error = %v", err)
	}
	if maintainer != nil {
		t.Fatalf("Maintainer = %+v, want nil without explicit opt-in", maintainer)
	}
}

func TestUpdateGitHubRepositoryRejectsInvalidDefaults(t *testing.T) {
	ns := testUserNS()
	existing := &triggersv1alpha1.GitHubRepository{
		ObjectMeta: metav1.ObjectMeta{Name: "acme-payments", Namespace: ns},
		Spec: triggersv1alpha1.GitHubRepositorySpec{
			Owner:             "acme",
			Repo:              "payments",
			GitHubTokenSecret: "trigger-token",
			Defaults: triggersv1alpha1.AgentRunDefaults{
				RepoURL: "https://github.com/acme/payments.git",
			},
		},
	}
	srv, _ := newCronTestServer(t, existing)

	// Trigger runs require an explicit model (validateTriggerRunDefaults).
	_, err := srv.UpdateGitHubRepository(projectActorCtx(), &platform.UpdateGitHubRepositoryRequest{
		Namespace: ns,
		Name:      "acme-payments",
		Defaults:  &platform.AgentRunDefaults{Provider: "anthropic"},
	})
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("empty model: got error %v, want CodeInvalidArgument", err)
	}

	// Explicit-credential configuration is validated when saved credentials
	// are not used (PR review P2).
	_, err = srv.UpdateGitHubRepository(projectActorCtx(), &platform.UpdateGitHubRepositoryRequest{
		Namespace: ns,
		Name:      "acme-payments",
		Defaults:  &platform.AgentRunDefaults{Provider: "anthropic", Model: "claude-sonnet-4-6"},
	})
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("missing explicit credentials: got error %v, want CodeInvalidArgument", err)
	}
}

func TestUpdateGitHubRepositorySavedCredentialsKeepInstallationTokenWiring(t *testing.T) {
	ns := testUserNS()
	existing := &triggersv1alpha1.GitHubRepository{
		ObjectMeta: metav1.ObjectMeta{Name: "acme-payments", Namespace: ns},
		Spec: triggersv1alpha1.GitHubRepositorySpec{
			Owner: "acme",
			Repo:  "payments",
			GitHubApp: &triggersv1alpha1.GitHubAppAuth{
				AppID:            99,
				InstallationID:   123,
				PrivateKeySecret: "github-app-key",
			},
			Defaults: triggersv1alpha1.AgentRunDefaults{
				RepoURL:  "https://github.com/acme/payments.git",
				Provider: triggersv1alpha1.ProviderAnthropic,
			},
		},
	}
	savedAnthropic := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: userCredentialSecretName("anthropic"), Namespace: ns},
		Data:       map[string][]byte{userCredAPIKeyKey: []byte("sk-ant")},
	}
	savedGitHub := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: userCredentialSecretName(credentialGitHub), Namespace: ns},
		Data:       map[string][]byte{userCredGithubTokenKey: []byte("gh-personal")},
	}
	srv, c := newCronTestServer(t, existing, savedAnthropic, savedGitHub)

	_, err := srv.UpdateGitHubRepository(projectActorCtx(), &platform.UpdateGitHubRepositoryRequest{
		Namespace:           ns,
		Name:                "acme-payments",
		Defaults:            &platform.AgentRunDefaults{Provider: "anthropic", AuthMode: "api-key", Model: "claude-sonnet-4-6"},
		UseSavedCredentials: true,
	})
	if err != nil {
		t.Fatalf("UpdateGitHubRepository() error = %v", err)
	}

	gh := &triggersv1alpha1.GitHubRepository{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: "acme-payments"}, gh); err != nil {
		t.Fatalf("Get(GitHubRepository) error = %v", err)
	}
	if gh.Spec.Defaults.Secrets.GithubToken != "" {
		t.Fatalf("Secrets.GithubToken = %q, want empty (installation-based trigger keeps GitHub App auth)", gh.Spec.Defaults.Secrets.GithubToken)
	}
	if len(gh.Spec.Defaults.Secrets.ProviderKeys) != 1 || gh.Spec.Defaults.Secrets.ProviderKeys[0].SecretName != userCredentialSecretName("anthropic") {
		t.Fatalf("ProviderKeys = %+v, want saved anthropic key", gh.Spec.Defaults.Secrets.ProviderKeys)
	}
}

func TestUpdateLinearProjectPreservesSpecLevelFields(t *testing.T) {
	ns := testUserNS()
	existing := &triggersv1alpha1.LinearProject{
		ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: ns},
		Spec: triggersv1alpha1.LinearProjectSpec{
			LinearAPIKeySecret: "linear-key",
			ProjectID:          "proj-1",
			TeamID:             "team-1",
			PollInterval:       metav1.Duration{Duration: 30 * 1e9},
			ApprovedLabel:      "ai-approved",
			AutoCreateTasks:    true,
			Defaults: triggersv1alpha1.AgentRunDefaults{
				RepoURL:  "https://github.com/example/old.git",
				Provider: triggersv1alpha1.ProviderOpenAI,
				Secrets:  triggersv1alpha1.AgentRunSecrets{GithubToken: "old-gh"},
			},
		},
	}
	srv, c := newCronTestServer(t, existing)

	defaults := fullParityDefaults()
	defaults.GithubTokenSecret = ""
	defaults.RuntimeProfileRef = ""
	defaults.McpPolicyRef = ""

	resp, err := srv.UpdateLinearProject(projectActorCtx(), &platform.UpdateLinearProjectRequest{
		Namespace: ns,
		Name:      "web",
		Defaults:  defaults,
		Policies:  parityPolicies(),
	})
	if err != nil {
		t.Fatalf("UpdateLinearProject() error = %v", err)
	}

	lp := &triggersv1alpha1.LinearProject{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: "web"}, lp); err != nil {
		t.Fatalf("Get(LinearProject) error = %v", err)
	}
	if lp.Spec.LinearAPIKeySecret != "linear-key" || lp.Spec.ProjectID != "proj-1" ||
		lp.Spec.TeamID != "team-1" || lp.Spec.ApprovedLabel != "ai-approved" ||
		!lp.Spec.AutoCreateTasks || lp.Spec.PollInterval.Duration != 30*1e9 {
		t.Fatalf("spec-level linear fields changed: %+v", lp.Spec)
	}
	d := lp.Spec.Defaults
	if d.RepoURL != "https://github.com/example/payments.git" || d.Model != "claude-sonnet-4-6" ||
		d.Provider != triggersv1alpha1.ProviderAnthropic {
		t.Fatalf("defaults = %+v", d)
	}
	if d.Secrets.GithubToken != "old-gh" {
		t.Fatalf("Secrets.GithubToken = %q, want preserved", d.Secrets.GithubToken)
	}
	assertPolicyObjects(t, c, ns, "web-runtime", "web-mcp-policy")
	if resp.Defaults.GetRuntimeProfileRef() != "web-runtime" || resp.PermissionMode != "read-only" {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestSavedCredentialModelQueryAuthModeOverride(t *testing.T) {
	const ns = "user-ns"

	tests := []struct {
		name     string
		secrets  []client.Object
		override string
		want     modelQuery
	}{
		{
			name: "forced api-key with only oauth falls back to oauth",
			secrets: []client.Object{userCredSecret("openai", map[string][]byte{
				"auth.json": []byte("{}"),
			})},
			override: "api-key",
			want: modelQuery{
				namespace:       ns,
				provider:        triggersv1alpha1.ProviderOpenAI,
				authMode:        platformv1alpha1.AgentRunAuthModeOAuth,
				oauthSecretName: "usercred-openai",
				openAIBaseURL:   triggersv1alpha1.DefaultOpenAIOAuthBaseURL,
			},
		},
		{
			name: "forced api-key wins when both credential kinds exist",
			secrets: []client.Object{userCredSecret("openai", map[string][]byte{
				"auth.json": []byte("{}"),
				"api-key":   []byte("sk-openai"),
			})},
			override: "api-key",
			want: modelQuery{
				namespace:        ns,
				provider:         triggersv1alpha1.ProviderOpenAI,
				authMode:         platformv1alpha1.AgentRunAuthModeAPIKey,
				apiKeySecretName: "usercred-openai",
				apiKeySecretKey:  "api-key",
				openAIBaseURL:    triggersv1alpha1.DefaultOpenAIBaseURL,
			},
		},
		{
			name: "forced oauth with only api-key falls back to api-key",
			secrets: []client.Object{userCredSecret("openai", map[string][]byte{
				"api-key": []byte("sk-openai"),
			})},
			override: "oauth",
			want: modelQuery{
				namespace:        ns,
				provider:         triggersv1alpha1.ProviderOpenAI,
				authMode:         platformv1alpha1.AgentRunAuthModeAPIKey,
				apiKeySecretName: "usercred-openai",
				apiKeySecretKey:  "api-key",
				openAIBaseURL:    triggersv1alpha1.DefaultOpenAIBaseURL,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scheme := testProjectScheme(t)
			c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(tt.secrets...).Build()
			srv := &Server{k8sClient: c, scheme: scheme}

			got, err := srv.savedCredentialModelQuery(context.Background(), ns, triggersv1alpha1.ProviderOpenAI, tt.override, nil)
			if err != nil {
				t.Fatalf("savedCredentialModelQuery() error = %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("query = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestSavedCredentialModelQueryAuthModeOverrideNoCredentialsStillFails(t *testing.T) {
	scheme := testProjectScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	srv := &Server{k8sClient: c, scheme: scheme}

	_, err := srv.savedCredentialModelQuery(context.Background(), "user-ns", triggersv1alpha1.ProviderOpenAI, "api-key", nil)
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("error = %v, want FailedPrecondition", err)
	}
}

func TestListAvailableModelsAuthModeOnlyRoutesToSavedCredentials(t *testing.T) {
	const ns = "user-ns"
	var gotAPIKey string
	swapProviderModelHTTPClient(t, func(r *http.Request) (*http.Response, error) {
		gotAPIKey = r.Header.Get("x-api-key")
		return jsonModelResponse(`{"data":[{"id":"claude-a"}]}`), nil
	})

	scheme := testProjectScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		userCredSecret("anthropic", map[string][]byte{"api-key": []byte("sk-ant-saved")}),
	).Build()
	srv := &Server{k8sClient: c, scheme: scheme}

	resp, err := srv.ListAvailableModels(context.Background(), &platform.ListAvailableModelsRequest{
		Namespace: ns,
		Provider:  "anthropic",
		AuthMode:  "api-key",
	})
	if err != nil {
		t.Fatalf("ListAvailableModels() error = %v", err)
	}
	if gotAPIKey != "sk-ant-saved" {
		t.Fatalf("x-api-key = %q, want saved anthropic key", gotAPIKey)
	}
	if len(resp.Models) != 1 || resp.Models[0] != "claude-a" {
		t.Fatalf("models = %v", resp.Models)
	}
}

// TestUpdateTriggerPreservesAdminOnlyDefaults ensures the GitHubRepository and
// LinearProject dashboard updates, which rebuild Spec.Defaults wholesale, keep
// the kubectl-only admin flags (disableCommandSandbox, kubernetesAdmin).
func TestUpdateTriggerPreservesAdminOnlyDefaults(t *testing.T) {
	ns := testUserNS()
	gh := &triggersv1alpha1.GitHubRepository{
		ObjectMeta: metav1.ObjectMeta{Name: "acme-payments", Namespace: ns},
		Spec: triggersv1alpha1.GitHubRepositorySpec{
			Owner:             "acme",
			Repo:              "payments",
			TriggerKeyword:    "@agent",
			GitHubTokenSecret: "trigger-token",
			Defaults: triggersv1alpha1.AgentRunDefaults{
				RepoURL:               "https://github.com/acme/payments.git",
				Provider:              triggersv1alpha1.ProviderOpenAI,
				DisableCommandSandbox: true,
				KubernetesAdmin:       true,
				Secrets:               triggersv1alpha1.AgentRunSecrets{GithubToken: "trigger-token"},
			},
		},
	}
	lp := &triggersv1alpha1.LinearProject{
		ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: ns},
		Spec: triggersv1alpha1.LinearProjectSpec{
			LinearAPIKeySecret: "linear-key",
			ProjectID:          "proj-1",
			Defaults: triggersv1alpha1.AgentRunDefaults{
				RepoURL:               "https://github.com/example/old.git",
				Provider:              triggersv1alpha1.ProviderOpenAI,
				DisableCommandSandbox: true,
				KubernetesAdmin:       true,
				Secrets:               triggersv1alpha1.AgentRunSecrets{GithubToken: "old-gh"},
			},
		},
	}
	srv, c := newCronTestServer(t, gh, lp)

	ghDefaults := fullParityDefaults()
	ghDefaults.GithubTokenSecret = ""
	ghDefaults.RuntimeProfileRef = ""
	ghDefaults.McpPolicyRef = ""
	if _, err := srv.UpdateGitHubRepository(projectActorCtx(), &platform.UpdateGitHubRepositoryRequest{
		Namespace: ns,
		Name:      "acme-payments",
		Defaults:  ghDefaults,
	}); err != nil {
		t.Fatalf("UpdateGitHubRepository() error = %v", err)
	}
	updatedGH := &triggersv1alpha1.GitHubRepository{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: "acme-payments"}, updatedGH); err != nil {
		t.Fatalf("Get(GitHubRepository) error = %v", err)
	}
	if !updatedGH.Spec.Defaults.DisableCommandSandbox || !updatedGH.Spec.Defaults.KubernetesAdmin {
		t.Fatalf("GitHubRepository admin-only defaults cleared: %+v", updatedGH.Spec.Defaults)
	}

	lpDefaults := fullParityDefaults()
	lpDefaults.GithubTokenSecret = ""
	lpDefaults.RuntimeProfileRef = ""
	lpDefaults.McpPolicyRef = ""
	if _, err := srv.UpdateLinearProject(projectActorCtx(), &platform.UpdateLinearProjectRequest{
		Namespace: ns,
		Name:      "web",
		Defaults:  lpDefaults,
	}); err != nil {
		t.Fatalf("UpdateLinearProject() error = %v", err)
	}
	updatedLP := &triggersv1alpha1.LinearProject{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: "web"}, updatedLP); err != nil {
		t.Fatalf("Get(LinearProject) error = %v", err)
	}
	if !updatedLP.Spec.Defaults.DisableCommandSandbox || !updatedLP.Spec.Defaults.KubernetesAdmin {
		t.Fatalf("LinearProject admin-only defaults cleared: %+v", updatedLP.Spec.Defaults)
	}
}
