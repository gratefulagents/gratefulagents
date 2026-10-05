package main

import (
	"context"
	"strings"
	"testing"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func operationalTestMonitor() *triggersv1alpha1.PullRequestMonitor {
	return &triggersv1alpha1.PullRequestMonitor{
		ObjectMeta: metav1.ObjectMeta{Name: "pr-one", Namespace: "ns"},
		Spec: triggersv1alpha1.PullRequestMonitorSpec{
			URL:            "https://github.com/acme/one/pull/1",
			ImplementerRef: corev1.LocalObjectReference{Name: "run-one"},
		},
		Status: triggersv1alpha1.PullRequestMonitorStatus{
			Lifecycle: triggersv1alpha1.PullRequestLifecycleOpen,
			HeadSHA:   "new",
			Checks: triggersv1alpha1.PullRequestMonitorHeadRollup{
				HeadSHA: "new",
				State:   "success",
				Count:   1,
			},
			Statuses: triggersv1alpha1.PullRequestMonitorHeadRollup{HeadSHA: "new", State: "none"},
		},
	}
}

func TestSlackMonitorChecks(t *testing.T) {
	for _, tt := range []struct {
		name, want string
		mutate     func(*triggersv1alpha1.PullRequestMonitor)
	}{
		{"success", "success", func(m *triggersv1alpha1.PullRequestMonitor) {}},
		{"failure", "failure", func(m *triggersv1alpha1.PullRequestMonitor) {
			m.Status.Statuses.State = "failure"
			m.Status.Statuses.Count = 1
		}},
		{"pending", "pending", func(m *triggersv1alpha1.PullRequestMonitor) { m.Status.Checks.State = "pending" }},
		{"stale", "unknown", func(m *triggersv1alpha1.PullRequestMonitor) { m.Status.Checks.HeadSHA = "old" }},
		{"error", "unknown", func(m *triggersv1alpha1.PullRequestMonitor) { m.Status.Statuses.Error = "API failed" }},
		{"empty", "none", func(m *triggersv1alpha1.PullRequestMonitor) {
			m.Status.Checks.Count = 0
			m.Status.Checks.State = "none"
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			m := operationalTestMonitor()
			tt.mutate(m)
			if got := slackMonitorChecks(m); got != tt.want {
				t.Fatalf("checks=%s want %s", got, tt.want)
			}
			for _, notice := range slackMonitorNotices(m) {
				if strings.HasSuffix(notice.key, "-checks") && tt.want != "success" &&
					tt.want != "failure" {
					t.Fatal("notified incomplete checks")
				}
			}
		})
	}
}

func TestSlackOperationalNotificationsPersistAndDeduplicate(t *testing.T) {
	run := operationalTestRun()
	m := operationalTestMonitor()
	foreign := m.DeepCopy()
	foreign.Name = "foreign"
	foreign.Spec.ImplementerRef.Name = "other"
	foreign.Spec.URL = "https://github.com/private/other/pull/2"
	o, f := operationalTestOrchestrator(t, run, m, foreign)
	ctx := context.Background()
	if err := o.publishOperationalNotifications(ctx); err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(strings.Join(f.methods(), ","), "chat.postMessage"); got != 2 {
		t.Fatalf("messages=%d want 2", got)
	}
	for i, method := range f.calls {
		if method == "chat.postMessage" &&
			(f.form[i].Get("channel") != "D1" || strings.Contains(f.form[i].Get("text"), "private/other")) {
			t.Fatal("notification not scoped/private")
		}
	}
	if f.form[0].Get("users") != "UCMD" {
		t.Fatalf("notification not sent to requester: %v", f.form[0])
	}
	saved := &platformv1alpha1.AgentRun{}
	if err := o.crdClient.Get(ctx, client.ObjectKeyFromObject(run), saved); err != nil {
		t.Fatal(err)
	}
	for _, notice := range slackMonitorNotices(m) {
		if saved.Annotations[notice.key] != notice.value || saved.Annotations[notice.key+"-lease"] != "" {
			t.Fatalf("missing durable delivery marker: %v", saved.Annotations)
		}
	}
	before := len(f.methods())
	restarted := &slackOrchestrator{
		web:         o.web,
		crdClient:   o.crdClient,
		namespace:   o.namespace,
		agentName:   o.agentName,
		ownerUserID: o.ownerUserID,
		commanders:  o.commanders,
	}
	if err := restarted.publishOperationalNotifications(ctx); err != nil {
		t.Fatal(err)
	}
	if len(f.methods()) != before {
		t.Fatal("restart duplicated notifications")
	}
	m.Status.Lifecycle = triggersv1alpha1.PullRequestLifecycleMerged
	if err := o.crdClient.Update(ctx, m); err != nil {
		t.Fatal(err)
	}
	if err := o.publishOperationalNotifications(ctx); err != nil {
		t.Fatal(err)
	}
	if len(f.methods()) != before+2 {
		t.Fatal("merge should send one new notification")
	}
}

func TestSlackOperationalNotificationFailureRetriesAfterLease(t *testing.T) {
	run := operationalTestRun()
	m := operationalTestMonitor()
	o, f := operationalTestOrchestrator(t, run, m)
	f.fail["chat.postMessage"] = "ratelimited"
	ctx := context.Background()
	if err := o.publishOperationalNotifications(ctx); err == nil {
		t.Fatal("expected delivery failure")
	}
	notice := slackMonitorNotices(m)[0]
	got := &platformv1alpha1.AgentRun{}
	if err := o.crdClient.Get(ctx, client.ObjectKeyFromObject(run), got); err != nil {
		t.Fatal(err)
	}
	if got.Annotations[notice.key] != "" || got.Annotations[notice.key+"-lease"] == "" {
		t.Fatal("failed delivery marked successful or no lease")
	}
	delete(f.fail, "chat.postMessage")
	got.Annotations[notice.key+"-lease"] = "1"
	if err := o.crdClient.Update(ctx, got); err != nil {
		t.Fatal(err)
	}
	if err := o.publishOperationalNotifications(ctx); err != nil {
		t.Fatal(err)
	}
	if err := o.crdClient.Get(ctx, client.ObjectKeyFromObject(run), got); err != nil {
		t.Fatal(err)
	}
	if got.Annotations[notice.key] != notice.value {
		t.Fatal("failed delivery was not retried")
	}
}
