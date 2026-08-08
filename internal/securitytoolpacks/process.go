package securitytoolpacks

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"syscall"
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
	cmd := exec.CommandContext(ctx, req.Invocation.Argv[0], req.Invocation.Argv[1:]...) // #nosec G204 -- executable and argv come only from the validated static registry.
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	err := cmd.Run()

	exitCode := processExitCode(err)
	result := NativeResult{
		Output:   stdout.Bytes(),
		ExitCode: exitCode,
		Environment: map[string]string{
			"os":      runtime.GOOS,
			"arch":    runtime.GOARCH,
			"runtime": runtime.Version(),
		},
	}
	if exitCode >= 0 {
		result.Examined = []string{req.Config.Target.Locator}
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

func verifyFileTarget(target Target) error {
	info, err := os.Stat(target.Locator)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		} // URL, address scope, or scanner-managed locator.
		return err
	}
	if !info.Mode().IsRegular() {
		return nil
	}
	data, err := os.ReadFile(target.Locator) // #nosec G304 -- typed registry target.
	if err != nil {
		return err
	}
	if actual := sha256Digest(data); actual != target.Digest {
		return fmt.Errorf("target digest mismatch: got %s, want %s", actual, target.Digest)
	}
	return nil
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
