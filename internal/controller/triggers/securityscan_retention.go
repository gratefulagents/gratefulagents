package triggers

import (
	"context"
	"time"

	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/store"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

const (
	// securityRetentionSweepInterval is how often a clean (no more work)
	// retention sweep re-runs per scan.
	securityRetentionSweepInterval = time.Hour

	// securityRetentionResumeDelay is the requeue used while a sweep reports
	// moreWork, so a large backlog drains one bounded batch at a time.
	securityRetentionResumeDelay = 30 * time.Second

	// securityRetentionBatchLimit bounds every purge statement of one sweep
	// batch, keeping each reconcile cheap.
	securityRetentionBatchLimit = 200
)

// securityRetentionPolicyFromPack converts the pack's retention block to the
// store policy.
func securityRetentionPolicyFromPack(r *triggersv1alpha1.SecurityPolicyPackRetention) store.SecurityRetentionPolicy {
	if r == nil {
		return store.SecurityRetentionPolicy{}
	}
	return store.SecurityRetentionPolicy{
		ScanDays:       r.ScanDays,
		FindingDays:    r.FindingDays,
		ReportDays:     r.ReportDays,
		EvidenceDays:   r.EvidenceDays,
		PoCDays:        r.PoCDays,
		AuditEventDays: r.AuditEventDays,
	}
}

// sweepSecurityRetention runs at most ONE bounded retention purge batch for
// the scan's policy pack retention configuration and records the outcome in
// status.retention plus a Kubernetes event when rows were purged. It runs
// only from the normal reconcile path — never from the deletion finalizer —
// so retention work can never delay or wedge scan deletion. Best-effort:
// store errors are recorded and retried, never failing the reconcile.
//
// The returned duration is a requeue hint: securityRetentionResumeDelay
// while the purge reports more work, otherwise the time until the next
// scheduled sweep. Zero means retention is not configured for this scan.
func (r *SecurityScanReconciler) sweepSecurityRetention(ctx context.Context, scan *triggersv1alpha1.SecurityScan) time.Duration {
	if r.Findings == nil {
		return 0
	}
	pack := r.scanPolicyPack(ctx, scan)
	if pack == nil || pack.Spec.Retention == nil {
		return 0
	}
	policy := securityRetentionPolicyFromPack(pack.Spec.Retention)
	if policy.IsZero() {
		return 0
	}

	now := r.now()
	if st := scan.Status.Retention; st != nil && !st.MoreWork && st.LastSweepTime != nil {
		if elapsed := now.Sub(st.LastSweepTime.Time); elapsed >= 0 && elapsed < securityRetentionSweepInterval {
			return securityRetentionSweepInterval - elapsed
		}
	}

	counts, moreWork, purgeErr := r.Findings.PurgeExpiredSecurityData(ctx, scan.Namespace, policy, securityRetentionBatchLimit)
	log := logf.FromContext(ctx)
	if purgeErr != nil {
		log.Error(purgeErr, "security retention sweep failed", "scan", scan.Name, "pack", pack.Name)
	}

	sweepTime := metav1.NewTime(now)
	if err := retrySecurityScanStatusUpdate(ctx, r.Client, client.ObjectKeyFromObject(scan), func(fresh *triggersv1alpha1.SecurityScan) {
		st := fresh.Status.Retention
		if st == nil {
			st = &triggersv1alpha1.SecurityScanRetentionStatus{}
		}
		st.LastSweepTime = &sweepTime
		st.MoreWork = moreWork
		st.LastError = ""
		if purgeErr != nil {
			st.LastError = purgeErr.Error()
		}
		st.ScansPurged += int64(counts.ScansDeleted)
		st.FindingsPurged += int64(counts.FindingsDeleted)
		st.ReportsPurged += int64(counts.ReportsDeleted)
		st.EvidenceRedacted += int64(counts.EvidenceRedacted)
		st.PoCRedacted += int64(counts.PoCsRedacted)
		st.AuditEventsPurged += int64(counts.AuditEventsDeleted)
		fresh.Status.Retention = st
	}); err != nil {
		log.Error(err, "failed to record retention sweep status", "scan", scan.Name)
	}

	if !counts.IsZero() && r.Recorder != nil {
		r.Recorder.Eventf(scan, nil, corev1.EventTypeNormal, "RetentionSweep", "RetentionSweep",
			"retention purge batch: %d scan rows, %d findings, %d reports, %d evidence redactions, %d PoC redactions, %d audit events (moreWork=%t)",
			counts.ScansDeleted, counts.FindingsDeleted, counts.ReportsDeleted,
			counts.EvidenceRedacted, counts.PoCsRedacted, counts.AuditEventsDeleted, moreWork)
	}

	switch {
	case purgeErr != nil:
		return time.Minute
	case moreWork:
		return securityRetentionResumeDelay
	default:
		return securityRetentionSweepInterval
	}
}
