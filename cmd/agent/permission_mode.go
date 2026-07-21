package main

import (
	"context"
	"fmt"
	"log"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	agentpolicy "github.com/gratefulagents/sdk/pkg/agentsdk/policy"
)

// Transient API failures at pod start are retried before the zero-trust
// read-only fallback applies: the resolved permission mode seeds the pod's
// tool registry and command sandbox, so a single API blip during startup
// (which happens on every pause/wake pod recycle) must not silently degrade
// a workspace-write run to a read-only filesystem. NotFound is retried too:
// a freshly created run can race the creation of its RuntimeProfile.
var (
	startupPermissionAttempts   = 8
	startupPermissionRetryDelay = 2 * time.Second
)

// permissionResolution is the outcome of resolving the pod's base permission
// mode from the run's RuntimeProfile.
type permissionResolution struct {
	// Mode is the pod's base permission mode, before pod-level mode-template
	// clamps (clampResolvedPermissionMode).
	Mode agentpolicy.PermissionMode
	// GitRemoteWrites is the RuntimeProfile's normalized remote-write policy.
	GitRemoteWrites agentpolicy.GitRemoteWrites
	// Run is the AgentRun read during resolution; nil when it was unreadable.
	Run *platformv1alpha1.AgentRun
	// Degraded marks a zero-trust read-only fallback caused by a startup
	// failure or race (unreadable AgentRun, missing RuntimeProfile or ref,
	// exhausted API retries) rather than by explicit configuration. Degraded
	// pods re-check the CRDs every turn and bounce compute through the
	// restart-request path once write access resolves, so a startup blip can
	// no longer pin a whole session to a read-only filesystem.
	Degraded bool
	// Reason explains a read-only fallback in one human-readable sentence.
	// Empty when Mode grants write access.
	Reason string
}

func restrictivePermissionFallback(reason string, degraded bool) permissionResolution {
	return permissionResolution{
		Mode:            agentpolicy.PermissionModeReadOnly,
		GitRemoteWrites: agentpolicy.GitRemoteWritesDisabled,
		Degraded:        degraded,
		Reason:          reason,
	}
}

// resolveStartupPermissionMode resolves the run's permission mode from its
// RuntimeProfile, returning the mode, the AgentRun it read, and whether the
// result is a degraded fallback. Zero trust: defaults to read-only when no
// RuntimeProfile is configured, the profile is missing, or reads keep
// failing after retries.
func resolveStartupPermissionMode(
	ctx context.Context, crdClient client.Client, taskName, namespace string,
) permissionResolution {
	var run *platformv1alpha1.AgentRun
	for attempt := 1; ; attempt++ {
		run = getAgentRun(ctx, crdClient, taskName, namespace)
		if run != nil {
			break
		}
		if attempt >= startupPermissionAttempts || ctx.Err() != nil {
			reason := fmt.Sprintf("could not read AgentRun %s/%s after %d attempts", namespace, taskName, attempt)
			log.Printf("WARN: %s — defaulting to read-only (zero trust)", reason)
			return restrictivePermissionFallback(reason, true)
		}
		log.Printf("WARN: could not read AgentRun %s/%s (attempt %d/%d) — retrying",
			namespace, taskName, attempt, startupPermissionAttempts)
		if !sleepStartupRetry(ctx) {
			reason := fmt.Sprintf("startup cancelled while reading AgentRun %s/%s", namespace, taskName)
			return restrictivePermissionFallback(reason, true)
		}
	}

	res := resolveRunPermissionMode(ctx, crdClient, run, startupPermissionAttempts)
	res.Run = run
	return res
}

// resolveRunPermissionMode resolves run's permission mode from its
// RuntimeProfile. attempts bounds the retries for API errors and NotFound
// races (1 = single shot, used by the per-turn heal check).
func resolveRunPermissionMode(
	ctx context.Context, crdClient client.Client, run *platformv1alpha1.AgentRun, attempts int,
) permissionResolution {
	if run.Spec.RuntimeProfileRef == nil {
		// Not retried: the ref is set at run creation, so a missing ref is
		// deployment configuration, not a race. Still marked degraded so the
		// per-turn re-check heals a run whose spec gains a profile later and
		// the session feed says why the workspace is read-only.
		reason := "no RuntimeProfile is configured for this run"
		log.Printf("No RuntimeProfile configured — defaulting to read-only (zero trust)")
		return restrictivePermissionFallback(reason, true)
	}

	rpKey := types.NamespacedName{Name: run.Spec.RuntimeProfileRef.Name, Namespace: run.Namespace}
	for attempt := 1; ; attempt++ {
		rp := &platformv1alpha1.RuntimeProfile{}
		err := crdClient.Get(ctx, rpKey, rp)
		if err == nil {
			if rp.Spec.Security == nil || rp.Spec.Security.PermissionMode == "" {
				reason := fmt.Sprintf("RuntimeProfile %s does not set security.permissionMode", rpKey)
				log.Printf("%s — defaulting to read-only (zero trust)", reason)
				return restrictivePermissionFallback(reason, false)
			}
			mode := agentpolicy.NormalizePermissionMode(string(rp.Spec.Security.PermissionMode))
			gitRemoteWrites := agentpolicy.NormalizeGitRemoteWrites(agentpolicy.GitRemoteWrites(rp.Spec.Security.GitRemoteWrites))
			res := permissionResolution{Mode: mode, GitRemoteWrites: gitRemoteWrites}
			if mode == agentpolicy.PermissionModeReadOnly {
				res.Reason = fmt.Sprintf("RuntimeProfile %s sets permissionMode read-only", rpKey)
			}
			log.Printf("RuntimeProfile %s resolved: permissionMode=%s gitRemoteWrites=%s", rpKey, mode, gitRemoteWrites)
			return res
		}

		notFound := apierrors.IsNotFound(err)
		if attempt >= attempts || ctx.Err() != nil {
			var reason string
			if notFound {
				reason = fmt.Sprintf("RuntimeProfile %s not found", rpKey)
			} else {
				reason = fmt.Sprintf("failed to read RuntimeProfile %s after %d attempts: %v", rpKey, attempt, err)
			}
			log.Printf("WARN: %s — defaulting to read-only (zero trust)", reason)
			return restrictivePermissionFallback(reason, true)
		}
		log.Printf("WARN: failed to read RuntimeProfile %s (attempt %d/%d): %v — retrying",
			rpKey, attempt, attempts, err)
		if !sleepStartupRetry(ctx) {
			reason := fmt.Sprintf("startup cancelled while reading RuntimeProfile %s", rpKey)
			return restrictivePermissionFallback(reason, true)
		}
	}
}

// clampResolvedPermissionMode applies the pod-level mode-template clamps to a
// resolved permission mode. Autonomous single-mode runs (review) are clamped
// to the most restrictive of the resolved profile mode and the mode
// snapshot's permissionMode: a mode template can restrict, never grant.
// Interactive read-only modes (plan) intentionally skip this pod-level clamp
// — their restriction is enforced per turn from the live mode snapshot, so
// switching to a write mode upgrades without a pod restart. The legacy
// mode-name fallback covers review templates installed before permissionMode
// existed.
func clampResolvedPermissionMode(
	mode agentpolicy.PermissionMode, run *platformv1alpha1.AgentRun,
) agentpolicy.PermissionMode {
	if snapMode := snapshotPermissionMode(run); snapMode != "" && snapshotIsAutonomous(run) {
		clamped := platformv1alpha1.MostRestrictivePermissionMode(platformv1alpha1.PermissionMode(string(mode)), snapMode)
		if normalized := agentpolicy.NormalizePermissionMode(string(clamped)); normalized != mode {
			log.Printf("Mode template clamped permission mode to %s", normalized)
			return normalized
		}
		return mode
	}
	if isReviewerRun(run) && mode != agentpolicy.PermissionModeReadOnly {
		log.Printf("Reviewer mode %q (legacy template without permissionMode): clamped to read-only", reviewerModeName)
		return agentpolicy.PermissionModeReadOnly
	}
	return mode
}

// healedWritePermissionMode re-resolves the run's permission mode once. It
// returns the healed mode and true only when resolution now succeeds without
// degradation and — after pod-level clamps — grants write access: proof that
// the pod's degraded read-only fallback is stale and a re-provisioned pod
// would come back writable.
func healedWritePermissionMode(
	ctx context.Context, crdClient client.Client, run *platformv1alpha1.AgentRun,
) (agentpolicy.PermissionMode, bool) {
	if run == nil {
		return "", false
	}
	res := resolveRunPermissionMode(ctx, crdClient, run, 1)
	if res.Degraded {
		return "", false
	}
	mode := clampResolvedPermissionMode(res.Mode, run)
	if !mode.AllowsWriteTools() {
		return "", false
	}
	return mode, true
}

// sleepStartupRetry waits one retry interval, returning false when the
// context is cancelled first.
func sleepStartupRetry(ctx context.Context) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(startupPermissionRetryDelay):
		return true
	}
}
