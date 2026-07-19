/*
Copyright 2026.

SPDX-License-Identifier: GPL-3.0-only
*/

package platform

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
)

// MCPServerReconciler reconciles MCPServer objects.
// It validates the spec and sets the status phase to Ready.
type MCPServerReconciler struct {
	client.Client
}

// +kubebuilder:rbac:groups=platform.gratefulagents.dev,resources=mcpservers,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=platform.gratefulagents.dev,resources=mcpservers/status,verbs=get;update;patch

func (r *MCPServerReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	srv := &platformv1alpha1.MCPServer{}
	if err := r.Get(ctx, req.NamespacedName, srv); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	phase, reason := validateMCPServer(srv)
	if !mcpServerStatusMatches(srv, phase, reason) {
		if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			fresh := &platformv1alpha1.MCPServer{}
			if err := r.Get(ctx, req.NamespacedName, fresh); err != nil {
				return err
			}
			if mcpServerStatusMatches(fresh, phase, reason) {
				return nil
			}
			patch := client.MergeFrom(fresh.DeepCopy())
			fresh.Status.Phase = phase
			setMCPServerCondition(fresh, "Validated", metav1.ConditionTrue, "Reconciled", reason)
			return r.Status().Patch(ctx, fresh, patch)
		}); err != nil {
			return ctrl.Result{}, fmt.Errorf("updating MCPServer status: %w", err)
		}
		log.Info("MCPServer status updated", "name", srv.Name, "phase", phase)
	}

	return ctrl.Result{}, nil
}

func (r *MCPServerReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&platformv1alpha1.MCPServer{}).
		Named("mcpserver").
		WithOptions(controller.Options{MaxConcurrentReconciles: 1}).
		Complete(r)
}

// validateMCPServer checks the spec and returns (phase, reason).
func validateMCPServer(srv *platformv1alpha1.MCPServer) (string, string) {
	if srv.Spec.MCPServerConfig == nil {
		return "Invalid", "mcpServerConfig is required"
	}
	if srv.Spec.MCPServerConfig.Command == "" {
		return "Invalid", "mcpServerConfig.command is required"
	}
	if srv.Spec.Version == "" {
		return "Ready", "Validated MCP server config"
	}
	return "Ready", fmt.Sprintf("Validated MCP server config version %s", srv.Spec.Version)
}

func setMCPServerCondition(srv *platformv1alpha1.MCPServer, condType string, status metav1.ConditionStatus, reason, message string) {
	now := metav1.Now()
	for i, c := range srv.Status.Conditions {
		if c.Type == condType {
			if c.Status == status && c.Reason == reason && c.Message == message {
				now = c.LastTransitionTime
			}
			srv.Status.Conditions[i].Status = status
			srv.Status.Conditions[i].Reason = reason
			srv.Status.Conditions[i].Message = message
			srv.Status.Conditions[i].LastTransitionTime = now
			return
		}
	}
	srv.Status.Conditions = append(srv.Status.Conditions, metav1.Condition{
		Type:               condType,
		Status:             status,
		Reason:             reason,
		Message:            message,
		LastTransitionTime: now,
	})
}

func mcpServerStatusMatches(srv *platformv1alpha1.MCPServer, phase, reason string) bool {
	if srv.Status.Phase != phase {
		return false
	}
	for _, cond := range srv.Status.Conditions {
		if cond.Type == "Validated" {
			return cond.Status == metav1.ConditionTrue &&
				cond.Reason == "Reconciled" &&
				cond.Message == reason
		}
	}
	return false
}
