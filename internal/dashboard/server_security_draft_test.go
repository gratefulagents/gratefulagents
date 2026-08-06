package dashboard

import (
	"context"
	"strings"
	"testing"

	"connectrpc.com/connect"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
)

// newSecurityDraftTestServer builds a server whose test actor has a saved
// Anthropic API key, so provider credential resolution succeeds.
func newSecurityDraftTestServer(t *testing.T, objs ...client.Object) (*Server, client.Client, *mockStateStore) {
	t.Helper()
	scheme := testProjectScheme(t)
	objs = append(objs, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: userCredentialSecretName(triggersv1alpha1.ProviderAnthropic), Namespace: testUserNS()},
		Data:       map[string][]byte{userCredAPIKeyKey: []byte("key")},
	})
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
	ms := newMockStateStore()
	return &Server{k8sClient: c, scheme: scheme, stateStore: ms}, c, ms
}

func TestGenerateSecurityDraftCreatesBoundedRepolessRun(t *testing.T) {
	srv, c, ms := newSecurityDraftTestServer(t)
	ctx := projectActorCtx()
	ns := testUserNS()

	resp, err := srv.GenerateSecurityDraft(ctx, &platform.GenerateSecurityDraftRequest{
		Kind:        platform.SecurityDraftKind_SECURITY_DRAFT_KIND_WORKFLOW,
		RequestText: "Hunt SQL injection and authz bypasses in the payments API.",
		Model:       "anthropic/claude-sonnet-4-6",
	})
	if err != nil {
		t.Fatalf("GenerateSecurityDraft() error = %v", err)
	}
	if resp.Namespace != ns || !strings.HasPrefix(resp.RunName, "security-draft-") {
		t.Fatalf("resp = %+v", resp)
	}

	run := &platformv1alpha1.AgentRun{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: resp.RunName}, run); err != nil {
		t.Fatalf("Get(AgentRun) error = %v", err)
	}
	// The run must be repo-less and carry no repository or GitHub material.
	if run.Spec.Repository.URL != "" || run.Spec.Repository.BaseBranch != "" || len(run.Spec.Repository.AdditionalRepos) != 0 {
		t.Errorf("repository = %+v, want empty (repoless)", run.Spec.Repository)
	}
	if run.Annotations["platform.gratefulagents.dev/repoless"] != "true" {
		t.Error("run must be annotated repoless")
	}
	if run.Annotations[securityDraftKindAnnotation] != securityDraftKindWorkflow {
		t.Errorf("draft-kind annotation = %q", run.Annotations[securityDraftKindAnnotation])
	}
	if run.Spec.ModeRef == nil || run.Spec.ModeRef.Name != securityDraftModeName {
		t.Errorf("modeRef = %+v, want %s", run.Spec.ModeRef, securityDraftModeName)
	}
	if run.Spec.Model != "anthropic/claude-sonnet-4-6" {
		t.Errorf("model = %q", run.Spec.Model)
	}
	if run.Spec.Limits == nil || run.Spec.Limits.MaxRuntime.Duration != securityDraftMaxRuntime {
		t.Errorf("limits = %+v, want maxRuntime %s", run.Spec.Limits, securityDraftMaxRuntime)
	}
	// Only the caller's credentials for the selected provider may be mounted.
	if run.Spec.Secrets == nil {
		t.Fatal("secrets missing")
	}
	if run.Spec.Secrets.GitHubTokenSecret != "" || run.Spec.Secrets.OpenAIOAuthSecret != "" {
		t.Errorf("unexpected extra secrets: %+v", run.Spec.Secrets)
	}
	if keys := run.Spec.Secrets.ProviderKeys; len(keys) != 1 ||
		keys[0].Provider != triggersv1alpha1.ProviderAnthropic ||
		keys[0].SecretName != userCredentialSecretName(triggersv1alpha1.ProviderAnthropic) {
		t.Errorf("providerKeys = %+v", run.Spec.Secrets.ProviderKeys)
	}

	// The seed prompt fences the operator request as untrusted data.
	sess, err := ms.GetSessionByRun(context.Background(), resp.RunName, ns)
	if err != nil {
		t.Fatalf("GetSessionByRun() error = %v", err)
	}
	msgs := ms.messagesFor(sess.ID)
	if len(msgs) != 1 || msgs[0].Role != "user" {
		t.Fatalf("messages = %+v", msgs)
	}
	for _, marker := range []string{"<operator_request>", "Hunt SQL injection", "untrusted data", "workflow draft schema"} {
		if !strings.Contains(msgs[0].Content, marker) {
			t.Errorf("seed prompt must contain %q", marker)
		}
	}

	// Ownership is recorded so the draft run is not treated as system-created.
	owner, err := ms.GetResourceOwner(context.Background(), "agent_run", resp.RunName, ns)
	if err != nil || owner == nil || owner.OwnerID != testProjectSubject {
		t.Fatalf("ownership = %+v, err = %v", owner, err)
	}
}

func TestGenerateSecurityDraftValidation(t *testing.T) {
	srv, _, _ := newSecurityDraftTestServer(t)
	ctx := projectActorCtx()

	cases := []struct {
		name string
		req  *platform.GenerateSecurityDraftRequest
		code connect.Code
	}{
		{"missing kind", &platform.GenerateSecurityDraftRequest{RequestText: "x"}, connect.CodeInvalidArgument},
		{"empty request", &platform.GenerateSecurityDraftRequest{Kind: platform.SecurityDraftKind_SECURITY_DRAFT_KIND_WORKFLOW}, connect.CodeInvalidArgument},
		{"oversized request", &platform.GenerateSecurityDraftRequest{
			Kind:        platform.SecurityDraftKind_SECURITY_DRAFT_KIND_POST_SCRIPT,
			RequestText: strings.Repeat("a", securityDraftMaxRequestChars+1),
		}, connect.CodeInvalidArgument},
		{"no saved credentials for provider", &platform.GenerateSecurityDraftRequest{
			Kind:        platform.SecurityDraftKind_SECURITY_DRAFT_KIND_WORKFLOW,
			RequestText: "x",
			Model:       "openai/gpt-5.2",
		}, connect.CodeFailedPrecondition},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := srv.GenerateSecurityDraft(ctx, tc.req)
			if connect.CodeOf(err) != tc.code {
				t.Fatalf("error = %v, want code %v", err, tc.code)
			}
		})
	}
}

func TestGenerateSecurityDraftRequiresStateStore(t *testing.T) {
	srv, _ := newCronTestServer(t)
	_, err := srv.GenerateSecurityDraft(projectActorCtx(), &platform.GenerateSecurityDraftRequest{
		Kind:        platform.SecurityDraftKind_SECURITY_DRAFT_KIND_WORKFLOW,
		RequestText: "x",
	})
	if connect.CodeOf(err) != connect.CodeUnimplemented {
		t.Fatalf("error = %v, want Unimplemented", err)
	}
}

// draftRun builds a draft AgentRun object with the given kind and phase.
func draftRun(ns, name, kind string, phase platformv1alpha1.AgentRunPhase, lastError string) *platformv1alpha1.AgentRun {
	annotations := map[string]string{}
	if kind != "" {
		annotations[securityDraftKindAnnotation] = kind
	}
	return &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Annotations: annotations},
		Status:     platformv1alpha1.AgentRunStatus{Phase: phase, LastError: lastError},
	}
}

// seedDraftConversation records the run's session with one assistant reply.
func seedDraftConversation(t *testing.T, ms *mockStateStore, ns, runName, reply string) {
	t.Helper()
	sess, err := ms.CreateSession(context.Background(), runName, ns, "completed", "done")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ms.AppendMessage(context.Background(), sess.ID, "user", "seed", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := ms.AppendMessage(context.Background(), sess.ID, "assistant", reply, nil); err != nil {
		t.Fatal(err)
	}
}

func TestGetSecurityDraftWorkflowLifecycle(t *testing.T) {
	ns := testUserNS()
	ctx := projectActorCtx()

	t.Run("running", func(t *testing.T) {
		srv, _, _ := newSecurityDraftTestServer(t, draftRun(ns, "security-draft-abc123", securityDraftKindWorkflow, platformv1alpha1.AgentRunPhaseRunning, ""))
		resp, err := srv.GetSecurityDraft(ctx, &platform.GetSecurityDraftRequest{RunName: "security-draft-abc123"})
		if err != nil {
			t.Fatalf("GetSecurityDraft() error = %v", err)
		}
		if resp.Status != platform.SecurityDraftStatus_SECURITY_DRAFT_STATUS_RUNNING || resp.Phase != "Running" {
			t.Fatalf("resp = %+v", resp)
		}
	})

	t.Run("failed run", func(t *testing.T) {
		srv, _, _ := newSecurityDraftTestServer(t, draftRun(ns, "security-draft-abc123", securityDraftKindWorkflow, platformv1alpha1.AgentRunPhaseFailed, "pod crashed"))
		resp, err := srv.GetSecurityDraft(ctx, &platform.GetSecurityDraftRequest{RunName: "security-draft-abc123"})
		if err != nil {
			t.Fatalf("GetSecurityDraft() error = %v", err)
		}
		if resp.Status != platform.SecurityDraftStatus_SECURITY_DRAFT_STATUS_FAILED || resp.Error != "pod crashed" {
			t.Fatalf("resp = %+v", resp)
		}
	})

	t.Run("completed with valid draft", func(t *testing.T) {
		srv, _, ms := newSecurityDraftTestServer(t, draftRun(ns, "security-draft-abc123", securityDraftKindWorkflow, platformv1alpha1.AgentRunPhaseSucceeded, ""))
		seedDraftConversation(t, ms, ns, "security-draft-abc123", "Here is the draft:\n```json\n"+
			`{"name":"Payments Hunt!","description":"payments focus","parallelism":2,"tasks":[`+
			`{"name":"injection","objective":"hunt injections","category":"injection","maxFindings":5},`+
			`{"name":"triage","objective":"triage","role":"finding-triager","dependsOn":["injection"]}]}`+
			"\n```\nDone.")
		resp, err := srv.GetSecurityDraft(ctx, &platform.GetSecurityDraftRequest{RunName: "security-draft-abc123"})
		if err != nil {
			t.Fatalf("GetSecurityDraft() error = %v", err)
		}
		if resp.Status != platform.SecurityDraftStatus_SECURITY_DRAFT_STATUS_COMPLETED {
			t.Fatalf("resp = %+v", resp)
		}
		if len(resp.ValidationErrors) != 0 {
			t.Fatalf("validation errors = %+v", resp.ValidationErrors)
		}
		wf := resp.Workflow
		if wf == nil || wf.Name != "payments-hunt" || wf.Parallelism != 2 || len(wf.Tasks) != 2 {
			t.Fatalf("workflow = %+v", wf)
		}
		if wf.Tasks[1].DependsOn[0] != "injection" || wf.Tasks[1].Role != "finding-triager" {
			t.Fatalf("tasks = %+v", wf.Tasks)
		}
	})

	t.Run("completed draft failing shared validation", func(t *testing.T) {
		srv, _, ms := newSecurityDraftTestServer(t, draftRun(ns, "security-draft-abc123", securityDraftKindWorkflow, platformv1alpha1.AgentRunPhaseSucceeded, ""))
		seedDraftConversation(t, ms, ns, "security-draft-abc123",
			"```json\n"+`{"tasks":[{"name":"a","objective":"x","dependsOn":["missing"]}]}`+"\n```")
		resp, err := srv.GetSecurityDraft(ctx, &platform.GetSecurityDraftRequest{RunName: "security-draft-abc123"})
		if err != nil {
			t.Fatalf("GetSecurityDraft() error = %v", err)
		}
		if resp.Status != platform.SecurityDraftStatus_SECURITY_DRAFT_STATUS_COMPLETED || len(resp.ValidationErrors) == 0 {
			t.Fatalf("resp = %+v", resp)
		}
		if !strings.Contains(resp.ValidationErrors[0].Message, "unknown task") {
			t.Fatalf("validation errors = %+v", resp.ValidationErrors)
		}
	})

	t.Run("unparseable output fails", func(t *testing.T) {
		srv, _, ms := newSecurityDraftTestServer(t, draftRun(ns, "security-draft-abc123", securityDraftKindWorkflow, platformv1alpha1.AgentRunPhaseSucceeded, ""))
		seedDraftConversation(t, ms, ns, "security-draft-abc123", "I could not produce a draft, sorry.")
		resp, err := srv.GetSecurityDraft(ctx, &platform.GetSecurityDraftRequest{RunName: "security-draft-abc123"})
		if err != nil {
			t.Fatalf("GetSecurityDraft() error = %v", err)
		}
		if resp.Status != platform.SecurityDraftStatus_SECURITY_DRAFT_STATUS_FAILED || !strings.Contains(resp.Error, "did not produce a JSON draft") {
			t.Fatalf("resp = %+v", resp)
		}
	})

	t.Run("non-draft run rejected", func(t *testing.T) {
		srv, _, _ := newSecurityDraftTestServer(t, draftRun(ns, "chat-task-abc123", "", platformv1alpha1.AgentRunPhaseSucceeded, ""))
		_, err := srv.GetSecurityDraft(ctx, &platform.GetSecurityDraftRequest{RunName: "chat-task-abc123"})
		if connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Fatalf("error = %v, want InvalidArgument", err)
		}
	})
}

func TestGetSecurityDraftPostScript(t *testing.T) {
	ns := testUserNS()
	srv, _, ms := newSecurityDraftTestServer(t, draftRun(ns, "security-draft-ps1234", securityDraftKindPostScript, platformv1alpha1.AgentRunPhaseSucceeded, ""))
	seedDraftConversation(t, ms, ns, "security-draft-ps1234",
		"```json\n"+`{"name":"cwe-tagger","description":"tags findings","prompt":"Assign a CWE to each finding.","runOn":"confirmed"}`+"\n```")
	resp, err := srv.GetSecurityDraft(projectActorCtx(), &platform.GetSecurityDraftRequest{RunName: "security-draft-ps1234"})
	if err != nil {
		t.Fatalf("GetSecurityDraft() error = %v", err)
	}
	if resp.Status != platform.SecurityDraftStatus_SECURITY_DRAFT_STATUS_COMPLETED || len(resp.ValidationErrors) != 0 {
		t.Fatalf("resp = %+v", resp)
	}
	ps := resp.PostScript
	if ps == nil || ps.Name != "cwe-tagger" || ps.RunOn != "confirmed" || !strings.Contains(ps.Prompt, "CWE") {
		t.Fatalf("postScript = %+v", ps)
	}
}

func TestExtractSecurityDraftJSON(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    string
		ok      bool
	}{
		{"fenced block", "prose\n```json\n{\"a\":1}\n```\nmore", `{"a":1}`, true},
		{"last fenced block wins", "```json\n{\"a\":1}\n```\ntext\n```JSON\n{\"b\":2}\n```", `{"b":2}`, true},
		{"raw object", `  {"a":1}  `, `{"a":1}`, true},
		{"no json", "sorry, cannot help", "", false},
		{"fenced non-object ignored", "```json\n[1,2]\n```", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := extractSecurityDraftJSON(tc.content)
			if ok != tc.ok || got != tc.want {
				t.Fatalf("extractSecurityDraftJSON() = %q, %v; want %q, %v", got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestSecurityDraftName(t *testing.T) {
	cases := map[string]string{
		"Payments Hunt!":        "payments-hunt",
		"already-valid":         "already-valid",
		"":                      "",
		"!!!":                   "",
		strings.Repeat("a", 80): strings.Repeat("a", 63),
	}
	for in, want := range cases {
		if got := securityDraftName(in); got != want {
			t.Errorf("securityDraftName(%q) = %q, want %q", in, got, want)
		}
	}
}
