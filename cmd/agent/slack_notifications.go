package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	internalslack "github.com/gratefulagents/gratefulagents/internal/slack"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// slackNotificationInterval is how often the connector polls run and PR
	// monitor state for notifications.
	slackNotificationInterval = time.Minute
	// slackNoticeLease is how long one connector holds a notice before another
	// (or a retry after a failed delivery) may send it.
	slackNoticeLease = 2 * time.Minute
	// slackCompletionNoticeKey records delivery of a run's completion notice.
	slackCompletionNoticeKey = "triggers.gratefulagents.dev/slack-completion"
	// slackPRNoticePrefix starts the per-PR delivery marker; the PR URL's hash
	// follows, truncated so the whole name fits Kubernetes' 63-character limit
	// after the prefix slash.
	slackPRNoticePrefix = "triggers.gratefulagents.dev/slack-pr-"
)

// slackOperationalNotice is one private notification about a run. key is the
// run annotation that records delivery; value is what was delivered, so a
// change (new lifecycle, new head) notifies again while a repeat does not.
type slackOperationalNotice struct{ key, value, text string }

// slackMonitorChecks folds a monitor's check-run and commit-status rollups
// into one state for the current head: success, failure, pending, none (no
// checks configured), or unknown (stale, missing, or errored observations).
// Only success and failure are reported, so a missing observation can never
// read as passing.
func slackMonitorChecks(m *triggersv1alpha1.PullRequestMonitor) string {
	s := m.Status
	if s.HeadSHA == "" || s.Checks.HeadSHA != s.HeadSHA || s.Statuses.HeadSHA != s.HeadSHA ||
		s.Checks.Error != "" ||
		s.Statuses.Error != "" {
		return "unknown"
	}
	if s.Checks.Count+s.Statuses.Count == 0 {
		return "none"
	}
	for _, state := range []string{s.Checks.State, s.Statuses.State} {
		if state != "success" && state != "failure" && state != "none" {
			return "pending"
		}
	}
	if s.Checks.State == "failure" || s.Statuses.State == "failure" {
		return "failure"
	}
	return "success"
}

// slackPRNoticeKey is the delivery-marker annotation for one pull request.
func slackPRNoticeKey(url string) string {
	return slackPRNoticePrefix + fmt.Sprintf("%x", sha256.Sum256([]byte(url)))[:32]
}

// slackMonitorNotices derives the notices a PR monitor currently warrants: its
// lifecycle (open, draft, merged, closed) and, once every check on the current
// head has finished, the pass/fail result.
func slackMonitorNotices(m *triggersv1alpha1.PullRequestMonitor) []slackOperationalNotice {
	key := slackPRNoticeKey(m.Spec.URL)
	link := "<" + m.Spec.URL + "|" + internalslack.PullRequestLabel(m.Spec.URL) + ">"
	if title := strings.TrimSpace(m.Status.Title); title != "" {
		link += " — " + internalslack.EscapeMrkdwn(truncateText(title, 120))
	}
	notices := []slackOperationalNotice{}
	if m.Status.Lifecycle != "" && m.Status.PullError == "" {
		notices = append(notices, slackOperationalNotice{
			key:   key,
			value: string(m.Status.Lifecycle),
			text:  lifecycleNotice(m.Status.Lifecycle) + " " + link,
		})
	}
	switch checks := slackMonitorChecks(m); checks {
	case "success", "failure":
		verdict := ":white_check_mark: *Checks passed* on "
		if checks == "failure" {
			verdict = ":x: *Checks failed* on "
		}
		notices = append(notices, slackOperationalNotice{
			key:   key + "-checks",
			value: m.Status.HeadSHA + ":" + checks,
			text:  verdict + link + " (`" + shortSHA(m.Status.HeadSHA) + "`)",
		})
	}
	return notices
}

func lifecycleNotice(lifecycle triggersv1alpha1.PullRequestLifecycle) string {
	switch lifecycle {
	case triggersv1alpha1.PullRequestLifecycleOpen:
		return ":arrow_heading_up: *PR opened*"
	case triggersv1alpha1.PullRequestLifecycleDraft:
		return ":pencil2: *Draft PR opened*"
	case triggersv1alpha1.PullRequestLifecycleMerged:
		return ":tada: *PR merged*"
	case triggersv1alpha1.PullRequestLifecycleClosed:
		return ":no_entry_sign: *PR closed*"
	default:
		return ":arrow_heading_up: *PR " + string(lifecycle) + "*"
	}
}

// completionNotice is the notice for a run that reached a terminal phase.
func completionNotice(run *platformv1alpha1.AgentRun) slackOperationalNotice {
	phase := string(run.Status.Phase)
	value := phase
	if run.Status.CompletedAt != nil && !run.Status.CompletedAt.IsZero() {
		value += ":" + run.Status.CompletedAt.UTC().Format(time.RFC3339)
	}
	return slackOperationalNotice{
		key:   slackCompletionNoticeKey,
		value: value,
		text:  internalslack.PhaseEmoji(phase) + " *Run " + strings.ToLower(phase) + "*",
	}
}

// watchOperationalNotifications polls for and delivers private notifications
// until ctx ends. The first poll runs immediately so a restarted connector
// catches up without waiting a full interval.
func (o *slackOrchestrator) watchOperationalNotifications(ctx context.Context) {
	ticker := time.NewTicker(slackNotificationInterval)
	defer ticker.Stop()
	for {
		pollCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
		err := o.publishOperationalNotifications(pollCtx)
		cancel()
		if err != nil && ctx.Err() == nil {
			log.Printf("slack connector %s: operational notifications: %v", o.agentName, err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// publishOperationalNotifications delivers every undelivered notice for this
// agent's Slack runs: PR lifecycle and completed-check changes from the PR
// monitors each run owns, and the run's own completion. The requester receives
// them while still authorized; otherwise the owner does. One run's failure
// does not block the others; the aggregate error is returned for logging.
func (o *slackOrchestrator) publishOperationalNotifications(ctx context.Context) error {
	if o.ownerUserID == "" || o.crdClient == nil {
		return nil
	}
	runs, err := o.operationalRuns(ctx)
	if err != nil {
		return err
	}
	monitors := &triggersv1alpha1.PullRequestMonitorList{}
	if err := o.crdClient.List(ctx, monitors, client.InNamespace(o.namespace)); err != nil {
		return err
	}
	byRun := monitorsByRun(monitors.Items)
	dms := map[string]string{}
	var errs []error
	for i := range runs {
		run := &runs[i]
		if run.Annotations["triggers.gratefulagents.dev/slack-channel"] == "" {
			continue
		}
		var notices []slackOperationalNotice
		for j := range byRun[run.Name] {
			notices = append(notices, slackMonitorNotices(&byRun[run.Name][j])...)
		}
		if isTerminalPhase(run.Status.Phase) {
			notices = append(notices, completionNotice(run))
		}
		for _, notice := range notices {
			if err := o.deliverOperationalNotice(ctx, run, notice, dms); err != nil {
				// The run object may be stale after a failed patch; leave its
				// remaining notices for the next poll.
				errs = append(errs, fmt.Errorf("%s: %w", run.Name, err))
				break
			}
		}
	}
	return errors.Join(errs...)
}

// deliverOperationalNotice sends one notice at most once per value. It skips
// notices already recorded on the run, takes a short lease under an optimistic
// lock so an overlapping connector does not send the same notice, posts the
// DM, then records delivery. Delivery is at-least-once: a crash between the
// post and the record repeats the notice once the lease expires.
func (o *slackOrchestrator) deliverOperationalNotice(
	ctx context.Context,
	run *platformv1alpha1.AgentRun,
	notice slackOperationalNotice,
	dms map[string]string,
) error {
	if run.Annotations[notice.key] == notice.value {
		return nil
	}
	leaseKey := notice.key + "-lease"
	if expires, _ := strconv.ParseInt(run.Annotations[leaseKey], 10, 64); expires > time.Now().Unix() {
		return nil
	}
	before := run.DeepCopy()
	if run.Annotations == nil {
		run.Annotations = map[string]string{}
	}
	run.Annotations[leaseKey] = strconv.FormatInt(time.Now().Add(slackNoticeLease).Unix(), 10)
	if err := o.crdClient.Patch(
		ctx,
		run,
		client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{}),
	); err != nil {
		if apierrors.IsConflict(err) {
			return nil // another connector or controller moved first; retry next poll
		}
		return fmt.Errorf("leasing notice: %w", err)
	}
	user := run.Annotations[slackRequesterAnnotation]
	if !o.mayStop(user) {
		user = o.ownerUserID
	}
	channel, ok := dms[user]
	if !ok {
		var err error
		if channel, err = o.web.OpenIMWithUser(ctx, user); err != nil {
			return fmt.Errorf("opening DM: %w", err)
		}
		dms[user] = channel
	}
	text := notice.text + "\nRun " + slackRunSummary(run)
	if _, err := o.web.PostMessageAsBot(ctx, channel, text, ""); err != nil {
		return fmt.Errorf("posting notice: %w", err)
	}
	before = run.DeepCopy()
	delete(run.Annotations, leaseKey)
	run.Annotations[notice.key] = notice.value
	if err := o.crdClient.Patch(ctx, run, client.MergeFrom(before)); err != nil {
		return fmt.Errorf("recording delivery: %w", err)
	}
	return nil
}

// truncateText caps s at n runes, appending an ellipsis when cut.
func truncateText(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
