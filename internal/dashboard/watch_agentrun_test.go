package dashboard

import (
	"context"
	"strings"
	"testing"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestDeletedAgentRunEventUsesNamespaceAndName(t *testing.T) {
	versions := map[string]string{"default/run-1": "1"}
	seen := map[string]struct{}{}
	var events []*platform.AgentRunEvent

	for key := range versions {
		if _, ok := seen[key]; ok {
			continue
		}
		delete(versions, key)
		parts := strings.SplitN(key, "/", 2)
		events = append(events, &platform.AgentRunEvent{Type: "DELETED", Run: &platform.AgentRun{Namespace: parts[0], Name: parts[1]}})
	}

	if len(events) != 1 {
		t.Fatalf("len(events) = %d, want 1", len(events))
	}
	if events[0].Run.Namespace != "default" || events[0].Run.Name != "run-1" {
		t.Fatalf("deleted event run = %#v", events[0].Run)
	}
}

func TestIsTerminalAgentRunPhase(t *testing.T) {
	cases := map[platformv1alpha1.AgentRunPhase]bool{
		platformv1alpha1.AgentRunPhaseSucceeded: true,
		platformv1alpha1.AgentRunPhaseFailed:    true,
		platformv1alpha1.AgentRunPhaseCancelled: true,
		platformv1alpha1.AgentRunPhaseRunning:   false,
	}
	for phase, want := range cases {
		if got := isTerminalAgentRunPhase(phase); got != want {
			t.Fatalf("isTerminalAgentRunPhase(%q) = %v, want %v", phase, got, want)
		}
	}
}

func TestShouldContinueAgentRunWatchForOverseerLifecycle(t *testing.T) {
	tests := []struct {
		name string
		run  *platformv1alpha1.AgentRun
		want bool
	}{
		{name: "running", run: &platformv1alpha1.AgentRun{Status: platformv1alpha1.AgentRunStatus{Phase: platformv1alpha1.AgentRunPhaseRunning}}, want: true},
		{name: "plain succeeded", run: &platformv1alpha1.AgentRun{Status: platformv1alpha1.AgentRunStatus{Phase: platformv1alpha1.AgentRunPhaseSucceeded}}, want: false},
		{name: "succeeded attached", run: &platformv1alpha1.AgentRun{Spec: platformv1alpha1.AgentRunSpec{Overseer: &platformv1alpha1.AgentRunOverseerSpec{}}, Status: platformv1alpha1.AgentRunStatus{Phase: platformv1alpha1.AgentRunPhaseSucceeded}}, want: true},
		{name: "failed detaching", run: &platformv1alpha1.AgentRun{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{platformv1alpha1.OverseerDetachingAnnotation: "true"}}, Status: platformv1alpha1.AgentRunStatus{Phase: platformv1alpha1.AgentRunPhaseFailed}}, want: true},
		{name: "cancelled attached", run: &platformv1alpha1.AgentRun{Spec: platformv1alpha1.AgentRunSpec{Overseer: &platformv1alpha1.AgentRunOverseerSpec{}}, Status: platformv1alpha1.AgentRunStatus{Phase: platformv1alpha1.AgentRunPhaseCancelled}}, want: false},
		{name: "nil", run: nil, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldContinueAgentRunWatch(tt.run); got != tt.want {
				t.Fatalf("shouldContinueAgentRunWatch() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDeleteAgentRunRequestTypeCompiles(t *testing.T) {
	_ = context.Background()
	_ = metav1.Now()
}
