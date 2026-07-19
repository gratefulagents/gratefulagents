package triggers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/store"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	externalPRReviewIDsAnnotation  = "triggers.gratefulagents.dev/github-review-ids"
	externalPRCommentIDsAnnotation = "triggers.gratefulagents.dev/github-comment-ids"
	githubEventMetadataKey         = "github_event_key"
	maxExternalPREventIDs          = 64
)

func externalPREventHandled(run *platformv1alpha1.AgentRun, loopKey string, event PullRequestEvent) bool {
	if event.SourceID == 0 {
		return false
	}
	for _, id := range externalPREventIDs(run, loopKey, event.Type) {
		if id == event.SourceID {
			return true
		}
	}
	return false
}

func markExternalPREvent(run *platformv1alpha1.AgentRun, loopKey string, event PullRequestEvent) {
	if event.SourceID == 0 || externalPREventHandled(run, loopKey, event) {
		return
	}
	ids := append(externalPREventIDs(run, loopKey, event.Type), event.SourceID)
	if len(ids) > maxExternalPREventIDs {
		ids = ids[len(ids)-maxExternalPREventIDs:]
	}
	encoded, _ := json.Marshal(ids)
	setLoopAnnotation(run, loopKey, externalPREventIDsAnnotation(event.Type), string(encoded))
}

func externalPREventIDs(run *platformv1alpha1.AgentRun, loopKey string, eventType PullRequestEventType) []int64 {
	var ids []int64
	_ = json.Unmarshal([]byte(loopAnnotation(run, loopKey, externalPREventIDsAnnotation(eventType))), &ids)
	return ids
}

func externalPREventIDsAnnotation(eventType PullRequestEventType) string {
	if eventType == PREventReviewSubmitted {
		return externalPRReviewIDsAnnotation
	}
	return externalPRCommentIDsAnnotation
}

func externalPREventID(event PullRequestEvent) string {
	return string(event.Type) + ":" + strconv.FormatInt(event.SourceID, 10)
}

func externalPREventKey(loopKey string, event PullRequestEvent) string {
	return loopKey + ":" + externalPREventID(event)
}

func (e *PRLoopEngine) wakeImplementerForPREvent(ctx context.Context, implementer *platformv1alpha1.AgentRun, loopKey, newState, message string, event PullRequestEvent) error {
	if event.SourceID == 0 {
		return e.wakeImplementer(ctx, implementer, loopKey, newState, message)
	}
	cancelled := implementer.Status.Phase == platformv1alpha1.AgentRunPhaseCancelled
	if !cancelled {
		if e.StateStore == nil {
			return fmt.Errorf("state store is required to message AgentRun %s/%s", implementer.Namespace, implementer.Name)
		}
		sess, err := e.StateStore.GetSessionByRun(ctx, implementer.Name, implementer.Namespace)
		if err != nil {
			return fmt.Errorf("getting session for AgentRun %s/%s: %w", implementer.Namespace, implementer.Name, err)
		}
		eventKey := externalPREventKey(loopKey, event)
		metadata, _ := json.Marshal(map[string]string{githubEventMetadataKey: eventKey})
		if _, err := e.StateStore.AppendMessage(ctx, sess.ID, "user", message, metadata); err != nil && !errors.Is(err, store.ErrMessageAlreadyExists) {
			return fmt.Errorf("appending message for AgentRun %s/%s: %w", implementer.Namespace, implementer.Name, err)
		}
	}

	key := client.ObjectKeyFromObject(implementer)
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		fresh := &platformv1alpha1.AgentRun{}
		if err := e.Get(ctx, key, fresh); err != nil {
			return err
		}
		if externalPREventHandled(fresh, loopKey, event) {
			*implementer = *fresh
			return nil
		}
		patch := client.MergeFrom(fresh.DeepCopy())
		setLoopLabel(fresh, loopKey, PRLoopStateLabel, newState)
		setLoopAnnotation(fresh, loopKey, PRLoopKeyAnnotation, loopKey)
		markExternalPREvent(fresh, loopKey, event)
		switch fresh.Status.Phase {
		case platformv1alpha1.AgentRunPhaseSucceeded, platformv1alpha1.AgentRunPhaseFailed, platformv1alpha1.AgentRunPhasePaused:
			fresh.Spec.WakeRequests++
		}
		if err := e.Patch(ctx, fresh, patch); err != nil {
			return err
		}
		*implementer = *fresh
		return nil
	})
	if err != nil {
		return fmt.Errorf("patching external PR event on %s/%s: %w", implementer.Namespace, implementer.Name, err)
	}
	if implementer.Status.Phase == platformv1alpha1.AgentRunPhaseCancelled {
		e.escalateBlocked(ctx, implementer, fmt.Sprintf("The PR review loop is blocked: implementer run %s is cancelled and cannot be woken. Review the PR manually or start a new implementer run.", implementer.Name))
	}
	return nil
}
