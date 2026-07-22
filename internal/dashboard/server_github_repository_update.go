package dashboard

import (
	"context"
	"fmt"
	"strings"

	"connectrpc.com/connect"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// UpdateGitHubRepository replaces the run defaults (and optional policies) of
// an existing GitHubRepository trigger. The spec-level GitHub auth wiring
// (githubTokenSecret / githubApp) and the derived repository URL are never
// changed by this RPC.
func (s *Server) UpdateGitHubRepository(ctx context.Context, req *platform.UpdateGitHubRepositoryRequest) (*platform.GitHubRepository, error) {
	namespace := strings.TrimSpace(req.GetNamespace())
	name := strings.TrimSpace(req.GetName())
	if namespace == "" || name == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("namespace and name are required"))
	}
	if err := s.requireResourceAccess(ctx, githubRepositoryResourceType, name, namespace, AccessCollaborator, "update this repository"); err != nil {
		return nil, err
	}

	existing := &triggersv1alpha1.GitHubRepository{}
	if err := s.k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, existing); err != nil {
		return nil, mapK8sError(fmt.Sprintf("get GitHubRepository %s/%s", namespace, name), err)
	}

	defaults, provider, authMode, err := protoDefaultsToCRD(req.GetDefaults())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	// Trigger-created runs require an explicit model (validateTriggerRunDefaults
	// in the controller); reject early instead of persisting a broken trigger.
	if strings.TrimSpace(defaults.Model) == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("defaults.model is required"))
	}
	// The trigger always targets the onboarded repository: keep the derived
	// URL, ignore any repo_url in the request, and re-dedupe the additional
	// repos against it so the onboarded repo is never cloned twice.
	defaults.RepoURL = existing.Spec.Defaults.RepoURL
	if defaults.AdditionalRepos, err = normalizeAdditionalRepoURLs(defaults.AdditionalRepos, defaults.RepoURL); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	if strings.TrimSpace(req.GetDefaults().GetGithubTokenSecret()) == "" {
		defaults.Secrets.GithubToken = existing.Spec.Defaults.Secrets.GithubToken
	}

	if req.GetUseSavedCredentials() {
		secrets := triggersv1alpha1.AgentRunSecrets{}
		if err := s.applyProjectSavedCredentials(ctx, namespace, provider, authMode, &secrets); err != nil {
			return nil, err
		}
		// The trigger's GitHub identity is part of its spec-level auth wiring,
		// not the provider credential being rewired here. Installation-based
		// triggers authenticate runs via the GitHub App (Defaults token stays
		// empty), and token-based triggers keep the dedicated token secret they
		// were onboarded with. Preserve the existing wiring whenever there is
		// any: overwriting it with the caller's saved personal token would
		// silently change the git identity of runs (and possibly lose access).
		// An explicit github_token_secret in the request is likewise
		// superseded by the existing wiring in this mode.
		if existing.Spec.GitHubApp != nil || strings.TrimSpace(existing.Spec.Defaults.Secrets.GithubToken) != "" {
			secrets.GithubToken = existing.Spec.Defaults.Secrets.GithubToken
		}
		defaults.Secrets = secrets
	} else if err := validateProviderAuthConfiguration(provider, authMode, defaults.Secrets.ClaudeApiKey, defaults.Secrets.OpenAIOAuthSecret, defaults.Secrets.ProviderKeys); err != nil { //nolint:staticcheck // legacy field retained for the explicit anthropic API-key path
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	// GitHub identity is fixed during onboarding; provider-default edits must
	// never redirect repository access to another Secret.
	defaults.Secrets.GithubToken = existing.Spec.Defaults.Secrets.GithubToken

	var pollInterval metav1.Duration
	var webhookSecret, triggerKeyword string
	var cancelOnClose bool
	var auth *triggersv1alpha1.TriggerAuth
	var reviewLoop *triggersv1alpha1.ReviewLoopSpec
	var maintainer *triggersv1alpha1.MaintainerSpec
	if req.TriggerSettings != nil {
		var err error
		pollInterval, webhookSecret, triggerKeyword, cancelOnClose, auth, reviewLoop, maintainer, err = protoGitHubTriggerSettingsToCRD(req.GetTriggerSettings())
		if err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
	}

	policyCleanup, err := s.applyTriggerPolicies(ctx, namespace, name, req.GetPolicies(), &defaults)
	if err != nil {
		return nil, err
	}
	reviewerDefaults, reviewerChanged, reviewerCleanup, err := s.resolveGitHubReviewerDefaultsUpdate(ctx, req, existing)
	if err != nil {
		for _, fn := range policyCleanup {
			fn()
		}
		return nil, err
	}
	policyCleanup = append(policyCleanup, reviewerCleanup...)

	priorReviewerDefaults := (*triggersv1alpha1.AgentRunDefaults)(nil)
	if existing.Spec.ReviewLoop != nil && existing.Spec.ReviewLoop.ReviewerDefaults != nil {
		priorReviewerDefaults = existing.Spec.ReviewLoop.ReviewerDefaults.DeepCopy()
	}
	if req.TriggerSettings != nil {
		if maintainer != nil && maintainer.WorkItemCutover == "" && existing.Spec.Maintainer != nil {
			// Older clients omit this optional migration field. Preserve the
			// operator-selected rollback mode rather than defaulting to Controller.
			maintainer.WorkItemCutover = existing.Spec.Maintainer.WorkItemCutover
		}
		existing.Spec.PollInterval = pollInterval
		existing.Spec.WebhookSecret = webhookSecret
		existing.Spec.TriggerKeyword = triggerKeyword
		existing.Spec.CancelRunsOnIssueClose = cancelOnClose
		existing.Spec.Auth = auth
		existing.Spec.ReviewLoop = reviewLoop
		existing.Spec.Maintainer = maintainer
	}
	if reviewerChanged {
		if reviewerDefaults != nil {
			if existing.Spec.ReviewLoop == nil {
				// Reviewer runtime configuration alone must not opt in to the loop.
				existing.Spec.ReviewLoop = &triggersv1alpha1.ReviewLoopSpec{Disabled: true}
			}
			existing.Spec.ReviewLoop.ReviewerDefaults = reviewerDefaults
		} else if existing.Spec.ReviewLoop != nil {
			existing.Spec.ReviewLoop.ReviewerDefaults = nil
		}
	} else if priorReviewerDefaults != nil {
		if existing.Spec.ReviewLoop == nil {
			existing.Spec.ReviewLoop = &triggersv1alpha1.ReviewLoopSpec{Disabled: true}
		}
		existing.Spec.ReviewLoop.ReviewerDefaults = priorReviewerDefaults
	}
	existing.Spec.ReviewLoop = compactReviewLoop(existing.Spec.ReviewLoop)

	preserveAdminOnlyTriggerDefaults(&defaults, existing.Spec.Defaults)
	existing.Spec.Defaults = defaults
	if err := s.k8sClient.Update(ctx, existing); err != nil {
		for _, fn := range policyCleanup {
			fn()
		}
		return nil, mapK8sError("update GitHubRepository", err)
	}

	pb := s.githubRepositoryProto(ctx, existing, nil)
	pb.ResourceOwner, pb.MyPermission = s.resourceACL(ctx, githubRepositoryResourceType, existing.Name, existing.Namespace)
	return pb, nil
}
