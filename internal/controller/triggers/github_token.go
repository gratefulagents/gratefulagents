package triggers

import (
	"context"
	"fmt"
	"strings"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/githubapp"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type gitHubAppTokenMinter interface {
	MintInstallationToken(ctx context.Context, appID, installationID int64, privateKeyPEM []byte) (string, error)
}

func defaultGitHubAppMinter(minter gitHubAppTokenMinter) gitHubAppTokenMinter {
	if minter != nil {
		return minter
	}
	return githubapp.NewKeyedMinter()
}

// mintRunInstallationToken mints the per-run installation token. Reviewer
// runs get a downscoped token (read code, write reviews — never push) when
// the minter supports scoping; this boundary holds even if the agent pod is
// compromised.
func mintRunInstallationToken(ctx context.Context, minter gitHubAppTokenMinter, gh *triggersv1alpha1.GitHubRepository, run *platformv1alpha1.AgentRun, privateKeyPEM []byte) (string, error) {
	m := defaultGitHubAppMinter(minter)
	if run != nil && run.Labels[triggersv1alpha1.PRLoopRoleLabelKey] == triggersv1alpha1.PRLoopRoleReviewerValue {
		scoped, ok := m.(githubapp.ScopedInstallationTokenMinter)
		if !ok {
			return "", fmt.Errorf("GitHub App token minter does not support reviewer-scoped tokens")
		}
		return scoped.MintScopedInstallationToken(ctx, gh.Spec.GitHubApp.AppID, gh.Spec.GitHubApp.InstallationID, privateKeyPEM, githubapp.ReviewerInstallationPermissions())
	}
	return m.MintInstallationToken(ctx, gh.Spec.GitHubApp.AppID, gh.Spec.GitHubApp.InstallationID, privateKeyPEM)
}

func resolveGitHubToken(ctx context.Context, c client.Client, gh *triggersv1alpha1.GitHubRepository, minter gitHubAppTokenMinter) (string, error) {
	if gh == nil {
		return "", fmt.Errorf("GitHubRepository is nil")
	}
	if gh.Spec.GitHubApp != nil {
		privateKeyPEM, err := readGitHubAppPrivateKey(ctx, c, gh)
		if err != nil {
			return "", err
		}
		return defaultGitHubAppMinter(minter).MintInstallationToken(ctx, gh.Spec.GitHubApp.AppID, gh.Spec.GitHubApp.InstallationID, privateKeyPEM)
	}
	secretName := strings.TrimSpace(gh.Spec.GitHubTokenSecret)
	if secretName == "" {
		return "", fmt.Errorf("GitHubRepository %s/%s requires exactly one of spec.githubTokenSecret or spec.githubApp", gh.Namespace, gh.Name)
	}
	return ReadSecretValue(ctx, c, gh.Namespace, secretName, githubapp.TokenSecretKey)
}

// resolveGitHubPollingToken returns the least-privilege credential used by the
// control-plane PR monitor. PAT-backed repositories retain their configured
// token; GitHub App credentials are permission-downscoped to read-only PR and
// issue metadata and are never exposed to an AgentRun.
func resolveGitHubPollingToken(ctx context.Context, c client.Client, gh *triggersv1alpha1.GitHubRepository, minter gitHubAppTokenMinter) (string, error) {
	if gh == nil || gh.Spec.GitHubApp == nil {
		return resolveGitHubToken(ctx, c, gh, minter)
	}
	privateKeyPEM, err := readGitHubAppPrivateKey(ctx, c, gh)
	if err != nil {
		return "", err
	}
	scoped, ok := defaultGitHubAppMinter(minter).(githubapp.ScopedInstallationTokenMinter)
	if !ok {
		return "", fmt.Errorf("GitHub App token minter does not support read-only polling tokens")
	}
	return scoped.MintScopedInstallationToken(ctx, gh.Spec.GitHubApp.AppID, gh.Spec.GitHubApp.InstallationID, privateKeyPEM, githubapp.PullRequestPollingInstallationPermissions())
}

func readGitHubAppPrivateKey(ctx context.Context, c client.Client, gh *triggersv1alpha1.GitHubRepository) ([]byte, error) {
	if gh == nil || gh.Spec.GitHubApp == nil {
		return nil, fmt.Errorf("GitHub App auth is not configured")
	}
	secretName := strings.TrimSpace(gh.Spec.GitHubApp.PrivateKeySecret)
	if secretName == "" {
		return nil, fmt.Errorf("GitHubRepository %s/%s spec.githubApp.privateKeySecret is required", gh.Namespace, gh.Name)
	}
	secret := &corev1.Secret{}
	if err := c.Get(ctx, client.ObjectKey{Namespace: gh.Namespace, Name: secretName}, secret); err != nil {
		return nil, fmt.Errorf("getting GitHub App private key secret %s: %w", secretName, err)
	}
	data, ok := secret.Data[githubapp.PrivateKeySecretKey]
	if !ok || len(data) == 0 {
		return nil, fmt.Errorf("GitHub App private key secret %s has no key %q", secretName, githubapp.PrivateKeySecretKey)
	}
	return data, nil
}

func ensureRunGitHubAppTokenSecret(ctx context.Context, c client.Client, scheme *runtime.Scheme, gh *triggersv1alpha1.GitHubRepository, run *platformv1alpha1.AgentRun, minter gitHubAppTokenMinter) error {
	if gh == nil || gh.Spec.GitHubApp == nil || run == nil || run.Spec.Secrets == nil {
		return nil
	}
	secretName := strings.TrimSpace(run.Spec.Secrets.GitHubTokenSecret)
	if secretName == "" {
		return fmt.Errorf("AgentRun %s/%s missing GitHub App token secret reference", run.Namespace, run.Name)
	}
	privateKeyPEM, err := readGitHubAppPrivateKey(ctx, c, gh)
	if err != nil {
		return err
	}
	token, err := mintRunInstallationToken(ctx, minter, gh, run, privateKeyPEM)
	if err != nil {
		return err
	}
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: run.Namespace}, Type: corev1.SecretTypeOpaque, Data: map[string][]byte{githubapp.TokenSecretKey: []byte(token)}}
	if err := ctrl.SetControllerReference(run, secret, scheme); err != nil {
		return fmt.Errorf("setting owner reference on GitHub token Secret: %w", err)
	}
	if err := c.Create(ctx, secret); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("creating GitHub token Secret: %w", err)
		}
		existing := &corev1.Secret{}
		if getErr := c.Get(ctx, client.ObjectKey{Namespace: run.Namespace, Name: secretName}, existing); getErr != nil {
			return fmt.Errorf("getting existing GitHub token Secret: %w", getErr)
		}
		if existing.Data == nil {
			existing.Data = map[string][]byte{}
		}
		existing.Data[githubapp.TokenSecretKey] = []byte(token)
		existing.Type = corev1.SecretTypeOpaque
		if len(existing.OwnerReferences) == 0 {
			if err := ctrl.SetControllerReference(run, existing, scheme); err != nil {
				return fmt.Errorf("setting owner reference on existing GitHub token Secret: %w", err)
			}
		}
		if err := c.Update(ctx, existing); err != nil {
			return fmt.Errorf("updating GitHub token Secret: %w", err)
		}
	}
	return nil
}
