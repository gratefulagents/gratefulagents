package mode

import (
	"context"
	"fmt"
	"log"
	"sync"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Registry is a thread-safe store of ModeTemplateSpec keyed by name-version.
// It replaces the former hardcoded SystemTemplates map.
// Templates are loaded from ModeTemplate CRDs in the cluster.
type Registry struct {
	mu        sync.RWMutex
	templates map[string]*platformv1alpha1.ModeTemplateSpec
}

// NewRegistry creates an empty registry.
func NewRegistry() *Registry {
	return &Registry{templates: make(map[string]*platformv1alpha1.ModeTemplateSpec)}
}

// Get returns a deep copy of the template for the given key, or nil if not found.
func (r *Registry) Get(key string) (*platformv1alpha1.ModeTemplateSpec, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	tmpl, ok := r.templates[key]
	if !ok {
		return nil, false
	}
	return tmpl.DeepCopy(), true
}

// Has returns true if the registry contains the given key.
func (r *Registry) Has(key string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.templates[key]
	return ok
}

// All returns a snapshot of all registered templates (deep copies).
func (r *Registry) All() map[string]*platformv1alpha1.ModeTemplateSpec {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[string]*platformv1alpha1.ModeTemplateSpec, len(r.templates))
	for k, v := range r.templates {
		out[k] = v.DeepCopy()
	}
	return out
}

// Register adds or replaces a template in the registry.
func (r *Registry) Register(key string, spec *platformv1alpha1.ModeTemplateSpec) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.templates[key] = spec
}

// Len returns the number of registered templates.
func (r *Registry) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.templates)
}

// GetOrLoad returns the template for key, falling back to a direct CRD lookup
// if the template is not already cached. On a successful CRD lookup the result
// is cached so subsequent calls are fast.
func (r *Registry) GetOrLoad(ctx context.Context, c client.Reader, key string) (*platformv1alpha1.ModeTemplateSpec, bool) {
	if tmpl, ok := r.Get(key); ok {
		return tmpl, true
	}

	// Direct CRD lookup — works even if the bulk load hasn't run yet.
	var crd platformv1alpha1.ModeTemplate
	if err := c.Get(ctx, client.ObjectKey{Name: key}, &crd); err != nil {
		return nil, false
	}

	spec := crd.Spec.DeepCopy()
	r.Register(key, spec)
	return spec.DeepCopy(), true
}

// LoadFromK8s lists all cluster-scoped ModeTemplate CRDs and populates the registry.
func (r *Registry) LoadFromK8s(ctx context.Context, c client.Reader) error {
	var list platformv1alpha1.ModeTemplateList
	if err := c.List(ctx, &list); err != nil {
		return fmt.Errorf("list ModeTemplate CRDs: %w", err)
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	for i := range list.Items {
		spec := list.Items[i].Spec.DeepCopy()
		key := TemplateKey(spec.Name, spec.Version)
		r.templates[key] = spec
	}

	log.Printf("Loaded %d ModeTemplate CRDs into registry", len(list.Items))
	return nil
}

// DefaultRegistry is the global registry used by all mode operations.
// Call DefaultRegistry.LoadFromK8s() at startup to populate it from CRDs.
var DefaultRegistry = NewRegistry()
