package securitytoolpacks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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

func (ProcessSandbox) Execute(ctx context.Context, req ExecutionRequest) NativeResult {
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
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...) // #nosec G204 -- executable and argv come only from the validated static registry.
	home, homeErr := os.MkdirTemp("", "ga-security-home-*")
	if homeErr != nil {
		return NativeResult{ExitCode: -1, Err: homeErr}
	}
	defer func() { _ = os.RemoveAll(home) }()
	cmd.Env = deterministicEnvironment(req.Tool.Name, home)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
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
	result := NativeResult{Output: stdout.Bytes(), ExitCode: exitCode, Environment: environment}
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
			// This solc-select release derives its artifact directory from HOME;
			// SOLC_SELECT_DIR is not honored by the installed wrapper. Restore the
			// immutable toolbox home while keeping caches and outputs under /work.
			"--setenv", "HOME", "/home/ethsec",
			"--setenv", "PYTHONUSERBASE", "/home/ethsec/.local",
			"--setenv", "XDG_CACHE_HOME", "/work/.cache",
			"--setenv", "SOLC_VERSION", "0.8.30",
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

func isLockedExternalTool(name string) bool {
	return slices.Contains([]string{"nuclei", "naabu", "aderyn", "forge-security-tests", "echidna"}, name)
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
	if toolName == "forge-security-tests" {
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
	case StatusPass:
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
