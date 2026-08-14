package securitytoolpacks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// ProcessSandbox executes a registry-produced argv vector directly. It never
// invokes a shell and therefore cannot reinterpret target values as shell
// syntax. The worker pod remains the outer CPU, memory, privilege, and network
// sandbox; this executor additionally enforces the declared wall-clock and
// output-size budgets.
type ProcessSandbox struct{}

//nolint:gocyclo // Execution keeps sandbox setup, quota enforcement, collection, and status mapping in one auditable path.
func (ProcessSandbox) Execute(ctx context.Context, req ExecutionRequest) (result NativeResult) {
	// Operator-configured fork endpoints carry API keys. Nothing this executor
	// returns may echo one, so every exit path is scrubbed.
	secrets := &operatorSecret{}
	defer func() { result = secrets.scrub(result) }()
	if len(req.Invocation.Argv) == 0 {
		return NativeResult{ExitCode: -1, Err: errors.New("empty registered invocation")}
	}
	if result, ok := executeBuiltin(req); ok {
		return result
	}
	if err := verifyFileTarget(req.Config.Target); err != nil {
		return NativeResult{ExitCode: -1, Err: err}
	}
	ctx, cancel := context.WithTimeout(ctx, req.Invocation.Budgets.Timeout)
	defer cancel()

	limit := req.Invocation.Budgets.MaxOutputSize
	stdout := &limitedBuffer{limit: limit}
	stderr := &limitedBuffer{limit: min(limit, 1<<20)}
	argv := slices.Clone(req.Invocation.Argv)
	executionTarget := req.Config.Target.Locator
	if snapshot, cleanup, err := snapshotDirectoryTarget(req.Config.Target); err != nil {
		return NativeResult{ExitCode: -1, Err: err}
	} else if snapshot != "" {
		defer cleanup()
		executionTarget = snapshot
		for i := range argv {
			if argv[i] == req.Config.Target.Locator {
				argv[i] = snapshot
			}
		}
	}
	if req.Tool.Name == "owasp-zap" {
		if err := validateZAPPlan(executionTarget, req.Config.Arguments["base_url"], req.Config.Scope); err != nil {
			return NativeResult{ExitCode: -1, Err: err}
		}
	}
	if req.Tool.Name == "schemathesis" {
		if err := validateOpenAPIBudget(executionTarget, 100); err != nil {
			return NativeResult{ExitCode: -1, Err: err}
		}
	}
	if req.Tool.Name == "slither" && runtime.GOARCH == "arm64" {
		return NativeResult{ExitCode: -1, Err: fmt.Errorf("slither project compilation is unsupported on arm64: the pinned upstream toolbox embeds an amd64 solc artifact")}
	}
	ociWork := ""
	if req.Tool.OCIRoot != "" {
		prepared, work, cleanup, err := prepareOCIInvocation(req.Tool, argv, executionTarget)
		if err != nil {
			return NativeResult{ExitCode: -1, Err: err}
		}
		defer cleanup()
		argv, ociWork = prepared, work
	}
	if isLockedExternalTool(req.Tool.Name) {
		binary, err := trustedToolBinary(argv[0], req.Tool.ToolArtifactDigest)
		if err != nil {
			return NativeResult{ExitCode: -1, Err: err}
		}
		argv[0] = binary
	}
	if req.Tool.Name == "nuclei" {
		knowledge, err := trustedNucleiKnowledge(req.Tool.KnowledgeDigests["bundle"])
		if err != nil {
			return NativeResult{ExitCode: -1, Err: err}
		}
		for i := range argv {
			if argv[i] == "@operator/nuclei-reviewed.yaml" {
				argv[i] = knowledge
			}
		}
	}
	if err := resolveOperatorEVMTokens(ctx, argv, executionTarget, secrets); err != nil {
		return NativeResult{ExitCode: -1, Err: err}
	}
	home, homeErr := os.MkdirTemp("", "ga-security-home-*")
	if homeErr != nil {
		return NativeResult{ExitCode: -1, Err: homeErr}
	}
	defer func() { _ = os.RemoveAll(home) }()
	childEnvironment := deterministicEnvironment(req.Tool.Name, home)
	if staged, handled := executeEVMStage(ctx, req, argv, executionTarget, childEnvironment, secrets); handled {
		return staged
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...) // #nosec G204 -- executable and argv come only from the validated static registry.
	cmd.Env = childEnvironment
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	goFuzzBaseline := map[string]bool{}
	if req.Tool.Name == "go-fuzz-tests" {
		var baselineErr error
		goFuzzBaseline, baselineErr = goFuzzCorpusPaths(executionTarget)
		if baselineErr != nil {
			return NativeResult{ExitCode: -1, Err: fmt.Errorf("inventory Go fuzz corpus: %w", baselineErr)}
		}
	}
	var err error
	if ociWork != "" {
		quotaDirectories := []string{ociWork}
		quotaLimit, quotaEntries := limit, 4096
		if req.Tool.OCIWritableTarget {
			baselineBytes, baselineEntries, usageErr := directoryUsage(executionTarget)
			if usageErr != nil {
				return NativeResult{ExitCode: -1, Err: usageErr}
			}
			quotaLimit += baselineBytes
			quotaEntries += baselineEntries
			quotaDirectories = append(quotaDirectories, executionTarget)
		}
		err = runWithDirectoryLimits(cmd, quotaDirectories, quotaLimit, quotaEntries)
	} else {
		err = cmd.Run()
	}
	ociOutputCollected := req.Tool.OCIRoot == "" || req.Tool.OCIOutputPath == ""
	if req.Tool.OCIOutputPath != "" && ociWork != "" {
		if output, readErr := readBoundedOCIOutput(ociWork, filepath.Base(req.Tool.OCIOutputPath), limit); readErr == nil {
			if req.Tool.Name == "owasp-zap" && !zapReportExaminedTarget(output, req.Config.Arguments["base_url"], req.Config.Scope) {
				if err == nil {
					err = fmt.Errorf("ZAP report contains no examined site")
				}
			} else {
				ociOutputCollected = true
			}
			stdout.buf.Reset()
			_, _ = stdout.buf.Write(output)
		} else if errors.Is(readErr, errOutputTooLarge) {
			stdout.overflow = true
		} else if !os.IsNotExist(readErr) || slices.Contains([]string{"owasp-zap", "schemathesis"}, req.Tool.Name) {
			if err == nil {
				err = fmt.Errorf("collect OCI output: %w", readErr)
			}
		}
	}

	environment := map[string]string{"os": runtime.GOOS, "arch": runtime.GOARCH, "runtime": runtime.Version()}
	if req.Tool.OCIRoot != "" {
		sandboxPath := argv[0]
		if len(argv) > 4 && argv[1] == "__sandbox-exec" {
			sandboxPath = argv[4]
		}
		if data, readErr := os.ReadFile(sandboxPath); readErr == nil {
			environment["sandbox_digest"] = sha256Digest(data)
		} else if err == nil {
			err = fmt.Errorf("hash sandbox runtime: %w", readErr)
		}
		environment["scanner_platform_digest"] = req.Tool.PlatformDigests[runtime.GOARCH]
		environment["wrapper_digest"] = req.Tool.WrapperDigest
	}
	exitCode := processExitCode(err)
	result = NativeResult{Output: stdout.Bytes(), ExitCode: exitCode, Environment: environment}
	if req.Tool.Name == "go-fuzz-tests" {
		artifacts, collectErr := collectGoFuzzArtifacts(executionTarget, goFuzzBaseline, limit)
		if collectErr != nil && err == nil {
			err = fmt.Errorf("collect Go fuzz reproducer: %w", collectErr)
		} else {
			result.Artifacts = artifacts
		}
	}
	completeEvidence := ociOutputCollected || (req.Tool.Name == "zeek" && exitCode == 0)
	if exitCode >= 0 && completeEvidence {
		result.Examined = []string{req.Config.Target.Locator}
	} else if req.Tool.OCIRoot != "" {
		result.Uncovered = []string{req.Config.Target.Locator}
	}
	if ctx.Err() == context.DeadlineExceeded {
		result.TimedOut = true
		result.Err = context.DeadlineExceeded
		return result
	}
	if stdout.overflow || stderr.overflow {
		result.Err = fmt.Errorf("tool output exceeded budget (stdout limit %d bytes)", limit)
		return result
	}
	if err != nil {
		mapped := req.Tool.ExitCodes[exitCode]
		if mapped != StatusPass && mapped != StatusFindings {
			message := stderr.String()
			if message == "" {
				message = err.Error()
			}
			result.Err = errors.New(message)
		}
	}
	return result
}

func goFuzzCorpusPaths(root string) (map[string]bool, error) {
	paths := map[string]bool{}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		parts := strings.Split(filepath.ToSlash(rel), "/")
		for i := 0; i+2 < len(parts); i++ {
			if parts[i] == "testdata" && parts[i+1] == "fuzz" {
				paths[path] = true
				break
			}
		}
		return nil
	})
	return paths, err
}

func collectGoFuzzArtifacts(root string, baseline map[string]bool, maxBytes int64) ([]Artifact, error) {
	const maxArtifacts = 100
	all, err := goFuzzCorpusPaths(root)
	if err != nil {
		return nil, err
	}
	paths := make([]string, 0, len(all))
	for path := range all {
		if !baseline[path] {
			paths = append(paths, path)
		}
	}
	if len(paths) > maxArtifacts {
		return nil, fmt.Errorf("more than %d new Go fuzz corpus artifacts", maxArtifacts)
	}
	sort.Strings(paths)
	artifacts := make([]Artifact, 0, len(paths))
	var total int64
	for _, path := range paths {
		info, statErr := os.Stat(path)
		if statErr != nil {
			return nil, statErr
		}
		total += info.Size()
		if total > maxBytes {
			return nil, fmt.Errorf("Go fuzz corpus exceeds %d-byte artifact budget", maxBytes)
		}
		data, readErr := os.ReadFile(path) // #nosec G304 -- path is rooted in the private immutable target snapshot.
		if readErr != nil {
			return nil, readErr
		}
		rel, _ := filepath.Rel(root, path)
		artifacts = append(artifacts, Artifact{Name: "go-fuzz-corpus/" + filepath.ToSlash(rel), MediaType: "application/octet-stream", Digest: sha256Digest(data), Size: len(data), Data: data})
	}
	return artifacts, nil
}

func zapReportExaminedTarget(output []byte, baseURL string, scopes []string) bool {
	var report struct {
		Sites []struct {
			Name string `json:"@name"`
			Host string `json:"@host"`
		} `json:"site"`
	}
	if json.Unmarshal(output, &report) != nil {
		return false
	}
	for _, site := range report.Sites {
		identity := site.Name
		if identity == "" {
			identity = site.Host
		}
		parsed, err := url.ParseRequestURI(identity)
		if err == nil && parsed.IsAbs() && safeURLPath(parsed.EscapedPath()) && scopeAllowsTarget(identity, scopes) && scopeAllowsTarget(identity, []string{baseURL}) {
			return true
		}
	}
	return false
}

func runWithDirectoryLimit(cmd *exec.Cmd, directory string, limit int64) error {
	return runWithDirectoryLimits(cmd, []string{directory}, limit, 4096)
}

func runWithDirectoryLimits(cmd *exec.Cmd, directories []string, limit int64, maxEntries int) error {
	if err := checkDirectoriesQuota(directories, limit, maxEntries); err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	stop := make(chan struct{})
	monitorDone := make(chan struct{})
	violation := make(chan error, 1)
	go func() {
		defer close(monitorDone)
		ticker := time.NewTicker(25 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				if err := checkDirectoriesQuota(directories, limit, maxEntries); err != nil {
					select {
					case violation <- err:
					default:
					}
					_ = cmd.Process.Kill()
					return
				}
			}
		}
	}()
	err := cmd.Wait()
	close(stop)
	<-monitorDone
	select {
	case quotaErr := <-violation:
		return quotaErr
	default:
		if quotaErr := checkDirectoriesQuota(directories, limit, maxEntries); quotaErr != nil {
			return quotaErr
		}
		return err
	}
}

func directoryUsage(root string) (int64, int, error) {
	var total int64
	entries := 0
	err := filepath.WalkDir(root, func(_ string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		entries++
		if entry.Type().IsRegular() {
			info, err := entry.Info()
			if err != nil {
				return err
			}
			total += info.Size()
		}
		return nil
	})
	return total, entries, err
}

func checkDirectoryQuota(root string, limit int64, maxEntries int) error {
	return checkDirectoriesQuota([]string{root}, limit, maxEntries)
}

func checkDirectoriesQuota(roots []string, limit int64, maxEntries int) error {
	var total int64
	entries := 0
	for _, root := range roots {
		err := filepath.WalkDir(root, func(_ string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			entries++
			if entries > maxEntries {
				return fmt.Errorf("OCI writable directories exceed %d entries", maxEntries)
			}
			if entry.Type().IsRegular() {
				info, err := entry.Info()
				if err != nil {
					return err
				}
				if info.Size() > limit-total {
					return errOutputTooLarge
				}
				total += info.Size()
			} else if entry.Type()&os.ModeSymlink != 0 {
				return fmt.Errorf("OCI writable directory contains a symlink")
			} else if !entry.IsDir() {
				return fmt.Errorf("OCI writable directory contains a special file")
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	return nil
}

var errOutputTooLarge = errors.New("OCI output exceeds configured limit")

func readBoundedOCIOutput(directory, name string, limit int64) ([]byte, error) {
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	info, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("OCI output must be a regular file")
	}
	if info.Size() > limit {
		return nil, errOutputTooLarge
	}
	file, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	openedInfo, err := file.Stat()
	if err != nil || !openedInfo.Mode().IsRegular() {
		return nil, fmt.Errorf("OCI output changed or is not regular")
	}
	if openedInfo.Size() > limit {
		return nil, errOutputTooLarge
	}
	output, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(output)) > limit {
		return nil, errOutputTooLarge
	}
	return output, nil
}

func verifyOperatorOwnedPath(path string) error {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return err
	}
	for current := resolved; ; current = filepath.Dir(current) {
		info, statErr := os.Stat(current)
		if statErr != nil {
			return statErr
		}
		if info.Mode().Perm()&0o022 != 0 {
			return fmt.Errorf("%s is group/other writable", current)
		}
		if stat, ok := info.Sys().(*syscall.Stat_t); ok && stat.Uid != 0 {
			return fmt.Errorf("%s is not owned by root", current)
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
	}
	return nil
}

func prepareOCIInvocation(tool Tool, toolArgv []string, executionTarget string) ([]string, string, func(), error) {
	executable, err := os.Executable()
	if err != nil {
		return nil, "", func() {}, err
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		return nil, "", func() {}, err
	}
	if err := verifyOperatorOwnedPath(executable); err != nil {
		return nil, "", func() {}, fmt.Errorf("untrusted wrapper: %w", err)
	}
	if data, readErr := os.ReadFile(executable); readErr != nil || sha256Digest(data) != tool.WrapperDigest {
		return nil, "", func() {}, fmt.Errorf("wrapper digest mismatch")
	}
	root := filepath.Dir(filepath.Dir(executable))
	rootCandidates := []string{
		filepath.Join(root, "toolroots", tool.OCIRoot),
		filepath.Join(root, "share", "ga-security", "toolroots", tool.OCIRoot),
	}
	var toolRoot string
	for _, candidate := range rootCandidates {
		marker, readErr := os.ReadFile(filepath.Join(candidate, ".ga-oci-digest")) // #nosec G304 -- operator-relative static root.
		if readErr == nil && strings.TrimSpace(string(marker)) == tool.ToolArtifactDigest {
			toolRoot = candidate
			break
		}
	}
	if toolRoot == "" {
		return nil, "", func() {}, fmt.Errorf("pinned OCI root %s is unavailable or has invalid provenance", tool.OCIRoot)
	}
	if err := verifyOperatorOwnedPath(toolRoot); err != nil {
		return nil, "", func() {}, fmt.Errorf("untrusted OCI root: %w", err)
	}
	bwrapCandidates := []string{filepath.Join(root, "fallback", "bin", "bwrap"), filepath.Join(filepath.Dir(executable), "bwrap")}
	var bwrap string
	for _, candidate := range bwrapCandidates {
		if info, statErr := os.Stat(candidate); statErr == nil && info.Mode().IsRegular() {
			bwrap = candidate
			break
		}
	}
	if bwrap == "" {
		return nil, "", func() {}, fmt.Errorf("operator bubblewrap runtime is unavailable")
	}
	if err := verifyOperatorOwnedPath(bwrap); err != nil {
		return nil, "", func() {}, fmt.Errorf("untrusted bubblewrap runtime: %w", err)
	}
	work, err := os.MkdirTemp("", "ga-security-oci-work-*")
	if err != nil {
		return nil, "", func() {}, err
	}
	cleanup := func() { _ = os.RemoveAll(work) }
	path := tool.OCIPath
	if path == "" {
		path = "/usr/local/zeek/bin:/opt/venv/bin:/usr/local/bin:/usr/bin:/bin"
	}
	argv := []string{bwrap, "--die-with-parent", "--new-session", "--unshare-pid", "--ro-bind", toolRoot, "/", "--proc", "/proc", "--dev", "/dev", "--tmpfs", "/dev/shm", "--tmpfs", "/tmp", "--bind", work, "/work", "--chdir", "/work", "--clearenv", "--setenv", "HOME", "/work", "--setenv", "LANG", "C.UTF-8", "--setenv", "PATH", path}
	if tool.Name == "slither" {
		// The immutable toolbox installs Slither into ethsec's user base. The
		// sandbox intentionally changes HOME to its writable work directory, so
		// preserve the pinned user base and let Python select its own versioned
		// site-packages directory.
		argv = append(argv,
			// This toolbox's solc-select wrapper derives its artifact directory
			// from HOME and its release requires an installed version. Restore the
			// immutable toolbox home; do not force a version absent on one arch.
			"--setenv", "HOME", "/home/ethsec",
			"--setenv", "PYTHONUSERBASE", "/home/ethsec/.local",
			"--setenv", "XDG_CACHE_HOME", "/work/.cache",
		)
	}
	if tool.Name == "halmos" {
		argv = append(argv, "--setenv", "FOUNDRY_FFI", "false")
	}
	if tool.Requirements.Network {
		for _, hostFile := range []string{"/etc/resolv.conf", "/etc/hosts"} {
			if _, statErr := os.Stat(filepath.Join(toolRoot, hostFile)); statErr == nil {
				argv = append(argv, "--ro-bind", hostFile, hostFile)
			}
		}
	} else {
		argv = append(argv, "--unshare-net")
	}
	if info, statErr := os.Stat(executionTarget); statErr == nil {
		_ = info
		bindMode := "--ro-bind"
		if tool.OCIWritableTarget {
			bindMode = "--bind"
		}
		argv = append(argv, bindMode, executionTarget, "/tmp/input")
		for i := range toolArgv {
			if toolArgv[i] == executionTarget {
				toolArgv[i] = "/tmp/input"
			}
		}
	}
	argv = append(argv, "--", tool.OCIExecutable)
	argv = append(argv, toolArgv[1:]...)
	launcher := []string{executable, "__sandbox-exec", strconv.FormatInt(tool.Budgets.MaxOutputSize, 10), strconv.FormatInt(tool.Budgets.Memory, 10)}
	return append(launcher, argv...), work, cleanup, nil
}

// isLockedExternalTool names the packs whose executable is extracted from the
// checksum-verified runtime lock. Their argv[0] is resolved inside the operator
// toolkit and verified against the locked artifact digest before exec, so a
// PATH entry can never stand in for the reviewed binary.
func isLockedExternalTool(name string) bool {
	return slices.Contains([]string{"nuclei", "naabu", "aderyn", "forge-security-tests", "echidna", "anvil-fork", "forge-fork-test", "forge-coverage-mutation"}, name)
}

func trustedToolBinary(binaryName, expectedDigest string) (string, error) {
	if filepath.Base(binaryName) != binaryName {
		return "", fmt.Errorf("locked tool executable must be a basename")
	}
	executable, err := os.Executable()
	if err != nil {
		return "", err
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		return "", err
	}
	root := filepath.Dir(filepath.Dir(executable))
	candidates := []string{
		filepath.Join(root, "fallback", "bin", binaryName),
		filepath.Join(filepath.Dir(executable), binaryName),
	}
	for _, candidate := range candidates {
		data, readErr := os.ReadFile(candidate) // #nosec G304 -- candidate is derived only from operator binary location and static argv.
		if readErr != nil {
			continue
		}
		if sha256Digest(data) != expectedDigest {
			return "", fmt.Errorf("locked tool binary digest mismatch for %s", candidate)
		}
		return candidate, nil
	}
	return "", fmt.Errorf("locked tool %s was not found in the operator toolkit", binaryName)
}

func deterministicEnvironment(toolName, home string) []string {
	environment := []string{"HOME=" + home, "LANG=C.UTF-8", "LC_ALL=C.UTF-8", "PATH=" + os.Getenv("PATH")}
	for _, name := range []string{"SSL_CERT_FILE", "SSL_CERT_DIR"} {
		if value := os.Getenv(name); value != "" {
			environment = append(environment, name+"="+value)
		}
	}
	if slices.Contains([]string{"forge-security-tests", "anvil-fork", "forge-fork-test", "forge-coverage-mutation"}, toolName) {
		environment = append(environment, "FOUNDRY_FFI=false")
	}
	sort.Strings(environment)
	return environment
}

func trustedNucleiKnowledge(expectedDigest string) (string, error) {
	executable, err := os.Executable()
	if err != nil {
		return "", err
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		return "", err
	}
	root := filepath.Dir(filepath.Dir(executable))
	candidates := []string{
		filepath.Join(root, "security", "nuclei-reviewed.yaml"),
		filepath.Join(root, "share", "ga-security", "knowledge", "nuclei-reviewed.yaml"),
	}
	for _, candidate := range candidates {
		data, readErr := os.ReadFile(candidate) // #nosec G304 -- path derives only from operator executable location.
		if readErr != nil {
			continue
		}
		if actual := sha256Digest(data); actual != expectedDigest {
			return "", fmt.Errorf("nuclei knowledge digest mismatch")
		}
		return candidate, nil
	}
	return "", fmt.Errorf("reviewed Nuclei knowledge was not found in the operator toolkit")
}

func snapshotDirectoryTarget(target Target) (string, func(), error) {
	info, err := os.Lstat(target.Locator)
	if err != nil {
		if os.IsNotExist(err) {
			return "", func() {}, nil
		}
		return "", func() {}, err
	}
	if !info.IsDir() && !info.Mode().IsRegular() {
		return "", func() {}, fmt.Errorf("target must be a regular file or directory")
	}
	root, err := os.MkdirTemp("", "ga-security-target-*")
	if err != nil {
		return "", func() {}, err
	}
	cleanup := func() { _ = os.RemoveAll(root) }
	snapshot := filepath.Join(root, "input")
	if info.Mode().IsRegular() {
		source, openErr := os.Open(target.Locator) // #nosec G304 -- immutable typed target snapshot.
		if openErr != nil {
			cleanup()
			return "", func() {}, openErr
		}
		defer func() { _ = source.Close() }()
		destination, createErr := os.OpenFile(snapshot, os.O_CREATE|os.O_EXCL|os.O_WRONLY, info.Mode().Perm())
		if createErr != nil {
			cleanup()
			return "", func() {}, createErr
		}
		_, copyErr := io.Copy(destination, source)
		closeErr := destination.Close()
		if copyErr != nil {
			cleanup()
			return "", func() {}, copyErr
		}
		if closeErr != nil {
			cleanup()
			return "", func() {}, closeErr
		}
		actual, _, digestErr := DigestPath(snapshot)
		if digestErr != nil {
			cleanup()
			return "", func() {}, digestErr
		}
		if actual != target.Digest {
			cleanup()
			return "", func() {}, fmt.Errorf("target changed while creating execution snapshot")
		}
		return snapshot, cleanup, nil
	}
	if err := os.Mkdir(snapshot, info.Mode().Perm()); err != nil {
		cleanup()
		return "", func() {}, err
	}
	err = filepath.WalkDir(target.Locator, func(current string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if current == target.Locator {
			return nil
		}
		relative, err := filepath.Rel(target.Locator, current)
		if err != nil {
			return err
		}
		destination := filepath.Join(snapshot, relative)
		entryInfo, err := entry.Info()
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return os.Mkdir(destination, entryInfo.Mode().Perm())
		}
		if !entryInfo.Mode().IsRegular() {
			return fmt.Errorf("target tree contains non-regular path %q", current)
		}
		data, err := os.ReadFile(current) // #nosec G304 -- WalkDir confines reads to the verified target.
		if err != nil {
			return err
		}
		return os.WriteFile(destination, data, entryInfo.Mode().Perm())
	})
	if err != nil {
		cleanup()
		return "", func() {}, err
	}
	actual, _, err := DigestPath(snapshot)
	if err != nil {
		cleanup()
		return "", func() {}, err
	}
	if actual != target.Digest {
		cleanup()
		return "", func() {}, fmt.Errorf("target changed while creating execution snapshot")
	}
	return snapshot, cleanup, nil
}

func verifyFileTarget(target Target) error {
	actual, exists, err := DigestPath(target.Locator)
	if err != nil {
		return err
	}
	if !exists {
		return nil
	} // URL, address scope, or scanner-managed locator.
	if actual != target.Digest {
		return fmt.Errorf("target digest mismatch: got %s, want %s", actual, target.Digest)
	}
	return nil
}

// DigestPath hashes a regular file or a directory tree deterministically. Tree
// hashes include sorted relative paths, file sizes, modes, and content digests;
// symlinks and special files are rejected so replay cannot escape its root.
func DigestPath(path string) (string, bool, error) {
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", false, nil
		}
		return "", false, err
	}
	if info.Mode().IsRegular() {
		data, err := os.ReadFile(path) // #nosec G304 -- caller-selected typed target.
		if err != nil {
			return "", true, err
		}
		return sha256Digest(data), true, nil
	}
	if !info.IsDir() {
		return "", true, fmt.Errorf("target must be a regular file or directory")
	}
	type entry struct {
		Path, Digest string
		Size         int64
		Mode         uint32
	}
	var entries []entry
	err = filepath.WalkDir(path, func(current string, dirEntry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if current == path || dirEntry.IsDir() {
			return nil
		}
		entryInfo, err := dirEntry.Info()
		if err != nil {
			return err
		}
		if !entryInfo.Mode().IsRegular() {
			return fmt.Errorf("target tree contains non-regular path %q", current)
		}
		data, err := os.ReadFile(current) // #nosec G304 -- path originates from WalkDir under target root.
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(path, current)
		if err != nil {
			return err
		}
		entries = append(entries, entry{Path: filepath.ToSlash(relative), Digest: sha256Digest(data), Size: entryInfo.Size(), Mode: uint32(entryInfo.Mode().Perm())})
		return nil
	})
	if err != nil {
		return "", true, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	encoded, err := canonicalJSON(entries)
	if err != nil {
		return "", true, err
	}
	return sha256Digest(encoded), true, nil
}

// Built-in vector and matrix runners consume immutable result artifacts. The
// application harness performs bounded requests or crypto operations and writes
// actual/expected outcomes; this wrapper validates and normalizes that artifact.
func executeBuiltin(req ExecutionRequest) (NativeResult, bool) {
	switch req.Invocation.Argv[0] {
	case "authz-matrix", "wycheproof-runner", "vector-runner", "crypto-differential":
		data, err := os.ReadFile(req.Config.Target.Locator) // #nosec G304 -- registry-validated typed target, covered by replay digest.
		result := NativeResult{
			Output: data, ExitCode: 0, Examined: []string{req.Config.Target.Locator},
			Environment: map[string]string{"runtime": runtime.Version(), "os": runtime.GOOS, "arch": runtime.GOARCH},
		}
		if err != nil {
			result.ExitCode, result.Err = -1, err
		} else if actual := sha256Digest(data); actual != req.Config.Target.Digest {
			result.ExitCode, result.Err = -1, fmt.Errorf("target digest mismatch: got %s, want %s", actual, req.Config.Target.Digest)
		}
		return result, true
	default:
		return NativeResult{}, false
	}
}

type limitedBuffer struct {
	buf      bytes.Buffer
	limit    int64
	overflow bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	original := len(p)
	remaining := b.limit - int64(b.buf.Len())
	if remaining <= 0 {
		b.overflow = true
		return original, nil
	}
	if int64(len(p)) > remaining {
		p = p[:remaining]
		b.overflow = true
	}
	_, _ = b.buf.Write(p)
	return original, nil
}
func (b *limitedBuffer) Bytes() []byte  { return append([]byte(nil), b.buf.Bytes()...) }
func (b *limitedBuffer) String() string { return b.buf.String() }

func processExitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		if status, ok := exitErr.Sys().(syscall.WaitStatus); ok {
			return status.ExitStatus()
		}
	}
	return -1
}

// ResultExitCode maps the six-state result model onto stable CLI exit codes.
func ResultExitCode(status Status) int {
	switch status {
	case StatusNotFoundUnder, StatusPass:
		// A bounded clean run is a successful execution: the tool ran and
		// found nothing under its bounds. Only a broken run exits non-zero.
		return 0
	case StatusFindings:
		return 10
	case StatusPartial:
		return 20
	case StatusNotApplicable:
		return 30
	case StatusTimeout:
		return 124
	default:
		return 1
	}
}

// EVM verification packs carry two operator-owned references in their argv:
// a fork-endpoint alias and a reviewed upstream revision. Both resolve from
// operator configuration only. A model can name an alias the operator
// authorized; it can never name a URL, a mirror, or a path, and an alias the
// operator did not configure fails the run instead of reaching the network.
const (
	evmForkEndpointURLEnvPrefix = "GA_SECURITY_EVM_FORK_ENDPOINT_"
	evmUpstreamMirrorEnvPrefix  = "GA_SECURITY_EVM_UPSTREAM_MIRROR_"

	forkDevnetReadinessCap = 90 * time.Second
	forkDevnetShutdown     = 5 * time.Second
	forkDevnetPoll         = 100 * time.Millisecond
	maxMutants             = 8
)

func operatorConfigurationName(prefix, alias string) string {
	return prefix + strings.ToUpper(strings.ReplaceAll(alias, "-", "_"))
}

// resolveForkEndpoint maps an operator-authorized alias to the operator's URL
// for it. Both the allowlist and the URL come from the environment the
// operator controls, so an unknown or unconfigured alias is an error and never
// falls through to the literal token.
func resolveForkEndpoint(alias string) (string, error) {
	if !slices.Contains(evmForkEndpointAliases(), alias) {
		return "", fmt.Errorf("fork endpoint alias %q is not authorized by %s", alias, evmForkEndpointsEnv)
	}
	variable := operatorConfigurationName(evmForkEndpointURLEnvPrefix, alias)
	endpoint := strings.TrimSpace(os.Getenv(variable))
	if endpoint == "" {
		return "", fmt.Errorf("fork endpoint alias %q has no operator-configured URL in %s", alias, variable)
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || !parsed.IsAbs() || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.Fragment != "" {
		return "", fmt.Errorf("%s must hold an absolute http(s) fork endpoint URL", variable)
	}
	return endpoint, nil
}

// resolveOperatorEVMTokens replaces the operator reference tokens in argv in
// place. Anything left unresolved is a bug, not a fallback: the tokens are not
// valid URLs or refs, so a missed replacement fails the tool rather than
// silently widening what the run reaches.
func resolveOperatorEVMTokens(ctx context.Context, argv []string, repository string, secrets *operatorSecret) error {
	for i := range argv {
		if alias, ok := strings.CutPrefix(argv[i], operatorForkEndpointToken); ok {
			endpoint, err := resolveForkEndpoint(alias)
			if err != nil {
				return err
			}
			secrets.hide(endpoint, operatorForkEndpointToken+alias)
			argv[i] = endpoint
			continue
		}
		if reference, ok := strings.CutPrefix(argv[i], operatorUpstreamToken); ok {
			revision, err := materializeUpstreamRevision(ctx, reference, repository)
			if err != nil {
				return err
			}
			argv[i] = revision
		}
	}
	return nil
}

// operatorSecret keeps operator-configured endpoint credentials out of every
// byte the executor returns. A fork endpoint URL routinely carries an API key
// in its path or query, and results are stored, replayed, and shown to
// callers, so the URL is replaced by the alias token it was resolved from.
type operatorSecret struct{ replacements [][2]string }

func (s *operatorSecret) hide(value, placeholder string) {
	if s == nil || len(value) < 8 {
		return
	}
	s.replacements = append(s.replacements, [2]string{value, placeholder})
	parsed, err := url.Parse(value)
	if err != nil {
		return
	}
	credentials := append(strings.Split(strings.Trim(parsed.EscapedPath(), "/"), "/"), strings.Trim(parsed.EscapedPath(), "/"), parsed.RawQuery)
	for _, values := range parsed.Query() {
		credentials = append(credentials, values...)
	}
	if parsed.User != nil {
		credentials = append(credentials, parsed.User.String())
	}
	for _, credential := range credentials {
		if len(credential) >= 8 {
			s.replacements = append(s.replacements, [2]string{credential, "[REDACTED]"})
		}
	}
}

func (s *operatorSecret) text(in string) string {
	if s == nil {
		return in
	}
	for _, replacement := range s.replacements {
		in = strings.ReplaceAll(in, replacement[0], replacement[1])
	}
	return in
}

func (s *operatorSecret) scrub(result NativeResult) NativeResult {
	if s == nil || len(s.replacements) == 0 {
		return result
	}
	if len(result.Output) > 0 {
		result.Output = []byte(s.text(string(result.Output)))
	}
	// The timeout path is recognized by error identity, so it is left intact.
	if result.Err != nil && !errors.Is(result.Err, context.DeadlineExceeded) {
		result.Err = errors.New(s.text(result.Err.Error()))
	}
	return result
}

// executeEVMStage runs the EVM packs that are not batch tools: anvil is a
// server whose lifecycle must be supervised, and the mutation pack re-runs its
// pinned argv once per mutant. Packs that are a single bounded process
// (forge-fork-test, upstream-fork-diff) fall through to the generic path.
func executeEVMStage(ctx context.Context, req ExecutionRequest, argv []string, executionTarget string, childEnvironment []string, secrets *operatorSecret) (NativeResult, bool) {
	environment := map[string]string{"os": runtime.GOOS, "arch": runtime.GOARCH, "runtime": runtime.Version()}
	switch req.Tool.Name {
	case "anvil-fork":
		output, err := runForkDevnet(ctx, req, argv, childEnvironment, secrets)
		return evmStageResult(ctx, environment, output, 0, err), true
	case "forge-coverage-mutation":
		limit := req.Invocation.Budgets.MaxOutputSize
		run := func(runCtx context.Context, root string) (int, []byte, error) {
			mutantArgv := slices.Clone(argv)
			for i := range mutantArgv {
				if mutantArgv[i] == executionTarget {
					mutantArgv[i] = root
				}
			}
			suite := exec.CommandContext(runCtx, mutantArgv[0], mutantArgv[1:]...) // #nosec G204 -- registry argv with only the staged root swapped for its mutant copy.
			suite.Env = childEnvironment
			output := &limitedBuffer{limit: limit}
			suite.Stdout, suite.Stderr = output, io.Discard
			err := suite.Run()
			if processExitCode(err) < 0 {
				return -1, nil, err
			}
			return processExitCode(err), output.Bytes(), nil
		}
		output, survivors, err := runMutationCampaign(ctx, executionTarget, req.Config.Arguments["mutation_operator"], run)
		exitCode := 0
		if survivors > 0 {
			exitCode = 1
		}
		return evmStageResult(ctx, environment, output, exitCode, err), true
	}
	return NativeResult{}, false
}

// evmStageResult reports the stage through the pack's own declared exit codes:
// 124 for an exhausted budget and 2 for an operational failure, so a broken
// devnet or mutation campaign is an error result rather than a silent pass.
func evmStageResult(ctx context.Context, environment map[string]string, output []byte, exitCode int, err error) NativeResult {
	if ctx.Err() == context.DeadlineExceeded {
		return NativeResult{Environment: environment, ExitCode: 124, TimedOut: true, Err: context.DeadlineExceeded}
	}
	if err != nil {
		return NativeResult{Environment: environment, ExitCode: 2, Err: err}
	}
	return NativeResult{Output: output, Environment: environment, ExitCode: exitCode}
}

// forkDevnetRequest is everything the supervisor needs to decide whether a
// devnet is the pinned one. Chain id, block number, and block hash come from
// the run's required typed arguments, so a devnet that reports anything else
// is an error result rather than a pass.
type forkDevnetRequest struct {
	Argv        []string
	Environment []string
	Alias       string
	ChainID     int64
	BlockNumber int64
	BlockHash   string
	ListenURL   string
	Readiness   time.Duration
	MaxLog      int64
	Secrets     *operatorSecret
}

func runForkDevnet(ctx context.Context, req ExecutionRequest, argv, childEnvironment []string, secrets *operatorSecret) ([]byte, error) {
	chainID, err := strconv.ParseInt(req.Config.Arguments["chain_id"], 10, 64)
	if err != nil {
		return nil, fmt.Errorf("fork run is missing its pinned chain id")
	}
	blockNumber, err := strconv.ParseInt(req.Config.Arguments["fork_block_number"], 10, 64)
	if err != nil {
		return nil, fmt.Errorf("fork run is missing its pinned fork block number")
	}
	blockHash := strings.ToLower(req.Config.Arguments["fork_block_hash"])
	if !evmBlockHashPattern.MatchString(blockHash) {
		return nil, fmt.Errorf("fork run is missing its pinned fork block hash")
	}
	devnetArgv, listenURL, err := loopbackDevnetArgv(argv)
	if err != nil {
		return nil, err
	}
	return superviseForkDevnet(ctx, forkDevnetRequest{
		Argv: devnetArgv, Environment: childEnvironment, Alias: req.Config.Arguments["fork_endpoint"],
		ChainID: chainID, BlockNumber: blockNumber, BlockHash: blockHash, ListenURL: listenURL,
		Readiness: min(req.Invocation.Budgets.Timeout/2, forkDevnetReadinessCap),
		MaxLog:    min(req.Invocation.Budgets.MaxOutputSize, 1<<20), Secrets: secrets,
	})
}

// loopbackDevnetArgv pins the devnet to a free loopback port. The registry
// fixes the interface; only the port is chosen here, so two runs on one node
// cannot collide on a listener.
func loopbackDevnetArgv(argv []string) ([]string, string, error) {
	out := slices.Clone(argv)
	host, port := -1, -1
	for i := 0; i+1 < len(out); i++ {
		switch out[i] {
		case "--host":
			host = i + 1
		case "--port":
			port = i + 1
		}
	}
	if host < 0 || port < 0 || out[host] != "127.0.0.1" {
		return nil, "", fmt.Errorf("fork devnet argv must bind the loopback interface")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, "", err
	}
	assigned := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		return nil, "", err
	}
	out[port] = strconv.Itoa(assigned)
	return out, fmt.Sprintf("http://127.0.0.1:%d", assigned), nil
}

// superviseForkDevnet starts the devnet, waits for JSON-RPC readiness on
// loopback within the pack's budget, asserts the reported chain state matches
// the pinned request, emits the replay record, and always terminates and reaps
// the process. A devnet that never becomes ready, dies early, or reports a
// different chain or block is an error, never a pass.
func superviseForkDevnet(ctx context.Context, request forkDevnetRequest) ([]byte, error) {
	if !isLoopbackDevnet(request.ListenURL) {
		return nil, fmt.Errorf("fork devnet listen URL %q is not loopback", request.ListenURL)
	}
	log := &limitedBuffer{limit: max(request.MaxLog, 4096)}
	devnet := exec.CommandContext(ctx, request.Argv[0], request.Argv[1:]...) // #nosec G204 -- registry argv whose only resolved value is the operator fork endpoint.
	devnet.Env = request.Environment
	devnet.Stdout, devnet.Stderr = log, log
	// A devnet that leaves a child holding its output pipe must not hold the
	// supervisor open past its shutdown grace.
	devnet.WaitDelay = forkDevnetShutdown
	if err := devnet.Start(); err != nil {
		return nil, err
	}
	var waitErr error
	done := make(chan struct{})
	go func() { waitErr = devnet.Wait(); close(done) }()
	defer terminateForkDevnet(devnet, done)

	client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{Proxy: nil, DisableKeepAlives: true}}
	deadline := time.Now().Add(request.Readiness)
	var chainID int64
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-done:
			return nil, fmt.Errorf("fork devnet exited before it was ready (%v): %s", waitErr, request.Secrets.text(strings.TrimSpace(log.String())))
		default:
		}
		id, err := devnetChainID(ctx, client, request.ListenURL)
		if err == nil {
			chainID = id
			break
		}
		if !time.Now().Before(deadline) {
			return nil, fmt.Errorf("fork devnet was not ready on %s within %s: %s", request.ListenURL, request.Readiness, request.Secrets.text(strings.TrimSpace(log.String())))
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-done:
		case <-time.After(forkDevnetPoll):
		}
	}
	if chainID != request.ChainID {
		return nil, fmt.Errorf("fork devnet reported chain id %d, but the run pinned %d", chainID, request.ChainID)
	}
	number, hash, err := devnetForkBlock(ctx, client, request.ListenURL)
	if err != nil {
		return nil, err
	}
	if number != request.BlockNumber || hash != request.BlockHash {
		return nil, fmt.Errorf("fork devnet head is block %d (%s), but the run pinned block %d (%s)", number, hash, request.BlockNumber, request.BlockHash)
	}
	return json.Marshal(evmForkRecord{ChainID: chainID, ForkBlockNumber: number, ForkBlockHash: hash, EndpointAlias: request.Alias, ListenURL: request.ListenURL})
}

func terminateForkDevnet(devnet *exec.Cmd, done <-chan struct{}) {
	select {
	case <-done:
		return
	default:
	}
	_ = devnet.Process.Signal(syscall.SIGTERM)
	select {
	case <-done:
	case <-time.After(forkDevnetShutdown):
		_ = devnet.Process.Kill()
		<-done
	}
}

func devnetChainID(ctx context.Context, client *http.Client, endpoint string) (int64, error) {
	var quantity string
	if err := devnetCall(ctx, client, endpoint, "eth_chainId", []any{}, &quantity); err != nil {
		return 0, err
	}
	return parseHexQuantity(quantity)
}

// devnetForkBlock reads the devnet head. The pack runs anvil with --no-mining,
// so the head is exactly the forked block and comparing it to the pinned block
// proves the fork point.
func devnetForkBlock(ctx context.Context, client *http.Client, endpoint string) (int64, string, error) {
	var block struct {
		Number string `json:"number"`
		Hash   string `json:"hash"`
	}
	if err := devnetCall(ctx, client, endpoint, "eth_getBlockByNumber", []any{"latest", false}, &block); err != nil {
		return 0, "", err
	}
	number, err := parseHexQuantity(block.Number)
	if err != nil {
		return 0, "", err
	}
	hash := strings.ToLower(block.Hash)
	if !evmBlockHashPattern.MatchString(hash) {
		return 0, "", fmt.Errorf("fork devnet reported malformed block hash %q", block.Hash)
	}
	return number, hash, nil
}

func devnetCall(ctx context.Context, client *http.Client, endpoint, method string, params []any, out any) error {
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	payload, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return err
	}
	var envelope struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return fmt.Errorf("fork devnet %s response is not JSON-RPC: %w", method, err)
	}
	if envelope.Error != nil {
		return fmt.Errorf("fork devnet %s failed: %s", method, envelope.Error.Message)
	}
	if len(envelope.Result) == 0 || string(envelope.Result) == "null" {
		return fmt.Errorf("fork devnet %s returned no result", method)
	}
	return json.Unmarshal(envelope.Result, out)
}

func parseHexQuantity(value string) (int64, error) {
	digits, ok := strings.CutPrefix(strings.ToLower(value), "0x")
	if !ok {
		return 0, fmt.Errorf("malformed JSON-RPC quantity %q", value)
	}
	parsed, err := strconv.ParseInt(digits, 16, 64)
	if err != nil {
		return 0, fmt.Errorf("malformed JSON-RPC quantity %q", value)
	}
	return parsed, nil
}

// materializeUpstreamRevision fetches the pinned upstream commit into the
// staged repository from an operator-configured mirror and verifies that the
// mirror produced exactly the requested 40-hex commit. Anything else fails the
// run: a diff against an unverified ref proves nothing about the fork.
func materializeUpstreamRevision(ctx context.Context, reference, repository string) (string, error) {
	name, revision, found := strings.Cut(reference, "@")
	if !found || !slices.Contains(evmUpstreams, name) {
		return "", fmt.Errorf("upstream reference %q does not name a reviewed upstream", reference)
	}
	if !gitRevisionPattern.MatchString(revision) {
		return "", fmt.Errorf("upstream %s must be pinned to a full 40-hex commit id", name)
	}
	variable := operatorConfigurationName(evmUpstreamMirrorEnvPrefix, name)
	mirror := strings.TrimSpace(os.Getenv(variable))
	if mirror == "" {
		return "", fmt.Errorf("upstream %s has no operator-configured mirror in %s", name, variable)
	}
	if !isOperatorMirror(mirror) {
		return "", fmt.Errorf("%s must be an https URL or an absolute directory path", variable)
	}
	git, err := exec.LookPath("git")
	if err != nil {
		return "", fmt.Errorf("git is unavailable in the runtime image: %w", err)
	}
	for _, arguments := range [][]string{
		{"fetch", "--no-tags", "--quiet", mirror, revision},
		{"fetch", "--no-tags", "--quiet", mirror, "+refs/heads/*:refs/ga-upstream/" + name + "/*"},
	} {
		if _, fetchErr := runGit(ctx, git, repository, arguments...); fetchErr == nil {
			break
		}
	}
	resolved, err := runGit(ctx, git, repository, "rev-parse", "--verify", "--quiet", revision+"^{commit}")
	if err != nil || resolved != revision {
		return "", fmt.Errorf("upstream %s mirror did not provide the pinned commit %s", name, revision)
	}
	return revision, nil
}

func isOperatorMirror(mirror string) bool {
	if strings.ContainsAny(mirror, " \t\r\n") || strings.HasPrefix(mirror, "-") {
		return false
	}
	if strings.HasPrefix(mirror, "/") {
		info, err := os.Stat(mirror)
		return err == nil && info.IsDir()
	}
	parsed, err := url.Parse(mirror)
	return err == nil && parsed.Scheme == "https" && parsed.Host != "" && parsed.User == nil
}

// runGit discards git's stderr: a mirror URL may embed a credential, and the
// caller reports the pinned commit rather than the transport's own message.
func runGit(ctx context.Context, git, repository string, arguments ...string) (string, error) {
	command := exec.CommandContext(ctx, git, append([]string{"-C", repository}, arguments...)...) // #nosec G204 -- git from LookPath, operator-configured mirror, and a validated 40-hex revision.
	output := &limitedBuffer{limit: 1 << 20}
	command.Stdout, command.Stderr = output, io.Discard
	command.Env = []string{"HOME=" + os.TempDir(), "LANG=C.UTF-8", "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1", "GIT_ASKPASS=", "PATH=" + os.Getenv("PATH")}
	err := command.Run()
	return strings.TrimSpace(output.String()), err
}

// mutationReport is the combined coverage and mutation document the
// forge-mutation-json adapter consumes.
type mutationReport struct {
	Coverage []mutationCoverageFile `json:"coverage"`
	Mutants  []mutationResult       `json:"mutants"`
}

type mutationCoverageFile struct {
	File       string `json:"file"`
	LinesTotal int    `json:"lines_total"`
	LinesHit   int    `json:"lines_hit"`
}

type mutationResult struct {
	File     string `json:"file"`
	Line     int    `json:"line"`
	Operator string `json:"operator"`
	Status   string `json:"status"`
}

type mutationSite struct {
	File    string
	Line    int
	Mutated string
}

// mutationRunner runs the pack's pinned argv against one project root and
// reports the suite's exit code and stdout.
type mutationRunner func(ctx context.Context, root string) (int, []byte, error)

// runMutationCampaign measures coverage on the staged project, then re-runs the
// same suite once per mutant in a private scratch copy. A mutant the suite
// still passes is a survivor: the assertions covering that line cannot fail.
// The staged tree is only ever read, never mutated in place.
func runMutationCampaign(ctx context.Context, staged, operator string, run mutationRunner) ([]byte, int, error) {
	limit := maxMutants
	exitCode, coverageOutput, err := run(ctx, staged)
	if err != nil {
		return nil, 0, err
	}
	if exitCode != 0 {
		return nil, 0, fmt.Errorf("baseline suite exited %d: mutants cannot be judged against a failing harness", exitCode)
	}
	sites, err := mutationSites(staged, operator, limit)
	if err != nil {
		return nil, 0, err
	}
	if len(sites) == 0 {
		return nil, 0, fmt.Errorf("mutation operator %q has no applicable site in the staged Solidity sources", operator)
	}
	report := mutationReport{Coverage: parseLCOVCoverage(staged, coverageOutput), Mutants: []mutationResult{}}
	survivors := 0
	for _, site := range sites {
		if ctx.Err() != nil {
			break
		}
		status, err := judgeMutant(ctx, staged, site, run)
		if err != nil {
			return nil, 0, err
		}
		if status == "survived" {
			survivors++
		}
		report.Mutants = append(report.Mutants, mutationResult{File: site.File, Line: site.Line, Operator: operator, Status: status})
	}
	document, err := json.Marshal(report)
	return document, survivors, err
}

func judgeMutant(ctx context.Context, staged string, site mutationSite, run mutationRunner) (string, error) {
	scratch, err := os.MkdirTemp("", "ga-security-mutant-*")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.RemoveAll(scratch) }()
	root := filepath.Join(scratch, "project")
	if err := copyStagedTree(staged, root); err != nil {
		return "", err
	}
	if err := applyMutation(root, site); err != nil {
		return "", err
	}
	exitCode, _, err := run(ctx, root)
	if err != nil {
		return "", err
	}
	if exitCode == 0 {
		return "survived", nil
	}
	return "killed", nil
}

func applyMutation(root string, site mutationSite) error {
	path := filepath.Join(root, filepath.FromSlash(site.File))
	data, err := os.ReadFile(path) // #nosec G304 -- path is a relative source discovered under the private scratch copy.
	if err != nil {
		return err
	}
	lines := strings.Split(string(data), "\n")
	if site.Line < 1 || site.Line > len(lines) {
		return fmt.Errorf("mutation site %s:%d is outside the staged source", site.File, site.Line)
	}
	lines[site.Line-1] = site.Mutated
	return os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o600)
}

func copyStagedTree(source, destination string) error {
	return filepath.WalkDir(source, func(current string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, current)
		if err != nil {
			return err
		}
		target := filepath.Join(destination, relative)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return os.MkdirAll(target, info.Mode().Perm()|0o700)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("staged tree contains non-regular path %q", current)
		}
		data, err := os.ReadFile(current) // #nosec G304 -- WalkDir confines reads to the staged snapshot.
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, info.Mode().Perm()|0o600)
	})
}

// mutationSites enumerates the sites the selected operator applies to, in a
// deterministic order, bounded by the pack's mutant budget. Only the project's
// own contracts are mutated: mutating tests or dependencies would prove
// nothing about the harness.
func mutationSites(root, operator string, limit int) ([]mutationSite, error) {
	var sites []mutationSite
	err := filepath.WalkDir(root, func(current string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(root, current)
		if err != nil {
			return err
		}
		slashed := filepath.ToSlash(relative)
		if entry.IsDir() {
			if current == root || slashed == "src" || strings.HasPrefix(slashed, "src/") {
				return nil
			}
			return filepath.SkipDir
		}
		if !strings.HasSuffix(slashed, ".sol") || !strings.HasPrefix(slashed, "src/") {
			return nil
		}
		data, err := os.ReadFile(current) // #nosec G304 -- WalkDir confines reads to the staged snapshot.
		if err != nil {
			return err
		}
		for index, line := range strings.Split(string(data), "\n") {
			if mutated, ok := mutateSolidityLine(operator, line); ok {
				sites = append(sites, mutationSite{File: slashed, Line: index + 1, Mutated: mutated})
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(sites, func(i, j int) bool {
		if sites[i].File != sites[j].File {
			return sites[i].File < sites[j].File
		}
		return sites[i].Line < sites[j].Line
	})
	if len(sites) > limit {
		sites = sites[:limit]
	}
	return sites, nil
}

func mutateSolidityLine(operator, line string) (string, bool) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "*") || strings.HasPrefix(trimmed, "/*") ||
		strings.HasPrefix(trimmed, "pragma") || strings.HasPrefix(trimmed, "import") {
		return "", false
	}
	switch operator {
	case "assertion-negation":
		if !strings.Contains(line, "require(") && !strings.Contains(line, "assert(") {
			return "", false
		}
		if strings.Contains(line, "==") {
			return strings.Replace(line, "==", "!=", 1), true
		}
		if strings.Contains(line, "!=") {
			return strings.Replace(line, "!=", "==", 1), true
		}
	case "require-removal":
		if strings.HasPrefix(trimmed, "require(") && strings.HasSuffix(trimmed, ";") {
			return line[:len(line)-len(strings.TrimLeft(line, " \t"))] + "// mutant removed: " + trimmed, true
		}
	case "boundary-shift":
		for _, shift := range [][2]string{{"<=", "<"}, {">=", ">"}} {
			if strings.Contains(line, shift[0]) {
				return strings.Replace(line, shift[0], shift[1], 1), true
			}
		}
	case "return-value-swap":
		if strings.Contains(line, "return true") {
			return strings.Replace(line, "return true", "return false", 1), true
		}
		if strings.Contains(line, "return false") {
			return strings.Replace(line, "return false", "return true", 1), true
		}
	}
	return "", false
}

// parseLCOVCoverage reads `forge coverage --report lcov`. Paths are reported
// relative to the project so a finding cites the source, not the scratch root.
func parseLCOVCoverage(root string, output []byte) []mutationCoverageFile {
	files := []mutationCoverageFile{}
	current := mutationCoverageFile{}
	hit, total := 0, 0
	for line := range strings.SplitSeq(string(output), "\n") {
		field := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(field, "SF:"):
			current = mutationCoverageFile{File: relativeCoveragePath(root, strings.TrimPrefix(field, "SF:"))}
			hit, total = 0, 0
		case strings.HasPrefix(field, "DA:"):
			counts := strings.Split(strings.TrimPrefix(field, "DA:"), ",")
			if len(counts) != 2 {
				continue
			}
			total++
			if executions, err := strconv.Atoi(counts[1]); err == nil && executions > 0 {
				hit++
			}
		case field == "end_of_record":
			if current.File == "" {
				continue
			}
			current.LinesTotal, current.LinesHit = total, hit
			files = append(files, current)
			current = mutationCoverageFile{}
		}
	}
	return files
}

func relativeCoveragePath(root, path string) string {
	path = strings.TrimSpace(path)
	if relative, err := filepath.Rel(root, path); err == nil && !strings.HasPrefix(relative, "..") {
		return filepath.ToSlash(relative)
	}
	return filepath.ToSlash(path)
}
