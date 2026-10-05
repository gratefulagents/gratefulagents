package main

import (
	"context"
	"crypto/sha256"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type slackOperationalNotice struct{ key, value, text string }

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

func slackMonitorNotices(m *triggersv1alpha1.PullRequestMonitor) []slackOperationalNotice {
	key := fmt.Sprintf("triggers.gratefulagents.dev/slack-pr-%x", sha256.Sum256([]byte(m.Spec.URL)))
	// Annotation names are limited to 63 characters after the slash.
	key = key[:len("triggers.gratefulagents.dev/slack-pr-")+32]
	notices := []slackOperationalNotice{}
	if m.Status.Lifecycle != "" && m.Status.PullError == "" {
		notices = append(
			notices,
			slackOperationalNotice{
				key:   key,
				value: string(m.Status.Lifecycle),
				text:  fmt.Sprintf("PR %s: %s", m.Status.Lifecycle, m.Spec.URL),
			},
		)
	}
	checks := slackMonitorChecks(m)
	if checks == "success" || checks == "failure" {
		notices = append(
			notices,
			slackOperationalNotice{
				key:   key + "-checks",
				value: m.Status.HeadSHA + ":" + checks,
				text: fmt.Sprintf(
					"Checks completed (%s) for %s at %s",
					checks,
					m.Spec.URL,
					m.Status.HeadSHA,
				),
			},
		)
	}
	return notices
}

func (o *slackOrchestrator) watchOperationalNotifications(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
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
	byRun := map[string][]slackOperationalNotice{}
	for i := range monitors.Items {
		m := &monitors.Items[i]
		byRun[m.Spec.ImplementerRef.Name] = append(
			byRun[m.Spec.ImplementerRef.Name],
			slackMonitorNotices(m)...)
	}
	for i := range runs {
		run := &runs[i]
		if run.Annotations["triggers.gratefulagents.dev/slack-channel"] == "" {
			continue
		}
		notices := byRun[run.Name]
		switch run.Status.Phase {
		case platformv1alpha1.AgentRunPhaseSucceeded,
			platformv1alpha1.AgentRunPhaseFailed,
			platformv1alpha1.AgentRunPhaseCancelled:
			notices = append(
				notices,
				slackOperationalNotice{
					key:   "triggers.gratefulagents.dev/slack-completion",
					value: fmt.Sprintf("%s:%v", run.Status.Phase, run.Status.CompletedAt),
					text:  "Run completed: " + slackRunSummary(run),
				},
			)
		}
		for _, notice := range notices {
			if run.Annotations[notice.key] == notice.value {
				continue
			}
			leaseKey := notice.key + "-lease"
			expires, _ := strconv.ParseInt(run.Annotations[leaseKey], 10, 64)
			if expires > time.Now().Unix() {
				continue
			}
			before := run.DeepCopy()
			if run.Annotations == nil {
				run.Annotations = map[string]string{}
			}
			run.Annotations[leaseKey] = strconv.FormatInt(time.Now().Add(2*time.Minute).Unix(), 10)
			if err := o.crdClient.Patch(
				ctx,
				run,
				client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{}),
			); err != nil {
				return err
			}
			user := run.Annotations[slackRequesterAnnotation]
			if !o.mayStop(user) {
				user = o.ownerUserID
			}
			channel, err := o.web.OpenIMWithUser(ctx, user)
			if err != nil {
				return err
			}
			if _, err := o.web.PostMessageAsBot(ctx, channel, run.Name+"\n"+notice.text, ""); err != nil {
				return err
			}
			before = run.DeepCopy()
			delete(run.Annotations, leaseKey)
			run.Annotations[notice.key] = notice.value
			if err := o.crdClient.Patch(ctx, run, client.MergeFrom(before)); err != nil {
				return err
			}
		}
	}
	return nil
}

func slackMonitorSummary(m *triggersv1alpha1.PullRequestMonitor) string {
	return strings.Join(
		[]string{m.Spec.URL, string(m.Status.Lifecycle), "checks: " + slackMonitorChecks(m)},
		" · ",
	)
}
