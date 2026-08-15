package configtest

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

func TestSecurityToolsImageTracksManagerReleaseByDefault(t *testing.T) {
	valuesBytes, err := os.ReadFile("../../dist/chart/values.yaml")
	if err != nil {
		t.Fatalf("read chart values: %v", err)
	}
	var values struct {
		AgentImages struct {
			SecurityTools string `yaml:"securityTools"`
		} `yaml:"agentImages"`
		SecurityTools struct {
			AllowUnpinnedImage bool `yaml:"allowUnpinnedImage"`
		} `yaml:"securityTools"`
	}
	if err := yaml.Unmarshal(valuesBytes, &values); err != nil {
		t.Fatalf("parse chart values: %v", err)
	}
	if values.AgentImages.SecurityTools != "" {
		t.Fatalf("agentImages.securityTools must remain empty by default so the manager release image is derived, got %q", values.AgentImages.SecurityTools)
	}
	if values.SecurityTools.AllowUnpinnedImage {
		t.Fatal("securityTools.allowUnpinnedImage must default false so explicit image overrides remain digest-only")
	}

	helperBytes, err := os.ReadFile("../../dist/chart/templates/_helpers.tpl")
	if err != nil {
		t.Fatalf("read chart helpers: %v", err)
	}
	helper := string(helperBytes)
	for _, want := range []string{
		`define "gratefulagents.securityToolsImage"`,
		`hasSuffix "/controller" $repository`,
		`trimSuffix "/controller" $repository`,
		`toString .Values.manager.image.tag`,
	} {
		if !strings.Contains(helper, want) {
			t.Errorf("security-tools image helper no longer derives the manager release image; missing %q", want)
		}
	}

	managerBytes, err := os.ReadFile("../../dist/chart/templates/manager/manager.yaml")
	if err != nil {
		t.Fatalf("read manager template: %v", err)
	}
	manager := string(managerBytes)
	if !strings.Contains(manager, `value: {{ include "gratefulagents.securityToolsImage" . | quote }}`) {
		t.Fatal("manager template must always populate SECURITY_TOOLS_IMAGE through the derived-image helper")
	}
}

// The runtime lock is the only provenance for every installed security-tool
// binary. A new entry must either pin an asset URL and sha256 for both release
// architectures, or be disabled with a reason: an entry that installs on one
// architecture only would silently break multi-architecture image builds, and
// an entry without a digest would install an unverified binary.
func TestSecurityToolsLockPinsEveryPlatformDigest(t *testing.T) {
	data, err := os.ReadFile("../../security-tools.lock.json")
	if err != nil {
		t.Fatalf("read runtime lock: %v", err)
	}
	var lock struct {
		SchemaVersion string `json:"schema_version"`
		Tools         []struct {
			Name      string `json:"name"`
			Status    string `json:"status"`
			Version   string `json:"version"`
			Binary    string `json:"binary"`
			Reason    string `json:"reason"`
			Release   string `json:"release_url"`
			Platforms map[string]struct {
				Asset        string `json:"asset"`
				SHA256       string `json:"sha256"`
				BinarySHA256 string `json:"binary_sha256"`
			} `json:"platforms"`
			UnsupportedPlatforms map[string]string `json:"unsupported_platforms"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(data, &lock); err != nil {
		t.Fatalf("parse runtime lock: %v", err)
	}
	if lock.SchemaVersion != "security-tools-lock/v1" {
		t.Fatalf("lock schema = %q", lock.SchemaVersion)
	}
	digest := regexp.MustCompile(`^[0-9a-f]{64}$`)
	seen := map[string]bool{}
	for _, tool := range lock.Tools {
		if tool.Name == "" || seen[tool.Name] {
			t.Fatalf("lock entry %q is unnamed or duplicated", tool.Name)
		}
		seen[tool.Name] = true
		if tool.Status != "enabled" {
			if strings.TrimSpace(tool.Reason) == "" {
				t.Errorf("%s: a non-enabled lock entry must state why", tool.Name)
			}
		} else {
			if tool.Version == "" || tool.Binary == "" || !strings.HasPrefix(tool.Release, "https://github.com/") {
				t.Errorf("%s: enabled entry must pin version, binary, and upstream release URL", tool.Name)
			}
			if strings.TrimSpace(tool.Reason) != "" {
				t.Errorf("%s: enabled entry must not carry a disabled reason", tool.Name)
			}
			for _, platform := range []string{"linux/amd64", "linux/arm64"} {
				if _, ok := tool.Platforms[platform]; ok {
					if tool.UnsupportedPlatforms[platform] != "" {
						t.Errorf("%s: %s cannot be both supported and unsupported", tool.Name, platform)
					}
					continue
				}
				if strings.TrimSpace(tool.UnsupportedPlatforms[platform]) == "" {
					t.Errorf("%s: enabled entry must provide an artifact or unsupported reason for %s", tool.Name, platform)
				}
			}
		}
		// Whatever is recorded must be verifiable, including on a disabled
		// entry that keeps digests for the architectures upstream does ship.
		for platform, artifact := range tool.Platforms {
			if !strings.HasPrefix(artifact.Asset, "https://github.com/") || !strings.Contains(artifact.Asset, "/releases/download/") {
				t.Errorf("%s/%s: asset must be an immutable upstream release download URL, got %q", tool.Name, platform, artifact.Asset)
			}
			if !digest.MatchString(artifact.SHA256) {
				t.Errorf("%s/%s: archive sha256 %q is not a sha256 digest", tool.Name, platform, artifact.SHA256)
			}
			if artifact.BinarySHA256 != "" && !digest.MatchString(artifact.BinarySHA256) {
				t.Errorf("%s/%s: binary sha256 %q is not a sha256 digest", tool.Name, platform, artifact.BinarySHA256)
			}
		}
	}
	for _, required := range []string{"forge", "anvil"} {
		if !seen[required] {
			t.Errorf("runtime lock lost the %s entry", required)
		}
	}
}
