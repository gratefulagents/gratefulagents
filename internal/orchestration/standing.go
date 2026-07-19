package orchestration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/store"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	StandingRunRoleLabel      = "platform.gratefulagents.dev/standing-run-role"
	StandingRunRoleOverseer   = "overseer"
	StandingRunRoleMaintainer = "maintainer"
	SupervisedRunLabel        = "platform.gratefulagents.dev/supervised-run"

	CheckpointSeqAnnotation     = "platform.gratefulagents.dev/checkpoint-seq"
	CheckpointHandledAnnotation = "platform.gratefulagents.dev/checkpoint-handled"
	CheckpointReasonAnnotation  = "platform.gratefulagents.dev/checkpoint-reason"
	CheckpointTimeAnnotation    = "platform.gratefulagents.dev/checkpoint-time"
	StandingRunSeededAnnotation = "platform.gratefulagents.dev/standing-run-seeded"
	LastWakeDeliveryAnnotation  = "platform.gratefulagents.dev/last-wake-delivery"
)

// StandingRunName returns a stable DNS label derived from an owner and role.
func StandingRunName(ownerName, role string) string {
	raw := strings.Trim(strings.ToLower(strings.TrimSpace(ownerName)+"-"+strings.TrimSpace(role)), "-")
	var b strings.Builder
	lastDash := false
	for _, r := range raw {
		valid := r >= 'a' && r <= 'z' || r >= '0' && r <= '9'
		if valid {
			b.WriteRune(r)
			lastDash = false
		} else if !lastDash && b.Len() > 0 {
			b.WriteByte('-')
			lastDash = true
		}
	}
	name := strings.Trim(b.String(), "-")
	if name == "" {
		name = "standing-run"
	}
	if len(name) <= 63 {
		return name
	}
	sum := sha256.Sum256([]byte(raw))
	suffix := hex.EncodeToString(sum[:])[:10]
	return strings.TrimRight(name[:52], "-") + "-" + suffix
}

// EnsureStandingRun creates a controller-owned standing AgentRun and seeds its
// durable session. Existing runs are returned without replacing their spec.
func EnsureStandingRun(ctx context.Context, k8sClient client.Client, scheme *runtime.Scheme, stateStore store.StateStore, owner client.Object, desired *platformv1alpha1.AgentRun, initialMessage string) (*platformv1alpha1.AgentRun, bool, error) {
	if k8sClient == nil || scheme == nil || stateStore == nil || owner == nil || desired == nil {
		return nil, false, fmt.Errorf("client, scheme, state store, owner, and desired run are required")
	}
	role := strings.TrimSpace(desired.Labels[StandingRunRoleLabel])
	if role == "" {
		return nil, false, fmt.Errorf("standing run role label is required")
	}
	initialMessage = strings.TrimSpace(initialMessage)
	if initialMessage == "" {
		return nil, false, fmt.Errorf("initial message is required")
	}

	run := desired.DeepCopy()
	run.Namespace = owner.GetNamespace()
	run.Name = StandingRunName(owner.GetName(), role)
	run.GenerateName = ""
	if run.Labels == nil {
		run.Labels = map[string]string{}
	}
	run.Labels[StandingRunRoleLabel] = role
	run.Labels[SupervisedRunLabel] = owner.GetName()
	if !containsString(run.Finalizers, platformv1alpha1.AgentRunCleanupFinalizer) {
		run.Finalizers = append(run.Finalizers, platformv1alpha1.AgentRunCleanupFinalizer)
	}
	if err := ctrl.SetControllerReference(owner, run, scheme); err != nil {
		return nil, false, fmt.Errorf("setting standing run controller reference: %w", err)
	}

	created := true
	if err := k8sClient.Create(ctx, run); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return nil, false, fmt.Errorf("creating standing AgentRun %s/%s: %w", run.Namespace, run.Name, err)
		}
		created = false
		run = &platformv1alpha1.AgentRun{}
		if err := k8sClient.Get(ctx, client.ObjectKey{Namespace: owner.GetNamespace(), Name: StandingRunName(owner.GetName(), role)}, run); err != nil {
			return nil, false, fmt.Errorf("getting standing AgentRun: %w", err)
		}
	}
	if run.Labels[StandingRunRoleLabel] != role || run.Labels[SupervisedRunLabel] != owner.GetName() || !metav1.IsControlledBy(run, owner) {
		return nil, false, fmt.Errorf("standing AgentRun name collision: %s/%s is not owned by %s with role %q", run.Namespace, run.Name, owner.GetName(), role)
	}

	if run.Annotations[StandingRunSeededAnnotation] == "true" {
		return run, created, nil
	}
	session, err := stateStore.CreateSession(ctx, run.Name, run.Namespace, "pending", "setup")
	if err != nil {
		return nil, created, fmt.Errorf("creating standing run session: %w", err)
	}
	messages, err := stateStore.GetMessages(ctx, session.ID)
	if err != nil {
		return nil, created, fmt.Errorf("checking standing run seed message: %w", err)
	}
	seeded := false
	for _, message := range messages {
		if message.Role == "user" && message.Content == initialMessage {
			seeded = true
			break
		}
	}
	if !seeded {
		if _, err := stateStore.AppendMessage(ctx, session.ID, "user", initialMessage, nil); err != nil {
			return nil, created, fmt.Errorf("seeding standing run session: %w", err)
		}
	}
	key := client.ObjectKeyFromObject(run)
	if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		fresh := &platformv1alpha1.AgentRun{}
		if err := k8sClient.Get(ctx, key, fresh); err != nil {
			return err
		}
		if fresh.Annotations == nil {
			fresh.Annotations = map[string]string{}
		}
		fresh.Annotations[StandingRunSeededAnnotation] = "true"
		if err := k8sClient.Update(ctx, fresh); err != nil {
			return err
		}
		run = fresh
		return nil
	}); err != nil {
		return nil, created, fmt.Errorf("marking standing run seeded: %w", err)
	}
	return run, created, nil
}

// MarkCheckpointHandled records the latest checkpoint sequence consumed by a supervisor.
func MarkCheckpointHandled(ctx context.Context, k8sClient client.Client, key client.ObjectKey, sequence int64) error {
	if k8sClient == nil || key.Namespace == "" || key.Name == "" || sequence <= 0 {
		return fmt.Errorf("client, run key, and positive checkpoint sequence are required")
	}
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		run := &platformv1alpha1.AgentRun{}
		if err := k8sClient.Get(ctx, key, run); err != nil {
			return err
		}
		if current, err := strconv.ParseInt(run.Annotations[CheckpointHandledAnnotation], 10, 64); err == nil && current >= sequence {
			return nil
		}
		if run.Annotations == nil {
			run.Annotations = map[string]string{}
		}
		run.Annotations[CheckpointHandledAnnotation] = fmt.Sprint(sequence)
		return k8sClient.Update(ctx, run)
	})
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
