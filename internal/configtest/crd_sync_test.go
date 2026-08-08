package configtest

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

func TestChartCRDsMatchGeneratedBases(t *testing.T) {
	t.Parallel()
	root := filepath.Clean(filepath.Join("..", ".."))
	basePaths, err := filepath.Glob(filepath.Join(root, "config", "crd", "bases", "*.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	chartPaths, err := filepath.Glob(filepath.Join(root, "dist", "chart", "templates", "crd", "*.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(basePaths) == 0 || len(chartPaths) == 0 {
		t.Fatalf("expected generated and chart CRDs, found %d and %d", len(basePaths), len(chartPaths))
	}

	chartByName := make(map[string]string, len(chartPaths))
	for _, path := range chartPaths {
		doc := readCRDDocument(t, path, true)
		name := crdName(t, path, doc)
		chartByName[name] = path
	}

	var missing, drifted []string
	for _, basePath := range basePaths {
		base := readCRDDocument(t, basePath, false)
		name := crdName(t, basePath, base)
		chartPath, ok := chartByName[name]
		if !ok {
			missing = append(missing, name)
			continue
		}
		chart := readCRDDocument(t, chartPath, true)
		if !reflect.DeepEqual(base, chart) {
			drifted = append(drifted, fmt.Sprintf("%s (%s)", name, firstCRDDifference(base, chart, "")))
		}
		delete(chartByName, name)
	}
	for name := range chartByName {
		missing = append(missing, "generated base for "+name)
	}
	sort.Strings(missing)
	sort.Strings(drifted)
	if len(missing) != 0 || len(drifted) != 0 {
		t.Fatalf("Helm CRDs are out of sync with config/crd/bases (missing=%v drifted=%v); regenerate dist/chart/templates/crd and commit the result", missing, drifted)
	}
}

func readCRDDocument(t *testing.T, path string, chartTemplate bool) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := strings.TrimSpace(string(data))
	if chartTemplate {
		lines := strings.Split(text, "\n")
		if len(lines) < 3 || strings.TrimSpace(lines[0]) != "{{- if .Values.crd.enable }}" || strings.TrimSpace(lines[len(lines)-1]) != "{{- end }}" {
			t.Fatalf("%s: expected the standard CRD Helm wrapper", path)
		}
		text = strings.Join(lines[1:len(lines)-1], "\n")
		if strings.Contains(text, "{{") || strings.Contains(text, "}}") {
			t.Fatalf("%s: CRD content contains an unintended Helm template delimiter", path)
		}
	}
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(text), &doc); err != nil {
		t.Fatalf("%s: parse CRD: %v", path, err)
	}
	metadata, _ := doc["metadata"].(map[string]any)
	annotations, _ := metadata["annotations"].(map[string]any)
	delete(annotations, "helm.sh/resource-policy")
	// Normalize through JSON so YAML number representations compare consistently.
	normalized, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(normalized, &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

func firstCRDDifference(want, got any, path string) string {
	if reflect.DeepEqual(want, got) {
		return ""
	}
	switch typedWant := want.(type) {
	case map[string]any:
		typedGot, ok := got.(map[string]any)
		if !ok {
			return path
		}
		keys := make([]string, 0, len(typedWant)+len(typedGot))
		seen := map[string]bool{}
		for key := range typedWant {
			keys = append(keys, key)
			seen[key] = true
		}
		for key := range typedGot {
			if !seen[key] {
				keys = append(keys, key)
			}
		}
		sort.Strings(keys)
		for _, key := range keys {
			child := key
			if path != "" {
				child = path + "." + key
			}
			if difference := firstCRDDifference(typedWant[key], typedGot[key], child); difference != "" {
				return difference
			}
		}
	case []any:
		typedGot, ok := got.([]any)
		if !ok || len(typedWant) != len(typedGot) {
			return path
		}
		for i := range typedWant {
			if difference := firstCRDDifference(typedWant[i], typedGot[i], fmt.Sprintf("%s[%d]", path, i)); difference != "" {
				return difference
			}
		}
	default:
		return path
	}
	return path
}

func crdName(t *testing.T, path string, doc map[string]any) string {
	t.Helper()
	metadata, ok := doc["metadata"].(map[string]any)
	if !ok {
		t.Fatalf("%s: missing metadata", path)
	}
	name, ok := metadata["name"].(string)
	if !ok || strings.TrimSpace(name) == "" {
		t.Fatalf("%s: missing metadata.name", path)
	}
	return name
}
