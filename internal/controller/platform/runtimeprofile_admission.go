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

	var reasons []string
	if limit := admission.MaxConcurrentRuns; limit > 0 && counts.cluster >= limit {
		reasons = append(reasons, fmt.Sprintf("runtime profile %s reached maxConcurrentRuns=%d (%d active)", profile.Name, limit, counts.cluster))
	}
	if limit := admission.PerNamespaceMaxConcurrentRuns; limit > 0 && counts.namespace >= limit {
		reasons = append(reasons, fmt.Sprintf("runtime profile %s reached perNamespaceMaxConcurrentRuns=%d in namespace %s (%d active)", profile.Name, limit, run.Namespace, counts.namespace))
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
	return retryAgentRunStatusPatch(ctx, r.Client, client.ObjectKeyFromObject(run), func(fresh *platformv1alpha1.AgentRun) {
		if fresh == nil || isTerminalPhase(fresh.Status.Phase) || runConsumesAdmissionSlot(fresh) {
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

func (r *AgentRunReconciler) countActiveRunsForRuntimeProfile(ctx context.Context, run *platformv1alpha1.AgentRun, profile *platformv1alpha1.RuntimeProfile) (runtimeProfileAdmissionCounts, error) {
	if profile == nil {
		return runtimeProfileAdmissionCounts{}, nil
	}

	runs := &platformv1alpha1.AgentRunList{}
	if err := r.List(ctx, runs, client.InNamespace(run.Namespace)); err != nil {
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
	switch run.Status.Phase {
	case platformv1alpha1.AgentRunPhaseAdmitted,
		platformv1alpha1.AgentRunPhaseProvisioning,
		platformv1alpha1.AgentRunPhaseRunning,
		platformv1alpha1.AgentRunPhaseQuestion,
		platformv1alpha1.AgentRunPhaseBlocked,
		platformv1alpha1.AgentRunPhaseWaitingApproval:
		return true
	default:
		return run.Status.Sandbox != nil
	}
}

func admissionWaitStartTime(run *platformv1alpha1.AgentRun) time.Time {
	if run == nil {
		return time.Time{}
	}
	if run.Status.StartedAt != nil && !run.Status.StartedAt.IsZero() {
		return run.Status.StartedAt.Time
	}
	if !run.CreationTimestamp.IsZero() {
		return run.CreationTimestamp.Time
	}
	return time.Time{}
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
