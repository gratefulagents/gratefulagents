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

func newCronTestServer(t *testing.T, objs ...client.Object) (*Server, client.Client) {
	t.Helper()
	scheme := testProjectScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
	return &Server{k8sClient: c, scheme: scheme}, c
}

func fullCronDefaults() *platform.AgentRunDefaults {
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
		OpenaiApi:          "responses",
		Timeout:            "45m",
		CustomInstructions: "do the thing",
		GithubTokenSecret:  "gh-secret",
		ProviderKeys: []*platform.ProviderKeyRef{
			{Provider: "anthropic", SecretName: "anthropic-cred", SecretKey: "api-key"},
		},
		RuntimeProfileRef: "cron-runtime",
		McpPolicyRef:      "cron-policy",
		McpServerRefs:     []string{"mcp-a", "mcp-b"},
		SkillRefs:         []string{"skill-a"},
		WorkflowMode:      "auto",
		ModeRef:           "deep-research",
		ExecutionMode:     "linear",
	}
}

func TestCreateCronHappyPathFullDefaults(t *testing.T) {
	srv, c := newCronTestServer(t)
	ms := newMockStateStore()
	srv.stateStore = ms
	ns := testUserNS()

	resp, err := srv.CreateCron(projectActorCtx(), &platform.CreateCronRequest{
		Name:              "nightly-report",
		Schedule:          "0 6 * * *",
		TimeZone:          "UTC",
		Suspend:           false,
		ConcurrencyPolicy: "Allow",
		Prompt:            "write the nightly report",
		Defaults:          fullCronDefaults(),
	})
	if err != nil {
		t.Fatalf("CreateCron() error = %v", err)
	}
	if resp.Namespace != ns || resp.Name != "nightly-report" {
		t.Fatalf("resp = %s/%s, want %s/nightly-report", resp.Namespace, resp.Name, ns)
	}
	if resp.ConcurrencyPolicy != "Allow" {
		t.Fatalf("ConcurrencyPolicy = %q, want Allow", resp.ConcurrencyPolicy)
	}

	cr := &triggersv1alpha1.Cron{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: "nightly-report"}, cr); err != nil {
		t.Fatalf("Get(Cron) error = %v", err)
	}
	if cr.Spec.Schedule != "0 6 * * *" || cr.Spec.TimeZone != "UTC" || cr.Spec.Prompt != "write the nightly report" {
		t.Fatalf("spec = %+v", cr.Spec)
	}
	if cr.Spec.ConcurrencyPolicy != triggersv1alpha1.CronConcurrencyAllow {
		t.Fatalf("ConcurrencyPolicy = %q, want Allow", cr.Spec.ConcurrencyPolicy)
	}
	d := cr.Spec.Defaults
	if d.RepoURL != "https://github.com/example/payments.git" || d.BaseBranch != "main" ||
		d.Image != "ghcr.io/example/worker:latest" || d.Model != "claude-sonnet-4-6" {
		t.Fatalf("defaults = %+v", d)
	}
	if len(d.AdditionalRepos) != 1 || d.AdditionalRepos[0] != "https://github.com/example/lib.git" {
		t.Fatalf("AdditionalRepos = %#v", d.AdditionalRepos)
	}
	if d.Provider != triggersv1alpha1.ProviderAnthropic || d.AuthMode != platformv1alpha1.AgentRunAuthModeAPIKey {
		t.Fatalf("provider/authMode = %q/%q", d.Provider, d.AuthMode)
	}
	if d.ReasoningLevel != platformv1alpha1.ReasoningHigh {
		t.Fatalf("ReasoningLevel = %q, want high", d.ReasoningLevel)
	}
	if d.Timeout.Duration != 45*time.Minute {
		t.Fatalf("Timeout = %s, want 45m", d.Timeout.Duration)
	}
	if len(d.Secrets.ProviderKeys) != 1 || d.Secrets.ProviderKeys[0].SecretName != "anthropic-cred" ||
		d.Secrets.ProviderKeys[0].Provider != triggersv1alpha1.ProviderAnthropic {
		t.Fatalf("ProviderKeys = %#v", d.Secrets.ProviderKeys)
	}
	if d.Secrets.GithubToken != "gh-secret" {
		t.Fatalf("GithubToken = %q", d.Secrets.GithubToken)
	}
	if d.RuntimeProfileRef == nil || d.RuntimeProfileRef.Name != "cron-runtime" {
		t.Fatalf("RuntimeProfileRef = %#v", d.RuntimeProfileRef)
	}
	if d.MCPPolicyRef == nil || d.MCPPolicyRef.Name != "cron-policy" {
		t.Fatalf("MCPPolicyRef = %#v", d.MCPPolicyRef)
	}
	if len(d.MCPServerRefs) != 2 || d.MCPServerRefs[0].Name != "mcp-a" {
		t.Fatalf("MCPServerRefs = %#v", d.MCPServerRefs)
	}
	if len(d.SkillRefs) != 1 || d.SkillRefs[0].Name != "skill-a" {
		t.Fatalf("SkillRefs = %#v", d.SkillRefs)
	}
	if d.ModeRef == nil || d.ModeRef.Name != "deep-research" {
		t.Fatalf("ModeRef = %#v", d.ModeRef)
	}
	if d.WorkflowMode != platformv1alpha1.WorkflowModeAuto || d.ExecutionMode != platformv1alpha1.ExecutionModeLinear {
		t.Fatalf("workflow/execution = %q/%q", d.WorkflowMode, d.ExecutionMode)
	}

	// Ownership recorded for the creating actor.
	owner, err := ms.GetResourceOwner(context.Background(), cronResourceType, "nightly-report", ns)
	if err != nil || owner == nil || owner.OwnerID != testProjectSubject {
		t.Fatalf("ownership = %#v, err = %v, want owner %q", owner, err, testProjectSubject)
	}

	// Proto round-trip: defaults (field 41) mirrors the CR spec.
	pd := resp.GetDefaults()
	if pd == nil {
		t.Fatal("resp.Defaults is nil")
	}
	if pd.RepoUrl != d.RepoURL || pd.Model != d.Model || pd.Timeout != "45m0s" ||
		pd.RuntimeProfileRef != "cron-runtime" || pd.McpPolicyRef != "cron-policy" ||
		pd.ModeRef != "deep-research" || pd.WorkflowMode != "auto" || pd.ExecutionMode != "linear" {
		t.Fatalf("proto defaults = %+v", pd)
	}
	if len(pd.ProviderKeys) != 1 || pd.ProviderKeys[0].SecretName != "anthropic-cred" {
		t.Fatalf("proto ProviderKeys = %#v", pd.ProviderKeys)
	}
	if len(pd.McpServerRefs) != 2 || len(pd.SkillRefs) != 1 {
		t.Fatalf("proto McpServerRefs/SkillRefs = %#v / %#v", pd.McpServerRefs, pd.SkillRefs)
	}
	// Flattened legacy fields stay populated.
	if resp.RepoUrl != d.RepoURL || resp.Model != d.Model || resp.Provider != "anthropic" ||
		resp.GithubTokenSecret != "gh-secret" || resp.Timeout != "45m0s" {
		t.Fatalf("flattened fields = %+v", resp)
	}
}

func TestCreateCronDerivesNameWhenEmpty(t *testing.T) {
	srv, _ := newCronTestServer(t)

	resp, err := srv.CreateCron(projectActorCtx(), &platform.CreateCronRequest{
		Schedule: "@hourly",
		Prompt:   "check things",
		Defaults: &platform.AgentRunDefaults{Provider: "openai", ClaudeApiKeySecret: "x"},
	})
	if err != nil {
		t.Fatalf("CreateCron() error = %v", err)
	}
	if resp.Name == "" || len(resp.Name) < len("cron-")+1 || resp.Name[:5] != "cron-" {
		t.Fatalf("derived name = %q, want cron-<suffix>", resp.Name)
	}
}

func TestCreateCronValidationFailures(t *testing.T) {
	cases := []struct {
		name string
		req  *platform.CreateCronRequest
	}{
		{"bad schedule", &platform.CreateCronRequest{Schedule: "not a cron", Prompt: "p"}},
		{"bad timezone", &platform.CreateCronRequest{Schedule: "0 6 * * *", TimeZone: "Not/AZone", Prompt: "p"}},
		{"empty prompt", &platform.CreateCronRequest{Schedule: "0 6 * * *"}},
		{"bad concurrency policy", &platform.CreateCronRequest{Schedule: "0 6 * * *", Prompt: "p", ConcurrencyPolicy: "Replace"}},
		{"bad timeout", &platform.CreateCronRequest{Schedule: "0 6 * * *", Prompt: "p", Defaults: &platform.AgentRunDefaults{Timeout: "banana"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, _ := newCronTestServer(t)
			_, err := srv.CreateCron(projectActorCtx(), tc.req)
			var connectErr *connect.Error
			if !errors.As(err, &connectErr) || connectErr.Code() != connect.CodeInvalidArgument {
				t.Fatalf("CreateCron() error = %v, want InvalidArgument", err)
			}
		})
	}
}

func TestCreateCronDeniedForForeignNamespace(t *testing.T) {
	srv, _ := newCronTestServer(t)

	_, err := srv.CreateCron(projectActorCtx(), &platform.CreateCronRequest{
		Namespace: "someone-elses-ns",
		Schedule:  "0 6 * * *",
		Prompt:    "p",
	})
	var connectErr *connect.Error
	if !errors.As(err, &connectErr) || connectErr.Code() != connect.CodePermissionDenied {
		t.Fatalf("CreateCron() error = %v, want PermissionDenied", err)
	}
}

func TestCreateCronUsesSavedCredentials(t *testing.T) {
	ns := testUserNS()
	saved := []client.Object{
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: userCredentialSecretName(triggersv1alpha1.ProviderAnthropic), Namespace: ns},
			Data:       map[string][]byte{userCredAPIKeyKey: []byte("sk-ant-saved")},
		},
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: userCredentialSecretName(credentialGitHub), Namespace: ns},
			Data:       map[string][]byte{userCredGithubTokenKey: []byte("gh-saved")},
		},
	}
	srv, c := newCronTestServer(t, saved...)

	_, err := srv.CreateCron(projectActorCtx(), &platform.CreateCronRequest{
		Name:     "saved-cron",
		Schedule: "0 6 * * *",
		Prompt:   "p",
		Defaults: &platform.AgentRunDefaults{
			Provider: "anthropic",
			AuthMode: "api-key",
			// Explicit refs must be ignored when use_saved_credentials is set.
			GithubTokenSecret: "explicit-gh",
		},
		UseSavedCredentials: true,
	})
	if err != nil {
		t.Fatalf("CreateCron() error = %v", err)
	}

	cr := &triggersv1alpha1.Cron{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: "saved-cron"}, cr); err != nil {
		t.Fatalf("Get(Cron) error = %v", err)
	}
	keys := cr.Spec.Defaults.Secrets.ProviderKeys
	if len(keys) != 1 || keys[0].Provider != triggersv1alpha1.ProviderAnthropic ||
		keys[0].SecretName != userCredentialSecretName(triggersv1alpha1.ProviderAnthropic) || keys[0].SecretKey != userCredAPIKeyKey {
		t.Fatalf("ProviderKeys = %#v, want saved anthropic credential", keys)
	}
	if cr.Spec.Defaults.Secrets.GithubToken != userCredentialSecretName(credentialGitHub) {
		t.Fatalf("GithubToken = %q, want saved github secret", cr.Spec.Defaults.Secrets.GithubToken)
	}
}

func TestCreateCronUseSavedCredentialsMissingFails(t *testing.T) {
	srv, _ := newCronTestServer(t)

	_, err := srv.CreateCron(projectActorCtx(), &platform.CreateCronRequest{
		Schedule:            "0 6 * * *",
		Prompt:              "p",
		Defaults:            &platform.AgentRunDefaults{Provider: "anthropic", AuthMode: "api-key"},
		UseSavedCredentials: true,
	})
	var connectErr *connect.Error
	if !errors.As(err, &connectErr) || connectErr.Code() != connect.CodeFailedPrecondition {
		t.Fatalf("CreateCron() error = %v, want FailedPrecondition", err)
	}
}

func TestUpdateCronReplacesSpec(t *testing.T) {
	ns := testUserNS()
	existing := &triggersv1alpha1.Cron{
		ObjectMeta: metav1.ObjectMeta{Name: "nightly", Namespace: ns},
		Spec: triggersv1alpha1.CronSpec{
			Schedule:          "0 6 * * *",
			TimeZone:          "UTC",
			Suspend:           false,
			ConcurrencyPolicy: triggersv1alpha1.CronConcurrencyForbid,
			Prompt:            "old prompt",
			Defaults: triggersv1alpha1.AgentRunDefaults{
				RepoURL:  "https://github.com/example/old.git",
				Provider: triggersv1alpha1.ProviderOpenAI,
				Secrets:  triggersv1alpha1.AgentRunSecrets{GithubToken: "old-gh"},
			},
		},
	}
	srv, c := newCronTestServer(t, existing)

	resp, err := srv.UpdateCron(projectActorCtx(), &platform.UpdateCronRequest{
		Namespace:         ns,
		Name:              "nightly",
		Schedule:          "@daily",
		Suspend:           true,
		ConcurrencyPolicy: "Allow",
		Prompt:            "new prompt",
		Defaults: &platform.AgentRunDefaults{
			Model:             "gpt-5.2",
			Provider:          "openai",
			GithubTokenSecret: "new-gh",
		},
	})
	if err != nil {
		t.Fatalf("UpdateCron() error = %v", err)
	}
	if !resp.Suspend || resp.Schedule != "@daily" || resp.Prompt != "new prompt" || resp.ConcurrencyPolicy != "Allow" {
		t.Fatalf("resp = %+v", resp)
	}

	cr := &triggersv1alpha1.Cron{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: "nightly"}, cr); err != nil {
		t.Fatalf("Get(Cron) error = %v", err)
	}
	if !cr.Spec.Suspend || cr.Spec.Schedule != "@daily" || cr.Spec.Prompt != "new prompt" {
		t.Fatalf("spec = %+v", cr.Spec)
	}
	// Spec is replaced as a whole: fields absent from the request reset.
	if cr.Spec.Defaults.RepoURL != "" {
		t.Fatalf("RepoURL = %q, want cleared", cr.Spec.Defaults.RepoURL)
	}
	if cr.Spec.Defaults.Secrets.GithubToken != "new-gh" || cr.Spec.Defaults.Model != "gpt-5.2" {
		t.Fatalf("defaults = %+v", cr.Spec.Defaults)
	}

	// Toggle suspend back off.
	resp, err = srv.UpdateCron(projectActorCtx(), &platform.UpdateCronRequest{
		Namespace: ns,
		Name:      "nightly",
		Schedule:  "@daily",
		Suspend:   false,
		Prompt:    "new prompt",
	})
	if err != nil {
		t.Fatalf("UpdateCron() error = %v", err)
	}
	if resp.Suspend {
		t.Fatal("Suspend = true, want false")
	}
}

func TestUpdateCronDeniedForStrangerOnOwnedCron(t *testing.T) {
	existing := &triggersv1alpha1.Cron{
		ObjectMeta: metav1.ObjectMeta{Name: "owned", Namespace: "default"},
		Spec:       triggersv1alpha1.CronSpec{Schedule: "0 6 * * *", Prompt: "p"},
	}
	srv, _ := newCronTestServer(t, existing)
	ms := newMockStateStore()
	srv.stateStore = ms
	if err := ms.SetResourceOwner(context.Background(), cronResourceType, "owned", "default", "alice"); err != nil {
		t.Fatalf("SetResourceOwner: %v", err)
	}

	_, err := srv.UpdateCron(actorContext("mallory", "member", "", ""), &platform.UpdateCronRequest{
		Namespace: "default", Name: "owned", Schedule: "0 6 * * *", Prompt: "p",
	})
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("UpdateCron by stranger: want PermissionDenied, got %v", err)
	}

	_, err = srv.DeleteCron(actorContext("mallory", "member", "", ""), &platform.DeleteCronRequest{Namespace: "default", Name: "owned"})
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("DeleteCron by stranger: want PermissionDenied, got %v", err)
	}
}

func TestUpdateCronNotFound(t *testing.T) {
	srv, _ := newCronTestServer(t)
	_, err := srv.UpdateCron(projectActorCtx(), &platform.UpdateCronRequest{
		Namespace: "default", Name: "missing", Schedule: "0 6 * * *", Prompt: "p",
	})
	if connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("UpdateCron() error = %v, want NotFound", err)
	}
}

func TestDeleteCron(t *testing.T) {
	ns := testUserNS()
	existing := &triggersv1alpha1.Cron{
		ObjectMeta: metav1.ObjectMeta{Name: "doomed", Namespace: ns},
		Spec:       triggersv1alpha1.CronSpec{Schedule: "0 6 * * *", Prompt: "p"},
	}
	srv, c := newCronTestServer(t, existing)

	if _, err := srv.DeleteCron(projectActorCtx(), &platform.DeleteCronRequest{Namespace: ns, Name: "doomed"}); err != nil {
		t.Fatalf("DeleteCron() error = %v", err)
	}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: "doomed"}, &triggersv1alpha1.Cron{}); !apierrors.IsNotFound(err) {
		t.Fatalf("cron still exists, err = %v", err)
	}

	_, err := srv.DeleteCron(projectActorCtx(), &platform.DeleteCronRequest{Namespace: ns, Name: "doomed"})
	if connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("DeleteCron(missing) error = %v, want NotFound", err)
	}
}

func TestGetAndListCronsExposeConcurrencyPolicyAndDefaults(t *testing.T) {
	existing := &triggersv1alpha1.Cron{
		ObjectMeta: metav1.ObjectMeta{Name: "reader", Namespace: "default"},
		Spec: triggersv1alpha1.CronSpec{
			Schedule:          "0 6 * * *",
			ConcurrencyPolicy: triggersv1alpha1.CronConcurrencyAllow,
			Prompt:            "p",
			Defaults: triggersv1alpha1.AgentRunDefaults{
				RepoURL:  "https://github.com/example/app.git",
				Model:    "claude-sonnet-4-6",
				Provider: triggersv1alpha1.ProviderAnthropic,
				ModeRef:  &platformv1alpha1.ModeRef{Name: "reviewer"},
				RuntimeProfileRef: &platformv1alpha1.NamedRef{
					Name: "rp",
				},
			},
		},
	}
	srv, _ := newCronTestServer(t, existing)

	got, err := srv.GetCron(context.Background(), &platform.GetCronRequest{Namespace: "default", Name: "reader"})
	if err != nil {
		t.Fatalf("GetCron() error = %v", err)
	}
	if got.ConcurrencyPolicy != "Allow" {
		t.Fatalf("ConcurrencyPolicy = %q, want Allow", got.ConcurrencyPolicy)
	}
	if got.Defaults == nil || got.Defaults.RepoUrl != "https://github.com/example/app.git" ||
		got.Defaults.ModeRef != "reviewer" || got.Defaults.RuntimeProfileRef != "rp" {
		t.Fatalf("Defaults = %+v", got.Defaults)
	}

	list, err := srv.ListCrons(context.Background(), &platform.ListCronsRequest{Namespace: "default"})
	if err != nil {
		t.Fatalf("ListCrons() error = %v", err)
	}
	if len(list.Crons) != 1 || list.Crons[0].ConcurrencyPolicy != "Allow" || list.Crons[0].Defaults == nil {
		t.Fatalf("ListCrons = %+v", list.Crons)
	}
}

func TestUpdateCronPreservesAdminOnlyDefaults(t *testing.T) {
	ns := testUserNS()
	existing := &triggersv1alpha1.Cron{
		ObjectMeta: metav1.ObjectMeta{Name: "nightly", Namespace: ns},
		Spec: triggersv1alpha1.CronSpec{
			Schedule:          "0 6 * * *",
			ConcurrencyPolicy: triggersv1alpha1.CronConcurrencyForbid,
			Prompt:            "old prompt",
			Defaults: triggersv1alpha1.AgentRunDefaults{
				Provider:              triggersv1alpha1.ProviderOpenAI,
				DisableCommandSandbox: true,
				KubernetesAdmin:       true,
				Secrets:               triggersv1alpha1.AgentRunSecrets{GithubToken: "old-gh"},
			},
		},
	}
	srv, c := newCronTestServer(t, existing)

	// The dashboard form cannot manage the kubectl-only admin flags; a save
	// that rebuilds the defaults must not clear them.
	if _, err := srv.UpdateCron(projectActorCtx(), &platform.UpdateCronRequest{
		Namespace:         ns,
		Name:              "nightly",
		Schedule:          "@daily",
		ConcurrencyPolicy: "Allow",
		Prompt:            "new prompt",
		Defaults: &platform.AgentRunDefaults{
			Model:             "gpt-5.2",
			Provider:          "openai",
			GithubTokenSecret: "new-gh",
		},
	}); err != nil {
		t.Fatalf("UpdateCron() error = %v", err)
	}

	cr := &triggersv1alpha1.Cron{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: "nightly"}, cr); err != nil {
		t.Fatalf("Get(Cron) error = %v", err)
	}
	if !cr.Spec.Defaults.DisableCommandSandbox {
		t.Fatal("DisableCommandSandbox cleared by dashboard update, want preserved")
	}
	if !cr.Spec.Defaults.KubernetesAdmin {
		t.Fatal("KubernetesAdmin cleared by dashboard update, want preserved")
	}
	if cr.Spec.Defaults.Model != "gpt-5.2" {
		t.Fatalf("Model = %q, want request value applied", cr.Spec.Defaults.Model)
	}
}
