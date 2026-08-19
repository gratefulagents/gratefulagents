/*
Copyright 2026.

SPDX-License-Identifier: AGPL-3.0-only
*/

package platform

import (
	"context"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
)

// SSHTunnelReconciler reconciles SSHTunnel objects.
// It validates the spec and the referenced Secret's required keys, then sets
// the status phase to Ready or Invalid so users see misconfiguration before a
// run tries to start a tunnel sidecar.
type SSHTunnelReconciler struct {
	client.Client
}

// +kubebuilder:rbac:groups=platform.gratefulagents.dev,resources=sshtunnels,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=platform.gratefulagents.dev,resources=sshtunnels/status,verbs=get;update;patch

func (r *SSHTunnelReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	tunnel := &platformv1alpha1.SSHTunnel{}
	if err := r.Get(ctx, req.NamespacedName, tunnel); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	phase, reason := r.validateSSHTunnel(ctx, tunnel)
	if !sshTunnelStatusMatches(tunnel, phase, reason) {
		if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			fresh := &platformv1alpha1.SSHTunnel{}
			if err := r.Get(ctx, req.NamespacedName, fresh); err != nil {
				return err
			}
			if sshTunnelStatusMatches(fresh, phase, reason) {
				return nil
			}
			patch := client.MergeFrom(fresh.DeepCopy())
			fresh.Status.Phase = phase
			status := metav1.ConditionTrue
			if phase != "Ready" {
				status = metav1.ConditionFalse
			}
			setSSHTunnelCondition(fresh, "Ready", status, "Reconciled", reason)
			return r.Status().Patch(ctx, fresh, patch)
		}); err != nil {
			return ctrl.Result{}, fmt.Errorf("updating SSHTunnel status: %w", err)
		}
		log.Info("SSHTunnel status updated", "name", tunnel.Name, "phase", phase)
	}

	return ctrl.Result{}, nil
}

func (r *SSHTunnelReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&platformv1alpha1.SSHTunnel{}).
		Named("sshtunnel").
		WithOptions(controller.Options{MaxConcurrentReconciles: 1}).
		Complete(r)
}

// validateSSHTunnel checks the spec and referenced Secret and returns
// (phase, reason).
func (r *SSHTunnelReconciler) validateSSHTunnel(ctx context.Context, tunnel *platformv1alpha1.SSHTunnel) (string, string) {
	if strings.TrimSpace(tunnel.Spec.Host) == "" {
		return "Invalid", "host is required"
	}
	if strings.TrimSpace(tunnel.Spec.User) == "" {
		return "Invalid", "user is required"
	}
	if tunnel.Spec.RemotePort <= 0 {
		return "Invalid", "remotePort is required"
	}
	secretName := strings.TrimSpace(tunnel.Spec.SecretRef.Name)
	if secretName == "" {
		return "Invalid", "secretRef.name is required"
	}
	secret := &corev1.Secret{}
	if err := r.Get(ctx, client.ObjectKey{Namespace: tunnel.Namespace, Name: secretName}, secret); err != nil {
		if apierrors.IsNotFound(err) {
			return "Invalid", fmt.Sprintf("secret %q not found", secretName)
		}
		return "Invalid", fmt.Sprintf("getting secret %q: %v", secretName, err)
	}
	for _, key := range []string{platformv1alpha1.SSHTunnelSecretPrivateKeyKey, platformv1alpha1.SSHTunnelSecretKnownHostsKey} {
		if len(secret.Data[key]) == 0 {
			return "Invalid", fmt.Sprintf("secret %q is missing key %q", secretName, key)
		}
	}
	return "Ready", fmt.Sprintf("Validated SSH tunnel to %s@%s", tunnel.Spec.User, tunnel.Spec.Host)
}

func setSSHTunnelCondition(tunnel *platformv1alpha1.SSHTunnel, condType string, status metav1.ConditionStatus, reason, message string) {
	now := metav1.Now()
	for i, c := range tunnel.Status.Conditions {
		if c.Type == condType {
			if c.Status == status && c.Reason == reason && c.Message == message {
				now = c.LastTransitionTime
			}
			tunnel.Status.Conditions[i].Status = status
			tunnel.Status.Conditions[i].Reason = reason
			tunnel.Status.Conditions[i].Message = message
			tunnel.Status.Conditions[i].LastTransitionTime = now
			return
		}
	}
	tunnel.Status.Conditions = append(tunnel.Status.Conditions, metav1.Condition{
		Type:               condType,
		Status:             status,
		Reason:             reason,
		Message:            message,
		LastTransitionTime: now,
	})
}

func sshTunnelStatusMatches(tunnel *platformv1alpha1.SSHTunnel, phase, reason string) bool {
	if tunnel.Status.Phase != phase {
		return false
	}
	wantStatus := metav1.ConditionTrue
	if phase != "Ready" {
		wantStatus = metav1.ConditionFalse
	}
	for _, cond := range tunnel.Status.Conditions {
		if cond.Type == "Ready" {
			return cond.Status == wantStatus &&
				cond.Reason == "Reconciled" &&
				cond.Message == reason
		}
	}
	return false
}
