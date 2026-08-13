package mcpattach

import (
	"regexp"
	"testing"
)

func TestSecretEnvPodNameIsStableValidAndScoped(t *testing.T) {
	got := SecretEnvPodName("lf-prod-grafana", "GRAFANA_URL")
	if got != SecretEnvPodName("lf-prod-grafana", "GRAFANA_URL") {
		t.Fatal("pod env name is not stable")
	}
	if !regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`).MatchString(got) {
		t.Fatalf("pod env name is invalid: %q", got)
	}
	if got == SecretEnvPodName("lf-dev-grafana", "GRAFANA_URL") {
		t.Fatal("different servers produced the same pod env name")
	}
	if got == SecretEnvPodName("lf-prod-grafana", "grafana-url") {
		t.Fatal("different declared env names produced the same pod env name")
	}
}
