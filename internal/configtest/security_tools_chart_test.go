package configtest

import (
	"os"
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
