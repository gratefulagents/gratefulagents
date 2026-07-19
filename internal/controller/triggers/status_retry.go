package triggers

import (
	"context"

	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func retryLinearProjectStatusUpdate(ctx context.Context, c client.Client, key client.ObjectKey, mutate func(*triggersv1alpha1.LinearProject)) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		fresh := &triggersv1alpha1.LinearProject{}
		if err := c.Get(ctx, key, fresh); err != nil {
			return err
		}
		mutate(fresh)
		return c.Status().Update(ctx, fresh)
	})
}

func retryGitHubRepositoryStatusUpdate(ctx context.Context, c client.Client, key client.ObjectKey, mutate func(*triggersv1alpha1.GitHubRepository)) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		fresh := &triggersv1alpha1.GitHubRepository{}
		if err := c.Get(ctx, key, fresh); err != nil {
			return err
		}
		mutate(fresh)
		return c.Status().Update(ctx, fresh)
	})
}

func retryCronStatusUpdate(ctx context.Context, c client.Client, key client.ObjectKey, mutate func(*triggersv1alpha1.Cron)) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		fresh := &triggersv1alpha1.Cron{}
		if err := c.Get(ctx, key, fresh); err != nil {
			return err
		}
		mutate(fresh)
		return c.Status().Update(ctx, fresh)
	})
}
