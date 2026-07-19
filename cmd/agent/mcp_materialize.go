package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	sdkmcp "github.com/gratefulagents/sdk/pkg/agentsdk/mcp"
	agentpolicy "github.com/gratefulagents/sdk/pkg/agentsdk/policy"
	sdksandbox "github.com/gratefulagents/sdk/pkg/agentsdk/sandbox"
)

// uvxMaterializeTimeout bounds one package install (including downloads on a
// cold pod).
const uvxMaterializeTimeout = 3 * time.Minute

var immutablePythonPackage = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*(\[[A-Za-z0-9._,-]+\])?==[A-Za-z0-9][A-Za-z0-9._+!-]*$`)

// installRunner abstracts the isolated package installation so tests never
// need network access or a working bubblewrap installation.
type installRunner func(ctx context.Context, root string, argv []string) (output string, err error)

// sandboxedInstallRunner installs a wheel inside a dedicated bubblewrap
// filesystem boundary. It deliberately starts from a new, minimal sandbox
// config instead of the run's config: installer processes get no MCP secrets,
// service-account mounts, workspace, or host environment. Network is enabled
// only so pip can reach its configured HTTPS package index.
func sandboxedInstallRunner(ctx context.Context, root string, argv []string) (string, error) {
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", fmt.Errorf("create installer root: %w", err)
	}
	executor := sdksandbox.DefaultWithConfig(sdksandbox.Config{
		Mode:          "required",
		WorkspaceRoot: root,
	})
	result, err := executor.Run(ctx, sdksandbox.Request{
		Argv:           argv,
		WorkDir:        root,
		PermissionMode: agentpolicy.PermissionModeWorkspaceWrite,
		Timeout:        uvxMaterializeTimeout,
		Env: map[string]string{
			"PIP_DISABLE_PIP_VERSION_CHECK": "1",
			"PIP_NO_INPUT":                  "1",
		},
		AllowNetwork: true,
	})
	if err != nil {
		return result.Output, err
	}
	if result.TimedOut {
		return result.Output, fmt.Errorf("installer timed out after %s", uvxMaterializeTimeout)
	}
	if result.ExitCode != 0 {
		return result.Output, fmt.Errorf("installer exited with status %d", result.ExitCode)
	}
	return result.Output, nil
}

// materializeUvxServers rewrites policy-approved, cluster-managed
// `uvx <package==version>` servers into direct commands installed at run
// startup. It returns the private tool root when at least one server was
// installed; the caller must expose that root read-only to MCP sandboxes.
//
// uvx itself cannot run in the MCP sandbox on container runtimes where a fresh
// procfs cannot be mounted: the safe fallback masks /proc, while uv resolves
// itself via /proc/self/exe. Installation therefore uses Python pip in a
// separate credential-free sandbox. --only-binary prevents package build
// backends from executing during installation; package code executes only
// later, inside the normal MCP sandbox with that server's explicit env.
func materializeUvxServers(ctx context.Context, cfg *sdkmcp.Config, clusterManaged map[string]struct{}, run installRunner) (string, []string) {
	root := filepath.Join(os.TempDir(), "gratefulagents-mcp-tools")
	installed, dropped := materializeUvxServersAt(ctx, cfg, clusterManaged, root, run)
	if installed {
		return root, dropped
	}
	return "", dropped
}

func materializeUvxServersAt(ctx context.Context, cfg *sdkmcp.Config, clusterManaged map[string]struct{}, root string, run installRunner) (bool, []string) {
	if cfg == nil || len(cfg.MCPServers) == 0 {
		return false, nil
	}
	installed := false
	var dropped []string
	drop := func(name, reason string) {
		delete(cfg.MCPServers, name)
		dropped = append(dropped, fmt.Sprintf("%s (MCPServer): %s", name, reason))
	}
	for name, srv := range cfg.MCPServers {
		if strings.TrimSpace(srv.Command) != "uvx" {
			continue
		}
		if _, trusted := clusterManaged[name]; !trusted {
			// Repository packages execute through the normal MCP sandbox and may
			// never trigger the more privileged networked installer.
			continue
		}

		spec, exe, rest, ok := parseUvxInvocation(srv.Args)
		if !ok || !isSafeExecutableName(exe) {
			reason := fmt.Sprintf("unsupported uvx invocation %v", srv.Args)
			log.Printf("WARN: removing MCP server %q: %s", name, reason)
			drop(name, reason)
			continue
		}
		if !isImmutableUvxSpec(spec) {
			reason := fmt.Sprintf("installable uvx packages require an exact package==version pin (got %q)", spec)
			log.Printf("WARN: removing MCP server %q: %s", name, reason)
			drop(name, reason)
			continue
		}

		digest := sha256.Sum256([]byte(name + "\x00" + spec))
		toolDir := filepath.Join(root, fmt.Sprintf("%x", digest[:12]))
		if err := os.RemoveAll(toolDir); err != nil {
			reason := fmt.Sprintf("clear install directory: %v", err)
			log.Printf("WARN: removing MCP server %q: %s", name, reason)
			drop(name, reason)
			continue
		}
		if err := os.MkdirAll(toolDir, 0o700); err != nil {
			reason := fmt.Sprintf("create install directory: %v", err)
			log.Printf("WARN: removing MCP server %q: %s", name, reason)
			drop(name, reason)
			continue
		}

		ictx, cancel := context.WithTimeout(ctx, uvxMaterializeTimeout)
		output, err := run(ictx, root, []string{
			"python3", "-m", "pip", "install",
			"--disable-pip-version-check", "--no-input", "--no-cache-dir",
			"--only-binary=:all:", "--target", toolDir, spec,
		})
		cancel()
		if err != nil {
			_ = os.RemoveAll(toolDir)
			reason := fmt.Sprintf("isolated wheel install %s failed: %v", spec, err)
			log.Printf("WARN: removing MCP server %q: %s; installer output: %s",
				name, reason, tailString(output, 800))
			drop(name, reason)
			continue
		}

		binPath := filepath.Join(toolDir, "bin", exe)
		if err := validateInstalledExecutable(binPath, toolDir); err != nil {
			_ = os.RemoveAll(toolDir)
			reason := fmt.Sprintf("wheel install %s produced no safe %q command: %v", spec, exe, err)
			log.Printf("WARN: removing MCP server %q: %s", name, reason)
			drop(name, reason)
			continue
		}

		// Keep the installed wheel immutable, but launch from a private writable
		// copy inside the MCP sandbox. Some packages legitimately initialize
		// bundled binaries, caches, or generated files beside their package on
		// first run; invoking the read-only source tree directly breaks them.
		commandPath, err := writeWritableToolLauncher(toolDir, exe)
		if err != nil {
			_ = os.RemoveAll(toolDir)
			reason := fmt.Sprintf("prepare writable runtime launcher for %s: %v", spec, err)
			log.Printf("WARN: removing MCP server %q: %s", name, reason)
			drop(name, reason)
			continue
		}

		srv.Command = commandPath
		srv.Args = rest
		if srv.Env == nil {
			srv.Env = make(map[string]string)
		}
		// pip --target places importable packages beside bin/. Console-script
		// shebangs use the system interpreter, so make that target importable.
		srv.Env["PYTHONPATH"] = prependEnvPath(toolDir, srv.Env["PYTHONPATH"])
		serverPath := srv.Env["PATH"]
		if strings.TrimSpace(serverPath) == "" {
			serverPath = "${PATH}"
		}
		srv.Env["PATH"] = prependEnvPath(filepath.Join(toolDir, "bin"), serverPath)
		cfg.MCPServers[name] = srv
		installed = true
		log.Printf("MCP server %q: installed %s at run start (isolated wheel, sandbox-safe)", name, spec)
	}
	return installed, dropped
}

// parseUvxInvocation understands the two supported uvx forms:
//
//	uvx <spec> [server args...]
//	uvx --from <spec> <command> [server args...]
func parseUvxInvocation(args []string) (spec, exe string, rest []string, ok bool) {
	if len(args) == 0 {
		return "", "", nil, false
	}
	if args[0] == "--from" {
		if len(args) < 3 || strings.HasPrefix(args[2], "-") {
			return "", "", nil, false
		}
		return args[1], args[2], args[3:], true
	}
	if strings.HasPrefix(args[0], "-") {
		return "", "", nil, false
	}
	exe = executableFromSpec(args[0])
	if exe == "" {
		return "", "", nil, false
	}
	return args[0], exe, args[1:], true
}

func isImmutableUvxSpec(spec string) bool {
	return immutablePythonPackage.MatchString(strings.TrimSpace(spec))
}

func isSafeExecutableName(exe string) bool {
	exe = strings.TrimSpace(exe)
	return exe != "" && exe != "." && exe != ".." && filepath.Base(exe) == exe && !strings.ContainsAny(exe, `/\\`)
}

// executableFromSpec derives the console-script name from a PEP 508-ish spec:
// "mcp-grafana==0.17.0" -> "mcp-grafana".
func executableFromSpec(spec string) string {
	spec = strings.TrimSpace(spec)
	if spec == "" || strings.Contains(spec, "/") || strings.Contains(spec, "://") {
		return ""
	}
	if i := strings.IndexAny(spec, "=<>!~@["); i >= 0 {
		spec = spec[:i]
	}
	return strings.TrimSpace(spec)
}

func writeWritableToolLauncher(toolDir, exe string) (string, error) {
	if !isSafeExecutableName(exe) {
		return "", errors.New("unsafe executable name")
	}
	launcherDir := filepath.Join(toolDir, ".gratefulagents")
	if err := os.MkdirAll(launcherDir, 0o700); err != nil {
		return "", err
	}
	launcherPath := filepath.Join(launcherDir, "launch-"+exe)
	digest := sha256.Sum256([]byte(toolDir))
	runtimeID := fmt.Sprintf("%x", digest[:8])
	script := fmt.Sprintf(`#!/bin/sh
set -eu
umask 077
runtime_dir="${TMPDIR:-/tmp}/gratefulagents-mcp-runtime-%s-$$"
mkdir -p "$runtime_dir"
cp -a %s/. "$runtime_dir/"
chmod -R u+w "$runtime_dir"
export PYTHONPATH="$runtime_dir${PYTHONPATH:+:$PYTHONPATH}"
export PATH="$runtime_dir/bin${PATH:+:$PATH}"
exec "$runtime_dir/bin/%s" "$@"
`, runtimeID, shellQuote(toolDir), exe)
	if err := os.WriteFile(launcherPath, []byte(script), 0o700); err != nil {
		return "", err
	}
	return launcherPath, validateInstalledExecutable(launcherPath, toolDir)
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}

func validateInstalledExecutable(binPath, toolDir string) error {
	info, err := os.Stat(binPath)
	if err != nil {
		return err
	}
	if info.IsDir() || info.Mode()&0o111 == 0 {
		return errors.New("installed command is not executable")
	}
	resolved, err := filepath.EvalSymlinks(binPath)
	if err != nil {
		return err
	}
	resolvedRoot, err := filepath.EvalSymlinks(toolDir)
	if err != nil {
		return err
	}
	if resolved != resolvedRoot && !strings.HasPrefix(resolved, resolvedRoot+string(os.PathSeparator)) {
		return errors.New("installed command resolves outside the private tool directory")
	}
	return nil
}

func prependEnvPath(entry, existing string) string {
	if strings.TrimSpace(existing) == "" {
		return entry
	}
	return entry + string(os.PathListSeparator) + existing
}

func tailString(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return "…" + s[len(s)-n:]
}
