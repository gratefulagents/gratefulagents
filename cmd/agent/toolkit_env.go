package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/gratefulagents/sdk/pkg/agentsdk/sandbox"
)

// The injector init container stages an operator-controlled toolkit into the
// user's runtime container. The user image only needs a glibc userland and a
// shell — everything else the agent depends on is injected:
//
//	bin/          — PATH-first: the agent binary itself
//	fallback/bin/ — PATH-last: static git, gh, rg, fd, jq, curl, bwrap
//	              (gap fillers; the image's own tools win when present)
//	etc/ca-certificates.crt — CA bundle for images without ca-certificates
//
// Vars (not consts) so tests can point the toolkit root at a scratch
// directory — hosts that genuinely have /opt/gratefulagents (e.g. dogfooding
// runners) must not change test behavior.
var (
	toolkitRoot           = "/opt/gratefulagents"
	toolkitBinDir         = toolkitRoot + "/bin"
	toolkitFallbackBinDir = toolkitRoot + "/fallback/bin"
	toolkitCABundle       = toolkitRoot + "/etc/ca-certificates.crt"
)

const (
	// fallbackHomeDir matches the HOME the SDK subprocess sandbox pins, so
	// gh/git state written by the agent process is visible to sandboxed
	// subprocesses and vice versa.
	fallbackHomeDir = "/tmp/home"
)

// systemCABundlePaths are the well-known CA bundle locations across glibc
// distros, in preference order. The distro's own bundle wins over the toolkit
// copy so system-level trust customizations keep working.
var systemCABundlePaths = []string{
	"/etc/ssl/certs/ca-certificates.crt", // Debian/Ubuntu/Alpine
	"/etc/pki/tls/certs/ca-bundle.crt",   // Fedora/RHEL/Amazon Linux
	"/etc/ssl/certs/ca-bundle.crt",       // Fedora (legacy symlink)
	"/etc/ssl/ca-bundle.pem",             // openSUSE
	"/etc/ssl/cert.pem",                  // misc
}

// setupToolkitEnv makes the injected toolkit usable regardless of what the
// user's runtime image provides. It is a no-op outside toolkit-injected pods
// (local development, tests).
func setupToolkitEnv() {
	if _, err := os.Stat(toolkitBinDir); err != nil {
		return
	}

	imagePath := os.Getenv("PATH")
	_ = os.Setenv("PATH", assembleProcessPath(imagePath, os.Getenv("GOPATH")))

	// Propagate the image's PATH plus the fallback tools into the subprocess
	// sandbox so bash tool calls see the same tool universe as the agent. The
	// SDK dedups entries and ignores appends when a full sandbox PATH override
	// is configured, so this composes with operator/RuntimeProfile settings.
	sandboxAppend := appendPathList(os.Getenv(sandbox.SandboxPathAppendEnv), splitPathEntries(imagePath)...)
	sandboxAppend = appendPathList(sandboxAppend, toolkitFallbackBinDir)
	_ = os.Setenv(sandbox.SandboxPathAppendEnv, sandboxAppend)

	setupHomeEnv()
	setupCABundleEnv()
}

// setupHomeEnv guarantees the agent process a writable HOME. Arbitrary
// runtime images often have no passwd entry for the pod's UID, so the
// container runtime leaves HOME unset or "/" — which breaks gh auth
// (mkdir /.config: permission denied) and git config --global writes.
func setupHomeEnv() {
	current := os.Getenv("HOME")
	usable := ensureUsableHome(current, fallbackHomeDir)
	if usable == current {
		return
	}
	log.Printf("HOME %q is not writable; using %s", current, usable)
	_ = os.Setenv("HOME", usable)
}

// ensureUsableHome returns home when it names a writable directory (creating
// it if the parent allows), otherwise the fallback (created on demand). The
// original value is kept when even the fallback cannot be created.
func ensureUsableHome(home, fallback string) string {
	home = strings.TrimSpace(home)
	if home != "" && home != "/" && writableDir(home) {
		return home
	}
	if !writableDir(fallback) {
		return home
	}
	return fallback
}

func writableDir(dir string) bool {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return false
	}
	probe, err := os.CreateTemp(dir, ".gratefulagents-home-probe-*")
	if err != nil {
		return false
	}
	_ = probe.Close()
	_ = os.Remove(probe.Name())
	return true
}

// assembleProcessPath builds the agent process PATH: pinned toolkit bin first,
// Go tool caches, then the image's own PATH, with the injected fallback tools
// last so the image's tools win whenever they exist.
func assembleProcessPath(imagePath, gopath string) string {
	entries := []string{toolkitBinDir}
	if gopath = strings.TrimSpace(gopath); gopath != "" {
		entries = append(entries, filepath.Join(gopath, "bin"))
	}
	entries = append(entries, splitPathEntries(imagePath)...)
	if len(entries) == 1 {
		// Image provided no PATH at all; fall back to the standard dirs.
		entries = append(entries, "/usr/local/sbin", "/usr/local/bin", "/usr/sbin", "/usr/bin", "/sbin", "/bin")
	}
	entries = append(entries, toolkitFallbackBinDir)
	return joinPathEntries(entries)
}

// setupCABundleEnv points TLS-consuming tools at a CA bundle that is
// guaranteed to exist. The distro bundle is preferred (a no-op for the
// image's own tools); the toolkit bundle covers images without
// ca-certificates. Explicit user-provided values are never overridden.
func setupCABundleEnv() {
	bundle := findCABundle()
	if bundle == "" {
		return
	}
	caEnvs := map[string]string{
		"SSL_CERT_FILE":  bundle, // Go tools (gh) and OpenSSL-based clients
		"GIT_SSL_CAINFO": bundle, // git-remote-https (static fallback git)
		"CURL_CA_BUNDLE": bundle, // curl (static fallback curl)
	}
	sandboxExtra := map[string]string{}
	for key, value := range caEnvs {
		if strings.TrimSpace(os.Getenv(key)) == "" {
			_ = os.Setenv(key, value)
		}
		sandboxExtra[key] = os.Getenv(key)
	}
	// SafeEnv strips ambient env inside the subprocess sandbox, so the CA
	// bundle must also travel via the sandbox extra-env contract.
	_ = os.Setenv(sandbox.SandboxExtraEnvEnv, mergeSandboxExtraEnv(os.Getenv(sandbox.SandboxExtraEnvEnv), sandboxExtra))
}

func findCABundle() string {
	for _, path := range systemCABundlePaths {
		if fileExists(path) {
			return path
		}
	}
	if fileExists(toolkitCABundle) {
		return toolkitCABundle
	}
	return ""
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func splitPathEntries(value string) []string {
	var out []string
	for _, entry := range filepath.SplitList(value) {
		if entry = strings.TrimSpace(entry); entry != "" {
			out = append(out, entry)
		}
	}
	return out
}

func joinPathEntries(entries []string) string {
	seen := map[string]struct{}{}
	cleaned := make([]string, 0, len(entries))
	for _, entry := range entries {
		if _, ok := seen[entry]; ok {
			continue
		}
		seen[entry] = struct{}{}
		cleaned = append(cleaned, entry)
	}
	return strings.Join(cleaned, string(os.PathListSeparator))
}

// appendPathList appends entries to an existing PATH-style list, preserving
// the existing order (operator/RuntimeProfile-provided entries stay first).
func appendPathList(existing string, entries ...string) string {
	merged := splitPathEntries(existing)
	merged = append(merged, entries...)
	return joinPathEntries(merged)
}

var kubernetesServiceAccountRoot = "/var/run/secrets/kubernetes.io/serviceaccount"

// setupKubernetesAdminSandboxEnv writes an in-cluster kubeconfig for the
// worker pod's service account and exposes KUBECONFIG to SDK subprocesses. It
// is intentionally gated by run.Spec.KubernetesAdmin before being called with
// enabled=true: ordinary runs should not receive Kubernetes client env.
func setupKubernetesAdminSandboxEnv(enabled bool) error {
	if !enabled {
		return nil
	}
	host := strings.TrimSpace(os.Getenv("KUBERNETES_SERVICE_HOST"))
	port := strings.TrimSpace(os.Getenv("KUBERNETES_SERVICE_PORT"))
	if host == "" || port == "" {
		return fmt.Errorf("KUBERNETES_SERVICE_HOST and KUBERNETES_SERVICE_PORT are required")
	}
	tokenPath := filepath.Join(kubernetesServiceAccountRoot, "token")
	tokenBytes, err := os.ReadFile(tokenPath)
	if err != nil {
		return fmt.Errorf("read service account token: %w", err)
	}
	token := strings.TrimSpace(string(tokenBytes))
	if token == "" {
		return fmt.Errorf("service account token is empty")
	}
	caPath := filepath.Join(kubernetesServiceAccountRoot, "ca.crt")
	if _, err := os.Stat(caPath); err != nil {
		return fmt.Errorf("stat service account CA: %w", err)
	}
	namespace := strings.TrimSpace(os.Getenv("POD_NAMESPACE"))
	if namespace == "" {
		if nsBytes, nsErr := os.ReadFile(filepath.Join(kubernetesServiceAccountRoot, "namespace")); nsErr == nil {
			namespace = strings.TrimSpace(string(nsBytes))
		}
	}
	if namespace == "" {
		namespace = "default"
	}
	home := ensureUsableHome(os.Getenv("HOME"), fallbackHomeDir)
	if home == "" {
		home = fallbackHomeDir
		if !writableDir(home) {
			return fmt.Errorf("no writable home directory for kubeconfig")
		}
	}
	kubeDir := filepath.Join(home, ".kube")
	if err := os.MkdirAll(kubeDir, 0o700); err != nil {
		return fmt.Errorf("create kubeconfig dir: %w", err)
	}
	kubeconfigPath := filepath.Join(kubeDir, "config")
	host = strings.TrimPrefix(strings.TrimSuffix(host, "]"), "[")
	server := "https://" + net.JoinHostPort(host, port)
	// Reference the projected token and CA by path instead of embedding
	// copies: the kubelet rotates the bound service-account token in place
	// (default TTL ~1h), so an embedded copy goes stale mid-session. The
	// command sandbox ro-binds /, so these paths stay readable for kubectl,
	// and no credential copy is persisted into $HOME.
	content := fmt.Sprintf(`apiVersion: v1
kind: Config
clusters:
- name: in-cluster
  cluster:
    server: %s
    certificate-authority: %s
users:
- name: worker
  user:
    tokenFile: %s
contexts:
- name: in-cluster
  context:
    cluster: in-cluster
    user: worker
    namespace: %s
current-context: in-cluster
`, server, caPath, tokenPath, namespace)
	if err := os.WriteFile(kubeconfigPath, []byte(content), 0o600); err != nil {
		return fmt.Errorf("write kubeconfig: %w", err)
	}
	_ = os.Setenv("KUBECONFIG", kubeconfigPath)
	_ = os.Setenv(sandbox.SandboxExtraEnvEnv, mergeSandboxExtraEnv(os.Getenv(sandbox.SandboxExtraEnvEnv), map[string]string{"KUBECONFIG": kubeconfigPath}))
	return nil
}

func commitAttributionPolicyPrompt() string {
	return "Commit attribution policy: credit the gratefulagents GitHub App as a co-author. The git tools add the configured Co-authored-by trailer automatically."
}

const (
	defaultGitIdentityName  = "gratefulagents[bot]"
	defaultGitIdentityEmail = "292420648+gratefulagents[bot]@users.noreply.github.com"
)

// setupGitIdentitySandboxEnv initializes and forwards the run's git identity.
// The controller injects GIT_AUTHOR_* / GIT_COMMITTER_* from the creating
// user's saved settings. Runs without saved settings use the default agent
// identity explicitly rather than relying on the image's global gitconfig:
// SafeEnv remaps HOME inside the command sandbox, and runtime images need not
// contain that config at all. Setting the process env also covers built-in git
// tools, while forwarding the same values covers raw git commands in Bash.
func setupGitIdentitySandboxEnv() {
	name := strings.TrimSpace(os.Getenv("GIT_AUTHOR_NAME"))
	email := strings.TrimSpace(os.Getenv("GIT_AUTHOR_EMAIL"))
	if name == "" || email == "" {
		name = defaultGitIdentityName
		email = defaultGitIdentityEmail
	}
	identity := map[string]string{
		"GIT_AUTHOR_NAME":     name,
		"GIT_AUTHOR_EMAIL":    email,
		"GIT_COMMITTER_NAME":  name,
		"GIT_COMMITTER_EMAIL": email,
	}
	for key, value := range identity {
		_ = os.Setenv(key, value)
	}
	_ = os.Setenv(sandbox.SandboxExtraEnvEnv, mergeSandboxExtraEnv(os.Getenv(sandbox.SandboxExtraEnvEnv), identity))
}

// mergeSandboxExtraEnv appends key=value pairs to the SDK sandbox extra-env
// contract without corrupting an existing operator-provided value. The SDK
// splits on newlines when any are present, otherwise on commas.
func mergeSandboxExtraEnv(existing string, kv map[string]string) string {
	keys := make([]string, 0, len(kv))
	for key := range kv {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	pairs := make([]string, 0, len(keys))
	for _, key := range keys {
		pairs = append(pairs, key+"="+kv[key])
	}
	existing = strings.TrimSpace(existing)
	if existing == "" {
		return strings.Join(pairs, "\n")
	}
	if strings.Contains(existing, "\n") {
		return existing + "\n" + strings.Join(pairs, "\n")
	}
	return existing + "," + strings.Join(pairs, ",")
}

// preflightTools verifies the runtime image + injected toolkit together
// provide everything the agent shells out to, failing fast with a precise
// message instead of surfacing cryptic mid-run tool errors.
func preflightTools() error {
	if _, err := os.Stat(toolkitBinDir); err != nil {
		// Not a toolkit-injected environment (local development, tests).
		return nil
	}

	var missing []string
	for _, tool := range []string{"bash", "git"} {
		if path, err := exec.LookPath(tool); err != nil {
			missing = append(missing, tool)
		} else {
			log.Printf("preflight: %s -> %s", tool, path)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf(
			"runtime image is missing required tools %v and the injected toolkit fallback (%s) did not provide them; "+
				"use a glibc-based Linux image with a shell, or check that the injector init container ran",
			missing, toolkitFallbackBinDir)
	}

	for _, tool := range []string{"gh", "curl", "jq", "rg", "fd"} {
		if path, err := exec.LookPath(tool); err != nil {
			log.Printf("preflight: WARN optional tool %q not found on PATH", tool)
		} else {
			log.Printf("preflight: %s -> %s", tool, path)
		}
	}

	return preflightSandbox()
}

// preflightSandbox verifies bubblewrap can actually create its namespaces on
// this node (a missing binary and a userns-restricted node fail differently).
func preflightSandbox() error {
	mode := strings.TrimSpace(os.Getenv(sandbox.SandboxModeEnv))
	if strings.EqualFold(mode, "disabled") {
		log.Printf("preflight: command sandbox disabled by run configuration; subprocesses run without bubblewrap")
		log.Print(commandSandboxCapabilityReport(mode, "not-probed", "disabled"))
		return nil
	}
	required := strings.EqualFold(mode, "required")

	bwrapPath, err := exec.LookPath("bwrap")
	if err != nil {
		if required {
			return fmt.Errorf("command sandbox is required but bwrap was not found on PATH "+
				"(runtime image lacks bubblewrap and toolkit fallback %s did not provide it)", toolkitFallbackBinDir)
		}
		log.Printf("preflight: WARN bwrap not found on PATH; subprocess sandboxing unavailable")
		log.Print(commandSandboxCapabilityReport(mode, "unavailable", "not-found"))
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bwrapPath, "--dev-bind", "/", "/", "true").CombinedOutput()
	if err != nil {
		detail := strings.TrimSpace(string(out))
		if required {
			return fmt.Errorf("command sandbox is required but bwrap cannot create namespaces on this node "+
				"(unprivileged user namespaces disabled?): %v: %s", err, detail)
		}
		log.Printf("preflight: WARN bwrap probe failed (%v: %s); subprocess sandboxing may not work", err, detail)
		log.Print(commandSandboxCapabilityReport(mode, bwrapPath, "namespace-probe-failed"))
		return nil
	}
	log.Printf("preflight: bwrap -> %s (namespace probe ok)", bwrapPath)
	log.Print(commandSandboxCapabilityReport(mode, bwrapPath, "namespace-probe-ok"))
	return nil
}

func commandSandboxCapabilityReport(mode, bwrapPath, bwrapStatus string) string {
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode != "disabled" && mode != "required" {
		mode = "auto"
	}
	filesystem := "readonly-scoped-writes"
	if mode == "disabled" {
		filesystem = "unrestricted"
	}
	return fmt.Sprintf("preflight: command-sandbox mode=%s bwrap_path=%s bwrap_status=%s procfs=runtime-probed sanitized_env=enabled filesystem=%s", mode, bwrapPath, bwrapStatus, filesystem)
}
