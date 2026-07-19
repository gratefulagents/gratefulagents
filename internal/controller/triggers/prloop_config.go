package triggers

import (
	"context"
	"fmt"
	"strings"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
)

// prLoopConfig is the trigger-independent input used to create reviewer runs.
// A matching GitHubRepository contributes repository policy and GitHub App
// auth; otherwise the implementer's already-resolved AgentRunSpec is used.
type prLoopConfig struct {
	Disabled           bool
	MaxRounds          int
	ReviewerModeRef    *platformv1alpha1.ModeRef
	ReviewerSpec       platformv1alpha1.AgentRunSpec
	CustomInstructions string
	Annotations        map[string]string
	Repository         *triggersv1alpha1.GitHubRepository
	RepositoryName     string
}

func (e *PRLoopEngine) resolveLoopConfig(ctx context.Context, gh *triggersv1alpha1.GitHubRepository, implementer *platformv1alpha1.AgentRun, repository, baseRef string) (*prLoopConfig, error) {
	if implementer == nil {
		return nil, fmt.Errorf("implementer AgentRun is required")
	}
	repository = normalizeRepositoryName(repository)
	// Only repository configuration beside the implementer and matching this
	// PR's repository may contribute defaults, secrets, auth, or loop policy.
	// In particular, do not fall back to the implementer's trigger repository:
	// a multi-repo run may be opening this PR from an unconfigured additional
	// repository.
	if !gitHubRepositoryMatches(gh, implementer.Namespace, repository) {
		gh = e.lookupRepositoryForLoop(ctx, implementer, repository)
		if !gitHubRepositoryMatches(gh, implementer.Namespace, repository) {
			gh = nil
		}
	}
	policyRepo := gh
	defaultsRepo := gh

	cfg := &prLoopConfig{
		Disabled:       reviewLoopDisabledForRun(implementer, policyRepo),
		MaxRounds:      reviewLoopMaxRounds(policyRepo),
		ReviewerSpec:   reviewerSpecFromImplementer(implementer, baseRef),
		Annotations:    reviewerAnnotationsFromImplementer(implementer),
		Repository:     defaultsRepo,
		RepositoryName: repositoryName(defaultsRepo),
	}
	if defaultsRepo != nil {
		cfg.ReviewerSpec, cfg.Annotations, cfg.CustomInstructions = reviewerSpecFromRepository(defaultsRepo, baseRef)
		cfg.ReviewerModeRef = configuredReviewerModeRef(defaultsRepo)
	}
	if cfg.ReviewerModeRef == nil && ModeExistsFromK8s(ctx, e.Client)(defaultReviewerModeName) {
		cfg.ReviewerModeRef = &platformv1alpha1.ModeRef{Name: defaultReviewerModeName}
	}
	if cfg.RepositoryName == "" && policyRepo != nil && policyRepo.Namespace == implementer.Namespace {
		cfg.RepositoryName = policyRepo.Name
	}
	if cfg.Disabled {
		return cfg, nil
	}
	if strings.TrimSpace(cfg.ReviewerSpec.Model) == "" {
		return nil, fmt.Errorf("AgentRun %s/%s spec.model is required for reviewer runs", implementer.Namespace, implementer.Name)
	}
	return cfg, nil
}

func reviewerSpecFromImplementer(implementer *platformv1alpha1.AgentRun, baseRef string) platformv1alpha1.AgentRunSpec {
	spec := *implementer.Spec.DeepCopy()
	spec.Trigger = platformv1alpha1.TriggerRef{}
	spec.Context = nil
	spec.Repository.BaseBranch = firstNonEmptyString(baseRef, spec.Repository.BaseBranch)
	spec.Repository.BranchName = ""
	spec.Repository.Revision = ""
	spec.ExecutionMode = platformv1alpha1.ExecutionModeLinear
	spec.WorkflowMode = platformv1alpha1.WorkflowModeAuto
	spec.Team = nil
	// Standing supervision runs and PR reviewers are orchestration roles, not
	// ordinary implementers. A reviewer must never inherit the implementer's
	// overseer or recursively create another supervisor.
	spec.Overseer = nil
	spec.ModeRef = nil
	spec.SpecArtifactRef = nil
	spec.WakeRequests = 0
	spec.RestartRequests = 0
	if strings.TrimSpace(spec.Model) == "" {
		spec.Model = triggersv1alpha1.DefaultMainModelForProvider(triggersv1alpha1.ProviderOpenAI)
	}
	if spec.ReasoningLevel == "" {
		spec.ReasoningLevel = platformv1alpha1.ReasoningMax
	}
	return spec
}

func reviewerAnnotationsFromImplementer(implementer *platformv1alpha1.AgentRun) map[string]string {
	annotations := map[string]string{}
	if value := strings.TrimSpace(implementer.Annotations[openAIAPIModeAnnotation]); value != "" {
		annotations[openAIAPIModeAnnotation] = value
	}
	return annotations
}

func reviewerSpecFromRepository(gh *triggersv1alpha1.GitHubRepository, baseRef string) (platformv1alpha1.AgentRunSpec, map[string]string, string) {
	d := gh.Spec.Defaults
	if gh.Spec.ReviewLoop != nil && gh.Spec.ReviewLoop.ReviewerDefaults != nil {
		d = *gh.Spec.ReviewLoop.ReviewerDefaults.DeepCopy()
		// Reviewer overrides cannot redirect repository identity or GitHub auth.
		d.RepoURL = gh.Spec.Defaults.RepoURL
		d.Secrets.GithubToken = gh.Spec.Defaults.Secrets.GithubToken
	}
	provider := triggersv1alpha1.NormalizeProvider(d.Provider)
	authMode := triggersv1alpha1.NormalizeAuthMode(string(d.AuthMode))
	model := strings.TrimSpace(d.Model)
	if model == "" {
		model = triggersv1alpha1.DefaultMainModelForProvider(provider)
	}
	reasoningLevel := d.ReasoningLevel
	if reasoningLevel == "" {
		reasoningLevel = platformv1alpha1.ReasoningMax
	}
	annotations := map[string]string{}
	if triggersv1alpha1.IsOpenAICompatibleProvider(provider) {
		annotations[openAIAPIModeAnnotation] = triggersv1alpha1.NormalizeOpenAIAPIForProvider(provider, d.OpenAIAPI)
	}

	gitHubTokenSecret := d.Secrets.GithubToken
	if gh.Spec.GitHubApp != nil {
		// The deterministic reviewer run name is filled in by createReviewerRun.
		gitHubTokenSecret = ""
	}
	spec := platformv1alpha1.AgentRunSpec{
		Repository: platformv1alpha1.RepositoryContext{
			URL:             d.RepoURL,
			BaseBranch:      firstNonEmptyString(baseRef, d.BaseBranch),
			AdditionalRepos: append([]string(nil), d.AdditionalRepos...),
		},
		ExecutionMode:  platformv1alpha1.ExecutionModeLinear,
		WorkflowMode:   platformv1alpha1.WorkflowModeAuto,
		Model:          prefixModelWithProvider(model, provider),
		AuthMode:       authMode,
		ReasoningLevel: reasoningLevel,
		OpenAIBaseURL:  triggersv1alpha1.ResolveOpenAIBaseURLWithAuth(provider, d.OpenAIBaseURL, authMode),
		Image:          d.Image,
		Secrets: &platformv1alpha1.AgentRunSecrets{
			ClaudeAPIKeySecret: d.Secrets.ClaudeApiKey,
			OpenAIOAuthSecret:  d.Secrets.OpenAIOAuthSecret,
			GitHubTokenSecret:  gitHubTokenSecret,
			ProviderKeys:       append([]platformv1alpha1.ProviderKeyRef(nil), d.Secrets.ProviderKeys...),
		},
	}
	applyPolicyRefs(&spec, d)
	return spec, annotations, d.CustomInstructions
}

func gitHubRepositoryMatches(gh *triggersv1alpha1.GitHubRepository, namespace, repository string) bool {
	return gh != nil && gh.Namespace == namespace &&
		normalizeRepositoryName(gh.Spec.Owner+"/"+gh.Spec.Repo) == normalizeRepositoryName(repository)
}

func repositoryName(gh *triggersv1alpha1.GitHubRepository) string {
	if gh == nil {
		return ""
	}
	return gh.Name
}

func configuredReviewerModeRef(gh *triggersv1alpha1.GitHubRepository) *platformv1alpha1.ModeRef {
	if gh != nil && gh.Spec.ReviewLoop != nil && gh.Spec.ReviewLoop.ReviewerModeRef != nil {
		return gh.Spec.ReviewLoop.ReviewerModeRef.DeepCopy()
	}
	return nil
}

func reviewLoopDisabledForRun(run *platformv1alpha1.AgentRun, gh *triggersv1alpha1.GitHubRepository) bool {
	if run != nil {
		switch strings.ToLower(strings.TrimSpace(run.Annotations[PRLoopOptAnnotation])) {
		case PRLoopOptEnabled:
			return false
		case PRLoopOptDisabled:
			return true
		}
	}
	return reviewLoopDisabled(gh)
}
