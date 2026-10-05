package platform

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/mode"
	"github.com/gratefulagents/gratefulagents/internal/orchestration"
	"github.com/gratefulagents/gratefulagents/internal/projectstate"
	"github.com/gratefulagents/gratefulagents/internal/store"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	agentsandboxextensionsv1alpha1 "sigs.k8s.io/agent-sandbox/extensions/api/v1alpha1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const (
	cancelRequestedAnnotation = "platform.gratefulagents.dev/cancel-requested"
	// promoteSucceededAnnotation asks the controller to tear the run down like
	// a cancellation but record the terminal phase as Succeeded — the user
	// explicitly promoted the run to success from the dashboard.
	promoteSucceededAnnotation = "platform.gratefulagents.dev/promote-succeeded-requested"
	agentRunCleanupFinalizer   = platformv1alpha1.AgentRunCleanupFinalizer
	teamParentLabel            = "platform.gratefulagents.dev/team-parent"
	teamStepLabel              = "platform.gratefulagents.dev/team-step"
	teamRoleLabel              = "platform.gratefulagents.dev/team-role"
	runModeAnnotation          = "platform.gratefulagents.dev/run-mode"
	podVisibilityGrace         = 2 * time.Minute
	ownerRunLabel              = "platform.gratefulagents.dev/owner-run"
	ownerRunUIDLabel           = "platform.gratefulagents.dev/owner-run-uid"
	runtimeProfileRefIndex     = "spec.runtimeProfileRef.name"
	// drainRequeueAfter paces drain waits. A fixed interval avoids the
	// controller's exponential backoff, which otherwise stretches waits for a
	// terminating pod or claim to minutes.
	drainRequeueAfter = 3 * time.Second
)

var errRunnerPodDrainPending = errors.New("runner pod drain pending")

type AgentRunReconciler struct {
	client.Client
	// APIReader, when set, confirms cache misses with an uncached read before
	// a missing object is treated as gone.
	APIReader    client.Reader
	ModeResolver *mode.Resolver
	StateStore   store.StateStore
	admissionMu  sync.Mutex
}

// +kubebuilder:rbac:groups=platform.gratefulagents.dev,resources=agentruns,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=platform.gratefulagents.dev,resources=agentruns/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=platform.gratefulagents.dev,resources=agentruns/finalizers,verbs=update
// +kubebuilder:rbac:groups=platform.gratefulagents.dev,resources=agentrunteamruntimes,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=platform.gratefulagents.dev,resources=agentrunteamruntimes/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=platform.gratefulagents.dev,resources=modetemplates,verbs=get;list;watch
// +kubebuilder:rbac:groups=triggers.gratefulagents.dev,resources=githubrepositories,verbs=get
// +kubebuilder:rbac:groups=triggers.gratefulagents.dev,resources=githubrepositories/status,verbs=get;update;patch
// +kubebuilder:rbac:groups="",resources=pods,verbs=create;delete;get;list;watch
// +kubebuilder:rbac:groups="",resources=serviceaccounts,verbs=create;get
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=roles;rolebindings,verbs=create;get;list;watch;update;patch
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=clusterroles;clusterrolebindings,verbs=create;get;update;patch;delete;list;watch
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=create;get;list;patch;update;watch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=create;get;list;watch;update
// +kubebuilder:rbac:groups="",resources=events,verbs=create
// +kubebuilder:rbac:groups="",resources=persistentvolumeclaims,verbs=create;get;list;watch;delete
// +kubebuilder:rbac:groups=agents.x-k8s.io,resources=sandboxes,verbs=get;list;watch
// +kubebuilder:rbac:groups=extensions.agents.x-k8s.io,resources=sandboxclaims,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=extensions.agents.x-k8s.io,resources=sandboxtemplates,verbs=get;list;watch;create;update;patch;delete

func (r *AgentRunReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	run := &platformv1alpha1.AgentRun{}
	if err := r.Get(ctx, req.NamespacedName, run); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !run.DeletionTimestamp.IsZero() {
		drained, err := r.releaseRunSandbox(ctx, run)
		if err != nil {
			return ctrl.Result{}, err
		}
		if !drained {
			return ctrl.Result{RequeueAfter: drainRequeueAfter}, nil
		}
		if r.StateStore != nil {
			if err := r.StateStore.DeleteAgentRunData(ctx, run.Name, run.Namespace, projectStateIDForRun(run)); err != nil {
				return ctrl.Result{}, fmt.Errorf("deleting AgentRun database state: %w", err)
			}
		}
		if err := cleanupClusterRoleBindings(ctx, r.Client, run); err != nil {
			return ctrl.Result{}, err
		}
		if err := releaseGitHubProcessedIssue(ctx, r.Client, run); err != nil {
			return ctrl.Result{}, err
		}
		if controllerutil.ContainsFinalizer(run, agentRunCleanupFinalizer) {
			if err := retryAgentRunPatch(ctx, r.Client, client.ObjectKeyFromObject(run), func(fresh *platformv1alpha1.AgentRun) {
				controllerutil.RemoveFinalizer(fresh, agentRunCleanupFinalizer)
			}); err != nil {
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{}, nil
	}

	if !controllerutil.ContainsFinalizer(run, agentRunCleanupFinalizer) {
		if err := retryAgentRunPatch(ctx, r.Client, client.ObjectKeyFromObject(run), func(fresh *platformv1alpha1.AgentRun) {
			controllerutil.AddFinalizer(fresh, agentRunCleanupFinalizer)
		}); err != nil {
			return ctrl.Result{}, err
		}
	}

	if run.Annotations[platformv1alpha1.AuthorizationPendingAnnotation] == "true" {
		return ctrl.Result{}, nil
	}

	if changed, err := r.ensureInitialized(ctx, run); err != nil {
		return ctrl.Result{}, err
	} else if changed {
		return ctrl.Result{Requeue: true}, nil
	}

	if handled, err := r.handleCancelRequest(ctx, run); err != nil {
		if errors.Is(err, errRunnerPodDrainPending) {
			return ctrl.Result{RequeueAfter: drainRequeueAfter}, nil
		}
		return ctrl.Result{}, err
	} else if handled {
		return ctrl.Result{}, nil
	}

	if changed, err := r.syncTeamStatus(ctx, run); err != nil {
		return ctrl.Result{}, err
	} else if changed {
		return ctrl.Result{Requeue: true}, nil
	}

	if handled, err := r.handleWakeRequest(ctx, run); err != nil {
		if errors.Is(err, errRunnerPodDrainPending) {
			return ctrl.Result{RequeueAfter: drainRequeueAfter}, nil
		}
		return ctrl.Result{}, err
	} else if handled {
		return ctrl.Result{Requeue: true}, nil
	}

	if handled, err := r.handleRestartRequest(ctx, run); err != nil {
		if errors.Is(err, errRunnerPodDrainPending) {
			return ctrl.Result{RequeueAfter: drainRequeueAfter}, nil
		}
		return ctrl.Result{}, err
	} else if handled {
		return ctrl.Result{Requeue: true}, nil
	}

	if isTerminalPhase(run.Status.Phase) {
		return r.reconcileTerminalRun(ctx, run)
	}

	if run.Status.Phase == platformv1alpha1.AgentRunPhasePaused {
		return r.reconcilePausedRun(ctx, run)
	}

	// All workflow modes use the unified reconcile path.
	return r.reconcileRun(ctx, run)
}

// reconcilePausedRun drains a paused run's worker and resumes the run once
// its limits allow it again: the timeout has been extended and/or the cost cap
// raised.
func (r *AgentRunReconciler) reconcilePausedRun(ctx context.Context, run *platformv1alpha1.AgentRun) (ctrl.Result, error) {
	if run.Status.Sandbox != nil {
		// Always drain the old worker before either staying paused or resuming.
		// Otherwise a limit extension can provision a replacement while the old
		// pod is still publishing its final encrypted checkpoint.
		drained, err := r.releaseRunSandbox(ctx, run)
		if err != nil {
			return ctrl.Result{}, err
		}
		if !drained {
			return ctrl.Result{RequeueAfter: drainRequeueAfter}, nil
		}
	}
	if err := cleanupClusterRoleBindings(ctx, r.Client, run); err != nil {
		return ctrl.Result{}, err
	}

	if run.Status.Sandbox == nil && !pausedRunResumable(run) && !pausedReasonNeedsUpdate(run, pausedRunBlocker(run)) {
		return ctrl.Result{}, nil
	}
	// Clear the drained sandbox and, when the latest persisted limits permit
	// it, leave Paused in the same status patch. Publishing an intermediate
	// Paused/nil state lets owner controllers consume a run as terminal
	// immediately before this controller resumes it.
	if err := retryAgentRunStatusPatch(ctx, r.Client, client.ObjectKeyFromObject(run), func(fresh *platformv1alpha1.AgentRun) {
		if fresh.Status.Phase != platformv1alpha1.AgentRunPhasePaused {
			return
		}
		fresh.Status.Sandbox = nil
		if pausedRunResumable(fresh) {
			now := metav1.Now()
			fresh.Status.LastWakeTime = &now
			fresh.Status.Phase = platformv1alpha1.AgentRunPhaseProvisioning
			fresh.Status.Queue = &platformv1alpha1.AgentRunQueueStatus{State: "Resuming", AdmittedAt: &now}
			return
		}
		if blocker := pausedRunBlocker(fresh); pausedReasonNeedsUpdate(fresh, blocker) {
			if fresh.Status.Queue == nil {
				fresh.Status.Queue = &platformv1alpha1.AgentRunQueueStatus{State: "Paused"}
			}
			fresh.Status.Queue.BlockedReason = blocker
		}
	}); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{Requeue: true}, nil
}

func (r *AgentRunReconciler) ensureInitialized(ctx context.Context, run *platformv1alpha1.AgentRun) (bool, error) {
	if run.Status.Phase != "" {
		return false, nil
	}

	// Resolve mode template once and pin as immutable snapshot.
	var snapshot *platformv1alpha1.ModeTemplateSpec
	if r.ModeResolver != nil {
		var err error
		snapshot, err = r.ModeResolver.ResolveOrDefault(
			ctx,
			run.Spec.ModeRef,
			run.Spec.WorkflowMode,
			run.Spec.ExecutionMode,
			run.Namespace,
		)
		if err != nil {
			return false, fmt.Errorf("resolving mode template: %w", err)
		}
	}

	runtimeProfile, err := resolveRuntimeProfileForRun(ctx, r.Client, run)
	if err != nil {
		return false, fmt.Errorf("resolving RuntimeProfile: %w", err)
	}

	if needsSpecDefaults(run, snapshot, runtimeProfile) {
		if err := retryAgentRunPatch(ctx, r.Client, client.ObjectKeyFromObject(run), func(fresh *platformv1alpha1.AgentRun) {
			applySpecDefaults(fresh, snapshot, runtimeProfile)
		}); err != nil {
			return false, fmt.Errorf("applying AgentRun spec defaults: %w", err)
		}
	}

	if err := retryAgentRunStatusPatch(ctx, r.Client, client.ObjectKeyFromObject(run), func(fresh *platformv1alpha1.AgentRun) {
		now := metav1.Now()

		// Pin mode snapshot.
		if snapshot != nil && fresh.Status.ModeSnapshot == nil {
			fresh.Status.ModeSnapshot = mode.StatusSnapshot(snapshot)
			fresh.Status.ModeName = snapshot.Name
			fresh.Status.ModeVersion = snapshot.Version
			fresh.Status.ModeRevision = 1
		}

		applyStatusPolicyDefaults(fresh, runtimeProfile)

		if isDelegatedChildRun(fresh) {
			fresh.Status.Phase = platformv1alpha1.AgentRunPhasePending
			fresh.Status.CurrentStep = initialCurrentStepForReconcile(fresh)
			fresh.Status.Queue = &platformv1alpha1.AgentRunQueueStatus{State: "Queued"}
			fresh.Status.StartedAt = &now
			return
		}
		if annotatedRunMode(fresh) == "chat" {
			fresh.Status.Phase = platformv1alpha1.AgentRunPhaseRunning
			fresh.Status.CurrentStep = awaitingUserStep
			fresh.Status.Queue = &platformv1alpha1.AgentRunQueueStatus{State: "Running"}
			fresh.Status.StartedAt = &now
			return
		}
		fresh.Status.Phase = platformv1alpha1.AgentRunPhasePending
		fresh.Status.CurrentStep = initialCurrentStepForReconcile(fresh)
		fresh.Status.Queue = &platformv1alpha1.AgentRunQueueStatus{State: "Queued"}
		fresh.Status.StartedAt = &now
	}); err != nil {
		return false, fmt.Errorf("initializing AgentRun status: %w", err)
	}
	return true, nil
}

// reconcileRun is the unified reconcile path for all workflow modes.
// With persistent pods, the same pod handles plan → execute → chat in-process.
// The controller only needs to: create the sandbox once, monitor health, timeout.
func (r *AgentRunReconciler) reconcileRun(ctx context.Context, run *platformv1alpha1.AgentRun) (ctrl.Result, error) {
	if run.Status.Sandbox != nil {
		return r.monitorAgentSandbox(ctx, run, 3*time.Second)
	}
	// Only create a sandbox for active (non-terminal, non-blocked) phases.
	switch run.Status.Phase {
	case platformv1alpha1.AgentRunPhasePending, platformv1alpha1.AgentRunPhaseAdmitted,
		platformv1alpha1.AgentRunPhaseProvisioning, platformv1alpha1.AgentRunPhaseRunning,
		platformv1alpha1.AgentRunPhaseQuestion, platformv1alpha1.AgentRunPhaseBlocked,
		platformv1alpha1.AgentRunPhaseWaitingApproval:
	default:
		return ctrl.Result{}, nil
	}

	if runPastTimeout(run) {
		return ctrl.Result{}, r.markRunPaused(ctx, run)
	}
	if started := provisioningAttemptStart(run); started != nil && time.Since(started.Time) > podStartupDeadline() {
		return ctrl.Result{}, r.markRunFailed(ctx, run, fmt.Errorf("sandbox not provisioned within %s", podStartupDeadline()))
	}
	runtimeProfile, err := resolveRuntimeProfileForRun(ctx, r.Client, run)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("resolving RuntimeProfile for sandbox provisioning: %w", err)
	}
	if runtimeProfile != nil && runtimeProfile.Spec.Admission != nil {
		// Serialize the capacity check through publishing admission. The live
		// confirmation below prevents informer lag from granting the slot twice.
		r.admissionMu.Lock()
		defer r.admissionMu.Unlock()
	}
	if admissionResult, err := r.enforceRuntimeProfileAdmission(ctx, run, runtimeProfile); err != nil {
		return ctrl.Result{}, err
	} else if admissionResult != nil {
		return *admissionResult, nil
	}
	if err := markProvisioningAttempt(ctx, r.Client, run); err != nil {
		return ctrl.Result{}, err
	}
	sandboxStatus, err := createPlanSandbox(ctx, r.Client, run, runtimeProfile)
	if err == nil {
		return r.patchSandboxQueued(ctx, run, sandboxStatus)
	}
	if isPermanentProvisioningError(err) {
		return ctrl.Result{}, r.markRunFailed(ctx, run, err)
	}
	// Everything else (API timeouts, throttling, optimistic-lock conflicts,
	// stale objects from a previous incarnation awaiting garbage collection,
	// a previous sandbox still draining) is retried until the provisioning
	// deadline instead of permanently failing a healthy run.
	switch {
	case errors.Is(err, errRunSandboxDrainRequired):
		drained, drainErr := r.releaseRunSandbox(ctx, run)
		if drainErr != nil {
			return ctrl.Result{}, drainErr
		}
		if !drained {
			return ctrl.Result{RequeueAfter: drainRequeueAfter}, nil
		}
		return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
	case errors.Is(err, errRunSandboxReplaced), errors.Is(err, errStaleRunResource):
		ctrl.LoggerFrom(ctx).Info("waiting to provision sandbox", "reason", err.Error())
		return ctrl.Result{RequeueAfter: drainRequeueAfter}, nil
	default:
		return ctrl.Result{}, fmt.Errorf("provisioning sandbox: %w", err)
	}
}

const provisioningQueueState = "Provisioning"

// provisioningAttemptStart returns when the current, not yet successful
// sandbox provisioning attempt began, or nil when none is recorded.
func provisioningAttemptStart(run *platformv1alpha1.AgentRun) *metav1.Time {
	if run.Status.Queue == nil || run.Status.Queue.State != provisioningQueueState {
		return nil
	}
	return run.Status.Queue.AdmittedAt
}

// Reserve admission before creating compute, including when a later API write
// fails. The same timestamp bounds provisioning retries for this attempt.
func markProvisioningAttempt(ctx context.Context, c client.Client, run *platformv1alpha1.AgentRun) error {
	return retryAgentRunStatusPatch(ctx, c, client.ObjectKeyFromObject(run), func(fresh *platformv1alpha1.AgentRun) {
		if runStopped(fresh) || fresh.Status.Sandbox != nil || provisioningAttemptStart(fresh) != nil {
			return
		}
		now := metav1.Now()
		fresh.Status.Queue = &platformv1alpha1.AgentRunQueueStatus{State: provisioningQueueState, AdmittedAt: &now}
	})
}

// startupElapsed is how long the run has been waiting for its sandbox worker:
// measured from admission (set when the claim is created), falling back to the
// run start.
func startupElapsed(run *platformv1alpha1.AgentRun) time.Duration {
	start := run.CreationTimestamp
	if run.Status.StartedAt != nil {
		start = *run.Status.StartedAt
	}
	if admitted := queueAdmittedAt(&run.Status); admitted != nil {
		start = *admitted
	}
	if start.IsZero() {
		return 0
	}
	return time.Since(start.Time)
}

func (r *AgentRunReconciler) patchSandboxQueued(ctx context.Context, run *platformv1alpha1.AgentRun, sandboxStatus *platformv1alpha1.AgentRunSandboxStatus) (ctrl.Result, error) {
	if err := retryAgentRunStatusPatch(ctx, r.Client, client.ObjectKeyFromObject(run), func(fresh *platformv1alpha1.AgentRun) {
		if fresh.UID != run.UID || !fresh.Status.LastWakeTime.Equal(run.Status.LastWakeTime) {
			return
		}
		if isTerminalPhase(fresh.Status.Phase) || fresh.Status.Phase == platformv1alpha1.AgentRunPhasePaused {
			// The run stopped while the sandbox was being created. Record the
			// sandbox so the terminal/paused drain releases it, but never move
			// the run back into an active phase.
			if fresh.Status.Sandbox == nil && sandboxStatus != nil {
				fresh.Status.Sandbox = sandboxStatus.DeepCopy()
			}
			return
		}
		if fresh.Status.Sandbox != nil {
			return
		}
		if fresh.Status.Phase != run.Status.Phase && (fresh.Status.Phase == platformv1alpha1.AgentRunPhaseQuestion || fresh.Status.Phase == platformv1alpha1.AgentRunPhaseBlocked || fresh.Status.Phase == platformv1alpha1.AgentRunPhaseWaitingApproval) {
			fresh.Status.Sandbox = sandboxStatus.DeepCopy()
			return
		}
		now := metav1.Now()
		fresh.Status.Phase = platformv1alpha1.AgentRunPhaseAdmitted
		fresh.Status.CurrentStep = initialCurrentStepForReconcile(fresh)
		fresh.Status.Queue = &platformv1alpha1.AgentRunQueueStatus{State: "Queued", AdmittedAt: &now}
		if sandboxStatus != nil {
			fresh.Status.Sandbox = sandboxStatus.DeepCopy()
		}
		if fresh.Status.StartedAt == nil {
			fresh.Status.StartedAt = &now
		}
	}); err != nil {
		return ctrl.Result{}, fmt.Errorf("patching AgentRun sandbox status: %w", err)
	}
	return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
}

func initialCurrentStepForReconcile(run *platformv1alpha1.AgentRun) string {
	if run.Status.ModeSnapshot != nil && run.Status.ModeSnapshot.Autonomous {
		return "auto"
	}
	if isDelegatedChildRun(run) && isAutonomousChildRun(run) {
		return "auto"
	}
	return awaitingUserStep
}

func (r *AgentRunReconciler) monitorPodName(ctx context.Context, run *platformv1alpha1.AgentRun, podName string, requeueAfter time.Duration) (ctrl.Result, error) {
	pod := &corev1.Pod{}
	if err := r.Get(ctx, client.ObjectKey{Namespace: run.Namespace, Name: podName}, pod); err != nil {
		if apierrors.IsNotFound(err) {
			// Sandbox providers can publish the eventual pod name before the pod
			// object is visible. Treat that expected startup window as provisioning,
			// not as a terminal runner failure.
			if isRunAwaitingPod(run) {
				return ctrl.Result{RequeueAfter: requeueAfter}, nil
			}
			return ctrl.Result{}, r.markRunFailed(ctx, run, fmt.Errorf("runner pod %s disappeared", podName))
		}
		return ctrl.Result{}, err
	}

	switch pod.Status.Phase {
	case corev1.PodPending:
		// Classify startup failures instead of letting them look like normal
		// provisioning until the run's multi-hour runtime cap: permanent
		// config errors fail fast (after a short grace for create-order
		// races), everything else — image pull backoff, unschedulable — fails
		// with a diagnosis once the startup deadline passes.
		podAge := time.Since(pod.CreationTimestamp.Time)
		if reason, fatal := fatalPodStartupReason(pod); fatal && podAge > podVisibilityGrace {
			return ctrl.Result{}, r.markRunFailed(ctx, run, fmt.Errorf("runner pod %s cannot start: %s", podName, reason))
		}
		if deadline := podStartupDeadline(); podAge > deadline {
			return ctrl.Result{}, r.markRunFailed(ctx, run, fmt.Errorf("runner pod %s failed to start within %s: %s", podName, deadline, classifyPodFailure(pod)))
		}
		return ctrl.Result{RequeueAfter: requeueAfter}, nil
	case corev1.PodRunning:
		if run.Status.Phase == platformv1alpha1.AgentRunPhaseBlocked || run.Status.Phase == platformv1alpha1.AgentRunPhaseWaitingApproval || run.Status.Phase == platformv1alpha1.AgentRunPhaseQuestion {
			return ctrl.Result{RequeueAfter: requeueAfter}, nil
		}
		if run.Status.Phase != platformv1alpha1.AgentRunPhaseRunning || run.Status.Queue == nil || run.Status.Queue.State != "Running" {
			if err := r.patchRunning(ctx, run); err != nil {
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{RequeueAfter: requeueAfter}, nil
	case corev1.PodSucceeded:
		if !isTerminalPhase(run.Status.Phase) {
			if err := r.patchSucceeded(ctx, run); err != nil {
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{}, nil
	case corev1.PodFailed:
		return ctrl.Result{}, r.markRunFailed(ctx, run, fmt.Errorf("runner pod %s failed: %s", podName, classifyPodFailure(pod)))
	default:
		return ctrl.Result{RequeueAfter: requeueAfter}, nil
	}
}

func isRunAwaitingPod(run *platformv1alpha1.AgentRun) bool {
	if run == nil {
		return false
	}
	startupPhase := false
	switch run.Status.Phase {
	case platformv1alpha1.AgentRunPhasePending, platformv1alpha1.AgentRunPhaseAdmitted,
		platformv1alpha1.AgentRunPhaseProvisioning:
		startupPhase = true
	}
	startupQueue := run.Status.Queue != nil && (run.Status.Queue.State == "Queued" || run.Status.Queue.State == "Provisioning")
	if !startupPhase && !startupQueue {
		return false
	}

	started := run.CreationTimestamp.Time
	if run.Status.StartedAt != nil {
		started = run.Status.StartedAt.Time
	}
	if run.Status.Queue != nil && run.Status.Queue.AdmittedAt != nil {
		started = run.Status.Queue.AdmittedAt.Time
	}
	return !started.IsZero() && time.Since(started) <= podVisibilityGrace
}

// patchRunning promotes a run whose worker pod is running. It only moves
// startup phases (or repairs a stale Running queue state); interactive phases
// the worker set (Question, Blocked, WaitingApproval), Paused, and terminal
// phases are left alone even when the cached run that triggered the call was
// stale.
func (r *AgentRunReconciler) patchRunning(ctx context.Context, run *platformv1alpha1.AgentRun) error {
	return retryAgentRunStatusPatch(ctx, r.Client, client.ObjectKeyFromObject(run), func(fresh *platformv1alpha1.AgentRun) {
		if fresh.UID != run.UID || !fresh.Status.LastWakeTime.Equal(run.Status.LastWakeTime) {
			return
		}
		switch fresh.Status.Phase {
		case platformv1alpha1.AgentRunPhasePending, platformv1alpha1.AgentRunPhaseAdmitted,
			platformv1alpha1.AgentRunPhaseProvisioning, platformv1alpha1.AgentRunPhaseRunning:
		default:
			return
		}
		fresh.Status.Phase = platformv1alpha1.AgentRunPhaseRunning
		fresh.Status.Queue = &platformv1alpha1.AgentRunQueueStatus{State: "Running", AdmittedAt: queueAdmittedAt(&fresh.Status)}
	})
}

// runStopped reports whether the run already reached a phase the controller
// must not overwrite from a pod observation: terminal, or Paused by the worker
// (cost cap) or by the runtime window.
func runStopped(run *platformv1alpha1.AgentRun) bool {
	return isTerminalPhase(run.Status.Phase) || run.Status.Phase == platformv1alpha1.AgentRunPhasePaused
}

func (r *AgentRunReconciler) patchSucceeded(ctx context.Context, run *platformv1alpha1.AgentRun) error {
	return retryAgentRunStatusPatch(ctx, r.Client, client.ObjectKeyFromObject(run), func(fresh *platformv1alpha1.AgentRun) {
		if fresh.UID != run.UID || !fresh.Status.LastWakeTime.Equal(run.Status.LastWakeTime) {
			return
		}
		if runStopped(fresh) {
			return
		}
		now := metav1.Now()
		fresh.Status.Phase = platformv1alpha1.AgentRunPhaseSucceeded
		fresh.Status.CompletedAt = &now
		fresh.Status.Queue = &platformv1alpha1.AgentRunQueueStatus{State: "Succeeded", AdmittedAt: queueAdmittedAt(&fresh.Status)}
	})
}

func (r *AgentRunReconciler) markRunFailed(ctx context.Context, run *platformv1alpha1.AgentRun, cause error) error {
	return retryAgentRunStatusPatch(ctx, r.Client, client.ObjectKeyFromObject(run), func(fresh *platformv1alpha1.AgentRun) {
		if fresh.UID != run.UID || !fresh.Status.LastWakeTime.Equal(run.Status.LastWakeTime) {
			return
		}
		if runStopped(fresh) {
			return
		}
		now := metav1.Now()
		fresh.Status.Phase = platformv1alpha1.AgentRunPhaseFailed
		fresh.Status.LastError = cause.Error()
		fresh.Status.CompletedAt = &now
		fresh.Status.Queue = &platformv1alpha1.AgentRunQueueStatus{State: "Failed", BlockedReason: cause.Error(), AdmittedAt: queueAdmittedAt(&fresh.Status)}
	})
}

func (r *AgentRunReconciler) markRunPaused(ctx context.Context, run *platformv1alpha1.AgentRun) error {
	return retryAgentRunStatusPatch(ctx, r.Client, client.ObjectKeyFromObject(run), func(fresh *platformv1alpha1.AgentRun) {
		if fresh.UID != run.UID || !fresh.Status.LastWakeTime.Equal(run.Status.LastWakeTime) {
			return
		}
		if runStopped(fresh) || !runPastTimeout(fresh) {
			return
		}
		fresh.Status.Phase = platformv1alpha1.AgentRunPhasePaused
		fresh.Status.Queue = &platformv1alpha1.AgentRunQueueStatus{
			State:         "Paused",
			BlockedReason: timeoutPauseReason(effectiveTimeout(fresh)),
			AdmittedAt:    queueAdmittedAt(&fresh.Status),
		}
	})
}

// reconcileTerminalRun drains a completed run's sandbox compute after a TTL.
// Runs that end through pod completion (patchSucceeded/markRunFailed) keep
// their SandboxClaim, managed template, and pod object; without this sweep
// they accumulate as zombie workers forever. The TTL keeps pod logs around
// briefly for post-mortems; a cleared status.Sandbox marks the drain done so
// terminal runs stop paying the discovery lists on every reconcile.
//
// Per-run ClusterRoleBindings (including any cluster-admin grant) are released
// immediately rather than after the TTL: runs usually end through a
// worker-written terminal status, which never passes through
// patchSucceeded/markRunFailed, and a finished run must not keep cluster
// access while its pod lingers for log retention.
func (r *AgentRunReconciler) reconcileTerminalRun(ctx context.Context, run *platformv1alpha1.AgentRun) (ctrl.Result, error) {
	if err := cleanupClusterRoleBindings(ctx, r.Client, run); err != nil {
		return ctrl.Result{}, err
	}
	if run.Status.Sandbox == nil {
		return ctrl.Result{}, nil
	}
	if run.Status.CompletedAt != nil {
		if remaining := terminalSandboxTTL() - time.Since(run.Status.CompletedAt.Time); remaining > 0 {
			return ctrl.Result{RequeueAfter: remaining}, nil
		}
	}
	drained, err := r.releaseRunSandbox(ctx, run)
	if err != nil {
		return ctrl.Result{}, err
	}
	if !drained {
		return ctrl.Result{RequeueAfter: drainRequeueAfter}, nil
	}
	if err := clearRunSandboxStatus(ctx, r.Client, run); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

func retryAgentRunStatusPatch(ctx context.Context, c client.Client, key client.ObjectKey, mutate func(*platformv1alpha1.AgentRun)) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		fresh := &platformv1alpha1.AgentRun{}
		if err := c.Get(ctx, key, fresh); err != nil {
			return err
		}
		before := fresh.DeepCopy()
		patch := client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{})
		mutate(fresh)
		if equality.Semantic.DeepEqual(before.Status, fresh.Status) {
			return nil
		}
		return c.Status().Patch(ctx, fresh, patch)
	})
}

func retryAgentRunPatch(ctx context.Context, c client.Client, key client.ObjectKey, mutate func(*platformv1alpha1.AgentRun)) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		fresh := &platformv1alpha1.AgentRun{}
		if err := c.Get(ctx, key, fresh); err != nil {
			return err
		}
		patch := client.MergeFromWithOptions(fresh.DeepCopy(), client.MergeFromWithOptimisticLock{})
		mutate(fresh)
		return c.Patch(ctx, fresh, patch)
	})
}

// releaseGitHubProcessedIssue makes an explicitly deleted issue-triggered run
// eligible for the GitHub poller to create again. IssuesProcessed remains a
// cumulative counter; only the durable deduplication entry is released.
func releaseGitHubProcessedIssue(ctx context.Context, c client.Client, run *platformv1alpha1.AgentRun) error {
	if run == nil || run.Spec.Trigger.Kind != "GitHubRepository" || run.Spec.Trigger.ExternalRef == nil {
		return nil
	}
	triggerName := strings.TrimSpace(run.Annotations["triggers.gratefulagents.dev/runtime-trigger-name"])
	if triggerName == "" {
		triggerName = strings.TrimSpace(run.Spec.Trigger.Name)
	}
	issueID := strings.TrimSpace(run.Spec.Trigger.ExternalRef.ID)
	if triggerName == "" || issueID == "" {
		return nil
	}

	key := client.ObjectKey{Namespace: run.Namespace, Name: triggerName}
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		fresh := &triggersv1alpha1.GitHubRepository{}
		if err := c.Get(ctx, key, fresh); err != nil {
			return err
		}
		if !fresh.DeletionTimestamp.IsZero() {
			return nil
		}

		processed := make([]string, 0, len(fresh.Status.ProcessedIssueIDs))
		removed := false
		for _, id := range fresh.Status.ProcessedIssueIDs {
			if strings.TrimSpace(id) == issueID {
				removed = true
				continue
			}
			processed = append(processed, id)
		}
		if !removed {
			return nil
		}

		fresh.Status.ProcessedIssueIDs = processed
		return c.Status().Update(ctx, fresh)
	})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("releasing GitHub issue %s from trigger %s/%s: %w", issueID, run.Namespace, triggerName, err)
	}
	return nil
}

func (r *AgentRunReconciler) handleCancelRequest(ctx context.Context, run *platformv1alpha1.AgentRun) (bool, error) {
	if handled, err := r.handleTerminationRequest(ctx, run, cancelRequestedAnnotation, platformv1alpha1.AgentRunPhaseCancelled,
		&platformv1alpha1.AgentRunQueueStatus{State: "Cancelled", BlockedReason: "cancelled by user"}); handled || err != nil {
		return handled, err
	}
	// User-requested promotion to success: same teardown as cancellation, but
	// the run is recorded as Succeeded.
	return r.handleTerminationRequest(ctx, run, promoteSucceededAnnotation, platformv1alpha1.AgentRunPhaseSucceeded,
		&platformv1alpha1.AgentRunQueueStatus{State: "Succeeded", BlockedReason: "promoted to succeeded by user"})
}

// handleTerminationRequest honors a user-requested terminal transition
// recorded as an annotation: it tears down the runner pod, sandbox claim,
// managed template, and cluster role bindings, then patches the run into the
// requested terminal phase and clears the annotation.
func (r *AgentRunReconciler) handleTerminationRequest(ctx context.Context, run *platformv1alpha1.AgentRun, annotation string, phase platformv1alpha1.AgentRunPhase, queue *platformv1alpha1.AgentRunQueueStatus) (bool, error) {
	if run == nil || strings.TrimSpace(run.Annotations[annotation]) == "" {
		return false, nil
	}
	key := client.ObjectKeyFromObject(run)
	if isTerminalPhase(run.Status.Phase) {
		if err := retryAgentRunPatch(ctx, r.Client, key, func(fresh *platformv1alpha1.AgentRun) {
			delete(fresh.Annotations, annotation)
		}); err != nil {
			return false, fmt.Errorf("clearing AgentRun %s annotation: %w", annotation, err)
		}
		return true, nil
	}

	drained, err := r.releaseRunSandbox(ctx, run)
	if err != nil {
		return false, err
	}
	if !drained {
		return false, errRunnerPodDrainPending
	}
	if err := cleanupClusterRoleBindings(ctx, r.Client, run); err != nil {
		return false, err
	}

	if err := retryAgentRunStatusPatch(ctx, r.Client, key, func(fresh *platformv1alpha1.AgentRun) {
		if fresh.UID != run.UID {
			return
		}
		fresh.Status.Sandbox = nil
		if isTerminalPhase(fresh.Status.Phase) {
			// The worker finished on its own while compute drained; its
			// terminal status is the authoritative outcome.
			return
		}
		now := metav1.Now()
		fresh.Status.Phase = phase
		fresh.Status.CompletedAt = &now
		fresh.Status.Queue = queue.DeepCopy()
		fresh.Status.LastError = ""
		// A stop supersedes every wake requested before it. Only a wake counter
		// incremented after this terminal transition may resume the run.
		if phase == platformv1alpha1.AgentRunPhaseCancelled {
			fresh.Status.WakeRequestsHandled = fresh.Spec.WakeRequests
		}
	}); err != nil {
		return false, fmt.Errorf("patching AgentRun termination status: %w", err)
	}
	if err := retryAgentRunPatch(ctx, r.Client, key, func(fresh *platformv1alpha1.AgentRun) {
		delete(fresh.Annotations, annotation)
	}); err != nil {
		return false, fmt.Errorf("clearing AgentRun %s annotation: %w", annotation, err)
	}
	return true, nil
}

func resolveRuntimeProfileForRun(ctx context.Context, c client.Client, run *platformv1alpha1.AgentRun) (*platformv1alpha1.RuntimeProfile, error) {
	if run == nil || run.Spec.RuntimeProfileRef == nil || strings.TrimSpace(run.Spec.RuntimeProfileRef.Name) == "" {
		return nil, nil
	}
	profile := &platformv1alpha1.RuntimeProfile{}
	key := client.ObjectKey{Namespace: run.Namespace, Name: run.Spec.RuntimeProfileRef.Name}
	if err := c.Get(ctx, key, profile); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return profile, nil
}

func isStandingOverseerRun(run *platformv1alpha1.AgentRun) bool {
	_, supervisedRunName, _ := supervisedIdentityForRun(run)
	return supervisedRunName != ""
}

func effectiveSkillRefs(run *platformv1alpha1.AgentRun, snapshot *platformv1alpha1.ModeTemplateSpec) []platformv1alpha1.NamedRef {
	if run == nil || isStandingOverseerRun(run) {
		return nil
	}
	refs := make([]platformv1alpha1.NamedRef, 0, len(run.Spec.SkillRefs))
	seen := make(map[string]struct{}, cap(refs))
	appendUnique := func(candidates []platformv1alpha1.NamedRef) {
		for _, ref := range candidates {
			name := strings.TrimSpace(ref.Name)
			if name == "" {
				continue
			}
			if _, exists := seen[name]; exists {
				continue
			}
			seen[name] = struct{}{}
			refs = append(refs, platformv1alpha1.NamedRef{Name: name})
		}
	}
	appendUnique(run.Spec.SkillRefs)
	if snapshot != nil {
		appendUnique(snapshot.DefaultSkillRefs)
	}
	// Installation only adds to the user catalog; activation is explicit per run
	// (including project/trigger selections) or through mode defaults.
	return refs
}

// effectiveMCPServerRefs merges the run's explicit MCP servers with the mode's
// defaults: explicit refs first, then mode defaults, trimmed and deduplicated.
// A project server must never displace a mode default, so the two lists are
// merged rather than the defaults applying only to runs with no servers.
func effectiveMCPServerRefs(run *platformv1alpha1.AgentRun, snapshot *platformv1alpha1.ModeTemplateSpec) []platformv1alpha1.NamedRef {
	if run == nil {
		return nil
	}
	refs := make([]platformv1alpha1.NamedRef, 0, len(run.Spec.MCPServerRefs))
	seen := make(map[string]struct{}, cap(refs))
	appendUnique := func(candidates []platformv1alpha1.NamedRef) {
		for _, ref := range candidates {
			name := strings.TrimSpace(ref.Name)
			if name == "" {
				continue
			}
			if _, exists := seen[name]; exists {
				continue
			}
			seen[name] = struct{}{}
			refs = append(refs, platformv1alpha1.NamedRef{Name: name})
		}
	}
	appendUnique(run.Spec.MCPServerRefs)
	if snapshot != nil {
		appendUnique(snapshot.DefaultMCPServerRefs)
	}
	return refs
}

func namedRefsEqual(a, b []platformv1alpha1.NamedRef) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Name != b[i].Name {
			return false
		}
	}
	return true
}

func needsSpecDefaults(run *platformv1alpha1.AgentRun, snapshot *platformv1alpha1.ModeTemplateSpec, runtimeProfile *platformv1alpha1.RuntimeProfile) bool {
	if run == nil {
		return false
	}
	if !namedRefsEqual(run.Spec.MCPServerRefs, effectiveMCPServerRefs(run, snapshot)) {
		return true
	}
	if !namedRefsEqual(run.Spec.SkillRefs, effectiveSkillRefs(run, snapshot)) {
		return true
	}
	if runtimeProfile != nil && runtimeProfile.Spec.Security != nil &&
		runtimeProfile.Spec.Security.DefaultTimeout.Duration > 0 &&
		(run.Spec.Limits == nil || run.Spec.Limits.MaxRuntime.Duration == 0) {
		return true
	}
	return false
}

func applySpecDefaults(run *platformv1alpha1.AgentRun, snapshot *platformv1alpha1.ModeTemplateSpec, runtimeProfile *platformv1alpha1.RuntimeProfile) {
	if run == nil {
		return
	}
	run.Spec.MCPServerRefs = effectiveMCPServerRefs(run, snapshot)
	run.Spec.SkillRefs = effectiveSkillRefs(run, snapshot)
	if runtimeProfile != nil && runtimeProfile.Spec.Security != nil && runtimeProfile.Spec.Security.DefaultTimeout.Duration > 0 {
		if run.Spec.Limits == nil {
			run.Spec.Limits = &platformv1alpha1.AgentRunLimits{}
		}
		if run.Spec.Limits.MaxRuntime.Duration == 0 {
			run.Spec.Limits.MaxRuntime = runtimeProfile.Spec.Security.DefaultTimeout
		}
	}
}

func resolvedGitRemoteWrites(runtimeProfile *platformv1alpha1.RuntimeProfile) string {
	if runtimeProfile == nil {
		return ""
	}
	if runtimeProfile.Spec.Security == nil || runtimeProfile.Spec.Security.PermissionMode == "" {
		return string(platformv1alpha1.GitRemoteWritesDisabled)
	}
	return string(platformv1alpha1.NormalizeGitRemoteWrites(runtimeProfile.Spec.Security.GitRemoteWrites))
}

func applyStatusPolicyDefaults(run *platformv1alpha1.AgentRun, runtimeProfile *platformv1alpha1.RuntimeProfile) {
	if run == nil {
		return
	}
	// Effective permission mode = most restrictive of the RuntimeProfile and
	// the mode template snapshot; a mode can restrict but never grant.
	var profileMode platformv1alpha1.PermissionMode
	if runtimeProfile != nil && runtimeProfile.Spec.Security != nil {
		profileMode = runtimeProfile.Spec.Security.PermissionMode
	}
	var modeMode platformv1alpha1.PermissionMode
	if run.Status.ModeSnapshot != nil {
		modeMode = run.Status.ModeSnapshot.PermissionMode
	}
	if resolved := platformv1alpha1.MostRestrictivePermissionMode(profileMode, modeMode); resolved != "" {
		if run.Status.Policy == nil {
			run.Status.Policy = &platformv1alpha1.AgentRunResolvedPolicy{}
		}
		run.Status.Policy.ResolvedPermissionMode = string(resolved)
	}
	if resolved := resolvedGitRemoteWrites(runtimeProfile); resolved != "" {
		if run.Status.Policy == nil {
			run.Status.Policy = &platformv1alpha1.AgentRunResolvedPolicy{}
		}
		run.Status.Policy.ResolvedGitRemoteWrites = resolved
	}
}

func isTerminalPhase(phase platformv1alpha1.AgentRunPhase) bool {
	switch phase {
	case platformv1alpha1.AgentRunPhaseSucceeded, platformv1alpha1.AgentRunPhaseFailed, platformv1alpha1.AgentRunPhaseCancelled:
		return true
	default:
		return false
	}
}

// costCapBlocker returns why the run's recorded spend blocks it from running,
// or "" when it is within spec.limits.maxCostUsd. An invalid cap blocks, like
// the worker, which refuses to run without a valid ceiling.
func costCapBlocker(run *platformv1alpha1.AgentRun) string {
	if run == nil {
		return ""
	}
	capUSD, set, err := run.Spec.Limits.CostCapUSD()
	if !set {
		return ""
	}
	if err != nil {
		return fmt.Sprintf("invalid spec.limits.maxCostUsd: %v — fix it to resume", err)
	}
	if run.Status.Metrics == nil {
		return ""
	}
	spent, err := strconv.ParseFloat(strings.TrimSpace(run.Status.Metrics.CostUsd), 64)
	if err != nil || spent < capUSD {
		return ""
	}
	return fmt.Sprintf("Cost cap reached: $%.4f spent of the $%.2f limit — increase spec.limits.maxCostUsd to resume.", spent, capUSD)
}

const timeoutPauseReasonPrefix = "paused after "

func timeoutPauseReason(timeout time.Duration) string {
	return fmt.Sprintf("%s%s timeout — extend maxRuntime to resume", timeoutPauseReasonPrefix, timeout)
}

// pausedForTimeout reports whether the controller paused the run because its
// runtime window elapsed (as opposed to the worker pausing at the cost cap).
func pausedForTimeout(run *platformv1alpha1.AgentRun) bool {
	return run != nil && run.Status.Queue != nil && strings.HasPrefix(run.Status.Queue.BlockedReason, timeoutPauseReasonPrefix)
}

// pausedRunBlocker returns the limit still keeping a paused run from
// resuming, or "" when none remains. The runtime window only blocks runs that
// were paused for it; time spent paused on the cost cap does not count as
// runtime.
func pausedRunBlocker(run *platformv1alpha1.AgentRun) string {
	if pausedForTimeout(run) && runPastTimeout(run) {
		// The pause reason records the exhausted limit. Compare an extension
		// against that limit, not wall time spent waiting for the user to edit it.
		limit, _, _ := strings.Cut(strings.TrimPrefix(run.Status.Queue.BlockedReason, timeoutPauseReasonPrefix), " timeout")
		pausedLimit, err := time.ParseDuration(limit)
		if err != nil || effectiveTimeout(run) <= pausedLimit {
			return run.Status.Queue.BlockedReason
		}
	}
	return costCapBlocker(run)
}

func pausedRunResumable(run *platformv1alpha1.AgentRun) bool {
	return run != nil && run.Status.StartedAt != nil && pausedRunBlocker(run) == ""
}

// pausedReasonNeedsUpdate reports whether the recorded blocked reason no
// longer describes the remaining blocker. A worker-written cost-cap message is
// kept while the cost cap is still what blocks the run.
func pausedReasonNeedsUpdate(run *platformv1alpha1.AgentRun, blocker string) bool {
	if run == nil || blocker == "" {
		return false
	}
	current := ""
	if run.Status.Queue != nil {
		current = run.Status.Queue.BlockedReason
	}
	if current == blocker {
		return false
	}
	return !isCostCapReason(blocker) || !isCostCapReason(current) || strings.HasPrefix(blocker, "invalid ")
}

func isCostCapReason(reason string) bool {
	lower := strings.ToLower(reason)
	return strings.Contains(lower, "cost cap") || strings.Contains(lower, "maxcostusd")
}

func (r *AgentRunReconciler) handleWakeRequest(ctx context.Context, run *platformv1alpha1.AgentRun) (bool, error) {
	if run == nil || run.Spec.WakeRequests <= run.Status.WakeRequestsHandled {
		return false, nil
	}
	phase := run.Status.Phase
	switch phase {
	case platformv1alpha1.AgentRunPhaseSucceeded, platformv1alpha1.AgentRunPhaseFailed, platformv1alpha1.AgentRunPhasePaused, platformv1alpha1.AgentRunPhaseCancelled:
	default:
		return false, nil
	}
	if phase == platformv1alpha1.AgentRunPhaseFailed &&
		run.Spec.Limits != nil &&
		run.Spec.Limits.MaxRetries > 0 &&
		run.Status.RetryCount >= run.Spec.Limits.MaxRetries {
		maxRetries := run.Spec.Limits.MaxRetries
		if err := retryAgentRunStatusPatch(ctx, r.Client, client.ObjectKeyFromObject(run), func(fresh *platformv1alpha1.AgentRun) {
			if fresh.Status.Phase != platformv1alpha1.AgentRunPhaseFailed || fresh.Spec.WakeRequests <= fresh.Status.WakeRequestsHandled {
				return
			}
			fresh.Status.LastError = fmt.Sprintf("wake refused: maxRetries (%d) exhausted", maxRetries)
			fresh.Status.WakeRequestsHandled = fresh.Spec.WakeRequests
		}); err != nil {
			return false, fmt.Errorf("patching AgentRun wake refusal status: %w", err)
		}
		return true, nil
	}

	drained, err := r.releaseRunSandbox(ctx, run)
	if err != nil {
		return false, err
	}
	if !drained {
		return false, errRunnerPodDrainPending
	}

	woke := false
	if err := retryAgentRunStatusPatch(ctx, r.Client, client.ObjectKeyFromObject(run), func(fresh *platformv1alpha1.AgentRun) {
		if fresh.Spec.WakeRequests <= fresh.Status.WakeRequestsHandled {
			return
		}
		switch fresh.Status.Phase {
		case platformv1alpha1.AgentRunPhaseSucceeded, platformv1alpha1.AgentRunPhaseFailed, platformv1alpha1.AgentRunPhasePaused, platformv1alpha1.AgentRunPhaseCancelled:
		default:
			return
		}
		if fresh.Status.Phase == platformv1alpha1.AgentRunPhaseFailed {
			fresh.Status.RetryCount++
		}
		resetForNewAttempt(fresh, "Waking", "wake-request")
		fresh.Status.WakeRequestsHandled = fresh.Spec.WakeRequests
		woke = true
	}); err != nil {
		return false, fmt.Errorf("patching AgentRun wake status: %w", err)
	}
	return woke, nil
}

// runPastTimeout reports whether the run's active window exceeded its timeout.
// The window restarts on wake so a resumed run gets a fresh maxRuntime budget
// instead of instantly re-pausing off the original start time.
//
// Standing runs (repository maintainers, overseers) are exempt: they are
// designed to live indefinitely blocked on an event wait, so a wall-clock
// runtime cap only recycles the pod every few hours, drops the workspace, and
// parks the loop until something nudges it. Their spend is bounded by the cost
// cap and per-episode turn budget instead.
func runPastTimeout(run *platformv1alpha1.AgentRun) bool {
	if run == nil || run.Status.StartedAt == nil {
		return false
	}
	if isStandingRun(run) {
		return false
	}
	start := run.Status.StartedAt.Time
	if lastWake := run.Status.LastWakeTime; lastWake != nil && lastWake.Time.After(start) {
		start = lastWake.Time
	}
	return time.Since(start) > effectiveTimeout(run)
}

// isStandingRun reports whether the run is a controller-owned standing run
// (maintainer or overseer) created through orchestration.EnsureStandingRun.
func isStandingRun(run *platformv1alpha1.AgentRun) bool {
	return run != nil && strings.TrimSpace(run.Labels[orchestration.StandingRunRoleLabel]) != ""
}

// handleRestartRequest bounces a non-terminal run's compute so spec changes
// that need a fresh pod (e.g. switched provider credentials) take effect.
// Session state lives in the store, so the re-provisioned pod resumes the
// run. Terminal and Paused runs consume the counter without action — wake
// requests own resumes of completed runs, and reconcilePausedRun resumes a
// paused run only once its limits allow it.
func (r *AgentRunReconciler) handleRestartRequest(ctx context.Context, run *platformv1alpha1.AgentRun) (bool, error) {
	if run == nil {
		return false, nil
	}
	if run.Spec.RestartRequests <= run.Status.RestartRequestsHandled {
		return false, nil
	}
	restartRequests := run.Spec.RestartRequests
	if runStopped(run) {
		if err := retryAgentRunStatusPatch(ctx, r.Client, client.ObjectKeyFromObject(run), func(fresh *platformv1alpha1.AgentRun) {
			fresh.Status.RestartRequestsHandled = restartRequests
		}); err != nil {
			return false, fmt.Errorf("patching AgentRun restart refusal status: %w", err)
		}
		return true, nil
	}

	drained, err := r.releaseRunSandbox(ctx, run)
	if err != nil {
		return false, err
	}
	if !drained {
		return false, errRunnerPodDrainPending
	}

	if err := retryAgentRunStatusPatch(ctx, r.Client, client.ObjectKeyFromObject(run), func(fresh *platformv1alpha1.AgentRun) {
		if runStopped(fresh) {
			// Stopped runs consume the counter without action.
			fresh.Status.RestartRequestsHandled = restartRequests
			return
		}
		resetForNewAttempt(fresh, "Restarting", "restart-request")
		fresh.Status.RestartRequestsHandled = restartRequests
	}); err != nil {
		return false, fmt.Errorf("patching AgentRun restart status: %w", err)
	}
	return true, nil
}

// releaseRunSandbox discovers compute by immutable owner UID rather than
// trusting status alone: provisioning can create a claim/pod before publishing
// status. It drains every owned Pod, then waits for every claim to disappear,
// before allowing callers to delete durable session state.
func (r *AgentRunReconciler) releaseRunSandbox(ctx context.Context, run *platformv1alpha1.AgentRun) (bool, error) {
	if run == nil {
		return true, nil
	}

	// Discover claims by immutable controller ownership. Derived names and
	// status references can point at another run after legacy name collisions.
	claimList := &agentsandboxextensionsv1alpha1.SandboxClaimList{}
	if err := r.List(ctx, claimList, client.InNamespace(run.Namespace), client.MatchingLabels{
		ownerRunLabel: runNameLabelValue(run.Name),
	}); err != nil {
		return false, fmt.Errorf("listing sandbox claims during drain: %w", err)
	}
	ownedClaims := make(map[string]*agentsandboxextensionsv1alpha1.SandboxClaim)
	for i := range claimList.Items {
		claim := &claimList.Items[i]
		if sandboxClaimOwnedByRun(claim, run) {
			ownedClaims[claim.Name] = claim.DeepCopy()
		}
	}

	ownedPods := &corev1.PodList{}
	if err := r.List(ctx, ownedPods, client.InNamespace(run.Namespace), client.MatchingLabels{
		ownerRunUIDLabel: string(run.UID),
	}); err != nil {
		return false, fmt.Errorf("listing owned runner pods during drain: %w", err)
	}
	if len(ownedPods.Items) > 0 {
		now := time.Now()
		for i := range ownedPods.Items {
			pod := &ownedPods.Items[i]
			if pod.DeletionTimestamp.IsZero() {
				preconditions := client.Preconditions{UID: &pod.UID, ResourceVersion: &pod.ResourceVersion}
				if err := r.Delete(ctx, pod, preconditions); err != nil && !apierrors.IsNotFound(err) {
					return false, fmt.Errorf("deleting runner pod %s/%s: %w", run.Namespace, pod.Name, err)
				}
				continue
			}
			// Bounded escalation: a pod stuck Terminating past its own
			// deletion deadline (lost node, wedged kubelet) would otherwise
			// hang cancellation, wake, restart, and AgentRun deletion
			// forever. Force-delete removes it from the API; note the
			// container may keep running on a partitioned node until its
			// kubelet reconnects.
			if podDrainEscalationDue(pod, now) {
				preconditions := client.Preconditions{UID: &pod.UID}
				if err := r.Delete(ctx, pod, preconditions, client.GracePeriodSeconds(0)); err != nil && !apierrors.IsNotFound(err) {
					return false, fmt.Errorf("force-deleting stuck runner pod %s/%s: %w", run.Namespace, pod.Name, err)
				}
			}
		}
		return false, nil
	}

	if len(ownedClaims) > 0 {
		now := time.Now()
		remaining := false
		for name, claim := range ownedClaims {
			// Already terminating: don't re-issue the delete (it is pointless
			// against the API server). Bounded escalation: a claim wedged
			// Terminating behind a third-party finalizer belongs to its own
			// controller — stop blocking the run's teardown on it after the
			// give-up window instead of hanging forever.
			if claim.DeletionTimestamp != nil && !claim.DeletionTimestamp.IsZero() {
				deletedAt := claim.DeletionTimestamp.Time
				if claimDrainAbandoned(&deletedAt, now) {
					ctrl.LoggerFrom(ctx).Info("abandoning drain wait for sandbox claim stuck terminating",
						"namespace", run.Namespace, "claim", name, "run", run.Name)
					continue
				}
				remaining = true
				continue
			}
			preconditions := client.Preconditions{UID: &claim.UID, ResourceVersion: &claim.ResourceVersion}
			if err := r.Delete(ctx, claim, preconditions); err != nil && !apierrors.IsNotFound(err) {
				return false, fmt.Errorf("deleting sandbox claim %s/%s: %w", run.Namespace, name, err)
			}
			probe := &agentsandboxextensionsv1alpha1.SandboxClaim{}
			if err := r.Get(ctx, client.ObjectKey{Name: name, Namespace: run.Namespace}, probe); err == nil {
				remaining = true
			} else if !apierrors.IsNotFound(err) {
				return false, fmt.Errorf("confirming sandbox claim %s/%s deletion: %w", run.Namespace, name, err)
			}
		}
		if remaining {
			return false, nil
		}
	}
	if err := deleteManagedSandboxTemplateIfOwned(ctx, r.Client, run, managedSandboxTemplateName(run)); err != nil {
		return false, err
	}
	return true, nil
}

func queueAdmittedAt(s *platformv1alpha1.AgentRunStatus) *metav1.Time {
	if s != nil && s.Queue != nil {
		return s.Queue.AdmittedAt
	}
	return nil
}

// resetForNewAttempt moves a run back to Pending for a fresh provisioning
// attempt (wake or restart), dropping the previous attempt's compute and
// completion state and restarting the runtime window.
func resetForNewAttempt(run *platformv1alpha1.AgentRun, queueState, reason string) {
	now := metav1.Now()
	run.Status.Phase = platformv1alpha1.AgentRunPhasePending
	run.Status.Queue = &platformv1alpha1.AgentRunQueueStatus{State: queueState, AdmittedAt: queueAdmittedAt(&run.Status)}
	run.Status.Sandbox = nil
	run.Status.CompletedAt = nil
	run.Status.CompletionRequested = false
	run.Status.LastError = ""
	run.Status.CurrentStep = initialCurrentStepForReconcile(run)
	run.Status.LastWakeTime = &now
	run.Status.LastWakeReason = reason
}

func (r *AgentRunReconciler) syncTeamStatus(ctx context.Context, run *platformv1alpha1.AgentRun) (bool, error) {
	if run == nil {
		return false, nil
	}
	if isTeamParentRun(run) {
		return r.syncTeamParentStatus(ctx, run)
	}
	parentName := strings.TrimSpace(run.Labels[teamParentLabel])
	if parentName == "" {
		return false, nil
	}
	parent := &platformv1alpha1.AgentRun{}
	if err := r.Get(ctx, client.ObjectKey{Namespace: run.Namespace, Name: parentName}, parent); err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	if !isTeamParentRun(parent) {
		return false, nil
	}
	return r.syncTeamParentStatus(ctx, parent)
}

func (r *AgentRunReconciler) syncTeamParentStatus(ctx context.Context, parent *platformv1alpha1.AgentRun) (bool, error) {
	changed := false
	if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		fresh := &platformv1alpha1.AgentRun{}
		if err := r.Get(ctx, client.ObjectKeyFromObject(parent), fresh); err != nil {
			return err
		}
		children, err := r.listOwnedTeamChildren(ctx, fresh)
		if err != nil {
			return err
		}
		nextSummary := buildTeamSummary(fresh, children)
		nextChildren := make([]platformv1alpha1.AgentRunChildStatus, 0, len(children))
		for _, child := range children {
			nextChildren = append(nextChildren, summarizeTeamChild(child))
		}
		if teamSummaryEqual(fresh.Status.TeamSummary, nextSummary) && teamChildrenEqual(fresh.Status.Children, nextChildren) {
			return nil
		}
		patch := client.MergeFromWithOptions(fresh.DeepCopy(), client.MergeFromWithOptimisticLock{})
		fresh.Status.TeamSummary = nextSummary
		fresh.Status.Children = nextChildren
		if err := r.Status().Patch(ctx, fresh, patch); err != nil {
			return err
		}
		changed = true
		return nil
	}); err != nil {
		return false, fmt.Errorf("patching team parent status: %w", err)
	}
	return changed, nil
}

func (r *AgentRunReconciler) listOwnedTeamChildren(ctx context.Context, parent *platformv1alpha1.AgentRun) ([]platformv1alpha1.AgentRun, error) {
	children := &platformv1alpha1.AgentRunList{}
	if err := r.List(ctx, children, client.InNamespace(parent.Namespace), client.MatchingLabels{teamParentLabel: parent.Name}); err != nil {
		return nil, fmt.Errorf("listing team children for %s/%s: %w", parent.Namespace, parent.Name, err)
	}
	filtered := make([]platformv1alpha1.AgentRun, 0, len(children.Items))
	for i := range children.Items {
		child := children.Items[i]
		if isOwnedTeamChild(parent, &child) {
			filtered = append(filtered, child)
		}
	}
	sort.Slice(filtered, func(i, j int) bool {
		left := filtered[i]
		right := filtered[j]
		if left.Labels[teamStepLabel] == right.Labels[teamStepLabel] {
			return left.Name < right.Name
		}
		return left.Labels[teamStepLabel] < right.Labels[teamStepLabel]
	})
	return filtered, nil
}

func buildTeamSummary(parent *platformv1alpha1.AgentRun, children []platformv1alpha1.AgentRun) *platformv1alpha1.AgentRunTeamSummary {
	if parent == nil {
		return nil
	}

	currentStep := strings.TrimSpace(parent.Status.CurrentStep)
	currentStepIndex := int32(0)
	if parent.Status.TeamSummary != nil {
		if currentStep == "" {
			currentStep = strings.TrimSpace(parent.Status.TeamSummary.CurrentStep)
		}
		currentStepIndex = parent.Status.TeamSummary.CurrentStepIndex
	}
	if idx, ok := teamStepIndex(parent, currentStep); ok {
		currentStepIndex = idx
	}

	summary := &platformv1alpha1.AgentRunTeamSummary{
		CurrentStepIndex: currentStepIndex,
		CurrentStep:      currentStep,
		ApprovalState:    deriveTeamApprovalState(parent),
		TotalChildren:    int32(len(children)),
	}
	for _, child := range children {
		switch child.Status.Phase {
		case platformv1alpha1.AgentRunPhasePending, platformv1alpha1.AgentRunPhaseAdmitted, platformv1alpha1.AgentRunPhaseProvisioning:
			summary.PendingChildren++
		case platformv1alpha1.AgentRunPhaseRunning, platformv1alpha1.AgentRunPhaseQuestion, platformv1alpha1.AgentRunPhaseBlocked, platformv1alpha1.AgentRunPhaseWaitingApproval:
			summary.RunningChildren++
		case platformv1alpha1.AgentRunPhaseSucceeded:
			summary.SucceededChildren++
		case platformv1alpha1.AgentRunPhaseFailed:
			summary.FailedChildren++
		case platformv1alpha1.AgentRunPhasePaused:
			summary.PausedChildren++
		case platformv1alpha1.AgentRunPhaseCancelled:
			summary.CancelledChildren++
		}
	}
	if parent.Status.Queue != nil {
		summary.BlockedReason = strings.TrimSpace(parent.Status.Queue.BlockedReason)
	}
	if summary.BlockedReason == "" {
		for _, child := range children {
			if child.Status.Queue != nil && strings.TrimSpace(child.Status.Queue.BlockedReason) != "" {
				summary.BlockedReason = strings.TrimSpace(child.Status.Queue.BlockedReason)
				break
			}
		}
	}
	return summary
}

func teamStepIndex(parent *platformv1alpha1.AgentRun, stepName string) (int32, bool) {
	if parent == nil || parent.Spec.Team == nil || stepName == "" {
		return 0, false
	}
	for i, step := range parent.Spec.Team.Steps {
		if step.Name == stepName {
			return int32(i), true
		}
	}
	return 0, false
}

func deriveTeamApprovalState(parent *platformv1alpha1.AgentRun) string {
	if parent == nil {
		return "unknown"
	}
	if parent.Status.TeamSummary != nil && strings.TrimSpace(parent.Status.TeamSummary.ApprovalState) != "" {
		return parent.Status.TeamSummary.ApprovalState
	}
	if parent.Status.Phase == platformv1alpha1.AgentRunPhaseWaitingApproval {
		return "waiting"
	}
	if parent.Spec.Team != nil && parent.Spec.Team.CompletionPolicy != nil && parent.Spec.Team.CompletionPolicy.RequireApproval {
		return "pending"
	}
	return "not_required"
}

func summarizeTeamChild(child platformv1alpha1.AgentRun) platformv1alpha1.AgentRunChildStatus {
	out := platformv1alpha1.AgentRunChildStatus{
		Name:      child.Name,
		Namespace: child.Namespace,
		Step:      child.Labels[teamStepLabel],
		Role:      child.Labels[teamRoleLabel],
		Phase:     child.Status.Phase,
	}
	if child.Status.Queue != nil {
		out.BlockedReason = child.Status.Queue.BlockedReason
	}
	return out
}

func isTeamParentRun(run *platformv1alpha1.AgentRun) bool {
	return run != nil && run.Spec.ExecutionMode == platformv1alpha1.ExecutionModeTeam && run.Spec.Team != nil
}

func isOwnedTeamChild(parent, child *platformv1alpha1.AgentRun) bool {
	if parent == nil || child == nil || child.Namespace != parent.Namespace {
		return false
	}
	if strings.TrimSpace(child.Labels[teamParentLabel]) == parent.Name {
		return true
	}
	for _, ownerRef := range child.OwnerReferences {
		if ownerRef.APIVersion == platformv1alpha1.GroupVersion.String() &&
			ownerRef.Kind == "AgentRun" &&
			ownerRef.Name == parent.Name &&
			ownerRef.UID == parent.UID {
			return true
		}
	}
	return false
}

func teamSummaryEqual(left, right *platformv1alpha1.AgentRunTeamSummary) bool {
	switch {
	case left == nil && right == nil:
		return true
	case left == nil || right == nil:
		return false
	default:
		return *left == *right
	}
}

func teamChildrenEqual(left, right []platformv1alpha1.AgentRunChildStatus) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func projectStateIDForRun(run *platformv1alpha1.AgentRun) string {
	if run == nil {
		return ""
	}
	return projectstate.ProjectID(run.Namespace, run.Spec.Repository.URL)
}

// agentRunRuntimeProfileRef indexes AgentRuns by the RuntimeProfile they
// reference so profile fan-out and admission counting avoid namespace-wide
// lists.
func agentRunRuntimeProfileRef(obj client.Object) []string {
	run, ok := obj.(*platformv1alpha1.AgentRun)
	if !ok || run.Spec.RuntimeProfileRef == nil {
		return nil
	}
	if name := strings.TrimSpace(run.Spec.RuntimeProfileRef.Name); name != "" {
		return []string{name}
	}
	return nil
}

func (r *AgentRunReconciler) requestsForRuntimeProfile(ctx context.Context, obj client.Object) []reconcile.Request {
	profile, ok := obj.(*platformv1alpha1.RuntimeProfile)
	if !ok || profile == nil {
		return nil
	}
	var runs platformv1alpha1.AgentRunList
	if err := r.List(ctx, &runs, client.InNamespace(profile.Namespace), client.MatchingFields{runtimeProfileRefIndex: profile.Name}); err != nil {
		ctrl.LoggerFrom(ctx).Error(err, "listing AgentRuns for RuntimeProfile", "namespace", profile.Namespace, "runtimeProfile", profile.Name)
		return nil
	}
	requests := make([]reconcile.Request, 0, len(runs.Items))
	for i := range runs.Items {
		if isTerminalPhase(runs.Items[i].Status.Phase) {
			continue
		}
		requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&runs.Items[i])})
	}
	return requests
}

// requestsForRunPod maps a sandbox worker pod to its AgentRun. agent-sandbox
// makes the Sandbox the pod's controller, so Owns() never sees these pods; the
// owner-run labels stamped through the SandboxTemplate identify the run.
func (r *AgentRunReconciler) requestsForRunPod(ctx context.Context, obj client.Object) []reconcile.Request {
	uid := obj.GetLabels()[ownerRunUIDLabel]
	if uid == "" {
		return nil
	}
	if name := obj.GetLabels()[ownerRunLabel]; name != "" {
		run := &platformv1alpha1.AgentRun{}
		if err := r.Get(ctx, client.ObjectKey{Namespace: obj.GetNamespace(), Name: name}, run); err == nil && string(run.UID) == uid {
			return []reconcile.Request{{NamespacedName: client.ObjectKeyFromObject(run)}}
		}
	}
	// Long run names carry a hashed owner-run label; fall back to the UID.
	var runs platformv1alpha1.AgentRunList
	if err := r.List(ctx, &runs, client.InNamespace(obj.GetNamespace())); err != nil {
		ctrl.LoggerFrom(ctx).Error(err, "listing AgentRuns for runner pod", "namespace", obj.GetNamespace(), "pod", obj.GetName())
		return nil
	}
	for i := range runs.Items {
		if string(runs.Items[i].UID) == uid {
			return []reconcile.Request{{NamespacedName: client.ObjectKeyFromObject(&runs.Items[i])}}
		}
	}
	return nil
}

func (r *AgentRunReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &platformv1alpha1.AgentRun{}, runtimeProfileRefIndex, agentRunRuntimeProfileRef); err != nil {
		return fmt.Errorf("indexing AgentRuns by RuntimeProfile: %w", err)
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&platformv1alpha1.AgentRun{}).
		Watches(&platformv1alpha1.RuntimeProfile{}, handler.EnqueueRequestsFromMapFunc(r.requestsForRuntimeProfile),
			builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Owns(&agentsandboxextensionsv1alpha1.SandboxClaim{}).
		Watches(&corev1.Pod{}, handler.EnqueueRequestsFromMapFunc(r.requestsForRunPod),
			builder.WithPredicates(predicate.NewPredicateFuncs(func(obj client.Object) bool {
				return obj.GetLabels()[ownerRunUIDLabel] != ""
			}))).
		Named("agentrun").
		WithOptions(controller.Options{MaxConcurrentReconciles: 2}).
		Complete(r)
}
