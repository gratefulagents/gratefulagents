package orchestration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/store"
	"github.com/gratefulagents/gratefulagents/internal/store/sessionclient"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// CheckpointCancelledError reports that a cancelled standing run cannot be woken.
type CheckpointCancelledError struct {
	Key client.ObjectKey
}

func (e *CheckpointCancelledError) Error() string {
	return fmt.Sprintf("standing AgentRun %s/%s is cancelled", e.Key.Namespace, e.Key.Name)
}

// IsCheckpointCancelled reports whether err is the nonfatal cancelled-run result.
func IsCheckpointCancelled(err error) bool {
	var target *CheckpointCancelledError
	return errors.As(err, &target)
}

// Checkpoint schedules one wake for a terminal standing run and appends its context.
func Checkpoint(ctx context.Context, k8sClient client.Client, stateStore store.StateStore, key client.ObjectKey, sequence int64, reason, message string) (bool, error) {
	reason = strings.TrimSpace(reason)
	message = strings.TrimSpace(message)
	if k8sClient == nil || stateStore == nil {
		return false, fmt.Errorf("k8s client and state store are required")
	}
	if key.Namespace == "" || key.Name == "" || sequence <= 0 || reason == "" || message == "" {
		return false, fmt.Errorf("run key, positive sequence, reason, and message are required")
	}

	run := &platformv1alpha1.AgentRun{}
	if err := k8sClient.Get(ctx, key, run); err != nil {
		return false, fmt.Errorf("getting standing AgentRun: %w", err)
	}
	if current, err := strconv.ParseInt(run.Annotations[CheckpointSeqAnnotation], 10, 64); err == nil && current >= sequence {
		return false, nil
	}
	switch run.Status.Phase {
	case platformv1alpha1.AgentRunPhaseCancelled:
		return false, &CheckpointCancelledError{Key: key}
	case platformv1alpha1.AgentRunPhaseSucceeded, platformv1alpha1.AgentRunPhaseFailed, platformv1alpha1.AgentRunPhasePaused:
	default:
		return false, fmt.Errorf("standing AgentRun %s/%s is not terminal: %s", key.Namespace, key.Name, run.Status.Phase)
	}

	session, err := stateStore.GetSessionByRun(ctx, key.Name, key.Namespace)
	if err != nil {
		return false, fmt.Errorf("getting session for standing AgentRun: %w", err)
	}
	messages, err := stateStore.GetMessages(ctx, session.ID)
	if err != nil {
		return false, fmt.Errorf("checking checkpoint delivery: %w", err)
	}
	if !hasCheckpointMessage(messages, sequence) {
		metadata, err := json.Marshal(map[string]any{
			"mode":           string(sessionclient.UserMessageModeEnqueue),
			"source":         "supervision-checkpoint",
			"checkpoint_seq": sequence,
		})
		if err != nil {
			return false, fmt.Errorf("encoding checkpoint metadata: %w", err)
		}
		if _, err := stateStore.AppendMessage(ctx, session.ID, "user", message, metadata); err != nil {
			return false, fmt.Errorf("appending checkpoint message: %w", err)
		}
	}

	sequenceText := strconv.FormatInt(sequence, 10)
	scheduled := true
	if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		fresh := &platformv1alpha1.AgentRun{}
		if err := k8sClient.Get(ctx, key, fresh); err != nil {
			return err
		}
		if current, err := strconv.ParseInt(fresh.Annotations[CheckpointSeqAnnotation], 10, 64); err == nil && current >= sequence {
			scheduled = false
			return nil
		}
		if fresh.Annotations == nil {
			fresh.Annotations = map[string]string{}
		}
		delete(fresh.Annotations, platformv1alpha1.OverseerVerdictAnnotation)
		delete(fresh.Annotations, platformv1alpha1.OverseerGuidanceAnnotation)
		delete(fresh.Annotations, platformv1alpha1.OverseerSummaryAnnotation)
		delete(fresh.Annotations, platformv1alpha1.OverseerInputResponseAnnotation)
		fresh.Annotations[CheckpointSeqAnnotation] = sequenceText
		fresh.Annotations[CheckpointReasonAnnotation] = reason
		fresh.Annotations[CheckpointTimeAnnotation] = time.Now().UTC().Format(time.RFC3339Nano)
		fresh.Spec.WakeRequests++
		return k8sClient.Update(ctx, fresh)
	}); err != nil {
		return false, fmt.Errorf("scheduling checkpoint for AgentRun %s/%s: %w", key.Namespace, key.Name, err)
	}
	return scheduled, nil
}

func hasCheckpointMessage(messages []store.Message, sequence int64) bool {
	for _, message := range messages {
		if message.Role != "user" || len(message.Metadata) == 0 {
			continue
		}
		var metadata struct {
			CheckpointSeq int64 `json:"checkpoint_seq"`
		}
		if json.Unmarshal(message.Metadata, &metadata) == nil && metadata.CheckpointSeq == sequence {
			return true
		}
	}
	return false
}

// DeliverImmediateMessageOnce appends an attributed immediate message at most
// once for a durable delivery ID. The ID is persisted in message metadata, so
// reconciliation can recover after a later Kubernetes status-patch failure.
func DeliverImmediateMessageOnce(ctx context.Context, stateStore store.StateStore, runNamespace, runName, message, attributedSource, deliveryID string) error {
	return deliverMessage(ctx, stateStore, runNamespace, runName, message, attributedSource, deliveryID, sessionclient.UserMessageModeImmediate)
}

// DeliverImmediateMessage appends an immediately delivered attributed user message.
func DeliverImmediateMessage(ctx context.Context, stateStore store.StateStore, runNamespace, runName, message, attributedSource string) error {
	return deliverMessage(ctx, stateStore, runNamespace, runName, message, attributedSource, "", sessionclient.UserMessageModeImmediate)
}

func deliverMessage(ctx context.Context, stateStore store.StateStore, runNamespace, runName, message, attributedSource, deliveryID string, mode sessionclient.UserMessageMode) error {
	runNamespace = strings.TrimSpace(runNamespace)
	runName = strings.TrimSpace(runName)
	message = strings.TrimSpace(message)
	attributedSource = strings.TrimSpace(attributedSource)
	if stateStore == nil || runNamespace == "" || runName == "" || message == "" || attributedSource == "" {
		return fmt.Errorf("state store, run namespace/name, message, and attributed source are required")
	}
	session, err := stateStore.GetSessionByRun(ctx, runName, runNamespace)
	if err != nil {
		return fmt.Errorf("getting session for AgentRun %s/%s: %w", runNamespace, runName, err)
	}
	if deliveryID != "" {
		messages, err := stateStore.GetMessages(ctx, session.ID)
		if err != nil {
			return fmt.Errorf("checking message delivery for AgentRun %s/%s: %w", runNamespace, runName, err)
		}
		if hasDeliveryMessage(messages, deliveryID) {
			return nil
		}
	}
	metadata := map[string]any{"mode": string(mode), "source": attributedSource}
	if deliveryID != "" {
		metadata["delivery_id"] = deliveryID
	}
	encoded, err := json.Marshal(metadata)
	if err != nil {
		return fmt.Errorf("encoding attributed message metadata: %w", err)
	}
	if _, err := stateStore.AppendMessage(ctx, session.ID, "user", message, encoded); err != nil {
		return fmt.Errorf("appending attributed message for AgentRun %s/%s: %w", runNamespace, runName, err)
	}
	return nil
}

func hasDeliveryMessage(messages []store.Message, deliveryID string) bool {
	for _, message := range messages {
		if message.Role != "user" || len(message.Metadata) == 0 {
			continue
		}
		var metadata struct {
			DeliveryID string `json:"delivery_id"`
		}
		if json.Unmarshal(message.Metadata, &metadata) == nil && metadata.DeliveryID == deliveryID {
			return true
		}
	}
	return false
}

// WakeAgentRunOnce appends wake context and advances the wake counter at most
// once for deliveryID. Message metadata and the run annotation repair the two
// sides independently after a partial failure.
func WakeAgentRunOnce(ctx context.Context, k8sClient client.Client, stateStore store.StateStore, runNamespace, runName, contextMessage, deliveryID string) error {
	return wakeAgentRunOnceWithOptions(ctx, k8sClient, stateStore, runNamespace, runName, contextMessage, deliveryID, func(*platformv1alpha1.AgentRun) bool { return true })
}

// WakeOrNudgeAgentRunOnce idempotently enqueues contextMessage into the run's
// durable session and increments spec.wakeRequests only when the freshest run
// phase is one the run controller's wake handler can consume (Succeeded,
// Failed, Paused, or Cancelled). A Running run parked on durable idle input
// has a live runner consuming the session queue, so the message alone resumes
// it; bumping wakeRequests there would leave a wake the controller only
// consumes at the next terminal or paused transition, spuriously resurrecting
// the run and suppressing later nudges in the interim. Deciding on the fresh
// read inside the patch loop avoids acting on a stale phase observed earlier
// in the caller's reconcile.
func WakeOrNudgeAgentRunOnce(ctx context.Context, k8sClient client.Client, stateStore store.StateStore, runNamespace, runName, contextMessage, deliveryID string) error {
	return wakeAgentRunOnceWithOptions(ctx, k8sClient, stateStore, runNamespace, runName, contextMessage, deliveryID, func(fresh *platformv1alpha1.AgentRun) bool {
		switch fresh.Status.Phase {
		case platformv1alpha1.AgentRunPhaseSucceeded, platformv1alpha1.AgentRunPhaseFailed,
			platformv1alpha1.AgentRunPhasePaused, platformv1alpha1.AgentRunPhaseCancelled:
			return true
		default:
			return false
		}
	})
}

func wakeAgentRunOnceWithOptions(ctx context.Context, k8sClient client.Client, stateStore store.StateStore, runNamespace, runName, contextMessage, deliveryID string, provisionRunner func(*platformv1alpha1.AgentRun) bool) error {
	deliveryID = strings.TrimSpace(deliveryID)
	if deliveryID == "" {
		return fmt.Errorf("delivery ID is required")
	}
	key := client.ObjectKey{Namespace: strings.TrimSpace(runNamespace), Name: strings.TrimSpace(runName)}
	if key.Namespace == "" || key.Name == "" || k8sClient == nil || stateStore == nil || strings.TrimSpace(contextMessage) == "" {
		return fmt.Errorf("k8s client, state store, run namespace/name, and context message are required")
	}
	run := &platformv1alpha1.AgentRun{}
	if err := k8sClient.Get(ctx, key, run); err != nil {
		return fmt.Errorf("getting AgentRun for idempotent wake: %w", err)
	}
	if run.Annotations[LastWakeDeliveryAnnotation] == deliveryID {
		return nil
	}
	if err := deliverMessage(ctx, stateStore, key.Namespace, key.Name, contextMessage, "overseer", deliveryID, sessionclient.UserMessageModeEnqueue); err != nil {
		return err
	}
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		fresh := &platformv1alpha1.AgentRun{}
		if err := k8sClient.Get(ctx, key, fresh); err != nil {
			return err
		}
		if fresh.Annotations[LastWakeDeliveryAnnotation] == deliveryID {
			return nil
		}
		patch := client.MergeFrom(fresh.DeepCopy())
		if fresh.Annotations == nil {
			fresh.Annotations = map[string]string{}
		}
		fresh.Annotations[LastWakeDeliveryAnnotation] = deliveryID
		if provisionRunner(fresh) {
			fresh.Spec.WakeRequests++
		}
		return k8sClient.Patch(ctx, fresh, patch)
	})
}

// WakeAgentRun appends wake context to the run's existing session and increments
// spec.wakeRequests so the controller provisions a fresh runner pod.
func WakeAgentRun(ctx context.Context, k8sClient client.Client, stateStore store.StateStore, runNamespace, runName, contextMessage string) error {
	return wakeAgentRun(ctx, k8sClient, stateStore, runNamespace, runName, contextMessage, nil)
}

// WakeAgentRunFromPhases wakes a run only while its freshly-read phase is one
// of allowedPhases. The phase check and wake-counter increment share the same
// optimistic-concurrency patch, preventing a delayed retry from queuing a wake
// after another request has already resumed the run.
func WakeAgentRunFromPhases(ctx context.Context, k8sClient client.Client, stateStore store.StateStore, runNamespace, runName, contextMessage string, allowedPhases ...platformv1alpha1.AgentRunPhase) error {
	allowed := make(map[platformv1alpha1.AgentRunPhase]struct{}, len(allowedPhases))
	for _, phase := range allowedPhases {
		allowed[phase] = struct{}{}
	}
	if len(allowed) == 0 {
		return fmt.Errorf("at least one allowed phase is required")
	}
	return wakeAgentRun(ctx, k8sClient, stateStore, runNamespace, runName, contextMessage, allowed)
}

func wakeAgentRun(ctx context.Context, k8sClient client.Client, stateStore store.StateStore, runNamespace, runName, contextMessage string, allowedPhases map[platformv1alpha1.AgentRunPhase]struct{}) error {
	return wakeAgentRunIdempotent(ctx, k8sClient, stateStore, runNamespace, runName, contextMessage, "", allowedPhases)
}

// WakeAgentRunIdempotent appends one wake message and reconciles one wake
// counter increment for a stable caller-supplied key.
func WakeAgentRunIdempotent(ctx context.Context, k8sClient client.Client, stateStore store.StateStore, runNamespace, runName, contextMessage, idempotencyKey string, allowedPhases ...platformv1alpha1.AgentRunPhase) error {
	allowed := make(map[platformv1alpha1.AgentRunPhase]struct{}, len(allowedPhases))
	for _, phase := range allowedPhases {
		allowed[phase] = struct{}{}
	}
	return wakeAgentRunIdempotent(ctx, k8sClient, stateStore, runNamespace, runName, contextMessage, idempotencyKey, allowed)
}

func wakeAgentRunIdempotent(ctx context.Context, k8sClient client.Client, stateStore store.StateStore, runNamespace, runName, contextMessage, idempotencyKey string, allowedPhases map[platformv1alpha1.AgentRunPhase]struct{}) error {
	runNamespace = strings.TrimSpace(runNamespace)
	runName = strings.TrimSpace(runName)
	contextMessage = strings.TrimSpace(contextMessage)
	if k8sClient == nil {
		return fmt.Errorf("k8s client is required")
	}
	if stateStore == nil {
		return fmt.Errorf("state store is required")
	}
	if runNamespace == "" || runName == "" {
		return fmt.Errorf("run namespace and name are required")
	}
	if contextMessage == "" {
		return fmt.Errorf("context message is required")
	}

	key := client.ObjectKey{Namespace: runNamespace, Name: runName}
	if allowedPhases != nil {
		run := &platformv1alpha1.AgentRun{}
		if err := k8sClient.Get(ctx, key, run); err != nil {
			return fmt.Errorf("getting AgentRun %s/%s before wake: %w", runNamespace, runName, err)
		}
		if _, allowed := allowedPhases[run.Status.Phase]; !allowed {
			return fmt.Errorf("AgentRun %s/%s cannot be woken from phase %s", runNamespace, runName, run.Status.Phase)
		}
	}

	sess, err := stateStore.GetSessionByRun(ctx, runName, runNamespace)
	if err != nil {
		return fmt.Errorf("getting session for AgentRun %s/%s: %w", runNamespace, runName, err)
	}
	var targetWakeRequests int64
	wakeIntents, durable := stateStore.(store.WakeIntentStore)
	if durable && strings.TrimSpace(idempotencyKey) != "" {
		current := &platformv1alpha1.AgentRun{}
		if err := k8sClient.Get(ctx, key, current); err != nil {
			return fmt.Errorf("getting wake target: %w", err)
		}
		targetWakeRequests = current.Spec.WakeRequests + 1
		if _, target, _, err := wakeIntents.ReserveWakeIntent(ctx, sess.ID, idempotencyKey, contextMessage, targetWakeRequests); err != nil {
			return fmt.Errorf("reserving wake intent: %w", err)
		} else {
			targetWakeRequests = target
		}
	} else if _, err := stateStore.AppendMessage(ctx, sess.ID, "user", contextMessage, nil); err != nil {
		return fmt.Errorf("appending wake message for AgentRun %s/%s: %w", runNamespace, runName, err)
	}

	if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		run := &platformv1alpha1.AgentRun{}
		if err := k8sClient.Get(ctx, key, run); err != nil {
			return err
		}
		if allowedPhases != nil {
			if _, allowed := allowedPhases[run.Status.Phase]; !allowed {
				return fmt.Errorf("AgentRun %s/%s cannot be woken from phase %s", runNamespace, runName, run.Status.Phase)
			}
		}
		patch := client.MergeFromWithOptions(run.DeepCopy(), client.MergeFromWithOptimisticLock{})
		if targetWakeRequests > 0 {
			if run.Spec.WakeRequests >= targetWakeRequests {
				return nil
			}
			run.Spec.WakeRequests = targetWakeRequests
		} else {
			run.Spec.WakeRequests++
		}
		return k8sClient.Patch(ctx, run, patch)
	}); err != nil {
		return fmt.Errorf("incrementing wake requests for AgentRun %s/%s: %w", runNamespace, runName, err)
	}
	if durable && strings.TrimSpace(idempotencyKey) != "" {
		if err := wakeIntents.MarkWakeIntentApplied(ctx, sess.ID, idempotencyKey); err != nil {
			return err
		}
	}
	return nil
}
