package dashboard

import (
	"testing"
	"time"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestK8sAgentRunToProto(t *testing.T) {
	now := metav1.NewTime(time.Unix(123, 0))
	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "run-1",
			Namespace:         "default",
			CreationTimestamp: now,
			Annotations:       map[string]string{platformv1alpha1.OverseerDetachingAnnotation: "true"},
		},
		Spec: platformv1alpha1.AgentRunSpec{
			Trigger: platformv1alpha1.TriggerRef{
				Kind: "ProjectChat",
				Name: "payments-chat",
				ExternalRef: &platformv1alpha1.ExternalRef{
					ID:         "ext-1",
					Identifier: "ENG-123",
					URL:        "https://example.com/thread/1",
				},
			},
			Repository: platformv1alpha1.RepositoryContext{
				URL:        "https://github.com/example/repo.git",
				BaseBranch: "main",
				BranchName: "ai/test",
			},
			Context: &platformv1alpha1.AgentRunContext{
				ProjectRef: &platformv1alpha1.ProjectRef{
					Kind: "LinearProject",
					Name: "payments",
				},
			},
			WorkflowMode:  platformv1alpha1.WorkflowModeChat,
			ExecutionMode: platformv1alpha1.ExecutionModeTeam,
			Team: &platformv1alpha1.AgentRunTeamSpec{
				Steps: []platformv1alpha1.AgentRunTeamStep{
					{
						Name: "parallel-implementers",
						Type: platformv1alpha1.TeamStepTypeParallel,
						Tasks: []platformv1alpha1.AgentRunTeamTask{
							{
								Name:              "worker-a",
								Role:              "executor",
								Objective:         "Implement the API surface",
								RuntimeProfileRef: &platformv1alpha1.NamedRef{Name: "team-runtime"},
								DependsOn:         []string{"schema-freeze"},
								MaxRetries:        2,
								ArtifactContract:  "patch/diff",
							},
						},
					},
				},
				DelegationPolicy: &platformv1alpha1.AgentRunDelegationPolicy{
					MaxChildren: 6,
					MaxDepth:    1,
					ParentOnly:  true,
				},
				CompletionPolicy: &platformv1alpha1.AgentRunCompletionPolicy{
					RequireApproval: true,
				},
			},
			Model:           "openrouter/gpt-5.4",
			KubernetesAdmin: true,
			Overseer: &platformv1alpha1.AgentRunOverseerSpec{
				ModeRef:          &platformv1alpha1.ModeRef{Name: "overseer", Version: "v2", Channel: "stable"},
				Model:            "openai/gpt-5",
				Authority:        platformv1alpha1.AgentRunOverseerAuthorityEnforce,
				IntervalMinutes:  3,
				MaxInterventions: 0,
			},
			ReasoningLevel: platformv1alpha1.ReasoningHigh,
			AuthMode:       platformv1alpha1.AgentRunAuthModeAPIKey,
			OpenAIBaseURL:  "https://openrouter.ai/api/v1",
			Image:          "ghcr.io/example/worker:latest",
			Secrets: &platformv1alpha1.AgentRunSecrets{
				ClaudeAPIKeySecret: "claude-key",
				OpenAIOAuthSecret:  "oauth-secret",
				GitHubTokenSecret:  "github-token",
			},
		},
		Status: platformv1alpha1.AgentRunStatus{
			Phase:        platformv1alpha1.AgentRunPhaseRunning,
			CurrentStep:  "gather-context",
			ModeSnapshot: &platformv1alpha1.ModeTemplateSpec{},
			Queue:        &platformv1alpha1.AgentRunQueueStatus{State: "Running"},
			OverseerSummary: &platformv1alpha1.AgentRunOverseerStatus{
				RunName:                  "run-1-overseer",
				State:                    "watching",
				CheckpointsHandled:       8,
				InterventionsUsed:        2,
				CompletionRejectionsUsed: 1,
				LastVerdict:              "steer",
				LastSummary:              "adjust course",
				LastVerdictTime:          &now,
			},
			TeamSummary: &platformv1alpha1.AgentRunTeamSummary{
				CurrentStepIndex: 1,
				CurrentStep:      "parallel-implementers",
				ApprovalState:    "pending",
				TotalChildren:    2,
				PendingChildren:  1,
				RunningChildren:  1,
			},
			Children: []platformv1alpha1.AgentRunChildStatus{
				{
					Name:      "run-1-worker-a",
					Namespace: "default",
					Step:      "parallel-implementers",
					Role:      "executor",
					Phase:     platformv1alpha1.AgentRunPhaseRunning,
				},
			},
		},
	}

	pb := k8sAgentRunToProto(run)
	if pb.Name != "run-1" {
		t.Fatalf("Name = %q, want run-1", pb.Name)
	}
	if pb.WorkflowMode != "auto" {
		t.Fatalf("WorkflowMode = %q, want effective auto mode", pb.WorkflowMode)
	}
	if pb.ExecutionMode != "team" {
		t.Fatalf("ExecutionMode = %q, want team", pb.ExecutionMode)
	}
	if pb.Trigger == nil || pb.Trigger.Kind != "ProjectChat" {
		t.Fatalf("Trigger = %#v", pb.Trigger)
	}
	if pb.Project == nil || pb.Project.Name != "payments" {
		t.Fatalf("Project = %#v", pb.Project)
	}
	if !pb.KubernetesAdmin {
		t.Fatalf("KubernetesAdmin = false, want true")
	}
	if pb.Model != "openrouter/gpt-5.4" || pb.OpenaiBaseUrl != "https://openrouter.ai/api/v1" {
		t.Fatalf("model/baseURL = %q/%q", pb.Model, pb.OpenaiBaseUrl)
	}
	if pb.ResolvedModel != "openrouter/gpt-5.4" || pb.ResolvedReasoningLevel != "high" {
		t.Fatalf("resolved model/reasoning = %q/%q", pb.ResolvedModel, pb.ResolvedReasoningLevel)
	}
	if pb.AuthMode != "api-key" {
		t.Fatalf("AuthMode = %q, want api-key", pb.AuthMode)
	}
	if pb.ClaudeApiKeySecret != "claude-key" || pb.OpenaiOauthSecret != "oauth-secret" || pb.GithubTokenSecret != "github-token" {
		t.Fatalf("secret refs = %q/%q/%q", pb.ClaudeApiKeySecret, pb.OpenaiOauthSecret, pb.GithubTokenSecret)
	}
	if pb.Phase != "Running" || pb.CurrentStep != "gather-context" {
		t.Fatalf("Phase/CurrentStep = %q/%q", pb.Phase, pb.CurrentStep)
	}
	if pb.Team == nil || len(pb.Team.Steps) != 1 {
		t.Fatalf("Team = %#v", pb.Team)
	}
	if got := pb.Team.Steps[0].Tasks[0].RuntimeProfileRef; got != "team-runtime" {
		t.Fatalf("RuntimeProfileRef = %q, want team-runtime", got)
	}
	if pb.Team.DelegationPolicy == nil || !pb.Team.DelegationPolicy.ParentOnly {
		t.Fatalf("DelegationPolicy = %#v", pb.Team.DelegationPolicy)
	}
	if pb.TeamSummary == nil || pb.TeamSummary.CurrentStep != "parallel-implementers" {
		t.Fatalf("TeamSummary = %#v", pb.TeamSummary)
	}
	if !pb.OverseerDetaching {
		t.Fatal("OverseerDetaching = false, want true")
	}
	if pb.Overseer == nil || pb.Overseer.ModeRefName != "overseer" || pb.Overseer.ModeRefVersion != "v2" || pb.Overseer.ModeRefChannel != "stable" || pb.Overseer.Model != "openai/gpt-5" || pb.Overseer.Authority != "enforce" || pb.Overseer.GetIntervalMinutes() != 3 || pb.Overseer.MaxInterventions == nil || pb.Overseer.GetMaxInterventions() != 0 {
		t.Fatalf("Overseer = %#v", pb.Overseer)
	}
	if pb.OverseerSummary == nil || pb.OverseerSummary.RunName != "run-1-overseer" || pb.OverseerSummary.State != "watching" || pb.OverseerSummary.CheckpointsHandled != 8 || pb.OverseerSummary.InterventionsUsed != 2 || pb.OverseerSummary.CompletionRejectionsUsed != 1 || pb.OverseerSummary.LastVerdict != "steer" || pb.OverseerSummary.LastSummary != "adjust course" || pb.OverseerSummary.LastVerdictAtUnix != 123 {
		t.Fatalf("OverseerSummary = %#v", pb.OverseerSummary)
	}
	if len(pb.Children) != 1 || pb.Children[0].Phase != "Running" {
		t.Fatalf("Children = %#v", pb.Children)
	}
}

func TestK8sAgentRunToProtoMapsChatWorkflow(t *testing.T) {
	now := metav1.NewTime(time.Unix(123, 0))
	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "run-chat",
			Namespace:         "default",
			CreationTimestamp: now,
		},
		Spec: platformv1alpha1.AgentRunSpec{
			WorkflowMode: platformv1alpha1.WorkflowModeChat,
		},
	}

	pb := k8sAgentRunToProto(run)
	if pb.WorkflowMode != "auto" {
		t.Fatalf("WorkflowMode = %q, want effective auto mode", pb.WorkflowMode)
	}
}

func TestK8sAgentRunToProtoDefaultsEmptyWorkflowToChat(t *testing.T) {
	now := metav1.NewTime(time.Unix(789, 0))
	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "run-empty-wf",
			Namespace:         "default",
			CreationTimestamp: now,
		},
		Spec: platformv1alpha1.AgentRunSpec{
			Trigger: platformv1alpha1.TriggerRef{Kind: "Project", Name: "eng"},
		},
	}

	pb := k8sAgentRunToProto(run)
	if pb.WorkflowMode != "auto" {
		t.Fatalf("WorkflowMode = %q, want auto default when empty", pb.WorkflowMode)
	}
}

func TestK8sAgentRunToProtoPRLoopImplementer(t *testing.T) {
	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "impl-run",
			Namespace: "default",
			Labels: map[string]string{
				prLoopStateLabel:  "in_review",
				prLoopNumberLabel: "42",
			},
			Annotations: map[string]string{
				prLoopURLAnnotation:       "https://github.com/example/repo/pull/42",
				prLoopRoundAnnotation:     "2",
				prLoopMaxRoundsAnnotation: "5",
			},
		},
	}

	pb := k8sAgentRunToProto(run)
	if pb.PrLoop == nil {
		t.Fatalf("PrLoop = nil, want status")
	}
	if pb.PrLoop.Role != "implementer" || pb.PrLoop.State != "in_review" {
		t.Fatalf("role/state = %q/%q, want implementer/in_review", pb.PrLoop.Role, pb.PrLoop.State)
	}
	if pb.PrLoop.PrNumber != 42 || pb.PrLoop.ReviewRound != 2 || pb.PrLoop.MaxRounds != 5 {
		t.Fatalf("number/round/max = %d/%d/%d, want 42/2/5", pb.PrLoop.PrNumber, pb.PrLoop.ReviewRound, pb.PrLoop.MaxRounds)
	}
	if pb.PrLoop.PrUrl != "https://github.com/example/repo/pull/42" {
		t.Fatalf("PrUrl = %q", pb.PrLoop.PrUrl)
	}
}

func TestK8sAgentRunToProtoPRLoopReviewer(t *testing.T) {
	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "review-run",
			Namespace: "default",
			Labels: map[string]string{
				prLoopRoleLabel:   "reviewer",
				prLoopNumberLabel: "42",
			},
			Annotations: map[string]string{
				prLoopImplementerAnnotation:              "impl-run",
				prLoopRoundAnnotation:                    "2",
				platformv1alpha1.ReviewVerdictAnnotation: platformv1alpha1.ReviewVerdictRequestChanges,
				platformv1alpha1.ReviewSummaryAnnotation: "Please tighten the tests.",
			},
		},
	}

	pb := k8sAgentRunToProto(run)
	if pb.PrLoop == nil {
		t.Fatalf("PrLoop = nil, want status")
	}
	if pb.PrLoop.Role != "reviewer" || pb.PrLoop.ImplementerRunName != "impl-run" {
		t.Fatalf("role/implementer = %q/%q, want reviewer/impl-run", pb.PrLoop.Role, pb.PrLoop.ImplementerRunName)
	}
	if pb.PrLoop.ReviewVerdict != "request_changes" || pb.PrLoop.ReviewSummary != "Please tighten the tests." {
		t.Fatalf("verdict/summary = %q/%q", pb.PrLoop.ReviewVerdict, pb.PrLoop.ReviewSummary)
	}
	if pb.PrLoop.State != "" {
		t.Fatalf("State = %q, want empty for reviewer run", pb.PrLoop.State)
	}
}

func ptrTime(v metav1.Time) *metav1.Time {
	return &v
}

func TestK8sAgentRunToProtoMapsDisplayName(t *testing.T) {
	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "run-dn", Namespace: "default"},
		Status:     platformv1alpha1.AgentRunStatus{DisplayName: "Fix retry race"},
	}

	pb := k8sAgentRunToProto(run)
	if pb.DisplayName != "Fix retry race" {
		t.Fatalf("DisplayName = %q, want %q", pb.DisplayName, "Fix retry race")
	}
}

func TestK8sAgentRunToProtoPullRequestUrls(t *testing.T) {
	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "run-prs", Namespace: "default"},
		Status: platformv1alpha1.AgentRunStatus{
			Artifacts: &platformv1alpha1.AgentRunArtifacts{
				PullRequestURL: "https://github.com/acme/charts/pull/2",
				PullRequestURLs: []string{
					"https://github.com/acme/app/pull/1",
					"https://github.com/acme/charts/pull/2",
				},
			},
		},
	}

	pb := k8sAgentRunToProto(run)
	if pb.PullRequestUrl != "https://github.com/acme/charts/pull/2" {
		t.Fatalf("PullRequestUrl = %q, want most recent PR", pb.PullRequestUrl)
	}
	if len(pb.PullRequestUrls) != 2 ||
		pb.PullRequestUrls[0] != "https://github.com/acme/app/pull/1" ||
		pb.PullRequestUrls[1] != "https://github.com/acme/charts/pull/2" {
		t.Fatalf("PullRequestUrls = %v", pb.PullRequestUrls)
	}
}

func TestK8sAgentRunToProtoPullRequestUrlsLegacyFallback(t *testing.T) {
	run := &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "run-legacy-pr", Namespace: "default"},
		Status: platformv1alpha1.AgentRunStatus{
			Artifacts: &platformv1alpha1.AgentRunArtifacts{
				PullRequestURL: "https://github.com/acme/app/pull/7",
			},
		},
	}

	pb := k8sAgentRunToProto(run)
	if len(pb.PullRequestUrls) != 1 || pb.PullRequestUrls[0] != "https://github.com/acme/app/pull/7" {
		t.Fatalf("PullRequestUrls = %v, want the legacy single URL", pb.PullRequestUrls)
	}
}
