package dashboard

import (
	"strconv"
	"strings"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/orchestration"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
)

const (
	prLoopStateLabel  = "triggers.gratefulagents.dev/pr-loop"
	prLoopRoleLabel   = "triggers.gratefulagents.dev/pr-loop-role"
	prLoopNumberLabel = "triggers.gratefulagents.dev/pr-number"

	prLoopURLAnnotation         = "triggers.gratefulagents.dev/pr-url"
	prLoopRoundAnnotation       = "triggers.gratefulagents.dev/review-round"
	prLoopMaxRoundsAnnotation   = "triggers.gratefulagents.dev/review-max-rounds"
	prLoopImplementerAnnotation = "triggers.gratefulagents.dev/pr-loop-implementer"

	prLoopDefaultMaxRounds = 3
	prLoopReviewerRole     = "reviewer"
	prLoopImplementerRole  = "implementer"
)

func k8sAgentRunToProto(run *platformv1alpha1.AgentRun) *platform.AgentRun {
	if run == nil {
		return nil
	}

	// WorkflowMode is retained in storage for compatibility, but the public
	// surface reports the sole effective pacing mode.
	workflowMode := string(platformv1alpha1.WorkflowModeAuto)

	pb := &platform.AgentRun{
		Namespace:       run.Namespace,
		Name:            run.Name,
		WorkflowMode:    workflowMode,
		ExecutionMode:   string(run.Spec.ExecutionMode),
		IntentTitle:     "", // Intent removed; title is seeded as first Postgres message
		CreatedAtUnix:   run.CreationTimestamp.Unix(),
		StandingRunRole: strings.TrimSpace(run.Labels[orchestration.StandingRunRoleLabel]),
	}

	if run.Spec.Trigger.ExternalRef != nil {
		pb.Trigger = &platform.AgentRunTriggerRef{
			Kind:               run.Spec.Trigger.Kind,
			Name:               run.Spec.Trigger.Name,
			Type:               run.Spec.Trigger.Type,
			ExternalId:         run.Spec.Trigger.ExternalRef.ID,
			ExternalIdentifier: run.Spec.Trigger.ExternalRef.Identifier,
			ExternalUrl:        run.Spec.Trigger.ExternalRef.URL,
		}
	} else {
		pb.Trigger = &platform.AgentRunTriggerRef{
			Kind: run.Spec.Trigger.Kind,
			Name: run.Spec.Trigger.Name,
			Type: run.Spec.Trigger.Type,
		}
	}
	pb.RepoUrl = run.Spec.Repository.URL
	pb.AdditionalRepoUrls = append(pb.AdditionalRepoUrls, run.Spec.Repository.AdditionalRepos...)
	pb.BaseBranch = run.Spec.Repository.BaseBranch
	pb.BranchName = run.Spec.Repository.BranchName
	pb.Revision = run.Spec.Repository.Revision
	pb.Model = run.Spec.Model
	pb.KubernetesAdmin = run.Spec.KubernetesAdmin
	pb.AuthMode = string(run.Spec.AuthMode)
	pb.OpenaiBaseUrl = run.Spec.OpenAIBaseURL
	pb.Image = run.Spec.Image
	if run.Spec.Team != nil {
		pb.Team = &platform.AgentRunTeamSpec{}
		for _, step := range run.Spec.Team.Steps {
			pbStep := &platform.AgentRunTeamStep{
				Name: step.Name,
				Type: string(step.Type),
			}
			for _, task := range step.Tasks {
				pbTask := &platform.AgentRunTeamTask{
					Name:             task.Name,
					Role:             task.Role,
					Objective:        task.Objective,
					MaxRetries:       task.MaxRetries,
					ArtifactContract: task.ArtifactContract,
				}
				if task.RuntimeProfileRef != nil {
					pbTask.RuntimeProfileRef = task.RuntimeProfileRef.Name
				}
				pbTask.DependsOn = append(pbTask.DependsOn, task.DependsOn...)
				pbStep.Tasks = append(pbStep.Tasks, pbTask)
			}
			pb.Team.Steps = append(pb.Team.Steps, pbStep)
		}
		if run.Spec.Team.DelegationPolicy != nil {
			pb.Team.DelegationPolicy = &platform.AgentRunDelegationPolicy{
				MaxChildren: run.Spec.Team.DelegationPolicy.MaxChildren,
				MaxDepth:    run.Spec.Team.DelegationPolicy.MaxDepth,
				ParentOnly:  run.Spec.Team.DelegationPolicy.ParentOnly,
			}
		}
		if run.Spec.Team.CompletionPolicy != nil {
			pb.Team.CompletionPolicy = &platform.AgentRunCompletionPolicy{
				RequireApproval: run.Spec.Team.CompletionPolicy.RequireApproval,
			}
		}
	}

	if run.Spec.Context != nil && run.Spec.Context.ProjectRef != nil {
		pb.Project = &platform.AgentRunProjectRef{
			Kind: run.Spec.Context.ProjectRef.Kind,
			Name: run.Spec.Context.ProjectRef.Name,
		}
	}

	if run.Spec.SpecArtifactRef != nil {
		pb.SpecArtifactKind = run.Spec.SpecArtifactRef.Kind
		pb.SpecArtifactName = run.Spec.SpecArtifactRef.Name
		pb.SpecArtifactKey = run.Spec.SpecArtifactRef.Key
	}
	if run.Spec.RuntimeProfileRef != nil {
		pb.RuntimeProfileRef = run.Spec.RuntimeProfileRef.Name
	}
	if run.Spec.MCPPolicyRef != nil {
		pb.McpPolicyRef = run.Spec.MCPPolicyRef.Name
	}
	for _, ref := range run.Spec.MCPServerRefs {
		pb.McpServerRefs = append(pb.McpServerRefs, ref.Name)
	}
	for _, ref := range run.Spec.SkillRefs {
		pb.SkillRefs = append(pb.SkillRefs, ref.Name)
	}
	if run.Spec.Limits != nil {
		pb.MaxTurns = run.Spec.Limits.MaxTurns
		pb.MaxRuntime = run.Spec.Limits.MaxRuntime.Duration.String()
		pb.MaxRetries = run.Spec.Limits.MaxRetries
	}
	if run.Spec.Secrets != nil {
		pb.ClaudeApiKeySecret = run.Spec.Secrets.ClaudeAPIKeySecret
		pb.OpenaiOauthSecret = run.Spec.Secrets.OpenAIOAuthSecret
		pb.GithubTokenSecret = run.Spec.Secrets.GitHubTokenSecret
		for _, pk := range run.Spec.Secrets.ProviderKeys {
			pb.ProviderKeys = append(pb.ProviderKeys, &platform.ProviderKeyRef{
				Provider:   pk.Provider,
				SecretName: pk.SecretName,
				SecretKey:  pk.SecretKey,
			})
		}
		for _, ref := range run.Spec.Secrets.ProviderOAuthSecrets {
			pb.ProviderOauthSecrets = append(pb.ProviderOauthSecrets, &platform.ProviderKeyRef{
				Provider:   ref.Provider,
				SecretName: ref.SecretName,
			})
		}
	}

	pb.Phase = string(run.Status.Phase)
	pb.DisplayName = run.Status.DisplayName
	if run.Status.Queue != nil {
		pb.QueueState = run.Status.Queue.State
		pb.BlockedReason = run.Status.Queue.BlockedReason
		if run.Status.Queue.AdmittedAt != nil {
			pb.AdmittedAtUnix = run.Status.Queue.AdmittedAt.Unix()
		}
	}
	if run.Status.Sandbox != nil {
		pb.SandboxProvider = run.Status.Sandbox.Provider
		if run.Status.Sandbox.ClaimRef != nil {
			pb.SandboxClaimRef = run.Status.Sandbox.ClaimRef.Name
		}
		if run.Status.Sandbox.SandboxRef != nil {
			pb.SandboxRef = run.Status.Sandbox.SandboxRef.Name
		}
	}
	if run.Status.Artifacts != nil {
		if run.Status.Artifacts.PlanRef != nil {
			pb.PlanArtifactKind = run.Status.Artifacts.PlanRef.Kind
			pb.PlanArtifactName = run.Status.Artifacts.PlanRef.Name
			pb.PlanArtifactKey = run.Status.Artifacts.PlanRef.Key
		}
		if run.Status.Artifacts.ReviewSummaryRef != nil {
			pb.ReviewArtifactKind = run.Status.Artifacts.ReviewSummaryRef.Kind
			pb.ReviewArtifactName = run.Status.Artifacts.ReviewSummaryRef.Name
			pb.ReviewArtifactKey = run.Status.Artifacts.ReviewSummaryRef.Key
		}
		pb.DiffUrl = run.Status.Artifacts.DiffURL
		pb.PullRequestUrl = run.Status.Artifacts.PullRequestURL
		pb.PullRequestUrls = append(pb.PullRequestUrls, run.Status.Artifacts.PullRequestURLs...)
		if len(pb.PullRequestUrls) == 0 && pb.PullRequestUrl != "" {
			// Runs recorded before multi-PR support only carry the single URL.
			pb.PullRequestUrls = []string{pb.PullRequestUrl}
		}
		pb.TraceId = run.Status.Artifacts.TraceID
	}
	if run.Status.Policy != nil {
		pb.ResolvedPermissionMode = run.Status.Policy.ResolvedPermissionMode
		pb.ResolvedAgentKinds = append(pb.ResolvedAgentKinds, run.Status.Policy.ResolvedAgentKinds...)
		pb.ResolvedSkills = append(pb.ResolvedSkills, run.Status.Policy.ResolvedSkills...)
		pb.ResolvedMcpServers = append(pb.ResolvedMcpServers, run.Status.Policy.ResolvedMCPServers...)
	}
	if run.Status.Metrics != nil {
		pb.CostUsd = run.Status.Metrics.CostUsd
		pb.InputTokens = run.Status.Metrics.InputTokens
		pb.OutputTokens = run.Status.Metrics.OutputTokens
		pb.ToolCallCount = run.Status.Metrics.ToolCallCount
	}
	pb.CurrentStep = run.Status.CurrentStep
	pb.SessionNumber = run.Status.SessionNumber
	pb.AgentCount = run.Status.AgentCount
	pb.RetryCount = run.Status.RetryCount
	pb.LastError = run.Status.LastError
	if run.Status.TeamSummary != nil {
		pb.TeamSummary = &platform.AgentRunTeamSummary{
			CurrentStepIndex:  run.Status.TeamSummary.CurrentStepIndex,
			CurrentStep:       run.Status.TeamSummary.CurrentStep,
			ApprovalState:     run.Status.TeamSummary.ApprovalState,
			TotalChildren:     run.Status.TeamSummary.TotalChildren,
			PendingChildren:   run.Status.TeamSummary.PendingChildren,
			RunningChildren:   run.Status.TeamSummary.RunningChildren,
			SucceededChildren: run.Status.TeamSummary.SucceededChildren,
			FailedChildren:    run.Status.TeamSummary.FailedChildren,
			CancelledChildren: run.Status.TeamSummary.CancelledChildren,
			BlockedReason:     run.Status.TeamSummary.BlockedReason,
		}
	}
	for _, child := range run.Status.Children {
		pb.Children = append(pb.Children, &platform.AgentRunChildStatus{
			Name:          child.Name,
			Namespace:     child.Namespace,
			Step:          child.Step,
			Role:          child.Role,
			Phase:         string(child.Phase),
			BlockedReason: child.BlockedReason,
		})
	}
	if run.Status.StartedAt != nil {
		pb.StartedAtUnix = run.Status.StartedAt.Unix()
	}
	if run.Status.CompletedAt != nil {
		pb.CompletedAtUnix = run.Status.CompletedAt.Unix()
	}

	pb.ResolvedModel = run.Spec.Model
	if lvl := strings.TrimSpace(string(run.Spec.ReasoningLevel)); lvl != "" {
		pb.ResolvedReasoningLevel = lvl
	}

	// Mode system fields.
	if run.Spec.ModeRef != nil {
		pb.ModeRefName = run.Spec.ModeRef.Name
		pb.ModeRefVersion = run.Spec.ModeRef.Version
		pb.ModeRefChannel = run.Spec.ModeRef.Channel
	}
	pb.ModeName = run.Status.ModeName
	pb.ModeVersion = run.Status.ModeVersion
	pb.ModeRevision = run.Status.ModeRevision
	if run.Status.ModeSnapshot != nil {
		pb.ModeCategory = string(run.Status.ModeSnapshot.Category)
		pb.ModeExecutionStrategy = string(run.Status.ModeSnapshot.ExecutionStrategy)
	}

	pb.OverseerDetaching = strings.TrimSpace(run.Annotations[platformv1alpha1.OverseerDetachingAnnotation]) != ""
	if run.Spec.Overseer != nil {
		intervalMinutes := run.Spec.Overseer.IntervalMinutes
		maxInterventions := run.Spec.Overseer.MaxInterventions
		pb.Overseer = &platform.AgentRunOverseerConfig{
			Model:            run.Spec.Overseer.Model,
			Authority:        string(run.Spec.Overseer.Authority),
			IntervalMinutes:  &intervalMinutes,
			MaxInterventions: &maxInterventions,
		}
		if run.Spec.Overseer.ModeRef != nil {
			pb.Overseer.ModeRefName = run.Spec.Overseer.ModeRef.Name
			pb.Overseer.ModeRefVersion = run.Spec.Overseer.ModeRef.Version
			pb.Overseer.ModeRefChannel = run.Spec.Overseer.ModeRef.Channel
		}
	}
	if run.Status.OverseerSummary != nil {
		pb.OverseerSummary = &platform.AgentRunOverseerSummary{
			RunName:                  run.Status.OverseerSummary.RunName,
			State:                    run.Status.OverseerSummary.State,
			CheckpointsHandled:       run.Status.OverseerSummary.CheckpointsHandled,
			InterventionsUsed:        run.Status.OverseerSummary.InterventionsUsed,
			CompletionRejectionsUsed: run.Status.OverseerSummary.CompletionRejectionsUsed,
			LastVerdict:              run.Status.OverseerSummary.LastVerdict,
			LastSummary:              run.Status.OverseerSummary.LastSummary,
		}
		if run.Status.OverseerSummary.LastVerdictTime != nil {
			pb.OverseerSummary.LastVerdictAtUnix = run.Status.OverseerSummary.LastVerdictTime.Unix()
		}
	}

	// ModeInstructions is populated from the live ModeTemplate CRD in
	// enrichAgentRunProto, not from the snapshot.
	pb.PrLoop = prLoopStatusFromAgentRun(run)

	return pb
}

func prLoopStatusFromAgentRun(run *platformv1alpha1.AgentRun) *platform.PRLoopStatus {
	if run == nil {
		return nil
	}
	labels := run.Labels
	annotations := run.Annotations
	state := strings.TrimSpace(labels[prLoopStateLabel])
	role := strings.TrimSpace(labels[prLoopRoleLabel])
	implementerRunName := strings.TrimSpace(annotations[prLoopImplementerAnnotation])
	if state == "" && role != prLoopReviewerRole && implementerRunName == "" {
		return nil
	}

	if role == "" && state != "" {
		role = prLoopImplementerRole
	}

	maxRounds := parsePositiveInt(annotations[prLoopMaxRoundsAnnotation])
	if maxRounds == 0 {
		maxRounds = prLoopDefaultMaxRounds
	}
	return &platform.PRLoopStatus{
		State:              state,
		PrNumber:           int32(parsePositiveInt(labels[prLoopNumberLabel])),
		PrUrl:              strings.TrimSpace(annotations[prLoopURLAnnotation]),
		ReviewRound:        int32(parsePositiveInt(annotations[prLoopRoundAnnotation])),
		MaxRounds:          int32(maxRounds),
		Role:               role,
		ImplementerRunName: implementerRunName,
		ReviewVerdict:      strings.TrimSpace(annotations[platformv1alpha1.ReviewVerdictAnnotation]),
		ReviewSummary:      strings.TrimSpace(annotations[platformv1alpha1.ReviewSummaryAnnotation]),
	}
}

func parsePositiveInt(raw string) int {
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || n < 0 {
		return 0
	}
	return n
}
