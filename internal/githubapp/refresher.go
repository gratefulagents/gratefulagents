package githubapp

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const appRefreshInterval = 10 * time.Minute

// Refresher periodically rotates per-run GitHub App installation token Secrets.
type Refresher struct {
	client client.Client
	minter InstallationTokenMinter
	scheme *runtime.Scheme
}

// NewRefresher creates a leader-only GitHub App token refresher.
func NewRefresher(c client.Client, minter InstallationTokenMinter, scheme *runtime.Scheme) *Refresher {
	if minter == nil {
		minter = NewKeyedMinter()
	}
	return &Refresher{client: c, minter: minter, scheme: scheme}
}

// NeedLeaderElection returns true so the manager only starts this on the leader.
func (r *Refresher) NeedLeaderElection() bool { return true }

// Start runs the refresh loop until the context is cancelled.
func (r *Refresher) Start(ctx context.Context) error {
	log.Println("[githubapp] token refresher started (leader-only)")
	r.refreshAll(ctx)
	ticker := time.NewTicker(appRefreshInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			log.Println("[githubapp] token refresher stopped")
			return nil
		case <-ticker.C:
			r.refreshAll(ctx)
		}
	}
}

func (r *Refresher) refreshAll(ctx context.Context) {
	repos := &triggersv1alpha1.GitHubRepositoryList{}
	if err := r.client.List(ctx, repos); err != nil {
		log.Printf("[githubapp] error listing GitHubRepository resources: %v", err)
		return
	}
	for i := range repos.Items {
		gh := &repos.Items[i]
		if gh.Spec.GitHubApp == nil {
			continue
		}
		if err := r.refreshRepositoryRuns(ctx, gh); err != nil {
			log.Printf("[githubapp] error refreshing tokens for GitHubRepository %s/%s: %v", gh.Namespace, gh.Name, err)
		}
	}
}

func (r *Refresher) refreshRepositoryRuns(ctx context.Context, gh *triggersv1alpha1.GitHubRepository) error {
	privateKeyPEM, err := readPrivateKeySecret(ctx, r.client, gh.Namespace, gh.Spec.GitHubApp.PrivateKeySecret)
	if err != nil {
		return err
	}
	runs := &platformv1alpha1.AgentRunList{}
	if err := r.client.List(ctx, runs, client.InNamespace(gh.Namespace)); err != nil {
		return fmt.Errorf("list AgentRuns: %w", err)
	}
	for i := range runs.Items {
		run := &runs.Items[i]
		if !isActiveGitHubAppRun(run, gh) {
			continue
		}
		if err := r.refreshRunSecret(ctx, gh, run, privateKeyPEM); err != nil {
			log.Printf("[githubapp] error refreshing run token %s/%s: %v", run.Namespace, run.Name, err)
		}
	}
	return nil
}

func (r *Refresher) refreshRunSecret(ctx context.Context, gh *triggersv1alpha1.GitHubRepository, run *platformv1alpha1.AgentRun, privateKeyPEM []byte) error {
	if run.Spec.Secrets == nil || strings.TrimSpace(run.Spec.Secrets.GitHubTokenSecret) == "" {
		return nil
	}
	// Preserve the reviewer downscope on refresh: a reviewer's rotated token
	// must never regain push permissions.
	var token string
	var err error
	if run.Labels[triggersv1alpha1.PRLoopRoleLabelKey] == triggersv1alpha1.PRLoopRoleReviewerValue {
		if scoped, ok := r.minter.(ScopedInstallationTokenMinter); ok {
			token, err = scoped.MintScopedInstallationToken(ctx, gh.Spec.GitHubApp.AppID, gh.Spec.GitHubApp.InstallationID, privateKeyPEM, ReviewerInstallationPermissions())
		} else {
			log.Printf("[githubapp] minter does not support scoped tokens — reviewer run %s/%s refreshed with full scope", run.Namespace, run.Name)
			token, err = r.minter.MintInstallationToken(ctx, gh.Spec.GitHubApp.AppID, gh.Spec.GitHubApp.InstallationID, privateKeyPEM)
		}
	} else {
		token, err = r.minter.MintInstallationToken(ctx, gh.Spec.GitHubApp.AppID, gh.Spec.GitHubApp.InstallationID, privateKeyPEM)
	}
	if err != nil {
		return err
	}
	secretName := strings.TrimSpace(run.Spec.Secrets.GitHubTokenSecret)
	secret := &corev1.Secret{}
	err = r.client.Get(ctx, client.ObjectKey{Namespace: run.Namespace, Name: secretName}, secret)
	if apierrors.IsNotFound(err) {
		secret = &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: run.Namespace}, Type: corev1.SecretTypeOpaque, Data: map[string][]byte{TokenSecretKey: []byte(token)}}
		if err := ctrl.SetControllerReference(run, secret, r.scheme); err != nil {
			return fmt.Errorf("set owner reference: %w", err)
		}
		return r.client.Create(ctx, secret)
	}
	if err != nil {
		return fmt.Errorf("get token Secret: %w", err)
	}
	if secret.Data == nil {
		secret.Data = map[string][]byte{}
	}
	secret.Data[TokenSecretKey] = []byte(token)
	secret.Type = corev1.SecretTypeOpaque
	if metav1.GetControllerOf(secret) == nil {
		if err := ctrl.SetControllerReference(run, secret, r.scheme); err != nil {
			return fmt.Errorf("set owner reference: %w", err)
		}
	}
	return r.client.Update(ctx, secret)
}

func isActiveGitHubAppRun(run *platformv1alpha1.AgentRun, gh *triggersv1alpha1.GitHubRepository) bool {
	if run == nil || gh == nil || run.Spec.Trigger.Kind != "GitHubRepository" {
		return false
	}
	triggerName := strings.TrimSpace(run.Annotations["triggers.gratefulagents.dev/runtime-trigger-name"])
	if triggerName == "" {
		triggerName = strings.TrimSpace(run.Spec.Trigger.Name)
	}
	if triggerName != gh.Name {
		return false
	}
	switch run.Status.Phase {
	case platformv1alpha1.AgentRunPhaseSucceeded, platformv1alpha1.AgentRunPhaseFailed, platformv1alpha1.AgentRunPhaseCancelled:
		return false
	default:
		return true
	}
}

func readPrivateKeySecret(ctx context.Context, c client.Client, namespace, name string) ([]byte, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("GitHub App private key secret name is required")
	}
	secret := &corev1.Secret{}
	if err := c.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, secret); err != nil {
		return nil, fmt.Errorf("get GitHub App private key Secret %s/%s: %w", namespace, name, err)
	}
	data := secret.Data[PrivateKeySecretKey]
	if len(data) == 0 {
		return nil, fmt.Errorf("GitHub App private key Secret %s/%s missing %q", namespace, name, PrivateKeySecretKey)
	}
	return data, nil
}
