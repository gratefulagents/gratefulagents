package configtest

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

// dashboardManagedResources are CRDs the dashboard creates and deletes on the
// user's behalf through its write RPCs. The reconciler markers for these kinds
// only cover get/list/watch/update/patch, so the manager ClusterRole must also
// carry create and delete — otherwise every dashboard "new resource" action
// fails at runtime with a Kubernetes forbidden error that no test catches.
var dashboardManagedResources = []string{
	"connections",
	"crons",
	"githubrepositories",
	"projects",
	"securitypolicypacks",
	"securitypostscripts",
	"securityrankers",
	"securityscans",
	"securityworkflows",
}

type clusterRoleDocument struct {
	Rules []struct {
		APIGroups []string `json:"apiGroups"`
		Resources []string `json:"resources"`
		Verbs     []string `json:"verbs"`
	} `json:"rules"`
}

// TestManagerRoleGrantsDashboardWriteVerbs fails when a dashboard-managed CRD
// is missing the write verbs its RPCs need. Add the resource to the marker in
// internal/controller/triggers/rbac_markers.go, regenerate config/rbac, and
// sync the chart copy.
func TestManagerRoleGrantsDashboardWriteVerbs(t *testing.T) {
	t.Parallel()

	data, err := os.ReadFile(filepath.Join("..", "..", "config", "rbac", "role.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var role clusterRoleDocument
	if err := yaml.Unmarshal(data, &role); err != nil {
		t.Fatalf("parse config/rbac/role.yaml: %v", err)
	}

	granted := map[string]map[string]bool{}
	for _, rule := range role.Rules {
		if !slices.Contains(rule.APIGroups, "triggers.gratefulagents.dev") {
			continue
		}
		for _, resource := range rule.Resources {
			if granted[resource] == nil {
				granted[resource] = map[string]bool{}
			}
			for _, verb := range rule.Verbs {
				granted[resource][verb] = true
			}
		}
	}

	for _, resource := range dashboardManagedResources {
		for _, verb := range []string{"create", "delete", "get", "list", "watch", "update", "patch"} {
			if !granted[resource][verb] {
				t.Errorf("manager role is missing verb %q on %q; the dashboard cannot manage it", verb, resource)
			}
		}
	}
}

// TestChartManagerRoleMatchesGeneratedRules keeps the shipped Helm ClusterRole
// from drifting from the generated one, so a regenerated role that is not
// synced into dist/chart cannot pass review.
func TestChartManagerRoleMatchesGeneratedRules(t *testing.T) {
	t.Parallel()

	generated, err := os.ReadFile(filepath.Join("..", "..", "config", "rbac", "role.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	chart, err := os.ReadFile(filepath.Join("..", "..", "dist", "chart", "templates", "rbac", "manager-role.yaml"))
	if err != nil {
		t.Fatal(err)
	}

	var generatedDoc, chartDoc clusterRoleDocument
	if err := yaml.Unmarshal(generated, &generatedDoc); err != nil {
		t.Fatalf("parse config/rbac/role.yaml: %v", err)
	}
	// The chart copy templates metadata.name, so compare only the rules.
	if err := yaml.Unmarshal(chartRulesOnly(t, chart), &chartDoc); err != nil {
		t.Fatalf("parse dist/chart/templates/rbac/manager-role.yaml: %v", err)
	}

	if len(generatedDoc.Rules) != len(chartDoc.Rules) {
		t.Fatalf("chart manager role has %d rules, generated role has %d; regenerate and sync the chart copy",
			len(chartDoc.Rules), len(generatedDoc.Rules))
	}
	for i := range generatedDoc.Rules {
		got, want := chartDoc.Rules[i], generatedDoc.Rules[i]
		if !slices.Equal(got.APIGroups, want.APIGroups) ||
			!slices.Equal(got.Resources, want.Resources) ||
			!slices.Equal(got.Verbs, want.Verbs) {
			t.Fatalf("chart manager role rule %d differs from the generated role; regenerate and sync the chart copy", i)
		}
	}
}

// chartRulesOnly returns the chart ClusterRole from its rules section onward,
// dropping the templated metadata so the document parses as plain YAML.
func chartRulesOnly(t *testing.T, chart []byte) []byte {
	t.Helper()
	text := string(chart)
	index := strings.Index(text, "\nrules:")
	if index < 0 {
		t.Fatal("chart manager role has no rules section")
	}
	return []byte(text[index+1:])
}
