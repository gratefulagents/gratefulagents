package platform

import (
	"context"

	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/agentroles"
)

// bootstrapDefaultAnnotation marks objects seeded by the chart's bootstrap
// defaults (dist/chart/templates/bootstrap/defaults.yaml).
const bootstrapDefaultAnnotation = "platform.gratefulagents.dev/bootstrap-default"

// RetiredRoleInstructionCleanup deletes chart-seeded RoleInstructions the chart
// no longer ships. Bootstrap defaults are Helm hooks, and Helm never deletes a
// hook object once it leaves the chart, so without this sweep upgraded clusters
// would keep offering retired roles. Roles without the bootstrap annotation were
// created by users and are left alone even when their name matches.
type RetiredRoleInstructionCleanup struct {
	Client client.Client
	Reader client.Reader
}

// NeedLeaderElection keeps manager replicas from sweeping concurrently.
func (c *RetiredRoleInstructionCleanup) NeedLeaderElection() bool { return true }

// Start runs one sweep. Retired roles are never recreated, so a single pass
// per leader start is enough, and a failure is logged rather than stopping the
// manager.
func (c *RetiredRoleInstructionCleanup) Start(ctx context.Context) error {
	log := logf.FromContext(ctx).WithName("retired-role-cleanup")
	deleted, err := c.Sweep(ctx)
	if err != nil {
		log.Error(err, "failed to delete retired bootstrap RoleInstructions", "deleted", deleted)
		return nil
	}
	if len(deleted) > 0 {
		log.Info("deleted retired bootstrap RoleInstructions", "names", deleted)
	}
	return nil
}

// Sweep deletes every retired role that still carries the bootstrap annotation
// and returns the names it deleted.
func (c *RetiredRoleInstructionCleanup) Sweep(ctx context.Context) ([]string, error) {
	reader := c.Reader
	if reader == nil {
		reader = c.Client
	}
	var deleted []string
	for _, name := range agentroles.RetiredBootstrapNames {
		role := &platformv1alpha1.RoleInstruction{}
		if err := reader.Get(ctx, client.ObjectKey{Name: name}, role); err != nil {
			if k8serrors.IsNotFound(err) {
				continue
			}
			return deleted, err
		}
		if role.Annotations[bootstrapDefaultAnnotation] != "true" {
			continue
		}
		// The UID precondition spares a role recreated under the same name
		// after the read.
		err := c.Client.Delete(ctx, role, client.Preconditions{UID: &role.UID})
		if err != nil && !k8serrors.IsNotFound(err) {
			return deleted, err
		}
		if err == nil {
			deleted = append(deleted, name)
		}
	}
	return deleted, nil
}
