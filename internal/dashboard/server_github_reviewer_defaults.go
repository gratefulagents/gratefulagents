package dashboard

import (
	"context"
	"fmt"
	"strings"

	"connectrpc.com/connect"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
)

// resolveGitHubReviewerDefaultsUpdate validates the optional reviewer-specific
// defaults and provisions their managed policies. changed is false for legacy
// clients that omitted use_reviewer_defaults, preserving stored configuration.
func (s *Server) resolveGitHubReviewerDefaultsUpdate(
	ctx context.Context,
	req *platform.UpdateGitHubRepositoryRequest,
	existing *triggersv1alpha1.GitHubRepository,
) (defaults *triggersv1alpha1.AgentRunDefaults, changed bool, cleanup []func(), err error) {
	if req.UseReviewerDefaults == nil {
		return nil, false, nil, nil
	}
	if !req.GetUseReviewerDefaults() {
		return nil, true, nil, nil
	}

	d, provider, authMode, err := protoDefaultsToCRD(req.GetReviewerDefaults())
	if err != nil {
		return nil, false, nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("reviewer defaults: %w", err))
	}
	if strings.TrimSpace(d.Model) == "" {
		return nil, false, nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("reviewer_defaults.model is required"))
	}
	// Reviewer runs always target this GitHubRepository. The repository URL and
	// GitHub identity are onboarding-owned, not reviewer-overridable settings.
	d.RepoURL = existing.Spec.Defaults.RepoURL
	if d.AdditionalRepos, err = normalizeAdditionalRepoURLs(d.AdditionalRepos, d.RepoURL); err != nil {
		return nil, false, nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("reviewer defaults: %w", err))
	}
	gitHubTokenSecret := existing.Spec.Defaults.Secrets.GithubToken
	if req.GetUseSavedReviewerCredentials() {
		secrets := triggersv1alpha1.AgentRunSecrets{}
		if err := s.applyProjectSavedCredentials(ctx, existing.Namespace, provider, authMode, &secrets); err != nil {
			return nil, false, nil, err
		}
		d.Secrets = secrets
	} else if err := validateProviderAuthConfiguration(provider, authMode, d.Secrets.ClaudeApiKey, d.Secrets.OpenAIOAuthSecret, d.Secrets.ProviderKeys); err != nil { //nolint:staticcheck // legacy field retained for explicit anthropic API-key auth
		return nil, false, nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("reviewer defaults: %w", err))
	}
	d.Secrets.GithubToken = gitHubTokenSecret
	// Reviewer orchestration is fixed by the PR loop; do not persist hidden
	// values copied from implementer defaults or supplied by API clients.
	d.WorkflowMode = ""
	d.ExecutionMode = ""
	d.Team = nil
	d.ModeRef = nil

	var prior triggersv1alpha1.AgentRunDefaults
	if existing.Spec.ReviewLoop != nil && existing.Spec.ReviewLoop.ReviewerDefaults != nil {
		prior = *existing.Spec.ReviewLoop.ReviewerDefaults
	}
	preserveAdminOnlyTriggerDefaults(&d, prior)
	cleanup, err = s.applyTriggerPolicies(ctx, existing.Namespace, existing.Name+"-reviewer", req.GetReviewerPolicies(), &d)
	if err != nil {
		return nil, false, nil, err
	}
	return &d, true, cleanup, nil
}

func compactReviewLoop(reviewLoop *triggersv1alpha1.ReviewLoopSpec) *triggersv1alpha1.ReviewLoopSpec {
	// An empty object is meaningful: reviewLoop: {} explicitly opts in while an
	// omitted object keeps the loop disabled by default.
	return reviewLoop
}
