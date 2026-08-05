package configtest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestKustomizationReferencesAllCRDBases guards against a generated CRD that
// never ships: cmd/main.go starts every controller unconditionally, so a CRD
// base missing from config/crd/kustomization.yaml leaves kustomize/config
// installs without the resource the controller needs.
func TestKustomizationReferencesAllCRDBases(t *testing.T) {
	t.Parallel()
	root := filepath.Clean(filepath.Join("..", ".."))
	basePaths, err := filepath.Glob(filepath.Join(root, "config", "crd", "bases", "*.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(basePaths) == 0 {
		t.Fatal("no generated CRD bases found under config/crd/bases")
	}
	data, err := os.ReadFile(filepath.Join(root, "config", "crd", "kustomization.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	kustomization := string(data)
	for _, basePath := range basePaths {
		ref := "bases/" + filepath.Base(basePath)
		if !strings.Contains(kustomization, "- "+ref) {
			t.Errorf("config/crd/kustomization.yaml is missing resource %q", ref)
		}
	}
}
