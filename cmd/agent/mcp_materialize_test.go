package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	sdkmcp "github.com/gratefulagents/sdk/pkg/agentsdk/mcp"
)

func TestParseUvxInvocation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		args     []string
		wantSpec string
		wantExe  string
		wantRest []string
		wantOK   bool
	}{
		{"plain", []string{"mcp-grafana", "--disable-oncall"}, "mcp-grafana", "mcp-grafana", []string{"--disable-oncall"}, true},
		{"versioned", []string{"mcp-grafana==0.17.0"}, "mcp-grafana==0.17.0", "mcp-grafana", []string{}, true},
		{"from form", []string{"--from", "mcp-grafana==0.17.0", "mcp-grafana", "--disable-oncall"}, "mcp-grafana==0.17.0", "mcp-grafana", []string{"--disable-oncall"}, true},
		{"from missing cmd", []string{"--from", "spec"}, "", "", nil, false},
		{"unknown flag", []string{"--python", "3.12", "pkg"}, "", "", nil, false},
		{"empty", nil, "", "", nil, false},
		{"path spec", []string{"./local/tool"}, "", "", nil, false},
		{"url spec", []string{"https://example.com/x.whl"}, "", "", nil, false},
	}
	for _, tt := range tests {
		spec, exe, rest, ok := parseUvxInvocation(tt.args)
		if ok != tt.wantOK || spec != tt.wantSpec || exe != tt.wantExe || strings.Join(rest, " ") != strings.Join(tt.wantRest, " ") {
			t.Errorf("%s: parseUvxInvocation(%v) = (%q, %q, %v, %v), want (%q, %q, %v, %v)",
				tt.name, tt.args, spec, exe, rest, ok, tt.wantSpec, tt.wantExe, tt.wantRest, tt.wantOK)
		}
	}
}

func TestMaterializeUvxServersInstallsPinnedWheel(t *testing.T) {
	root := t.TempDir()
	cfg := &sdkmcp.Config{MCPServers: map[string]sdkmcp.ServerConfig{
		"grafana": {
			Command: "uvx",
			Args:    []string{"mcp-grafana==0.17.0", "--disable-oncall"},
			Env:     map[string]string{"GRAFANA_TOKEN": "server-secret", "PYTHONPATH": "/existing/python"},
		},
		"node": {Command: "npx", Args: []string{"-y", "some-server"}},
	}}

	var gotRoot string
	var gotArgv []string
	runner := func(_ context.Context, installRoot string, argv []string) (string, error) {
		gotRoot = installRoot
		gotArgv = append([]string(nil), argv...)
		target := argumentValue(argv, "--target")
		binDir := filepath.Join(target, "bin")
		if err := os.MkdirAll(binDir, 0o700); err != nil {
			return "", err
		}
		return "", os.WriteFile(filepath.Join(binDir, "mcp-grafana"), []byte("#!/bin/sh\n"), 0o755)
	}

	installed, dropped := materializeUvxServersAt(context.Background(), cfg, map[string]struct{}{"grafana": {}}, root, runner)
	if !installed {
		t.Fatal("expected a materialized server")
	}
	if len(dropped) != 0 {
		t.Fatalf("unexpected dropped servers: %v", dropped)
	}
	if gotRoot != root {
		t.Fatalf("runner root = %q, want %q", gotRoot, root)
	}
	joined := strings.Join(gotArgv, " ")
	for _, required := range []string{"python3 -m pip install", "--only-binary=:all:", "mcp-grafana==0.17.0"} {
		if !strings.Contains(joined, required) {
			t.Errorf("installer argv %q missing %q", joined, required)
		}
	}
	grafana := cfg.MCPServers["grafana"]
	toolDir := argumentValue(gotArgv, "--target")
	if !strings.HasPrefix(grafana.Command, filepath.Join(toolDir, ".gratefulagents")+string(os.PathSeparator)) {
		t.Fatalf("command = %q, want private runtime launcher under %q", grafana.Command, toolDir)
	}
	launcher, err := os.ReadFile(grafana.Command)
	if err != nil {
		t.Fatalf("read runtime launcher: %v", err)
	}
	for _, want := range []string{"cp -a", shellQuote(toolDir), `exec "$runtime_dir/bin/mcp-grafana" "$@"`} {
		if !strings.Contains(string(launcher), want) {
			t.Errorf("runtime launcher missing %q:\n%s", want, launcher)
		}
	}
	if strings.Join(grafana.Args, " ") != "--disable-oncall" {
		t.Fatalf("server args = %v", grafana.Args)
	}
	if grafana.Env["GRAFANA_TOKEN"] != "server-secret" {
		t.Fatal("server runtime credentials were not preserved")
	}
	if want := argumentValue(gotArgv, "--target") + string(os.PathListSeparator) + "/existing/python"; grafana.Env["PYTHONPATH"] != want {
		t.Fatalf("PYTHONPATH = %q, want %q", grafana.Env["PYTHONPATH"], want)
	}
	if want := filepath.Join(argumentValue(gotArgv, "--target"), "bin") + string(os.PathListSeparator) + "${PATH}"; grafana.Env["PATH"] != want {
		t.Fatalf("PATH = %q, want %q", grafana.Env["PATH"], want)
	}
	if cfg.MCPServers["node"].Command != "npx" {
		t.Fatal("non-uvx server was modified")
	}
}

func TestWritableToolLauncherUsesPrivateMutableCopy(t *testing.T) {
	toolDir := t.TempDir()
	binDir := filepath.Join(toolDir, "bin")
	if err := os.MkdirAll(binDir, 0o700); err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(toolDir, "state")
	if err := os.WriteFile(statePath, []byte("immutable"), 0o600); err != nil {
		t.Fatal(err)
	}
	serverPath := filepath.Join(binDir, "server")
	server := `#!/bin/sh
set -eu
runtime_root="$(dirname "$0")/.."
printf mutable > "$runtime_root/state"
printf '%s:%s' "$(cat "$runtime_root/state")" "$1"
`
	if err := os.WriteFile(serverPath, []byte(server), 0o700); err != nil {
		t.Fatal(err)
	}
	launcher, err := writeWritableToolLauncher(toolDir, "server")
	if err != nil {
		t.Fatalf("writeWritableToolLauncher() error = %v", err)
	}

	cmd := exec.Command(launcher, "argument")
	cmd.Env = append(os.Environ(), "TMPDIR="+t.TempDir())
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("launcher error = %v, output = %s", err, output)
	}
	if got := string(output); got != "mutable:argument" {
		t.Fatalf("launcher output = %q, want %q", got, "mutable:argument")
	}
	if source, err := os.ReadFile(statePath); err != nil || string(source) != "immutable" {
		t.Fatalf("source state = %q, err = %v; immutable install was modified", source, err)
	}
}

func TestMaterializeUvxServersDoesNotInstallRepoUvx(t *testing.T) {
	t.Parallel()
	cfg := &sdkmcp.Config{MCPServers: map[string]sdkmcp.ServerConfig{
		"repo": {Command: "uvx", Args: []string{"repo-package==1.0.0"}},
	}}
	called := false
	installed, dropped := materializeUvxServersAt(context.Background(), cfg, nil, t.TempDir(),
		func(context.Context, string, []string) (string, error) { called = true; return "", nil })
	if installed || called || len(dropped) != 0 || cfg.MCPServers["repo"].Command != "uvx" {
		t.Fatalf("repository uvx config triggered installation: installed=%v called=%v dropped=%v cfg=%+v", installed, called, dropped, cfg)
	}
}

func TestMaterializeUvxServersRejectsUnpinnedPackage(t *testing.T) {
	t.Parallel()
	cfg := &sdkmcp.Config{MCPServers: map[string]sdkmcp.ServerConfig{
		"unpinned": {Command: "uvx", Args: []string{"mutable-package"}},
	}}
	called := false
	_, dropped := materializeUvxServersAt(context.Background(), cfg, map[string]struct{}{"unpinned": {}}, t.TempDir(),
		func(context.Context, string, []string) (string, error) { called = true; return "", nil })
	if called {
		t.Fatal("unpinned package invoked installer")
	}
	if _, ok := cfg.MCPServers["unpinned"]; ok {
		t.Fatal("unpinned package remained configured")
	}
	if len(dropped) != 1 || !strings.Contains(dropped[0], "exact package==version pin") {
		t.Fatalf("drop reason = %v", dropped)
	}
}

func TestMaterializeUvxServersRemovesServerOnInstallFailure(t *testing.T) {
	t.Parallel()
	cfg := &sdkmcp.Config{MCPServers: map[string]sdkmcp.ServerConfig{
		"broken": {Command: "uvx", Args: []string{"broken-package==1.0.0"}},
	}}
	materializeUvxServersAt(context.Background(), cfg, map[string]struct{}{"broken": {}}, t.TempDir(),
		func(context.Context, string, []string) (string, error) {
			return "no matching wheel", errors.New("exit status 1")
		})
	if _, ok := cfg.MCPServers["broken"]; ok {
		t.Fatal("failed package remained configured")
	}
}

func TestMaterializeUvxServersRejectsUnsafeFromCommand(t *testing.T) {
	t.Parallel()
	cfg := &sdkmcp.Config{MCPServers: map[string]sdkmcp.ServerConfig{
		"unsafe": {Command: "uvx", Args: []string{"--from", "safe-package==1.0.0", "../../escape"}},
	}}
	called := false
	materializeUvxServersAt(context.Background(), cfg, map[string]struct{}{"unsafe": {}}, t.TempDir(),
		func(context.Context, string, []string) (string, error) { called = true; return "", nil })
	if called {
		t.Fatal("unsafe command invoked installer")
	}
	if _, ok := cfg.MCPServers["unsafe"]; ok {
		t.Fatal("unsafe command remained configured")
	}
}

func TestMaterializeUvxServersRejectsEscapingInstalledCommand(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := &sdkmcp.Config{MCPServers: map[string]sdkmcp.ServerConfig{
		"linked": {Command: "uvx", Args: []string{"linked==1.0.0"}},
	}}
	materializeUvxServersAt(context.Background(), cfg, map[string]struct{}{"linked": {}}, root,
		func(_ context.Context, _ string, argv []string) (string, error) {
			binDir := filepath.Join(argumentValue(argv, "--target"), "bin")
			if err := os.MkdirAll(binDir, 0o700); err != nil {
				return "", err
			}
			return "", os.Symlink(outside, filepath.Join(binDir, "linked"))
		})
	if _, ok := cfg.MCPServers["linked"]; ok {
		t.Fatal("escaping installed command remained configured")
	}
}

func TestImmutableUvxSpec(t *testing.T) {
	t.Parallel()
	for _, spec := range []string{"mcp-grafana==0.17.0", "pkg_name[extra,more]==1.2.3", "pkg==1!2.0+linux"} {
		if !isImmutableUvxSpec(spec) {
			t.Errorf("expected immutable spec: %q", spec)
		}
	}
	for _, spec := range []string{"mcp-grafana", "pkg>=1", "pkg==1.*", "../pkg==1", "https://x/pkg==1", "pkg==https://x", "pkg==1/../../x", "pkg=="} {
		if isImmutableUvxSpec(spec) {
			t.Errorf("expected rejected spec: %q", spec)
		}
	}
}

func TestExecutableFromSpec(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		"mcp-grafana==0.17.0": "mcp-grafana",
		"pkg[extra]@1.2":      "pkg",
		"plain":               "plain",
		"./path/tool":         "",
		"https://x/y.whl":     "",
		"":                    "",
	}
	for in, want := range tests {
		if got := executableFromSpec(in); got != want {
			t.Errorf("executableFromSpec(%q) = %q, want %q", in, got, want)
		}
	}
}

func argumentValue(argv []string, flag string) string {
	for i := range argv {
		if argv[i] == flag && i+1 < len(argv) {
			return argv[i+1]
		}
	}
	return ""
}
