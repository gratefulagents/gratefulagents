package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gratefulagents/sdk/pkg/agentsdk/sandbox"
)

func TestAssembleProcessPathPreservesImagePathAndAppendsFallback(t *testing.T) {
	got := assembleProcessPath("/usr/local/bundle/bin:/usr/local/bin:/usr/bin:/bin", "/workspace/.cache/go")
	want := strings.Join([]string{
		toolkitBinDir,
		"/workspace/.cache/go/bin",
		"/usr/local/bundle/bin",
		"/usr/local/bin",
		"/usr/bin",
		"/bin",
		toolkitFallbackBinDir,
	}, ":")
	if got != want {
		t.Fatalf("assembleProcessPath = %q, want %q", got, want)
	}
}

func TestAssembleProcessPathEmptyImagePathFallsBackToStandardDirs(t *testing.T) {
	got := assembleProcessPath("", "")
	if !strings.HasPrefix(got, toolkitBinDir+":") {
		t.Fatalf("toolkit bin should be first, got %q", got)
	}
	if !strings.HasSuffix(got, ":"+toolkitFallbackBinDir) {
		t.Fatalf("fallback bin should be last, got %q", got)
	}
	if !strings.Contains(got, "/usr/bin") {
		t.Fatalf("standard dirs should be present when image PATH is empty, got %q", got)
	}
}

func TestAssembleProcessPathDedupsRepeatedEntries(t *testing.T) {
	got := assembleProcessPath("/usr/bin:/usr/bin:"+toolkitBinDir, "")
	if strings.Count(got, "/usr/bin") != 1 {
		t.Fatalf("expected /usr/bin deduped, got %q", got)
	}
	if strings.Count(got, toolkitBinDir) != 1 {
		t.Fatalf("expected toolkit bin deduped, got %q", got)
	}
}

func TestAppendPathListPreservesExistingOrder(t *testing.T) {
	got := appendPathList("/profile/tools:/other", "/img/bin", toolkitFallbackBinDir)
	want := "/profile/tools:/other:/img/bin:" + toolkitFallbackBinDir
	if got != want {
		t.Fatalf("appendPathList = %q, want %q", got, want)
	}
}

func TestAppendPathListEmptyExisting(t *testing.T) {
	got := appendPathList("", toolkitFallbackBinDir)
	if got != toolkitFallbackBinDir {
		t.Fatalf("appendPathList = %q, want %q", got, toolkitFallbackBinDir)
	}
}

func TestMergeSandboxExtraEnvEmptyExisting(t *testing.T) {
	got := mergeSandboxExtraEnv("", map[string]string{
		"SSL_CERT_FILE":  "/opt/ca.crt",
		"GIT_SSL_CAINFO": "/opt/ca.crt",
	})
	want := "GIT_SSL_CAINFO=/opt/ca.crt\nSSL_CERT_FILE=/opt/ca.crt"
	if got != want {
		t.Fatalf("mergeSandboxExtraEnv = %q, want %q", got, want)
	}
}

func TestMergeSandboxExtraEnvCommaExisting(t *testing.T) {
	// A comma-separated operator value must stay comma-separated: mixing in a
	// newline would flip the SDK parser into newline mode and corrupt it.
	got := mergeSandboxExtraEnv("JAVA_HOME=/opt/jdk,FOO=bar", map[string]string{"SSL_CERT_FILE": "/opt/ca.crt"})
	want := "JAVA_HOME=/opt/jdk,FOO=bar,SSL_CERT_FILE=/opt/ca.crt"
	if got != want {
		t.Fatalf("mergeSandboxExtraEnv = %q, want %q", got, want)
	}
}

func TestMergeSandboxExtraEnvNewlineExisting(t *testing.T) {
	got := mergeSandboxExtraEnv("JAVA_HOME=/opt/jdk\nFOO=bar", map[string]string{"SSL_CERT_FILE": "/opt/ca.crt"})
	want := "JAVA_HOME=/opt/jdk\nFOO=bar\nSSL_CERT_FILE=/opt/ca.crt"
	if got != want {
		t.Fatalf("mergeSandboxExtraEnv = %q, want %q", got, want)
	}
}

func TestCommandSandboxCapabilityReport(t *testing.T) {
	tests := []struct {
		name       string
		mode       string
		bwrapPath  string
		bwrapState string
		want       []string
	}{
		{
			name:       "required sandbox available",
			mode:       "required",
			bwrapPath:  "/opt/gratefulagents/fallback/bin/bwrap",
			bwrapState: "namespace-probe-ok",
			want: []string{
				"mode=required",
				"bwrap_path=/opt/gratefulagents/fallback/bin/bwrap",
				"bwrap_status=namespace-probe-ok",
				"procfs=runtime-probed",
				"sanitized_env=enabled",
				"filesystem=readonly-scoped-writes",
			},
		},
		{
			name:       "disabled sandbox",
			mode:       "disabled",
			bwrapPath:  "not-probed",
			bwrapState: "disabled",
			want: []string{
				"mode=disabled",
				"bwrap_path=not-probed",
				"bwrap_status=disabled",
				"procfs=runtime-probed",
				"sanitized_env=enabled",
				"filesystem=unrestricted",
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := commandSandboxCapabilityReport(test.mode, test.bwrapPath, test.bwrapState)
			for _, want := range test.want {
				if !strings.Contains(got, want) {
					t.Fatalf("commandSandboxCapabilityReport() = %q, missing %q", got, want)
				}
			}
		})
	}
}

func TestSetupToolkitEnvNoopWithoutToolkit(t *testing.T) {
	// The bootstrap and preflight must be inert when the toolkit is absent.
	// Point the toolkit root at a nonexistent scratch path so the test stays
	// hermetic on hosts that really have /opt/gratefulagents (e.g. dogfooding
	// runners).
	oldRoot, oldBin, oldFallback, oldCA := toolkitRoot, toolkitBinDir, toolkitFallbackBinDir, toolkitCABundle
	toolkitRoot = filepath.Join(t.TempDir(), "missing-toolkit")
	toolkitBinDir = toolkitRoot + "/bin"
	toolkitFallbackBinDir = toolkitRoot + "/fallback/bin"
	toolkitCABundle = toolkitRoot + "/etc/ca-certificates.crt"
	t.Cleanup(func() {
		toolkitRoot, toolkitBinDir, toolkitFallbackBinDir, toolkitCABundle = oldRoot, oldBin, oldFallback, oldCA
	})

	t.Setenv("PATH", "/usr/bin:/bin")
	setupToolkitEnv()
	if got := os.Getenv("PATH"); got != "/usr/bin:/bin" {
		t.Fatalf("setupToolkitEnv should not touch PATH without toolkit, got %q", got)
	}
	if err := preflightTools(); err != nil {
		t.Fatalf("preflightTools should be a no-op without toolkit: %v", err)
	}
}

func TestEnsureUsableHomeKeepsWritableHome(t *testing.T) {
	home := t.TempDir()
	if got := ensureUsableHome(home, "/tmp/home"); got != home {
		t.Fatalf("ensureUsableHome = %q, want existing home %q", got, home)
	}
}

func TestEnsureUsableHomeCreatesMissingHome(t *testing.T) {
	// Images may declare a HOME that does not exist yet but is creatable.
	home := filepath.Join(t.TempDir(), "newhome")
	if got := ensureUsableHome(home, "/tmp/home"); got != home {
		t.Fatalf("ensureUsableHome = %q, want creatable home %q", got, home)
	}
	if info, err := os.Stat(home); err != nil || !info.IsDir() {
		t.Fatalf("expected home dir to be created, stat: %v", err)
	}
}

func TestEnsureUsableHomeFallsBackWhenUnsetOrRoot(t *testing.T) {
	// UID without a passwd entry gets HOME unset or "/" — both must fall back
	// (this broke gh auth with "mkdir /.config: permission denied").
	fallback := filepath.Join(t.TempDir(), "home")
	for _, home := range []string{"", "/", "  "} {
		if got := ensureUsableHome(home, fallback); got != fallback {
			t.Fatalf("ensureUsableHome(%q) = %q, want fallback %q", home, got, fallback)
		}
	}
}

func TestEnsureUsableHomeFallsBackWhenHomeNotWritable(t *testing.T) {
	// A HOME pointing at a plain file cannot host config dirs.
	notADir := filepath.Join(t.TempDir(), "occupied")
	if err := os.WriteFile(notADir, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	fallback := filepath.Join(t.TempDir(), "home")
	if got := ensureUsableHome(notADir, fallback); got != fallback {
		t.Fatalf("ensureUsableHome = %q, want fallback %q", got, fallback)
	}
}

func TestEnsureUsableHomeKeepsOriginalWhenFallbackUnusable(t *testing.T) {
	badFallback := filepath.Join(t.TempDir(), "occupied")
	if err := os.WriteFile(badFallback, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := ensureUsableHome("/", badFallback); got != "/" {
		t.Fatalf("ensureUsableHome = %q, want original home kept", got)
	}
}

func TestSetupKubernetesAdminSandboxEnvNoopWhenDisabled(t *testing.T) {
	t.Setenv("KUBECONFIG", "")
	t.Setenv(sandbox.SandboxExtraEnvEnv, "EXISTING=1")
	if err := setupKubernetesAdminSandboxEnv(false); err != nil {
		t.Fatalf("setupKubernetesAdminSandboxEnv(false) error = %v", err)
	}
	if got := os.Getenv("KUBECONFIG"); got != "" {
		t.Fatalf("KUBECONFIG = %q, want empty", got)
	}
	if got := os.Getenv(sandbox.SandboxExtraEnvEnv); got != "EXISTING=1" {
		t.Fatalf("sandbox extra env = %q, want unchanged", got)
	}
}

func TestSetupKubernetesAdminSandboxEnvWritesKubeconfig(t *testing.T) {
	oldRoot := kubernetesServiceAccountRoot
	root := t.TempDir()
	kubernetesServiceAccountRoot = root
	t.Cleanup(func() { kubernetesServiceAccountRoot = oldRoot })

	if err := os.WriteFile(filepath.Join(root, "token"), []byte("test-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "ca.crt"), []byte("ca-data"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "namespace"), []byte("run-ns"), 0o600); err != nil {
		t.Fatal(err)
	}

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("POD_NAMESPACE", "")
	t.Setenv("KUBERNETES_SERVICE_HOST", "10.0.0.1")
	t.Setenv("KUBERNETES_SERVICE_PORT", "443")
	t.Setenv(sandbox.SandboxExtraEnvEnv, "EXISTING=1")

	if err := setupKubernetesAdminSandboxEnv(true); err != nil {
		t.Fatalf("setupKubernetesAdminSandboxEnv(true) error = %v", err)
	}
	kubeconfig := os.Getenv("KUBECONFIG")
	if kubeconfig == "" {
		t.Fatal("KUBECONFIG is empty")
	}
	if !strings.HasPrefix(kubeconfig, filepath.Join(home, ".kube")) {
		t.Fatalf("KUBECONFIG = %q, want under HOME .kube", kubeconfig)
	}
	content, err := os.ReadFile(kubeconfig)
	if err != nil {
		t.Fatalf("read kubeconfig: %v", err)
	}
	text := string(content)
	for _, want := range []string{
		"server: https://10.0.0.1:443",
		"tokenFile: " + filepath.Join(root, "token"),
		"certificate-authority: " + filepath.Join(root, "ca.crt"),
		"namespace: run-ns",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("kubeconfig %q missing %q", text, want)
		}
	}
	// The projected token rotates in place; the kubeconfig must reference it
	// by path, never embed a stale copy of its value.
	if strings.Contains(text, "test-token") {
		t.Fatalf("kubeconfig %q embeds the token value, want tokenFile reference", text)
	}
	if got := os.Getenv(sandbox.SandboxExtraEnvEnv); !strings.Contains(got, "KUBECONFIG="+kubeconfig) || !strings.Contains(got, "EXISTING=1") {
		t.Fatalf("sandbox extra env = %q, want existing + KUBECONFIG", got)
	}
}

func TestSetupKubernetesAdminSandboxEnvIPv6Server(t *testing.T) {
	oldRoot := kubernetesServiceAccountRoot
	root := t.TempDir()
	kubernetesServiceAccountRoot = root
	t.Cleanup(func() { kubernetesServiceAccountRoot = oldRoot })
	if err := os.WriteFile(filepath.Join(root, "token"), []byte("test-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "ca.crt"), []byte("ca-data"), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, host := range []string{"fd00::1", "[fd00::1]"} {
		t.Run(host, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			t.Setenv("POD_NAMESPACE", "default")
			t.Setenv("KUBERNETES_SERVICE_HOST", host)
			t.Setenv("KUBERNETES_SERVICE_PORT", "443")
			if err := setupKubernetesAdminSandboxEnv(true); err != nil {
				t.Fatalf("setupKubernetesAdminSandboxEnv(true) error = %v", err)
			}
			content, err := os.ReadFile(os.Getenv("KUBECONFIG"))
			if err != nil {
				t.Fatalf("read kubeconfig: %v", err)
			}
			if !strings.Contains(string(content), "server: https://[fd00::1]:443") {
				t.Fatalf("kubeconfig %q does not contain bracketed IPv6 server", content)
			}
		})
	}
}

func TestCommitAttributionPolicyPrompt(t *testing.T) {
	got := commitAttributionPolicyPrompt()
	if !strings.Contains(got, "gratefulagents GitHub App") || !strings.Contains(got, "add the configured Co-authored-by trailer") {
		t.Fatalf("policy prompt = %q", got)
	}
}

func TestSetupGitIdentitySandboxEnv(t *testing.T) {
	t.Setenv("GIT_AUTHOR_NAME", "Alice Doe")
	t.Setenv("GIT_AUTHOR_EMAIL", "alice@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "Alice Doe")
	t.Setenv("GIT_COMMITTER_EMAIL", "alice@example.com")
	t.Setenv(sandbox.SandboxExtraEnvEnv, "EXISTING=1")

	setupGitIdentitySandboxEnv()

	got := os.Getenv(sandbox.SandboxExtraEnvEnv)
	for _, want := range []string{
		"EXISTING=1",
		"GIT_AUTHOR_NAME=Alice Doe",
		"GIT_AUTHOR_EMAIL=alice@example.com",
		"GIT_COMMITTER_NAME=Alice Doe",
		"GIT_COMMITTER_EMAIL=alice@example.com",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("sandbox extra env %q missing %q", got, want)
		}
	}
}

func TestSetupGitIdentitySandboxEnvUsesDefaultIdentityWhenUnset(t *testing.T) {
	t.Setenv("GIT_AUTHOR_NAME", "")
	t.Setenv("GIT_AUTHOR_EMAIL", "")
	t.Setenv("GIT_COMMITTER_NAME", "")
	t.Setenv("GIT_COMMITTER_EMAIL", "")
	t.Setenv(sandbox.SandboxExtraEnvEnv, "EXISTING=1")

	setupGitIdentitySandboxEnv()

	wantEnv := map[string]string{
		"GIT_AUTHOR_NAME":     defaultGitIdentityName,
		"GIT_AUTHOR_EMAIL":    defaultGitIdentityEmail,
		"GIT_COMMITTER_NAME":  defaultGitIdentityName,
		"GIT_COMMITTER_EMAIL": defaultGitIdentityEmail,
	}
	for key, want := range wantEnv {
		if got := os.Getenv(key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
		if got := os.Getenv(sandbox.SandboxExtraEnvEnv); !strings.Contains(got, key+"="+want) {
			t.Errorf("sandbox extra env %q missing %s=%s", got, key, want)
		}
	}
}

func TestSetupGitIdentitySandboxEnvRejectsPartialIdentity(t *testing.T) {
	t.Setenv("GIT_AUTHOR_NAME", "Alice Doe")
	t.Setenv("GIT_AUTHOR_EMAIL", "")
	t.Setenv("GIT_COMMITTER_NAME", "Alice Doe")
	t.Setenv("GIT_COMMITTER_EMAIL", "")
	t.Setenv(sandbox.SandboxExtraEnvEnv, "")

	setupGitIdentitySandboxEnv()

	if got := os.Getenv("GIT_AUTHOR_NAME"); got != defaultGitIdentityName {
		t.Fatalf("GIT_AUTHOR_NAME = %q, want default %q", got, defaultGitIdentityName)
	}
	if got := os.Getenv("GIT_AUTHOR_EMAIL"); got != defaultGitIdentityEmail {
		t.Fatalf("GIT_AUTHOR_EMAIL = %q, want default %q", got, defaultGitIdentityEmail)
	}
}
