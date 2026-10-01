package platform

import (
	"context"
	"fmt"
	"strings"
	"time"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const runtimeProfileAdmissionRequeueAfter = 5 * time.Second

type runtimeProfileAdmissionCounts struct {
	cluster   int32
	namespace int32
}

func (r *AgentRunReconciler) enforceRuntimeProfileAdmission(ctx context.Context, run *platformv1alpha1.AgentRun, profile *platformv1alpha1.RuntimeProfile) (*ctrl.Result, error) {
	if run == nil || profile == nil || profile.Spec.Admission == nil {
		return nil, nil
	}
	if runConsumesAdmissionSlot(run) {
		return nil, nil
	}

	admission := profile.Spec.Admission
	if timeout := admission.StaleRunTimeout.Duration; timeout > 0 {
		if startedAt := admissionWaitStartTime(run); !startedAt.IsZero() && time.Since(startedAt) > timeout {
			return &ctrl.Result{}, r.markRunFailed(ctx, run, fmt.Errorf("run exceeded runtime profile staleRunTimeout of %s before admission", timeout))
		}
	}

	if admission.MaxConcurrentRuns <= 0 && admission.PerNamespaceMaxConcurrentRuns <= 0 {
		return nil, nil
	}

	counts, err := r.countActiveRunsForRuntimeProfile(ctx, run, profile)
	if err != nil {
		return nil, err
	}
	if r.APIReader != nil &&
		(admission.MaxConcurrentRuns <= 0 || counts.cluster < admission.MaxConcurrentRuns) &&
		(admission.PerNamespaceMaxConcurrentRuns <= 0 || counts.namespace < admission.PerNamespaceMaxConcurrentRuns) {
		// Indexed cache reads suffice while queued. Only a would-be grant needs
		// a live list: custom field indexes are not supported by the API server.
		counts, err = countActiveRuns(ctx, r.APIReader, run, profile, client.InNamespace(run.Namespace))
		if err != nil {
			return nil, err
		}
	}

	var reasons []string
	if limit := admission.MaxConcurrentRuns; limit > 0 && counts.cluster >= limit {
		reasons = append(reasons, fmt.Sprintf("runtime profile %s reached maxConcurrentRuns=%d", profile.Name, limit))
	}
	if limit := admission.PerNamespaceMaxConcurrentRuns; limit > 0 && counts.namespace >= limit {
		reasons = append(reasons, fmt.Sprintf("runtime profile %s reached perNamespaceMaxConcurrentRuns=%d in namespace %s", profile.Name, limit, run.Namespace))
	}
	if len(reasons) == 0 {
		return nil, nil
	}

	if err := r.queueRunForAdmission(ctx, run, strings.Join(reasons, "; ")); err != nil {
		return nil, err
	}

	result := ctrl.Result{RequeueAfter: runtimeProfileAdmissionRequeueAfter}
	return &result, nil
}

func (r *AgentRunReconciler) queueRunForAdmission(ctx context.Context, run *platformv1alpha1.AgentRun, reason string) error {
	// Admission is polled; writing an unchanged status would only trigger
	// another reconcile.
	if queuedForAdmission(run, reason) {
		return nil
	}
	return retryAgentRunStatusPatch(ctx, r.Client, client.ObjectKeyFromObject(run), func(fresh *platformv1alpha1.AgentRun) {
		if fresh == nil || runStopped(fresh) || runConsumesAdmissionSlot(fresh) || queuedForAdmission(fresh, reason) {
			return
		}
		if fresh.Status.StartedAt == nil {
			now := metav1.Now()
			fresh.Status.StartedAt = &now
		}
		fresh.Status.Phase = platformv1alpha1.AgentRunPhasePending
		fresh.Status.Queue = &platformv1alpha1.AgentRunQueueStatus{
			State:         "Queued",
			BlockedReason: reason,
		}
	})
}

func queuedForAdmission(run *platformv1alpha1.AgentRun, reason string) bool {
	return run.Status.StartedAt != nil &&
		run.Status.Phase == platformv1alpha1.AgentRunPhasePending &&
		run.Status.Queue != nil &&
		run.Status.Queue.State == "Queued" &&
		run.Status.Queue.BlockedReason == reason &&
		run.Status.Queue.AdmittedAt == nil
}

func (r *AgentRunReconciler) countActiveRunsForRuntimeProfile(ctx context.Context, run *platformv1alpha1.AgentRun, profile *platformv1alpha1.RuntimeProfile) (runtimeProfileAdmissionCounts, error) {
	if profile == nil {
		return runtimeProfileAdmissionCounts{}, nil
	}
	return countActiveRuns(ctx, r.Client, run, profile, client.InNamespace(run.Namespace), client.MatchingFields{runtimeProfileRefIndex: profile.Name})
}

func countActiveRuns(ctx context.Context, reader client.Reader, run *platformv1alpha1.AgentRun, profile *platformv1alpha1.RuntimeProfile, opts ...client.ListOption) (runtimeProfileAdmissionCounts, error) {
	if profile == nil {
		return runtimeProfileAdmissionCounts{}, nil
	}

	runs := &platformv1alpha1.AgentRunList{}
	if err := reader.List(ctx, runs, opts...); err != nil {
		return runtimeProfileAdmissionCounts{}, fmt.Errorf("listing runs for runtime profile admission: %w", err)
	}

	var counts runtimeProfileAdmissionCounts
	for i := range runs.Items {
		candidate := &runs.Items[i]
		if sameAgentRun(candidate, run) || !candidateReferencesRuntimeProfile(candidate, profile) || !runConsumesAdmissionSlot(candidate) {
			continue
		}
		counts.cluster++
		if candidate.Namespace == run.Namespace {
			counts.namespace++
		}
	}
	return counts, nil
}

func candidateReferencesRuntimeProfile(run *platformv1alpha1.AgentRun, profile *platformv1alpha1.RuntimeProfile) bool {
	if run == nil || profile == nil || run.Spec.RuntimeProfileRef == nil {
		return false
	}
	if run.Namespace != profile.Namespace {
		return false
	}
	return strings.TrimSpace(run.Spec.RuntimeProfileRef.Name) == profile.Name
}

func runConsumesAdmissionSlot(run *platformv1alpha1.AgentRun) bool {
	if run == nil || isTerminalPhase(run.Status.Phase) {
		return false
	}
	if run.Status.Sandbox == nil && run.Status.Queue != nil && run.Status.Queue.State == "Resuming" {
		return false
	}
	switch run.Status.Phase {
	case platformv1alpha1.AgentRunPhaseAdmitted,
		platformv1alpha1.AgentRunPhaseProvisioning,
		platformv1alpha1.AgentRunPhaseRunning,
		platformv1alpha1.AgentRunPhaseQuestion,
		platformv1alpha1.AgentRunPhaseBlocked,
		platformv1alpha1.AgentRunPhaseWaitingApproval:
		return true
	default:
		return run.Status.Sandbox != nil || provisioningAttemptStart(run) != nil
	}
}

func admissionWaitStartTime(run *platformv1alpha1.AgentRun) time.Time {
	if run == nil {
		return time.Time{}
	}
	start := run.CreationTimestamp.Time
	if run.Status.StartedAt != nil && !run.Status.StartedAt.IsZero() {
		start = run.Status.StartedAt.Time
	}
	// Resumed runs compete for capacity again, but waiting from a previous
	// attempt must not exhaust the new attempt's admission deadline.
	if wake := run.Status.LastWakeTime; wake != nil && wake.Time.After(start) {
		start = wake.Time
	}
	return start
}

func sameAgentRun(left, right *platformv1alpha1.AgentRun) bool {
	if left == nil || right == nil {
		return false
	}
	if left.Namespace != right.Namespace || left.Name != right.Name {
		return false
	}
	if left.UID == "" || right.UID == "" {
		return true
	}
	return left.UID == right.UID
}
