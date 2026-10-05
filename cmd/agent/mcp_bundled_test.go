package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	platformv1alpha1 "github.com/gratefulagents/gratefulagents/api/platform/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/yaml"
)

// Bundled uvx servers must survive both configuration and materialization now
// that all attached CRD servers are eligible without a separate policy.
func TestBundledMCPServersMaterializeWithoutPolicy(t *testing.T) {
	for _, name := range []string{"fetch", "git-mcp", "search-duckduckgo"} {
		t.Run(name, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("..", "..", "configs", "mcpservers", name+".yaml"))
			if err != nil {
				t.Fatal(err)
			}
			var server platformv1alpha1.MCPServer
			if err := yaml.Unmarshal(data, &server); err != nil {
				t.Fatal(err)
			}
			server.Namespace = "default"
			scheme := runtime.NewScheme()
			if err := platformv1alpha1.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(&server).Build()
			run := &platformv1alpha1.AgentRun{
				ObjectMeta: metav1.ObjectMeta{Namespace: "default"},
				Spec:       platformv1alpha1.AgentRunSpec{MCPServerRefs: []platformv1alpha1.NamedRef{{Name: name}}},
			}
			cfg, managed, _ := buildMCPConfig(context.Background(), c, "default", t.TempDir(), run)
			spec, exe, _, ok := parseUvxInvocation(cfg.MCPServers[name].Args)
			if !ok || !isImmutableUvxSpec(spec) {
				t.Fatalf("bundled server must use an exact package pin: %v", cfg.MCPServers[name].Args)
			}
			calls := 0
			runner := func(_ context.Context, _ string, argv []string) (string, error) {
				calls++
				if argv[len(argv)-1] != spec || !strings.Contains(strings.Join(argv, " "), "--only-binary=:all:") {
					t.Fatalf("expected pinned wheel-only installation, got %v", argv)
				}
				bin := filepath.Join(argumentValue(argv, "--target"), "bin")
				if err := os.MkdirAll(bin, 0o700); err != nil {
					return "", err
				}
				return "", os.WriteFile(filepath.Join(bin, exe), []byte("#!/bin/sh\n"), 0o755)
			}
			installed, dropped := materializeUvxServersAt(context.Background(), &cfg, managed, t.TempDir(), runner)
			if !installed || len(dropped) != 0 || calls != 1 {
				t.Fatalf("installed=%v, dropped=%v, installer calls=%d", installed, dropped, calls)
			}
			if server, ok := cfg.MCPServers[name]; !ok || server.Command == "uvx" {
				t.Fatalf("bundled server not materialized: %+v", server)
			}
		})
	}
}
