package platform

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
)

// RuntimeProfileReconciler keeps RuntimeProfile status aligned with the effective defaults.
type RuntimeProfileReconciler struct {
	client.Client
}

// +kubebuilder:rbac:groups=platform.gratefulagents.dev,resources=runtimeprofiles,verbs=get;list;watch
// +kubebuilder:rbac:groups=platform.gratefulagents.dev,resources=runtimeprofiles/status,verbs=get;update;patch

func (r *RuntimeProfileReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	profile := &platformv1alpha1.RuntimeProfile{}
	if err := r.Get(ctx, req.NamespacedName, profile); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	hash, err := runtimeProfileResolvedDefaultsHash(profile)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("computing runtime profile defaults hash: %w", err)
	}

	if runtimeProfileStatusMatches(profile, hash) {
		return ctrl.Result{}, nil
	}

	if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		fresh := &platformv1alpha1.RuntimeProfile{}
		if err := r.Get(ctx, req.NamespacedName, fresh); err != nil {
			return err
		}
		freshHash, err := runtimeProfileResolvedDefaultsHash(fresh)
		if err != nil {
			return fmt.Errorf("computing runtime profile defaults hash: %w", err)
		}
		if runtimeProfileStatusMatches(fresh, freshHash) {
			return nil
		}
		patch := client.MergeFrom(fresh.DeepCopy())
		fresh.Status.Phase = "Ready"
		fresh.Status.ResolvedDefaultsHash = freshHash
		setRuntimeProfileCondition(fresh, metav1.Condition{
			Type:               "Ready",
			Status:             metav1.ConditionTrue,
			Reason:             "ResolvedDefaults",
			Message:            "RuntimeProfile defaults resolved",
			ObservedGeneration: fresh.Generation,
			LastTransitionTime: metav1.Now(),
		})
		return r.Status().Patch(ctx, fresh, patch)
	}); err != nil {
		return ctrl.Result{}, fmt.Errorf("updating RuntimeProfile status: %w", err)
	}
	return ctrl.Result{}, nil
}

func (r *RuntimeProfileReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&platformv1alpha1.RuntimeProfile{}).
		Named("runtimeprofile").
		WithOptions(controller.Options{MaxConcurrentReconciles: 1}).
		Complete(r)
}

func runtimeProfileResolvedDefaultsHash(profile *platformv1alpha1.RuntimeProfile) (string, error) {
	if profile == nil {
		return "", nil
	}
	payload, err := json.Marshal(profile.Spec)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:]), nil
}

func setRuntimeProfileCondition(profile *platformv1alpha1.RuntimeProfile, next metav1.Condition) {
	if profile == nil {
		return
	}
	for i := range profile.Status.Conditions {
		if profile.Status.Conditions[i].Type != next.Type {
			continue
		}
		current := &profile.Status.Conditions[i]
		if current.Status == next.Status && current.Reason == next.Reason && current.Message == next.Message && current.ObservedGeneration == next.ObservedGeneration {
			next.LastTransitionTime = current.LastTransitionTime
		}
		*current = next
		return
	}
	profile.Status.Conditions = append(profile.Status.Conditions, next)
}

func runtimeProfileStatusMatches(profile *platformv1alpha1.RuntimeProfile, hash string) bool {
	if profile.Status.Phase != "Ready" || profile.Status.ResolvedDefaultsHash != hash {
		return false
	}
	for _, cond := range profile.Status.Conditions {
		if cond.Type == "Ready" {
			return cond.Status == metav1.ConditionTrue &&
				cond.Reason == "ResolvedDefaults" &&
				cond.Message == "RuntimeProfile defaults resolved" &&
				cond.ObservedGeneration == profile.Generation
		}
	}
	return false
}
