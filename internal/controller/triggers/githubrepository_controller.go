package triggers

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"github.com/google/go-github/v68/github"
	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/store"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
)

const (
	defaultGitHubPollInterval             = 60 * time.Second
	maxGitHubIssuePages                   = 10
	maxGitHubIssues                       = 1000
	agentRunAuthorizationIntentAnnotation = "triggers.gratefulagents.dev/authorization-intent-secret"
	agentRunAuthorizationProofAnnotation  = "triggers.gratefulagents.dev/authorization-intent-proof"
	agentRunAuthorizationIntentKey        = "key"
)

var ghNonAlphaNum = regexp.MustCompile(`[^a-z0-9-]`)

type GitHubRepositoryReconciler struct {
	client.Client
	APIReader         client.Reader
	Scheme            *runtime.Scheme
	StateStore        store.StateStore
	GitHubAppMinter   gitHubAppTokenMinter
	Recorder          record.EventRecorder
	MaintainerEnabled bool
	MaintainerEngine  *MaintainerEngine
	GitHubTriage      GitHubTriageClient
	GitHubDelivery    maintainerGitHubDeliveryClient
}

// +kubebuilder:rbac:groups=triggers.gratefulagents.dev,resources=githubrepositories,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=triggers.gratefulagents.dev,resources=githubrepositories/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=triggers.gratefulagents.dev,resources=maintainerworkitems;maintainerworkitemcommands,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups=triggers.gratefulagents.dev,resources=pullrequestmonitors,verbs=get;list;watch
// +kubebuilder:rbac:groups=triggers.gratefulagents.dev,resources=maintainerworkitems/status;maintainerworkitemcommands/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=platform.gratefulagents.dev,resources=agentruns,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;create;update;patch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

func (r *GitHubRepositoryReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	gh := &triggersv1alpha1.GitHubRepository{}
	if err := r.Get(ctx, req.NamespacedName, gh); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// PullRequestMonitor status is already a durable GitHub observation. Project
	// it before any remote issue/auth call so check and review changes advance the
	// work-item semantic cursor—and wake wait_for_repo_events—even while unrelated
	// GitHub polling is slow or temporarily unavailable.
	if maintainerWorkItemsEnabled(r, gh) {
		if err := r.reconcileMaintainerExecutionProjection(ctx, gh); err != nil {
			return ctrl.Result{}, err
		}
	}

	token, err := resolveGitHubToken(ctx, r.Client, gh, r.GitHubAppMinter)
	if err != nil {
		if maintainerWorkItemsEnabled(r, gh) {
			if staleErr := r.markMaintainerWorkItemObservationsUnavailable(ctx, gh, "AuthenticationUnavailable", err.Error()); staleErr != nil {
				return ctrl.Result{}, staleErr
			}
		}
		_ = retryGitHubRepositoryStatusUpdate(ctx, r.Client, client.ObjectKeyFromObject(gh), func(fresh *triggersv1alpha1.GitHubRepository) {
			fresh.Status.LastError = err.Error()
		})
		return r.reconcileWithMaintainer(ctx, gh, nil, false, ctrl.Result{RequeueAfter: time.Minute})
	}

	ghClient := github.NewClient(nil).WithAuthToken(token)

	// Fetch open issues from the repo.
	issues, issueListComplete, err := listOpenGitHubIssues(ctx, ghClient.Issues, gh.Spec.Owner, gh.Spec.Repo, log)
	if err != nil {
		log.Error(err, "failed to fetch issues from GitHub")
		if maintainerWorkItemsEnabled(r, gh) {
			if staleErr := r.markMaintainerWorkItemObservationsUnavailable(ctx, gh, "IssuePollUnavailable", err.Error()); staleErr != nil {
				return ctrl.Result{}, staleErr
			}
		}
		_ = retryGitHubRepositoryStatusUpdate(ctx, r.Client, client.ObjectKeyFromObject(gh), func(fresh *triggersv1alpha1.GitHubRepository) {
			fresh.Status.LastError = err.Error()
		})
		return r.reconcileWithMaintainer(ctx, gh, nil, false, ctrl.Result{RequeueAfter: time.Minute})
	}

	if maintainerWorkItemsEnabled(r, gh) {
		triageClient := r.GitHubTriage
		if triageClient == nil {
			triageClient = githubTriageAdapter{issues: ghClient.Issues}
		}
		if err := r.reconcileMaintainerWorkItems(ctx, gh, issues, issueListComplete, triageClient); err != nil {
			return ctrl.Result{}, err
		}
		deliveryClient := r.GitHubDelivery
		if deliveryClient == nil {
			deliveryClient = newMaintainerGitHubDeliveryClient(ghClient)
		}
		if err := r.reconcileMaintainerExecutionProjection(ctx, gh); err != nil {
			return ctrl.Result{}, err
		}
		if err := r.reconcileMaintainerWorkItemCommands(ctx, gh, triageClient, deliveryClient); err != nil {
			return ctrl.Result{}, err
		}
		if err := r.reconcileMaintainerExecutionProjection(ctx, gh); err != nil {
			return ctrl.Result{}, err
		}
	}

	result, err := r.syncGitHubIssues(ctx, gh, issues)
	if err != nil {
		return result, err
	}
	return r.reconcileWithMaintainer(ctx, gh, issues, true, result)
}

func (r *GitHubRepositoryReconciler) reconcileWithMaintainer(ctx context.Context, gh *triggersv1alpha1.GitHubRepository, issues []*github.Issue, issuesAvailable bool, result ctrl.Result) (ctrl.Result, error) {
	maintainerResult, err := r.reconcileMaintainer(ctx, gh, issues, issuesAvailable)
	if err != nil {
		return ctrl.Result{}, err
	}
	if maintainerResult.RequeueAfter > 0 && (result.RequeueAfter == 0 || maintainerResult.RequeueAfter < result.RequeueAfter) {
		result.RequeueAfter = maintainerResult.RequeueAfter
	}
	return result, nil
}

func (r *GitHubRepositoryReconciler) syncGitHubIssues(ctx context.Context, gh *triggersv1alpha1.GitHubRepository, issues []*github.Issue) (ctrl.Result, error) {
	if gh.Annotations["triggers.gratefulagents.dev/generated-runtime"] == "true" && gh.Annotations["triggers.gratefulagents.dev/project-trigger-issues"] == "false" {
		return r.updateStatusAndRequeue(ctx, gh, 0, nil)
	}
	existing, err := ExistingTriggerIssueIDs(ctx, r.Client, gh.Namespace, gitHubRepositoryTriggerKind, gh.Name)
	if err != nil {
		return ctrl.Result{}, err
	}

	created := 0
	var processedIssueIDs []string
	modeExists := ModeExistsFromK8s(ctx, r.Client)
	for _, issue := range issues {
		if issue.IsPullRequest() {
			continue
		}
		issueID := fmt.Sprintf("%d", issue.GetNumber())
		if _, ok := existing[issueID]; ok {
			continue
		}
		if hasProcessedIssueID(gh.Status.ProcessedIssueIDs, issueID) {
			continue
		}

		// Check if issue has any mode label.
		var labelNames []string
		for _, l := range issue.Labels {
			labelNames = append(labelNames, l.GetName())
		}
		modeRef := ResolveModeFromLabels(labelNames, modeExists)
		if modeRef == nil {
			continue // No mode label = not a trigger candidate
		}

		// Auth check on issue author.
		author := issue.GetUser().GetLogin()
		authorAssociation := issue.GetAuthorAssociation()
		if !gh.Spec.Auth.IsGitHubActorAllowed(author, authorAssociation) {
			recordTriggerActorRejected(ctx, r.Client, r.Recorder, gh, author, authorAssociation)
			continue
		}

		// Build user request from full issue content.
		title := issue.GetTitle()
		body := issue.GetBody()
		userRequest := fmt.Sprintf("# %s\n\n%s", title, body)

		createdRun, err := r.createAgentRun(ctx, gh, issueID, issue.GetNumber(), issue.GetHTMLURL(), userRequest, author, modeRef)
		if err != nil {
			logf.FromContext(ctx).Error(err, "failed to create AgentRun", "issueNumber", issue.GetNumber())
			continue
		}
		// Record the issue as processed even when the run already existed
		// (e.g. another trigger watching the same repository created it
		// first); retrying forever would re-seed the same message every poll.
		processedIssueIDs = append(processedIssueIDs, issueID)
		if createdRun {
			created++
		}
	}

	return r.updateStatusAndRequeue(ctx, gh, created, processedIssueIDs)
}

// HandleIssueComment processes a GitHub issue_comment webhook event.
func (r *GitHubRepositoryReconciler) HandleIssueComment(ctx context.Context, gh *triggersv1alpha1.GitHubRepository, event *github.IssueCommentEvent) error {
	if gh.Annotations["triggers.gratefulagents.dev/generated-runtime"] == "true" && gh.Annotations["triggers.gratefulagents.dev/project-trigger-comments"] == "false" {
		return nil
	}
	if event.GetAction() != githubActionCreated {
		return nil
	}

	comment := event.GetComment()
	issue := event.GetIssue()
	author := comment.GetUser().GetLogin()
	authorAssociation := comment.GetAuthorAssociation()

	// Check if comment mentions the trigger keyword.
	keyword := gh.Spec.TriggerKeyword
	if keyword == "" {
		keyword = "@agent"
	}
	commentBody := comment.GetBody()
	if !strings.Contains(strings.ToLower(commentBody), strings.ToLower(keyword)) {
		return nil
	}

	// Auth check.
	if !gh.Spec.Auth.IsGitHubActorAllowed(author, authorAssociation) {
		recordTriggerActorRejected(ctx, r.Client, r.Recorder, gh, author, authorAssociation)
		return nil
	}

	if maintainerWorkItemsEnabled(r, gh) {
		if decisionID, answer, ok := parseMaintainerDecisionAnswer(commentBody, keyword); ok {
			return r.resolveMaintainerDecisionFromGitHubComment(ctx, gh, issue.GetNumber(), decisionID, answer, author, comment.GetID())
		}
	}

	// Strip the keyword from the comment to get user request.
	userRequest := strings.TrimSpace(strings.Replace(commentBody, keyword, "", 1))
	if userRequest == "" {
		userRequest = "Please help with this task."
	}

	// Mode resolution from both text and issue labels.
	modeExists := ModeExistsFromK8s(ctx, r.Client)
	modeFromText := ResolveModeFromText(userRequest, modeExists)
	userRequest = StripModePrefix(userRequest, modeExists)

	var labelNames []string
	for _, l := range issue.Labels {
		labelNames = append(labelNames, l.GetName())
	}
	labelMode := ResolveModeFromLabels(labelNames, modeExists)

	resolvedMode := MergeModeRef(modeFromText, labelMode, gh.Spec.Defaults.ModeRef)

	issueID := fmt.Sprintf("%d-%d", issue.GetNumber(), comment.GetID())
	_, err := r.createAgentRun(ctx, gh, issueID, issue.GetNumber(), issue.GetHTMLURL(), userRequest, author, resolvedMode)
	return err
}

func (r *GitHubRepositoryReconciler) createAgentRun(ctx context.Context, gh *triggersv1alpha1.GitHubRepository, issueID string, issueNumber int, issueURL, userRequest, author string, modeRef *platformv1alpha1.ModeRef) (bool, error) {
	runName := ghIssueName(gh.Spec.Owner, gh.Spec.Repo, issueID)
	d := gh.Spec.Defaults
	provider := triggersv1alpha1.NormalizeProvider(d.Provider)
	if err := validateTriggerRunDefaults(TriggerRunSpec{
		Namespace:   gh.Namespace,
		TriggerKind: "GitHubRepository",
		TriggerName: gh.Name,
		Defaults:    d,
	}); err != nil {
		return false, err
	}

	annotations := map[string]string{
		"platform.gratefulagents.dev/triggered-by": author,
	}
	if strings.TrimSpace(d.CustomInstructions) != "" {
		instructionsName := runName + "-instructions"
		instructions := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: instructionsName, Namespace: gh.Namespace}, Data: map[string]string{"instructions.md": d.CustomInstructions}}
		if err := ctrl.SetControllerReference(gh, instructions, r.Scheme); err != nil {
			return false, fmt.Errorf("setting owner reference on instructions ConfigMap: %w", err)
		}
		if err := r.Create(ctx, instructions); err != nil {
			if apierrors.IsAlreadyExists(err) {
				existing := &corev1.ConfigMap{}
				if getErr := r.Get(ctx, client.ObjectKey{Namespace: gh.Namespace, Name: instructionsName}, existing); getErr != nil {
					return false, fmt.Errorf("getting existing instructions ConfigMap: %w", getErr)
				}
				if len(existing.Data) != 1 || existing.Data["instructions.md"] != d.CustomInstructions || len(existing.BinaryData) != 0 || !existing.DeletionTimestamp.IsZero() {
					return false, fmt.Errorf("refusing instructions ConfigMap collision for %s/%s", gh.Namespace, instructionsName)
				}
				authorizedOwner := metav1.IsControlledBy(existing, gh)
				if !authorizedOwner {
					persisted := &platformv1alpha1.AgentRun{}
					if getErr := r.maintainerReader().Get(ctx, client.ObjectKey{Name: runName, Namespace: gh.Namespace}, persisted); getErr == nil && metav1.IsControlledBy(existing, persisted) {
						authorizedOwner = runOwnedByGitHubRepository(persisted, gh)
						if !authorizedOwner {
							item := &triggersv1alpha1.MaintainerWorkItem{}
							itemKey := client.ObjectKey{Name: MaintainerWorkItemName(gh.Name, int32(issueNumber)), Namespace: gh.Namespace}
							if itemErr := r.maintainerReader().Get(ctx, itemKey, item); itemErr == nil {
								authorizedOwner = r.requireAuthorizedAgentRun(ctx, gh, item, persisted) == nil || r.claimAgentRunAuthorizationIntent(ctx, gh, item, persisted, issueID) == nil
							}
						}
					}
				}
				if !authorizedOwner {
					return false, fmt.Errorf("refusing instructions ConfigMap %s/%s without trusted controller ownership", gh.Namespace, instructionsName)
				}
				annotations["platform.gratefulagents.dev/instructions-configmap-ref"] = instructionsName
			} else {
				logf.FromContext(ctx).Error(err, "failed to create instructions ConfigMap", "configMap", instructionsName)
			}
		} else {
			annotations["platform.gratefulagents.dev/instructions-configmap-ref"] = instructionsName
		}
	}
	if triggersv1alpha1.IsOpenAICompatibleProvider(provider) {
		annotations["platform.gratefulagents.dev/openai-api-mode"] = triggersv1alpha1.NormalizeOpenAIAPIForProvider(provider, d.OpenAIAPI)
	}

	runContext := &platformv1alpha1.AgentRunContext{
		ProjectRef: &platformv1alpha1.ProjectRef{Kind: "GitHubRepository", Name: gh.Name},
	}

	gitHubTokenSecret := gh.Spec.GitHubTokenSecret
	if gh.Spec.GitHubApp != nil {
		gitHubTokenSecret = runName + "-gh-token"
		if _, err := resolveGitHubToken(ctx, r.Client, gh, r.GitHubAppMinter); err != nil {
			return false, fmt.Errorf("minting GitHub App token for AgentRun: %w", err)
		}
	}

	workItemLabels := map[string]string{}
	workItem := &triggersv1alpha1.MaintainerWorkItem{}
	workItemKey := client.ObjectKey{Namespace: gh.Namespace, Name: MaintainerWorkItemName(gh.Name, int32(issueNumber))}
	if err := r.maintainerReader().Get(ctx, workItemKey, workItem); err != nil {
		if !apierrors.IsNotFound(err) {
			return false, fmt.Errorf("getting maintainer work item for issue #%d: %w", issueNumber, err)
		}
		if maintainerWorkItemsEnabled(r, gh) {
			if err := r.ensureMaintainerWorkItem(ctx, gh, workItemKey.Name, int32(issueNumber)); err != nil {
				return false, err
			}
			if err := r.maintainerReader().Get(ctx, workItemKey, workItem); err != nil {
				return false, fmt.Errorf("rereading maintainer work item for issue #%d: %w", issueNumber, err)
			}
		}
	}
	if workItem.UID != "" {
		workItemLabels[triggersv1alpha1.MaintainerWorkItemNameLabelKey] = workItem.Name
		workItemLabels[triggersv1alpha1.MaintainerWorkItemUIDLabelKey] = string(workItem.UID)
	}
	if workItem.UID != "" && isGeneratedProjectRuntime(gh) {
		existing := &platformv1alpha1.AgentRun{}
		if err := r.maintainerReader().Get(ctx, client.ObjectKey{Namespace: gh.Namespace, Name: runName}, existing); apierrors.IsNotFound(err) {
			intentAnnotations, intentErr := r.createAgentRunAuthorizationIntent(ctx, gh, workItem, runName, issueID)
			if intentErr != nil {
				return false, intentErr
			}
			for key, value := range intentAnnotations {
				annotations[key] = value
			}
		} else if err != nil {
			return false, fmt.Errorf("checking for existing AgentRun authorization intent: %w", err)
		}
	}
	createdRun, _, err := CreateTriggerRun(ctx, r.Client, r.StateStore, TriggerRunSpec{
		RunName:             runName,
		Namespace:           gh.Namespace,
		TriggerKind:         "GitHubRepository",
		TriggerName:         gh.Name,
		ExternalID:          issueID,
		ExternalIdentifier:  fmt.Sprintf("#%d", issueNumber),
		ExternalURL:         issueURL,
		SeedMessage:         userRequest,
		Defaults:            d,
		OwnerRef:            gh,
		Scheme:              r.Scheme,
		Annotations:         annotations,
		Labels:              workItemLabels,
		Context:             runContext,
		ModeRef:             modeRef,
		GitHubTokenSecret:   gitHubTokenSecret,
		SeedLogPrefix:       "github",
		SeedOnAlreadyExists: true,
		// Existing runs must be fetched so retries can durably record their
		// controller-issued work-item authorization binding.
		FetchExistingOnAlreadyExists: true,
		AuthorizationReader:          r.maintainerReader(),
		AuthorizePersisted: func(ctx context.Context, persisted *platformv1alpha1.AgentRun, created bool) error {
			if persisted == nil || persisted.Name != runName || persisted.Namespace != gh.Namespace || !TriggerRunMatches(persisted, "GitHubRepository", gh.Name) || persisted.Spec.Trigger.ExternalRef == nil || persisted.Spec.Trigger.ExternalRef.ID != issueID {
				return fmt.Errorf("persisted AgentRun provenance does not match this GitHub event")
			}
			if persisted.Spec.Secrets == nil || persisted.Spec.Secrets.GitHubTokenSecret != gitHubTokenSecret {
				return fmt.Errorf("persisted AgentRun GitHub token destination does not match the expected secret")
			}
			if created {
				if workItem.UID != "" {
					if persisted.Annotations[agentRunAuthorizationIntentAnnotation] != "" {
						if err := r.claimAgentRunAuthorizationIntent(ctx, gh, workItem, persisted, issueID); err != nil {
							return err
						}
					}
					return r.bindAuthorizedAgentRun(ctx, gh, workItem, persisted)
				}
				return nil
			}
			if runOwnedByGitHubRepository(persisted, gh) {
				return nil
			}
			if workItem.UID == "" {
				return fmt.Errorf("existing ownerless AgentRun has no controller-issued work-item binding")
			}
			if err := r.requireAuthorizedAgentRun(ctx, gh, workItem, persisted); err == nil {
				return nil
			}
			if err := r.claimAgentRunAuthorizationIntent(ctx, gh, workItem, persisted, issueID); err != nil {
				return err
			}
			return r.bindAuthorizedAgentRun(ctx, gh, workItem, persisted)
		},
		AfterCreate: func(ctx context.Context, run *platformv1alpha1.AgentRun, created bool) error {
			if err := ensureRunGitHubAppTokenSecret(ctx, r.Client, r.Scheme, gh, run, r.GitHubAppMinter); err != nil {
				if created {
					_ = r.Delete(ctx, run)
				}
				return err
			}
			return nil
		},
	})
	if err != nil {
		return false, err
	}
	return createdRun, nil
}

func (r *GitHubRepositoryReconciler) createAgentRunAuthorizationIntent(ctx context.Context, repository *triggersv1alpha1.GitHubRepository, workItem *triggersv1alpha1.MaintainerWorkItem, runName, issueID string) (map[string]string, error) {
	if repository == nil || repository.UID == "" || workItem == nil || workItem.UID == "" || strings.TrimSpace(runName) == "" {
		return nil, fmt.Errorf("stable repository, work-item, and run identities are required for authorization intent")
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("generating AgentRun authorization key: %w", err)
	}
	for attempt := 0; attempt < 5; attempt++ {
		suffix := make([]byte, 12)
		if _, err := rand.Read(suffix); err != nil {
			return nil, fmt.Errorf("generating AgentRun authorization intent name: %w", err)
		}
		secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "run-auth-" + hex.EncodeToString(suffix), Namespace: repository.Namespace}, Type: corev1.SecretTypeOpaque, Data: map[string][]byte{agentRunAuthorizationIntentKey: key}}
		if err := ctrl.SetControllerReference(workItem, secret, r.Scheme); err != nil {
			return nil, fmt.Errorf("owning AgentRun authorization intent: %w", err)
		}
		if err := r.Create(ctx, secret); err != nil {
			if apierrors.IsAlreadyExists(err) {
				continue
			}
			return nil, fmt.Errorf("creating AgentRun authorization intent: %w", err)
		}
		proof := agentRunAuthorizationIntentProof(key, repository.UID, workItem.UID, runName, issueID)
		return map[string]string{agentRunAuthorizationIntentAnnotation: secret.Name, agentRunAuthorizationProofAnnotation: proof}, nil
	}
	return nil, fmt.Errorf("could not allocate a unique AgentRun authorization intent")
}

func (r *GitHubRepositoryReconciler) claimAgentRunAuthorizationIntent(ctx context.Context, repository *triggersv1alpha1.GitHubRepository, workItem *triggersv1alpha1.MaintainerWorkItem, run *platformv1alpha1.AgentRun, issueID string) error {
	if repository == nil || repository.UID == "" || workItem == nil || workItem.UID == "" || run == nil || run.UID == "" || run.Annotations[platformv1alpha1.AuthorizationPendingAnnotation] != "true" {
		return fmt.Errorf("existing AgentRun has no recoverable authorization hold")
	}
	secretName := strings.TrimSpace(run.Annotations[agentRunAuthorizationIntentAnnotation])
	proof := strings.TrimSpace(run.Annotations[agentRunAuthorizationProofAnnotation])
	if secretName == "" || proof == "" {
		return fmt.Errorf("existing AgentRun has no controller-issued authorization intent")
	}
	secretKey := client.ObjectKey{Name: secretName, Namespace: run.Namespace}
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		secret := &corev1.Secret{}
		if err := r.maintainerReader().Get(ctx, secretKey, secret); err != nil {
			return fmt.Errorf("reading AgentRun authorization intent: %w", err)
		}
		if !secret.DeletionTimestamp.IsZero() {
			return fmt.Errorf("AgentRun authorization intent is being deleted")
		}
		key := secret.Data[agentRunAuthorizationIntentKey]
		if len(key) != 32 {
			return fmt.Errorf("AgentRun authorization intent key is invalid")
		}
		expected := agentRunAuthorizationIntentProof(key, repository.UID, workItem.UID, run.Name, issueID)
		if !hmac.Equal([]byte(proof), []byte(expected)) {
			return fmt.Errorf("AgentRun authorization intent proof is invalid")
		}
		if metav1.IsControlledBy(secret, run) {
			return nil
		}
		if !metav1.IsControlledBy(secret, workItem) {
			return fmt.Errorf("AgentRun authorization intent was consumed by a different AgentRun UID")
		}
		patch := client.MergeFromWithOptions(secret.DeepCopy(), client.MergeFromWithOptimisticLock{})
		secret.OwnerReferences = nil
		if err := ctrl.SetControllerReference(run, secret, r.Scheme); err != nil {
			return fmt.Errorf("claiming AgentRun authorization intent: %w", err)
		}
		return r.Patch(ctx, secret, patch)
	})
}

func agentRunAuthorizationIntentProof(key []byte, repositoryUID, workItemUID types.UID, runName, issueID string) string {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(string(repositoryUID) + "\x00" + string(workItemUID) + "\x00" + runName + "\x00" + issueID))
	return hex.EncodeToString(mac.Sum(nil))
}

func (r *GitHubRepositoryReconciler) requireAuthorizedAgentRun(ctx context.Context, repository *triggersv1alpha1.GitHubRepository, expectedItem *triggersv1alpha1.MaintainerWorkItem, run *platformv1alpha1.AgentRun) error {
	if repository == nil || repository.UID == "" || expectedItem == nil || expectedItem.UID == "" || run == nil || run.UID == "" || run.Namespace != repository.Namespace {
		return fmt.Errorf("repository, work item, and AgentRun require stable matching identities")
	}
	fresh := &triggersv1alpha1.MaintainerWorkItem{}
	if err := r.maintainerReader().Get(ctx, client.ObjectKeyFromObject(expectedItem), fresh); err != nil {
		return err
	}
	if fresh.UID != expectedItem.UID || fresh.Spec.RepositoryRef.Name != repository.Name || !fresh.DeletionTimestamp.IsZero() {
		return fmt.Errorf("maintainer work item identity changed")
	}
	ownedByRepository := false
	for _, owner := range fresh.OwnerReferences {
		if owner.Controller != nil && *owner.Controller && owner.APIVersion == triggersv1alpha1.GroupVersion.String() && owner.Kind == "GitHubRepository" && owner.Name == repository.Name && owner.UID == repository.UID {
			ownedByRepository = true
			break
		}
	}
	if !ownedByRepository {
		return fmt.Errorf("maintainer work item is not owned by the current GitHubRepository UID")
	}
	for _, binding := range fresh.Status.AuthorizedAgentRuns {
		if binding.Name == run.Name && binding.UID == run.UID {
			return nil
		}
	}
	return fmt.Errorf("existing AgentRun UID has no controller-issued authorization binding")
}

func (r *GitHubRepositoryReconciler) bindAuthorizedAgentRun(ctx context.Context, repository *triggersv1alpha1.GitHubRepository, expectedItem *triggersv1alpha1.MaintainerWorkItem, run *platformv1alpha1.AgentRun) error {
	if repository == nil || repository.UID == "" || expectedItem == nil || expectedItem.UID == "" || run == nil || run.UID == "" || run.Namespace != repository.Namespace {
		return fmt.Errorf("repository, work item, and AgentRun require stable matching identities")
	}
	return r.retryMaintainerWorkItemStatusMutation(ctx, client.ObjectKeyFromObject(expectedItem), func(fresh *triggersv1alpha1.MaintainerWorkItem) (bool, error) {
		if fresh.UID != expectedItem.UID || fresh.Spec.RepositoryRef.Name != repository.Name || !fresh.DeletionTimestamp.IsZero() {
			return false, fmt.Errorf("maintainer work item identity changed")
		}
		ownedByRepository := false
		for _, owner := range fresh.OwnerReferences {
			if owner.Controller != nil && *owner.Controller && owner.APIVersion == triggersv1alpha1.GroupVersion.String() && owner.Kind == "GitHubRepository" && owner.Name == repository.Name && owner.UID == repository.UID {
				ownedByRepository = true
				break
			}
		}
		if !ownedByRepository {
			return false, fmt.Errorf("maintainer work item is not owned by the current GitHubRepository UID")
		}
		binding := triggersv1alpha1.MaintainerAuthorizedAgentRunReference{Name: run.Name, UID: run.UID}
		for i := range fresh.Status.AuthorizedAgentRuns {
			if fresh.Status.AuthorizedAgentRuns[i].Name == run.Name {
				if fresh.Status.AuthorizedAgentRuns[i].UID == run.UID {
					return false, nil
				}
				fresh.Status.AuthorizedAgentRuns[i] = binding
				return true, nil
			}
		}
		fresh.Status.AuthorizedAgentRuns = append(fresh.Status.AuthorizedAgentRuns, binding)
		return true, nil
	})
}

func (r *GitHubRepositoryReconciler) updateStatusAndRequeue(ctx context.Context, gh *triggersv1alpha1.GitHubRepository, created int, processedIssueIDs []string) (ctrl.Result, error) {
	if err := retryGitHubRepositoryStatusUpdate(ctx, r.Client, client.ObjectKeyFromObject(gh), func(fresh *triggersv1alpha1.GitHubRepository) {
		now := metav1.Now()
		fresh.Status.LastPollTime = &now
		fresh.Status.LastError = ""
		fresh.Status.IssuesProcessed += int32(created)
		fresh.Status.ProcessedIssueIDs = appendProcessedIssueIDs(fresh.Status.ProcessedIssueIDs, processedIssueIDs...)
	}); err != nil && !apierrors.IsNotFound(err) {
		return ctrl.Result{}, fmt.Errorf("updating GitHubRepository status: %w", err)
	}
	pollInterval := gh.Spec.PollInterval.Duration
	if pollInterval == 0 {
		pollInterval = defaultGitHubPollInterval
	}
	return ctrl.Result{RequeueAfter: pollInterval}, nil
}

func ghIssueName(owner, repo, issueID string) string {
	sanitized := ghNonAlphaNum.ReplaceAllString(strings.ToLower(fmt.Sprintf("%s-%s-%s", owner, repo, issueID)), "-")
	sanitized = strings.Trim(sanitized, "-")
	if sanitized == "" {
		sanitized = "issue"
	}
	name := "gh-" + sanitized
	if len(name) <= 63 {
		return name
	}
	hashBytes := sha1.Sum([]byte(name))
	hash := hex.EncodeToString(hashBytes[:])[:8]
	maxBase := max(63-len("gh-")-len("-")-len(hash), 1)
	if len(sanitized) > maxBase {
		sanitized = strings.TrimRight(sanitized[:maxBase], "-")
	}
	return "gh-" + sanitized + "-" + hash
}

type githubIssueLister interface {
	ListByRepo(context.Context, string, string, *github.IssueListByRepoOptions) ([]*github.Issue, *github.Response, error)
}

func listOpenGitHubIssues(ctx context.Context, issues githubIssueLister, owner, repo string, logger logr.Logger) ([]*github.Issue, bool, error) {
	opts := &github.IssueListByRepoOptions{
		State:       "open",
		ListOptions: github.ListOptions{PerPage: 100},
	}
	var all []*github.Issue
	for pagesFetched, page := 0, 1; ; pagesFetched++ {
		opts.Page = page
		batch, resp, err := issues.ListByRepo(ctx, owner, repo, opts)
		if err != nil {
			return nil, false, err
		}
		all = append(all, batch...)
		hasNextPage := resp != nil && resp.NextPage != 0
		if len(all) >= maxGitHubIssues {
			complete := len(all) == maxGitHubIssues && !hasNextPage
			if !complete {
				logger.Info("hit GitHub issue pagination cap", "owner", owner, "repo", repo, "issues", len(all), "maxIssues", maxGitHubIssues)
			}
			return all[:maxGitHubIssues], complete, nil
		}
		if !hasNextPage {
			return all, true, nil
		}
		if pagesFetched+1 >= maxGitHubIssuePages {
			logger.Info("hit GitHub issue page cap", "owner", owner, "repo", repo, "pages", pagesFetched+1, "maxPages", maxGitHubIssuePages)
			return all, false, nil
		}
		page = resp.NextPage
	}
}

func (r *GitHubRepositoryReconciler) mapMaintainerWorkItemCommandToRepository(_ context.Context, obj client.Object) []ctrl.Request {
	command, ok := obj.(*triggersv1alpha1.MaintainerWorkItemCommand)
	if !ok || command.Spec.RepositoryRef.Name == "" {
		return nil
	}
	return []ctrl.Request{{NamespacedName: client.ObjectKey{Namespace: command.Namespace, Name: command.Spec.RepositoryRef.Name}}}
}

func (r *GitHubRepositoryReconciler) mapPullRequestMonitorToRepository(ctx context.Context, obj client.Object) []ctrl.Request {
	monitor, ok := obj.(*triggersv1alpha1.PullRequestMonitor)
	if !ok {
		return nil
	}
	if monitor.Spec.GitHubRepositoryRef != nil && monitor.Spec.GitHubRepositoryRef.Name != "" {
		return []ctrl.Request{{NamespacedName: client.ObjectKey{Namespace: monitor.Namespace, Name: monitor.Spec.GitHubRepositoryRef.Name}}}
	}
	// Backward compatibility for monitors created before githubRepositoryRef was
	// persisted: resolve the controller-owned implementer and its repository.
	run := &platformv1alpha1.AgentRun{}
	if err := r.maintainerReader().Get(ctx, client.ObjectKey{Namespace: monitor.Namespace, Name: monitor.Spec.ImplementerRef.Name}, run); err != nil {
		return nil
	}
	for _, owner := range run.OwnerReferences {
		if owner.Controller != nil && *owner.Controller && owner.Kind == gitHubRepositoryTriggerKind && owner.Name != "" {
			return []ctrl.Request{{NamespacedName: client.ObjectKey{Namespace: monitor.Namespace, Name: owner.Name}}}
		}
	}
	return nil
}

func pullRequestMonitorProjectionChanged(oldMonitor, newMonitor *triggersv1alpha1.PullRequestMonitor) bool {
	if oldMonitor == nil || newMonitor == nil {
		return oldMonitor != newMonitor
	}
	oldStatus, newStatus := oldMonitor.Status, newMonitor.Status
	return oldStatus.State != newStatus.State ||
		oldStatus.HeadSHA != newStatus.HeadSHA ||
		oldStatus.BaseRef != newStatus.BaseRef ||
		oldStatus.Lifecycle != newStatus.Lifecycle ||
		oldStatus.Mergeability != newStatus.Mergeability ||
		oldStatus.ReviewDecision != newStatus.ReviewDecision ||
		oldStatus.PullError != newStatus.PullError ||
		oldStatus.ReviewsError != newStatus.ReviewsError ||
		oldStatus.Checks.HeadSHA != newStatus.Checks.HeadSHA ||
		oldStatus.Checks.State != newStatus.Checks.State ||
		oldStatus.Checks.Count != newStatus.Checks.Count ||
		oldStatus.Checks.Error != newStatus.Checks.Error ||
		oldStatus.Statuses.HeadSHA != newStatus.Statuses.HeadSHA ||
		oldStatus.Statuses.State != newStatus.Statuses.State ||
		oldStatus.Statuses.Count != newStatus.Statuses.Count ||
		oldStatus.Statuses.Error != newStatus.Statuses.Error
}

func (r *GitHubRepositoryReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		// Reconciles rewrite status.lastPollTime on every pass; without a
		// predicate that status write re-triggers the watch and the controller
		// hot-loops polling GitHub instead of waiting for RequeueAfter.
		For(&triggersv1alpha1.GitHubRepository{}, builder.WithPredicates(predicate.Or(
			predicate.GenerationChangedPredicate{},
			predicate.AnnotationChangedPredicate{},
			predicate.LabelChangedPredicate{},
		))).
		Owns(&platformv1alpha1.AgentRun{}).
		Watches(&triggersv1alpha1.PullRequestMonitor{}, handler.EnqueueRequestsFromMapFunc(r.mapPullRequestMonitorToRepository), builder.WithPredicates(predicate.Funcs{
			CreateFunc: func(event.CreateEvent) bool { return true },
			UpdateFunc: func(update event.UpdateEvent) bool {
				oldMonitor, oldOK := update.ObjectOld.(*triggersv1alpha1.PullRequestMonitor)
				newMonitor, newOK := update.ObjectNew.(*triggersv1alpha1.PullRequestMonitor)
				return oldOK && newOK && pullRequestMonitorProjectionChanged(oldMonitor, newMonitor)
			},
			DeleteFunc: func(event.DeleteEvent) bool { return true },
		})).
		Watches(&triggersv1alpha1.MaintainerWorkItemCommand{}, handler.EnqueueRequestsFromMapFunc(r.mapMaintainerWorkItemCommandToRepository), builder.WithPredicates(predicate.Funcs{
			CreateFunc: func(event.CreateEvent) bool { return true },
			UpdateFunc: func(update event.UpdateEvent) bool {
				return update.ObjectOld.GetGeneration() != update.ObjectNew.GetGeneration()
			},
		})).
		Named("githubrepository").
		WithOptions(controller.Options{MaxConcurrentReconciles: 1}).
		Complete(r)
}
