package configtest

import (
	"os"
	"slices"
	"testing"

	"sigs.k8s.io/yaml"
)

func TestSecurityScanStatusProgramSnapshotSupportsWebTargets(t *testing.T) {
	t.Parallel()

	data, err := os.ReadFile("../../config/crd/bases/triggers.gratefulagents.dev_securityscans.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var crd map[string]any
	if err := yaml.Unmarshal(data, &crd); err != nil {
		t.Fatalf("parse SecurityScan CRD: %v", err)
	}

	version := crdMapSlice(t, crd, "spec", "versions")[0]
	snapshot := crdMap(t, version, "schema", "openAPIV3Schema", "properties", "status", "properties", "lastExecution", "properties", "securityProgramSnapshot")
	for _, field := range []string{"scanTarget", "scanTargets"} {
		t.Run(field, func(t *testing.T) {
			targetSchema := crdMap(t, snapshot, "properties", field)
			if field == "scanTargets" {
				targetSchema = crdMap(t, targetSchema, "items")
			}
			properties := crdMap(t, targetSchema, "properties")
			if _, ok := properties["targetURL"]; !ok {
				t.Fatal("targetURL is missing from the embedded SecurityProgram scan target schema")
			}
			required, _ := targetSchema["required"].([]any)
			requiredNames := make([]string, 0, len(required))
			for _, value := range required {
				name, ok := value.(string)
				if !ok {
					t.Fatalf("required entry has type %T, want string", value)
				}
				requiredNames = append(requiredNames, name)
			}
			for _, urlField := range []string{"repositoryURL", "targetURL"} {
				if slices.Contains(requiredNames, urlField) {
					t.Fatalf("%s must remain optional because exactly one URL kind is allowed", urlField)
				}
			}

			const xorRule = "(has(self.repositoryURL) && self.repositoryURL.size() > 0) != (has(self.targetURL) && self.targetURL.size() > 0)"
			validations, ok := targetSchema["x-kubernetes-validations"].([]any)
			if !ok {
				t.Fatalf("x-kubernetes-validations has type %T, want array", targetSchema["x-kubernetes-validations"])
			}
			foundXOR := false
			for _, value := range validations {
				validation, ok := value.(map[string]any)
				if !ok {
					t.Fatalf("validation entry has type %T, want object", value)
				}
				if validation["rule"] == xorRule {
					foundXOR = true
				}
			}
			if !foundXOR {
				t.Fatal("embedded scan target schema is missing the repositoryURL/targetURL XOR validation")
			}
		})
	}
}

func crdMap(t *testing.T, root map[string]any, path ...string) map[string]any {
	t.Helper()
	current := root
	for _, key := range path {
		next, ok := current[key].(map[string]any)
		if !ok {
			t.Fatalf("CRD path %q has type %T, want object", key, current[key])
		}
		current = next
	}
	return current
}

func crdMapSlice(t *testing.T, root map[string]any, path ...string) []map[string]any {
	t.Helper()
	current := crdMap(t, root, path[:len(path)-1]...)
	values, ok := current[path[len(path)-1]].([]any)
	if !ok {
		t.Fatalf("CRD path %q has type %T, want array", path[len(path)-1], current[path[len(path)-1]])
	}
	result := make([]map[string]any, 0, len(values))
	for _, value := range values {
		item, ok := value.(map[string]any)
		if !ok {
			t.Fatalf("CRD array entry has type %T, want object", value)
		}
		result = append(result, item)
	}
	return result
}
