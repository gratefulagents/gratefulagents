package tools

import (
	"os"
	"reflect"
	"strings"
	"testing"

	rbacv1 "k8s.io/api/rbac/v1"
	"sigs.k8s.io/yaml"
)

const helmManagerRoleNameTemplate = `{{ include "gratefulagents.resourceName" (dict "suffix" "manager-role" "context" $) }}`

// TestManagerRoleHelmParity prevents the separately packaged Helm ClusterRole
// from drifting from controller-gen's canonical manager role. The Helm copy
// only differs in its templated metadata.name and indentation.
func TestManagerRoleHelmParity(t *testing.T) {
	generatedBytes, err := os.ReadFile("../../config/rbac/role.yaml")
	if err != nil {
		t.Fatalf("read generated manager role: %v", err)
	}
	helmBytes, err := os.ReadFile("../../dist/chart/templates/rbac/manager-role.yaml")
	if err != nil {
		t.Fatalf("read Helm manager role: %v", err)
	}

	var generated, chart rbacv1.ClusterRole
	if err := yaml.Unmarshal(generatedBytes, &generated); err != nil {
		t.Fatalf("parse generated manager role: %v", err)
	}
	renderedHelm := strings.ReplaceAll(string(helmBytes), helmManagerRoleNameTemplate, "manager-role")
	if err := yaml.Unmarshal([]byte(renderedHelm), &chart); err != nil {
		t.Fatalf("parse Helm manager role: %v", err)
	}
	if !reflect.DeepEqual(generated.Rules, chart.Rules) {
		t.Fatal("Helm manager-role rules differ from config/rbac/role.yaml; regenerate manifests and sync dist/chart/templates/rbac/manager-role.yaml")
	}
}
