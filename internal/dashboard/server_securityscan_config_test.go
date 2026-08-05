package dashboard

import (
	"context"
	"errors"
	"testing"
	"time"

	"connectrpc.com/connect"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
)

func fullSecurityScanSpec() *platform.SecurityScanConfigSpec {
	return &platform.SecurityScanConfigSpec{
		RepoUrl:         "https://github.com/example/payments.git",
		BaseBranch:      "release",
		Revision:        "abc123",
		AdditionalRepos: []string{"https://github.com/example/lib.git"},
		Scope: &platform.SecurityScanScopeConfig{
			Focus:        "payment flows",
			IncludePaths: []string{"internal/**"},
			ExcludePaths: []string{"vendor/**"},
			Languages:    []string{"go"},
		},
		Workflow: []*platform.SecurityScanTaskConfig{
			{Name: "injection", Objective: "hunt injections", Category: "injection", MaxFindings: 5},
			{Name: "triage", Objective: "triage findings", Role: "finding-triager", Model: "gpt-5.2",
				DependsOn: []string{"injection"}},
		},
		Parallelism: 8,
		SeverityRankers: []*platform.SecurityRankerConfig{
			{Name: "payments", Rules: "auth bypass is always critical"},
		},
		PostScripts: []*platform.SecurityPostScriptConfig{
			{Name: "poc", Prompt: "write a proof of concept", RunOn: "high-and-above"},
		},
		Dedupe:            &platform.SecurityScanDedupeConfig{Enabled: true, SimilarityThresholdPermille: 900},
		MinSeverity:       "medium",
		FailOnSeverity:    "high",
		Schedule:          "0 3 * * *",
		TimeZone:          "UTC",
		Suspend:           false,
		ConcurrencyPolicy: "Allow",
		Defaults:          fullCronDefaults(),
		MaxRuntime:        "2h",
	}
}

func TestCreateSecurityScanHappyPathFullSpec(t *testing.T) {
	srv, c := newCronTestServer(t)
	ms := newMockStateStore()
	srv.stateStore = ms
	ns := testUserNS()

	resp, err := srv.CreateSecurityScan(projectActorCtx(), &platform.CreateSecurityScanRequest{
		Name: "nightly-scan",
		Spec: fullSecurityScanSpec(),
	})
	if err != nil {
		t.Fatalf("CreateSecurityScan() error = %v", err)
	}
	if resp.Namespace != ns || resp.Name != "nightly-scan" {
		t.Fatalf("resp = %s/%s, want %s/nightly-scan", resp.Namespace, resp.Name, ns)
	}

	cr := &triggersv1alpha1.SecurityScan{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: "nightly-scan"}, cr); err != nil {
		t.Fatalf("Get(SecurityScan) error = %v", err)
	}
	assertFullScanSpec(t, cr.Spec)

	// Ownership recorded for the creating actor.
	owner, err := ms.GetResourceOwner(context.Background(), securityScanResourceType, "nightly-scan", ns)
	if err != nil || owner == nil || owner.OwnerID != testProjectSubject {
		t.Fatalf("ownership = %#v, err = %v, want owner %q", owner, err, testProjectSubject)
	}

	// Proto round-trip mirrors the CR spec.
	ps := resp.GetSpec()
	if ps == nil || ps.RepoUrl != cr.Spec.RepoURL || ps.MaxRuntime != "2h0m0s" || ps.Parallelism != 8 ||
		len(ps.Workflow) != 2 || ps.Dedupe == nil || !ps.Dedupe.Enabled ||
		ps.Defaults == nil || ps.Defaults.Model != "claude-sonnet-4-6" {
		t.Fatalf("proto spec = %+v", ps)
	}
}

func assertFullScanSpec(t *testing.T, spec triggersv1alpha1.SecurityScanSpec) {
	t.Helper()
	if spec.RepoURL != "https://github.com/example/payments.git" || spec.BaseBranch != "release" ||
		spec.Revision != "abc123" || spec.Schedule != "0 3 * * *" || spec.TimeZone != "UTC" {
		t.Fatalf("spec = %+v", spec)
	}
	if len(spec.AdditionalRepos) != 1 || spec.AdditionalRepos[0] != "https://github.com/example/lib.git" {
		t.Fatalf("AdditionalRepos = %#v", spec.AdditionalRepos)
	}
	if spec.Parallelism != 8 || spec.MinSeverity != "medium" || spec.FailOnSeverity != "high" {
		t.Fatalf("parallelism/severities = %+v", spec)
	}
	if spec.ConcurrencyPolicy != triggersv1alpha1.SecurityScanConcurrencyAllow {
		t.Fatalf("ConcurrencyPolicy = %q, want Allow", spec.ConcurrencyPolicy)
	}
	if spec.MaxRuntime.Duration != 2*time.Hour {
		t.Fatalf("MaxRuntime = %s, want 2h", spec.MaxRuntime.Duration)
	}
	if spec.Defaults.RepoURL != "https://github.com/example/payments.git" || spec.Defaults.Model != "claude-sonnet-4-6" {
		t.Fatalf("Defaults = %+v", spec.Defaults)
	}
	assertFullScanAdvancedSpec(t, spec)
}

func assertFullScanAdvancedSpec(t *testing.T, spec triggersv1alpha1.SecurityScanSpec) {
	t.Helper()
	if spec.Scope == nil || spec.Scope.Focus != "payment flows" || len(spec.Scope.IncludePaths) != 1 ||
		len(spec.Scope.ExcludePaths) != 1 || len(spec.Scope.Languages) != 1 {
		t.Fatalf("Scope = %+v", spec.Scope)
	}
	if len(spec.Workflow) != 2 || spec.Workflow[0].Name != "injection" || spec.Workflow[0].MaxFindings != 5 ||
		spec.Workflow[1].Role != "finding-triager" || spec.Workflow[1].DependsOn[0] != "injection" {
		t.Fatalf("Workflow = %+v", spec.Workflow)
	}
	if len(spec.SeverityRankers) != 1 || spec.SeverityRankers[0].Name != "payments" {
		t.Fatalf("SeverityRankers = %+v", spec.SeverityRankers)
	}
	if len(spec.PostScripts) != 1 || spec.PostScripts[0].RunOn != "high-and-above" {
		t.Fatalf("PostScripts = %+v", spec.PostScripts)
	}
	if spec.Dedupe == nil || spec.Dedupe.Enabled == nil || !*spec.Dedupe.Enabled ||
		spec.Dedupe.SimilarityThresholdPermille != 900 {
		t.Fatalf("Dedupe = %+v", spec.Dedupe)
	}
}

func TestCreateSecurityScanDerivesNameWhenEmpty(t *testing.T) {
	srv, _ := newCronTestServer(t)

	resp, err := srv.CreateSecurityScan(projectActorCtx(), &platform.CreateSecurityScanRequest{
		Spec: &platform.SecurityScanConfigSpec{RepoUrl: "https://github.com/example/app.git"},
	})
	if err != nil {
		t.Fatalf("CreateSecurityScan() error = %v", err)
	}
	if len(resp.Name) <= len("securityscan-") || resp.Name[:13] != "securityscan-" {
		t.Fatalf("derived name = %q, want securityscan-<suffix>", resp.Name)
	}
}

func TestCreateSecurityScanValidationFailures(t *testing.T) {
	base := func() *platform.SecurityScanConfigSpec {
		return &platform.SecurityScanConfigSpec{RepoUrl: "https://github.com/example/app.git"}
	}
	cases := []struct {
		name string
		spec *platform.SecurityScanConfigSpec
	}{
		{"missing repo_url", &platform.SecurityScanConfigSpec{Schedule: "@daily"}},
		{"bad schedule", func() *platform.SecurityScanConfigSpec { s := base(); s.Schedule = "not a cron"; return s }()},
		{"bad timezone", func() *platform.SecurityScanConfigSpec {
			s := base()
			s.Schedule = "@daily"
			s.TimeZone = "Not/AZone"
			return s
		}()},
		{"bad timezone without schedule", func() *platform.SecurityScanConfigSpec {
			s := base()
			s.TimeZone = "Not/AZone"
			return s
		}()},
		{"bad concurrency policy", func() *platform.SecurityScanConfigSpec {
			s := base()
			s.ConcurrencyPolicy = "Replace"
			return s
		}()},
		{"bad min severity", func() *platform.SecurityScanConfigSpec { s := base(); s.MinSeverity = "severe"; return s }()},
		{"bad fail severity", func() *platform.SecurityScanConfigSpec { s := base(); s.FailOnSeverity = "bad"; return s }()},
		{"parallelism too high", func() *platform.SecurityScanConfigSpec { s := base(); s.Parallelism = 17; return s }()},
		{"parallelism negative", func() *platform.SecurityScanConfigSpec { s := base(); s.Parallelism = -1; return s }()},
		{"duplicate task names", func() *platform.SecurityScanConfigSpec {
			s := base()
			s.Workflow = []*platform.SecurityScanTaskConfig{
				{Name: "dup", Objective: "a"},
				{Name: "dup", Objective: "b"},
			}
			return s
		}()},
		{"invalid task name", func() *platform.SecurityScanConfigSpec {
			s := base()
			s.Workflow = []*platform.SecurityScanTaskConfig{{Name: "Bad Name", Objective: "a"}}
			return s
		}()},
		{"task without objective", func() *platform.SecurityScanConfigSpec {
			s := base()
			s.Workflow = []*platform.SecurityScanTaskConfig{{Name: "task"}}
			return s
		}()},
		{"unresolvable depends_on", func() *platform.SecurityScanConfigSpec {
			s := base()
			s.Workflow = []*platform.SecurityScanTaskConfig{
				{Name: "task", Objective: "a", DependsOn: []string{"ghost"}},
			}
			return s
		}()},
		{"self depends_on", func() *platform.SecurityScanConfigSpec {
			s := base()
			s.Workflow = []*platform.SecurityScanTaskConfig{
				{Name: "task", Objective: "a", DependsOn: []string{"task"}},
			}
			return s
		}()},
		{"bad dedupe permille", func() *platform.SecurityScanConfigSpec {
			s := base()
			s.Dedupe = &platform.SecurityScanDedupeConfig{Enabled: true, SimilarityThresholdPermille: 1500}
			return s
		}()},
		{"bad post-script run_on", func() *platform.SecurityScanConfigSpec {
			s := base()
			s.PostScripts = []*platform.SecurityPostScriptConfig{{Name: "p", Prompt: "x", RunOn: "never"}}
			return s
		}()},
		{"ranker without rules", func() *platform.SecurityScanConfigSpec {
			s := base()
			s.SeverityRankers = []*platform.SecurityRankerConfig{{Name: "r"}}
			return s
		}()},
		{"bad max_runtime", func() *platform.SecurityScanConfigSpec { s := base(); s.MaxRuntime = "banana"; return s }()},
		{"bad defaults timeout", func() *platform.SecurityScanConfigSpec {
			s := base()
			s.Defaults = &platform.AgentRunDefaults{Timeout: "banana"}
			return s
		}()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, _ := newCronTestServer(t)
			_, err := srv.CreateSecurityScan(projectActorCtx(), &platform.CreateSecurityScanRequest{Spec: tc.spec})
			var connectErr *connect.Error
			if !errors.As(err, &connectErr) || connectErr.Code() != connect.CodeInvalidArgument {
				t.Fatalf("CreateSecurityScan() error = %v, want InvalidArgument", err)
			}
		})
	}
}

func TestCreateSecurityScanDeniedForForeignNamespace(t *testing.T) {
	srv, _ := newCronTestServer(t)

	_, err := srv.CreateSecurityScan(projectActorCtx(), &platform.CreateSecurityScanRequest{
		Namespace: "someone-elses-ns",
		Spec:      &platform.SecurityScanConfigSpec{RepoUrl: "https://github.com/example/app.git"},
	})
	var connectErr *connect.Error
	if !errors.As(err, &connectErr) || connectErr.Code() != connect.CodePermissionDenied {
		t.Fatalf("CreateSecurityScan() error = %v, want PermissionDenied", err)
	}
}

func TestCreateSecurityScanUsesSavedCredentials(t *testing.T) {
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

	_, err := srv.CreateSecurityScan(projectActorCtx(), &platform.CreateSecurityScanRequest{
		Name: "saved-scan",
		Spec: &platform.SecurityScanConfigSpec{
			RepoUrl: "https://github.com/example/app.git",
			Defaults: &platform.AgentRunDefaults{
				Provider: "anthropic",
				AuthMode: "api-key",
				// Explicit refs must be ignored when use_saved_credentials is set.
				GithubTokenSecret: "explicit-gh",
			},
		},
		UseSavedCredentials: true,
	})
	if err != nil {
		t.Fatalf("CreateSecurityScan() error = %v", err)
	}

	cr := &triggersv1alpha1.SecurityScan{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: "saved-scan"}, cr); err != nil {
		t.Fatalf("Get(SecurityScan) error = %v", err)
	}
	keys := cr.Spec.Defaults.Secrets.ProviderKeys
	if len(keys) != 1 || keys[0].Provider != triggersv1alpha1.ProviderAnthropic ||
		keys[0].SecretName != userCredentialSecretName(triggersv1alpha1.ProviderAnthropic) {
		t.Fatalf("ProviderKeys = %#v, want saved anthropic credential", keys)
	}
	if cr.Spec.Defaults.Secrets.GithubToken != userCredentialSecretName(credentialGitHub) {
		t.Fatalf("GithubToken = %q, want saved github secret", cr.Spec.Defaults.Secrets.GithubToken)
	}
}

func TestUpdateSecurityScanReplacesSpecAndPreservesAdminDefaults(t *testing.T) {
	ns := testUserNS()
	existing := &triggersv1alpha1.SecurityScan{
		ObjectMeta: metav1.ObjectMeta{Name: "nightly-scan", Namespace: ns},
		Spec: triggersv1alpha1.SecurityScanSpec{
			RepoURL:  "https://github.com/example/old.git",
			Schedule: "0 3 * * *",
			Defaults: triggersv1alpha1.AgentRunDefaults{
				Provider:              triggersv1alpha1.ProviderOpenAI,
				DisableCommandSandbox: true,
				Secrets:               triggersv1alpha1.AgentRunSecrets{GithubToken: "old-gh"},
			},
		},
	}
	srv, c := newCronTestServer(t, existing)

	resp, err := srv.UpdateSecurityScan(projectActorCtx(), &platform.UpdateSecurityScanRequest{
		Namespace: ns,
		Name:      "nightly-scan",
		Spec: &platform.SecurityScanConfigSpec{
			RepoUrl: "https://github.com/example/new.git",
			Suspend: true,
			Defaults: &platform.AgentRunDefaults{
				Provider:          "openai",
				GithubTokenSecret: "new-gh",
			},
		},
	})
	if err != nil {
		t.Fatalf("UpdateSecurityScan() error = %v", err)
	}
	if !resp.GetSpec().GetSuspend() || resp.GetSpec().GetRepoUrl() != "https://github.com/example/new.git" {
		t.Fatalf("resp spec = %+v", resp.GetSpec())
	}

	cr := &triggersv1alpha1.SecurityScan{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: "nightly-scan"}, cr); err != nil {
		t.Fatalf("Get(SecurityScan) error = %v", err)
	}
	// Spec is replaced as a whole: fields absent from the request reset.
	if cr.Spec.Schedule != "" || !cr.Spec.Suspend || cr.Spec.RepoURL != "https://github.com/example/new.git" {
		t.Fatalf("spec = %+v", cr.Spec)
	}
	if cr.Spec.Defaults.Secrets.GithubToken != "new-gh" {
		t.Fatalf("defaults = %+v", cr.Spec.Defaults)
	}
	// kubectl-only admin flags survive a dashboard save.
	if !cr.Spec.Defaults.DisableCommandSandbox {
		t.Fatal("DisableCommandSandbox cleared by dashboard update, want preserved")
	}
}

func TestUpdateSecurityScanDeniedForStrangerOnOwnedScan(t *testing.T) {
	existing := &triggersv1alpha1.SecurityScan{
		ObjectMeta: metav1.ObjectMeta{Name: "owned", Namespace: "default"},
		Spec:       triggersv1alpha1.SecurityScanSpec{RepoURL: "https://github.com/example/app.git"},
	}
	srv, _ := newCronTestServer(t, existing)
	ms := newMockStateStore()
	srv.stateStore = ms
	if err := ms.SetResourceOwner(context.Background(), securityScanResourceType, "owned", "default", "alice"); err != nil {
		t.Fatalf("SetResourceOwner: %v", err)
	}

	_, err := srv.UpdateSecurityScan(actorContext("mallory", "member", "", ""), &platform.UpdateSecurityScanRequest{
		Namespace: "default", Name: "owned",
		Spec: &platform.SecurityScanConfigSpec{RepoUrl: "https://github.com/example/app.git"},
	})
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("UpdateSecurityScan by stranger: want PermissionDenied, got %v", err)
	}

	_, err = srv.DeleteSecurityScan(actorContext("mallory", "member", "", ""),
		&platform.DeleteSecurityScanRequest{Namespace: "default", Name: "owned"})
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("DeleteSecurityScan by stranger: want PermissionDenied, got %v", err)
	}
}

func TestUpdateSecurityScanNotFound(t *testing.T) {
	srv, _ := newCronTestServer(t)
	_, err := srv.UpdateSecurityScan(projectActorCtx(), &platform.UpdateSecurityScanRequest{
		Namespace: "default", Name: "missing",
		Spec: &platform.SecurityScanConfigSpec{RepoUrl: "https://github.com/example/app.git"},
	})
	if connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("UpdateSecurityScan() error = %v, want NotFound", err)
	}
}

func TestDeleteSecurityScan(t *testing.T) {
	ns := testUserNS()
	existing := &triggersv1alpha1.SecurityScan{
		ObjectMeta: metav1.ObjectMeta{Name: "doomed", Namespace: ns},
		Spec:       triggersv1alpha1.SecurityScanSpec{RepoURL: "https://github.com/example/app.git"},
	}
	srv, c := newCronTestServer(t, existing)

	if _, err := srv.DeleteSecurityScan(projectActorCtx(),
		&platform.DeleteSecurityScanRequest{Namespace: ns, Name: "doomed"}); err != nil {
		t.Fatalf("DeleteSecurityScan() error = %v", err)
	}
	err := c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: "doomed"}, &triggersv1alpha1.SecurityScan{})
	if !apierrors.IsNotFound(err) {
		t.Fatalf("scan still exists, err = %v", err)
	}

	_, err = srv.DeleteSecurityScan(projectActorCtx(), &platform.DeleteSecurityScanRequest{Namespace: ns, Name: "doomed"})
	if connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("DeleteSecurityScan(missing) error = %v, want NotFound", err)
	}
}

func TestListAndGetSecurityScanConfigsExposeSpecAndStatus(t *testing.T) {
	lastScan := metav1.NewTime(time.Date(2026, 2, 1, 3, 0, 0, 0, time.UTC))
	next := metav1.NewTime(time.Date(2026, 2, 2, 3, 0, 0, 0, time.UTC))
	existing := &triggersv1alpha1.SecurityScan{
		ObjectMeta: metav1.ObjectMeta{Name: "reader", Namespace: "default"},
		Spec: triggersv1alpha1.SecurityScanSpec{
			RepoURL:  "https://github.com/example/app.git",
			Schedule: "0 3 * * *",
			Defaults: triggersv1alpha1.AgentRunDefaults{Provider: triggersv1alpha1.ProviderAnthropic},
		},
		Status: triggersv1alpha1.SecurityScanStatus{
			Phase:            "Scheduled",
			LastRunName:      "reader-run-3",
			LastScanTime:     &lastScan,
			NextScheduleTime: &next,
			RunsCreated:      3,
			LastError:        "boom",
			Findings:         &triggersv1alpha1.SecurityScanFindingCounts{Total: 4, Open: 2, Critical: 1, High: 1},
			Conditions: []metav1.Condition{
				{Type: triggersv1alpha1.ConditionSecurityScanReady, Status: metav1.ConditionTrue, Reason: "Ready"},
			},
		},
	}
	srv, _ := newCronTestServer(t, existing)

	got, err := srv.GetSecurityScanConfig(context.Background(),
		&platform.GetSecurityScanConfigRequest{Namespace: "default", Name: "reader"})
	if err != nil {
		t.Fatalf("GetSecurityScanConfig() error = %v", err)
	}
	if got.GetSpec().GetRepoUrl() != "https://github.com/example/app.git" || got.GetSpec().GetSchedule() != "0 3 * * *" {
		t.Fatalf("spec = %+v", got.GetSpec())
	}
	if got.Phase != "Scheduled" || got.LastRunName != "reader-run-3" || got.RunsCreated != 3 ||
		got.LastError != "boom" || got.ConditionReady != string(metav1.ConditionTrue) {
		t.Fatalf("status = %+v", got)
	}
	if got.LastScanTimeUnix != lastScan.Unix() || got.NextScheduleTimeUnix != next.Unix() {
		t.Fatalf("times = %d/%d", got.LastScanTimeUnix, got.NextScheduleTimeUnix)
	}
	if got.FindingCounts["total"] != 4 || got.FindingCounts["open"] != 2 || got.FindingCounts["critical"] != 1 {
		t.Fatalf("FindingCounts = %+v", got.FindingCounts)
	}

	list, err := srv.ListSecurityScanConfigs(context.Background(), &platform.ListSecurityScanConfigsRequest{Namespace: "default"})
	if err != nil {
		t.Fatalf("ListSecurityScanConfigs() error = %v", err)
	}
	if len(list.Configs) != 1 || list.Configs[0].Name != "reader" || list.Configs[0].GetSpec() == nil {
		t.Fatalf("ListSecurityScanConfigs = %+v", list.Configs)
	}
}

func TestRunSecurityScanNowStampsAnnotationToken(t *testing.T) {
	ns := testUserNS()
	existing := &triggersv1alpha1.SecurityScan{
		ObjectMeta: metav1.ObjectMeta{Name: "nightly", Namespace: ns},
		Spec:       triggersv1alpha1.SecurityScanSpec{RepoURL: "https://github.com/example/app.git"},
	}
	srv, c := newCronTestServer(t, existing)
	ms := newMockStateStore()
	srv.stateStore = ms
	if err := ms.SetResourceOwner(context.Background(), securityScanResourceType, "nightly", ns, testProjectSubject); err != nil {
		t.Fatalf("SetResourceOwner: %v", err)
	}

	resp, err := srv.RunSecurityScanNow(projectActorCtx(),
		&platform.RunSecurityScanNowRequest{Namespace: ns, Name: "nightly"})
	if err != nil {
		t.Fatalf("RunSecurityScanNow() error = %v", err)
	}
	if resp.Namespace != ns || resp.Name != "nightly" {
		t.Fatalf("resp = %s/%s, want %s/nightly", resp.Namespace, resp.Name, ns)
	}

	cr := &triggersv1alpha1.SecurityScan{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: "nightly"}, cr); err != nil {
		t.Fatalf("Get(SecurityScan) error = %v", err)
	}
	first := cr.Annotations[triggersv1alpha1.SecurityScanRunNowAnnotation]
	if first == "" {
		t.Fatalf("run-now annotation not set: %#v", cr.Annotations)
	}

	// A later request stamps a fresh token; the spec is untouched.
	time.Sleep(time.Millisecond)
	if _, err := srv.RunSecurityScanNow(projectActorCtx(),
		&platform.RunSecurityScanNowRequest{Namespace: ns, Name: "nightly"}); err != nil {
		t.Fatalf("second RunSecurityScanNow() error = %v", err)
	}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: "nightly"}, cr); err != nil {
		t.Fatalf("Get(SecurityScan) error = %v", err)
	}
	if second := cr.Annotations[triggersv1alpha1.SecurityScanRunNowAnnotation]; second == "" || second == first {
		t.Fatalf("second token = %q, want a fresh token different from %q", second, first)
	}
	if cr.Spec.RepoURL != "https://github.com/example/app.git" || cr.Spec.Suspend {
		t.Fatalf("spec was modified: %+v", cr.Spec)
	}
}

func TestRunSecurityScanNowDeniedForStranger(t *testing.T) {
	existing := &triggersv1alpha1.SecurityScan{
		ObjectMeta: metav1.ObjectMeta{Name: "owned", Namespace: "default"},
		Spec:       triggersv1alpha1.SecurityScanSpec{RepoURL: "https://github.com/example/app.git"},
	}
	srv, c := newCronTestServer(t, existing)
	ms := newMockStateStore()
	srv.stateStore = ms
	if err := ms.SetResourceOwner(context.Background(), securityScanResourceType, "owned", "default", "alice"); err != nil {
		t.Fatalf("SetResourceOwner: %v", err)
	}

	_, err := srv.RunSecurityScanNow(actorContext("mallory", "member", "", ""),
		&platform.RunSecurityScanNowRequest{Namespace: "default", Name: "owned"})
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("RunSecurityScanNow by stranger: want PermissionDenied, got %v", err)
	}
	cr := &triggersv1alpha1.SecurityScan{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: "owned"}, cr); err != nil {
		t.Fatalf("Get(SecurityScan) error = %v", err)
	}
	if cr.Annotations[triggersv1alpha1.SecurityScanRunNowAnnotation] != "" {
		t.Fatalf("annotation stamped despite denial: %#v", cr.Annotations)
	}
}

func TestRunSecurityScanNowNotFound(t *testing.T) {
	srv, _ := newCronTestServer(t)
	_, err := srv.RunSecurityScanNow(projectActorCtx(),
		&platform.RunSecurityScanNowRequest{Namespace: "default", Name: "missing"})
	if connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("RunSecurityScanNow() error = %v, want NotFound", err)
	}
}

func TestRunSecurityScanNowRejectsSuspendedScan(t *testing.T) {
	ns := testUserNS()
	existing := &triggersv1alpha1.SecurityScan{
		ObjectMeta: metav1.ObjectMeta{Name: "paused", Namespace: ns},
		Spec: triggersv1alpha1.SecurityScanSpec{
			RepoURL: "https://github.com/example/app.git",
			Suspend: true,
		},
	}
	srv, c := newCronTestServer(t, existing)

	_, err := srv.RunSecurityScanNow(projectActorCtx(),
		&platform.RunSecurityScanNowRequest{Namespace: ns, Name: "paused"})
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("RunSecurityScanNow(suspended) error = %v, want FailedPrecondition", err)
	}
	cr := &triggersv1alpha1.SecurityScan{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: "paused"}, cr); err != nil {
		t.Fatalf("Get(SecurityScan) error = %v", err)
	}
	if cr.Annotations[triggersv1alpha1.SecurityScanRunNowAnnotation] != "" {
		t.Fatalf("annotation stamped on suspended scan: %#v", cr.Annotations)
	}
}

func TestRunSecurityScanNowValidatesRequest(t *testing.T) {
	srv, _ := newCronTestServer(t)
	_, err := srv.RunSecurityScanNow(projectActorCtx(), &platform.RunSecurityScanNowRequest{Namespace: "", Name: ""})
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("RunSecurityScanNow() error = %v, want InvalidArgument", err)
	}
}
