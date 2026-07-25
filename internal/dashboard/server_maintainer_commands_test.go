package dashboard

import (
	"context"
	"strings"
	"testing"

	"connectrpc.com/connect"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
)

// buildCommandTestEnv creates a fake server with a GitHubRepository, a
// MaintainerWorkItem, and a valid human command capability secret.
func buildCommandTestEnv(t *testing.T) (*Server, *triggersv1alpha1.GitHubRepository, *triggersv1alpha1.MaintainerWorkItem, *mockStateStore) {
	t.Helper()
	scheme := testProjectScheme(t)

	repoUID := types.UID("repo-uid-1")
	repo := &triggersv1alpha1.GitHubRepository{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "acme",
			Namespace: "default",
			UID:       repoUID,
		},
		Spec: triggersv1alpha1.GitHubRepositorySpec{
			Owner: "acme-org",
			Repo:  "payments",
		},
	}

	wi := &triggersv1alpha1.MaintainerWorkItem{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "acme-wi-42",
			Namespace:       "default",
			UID:             "wi-uid-42",
			ResourceVersion: "rv-1",
		},
		Spec: triggersv1alpha1.MaintainerWorkItemSpec{
			RepositoryRef: corev1.LocalObjectReference{Name: "acme"},
			IssueNumber:   42,
		},
		Status: triggersv1alpha1.MaintainerWorkItemStatus{
			Phase:              triggersv1alpha1.MaintainerWorkItemPhasePendingTriage,
			ProjectionSequence: 7,
			PullRequests: []triggersv1alpha1.MaintainerWorkItemPullRequestProjection{{
				IntentName: "pr-1",
				Repository: "acme-org/payments",
				Number:     77,
				HeadSHA:    "aabbcc" + "0011223344556677889900112233445566778899",
			}},
		},
	}

	// Use a deterministic key for testing.
	key := []byte("test-hmac-key-32-bytes-0123456789")
	secretName := triggersv1alpha1.MaintainerHumanCommandCapabilitySecretName(repo.Name)
	capSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: "default",
		},
		Data: map[string][]byte{
			triggersv1alpha1.MaintainerCommandCapabilitySecretKey:         key,
			triggersv1alpha1.MaintainerCommandCapabilityRepositoryNameKey: []byte(repo.Name),
			triggersv1alpha1.MaintainerCommandCapabilityRepositoryUIDKey:  []byte(repoUID),
		},
	}

	builder := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(repo, wi, capSecret).
		WithStatusSubresource(wi)

	ms := newMockStateStore()
	if err := ms.SetResourceOwner(context.Background(), githubRepositoryResourceType, repo.Name, repo.Namespace, "alice"); err != nil {
		t.Fatalf("SetResourceOwner: %v", err)
	}
	srv := &Server{k8sClient: builder.Build(), scheme: scheme, stateStore: ms}
	return srv, repo, wi, ms
}

// editorCtx returns a context for an authenticated editor.
func editorCtx(subject string) context.Context {
	return actorContext(subject, "member", "", "")
}

func TestIssueMaintainerCommandRequiresAuthentication(t *testing.T) {
	srv, _, wi, _ := buildCommandTestEnv(t)
	req := &platform.IssueMaintainerCommandRequest{
		Namespace:                  "default",
		RepositoryName:             "acme",
		WorkItemName:               wi.Name,
		ExpectedProjectionSequence: 7,
		Type:                       "TriageIssue",
	}
	if _, err := srv.IssueMaintainerCommand(context.Background(), req); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("unrecorded actor: want Unauthenticated, got %v", err)
	}
	if _, err := srv.IssueMaintainerCommand(actorContext("", "member", "", ""), req); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("empty actor: want Unauthenticated, got %v", err)
	}
}

func TestIssueMaintainerCommandFailsClosedWithoutOwnership(t *testing.T) {
	for _, tc := range []struct {
		name       string
		stateStore *mockStateStore
		noStore    bool
	}{
		{name: "unowned", stateStore: newMockStateStore()},
		{name: "no state store", noStore: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, _, wi, _ := buildCommandTestEnv(t)
			if tc.noStore {
				srv.stateStore = nil
			} else {
				srv.stateStore = tc.stateStore
			}
			req := &platform.IssueMaintainerCommandRequest{
				Namespace:                  "default",
				RepositoryName:             "acme",
				WorkItemName:               wi.Name,
				ExpectedProjectionSequence: wi.Status.ProjectionSequence,
				Type:                       "TriageIssue",
			}
			if _, err := srv.IssueMaintainerCommand(editorCtx("alice"), req); connect.CodeOf(err) != connect.CodePermissionDenied {
				t.Fatalf("IssueMaintainerCommand: want PermissionDenied, got %v", err)
			}
		})
	}
}

func TestIssueMaintainerCommandRejectsIdempotencyPayloadCollision(t *testing.T) {
	srv, _, wi, _ := buildCommandTestEnv(t)
	req := &platform.IssueMaintainerCommandRequest{
		Namespace:                  "default",
		RepositoryName:             "acme",
		WorkItemName:               wi.Name,
		ExpectedProjectionSequence: wi.Status.ProjectionSequence,
		Type:                       "TriageIssue",
		IdempotencyKey:             "same-key",
		Triage: &platform.MaintainerTriageInput{
			Disposition:     "Bounded",
			EvidenceSummary: "first payload",
		},
	}
	if _, err := srv.IssueMaintainerCommand(editorCtx("alice"), req); err != nil {
		t.Fatalf("first IssueMaintainerCommand: %v", err)
	}
	req.Triage.EvidenceSummary = "different payload"
	if _, err := srv.IssueMaintainerCommand(editorCtx("alice"), req); connect.CodeOf(err) != connect.CodeAlreadyExists {
		t.Fatalf("different payload with same idempotency key: want AlreadyExists, got %v", err)
	}
}

func TestIssueMaintainerCommandGeneratedIdempotencyKeyBindsPayloadAndActor(t *testing.T) {
	srv, _, wi, _ := buildCommandTestEnv(t)
	request := func(evidence string) *platform.IssueMaintainerCommandRequest {
		return &platform.IssueMaintainerCommandRequest{
			Namespace:                  "default",
			RepositoryName:             "acme",
			WorkItemName:               wi.Name,
			ExpectedProjectionSequence: wi.Status.ProjectionSequence,
			Type:                       "TriageIssue",
			Triage: &platform.MaintainerTriageInput{
				Disposition:     "Bounded",
				EvidenceSummary: evidence,
			},
		}
	}

	first, err := srv.IssueMaintainerCommand(editorCtx("alice"), request("first payload"))
	if err != nil {
		t.Fatalf("first IssueMaintainerCommand: %v", err)
	}
	second, err := srv.IssueMaintainerCommand(editorCtx("alice"), request("second payload"))
	if err != nil {
		t.Fatalf("different-payload IssueMaintainerCommand: %v", err)
	}
	third, err := srv.IssueMaintainerCommand(actorContext("bob", "admin", "", ""), request("first payload"))
	if err != nil {
		t.Fatalf("different-actor IssueMaintainerCommand: %v", err)
	}
	if first.CommandName == second.CommandName || first.CommandName == third.CommandName {
		t.Fatalf("generated command names must differ by payload and actor: %q, %q, %q", first.CommandName, second.CommandName, third.CommandName)
	}

	commands := &triggersv1alpha1.MaintainerWorkItemCommandList{}
	if err := srv.k8sClient.List(context.Background(), commands); err != nil {
		t.Fatalf("list commands: %v", err)
	}
	if len(commands.Items) != 3 {
		t.Fatalf("want 3 commands, got %d", len(commands.Items))
	}
	for _, command := range commands.Items {
		if len(command.Spec.IdempotencyKey) > 128 || !strings.HasPrefix(command.Spec.IdempotencyKey, "human-triageissue-") {
			t.Errorf("generated idempotency key = %q", command.Spec.IdempotencyKey)
		}
	}
}

func TestIssueMaintainerCommandRejectsCrossRepositoryGraphRef(t *testing.T) {
	srv, _, wi, _ := buildCommandTestEnv(t)
	foreign := &triggersv1alpha1.MaintainerWorkItem{
		ObjectMeta: metav1.ObjectMeta{Name: "other-wi", Namespace: "default", UID: "other-wi-uid"},
		Spec: triggersv1alpha1.MaintainerWorkItemSpec{
			RepositoryRef: corev1.LocalObjectReference{Name: "other"},
			IssueNumber:   99,
		},
	}
	if err := srv.k8sClient.Create(context.Background(), foreign); err != nil {
		t.Fatalf("create foreign work item: %v", err)
	}
	req := &platform.IssueMaintainerCommandRequest{
		Namespace:                  "default",
		RepositoryName:             "acme",
		WorkItemName:               wi.Name,
		ExpectedProjectionSequence: wi.Status.ProjectionSequence,
		Type:                       "BreakdownIssue",
		Breakdown: &platform.MaintainerBreakdownInput{
			ChildWorkItemNames: []string{foreign.Name},
		},
	}
	if _, err := srv.IssueMaintainerCommand(editorCtx("alice"), req); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("cross-repository graph ref: want InvalidArgument, got %v", err)
	}
}

func TestIssueMaintainerCommandEditorSucceeds(t *testing.T) {
	srv, _, wi, _ := buildCommandTestEnv(t)
	req := &platform.IssueMaintainerCommandRequest{
		Namespace:                  "default",
		RepositoryName:             "acme",
		WorkItemName:               wi.Name,
		ExpectedProjectionSequence: 7,
		Type:                       "TriageIssue",
		Triage: &platform.MaintainerTriageInput{
			Disposition:     "Bounded",
			EvidenceSummary: "Reproducible crash in checkout",
			AcceptedScope: &platform.MaintainerAcceptedScopeInput{
				Statement:          "Fix the nil dereference",
				AcceptanceCriteria: []string{"no panic", "tests green"},
			},
		},
	}
	resp, err := srv.IssueMaintainerCommand(actorContext("alice", "member", "", ""), req)
	if err != nil {
		t.Fatalf("owner alice IssueMaintainerCommand: %v", err)
	}
	if resp.CommandName == "" {
		t.Errorf("CommandName is empty")
	}
	if resp.Item == nil {
		t.Errorf("Item is nil")
	}
}

func TestIssueMaintainerCommandStaleSequenceAborts(t *testing.T) {
	srv, _, wi, _ := buildCommandTestEnv(t)

	req := &platform.IssueMaintainerCommandRequest{
		Namespace:                  "default",
		RepositoryName:             "acme",
		WorkItemName:               wi.Name,
		ExpectedProjectionSequence: 99, // stale
		Type:                       "TriageIssue",
	}
	_, err := srv.IssueMaintainerCommand(editorCtx("alice"), req)
	if connect.CodeOf(err) != connect.CodeAborted {
		t.Fatalf("stale sequence: want Aborted, got %v", err)
	}
}

func TestIssueMaintainerCommandUnknownTypeInvalidArgument(t *testing.T) {
	srv, _, wi, _ := buildCommandTestEnv(t)

	req := &platform.IssueMaintainerCommandRequest{
		Namespace:                  "default",
		RepositoryName:             "acme",
		WorkItemName:               wi.Name,
		ExpectedProjectionSequence: 7,
		Type:                       "DoSomethingWeird",
	}
	_, err := srv.IssueMaintainerCommand(editorCtx("alice"), req)
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("unknown type: want InvalidArgument, got %v", err)
	}
}

func TestIssueMaintainerCommandResolveDecisionStampsActor(t *testing.T) {
	srv, repo, wi, _ := buildCommandTestEnv(t)

	req := &platform.IssueMaintainerCommandRequest{
		Namespace:                  "default",
		RepositoryName:             repo.Name,
		WorkItemName:               wi.Name,
		ExpectedProjectionSequence: wi.Status.ProjectionSequence,
		Type:                       "ResolveDecision",
		ResolveDecision: &platform.MaintainerResolveDecisionInput{
			DecisionId: "d-1",
			Answer:     "yes",
		},
	}
	resp, err := srv.IssueMaintainerCommand(editorCtx("alice"), req)
	if err != nil {
		t.Fatalf("ResolveDecision: %v", err)
	}

	// Read back the command and check the human answer subject.
	cmdList := &triggersv1alpha1.MaintainerWorkItemCommandList{}
	if err := srv.k8sClient.List(context.Background(), cmdList); err != nil {
		t.Fatalf("list commands: %v", err)
	}
	if len(cmdList.Items) != 1 {
		t.Fatalf("want 1 command, got %d", len(cmdList.Items))
	}
	cmd := cmdList.Items[0]
	if cmd.Spec.ResolveDecision == nil {
		t.Fatal("ResolveDecision payload is nil")
	}
	if cmd.Spec.ResolveDecision.HumanAnswer.Subject != "alice" {
		t.Errorf("HumanAnswer.Subject = %q, want %q", cmd.Spec.ResolveDecision.HumanAnswer.Subject, "alice")
	}
	if resp.CommandName == "" {
		t.Errorf("CommandName is empty")
	}
}

func TestIssueMaintainerCommandMergeDerivesRepositoryFromSpec(t *testing.T) {
	srv, repo, wi, _ := buildCommandTestEnv(t)

	headSHA := "a" + "bcdef0123456789abcdef0123456789abcdef01" // 40 hex chars
	req := &platform.IssueMaintainerCommandRequest{
		Namespace:                  "default",
		RepositoryName:             repo.Name,
		WorkItemName:               wi.Name,
		ExpectedProjectionSequence: wi.Status.ProjectionSequence,
		Type:                       "RequestMerge",
		RequestMerge: &platform.MaintainerRequestMergeInput{
			PullRequestNumber: 77,
			ExpectedHeadSha:   headSHA,
			MergeMethod:       "squash",
		},
	}
	resp, err := srv.IssueMaintainerCommand(editorCtx("alice"), req)
	if err != nil {
		t.Fatalf("RequestMerge: %v", err)
	}

	// Check the command was created with the server-derived repository.
	cmdList := &triggersv1alpha1.MaintainerWorkItemCommandList{}
	if err := srv.k8sClient.List(context.Background(), cmdList); err != nil {
		t.Fatalf("list commands: %v", err)
	}
	if len(cmdList.Items) != 1 {
		t.Fatalf("want 1 command, got %d", len(cmdList.Items))
	}
	cmd := cmdList.Items[0]
	if cmd.Spec.RequestMerge == nil {
		t.Fatal("RequestMerge payload is nil")
	}
	wantRepo := "acme-org/payments"
	if cmd.Spec.RequestMerge.Repository != wantRepo {
		t.Errorf("Repository = %q, want %q", cmd.Spec.RequestMerge.Repository, wantRepo)
	}
	if cmd.Spec.RequestMerge.ExpectedHeadSHA != headSHA {
		t.Errorf("ExpectedHeadSHA = %q, want %q", cmd.Spec.RequestMerge.ExpectedHeadSHA, headSHA)
	}
	if resp.Item == nil {
		t.Errorf("Item is nil")
	}
}

func TestIssueMaintainerCommandProofIsValid(t *testing.T) {
	srv, repo, wi, _ := buildCommandTestEnv(t)

	req := &platform.IssueMaintainerCommandRequest{
		Namespace:                  "default",
		RepositoryName:             repo.Name,
		WorkItemName:               wi.Name,
		ExpectedProjectionSequence: wi.Status.ProjectionSequence,
		Type:                       "TriageIssue",
		Triage: &platform.MaintainerTriageInput{
			Disposition:     "Bounded",
			EvidenceSummary: "Bug confirmed",
		},
	}
	if _, err := srv.IssueMaintainerCommand(editorCtx("alice"), req); err != nil {
		t.Fatalf("IssueMaintainerCommand: %v", err)
	}

	cmdList := &triggersv1alpha1.MaintainerWorkItemCommandList{}
	if err := srv.k8sClient.List(context.Background(), cmdList); err != nil {
		t.Fatalf("list commands: %v", err)
	}
	if len(cmdList.Items) != 1 {
		t.Fatalf("want 1 command, got %d", len(cmdList.Items))
	}
	cmd := cmdList.Items[0]
	if cmd.Spec.HumanIssuer == nil {
		t.Fatal("HumanIssuer is nil")
	}
	if cmd.Spec.HumanIssuer.Subject != "alice" {
		t.Errorf("HumanIssuer.Subject = %q, want alice", cmd.Spec.HumanIssuer.Subject)
	}

	// Re-derive the proof and verify it matches.
	secretName := triggersv1alpha1.MaintainerHumanCommandCapabilitySecretName(repo.Name)
	cap := &corev1.Secret{}
	if err := srv.k8sClient.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: secretName}, cap); err != nil {
		t.Fatalf("get secret: %v", err)
	}
	key := cap.Data[triggersv1alpha1.MaintainerCommandCapabilitySecretKey]
	wantProof := triggersv1alpha1.MaintainerHumanCommandProof(key, repo.Name, repo.UID, cmd.Spec.IdempotencyKey, cmd.Spec.PayloadHash, "alice")
	if cmd.Spec.HumanIssuer.Proof != wantProof {
		t.Errorf("proof mismatch:\n  got  %s\n  want %s", cmd.Spec.HumanIssuer.Proof, wantProof)
	}
}

func TestBuildMaintainerFinalizeIncludesAllProjectedImplementers(t *testing.T) {
	item := &triggersv1alpha1.MaintainerWorkItem{
		ObjectMeta: metav1.ObjectMeta{Name: "acme-wi-42", Namespace: "default", UID: "wi-uid-42", ResourceVersion: "rv-1"},
		Spec: triggersv1alpha1.MaintainerWorkItemSpec{
			RepositoryRef: corev1.LocalObjectReference{Name: "acme"},
			IssueNumber:   42,
			AcceptedScope: &triggersv1alpha1.MaintainerAcceptedScope{Statement: "Ship it"},
		},
		Status: triggersv1alpha1.MaintainerWorkItemStatus{
			ProjectionSequence: 7,
			AgentRuns: []triggersv1alpha1.MaintainerWorkItemAgentRunProjection{
				{Name: "impl-z", Role: triggersv1alpha1.MaintainerWorkItemAgentRunRoleImplementer},
				{Name: "reviewer", Role: triggersv1alpha1.MaintainerWorkItemAgentRunRoleReviewer},
				{Name: "impl-a", Role: triggersv1alpha1.MaintainerWorkItemAgentRunRoleImplementer},
			},
		},
	}
	repo := &triggersv1alpha1.GitHubRepository{
		ObjectMeta: metav1.ObjectMeta{Name: "acme", Namespace: "default"},
		Spec:       triggersv1alpha1.GitHubRepositorySpec{Owner: "acme-org", Repo: "payments"},
	}
	req := &platform.IssueMaintainerCommandRequest{
		Type: "FinalizeWorkItem",
		Finalize: &platform.MaintainerFinalizeInput{
			DeliverySummary:  "Delivered",
			DeliveryEvidence: "PR #77 merged",
		},
	}
	preconditions := triggersv1alpha1.MaintainerWorkItemCommandPreconditions{
		WorkItemName: item.Name, WorkItemUID: item.UID, ProjectionSequence: 7, ResourceVersion: "rv-1",
	}

	spec, err := (&Server{}).buildMaintainerCommandSpec(
		context.Background(), req, triggersv1alpha1.MaintainerWorkItemCommandTypeFinalizeWorkItem,
		repo, item, preconditions, "finalize-42", "alice",
	)
	if err != nil {
		t.Fatalf("buildMaintainerCommandSpec: %v", err)
	}
	if got, want := strings.Join(spec.Finalize.ImplementerRunNames, ","), "impl-a,impl-z"; got != want {
		t.Fatalf("ImplementerRunNames = %q, want %q", got, want)
	}
}
