package dashboard

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/store"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
	"google.golang.org/protobuf/proto"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func newDashboardTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()

	scheme := runtime.NewScheme()
	if err := platformv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(platform): %v", err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme(core): %v", err)
	}
	return scheme
}

func TestListAndGetAgentRuns(t *testing.T) {
	scheme := newDashboardTestScheme(t)

	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "run-1",
			Namespace: "default",
		},
		Spec: platformv1alpha1.AgentRunSpec{
			Trigger: platformv1alpha1.TriggerRef{Kind: "ProjectChat", Name: "payments-chat"},
			Repository: platformv1alpha1.RepositoryContext{
				URL: "https://github.com/example/repo.git",
			},
			WorkflowMode:  platformv1alpha1.WorkflowModeChat,
			ExecutionMode: platformv1alpha1.ExecutionModeTeam,
			Team: &platformv1alpha1.AgentRunTeamSpec{
				Steps: []platformv1alpha1.AgentRunTeamStep{
					{Name: "parallel-implementers", Type: platformv1alpha1.TeamStepTypeParallel},
				},
			},
		},
		Status: platformv1alpha1.AgentRunStatus{
			Phase:       platformv1alpha1.AgentRunPhasePending,
			CurrentStep: "gather-context",
			Artifacts: &platformv1alpha1.AgentRunArtifacts{
				PlanRef: &platformv1alpha1.ArtifactRef{Kind: "ConfigMap", Name: "run-1-plan", Key: "plan.md"},
			},
			TeamSummary: &platformv1alpha1.AgentRunTeamSummary{
				CurrentStep:     "parallel-implementers",
				TotalChildren:   2,
				PendingChildren: 2,
			},
		},
	}

	planCM := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "run-1-plan", Namespace: "default"},
		Data:       map[string]string{"plan.md": "## Plan\nDo the thing"},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run, planCM).Build()
	srv := &Server{k8sClient: c, scheme: scheme}

	listResp, err := srv.ListAgentRuns(context.Background(), &platform.ListAgentRunsRequest{Namespace: "default"})
	if err != nil {
		t.Fatalf("ListAgentRuns() error = %v", err)
	}
	if len(listResp.Runs) != 1 {
		t.Fatalf("len(Runs) = %d, want 1", len(listResp.Runs))
	}

	getResp, err := srv.GetAgentRun(context.Background(), &platform.GetAgentRunRequest{Namespace: "default", Name: "run-1"})
	if err != nil {
		t.Fatalf("GetAgentRun() error = %v", err)
	}
	if getResp.WorkflowMode != "auto" {
		t.Fatalf("WorkflowMode = %q, want effective auto mode", getResp.WorkflowMode)
	}
	if getResp.ExecutionMode != "team" {
		t.Fatalf("ExecutionMode = %q, want team", getResp.ExecutionMode)
	}
	if getResp.CurrentPlan == "" {
		t.Fatal("expected CurrentPlan to be enriched from AgentRun plan artifact ref")
	}
	if getResp.TeamSummary == nil || getResp.TeamSummary.CurrentStep != "parallel-implementers" {
		t.Fatalf("TeamSummary = %#v", getResp.TeamSummary)
	}
}

func TestGetAgentRunEnrichesImplementationFields(t *testing.T) {
	scheme := newDashboardTestScheme(t)

	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "run-impl", Namespace: "default"},
		Spec: platformv1alpha1.AgentRunSpec{
			Trigger:      platformv1alpha1.TriggerRef{Kind: "ProjectChat", Name: "payments-chat"},
			Repository:   platformv1alpha1.RepositoryContext{URL: "https://github.com/example/repo.git"},
			WorkflowMode: platformv1alpha1.WorkflowModeChat,
		},
		Status: platformv1alpha1.AgentRunStatus{
			Phase:         platformv1alpha1.AgentRunPhaseRunning,
			CurrentStep:   "reviewing diff",
			SessionNumber: 2,
			AgentCount:    1,
			RetryCount:    1,
			LastError:     "temporary git error",
		},
	}
	specCM := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "run-impl", Namespace: "default"},
		Data:       map[string]string{"spec.md": "# Spec\nImplement feature"},
	}
	run.Spec.SpecArtifactRef = &platformv1alpha1.ArtifactRef{Kind: "ConfigMap", Name: "run-impl", Key: "spec.md"}

	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run, specCM).Build()
	ms := newMockStateStore()
	sess, _ := ms.CreateSession(context.Background(), "run-impl", "default", "running", "reviewing diff")
	ms.getRecentActivityBySession = map[uuid.UUID][]store.ActivityEvent{
		sess.ID: {
			{EventType: "step_change", Summary: "reviewing diff", CreatedAt: time.Unix(200, 0)},
		},
	}
	srv := &Server{k8sClient: c, scheme: scheme, stateStore: ms}

	resp, err := srv.GetAgentRun(context.Background(), &platform.GetAgentRunRequest{Namespace: "default", Name: "run-impl"})
	if err != nil {
		t.Fatalf("GetAgentRun() error = %v", err)
	}
	if resp.CurrentStep != "reviewing diff" {
		t.Fatalf("CurrentStep = %q", resp.CurrentStep)
	}
	if resp.SessionNumber != 2 {
		t.Fatalf("SessionNumber = %d", resp.SessionNumber)
	}
	if resp.AgentCount != 1 {
		t.Fatalf("AgentCount = %d", resp.AgentCount)
	}
	if resp.RetryCount != 1 {
		t.Fatalf("RetryCount = %d", resp.RetryCount)
	}
	if resp.LastError != "temporary git error" {
		t.Fatalf("LastError = %q", resp.LastError)
	}
	if resp.SpecMarkdown == "" {
		t.Fatal("expected SpecMarkdown from AgentRun spec artifact ref")
	}
	if len(resp.RecentActivity) != 1 {
		t.Fatalf("RecentActivity len = %d", len(resp.RecentActivity))
	}
}

func TestGetAgentRunEnrichesSpecMarkdownForChatStartMode(t *testing.T) {
	scheme := newDashboardTestScheme(t)

	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "run-chat",
			Namespace: "default",
		},
		Spec: platformv1alpha1.AgentRunSpec{
			Trigger:      platformv1alpha1.TriggerRef{Kind: "ProjectChat", Name: "payments-chat"},
			Repository:   platformv1alpha1.RepositoryContext{URL: "https://github.com/example/repo.git", BranchName: "ai/payments"},
			WorkflowMode: platformv1alpha1.WorkflowModeChat,
			SpecArtifactRef: &platformv1alpha1.ArtifactRef{
				Kind: "ConfigMap",
				Name: "run-chat-spec",
				Key:  "spec.md",
			},
		},
		Status: platformv1alpha1.AgentRunStatus{
			Phase:       platformv1alpha1.AgentRunPhaseRunning,
			CurrentStep: "implement",
		},
	}
	specCM := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "run-chat-spec", Namespace: "default"},
		Data:       map[string]string{"spec.md": "# Spec\nImplement usage-based billing"},
	}

	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run, specCM).Build()
	srv := &Server{k8sClient: c, scheme: scheme}

	resp, err := srv.GetAgentRun(context.Background(), &platform.GetAgentRunRequest{Namespace: "default", Name: "run-chat"})
	if err != nil {
		t.Fatalf("GetAgentRun() error = %v", err)
	}
	if resp.WorkflowMode != "auto" {
		t.Fatalf("WorkflowMode = %q, want effective auto mode", resp.WorkflowMode)
	}
	if resp.SpecMarkdown == "" {
		t.Fatal("SpecMarkdown was empty for chat-start run with spec artifact")
	}
}

func TestAgentRunMessageReadinessRejectsStoppingRuns(t *testing.T) {
	sess := &store.Session{}
	for _, annotation := range []string{cancelRequestedAnnotation, promoteSucceededAnnotation} {
		run := &platformv1alpha1.AgentRun{
			ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{annotation: "requested"}},
			Status:     platformv1alpha1.AgentRunStatus{Phase: platformv1alpha1.AgentRunPhaseRunning},
		}
		if ready, _ := agentRunMessageReadiness(run, sess); ready {
			t.Fatalf("annotation %s: readiness = true, want false", annotation)
		}
	}
}

func TestAgentRunMessageReadinessRejectsTerminalRuns(t *testing.T) {
	run := &platformv1alpha1.AgentRun{Status: platformv1alpha1.AgentRunStatus{Phase: platformv1alpha1.AgentRunPhaseSucceeded}}
	if ready, _ := agentRunMessageReadiness(run, &store.Session{}); ready {
		t.Fatal("terminal run readiness = true, want false")
	}
}

func TestAgentRunMessageReadinessRejectsPausedRuns(t *testing.T) {
	run := &platformv1alpha1.AgentRun{Status: platformv1alpha1.AgentRunStatus{Phase: platformv1alpha1.AgentRunPhasePaused}}
	ready, reason := agentRunMessageReadiness(run, &store.Session{})
	if ready {
		t.Fatal("paused run readiness = true, want false")
	}
	if !strings.Contains(strings.ToLower(reason), "paused") {
		t.Fatalf("paused run readiness reason = %q, want paused reason", reason)
	}
}

func TestGetAgentRunMarksSendReadyWhenSessionExistsInBootstrapState(t *testing.T) {
	scheme := newDashboardTestScheme(t)

	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "run-ready", Namespace: "default"},
		Spec: platformv1alpha1.AgentRunSpec{
			WorkflowMode: platformv1alpha1.WorkflowModeChat,
		},
		Status: platformv1alpha1.AgentRunStatus{
			Phase:       platformv1alpha1.AgentRunPhaseRunning,
			CurrentStep: "implement",
		},
	}

	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run).Build()
	ms := newMockStateStore()
	ms.CreateSession(context.Background(), "run-ready", "default", "pending", "setup")
	srv := &Server{k8sClient: c, scheme: scheme, stateStore: ms}

	resp, err := srv.GetAgentRun(context.Background(), &platform.GetAgentRunRequest{Namespace: "default", Name: "run-ready"})
	if err != nil {
		t.Fatalf("GetAgentRun() error = %v", err)
	}
	if !resp.SendReady {
		t.Fatalf("SendReady = false, want true")
	}
	if resp.SendReadinessReason != "" {
		t.Fatalf("SendReadinessReason = %q, want empty", resp.SendReadinessReason)
	}
}

func TestGetAgentRunSuppressesPausedRunReadinessAndStaleInput(t *testing.T) {
	scheme := newDashboardTestScheme(t)
	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "run-paused", Namespace: "default"},
		Spec:       platformv1alpha1.AgentRunSpec{WorkflowMode: platformv1alpha1.WorkflowModeChat},
		Status:     platformv1alpha1.AgentRunStatus{Phase: platformv1alpha1.AgentRunPhasePaused},
	}

	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run).Build()
	ms := newMockStateStore()
	sess, _ := ms.CreateSession(context.Background(), "run-paused", "default", "running", "awaiting-user")
	sess.PendingInputType = "idle"
	sess.PendingQuestion = "Ready for another message"
	srv := &Server{k8sClient: c, scheme: scheme, stateStore: ms}

	resp, err := srv.GetAgentRun(context.Background(), &platform.GetAgentRunRequest{Namespace: "default", Name: "run-paused"})
	if err != nil {
		t.Fatalf("GetAgentRun() error = %v", err)
	}
	if resp.SendReady {
		t.Fatal("SendReady = true for paused run, want false")
	}
	if !strings.Contains(strings.ToLower(resp.SendReadinessReason), "paused") {
		t.Fatalf("SendReadinessReason = %q, want paused reason", resp.SendReadinessReason)
	}
	if resp.UserInputRequest != nil {
		t.Fatalf("UserInputRequest = %#v, want nil for paused run", resp.UserInputRequest)
	}
}

func TestGetAgentRunBuildsUserInputRequestFromPostgres(t *testing.T) {
	scheme := newDashboardTestScheme(t)

	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "run-pending-input", Namespace: "default"},
		Spec:       platformv1alpha1.AgentRunSpec{WorkflowMode: platformv1alpha1.WorkflowModeChat},
		Status: platformv1alpha1.AgentRunStatus{
			Phase:       platformv1alpha1.AgentRunPhaseQuestion,
			CurrentStep: "awaiting-user",
		},
	}

	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run).Build()
	ms := newMockStateStore()
	sess, _ := ms.CreateSession(context.Background(), "run-pending-input", "default", "question", "awaiting-user")
	sess.PendingInputType = "approval"
	sess.PendingQuestion = "Approve the implementation plan?"
	sess.PendingActions = json.RawMessage(`[{"id":"approve","label":"Approve","style":"primary"}]`)
	srv := &Server{k8sClient: c, scheme: scheme, stateStore: ms}

	resp, err := srv.GetAgentRun(context.Background(), &platform.GetAgentRunRequest{Namespace: "default", Name: "run-pending-input"})
	if err != nil {
		t.Fatalf("GetAgentRun() error = %v", err)
	}
	if resp.UserInputRequest == nil {
		t.Fatal("UserInputRequest = nil, want hydrated request from Postgres")
	}
	if resp.UserInputRequest.Type != "approval" {
		t.Fatalf("UserInputRequest.Type = %q, want approval", resp.UserInputRequest.Type)
	}
	if resp.UserInputRequest.Message != "Approve the implementation plan?" {
		t.Fatalf("UserInputRequest.Message = %q", resp.UserInputRequest.Message)
	}
	if len(resp.UserInputRequest.Actions) != 1 || resp.UserInputRequest.Actions[0].Id != "approve" {
		t.Fatalf("UserInputRequest.Actions = %#v", resp.UserInputRequest.Actions)
	}
}

func TestGetAgentRunPrefersPlanFromPostgresOverPlanArtifact(t *testing.T) {
	scheme := newDashboardTestScheme(t)

	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "run-plan-source", Namespace: "default"},
		Spec:       platformv1alpha1.AgentRunSpec{WorkflowMode: platformv1alpha1.WorkflowModeChat},
		Status: platformv1alpha1.AgentRunStatus{
			Phase:       platformv1alpha1.AgentRunPhaseRunning,
			CurrentStep: "planning",
			Artifacts: &platformv1alpha1.AgentRunArtifacts{
				PlanRef: &platformv1alpha1.ArtifactRef{Kind: "ConfigMap", Name: "run-plan-source-plan", Key: "plan.md"},
			},
		},
	}
	planCM := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "run-plan-source-plan", Namespace: "default"},
		Data:       map[string]string{"plan.md": "stale ConfigMap plan"},
	}

	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run, planCM).Build()
	ms := newMockStateStore()
	ms.CreateSession(context.Background(), "run-plan-source", "default", "running", "planning")
	ms.getArtifact = &store.Artifact{Kind: "plan", Content: "fresh Postgres plan"}
	srv := &Server{k8sClient: c, scheme: scheme, stateStore: ms}

	resp, err := srv.GetAgentRun(context.Background(), &platform.GetAgentRunRequest{Namespace: "default", Name: "run-plan-source"})
	if err != nil {
		t.Fatalf("GetAgentRun() error = %v", err)
	}
	if resp.CurrentPlan != "fresh Postgres plan" {
		t.Fatalf("CurrentPlan = %q, want Postgres plan to win", resp.CurrentPlan)
	}
}

func TestGetAgentRunPrefersRecentActivityFromPostgres(t *testing.T) {
	scheme := newDashboardTestScheme(t)

	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "run-activity", Namespace: "default"},
		Spec:       platformv1alpha1.AgentRunSpec{WorkflowMode: platformv1alpha1.WorkflowModeChat},
		Status: platformv1alpha1.AgentRunStatus{
			Phase:       platformv1alpha1.AgentRunPhaseRunning,
			CurrentStep: "implement",
		},
	}

	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run).Build()
	ms := newMockStateStore()
	sess, _ := ms.CreateSession(context.Background(), "run-activity", "default", "running", "implement")
	ms.getRecentActivityBySession = map[uuid.UUID][]store.ActivityEvent{
		sess.ID: {
			{EventType: "assistant_text", Summary: "fresh Postgres activity", CreatedAt: time.Unix(300, 0)},
		},
	}
	srv := &Server{k8sClient: c, scheme: scheme, stateStore: ms}

	resp, err := srv.GetAgentRun(context.Background(), &platform.GetAgentRunRequest{Namespace: "default", Name: "run-activity"})
	if err != nil {
		t.Fatalf("GetAgentRun() error = %v", err)
	}
	if len(resp.RecentActivity) != 1 || resp.RecentActivity[0].Summary != "fresh Postgres activity" {
		t.Fatalf("RecentActivity = %#v, want Postgres-backed activity", resp.RecentActivity)
	}
}

func TestGetAgentRunClearsStaleCrdDataWhenPostgresHasNone(t *testing.T) {
	scheme := newDashboardTestScheme(t)

	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "run-empty-session", Namespace: "default"},
		Spec:       platformv1alpha1.AgentRunSpec{WorkflowMode: platformv1alpha1.WorkflowModeChat},
		Status: platformv1alpha1.AgentRunStatus{
			Phase:       platformv1alpha1.AgentRunPhaseRunning,
			CurrentStep: "implement",
		},
	}

	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run).Build()
	ms := newMockStateStore()
	ms.CreateSession(context.Background(), "run-empty-session", "default", "running", "implement")
	srv := &Server{k8sClient: c, scheme: scheme, stateStore: ms}

	resp, err := srv.GetAgentRun(context.Background(), &platform.GetAgentRunRequest{Namespace: "default", Name: "run-empty-session"})
	if err != nil {
		t.Fatalf("GetAgentRun() error = %v", err)
	}
	if resp.UserInputRequest != nil {
		t.Fatalf("UserInputRequest = %#v, want nil from Postgres session state", resp.UserInputRequest)
	}
	if len(resp.Conversation) != 0 {
		t.Fatalf("Conversation = %#v, want empty Postgres-backed conversation", resp.Conversation)
	}
	if len(resp.RecentActivity) != 0 {
		t.Fatalf("RecentActivity = %#v, want empty Postgres-backed activity", resp.RecentActivity)
	}
}

func TestGetAgentRunEnrichesPlanAndSpecForMaterializedPlanRun(t *testing.T) {
	scheme := newDashboardTestScheme(t)

	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "run-plan-materialized",
			Namespace: "default",
		},
		Spec: platformv1alpha1.AgentRunSpec{
			Trigger:      platformv1alpha1.TriggerRef{Kind: "ProjectChat", Name: "payments-chat"},
			Repository:   platformv1alpha1.RepositoryContext{URL: "https://github.com/example/repo.git", BranchName: "ai/payments"},
			WorkflowMode: platformv1alpha1.WorkflowModeChat,
			SpecArtifactRef: &platformv1alpha1.ArtifactRef{
				Kind: "ConfigMap",
				Name: "run-plan-materialized-spec",
				Key:  "spec.md",
			},
		},
		Status: platformv1alpha1.AgentRunStatus{
			Phase:       platformv1alpha1.AgentRunPhaseRunning,
			CurrentStep: awaitingUserStep,
			Artifacts: &platformv1alpha1.AgentRunArtifacts{
				PlanRef: &platformv1alpha1.ArtifactRef{
					Kind: "ConfigMap",
					Name: "run-plan-materialized-plan",
					Key:  "plan.md",
				},
			},
		},
	}
	planCM := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "run-plan-materialized-plan", Namespace: "default"},
		Data:       map[string]string{"plan.md": "## Plan\nShip usage-based billing"},
	}
	specCM := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "run-plan-materialized-spec", Namespace: "default"},
		Data:       map[string]string{"spec.md": "# Spec\nExecute approved plan"},
	}

	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run, planCM, specCM).Build()
	srv := &Server{k8sClient: c, scheme: scheme}

	resp, err := srv.GetAgentRun(context.Background(), &platform.GetAgentRunRequest{
		Namespace: "default",
		Name:      "run-plan-materialized",
	})
	if err != nil {
		t.Fatalf("GetAgentRun() error = %v", err)
	}
	if resp.WorkflowMode != "auto" {
		t.Fatalf("WorkflowMode = %q, want effective auto mode", resp.WorkflowMode)
	}
	if resp.CurrentPlan == "" {
		t.Fatal("CurrentPlan was empty, want enriched plan markdown")
	}
	if resp.SpecMarkdown == "" {
		t.Fatal("SpecMarkdown was empty, want enriched execute spec markdown")
	}
}

func TestGetActivityLogFallsBackToAgentRunArtifacts(t *testing.T) {
	scheme := newDashboardTestScheme(t)

	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "run-log", Namespace: "default"},
		Spec: platformv1alpha1.AgentRunSpec{
			Trigger:      platformv1alpha1.TriggerRef{Kind: "ProjectChat", Name: "payments-chat"},
			Repository:   platformv1alpha1.RepositoryContext{URL: "https://github.com/example/repo.git"},
			WorkflowMode: platformv1alpha1.WorkflowModeChat,
		},
		Status: platformv1alpha1.AgentRunStatus{
			Phase:       platformv1alpha1.AgentRunPhaseSucceeded,
			CurrentStep: "clarify",
			Artifacts: &platformv1alpha1.AgentRunArtifacts{
				EventsLogURL: "s3://bucket/run-log.events.jsonl",
			},
		},
	}

	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run).Build()
	srv := &Server{
		k8sClient: c,
		scheme:    scheme,
		s3Reader: &s3ActivityReader{
			cache: map[string][]*platform.ActivityEntry{
				"s3://bucket/run-log.events.jsonl": {
					{TimestampUnix: 100, Type: "assistant_text", Message: "done"},
				},
			},
		},
	}

	resp, err := srv.GetActivityLog(context.Background(), &platform.GetActivityLogRequest{
		Namespace: "default",
		Name:      "run-log",
	})
	if err != nil {
		t.Fatalf("GetActivityLog() error = %v", err)
	}
	if !resp.IsComplete {
		t.Fatal("expected terminal AgentRun activity log to be complete")
	}
	if len(resp.Entries) != 1 || resp.Entries[0].Message != "done" {
		t.Fatalf("Entries = %#v, want cached AgentRun log entry", resp.Entries)
	}
}

func TestGetAgentRunEnrichesReadinessFieldsFromLiveStatus(t *testing.T) {
	scheme := newDashboardTestScheme(t)

	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "run-ready", Namespace: "default"},
		Spec: platformv1alpha1.AgentRunSpec{
			Trigger:      platformv1alpha1.TriggerRef{Kind: "ProjectChat", Name: "payments-chat"},
			Repository:   platformv1alpha1.RepositoryContext{URL: "https://github.com/example/repo.git"},
			WorkflowMode: platformv1alpha1.WorkflowModeChat,
		},
		Status: platformv1alpha1.AgentRunStatus{
			Phase:       platformv1alpha1.AgentRunPhaseRunning,
			CurrentStep: awaitingUserStep,
			Queue: &platformv1alpha1.AgentRunQueueStatus{
				State:         "Blocked",
				BlockedReason: "waiting for session",
			},
			Sandbox: &platformv1alpha1.AgentRunSandboxStatus{
				Provider:   "daytona",
				ClaimRef:   &platformv1alpha1.NamedRef{Name: "claim-ready"},
				SandboxRef: &platformv1alpha1.NamedRef{Name: "sandbox-ready"},
			},
		},
	}

	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run).Build()
	ms := newMockStateStore()
	ms.CreateSession(context.Background(), "run-ready", "default", "running", awaitingUserStep)
	srv := &Server{k8sClient: c, scheme: scheme, stateStore: ms}

	resp, err := srv.GetAgentRun(context.Background(), &platform.GetAgentRunRequest{Namespace: "default", Name: "run-ready"})
	if err != nil {
		t.Fatalf("GetAgentRun() error = %v", err)
	}
	if resp.QueueState != "Blocked" {
		t.Fatalf("QueueState = %q, want Blocked", resp.QueueState)
	}
	if resp.BlockedReason != "waiting for session" {
		t.Fatalf("BlockedReason = %q, want waiting for session", resp.BlockedReason)
	}
	if resp.SandboxProvider != "daytona" {
		t.Fatalf("SandboxProvider = %q, want daytona", resp.SandboxProvider)
	}
	if resp.SandboxClaimRef != "claim-ready" {
		t.Fatalf("SandboxClaimRef = %q, want claim-ready", resp.SandboxClaimRef)
	}
	if resp.SandboxRef != "sandbox-ready" {
		t.Fatalf("SandboxRef = %q, want sandbox-ready", resp.SandboxRef)
	}
}

func TestGetActivityLogRequiresAgentRun(t *testing.T) {
	scheme := newDashboardTestScheme(t)

	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	srv := &Server{k8sClient: c, scheme: scheme}

	_, err := srv.GetActivityLog(context.Background(), &platform.GetActivityLogRequest{
		Namespace: "default",
		Name:      "run-log",
	})
	if err == nil {
		t.Fatal("expected GetActivityLog() to require an AgentRun")
	}
	if connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("GetActivityLog() code = %v, want %v", connect.CodeOf(err), connect.CodeNotFound)
	}
}

func TestGetActivityLogFallsBackToPodForTerminalRunWithoutFinalArtifact(t *testing.T) {
	scheme := newDashboardTestScheme(t)

	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "run-live-fallback", Namespace: "default"},
		Spec: platformv1alpha1.AgentRunSpec{
			Trigger:      platformv1alpha1.TriggerRef{Kind: "ProjectChat", Name: "payments-chat"},
			Repository:   platformv1alpha1.RepositoryContext{URL: "https://github.com/example/repo.git"},
			WorkflowMode: platformv1alpha1.WorkflowModeChat,
		},
		Status: platformv1alpha1.AgentRunStatus{
			Phase: platformv1alpha1.AgentRunPhaseSucceeded,
			Sandbox: &platformv1alpha1.AgentRunSandboxStatus{
				SandboxRef: &platformv1alpha1.NamedRef{Name: "sandbox-live-fallback"},
			},
		},
	}

	origExec := execInPodFunc
	execInPodFunc = func(_ context.Context, _ *kubernetes.Clientset, _ *rest.Config, podName, namespace string, command []string) (string, error) {
		if podName != "sandbox-live-fallback" || namespace != "default" {
			t.Fatalf("execInPodFunc pod/namespace = %s/%s", namespace, podName)
		}
		want := []string{"cat", "/workspace/events.jsonl"}
		if len(command) != len(want) {
			t.Fatalf("execInPodFunc command len = %d, want %d (%#v)", len(command), len(want), command)
		}
		for i := range want {
			if command[i] != want[i] {
				t.Fatalf("execInPodFunc command[%d] = %q, want %q (%#v)", i, command[i], want[i], command)
			}
		}
		return "{\"ts\":\"2026-04-02T15:00:00Z\",\"type\":\"assistant_text\",\"message\":\"live fallback\"}\n", nil
	}
	t.Cleanup(func() { execInPodFunc = origExec })

	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run).Build()
	srv := &Server{
		k8sClient:  c,
		scheme:     scheme,
		clientset:  &kubernetes.Clientset{},
		restConfig: &rest.Config{},
	}

	resp, err := srv.GetActivityLog(context.Background(), &platform.GetActivityLogRequest{
		Namespace: "default",
		Name:      "run-live-fallback",
	})
	if err != nil {
		t.Fatalf("GetActivityLog() error = %v", err)
	}
	if resp.IsComplete {
		t.Fatal("expected pod fallback activity log to remain non-final")
	}
	if len(resp.Entries) != 1 || resp.Entries[0].Message != "live fallback" {
		t.Fatalf("Entries = %#v, want pod fallback activity entry", resp.Entries)
	}
}

func TestGetActivityLogReturnsCompleteUnavailableWhenTerminalSandboxPodIsGone(t *testing.T) {
	scheme := newDashboardTestScheme(t)

	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "run-live-missing-pod", Namespace: "default"},
		Spec: platformv1alpha1.AgentRunSpec{
			Trigger:      platformv1alpha1.TriggerRef{Kind: "ProjectChat", Name: "payments-chat"},
			Repository:   platformv1alpha1.RepositoryContext{URL: "https://github.com/example/repo.git"},
			WorkflowMode: platformv1alpha1.WorkflowModeChat,
		},
		Status: platformv1alpha1.AgentRunStatus{
			Phase: platformv1alpha1.AgentRunPhaseSucceeded,
			Sandbox: &platformv1alpha1.AgentRunSandboxStatus{
				SandboxRef: &platformv1alpha1.NamedRef{Name: "sandbox-live-missing-pod"},
			},
		},
	}

	origExec := execInPodFunc
	execInPodFunc = func(_ context.Context, _ *kubernetes.Clientset, _ *rest.Config, podName, namespace string, _ []string) (string, error) {
		if podName != "sandbox-live-missing-pod" || namespace != "default" {
			t.Fatalf("execInPodFunc pod/namespace = %s/%s", namespace, podName)
		}
		return "", apierrors.NewNotFound(schema.GroupResource{Resource: "pods"}, podName)
	}
	t.Cleanup(func() { execInPodFunc = origExec })

	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run).Build()
	srv := &Server{
		k8sClient:  c,
		scheme:     scheme,
		clientset:  &kubernetes.Clientset{},
		restConfig: &rest.Config{},
	}

	resp, err := srv.GetActivityLog(context.Background(), &platform.GetActivityLogRequest{
		Namespace: "default",
		Name:      "run-live-missing-pod",
	})
	if err != nil {
		t.Fatalf("GetActivityLog() error = %v", err)
	}
	if !resp.IsComplete {
		t.Fatal("expected missing terminal sandbox pod to yield a complete unavailable activity log")
	}
	if len(resp.Entries) != 0 {
		t.Fatalf("Entries = %#v, want no entries when sandbox pod is gone", resp.Entries)
	}
}

func TestGetAgentRunUsageAggregatesFromActivityPrecedence(t *testing.T) {
	scheme := newDashboardTestScheme(t)

	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "run-usage", Namespace: "default"},
		Status: platformv1alpha1.AgentRunStatus{
			Phase:     platformv1alpha1.AgentRunPhaseSucceeded,
			Artifacts: &platformv1alpha1.AgentRunArtifacts{EventsLogURL: "s3://bucket/run-usage.events.jsonl"},
		},
	}

	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run).Build()
	srv := &Server{
		k8sClient: c,
		scheme:    scheme,
		s3Reader: &s3ActivityReader{cache: map[string][]*platform.ActivityEntry{
			"s3://bucket/run-usage.events.jsonl": {
				{Type: "llm_attempt", Phase: "planning", Step: "exploring", AgentName: "planner", LlmAttemptId: "a1", LlmAttemptInputTokens: 11, LlmAttemptOutputTokens: 7, LlmAttemptTokensKnown: true},
				{Type: "llm_attempt", Phase: "planning", Step: "reviewing", AgentName: "reviewer", TaskId: "sub-1", LlmAttemptId: "a2", LlmAttemptInputTokens: 5, LlmAttemptOutputTokens: 3, LlmAttemptTokensKnown: true},
				{Type: "result"},
			},
		}},
	}

	resp, err := srv.GetAgentRunUsage(context.Background(), &platform.GetAgentRunUsageRequest{Namespace: "default", Name: "run-usage"})
	if err != nil {
		t.Fatalf("GetAgentRunUsage() error = %v", err)
	}
	if !resp.IsAvailable || !resp.IsComplete {
		t.Fatalf("availability/complete = %v/%v, want true/true", resp.IsAvailable, resp.IsComplete)
	}
	if resp.Summary == nil || resp.Summary.TotalTokens != 26 {
		t.Fatalf("summary = %#v, want total 26", resp.Summary)
	}
	if len(resp.TopLevelTasks) != 1 || resp.TopLevelTasks[0].TaskId != "top-level" {
		t.Fatalf("TopLevelTasks = %#v, want synthetic top-level bucket", resp.TopLevelTasks)
	}
	if len(resp.SubagentTasks) != 1 || resp.SubagentTasks[0].TaskId != "sub-1" {
		t.Fatalf("SubagentTasks = %#v, want sub-1", resp.SubagentTasks)
	}
}

func TestGetActivityLogPodFallbackPreservesGraphFixtureFields(t *testing.T) {
	scheme := newDashboardTestScheme(t)

	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "run-graph-fixture", Namespace: "default"},
		Spec: platformv1alpha1.AgentRunSpec{
			Trigger:      platformv1alpha1.TriggerRef{Kind: "ProjectChat", Name: "payments-chat"},
			Repository:   platformv1alpha1.RepositoryContext{URL: "https://github.com/example/repo.git"},
			WorkflowMode: platformv1alpha1.WorkflowModeChat,
		},
		Status: platformv1alpha1.AgentRunStatus{
			Phase: platformv1alpha1.AgentRunPhaseRunning,
			Sandbox: &platformv1alpha1.AgentRunSandboxStatus{
				SandboxRef: &platformv1alpha1.NamedRef{Name: "sandbox-graph-fixture"},
			},
		},
	}

	fixture := `{"ts":"2026-04-06T10:00:00Z","type":"tool_start","tool":"Agent","tool_use_id":"spawn_parent_1","message":"Spawn review helper"}
{"ts":"2026-04-06T10:00:01Z","type":"subagent_status","task_id":"task_spawned_1","tool_use_id":"spawn_parent_1","message":"Review helper","subagent_type":"reviewer","status":"started","subagent_model":"gpt-5.4"}
{"ts":"2026-04-06T10:00:02Z","type":"subagent_status","task_id":"task_spawned_1","subagent_type":"reviewer","status":"running","subagent_tool_count":1,"subagent_tokens":42,"subagent_duration_ms":50}
{"ts":"2026-04-06T10:00:03Z","type":"tool_end","tool":"Agent","tool_use_id":"spawn_parent_1","output":"spawned helper done"}
{"ts":"2026-04-06T10:00:04Z","type":"subagent_status","task_id":"task_spawned_1","message":"Spawn helper finished","step":"completed","subagent_type":"reviewer","status":"completed","subagent_num_turns":2,"subagent_cost_usd":0.0100,"subagent_stop_reason":"end_turn"}
{"ts":"2026-04-06T10:00:05Z","type":"tool_start","tool":"agent_explore","tool_use_id":"inline_parent_1","message":"Inline explore"}
{"ts":"2026-04-06T10:00:06Z","type":"tool_start","tool":"Read","tool_use_id":"inline_child_1","input_raw":"/repo/main.go"}
{"ts":"2026-04-06T10:00:07Z","type":"tool_end","tool":"Read","tool_use_id":"inline_child_1","output":"package main"}
{"ts":"2026-04-06T10:00:08Z","type":"tool_end","tool":"agent_explore","tool_use_id":"inline_parent_1","output":"inline helper done"}
{"ts":"2026-04-06T10:00:09Z","type":"subagent_status","message":"Fallback helper","subagent_type":"reviewer","status":"started"}
{"ts":"2026-04-06T10:00:10Z","type":"subagent_status","message":"Fallback helper finished","step":"completed","subagent_type":"reviewer","status":"completed"}
`

	origExec := execInPodFunc
	execInPodFunc = func(_ context.Context, _ *kubernetes.Clientset, _ *rest.Config, podName, namespace string, command []string) (string, error) {
		if podName != "sandbox-graph-fixture" || namespace != "default" {
			t.Fatalf("execInPodFunc pod/namespace = %s/%s", namespace, podName)
		}
		// Return fixture data from events.jsonl.
		want := []string{"cat", "/workspace/events.jsonl"}
		if len(command) != len(want) {
			t.Fatalf("execInPodFunc command len = %d, want %d (%#v)", len(command), len(want), command)
		}
		for i := range want {
			if command[i] != want[i] {
				t.Fatalf("execInPodFunc command[%d] = %q, want %q (%#v)", i, command[i], want[i], command)
			}
		}
		return fixture, nil
	}
	t.Cleanup(func() { execInPodFunc = origExec })

	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run).Build()
	srv := &Server{
		k8sClient:  c,
		scheme:     scheme,
		clientset:  &kubernetes.Clientset{},
		restConfig: &rest.Config{},
	}

	resp, err := srv.GetActivityLog(context.Background(), &platform.GetActivityLogRequest{
		Namespace: "default",
		Name:      "run-graph-fixture",
	})
	if err != nil {
		t.Fatalf("GetActivityLog() error = %v", err)
	}
	if len(resp.Entries) != 11 {
		t.Fatalf("len(Entries) = %d, want 11", len(resp.Entries))
	}

	var (
		spawnStart    *platform.ActivityEntry
		spawnComplete *platform.ActivityEntry
		inlineChild   *platform.ActivityEntry
		fallbackDone  *platform.ActivityEntry
	)
	for _, entry := range resp.Entries {
		switch {
		case entry.Type == "subagent_started":
			if entry.TaskId == "task_spawned_1" {
				spawnStart = entry
			}
		case entry.Type == "subagent_completed":
			if entry.TaskId == "task_spawned_1" {
				spawnComplete = entry
			} else if entry.SubagentDescription == "Fallback helper finished" {
				fallbackDone = entry
			}
		case entry.Type == "tool_use" && entry.ToolUseId == "inline_child_1":
			inlineChild = entry
		}
	}

	if spawnStart == nil {
		t.Fatal("expected spawned subagent start entry")
	}
	if spawnStart.TaskId != "task_spawned_1" {
		t.Fatalf("spawnStart task id = %q, want task_spawned_1", spawnStart.TaskId)
	}

	if spawnComplete == nil {
		t.Fatal("expected spawned subagent completion entry")
	}
	if spawnComplete.TaskId != "task_spawned_1" || spawnComplete.SubagentStatus != "completed" {
		t.Fatalf("spawnComplete task/status = %q/%q, want task_spawned_1/completed", spawnComplete.TaskId, spawnComplete.SubagentStatus)
	}

	if inlineChild == nil {
		t.Fatal("expected inline child entry")
	}
	if inlineChild.ToolUseId != "inline_child_1" {
		t.Fatalf("inline child tool_use_id = %q, want inline_child_1", inlineChild.ToolUseId)
	}

	if fallbackDone == nil {
		t.Fatal("expected fallback subagent completion entry")
	}
	if fallbackDone.SubagentStatus != "completed" || fallbackDone.Step != "completed" {
		t.Fatalf("fallback completion status/step = %q/%q, want completed/completed", fallbackDone.SubagentStatus, fallbackDone.Step)
	}
}

type recordingAgentRunEventStream struct {
	mu     sync.Mutex
	events []*platform.AgentRunEvent
}

func (s *recordingAgentRunEventStream) Spec() connect.Spec           { return connect.Spec{} }
func (s *recordingAgentRunEventStream) Peer() connect.Peer           { return connect.Peer{} }
func (s *recordingAgentRunEventStream) RequestHeader() http.Header   { return http.Header{} }
func (s *recordingAgentRunEventStream) ResponseHeader() http.Header  { return http.Header{} }
func (s *recordingAgentRunEventStream) ResponseTrailer() http.Header { return http.Header{} }
func (s *recordingAgentRunEventStream) Receive(any) error            { return nil }
func (s *recordingAgentRunEventStream) Send(msg any) error {
	event, ok := msg.(*platform.AgentRunEvent)
	if !ok || event == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	clone := proto.Clone(event).(*platform.AgentRunEvent)
	s.events = append(s.events, clone)
	return nil
}

func TestResolveSandboxUsesAgentRunExecutionFields(t *testing.T) {
	scheme := newDashboardTestScheme(t)

	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "run-1", Namespace: "default"},
		Spec: platformv1alpha1.AgentRunSpec{
			Trigger: platformv1alpha1.TriggerRef{Kind: "ProjectChat", Name: "payments-chat"},
			Repository: platformv1alpha1.RepositoryContext{
				URL:        "https://github.com/example/repo.git",
				BaseBranch: "release/v2",
			},
			WorkflowMode: platformv1alpha1.WorkflowModeChat,
		},
		Status: platformv1alpha1.AgentRunStatus{
			Phase: platformv1alpha1.AgentRunPhaseRunning,
			Sandbox: &platformv1alpha1.AgentRunSandboxStatus{
				SandboxRef: &platformv1alpha1.NamedRef{Name: "sandbox-run-1"},
			},
			Artifacts: &platformv1alpha1.AgentRunArtifacts{
				DiffURL: "s3://bucket/diffs/run-1.diff",
			},
		},
	}

	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run).Build()
	srv := &Server{k8sClient: c, scheme: scheme}

	sandboxName, namespace, baseBranch, diffURL, isTerminal, err := srv.resolveSandbox(context.Background(), "default", "run-1", "AgentRun")
	if err != nil {
		t.Fatalf("resolveSandbox() error = %v", err)
	}
	if sandboxName != "sandbox-run-1" {
		t.Fatalf("sandboxName = %q, want sandbox-run-1", sandboxName)
	}
	if namespace != "default" {
		t.Fatalf("namespace = %q, want default", namespace)
	}
	if baseBranch != "release/v2" {
		t.Fatalf("baseBranch = %q, want release/v2", baseBranch)
	}
	if diffURL != "s3://bucket/diffs/run-1.diff" {
		t.Fatalf("diffURL = %q, want AgentRun artifact diff", diffURL)
	}
	if isTerminal {
		t.Fatal("expected running AgentRun to be non-terminal")
	}
}

func TestResolveSandboxUsesAgentRunPlanFields(t *testing.T) {
	scheme := newDashboardTestScheme(t)

	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "run-plan", Namespace: "default"},
		Spec: platformv1alpha1.AgentRunSpec{
			Trigger:      platformv1alpha1.TriggerRef{Kind: "ProjectChat", Name: "payments-chat"},
			Repository:   platformv1alpha1.RepositoryContext{URL: "https://github.com/example/repo.git", BaseBranch: "release/v2"},
			WorkflowMode: platformv1alpha1.WorkflowModeChat,
		},
		Status: platformv1alpha1.AgentRunStatus{
			Phase: platformv1alpha1.AgentRunPhaseRunning,
			Sandbox: &platformv1alpha1.AgentRunSandboxStatus{
				SandboxRef: &platformv1alpha1.NamedRef{Name: "sandbox-plan"},
			},
		},
	}

	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run).Build()
	srv := &Server{k8sClient: c, scheme: scheme}

	sandboxName, _, baseBranch, diffURL, isTerminal, err := srv.resolveSandbox(context.Background(), "default", "run-plan", "AgentRun")
	if err != nil {
		t.Fatalf("resolveSandbox() error = %v", err)
	}
	if sandboxName != "sandbox-plan" {
		t.Fatalf("sandboxName = %q, want sandbox-plan", sandboxName)
	}
	if baseBranch != "release/v2" {
		t.Fatalf("baseBranch = %q, want release/v2", baseBranch)
	}
	if diffURL != "" {
		t.Fatalf("diffURL = %q, want empty for plan runs", diffURL)
	}
	if isTerminal {
		t.Fatal("isTerminal = true, want false")
	}
}

func TestResolveSandboxUsesAgentRunImplementationFields(t *testing.T) {
	scheme := newDashboardTestScheme(t)

	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "run-code", Namespace: "default"},
		Spec: platformv1alpha1.AgentRunSpec{
			Trigger:      platformv1alpha1.TriggerRef{Kind: "ProjectChat", Name: "payments-chat"},
			Repository:   platformv1alpha1.RepositoryContext{URL: "https://github.com/example/repo.git", BaseBranch: "release/v2"},
			WorkflowMode: platformv1alpha1.WorkflowModeChat,
		},
		Status: platformv1alpha1.AgentRunStatus{
			Phase: platformv1alpha1.AgentRunPhaseRunning,
			Sandbox: &platformv1alpha1.AgentRunSandboxStatus{
				SandboxRef: &platformv1alpha1.NamedRef{Name: "sandbox-code"},
			},
			Artifacts: &platformv1alpha1.AgentRunArtifacts{
				DiffURL: "s3://bucket/run-code.diff",
			},
		},
	}

	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run).Build()
	srv := &Server{k8sClient: c, scheme: scheme}

	sandboxName, _, baseBranch, diffURL, isTerminal, err := srv.resolveSandbox(context.Background(), "default", "run-code", "AgentRun")
	if err != nil {
		t.Fatalf("resolveSandbox() error = %v", err)
	}
	if sandboxName != "sandbox-code" {
		t.Fatalf("sandboxName = %q, want sandbox-code", sandboxName)
	}
	if baseBranch != "release/v2" {
		t.Fatalf("baseBranch = %q, want release/v2", baseBranch)
	}
	if diffURL != "s3://bucket/run-code.diff" {
		t.Fatalf("diffURL = %q, want coding-task diff url", diffURL)
	}
	if isTerminal {
		t.Fatal("isTerminal = true, want false")
	}
}

func TestResolveSandboxTreatsTerminalAgentRunAsComplete(t *testing.T) {
	scheme := newDashboardTestScheme(t)

	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "run-done", Namespace: "default"},
		Spec: platformv1alpha1.AgentRunSpec{
			Trigger:      platformv1alpha1.TriggerRef{Kind: "ProjectChat", Name: "payments-chat"},
			Repository:   platformv1alpha1.RepositoryContext{URL: "https://github.com/example/repo.git", BaseBranch: "main"},
			WorkflowMode: platformv1alpha1.WorkflowModeChat,
		},
		Status: platformv1alpha1.AgentRunStatus{
			Phase: platformv1alpha1.AgentRunPhaseSucceeded,
		},
	}

	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run).Build()
	srv := &Server{k8sClient: c, scheme: scheme}

	_, _, _, _, isTerminal, err := srv.resolveSandbox(context.Background(), "default", "run-done", "AgentRun")
	if err != nil {
		t.Fatalf("resolveSandbox() error = %v", err)
	}
	if !isTerminal {
		t.Fatal("expected succeeded AgentRun to be terminal")
	}
}

func TestResolveSandboxRejectsUnsupportedResourceTypes(t *testing.T) {
	scheme := newDashboardTestScheme(t)
	srv := &Server{k8sClient: fake.NewClientBuilder().WithScheme(scheme).Build(), scheme: scheme}

	_, _, _, _, _, err := srv.resolveSandbox(context.Background(), "default", "run-1", "Task")
	if err == nil {
		t.Fatal("expected resolveSandbox() to reject unsupported resource types")
	}
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("resolveSandbox() code = %v, want %v", connect.CodeOf(err), connect.CodeInvalidArgument)
	}
}

func TestGetDiffFallsBackToPodForTerminalRunWithoutFinalArtifact(t *testing.T) {
	scheme := newDashboardTestScheme(t)

	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "run-diff-fallback", Namespace: "default"},
		Spec: platformv1alpha1.AgentRunSpec{
			Trigger: platformv1alpha1.TriggerRef{Kind: "ProjectChat", Name: "payments-chat"},
			Repository: platformv1alpha1.RepositoryContext{
				URL:        "https://github.com/example/repo.git",
				BaseBranch: "release/v2",
			},
			WorkflowMode: platformv1alpha1.WorkflowModeChat,
		},
		Status: platformv1alpha1.AgentRunStatus{
			Phase: platformv1alpha1.AgentRunPhaseFailed,
			Sandbox: &platformv1alpha1.AgentRunSandboxStatus{
				SandboxRef: &platformv1alpha1.NamedRef{Name: "sandbox-diff-fallback"},
			},
		},
	}

	origExec := execInPodFunc
	execInPodFunc = func(_ context.Context, _ *kubernetes.Clientset, _ *rest.Config, podName, namespace string, command []string) (string, error) {
		if podName != "sandbox-diff-fallback" || namespace != "default" {
			t.Fatalf("execInPodFunc pod/namespace = %s/%s", namespace, podName)
		}
		if len(command) >= 3 && strings.Contains(command[2], "ls-files -z") {
			return "new-file.txt\x00", nil
		}
		want := []string{"git", "-C", repoDir, "diff", "origin/release/v2"}
		if len(command) != len(want) {
			t.Fatalf("execInPodFunc command len = %d, want %d (%#v)", len(command), len(want), command)
		}
		for i := range want {
			if command[i] != want[i] {
				t.Fatalf("execInPodFunc command[%d] = %q, want %q (%#v)", i, command[i], want[i], command)
			}
		}
		return "diff --git a/file.txt b/file.txt\n+fallback\n", nil
	}
	t.Cleanup(func() { execInPodFunc = origExec })

	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run).Build()
	srv := &Server{
		k8sClient:  c,
		scheme:     scheme,
		clientset:  &kubernetes.Clientset{},
		restConfig: &rest.Config{},
	}

	resp, err := srv.GetDiff(context.Background(), &platform.GetDiffRequest{
		Namespace:    "default",
		Name:         "run-diff-fallback",
		ResourceType: "AgentRun",
	})
	if err != nil {
		t.Fatalf("GetDiff() error = %v", err)
	}
	if resp.Source != "pod" {
		t.Fatalf("GetDiff().Source = %q, want pod", resp.Source)
	}
	if resp.IsComplete {
		t.Fatal("expected pod fallback diff to remain non-final")
	}
	if resp.Diff == "" {
		t.Fatal("expected diff content from pod fallback")
	}
}

func TestGetDiffReturnsCompleteUnavailableWhenTerminalSandboxPodIsGone(t *testing.T) {
	scheme := newDashboardTestScheme(t)

	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "run-diff-missing-pod", Namespace: "default"},
		Spec: platformv1alpha1.AgentRunSpec{
			Trigger: platformv1alpha1.TriggerRef{Kind: "ProjectChat", Name: "payments-chat"},
			Repository: platformv1alpha1.RepositoryContext{
				URL:        "https://github.com/example/repo.git",
				BaseBranch: "main",
			},
			WorkflowMode: platformv1alpha1.WorkflowModeChat,
		},
		Status: platformv1alpha1.AgentRunStatus{
			Phase: platformv1alpha1.AgentRunPhaseFailed,
			Sandbox: &platformv1alpha1.AgentRunSandboxStatus{
				SandboxRef: &platformv1alpha1.NamedRef{Name: "sandbox-diff-missing-pod"},
			},
		},
	}

	origExec := execInPodFunc
	execInPodFunc = func(_ context.Context, _ *kubernetes.Clientset, _ *rest.Config, podName, namespace string, _ []string) (string, error) {
		if podName != "sandbox-diff-missing-pod" || namespace != "default" {
			t.Fatalf("execInPodFunc pod/namespace = %s/%s", namespace, podName)
		}
		return "", apierrors.NewNotFound(schema.GroupResource{Resource: "pods"}, podName)
	}
	t.Cleanup(func() { execInPodFunc = origExec })

	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run).Build()
	srv := &Server{
		k8sClient:  c,
		scheme:     scheme,
		clientset:  &kubernetes.Clientset{},
		restConfig: &rest.Config{},
	}

	resp, err := srv.GetDiff(context.Background(), &platform.GetDiffRequest{
		Namespace:    "default",
		Name:         "run-diff-missing-pod",
		ResourceType: "AgentRun",
	})
	if err != nil {
		t.Fatalf("GetDiff() error = %v", err)
	}
	if resp.Source != "unavailable" {
		t.Fatalf("GetDiff().Source = %q, want unavailable", resp.Source)
	}
	if !resp.IsComplete {
		t.Fatal("expected missing terminal sandbox pod to yield a complete unavailable diff")
	}
	if resp.Diff != "" {
		t.Fatalf("GetDiff().Diff = %q, want empty diff when sandbox pod is gone", resp.Diff)
	}
}

func TestGetDiffPrefersFinalS3ArtifactOverPodFallback(t *testing.T) {
	scheme := newDashboardTestScheme(t)

	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "run-diff-final", Namespace: "default"},
		Spec: platformv1alpha1.AgentRunSpec{
			Trigger: platformv1alpha1.TriggerRef{Kind: "ProjectChat", Name: "payments-chat"},
			Repository: platformv1alpha1.RepositoryContext{
				URL:        "https://github.com/example/repo.git",
				BaseBranch: "main",
			},
			WorkflowMode: platformv1alpha1.WorkflowModeChat,
		},
		Status: platformv1alpha1.AgentRunStatus{
			Phase: platformv1alpha1.AgentRunPhaseSucceeded,
			Sandbox: &platformv1alpha1.AgentRunSandboxStatus{
				SandboxRef: &platformv1alpha1.NamedRef{Name: "sandbox-diff-final"},
			},
			Artifacts: &platformv1alpha1.AgentRunArtifacts{
				DiffURL: "s3://bucket/diffs/final.diff",
			},
		},
	}

	origExec := execInPodFunc
	execInPodFunc = func(_ context.Context, _ *kubernetes.Clientset, _ *rest.Config, _, _ string, command []string) (string, error) {
		if len(command) >= 3 && strings.Contains(command[2], "ls-files -z") {
			return "new-final.txt\x00", nil
		}
		t.Fatal("expected S3 diff to win over pod diff fallback")
		return "", nil
	}
	t.Cleanup(func() { execInPodFunc = origExec })

	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run).Build()
	srv := &Server{
		k8sClient:  c,
		scheme:     scheme,
		clientset:  &kubernetes.Clientset{},
		restConfig: &rest.Config{},
		s3Diff: &s3DiffReader{
			cache: map[string]string{
				"s3://bucket/diffs/final.diff": "diff --git a/final.txt b/final.txt\n+final\n",
			},
		},
	}

	resp, err := srv.GetDiff(context.Background(), &platform.GetDiffRequest{
		Namespace:    "default",
		Name:         "run-diff-final",
		ResourceType: "AgentRun",
	})
	if err != nil {
		t.Fatalf("GetDiff() error = %v", err)
	}
	if resp.Source != "s3" {
		t.Fatalf("GetDiff().Source = %q, want s3", resp.Source)
	}
	if !resp.IsComplete {
		t.Fatal("expected final S3 diff to be complete")
	}
	if resp.Diff == "" {
		t.Fatal("expected S3 diff content")
	}
	if len(resp.NewFiles) != 1 || resp.NewFiles[0] != "new-final.txt" {
		t.Fatalf("GetDiff().NewFiles = %#v, want terminal sandbox paths", resp.NewFiles)
	}
}

func TestGetDiffExtraRepoUsesPodAndSkipsFinalS3Artifact(t *testing.T) {
	scheme := newDashboardTestScheme(t)

	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "run-diff-extra", Namespace: "default"},
		Spec: platformv1alpha1.AgentRunSpec{
			Trigger: platformv1alpha1.TriggerRef{Kind: "ProjectChat", Name: "payments-chat"},
			Repository: platformv1alpha1.RepositoryContext{
				URL:        "https://github.com/example/repo.git",
				BaseBranch: "main",
			},
			WorkflowMode: platformv1alpha1.WorkflowModeChat,
		},
		Status: platformv1alpha1.AgentRunStatus{
			Phase: platformv1alpha1.AgentRunPhaseSucceeded,
			Sandbox: &platformv1alpha1.AgentRunSandboxStatus{
				SandboxRef: &platformv1alpha1.NamedRef{Name: "sandbox-diff-extra"},
			},
			Artifacts: &platformv1alpha1.AgentRunArtifacts{
				DiffURL: "s3://bucket/diffs/final.diff",
			},
		},
	}

	origExec := execInPodFunc
	var gotCommand []string
	execInPodFunc = func(_ context.Context, _ *kubernetes.Clientset, _ *rest.Config, _, _ string, command []string) (string, error) {
		if len(command) >= 3 && strings.Contains(command[2], "ls-files -z") {
			return "", nil
		}
		gotCommand = command
		return "diff --git a/w b/w\n+w\n", nil
	}
	t.Cleanup(func() { execInPodFunc = origExec })

	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run).Build()
	srv := &Server{
		k8sClient:  c,
		scheme:     scheme,
		clientset:  &kubernetes.Clientset{},
		restConfig: &rest.Config{},
		s3Diff: &s3DiffReader{
			cache: map[string]string{
				"s3://bucket/diffs/final.diff": "diff --git a/final.txt b/final.txt\n+final\n",
			},
		},
	}

	resp, err := srv.GetDiff(context.Background(), &platform.GetDiffRequest{
		Namespace:    "default",
		Name:         "run-diff-extra",
		ResourceType: "AgentRun",
		RepoPath:     "/workspace/repo/repos/widgets",
	})
	if err != nil {
		t.Fatalf("GetDiff() error = %v", err)
	}
	if resp.Source != "pod" {
		t.Fatalf("GetDiff().Source = %q, want pod (the S3 final diff only covers the primary repo)", resp.Source)
	}
	if resp.Diff == "" {
		t.Fatal("expected pod diff content for the extra repo")
	}
	if len(gotCommand) != 3 || gotCommand[0] != "sh" || gotCommand[1] != "-c" {
		t.Fatalf("exec command = %#v, want sh -c script", gotCommand)
	}
	if !strings.Contains(gotCommand[2], "/workspace/repo/repos/widgets") {
		t.Fatalf("exec script does not target the requested repo:\n%s", gotCommand[2])
	}
}

func TestGetDiffRejectsInvalidRepoPath(t *testing.T) {
	scheme := newDashboardTestScheme(t)

	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "run-diff-badrepo", Namespace: "default"},
		Spec: platformv1alpha1.AgentRunSpec{
			Trigger: platformv1alpha1.TriggerRef{Kind: "ProjectChat", Name: "payments-chat"},
			Repository: platformv1alpha1.RepositoryContext{
				URL:        "https://github.com/example/repo.git",
				BaseBranch: "main",
			},
			WorkflowMode: platformv1alpha1.WorkflowModeChat,
		},
		Status: platformv1alpha1.AgentRunStatus{
			Phase: platformv1alpha1.AgentRunPhaseRunning,
			Sandbox: &platformv1alpha1.AgentRunSandboxStatus{
				SandboxRef: &platformv1alpha1.NamedRef{Name: "sandbox-diff-badrepo"},
			},
		},
	}

	origExec := execInPodFunc
	execInPodFunc = func(_ context.Context, _ *kubernetes.Clientset, _ *rest.Config, _, _ string, _ []string) (string, error) {
		t.Fatal("execInPodFunc must not run for invalid repo paths")
		return "", nil
	}
	t.Cleanup(func() { execInPodFunc = origExec })

	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run).Build()
	srv := &Server{
		k8sClient:  c,
		scheme:     scheme,
		clientset:  &kubernetes.Clientset{},
		restConfig: &rest.Config{},
	}

	_, err := srv.GetDiff(context.Background(), &platform.GetDiffRequest{
		Namespace:    "default",
		Name:         "run-diff-badrepo",
		ResourceType: "AgentRun",
		RepoPath:     "/workspace/../etc",
	})
	if err == nil {
		t.Fatal("expected GetDiff() to reject traversal repo path")
	}
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("GetDiff() error code = %v, want InvalidArgument (%v)", connect.CodeOf(err), err)
	}
}
