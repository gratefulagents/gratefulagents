package orchestration

import (
	"strings"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
)

// AgentRunEpisodeFinished reports whether an AgentRun has stopped progressing
// on its own: it is terminal, paused, blocked, or has ended its current
// execution episode and is waiting for external input (for example an
// implementer that called finish and now idles at an input boundary while its
// CRD phase remains Running so review feedback can wake it).
//
// AgentRun phase alone must never be used to infer activity. A run whose
// episode is finished will not produce further work until it is woken, so
// supervisors must act on its existing deliverables instead of waiting for it.
//
// Pre-execution phases (Pending, Admitted, Provisioning) report false: those
// runs progress without supervisor intervention once scheduled.
func AgentRunEpisodeFinished(run *platformv1alpha1.AgentRun) bool {
	if run == nil {
		return false
	}
	switch run.Status.Phase {
	case platformv1alpha1.AgentRunPhaseSucceeded,
		platformv1alpha1.AgentRunPhaseFailed,
		platformv1alpha1.AgentRunPhaseCancelled,
		platformv1alpha1.AgentRunPhasePaused,
		platformv1alpha1.AgentRunPhaseQuestion,
		platformv1alpha1.AgentRunPhaseWaitingApproval,
		platformv1alpha1.AgentRunPhaseBlocked:
		return true
	case platformv1alpha1.AgentRunPhasePending,
		platformv1alpha1.AgentRunPhaseAdmitted,
		platformv1alpha1.AgentRunPhaseProvisioning:
		return false
	}
	switch strings.TrimSpace(run.Status.CurrentStep) {
	case "awaiting-user", "stopped", "blocked":
		return true
	}
	// The session client mirrors every pending user-input boundary (idle,
	// question, approval, ...) into the queue's blocked reason even when the
	// phase stays Running, so a non-empty reason means the current episode is
	// not executing.
	return run.Status.Queue != nil && strings.TrimSpace(run.Status.Queue.BlockedReason) != ""
}
