package platform

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/mcppolicy"
	"github.com/gratefulagents/gratefulagents/internal/mode"
	"github.com/gratefulagents/gratefulagents/internal/projectstate"
	"github.com/gratefulagents/gratefulagents/internal/store"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	agentsandboxextensionsv1alpha1 "sigs.k8s.io/agent-sandbox/extensions/api/v1alpha1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

const (
	approvalRequestedAnnotation = "platform.gratefulagents.dev/approval-requested"
	cancelRequestedAnnotation   = "platform.gratefulagents.dev/cancel-requested"
	// promoteSucceededAnnotation asks the controller to tear the run down like
	// a cancellation but record the terminal phase as Succeeded — the user
	// explicitly promoted the run to success from the dashboard.
	promoteSucceededAnnotation = "platform.gratefulagents.dev/promote-succeeded-requested"
	agentRunCleanupFinalizer   = platformv1alpha1.AgentRunCleanupFinalizer
	teamParentLabel            = "platform.gratefulagents.dev/team-parent"
	teamStepLabel              = "platform.gratefulagents.dev/team-step"
	teamRoleLabel              = "platform.gratefulagents.dev/team-role"
	runModeAnnotation          = "platform.gratefulagents.dev/run-mode"
	approvalMaterializingStep  = "approval-materializing"
	podVisibilityGrace         = 2 * time.Minute
)

var errRunnerPodDrainPending = errors.New("runner pod drain pending")

type AgentRunReconciler struct {
	client.Client
	ModeResolver *mode.Resolver
	StateStore   store.StateStore
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
			return ctrl.Result{Requeue: true}, nil
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

	if changed, err := r.ensureInitialized(ctx, run); err != nil {
		return ctrl.Result{}, err
	} else if changed {
		return ctrl.Result{Requeue: true}, nil
	}

	if handled, err := r.handleCancelRequest(ctx, run); err != nil {
		if errors.Is(err, errRunnerPodDrainPending) {
			return ctrl.Result{Requeue: true}, nil
		}
		return ctrl.Result{}, err
	} else if handled {
		return ctrl.Result{}, nil
	}

	if handled, err := r.consumeInteractionAnnotations(ctx, run); err != nil {
		return ctrl.Result{}, err
	} else if handled {
		return ctrl.Result{Requeue: true}, nil
	}

	if changed, err := r.syncTeamStatus(ctx, run); err != nil {
		return ctrl.Result{}, err
	} else if changed {
		return ctrl.Result{Requeue: true}, nil
	}

	if handled, err := r.handleWakeRequest(ctx, run); err != nil {
		if errors.Is(err, errRunnerPodDrainPending) {
			return ctrl.Result{Requeue: true}, nil
		}
		return ctrl.Result{}, err
	} else if handled {
		return ctrl.Result{Requeue: true}, nil
	}

	if handled, err := r.handleRestartRequest(ctx, run); err != nil {
		if errors.Is(err, errRunnerPodDrainPending) {
			return ctrl.Result{Requeue: true}, nil
		}
		return ctrl.Result{}, err
	} else if handled {
		return ctrl.Result{Requeue: true}, nil
	}

	if isTerminalPhase(run.Status.Phase) {
		return ctrl.Result{}, nil
	}

	// Resume a paused run when its limits allow it again: the timeout has
	// been extended and/or the cost cap raised.
	if run.Status.Phase == platformv1alpha1.AgentRunPhasePaused {
		resumeAllowed := run.Status.StartedAt != nil && !runPastTimeout(run) && costCapSatisfied(run)
		// Always drain the old worker before either staying paused or resuming.
		// Otherwise a limit extension can provision a replacement while the old
		// pod is still publishing its final encrypted checkpoint.
		drained, err := r.releaseRunSandbox(ctx, run)
		if err != nil {
			return ctrl.Result{}, err
		}
		if !drained {
			return ctrl.Result{Requeue: true}, nil
		}
		if run.Status.Sandbox != nil {
			if err := clearRunSandboxStatus(ctx, r.Client, run); err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{Requeue: true}, nil
		}
		if resumeAllowed {
			if err := retryAgentRunStatusPatch(ctx, r.Client, client.ObjectKeyFromObject(run), func(fresh *platformv1alpha1.AgentRun) {
				fresh.Status.Phase = platformv1alpha1.AgentRunPhaseProvisioning
				fresh.Status.Queue = &platformv1alpha1.AgentRunQueueStatus{State: "Resuming", AdmittedAt: queueAdmittedAt(&fresh.Status)}
			}); err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{Requeue: true}, nil
		}
		return ctrl.Result{}, nil
	}

	if isDelegatedChildRun(run) {
		return r.reconcileChildRun(ctx, run)
	}

	// All workflow modes use the unified reconcile path.
	return r.reconcileRun(ctx, run)
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

	mcpPolicy, err := resolveMCPPolicyForRun(ctx, r.Client, run)
	if err != nil {
		return false, fmt.Errorf("resolving MCPPolicy: %w", err)
	}

	userSkillRefs, err := listUserSkillRefsForRun(ctx, r.Client, run)
	if err != nil {
		return false, fmt.Errorf("listing user skills: %w", err)
	}

	if needsSpecDefaults(run, snapshot, runtimeProfile, userSkillRefs) {
		if err := retryAgentRunPatch(ctx, r.Client, client.ObjectKeyFromObject(run), func(fresh *platformv1alpha1.AgentRun) {
			applySpecDefaults(fresh, snapshot, runtimeProfile, userSkillRefs)
		}); err != nil {
			return false, fmt.Errorf("applying AgentRun spec defaults: %w", err)
		}
	}

	if err := retryAgentRunStatusPatch(ctx, r.Client, client.ObjectKeyFromObject(run), func(fresh *platformv1alpha1.AgentRun) {
		now := metav1.Now()

		// Pin mode snapshot.
		if snapshot != nil && fresh.Status.ModeSnapshot == nil {
			fresh.Status.ModeSnapshot = snapshot
			fresh.Status.ModeName = snapshot.Name
			fresh.Status.ModeVersion = snapshot.Version
			fresh.Status.ModeRevision = 1
		}

		applyStatusPolicyDefaults(fresh, runtimeProfile, mcpPolicy)

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
		fresh.Status.Phase = initialPhaseForRun(fresh)
		fresh.Status.CurrentStep = initialCurrentStepForReconcile(fresh)
		fresh.Status.Queue = &platformv1alpha1.AgentRunQueueStatus{State: "Queued"}
		fresh.Status.StartedAt = &now
	}); err != nil {
		return false, fmt.Errorf("initializing AgentRun status: %w", err)
	}
	return true, nil
}

func (r *AgentRunReconciler) consumeInteractionAnnotations(ctx context.Context, run *platformv1alpha1.AgentRun) (bool, error) {
	approvalRequested := strings.EqualFold(strings.TrimSpace(run.Annotations[approvalRequestedAnnotation]), "true")
	if !approvalRequested {
		return false, nil
	}

	metaPatch := client.MergeFrom(run.DeepCopy())
	if run.Annotations == nil {
		run.Annotations = map[string]string{}
	}
	delete(run.Annotations, approvalRequestedAnnotation)
	if err := r.Patch(ctx, run, metaPatch); err != nil {
		return false, fmt.Errorf("clearing AgentRun interaction annotations: %w", err)
	}
	return true, nil
}

func shouldResumeIntoPending(run *platformv1alpha1.AgentRun) bool {
	return run != nil
}

func executeRunMaterialized(run *platformv1alpha1.AgentRun) bool {
	if run == nil {
		return false
	}
	if strings.TrimSpace(run.Spec.Repository.BranchName) == "" {
		return false
	}
	return run.Spec.SpecArtifactRef != nil && strings.TrimSpace(run.Spec.SpecArtifactRef.Name) != ""
}

// reconcileRun is the unified reconcile path for all workflow modes.
// With persistent pods, the same pod handles plan → execute → chat in-process.
// The controller only needs to: create pod once, monitor health, timeout.
func (r *AgentRunReconciler) reconcileRun(ctx context.Context, run *platformv1alpha1.AgentRun) (ctrl.Result, error) {
	if run.Status.Sandbox != nil {
		if run.Status.Sandbox.ClaimRef != nil || strings.EqualFold(run.Status.Sandbox.Provider, agentSandboxProvider) {
			return r.monitorAgentSandbox(ctx, run, 3*time.Second)
		}
		if run.Status.Sandbox.SandboxRef != nil {
			return r.monitorPod(ctx, run, 3*time.Second)
		}
	}
	// Only create a pod for active (non-terminal, non-blocked) phases.
	switch run.Status.Phase {
	case platformv1alpha1.AgentRunPhasePending, platformv1alpha1.AgentRunPhaseAdmitted,
		platformv1alpha1.AgentRunPhaseProvisioning, platformv1alpha1.AgentRunPhaseRunning,
		platformv1alpha1.AgentRunPhaseQuestion, platformv1alpha1.AgentRunPhaseBlocked,
		platformv1alpha1.AgentRunPhaseWaitingApproval:
		// Active — create or re-attach pod.
	default:
		return ctrl.Result{}, nil
	}

	runtimeProfile, err := resolveRuntimeProfileForRun(ctx, r.Client, run)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("resolving RuntimeProfile for sandbox provisioning: %w", err)
	}
	if admissionResult, err := r.enforceRuntimeProfileAdmission(ctx, run, runtimeProfile); err != nil {
		return ctrl.Result{}, err
	} else if admissionResult != nil {
		return *admissionResult, nil
	}
	sandboxStatus, err := createPlanSandbox(ctx, r.Client, run, runtimeProfile)
	if err != nil {
		if errors.Is(err, errRunSandboxDrainRequired) {
			drained, drainErr := r.releaseRunSandbox(ctx, run)
			if drainErr != nil {
				return ctrl.Result{}, drainErr
			}
			if !drained {
				return ctrl.Result{Requeue: true}, nil
			}
			if run.Status.Sandbox != nil {
				if clearErr := clearRunSandboxStatus(ctx, r.Client, run); clearErr != nil {
					return ctrl.Result{}, clearErr
				}
			}
			return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
		}
		if errors.Is(err, errRunPodReplaced) || errors.Is(err, errRunSandboxReplaced) {
			return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
		}
		return ctrl.Result{}, r.markRunFailed(ctx, run, err)
	}
	return r.patchSandboxQueued(ctx, run, sandboxStatus, platformv1alpha1.AgentRunPhaseAdmitted)
}

func (r *AgentRunReconciler) reconcileChildRun(ctx context.Context, run *platformv1alpha1.AgentRun) (ctrl.Result, error) {
	return r.reconcileRun(ctx, run)
}

func (r *AgentRunReconciler) patchSandboxQueued(ctx context.Context, run *platformv1alpha1.AgentRun, sandboxStatus *platformv1alpha1.AgentRunSandboxStatus, phase platformv1alpha1.AgentRunPhase) (ctrl.Result, error) {
	if err := retryAgentRunStatusPatch(ctx, r.Client, client.ObjectKeyFromObject(run), func(fresh *platformv1alpha1.AgentRun) {
		now := metav1.Now()
		fresh.Status.Phase = phase
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

func (r *AgentRunReconciler) monitorPod(ctx context.Context, run *platformv1alpha1.AgentRun, requeueAfter time.Duration) (ctrl.Result, error) {
	podName := ""
	if run.Status.Sandbox != nil && run.Status.Sandbox.SandboxRef != nil {
		podName = run.Status.Sandbox.SandboxRef.Name
	}
	if podName == "" {
		return ctrl.Result{}, nil
	}
	return r.monitorPodName(ctx, run, podName, requeueAfter)
}

func (r *AgentRunReconciler) monitorPodName(ctx context.Context, run *platformv1alpha1.AgentRun, podName string, requeueAfter time.Duration) (ctrl.Result, error) {
	if runPastTimeout(run) && !isTerminalPhase(run.Status.Phase) && run.Status.Phase != platformv1alpha1.AgentRunPhasePaused {
		// Check the deadline before pod lookup so a stale startup reference cannot
		// requeue forever after the run's runtime limit has elapsed.
		return ctrl.Result{}, r.markRunPaused(ctx, run, effectiveTimeout(run))
	}

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
		return ctrl.Result{}, r.markRunFailed(ctx, run, fmt.Errorf("runner pod %s failed", podName))
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

func (r *AgentRunReconciler) patchRunning(ctx context.Context, run *platformv1alpha1.AgentRun) error {
	return retryAgentRunStatusPatch(ctx, r.Client, client.ObjectKeyFromObject(run), func(fresh *platformv1alpha1.AgentRun) {
		fresh.Status.Phase = platformv1alpha1.AgentRunPhaseRunning
		fresh.Status.Queue = &platformv1alpha1.AgentRunQueueStatus{State: "Running", AdmittedAt: queueAdmittedAt(&fresh.Status)}
	})
}

func (r *AgentRunReconciler) patchSucceeded(ctx context.Context, run *platformv1alpha1.AgentRun) error {
	if err := cleanupClusterRoleBindings(ctx, r.Client, run); err != nil {
		return err
	}
	return retryAgentRunStatusPatch(ctx, r.Client, client.ObjectKeyFromObject(run), func(fresh *platformv1alpha1.AgentRun) {
		now := metav1.Now()
		fresh.Status.Phase = platformv1alpha1.AgentRunPhaseSucceeded
		fresh.Status.CompletedAt = &now
		fresh.Status.Queue = &platformv1alpha1.AgentRunQueueStatus{State: "Succeeded", AdmittedAt: queueAdmittedAt(&fresh.Status)}
	})
}

func (r *AgentRunReconciler) markRunFailed(ctx context.Context, run *platformv1alpha1.AgentRun, cause error) error {
	if err := cleanupClusterRoleBindings(ctx, r.Client, run); err != nil {
		return err
	}
	return retryAgentRunStatusPatch(ctx, r.Client, client.ObjectKeyFromObject(run), func(fresh *platformv1alpha1.AgentRun) {
		now := metav1.Now()
		fresh.Status.Phase = platformv1alpha1.AgentRunPhaseFailed
		fresh.Status.LastError = cause.Error()
		fresh.Status.CompletedAt = &now
		fresh.Status.Queue = &platformv1alpha1.AgentRunQueueStatus{State: "Failed", BlockedReason: cause.Error(), AdmittedAt: queueAdmittedAt(&fresh.Status)}
	})
}

func (r *AgentRunReconciler) markRunPaused(ctx context.Context, run *platformv1alpha1.AgentRun, timeout time.Duration) error {
	return retryAgentRunStatusPatch(ctx, r.Client, client.ObjectKeyFromObject(run), func(fresh *platformv1alpha1.AgentRun) {
		fresh.Status.Phase = platformv1alpha1.AgentRunPhasePaused
		fresh.Status.Queue = &platformv1alpha1.AgentRunQueueStatus{
			State:         "Paused",
			BlockedReason: fmt.Sprintf("paused after %s timeout — extend maxRuntime to resume", timeout),
			AdmittedAt:    queueAdmittedAt(&fresh.Status),
		}
	})
}

func retryAgentRunStatusPatch(ctx context.Context, c client.Client, key client.ObjectKey, mutate func(*platformv1alpha1.AgentRun)) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		fresh := &platformv1alpha1.AgentRun{}
		if err := c.Get(ctx, key, fresh); err != nil {
			return err
		}
		patch := client.MergeFromWithOptions(fresh.DeepCopy(), client.MergeFromWithOptimisticLock{})
		mutate(fresh)
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
		now := metav1.Now()
		fresh.Status.Phase = phase
		fresh.Status.CompletedAt = &now
		fresh.Status.Queue = queue.DeepCopy()
		fresh.Status.LastError = ""
		fresh.Status.Sandbox = nil
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

func resolveMCPPolicyForRun(ctx context.Context, c client.Client, run *platformv1alpha1.AgentRun) (*platformv1alpha1.MCPPolicy, error) {
	if run == nil || run.Spec.MCPPolicyRef == nil || strings.TrimSpace(run.Spec.MCPPolicyRef.Name) == "" {
		return nil, nil
	}
	policy := &platformv1alpha1.MCPPolicy{}
	key := client.ObjectKey{Namespace: run.Namespace, Name: run.Spec.MCPPolicyRef.Name}
	if err := c.Get(ctx, key, policy); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return policy, nil
}

func listUserSkillRefsForRun(ctx context.Context, c client.Client, run *platformv1alpha1.AgentRun) ([]platformv1alpha1.NamedRef, error) {
	if run == nil || isStandingOverseerRun(run) {
		return nil, nil
	}
	var skills platformv1alpha1.SkillList
	if err := c.List(ctx, &skills, client.InNamespace(run.Namespace)); err != nil {
		return nil, err
	}
	sort.Slice(skills.Items, func(i, j int) bool { return skills.Items[i].Name < skills.Items[j].Name })
	refs := make([]platformv1alpha1.NamedRef, 0, len(skills.Items))
	for i := range skills.Items {
		refs = append(refs, platformv1alpha1.NamedRef{Name: skills.Items[i].Name})
	}
	return refs, nil
}

func isStandingOverseerRun(run *platformv1alpha1.AgentRun) bool {
	_, supervisedRunName, _ := supervisedIdentityForRun(run)
	return supervisedRunName != ""
}

func effectiveSkillRefs(run *platformv1alpha1.AgentRun, snapshot *platformv1alpha1.ModeTemplateSpec, userSkillRefs []platformv1alpha1.NamedRef) []platformv1alpha1.NamedRef {
	if run == nil || isStandingOverseerRun(run) {
		return nil
	}
	refs := make([]platformv1alpha1.NamedRef, 0, len(run.Spec.SkillRefs)+len(userSkillRefs))
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
	appendUnique(userSkillRefs)
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

func needsSpecDefaults(run *platformv1alpha1.AgentRun, snapshot *platformv1alpha1.ModeTemplateSpec, runtimeProfile *platformv1alpha1.RuntimeProfile, userSkillRefs []platformv1alpha1.NamedRef) bool {
	if run == nil {
		return false
	}
	if snapshot != nil && len(snapshot.DefaultMCPServerRefs) > 0 && len(run.Spec.MCPServerRefs) == 0 {
		return true
	}
	if !namedRefsEqual(run.Spec.SkillRefs, effectiveSkillRefs(run, snapshot, userSkillRefs)) {
		return true
	}
	if runtimeProfile != nil && runtimeProfile.Spec.Security != nil &&
		runtimeProfile.Spec.Security.DefaultTimeout.Duration > 0 &&
		(run.Spec.Limits == nil || run.Spec.Limits.MaxRuntime.Duration == 0) {
		return true
	}
	return false
}

func applySpecDefaults(run *platformv1alpha1.AgentRun, snapshot *platformv1alpha1.ModeTemplateSpec, runtimeProfile *platformv1alpha1.RuntimeProfile, userSkillRefs []platformv1alpha1.NamedRef) {
	if run == nil {
		return
	}
	if snapshot != nil && len(snapshot.DefaultMCPServerRefs) > 0 && len(run.Spec.MCPServerRefs) == 0 {
		refs := make([]platformv1alpha1.NamedRef, len(snapshot.DefaultMCPServerRefs))
		copy(refs, snapshot.DefaultMCPServerRefs)
		run.Spec.MCPServerRefs = refs
	}
	run.Spec.SkillRefs = effectiveSkillRefs(run, snapshot, userSkillRefs)
	if runtimeProfile != nil && runtimeProfile.Spec.Security != nil && runtimeProfile.Spec.Security.DefaultTimeout.Duration > 0 {
		if run.Spec.Limits == nil {
			run.Spec.Limits = &platformv1alpha1.AgentRunLimits{}
		}
		if run.Spec.Limits.MaxRuntime.Duration == 0 {
			run.Spec.Limits.MaxRuntime = runtimeProfile.Spec.Security.DefaultTimeout
		}
	}
}

func applyStatusPolicyDefaults(run *platformv1alpha1.AgentRun, runtimeProfile *platformv1alpha1.RuntimeProfile, mcpPolicy *platformv1alpha1.MCPPolicy) {
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
	if mcpPolicy != nil {
		if run.Status.Policy == nil {
			run.Status.Policy = &platformv1alpha1.AgentRunResolvedPolicy{}
		}
		run.Status.Policy.ResolvedMCPServers = append(run.Status.Policy.ResolvedMCPServers[:0], mcppolicy.ExplicitAllowedServers(mcpPolicy)...)
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

// costCapSatisfied reports whether the run is within its spec.limits.maxCostUsd
// ceiling. Runs the agent paused at the cap resume once the cap is raised
// above the recorded spend. Missing or invalid values never block.
func costCapSatisfied(run *platformv1alpha1.AgentRun) bool {
	if run == nil || run.Spec.Limits == nil {
		return true
	}
	capRaw := strings.TrimSpace(run.Spec.Limits.MaxCostUsd)
	if capRaw == "" {
		return true
	}
	capUSD, err := strconv.ParseFloat(capRaw, 64)
	if err != nil || capUSD <= 0 {
		return true
	}
	if run.Status.Metrics == nil {
		return true
	}
	spent, err := strconv.ParseFloat(strings.TrimSpace(run.Status.Metrics.CostUsd), 64)
	if err != nil {
		return true
	}
	return spent < capUSD
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
		freshPhase := fresh.Status.Phase
		now := metav1.Now()
		fresh.Status.Phase = platformv1alpha1.AgentRunPhasePending
		fresh.Status.Queue = &platformv1alpha1.AgentRunQueueStatus{State: "Waking", AdmittedAt: queueAdmittedAt(&fresh.Status)}
		fresh.Status.Sandbox = nil
		fresh.Status.CompletedAt = nil
		fresh.Status.CompletionRequested = false
		fresh.Status.LastError = ""
		fresh.Status.CurrentStep = initialCurrentStepForReconcile(fresh)
		fresh.Status.WakeRequestsHandled = fresh.Spec.WakeRequests
		fresh.Status.LastWakeTime = &now
		fresh.Status.LastWakeReason = "wake-request"
		if freshPhase == platformv1alpha1.AgentRunPhaseFailed {
			fresh.Status.RetryCount++
		}
		woke = true
	}); err != nil {
		return false, fmt.Errorf("patching AgentRun wake status: %w", err)
	}
	return woke, nil
}

// runPastTimeout reports whether the run's active window exceeded its timeout.
// The window restarts on wake so a resumed run gets a fresh maxRuntime budget
// instead of instantly re-pausing off the original start time.
func runPastTimeout(run *platformv1alpha1.AgentRun) bool {
	if run == nil || run.Status.StartedAt == nil {
		return false
	}
	start := run.Status.StartedAt.Time
	if lastWake := run.Status.LastWakeTime; lastWake != nil && lastWake.Time.After(start) {
		start = lastWake.Time
	}
	return time.Since(start) > effectiveTimeout(run)
}

// handleRestartRequest bounces a non-terminal run's compute so spec changes
// that need a fresh pod (e.g. switched provider credentials) take effect.
// Session state lives in the store, so the re-provisioned pod resumes the
// run. Terminal runs consume the counter without action — wake requests own
// resumes of completed runs.
func (r *AgentRunReconciler) handleRestartRequest(ctx context.Context, run *platformv1alpha1.AgentRun) (bool, error) {
	if run == nil {
		return false, nil
	}
	if run.Spec.RestartRequests <= run.Status.RestartRequestsHandled {
		return false, nil
	}
	restartRequests := run.Spec.RestartRequests
	if isTerminalPhase(run.Status.Phase) {
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
		now := metav1.Now()
		fresh.Status.Phase = platformv1alpha1.AgentRunPhasePending
		fresh.Status.Queue = &platformv1alpha1.AgentRunQueueStatus{State: "Restarting", AdmittedAt: queueAdmittedAt(&fresh.Status)}
		fresh.Status.Sandbox = nil
		fresh.Status.CompletedAt = nil
		fresh.Status.CompletionRequested = false
		fresh.Status.LastError = ""
		fresh.Status.CurrentStep = initialCurrentStepForReconcile(fresh)
		fresh.Status.RestartRequestsHandled = restartRequests
		fresh.Status.LastWakeTime = &now
		fresh.Status.LastWakeReason = "restart-request"
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

	podNames := make(map[string]struct{})
	claimNames := map[string]struct{}{sandboxClaimName(run): {}}
	if sandbox := run.Status.Sandbox; sandbox != nil {
		if sandbox.SandboxRef != nil {
			if name := strings.TrimSpace(sandbox.SandboxRef.Name); name != "" {
				podNames[name] = struct{}{}
			}
		}
		if sandbox.ClaimRef != nil {
			if name := strings.TrimSpace(sandbox.ClaimRef.Name); name != "" {
				claimNames[name] = struct{}{}
			}
		}
	}

	claimsPresent := false
	for name := range claimNames {
		claim := &agentsandboxextensionsv1alpha1.SandboxClaim{}
		err := r.Get(ctx, client.ObjectKey{Name: name, Namespace: run.Namespace}, claim)
		if apierrors.IsNotFound(err) {
			continue
		}
		if err != nil {
			return false, fmt.Errorf("getting sandbox claim %s/%s during drain: %w", run.Namespace, name, err)
		}
		claimsPresent = true
		if sandboxName := strings.TrimSpace(claim.Status.SandboxStatus.Name); sandboxName != "" {
			if podName, err := resolveSandboxPodName(ctx, r.Client, run.Namespace, sandboxName); err == nil && podName != "" {
				podNames[podName] = struct{}{}
			} else if err != nil && !apierrors.IsNotFound(err) {
				return false, fmt.Errorf("resolving sandbox pod during drain: %w", err)
			}
		}
	}

	ownedPods := &corev1.PodList{}
	if err := r.List(ctx, ownedPods, client.InNamespace(run.Namespace), client.MatchingLabels{
		"platform.gratefulagents.dev/owner-run-uid": string(run.UID),
	}); err != nil {
		return false, fmt.Errorf("listing owned runner pods during drain: %w", err)
	}
	for i := range ownedPods.Items {
		podNames[ownedPods.Items[i].Name] = struct{}{}
	}

	podPresent := false
	for name := range podNames {
		pod := &corev1.Pod{}
		err := r.Get(ctx, client.ObjectKey{Namespace: run.Namespace, Name: name}, pod)
		if apierrors.IsNotFound(err) {
			continue
		}
		if err != nil {
			return false, fmt.Errorf("getting runner pod %s/%s during drain: %w", run.Namespace, name, err)
		}
		podPresent = true
		if pod.DeletionTimestamp.IsZero() {
			if err := r.Delete(ctx, pod); err != nil && !apierrors.IsNotFound(err) {
				return false, fmt.Errorf("deleting runner pod %s/%s: %w", run.Namespace, name, err)
			}
		}
	}
	if podPresent {
		return false, nil
	}

	if claimsPresent {
		remaining := false
		for name := range claimNames {
			claim := &agentsandboxextensionsv1alpha1.SandboxClaim{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: run.Namespace}}
			if err := r.Delete(ctx, claim); err != nil && !apierrors.IsNotFound(err) {
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
	if err := deleteManagedSandboxTemplateIfExists(ctx, r.Client, run.Namespace, managedSandboxTemplateName(run)); err != nil {
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

func sandboxPodName(run *platformv1alpha1.AgentRun) string {
	if run == nil || run.Status.Sandbox == nil || run.Status.Sandbox.SandboxRef == nil {
		return ""
	}
	return strings.TrimSpace(run.Status.Sandbox.SandboxRef.Name)
}

func isChatLikeRun(run *platformv1alpha1.AgentRun) bool {
	return run != nil
}

func shouldQueueFreshTurnFromReply(run *platformv1alpha1.AgentRun) bool {
	if run == nil {
		return false
	}
	// Autonomous runs (auto, ultrawork, pipeline, etc.) always queue fresh turns.
	if run.Status.ModeSnapshot != nil && run.Status.ModeSnapshot.Autonomous {
		return true
	}
	// Chat-like runs only queue on terminal phase (pod restart).
	if isTerminalPhase(run.Status.Phase) {
		return true
	}
	return false
}

func prepareRunForNewAttempt(run *platformv1alpha1.AgentRun, currentStep string) {
	if run == nil {
		return
	}
	currentStep = strings.TrimSpace(currentStep)
	if currentStep == "" {
		if run.Status.ModeSnapshot != nil && run.Status.ModeSnapshot.Autonomous {
			currentStep = "auto"
		} else {
			currentStep = awaitingUserStep
		}
	}
	run.Status.Phase = platformv1alpha1.AgentRunPhasePending
	run.Status.Queue = &platformv1alpha1.AgentRunQueueStatus{State: "Queued"}
	run.Status.Sandbox = nil
	run.Status.LastError = ""
	run.Status.CompletedAt = nil
	run.Status.CurrentStep = currentStep
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
	children, err := r.listOwnedTeamChildren(ctx, parent)
	if err != nil {
		return false, err
	}

	nextSummary := buildTeamSummary(parent, children)
	nextChildren := make([]platformv1alpha1.AgentRunChildStatus, 0, len(children))
	for _, child := range children {
		nextChildren = append(nextChildren, summarizeTeamChild(child))
	}

	if teamSummaryEqual(parent.Status.TeamSummary, nextSummary) && teamChildrenEqual(parent.Status.Children, nextChildren) {
		return false, nil
	}

	if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		fresh := &platformv1alpha1.AgentRun{}
		if err := r.Get(ctx, client.ObjectKeyFromObject(parent), fresh); err != nil {
			return err
		}
		if teamSummaryEqual(fresh.Status.TeamSummary, nextSummary) && teamChildrenEqual(fresh.Status.Children, nextChildren) {
			return nil
		}
		patch := client.MergeFrom(fresh.DeepCopy())
		fresh.Status.TeamSummary = nextSummary
		fresh.Status.Children = nextChildren
		return r.Status().Patch(ctx, fresh, patch)
	}); err != nil {
		return false, fmt.Errorf("patching team parent status: %w", err)
	}
	return true, nil
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

func (r *AgentRunReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&platformv1alpha1.AgentRun{}).
		Owns(&agentsandboxextensionsv1alpha1.SandboxClaim{}).
		Owns(&corev1.Pod{}).
		Named("agentrun").
		WithOptions(controller.Options{MaxConcurrentReconciles: 2}).
		Complete(r)
}
