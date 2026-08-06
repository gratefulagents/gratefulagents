package triggers

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/google/go-github/v68/github"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

// SecurityScanTriggerEvent is the platform-stamped payload the GitHub webhook
// ingress writes into the SecurityScanEventAnnotation. Every field is derived
// from the verified webhook delivery — never from model output — so the
// revision the scan runs on (and any check is published on) is authoritative.
type SecurityScanTriggerEvent struct {
	// Token is derived deterministically from (repository, source, revision)
	// so webhook redeliveries and duplicate deliveries for the same head SHA
	// carry the same token and never create duplicate runs.
	Token string `json:"token"`
	// Source is "pull_request" or "push".
	Source string `json:"source"`
	// Repository is the GitHub owner/name the event came from.
	Repository string `json:"repository"`
	// Revision is the event's head commit SHA.
	Revision string `json:"revision"`
	// BaseRevision is the PR base SHA or the push's before SHA; empty when
	// unknown (e.g. a newly created branch).
	BaseRevision string `json:"baseRevision,omitempty"`
	// Branch is the PR head ref or pushed branch name.
	Branch string `json:"branch,omitempty"`
	// PRNumber and PRURL identify the pull request for pull_request events.
	PRNumber int    `json:"prNumber,omitempty"`
	PRURL    string `json:"prURL,omitempty"`
	// Fork reports that the PR head repository differs from the base
	// repository (an untrusted contribution).
	Fork bool `json:"fork,omitempty"`
	// HeadRepo is the PR head repository owner/name when it differs.
	HeadRepo string `json:"headRepo,omitempty"`
}

// securityScanEventToken derives the deterministic idempotency token for a
// repository event.
func securityScanEventToken(repository, source, revision string) string {
	sum := sha1.Sum([]byte(repository + "\x00" + source + "\x00" + revision))
	return hex.EncodeToString(sum[:])[:20]
}

// githubPushEvent is the minimal push webhook payload.
type githubPushEvent struct {
	Ref        string              `json:"ref"` // refs/heads/<branch>
	Before     string              `json:"before"`
	After      string              `json:"after"`
	Deleted    bool                `json:"deleted"`
	Repository githubRepositoryRef `json:"repository"`
}

const githubZeroSHA = "0000000000000000000000000000000000000000"

// handlePushEvent routes push deliveries to security scan triggers. Push
// events have no other consumers today.
func (h *GitHubWebhookHandler) handlePushEvent(ctx context.Context, gh *triggersv1alpha1.GitHubRepository, payload []byte) error {
	if gh == nil {
		return nil
	}
	var event githubPushEvent
	if err := json.Unmarshal(payload, &event); err != nil {
		return errBadWebhookPayload
	}
	branch := strings.TrimPrefix(event.Ref, "refs/heads/")
	if branch == event.Ref || event.Deleted || event.After == "" || event.After == githubZeroSHA {
		// Tag pushes and branch deletions never trigger scans.
		return nil
	}
	base := event.Before
	if base == githubZeroSHA {
		base = ""
	}
	return h.dispatchSecurityScanEvent(ctx, gh, SecurityScanTriggerEvent{
		Token:        securityScanEventToken(event.Repository.FullName, "push", event.After),
		Source:       "push",
		Repository:   event.Repository.FullName,
		Revision:     event.After,
		BaseRevision: base,
		Branch:       branch,
	})
}

// securityScanEventFromPR converts a decoded pull_request delivery into the
// normalized trigger event, or nil when the payload carries no head SHA.
func securityScanEventFromPR(event *githubPullRequestEvent) *SecurityScanTriggerEvent {
	head := strings.TrimSpace(event.PullRequest.Head.SHA)
	if head == "" {
		return nil
	}
	headRepo := strings.TrimSpace(event.PullRequest.Head.Repo.FullName)
	fork := headRepo != "" && !strings.EqualFold(headRepo, event.Repository.FullName)
	ev := &SecurityScanTriggerEvent{
		Token:        securityScanEventToken(event.Repository.FullName, "pull_request", head),
		Source:       "pull_request",
		Repository:   event.Repository.FullName,
		Revision:     head,
		BaseRevision: strings.TrimSpace(event.PullRequest.Base.SHA),
		Branch:       event.PullRequest.Head.Ref,
		PRNumber:     event.PullRequest.Number,
		PRURL:        event.PullRequest.HTMLURL,
		Fork:         fork,
	}
	if fork {
		ev.HeadRepo = headRepo
	}
	return ev
}

// dispatchSecurityScanEvent stamps the scan-event annotation on every
// SecurityScan in the repository's namespace whose spec.triggers matches the
// event. The annotation write is the only side effect: the SecurityScan
// controller performs the durable, idempotent run creation.
func (h *GitHubWebhookHandler) dispatchSecurityScanEvent(ctx context.Context, gh *triggersv1alpha1.GitHubRepository, ev SecurityScanTriggerEvent) error {
	log := logf.FromContext(ctx).WithName("securityscan-events")
	scans := &triggersv1alpha1.SecurityScanList{}
	if err := h.Client.List(ctx, scans, client.InNamespace(gh.Namespace)); err != nil {
		return fmt.Errorf("listing SecurityScans: %w", err)
	}
	payload, err := json.Marshal(ev)
	if err != nil {
		return fmt.Errorf("encoding scan event: %w", err)
	}
	for i := range scans.Items {
		scan := &scans.Items[i]
		if !securityScanMatchesEvent(scan, gh.Name, ev) {
			continue
		}
		if scan.Status.LastEventToken == ev.Token ||
			strings.TrimSpace(scan.Annotations[triggersv1alpha1.SecurityScanEventAnnotation]) == string(payload) {
			continue
		}
		if err := retrySecurityScanPatch(ctx, h.Client, client.ObjectKeyFromObject(scan), func(fresh *triggersv1alpha1.SecurityScan) {
			if fresh.Annotations == nil {
				fresh.Annotations = map[string]string{}
			}
			fresh.Annotations[triggersv1alpha1.SecurityScanEventAnnotation] = string(payload)
		}); err != nil {
			return fmt.Errorf("stamping scan event on SecurityScan %s/%s: %w", scan.Namespace, scan.Name, err)
		}
		log.Info("security scan event stamped", "scan", scan.Name, "source", ev.Source, "revision", ev.Revision)
	}
	return nil
}

// securityScanMatchesEvent reports whether the scan's spec.triggers selects
// this repository event.
func securityScanMatchesEvent(scan *triggersv1alpha1.SecurityScan, repoCRName string, ev SecurityScanTriggerEvent) bool {
	t := scan.Spec.Triggers
	if t == nil || t.RepositoryRef == nil || t.RepositoryRef.Name != repoCRName {
		return false
	}
	switch ev.Source {
	case "pull_request":
		return t.OnPullRequest
	case "push":
		return t.OnPush && securityScanBranchMatches(t.Branches, ev.Branch)
	default:
		return false
	}
}

// securityScanBranchMatches reports whether branch matches any of the
// configured patterns (path.Match globs, with a trailing "*" also acting as a
// plain prefix match). An empty pattern list matches every branch.
func securityScanBranchMatches(patterns []string, branch string) bool {
	if len(patterns) == 0 {
		return true
	}
	for _, p := range patterns {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if ok, err := path.Match(p, branch); err == nil && ok {
			return true
		}
		if strings.HasSuffix(p, "*") && strings.HasPrefix(branch, strings.TrimSuffix(p, "*")) {
			return true
		}
	}
	return false
}

// pendingTriggerEvent returns the scan-event annotation payload when its
// token has not been consumed yet.
func pendingTriggerEvent(scan *triggersv1alpha1.SecurityScan) *SecurityScanTriggerEvent {
	raw := strings.TrimSpace(scan.Annotations[triggersv1alpha1.SecurityScanEventAnnotation])
	if raw == "" {
		return nil
	}
	ev := &SecurityScanTriggerEvent{}
	if err := json.Unmarshal([]byte(raw), ev); err != nil {
		return nil
	}
	if ev.Token == "" || ev.Token == scan.Status.LastEventToken || strings.TrimSpace(ev.Revision) == "" {
		return nil
	}
	return ev
}

// SecurityScanDiffLister lists the files changed between two commits of the
// scan's repository. The default implementation compares via the GitHub API
// with a read-only credential; tests inject fakes.
type SecurityScanDiffLister interface {
	ListChangedFiles(ctx context.Context, gh *triggersv1alpha1.GitHubRepository, base, head string) ([]string, error)
}

// githubSecurityScanDiffLister compares base...head (merge-base semantics)
// through the GitHub compare API using the repository's read-only credential.
type githubSecurityScanDiffLister struct {
	client client.Client
	minter gitHubAppTokenMinter
}

// securityScanDiffMaxFiles caps how many changed files are fetched and
// embedded in the run prompt.
const securityScanDiffMaxFiles = 300

func (l *githubSecurityScanDiffLister) ListChangedFiles(ctx context.Context, gh *triggersv1alpha1.GitHubRepository, base, head string) ([]string, error) {
	token, err := resolveGitHubPollingToken(ctx, l.client, gh, l.minter)
	if err != nil {
		return nil, err
	}
	ghc := github.NewClient(nil).WithAuthToken(token)
	var files []string
	opts := &github.ListOptions{PerPage: 100}
	for {
		cmp, resp, err := ghc.Repositories.CompareCommits(ctx, gh.Spec.Owner, gh.Spec.Repo, base, head, opts)
		if err != nil {
			return nil, fmt.Errorf("comparing %s...%s: %w", base, head, err)
		}
		for _, f := range cmp.Files {
			if name := f.GetFilename(); name != "" {
				files = append(files, name)
			}
			if len(files) >= securityScanDiffMaxFiles {
				return files, nil
			}
		}
		if resp == nil || resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	return files, nil
}

func (r *SecurityScanReconciler) diffLister() SecurityScanDiffLister {
	if r.DiffLister != nil {
		return r.DiffLister
	}
	return &githubSecurityScanDiffLister{client: r.Client, minter: nil}
}

// triggerRepository resolves spec.triggers.repositoryRef.
func (r *SecurityScanReconciler) triggerRepository(ctx context.Context, scan *triggersv1alpha1.SecurityScan) (*triggersv1alpha1.GitHubRepository, error) {
	t := scan.Spec.Triggers
	if t == nil || t.RepositoryRef == nil || strings.TrimSpace(t.RepositoryRef.Name) == "" {
		return nil, fmt.Errorf("spec.triggers.repositoryRef is not set")
	}
	gh := &triggersv1alpha1.GitHubRepository{}
	if err := r.Get(ctx, client.ObjectKey{Namespace: scan.Namespace, Name: t.RepositoryRef.Name}, gh); err != nil {
		return nil, fmt.Errorf("getting GitHubRepository %s/%s: %w", scan.Namespace, t.RepositoryRef.Name, err)
	}
	return gh, nil
}

// reconcileTriggerEvent processes one repository event. The design mirrors
// reconcileRunNow: the run name is derived deterministically from the event
// token, so a crash between run creation and the status update re-enters here
// and CreateTriggerRun observes AlreadyExists instead of creating a second
// run; the consumed token lives in status.lastEventToken, never in memory.
func (r *SecurityScanReconciler) reconcileTriggerEvent(ctx context.Context, scan *triggersv1alpha1.SecurityScan, ev *SecurityScanTriggerEvent) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	allowForks := scan.Spec.Triggers != nil && scan.Spec.Triggers.AllowForks
	if ev.Fork && !allowForks {
		msg := fmt.Sprintf("skipped %s event for fork head repository %s (revision %s): set spec.triggers.allowForks to scan untrusted fork contributions without repository credentials",
			ev.Source, ev.HeadRepo, ev.Revision)
		log.Info("skipping fork-originated security scan event", "headRepo", ev.HeadRepo, "revision", ev.Revision)
		r.recordScanEvent(scan, corev1.EventTypeWarning, "ForkPullRequestSkipped", msg)
		if err := r.updateStatus(ctx, scan, r.summarizeFindings(ctx, scan), func(fresh *triggersv1alpha1.SecurityScan) {
			fresh.Status.LastEventToken = ev.Token
			fresh.Status.LastEventRevision = ev.Revision
			fresh.Status.LastError = msg
			setSecurityScanCondition(fresh, metav1.ConditionFalse, "ForkPullRequestSkipped", msg)
		}); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	externalID := "event-" + securityScanManualRunSuffix(ev.Token)
	if scan.Spec.ConcurrencyPolicy == "" || scan.Spec.ConcurrencyPolicy == triggersv1alpha1.SecurityScanConcurrencyForbid {
		activeRun, err := r.activeScanRun(ctx, scan, externalID)
		if err != nil {
			return ctrl.Result{}, err
		}
		if activeRun != nil {
			msg := fmt.Sprintf("%s event for revision %s skipped: previous run %s still active", ev.Source, ev.Revision, activeRun.Name)
			log.Info("skipping event-triggered scan AgentRun because previous run is still active", "activeRun", activeRun.Name)
			if err := r.updateStatus(ctx, scan, r.summarizeFindings(ctx, scan), func(fresh *triggersv1alpha1.SecurityScan) {
				fresh.Status.LastEventToken = ev.Token
				fresh.Status.LastEventRevision = ev.Revision
				fresh.Status.LastError = msg
				setSecurityScanCondition(fresh, metav1.ConditionFalse, "ConcurrencyBlocked", msg)
			}); err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{RequeueAfter: time.Minute}, nil
		}
	}

	runCtx := &securityScanRunContext{Event: ev}
	if scan.Spec.Triggers != nil && scan.Spec.Triggers.DiffScope {
		runCtx.ChangedFiles, runCtx.DiffFallback = r.eventChangedFiles(ctx, scan, ev)
	}

	runName := securityScanRunName(scan.Name, externalID)
	created, resolvedRefs, err := r.createScanRun(ctx, scan, runName, externalID, externalID, runCtx)
	if err != nil {
		log.Error(err, "failed to create event-triggered scan AgentRun", "run", runName)
		reason := securityScanRunFailureReason(err)
		if statusErr := r.updateStatus(ctx, scan, nil, func(fresh *triggersv1alpha1.SecurityScan) {
			fresh.Status.LastError = err.Error()
			setSecurityScanCondition(fresh, metav1.ConditionFalse, reason, err.Error())
		}); statusErr != nil {
			return ctrl.Result{}, statusErr
		}
		return ctrl.Result{RequeueAfter: time.Minute}, nil
	}

	msg := fmt.Sprintf("Scan AgentRun created for %s event at revision %s", ev.Source, ev.Revision)
	if runCtx.DiffFallback != "" {
		msg += "; diff scope fell back to a full-repository scan: " + runCtx.DiffFallback
	}
	now := metav1.NewTime(r.now())
	generation := scan.Generation
	oneShot := strings.TrimSpace(scan.Spec.Schedule) == ""
	if err := r.updateStatus(ctx, scan, r.summarizeFindings(ctx, scan), func(fresh *triggersv1alpha1.SecurityScan) {
		fresh.Status.Phase = "Running"
		fresh.Status.LastRunName = runName
		fresh.Status.LastScanTime = &now
		fresh.Status.LastEventToken = ev.Token
		fresh.Status.LastEventRevision = ev.Revision
		fresh.Status.LastError = ""
		if oneShot {
			fresh.Status.ObservedGeneration = generation
		}
		if created {
			fresh.Status.RunsCreated++
			fresh.Status.EventRunsCreated++
			fresh.Status.LastResolvedRefs = resolvedRefs
		}
		setSecurityScanCondition(fresh, metav1.ConditionTrue, "EventRunStarted", msg)
	}); err != nil {
		return ctrl.Result{}, err
	}
	if created {
		r.recordScanEvent(scan, corev1.EventTypeNormal, "EventRunStarted", msg)
	}
	return ctrl.Result{RequeueAfter: time.Minute}, nil
}

// eventChangedFiles resolves the diff-scope file list for an event. Any
// failure (missing base revision, unresolvable repository, API error, empty
// diff) falls back to a full-repository scan with an explicit reason.
func (r *SecurityScanReconciler) eventChangedFiles(ctx context.Context, scan *triggersv1alpha1.SecurityScan, ev *SecurityScanTriggerEvent) ([]string, string) {
	if strings.TrimSpace(ev.BaseRevision) == "" {
		return nil, "the event has no base revision (for example a newly created branch), so the changed-file range is unknown"
	}
	gh, err := r.triggerRepository(ctx, scan)
	if err != nil {
		return nil, "the trigger repository could not be resolved: " + err.Error()
	}
	files, err := r.diffLister().ListChangedFiles(ctx, gh, ev.BaseRevision, ev.Revision)
	if err != nil {
		return nil, "the changed files could not be computed: " + err.Error()
	}
	if len(files) == 0 {
		return nil, "the computed diff contained no files"
	}
	return files, ""
}

// recordScanEvent emits a Kubernetes event for the scan when a recorder is
// configured; best-effort.
func (r *SecurityScanReconciler) recordScanEvent(scan *triggersv1alpha1.SecurityScan, eventType, reason, message string) {
	if r.Recorder == nil {
		return
	}
	r.Recorder.Eventf(scan, nil, eventType, reason, reason, message)
}
