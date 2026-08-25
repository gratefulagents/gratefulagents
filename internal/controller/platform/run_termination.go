package platform

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
)

// Termination policy knobs. Each is overridable through an environment
// variable so operators can tune fleets without a rebuild.
const (
	// podStartupDeadlineEnv bounds how long a runner pod may stay in
	// PodPending before the run fails with a classified diagnosis instead of
	// silently consuming its maxRuntime budget (default 6h) while, for
	// example, its image cannot be pulled or no node can schedule it.
	podStartupDeadlineEnv     = "AGENTRUN_POD_STARTUP_DEADLINE_SECONDS"
	defaultPodStartupDeadline = 15 * time.Minute

	// podForceDeleteAfterEnv bounds how long a drain waits for a Terminating
	// pod past its own deletion deadline before escalating to a force delete
	// (GracePeriodSeconds=0). Pods on lost nodes otherwise stay Terminating
	// forever and wedge cancellation, wake, restart, and AgentRun deletion.
	podForceDeleteAfterEnv     = "AGENTRUN_POD_FORCE_DELETE_AFTER_SECONDS"
	defaultPodForceDeleteAfter = 90 * time.Second

	// claimDrainGiveUpAfterEnv bounds how long a drain blocks on a
	// SandboxClaim stuck Terminating behind a third-party finalizer. Past the
	// deadline the drain proceeds (the claim's controller owns the cleanup)
	// so a broken external controller cannot hang run termination forever.
	claimDrainGiveUpAfterEnv     = "AGENTRUN_CLAIM_DRAIN_GIVE_UP_AFTER_SECONDS"
	defaultClaimDrainGiveUpAfter = 10 * time.Minute

	// terminalSandboxTTLEnv is how long a terminal (Succeeded/Failed/
	// Cancelled) run keeps its sandbox compute before the controller drains
	// it. The delay preserves pod logs for post-mortem debugging; without a
	// TTL, completed runs accumulate zombie sandboxes forever.
	terminalSandboxTTLEnv     = "AGENTRUN_TERMINAL_SANDBOX_TTL_SECONDS"
	defaultTerminalSandboxTTL = 10 * time.Minute

	// overseerDetachDeadline bounds how long an overseer detach may sit in
	// the "detaching" state before its status is surfaced as degraded. The
	// underlying standing-run drain keeps escalating independently.
	overseerDetachDeadline = 5 * time.Minute
)

func durationFromSecondsEnv(name string, fallback time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback
	}
	seconds, err := strconv.Atoi(raw)
	if err != nil || seconds <= 0 {
		return fallback
	}
	return time.Duration(seconds) * time.Second
}

func podStartupDeadline() time.Duration {
	return durationFromSecondsEnv(podStartupDeadlineEnv, defaultPodStartupDeadline)
}

func podForceDeleteAfter() time.Duration {
	return durationFromSecondsEnv(podForceDeleteAfterEnv, defaultPodForceDeleteAfter)
}

func claimDrainGiveUpAfter() time.Duration {
	return durationFromSecondsEnv(claimDrainGiveUpAfterEnv, defaultClaimDrainGiveUpAfter)
}

func terminalSandboxTTL() time.Duration {
	return durationFromSecondsEnv(terminalSandboxTTLEnv, defaultTerminalSandboxTTL)
}

// classifyPodFailure builds a human-readable diagnosis of a failed or stuck
// runner pod from the pod object itself: scheduling conditions, init and main
// container waiting/terminated states (exit codes, OOM kills, image errors),
// and the pod-level reason. It never returns an empty string.
func classifyPodFailure(pod *corev1.Pod) string {
	if pod == nil {
		return "no pod diagnostics available"
	}
	var parts []string
	if reason := strings.TrimSpace(pod.Status.Reason); reason != "" {
		detail := reason
		if msg := strings.TrimSpace(pod.Status.Message); msg != "" {
			detail += ": " + msg
		}
		parts = append(parts, detail)
	}
	for _, cond := range pod.Status.Conditions {
		if cond.Type == corev1.PodScheduled && cond.Status == corev1.ConditionFalse {
			detail := "unschedulable"
			if reason := strings.TrimSpace(cond.Reason); reason != "" && !strings.EqualFold(reason, "Unschedulable") {
				detail = "unschedulable (" + reason + ")"
			}
			if msg := strings.TrimSpace(cond.Message); msg != "" {
				detail += ": " + msg
			}
			parts = append(parts, detail)
		}
	}
	parts = append(parts, describeContainerStatuses("init container", pod.Status.InitContainerStatuses)...)
	parts = append(parts, describeContainerStatuses("container", pod.Status.ContainerStatuses)...)
	if len(parts) == 0 {
		return fmt.Sprintf("pod phase %s with no diagnostic details", pod.Status.Phase)
	}
	return strings.Join(parts, "; ")
}

func describeContainerStatuses(kind string, statuses []corev1.ContainerStatus) []string {
	var parts []string
	for _, status := range statuses {
		switch {
		case status.State.Terminated != nil && status.State.Terminated.ExitCode != 0:
			t := status.State.Terminated
			detail := fmt.Sprintf("%s %q exited with code %d", kind, status.Name, t.ExitCode)
			if reason := strings.TrimSpace(t.Reason); reason != "" {
				detail += " (" + reason + ")"
			}
			if t.Signal != 0 {
				detail += fmt.Sprintf(" [signal %d]", t.Signal)
			}
			if msg := strings.TrimSpace(t.Message); msg != "" {
				detail += ": " + truncateDiagnostic(msg)
			}
			parts = append(parts, detail)
		case status.State.Waiting != nil && !benignWaitingReason(status.State.Waiting.Reason):
			w := status.State.Waiting
			detail := fmt.Sprintf("%s %q waiting (%s)", kind, status.Name, strings.TrimSpace(w.Reason))
			if msg := strings.TrimSpace(w.Message); msg != "" {
				detail += ": " + truncateDiagnostic(msg)
			}
			parts = append(parts, detail)
		case status.LastTerminationState.Terminated != nil && status.LastTerminationState.Terminated.Reason == "OOMKilled":
			parts = append(parts, fmt.Sprintf("%s %q previously OOMKilled", kind, status.Name))
		}
	}
	return parts
}

// benignWaitingReason reports whether a container waiting reason is a normal
// startup state rather than a failure signal.
func benignWaitingReason(reason string) bool {
	switch strings.TrimSpace(reason) {
	case "", "ContainerCreating", "PodInitializing":
		return true
	default:
		return false
	}
}

// fatalPodStartupReason reports a waiting reason that can never self-heal:
// there is no point burning the startup deadline waiting on it. Transient
// reasons (ImagePullBackOff, Unschedulable, …) are left to the deadline
// because registries flake and autoscalers add nodes.
func fatalPodStartupReason(pod *corev1.Pod) (string, bool) {
	if pod == nil {
		return "", false
	}
	check := func(statuses []corev1.ContainerStatus) (string, bool) {
		for _, status := range statuses {
			if status.State.Waiting == nil {
				continue
			}
			switch status.State.Waiting.Reason {
			case "InvalidImageName", "CreateContainerConfigError":
				detail := fmt.Sprintf("container %q: %s", status.Name, status.State.Waiting.Reason)
				if msg := strings.TrimSpace(status.State.Waiting.Message); msg != "" {
					detail += ": " + truncateDiagnostic(msg)
				}
				return detail, true
			}
		}
		return "", false
	}
	if detail, fatal := check(pod.Status.InitContainerStatuses); fatal {
		return detail, true
	}
	return check(pod.Status.ContainerStatuses)
}

func truncateDiagnostic(msg string) string {
	const maxLen = 300
	msg = strings.TrimSpace(msg)
	if len(msg) <= maxLen {
		return msg
	}
	return msg[:maxLen] + "…"
}

// podDrainEscalationDue reports whether a Terminating pod has outlived its
// own graceful-deletion deadline by the escalation window and should be
// force-deleted. deletionTimestamp already includes the grace period, so the
// window is measured from it directly.
func podDrainEscalationDue(pod *corev1.Pod, now time.Time) bool {
	if pod == nil || pod.DeletionTimestamp == nil || pod.DeletionTimestamp.IsZero() {
		return false
	}
	return now.Sub(pod.DeletionTimestamp.Time) > podForceDeleteAfter()
}

// claimDrainAbandoned reports whether a Terminating SandboxClaim has been
// stuck long enough that the drain should stop blocking on it.
func claimDrainAbandoned(deletionTimestamp *time.Time, now time.Time) bool {
	if deletionTimestamp == nil || deletionTimestamp.IsZero() {
		return false
	}
	return now.Sub(*deletionTimestamp) > claimDrainGiveUpAfter()
}
