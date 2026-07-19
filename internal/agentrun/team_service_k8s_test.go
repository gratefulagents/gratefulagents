package agentrun

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/store"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

type teamLogsStateStore struct {
	store.StateStore
	session  *store.Session
	messages []store.Message
	recent   []store.ActivityEvent
}

func (s *teamLogsStateStore) GetSessionByRun(_ context.Context, name, ns string) (*store.Session, error) {
	if s.session == nil || s.session.AgentRunName != name || s.session.AgentRunNS != ns {
		return nil, errors.New("session not found")
	}
	return s.session, nil
}

func (s *teamLogsStateStore) GetMessages(_ context.Context, sessionID uuid.UUID) ([]store.Message, error) {
	if s.session == nil || s.session.ID != sessionID {
		return nil, errors.New("session not found")
	}
	return append([]store.Message(nil), s.messages...), nil
}

func (s *teamLogsStateStore) GetRecentActivity(_ context.Context, sessionID uuid.UUID, _ int32) ([]store.ActivityEvent, error) {
	if s.session == nil || s.session.ID != sessionID {
		return nil, errors.New("session not found")
	}
	return append([]store.ActivityEvent(nil), s.recent...), nil
}

func TestKubeTeamServiceCreateChildRunCopiesParentContract(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(platform): %v", err)
	}

	parent := newTeamParentRun("default", "parent-run")
	parent.Spec.RuntimeProfileRef = &platformv1alpha1.NamedRef{Name: "parent-profile"}
	parent.Spec.SpecArtifactRef = &platformv1alpha1.ArtifactRef{Kind: "ConfigMap", Name: "spec-artifact", Key: "spec.md"}
	parent.UID = types.UID("parent-uid")
	parent.Spec.Team.Steps[0].Tasks[0].Objective = "Implement the worker slice"
	parent.Spec.Team.Steps[0].Tasks[0].RuntimeProfileRef = &platformv1alpha1.NamedRef{Name: "child-profile"}

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&platformv1alpha1.AgentRun{}).
		WithObjects(parent).
		Build()
	service := NewKubeTeamService(c, scheme)

	status, err := service.CreateChildRun(context.Background(), CreateChildRunRequest{
		Parent:       ParentRunRef{Namespace: "default", Name: "parent-run"},
		StepName:     "parallel-implementers",
		TaskName:     "executor-a",
		Instructions: "Explore only and report the repository shape",
	})
	if err != nil {
		t.Fatalf("CreateChildRun() error = %v", err)
	}
	if status.Step != "parallel-implementers" {
		t.Fatalf("Step = %q, want parallel-implementers", status.Step)
	}
	if status.Role != "executor" {
		t.Fatalf("Role = %q, want executor", status.Role)
	}

	child := &platformv1alpha1.AgentRun{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: status.Name}, child); err != nil {
		t.Fatalf("Get(child) error = %v", err)
	}
	if child.Spec.ExecutionMode != platformv1alpha1.ExecutionModeLinear {
		t.Fatalf("ExecutionMode = %q, want linear", child.Spec.ExecutionMode)
	}
	if child.Spec.WorkflowMode != platformv1alpha1.WorkflowModeAuto {
		t.Fatalf("WorkflowMode = %q, want auto by default", child.Spec.WorkflowMode)
	}
	if child.Spec.Team != nil {
		t.Fatal("expected child team spec to be cleared")
	}
	if child.Spec.RuntimeProfileRef == nil || child.Spec.RuntimeProfileRef.Name != "child-profile" {
		t.Fatalf("RuntimeProfileRef = %#v, want child-profile", child.Spec.RuntimeProfileRef)
	}
	if got := strings.TrimSpace(child.Annotations[childAutonomousAnnotation]); got != "true" {
		t.Fatalf("child autonomous annotation = %q, want true for default auto mode", got)
	}
	if child.Labels[teamParentLabel] != parent.Name || child.Labels[teamTaskLabel] != "executor-a" {
		t.Fatalf("child labels = %v, want team lineage labels", child.Labels)
	}
	if len(child.OwnerReferences) != 1 || child.OwnerReferences[0].Name != parent.Name {
		t.Fatalf("OwnerReferences = %v, want parent owner ref", child.OwnerReferences)
	}
}

func TestKubeTeamServiceCreateChildRunAllowsAdHocChatParent(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(platform): %v", err)
	}

	parent := &platformv1alpha1.AgentRun{
		TypeMeta:   metav1.TypeMeta{APIVersion: platformv1alpha1.GroupVersion.String(), Kind: "AgentRun"},
		ObjectMeta: metav1.ObjectMeta{Name: "chat-run", Namespace: "default", UID: types.UID("chat-parent-uid")},
		Spec: platformv1alpha1.AgentRunSpec{
			WorkflowMode:  platformv1alpha1.WorkflowModeChat,
			ExecutionMode: platformv1alpha1.ExecutionModeLinear,
		},
	}

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&platformv1alpha1.AgentRun{}).
		WithObjects(parent).
		Build()
	service := NewKubeTeamService(c, scheme)

	status, err := service.CreateChildRun(context.Background(), CreateChildRunRequest{
		Parent:       ParentRunRef{Namespace: "default", Name: "chat-run"},
		StepName:     "chat-delegation",
		TaskName:     "worker-a",
		Instructions: "Investigate the failing migration and report findings.",
	})
	if err != nil {
		t.Fatalf("CreateChildRun() error = %v", err)
	}
	if status.Step != "chat-delegation" {
		t.Fatalf("Step = %q, want chat-delegation", status.Step)
	}
	if status.Role != "worker" {
		t.Fatalf("Role = %q, want worker", status.Role)
	}

	child := &platformv1alpha1.AgentRun{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: status.Name}, child); err != nil {
		t.Fatalf("Get(child) error = %v", err)
	}
	if child.Spec.ExecutionMode != platformv1alpha1.ExecutionModeLinear {
		t.Fatalf("ExecutionMode = %q, want linear", child.Spec.ExecutionMode)
	}
	if child.Spec.WorkflowMode != platformv1alpha1.WorkflowModeAuto {
		t.Fatalf("WorkflowMode = %q, want auto", child.Spec.WorkflowMode)
	}
	if child.Spec.Team != nil {
		t.Fatal("expected ad-hoc chat child to have no team spec")
	}
	if got := child.Annotations[childAutonomousAnnotation]; got != "true" {
		t.Fatalf("child autonomous annotation = %q, want true", got)
	}
	if child.Labels[teamParentLabel] != parent.Name || child.Labels[teamStepLabel] != "chat-delegation" || child.Labels[teamTaskLabel] != "worker-a" {
		t.Fatalf("child labels = %v, want chat lineage labels", child.Labels)
	}
}

func TestKubeTeamServiceCreateChildRunDefaultsToAutoWhenNotSpecified(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(platform): %v", err)
	}

	parent := &platformv1alpha1.AgentRun{
		TypeMeta:   metav1.TypeMeta{APIVersion: platformv1alpha1.GroupVersion.String(), Kind: "AgentRun"},
		ObjectMeta: metav1.ObjectMeta{Name: "chat-run", Namespace: "default", UID: types.UID("chat-parent-uid")},
		Spec: platformv1alpha1.AgentRunSpec{
			WorkflowMode:  platformv1alpha1.WorkflowModeChat,
			ExecutionMode: platformv1alpha1.ExecutionModeLinear,
		},
	}

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&platformv1alpha1.AgentRun{}).
		WithObjects(parent).
		Build()
	service := NewKubeTeamService(c, scheme)

	status, err := service.CreateChildRun(context.Background(), CreateChildRunRequest{
		Parent:       ParentRunRef{Namespace: "default", Name: "chat-run"},
		StepName:     "chat-delegation",
		TaskName:     "worker-b",
		Instructions: "Implement migration task and ask if blocked.",
	})
	if err != nil {
		t.Fatalf("CreateChildRun() error = %v", err)
	}

	child := &platformv1alpha1.AgentRun{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: status.Name}, child); err != nil {
		t.Fatalf("Get(child) error = %v", err)
	}
	if child.Spec.WorkflowMode != platformv1alpha1.WorkflowModeAuto {
		t.Fatalf("WorkflowMode = %q, want auto", child.Spec.WorkflowMode)
	}
	if got := strings.TrimSpace(child.Annotations[childAutonomousAnnotation]); got != "true" {
		t.Fatalf("child autonomous annotation = %q, want true", got)
	}
}

func TestKubeTeamServiceCreateChildRunAllowsAutonomyOverrideTrueForStructuredTeamParent(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(platform): %v", err)
	}

	parent := newTeamParentRun("default", "parent-run")
	parent.UID = types.UID("parent-uid")

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&platformv1alpha1.AgentRun{}).
		WithObjects(parent).
		Build()
	service := NewKubeTeamService(c, scheme)

	status, err := service.CreateChildRun(context.Background(), CreateChildRunRequest{
		Parent:       ParentRunRef{Namespace: "default", Name: "parent-run"},
		StepName:     "parallel-implementers",
		TaskName:     "executor-a",
		Instructions: "Explore architecture and report findings.",
	})
	if err != nil {
		t.Fatalf("CreateChildRun() error = %v", err)
	}

	child := &platformv1alpha1.AgentRun{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: status.Name}, child); err != nil {
		t.Fatalf("Get(child) error = %v", err)
	}
	if got := child.Annotations[childAutonomousAnnotation]; got != "true" {
		t.Fatalf("child autonomous annotation = %q, want true", got)
	}
}

func TestKubeTeamServiceRejectsForeignChildAccess(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(platform): %v", err)
	}

	parent := newTeamParentRun("default", "parent-run")
	parent.UID = types.UID("parent-uid")
	foreign := newLinearChildRun("default", "foreign-child")
	foreign.Labels = map[string]string{teamParentLabel: "someone-else", teamStepLabel: "parallel-implementers", teamRoleLabel: "executor"}

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&platformv1alpha1.AgentRun{}).
		WithObjects(parent, foreign).
		Build()
	service := NewKubeTeamService(c, scheme)

	_, err := service.GetChildRunStatus(context.Background(), GetChildRunStatusRequest{
		Parent: ParentRunRef{Namespace: "default", Name: "parent-run"},
		Child:  ChildRunRef{Namespace: "default", Name: "foreign-child"},
	})
	if !errors.Is(err, ErrChildRunNotOwned) {
		t.Fatalf("GetChildRunStatus() error = %v, want %v", err, ErrChildRunNotOwned)
	}
}

func TestKubeTeamServiceGetParentTeamStatusDerivesCounts(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(platform): %v", err)
	}

	parent := newTeamParentRun("default", "parent-run")
	parent.UID = types.UID("parent-uid")
	parent.Status.CurrentStep = "parallel-implementers"
	parent.Status.Phase = platformv1alpha1.AgentRunPhaseRunning
	parent.Status.Queue = &platformv1alpha1.AgentRunQueueStatus{State: "Running"}
	parent.Spec.Team.CompletionPolicy = &platformv1alpha1.AgentRunCompletionPolicy{RequireApproval: true}

	runningChild := newLinearChildRun("default", "child-running")
	runningChild.Labels = map[string]string{teamParentLabel: parent.Name, teamStepLabel: "parallel-implementers", teamRoleLabel: "executor"}
	runningChild.OwnerReferences = append(runningChild.OwnerReferences, metav1.OwnerReference{APIVersion: platformv1alpha1.GroupVersion.String(), Kind: "AgentRun", Name: parent.Name, UID: parent.UID})
	runningChild.Status.Phase = platformv1alpha1.AgentRunPhaseRunning

	failedChild := newLinearChildRun("default", "child-failed")
	failedChild.Labels = map[string]string{teamParentLabel: parent.Name, teamStepLabel: "parallel-implementers", teamRoleLabel: "reviewer"}
	failedChild.OwnerReferences = append(failedChild.OwnerReferences, metav1.OwnerReference{APIVersion: platformv1alpha1.GroupVersion.String(), Kind: "AgentRun", Name: parent.Name, UID: parent.UID})
	failedChild.Status.Phase = platformv1alpha1.AgentRunPhaseFailed

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&platformv1alpha1.AgentRun{}).
		WithObjects(parent, runningChild, failedChild).
		Build()
	service := NewKubeTeamService(c, scheme)

	summary, err := service.GetParentTeamStatus(context.Background(), GetParentTeamStatusRequest{Parent: ParentRunRef{Namespace: "default", Name: "parent-run"}})
	if err != nil {
		t.Fatalf("GetParentTeamStatus() error = %v", err)
	}
	if summary.CurrentStep != "parallel-implementers" {
		t.Fatalf("CurrentStep = %q, want parallel-implementers", summary.CurrentStep)
	}
	if summary.TotalChildren != 2 || summary.RunningChildren != 1 || summary.FailedChildren != 1 {
		t.Fatalf("summary counts = %#v, want total=2 running=1 failed=1", summary)
	}
	if summary.ApprovalState != "pending" {
		t.Fatalf("ApprovalState = %q, want pending", summary.ApprovalState)
	}
}

func TestKubeTeamServiceGetChildRunStatusIncludesReport(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(platform): %v", err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(core): %v", err)
	}

	parent := newTeamParentRun("default", "parent-run")
	parent.Spec.WorkflowMode = platformv1alpha1.WorkflowModeChat
	parent.UID = types.UID("parent-uid")
	child := newLinearChildRun("default", "child-plan")
	child.Labels = map[string]string{teamParentLabel: parent.Name, teamStepLabel: "parallel-implementers", teamRoleLabel: "explore"}
	child.OwnerReferences = append(child.OwnerReferences, metav1.OwnerReference{APIVersion: platformv1alpha1.GroupVersion.String(), Kind: "AgentRun", Name: parent.Name, UID: parent.UID})
	child.Status.Phase = platformv1alpha1.AgentRunPhaseSucceeded
	child.Status.Artifacts = &platformv1alpha1.AgentRunArtifacts{
		PlanRef: &platformv1alpha1.ArtifactRef{Kind: "ConfigMap", Name: "child-plan-plan", Key: "plan.md"},
	}
	report := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "child-plan-plan", Namespace: "default"},
		Data:       map[string]string{"plan.md": "# Findings\n- Repo uses Go"},
	}

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&platformv1alpha1.AgentRun{}).
		WithObjects(parent, child, report).
		Build()
	service := NewKubeTeamService(c, scheme)

	status, err := service.GetChildRunStatus(context.Background(), GetChildRunStatusRequest{
		Parent: ParentRunRef{Namespace: "default", Name: "parent-run"},
		Child:  ChildRunRef{Namespace: "default", Name: "child-plan"},
	})
	if err != nil {
		t.Fatalf("GetChildRunStatus() error = %v", err)
	}
	if status.Report != "# Findings\n- Repo uses Go" {
		t.Fatalf("Report = %q, want configmap report", status.Report)
	}
}

func TestKubeTeamServiceGetChildRunArtifactReadsOutputAndDiff(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(platform): %v", err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(core): %v", err)
	}

	parent := newTeamParentRun("default", "parent-run")
	parent.Spec.WorkflowMode = platformv1alpha1.WorkflowModeChat
	parent.UID = types.UID("parent-uid")
	child := newLinearChildRun("default", "child-plan")
	child.Labels = map[string]string{teamParentLabel: parent.Name, teamStepLabel: "parallel-implementers", teamRoleLabel: "explore"}
	child.OwnerReferences = append(child.OwnerReferences, metav1.OwnerReference{APIVersion: platformv1alpha1.GroupVersion.String(), Kind: "AgentRun", Name: parent.Name, UID: parent.UID})
	child.Status.Phase = platformv1alpha1.AgentRunPhaseSucceeded
	child.Status.Artifacts = &platformv1alpha1.AgentRunArtifacts{
		PlanRef: &platformv1alpha1.ArtifactRef{Kind: "ConfigMap", Name: "child-plan-plan", Key: "plan.md"},
		DiffURL: "s3://bucket/diff.patch",
	}
	plan := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "child-plan-plan", Namespace: "default"},
		Data:       map[string]string{"plan.md": "# Findings\n- Repo uses Go"},
	}

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&platformv1alpha1.AgentRun{}).
		WithObjects(parent, child, plan).
		Build()
	service := NewKubeTeamService(c, scheme)

	assertArtifact := func(selector, wantKind, wantContains string) {
		t.Helper()
		artifact, err := service.GetChildRunArtifact(context.Background(), GetChildRunArtifactRequest{
			Parent:   ParentRunRef{Namespace: "default", Name: "parent-run"},
			Child:    ChildRunRef{Namespace: "default", Name: "child-plan"},
			Artifact: selector,
		})
		if err != nil {
			t.Fatalf("GetChildRunArtifact(%q) error = %v", selector, err)
		}
		if artifact.Kind != wantKind {
			t.Fatalf("Artifact kind for %q = %q, want %q", selector, artifact.Kind, wantKind)
		}
		if !strings.Contains(artifact.Content, wantContains) {
			t.Fatalf("Artifact content for %q = %q, want contains %q", selector, artifact.Content, wantContains)
		}
	}

	assertArtifact("output", "ConfigMap", "# Findings")
	assertArtifact("diff", "URL", "s3://bucket/diff.patch")
}

func TestKubeTeamServiceGetChildRunArtifactRejectsLegacySelectors(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(platform): %v", err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(core): %v", err)
	}

	parent := newTeamParentRun("default", "parent-run")
	parent.Spec.WorkflowMode = platformv1alpha1.WorkflowModeChat
	parent.UID = types.UID("parent-uid")
	child := newLinearChildRun("default", "child-plan")
	child.Labels = map[string]string{teamParentLabel: parent.Name, teamStepLabel: "parallel-implementers", teamRoleLabel: "explore"}
	child.OwnerReferences = append(child.OwnerReferences, metav1.OwnerReference{APIVersion: platformv1alpha1.GroupVersion.String(), Kind: "AgentRun", Name: parent.Name, UID: parent.UID})
	child.Status.Phase = platformv1alpha1.AgentRunPhaseSucceeded
	child.Status.Artifacts = &platformv1alpha1.AgentRunArtifacts{
		PlanRef: &platformv1alpha1.ArtifactRef{Kind: "ConfigMap", Name: "child-plan-plan", Key: "plan.md"},
		DiffURL: "s3://bucket/diff.patch",
	}
	plan := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "child-plan-plan", Namespace: "default"},
		Data:       map[string]string{"plan.md": "# Findings\n- Repo uses Go"},
	}

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&platformv1alpha1.AgentRun{}).
		WithObjects(parent, child, plan).
		Build()
	service := NewKubeTeamService(c, scheme)

	for _, selector := range []string{"plan", "review", "spec", "activity-log"} {
		_, err := service.GetChildRunArtifact(context.Background(), GetChildRunArtifactRequest{
			Parent:   ParentRunRef{Namespace: "default", Name: "parent-run"},
			Child:    ChildRunRef{Namespace: "default", Name: "child-plan"},
			Artifact: selector,
		})
		if !errors.Is(err, ErrChildArtifactNotFound) {
			t.Fatalf("GetChildRunArtifact(%q) error = %v, want ErrChildArtifactNotFound", selector, err)
		}
	}
}

func TestKubeTeamServiceGetChildRunLogsIncludesDiagnosticsAndUnblockHint(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(platform): %v", err)
	}

	parent := newTeamParentRun("default", "parent-run")
	parent.UID = types.UID("parent-uid")
	child := newLinearChildRun("default", "child-blocked")
	child.Labels = map[string]string{teamParentLabel: parent.Name, teamStepLabel: "parallel-implementers", teamRoleLabel: "executor"}
	child.OwnerReferences = append(child.OwnerReferences, metav1.OwnerReference{APIVersion: platformv1alpha1.GroupVersion.String(), Kind: "AgentRun", Name: parent.Name, UID: parent.UID})
	child.Status.Phase = platformv1alpha1.AgentRunPhaseBlocked
	child.Status.LastError = "blocked on manual input"
	child.Status.Artifacts = &platformv1alpha1.AgentRunArtifacts{ActivityLogURL: "s3://bucket/activity.ndjson"}

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&platformv1alpha1.AgentRun{}).
		WithObjects(parent, child).
		Build()
	session := &store.Session{
		ID:               uuid.New(),
		AgentRunName:     child.Name,
		AgentRunNS:       child.Namespace,
		PendingInputType: "question",
		PendingQuestion:  "Please confirm migration strategy",
	}
	stateStore := &teamLogsStateStore{
		session: session,
		messages: []store.Message{
			{SessionID: session.ID, Role: "assistant", Content: "Please confirm migration strategy", CreatedAt: time.Unix(100, 0)},
		},
		recent: []store.ActivityEvent{
			{SessionID: session.ID, EventType: "assistant_text", Summary: "waiting for input", CreatedAt: time.Unix(101, 0)},
		},
	}
	service := NewKubeTeamService(c, scheme).WithStateStore(stateStore)

	logs, err := service.GetChildRunLogs(context.Background(), GetChildRunStatusRequest{
		Parent: ParentRunRef{Namespace: "default", Name: "parent-run"},
		Child:  ChildRunRef{Namespace: "default", Name: "child-blocked"},
	})
	if err != nil {
		t.Fatalf("GetChildRunLogs() error = %v", err)
	}
	if logs.Status.Phase != "Blocked" || logs.UserInputType == "" {
		t.Fatalf("logs = %#v, want blocked diagnostics", logs)
	}
	if !logs.CanUnblockWithRetry || logs.SuggestedUnblockAction != "retry_child_run" {
		t.Fatalf("unblock hint = %#v", logs)
	}
	if logs.ActivityLogURL == "" || len(logs.RecentActivity) != 1 || len(logs.ConversationTail) != 1 {
		t.Fatalf("logs payload incomplete = %#v", logs)
	}
}

func TestKubeTeamServiceGetChildRunLogsPrefersPostgresData(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(platform): %v", err)
	}

	parent := newTeamParentRun("default", "parent-run")
	parent.UID = types.UID("parent-uid")
	child := newLinearChildRun("default", "child-blocked")
	child.Labels = map[string]string{teamParentLabel: parent.Name, teamStepLabel: "parallel-implementers", teamRoleLabel: "executor"}
	child.OwnerReferences = append(child.OwnerReferences, metav1.OwnerReference{APIVersion: platformv1alpha1.GroupVersion.String(), Kind: "AgentRun", Name: parent.Name, UID: parent.UID})
	child.Status.Phase = platformv1alpha1.AgentRunPhaseBlocked
	child.Status.LastError = "blocked on manual input"

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&platformv1alpha1.AgentRun{}).
		WithObjects(parent, child).
		Build()

	session := &store.Session{
		ID:               uuid.New(),
		AgentRunName:     child.Name,
		AgentRunNS:       child.Namespace,
		PendingInputType: "approval",
		PendingQuestion:  "Approve the updated migration plan?",
	}
	stateStore := &teamLogsStateStore{
		session: session,
		messages: []store.Message{
			{SessionID: session.ID, Role: "user", Content: "Use strategy B.", CreatedAt: time.Unix(200, 0)},
			{SessionID: session.ID, Role: "assistant", Content: "Approve the updated migration plan?", CreatedAt: time.Unix(201, 0)},
		},
		recent: []store.ActivityEvent{
			{SessionID: session.ID, EventType: "tool_use", Summary: "Ran migration diff", CreatedAt: time.Unix(220, 0)},
		},
	}
	service := NewKubeTeamService(c, scheme).WithStateStore(stateStore)

	logs, err := service.GetChildRunLogs(context.Background(), GetChildRunStatusRequest{
		Parent: ParentRunRef{Namespace: "default", Name: "parent-run"},
		Child:  ChildRunRef{Namespace: "default", Name: "child-blocked"},
	})
	if err != nil {
		t.Fatalf("GetChildRunLogs() error = %v", err)
	}
	if logs.UserInputType != "approval" {
		t.Fatalf("UserInputType = %q, want approval from Postgres", logs.UserInputType)
	}
	if logs.PendingQuestion != "Approve the updated migration plan?" {
		t.Fatalf("PendingQuestion = %q", logs.PendingQuestion)
	}
	if len(logs.ConversationTail) != 2 || logs.ConversationTail[1].Content != "Approve the updated migration plan?" {
		t.Fatalf("ConversationTail = %#v, want Postgres-backed messages", logs.ConversationTail)
	}
	if len(logs.RecentActivity) != 1 || logs.RecentActivity[0].Summary != "Ran migration diff" {
		t.Fatalf("RecentActivity = %#v, want Postgres-backed activity", logs.RecentActivity)
	}
}

func TestKubeTeamServiceRetryChildRunResetsExecutionState(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(platform): %v", err)
	}

	parent := newTeamParentRun("default", "parent-run")
	parent.UID = types.UID("parent-uid")
	now := metav1.Now()
	child := newLinearChildRun("default", "child-running")
	child.Labels = map[string]string{teamParentLabel: parent.Name, teamStepLabel: "parallel-implementers", teamRoleLabel: "executor"}
	child.OwnerReferences = append(child.OwnerReferences, metav1.OwnerReference{APIVersion: platformv1alpha1.GroupVersion.String(), Kind: "AgentRun", Name: parent.Name, UID: parent.UID})
	child.Status.Phase = platformv1alpha1.AgentRunPhaseFailed
	child.Status.Queue = &platformv1alpha1.AgentRunQueueStatus{State: "Blocked", BlockedReason: "conflict"}
	child.Status.Sandbox = &platformv1alpha1.AgentRunSandboxStatus{SandboxRef: &platformv1alpha1.NamedRef{Name: "pod-1"}}
	child.Status.Artifacts = &platformv1alpha1.AgentRunArtifacts{DiffURL: "s3://diff"}
	child.Status.CurrentStep = "implementing"
	child.Status.LastError = "boom"
	child.Status.StartedAt = &now
	child.Status.CompletedAt = &now
	child.Status.RetryCount = 1

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&platformv1alpha1.AgentRun{}).
		WithObjects(parent, child).
		Build()
	service := NewKubeTeamService(c, scheme)

	status, err := service.RetryChildRun(context.Background(), RetryChildRunRequest{
		Parent: ParentRunRef{Namespace: "default", Name: "parent-run"},
		Child:  ChildRunRef{Namespace: "default", Name: "child-running"},
	})
	if err != nil {
		t.Fatalf("RetryChildRun() error = %v", err)
	}
	if status.Phase != string(platformv1alpha1.AgentRunPhasePending) {
		t.Fatalf("Phase = %q, want Pending", status.Phase)
	}

	updated := &platformv1alpha1.AgentRun{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: "child-running"}, updated); err != nil {
		t.Fatalf("Get(updated child) error = %v", err)
	}
	if updated.Status.Queue == nil || updated.Status.Queue.State != "Queued" {
		t.Fatalf("Queue = %#v, want queued state", updated.Status.Queue)
	}
	if updated.Status.Sandbox != nil || updated.Status.Artifacts != nil {
		t.Fatalf("expected sandbox/artifacts to be cleared, got sandbox=%#v artifacts=%#v", updated.Status.Sandbox, updated.Status.Artifacts)
	}
	if updated.Status.CurrentStep != "" || updated.Status.LastError != "" {
		t.Fatalf("expected transient status to be cleared, got step=%q error=%q", updated.Status.CurrentStep, updated.Status.LastError)
	}
	if updated.Status.StartedAt != nil || updated.Status.CompletedAt != nil {
		t.Fatalf("expected execution timestamps to be cleared, got started=%v completed=%v", updated.Status.StartedAt, updated.Status.CompletedAt)
	}
	if updated.Status.RetryCount != 2 {
		t.Fatalf("RetryCount = %d, want 2", updated.Status.RetryCount)
	}
}

func TestKubeTeamServiceSendMessageToChildWritesToPostgres(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(platform): %v", err)
	}

	parent := newTeamParentRun("default", "parent-run")
	parent.UID = types.UID("parent-uid")
	child := newLinearChildRun("default", "child-blocked")
	child.Labels = map[string]string{teamParentLabel: parent.Name, teamStepLabel: "parallel-implementers", teamRoleLabel: "executor"}
	child.OwnerReferences = append(child.OwnerReferences, metav1.OwnerReference{APIVersion: platformv1alpha1.GroupVersion.String(), Kind: "AgentRun", Name: parent.Name, UID: parent.UID})
	child.Status.Phase = platformv1alpha1.AgentRunPhaseBlocked

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&platformv1alpha1.AgentRun{}).
		WithObjects(parent, child).
		Build()
	service := NewKubeTeamService(c, scheme)

	// Without stateStore, SendMessageToChild should error.
	_, err := service.SendMessageToChild(context.Background(), SendMessageToChildRequest{
		Parent:  ParentRunRef{Namespace: "default", Name: "parent-run"},
		Child:   ChildRunRef{Namespace: "default", Name: "child-blocked"},
		Message: "Use migration strategy B and continue.",
	})
	if err == nil {
		t.Fatal("expected error when stateStore is nil")
	}
}

func TestKubeTeamServiceWaitForRunChangeHonorsUntilPhases(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(platform): %v", err)
	}

	parent := newTeamParentRun("default", "parent-run")
	parent.UID = types.UID("parent-uid")
	parent.Status.Phase = platformv1alpha1.AgentRunPhaseRunning
	child := newLinearChildRun("default", "child-succeeded")
	child.Labels = map[string]string{teamParentLabel: parent.Name, teamStepLabel: "parallel-implementers", teamRoleLabel: "executor"}
	child.OwnerReferences = append(child.OwnerReferences, metav1.OwnerReference{APIVersion: platformv1alpha1.GroupVersion.String(), Kind: "AgentRun", Name: parent.Name, UID: parent.UID})
	child.Status.Phase = platformv1alpha1.AgentRunPhaseSucceeded

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&platformv1alpha1.AgentRun{}).
		WithObjects(parent, child).
		Build()
	service := NewKubeTeamService(c, scheme)
	service.pollInterval = 10 * time.Millisecond

	resp, err := service.WaitForRunChange(context.Background(), WaitForRunChangeRequest{
		Parent:      ParentRunRef{Namespace: "default", Name: "parent-run"},
		Scope:       "children",
		UntilPhases: []string{"Succeeded"},
		TimeoutMS:   50,
	})
	if err != nil {
		t.Fatalf("WaitForRunChange() error = %v", err)
	}
	if !resp.Changed {
		t.Fatal("Changed = false, want true")
	}
	if resp.Phase != "Succeeded" {
		t.Fatalf("Phase = %q, want Succeeded", resp.Phase)
	}
	if resp.RunName == "" {
		t.Fatal("RunName = empty, want child scope fingerprint/name")
	}
}

func TestKubeTeamServiceWaitForRunChangeRejectsInvalidScope(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(platform): %v", err)
	}

	parent := newTeamParentRun("default", "parent-run")
	parent.UID = types.UID("parent-uid")

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&platformv1alpha1.AgentRun{}).
		WithObjects(parent).
		Build()
	service := NewKubeTeamService(c, scheme)

	_, err := service.WaitForRunChange(context.Background(), WaitForRunChangeRequest{
		Parent: ParentRunRef{Namespace: "default", Name: "parent-run"},
		Scope:  "bogus",
	})
	if !errors.Is(err, ErrTeamScopeInvalid) {
		t.Fatalf("WaitForRunChange() error = %v, want %v", err, ErrTeamScopeInvalid)
	}
}

func TestKubeTeamServiceLoadParentRejectsRuntimeParentMismatch(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(platform): %v", err)
	}

	t.Setenv("AGENTRUN_PARENT_NAMESPACE", "default")
	t.Setenv("AGENTRUN_PARENT_NAME", "expected-parent")

	parent := newTeamParentRun("default", "actual-parent")
	parent.UID = types.UID("parent-uid")

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&platformv1alpha1.AgentRun{}).
		WithObjects(parent).
		Build()
	service := NewKubeTeamService(c, scheme)

	_, err := service.GetParentTeamStatus(context.Background(), GetParentTeamStatusRequest{
		Parent: ParentRunRef{Namespace: "default", Name: "actual-parent"},
	})
	if !errors.Is(err, ErrParentScopeMismatch) {
		t.Fatalf("GetParentTeamStatus() error = %v, want %v", err, ErrParentScopeMismatch)
	}
}

func TestKubeTeamServiceLoadOwnedChildRejectsCrossNamespace(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(platform): %v", err)
	}

	parent := newTeamParentRun("default", "parent-run")
	parent.UID = types.UID("parent-uid")
	child := newLinearChildRun("other", "child-a")
	child.Labels = map[string]string{teamParentLabel: parent.Name, teamStepLabel: "parallel-implementers", teamRoleLabel: "executor"}
	child.OwnerReferences = append(child.OwnerReferences, metav1.OwnerReference{APIVersion: platformv1alpha1.GroupVersion.String(), Kind: "AgentRun", Name: parent.Name, UID: parent.UID})

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&platformv1alpha1.AgentRun{}).
		WithObjects(parent, child).
		Build()
	service := NewKubeTeamService(c, scheme)

	_, err := service.GetChildRunStatus(context.Background(), GetChildRunStatusRequest{
		Parent: ParentRunRef{Namespace: "default", Name: "parent-run"},
		Child:  ChildRunRef{Namespace: "other", Name: "child-a"},
	})
	if !errors.Is(err, ErrChildCrossNamespace) {
		t.Fatalf("GetChildRunStatus() error = %v, want %v", err, ErrChildCrossNamespace)
	}
}

func newTeamParentRun(namespace, name string) *platformv1alpha1.AgentRun {
	return &platformv1alpha1.AgentRun{
		TypeMeta:   metav1.TypeMeta{APIVersion: platformv1alpha1.GroupVersion.String(), Kind: "AgentRun"},
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: platformv1alpha1.AgentRunSpec{
			WorkflowMode:  platformv1alpha1.WorkflowModeChat,
			ExecutionMode: platformv1alpha1.ExecutionModeTeam,
			Team: &platformv1alpha1.AgentRunTeamSpec{
				Steps: []platformv1alpha1.AgentRunTeamStep{{
					Name: "parallel-implementers",
					Type: platformv1alpha1.TeamStepTypeParallel,
					Tasks: []platformv1alpha1.AgentRunTeamTask{{
						Name: "executor-a",
						Role: "executor",
					}},
				}},
				DelegationPolicy: &platformv1alpha1.AgentRunDelegationPolicy{ParentOnly: true},
			},
		},
	}
}

func newLinearChildRun(namespace, name string) *platformv1alpha1.AgentRun {
	return &platformv1alpha1.AgentRun{
		TypeMeta:   metav1.TypeMeta{APIVersion: platformv1alpha1.GroupVersion.String(), Kind: "AgentRun"},
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: platformv1alpha1.AgentRunSpec{
			WorkflowMode:  platformv1alpha1.WorkflowModeChat,
			ExecutionMode: platformv1alpha1.ExecutionModeLinear,
		},
	}
}

// --- Phase 1 policy enforcement tests ---

func TestKubeTeamServiceCreateChildRunEnforcesDependsOnReadiness(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(platform): %v", err)
	}

	parent := &platformv1alpha1.AgentRun{
		TypeMeta:   metav1.TypeMeta{APIVersion: platformv1alpha1.GroupVersion.String(), Kind: "AgentRun"},
		ObjectMeta: metav1.ObjectMeta{Name: "parent-run", Namespace: "default", UID: types.UID("parent-uid")},
		Spec: platformv1alpha1.AgentRunSpec{
			WorkflowMode:  platformv1alpha1.WorkflowModeChat,
			ExecutionMode: platformv1alpha1.ExecutionModeTeam,
			Team: &platformv1alpha1.AgentRunTeamSpec{
				Steps: []platformv1alpha1.AgentRunTeamStep{{
					Name: "step-1",
					Type: platformv1alpha1.TeamStepTypeParallel,
					Tasks: []platformv1alpha1.AgentRunTeamTask{
						{Name: "upstream-task", Role: "executor", Objective: "Do upstream work"},
						{Name: "downstream-task", Role: "executor", Objective: "Do downstream work", DependsOn: []string{"upstream-task"}},
					},
				}},
				DelegationPolicy: &platformv1alpha1.AgentRunDelegationPolicy{ParentOnly: true},
			},
		},
	}

	t.Run("rejects when dependency child does not exist", func(t *testing.T) {
		c := fake.NewClientBuilder().
			WithScheme(scheme).
			WithStatusSubresource(&platformv1alpha1.AgentRun{}).
			WithObjects(parent.DeepCopy()).
			Build()
		service := NewKubeTeamService(c, scheme)

		_, err := service.CreateChildRun(context.Background(), CreateChildRunRequest{
			Parent:   ParentRunRef{Namespace: "default", Name: "parent-run"},
			StepName: "step-1",
			TaskName: "downstream-task",
		})
		if err == nil {
			t.Fatal("expected error for missing dependency")
		}
		if !errors.Is(err, ErrDependencyNotReady) {
			t.Fatalf("expected ErrDependencyNotReady, got: %v", err)
		}
	})

	t.Run("rejects when dependency child exists but not succeeded", func(t *testing.T) {
		parentCopy := parent.DeepCopy()
		upstreamChild := &platformv1alpha1.AgentRun{
			TypeMeta: metav1.TypeMeta{APIVersion: platformv1alpha1.GroupVersion.String(), Kind: "AgentRun"},
			ObjectMeta: metav1.ObjectMeta{
				Name: buildTeamChildName(parentCopy.Name, "step-1", "upstream-task"), Namespace: "default",
				Labels:          map[string]string{teamParentLabel: parentCopy.Name, teamStepLabel: "step-1", teamTaskLabel: "upstream-task"},
				OwnerReferences: []metav1.OwnerReference{{APIVersion: platformv1alpha1.GroupVersion.String(), Kind: "AgentRun", Name: parentCopy.Name, UID: parentCopy.UID}},
			},
			Spec:   platformv1alpha1.AgentRunSpec{WorkflowMode: platformv1alpha1.WorkflowModeAuto, ExecutionMode: platformv1alpha1.ExecutionModeLinear},
			Status: platformv1alpha1.AgentRunStatus{Phase: platformv1alpha1.AgentRunPhaseRunning},
		}

		c := fake.NewClientBuilder().
			WithScheme(scheme).
			WithStatusSubresource(&platformv1alpha1.AgentRun{}).
			WithObjects(parentCopy, upstreamChild).
			Build()
		service := NewKubeTeamService(c, scheme)

		_, err := service.CreateChildRun(context.Background(), CreateChildRunRequest{
			Parent:   ParentRunRef{Namespace: "default", Name: "parent-run"},
			StepName: "step-1",
			TaskName: "downstream-task",
		})
		if err == nil {
			t.Fatal("expected error for non-succeeded dependency")
		}
		if !errors.Is(err, ErrDependencyNotReady) {
			t.Fatalf("expected ErrDependencyNotReady, got: %v", err)
		}
	})

	t.Run("allows creation when dependency is succeeded", func(t *testing.T) {
		parentCopy := parent.DeepCopy()
		upstreamChild := &platformv1alpha1.AgentRun{
			TypeMeta: metav1.TypeMeta{APIVersion: platformv1alpha1.GroupVersion.String(), Kind: "AgentRun"},
			ObjectMeta: metav1.ObjectMeta{
				Name: buildTeamChildName(parentCopy.Name, "step-1", "upstream-task"), Namespace: "default",
				Labels:          map[string]string{teamParentLabel: parentCopy.Name, teamStepLabel: "step-1", teamTaskLabel: "upstream-task"},
				OwnerReferences: []metav1.OwnerReference{{APIVersion: platformv1alpha1.GroupVersion.String(), Kind: "AgentRun", Name: parentCopy.Name, UID: parentCopy.UID}},
			},
			Spec:   platformv1alpha1.AgentRunSpec{WorkflowMode: platformv1alpha1.WorkflowModeAuto, ExecutionMode: platformv1alpha1.ExecutionModeLinear},
			Status: platformv1alpha1.AgentRunStatus{Phase: platformv1alpha1.AgentRunPhaseSucceeded},
		}

		c := fake.NewClientBuilder().
			WithScheme(scheme).
			WithStatusSubresource(&platformv1alpha1.AgentRun{}).
			WithObjects(parentCopy, upstreamChild).
			Build()
		service := NewKubeTeamService(c, scheme)

		status, err := service.CreateChildRun(context.Background(), CreateChildRunRequest{
			Parent:   ParentRunRef{Namespace: "default", Name: "parent-run"},
			StepName: "step-1",
			TaskName: "downstream-task",
		})
		if err != nil {
			t.Fatalf("CreateChildRun() error = %v", err)
		}
		if status.Step != "step-1" {
			t.Fatalf("Step = %q, want step-1", status.Step)
		}
	})
}

func TestKubeTeamServiceCreateChildRunEnforcesArtifactContract(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(platform): %v", err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(core): %v", err)
	}

	parent := &platformv1alpha1.AgentRun{
		TypeMeta:   metav1.TypeMeta{APIVersion: platformv1alpha1.GroupVersion.String(), Kind: "AgentRun"},
		ObjectMeta: metav1.ObjectMeta{Name: "parent-run", Namespace: "default", UID: types.UID("parent-uid")},
		Spec: platformv1alpha1.AgentRunSpec{
			WorkflowMode:  platformv1alpha1.WorkflowModeChat,
			ExecutionMode: platformv1alpha1.ExecutionModeTeam,
			Team: &platformv1alpha1.AgentRunTeamSpec{
				Steps: []platformv1alpha1.AgentRunTeamStep{{
					Name: "step-1",
					Type: platformv1alpha1.TeamStepTypeParallel,
					Tasks: []platformv1alpha1.AgentRunTeamTask{
						{Name: "producer", Role: "executor", Objective: "Produce output", ArtifactContract: "output"},
						{Name: "consumer", Role: "executor", Objective: "Consume output", DependsOn: []string{"producer"}},
					},
				}},
				DelegationPolicy: &platformv1alpha1.AgentRunDelegationPolicy{ParentOnly: true},
			},
		},
	}

	t.Run("rejects when upstream artifact contract is not satisfied", func(t *testing.T) {
		parentCopy := parent.DeepCopy()
		// upstream child succeeded but has no artifact
		producerChild := &platformv1alpha1.AgentRun{
			TypeMeta: metav1.TypeMeta{APIVersion: platformv1alpha1.GroupVersion.String(), Kind: "AgentRun"},
			ObjectMeta: metav1.ObjectMeta{
				Name: buildTeamChildName(parentCopy.Name, "step-1", "producer"), Namespace: "default",
				Labels:          map[string]string{teamParentLabel: parentCopy.Name, teamStepLabel: "step-1", teamTaskLabel: "producer"},
				OwnerReferences: []metav1.OwnerReference{{APIVersion: platformv1alpha1.GroupVersion.String(), Kind: "AgentRun", Name: parentCopy.Name, UID: parentCopy.UID}},
			},
			Spec:   platformv1alpha1.AgentRunSpec{WorkflowMode: platformv1alpha1.WorkflowModeAuto, ExecutionMode: platformv1alpha1.ExecutionModeLinear},
			Status: platformv1alpha1.AgentRunStatus{Phase: platformv1alpha1.AgentRunPhaseSucceeded},
		}

		c := fake.NewClientBuilder().
			WithScheme(scheme).
			WithStatusSubresource(&platformv1alpha1.AgentRun{}).
			WithObjects(parentCopy, producerChild).
			Build()
		service := NewKubeTeamService(c, scheme)

		_, err := service.CreateChildRun(context.Background(), CreateChildRunRequest{
			Parent:   ParentRunRef{Namespace: "default", Name: "parent-run"},
			StepName: "step-1",
			TaskName: "consumer",
		})
		if err == nil {
			t.Fatal("expected error for unsatisfied artifact contract")
		}
		if !errors.Is(err, ErrArtifactContractNotMet) {
			t.Fatalf("expected ErrArtifactContractNotMet, got: %v", err)
		}
	})

	t.Run("allows when upstream artifact contract is satisfied", func(t *testing.T) {
		parentCopy := parent.DeepCopy()
		producerChild := &platformv1alpha1.AgentRun{
			TypeMeta: metav1.TypeMeta{APIVersion: platformv1alpha1.GroupVersion.String(), Kind: "AgentRun"},
			ObjectMeta: metav1.ObjectMeta{
				Name: buildTeamChildName(parentCopy.Name, "step-1", "producer"), Namespace: "default",
				Labels:          map[string]string{teamParentLabel: parentCopy.Name, teamStepLabel: "step-1", teamTaskLabel: "producer"},
				OwnerReferences: []metav1.OwnerReference{{APIVersion: platformv1alpha1.GroupVersion.String(), Kind: "AgentRun", Name: parentCopy.Name, UID: parentCopy.UID}},
			},
			Spec: platformv1alpha1.AgentRunSpec{WorkflowMode: platformv1alpha1.WorkflowModeAuto, ExecutionMode: platformv1alpha1.ExecutionModeLinear},
			Status: platformv1alpha1.AgentRunStatus{
				Phase:     platformv1alpha1.AgentRunPhaseSucceeded,
				Artifacts: &platformv1alpha1.AgentRunArtifacts{PlanRef: &platformv1alpha1.ArtifactRef{Kind: "ConfigMap", Name: "producer-output", Key: "plan.md"}},
			},
		}
		outputCM := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: "producer-output", Namespace: "default"},
			Data:       map[string]string{"plan.md": "output content"},
		}

		c := fake.NewClientBuilder().
			WithScheme(scheme).
			WithStatusSubresource(&platformv1alpha1.AgentRun{}).
			WithObjects(parentCopy, producerChild, outputCM).
			Build()
		service := NewKubeTeamService(c, scheme)

		status, err := service.CreateChildRun(context.Background(), CreateChildRunRequest{
			Parent:   ParentRunRef{Namespace: "default", Name: "parent-run"},
			StepName: "step-1",
			TaskName: "consumer",
		})
		if err != nil {
			t.Fatalf("CreateChildRun() error = %v", err)
		}
		if status.Step != "step-1" {
			t.Fatalf("Step = %q, want step-1", status.Step)
		}
	})
}

func TestKubeTeamServiceRetryChildRunEnforcesMaxRetriesBudget(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(platform): %v", err)
	}

	parent := &platformv1alpha1.AgentRun{
		TypeMeta:   metav1.TypeMeta{APIVersion: platformv1alpha1.GroupVersion.String(), Kind: "AgentRun"},
		ObjectMeta: metav1.ObjectMeta{Name: "parent-run", Namespace: "default", UID: types.UID("parent-uid")},
		Spec: platformv1alpha1.AgentRunSpec{
			WorkflowMode:  platformv1alpha1.WorkflowModeChat,
			ExecutionMode: platformv1alpha1.ExecutionModeTeam,
			Team: &platformv1alpha1.AgentRunTeamSpec{
				Steps: []platformv1alpha1.AgentRunTeamStep{{
					Name: "step-1",
					Type: platformv1alpha1.TeamStepTypeParallel,
					Tasks: []platformv1alpha1.AgentRunTeamTask{
						{Name: "limited-task", Role: "executor", Objective: "Do work", MaxRetries: 2},
					},
				}},
				DelegationPolicy: &platformv1alpha1.AgentRunDelegationPolicy{ParentOnly: true},
			},
		},
	}

	t.Run("rejects when retry budget is exhausted", func(t *testing.T) {
		parentCopy := parent.DeepCopy()
		child := &platformv1alpha1.AgentRun{
			TypeMeta: metav1.TypeMeta{APIVersion: platformv1alpha1.GroupVersion.String(), Kind: "AgentRun"},
			ObjectMeta: metav1.ObjectMeta{
				Name: "child-limited", Namespace: "default",
				Labels:          map[string]string{teamParentLabel: parentCopy.Name, teamStepLabel: "step-1", teamTaskLabel: "limited-task"},
				OwnerReferences: []metav1.OwnerReference{{APIVersion: platformv1alpha1.GroupVersion.String(), Kind: "AgentRun", Name: parentCopy.Name, UID: parentCopy.UID}},
			},
			Spec:   platformv1alpha1.AgentRunSpec{WorkflowMode: platformv1alpha1.WorkflowModeAuto, ExecutionMode: platformv1alpha1.ExecutionModeLinear},
			Status: platformv1alpha1.AgentRunStatus{Phase: platformv1alpha1.AgentRunPhaseFailed, RetryCount: 2},
		}

		c := fake.NewClientBuilder().
			WithScheme(scheme).
			WithStatusSubresource(&platformv1alpha1.AgentRun{}).
			WithObjects(parentCopy, child).
			Build()
		service := NewKubeTeamService(c, scheme)

		_, err := service.RetryChildRun(context.Background(), RetryChildRunRequest{
			Parent: ParentRunRef{Namespace: "default", Name: "parent-run"},
			Child:  ChildRunRef{Namespace: "default", Name: "child-limited"},
		})
		if err == nil {
			t.Fatal("expected error for exhausted retry budget")
		}
		if !errors.Is(err, ErrRetryBudgetExhausted) {
			t.Fatalf("expected ErrRetryBudgetExhausted, got: %v", err)
		}
	})

	t.Run("allows retry when budget remains", func(t *testing.T) {
		parentCopy := parent.DeepCopy()
		child := &platformv1alpha1.AgentRun{
			TypeMeta: metav1.TypeMeta{APIVersion: platformv1alpha1.GroupVersion.String(), Kind: "AgentRun"},
			ObjectMeta: metav1.ObjectMeta{
				Name: "child-limited", Namespace: "default",
				Labels:          map[string]string{teamParentLabel: parentCopy.Name, teamStepLabel: "step-1", teamTaskLabel: "limited-task"},
				OwnerReferences: []metav1.OwnerReference{{APIVersion: platformv1alpha1.GroupVersion.String(), Kind: "AgentRun", Name: parentCopy.Name, UID: parentCopy.UID}},
			},
			Spec:   platformv1alpha1.AgentRunSpec{WorkflowMode: platformv1alpha1.WorkflowModeAuto, ExecutionMode: platformv1alpha1.ExecutionModeLinear},
			Status: platformv1alpha1.AgentRunStatus{Phase: platformv1alpha1.AgentRunPhaseFailed, RetryCount: 1},
		}

		c := fake.NewClientBuilder().
			WithScheme(scheme).
			WithStatusSubresource(&platformv1alpha1.AgentRun{}).
			WithObjects(parentCopy, child).
			Build()
		service := NewKubeTeamService(c, scheme)

		status, err := service.RetryChildRun(context.Background(), RetryChildRunRequest{
			Parent: ParentRunRef{Namespace: "default", Name: "parent-run"},
			Child:  ChildRunRef{Namespace: "default", Name: "child-limited"},
		})
		if err != nil {
			t.Fatalf("RetryChildRun() error = %v", err)
		}
		if status.Phase != string(platformv1alpha1.AgentRunPhasePending) {
			t.Fatalf("Phase = %q, want Pending", status.Phase)
		}
	})

	t.Run("allows unlimited retries when maxRetries is zero", func(t *testing.T) {
		unlimitedParent := parent.DeepCopy()
		unlimitedParent.Spec.Team.Steps[0].Tasks[0].MaxRetries = 0
		child := &platformv1alpha1.AgentRun{
			TypeMeta: metav1.TypeMeta{APIVersion: platformv1alpha1.GroupVersion.String(), Kind: "AgentRun"},
			ObjectMeta: metav1.ObjectMeta{
				Name: "child-limited", Namespace: "default",
				Labels:          map[string]string{teamParentLabel: unlimitedParent.Name, teamStepLabel: "step-1", teamTaskLabel: "limited-task"},
				OwnerReferences: []metav1.OwnerReference{{APIVersion: platformv1alpha1.GroupVersion.String(), Kind: "AgentRun", Name: unlimitedParent.Name, UID: unlimitedParent.UID}},
			},
			Spec:   platformv1alpha1.AgentRunSpec{WorkflowMode: platformv1alpha1.WorkflowModeAuto, ExecutionMode: platformv1alpha1.ExecutionModeLinear},
			Status: platformv1alpha1.AgentRunStatus{Phase: platformv1alpha1.AgentRunPhaseFailed, RetryCount: 100},
		}

		c := fake.NewClientBuilder().
			WithScheme(scheme).
			WithStatusSubresource(&platformv1alpha1.AgentRun{}).
			WithObjects(unlimitedParent, child).
			Build()
		service := NewKubeTeamService(c, scheme)

		_, err := service.RetryChildRun(context.Background(), RetryChildRunRequest{
			Parent: ParentRunRef{Namespace: "default", Name: "parent-run"},
			Child:  ChildRunRef{Namespace: "default", Name: "child-limited"},
		})
		if err != nil {
			t.Fatalf("RetryChildRun() error = %v, want nil (unlimited)", err)
		}
	})
}

func TestKubeTeamServiceCreateChildRunEnforcesMaxChildren(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(platform): %v", err)
	}

	parent := &platformv1alpha1.AgentRun{
		TypeMeta:   metav1.TypeMeta{APIVersion: platformv1alpha1.GroupVersion.String(), Kind: "AgentRun"},
		ObjectMeta: metav1.ObjectMeta{Name: "parent-run", Namespace: "default", UID: types.UID("parent-uid")},
		Spec: platformv1alpha1.AgentRunSpec{
			WorkflowMode:  platformv1alpha1.WorkflowModeChat,
			ExecutionMode: platformv1alpha1.ExecutionModeTeam,
			Team: &platformv1alpha1.AgentRunTeamSpec{
				Steps: []platformv1alpha1.AgentRunTeamStep{{
					Name: "step-1",
					Type: platformv1alpha1.TeamStepTypeParallel,
					Tasks: []platformv1alpha1.AgentRunTeamTask{
						{Name: "task-a", Role: "executor"},
						{Name: "task-b", Role: "executor"},
						{Name: "task-c", Role: "executor"},
					},
				}},
				DelegationPolicy: &platformv1alpha1.AgentRunDelegationPolicy{ParentOnly: true, MaxChildren: 2},
			},
		},
	}

	t.Run("rejects when maxChildren is reached", func(t *testing.T) {
		parentCopy := parent.DeepCopy()
		existingA := &platformv1alpha1.AgentRun{
			TypeMeta: metav1.TypeMeta{APIVersion: platformv1alpha1.GroupVersion.String(), Kind: "AgentRun"},
			ObjectMeta: metav1.ObjectMeta{
				Name: "parent-run-step-1-task-a", Namespace: "default",
				Labels:          map[string]string{teamParentLabel: parentCopy.Name, teamStepLabel: "step-1", teamTaskLabel: "task-a"},
				OwnerReferences: []metav1.OwnerReference{{APIVersion: platformv1alpha1.GroupVersion.String(), Kind: "AgentRun", Name: parentCopy.Name, UID: parentCopy.UID}},
			},
			Spec: platformv1alpha1.AgentRunSpec{WorkflowMode: platformv1alpha1.WorkflowModeAuto, ExecutionMode: platformv1alpha1.ExecutionModeLinear},
		}
		existingB := &platformv1alpha1.AgentRun{
			TypeMeta: metav1.TypeMeta{APIVersion: platformv1alpha1.GroupVersion.String(), Kind: "AgentRun"},
			ObjectMeta: metav1.ObjectMeta{
				Name: "parent-run-step-1-task-b", Namespace: "default",
				Labels:          map[string]string{teamParentLabel: parentCopy.Name, teamStepLabel: "step-1", teamTaskLabel: "task-b"},
				OwnerReferences: []metav1.OwnerReference{{APIVersion: platformv1alpha1.GroupVersion.String(), Kind: "AgentRun", Name: parentCopy.Name, UID: parentCopy.UID}},
			},
			Spec: platformv1alpha1.AgentRunSpec{WorkflowMode: platformv1alpha1.WorkflowModeAuto, ExecutionMode: platformv1alpha1.ExecutionModeLinear},
		}

		c := fake.NewClientBuilder().
			WithScheme(scheme).
			WithStatusSubresource(&platformv1alpha1.AgentRun{}).
			WithObjects(parentCopy, existingA, existingB).
			Build()
		service := NewKubeTeamService(c, scheme)

		_, err := service.CreateChildRun(context.Background(), CreateChildRunRequest{
			Parent:   ParentRunRef{Namespace: "default", Name: "parent-run"},
			StepName: "step-1",
			TaskName: "task-c",
		})
		if err == nil {
			t.Fatal("expected error for maxChildren exceeded")
		}
		if !errors.Is(err, ErrMaxChildrenExceeded) {
			t.Fatalf("expected ErrMaxChildrenExceeded, got: %v", err)
		}
	})

	t.Run("allows creation when under maxChildren limit", func(t *testing.T) {
		parentCopy := parent.DeepCopy()
		existingA := &platformv1alpha1.AgentRun{
			TypeMeta: metav1.TypeMeta{APIVersion: platformv1alpha1.GroupVersion.String(), Kind: "AgentRun"},
			ObjectMeta: metav1.ObjectMeta{
				Name: "parent-run-step-1-task-a", Namespace: "default",
				Labels:          map[string]string{teamParentLabel: parentCopy.Name, teamStepLabel: "step-1", teamTaskLabel: "task-a"},
				OwnerReferences: []metav1.OwnerReference{{APIVersion: platformv1alpha1.GroupVersion.String(), Kind: "AgentRun", Name: parentCopy.Name, UID: parentCopy.UID}},
			},
			Spec: platformv1alpha1.AgentRunSpec{WorkflowMode: platformv1alpha1.WorkflowModeAuto, ExecutionMode: platformv1alpha1.ExecutionModeLinear},
		}

		c := fake.NewClientBuilder().
			WithScheme(scheme).
			WithStatusSubresource(&platformv1alpha1.AgentRun{}).
			WithObjects(parentCopy, existingA).
			Build()
		service := NewKubeTeamService(c, scheme)

		status, err := service.CreateChildRun(context.Background(), CreateChildRunRequest{
			Parent:   ParentRunRef{Namespace: "default", Name: "parent-run"},
			StepName: "step-1",
			TaskName: "task-b",
		})
		if err != nil {
			t.Fatalf("CreateChildRun() error = %v", err)
		}
		if status.Step != "step-1" {
			t.Fatalf("Step = %q, want step-1", status.Step)
		}
	})
}

func TestKubeTeamServiceCreateChildRunEnforcesMaxDepth(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(platform): %v", err)
	}

	// Parent that is itself a team child (has team-parent label) trying to create a grandchild.
	parent := &platformv1alpha1.AgentRun{
		TypeMeta: metav1.TypeMeta{APIVersion: platformv1alpha1.GroupVersion.String(), Kind: "AgentRun"},
		ObjectMeta: metav1.ObjectMeta{
			Name: "nested-parent", Namespace: "default", UID: types.UID("nested-uid"),
			Labels: map[string]string{teamParentLabel: "grandparent"},
		},
		Spec: platformv1alpha1.AgentRunSpec{
			WorkflowMode:  platformv1alpha1.WorkflowModeChat,
			ExecutionMode: platformv1alpha1.ExecutionModeTeam,
			Team: &platformv1alpha1.AgentRunTeamSpec{
				Steps: []platformv1alpha1.AgentRunTeamStep{{
					Name:  "step-1",
					Type:  platformv1alpha1.TeamStepTypeParallel,
					Tasks: []platformv1alpha1.AgentRunTeamTask{{Name: "task-a", Role: "executor"}},
				}},
				DelegationPolicy: &platformv1alpha1.AgentRunDelegationPolicy{ParentOnly: true, MaxDepth: 1},
			},
		},
	}

	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&platformv1alpha1.AgentRun{}).
		WithObjects(parent).
		Build()
	service := NewKubeTeamService(c, scheme)

	_, err := service.CreateChildRun(context.Background(), CreateChildRunRequest{
		Parent:   ParentRunRef{Namespace: "default", Name: "nested-parent"},
		StepName: "step-1",
		TaskName: "task-a",
	})
	if err == nil {
		t.Fatal("expected error for maxDepth exceeded")
	}
	if !errors.Is(err, ErrMaxDepthExceeded) {
		t.Fatalf("expected ErrMaxDepthExceeded, got: %v", err)
	}
}

func TestNewChildRunFromParentInheritsGitIdentityAnnotations(t *testing.T) {
	parent := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "parent-run",
			Namespace: "default",
			Annotations: map[string]string{
				platformv1alpha1.GitAuthorNameAnnotation:  "Alice Doe",
				platformv1alpha1.GitAuthorEmailAnnotation: "alice@example.com",
				"platform.gratefulagents.dev/repoless":    "true", // unrelated: must not propagate
			},
		},
		Spec: platformv1alpha1.AgentRunSpec{
			Repository: platformv1alpha1.RepositoryContext{URL: "https://github.com/example/repo.git"},
		},
	}

	child, _ := newChildRunFromParent(parent, "step-1", platformv1alpha1.AgentRunTeamTask{Name: "task-a"}, "child-1", "do the thing", "")
	if got := child.Annotations[platformv1alpha1.GitAuthorNameAnnotation]; got != "Alice Doe" {
		t.Fatalf("child git author name = %q, want Alice Doe", got)
	}
	if got := child.Annotations[platformv1alpha1.GitAuthorEmailAnnotation]; got != "alice@example.com" {
		t.Fatalf("child git author email = %q, want alice@example.com", got)
	}
	if _, ok := child.Annotations["platform.gratefulagents.dev/repoless"]; ok {
		t.Fatal("unrelated parent annotations must not propagate to children")
	}

	// Parent without identity: child has no identity annotations.
	parent.Annotations = nil
	child, _ = newChildRunFromParent(parent, "step-1", platformv1alpha1.AgentRunTeamTask{Name: "task-a"}, "child-2", "do the thing", "")
	if _, ok := child.Annotations[platformv1alpha1.GitAuthorNameAnnotation]; ok {
		t.Fatal("unexpected git author name annotation on child of identity-less parent")
	}
}
