package triggers

import (
	"testing"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
)

// testModeExists is a ModeExistsFunc for tests backed by a fixed set.
func testModeExists(modes ...string) ModeExistsFunc {
	set := map[string]bool{}
	for _, m := range modes {
		set[m] = true
	}
	return func(name string) bool { return set[name] }
}

func TestResolveModeFromText(t *testing.T) {
	exists := testModeExists("ralplan", "research", "team-chat")
	tests := []struct {
		name string
		text string
		want string // empty = nil
	}{
		{"empty text", "", ""},
		{"no mode prefix", "fix the bug", ""},
		{"mode: prefix", "mode:ralplan fix the bug", "ralplan"},
		{"mode: prefix uppercase", "Mode:RALPLAN fix the bug", "ralplan"},
		{"slash prefix", "/ralplan fix the bug", "ralplan"},
		{"slash prefix no args", "/research", "research"},
		{"unknown mode", "mode:unknown fix things", ""},
		{"team-chat", "mode:team-chat do stuff", "team-chat"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ResolveModeFromText(tt.text, exists)
			if tt.want == "" {
				if got != nil {
					t.Errorf("ResolveModeFromText(%q) = %v, want nil", tt.text, got)
				}
			} else {
				if got == nil || got.Name != tt.want {
					t.Errorf("ResolveModeFromText(%q) = %v, want %s", tt.text, got, tt.want)
				}
			}
		})
	}
}

func TestStripModePrefix(t *testing.T) {
	exists := testModeExists("ralplan", "research")
	tests := []struct {
		name string
		text string
		want string
	}{
		{"no prefix", "fix the bug", "fix the bug"},
		{"mode: prefix", "mode:ralplan fix the bug", "fix the bug"},
		{"slash prefix", "/ralplan fix the bug", "fix the bug"},
		{"mode only", "/research", ""},
		{"unknown mode", "mode:unknown fix things", "mode:unknown fix things"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := StripModePrefix(tt.text, exists); got != tt.want {
				t.Errorf("StripModePrefix(%q) = %q, want %q", tt.text, got, tt.want)
			}
		})
	}
}

func TestResolveModeFromLabels(t *testing.T) {
	exists := testModeExists("ralplan", "tdd")
	tests := []struct {
		name   string
		labels []string
		want   string
	}{
		{"empty labels", nil, ""},
		{"no match", []string{"bug", "enhancement"}, ""},
		{"match", []string{"bug", "ralplan"}, "ralplan"},
		{"first match wins", []string{"tdd", "ralplan"}, "tdd"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ResolveModeFromLabels(tt.labels, exists)
			if tt.want == "" {
				if got != nil {
					t.Errorf("ResolveModeFromLabels() = %v, want nil", got)
				}
			} else if got == nil || got.Name != tt.want {
				t.Errorf("ResolveModeFromLabels() = %v, want %s", got, tt.want)
			}
		})
	}
}

func TestMergeModeRef(t *testing.T) {
	ref := func(name string) *platformv1alpha1.ModeRef { return &platformv1alpha1.ModeRef{Name: name} }
	tests := []struct {
		name                         string
		textMode, labelMode, defMode *platformv1alpha1.ModeRef
		want                         string
	}{
		{"all nil", nil, nil, nil, ""},
		{"text wins", ref("a"), ref("b"), ref("c"), "a"},
		{"label wins", nil, ref("b"), ref("c"), "b"},
		{"default wins", nil, nil, ref("c"), "c"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := MergeModeRef(tt.textMode, tt.labelMode, tt.defMode)
			if tt.want == "" {
				if got != nil {
					t.Errorf("MergeModeRef() = %v, want nil", got)
				}
			} else if got == nil || got.Name != tt.want {
				t.Errorf("MergeModeRef() = %v, want %s", got, tt.want)
			}
		})
	}
}

func TestPrefixModelWithProviderPreservesProviderNativeModelID(t *testing.T) {
	t.Parallel()

	if got := prefixModelWithProvider("z-ai/glm-4.7", "openrouter"); got != "openrouter/z-ai/glm-4.7" {
		t.Fatalf("prefixModelWithProvider() = %q, want %q", got, "openrouter/z-ai/glm-4.7")
	}
	if got := prefixModelWithProvider("openrouter/z-ai/glm-4.7", "openrouter"); got != "openrouter/z-ai/glm-4.7" {
		t.Fatalf("prefixModelWithProvider() duplicated provider prefix: %q", got)
	}
}
