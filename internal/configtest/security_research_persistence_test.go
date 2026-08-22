package configtest

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

func TestBlockchainResearchAssetsRequireDurablePersistence(t *testing.T) {
	t.Parallel()

	assets := []struct {
		path  string
		tools []string
	}{
		{
			path: "../../configs/skills/blockchain-security-research-method.yaml",
			tools: []string{
				"get_security_research_context", "amend_security_dossier",
				"create_security_hypothesis", "transition_security_hypothesis",
				"record_security_coverage", "create_security_variant_sweep",
				"complete_security_variant_sweep", "get_security_campaign_status",
			},
		},
		{path: "../../configs/securityworkflows/bounty-hunt-evm.yaml", tools: []string{"get_security_research_context", "transition_security_hypothesis", "record_security_coverage", "create_security_variant_sweep", "complete_security_variant_sweep", "get_security_campaign_status"}},
		{path: "../../configs/securityworkflows/blockchain-protocol-audit.yaml", tools: []string{"get_security_research_context", "transition_security_hypothesis", "record_security_coverage", "create_security_variant_sweep", "complete_security_variant_sweep", "get_security_campaign_status"}},
		{path: "../../configs/securityworkflows/smart-contract-review.yaml", tools: []string{"get_security_research_context", "transition_security_hypothesis", "record_security_coverage", "create_security_variant_sweep", "complete_security_variant_sweep", "get_security_campaign_status"}},
	}

	for _, asset := range assets {
		t.Run(asset.path, func(t *testing.T) {
			body, err := os.ReadFile(asset.path)
			if err != nil {
				t.Fatal(err)
			}
			text := string(body)
			for _, tool := range asset.tools {
				if !strings.Contains(text, tool) {
					t.Errorf("%s does not require %s", asset.path, tool)
				}
			}
			if !strings.Contains(text, "prior-revision") && !strings.Contains(text, "prior revision") && !strings.Contains(text, "exact binding") {
				t.Errorf("%s does not distinguish stale cross-revision evidence", asset.path)
			}

			mirror := strings.Replace(asset.path, "../../configs/", "../../dist/chart/files/bootstrap/", 1)
			mirrored, err := os.ReadFile(mirror)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(body, mirrored) {
				t.Errorf("bootstrap mirror %s differs from %s", mirror, asset.path)
			}
		})
	}
}
