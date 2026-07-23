package platform

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/mcppolicy"
	"github.com/gratefulagents/gratefulagents/internal/mode"
	"github.com/gratefulagents/gratefulagents/internal/orchestration"
	"github.com/gratefulagents/gratefulagents/internal/store"
	"github.com/gratefulagents/gratefulagents/internal/store/sessionclient"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
)

const (
	defaultOverseerModeName       = "overseer"
	defaultOverseerInterval       = 10 * time.Minute
	defaultOverseerEpisodeTimeout = 30 * time.Minute
	maxCompletionRejections       = int32(2)
	overseerAttribution           = "overseer"
	overseerOpenAIAPIMode         = "platform.gratefulagents.dev/openai-api-mode"
	overseerStateActive           = "active"
	overseerStateCancelled        = "cancelled"
	overseerStateCapped           = "capped"
	overseerStateChecking         = "checking"
	overseerStateDegraded         = "degraded"
	overseerStateDetaching        = "detaching"
	overseerStateEscalated        = "escalated"
	overseerStateObserving        = "observing"
	overseerStateUnavailable      = "unavailable"
)

// AgentRunOverseerReconciler manages one persistent standing overseer run for
// each opt-in AgentRun. The standing run is dormant between checkpoint wakes;
// its Postgres session is reused for the supervised run's lifetime.
type AgentRunOverseerReconciler struct {
	client.Client
	Scheme     *runtime.Scheme
	StateStore store.StateStore
	Recorder   record.EventRecorder
	Now        func() time.Time
}

// +kubebuilder:rbac:groups=platform.gratefulagents.dev,resources=agentruns,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=platform.gratefulagents.dev,resources=agentruns/status,verbs=get;update;patch
// +kubebuilder:rbac:groups="",resources=events,verbs=create

func (r *AgentRunOverseerReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	run := &platformv1alpha1.AgentRun{}
	if err := r.Get(ctx, req.NamespacedName, run); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !run.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}

	if role := strings.TrimSpace(run.Labels[orchestration.StandingRunRoleLabel]); role != "" {
		if role != orchestration.StandingRunRoleOverseer {
			return ctrl.Result{}, nil
		}
		primary, err := r.primaryForOverseer(ctx, run)
		if err != nil || primary == nil {
			return ctrl.Result{}, err
		}
		if primary.Spec.Overseer == nil {
			return r.detachOverseer(ctx, primary)
		}
		return r.reconcilePrimary(ctx, primary)
	}

	if run.Spec.Overseer == nil {
		return r.detachOverseer(ctx, run)
	}
	return r.reconcilePrimary(ctx, run)
}

func (r *AgentRunOverseerReconciler) primaryForOverseer(ctx context.Context, overseer *platformv1alpha1.AgentRun) (*platformv1alpha1.AgentRun, error) {
	name := strings.TrimSpace(overseer.Labels[orchestration.SupervisedRunLabel])
	if name == "" {
		return nil, nil
	}
	primary := &platformv1alpha1.AgentRun{}
	if err := r.Get(ctx, client.ObjectKey{Namespace: overseer.Namespace, Name: name}, primary); err != nil {
		return nil, client.IgnoreNotFound(err)
	}
	if !metav1.IsControlledBy(overseer, primary) {
		return nil, nil
	}
	return primary, nil
}

func (r *AgentRunOverseerReconciler) detachOverseer(ctx context.Context, primary *platformv1alpha1.AgentRun) (ctrl.Result, error) {
	key := client.ObjectKey{
		Namespace: primary.Namespace,
		Name:      orchestration.StandingRunName(primary.Name, orchestration.StandingRunRoleOverseer),
	}
	standing := &platformv1alpha1.AgentRun{}
	err := r.Get(ctx, key, standing)
	if err == nil {
		if standing.Labels[orchestration.StandingRunRoleLabel] != orchestration.StandingRunRoleOverseer || !metav1.IsControlledBy(standing, primary) {
			if updateErr := r.updateOverseerSummary(ctx, primary, func(summary *platformv1alpha1.AgentRunOverseerStatus) {
				summary.RunName = key.Name
				summary.State = overseerStateDegraded
				summary.LastSummary = "Cannot detach overseer because its deterministic run name is occupied by an unrelated AgentRun."
			}); updateErr != nil {
				return ctrl.Result{}, updateErr
			}
			return ctrl.Result{}, fmt.Errorf("detaching overseer: AgentRun %s/%s is not controlled by the supervised run", key.Namespace, key.Name)
		}

		if strings.TrimSpace(primary.Annotations[platformv1alpha1.OverseerDetachingAnnotation]) == "" {
			if err := retryAgentRunPatch(ctx, r.Client, client.ObjectKeyFromObject(primary), func(fresh *platformv1alpha1.AgentRun) {
				if fresh.Annotations == nil {
					fresh.Annotations = map[string]string{}
				}
				fresh.Annotations[platformv1alpha1.OverseerDetachingAnnotation] = "true"
			}); err != nil {
				return ctrl.Result{}, err
			}
		}
		if primary.Status.OverseerSummary == nil || primary.Status.OverseerSummary.RunName != key.Name || primary.Status.OverseerSummary.State != overseerStateDetaching {
			if err := r.updateOverseerSummary(ctx, primary, func(summary *platformv1alpha1.AgentRunOverseerStatus) {
				summary.RunName = key.Name
				summary.State = overseerStateDetaching
				summary.LastSummary = "Waiting for the standing overseer run to stop."
			}); err != nil {
				return ctrl.Result{}, err
			}
		}
		if standing.DeletionTimestamp.IsZero() {
			if err := r.Delete(ctx, standing); err != nil && !apierrors.IsNotFound(err) {
				return ctrl.Result{}, fmt.Errorf("deleting detached overseer run: %w", err)
			}
		}
		return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
	}
	if !apierrors.IsNotFound(err) {
		return ctrl.Result{}, err
	}

	// The deterministic standing run is fully gone (including finalizers), so
	// reattachment may safely create a fresh immutable mode/model snapshot.
	if primary.Status.OverseerSummary != nil {
		if err := retryAgentRunStatusPatch(ctx, r.Client, client.ObjectKeyFromObject(primary), func(fresh *platformv1alpha1.AgentRun) {
			fresh.Status.OverseerSummary = nil
		}); err != nil {
			return ctrl.Result{}, err
		}
	}
	if strings.TrimSpace(primary.Annotations[platformv1alpha1.OverseerDetachingAnnotation]) != "" {
		if err := retryAgentRunPatch(ctx, r.Client, client.ObjectKeyFromObject(primary), func(fresh *platformv1alpha1.AgentRun) {
			delete(fresh.Annotations, platformv1alpha1.OverseerDetachingAnnotation)
		}); err != nil {
			return ctrl.Result{}, err
		}
	}
	return ctrl.Result{}, nil
}

func (r *AgentRunOverseerReconciler) stopCancelledOverseer(ctx context.Context, primary *platformv1alpha1.AgentRun) (ctrl.Result, error) {
	name := orchestration.StandingRunName(primary.Name, orchestration.StandingRunRoleOverseer)
	standing := &platformv1alpha1.AgentRun{}
	err := r.Get(ctx, client.ObjectKey{Namespace: primary.Namespace, Name: name}, standing)
	if err == nil && standing.Labels[orchestration.StandingRunRoleLabel] == orchestration.StandingRunRoleOverseer && metav1.IsControlledBy(standing, primary) {
		if err := r.Delete(ctx, standing); err != nil && !apierrors.IsNotFound(err) {
			return ctrl.Result{}, fmt.Errorf("deleting overseer for cancelled primary: %w", err)
		}
	} else if err != nil && !apierrors.IsNotFound(err) {
		return ctrl.Result{}, err
	}
	if err := r.updateOverseerSummary(ctx, primary, func(summary *platformv1alpha1.AgentRunOverseerStatus) {
		summary.RunName = name
		summary.State = overseerStateCancelled
		summary.LastSummary = "Primary run was cancelled; its standing overseer was stopped."
	}); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

func (r *AgentRunOverseerReconciler) reconcilePrimary(ctx context.Context, primary *platformv1alpha1.AgentRun) (ctrl.Result, error) {
	if primary == nil || primary.Spec.Overseer == nil {
		return ctrl.Result{}, nil
	}
	if primary.Status.Phase == platformv1alpha1.AgentRunPhaseCancelled {
		return r.stopCancelledOverseer(ctx, primary)
	}
	if r.StateStore == nil {
		err := r.updateOverseerSummary(ctx, primary, func(summary *platformv1alpha1.AgentRunOverseerStatus) {
			summary.State = overseerStateUnavailable
			summary.LastSummary = "Overseer requires the Postgres state store; the primary run continues unsupervised."
		})
		return ctrl.Result{}, err
	}

	standingKey := client.ObjectKey{Namespace: primary.Namespace, Name: orchestration.StandingRunName(primary.Name, orchestration.StandingRunRoleOverseer)}
	existing := &platformv1alpha1.AgentRun{}
	if err := r.Get(ctx, standingKey, existing); apierrors.IsNotFound(err) {
		if err := r.validateOverseerMode(ctx, configuredOverseerModeRef(primary)); err != nil {
			_ = r.updateOverseerSummary(ctx, primary, func(summary *platformv1alpha1.AgentRunOverseerStatus) {
				summary.State = overseerStateDegraded
				summary.LastSummary = truncateStatusText(err.Error())
			})
			return ctrl.Result{}, err
		}
	} else if err != nil {
		return ctrl.Result{}, err
	}

	now := r.now()
	primarySession, err := r.StateStore.GetSessionByRun(ctx, primary.Name, primary.Namespace)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("getting primary session for overseer checkpoint: %w", err)
	}
	initialObservation, initialInput, err := observationForSession(primary, "attached", primarySession)
	if err != nil {
		return ctrl.Result{}, err
	}
	desired := r.desiredOverseerRun(primary, initialObservation, now)
	initialMessage := overseerCheckpointMessage(primary, 1, initialObservation, initialInput)
	standing, created, err := orchestration.EnsureStandingRun(ctx, r.Client, r.Scheme, r.StateStore, primary, desired, initialMessage)
	if err != nil {
		_ = r.updateOverseerSummary(ctx, primary, func(summary *platformv1alpha1.AgentRunOverseerStatus) {
			summary.State = overseerStateDegraded
			summary.LastSummary = truncateStatusText(fmt.Sprintf("Failed to ensure overseer run: %v", err))
		})
		return ctrl.Result{}, err
	}
	configChanged := !created && (standing.Spec.Model != desired.Spec.Model || !reflect.DeepEqual(standing.Spec.ModeRef, desired.Spec.ModeRef))
	if err := r.updateOverseerSummary(ctx, primary, func(summary *platformv1alpha1.AgentRunOverseerStatus) {
		summary.RunName = standing.Name
		if summary.State == "" || summary.State == "starting" || created {
			summary.State = overseerStateChecking
		}
	}); err != nil {
		return ctrl.Result{}, err
	}
	if created {
		r.eventf(primary, corev1.EventTypeNormal, "OverseerAttached", "Attached standing overseer run %s", standing.Name)
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}

	handled, wait, err := r.routeCompletedCheckpoint(ctx, primary, standing)
	if err != nil {
		return ctrl.Result{}, err
	}
	if wait {
		return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
	}
	if handled {
		if err := r.Get(ctx, client.ObjectKeyFromObject(primary), primary); err != nil {
			return ctrl.Result{}, client.IgnoreNotFound(err)
		}
		if err := r.Get(ctx, client.ObjectKeyFromObject(standing), standing); err != nil {
			return ctrl.Result{}, client.IgnoreNotFound(err)
		}
	}
	if configChanged {
		if err := r.updateOverseerSummary(ctx, primary, func(summary *platformv1alpha1.AgentRunOverseerStatus) {
			summary.RunName = standing.Name
			summary.State = overseerStateDegraded
			summary.LastSummary = "Overseer model/mode changes require detaching and reattaching spec.overseer so the standing session can be recreated safely."
		}); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	return r.maybeScheduleCheckpoint(ctx, primary, standing, now)
}

func (r *AgentRunOverseerReconciler) desiredOverseerRun(primary *platformv1alpha1.AgentRun, observation overseerObservation, now time.Time) *platformv1alpha1.AgentRun {
	model := strings.TrimSpace(primary.Spec.Model)
	if configured := strings.TrimSpace(primary.Spec.Overseer.Model); configured != "" {
		model = configured
	} else if configured := strings.TrimSpace(os.Getenv("OVERSEER_DEFAULT_MODEL")); configured != "" {
		model = configured
	}
	repository := primary.Spec.Repository
	repository.AdditionalRepos = append([]string(nil), primary.Spec.Repository.AdditionalRepos...)
	spec := platformv1alpha1.AgentRunSpec{
		Trigger: platformv1alpha1.TriggerRef{
			Kind: "AgentRunOverseer",
			Name: primary.Name,
			ExternalRef: &platformv1alpha1.ExternalRef{
				ID:         string(primary.UID),
				Identifier: "Overseer for " + primary.Name,
			},
		},
		Repository:         repository,
		Context:            &platformv1alpha1.AgentRunContext{ProjectRef: &platformv1alpha1.ProjectRef{Kind: "AgentRun", Name: primary.Name}},
		WorkflowMode:       platformv1alpha1.WorkflowModeAuto,
		ExecutionMode:      platformv1alpha1.ExecutionModeLinear,
		ModeRef:            &platformv1alpha1.ModeRef{Name: defaultOverseerModeName},
		Model:              model,
		ReasoningLevel:     primary.Spec.ReasoningLevel,
		AuthMode:           primary.Spec.AuthMode,
		OpenAIBaseURL:      primary.Spec.OpenAIBaseURL,
		Image:              primary.Spec.Image,
		RuntimeProfileRef:  copyNamedRef(primary.Spec.RuntimeProfileRef),
		GuardrailPolicyRef: copyNamedRef(primary.Spec.GuardrailPolicyRef),
		Secrets:            minimalOverseerSecrets(primary.Spec.Secrets, model, primary.Spec.AuthMode),
		Limits: &platformv1alpha1.AgentRunLimits{
			MaxTurns:   40,
			MaxRuntime: metav1.Duration{Duration: defaultOverseerEpisodeTimeout},
			// Wake retries are intentionally uncapped: this standing run is
			// reused across independent checkpoints. Per-episode retry behavior
			// is bounded by the overseer ModeTemplate; intervention effects are
			// bounded by spec.overseer.maxInterventions.
			MaxRetries: 0,
		},
	}
	if primary.Spec.Overseer.ModeRef != nil {
		spec.ModeRef = primary.Spec.Overseer.ModeRef.DeepCopy()
	}

	annotations := map[string]string{
		orchestration.CheckpointSeqAnnotation:    "1",
		orchestration.CheckpointReasonAnnotation: encodeObservation(observation),
		orchestration.CheckpointTimeAnnotation:   now.UTC().Format(time.RFC3339Nano),
	}
	if value := strings.TrimSpace(primary.Annotations[overseerOpenAIAPIMode]); value != "" {
		annotations[overseerOpenAIAPIMode] = value
	}
	return &platformv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:  primary.Namespace,
			Finalizers: []string{platformv1alpha1.AgentRunCleanupFinalizer},
			Labels: map[string]string{
				orchestration.StandingRunRoleLabel: orchestration.StandingRunRoleOverseer,
				orchestration.SupervisedRunLabel:   primary.Name,
			},
			Annotations: annotations,
		},
		Spec: spec,
	}
}

func configuredOverseerModeRef(primary *platformv1alpha1.AgentRun) *platformv1alpha1.ModeRef {
	if primary != nil && primary.Spec.Overseer != nil && primary.Spec.Overseer.ModeRef != nil {
		return primary.Spec.Overseer.ModeRef.DeepCopy()
	}
	return &platformv1alpha1.ModeRef{Name: defaultOverseerModeName}
}

func (r *AgentRunOverseerReconciler) validateOverseerMode(ctx context.Context, ref *platformv1alpha1.ModeRef) error {
	if ref == nil || strings.TrimSpace(ref.Name) == "" {
		return fmt.Errorf("overseer ModeTemplate reference is required")
	}
	mode := &platformv1alpha1.ModeTemplate{}
	if err := r.Get(ctx, client.ObjectKey{Name: strings.TrimSpace(ref.Name)}, mode); err != nil {
		return fmt.Errorf("getting overseer ModeTemplate %q: %w", ref.Name, err)
	}
	if mode.Spec.PermissionMode != platformv1alpha1.PermissionModeReadOnly {
		return fmt.Errorf("overseer ModeTemplate %q must enforce permissionMode=read-only", ref.Name)
	}
	if !mode.Spec.Autonomous || mode.Spec.ExecutionStrategy != platformv1alpha1.ExecutionStrategySerial {
		return fmt.Errorf("overseer ModeTemplate %q must be autonomous with serial execution", ref.Name)
	}
	if len(mode.Spec.AllowedMutatingTools) != 1 || mode.Spec.AllowedMutatingTools[0] != "submit_overseer_verdict" {
		return fmt.Errorf("overseer ModeTemplate %q may allow only submit_overseer_verdict as a mutating tool", ref.Name)
	}
	if len(mode.Spec.DefaultMCPServerRefs) != 0 || len(mode.Spec.DefaultSkillRefs) != 0 {
		return fmt.Errorf("overseer ModeTemplate %q must not attach default MCP servers or skills", ref.Name)
	}
	return nil
}

func copyNamedRef(ref *platformv1alpha1.NamedRef) *platformv1alpha1.NamedRef {
	if ref == nil {
		return nil
	}
	return &platformv1alpha1.NamedRef{Name: ref.Name}
}

// minimalOverseerSecrets carries only credentials required to call the chosen
// model and clone the supervised repository. Slack and credentials for other
// model providers are deliberately excluded from the standing run.
func minimalOverseerSecrets(in *platformv1alpha1.AgentRunSecrets, model string, authMode platformv1alpha1.AgentRunAuthMode) *platformv1alpha1.AgentRunSecrets {
	if in == nil {
		return nil
	}
	provider := "openai"
	if slash := strings.Index(model, "/"); slash > 0 {
		provider = strings.ToLower(strings.TrimSpace(model[:slash]))
	}
	out := &platformv1alpha1.AgentRunSecrets{
		GitHubTokenSecret: strings.TrimSpace(in.GitHubTokenSecret),
	}
	if authMode == platformv1alpha1.AgentRunAuthModeOAuth {
		for _, oauth := range in.ProviderOAuthSecrets {
			if strings.EqualFold(strings.TrimSpace(oauth.Provider), provider) {
				out.ProviderOAuthSecrets = append(out.ProviderOAuthSecrets, oauth)
			}
		}
		if provider == "openai" {
			out.OpenAIOAuthSecret = strings.TrimSpace(in.OpenAIOAuthSecret)
		}
	} else {
		for _, key := range in.ProviderKeys {
			if strings.EqualFold(strings.TrimSpace(key.Provider), provider) {
				out.ProviderKeys = append(out.ProviderKeys, key)
			}
		}
		if len(out.ProviderKeys) == 0 {
			// ClaudeAPIKeySecret is the legacy generic API-key fallback despite its
			// name. Run creation and pod wiring use it for the selected provider.
			out.ClaudeAPIKeySecret = strings.TrimSpace(in.ClaudeAPIKeySecret)
		}
	}
	return out
}

type overseerObservation struct {
	Trigger           string                         `json:"trigger"`
	Phase             platformv1alpha1.AgentRunPhase `json:"phase,omitempty"`
	ModeRevision      int64                          `json:"modeRevision,omitempty"`
	BudgetThreshold   int32                          `json:"budgetThreshold,omitempty"`
	CompletionAttempt string                         `json:"completionAttempt,omitempty"`
	InputRequestID    string                         `json:"inputRequestID,omitempty"`
}

func observationFor(run *platformv1alpha1.AgentRun, trigger string) overseerObservation {
	observation := overseerObservation{Trigger: trigger}
	if run == nil {
		return observation
	}
	observation.Phase = run.Status.Phase
	observation.ModeRevision = run.Status.ModeRevision
	observation.BudgetThreshold = runBudgetThreshold(run)
	observation.CompletionAttempt = fmt.Sprintf("%d:%t", run.Status.WakeRequestsHandled, run.Status.CompletionRequested)
	return observation
}

func observationForSession(run *platformv1alpha1.AgentRun, trigger string, session *store.Session) (overseerObservation, *orchestration.PendingUserInput, error) {
	observation := observationFor(run, trigger)
	request, err := pendingUserInputForRun(run, session)
	if err != nil {
		return overseerObservation{}, nil, err
	}
	if request != nil {
		observation.InputRequestID = request.ID
	}
	return observation, request, nil
}

func pendingUserInputForRun(run *platformv1alpha1.AgentRun, session *store.Session) (*orchestration.PendingUserInput, error) {
	request := orchestration.PendingUserInputForSession(session)
	if request == nil || run == nil {
		return request, nil
	}
	pendingMCP, err := mcppolicy.PendingRequest(run)
	if err != nil {
		return nil, fmt.Errorf("decoding pending MCP break-glass request: %w", err)
	}
	if pendingMCP != nil {
		request = orchestration.BindPendingUserInputContext(request, pendingMCP.ID)
	}
	return request, nil
}

func encodeObservation(observation overseerObservation) string {
	raw, err := json.Marshal(observation)
	if err != nil {
		return `{"trigger":"unknown"}`
	}
	return string(raw)
}

func decodeObservation(raw string) (overseerObservation, bool) {
	var observation overseerObservation
	if err := json.Unmarshal([]byte(raw), &observation); err != nil || strings.TrimSpace(observation.Trigger) == "" {
		return overseerObservation{}, false
	}
	return observation, true
}

// checkpointObservedCompletion keeps post-hoc completion countersigning tied
// to the checkpoint that witnessed completion, rather than to the transient
// CompletionRequested flag. The agent loop may clear that flag before the
// primary reaches Succeeded, while the checkpoint observation is durable.
func checkpointObservedCompletion(standing *platformv1alpha1.AgentRun) bool {
	if standing == nil {
		return false
	}
	observation, ok := decodeObservation(standing.Annotations[orchestration.CheckpointReasonAnnotation])
	if !ok {
		return false
	}
	return observation.Trigger == "completion_requested" || observation.Phase == platformv1alpha1.AgentRunPhaseSucceeded
}

func overseerCheckpointMessage(primary *platformv1alpha1.AgentRun, sequence int64, observation overseerObservation, input *orchestration.PendingUserInput) string {
	payload := struct {
		CheckpointSeq    int64                           `json:"checkpoint_seq"`
		Trigger          string                          `json:"trigger"`
		SupervisedRun    string                          `json:"supervised_run"`
		Namespace        string                          `json:"namespace"`
		Phase            platformv1alpha1.AgentRunPhase  `json:"phase,omitempty"`
		ModeRevision     int64                           `json:"mode_revision,omitempty"`
		BudgetThreshold  int32                           `json:"budget_threshold_percent,omitempty"`
		Completion       bool                            `json:"completion_requested"`
		Authority        string                          `json:"authority"`
		InterventionsMax int32                           `json:"max_interventions"`
		InputRequest     *orchestration.PendingUserInput `json:"user_input_request,omitempty"`
	}{
		CheckpointSeq: sequence, Trigger: observation.Trigger, SupervisedRun: primary.Name,
		Namespace: primary.Namespace, Phase: primary.Status.Phase, ModeRevision: primary.Status.ModeRevision,
		BudgetThreshold: observation.BudgetThreshold, Completion: primary.Status.CompletionRequested,
		Authority: string(overseerAuthority(primary)), InterventionsMax: overseerMaxInterventions(primary),
		InputRequest: input,
	}
	raw, _ := json.Marshal(payload)
	return "OVERSEER CHECKPOINT\n\n" + string(raw) + "\n\n" +
		"Inspect the supervised trajectory and current state with get_supervised_activity, verify claims against the repository checkout, manage a pending user-input request when safe and authorized, submit exactly one submit_overseer_verdict, then finish."
}

//nolint:gocyclo // Verdict routing is intentionally centralized to keep cap and fail-open ordering explicit.
func (r *AgentRunOverseerReconciler) routeCompletedCheckpoint(ctx context.Context, primary, standing *platformv1alpha1.AgentRun) (handled, wait bool, err error) {
	sequence := annotationInt64(standing.Annotations[orchestration.CheckpointSeqAnnotation])
	if sequence <= 0 {
		return false, false, nil
	}
	statusHandled := int64(0)
	if primary.Status.OverseerSummary != nil {
		statusHandled = primary.Status.OverseerSummary.CheckpointsHandled
	}
	if statusHandled >= sequence {
		if annotationInt64(standing.Annotations[orchestration.CheckpointHandledAnnotation]) < sequence {
			return true, false, orchestration.MarkCheckpointHandled(ctx, r.Client, client.ObjectKeyFromObject(standing), sequence)
		}
		return false, false, nil
	}

	// Scheduling a checkpoint clears the verdict annotations and bumps
	// spec.wakeRequests while the standing run still reports the previous
	// episode's terminal phase. Until the run controller consumes that wake,
	// the terminal phase is stale: judging it now would fail supervision open
	// ("completed without a valid verdict") and permanently swallow the
	// episode's real verdict, because the checkpoint gets marked handled.
	if standing.Spec.WakeRequests > standing.Status.WakeRequestsHandled {
		return false, true, nil
	}

	switch standing.Status.Phase {
	case platformv1alpha1.AgentRunPhaseSucceeded, platformv1alpha1.AgentRunPhaseFailed, platformv1alpha1.AgentRunPhasePaused:
		// completed episode
	case platformv1alpha1.AgentRunPhaseCancelled:
		if err := r.recordOverseerVerdict(ctx, primary, sequence, "", "Overseer run was cancelled; supervision is fail-open.", overseerStateCancelled, false, false); err != nil {
			return false, false, err
		}
		return true, false, orchestration.MarkCheckpointHandled(ctx, r.Client, client.ObjectKeyFromObject(standing), sequence)
	default:
		return false, false, nil
	}

	verdict := strings.ToLower(strings.TrimSpace(standing.Annotations[platformv1alpha1.OverseerVerdictAnnotation]))
	summary := strings.TrimSpace(standing.Annotations[platformv1alpha1.OverseerSummaryAnnotation])
	guidance := strings.TrimSpace(standing.Annotations[platformv1alpha1.OverseerGuidanceAnnotation])
	if standing.Status.Phase != platformv1alpha1.AgentRunPhaseSucceeded || !validOverseerVerdict(verdict) || summary == "" || (overseerVerdictRequiresGuidance(verdict) && guidance == "") {
		reason := summary
		if reason == "" {
			reason = fmt.Sprintf("Overseer episode %d completed without a valid verdict; supervision is fail-open.", sequence)
		}
		if err := r.recordOverseerVerdict(ctx, primary, sequence, verdict, reason, overseerStateDegraded, false, false); err != nil {
			return false, false, err
		}
		r.eventf(primary, corev1.EventTypeWarning, "OverseerFailedOpen", "%s", reason)
		return true, false, orchestration.MarkCheckpointHandled(ctx, r.Client, client.ObjectKeyFromObject(standing), sequence)
	}

	authority := overseerAuthority(primary)
	interventions, rejections := int32(0), int32(0)
	if primary.Status.OverseerSummary != nil {
		interventions = primary.Status.OverseerSummary.InterventionsUsed
		rejections = primary.Status.OverseerSummary.CompletionRejectionsUsed
	}
	capReached := interventions >= overseerMaxInterventions(primary)
	intervened, rejected := false, false
	state := overseerStateActive

	switch verdict {
	case platformv1alpha1.OverseerVerdictAllClear:
		state = overseerStateActive
	case platformv1alpha1.OverseerVerdictSteer:
		if authority == platformv1alpha1.AgentRunOverseerAuthorityObserve {
			state = overseerStateObserving
		} else if capReached {
			state = overseerStateCapped
		} else {
			if err := r.deliverGuidance(ctx, primary, sequence, guidance, authority == platformv1alpha1.AgentRunOverseerAuthorityEnforce); err != nil {
				return false, false, err
			}
			intervened = true
		}
	case platformv1alpha1.OverseerVerdictRejectCompletion:
		if authority != platformv1alpha1.AgentRunOverseerAuthorityEnforce {
			state = overseerStateObserving
		} else if capReached || rejections >= maxCompletionRejections {
			state = overseerStateCapped
		} else if !primary.Status.CompletionRequested && !checkpointObservedCompletion(standing) {
			state = overseerStateObserving
		} else if !wakeableOverseerTarget(primary.Status.Phase) {
			if primary.Status.Phase == platformv1alpha1.AgentRunPhaseCancelled {
				state = overseerStateDegraded
			} else {
				return false, true, nil
			}
		} else {
			message := attributedOverseerGuidance(sequence, guidance)
			if err := orchestration.WakeAgentRunOnce(ctx, r.Client, r.StateStore, primary.Namespace, primary.Name, message, overseerDeliveryID(primary, sequence, "reject-completion")); err != nil {
				return false, false, err
			}
			intervened, rejected = true, true
			r.eventf(primary, corev1.EventTypeNormal, "OverseerRejectedCompletion", "Overseer rejected completion at checkpoint %d", sequence)
		}
	case platformv1alpha1.OverseerVerdictResolveInput:
		if authority != platformv1alpha1.AgentRunOverseerAuthorityEnforce {
			state = overseerStateObserving
		} else if capReached {
			state = overseerStateCapped
		} else {
			resolved, resolutionSummary, resolveErr := r.resolvePendingInput(ctx, primary, standing, sequence)
			if resolveErr != nil {
				return false, false, resolveErr
			}
			if resolved {
				intervened = true
				r.eventf(primary, corev1.EventTypeNormal, "OverseerResolvedInput", "Overseer resolved pending input at checkpoint %d", sequence)
			} else {
				state = overseerStateEscalated
				if strings.TrimSpace(resolutionSummary) != "" {
					summary = resolutionSummary
				}
				r.eventf(primary, corev1.EventTypeWarning, "OverseerInputEscalated", "%s", summary)
			}
		}
	case platformv1alpha1.OverseerVerdictEscalate:
		if authority != platformv1alpha1.AgentRunOverseerAuthorityEnforce {
			state = overseerStateObserving
		} else if capReached {
			state = overseerStateCapped
		} else {
			if err := r.escalate(ctx, primary, sequence, summary, guidance); err != nil {
				return false, false, err
			}
			intervened = true
			state = overseerStateEscalated
		}
	}

	if err := r.recordOverseerVerdict(ctx, primary, sequence, verdict, summary, state, intervened, rejected); err != nil {
		return false, false, err
	}
	if err := orchestration.MarkCheckpointHandled(ctx, r.Client, client.ObjectKeyFromObject(standing), sequence); err != nil {
		return false, false, err
	}
	return true, false, nil
}

func (r *AgentRunOverseerReconciler) deliverGuidance(ctx context.Context, primary *platformv1alpha1.AgentRun, sequence int64, guidance string, interrupt bool) error {
	message := attributedOverseerGuidance(sequence, guidance)
	deliveryID := overseerDeliveryID(primary, sequence, "steer")
	if wakeableOverseerTarget(primary.Status.Phase) {
		return orchestration.WakeAgentRunOnce(ctx, r.Client, r.StateStore, primary.Namespace, primary.Name, message, deliveryID)
	}
	if primary.Status.Phase == platformv1alpha1.AgentRunPhaseCancelled {
		return nil
	}
	if err := orchestration.DeliverImmediateMessageOnce(ctx, r.StateStore, primary.Namespace, primary.Name, message, overseerAttribution, deliveryID); err != nil {
		return err
	}
	if interrupt && primary.Status.Phase == platformv1alpha1.AgentRunPhaseRunning {
		session, err := r.StateStore.GetSessionByRun(ctx, primary.Name, primary.Namespace)
		if err != nil {
			return fmt.Errorf("getting primary session for overseer interrupt: %w", err)
		}
		if err := sessionclient.RequestInterrupt(ctx, r.StateStore, session.ID, overseerAttribution); err != nil {
			return fmt.Errorf("requesting overseer interrupt: %w", err)
		}
	}
	return nil
}

func overseerDeliveryID(primary *platformv1alpha1.AgentRun, sequence int64, action string) string {
	identity := "unknown"
	if primary != nil {
		identity = string(primary.UID)
		if identity == "" {
			identity = primary.Namespace + "/" + primary.Name
		}
	}
	return fmt.Sprintf("overseer:%s:%d:%s", identity, sequence, action)
}

func attributedOverseerGuidance(sequence int64, guidance string) string {
	return fmt.Sprintf("[Overseer guidance — checkpoint %d]\n\n%s", sequence, strings.TrimSpace(guidance))
}

type managedInputResolutionRecord struct {
	RequestID    string                       `json:"request_id"`
	InputType    string                       `json:"input_type,omitempty"`
	ActionID     string                       `json:"action_id,omitempty"`
	PlanApproval bool                         `json:"plan_approval,omitempty"`
	TargetMode   string                       `json:"target_mode,omitempty"`
	MCP          *mcppolicy.BreakGlassRequest `json:"mcp_request,omitempty"`
}

func (r *AgentRunOverseerReconciler) resolvePendingInput(ctx context.Context, primary, standing *platformv1alpha1.AgentRun, sequence int64) (bool, string, error) {
	var response platformv1alpha1.OverseerInputResponse
	if err := json.Unmarshal([]byte(standing.Annotations[platformv1alpha1.OverseerInputResponseAnnotation]), &response); err != nil {
		return false, "Overseer submitted an invalid input response; the request remains pending for the user.", nil
	}
	response.RequestID = strings.TrimSpace(response.RequestID)
	response.ActionID = strings.TrimSpace(response.ActionID)
	response.Response = strings.TrimSpace(response.Response)
	if response.RequestID == "" || (response.ActionID == "" && response.Response == "") {
		return false, "Overseer submitted an incomplete input response; the request remains pending for the user.", nil
	}

	session, err := r.StateStore.GetSessionByRun(ctx, primary.Name, primary.Namespace)
	if err != nil {
		return false, "", fmt.Errorf("getting primary session for overseer input response: %w", err)
	}
	resolver, ok := r.StateStore.(store.PendingInputResolver)
	if !ok {
		return false, "", fmt.Errorf("state store does not support atomic pending-input resolution")
	}

	deliveryID := overseerDeliveryID(primary, sequence, "resolve-input")
	request, err := pendingUserInputForRun(primary, session)
	if err != nil {
		return false, "", err
	}
	reservationRequestID := response.RequestID
	message := response.Response
	record := managedInputResolutionRecord{RequestID: response.RequestID, ActionID: response.ActionID}
	if request != nil && request.ID == response.RequestID {
		reservationRequestID = session.PendingRequestID
		record.InputType = request.Type
		if len(request.Actions) > 0 && response.ActionID == "" {
			return false, "The pending request offers explicit actions; the overseer must select one exact action ID.", nil
		}
		var action *orchestration.PendingUserAction
		if response.ActionID != "" {
			action = orchestration.FindPendingUserAction(request, response.ActionID)
			if action == nil {
				return false, fmt.Sprintf("The overseer selected unknown action %q; the current request remains pending.", response.ActionID), nil
			}
		}
		message = managedInputMessage(action, response.Response)
		record.PlanApproval = managedInputIsPlanApproval(request.Type, action)
		// Plan approval resumes the current mode. Ignore legacy or agent-authored
		// target modes so overseer-mediated approval follows the same contract as
		// direct user approval.
		if !record.PlanApproval {
			record.TargetMode = managedInputTargetMode(action)
		}
		if record.TargetMode != "" {
			if reason, err := r.validateManagedInputMode(ctx, record.TargetMode); err != nil || reason != "" {
				return false, reason, err
			}
		}
		pendingMCP, err := mcppolicy.PendingRequest(primary)
		if err != nil {
			return false, "", fmt.Errorf("decoding pending MCP break-glass request: %w", err)
		}
		if pendingMCP != nil {
			if strings.TrimSpace(pendingMCP.ID) == "" {
				return false, "This legacy MCP request has no immutable identity and remains pending for a human decision.", nil
			}
			if reason, err := r.validateMCPInput(ctx, primary, pendingMCP, response.ActionID); err != nil || reason != "" {
				return false, reason, err
			}
			record.MCP = pendingMCP
			message = managedMCPInputMessage(pendingMCP, response.ActionID, response.Response)
		}
	}
	if strings.TrimSpace(message) == "" {
		// On replay the original action label is already in the reserved message;
		// this placeholder is never inserted unless the request still matches.
		message = strings.TrimSpace(response.ActionID)
	}
	if message == "" {
		return false, "Overseer input response produced no continuation message; the request remains pending.", nil
	}

	metadata, _ := json.Marshal(map[string]any{
		"mode":                string(sessionclient.UserMessageModeEnqueue),
		"source":              overseerAttribution,
		"delivery_id":         deliveryID,
		"overseer_resolution": record,
	})
	reserved, accepted, err := resolver.ReservePendingInputResponse(ctx, session.ID, store.PendingInputResolution{
		RequestID: reservationRequestID,
		Phase:     "running",
		Role:      "user",
		Content:   message,
		Metadata:  metadata,
	})
	if err != nil {
		return false, "", err
	}
	if !accepted || reserved == nil {
		return false, "The supervised run's input request changed after this checkpoint; the stale overseer response was not applied.", nil
	}
	if err := decodeManagedInputResolution(reserved.Metadata, &record); err != nil {
		return false, "", err
	}
	if record.RequestID != response.RequestID {
		return false, "", fmt.Errorf("reserved overseer response belongs to a different input request")
	}
	freshBeforeEffects := &platformv1alpha1.AgentRun{}
	if err := r.Get(ctx, client.ObjectKeyFromObject(primary), freshBeforeEffects); err != nil {
		return false, "", err
	}
	if freshBeforeEffects.Status.Phase == platformv1alpha1.AgentRunPhaseCancelled {
		if err := resolver.CancelPendingInputResponse(ctx, session.ID, reserved.ID, deliveryID); err != nil {
			return false, "", err
		}
		return false, "The supervised run was cancelled; its reserved overseer response was discarded.", nil
	}

	if record.PlanApproval {
		if _, err := mode.RefreshCurrentSnapshot(ctx, r.Client, client.ObjectKeyFromObject(primary)); err != nil {
			return false, "", fmt.Errorf("refreshing current mode after plan approval: %w", err)
		}
	}
	if record.MCP != nil {
		if err := r.applyMCPInput(ctx, primary, standing, record.MCP, record.ActionID); err != nil {
			return false, "", err
		}
	}
	if record.TargetMode != "" {
		applied, reason, err := r.switchManagedInputMode(ctx, primary, record.TargetMode)
		if err != nil {
			return false, "", err
		}
		if !applied {
			return false, "", fmt.Errorf("reserved overseer input response could not apply mode %q: %s", record.TargetMode, reason)
		}
	}

	freshPrimary := &platformv1alpha1.AgentRun{}
	if err := r.Get(ctx, client.ObjectKeyFromObject(primary), freshPrimary); err != nil {
		return false, "", err
	}
	if freshPrimary.Status.Phase == platformv1alpha1.AgentRunPhaseCancelled {
		if record.MCP != nil && record.ActionID == "approve" {
			if err := r.removeCancelledMCPGrant(ctx, primary, record.MCP.ID); err != nil {
				return false, "", err
			}
		}
		if err := resolver.CancelPendingInputResponse(ctx, session.ID, reserved.ID, deliveryID); err != nil {
			return false, "", err
		}
		return false, "The supervised run was cancelled; its overseer response was discarded.", nil
	}
	if err := resolver.ReleasePendingInputResponse(ctx, session.ID, reserved.ID, deliveryID); err != nil {
		return false, "", err
	}
	if wakeableOverseerTarget(freshPrimary.Status.Phase) {
		if err := orchestration.WakeAgentRunOnce(ctx, r.Client, r.StateStore, primary.Namespace, primary.Name, reserved.Content, deliveryID); err != nil {
			return false, "", err
		}
	}

	latestSession, err := r.StateStore.GetSession(ctx, session.ID)
	if err != nil {
		return false, "", fmt.Errorf("refreshing primary session after overseer input response: %w", err)
	}
	if strings.TrimSpace(latestSession.PendingRequestID) == "" && !isTerminalPhase(freshPrimary.Status.Phase) && freshPrimary.Status.Phase != platformv1alpha1.AgentRunPhasePaused {
		if err := retryAgentRunStatusPatch(ctx, r.Client, client.ObjectKeyFromObject(primary), func(fresh *platformv1alpha1.AgentRun) {
			fresh.Status.Phase = platformv1alpha1.AgentRunPhaseRunning
			fresh.Status.Queue = &platformv1alpha1.AgentRunQueueStatus{State: "Running"}
		}); err != nil {
			return false, "", err
		}
	}

	detail, _ := json.Marshal(map[string]string{
		"request_id": record.RequestID,
		"input_type": record.InputType,
		"action_id":  record.ActionID,
		"overseer":   standing.Name,
	})
	_, _ = r.StateStore.WriteActivityEvent(ctx, session.ID, "overseer_input_resolved", "Overseer resolved a pending input request", detail)
	return true, "", nil
}

func decodeManagedInputResolution(metadata json.RawMessage, out *managedInputResolutionRecord) error {
	var envelope struct {
		Resolution json.RawMessage `json:"overseer_resolution"`
	}
	if err := json.Unmarshal(metadata, &envelope); err != nil || len(envelope.Resolution) == 0 {
		return fmt.Errorf("reserved overseer response has invalid resolution metadata")
	}
	if err := json.Unmarshal(envelope.Resolution, out); err != nil {
		return fmt.Errorf("decoding reserved overseer response metadata: %w", err)
	}
	return nil
}

func managedInputIsPlanApproval(inputType string, action *orchestration.PendingUserAction) bool {
	if action == nil {
		return false
	}
	switch action.ID {
	case "reject", "request_changes":
		return false
	case "accept_plan", "accept_build", "accept_build_auto":
		return true
	default:
		return strings.EqualFold(strings.TrimSpace(inputType), string(platformv1alpha1.UserInputPlanReview))
	}
}

func managedInputTargetMode(action *orchestration.PendingUserAction) string {
	if action == nil {
		return ""
	}
	return strings.TrimSpace(action.Mode)
}

func managedInputMessage(action *orchestration.PendingUserAction, response string) string {
	response = strings.TrimSpace(response)
	if action == nil {
		return response
	}
	var message string
	switch action.ID {
	case "accept_plan", "accept_build", "accept_build_auto":
		message = "Plan approved. Continue with implementation."
	case "approve":
		message = "approve"
	case "reject":
		message = "Rejected. Revise the approach and continue."
	case "request_changes":
		message = "Please revise and continue."
	default:
		message = strings.TrimSpace(action.Label)
		if message == "" {
			message = strings.TrimSpace(action.ID)
		}
	}
	if response == "" {
		return message
	}
	switch action.ID {
	case "accept_plan", "accept_build", "accept_build_auto":
		return message + " Notes: " + response
	case "request_changes", "reject":
		return message + " Feedback: " + response
	default:
		return message + ": " + response
	}
}

func (r *AgentRunOverseerReconciler) validateManagedInputMode(ctx context.Context, targetMode string) (string, error) {
	target := &platformv1alpha1.ModeTemplate{}
	if err := r.Get(ctx, client.ObjectKey{Name: strings.TrimSpace(targetMode)}, target); err != nil {
		if apierrors.IsNotFound(err) {
			return fmt.Sprintf("Mode %q required by the selected action is unavailable; the input remains pending.", targetMode), nil
		}
		return "", err
	}
	return "", nil
}

func (r *AgentRunOverseerReconciler) switchManagedInputMode(ctx context.Context, primary *platformv1alpha1.AgentRun, targetMode string) (bool, string, error) {
	target := &platformv1alpha1.ModeTemplate{}
	if err := r.Get(ctx, client.ObjectKey{Name: strings.TrimSpace(targetMode)}, target); err != nil {
		if apierrors.IsNotFound(err) {
			return false, fmt.Sprintf("Mode %q required by the selected action is unavailable; the input remains pending.", targetMode), nil
		}
		return false, "", err
	}
	fresh := &platformv1alpha1.AgentRun{}
	if err := r.Get(ctx, client.ObjectKeyFromObject(primary), fresh); err != nil {
		return false, "", err
	}
	if fresh.Status.Phase == platformv1alpha1.AgentRunPhaseCancelled {
		return false, "The supervised run was cancelled before the mode transition.", nil
	}
	evaluation := mode.Evaluate(fresh.Status.ModeSnapshot, target.Spec.DeepCopy(), mode.EvaluateOpts{
		Run: fresh, ActorRole: mode.RoleMember, Source: overseerAttribution,
	})
	switch evaluation.Result {
	case mode.ResultNoop:
		return true, "", nil
	case mode.ResultDenied:
		return false, fmt.Sprintf("The overseer could not switch to mode %q: %s", targetMode, evaluation.Reason), nil
	case mode.ResultApplied:
		if _, err := mode.ExecuteSwitch(ctx, r.Client, client.ObjectKeyFromObject(fresh), evaluation, overseerAttribution, "input-resolution"); err != nil {
			return false, "", err
		}
		return true, "", nil
	default:
		return false, fmt.Sprintf("The overseer could not switch to mode %q.", targetMode), nil
	}
}

func (r *AgentRunOverseerReconciler) validateMCPInput(ctx context.Context, primary *platformv1alpha1.AgentRun, request *mcppolicy.BreakGlassRequest, actionID string) (string, error) {
	if actionID != "approve" && actionID != "reject" {
		return "MCP break-glass input must be explicitly approved or rejected; it remains pending for the user.", nil
	}
	if strings.TrimSpace(request.ID) == "" {
		return "This legacy MCP request has no immutable identity and remains pending for a human decision.", nil
	}
	if actionID != "approve" {
		return "", nil
	}
	var policy *platformv1alpha1.MCPPolicy
	if primary.Spec.MCPPolicyRef != nil && strings.TrimSpace(primary.Spec.MCPPolicyRef.Name) != "" {
		policy = &platformv1alpha1.MCPPolicy{}
		if err := r.Get(ctx, client.ObjectKey{Namespace: primary.Namespace, Name: primary.Spec.MCPPolicyRef.Name}, policy); err != nil {
			return "", err
		}
	}
	cfg := mcppolicy.NewEvaluator(primary, policy).BreakGlass()
	if !cfg.Enabled {
		return "MCP break-glass is no longer enabled; the approval was not applied.", nil
	}
	if cfg.RequireAuditReason && strings.TrimSpace(request.Reason) == "" {
		return "MCP break-glass approval requires an audit reason; the request remains pending.", nil
	}
	if cfg.AdminMediated {
		return "MCP break-glass policy requires a human administrator; the overseer did not bypass that boundary.", nil
	}
	return "", nil
}

func (r *AgentRunOverseerReconciler) applyMCPInput(ctx context.Context, primary, standing *platformv1alpha1.AgentRun, request *mcppolicy.BreakGlassRequest, actionID string) error {
	freshPrimary := &platformv1alpha1.AgentRun{}
	if err := r.Get(ctx, client.ObjectKeyFromObject(primary), freshPrimary); err != nil {
		return err
	}
	if reason, err := r.validateMCPInput(ctx, freshPrimary, request, actionID); err != nil {
		return err
	} else if reason != "" {
		return fmt.Errorf("MCP break-glass policy changed before the reserved decision was applied: %s", reason)
	}
	applied := false
	mismatched := false
	conflictingDecision := false
	expectedDecision := "denied"
	if actionID == "approve" {
		expectedDecision = "approved"
	}
	if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		fresh := &platformv1alpha1.AgentRun{}
		if err := r.Get(ctx, client.ObjectKeyFromObject(primary), fresh); err != nil {
			return err
		}
		if fresh.Status.Phase == platformv1alpha1.AgentRunPhaseCancelled {
			return fmt.Errorf("supervised run was cancelled before MCP decision")
		}
		if reason, err := r.validateMCPInput(ctx, fresh, request, actionID); err != nil {
			return err
		} else if reason != "" {
			return fmt.Errorf("MCP break-glass policy changed before decision: %s", reason)
		}
		current, err := mcppolicy.PendingRequest(fresh)
		if err != nil {
			return err
		}
		grants, err := mcppolicy.GrantedGrants(fresh)
		if err != nil {
			return err
		}
		decisions, err := mcppolicy.BreakGlassDecisions(fresh)
		if err != nil {
			return err
		}
		if decision := mcppolicy.FindBreakGlassDecision(decisions, request.ID); decision != nil {
			if decision.Decision == expectedDecision {
				applied = true
			} else {
				conflictingDecision = true
			}
			return nil
		}
		if current == nil {
			return nil
		}
		if !mcppolicy.SameBreakGlassRequest(current, request) {
			mismatched = true
			return nil
		}
		patch := client.MergeFrom(fresh.DeepCopy())
		if fresh.Annotations == nil {
			fresh.Annotations = map[string]string{}
		}
		decidedAt := r.now().UTC().Format(time.RFC3339)
		decidedBy := overseerAttribution + ":" + standing.Name
		if actionID == "approve" {
			grant := mcppolicy.BreakGlassGrant{
				RequestID: request.ID,
				Server:    request.Server, Tool: request.Tool, Reason: request.Reason,
				RequestedAt: request.RequestedAt, RequestedBy: request.RequestedBy,
				ApprovedAt: decidedAt, ApprovedBy: decidedBy,
			}
			if err := mcppolicy.SetGrantedGrants(fresh.Annotations, mcppolicy.UpsertGrant(grants, grant)); err != nil {
				return err
			}
		}
		decision := mcppolicy.BreakGlassDecision{RequestID: request.ID, Decision: expectedDecision, DecidedAt: decidedAt, DecidedBy: decidedBy}
		if err := mcppolicy.SetBreakGlassDecisions(fresh.Annotations, mcppolicy.UpsertBreakGlassDecision(decisions, decision)); err != nil {
			return err
		}
		mcppolicy.ClearPendingRequest(fresh.Annotations)
		if err := r.Patch(ctx, fresh, patch); err != nil {
			return err
		}
		applied = true
		return nil
	}); err != nil {
		return err
	}
	if conflictingDecision {
		return fmt.Errorf("MCP break-glass request already has a conflicting durable decision")
	}
	if mismatched {
		return fmt.Errorf("MCP break-glass request changed before the reserved overseer decision was applied")
	}
	if !applied {
		return fmt.Errorf("reserved MCP break-glass decision could not be verified as applied")
	}
	return nil
}

func (r *AgentRunOverseerReconciler) removeCancelledMCPGrant(ctx context.Context, primary *platformv1alpha1.AgentRun, requestID string) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		fresh := &platformv1alpha1.AgentRun{}
		if err := r.Get(ctx, client.ObjectKeyFromObject(primary), fresh); err != nil {
			return err
		}
		grants, err := mcppolicy.GrantedGrants(fresh)
		if err != nil {
			return err
		}
		filtered := mcppolicy.RemoveBreakGlassGrantByRequestID(grants, requestID)
		if len(filtered) == len(grants) {
			return nil
		}
		patch := client.MergeFrom(fresh.DeepCopy())
		if fresh.Annotations == nil {
			fresh.Annotations = map[string]string{}
		}
		if err := mcppolicy.SetGrantedGrants(fresh.Annotations, filtered); err != nil {
			return err
		}
		return r.Patch(ctx, fresh, patch)
	})
}

func managedMCPInputMessage(request *mcppolicy.BreakGlassRequest, actionID, note string) string {
	target := fmt.Sprintf("server %q", request.Server)
	if strings.TrimSpace(request.Tool) != "" {
		target = fmt.Sprintf("server %q tool %q", request.Server, request.Tool)
	}
	note = strings.TrimSpace(note)
	if actionID == "approve" {
		message := fmt.Sprintf("MCP break-glass approved for %s. Continue.", target)
		if note != "" {
			message += " Approval note: " + note
		}
		return message
	}
	message := fmt.Sprintf("MCP break-glass denied for %s. Continue without that access.", target)
	if note != "" {
		message += " Feedback: " + note
	}
	return message
}

func (r *AgentRunOverseerReconciler) escalate(ctx context.Context, primary *platformv1alpha1.AgentRun, sequence int64, summary, guidance string) error {
	session, err := r.StateStore.GetSessionByRun(ctx, primary.Name, primary.Namespace)
	if err != nil {
		return fmt.Errorf("getting primary session for overseer escalation: %w", err)
	}
	reason := fmt.Sprintf("Overseer escalation at checkpoint %d: %s\n\n%s", sequence, summary, guidance)
	if err := r.StateStore.SetPendingQuestion(ctx, session.ID, "blocked", reason, "question"); err != nil {
		return fmt.Errorf("recording overseer escalation: %w", err)
	}
	if primary.Status.Phase == platformv1alpha1.AgentRunPhaseRunning {
		if err := sessionclient.RequestInterrupt(ctx, r.StateStore, session.ID, overseerAttribution); err != nil {
			return fmt.Errorf("interrupting escalated run: %w", err)
		}
	}
	if !isTerminalPhase(primary.Status.Phase) && primary.Status.Phase != platformv1alpha1.AgentRunPhasePaused {
		if err := retryAgentRunStatusPatch(ctx, r.Client, client.ObjectKeyFromObject(primary), func(fresh *platformv1alpha1.AgentRun) {
			fresh.Status.Phase = platformv1alpha1.AgentRunPhaseBlocked
			fresh.Status.Queue = &platformv1alpha1.AgentRunQueueStatus{State: "Blocked", BlockedReason: truncateStatusText(reason)}
		}); err != nil {
			return err
		}
	}
	r.eventf(primary, corev1.EventTypeWarning, "OverseerEscalated", "%s", summary)
	return nil
}

func (r *AgentRunOverseerReconciler) recordOverseerVerdict(ctx context.Context, primary *platformv1alpha1.AgentRun, sequence int64, verdict, summary, state string, intervention, rejection bool) error {
	now := metav1.NewTime(r.now())
	return retryAgentRunStatusPatch(ctx, r.Client, client.ObjectKeyFromObject(primary), func(fresh *platformv1alpha1.AgentRun) {
		if fresh.Status.OverseerSummary == nil {
			fresh.Status.OverseerSummary = &platformv1alpha1.AgentRunOverseerStatus{}
		}
		out := fresh.Status.OverseerSummary
		if out.CheckpointsHandled >= sequence {
			return
		}
		out.RunName = orchestration.StandingRunName(fresh.Name, orchestration.StandingRunRoleOverseer)
		out.State = state
		out.CheckpointsHandled = sequence
		if intervention {
			out.InterventionsUsed++
		}
		if rejection {
			out.CompletionRejectionsUsed++
		}
		out.LastVerdict = verdict
		out.LastSummary = truncateStatusText(summary)
		out.LastVerdictTime = &now
	})
}

func (r *AgentRunOverseerReconciler) maybeScheduleCheckpoint(ctx context.Context, primary, standing *platformv1alpha1.AgentRun, now time.Time) (ctrl.Result, error) {
	if standing.Status.Phase == platformv1alpha1.AgentRunPhaseCancelled {
		if err := r.updateOverseerSummary(ctx, primary, func(summary *platformv1alpha1.AgentRunOverseerStatus) {
			summary.State = overseerStateCancelled
			summary.LastSummary = "The standing overseer run was cancelled; the primary continues fail-open."
		}); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}
	if !wakeableOverseerTarget(standing.Status.Phase) {
		return ctrl.Result{RequeueAfter: r.requeueForCadence(primary, standing, now)}, nil
	}
	sequence := annotationInt64(standing.Annotations[orchestration.CheckpointSeqAnnotation])
	handled := annotationInt64(standing.Annotations[orchestration.CheckpointHandledAnnotation])
	if primary.Status.OverseerSummary != nil && primary.Status.OverseerSummary.CheckpointsHandled > handled {
		handled = primary.Status.OverseerSummary.CheckpointsHandled
	}
	if sequence <= 0 || handled < sequence {
		return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
	}

	previous, ok := decodeObservation(standing.Annotations[orchestration.CheckpointReasonAnnotation])
	trigger := ""
	primarySession, err := r.StateStore.GetSessionByRun(ctx, primary.Name, primary.Namespace)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("getting primary session for overseer checkpoint: %w", err)
	}
	current, currentInput, err := observationForSession(primary, "", primarySession)
	if err != nil {
		return ctrl.Result{}, err
	}
	switch {
	case !ok:
		trigger = "state_repair"
	case current.InputRequestID != "" && current.InputRequestID != previous.InputRequestID:
		trigger = "input_requested"
	case primary.Status.CompletionRequested && current.CompletionAttempt != previous.CompletionAttempt:
		trigger = "completion_requested"
	case current.Phase != previous.Phase:
		trigger = "phase_transition"
	case current.ModeRevision != previous.ModeRevision:
		trigger = "mode_transition"
	case current.BudgetThreshold > previous.BudgetThreshold:
		trigger = fmt.Sprintf("budget_%d_percent", current.BudgetThreshold)
	case primary.Status.Phase == platformv1alpha1.AgentRunPhaseRunning && checkpointDue(standing, now, overseerInterval(primary)):
		trigger = "cadence"
	}
	if trigger == "" {
		return ctrl.Result{RequeueAfter: r.requeueForCadence(primary, standing, now)}, nil
	}
	current.Trigger = trigger
	nextSequence := sequence + 1
	message := overseerCheckpointMessage(primary, nextSequence, current, currentInput)
	scheduled, err := orchestration.Checkpoint(ctx, r.Client, r.StateStore, client.ObjectKeyFromObject(standing), nextSequence, encodeObservation(current), message)
	if err != nil {
		if orchestration.IsCheckpointCancelled(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	if scheduled {
		if err := r.updateOverseerSummary(ctx, primary, func(summary *platformv1alpha1.AgentRunOverseerStatus) {
			summary.State = overseerStateChecking
		}); err != nil {
			return ctrl.Result{}, err
		}
		r.eventf(primary, corev1.EventTypeNormal, "OverseerCheckpoint", "Scheduled overseer checkpoint %d (%s)", nextSequence, trigger)
	}
	return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
}

func (r *AgentRunOverseerReconciler) requeueForCadence(primary, standing *platformv1alpha1.AgentRun, now time.Time) time.Duration {
	if primary == nil || primary.Status.Phase != platformv1alpha1.AgentRunPhaseRunning {
		return 0
	}
	interval := overseerInterval(primary)
	last, err := time.Parse(time.RFC3339Nano, standing.Annotations[orchestration.CheckpointTimeAnnotation])
	if err != nil {
		return time.Second
	}
	remaining := interval - now.Sub(last)
	if remaining <= 0 {
		return time.Second
	}
	return remaining
}

func checkpointDue(standing *platformv1alpha1.AgentRun, now time.Time, interval time.Duration) bool {
	last, err := time.Parse(time.RFC3339Nano, standing.Annotations[orchestration.CheckpointTimeAnnotation])
	return err != nil || !now.Before(last.Add(interval))
}

func runBudgetThreshold(run *platformv1alpha1.AgentRun) int32 {
	if run == nil || run.Spec.Limits == nil || run.Status.Metrics == nil {
		return 0
	}
	limit, err := strconv.ParseFloat(strings.TrimSpace(run.Spec.Limits.MaxCostUsd), 64)
	if err != nil || limit <= 0 {
		return 0
	}
	spent, err := strconv.ParseFloat(strings.TrimSpace(run.Status.Metrics.CostUsd), 64)
	if err != nil || spent < 0 {
		return 0
	}
	ratio := spent / limit
	if ratio >= .9 {
		return 90
	}
	if ratio >= .5 {
		return 50
	}
	return 0
}

func overseerAuthority(run *platformv1alpha1.AgentRun) platformv1alpha1.AgentRunOverseerAuthority {
	if run != nil && run.Spec.Overseer != nil {
		switch run.Spec.Overseer.Authority {
		case platformv1alpha1.AgentRunOverseerAuthorityObserve, platformv1alpha1.AgentRunOverseerAuthorityEnforce:
			return run.Spec.Overseer.Authority
		}
	}
	return platformv1alpha1.AgentRunOverseerAuthorityAdvise
}

func overseerMaxInterventions(run *platformv1alpha1.AgentRun) int32 {
	if run == nil || run.Spec.Overseer == nil {
		return 0
	}
	value := run.Spec.Overseer.MaxInterventions
	if value < 0 {
		return 0
	}
	if value > platformv1alpha1.AgentRunOverseerMaxInterventions {
		return platformv1alpha1.AgentRunOverseerMaxInterventions
	}
	return value
}

func overseerInterval(run *platformv1alpha1.AgentRun) time.Duration {
	if run == nil || run.Spec.Overseer == nil {
		return defaultOverseerInterval
	}
	minutes := run.Spec.Overseer.IntervalMinutes
	if minutes < 1 || minutes > platformv1alpha1.AgentRunOverseerMaxIntervalMinutes {
		return defaultOverseerInterval
	}
	return time.Duration(minutes) * time.Minute
}

func wakeableOverseerTarget(phase platformv1alpha1.AgentRunPhase) bool {
	switch phase {
	case platformv1alpha1.AgentRunPhaseSucceeded, platformv1alpha1.AgentRunPhaseFailed, platformv1alpha1.AgentRunPhasePaused:
		return true
	default:
		return false
	}
}

func validOverseerVerdict(verdict string) bool {
	switch verdict {
	case platformv1alpha1.OverseerVerdictAllClear, platformv1alpha1.OverseerVerdictSteer,
		platformv1alpha1.OverseerVerdictRejectCompletion, platformv1alpha1.OverseerVerdictResolveInput,
		platformv1alpha1.OverseerVerdictEscalate:
		return true
	default:
		return false
	}
}

func overseerVerdictRequiresGuidance(verdict string) bool {
	switch verdict {
	case platformv1alpha1.OverseerVerdictSteer, platformv1alpha1.OverseerVerdictRejectCompletion, platformv1alpha1.OverseerVerdictEscalate:
		return true
	default:
		return false
	}
}

func annotationInt64(raw string) int64 {
	value, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || value < 0 {
		return 0
	}
	return value
}

func truncateStatusText(value string) string {
	value = strings.TrimSpace(value)
	if len(value) <= 4000 {
		return value
	}
	value = value[:4000]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}

func (r *AgentRunOverseerReconciler) updateOverseerSummary(ctx context.Context, primary *platformv1alpha1.AgentRun, mutate func(*platformv1alpha1.AgentRunOverseerStatus)) error {
	key := client.ObjectKeyFromObject(primary)
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		fresh := &platformv1alpha1.AgentRun{}
		if err := r.Get(ctx, key, fresh); err != nil {
			return err
		}
		original := fresh.DeepCopy()
		before := fresh.Status.OverseerSummary.DeepCopy()
		if fresh.Status.OverseerSummary == nil {
			fresh.Status.OverseerSummary = &platformv1alpha1.AgentRunOverseerStatus{}
		}
		mutate(fresh.Status.OverseerSummary)
		if reflect.DeepEqual(before, fresh.Status.OverseerSummary) {
			return nil
		}
		return r.Status().Patch(ctx, fresh, client.MergeFrom(original))
	})
}

func (r *AgentRunOverseerReconciler) eventf(run *platformv1alpha1.AgentRun, eventType, reason, format string, args ...any) {
	if r.Recorder != nil {
		r.Recorder.Eventf(run, eventType, reason, format, args...)
	}
}

func (r *AgentRunOverseerReconciler) now() time.Time {
	if r.Now != nil {
		return r.Now().UTC()
	}
	return time.Now().UTC()
}

func overseerPredicate() predicate.Predicate {
	relevant := func(obj client.Object) bool {
		run, ok := obj.(*platformv1alpha1.AgentRun)
		return ok && (run.Spec.Overseer != nil || run.Status.OverseerSummary != nil || strings.TrimSpace(run.Annotations[platformv1alpha1.OverseerDetachingAnnotation]) != "" || strings.TrimSpace(run.Labels[orchestration.StandingRunRoleLabel]) != "")
	}
	return predicate.Funcs{
		CreateFunc:  func(e event.CreateEvent) bool { return relevant(e.Object) },
		UpdateFunc:  func(e event.UpdateEvent) bool { return relevant(e.ObjectOld) || relevant(e.ObjectNew) },
		DeleteFunc:  func(event.DeleteEvent) bool { return false },
		GenericFunc: func(e event.GenericEvent) bool { return relevant(e.Object) },
	}
}

func (r *AgentRunOverseerReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&platformv1alpha1.AgentRun{}).
		Named("agentrun-overseer").
		WithEventFilter(overseerPredicate()).
		WithOptions(controller.Options{MaxConcurrentReconciles: 1}).
		Complete(r)
}
