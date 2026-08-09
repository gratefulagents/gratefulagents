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

// reconcilerRequiredRules are the API access the controllers themselves need
// at runtime, kind by kind. Unlike the dashboard table above these are not
// about write RPCs: a missing verb here means a reconcile loop fails against a
// live API server with a forbidden error that no unit test using a fake client
// can catch. The SecurityToolRun reconciler runs one Kubernetes Job per
// request and mounts its typed configuration from a ConfigMap, so it needs the
// batch and core verbs as well as its own kind.
var reconcilerRequiredRules = []struct {
	apiGroup string
	resource string
	verbs    []string
}{
	{
		apiGroup: "platform.gratefulagents.dev",
		resource: "securitytoolruns",
		verbs:    []string{"get", "list", "watch", "create", "update", "patch", "delete"},
	},
	{
		apiGroup: "platform.gratefulagents.dev",
		resource: "securitytoolruns/status",
		verbs:    []string{"get", "update", "patch"},
	},
	{
		apiGroup: "batch",
		resource: "jobs",
		verbs:    []string{"create", "get", "list", "watch", "delete"},
	},
	{
		apiGroup: "",
		resource: "configmaps",
		verbs:    []string{"create", "get", "list", "watch"},
	},
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

	granted := grantedVerbs(role, "triggers.gratefulagents.dev")

	for _, resource := range dashboardManagedResources {
		for _, verb := range []string{"create", "delete", "get", "list", "watch", "update", "patch"} {
			if !granted[resource][verb] {
				t.Errorf("manager role is missing verb %q on %q; the dashboard cannot manage it", verb, resource)
			}
		}
	}
}

// TestManagerRoleGrantsReconcilerVerbs fails when a controller is missing API
// access its reconcile loop performs. Add the verb to the marker in the
// controller package, regenerate config/rbac, and sync the chart copy.
func TestManagerRoleGrantsReconcilerVerbs(t *testing.T) {
	t.Parallel()

	role := readManagerRole(t)
	for _, required := range reconcilerRequiredRules {
		granted := grantedVerbs(role, required.apiGroup)
		for _, verb := range required.verbs {
			if !granted[required.resource][verb] {
				t.Errorf("manager role is missing verb %q on %q (apiGroup %q); the reconciler cannot run",
					verb, required.resource, required.apiGroup)
			}
		}
	}
}

// grantedVerbs collects the verbs the ClusterRole grants per resource within
// one API group.
func grantedVerbs(role clusterRoleDocument, apiGroup string) map[string]map[string]bool {
	granted := map[string]map[string]bool{}
	for _, rule := range role.Rules {
		if !slices.Contains(rule.APIGroups, apiGroup) {
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
	return granted
}

func readManagerRole(t *testing.T) clusterRoleDocument {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "config", "rbac", "role.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var role clusterRoleDocument
	if err := yaml.Unmarshal(data, &role); err != nil {
		t.Fatalf("parse config/rbac/role.yaml: %v", err)
	}
	return role
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
