package triggers

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/go-github/v68/github"
	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/orchestration"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const (
	maintainerWorkItemTestNamespace = "default"
	maintainerAgentRunKind          = "AgentRun"
	maintainerWorkItemTestOwner     = "owner"
	maintainerWorkItemTestRepo      = "repo"
)

func TestAuthenticatedGitHubCommentResolvesPendingDecision(t *testing.T) {
	scheme := maintainerWorkItemScheme(t)
	repository := testMaintainerRepository()
	repository.Spec.Auth = &triggersv1alpha1.TriggerAuth{AllowedUsers: []string{monitorTestAlice}}
	repository.Spec.Maintainer = &triggersv1alpha1.MaintainerSpec{}
	item := testMaintainerWorkItem(repository, 7)
	item.Status.PendingDecision = &triggersv1alpha1.MaintainerPendingDecision{ID: "ship-policy", Question: "Ship?", RequestedAt: metav1.Now()}
	item.Status.Phase = triggersv1alpha1.MaintainerWorkItemPhaseAwaitingDecision
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&triggersv1alpha1.MaintainerWorkItem{}).WithObjects(repository, item).Build()
	r := &GitHubRepositoryReconciler{Client: c, APIReader: c, Scheme: scheme, MaintainerEnabled: true}
	event := func(login string, id int64) *github.IssueCommentEvent {
		return &github.IssueCommentEvent{Action: new(githubActionCreated), Issue: &github.Issue{Number: new(7)}, Comment: &github.IssueComment{ID: new(id), Body: new("@agent answer ship-policy: use the safer option"), User: &github.User{Login: new(login)}, AuthorAssociation: new("NONE")}}
	}
	if err := r.HandleIssueComment(context.Background(), repository, event("mallory", 10)); err != nil {
		t.Fatal(err)
	}
	current := &triggersv1alpha1.MaintainerWorkItem{}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(item), current); err != nil {
		t.Fatal(err)
	}
	if current.Status.PendingDecision == nil {
		t.Fatal("unauthorized comment cleared pending decision")
	}
	if err := r.HandleIssueComment(context.Background(), repository, event(monitorTestAlice, 11)); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(item), current); err != nil {
		t.Fatal(err)
	}
	if current.Status.PendingDecision != nil || current.Status.ResolvedDecision == nil || current.Status.ResolvedDecision.HumanSubject != monitorTestAlice || current.Status.ResolvedDecision.Answer != "use the safer option" || current.Status.ResolvedDecision.ResolvedByCommand.Name != "github-comment-11" {
		t.Fatalf("resolved decision = %#v", current.Status.ResolvedDecision)
	}
}

func TestAgentRunResolveDecisionCommandIsRejected(t *testing.T) {
	command := &triggersv1alpha1.MaintainerWorkItemCommand{Spec: triggersv1alpha1.MaintainerWorkItemCommandSpec{Type: triggersv1alpha1.MaintainerWorkItemCommandTypeResolveDecision, ResolveDecision: &triggersv1alpha1.MaintainerResolveDecisionCommand{IssueNumber: 7, DecisionID: "ship-policy", HumanAnswer: triggersv1alpha1.MaintainerAuthenticatedHumanAnswer{Subject: "forged", Answer: "yes"}}}}
	if _, err := validateMaintainerCommandPayload(command); err == nil {
		t.Fatal("agent-supplied human answer was accepted")
	}
}

func TestMaintainerWorkItemName(t *testing.T) {
	t.Parallel()

	name := MaintainerWorkItemName("Repository", 42)
	if name != MaintainerWorkItemName("Repository", 42) || !strings.HasPrefix(name, "mwi-repository-42-") {
		t.Fatalf("MaintainerWorkItemName() = %q", name)
	}
	if MaintainerWorkItemName("foo.bar", 42) == MaintainerWorkItemName("foo-bar", 42) {
		t.Fatal("normalized repository names collided")
	}
	long := strings.Repeat("repository-", 10)
	first := MaintainerWorkItemName(long, 42)
	second := MaintainerWorkItemName(long, 43)
	if len(first) > 63 || first == second {
		t.Fatalf("truncated names = %q, %q", first, second)
	}
	if !strings.HasPrefix(first, "mwi-") {
		t.Fatalf("truncated name %q is missing prefix", first)
	}
}

func TestMaintainerWorkItemProjectionNoopAndFreshness(t *testing.T) {
	t.Parallel()

	scheme := maintainerWorkItemScheme(t)
	repository := testMaintainerRepository()
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&triggersv1alpha1.MaintainerWorkItem{}).WithObjects(repository).Build()
	reconciler := &GitHubRepositoryReconciler{Client: c, Scheme: scheme}
	issue := testMaintainerIssue(7)
	if err := reconciler.reconcileMaintainerWorkItems(context.Background(), repository, []*github.Issue{issue}, true); err != nil {
		t.Fatalf("first projection: %v", err)
	}
	item := getMaintainerWorkItem(t, c, repository, 7)
	sequence := item.Status.ProjectionSequence
	observedAt := item.Status.IssueObservation.ObservedAt

	time.Sleep(time.Millisecond)
	if err := reconciler.reconcileMaintainerWorkItems(context.Background(), repository, []*github.Issue{issue}, true); err != nil {
		t.Fatalf("second projection: %v", err)
	}
	item = getMaintainerWorkItem(t, c, repository, 7)
	if item.Status.ProjectionSequence != sequence || !item.Status.IssueObservation.ObservedAt.Equal(&observedAt) {
		t.Fatalf("no-op projection changed sequence or observedAt: %#v", item.Status)
	}

	if err := reconciler.reconcileMaintainerWorkItems(context.Background(), repository, nil, false); err != nil {
		t.Fatalf("incomplete projection: %v", err)
	}
	item = getMaintainerWorkItem(t, c, repository, 7)
	if item.Status.ProjectionSequence != sequence || !maintainerWorkItemObservationIsFresh(item) {
		t.Fatalf("incomplete issue list changed freshness: %#v", item.Status)
	}

	if err := reconciler.reconcileMaintainerWorkItems(context.Background(), repository, nil, true); err != nil {
		t.Fatalf("stale projection: %v", err)
	}
	item = getMaintainerWorkItem(t, c, repository, 7)
	if item.Status.ProjectionSequence != sequence+1 {
		t.Fatalf("stale sequence = %d, want %d", item.Status.ProjectionSequence, sequence+1)
	}
	condition := findMaintainerWorkItemCondition(item, triggersv1alpha1.ConditionMaintainerWorkItemObservationFresh)
	if condition == nil || condition.Status != metav1.ConditionFalse || condition.Reason != "NotInOpenIssueList" {
		t.Fatalf("freshness condition = %#v", condition)
	}
}

func TestMaintainerCommandRejectsUnauthorizedIssuer(t *testing.T) {
	t.Parallel()

	scheme := maintainerWorkItemScheme(t)
	repository := testMaintainerRepository()
	item := testMaintainerWorkItem(repository, 8)
	command := testMaintainerCommand(repository, item, "command", types.UID("wrong"))
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&triggersv1alpha1.MaintainerWorkItem{}, &triggersv1alpha1.MaintainerWorkItemCommand{}).WithObjects(repository, item, command).Build()
	reconciler := &GitHubRepositoryReconciler{Client: c, Scheme: scheme}
	if err := reconciler.reconcileMaintainerWorkItemCommands(context.Background(), repository, &fakeMaintainerGitHub{}); err != nil {
		t.Fatalf("reconcile commands: %v", err)
	}
	current := &triggersv1alpha1.MaintainerWorkItemCommand{}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(command), current); err != nil {
		t.Fatal(err)
	}
	if current.Status.Phase != triggersv1alpha1.MaintainerWorkItemCommandPhaseRejected {
		t.Fatalf("phase = %q, want Rejected", current.Status.Phase)
	}
}

func TestMaintainerCommandRejectsForgedIssuerProof(t *testing.T) {
	t.Parallel()

	scheme := maintainerWorkItemScheme(t)
	repository := testMaintainerRepository()
	item := testMaintainerWorkItem(repository, 12)
	issuer := testMaintainerIssuer(repository)
	command := testMaintainerCommand(repository, item, "forged", issuer.UID)
	command.Spec.Issuer.Proof = strings.Repeat("0", 64)
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&triggersv1alpha1.MaintainerWorkItem{}, &triggersv1alpha1.MaintainerWorkItemCommand{}).
		WithObjects(repository, item, issuer, testMaintainerCapability(repository, issuer), command).Build()
	reconciler := &GitHubRepositoryReconciler{Client: c, Scheme: scheme}
	if err := reconciler.reconcileMaintainerWorkItemCommands(context.Background(), repository, &fakeMaintainerGitHub{}); err != nil {
		t.Fatalf("reconcile commands: %v", err)
	}
	receipt := &triggersv1alpha1.MaintainerWorkItemCommand{}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(command), receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.Status.Phase != triggersv1alpha1.MaintainerWorkItemCommandPhaseRejected || receipt.Status.Result == nil || !strings.Contains(receipt.Status.Result.Message, "proof is invalid") {
		t.Fatalf("command receipt = %#v", receipt.Status)
	}
}

func TestMaintainerCommandAppliesEmptyStatusAsPending(t *testing.T) {
	t.Parallel()

	scheme := maintainerWorkItemScheme(t)
	repository := testMaintainerRepository()
	item := testMaintainerWorkItem(repository, 11)
	issuer := testMaintainerIssuer(repository)
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&triggersv1alpha1.MaintainerWorkItem{}, &triggersv1alpha1.MaintainerWorkItemCommand{}).
		WithObjects(repository, item, issuer, testMaintainerCapability(repository, issuer)).Build()
	stored := getMaintainerWorkItem(t, c, repository, 11)
	command := testMaintainerCommand(repository, stored, "apply", issuer.UID)
	if err := c.Create(context.Background(), command); err != nil {
		t.Fatal(err)
	}
	reconciler := &GitHubRepositoryReconciler{Client: c, Scheme: scheme}
	if err := reconciler.reconcileMaintainerWorkItemCommands(context.Background(), repository, &fakeMaintainerGitHub{}); err != nil {
		t.Fatalf("reconcile commands: %v", err)
	}
	applied := getMaintainerWorkItem(t, c, repository, 11)
	if applied.Spec.Disposition != triggersv1alpha1.MaintainerWorkItemDispositionBounded || applied.Status.Phase != triggersv1alpha1.MaintainerWorkItemPhaseTriaged {
		t.Fatalf("work item was not triaged: spec=%#v status=%#v", applied.Spec, applied.Status)
	}
	receipt := &triggersv1alpha1.MaintainerWorkItemCommand{}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(command), receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.Status.Phase != triggersv1alpha1.MaintainerWorkItemCommandPhaseSucceeded || receipt.Status.Result == nil || !receipt.Status.Result.Applied {
		t.Fatalf("command receipt = %#v", receipt.Status)
	}
}

func TestFailedMaintainerCommandCannotApplyAfterNewerTriage(t *testing.T) {
	t.Parallel()

	scheme := maintainerWorkItemScheme(t)
	repository := testMaintainerRepository()
	item := testMaintainerWorkItem(repository, 13)
	item.Spec.Disposition = triggersv1alpha1.MaintainerWorkItemDispositionBounded
	item.Spec.EvidenceSummary = "newer evidence"
	item.Spec.AcceptedScope = &triggersv1alpha1.MaintainerAcceptedScope{Statement: "newer scope"}
	item.Spec.TriagedByCommand = &corev1.LocalObjectReference{Name: "newer-command"}
	issuer := testMaintainerIssuer(repository)
	command := testMaintainerCommand(repository, item, "older", issuer.UID)
	reason := triggersv1alpha1.MaintainerWorkItemCloseReasonNotPlanned
	command.Spec.Triage.Disposition = triggersv1alpha1.MaintainerWorkItemDispositionNotActionable
	command.Spec.Triage.CloseReason = &reason
	command.Spec.PayloadHash = MaintainerWorkItemCommandPayloadHash(command.Spec.Type, command.Spec.Triage, command.Spec.Preconditions)
	command.Spec.Issuer.Proof = triggersv1alpha1.MaintainerWorkItemCommandProof(testMaintainerCapabilityKey(), repository.Name, repository.UID, command.Spec.IdempotencyKey, command.Spec.PayloadHash, command.Spec.Issuer.RunName, command.Spec.Issuer.UID)
	command.Status.Phase = triggersv1alpha1.MaintainerWorkItemCommandPhaseFailed
	githubClient := &fakeMaintainerGitHub{issue: &github.Issue{State: new("open")}}
	c := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&triggersv1alpha1.MaintainerWorkItem{}, &triggersv1alpha1.MaintainerWorkItemCommand{}).
		WithObjects(repository, item, issuer, testMaintainerCapability(repository, issuer), command).Build()
	reconciler := &GitHubRepositoryReconciler{Client: c, Scheme: scheme}
	if err := reconciler.reconcileMaintainerWorkItemCommands(context.Background(), repository, githubClient); err != nil {
		t.Fatalf("reconcile commands: %v", err)
	}
	receipt := &triggersv1alpha1.MaintainerWorkItemCommand{}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(command), receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.Status.Phase != triggersv1alpha1.MaintainerWorkItemCommandPhaseRejected || receipt.Status.Result == nil || !strings.Contains(receipt.Status.Result.Message, "superseded") {
		t.Fatalf("command receipt = %#v", receipt.Status)
	}
	if githubClient.created != 0 || githubClient.editedIssue != 0 {
		t.Fatalf("superseded command performed GitHub effects: comments=%d issueEdits=%d", githubClient.created, githubClient.editedIssue)
	}
}

func TestMaintainerCommandRejectsStaleProjection(t *testing.T) {
	t.Parallel()

	scheme := maintainerWorkItemScheme(t)
	repository := testMaintainerRepository()
	item := testMaintainerWorkItem(repository, 9)
	item.Status.ProjectionSequence = 2
	item.ResourceVersion = "stale"
	issuer := testMaintainerIssuer(repository)
	command := testMaintainerCommand(repository, item, "command", issuer.UID)
	command.Spec.Preconditions.ProjectionSequence = 1
	command.Spec.Preconditions.ResourceVersion = "other"
	command.Spec.PayloadHash = MaintainerWorkItemCommandPayloadHash(command.Spec.Type, command.Spec.Triage, command.Spec.Preconditions)
	command.Spec.Issuer.Proof = triggersv1alpha1.MaintainerWorkItemCommandProof(testMaintainerCapabilityKey(), repository.Name, repository.UID, command.Spec.IdempotencyKey, command.Spec.PayloadHash, command.Spec.Issuer.RunName, command.Spec.Issuer.UID)
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&triggersv1alpha1.MaintainerWorkItem{}, &triggersv1alpha1.MaintainerWorkItemCommand{}).WithObjects(repository, item, issuer, testMaintainerCapability(repository, issuer), command).Build()
	reconciler := &GitHubRepositoryReconciler{Client: c, Scheme: scheme}
	if err := reconciler.reconcileMaintainerWorkItemCommands(context.Background(), repository, &fakeMaintainerGitHub{}); err != nil {
		t.Fatalf("reconcile commands: %v", err)
	}
	current := &triggersv1alpha1.MaintainerWorkItemCommand{}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(command), current); err != nil {
		t.Fatal(err)
	}
	if current.Status.Phase != triggersv1alpha1.MaintainerWorkItemCommandPhaseRejected || !strings.Contains(current.Status.Result.Message, "current projection sequence") {
		t.Fatalf("status = %#v", current.Status)
	}
}

func TestAgentRunResolveDecisionCannotClearPendingDecision(t *testing.T) {
	t.Parallel()
	scheme := maintainerWorkItemScheme(t)
	repository := testMaintainerRepository()
	item := testMaintainerWorkItem(repository, 11)
	item.ResourceVersion = "999"
	item.Spec.Disposition = triggersv1alpha1.MaintainerWorkItemDispositionEscalated
	item.Status.PendingDecision = &triggersv1alpha1.MaintainerPendingDecision{ID: "policy", Question: "Proceed?", RequestedAt: metav1.Now()}
	item.Status.Phase = triggersv1alpha1.MaintainerWorkItemPhaseAwaitingDecision
	issuer := testMaintainerIssuer(repository)
	preconditions := triggersv1alpha1.MaintainerWorkItemCommandPreconditions{WorkItemName: item.Name, ProjectionSequence: item.Status.ProjectionSequence, ResourceVersion: item.ResourceVersion}
	command := &triggersv1alpha1.MaintainerWorkItemCommand{ObjectMeta: metav1.ObjectMeta{Name: MaintainerWorkItemCommandName(repository.Name, "answer-1"), Namespace: repository.Namespace, OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(repository, triggersv1alpha1.GroupVersion.WithKind(gitHubRepositoryTriggerKind))}}, Spec: triggersv1alpha1.MaintainerWorkItemCommandSpec{RepositoryRef: corev1.LocalObjectReference{Name: repository.Name}, IdempotencyKey: "answer-1", Preconditions: preconditions, Type: triggersv1alpha1.MaintainerWorkItemCommandTypeResolveDecision, ResolveDecision: &triggersv1alpha1.MaintainerResolveDecisionCommand{IssueNumber: 11, DecisionID: "policy", HumanAnswer: triggersv1alpha1.MaintainerAuthenticatedHumanAnswer{Subject: "user:42", Answer: "proceed"}}}}
	command.Spec.PayloadHash = triggersv1alpha1.MaintainerWorkItemCommandSpecPayloadHash(command.Spec)
	command.Spec.Issuer = triggersv1alpha1.MaintainerWorkItemCommandIssuer{RunName: issuer.Name, UID: issuer.UID, Proof: triggersv1alpha1.MaintainerWorkItemCommandProof(testMaintainerCapabilityKey(), repository.Name, repository.UID, command.Spec.IdempotencyKey, command.Spec.PayloadHash, issuer.Name, issuer.UID)}
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&triggersv1alpha1.MaintainerWorkItem{}, &triggersv1alpha1.MaintainerWorkItemCommand{}).WithObjects(repository, item, issuer, testMaintainerCapability(repository, issuer), command).Build()
	r := &GitHubRepositoryReconciler{Client: c, Scheme: scheme}
	if err := r.reconcileMaintainerWorkItemCommands(context.Background(), repository, &fakeMaintainerGitHub{}); err != nil {
		t.Fatal(err)
	}
	current := getMaintainerWorkItem(t, c, repository, 11)
	receipt := &triggersv1alpha1.MaintainerWorkItemCommand{}
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(command), receipt); err != nil {
		t.Fatal(err)
	}
	if current.Status.PendingDecision == nil || current.Status.ResolvedDecision != nil {
		t.Fatalf("agent command changed decision status = %#v", current.Status)
	}
	if receipt.Status.Phase != triggersv1alpha1.MaintainerWorkItemCommandPhaseRejected || !strings.Contains(receipt.Status.Result.Message, "not authorized") {
		t.Fatalf("receipt = %#v", receipt.Status)
	}
}

func TestNotActionableTriageReusesDecisionMarker(t *testing.T) {
	t.Parallel()

	repository := testMaintainerRepository()
	item := testMaintainerWorkItem(repository, 10)
	reason := triggersv1alpha1.MaintainerWorkItemCloseReasonNotPlanned
	triage := &triggersv1alpha1.MaintainerTriageCommand{IssueNumber: 10, Disposition: triggersv1alpha1.MaintainerWorkItemDispositionNotActionable, EvidenceSummary: "duplicate", CloseReason: &reason}
	githubClient := &fakeMaintainerGitHub{issue: &github.Issue{State: new("open")}}

	firstURL, _, err := (&GitHubRepositoryReconciler{}).applyNotActionableTriage(context.Background(), repository, item, triage, githubClient)
	if err != nil {
		t.Fatalf("first triage: %v", err)
	}
	secondURL, _, err := (&GitHubRepositoryReconciler{}).applyNotActionableTriage(context.Background(), repository, item, triage, githubClient)
	if err != nil {
		t.Fatalf("second triage: %v", err)
	}
	if githubClient.created != 1 || githubClient.editedIssue != 1 || githubClient.issue.GetState() != string(triggersv1alpha1.MaintainerIssueStateClosed) || githubClient.issue.GetStateReason() != "not_planned" || firstURL != secondURL {
		t.Fatalf("created=%d editedIssue=%d issue=%#v URLs=%q,%q", githubClient.created, githubClient.editedIssue, githubClient.issue, firstURL, secondURL)
	}
}

func maintainerWorkItemScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := triggersv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return scheme
}

func testMaintainerRepository() *triggersv1alpha1.GitHubRepository {
	return &triggersv1alpha1.GitHubRepository{ObjectMeta: metav1.ObjectMeta{Name: maintainerWorkItemTestRepo, Namespace: maintainerWorkItemTestNamespace, UID: types.UID("repository")}, Spec: triggersv1alpha1.GitHubRepositorySpec{Owner: maintainerWorkItemTestOwner, Repo: maintainerWorkItemTestRepo}}
}

func testMaintainerIssue(number int) *github.Issue {
	updated := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	return &github.Issue{Number: new(number), Title: new("title"), Body: new("body"), HTMLURL: new("https://example.test/issues/1"), User: &github.User{Login: new("author")}, UpdatedAt: &github.Timestamp{Time: updated}, Labels: []*github.Label{{Name: new("bug")}}}
}

func testMaintainerWorkItem(repository *triggersv1alpha1.GitHubRepository, issue int32) *triggersv1alpha1.MaintainerWorkItem {
	return &triggersv1alpha1.MaintainerWorkItem{ObjectMeta: metav1.ObjectMeta{Name: MaintainerWorkItemName(repository.Name, issue), Namespace: repository.Namespace}, Spec: triggersv1alpha1.MaintainerWorkItemSpec{RepositoryRef: corev1.LocalObjectReference{Name: repository.Name}, IssueNumber: issue}, Status: triggersv1alpha1.MaintainerWorkItemStatus{
		ProjectionSequence: 1,
		IssueObservation:   &triggersv1alpha1.MaintainerIssueObservation{Number: issue, State: triggersv1alpha1.MaintainerIssueStateOpen},
		Conditions:         []metav1.Condition{{Type: triggersv1alpha1.ConditionMaintainerWorkItemObservationFresh, Status: metav1.ConditionTrue}},
	}}
}

func testMaintainerIssuer(repository *triggersv1alpha1.GitHubRepository) *platformv1alpha1.AgentRun {
	controller := true
	return &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:      defaultMaintainerModeName,
			Namespace: repository.Namespace,
			UID:       types.UID("issuer"),
			Labels: map[string]string{
				orchestration.StandingRunRoleLabel: orchestration.StandingRunRoleMaintainer,
				orchestration.SupervisedRunLabel:   repository.Name,
			},
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: triggersv1alpha1.GroupVersion.String(),
				Kind:       gitHubRepositoryTriggerKind,
				Name:       repository.Name,
				UID:        repository.UID,
				Controller: &controller,
			}},
		},
	}
}

func testMaintainerCapabilityKey() []byte {
	return []byte("01234567890123456789012345678901")
}

func testMaintainerCapability(repository *triggersv1alpha1.GitHubRepository, issuer *platformv1alpha1.AgentRun) *corev1.Secret {
	controller := true
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name: triggersv1alpha1.MaintainerCommandCapabilitySecretName(issuer.Name), Namespace: issuer.Namespace,
			OwnerReferences: []metav1.OwnerReference{{APIVersion: platformv1alpha1.GroupVersion.String(), Kind: maintainerAgentRunKind, Name: issuer.Name, UID: issuer.UID, Controller: &controller}},
		},
		Data: map[string][]byte{
			triggersv1alpha1.MaintainerCommandCapabilitySecretKey:         testMaintainerCapabilityKey(),
			triggersv1alpha1.MaintainerCommandCapabilityRepositoryNameKey: []byte(repository.Name),
			triggersv1alpha1.MaintainerCommandCapabilityRepositoryUIDKey:  []byte(repository.UID),
		},
	}
}

func testMaintainerCommand(repository *triggersv1alpha1.GitHubRepository, item *triggersv1alpha1.MaintainerWorkItem, idempotencyKey string, issuerUID types.UID) *triggersv1alpha1.MaintainerWorkItemCommand {
	triage := &triggersv1alpha1.MaintainerTriageCommand{IssueNumber: item.Spec.IssueNumber, Disposition: triggersv1alpha1.MaintainerWorkItemDispositionBounded, EvidenceSummary: "evidence", AcceptedScope: triggersv1alpha1.MaintainerAcceptedScope{Statement: "scope"}}
	preconditions := triggersv1alpha1.MaintainerWorkItemCommandPreconditions{WorkItemName: item.Name, ProjectionSequence: item.Status.ProjectionSequence, ResourceVersion: item.ResourceVersion}
	payloadHash := MaintainerWorkItemCommandPayloadHash(triggersv1alpha1.MaintainerWorkItemCommandTypeTriageIssue, triage, preconditions)
	proof := triggersv1alpha1.MaintainerWorkItemCommandProof(testMaintainerCapabilityKey(), repository.Name, repository.UID, idempotencyKey, payloadHash, defaultMaintainerModeName, issuerUID)
	controller := true
	return &triggersv1alpha1.MaintainerWorkItemCommand{
		ObjectMeta: metav1.ObjectMeta{
			Name:      MaintainerWorkItemCommandName(repository.Name, idempotencyKey),
			Namespace: repository.Namespace,
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: triggersv1alpha1.GroupVersion.String(), Kind: gitHubRepositoryTriggerKind, Name: repository.Name, UID: repository.UID, Controller: &controller,
			}},
		},
		Spec: triggersv1alpha1.MaintainerWorkItemCommandSpec{
			RepositoryRef: corev1.LocalObjectReference{Name: repository.Name}, IdempotencyKey: idempotencyKey,
			Issuer: triggersv1alpha1.MaintainerWorkItemCommandIssuer{RunName: defaultMaintainerModeName, UID: issuerUID, Proof: proof},
			Type:   triggersv1alpha1.MaintainerWorkItemCommandTypeTriageIssue, Triage: triage, Preconditions: preconditions,
			PayloadHash: payloadHash,
		},
	}
}

func getMaintainerWorkItem(t *testing.T, c client.Client, repository *triggersv1alpha1.GitHubRepository, issue int32) *triggersv1alpha1.MaintainerWorkItem {
	t.Helper()
	item := &triggersv1alpha1.MaintainerWorkItem{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: repository.Namespace, Name: MaintainerWorkItemName(repository.Name, issue)}, item); err != nil {
		t.Fatal(err)
	}
	return item
}

func findMaintainerWorkItemCondition(item *triggersv1alpha1.MaintainerWorkItem, conditionType string) *metav1.Condition {
	for i := range item.Status.Conditions {
		if item.Status.Conditions[i].Type == conditionType {
			return &item.Status.Conditions[i]
		}
	}
	return nil
}

type fakeMaintainerGitHub struct {
	comments    []*github.IssueComment
	issue       *github.Issue
	created     int
	editedIssue int
}

func (f *fakeMaintainerGitHub) ListIssueComments(context.Context, string, string, int, *github.IssueListCommentsOptions) ([]*github.IssueComment, *github.Response, error) {
	return f.comments, &github.Response{}, nil
}

func (f *fakeMaintainerGitHub) CreateIssueComment(_ context.Context, _ string, _ string, _ int, comment *github.IssueComment) (*github.IssueComment, *github.Response, error) {
	f.created++
	comment.ID = new(int64(f.created))
	comment.HTMLURL = new("https://example.test/comments/1")
	f.comments = append(f.comments, comment)
	return comment, &github.Response{}, nil
}

func (f *fakeMaintainerGitHub) EditIssueComment(_ context.Context, _ string, _ string, id int64, comment *github.IssueComment) (*github.IssueComment, *github.Response, error) {
	for _, existing := range f.comments {
		if existing.GetID() == id {
			existing.Body = comment.Body
			return existing, &github.Response{}, nil
		}
	}
	return nil, nil, nil
}

func (f *fakeMaintainerGitHub) GetIssue(context.Context, string, string, int) (*github.Issue, *github.Response, error) {
	return f.issue, &github.Response{}, nil
}

func (f *fakeMaintainerGitHub) AddLabelsToIssue(_ context.Context, _ string, _ string, _ int, labels []string) ([]*github.Label, *github.Response, error) {
	for _, label := range labels {
		f.issue.Labels = append(f.issue.Labels, &github.Label{Name: new(label)})
	}
	return f.issue.Labels, nil, nil
}

func (f *fakeMaintainerGitHub) EditIssue(_ context.Context, _ string, _ string, _ int, request *github.IssueRequest) (*github.Issue, *github.Response, error) {
	f.editedIssue++
	f.issue.State = request.State
	f.issue.StateReason = request.StateReason
	return f.issue, &github.Response{}, nil
}
