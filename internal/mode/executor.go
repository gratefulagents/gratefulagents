package mode

import (
	"context"
	"fmt"
	"log"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ExecuteSwitch atomically patches the AgentRun status to reflect a mode switch.
func ExecuteSwitch(
	ctx context.Context,
	k8s client.Client,
	key client.ObjectKey,
	eval EvaluateResult,
	actor string,
	source string,
) (*platformv1alpha1.ModeTransitionEvent, error) {
	if eval.Result == ResultDenied || eval.Result == ResultNoop {
		return nil, fmt.Errorf("cannot execute transition with result %q", eval.Result)
	}
	if eval.Target == nil {
		return nil, fmt.Errorf("no target template in evaluation result")
	}

	var event platformv1alpha1.ModeTransitionEvent

	if err := retryStatusPatch(ctx, k8s, key, func(run *platformv1alpha1.AgentRun) {
		now := metav1.Now()

		event = platformv1alpha1.ModeTransitionEvent{
			FromMode:  run.Status.ModeName,
			ToMode:    eval.Target.Name,
			Result:    platformv1alpha1.TransitionApplied,
			Actor:     actor,
			Source:    source,
			Timestamp: now,
		}

		// Apply snapshot.
		run.Status.ModeSnapshot = eval.Target.DeepCopy()
		run.Status.ModeName = eval.Target.Name
		run.Status.ModeVersion = eval.Target.Version
		run.Status.ModeRevision++
	}); err != nil {
		return nil, fmt.Errorf("applying mode switch: %w", err)
	}

	logAudit("APPLIED", key, event.FromMode, event.ToMode, actor, source, "")
	return &event, nil
}

// RecordDenied records a denied transition attempt on the run status.
func RecordDenied(
	ctx context.Context,
	k8s client.Client,
	key client.ObjectKey,
	targetMode string,
	reason string,
	actor string,
	source string,
) error {
	logAudit("DENIED", key, "", targetMode, actor, source, reason)
	return retryStatusPatch(ctx, k8s, key, func(run *platformv1alpha1.AgentRun) {
		run.Status.ModeDeniedCount++
	})
}

// RecordNoop records a noop transition attempt on the run status.
func RecordNoop(
	ctx context.Context,
	k8s client.Client,
	key client.ObjectKey,
	targetMode string,
	reason string,
) error {
	logAudit("NOOP", key, "", targetMode, "", "", reason)
	return retryStatusPatch(ctx, k8s, key, func(run *platformv1alpha1.AgentRun) {
		run.Status.ModeNoopCount++
	})
}

// retryStatusPatch performs an optimistic-locking status patch on an AgentRun.
func retryStatusPatch(ctx context.Context, k8s client.Client, key client.ObjectKey, mutate func(*platformv1alpha1.AgentRun)) error {
	for i := 0; i < 5; i++ {
		var run platformv1alpha1.AgentRun
		if err := k8s.Get(ctx, key, &run); err != nil {
			return err
		}
		patch := client.MergeFrom(run.DeepCopy())
		mutate(&run)
		if err := k8s.Status().Patch(ctx, &run, patch); err != nil {
			if i < 4 {
				continue
			}
			return err
		}
		return nil
	}
	return fmt.Errorf("failed to patch status after retries")
}

// logAudit emits a structured audit log for mode transitions.
func logAudit(result string, key client.ObjectKey, fromMode, toMode, actor, source, reason string) {
	if reason != "" {
		log.Printf("MODE_AUDIT %s/%s result=%s from=%s to=%s actor=%s source=%s reason=%s",
			key.Namespace, key.Name, result, fromMode, toMode, actor, source, reason)
	} else {
		log.Printf("MODE_AUDIT %s/%s result=%s from=%s to=%s actor=%s source=%s",
			key.Namespace, key.Name, result, fromMode, toMode, actor, source)
	}
}
