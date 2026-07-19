package triggers

import (
	"context"
	"strings"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ModeExistsFunc returns true if a mode template with the given spec.name exists
// in the cluster. Callers build this from a k8s client.Reader.
type ModeExistsFunc func(name string) bool

// ModeExistsFromK8s returns a ModeExistsFunc that checks the cluster for a
// ModeTemplate CRD with the given name. Results are cached within the
// returned function so repeated lookups for the same name are free.
func ModeExistsFromK8s(ctx context.Context, c client.Reader) ModeExistsFunc {
	cache := map[string]bool{}
	return func(name string) bool {
		if hit, ok := cache[name]; ok {
			return hit
		}
		var crd platformv1alpha1.ModeTemplate
		exists := c.Get(ctx, client.ObjectKey{Name: name}, &crd) == nil
		cache[name] = exists
		return exists
	}
}

// ResolveModeFromText parses text for a mode prefix like "mode:NAME" or "/NAME".
// Returns a ModeRef if found, nil otherwise.
func ResolveModeFromText(text string, modeExists ModeExistsFunc) *platformv1alpha1.ModeRef {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return nil
	}

	// Check for "mode:NAME ..." prefix.
	if strings.HasPrefix(strings.ToLower(trimmed), "mode:") {
		rest := trimmed[5:]
		parts := strings.SplitN(rest, " ", 2)
		name := strings.ToLower(strings.TrimSpace(parts[0]))
		if modeExists(name) {
			return &platformv1alpha1.ModeRef{Name: name}
		}
	}

	// Check for "/NAME ..." prefix.
	if strings.HasPrefix(trimmed, "/") {
		rest := trimmed[1:]
		parts := strings.SplitN(rest, " ", 2)
		name := strings.ToLower(strings.TrimSpace(parts[0]))
		if modeExists(name) {
			return &platformv1alpha1.ModeRef{Name: name}
		}
	}

	return nil
}

// StripModePrefix removes a mode prefix ("mode:NAME " or "/NAME ") from text.
// Returns the original text if no mode prefix is found.
func StripModePrefix(text string, modeExists ModeExistsFunc) string {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return text
	}

	lower := strings.ToLower(trimmed)
	if strings.HasPrefix(lower, "mode:") {
		rest := trimmed[5:]
		parts := strings.SplitN(rest, " ", 2)
		name := strings.ToLower(strings.TrimSpace(parts[0]))
		if modeExists(name) {
			if len(parts) > 1 {
				return strings.TrimSpace(parts[1])
			}
			return ""
		}
	}

	if strings.HasPrefix(trimmed, "/") {
		rest := trimmed[1:]
		parts := strings.SplitN(rest, " ", 2)
		name := strings.ToLower(strings.TrimSpace(parts[0]))
		if modeExists(name) {
			if len(parts) > 1 {
				return strings.TrimSpace(parts[1])
			}
			return ""
		}
	}

	return text
}

// ResolveModeFromLabels scans a list of labels for a known mode template name.
// Returns the first match as a ModeRef, nil if none found.
func ResolveModeFromLabels(labels []string, modeExists ModeExistsFunc) *platformv1alpha1.ModeRef {
	for _, label := range labels {
		name := strings.ToLower(strings.TrimSpace(label))
		if modeExists(name) {
			return &platformv1alpha1.ModeRef{Name: name}
		}
	}
	return nil
}

// MergeModeRef returns the highest-priority non-nil ModeRef.
// Priority: textMode > labelMode > defaultMode.
func MergeModeRef(textMode, labelMode, defaultMode *platformv1alpha1.ModeRef) *platformv1alpha1.ModeRef {
	if textMode != nil {
		return textMode
	}
	if labelMode != nil {
		return labelMode
	}
	return defaultMode
}

// prefixModelWithProvider normalizes a model name for storage on an AgentRun.
// When the provider is "openai" (the default), the model is stored bare
// (e.g. "gpt-5.4"), stripping any "openai/" prefix the user may have set.
// For non-openai providers the "provider/" prefix is added when absent.
func prefixModelWithProvider(model, provider string) string {
	return triggersv1alpha1.PrefixModelWithProvider(model, provider)
}
