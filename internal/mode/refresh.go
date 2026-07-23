package mode

import (
	"context"
	"fmt"
	"reflect"
	"strings"

	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
)

// RefreshCurrentSnapshot reloads the active ModeTemplate into an AgentRun
// without changing the mode name. It is used when a run paused on a plan made
// under an older template and must continue under the template's current
// permissions after approval.
func RefreshCurrentSnapshot(ctx context.Context, c client.Client, key client.ObjectKey) (bool, error) {
	refreshed := false
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		run := &platformv1alpha1.AgentRun{}
		if err := c.Get(ctx, key, run); err != nil {
			return err
		}
		modeName := strings.TrimSpace(run.Status.ModeName)
		if modeName == "" {
			return fmt.Errorf("AgentRun %s/%s has no active mode", key.Namespace, key.Name)
		}

		tmpl := &platformv1alpha1.ModeTemplate{}
		if err := c.Get(ctx, client.ObjectKey{Name: modeName}, tmpl); err != nil {
			return fmt.Errorf("getting current ModeTemplate %q: %w", modeName, err)
		}
		next := tmpl.Spec.DeepCopy()
		next.Name = modeName
		// Match normal mode resolution: all runs use autonomous pacing even when
		// an older template omits the legacy autonomous field.
		next.Autonomous = true
		if reflect.DeepEqual(run.Status.ModeSnapshot, next) {
			return nil
		}

		patch := client.MergeFrom(run.DeepCopy())
		run.Status.ModeSnapshot = next
		run.Status.ModeName = next.Name
		run.Status.ModeVersion = next.Version
		run.Status.ModeRevision++
		if err := c.Status().Patch(ctx, run, patch); err != nil {
			return err
		}
		refreshed = true
		return nil
	})
	return refreshed, err
}
