package triggers

import (
	"context"
	"fmt"
	"strings"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	gitHubRepositoryTriggerKind     = "GitHubRepository"
	cancelRequestedAnnotation       = "platform.gratefulagents.dev/cancel-requested"
	maxGitHubRepositoryProcessedIDs = 500
)

// ExistingTriggerIssueIDs returns the set of external IDs already created by
// AgentRuns with the given trigger kind and name. Used to avoid creating
// duplicate runs for the same issue.
func ExistingTriggerIssueIDs(ctx context.Context, c client.Client, namespace, triggerKind, triggerName string) (map[string]struct{}, error) {
	runs := &platformv1alpha1.AgentRunList{}
	if err := c.List(ctx, runs, client.InNamespace(namespace)); err != nil {
		return nil, fmt.Errorf("listing AgentRuns: %w", err)
	}
	ids := map[string]struct{}{}
	for i := range runs.Items {
		run := &runs.Items[i]
		if !TriggerRunMatches(run, triggerKind, triggerName) || run.Spec.Trigger.ExternalRef == nil {
			continue
		}
		if id := strings.TrimSpace(run.Spec.Trigger.ExternalRef.ID); id != "" {
			ids[id] = struct{}{}
		}
	}
	return ids, nil
}

func hasProcessedIssueID(ids []string, issueID string) bool {
	issueID = strings.TrimSpace(issueID)
	if issueID == "" {
		return false
	}
	for _, id := range ids {
		if strings.TrimSpace(id) == issueID {
			return true
		}
	}
	return false
}

func appendProcessedIssueIDs(existing []string, issueIDs ...string) []string {
	out := append([]string(nil), existing...)
	for _, issueID := range issueIDs {
		issueID = strings.TrimSpace(issueID)
		if issueID == "" || hasProcessedIssueID(out, issueID) {
			continue
		}
		out = append(out, issueID)
	}
	if len(out) > maxGitHubRepositoryProcessedIDs {
		out = out[len(out)-maxGitHubRepositoryProcessedIDs:]
	}
	return out
}

// ReadSecretValue reads a single key from a Kubernetes Secret.
func ReadSecretValue(ctx context.Context, c client.Client, namespace, secretName, key string) (string, error) {
	secret := &corev1.Secret{}
	if err := c.Get(ctx, client.ObjectKey{Namespace: namespace, Name: secretName}, secret); err != nil {
		return "", fmt.Errorf("getting secret %s: %w", secretName, err)
	}
	data, ok := secret.Data[key]
	if !ok {
		return "", fmt.Errorf("secret %s has no key %q", secretName, key)
	}
	return string(data), nil
}

// ReadSecretValueWithVersion reads a single key from a Kubernetes Secret and
// returns the secret's ResourceVersion alongside it.
func ReadSecretValueWithVersion(ctx context.Context, c client.Client, namespace, secretName, key string) (string, string, error) {
	secret := &corev1.Secret{}
	if err := c.Get(ctx, client.ObjectKey{Namespace: namespace, Name: secretName}, secret); err != nil {
		return "", "", fmt.Errorf("getting secret %s: %w", secretName, err)
	}
	data, ok := secret.Data[key]
	if !ok {
		return "", "", fmt.Errorf("secret %s has no key %q", secretName, key)
	}
	return string(data), secret.ResourceVersion, nil
}
