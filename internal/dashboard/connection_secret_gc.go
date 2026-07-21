package dashboard

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"
	"time"

	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
)

const (
	connectionSecretGCInterval = 15 * time.Minute
	// Keep newly-created, unreferenced Secrets long enough for an overlapping
	// Connection create/update to commit its reference before a sweep runs.
	connectionSecretGCGracePeriod = time.Hour
)

// ConnectionSecretGarbageCollector removes versioned dashboard-managed
// credential Secrets after their Connection stops referencing them.
type ConnectionSecretGarbageCollector struct {
	client   client.Client
	reader   client.Reader
	interval time.Duration
	grace    time.Duration
	now      func() time.Time
}

// NewConnectionSecretGarbageCollector creates a leader-only collector. The
// uncached reader ensures each sweep computes references from API-server state.
func NewConnectionSecretGarbageCollector(c client.Client, reader client.Reader) *ConnectionSecretGarbageCollector {
	if reader == nil {
		reader = c
	}
	return &ConnectionSecretGarbageCollector{
		client:   c,
		reader:   reader,
		interval: connectionSecretGCInterval,
		grace:    connectionSecretGCGracePeriod,
		now:      time.Now,
	}
}

// NeedLeaderElection keeps multiple manager replicas from performing the same
// sweep concurrently.
func (g *ConnectionSecretGarbageCollector) NeedLeaderElection() bool { return true }

// Start runs an initial sweep and then repeats until the manager stops.
func (g *ConnectionSecretGarbageCollector) Start(ctx context.Context) error {
	log.Println("[connection-secret-gc] started (leader-only)")
	g.runSweep(ctx)
	ticker := time.NewTicker(g.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			log.Println("[connection-secret-gc] stopped")
			return nil
		case <-ticker.C:
			g.runSweep(ctx)
		}
	}
}

func (g *ConnectionSecretGarbageCollector) runSweep(ctx context.Context) {
	deleted, err := g.sweep(ctx)
	if err != nil {
		log.Printf("[connection-secret-gc] sweep failed: %v", err)
		return
	}
	if deleted > 0 {
		log.Printf("[connection-secret-gc] deleted %d orphaned credential Secret(s)", deleted)
	}
}

func (g *ConnectionSecretGarbageCollector) sweep(ctx context.Context) (int, error) {
	connections := &triggersv1alpha1.ConnectionList{}
	if err := g.reader.List(ctx, connections); err != nil {
		return 0, fmt.Errorf("list Connections: %w", err)
	}
	referenced := make(map[types.NamespacedName]struct{})
	for i := range connections.Items {
		for _, name := range connectionSecretReferences(&connections.Items[i]) {
			if name != "" {
				referenced[types.NamespacedName{Namespace: connections.Items[i].Namespace, Name: name}] = struct{}{}
			}
		}
	}

	requirement, err := labels.NewRequirement(connectionSecretLabel, selection.Exists, nil)
	if err != nil {
		return 0, fmt.Errorf("build managed Secret selector: %w", err)
	}
	managed := &corev1.SecretList{}
	if err := g.reader.List(ctx, managed, client.MatchingLabelsSelector{Selector: labels.NewSelector().Add(*requirement)}); err != nil {
		return 0, fmt.Errorf("list managed Secrets: %w", err)
	}

	now := g.now()
	cutoff := now.Add(-g.grace)
	deleted := 0
	var sweepErrors []error
	for i := range managed.Items {
		secret := &managed.Items[i]
		key := types.NamespacedName{Namespace: secret.Namespace, Name: secret.Name}
		_, isReferenced := referenced[key]
		orphanedAtRaw := secret.Annotations[connectionSecretOrphanedAt]
		if isReferenced {
			if orphanedAtRaw != "" {
				delete(secret.Annotations, connectionSecretOrphanedAt)
				if err := g.client.Update(ctx, secret); err != nil && !k8serrors.IsNotFound(err) {
					sweepErrors = append(sweepErrors, fmt.Errorf("clear orphan marker on Secret %s/%s: %w", secret.Namespace, secret.Name, err))
				}
			}
			continue
		}
		if secret.CreationTimestamp.IsZero() || secret.CreationTimestamp.Time.After(cutoff) {
			continue
		}

		orphanedUnixNano, err := strconv.ParseInt(orphanedAtRaw, 10, 64)
		orphanedAt := time.Unix(0, orphanedUnixNano)
		if err != nil || orphanedAtRaw == "" || orphanedAt.After(now) {
			if secret.Annotations == nil {
				secret.Annotations = map[string]string{}
			}
			secret.Annotations[connectionSecretOrphanedAt] = strconv.FormatInt(now.UnixNano(), 10)
			if err := g.client.Update(ctx, secret); err != nil && !k8serrors.IsNotFound(err) {
				sweepErrors = append(sweepErrors, fmt.Errorf("mark orphaned Secret %s/%s: %w", secret.Namespace, secret.Name, err))
			}
			continue
		}
		if orphanedAt.After(cutoff) {
			continue
		}
		preconditions := client.Preconditions{UID: &secret.UID, ResourceVersion: &secret.ResourceVersion}
		if err := g.client.Delete(ctx, secret, preconditions); err != nil && !k8serrors.IsNotFound(err) {
			sweepErrors = append(sweepErrors, fmt.Errorf("delete Secret %s/%s: %w", secret.Namespace, secret.Name, err))
			continue
		}
		deleted++
	}
	return deleted, errors.Join(sweepErrors...)
}

func connectionSecretReferences(connection *triggersv1alpha1.Connection) []string {
	if connection == nil {
		return nil
	}
	refs := make([]string, 0, 2)
	if connection.Spec.GitHub != nil {
		refs = append(refs, connection.Spec.GitHub.TokenSecret, connection.Spec.GitHub.PrivateKeySecret)
	}
	if connection.Spec.Slack != nil {
		refs = append(refs, connection.Spec.Slack.TokensSecret)
	}
	if connection.Spec.Linear != nil {
		refs = append(refs, connection.Spec.Linear.APIKeySecret)
	}
	return refs
}
