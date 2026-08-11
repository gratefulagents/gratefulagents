package configtest

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"sigs.k8s.io/yaml"
)

func TestSecurityProgramLibrary(t *testing.T) {
	t.Parallel()

	paths, err := filepath.Glob(repoPath("configs", "securityprograms", "*.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) < 10 || len(paths) > 20 {
		t.Fatalf("security program count = %d, want 10..20", len(paths))
	}

	seen := make(map[string]struct{}, len(paths))
	for _, sourcePath := range paths {
		sourcePath := sourcePath
		t.Run(strings.TrimSuffix(filepath.Base(sourcePath), ".yaml"), func(t *testing.T) {
			source, err := os.ReadFile(sourcePath)
			if err != nil {
				t.Fatal(err)
			}
			mirrorPath := repoPath("dist", "chart", "files", "bootstrap", "securityprograms", filepath.Base(sourcePath))
			mirror, err := os.ReadFile(mirrorPath)
			if err != nil {
				t.Fatalf("chart bootstrap mirror missing: %v", err)
			}
			if !bytes.Equal(source, mirror) {
				t.Fatalf("%s and %s differ", sourcePath, mirrorPath)
			}

			var program triggersv1alpha1.SecurityProgram
			if err := yaml.UnmarshalStrict(source, &program); err != nil {
				t.Fatalf("parse %s: %v", sourcePath, err)
			}
			if program.APIVersion != "triggers.gratefulagents.dev/v1alpha1" || program.Kind != "SecurityProgram" {
				t.Fatalf("unexpected type metadata %q/%q", program.APIVersion, program.Kind)
			}
			if _, ok := seen[program.Name]; ok {
				t.Fatalf("duplicate metadata.name %q", program.Name)
			}
			seen[program.Name] = struct{}{}
			if errs := triggersv1alpha1.ValidateSecurityProgramSpec(program.Spec); len(errs) != 0 {
				t.Fatalf("invalid spec: %v", errs)
			}
			if program.Spec.Provider != "Immunefi" || !strings.HasPrefix(program.Spec.ProgramURL, "https://immunefi.com/bug-bounty/") {
				t.Fatalf("unexpected provider or provenance URL: %q %q", program.Spec.Provider, program.Spec.ProgramURL)
			}
			for _, marker := range []string{"Repository targets:", "https://github.com/", "Rewards:", "Eligible impacts:", "Out of scope:", "Testing and submission:"} {
				if !strings.Contains(program.Spec.ScopePolicy, marker) {
					t.Errorf("scopePolicy missing %q", marker)
				}
			}
		})
	}
}
