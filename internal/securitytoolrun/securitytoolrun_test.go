/*
Copyright 2026.

SPDX-License-Identifier: AGPL-3.0-only
*/

package securitytoolrun

import (
	"slices"
	"strings"
	"testing"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
)

const testDigest = "sha256:1111111111111111111111111111111111111111111111111111111111111111"

func TestObjectKeyLayout(t *testing.T) {
	if got := TargetObjectKey("ns", "scan"); got != "security-tool-runs/ns/scan/target.tar.gz" {
		t.Fatalf("TargetObjectKey() = %q", got)
	}
	if got := OutputPrefix("ns", "scan"); got != "security-tool-runs/ns/scan/output" {
		t.Fatalf("OutputPrefix() = %q", got)
	}
	if got := ManifestObjectKey("ns", "scan"); got != "security-tool-runs/ns/scan/output/manifest.json" {
		t.Fatalf("ManifestObjectKey() = %q", got)
	}
	if got := ResultObjectKey("ns", "scan"); got != "security-tool-runs/ns/scan/output/result.json" {
		t.Fatalf("ResultObjectKey() = %q", got)
	}
}

func TestManifestValidate(t *testing.T) {
	valid := Manifest{
		SchemaVersion:   ManifestSchemaVersion,
		Tool:            "authorization-matrix",
		Status:          "findings",
		FindingCount:    2,
		ResultObjectKey: ResultObjectKey("ns", "scan"),
		ResultDigest:    testDigest,
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	cases := map[string]func(*Manifest){
		"schema":       func(m *Manifest) { m.SchemaVersion = "security-tool-job-manifest/v2" },
		"tool":         func(m *Manifest) { m.Tool = "" },
		"status":       func(m *Manifest) { m.Status = "clean" },
		"count":        func(m *Manifest) { m.FindingCount = -1 },
		"digest":       func(m *Manifest) { m.ResultDigest = "abc" },
		"artifact key": func(m *Manifest) { m.Artifacts = []ManifestArtifact{{Digest: testDigest}} },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			manifest := valid
			mutate(&manifest)
			if err := manifest.Validate(); err == nil {
				t.Fatalf("Validate() = nil, want error")
			}
		})
	}
}

func baseSpec() platformv1alpha1.SecurityToolRunSpec {
	return platformv1alpha1.SecurityToolRunSpec{
		Tool: "authorization-matrix",
		Target: platformv1alpha1.SecurityToolTarget{
			Type:            "authorization_matrix",
			Locator:         "matrix.json",
			Revision:        "1a2b3c",
			Digest:          testDigest,
			StagedObjectKey: TargetObjectKey("ns", "scan"),
		},
	}
}

func TestRunConfigForRejectsDuplicateArguments(t *testing.T) {
	spec := baseSpec()
	spec.Arguments = []platformv1alpha1.SecurityToolArgument{{Name: "rate", Value: "1"}, {Name: "rate", Value: "2"}}
	if _, err := RunConfigFor(spec); err == nil || !strings.Contains(err.Error(), "more than once") {
		t.Fatalf("RunConfigFor() error = %v", err)
	}
}

func TestValidate(t *testing.T) {
	registry, err := DefaultRegistry()
	if err != nil {
		t.Fatalf("DefaultRegistry() error = %v", err)
	}
	config, err := RunConfigFor(baseSpec())
	if err != nil {
		t.Fatalf("RunConfigFor() error = %v", err)
	}
	tool, err := Validate(registry, config)
	if err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if tool.Name != "authorization-matrix" || tool.Budgets.Timeout <= 0 {
		t.Fatalf("tool = %+v", tool)
	}

	cases := map[string]func(spec *platformv1alpha1.SecurityToolRunSpec){
		"unknown tool": func(spec *platformv1alpha1.SecurityToolRunSpec) { spec.Tool = "cat" },
		"disabled tool": func(spec *platformv1alpha1.SecurityToolRunSpec) {
			spec.Tool = "playwright"
			spec.Target.Type = "browser_script"
		},
		"wrong target":     func(spec *platformv1alpha1.SecurityToolRunSpec) { spec.Target.Type = "pcap" },
		"missing revision": func(spec *platformv1alpha1.SecurityToolRunSpec) { spec.Target.Revision = "" },
		"placeholder": func(spec *platformv1alpha1.SecurityToolRunSpec) {
			spec.Target.Locator = "{{target}}"
		},
		"unknown argument": func(spec *platformv1alpha1.SecurityToolRunSpec) {
			spec.Arguments = []platformv1alpha1.SecurityToolArgument{{Name: "shell", Value: "sh"}}
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			spec := baseSpec()
			mutate(&spec)
			config, err := RunConfigFor(spec)
			if err != nil {
				return
			}
			if _, err := Validate(registry, config); err == nil {
				t.Fatalf("Validate() = nil, want error")
			}
		})
	}
}

func TestValidateRequiresSeedAndScope(t *testing.T) {
	registry, err := DefaultRegistry()
	if err != nil {
		t.Fatalf("DefaultRegistry() error = %v", err)
	}
	spec := baseSpec()
	spec.Tool = "schemathesis"
	spec.Target.Type = "openapi"
	spec.Arguments = []platformv1alpha1.SecurityToolArgument{{Name: "base_url", Value: "https://api.example.com"}}
	config, err := RunConfigFor(spec)
	if err != nil {
		t.Fatalf("RunConfigFor() error = %v", err)
	}
	if _, err := Validate(registry, config); err == nil || !strings.Contains(err.Error(), "scope") {
		t.Fatalf("Validate() error = %v, want a scope requirement", err)
	}
	config.Scope = []string{"https://api.example.com"}
	if _, err := Validate(registry, config); err == nil || !strings.Contains(err.Error(), "seed") {
		t.Fatalf("Validate() error = %v, want a seed requirement", err)
	}
	seed := int64(7)
	config.Seed = &seed
	if _, err := Validate(registry, config); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestManifestValidateForConfinesObjectKeys(t *testing.T) {
	prefix := OutputPrefix("ns", "scan")
	valid := Manifest{
		SchemaVersion:   ManifestSchemaVersion,
		Tool:            "authorization-matrix",
		Status:          "pass",
		ResultObjectKey: ResultObjectKey("ns", "scan"),
		ResultDigest:    testDigest,
		Artifacts:       []ManifestArtifact{{ObjectKey: prefix + "/raw-00", Digest: testDigest}},
	}
	if err := valid.ValidateFor(prefix); err != nil {
		t.Fatalf("ValidateFor() error = %v", err)
	}
	cases := map[string]func(*Manifest){
		"foreign result":   func(m *Manifest) { m.ResultObjectKey = "security-tool-runs/other/run/output/result.json" },
		"foreign artifact": func(m *Manifest) { m.Artifacts[0].ObjectKey = "security-tool-runs/other/run/output/raw-00" },
		"traversal":        func(m *Manifest) { m.Artifacts[0].ObjectKey = prefix + "/../../other/output/raw-00" },
		"prefix sibling":   func(m *Manifest) { m.ResultObjectKey = prefix + "-evil/result.json" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			manifest := valid
			manifest.Artifacts = slices.Clone(valid.Artifacts)
			mutate(&manifest)
			if err := manifest.ValidateFor(prefix); err == nil {
				t.Fatal("ValidateFor() = nil, want error")
			}
		})
	}
}

// An unstaged target carries a digest nobody verified: the locator itself must
// be network-addressable, never a path the Job would read or bind-mount.
func TestValidateRejectsUnverifiedFilesystemTargets(t *testing.T) {
	registry, err := DefaultRegistry()
	if err != nil {
		t.Fatalf("DefaultRegistry() error = %v", err)
	}
	cases := map[string]struct {
		mutate  func(*platformv1alpha1.SecurityToolRunSpec)
		wantErr bool
	}{
		"workspace path without staging": {
			mutate: func(spec *platformv1alpha1.SecurityToolRunSpec) {
				spec.Target.StagedObjectKey = ""
				spec.Target.Locator = "repo/contracts/matrix.json"
			},
			wantErr: true,
		},
		"absolute host path": {
			mutate: func(spec *platformv1alpha1.SecurityToolRunSpec) {
				spec.Target.StagedObjectKey = ""
				spec.Target.Locator = "/proc/1/environ"
			},
			wantErr: true,
		},
		"relative escape": {
			mutate: func(spec *platformv1alpha1.SecurityToolRunSpec) {
				spec.Target.StagedObjectKey = ""
				spec.Target.Locator = "../../etc/shadow"
			},
			wantErr: true,
		},
		"home path": {
			mutate: func(spec *platformv1alpha1.SecurityToolRunSpec) {
				spec.Target.StagedObjectKey = ""
				spec.Target.Locator = "~/.aws/credentials"
			},
			wantErr: true,
		},
		"file url": {
			mutate: func(spec *platformv1alpha1.SecurityToolRunSpec) {
				spec.Tool = "nuclei"
				spec.Target.Type = "base_url"
				spec.Target.StagedObjectKey = ""
				spec.Target.Locator = "file:///etc/shadow"
			},
			wantErr: true,
		},
		"escape inside staged archive": {
			mutate: func(spec *platformv1alpha1.SecurityToolRunSpec) {
				spec.Target.Locator = "../../../proc/1/environ"
			},
			wantErr: true,
		},
		"http url": {
			mutate: func(spec *platformv1alpha1.SecurityToolRunSpec) {
				spec.Tool = "nuclei"
				spec.Target.Type = "base_url"
				spec.Target.StagedObjectKey = ""
				spec.Target.Locator = "https://api.example.com"
				spec.Scope = []string{"api.example.com"}
				spec.Arguments = []platformv1alpha1.SecurityToolArgument{{Name: "rate", Value: "10"}}
			},
		},
		"host and port": {
			mutate: func(spec *platformv1alpha1.SecurityToolRunSpec) {
				spec.Tool = "sslyze"
				spec.Target.Type = "tls_service"
				spec.Target.StagedObjectKey = ""
				spec.Target.Locator = "api.example.com:443"
				spec.Scope = []string{"api.example.com"}
			},
		},
		"image pinned by digest": {
			mutate: func(spec *platformv1alpha1.SecurityToolRunSpec) {
				spec.Target.StagedObjectKey = ""
				spec.Target.Locator = "ghcr.io/example/app@" + testDigest
			},
		},
		"image digest mismatch": {
			mutate: func(spec *platformv1alpha1.SecurityToolRunSpec) {
				spec.Target.StagedObjectKey = ""
				spec.Target.Locator = "ghcr.io/example/app@sha256:" + strings.Repeat("2", 64)
			},
			wantErr: true,
		},
		"staged archive": {mutate: func(*platformv1alpha1.SecurityToolRunSpec) {}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			spec := baseSpec()
			tc.mutate(&spec)
			request, err := RunConfigFor(spec)
			if err != nil {
				t.Fatalf("RunConfigFor() error = %v", err)
			}
			_, err = Validate(registry, request)
			if tc.wantErr && err == nil {
				t.Fatal("Validate() = nil, want the target to be rejected")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("Validate() error = %v", err)
			}
		})
	}
}
