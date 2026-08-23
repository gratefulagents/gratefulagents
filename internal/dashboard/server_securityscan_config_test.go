package dashboard

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/store"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
)

func TestSecurityScanSpecAcceptsExactlyOneWebsiteTarget(t *testing.T) {
	t.Parallel()

	spec, _, _, err := securityScanSpecFromRequest(&platform.SecurityScanConfigSpec{TargetUrl: "https://app.example.test"})
	if err != nil {
		t.Fatalf("securityScanSpecFromRequest() error = %v", err)
	}
	if spec.TargetURL != "https://app.example.test" || spec.RepoURL != "" {
		t.Fatalf("website target = %+v", spec)
	}
	bare, _, _, err := securityScanSpecFromRequest(&platform.SecurityScanConfigSpec{TargetUrl: "example.com"})
	if err != nil || bare.TargetURL != "https://example.com" {
		t.Fatalf("bare-domain target = %+v, error = %v", bare, err)
	}

	for _, input := range []*platform.SecurityScanConfigSpec{
		{},
		{RepoUrl: "https://github.com/acme/app", TargetUrl: "https://app.example.test"},
		{TargetUrl: "file:///etc/passwd"},
		{TargetUrl: "https://user:secret@example.com"},
		{TargetUrl: "https://example.com/,https://other.example"},
	} {
		if _, _, _, err := securityScanSpecFromRequest(input); err == nil {
			t.Fatalf("securityScanSpecFromRequest(%+v) succeeded, want error", input)
		}
	}
}

func fullSecurityScanSpec() *platform.SecurityScanConfigSpec {
	taskRetries := int32(2)
	execRetries := int32(3)
	return &platform.SecurityScanConfigSpec{
		RepoUrl:         "https://github.com/example/payments.git",
		BaseBranch:      "release",
		Revision:        "abc123",
		AdditionalRepos: []string{"https://github.com/example/lib.git"},
		Scope: &platform.SecurityScanScopeConfig{
			Focus:                    "payment flows",
			IncludePaths:             []string{"internal/**"},
			ExcludePaths:             []string{"vendor/**"},
			Languages:                []string{"go"},
			AuthorizedNetworkTargets: []string{" staging.example.test ", "", "192.0.2.0/24"},
		},
		Workflow: []*platform.SecurityScanTaskConfig{
			{Name: "injection", Objective: "hunt injections", Category: "injection",
				OutputSchema: `{"type":"array","items":{"type":"object"}}`},
			{Name: "triage", Objective: "triage findings", Role: "finding-triager", Model: "gpt-5.2",
				DependsOn: []string{"injection"}, MaxRetries: &taskRetries, Timeout: "45m", MaxTurns: 30,
				MaxCostUsd: "2.50", ForEach: "injection", TargetRuns: 5,
				Tools: &platform.SecurityScanTaskTools{Allowed: []string{"grep", "read_file"}, Denied: []string{"web_fetch"}}},
		},
		Parallelism: 8,
		Execution: &platform.SecurityScanExecutionConfig{
			Mode:           "deterministic",
			TaskMaxRetries: &execRetries,
			RetryBackoff:   "45s",
		},
		ParameterValues:    map[string]string{"target_env": "staging"},
		SecurityProgramRef: "acme-bounty",
		SeverityRankers: []*platform.SecurityRankerConfig{
			{Name: "payments", Rules: "auth bypass is always critical"},
		},
		PostScripts: []*platform.SecurityPostScriptConfig{
			{Name: "poc", Prompt: "write a proof of concept", RunOn: "high-and-above"},
		},
		Dedupe:            &platform.SecurityScanDedupeConfig{Enabled: true, SimilarityThresholdPermille: 900},
		MinSeverity:       "medium",
		FailOnSeverity:    "high",
		ManualOnly:        true,
		Schedule:          "0 3 * * *",
		TimeZone:          "UTC",
		Suspend:           false,
		ConcurrencyPolicy: "Allow",
		Defaults:          fullCronDefaults(),
		MaxRuntime:        "2h",
		Budgets: &platform.SecurityScanBudgetsConfig{
			MaxModelJobs:      12,
			MaxCostUsd:        "3.75",
			MaxTokens:         250000,
			MaxRuntime:        "90m",
			MaxValidationJobs: 4,
		},
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
	if ps.Budgets == nil || ps.Budgets.MaxCostUsd != "3.75" || ps.Budgets.MaxRuntime != "1h30m0s" {
		t.Fatalf("proto budgets = %+v", ps.Budgets)
	}
	if ps.Execution == nil || ps.Execution.Mode != "deterministic" ||
		ps.Execution.TaskMaxRetries == nil || *ps.Execution.TaskMaxRetries != 3 || ps.Execution.RetryBackoff != "45s" {
		t.Fatalf("proto execution = %+v", ps.Execution)
	}
	if ps.ParameterValues["target_env"] != "staging" {
		t.Fatalf("proto parameter values = %+v", ps.ParameterValues)
	}
	if ps.SecurityProgramRef != "acme-bounty" {
		t.Fatalf("proto security_program_ref = %q", ps.SecurityProgramRef)
	}
	if !ps.ManualOnly {
		t.Fatalf("proto manual_only = false, want true")
	}
	pt := ps.Workflow[1]
	if pt.MaxRetries == nil || *pt.MaxRetries != 2 || pt.Timeout != "45m0s" || pt.MaxTurns != 30 ||
		pt.MaxCostUsd != "2.50" || pt.ForEach != "injection" || pt.TargetRuns != 5 ||
		pt.Tools == nil || len(pt.Tools.Allowed) != 2 || len(pt.Tools.Denied) != 1 {
		t.Fatalf("proto workflow[1] = %+v", pt)
	}
}

func assertFullScanSpec(t *testing.T, spec triggersv1alpha1.SecurityScanSpec) {
	t.Helper()
	if spec.RepoURL != "https://github.com/example/payments.git" || spec.BaseBranch != "release" ||
		spec.Revision != "abc123" || spec.Schedule != "0 3 * * *" || spec.TimeZone != "UTC" {
		t.Fatalf("spec = %+v", spec)
	}
	if !spec.ManualOnly {
		t.Fatalf("ManualOnly = false, want true")
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
	if spec.Budgets == nil || spec.Budgets.MaxModelJobs != 12 || spec.Budgets.MaxCostUSD != "3.75" ||
		spec.Budgets.MaxTokens != 250000 || spec.Budgets.MaxRuntime.Duration != 90*time.Minute ||
		spec.Budgets.MaxValidationJobs != 4 {
		t.Fatalf("Budgets = %+v", spec.Budgets)
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
	if !slices.Equal(spec.Scope.AuthorizedNetworkTargets, []string{"staging.example.test", "192.0.2.0/24"}) {
		t.Fatalf("Scope.AuthorizedNetworkTargets = %#v", spec.Scope.AuthorizedNetworkTargets)
	}
	if len(spec.Workflow) != 2 || spec.Workflow[0].Name != "injection" ||
		spec.Workflow[1].Role != "finding-triager" || spec.Workflow[1].DependsOn[0] != "injection" {
		t.Fatalf("Workflow = %+v", spec.Workflow)
	}
	if spec.Workflow[0].OutputSchema == "" {
		t.Fatalf("Workflow[0].OutputSchema empty")
	}
	task := spec.Workflow[1]
	if task.MaxRetries == nil || *task.MaxRetries != 2 || task.Timeout.Duration != 45*time.Minute ||
		task.MaxTurns != 30 || task.MaxCostUSD != "2.50" || task.ForEach != "injection" || task.TargetRuns != 5 {
		t.Fatalf("Workflow[1] execution fields = %+v", task)
	}
	if task.Tools == nil || len(task.Tools.Allowed) != 2 || task.Tools.Allowed[0] != "grep" ||
		len(task.Tools.Denied) != 1 || task.Tools.Denied[0] != "web_fetch" {
		t.Fatalf("Workflow[1].Tools = %+v", task.Tools)
	}
	if spec.Execution == nil || spec.Execution.Mode != "deterministic" ||
		spec.Execution.TaskMaxRetries == nil || *spec.Execution.TaskMaxRetries != 3 ||
		spec.Execution.RetryBackoff.Duration != 45*time.Second {
		t.Fatalf("Execution = %+v", spec.Execution)
	}
	if len(spec.ParameterValues) != 1 || spec.ParameterValues["target_env"] != "staging" {
		t.Fatalf("ParameterValues = %+v", spec.ParameterValues)
	}
	if spec.SecurityProgramRef == nil || spec.SecurityProgramRef.Name != "acme-bounty" {
		t.Fatalf("SecurityProgramRef = %+v", spec.SecurityProgramRef)
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

func TestSecurityScanScopeAuthorizedNetworkTargetsRoundTrip(t *testing.T) {
	scope := securityScanScopeFromProto(&platform.SecurityScanScopeConfig{
		AuthorizedNetworkTargets: []string{" api.example.test:8443 ", "  ", "10.0.0.0/8"},
	})
	if scope == nil {
		t.Fatalf("securityScanScopeFromProto() = nil, want scope carrying authorized targets")
	}
	want := []string{"api.example.test:8443", "10.0.0.0/8"}
	if !slices.Equal(scope.AuthorizedNetworkTargets, want) {
		t.Fatalf("AuthorizedNetworkTargets = %#v, want %#v", scope.AuthorizedNetworkTargets, want)
	}

	pb := securityScanSpecToProto(&triggersv1alpha1.SecurityScanSpec{Scope: scope})
	if !slices.Equal(pb.GetScope().GetAuthorizedNetworkTargets(), want) {
		t.Fatalf("proto AuthorizedNetworkTargets = %#v, want %#v", pb.GetScope().GetAuthorizedNetworkTargets(), want)
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

func TestCreateSecurityScanDockerInDockerRequiresAdmin(t *testing.T) {
	enabled := true
	spec := &platform.SecurityScanConfigSpec{
		RepoUrl:  "https://github.com/example/app.git",
		Defaults: &platform.AgentRunDefaults{DockerInDocker: &enabled},
	}
	srv, c := newCronTestServer(t)
	if _, err := srv.CreateSecurityScan(projectActorCtx(), &platform.CreateSecurityScanRequest{
		Name: "member-dind", Spec: spec,
	}); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("CreateSecurityScan(member DinD) error = %v, want PermissionDenied", err)
	}

	resp, err := srv.CreateSecurityScan(actorContext("admin-1", "admin", "", ""),
		&platform.CreateSecurityScanRequest{Name: "admin-dind", Spec: spec})
	if err != nil {
		t.Fatalf("CreateSecurityScan(admin DinD) error = %v", err)
	}
	if !resp.GetSpec().GetDefaults().GetDockerInDocker() {
		t.Fatal("response DockerInDocker = false, want true")
	}
	stored := &triggersv1alpha1.SecurityScan{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: resp.Namespace, Name: resp.Name}, stored); err != nil {
		t.Fatalf("Get(SecurityScan) error = %v", err)
	}
	if !stored.Spec.Defaults.DockerInDocker {
		t.Fatal("stored DockerInDocker = false, want true")
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
		{"bad task timeout", func() *platform.SecurityScanConfigSpec {
			s := base()
			s.Workflow = []*platform.SecurityScanTaskConfig{{Name: "task", Objective: "a", Timeout: "banana"}}
			return s
		}()},
		{"task max_retries too high", func() *platform.SecurityScanConfigSpec {
			s := base()
			retries := int32(11)
			s.Workflow = []*platform.SecurityScanTaskConfig{{Name: "task", Objective: "a", MaxRetries: &retries}}
			return s
		}()},
		{"bad task max_cost_usd", func() *platform.SecurityScanConfigSpec {
			s := base()
			s.Workflow = []*platform.SecurityScanTaskConfig{{Name: "task", Objective: "a", MaxCostUsd: "$5"}}
			return s
		}()},
		{"for_each without depends_on", func() *platform.SecurityScanConfigSpec {
			s := base()
			s.Workflow = []*platform.SecurityScanTaskConfig{
				{Name: "seed", Objective: "a", OutputSchema: `{"type":"array"}`},
				{Name: "task", Objective: "b", ForEach: "seed"},
			}
			return s
		}()},
		{"bad execution mode", func() *platform.SecurityScanConfigSpec {
			s := base()
			s.Execution = &platform.SecurityScanExecutionConfig{Mode: "chaotic"}
			return s
		}()},
		{"bad execution retry_backoff", func() *platform.SecurityScanConfigSpec {
			s := base()
			s.Execution = &platform.SecurityScanExecutionConfig{RetryBackoff: "banana"}
			return s
		}()},
		{"execution task_max_retries too high", func() *platform.SecurityScanConfigSpec {
			s := base()
			retries := int32(11)
			s.Execution = &platform.SecurityScanExecutionConfig{TaskMaxRetries: &retries}
			return s
		}()},
		{"bad parameter name", func() *platform.SecurityScanConfigSpec {
			s := base()
			s.ParameterValues = map[string]string{"not a name": "x"}
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
		{"bad budgets max_runtime", func() *platform.SecurityScanConfigSpec {
			s := base()
			s.Budgets = &platform.SecurityScanBudgetsConfig{MaxRuntime: "banana"}
			return s
		}()},
		{"bad budgets max_cost_usd", func() *platform.SecurityScanConfigSpec {
			s := base()
			s.Budgets = &platform.SecurityScanBudgetsConfig{MaxCostUsd: "$5"}
			return s
		}()},
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

func TestSecurityScanConfigProtoReportsSavedCredentialSource(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		secrets triggersv1alpha1.AgentRunSecrets
		want    bool
	}{
		{name: "no refs keeps historical saved default", want: true},
		{
			name: "materialized saved refs",
			secrets: triggersv1alpha1.AgentRunSecrets{
				OpenAIOAuthSecret: "usercred-openai",
				GithubToken:       "usercred-github",
			},
			want: true,
		},
		{
			name: "materialized saved provider key",
			secrets: triggersv1alpha1.AgentRunSecrets{ProviderKeys: []platformv1alpha1.ProviderKeyRef{
				{Provider: "openai", SecretName: "usercred-openai", SecretKey: "api-key"},
			}},
			want: true,
		},
		{
			name: "explicit ref",
			secrets: triggersv1alpha1.AgentRunSecrets{
				OpenAIOAuthSecret: "team-openai-oauth",
			},
			want: false,
		},
		{
			name: "explicit ref with reserved prefix",
			secrets: triggersv1alpha1.AgentRunSecrets{
				OpenAIOAuthSecret: "usercred-team-openai",
			},
			want: false,
		},
		{
			name: "mixed saved and explicit refs",
			secrets: triggersv1alpha1.AgentRunSecrets{
				OpenAIOAuthSecret: "usercred-openai",
				GithubToken:       "team-github-token",
			},
			want: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cr := &triggersv1alpha1.SecurityScan{
				Spec: triggersv1alpha1.SecurityScanSpec{
					Defaults: triggersv1alpha1.AgentRunDefaults{Secrets: tc.secrets},
				},
			}
			if got := securityScanConfigProto(cr).GetUseSavedCredentials(); got != tc.want {
				t.Fatalf("UseSavedCredentials = %v, want %v", got, tc.want)
			}
		})
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

func TestUpdateSecurityScanDockerInDockerAuthorization(t *testing.T) {
	ns := testUserNS()
	existing := &triggersv1alpha1.SecurityScan{
		ObjectMeta: metav1.ObjectMeta{Name: "dind-scan", Namespace: ns},
		Spec: triggersv1alpha1.SecurityScanSpec{
			RepoURL:  "https://github.com/example/app.git",
			Defaults: triggersv1alpha1.AgentRunDefaults{DockerInDocker: true},
		},
	}
	srv, c := newCronTestServer(t, existing)
	enabled := true
	if _, err := srv.UpdateSecurityScan(projectActorCtx(), &platform.UpdateSecurityScanRequest{
		Namespace: ns, Name: existing.Name,
		Spec: &platform.SecurityScanConfigSpec{RepoUrl: existing.Spec.RepoURL,
			Defaults: &platform.AgentRunDefaults{DockerInDocker: &enabled}},
	}); err != nil {
		t.Fatalf("UpdateSecurityScan(member unchanged DinD) error = %v", err)
	}

	disabled := false
	if _, err := srv.UpdateSecurityScan(projectActorCtx(), &platform.UpdateSecurityScanRequest{
		Namespace: ns, Name: existing.Name,
		Spec: &platform.SecurityScanConfigSpec{RepoUrl: existing.Spec.RepoURL,
			Defaults: &platform.AgentRunDefaults{DockerInDocker: &disabled}},
	}); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("UpdateSecurityScan(member changes DinD) error = %v, want PermissionDenied", err)
	}

	if _, err := srv.UpdateSecurityScan(actorContext("admin-1", "admin", "", ""),
		&platform.UpdateSecurityScanRequest{
			Namespace: ns, Name: existing.Name,
			Spec: &platform.SecurityScanConfigSpec{RepoUrl: existing.Spec.RepoURL,
				Defaults: &platform.AgentRunDefaults{DockerInDocker: &disabled}},
		}); err != nil {
		t.Fatalf("UpdateSecurityScan(admin changes DinD) error = %v", err)
	}
	stored := &triggersv1alpha1.SecurityScan{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: existing.Name}, stored); err != nil {
		t.Fatalf("Get(SecurityScan) error = %v", err)
	}
	if stored.Spec.Defaults.DockerInDocker {
		t.Fatal("stored DockerInDocker = true, want false after admin update")
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
			Budget: &triggersv1alpha1.SecurityScanBudgetStatus{
				Effective: &triggersv1alpha1.SecurityScanBudgets{MaxCostUSD: "5"},
				Exceeded:  true,
				Message:   "model cost exceeds budgets.maxCostUSD",
			},
			Retention: &triggersv1alpha1.SecurityScanRetentionStatus{
				LastSweepTime:  &lastScan,
				FindingsPurged: 7,
				PoCRedacted:    2,
				MoreWork:       true,
				LastError:      "sweep hiccup",
			},
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
	if !got.BudgetExceeded || got.BudgetMessage == "" || got.GetEffectiveBudgets().GetMaxCostUsd() != "5" {
		t.Fatalf("budget status = exceeded=%v message=%q effective=%+v", got.BudgetExceeded, got.BudgetMessage, got.GetEffectiveBudgets())
	}
	if got.GetRetention().GetLastSweepTimeUnix() != lastScan.Unix() ||
		got.GetRetention().GetFindingsPurged() != 7 || got.GetRetention().GetPocRedacted() != 2 ||
		!got.GetRetention().GetMoreWork() || got.GetRetention().GetLastError() != "sweep hiccup" {
		t.Fatalf("retention status = %+v", got.GetRetention())
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

func TestCreateSecurityScanEventTriggersChecksAndNotificationsRoundTrip(t *testing.T) {
	srv, c := newCronTestServer(t)
	srv.stateStore = newMockStateStore()
	ns := testUserNS()

	spec := fullSecurityScanSpec()
	spec.Triggers = &platform.SecurityScanTriggersConfig{
		RepositoryRef: "widget-repo",
		OnPullRequest: true,
		OnPush:        true,
		Branches:      []string{"main", "release/*"},
		DiffScope:     true,
		AllowForks:    true,
	}
	spec.Checks = &platform.SecurityScanChecksConfig{
		Enabled:                 true,
		IncludeFindingSummaries: true,
		UploadSarif:             true,
	}
	spec.Notifications = []*platform.SecurityScanNotificationRuleConfig{{
		Name:                  "critical-alerts",
		MinSeverity:           "critical",
		NotifyOn:              "new",
		SlackWebhookSecretRef: "slack-webhook",
		GithubIssues:          true,
		LinearApiKeySecretRef: "linear-key",
		LinearTeamId:          "team-1",
	}}

	resp, err := srv.CreateSecurityScan(projectActorCtx(), &platform.CreateSecurityScanRequest{Name: "event-scan", Spec: spec})
	if err != nil {
		t.Fatalf("CreateSecurityScan() error = %v", err)
	}

	cr := &triggersv1alpha1.SecurityScan{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: "event-scan"}, cr); err != nil {
		t.Fatalf("Get(SecurityScan) error = %v", err)
	}
	tr := cr.Spec.Triggers
	if tr == nil || tr.RepositoryRef == nil || tr.RepositoryRef.Name != "widget-repo" ||
		!tr.OnPullRequest || !tr.OnPush || !tr.DiffScope || !tr.AllowForks || len(tr.Branches) != 2 {
		t.Fatalf("Triggers = %+v", tr)
	}
	if cr.Spec.Checks == nil || !cr.Spec.Checks.Enabled || !cr.Spec.Checks.IncludeFindingSummaries || !cr.Spec.Checks.UploadSARIF {
		t.Fatalf("Checks = %+v", cr.Spec.Checks)
	}
	if len(cr.Spec.Notifications) != 1 {
		t.Fatalf("Notifications = %+v", cr.Spec.Notifications)
	}
	rule := cr.Spec.Notifications[0]
	if rule.Name != "critical-alerts" || rule.MinSeverity != "critical" || rule.NotifyOn != "new" ||
		rule.Slack == nil || rule.Slack.WebhookSecretRef != "slack-webhook" ||
		rule.GitHubIssues == nil || rule.Linear == nil || rule.Linear.TeamID != "team-1" {
		t.Fatalf("rule = %+v", rule)
	}

	ps := resp.GetSpec()
	if ps.GetTriggers() == nil || ps.GetTriggers().GetRepositoryRef() != "widget-repo" ||
		ps.GetChecks() == nil || !ps.GetChecks().GetUploadSarif() ||
		len(ps.GetNotifications()) != 1 || ps.GetNotifications()[0].GetSlackWebhookSecretRef() != "slack-webhook" {
		t.Fatalf("proto spec round-trip = %+v", ps)
	}
}

func TestCreateSecurityScanRejectsInvalidEventConfig(t *testing.T) {
	srv, _ := newCronTestServer(t)
	srv.stateStore = newMockStateStore()

	cases := []struct {
		name   string
		mutate func(spec *platform.SecurityScanConfigSpec)
	}{
		{"triggers without repository ref", func(spec *platform.SecurityScanConfigSpec) {
			spec.Triggers = &platform.SecurityScanTriggersConfig{OnPullRequest: true}
		}},
		{"checks without triggers", func(spec *platform.SecurityScanConfigSpec) {
			spec.Checks = &platform.SecurityScanChecksConfig{Enabled: true}
		}},
		{"notification without channel", func(spec *platform.SecurityScanConfigSpec) {
			spec.Notifications = []*platform.SecurityScanNotificationRuleConfig{{Name: "r1"}}
		}},
		{"duplicate notification names", func(spec *platform.SecurityScanConfigSpec) {
			spec.Notifications = []*platform.SecurityScanNotificationRuleConfig{
				{Name: "r1", SlackWebhookSecretRef: "s"},
				{Name: "r1", SlackWebhookSecretRef: "s"},
			}
		}},
		{"linear key without team", func(spec *platform.SecurityScanConfigSpec) {
			spec.Notifications = []*platform.SecurityScanNotificationRuleConfig{{Name: "r1", LinearApiKeySecretRef: "k"}}
		}},
		{"invalid notify_on", func(spec *platform.SecurityScanConfigSpec) {
			spec.Notifications = []*platform.SecurityScanNotificationRuleConfig{{Name: "r1", SlackWebhookSecretRef: "s", NotifyOn: "always"}}
		}},
	}
	for _, tc := range cases {
		spec := fullSecurityScanSpec()
		tc.mutate(spec)
		_, err := srv.CreateSecurityScan(projectActorCtx(), &platform.CreateSecurityScanRequest{Name: "bad", Spec: spec})
		if err == nil {
			t.Errorf("%s: CreateSecurityScan() = nil error, want InvalidArgument", tc.name)
			continue
		}
		if connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Errorf("%s: code = %v, want InvalidArgument (%v)", tc.name, connect.CodeOf(err), err)
		}
	}
}

func TestRunSecurityScanNowMergesParameterValues(t *testing.T) {
	ns := testUserNS()
	existing := &triggersv1alpha1.SecurityScan{
		ObjectMeta: metav1.ObjectMeta{Name: "parameterized", Namespace: ns},
		Spec: triggersv1alpha1.SecurityScanSpec{
			RepoURL:         "https://github.com/example/app.git",
			ParameterValues: map[string]string{"target_env": "prod", "depth": "quick"},
		},
	}
	srv, c := newCronTestServer(t, existing)
	ms := newMockStateStore()
	srv.stateStore = ms
	if err := ms.SetResourceOwner(context.Background(), securityScanResourceType, "parameterized", ns, testProjectSubject); err != nil {
		t.Fatalf("SetResourceOwner: %v", err)
	}

	resp, err := srv.RunSecurityScanNow(projectActorCtx(), &platform.RunSecurityScanNowRequest{
		Namespace: ns, Name: "parameterized",
		ParameterValues: map[string]string{"target_env": "staging", "focus": "auth"},
	})
	if err != nil {
		t.Fatalf("RunSecurityScanNow() error = %v", err)
	}
	if resp.GetSpec().GetParameterValues()["target_env"] != "staging" {
		t.Fatalf("resp parameter values = %+v", resp.GetSpec().GetParameterValues())
	}

	cr := &triggersv1alpha1.SecurityScan{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: "parameterized"}, cr); err != nil {
		t.Fatalf("Get(SecurityScan) error = %v", err)
	}
	want := map[string]string{"target_env": "staging", "depth": "quick", "focus": "auth"}
	if len(cr.Spec.ParameterValues) != len(want) {
		t.Fatalf("ParameterValues = %+v, want %+v", cr.Spec.ParameterValues, want)
	}
	for k, v := range want {
		if cr.Spec.ParameterValues[k] != v {
			t.Fatalf("ParameterValues[%q] = %q, want %q", k, cr.Spec.ParameterValues[k], v)
		}
	}
	if cr.Annotations[triggersv1alpha1.SecurityScanRunNowAnnotation] == "" {
		t.Fatalf("run-now annotation missing: %#v", cr.Annotations)
	}

	// Invalid parameter names are rejected before anything is stamped.
	_, err = srv.RunSecurityScanNow(projectActorCtx(), &platform.RunSecurityScanNowRequest{
		Namespace: ns, Name: "parameterized",
		ParameterValues: map[string]string{"not a name": "x"},
	})
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("RunSecurityScanNow(bad key) error = %v, want InvalidArgument", err)
	}
}

func TestSecurityScanConfigSurfacesLastExecution(t *testing.T) {
	started := metav1.NewTime(time.Unix(1700000000, 0))
	finished := metav1.NewTime(time.Unix(1700000600, 0))
	nextRetry := metav1.NewTime(time.Unix(1700000900, 0))
	existing := &triggersv1alpha1.SecurityScan{
		ObjectMeta: metav1.ObjectMeta{Name: "deterministic", Namespace: "default"},
		Spec:       triggersv1alpha1.SecurityScanSpec{RepoURL: "https://github.com/example/app.git"},
		Status: triggersv1alpha1.SecurityScanStatus{
			LastExecution: &triggersv1alpha1.SecurityScanExecutionStatus{
				ID:                       "run-7",
				Mode:                     "deterministic",
				Phase:                    "Running",
				EffectiveParallelism:     4,
				EffectiveParallelismNote: "clamped by mode template",
				StartedAt:                &started,
				LastResumeToken:          "token-1",
				Tasks: []triggersv1alpha1.SecurityScanTaskExecutionStatus{
					{
						Name:          "triage",
						Instance:      1,
						State:         "Running",
						RunName:       "deterministic-triage-1",
						Attempts:      2,
						LastError:     "attempt 1 timed out",
						NextRetryTime: &nextRetry,
						StartedAt:     &started,
						RecordStart:   25,
						RecordEnd:     50,
						InputSHA256:   "chunk-sha",
						Retries: []triggersv1alpha1.SecurityScanTaskAttempt{{
							RunName:    "deterministic-triage-0",
							Reason:     "timeout",
							Class:      "retryable",
							StartedAt:  &started,
							FinishedAt: &finished,
						}},
					},
				},
				FanOuts: []triggersv1alpha1.SecurityScanFanOutExecutionStatus{{
					Name: "triage", SourceTask: "injection", SourceRunName: "deterministic-injection-0",
					Strategy: "chunk-v1", SourceOutputSHA256: "source-sha", RecordCount: 100, ChunkCount: 4,
				}},
			},
		},
	}
	srv, _ := newCronTestServer(t, existing)

	got, err := srv.GetSecurityScanConfig(context.Background(),
		&platform.GetSecurityScanConfigRequest{Namespace: "default", Name: "deterministic"})
	if err != nil {
		t.Fatalf("GetSecurityScanConfig() error = %v", err)
	}
	le := got.GetLastExecution()
	if le == nil || le.Id != "run-7" || le.Mode != "deterministic" || le.Phase != "Running" ||
		le.EffectiveParallelism != 4 || le.EffectiveParallelismNote != "clamped by mode template" ||
		le.StartedAtUnix != started.Unix() || le.CompletedAtUnix != 0 || le.LastResumeToken != "token-1" {
		t.Fatalf("last execution = %+v", le)
	}
	if len(le.Tasks) != 1 {
		t.Fatalf("tasks = %+v", le.Tasks)
	}
	task := le.Tasks[0]
	if task.Name != "triage" || task.Instance != 1 || task.State != "Running" ||
		task.RunName != "deterministic-triage-1" || task.Attempts != 2 ||
		task.LastError != "attempt 1 timed out" || task.NextRetryTimeUnix != nextRetry.Unix() ||
		task.StartedAtUnix != started.Unix() || task.FinishedAtUnix != 0 ||
		task.RecordStart != 25 || task.RecordEnd != 50 || task.InputSha256 != "chunk-sha" {
		t.Fatalf("task = %+v", task)
	}
	if len(le.FanOuts) != 1 || le.FanOuts[0].Name != "triage" ||
		le.FanOuts[0].SourceTask != "injection" || le.FanOuts[0].SourceRunName != "deterministic-injection-0" ||
		le.FanOuts[0].Strategy != "chunk-v1" || le.FanOuts[0].SourceOutputSha256 != "source-sha" ||
		le.FanOuts[0].RecordCount != 100 || le.FanOuts[0].ChunkCount != 4 {
		t.Fatalf("fan outs = %+v", le.FanOuts)
	}
	if len(task.Retries) != 1 || task.Retries[0].RunName != "deterministic-triage-0" ||
		task.Retries[0].Reason != "timeout" || task.Retries[0].Class != "retryable" ||
		task.Retries[0].StartedAtUnix != started.Unix() || task.Retries[0].FinishedAtUnix != finished.Unix() {
		t.Fatalf("retries = %+v", task.Retries)
	}

	list, err := srv.ListSecurityScanConfigs(context.Background(), &platform.ListSecurityScanConfigsRequest{Namespace: "default"})
	if err != nil {
		t.Fatalf("ListSecurityScanConfigs() error = %v", err)
	}
	if len(list.Configs) != 1 || list.Configs[0].GetLastExecution().GetId() != "run-7" {
		t.Fatalf("list last execution = %+v", list.Configs)
	}
}

func TestRunSecurityScanNowRejectsOversizedParameterValues(t *testing.T) {
	ns := testUserNS()
	existing := &triggersv1alpha1.SecurityScan{
		ObjectMeta: metav1.ObjectMeta{Name: "bounded", Namespace: ns},
		Spec:       triggersv1alpha1.SecurityScanSpec{RepoURL: "https://github.com/example/app.git"},
	}
	srv, _ := newCronTestServer(t, existing)
	ms := newMockStateStore()
	srv.stateStore = ms
	if err := ms.SetResourceOwner(context.Background(), securityScanResourceType, "bounded", ns, testProjectSubject); err != nil {
		t.Fatalf("SetResourceOwner: %v", err)
	}

	_, err := srv.RunSecurityScanNow(projectActorCtx(), &platform.RunSecurityScanNowRequest{
		Namespace: ns, Name: "bounded",
		ParameterValues: map[string]string{"big": strings.Repeat("v", 4097)},
	})
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("RunSecurityScanNow(oversized value) error = %v, want InvalidArgument", err)
	}

	values := map[string]string{}
	for i := range 16 {
		values[fmt.Sprintf("p%02d", i)] = strings.Repeat("v", 4096)
	}
	_, err = srv.RunSecurityScanNow(projectActorCtx(), &platform.RunSecurityScanNowRequest{
		Namespace: ns, Name: "bounded", ParameterValues: values,
	})
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("RunSecurityScanNow(oversized total) error = %v, want InvalidArgument", err)
	}

	// A value at exactly the per-value cap stays accepted.
	if _, err := srv.RunSecurityScanNow(projectActorCtx(), &platform.RunSecurityScanNowRequest{
		Namespace: ns, Name: "bounded",
		ParameterValues: map[string]string{"big": strings.Repeat("v", 4096)},
	}); err != nil {
		t.Fatalf("RunSecurityScanNow(at-cap value) error = %v", err)
	}
}

func TestRunSecurityScanNowWithParameterValuesRequiresCollaboratorAccess(t *testing.T) {
	ns := testUserNS()
	existing := &triggersv1alpha1.SecurityScan{
		ObjectMeta: metav1.ObjectMeta{Name: "shared", Namespace: ns},
		Spec:       triggersv1alpha1.SecurityScanSpec{RepoURL: "https://github.com/example/app.git"},
	}
	srv, _ := newCronTestServer(t, existing)
	ms := newCollaborationStateStore()
	srv.stateStore = ms
	if err := ms.SetResourceOwner(context.Background(), securityScanResourceType, "shared", ns, testProjectSubject); err != nil {
		t.Fatalf("SetResourceOwner: %v", err)
	}
	for subject, permission := range map[string]string{"victor": "viewer", "carl": "collaborator"} {
		if _, err := ms.ShareResource(context.Background(), &store.ResourceShare{
			ResourceType: securityScanResourceType, ResourceID: "shared", ResourceNamespace: ns,
			SharedWithUserID: subject, SharedByUserID: testProjectSubject, Permission: permission,
		}); err != nil {
			t.Fatalf("ShareResource(%s): %v", permission, err)
		}
	}

	req := &platform.RunSecurityScanNowRequest{
		Namespace: ns, Name: "shared",
		ParameterValues: map[string]string{"focus": "auth"},
	}
	// The merge persists into spec.parameterValues, so a viewer share (read
	// access) must not be enough.
	if _, err := srv.RunSecurityScanNow(actorContext("victor", "member", "", ""), req); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("RunSecurityScanNow(viewer + params) error = %v, want PermissionDenied", err)
	}
	if _, err := srv.RunSecurityScanNow(actorContext("carl", "member", "", ""), req); err != nil {
		t.Fatalf("RunSecurityScanNow(collaborator + params) error = %v", err)
	}
}

// failedDeterministicScan builds a SecurityScan whose last execution is a
// deterministic run in the given phase, for resume/output tests.
func failedDeterministicScan(ns, name, phase string) *triggersv1alpha1.SecurityScan {
	return &triggersv1alpha1.SecurityScan{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec:       triggersv1alpha1.SecurityScanSpec{RepoURL: "https://github.com/example/app.git"},
		Status: triggersv1alpha1.SecurityScanStatus{
			LastExecution: &triggersv1alpha1.SecurityScanExecutionStatus{
				ID:    "20260101-abc",
				Mode:  triggersv1alpha1.SecurityScanExecutionModeDeterministic,
				Phase: phase,
			},
		},
	}
}

func TestResumeSecurityScanStampsAnnotationToken(t *testing.T) {
	ns := testUserNS()
	existing := failedDeterministicScan(ns, "nightly", triggersv1alpha1.SecurityScanExecutionPhaseFailed)
	srv, c := newCronTestServer(t, existing)
	ms := newMockStateStore()
	srv.stateStore = ms
	if err := ms.SetResourceOwner(context.Background(), securityScanResourceType, "nightly", ns, testProjectSubject); err != nil {
		t.Fatalf("SetResourceOwner: %v", err)
	}

	resp, err := srv.ResumeSecurityScan(projectActorCtx(),
		&platform.ResumeSecurityScanRequest{Namespace: ns, Name: "nightly"})
	if err != nil {
		t.Fatalf("ResumeSecurityScan() error = %v", err)
	}
	if resp.Namespace != ns || resp.Name != "nightly" {
		t.Fatalf("resp = %s/%s, want %s/nightly", resp.Namespace, resp.Name, ns)
	}

	cr := &triggersv1alpha1.SecurityScan{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: "nightly"}, cr); err != nil {
		t.Fatalf("Get(SecurityScan) error = %v", err)
	}
	first := cr.Annotations[triggersv1alpha1.SecurityScanResumeAnnotation]
	if first == "" {
		t.Fatalf("resume annotation not set: %#v", cr.Annotations)
	}

	// A later request stamps a fresh token so the controller sees it as new.
	time.Sleep(time.Millisecond)
	if _, err := srv.ResumeSecurityScan(projectActorCtx(),
		&platform.ResumeSecurityScanRequest{Namespace: ns, Name: "nightly"}); err != nil {
		t.Fatalf("second ResumeSecurityScan() error = %v", err)
	}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: "nightly"}, cr); err != nil {
		t.Fatalf("Get(SecurityScan) error = %v", err)
	}
	if second := cr.Annotations[triggersv1alpha1.SecurityScanResumeAnnotation]; second == "" || second == first {
		t.Fatalf("second token = %q, want a fresh token different from %q", second, first)
	}
}

func TestResumeSecurityScanRequiresFailedDeterministicExecution(t *testing.T) {
	ns := testUserNS()
	cases := []struct {
		name string
		scan *triggersv1alpha1.SecurityScan
	}{
		{
			name: "no execution",
			scan: &triggersv1alpha1.SecurityScan{
				ObjectMeta: metav1.ObjectMeta{Name: "nightly", Namespace: ns},
				Spec:       triggersv1alpha1.SecurityScanSpec{RepoURL: "https://github.com/example/app.git"},
			},
		},
		{
			name: "running execution",
			scan: failedDeterministicScan(ns, "nightly", triggersv1alpha1.SecurityScanExecutionPhaseRunning),
		},
		{
			name: "succeeded execution",
			scan: failedDeterministicScan(ns, "nightly", triggersv1alpha1.SecurityScanExecutionPhaseSucceeded),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, c := newCronTestServer(t, tc.scan)
			ms := newMockStateStore()
			srv.stateStore = ms
			if err := ms.SetResourceOwner(context.Background(), securityScanResourceType, "nightly", ns, testProjectSubject); err != nil {
				t.Fatalf("SetResourceOwner: %v", err)
			}
			_, err := srv.ResumeSecurityScan(projectActorCtx(),
				&platform.ResumeSecurityScanRequest{Namespace: ns, Name: "nightly"})
			if connect.CodeOf(err) != connect.CodeFailedPrecondition {
				t.Fatalf("ResumeSecurityScan() error = %v, want FailedPrecondition", err)
			}
			cr := &triggersv1alpha1.SecurityScan{}
			if err := c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: "nightly"}, cr); err != nil {
				t.Fatalf("Get(SecurityScan) error = %v", err)
			}
			if cr.Annotations[triggersv1alpha1.SecurityScanResumeAnnotation] != "" {
				t.Fatalf("annotation stamped despite precondition failure: %#v", cr.Annotations)
			}
		})
	}
}

func TestResumeSecurityScanDeniedForStranger(t *testing.T) {
	existing := failedDeterministicScan("default", "owned", triggersv1alpha1.SecurityScanExecutionPhaseFailed)
	srv, c := newCronTestServer(t, existing)
	ms := newMockStateStore()
	srv.stateStore = ms
	if err := ms.SetResourceOwner(context.Background(), securityScanResourceType, "owned", "default", "alice"); err != nil {
		t.Fatalf("SetResourceOwner: %v", err)
	}

	_, err := srv.ResumeSecurityScan(actorContext("mallory", "member", "", ""),
		&platform.ResumeSecurityScanRequest{Namespace: "default", Name: "owned"})
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("ResumeSecurityScan by stranger: want PermissionDenied, got %v", err)
	}
	cr := &triggersv1alpha1.SecurityScan{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: "owned"}, cr); err != nil {
		t.Fatalf("Get(SecurityScan) error = %v", err)
	}
	if cr.Annotations[triggersv1alpha1.SecurityScanResumeAnnotation] != "" {
		t.Fatalf("annotation stamped despite denial: %#v", cr.Annotations)
	}
}

func TestResumeSecurityScanValidatesRequest(t *testing.T) {
	srv, _ := newCronTestServer(t)
	for _, req := range []*platform.ResumeSecurityScanRequest{
		{Namespace: "", Name: "nightly"},
		{Namespace: "default", Name: ""},
	} {
		if _, err := srv.ResumeSecurityScan(projectActorCtx(), req); connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Fatalf("ResumeSecurityScan(%v) error = %v, want InvalidArgument", req, err)
		}
	}
}

func TestGetSecurityScanConfigPopulatesTaskOutputs(t *testing.T) {
	ns := testUserNS()
	scan := &triggersv1alpha1.SecurityScan{
		ObjectMeta: metav1.ObjectMeta{Name: "nightly", Namespace: ns},
		Spec:       triggersv1alpha1.SecurityScanSpec{RepoURL: "https://github.com/example/app.git"},
		Status: triggersv1alpha1.SecurityScanStatus{
			LastExecution: &triggersv1alpha1.SecurityScanExecutionStatus{
				ID:    "20260101-abc",
				Mode:  triggersv1alpha1.SecurityScanExecutionModeDeterministic,
				Phase: triggersv1alpha1.SecurityScanExecutionPhaseRunning,
				Tasks: []triggersv1alpha1.SecurityScanTaskExecutionStatus{
					{Name: "recon", State: triggersv1alpha1.SecurityScanTaskStateSucceeded, RunName: "nightly-recon-1"},
					{Name: "hunt", State: triggersv1alpha1.SecurityScanTaskStateFailed, RunName: "nightly-hunt-1"},
					{Name: "gone", State: triggersv1alpha1.SecurityScanTaskStateSucceeded, RunName: "nightly-gone-1"},
					{Name: "triage", State: triggersv1alpha1.SecurityScanTaskStateBlocked},
					{Name: "solana", State: triggersv1alpha1.SecurityScanTaskStateSucceeded, StructuredOutput: `{"area":"solana","status":"skipped"}`},
				},
			},
		},
	}
	reconRun := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "nightly-recon-1", Namespace: ns},
		Status:     platformv1alpha1.AgentRunStatus{StructuredOutput: `{"targets":["/api"]}`},
	}
	huntRun := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "nightly-hunt-1", Namespace: ns},
		Status:     platformv1alpha1.AgentRunStatus{StructuredOutput: `{"ignored":true}`},
	}
	srv, _ := newCronTestServer(t, scan, reconRun, huntRun)
	ms := newMockStateStore()
	srv.stateStore = ms
	if err := ms.SetResourceOwner(context.Background(), securityScanResourceType, "nightly", ns, testProjectSubject); err != nil {
		t.Fatalf("SetResourceOwner: %v", err)
	}

	resp, err := srv.GetSecurityScanConfig(projectActorCtx(),
		&platform.GetSecurityScanConfigRequest{Namespace: ns, Name: "nightly"})
	if err != nil {
		t.Fatalf("GetSecurityScanConfig() error = %v", err)
	}
	byName := map[string]*platform.SecurityScanTaskExecutionState{}
	for _, task := range resp.LastExecution.Tasks {
		byName[task.Name] = task
	}
	if got := byName["recon"].OutputJson; got != `{"targets":["/api"]}` {
		t.Fatalf("recon output = %q, want the run's structured output", got)
	}
	// Failed tasks never expose output, even when the run recorded one.
	if got := byName["hunt"].OutputJson; got != "" {
		t.Fatalf("hunt output = %q, want empty for a failed task", got)
	}
	// A deleted run and a never-started task are quietly empty.
	if got := byName["gone"].OutputJson; got != "" {
		t.Fatalf("gone output = %q, want empty for a deleted run", got)
	}
	if got := byName["triage"].OutputJson; got != "" {
		t.Fatalf("triage output = %q, want empty without a run", got)
	}
	if got := byName["solana"].OutputJson; got != `{"area":"solana","status":"skipped"}` {
		t.Fatalf("solana output = %q, want controller-published skip output", got)
	}

	// The list path stays lean: no outputs are populated.
	listResp, err := srv.ListSecurityScanConfigs(projectActorCtx(),
		&platform.ListSecurityScanConfigsRequest{Namespace: ns})
	if err != nil {
		t.Fatalf("ListSecurityScanConfigs() error = %v", err)
	}
	for _, cfg := range listResp.Configs {
		for _, task := range cfg.LastExecution.GetTasks() {
			if task.OutputJson != "" {
				t.Fatalf("list response carries output for %s", task.Name)
			}
		}
	}
}

func TestSecurityScanExecutionStateProtoCarriesPostScriptJobsAndCoverageGaps(t *testing.T) {
	started := metav1.NewTime(time.Unix(1767225600, 0))
	finished := metav1.NewTime(time.Unix(1767226600, 0))
	exec := &triggersv1alpha1.SecurityScanExecutionStatus{
		ID:                      "20260101-abc",
		Mode:                    triggersv1alpha1.SecurityScanExecutionModeDeterministic,
		Phase:                   triggersv1alpha1.SecurityScanExecutionPhaseRunning,
		PostScriptsMaterialized: true,
		CoverageGaps:            []string{"forEach inventory truncated to 50 instances"},
		Plan: []triggersv1alpha1.SecurityScanExecutionPlanNode{
			{Name: "recon"},
			{Name: "hunt", DependsOn: []string{"recon"}, ForEach: "recon"},
		},
		PostScriptJobs: []triggersv1alpha1.SecurityScanPostScriptJobStatus{
			{
				Script:      "false-positive-check",
				Scripts:     []string{"false-positive-check", "poc-builder"},
				Order:       1,
				FindingID:   "22222222-2222-2222-2222-222222222222",
				Fingerprint: "sqli-users-list",
				State:       triggersv1alpha1.SecurityScanPostScriptStateSucceeded,
				RunName:     "nightly-ps-1",
				Attempts:    2,
				Result:      "confirmed",
				LastError:   "first attempt timed out",
				StartedAt:   &started,
				FinishedAt:  &finished,
			},
		},
	}

	pb := securityScanExecutionStateProto(exec)
	if !pb.PostScriptsMaterialized {
		t.Fatalf("PostScriptsMaterialized not carried")
	}
	if len(pb.CoverageGaps) != 1 || pb.CoverageGaps[0] != "forEach inventory truncated to 50 instances" {
		t.Fatalf("CoverageGaps = %v", pb.CoverageGaps)
	}
	if len(pb.Plan) != 2 || pb.Plan[0].Name != "recon" ||
		pb.Plan[1].Name != "hunt" || len(pb.Plan[1].DependsOn) != 1 ||
		pb.Plan[1].DependsOn[0] != "recon" || pb.Plan[1].ForEach != "recon" {
		t.Fatalf("Plan not fully converted: %+v", pb.Plan)
	}
	if len(pb.PostScriptJobs) != 1 {
		t.Fatalf("PostScriptJobs = %v", pb.PostScriptJobs)
	}
	job := pb.PostScriptJobs[0]
	if job.Script != "false-positive-check" || !slices.Equal(job.Scripts, []string{"false-positive-check", "poc-builder"}) || job.Order != 1 ||
		job.FindingId != "22222222-2222-2222-2222-222222222222" || job.Fingerprint != "sqli-users-list" ||
		job.State != "Succeeded" || job.RunName != "nightly-ps-1" || job.Attempts != 2 ||
		job.Result != "confirmed" || job.LastError != "first attempt timed out" ||
		job.StartedAtUnix != 1767225600 || job.FinishedAtUnix != 1767226600 {
		t.Fatalf("job not fully converted: %+v", job)
	}
}
