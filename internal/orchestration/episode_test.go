package orchestration

import (
	"testing"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
)

func TestAgentRunEpisodeFinished(t *testing.T) {
	t.Parallel()
	run := func(phase platformv1alpha1.AgentRunPhase, step, blockedReason string) *platformv1alpha1.AgentRun {
		out := &platformv1alpha1.AgentRun{Status: platformv1alpha1.AgentRunStatus{Phase: phase, CurrentStep: step}}
		if blockedReason != "" {
			out.Status.Queue = &platformv1alpha1.AgentRunQueueStatus{State: string(phase), BlockedReason: blockedReason}
		}
		return out
	}
	for _, tc := range []struct {
		name     string
		run      *platformv1alpha1.AgentRun
		finished bool
	}{
		{"nil run", nil, false},
		{"actively running", run(platformv1alpha1.AgentRunPhaseRunning, "implementing", ""), false},
		{"pending", run(platformv1alpha1.AgentRunPhasePending, "", ""), false},
		{"admitted", run(platformv1alpha1.AgentRunPhaseAdmitted, "", ""), false},
		{"provisioning waiting on capacity", run(platformv1alpha1.AgentRunPhaseProvisioning, "", "waiting for capacity"), false},
		{"succeeded", run(platformv1alpha1.AgentRunPhaseSucceeded, "", ""), true},
		{"failed", run(platformv1alpha1.AgentRunPhaseFailed, "", ""), true},
		{"cancelled", run(platformv1alpha1.AgentRunPhaseCancelled, "", ""), true},
		{"paused", run(platformv1alpha1.AgentRunPhasePaused, "", ""), true},
		{"question", run(platformv1alpha1.AgentRunPhaseQuestion, "awaiting-user", "question"), true},
		{"waiting approval", run(platformv1alpha1.AgentRunPhaseWaitingApproval, "", "approval"), true},
		{"blocked", run(platformv1alpha1.AgentRunPhaseBlocked, "", "circuit break"), true},
		// The critical case: an implementer that called finish stays Running
		// so review feedback can wake it, but its episode is over.
		{"running but idle after finish", run(platformv1alpha1.AgentRunPhaseRunning, "awaiting-user", "idle"), true},
		{"running with idle queue mirror only", run(platformv1alpha1.AgentRunPhaseRunning, "implementing", "idle"), true},
		{"running with awaiting-user step only", run(platformv1alpha1.AgentRunPhaseRunning, "awaiting-user", ""), true},
		{"running stopped by user", run(platformv1alpha1.AgentRunPhaseRunning, "stopped", ""), true},
		{"running circuit-break step", run(platformv1alpha1.AgentRunPhaseRunning, "blocked", ""), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := AgentRunEpisodeFinished(tc.run); got != tc.finished {
				t.Fatalf("AgentRunEpisodeFinished = %v, want %v", got, tc.finished)
			}
		})
	}
}
