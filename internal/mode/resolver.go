package mode

import (
	"context"
	"fmt"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Resolver resolves a ModeRef to an immutable ModeTemplateSpec snapshot.
type Resolver struct {
	client client.Reader
}

// NewResolver creates a mode resolver backed by a Kubernetes client.
func NewResolver(c client.Reader) *Resolver {
	return &Resolver{client: c}
}

// Resolve takes a ModeRef and returns the resolved template spec.
// If ref is nil, returns nil (caller should use CompatAdapter).
// Always reads the live CRD to pick up template edits without a controller restart.
func (r *Resolver) Resolve(ctx context.Context, ref *platformv1alpha1.ModeRef, namespace string) (*platformv1alpha1.ModeTemplateSpec, error) {
	if ref == nil {
		return nil, nil
	}

	key := TemplateKey(ref.Name, ref.Version)
	// "chat" is a legacy pacing alias. Resolve it to autopilot so old stored
	// defaults and explicit mode refs adopt finish-gated autonomous execution.
	if key == "chat" {
		key = "autopilot"
	}

	var crd platformv1alpha1.ModeTemplate
	if err := r.client.Get(ctx, client.ObjectKey{Name: key}, &crd); err != nil {
		return nil, fmt.Errorf("resolve mode template %q: %w", key, err)
	}

	spec := crd.Spec.DeepCopy()
	spec.Autonomous = true
	return spec, nil
}

// ResolveOrDefault resolves a ModeRef, falling back to compat adapter for
// legacy runs that have no explicit modeRef.
func (r *Resolver) ResolveOrDefault(
	ctx context.Context,
	ref *platformv1alpha1.ModeRef,
	workflowMode platformv1alpha1.AgentRunWorkflowMode,
	executionMode platformv1alpha1.AgentRunExecutionMode,
	namespace string,
) (*platformv1alpha1.ModeTemplateSpec, error) {
	if ref != nil {
		return r.Resolve(ctx, ref, namespace)
	}
	tmpl := InferSystemTemplate(ctx, r.client, workflowMode, executionMode)
	return tmpl.DeepCopy(), nil
}
