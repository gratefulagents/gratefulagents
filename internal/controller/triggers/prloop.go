package triggers

import (
	"context"
	"fmt"
	"strings"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/orchestration"
	"github.com/gratefulagents/gratefulagents/internal/store"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

// Labels and annotations tracking the autonomous PR review loop. The loop
// state machine is derived from these markers — there is no separate store.
const (
	// PRLoopStateLabel marks an implementer AgentRun as participating in the
	// loop. Values: in_review, resolving, approved, blocked.
	PRLoopStateLabel = "triggers.gratefulagents.dev/pr-loop"
	// PRLoopRoleLabel marks reviewer AgentRuns created by the loop.
	PRLoopRoleLabel = triggersv1alpha1.PRLoopRoleLabelKey
	// PRLoopNumberLabel carries the PR number on implementer runs so comment
	// events (which lack the head ref) can find the linked run.
	PRLoopNumberLabel = "triggers.gratefulagents.dev/pr-number"

	// PRLoopURLAnnotation, PRLoopBaseRefAnnotation and PRLoopRoundAnnotation
	// snapshot PR coordinates and progress on the implementer run.
	PRLoopURLAnnotation       = "triggers.gratefulagents.dev/pr-url"
	PRLoopBaseRefAnnotation   = "triggers.gratefulagents.dev/pr-base-ref"
	PRLoopRoundAnnotation     = "triggers.gratefulagents.dev/review-round"
	PRLoopMaxRoundsAnnotation = "triggers.gratefulagents.dev/review-max-rounds"
	// PRLoopOptAnnotation is the trigger-independent, per-run loop policy.
	// The loop is disabled by default; set it to "enabled" before opening a PR
	// to opt that run in. "disabled" remains an explicit opt-out.
	PRLoopOptAnnotation = "triggers.gratefulagents.dev/review-loop"
	PRLoopOptEnabled    = "enabled"
	PRLoopOptDisabled   = "disabled"
	// PRLoopImplementerAnnotation points a reviewer run back at the
	// implementer run it reviews.
	PRLoopImplementerAnnotation = "triggers.gratefulagents.dev/pr-loop-implementer"
	// PRLoopRepositoryAnnotation names the GitHubRepository that owns the loop.
	PRLoopRepositoryAnnotation = "triggers.gratefulagents.dev/pr-loop-repository"
	// PRLoopVerdictHandledAnnotation marks a reviewer run whose verdict has
	// been consumed by the loop reconciler (idempotency guard).
	PRLoopVerdictHandledAnnotation = "triggers.gratefulagents.dev/verdict-handled"
)

// PR loop states stored in PRLoopStateLabel.
const (
	PRLoopStateInReview  = "in_review"
	PRLoopStateResolving = "resolving"
	PRLoopStateApproved  = "approved"
	PRLoopStateBlocked   = "blocked"
)

const (
	// PRLoopRoleReviewer is the PRLoopRoleLabel value for reviewer runs.
	PRLoopRoleReviewer = triggersv1alpha1.PRLoopRoleReviewerValue
	// defaultMaxReviewRounds caps automatic review/resolve cycles per PR.
	defaultMaxReviewRounds = 3
	// defaultReviewerModeName is the ModeTemplate used for reviewer runs when
	// the GitHubRepository does not override it.
	defaultReviewerModeName = "review"
	openAIAPIModeAnnotation = "platform.gratefulagents.dev/openai-api-mode"
)

// PRLoopEngine drives the autonomous loop:
//
//	issue → implementer run → PR opened → reviewer run → verdict
//	  approve          → loop approved (human merges)
//	  request_changes  → wake implementer (resume session) → push fixes →
//	                     implementer finishes → next review round → …
//
// It consumes normalized webhook events (PullRequestEventSink) and is driven
// by run completions via the PRLoopReconciler. Loop-runaway protection:
// reviewer rounds are only spawned from run completions (never from
// synchronize events), self-authored GitHub reviews are ignored, reviewer run
// names are deterministic per round, and rounds are capped.
type PRLoopEngine struct {
	client.Client
	Scheme     *runtime.Scheme
	StateStore store.StateStore
	Recorder   record.EventRecorder
	// GitHubAppMinter mints installation tokens for reviewer runs when the
	// owning GitHubRepository uses App auth. Nil falls back to a fresh minter.
	GitHubAppMinter gitHubAppTokenMinter
}

var _ PullRequestEventSink = (*PRLoopEngine)(nil)

// HandlePullRequestEvent routes normalized webhook events into the loop. gh is
// optional for app deliveries; event.Repository then supplies owner/name.
func (e *PRLoopEngine) HandlePullRequestEvent(ctx context.Context, gh *triggersv1alpha1.GitHubRepository, event PullRequestEvent) (bool, error) {
	if event.Repository == "" && gh != nil {
		event.Repository = strings.TrimSpace(gh.Spec.Owner) + "/" + strings.TrimSpace(gh.Spec.Repo)
	}
	switch event.Type {
	case PREventOpened:
		return e.handlePROpened(ctx, gh, event)
	case PREventReviewSubmitted:
		return e.handleReviewSubmitted(ctx, gh, event)
	case PREventComment:
		return e.handlePRComment(ctx, gh, event)
	default:
		// synchronize / review_comment are intentionally ignored: review
		// rounds are driven by run completions, and inline comments arrive
		// alongside their review submission.
		return false, nil
	}
}

// handlePROpened links an agent-created PR to its implementer run and starts
// review round 1. PRs whose head branch does not match an AgentRun are not
// part of the loop (human PRs).
func (e *PRLoopEngine) handlePROpened(ctx context.Context, gh *triggersv1alpha1.GitHubRepository, event PullRequestEvent) (bool, error) {
	implementer, err := e.implementerForPullRequestEvent(ctx, event, false)
	if err == nil && implementer == nil {
		implementer, err = e.findImplementerByHead(ctx, gh, event.Repository, event.URL, event.HeadRef)
	}
	if err != nil || implementer == nil {
		return false, err
	}
	cfg, err := e.resolveLoopConfig(ctx, gh, implementer, event.Repository, event.BaseRef)
	if err != nil {
		return false, err
	}
	if cfg.Disabled {
		return false, nil
	}

	loop := loopRecordForEvent(event)
	if hasLoopRecord(implementer, loop.Key) {
		return true, nil // already in the loop (redelivery or reopen)
	}
	if err := e.patchLoopMarkers(ctx, implementer, loop.Key, PRLoopStateInReview, func(run *platformv1alpha1.AgentRun) {
		setLoopLabel(run, loop.Key, PRLoopNumberLabel, fmt.Sprintf("%d", event.Number))
		setLoopAnnotation(run, loop.Key, PRLoopURLAnnotation, event.URL)
		setLoopAnnotation(run, loop.Key, PRLoopBaseRefAnnotation, event.BaseRef)
		setLoopAnnotation(run, loop.Key, PRLoopRoundAnnotation, "1")
		setLoopAnnotation(run, loop.Key, PRLoopMaxRoundsAnnotation, fmt.Sprintf("%d", cfg.MaxRounds))
		setLoopAnnotation(run, loop.Key, PRLoopRepositoryAnnotation, cfg.RepositoryName)
	}); err != nil {
		return false, err
	}

	if err := e.createReviewerRun(ctx, cfg, implementer, loop, event.Title, 1); err != nil {
		return false, err
	}
	e.eventf(implementer, "PRLoopReviewStarted", "Started review round 1 for %s", event.URL)
	return true, nil
}

// handleReviewSubmitted reacts to GitHub reviews from OTHER identities
// (humans, or a differently-authenticated reviewer). Reviews posted through
// the run's own token share the PR author's identity and are ignored here —
// those verdicts travel through submit_review_verdict instead.
func (e *PRLoopEngine) handleReviewSubmitted(ctx context.Context, gh *triggersv1alpha1.GitHubRepository, event PullRequestEvent) (bool, error) {
	if event.SenderLogin == "" || event.SenderLogin == event.AuthorLogin {
		return false, nil
	}
	var auth *triggersv1alpha1.TriggerAuth
	if gh != nil {
		auth = gh.Spec.Auth
	}
	if !auth.IsGitHubActorAllowed(event.SenderLogin, event.SenderAuthorAssociation) {
		if gh != nil {
			recordTriggerActorRejected(ctx, e.Client, e.Recorder, gh, event.SenderLogin, event.SenderAuthorAssociation)
		}
		return false, nil
	}
	loopKey := prLoopKey(event.Repository, event.Number)
	implementer, err := e.implementerForPullRequestEvent(ctx, event, true)
	if err == nil && implementer == nil {
		implementer, loopKey, err = e.findImplementerByPR(ctx, gh, event.Repository, event.Number)
	}
	if err != nil || implementer == nil {
		return false, err
	}
	if state := loopState(implementer, loopKey, PRLoopStateLabel); state == PRLoopStateApproved || state == PRLoopStateBlocked {
		return true, nil
	}
	if externalPREventHandled(implementer, loopKey, event) {
		return true, nil
	}

	state := strings.ToLower(strings.TrimSpace(event.ReviewState))
	switch state {
	case "approved":
		if err := e.patchLoopMarkers(ctx, implementer, loopKey, PRLoopStateApproved, func(run *platformv1alpha1.AgentRun) {
			markExternalPREvent(run, loopKey, event)
		}); err != nil {
			return false, err
		}
		e.eventf(implementer, "PRLoopApproved", "PR #%d approved by %s — ready for human merge", event.Number, event.SenderLogin)
		return true, nil
	case "changes_requested", "commented":
		if state == "commented" && strings.TrimSpace(event.Body) == "" {
			return false, nil
		}
		msg := fmt.Sprintf(
			"GitHub review feedback on your pull request #%d (%s) from @%s (%s):\n\n%s\n\n"+
				"Address this feedback on your existing branch: inspect unresolved threads with "+
				"list_review_threads, make the fixes, push, reply to and resolve each thread, "+
				"then finish.",
			event.Number, event.URL, event.SenderLogin, state, event.Body)
		if err := e.wakeImplementerForPREvent(ctx, implementer, loopKey, PRLoopStateResolving, msg, event); err != nil {
			return false, err
		}
		return true, nil
	default:
		return false, nil
	}
}

// handlePRComment wakes the implementer when someone mentions the trigger
// keyword on its PR.
func (e *PRLoopEngine) handlePRComment(ctx context.Context, gh *triggersv1alpha1.GitHubRepository, event PullRequestEvent) (bool, error) {
	keyword := "@agent"
	if gh != nil && strings.TrimSpace(gh.Spec.TriggerKeyword) != "" {
		keyword = gh.Spec.TriggerKeyword
	}
	if !strings.Contains(strings.ToLower(event.Body), strings.ToLower(keyword)) {
		return false, nil
	}
	var auth *triggersv1alpha1.TriggerAuth
	if gh != nil {
		auth = gh.Spec.Auth
	}
	if !auth.IsGitHubActorAllowed(event.SenderLogin, event.SenderAuthorAssociation) {
		if gh != nil {
			recordTriggerActorRejected(ctx, e.Client, e.Recorder, gh, event.SenderLogin, event.SenderAuthorAssociation)
		}
		return false, nil
	}
	loopKey := prLoopKey(event.Repository, event.Number)
	implementer, err := e.implementerForPullRequestEvent(ctx, event, true)
	if err == nil && implementer == nil {
		implementer, loopKey, err = e.findImplementerByPR(ctx, gh, event.Repository, event.Number)
	}
	if err != nil || implementer == nil {
		return false, err
	}
	if state := loopState(implementer, loopKey, PRLoopStateLabel); state == PRLoopStateApproved || state == PRLoopStateBlocked {
		return true, nil
	}
	if externalPREventHandled(implementer, loopKey, event) {
		return true, nil
	}
	body := strings.TrimSpace(strings.Replace(event.Body, keyword, "", 1))
	if body == "" {
		body = "Please review the latest feedback on the pull request and address it."
	}
	msg := fmt.Sprintf("Comment from @%s on your pull request #%d (%s):\n\n%s",
		event.SenderLogin, event.Number, event.URL, body)
	if err := e.wakeImplementerForPREvent(ctx, implementer, loopKey, PRLoopStateResolving, msg, event); err != nil {
		return false, err
	}
	return true, nil
}

// OnReviewerRunCompleted consumes a finished reviewer run's verdict and either
// approves the loop or wakes the implementer for the next resolve cycle.
func (e *PRLoopEngine) OnReviewerRunCompleted(ctx context.Context, reviewer *platformv1alpha1.AgentRun) error {
	log := logf.FromContext(ctx)
	if reviewer.Annotations[PRLoopVerdictHandledAnnotation] == "true" {
		return nil
	}
	implementerName := reviewer.Annotations[PRLoopImplementerAnnotation]
	if implementerName == "" {
		return nil
	}
	implementer := &platformv1alpha1.AgentRun{}
	if err := e.Get(ctx, client.ObjectKey{Namespace: reviewer.Namespace, Name: implementerName}, implementer); err != nil {
		if apierrors.IsNotFound(err) {
			return e.markVerdictHandled(ctx, reviewer)
		}
		return err
	}
	loopKey := strings.TrimSpace(reviewer.Annotations[PRLoopKeyAnnotation])
	loop := loopRecordFromRun(implementer, loopKey)
	currentState := loopState(implementer, loop.Key, PRLoopStateLabel)
	if currentState == PRLoopStateApproved || currentState == PRLoopStateBlocked {
		return e.markVerdictHandled(ctx, reviewer)
	}

	verdict := reviewer.Annotations[platformv1alpha1.ReviewVerdictAnnotation]
	summary := strings.TrimSpace(reviewer.Annotations[platformv1alpha1.ReviewSummaryAnnotation])

	if reviewer.Status.Phase == platformv1alpha1.AgentRunPhaseFailed {
		// A crashed reviewer must not block the loop silently; surface it.
		if err := e.patchLoopMarkers(ctx, implementer, loop.Key, PRLoopStateBlocked, nil); err != nil {
			return err
		}
		e.eventf(implementer, "PRLoopBlocked", "Reviewer run %s failed; human attention required", reviewer.Name)
		e.escalateBlocked(ctx, implementer, fmt.Sprintf("The PR review loop is blocked: reviewer run %s failed. Review the PR manually or send guidance to continue.", reviewer.Name))
		return e.markVerdictHandled(ctx, reviewer)
	}

	switch verdict {
	case platformv1alpha1.ReviewVerdictApprove:
		if err := e.patchLoopMarkers(ctx, implementer, loop.Key, PRLoopStateApproved, nil); err != nil {
			return err
		}
		e.eventf(implementer, "PRLoopApproved", "Reviewer approved PR %s — ready for human merge", loop.URL)
	case platformv1alpha1.ReviewVerdictRequestChanges, "":
		if verdict == "" {
			log.Info("reviewer run completed without a verdict; treating as request_changes", "reviewer", reviewer.Name)
			summary = "The reviewer left feedback on the pull request but did not record a structured verdict."
		}
		round := annotationInt(loopAnnotation(implementer, loop.Key, PRLoopRoundAnnotation), 1)
		maxRounds := annotationInt(loopAnnotation(implementer, loop.Key, PRLoopMaxRoundsAnnotation), reviewLoopMaxRounds(e.lookupRepositoryForLoop(ctx, implementer, loop.Repository)))
		if round >= maxRounds {
			if err := e.patchLoopMarkers(ctx, implementer, loop.Key, PRLoopStateBlocked, nil); err != nil {
				return err
			}
			e.eventf(implementer, "PRLoopBlocked", "Review round cap (%d) reached without approval; human attention required", maxRounds)
			e.escalateBlocked(ctx, implementer, fmt.Sprintf(
				"The PR review loop is blocked after %d review rounds without approval on %s. Last reviewer summary: %s",
				maxRounds, loop.URL, summary))
		} else {
			msg := fmt.Sprintf(
				"Your pull request #%d (%s) was reviewed — changes requested (round %d of %d).\n\n"+
					"Reviewer summary:\n%s\n\n"+
					"Work on your existing branch: inspect every unresolved thread with "+
					"list_review_threads, address each finding, push your fixes, reply to and "+
					"resolve each thread with reply_to_review_thread/resolve_review_thread, then "+
					"finish. Another review round starts automatically.",
				loop.Number, loop.URL, round, maxRounds, summary)
			if err := e.wakeImplementer(ctx, implementer, loop.Key, PRLoopStateResolving, msg); err != nil {
				return err
			}
			e.eventf(implementer, "PRLoopChangesRequested", "Reviewer requested changes (round %d/%d); implementer woken", round, maxRounds)
		}
	}
	return e.markVerdictHandled(ctx, reviewer)
}

// OnImplementerRunCompleted starts the next review round after the implementer
// finishes a resolve cycle.
func (e *PRLoopEngine) OnImplementerRunCompleted(ctx context.Context, implementer *platformv1alpha1.AgentRun) error {
	for _, loop := range loopRecords(implementer) {
		if loopState(implementer, loop.Key, PRLoopStateLabel) != PRLoopStateResolving || loop.Number == 0 {
			continue
		}
		gh := e.lookupRepositoryForLoop(ctx, implementer, loop.Repository)
		cfg, err := e.resolveLoopConfig(ctx, gh, implementer, loop.Repository, loop.BaseRef)
		if err != nil {
			return err
		}
		if cfg.Disabled {
			continue
		}
		round := annotationInt(loopAnnotation(implementer, loop.Key, PRLoopRoundAnnotation), 1) + 1
		if err := e.patchLoopMarkers(ctx, implementer, loop.Key, PRLoopStateInReview, func(run *platformv1alpha1.AgentRun) {
			setLoopAnnotation(run, loop.Key, PRLoopRoundAnnotation, fmt.Sprintf("%d", round))
		}); err != nil {
			return err
		}
		if err := e.createReviewerRun(ctx, cfg, implementer, loop, "", round); err != nil {
			return err
		}
		e.eventf(implementer, "PRLoopReviewStarted", "Started review round %d for %s", round, loop.URL)
	}
	return nil
}

// wakeImplementer delivers context to the implementer run. Wakeable inactive
// runs are woken (fresh pod resumes the persisted session); active runs just
// receive the message, which the live pod picks up.
func (e *PRLoopEngine) wakeImplementer(ctx context.Context, implementer *platformv1alpha1.AgentRun, loopKey, newState, message string) error {
	if err := e.patchLoopMarkers(ctx, implementer, loopKey, newState, nil); err != nil {
		return err
	}
	switch implementer.Status.Phase {
	case platformv1alpha1.AgentRunPhaseSucceeded, platformv1alpha1.AgentRunPhaseFailed, platformv1alpha1.AgentRunPhasePaused:
		if err := orchestration.WakeAgentRun(ctx, e.Client, e.StateStore, implementer.Namespace, implementer.Name, message); err != nil {
			e.escalateBlocked(ctx, implementer, fmt.Sprintf("The PR review loop is blocked: failed to wake implementer run %s: %v", implementer.Name, err))
			return err
		}
		return nil
	case platformv1alpha1.AgentRunPhaseCancelled:
		e.escalateBlocked(ctx, implementer, fmt.Sprintf("The PR review loop is blocked: implementer run %s is cancelled and cannot be woken. Review the PR manually or start a new implementer run.", implementer.Name))
		return nil
	}
	if e.StateStore == nil {
		return fmt.Errorf("state store is required to message AgentRun %s/%s", implementer.Namespace, implementer.Name)
	}
	sess, err := e.StateStore.GetSessionByRun(ctx, implementer.Name, implementer.Namespace)
	if err != nil {
		return fmt.Errorf("getting session for AgentRun %s/%s: %w", implementer.Namespace, implementer.Name, err)
	}
	if _, err := e.StateStore.AppendMessage(ctx, sess.ID, "user", message, nil); err != nil {
		return fmt.Errorf("appending message for AgentRun %s/%s: %w", implementer.Namespace, implementer.Name, err)
	}
	return nil
}

// createReviewerRun provisions an autonomous, review-mode AgentRun for one
// review round. Names are deterministic per (implementer, PR, round), so one
// multi-repo run can review several PRs independently without collisions.
func (e *PRLoopEngine) createReviewerRun(ctx context.Context, cfg *prLoopConfig, implementer *platformv1alpha1.AgentRun, loop prLoopRecord, prTitle string, round int) error {
	runName := reviewerRunName(implementer.Name, loop.Key, round)
	annotations := copyStringMap(cfg.Annotations)
	if annotations == nil {
		annotations = map[string]string{}
	}
	annotations[PRLoopImplementerAnnotation] = implementer.Name
	annotations[PRLoopURLAnnotation] = loop.URL
	annotations[PRLoopRoundAnnotation] = fmt.Sprintf("%d", round)
	annotations[PRLoopRepositoryAnnotation] = cfg.RepositoryName
	annotations[PRLoopKeyAnnotation] = loop.Key
	if strings.TrimSpace(cfg.CustomInstructions) != "" {
		annotations["platform.gratefulagents.dev/instructions-configmap-ref"] = runName + "-instructions"
	}

	run := &platformv1alpha1.AgentRun{ObjectMeta: metav1.ObjectMeta{
		Name:            runName,
		Namespace:       implementer.Namespace,
		Finalizers:      []string{platformv1alpha1.AgentRunCleanupFinalizer},
		OwnerReferences: reviewerOwnerReferences(implementer),
		Labels: map[string]string{
			PRLoopRoleLabel:   PRLoopRoleReviewer,
			PRLoopNumberLabel: fmt.Sprintf("%d", loop.Number),
		},
		Annotations: annotations,
	}}
	if cfg.Repository != nil {
		// GitHubRepository-sourced reviewers retain the existing lifecycle.
		// Fallback reviewers instead share the implementer's trigger owner;
		// owning them by the implementer would classify them as team children.
		run.OwnerReferences = nil
		if err := ctrl.SetControllerReference(cfg.Repository, run, e.Scheme); err != nil {
			return fmt.Errorf("setting repository owner reference on reviewer AgentRun: %w", err)
		}
	}
	var instructions *corev1.ConfigMap
	instructionsCreated := false
	if strings.TrimSpace(cfg.CustomInstructions) != "" {
		instructions = &corev1.ConfigMap{}
		key := client.ObjectKey{Namespace: run.Namespace, Name: runName + "-instructions"}
		if err := e.Get(ctx, key, instructions); apierrors.IsNotFound(err) {
			instructions = &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:            key.Name,
					Namespace:       key.Namespace,
					OwnerReferences: append([]metav1.OwnerReference(nil), run.OwnerReferences...),
				},
				Data: map[string]string{"instructions.md": cfg.CustomInstructions},
			}
			if err := e.Create(ctx, instructions); err != nil {
				return fmt.Errorf("creating reviewer instructions ConfigMap: %w", err)
			}
			instructionsCreated = true
		} else if err != nil {
			return fmt.Errorf("getting reviewer instructions ConfigMap: %w", err)
		} else {
			instructions.Data = map[string]string{"instructions.md": cfg.CustomInstructions}
			if err := e.Update(ctx, instructions); err != nil {
				return fmt.Errorf("updating reviewer instructions ConfigMap: %w", err)
			}
		}
	}

	run.Spec = *cfg.ReviewerSpec.DeepCopy()
	triggerKind, triggerName := "PRReviewLoop", implementer.Name
	if cfg.Repository != nil {
		// Preserve GitHubRepository identity so App-token refresh keeps working
		// for repository-configured reviewers.
		triggerKind, triggerName = gitHubRepositoryTriggerKind, cfg.Repository.Name
	}
	run.Spec.Trigger = platformv1alpha1.TriggerRef{
		Kind: triggerKind,
		Name: triggerName,
		ExternalRef: &platformv1alpha1.ExternalRef{
			ID:         fmt.Sprintf("%s-review-%d", loop.Key, round),
			Identifier: fmt.Sprintf("PR #%d review round %d", loop.Number, round),
			URL:        loop.URL,
		},
	}
	run.Spec.Context = &platformv1alpha1.AgentRunContext{ProjectRef: &platformv1alpha1.ProjectRef{Kind: "AgentRun", Name: implementer.Name}}
	run.Spec.ModeRef = cfg.ReviewerModeRef
	// For an additional-repository PR, make that repository the reviewer's
	// primary checkout so repo_path defaults remain intuitive.
	if repoURL := declaredRepositoryURL(implementer, loop.Repository); repoURL != "" {
		configuredAdditional := append([]string(nil), run.Spec.Repository.AdditionalRepos...)
		run.Spec.Repository.URL = repoURL
		run.Spec.Repository.AdditionalRepos = reviewerAdditionalRepositories(implementer, repoURL, configuredAdditional)
		run.Spec.Repository.BaseBranch = firstNonEmptyString(loop.BaseRef, run.Spec.Repository.BaseBranch)
	}
	if cfg.Repository != nil && cfg.Repository.Spec.GitHubApp != nil {
		if run.Spec.Secrets == nil {
			run.Spec.Secrets = &platformv1alpha1.AgentRunSecrets{}
		}
		run.Spec.Secrets.GitHubTokenSecret = runName + "-gh-token"
	}

	alreadyExists := false
	if err := e.Create(ctx, run); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			if instructionsCreated {
				_ = e.Delete(ctx, instructions)
			}
			return fmt.Errorf("creating reviewer AgentRun: %w", err)
		}
		alreadyExists = true
		if err := e.Get(ctx, client.ObjectKeyFromObject(run), run); err != nil {
			return fmt.Errorf("getting existing reviewer AgentRun: %w", err)
		}
	}
	if instructions != nil {
		instructions.OwnerReferences = nil
		if err := ctrl.SetControllerReference(run, instructions, e.Scheme); err != nil {
			return fmt.Errorf("setting reviewer instructions owner reference: %w", err)
		}
		if err := e.Update(ctx, instructions); err != nil {
			return fmt.Errorf("updating reviewer instructions owner reference: %w", err)
		}
	}
	if cfg.Repository != nil && cfg.Repository.Spec.GitHubApp != nil {
		if err := ensureRunGitHubAppTokenSecret(ctx, e.Client, e.Scheme, cfg.Repository, run, e.GitHubAppMinter); err != nil {
			return fmt.Errorf("minting reviewer GitHub App token: %w", err)
		}
	}
	if alreadyExists {
		return nil
	}

	if e.StateStore != nil {
		task := reviewerTaskMessage(loop.Number, loop.URL, prTitle, implementer.Name, round)
		sess, err := e.StateStore.CreateSession(ctx, run.Name, run.Namespace, "pending", "setup")
		if err != nil {
			logf.FromContext(ctx).Error(err, "failed to create reviewer session", "run", run.Name)
		} else if _, err := e.StateStore.AppendMessage(ctx, sess.ID, "user", task, nil); err != nil {
			logf.FromContext(ctx).Error(err, "failed to seed reviewer task", "run", run.Name)
		}
	}
	return nil
}

func reviewerTaskMessage(prNumber int, prURL, prTitle, implementerName string, round int) string {
	title := strings.TrimSpace(prTitle)
	if title != "" {
		title = fmt.Sprintf(" (%q)", title)
	}
	return fmt.Sprintf(
		"You are the PR reviewer for review round %d of pull request #%d%s: %s\n\n"+
			"The PR was produced autonomously by agent run %q. Review it rigorously:\n"+
			"1. get_pull_request to read the description, changed files, and full diff.\n"+
			"2. Inspect the repository checkout for context around every changed area; "+
			"verify correctness, tests, security, and that the change actually fulfils its task.\n"+
			"3. list_review_threads to see earlier feedback; confirm prior findings were addressed.\n"+
			"4. Post your findings on GitHub with submit_pull_request_review "+
			"(event=COMMENT, inline comments on the exact lines; do not use APPROVE/REQUEST_CHANGES — "+
			"GitHub rejects self-reviews on same-identity PRs).\n"+
			"5. Record your structured verdict with submit_review_verdict: approve only when there "+
			"are no blocking findings; otherwise request_changes with a crisp summary.\n"+
			"6. finish.\n\n"+
			"Do not push commits or edit files — you are a reviewer, not the author.",
		round, prNumber, title, prURL, implementerName)
}

// prReviewerRunName is retained for compatibility with pre-multi-PR reviewer
// names. New reviewers include a PR-key hash via reviewerRunName.
func prReviewerRunName(implementerName string, round int) string {
	return ghIssueName("rev", implementerName, fmt.Sprintf("r%d", round))
}

// patchLoopMarkers updates the loop label plus any extra markers with
// optimistic-concurrency retry, mutating the caller's copy on success.
func (e *PRLoopEngine) patchLoopMarkers(ctx context.Context, run *platformv1alpha1.AgentRun, loopKey, state string, mutate func(*platformv1alpha1.AgentRun)) error {
	fresh := &platformv1alpha1.AgentRun{}
	if err := e.Get(ctx, client.ObjectKeyFromObject(run), fresh); err != nil {
		return err
	}
	patch := client.MergeFrom(fresh.DeepCopy())
	if fresh.Labels == nil {
		fresh.Labels = map[string]string{}
	}
	if fresh.Annotations == nil {
		fresh.Annotations = map[string]string{}
	}
	setLoopLabel(fresh, loopKey, PRLoopStateLabel, state)
	if loopKey != "" {
		setLoopAnnotation(fresh, loopKey, PRLoopKeyAnnotation, loopKey)
	}
	if mutate != nil {
		mutate(fresh)
	}
	if err := e.Patch(ctx, fresh, patch); err != nil {
		return fmt.Errorf("patching PR loop markers on %s/%s: %w", run.Namespace, run.Name, err)
	}
	*run = *fresh
	return nil
}

func (e *PRLoopEngine) markVerdictHandled(ctx context.Context, reviewer *platformv1alpha1.AgentRun) error {
	fresh := &platformv1alpha1.AgentRun{}
	if err := e.Get(ctx, client.ObjectKeyFromObject(reviewer), fresh); err != nil {
		return client.IgnoreNotFound(err)
	}
	patch := client.MergeFrom(fresh.DeepCopy())
	if fresh.Annotations == nil {
		fresh.Annotations = map[string]string{}
	}
	fresh.Annotations[PRLoopVerdictHandledAnnotation] = "true"
	return e.Patch(ctx, fresh, patch)
}

// escalateBlocked surfaces a blocked loop as a pending question on the
// implementer's session so the dashboard requests human input. Best effort:
// the loop label and events remain the source of truth.
func (e *PRLoopEngine) escalateBlocked(ctx context.Context, implementer *platformv1alpha1.AgentRun, reason string) {
	if e.StateStore == nil {
		return
	}
	sess, err := e.StateStore.GetSessionByRun(ctx, implementer.Name, implementer.Namespace)
	if err != nil {
		logf.FromContext(ctx).Error(err, "pr loop: failed to load session for blocked escalation", "run", implementer.Name)
		return
	}
	if err := e.StateStore.SetPendingQuestion(ctx, sess.ID, "blocked", reason, "question"); err != nil {
		logf.FromContext(ctx).Error(err, "pr loop: failed to set blocked question", "run", implementer.Name)
	}
}

func (e *PRLoopEngine) eventf(run *platformv1alpha1.AgentRun, reason, format string, args ...any) {
	if e.Recorder == nil {
		return
	}
	e.Recorder.Eventf(run, corev1.EventTypeNormal, reason, format, args...)
}

func reviewLoopDisabled(gh *triggersv1alpha1.GitHubRepository) bool {
	// An omitted policy is an opt-out. A present policy explicitly enables the
	// loop unless disabled is set, preserving a simple CRD opt-in shape:
	// reviewLoop: {}.
	return gh == nil || gh.Spec.ReviewLoop == nil || gh.Spec.ReviewLoop.Disabled
}

func reviewLoopMaxRounds(gh *triggersv1alpha1.GitHubRepository) int {
	if gh != nil && gh.Spec.ReviewLoop != nil && gh.Spec.ReviewLoop.MaxRounds > 0 {
		return int(gh.Spec.ReviewLoop.MaxRounds)
	}
	return defaultMaxReviewRounds
}

func annotationInt(s string, fallback int) int {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return fallback
		}
		n = n*10 + int(r-'0')
		if n > 1<<30 {
			return fallback
		}
	}
	if s == "" {
		return fallback
	}
	return n
}

func firstNonEmptyString(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
