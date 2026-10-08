package configtest

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	"github.com/gratefulagents/gratefulagents/internal/agentroles"
	"sigs.k8s.io/yaml"
)

func shippedRoleInstructions(t *testing.T) map[string]platformv1alpha1.RoleInstruction {
	t.Helper()
	root := filepath.Join("..", "..")
	sources, err := filepath.Glob(filepath.Join(root, "configs", "roleinstructions", "*.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	mirrors, err := filepath.Glob(filepath.Join(root, "dist", "chart", "files", "bootstrap", "roleinstructions", "*.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) != len(mirrors) {
		t.Fatalf("configs ships %d roles but the chart mirrors %d; run make helm-sync-bootstrap", len(sources), len(mirrors))
	}

	roles := make(map[string]platformv1alpha1.RoleInstruction, len(sources))
	for _, sourcePath := range sources {
		mirrorPath := filepath.Join(root, "dist", "chart", "files", "bootstrap", "roleinstructions", filepath.Base(sourcePath))
		source, err := os.ReadFile(sourcePath)
		if err != nil {
			t.Fatal(err)
		}
		mirror, err := os.ReadFile(mirrorPath)
		if err != nil {
			t.Fatalf("%s has no chart mirror: %v", sourcePath, err)
		}
		if !bytes.Equal(source, mirror) {
			t.Fatalf("%s and %s differ; run make helm-sync-bootstrap", sourcePath, mirrorPath)
		}
		var role platformv1alpha1.RoleInstruction
		if err := yaml.UnmarshalStrict(source, &role); err != nil {
			t.Fatalf("parse %s: %v", sourcePath, err)
		}
		if want := strings.TrimSuffix(filepath.Base(sourcePath), ".yaml"); role.Name != want {
			t.Fatalf("%s declares role %q, want %q", sourcePath, role.Name, want)
		}
		roles[role.Name] = role
	}
	return roles
}

func TestShippedRoleInstructionsInheritParentModel(t *testing.T) {
	for name, role := range shippedRoleInstructions(t) {
		// A pinned model stays behind when the parent run moves to a newer
		// one, so shipped roles leave model choice to the parent.
		if role.Spec.Model != "" || len(role.Spec.ModelsByProvider) > 0 {
			t.Errorf("shipped role %s pins a model; shipped roles must inherit the parent run's model", name)
		}
		if strings.TrimSpace(role.Spec.Description) == "" || strings.TrimSpace(role.Spec.Instructions) == "" {
			t.Errorf("shipped role %s needs a description and instructions", name)
		}
	}
}

func TestShippedGeneralRoleMatchesBuiltin(t *testing.T) {
	general, ok := shippedRoleInstructions(t)[agentroles.GeneralName]
	if !ok {
		t.Fatalf("configs/roleinstructions must ship %s.yaml so the dashboard can show and edit it", agentroles.GeneralName)
	}
	if general.Spec.Description != agentroles.GeneralDescription {
		t.Errorf("general.yaml description = %q, want agentroles.GeneralDescription %q", general.Spec.Description, agentroles.GeneralDescription)
	}
	if got := strings.TrimSpace(general.Spec.Instructions); got != agentroles.GeneralInstructions {
		t.Errorf("general.yaml instructions = %q, want agentroles.GeneralInstructions %q", got, agentroles.GeneralInstructions)
	}
	if general.Spec.ToolAccess != agentroles.GeneralToolAccess {
		t.Errorf("general.yaml toolAccess = %q, want %q", general.Spec.ToolAccess, agentroles.GeneralToolAccess)
	}
}

func TestRetiredRolesAreNotShipped(t *testing.T) {
	roles := shippedRoleInstructions(t)
	for _, name := range agentroles.RetiredBootstrapNames {
		if _, ok := roles[name]; ok {
			t.Errorf("role %s is retired, and the manager deletes its seeded copy on startup; remove it from agentroles.RetiredBootstrapNames before shipping it again", name)
		}
	}
	if !slices.IsSorted(agentroles.RetiredBootstrapNames) {
		t.Error("agentroles.RetiredBootstrapNames must stay sorted")
	}
}
