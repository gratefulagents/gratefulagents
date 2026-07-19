package dashboard

import (
	"strings"
	"testing"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	"github.com/gratefulagents/gratefulagents/rpc/platform"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestMCPServerInfoIncludesNetworkOptIn(t *testing.T) {
	srv := &platformv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: "grafana"},
		Spec: platformv1alpha1.MCPServerSpec{
			MCPServerConfig: &platformv1alpha1.MCPServerConfig{
				Command:      "mcp-grafana",
				AllowNetwork: true,
			},
		},
	}

	info := mcpServerInfo(srv)
	if !info.GetAllowNetwork() {
		t.Fatal("AllowNetwork = false, want true")
	}
}

func TestMCPServerSecretEnvSpecsRequiresUserCredSecret(t *testing.T) {
	_, err := mcpServerSecretEnvSpecs([]*platform.MCPServerSecretEnv{
		{Name: "SOME_TOKEN", SecretName: "kube-root-ca", SecretKey: "token"},
	})
	if err == nil || !strings.Contains(err.Error(), "usercred-") {
		t.Fatalf("expected usercred- allowlist error, got %v", err)
	}

	out, err := mcpServerSecretEnvSpecs([]*platform.MCPServerSecretEnv{
		{Name: "SOME_TOKEN", SecretName: "usercred-grafana", SecretKey: "token", Required: true},
	})
	if err != nil {
		t.Fatalf("valid usercred secret rejected: %v", err)
	}
	if len(out) != 1 || out[0].SecretName != "usercred-grafana" || out[0].Optional == nil || *out[0].Optional {
		t.Fatalf("unexpected specs: %+v", out)
	}
}

func TestValidateMCPServerPlainEnvRejectsCredentialKeys(t *testing.T) {
	if err := validateMCPServerPlainEnv(map[string]string{"GRAFANA_URL": "https://g.example"}); err != nil {
		t.Fatalf("benign env rejected: %v", err)
	}
	err := validateMCPServerPlainEnv(map[string]string{"GRAFANA_SERVICE_ACCOUNT_TOKEN": "glsa_x"})
	if err == nil || !strings.Contains(err.Error(), "secretEnv") {
		t.Fatalf("credential env key not rejected: %v", err)
	}
}
