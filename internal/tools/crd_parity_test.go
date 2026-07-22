package tools

import (
	"os"
	"strings"
	"testing"
)

func TestMaintainerWorkItemCRDHelmParity(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"maintainerworkitems", "maintainerworkitemcommands", "pullrequestmonitors"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			generated, err := os.ReadFile("../../config/crd/bases/triggers.gratefulagents.dev_" + name + ".yaml")
			if err != nil {
				t.Fatalf("read generated CRD: %v", err)
			}
			chart, err := os.ReadFile("../../dist/chart/templates/crd/" + name + ".triggers.gratefulagents.dev.yaml")
			if err != nil {
				t.Fatalf("read Helm CRD: %v", err)
			}
			wrapped := strings.TrimSpace(string(chart))
			wrapped = strings.TrimPrefix(wrapped, "{{- if .Values.crd.enable }}")
			wrapped = strings.TrimSuffix(wrapped, "{{- end }}")
			// The chart adds a Helm-only keep policy so uninstall/reinstall and
			// crd.enable toggles cannot delete durable maintainer state.
			if !strings.Contains(wrapped, "helm.sh/resource-policy: keep") {
				t.Fatal("Helm CRD copy must carry helm.sh/resource-policy: keep to protect durable maintainer state")
			}
			wrapped = strings.ReplaceAll(wrapped, "\n    helm.sh/resource-policy: keep", "")
			if strings.TrimSpace(string(generated)) != strings.TrimSpace(wrapped) {
				t.Fatalf("Helm %s CRD differs from generated base; regenerate and sync the chart copy", name)
			}
			if name == "maintainerworkitems" && !strings.Contains(string(generated), "!has(self.disposition) || self.disposition !=") {
				t.Fatal("pending work-item CRD validation does not guard the optional disposition")
			}
		})
	}
}
