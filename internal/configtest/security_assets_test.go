package configtest

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	triggersv1alpha1 "github.com/gratefulagents/gratefulagents/api/triggers/v1alpha1"
	"sigs.k8s.io/yaml"
)

// securityScanRoles are the specialist RoleInstructions shipped for security
// scans. The default scan workflow delegates to them by name, so a missing or
// renamed asset silently degrades every scan to the generic reviewer role.
var securityScanRoles = []string{
	"dependency-auditor",
	"exploit-validator",
	"finding-triager",
	"secrets-auditor",
	"threat-modeler",
	"vulnerability-hunter",
}

func repoPath(parts ...string) string {
	return filepath.Join(append([]string{"..", ".."}, parts...)...)
}

// readBootstrapAsset reads a configs/ asset, requires a byte-identical chart
// bootstrap mirror, and strictly decodes it into obj.
func readBootstrapAsset(t *testing.T, kindDir, name string, obj any) {
	t.Helper()
	sourcePath := repoPath("configs", kindDir, name+".yaml")
	source, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	mirrorPath := repoPath("dist", "chart", "files", "bootstrap", kindDir, name+".yaml")
	if mirror, err := os.ReadFile(mirrorPath); err != nil {
		t.Fatalf("chart bootstrap mirror missing for %s: %v", sourcePath, err)
	} else if !bytes.Equal(source, mirror) {
		t.Fatalf("%s and %s differ; keep the chart bootstrap copy byte-identical", sourcePath, mirrorPath)
	}
	if err := yaml.UnmarshalStrict(source, obj); err != nil {
		t.Fatalf("parse %s: %v", sourcePath, err)
	}
}

func TestSecurityScanModeTemplateAsset(t *testing.T) {
	t.Parallel()

	var mode platformv1alpha1.ModeTemplate
	readBootstrapAsset(t, "modetemplates", "security-scan", &mode)

	if mode.Name != "security-scan" || mode.Spec.Name != "security-scan" {
		t.Fatalf("mode template name = %q/%q, want security-scan", mode.Name, mode.Spec.Name)
	}
	if !mode.Spec.Autonomous {
		t.Error("scan mode must be autonomous")
	}
	// Scanned repositories are untrusted input: scan runs must never be able
	// to modify the workspace or push code.
	if mode.Spec.PermissionMode != platformv1alpha1.PermissionModeReadOnly {
		t.Errorf("permissionMode = %q, want read-only", mode.Spec.PermissionMode)
	}
	if mode.Spec.Constraints == nil || mode.Spec.Constraints.MaxConcurrentSubAgents < 1 {
		t.Fatal("scan mode must bound maxConcurrentSubAgents")
	}
	// The finding tools write platform scan state only, so they must survive
	// the read-only clamp or no finding can ever be recorded.
	wantTools := []string{"report_security_finding", "update_security_finding", "submit_security_scan_report"}
	for _, tool := range wantTools {
		found := false
		for _, allowed := range mode.Spec.AllowedMutatingTools {
			if allowed == tool {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("allowedMutatingTools must include %q, got %v", tool, mode.Spec.AllowedMutatingTools)
		}
	}
	for _, marker := range []string{"report_security_finding", "submit_security_scan_report", "file:line"} {
		if !strings.Contains(mode.Spec.Instructions, marker) {
			t.Errorf("scan mode instructions must mention %q", marker)
		}
	}
}

func TestSecurityScanRoleInstructionAssets(t *testing.T) {
	t.Parallel()

	for _, name := range securityScanRoles {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var role platformv1alpha1.RoleInstruction
			readBootstrapAsset(t, "roleinstructions", name, &role)

			if role.Name != name {
				t.Fatalf("metadata.name = %q, want %q", role.Name, name)
			}
			if strings.TrimSpace(role.Spec.Description) == "" {
				t.Error("role must carry a description for the specialist catalog")
			}
			if strings.TrimSpace(role.Spec.Instructions) == "" {
				t.Fatal("role must carry instructions")
			}
			// Every scan specialist reports through the finding tools rather
			// than prose, so the schema contract has to be in its prompt.
			for _, marker := range []string{"file_path", "severity", "attack_vector"} {
				if !strings.Contains(role.Spec.Instructions, marker) {
					t.Errorf("instructions must state the finding field %q", marker)
				}
			}
		})
	}
}

func TestSecurityScanSkillAsset(t *testing.T) {
	t.Parallel()

	var skill platformv1alpha1.Skill
	readBootstrapAsset(t, "skills", "security-scan", &skill)

	if skill.Name != "security-scan" {
		t.Fatalf("metadata.name = %q, want security-scan", skill.Name)
	}
	if skill.Spec.Source.Inline == nil || strings.TrimSpace(skill.Spec.Source.Inline.Instructions) == "" {
		t.Fatal("skill must ship inline instructions")
	}
	instructions := skill.Spec.Source.Inline.Instructions
	// The handbook must agree with the schema the tools actually enforce.
	for _, marker := range []string{"report_security_finding", "attack_vector", "confidence", "supply-chain", "path-traversal"} {
		if !strings.Contains(instructions, marker) {
			t.Errorf("skill instructions must cover %q", marker)
		}
	}
}

// TestDefaultSecurityWorkflowRolesHaveAssets keeps the built-in scan workflow
// and the shipped RoleInstruction assets from drifting apart.
func TestDefaultSecurityWorkflowRolesHaveAssets(t *testing.T) {
	t.Parallel()

	available := map[string]bool{triggersv1alpha1.DefaultSecurityScanRole: true}
	for _, name := range securityScanRoles {
		available[name] = true
	}
	for _, task := range triggersv1alpha1.DefaultSecurityWorkflow() {
		role := task.EffectiveRole()
		if !available[role] {
			t.Errorf("workflow task %q references role %q with no shipped RoleInstruction asset", task.Name, role)
		}
		if _, err := os.Stat(repoPath("configs", "roleinstructions", role+".yaml")); err != nil {
			t.Errorf("workflow task %q role %q: %v", task.Name, role, err)
		}
	}
}
