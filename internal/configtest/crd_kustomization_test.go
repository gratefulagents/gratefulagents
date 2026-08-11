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

// requiredCRDs are the kinds whose absence would silently break a controller
// that cmd/main.go starts unconditionally. The generic parity tests above
// cannot catch a CRD that was never generated in the first place — a
// hand-deleted or never-committed base leaves both sides consistently empty —
// so the kinds the platform depends on are listed explicitly.
var requiredCRDs = []struct {
	base  string
	chart string
}{
	{
		base:  "platform.gratefulagents.dev_securitytoolruns.yaml",
		chart: "securitytoolruns.platform.gratefulagents.dev.yaml",
	},
	{
		base:  "triggers.gratefulagents.dev_securityscans.yaml",
		chart: "securityscans.triggers.gratefulagents.dev.yaml",
	},
	{
		base:  "triggers.gratefulagents.dev_securityprograms.yaml",
		chart: "securityprograms.triggers.gratefulagents.dev.yaml",
	},
}

func TestRequiredCRDsAreGeneratedAndShipped(t *testing.T) {
	t.Parallel()
	root := filepath.Clean(filepath.Join("..", ".."))
	data, err := os.ReadFile(filepath.Join(root, "config", "crd", "kustomization.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	kustomization := string(data)
	for _, required := range requiredCRDs {
		if _, err := os.Stat(filepath.Join(root, "config", "crd", "bases", required.base)); err != nil {
			t.Errorf("config/crd/bases/%s is missing; regenerate the CRDs: %v", required.base, err)
		}
		if !strings.Contains(kustomization, "- bases/"+required.base) {
			t.Errorf("config/crd/kustomization.yaml is missing resource %q", "bases/"+required.base)
		}
		if _, err := os.Stat(filepath.Join(root, "dist", "chart", "templates", "crd", required.chart)); err != nil {
			t.Errorf("dist/chart/templates/crd/%s is missing; sync the chart copy: %v", required.chart, err)
		}
	}
}
