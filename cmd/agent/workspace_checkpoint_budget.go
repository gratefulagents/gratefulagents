package main

import (
	"context"
	"log"
	"math/rand"
	"os"
	"strings"
	"time"
)

// Checkpoint work has two very different cost profiles: local Git preparation
// (staging, bundling, packing the anchor tree) scales with repository size and
// can take minutes on a large monorepo, while an object-store upload should
// complete in seconds. Sharing one short deadline across both meant that a slow
// pack either got killed mid-flight or handed the upload an almost-expired
// context, which surfaced as S3 "context deadline exceeded". Each stage now
// carries its own budget.
const (
	defaultWorkspaceCheckpointPrepareTimeout = 5 * time.Minute
	defaultWorkspaceCheckpointUploadTimeout  = 60 * time.Second

	// Shutdown runs inside the pod's 60s termination grace period, shared with
	// meta-harness trace finalization (15s), so the final checkpoint keeps a
	// tight budget: at most 20s of Git preparation plus 10s of upload.
	shutdownWorkspaceCheckpointPrepareTimeout = 20 * time.Second
	shutdownWorkspaceCheckpointUploadTimeout  = 10 * time.Second

	workspaceCheckpointPrepareTimeoutEnv = "WORKSPACE_CHECKPOINT_PREPARE_TIMEOUT"
	workspaceCheckpointUploadTimeoutEnv  = "WORKSPACE_CHECKPOINT_UPLOAD_TIMEOUT"

	// Stages faster than this are not worth a log line; slower ones are the
	// ones operators need to correlate with checkpoint gaps.
	workspaceCheckpointStageLogThreshold = 5 * time.Second
)

// checkpointBudget carries the independent per-stage deadlines of one
// checkpoint attempt.
type checkpointBudget struct {
	prepare time.Duration
	upload  time.Duration
	// hardDeadline, when set, is an absolute end-to-end bound that no stage may
	// extend. The shutdown path sets it so a sequence of detached upload
	// contexts cannot push the final checkpoint past the pod termination grace
	// period.
	hardDeadline time.Time
}

func defaultCheckpointBudget() checkpointBudget {
	return checkpointBudget{
		prepare: envDurationOrDefault(workspaceCheckpointPrepareTimeoutEnv, defaultWorkspaceCheckpointPrepareTimeout),
		upload:  envDurationOrDefault(workspaceCheckpointUploadTimeoutEnv, defaultWorkspaceCheckpointUploadTimeout),
	}
}

// shutdownCheckpointBudget caps the configured budgets and pins an absolute
// deadline so the final checkpoint always fits inside the pod termination
// grace period, however many payloads it has to upload.
func shutdownCheckpointBudget(hardDeadline time.Time) checkpointBudget {
	budget := defaultCheckpointBudget()
	if budget.prepare > shutdownWorkspaceCheckpointPrepareTimeout {
		budget.prepare = shutdownWorkspaceCheckpointPrepareTimeout
	}
	if budget.upload > shutdownWorkspaceCheckpointUploadTimeout {
		budget.upload = shutdownWorkspaceCheckpointUploadTimeout
	}
	budget.hardDeadline = hardDeadline
	return budget
}

// prepareContext bounds local Git work. It never extends the caller's deadline.
func (b checkpointBudget) prepareContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithDeadline(ctx, b.stageDeadline(b.prepare))
}

// uploadContext gives an object-store upload its own budget. When Git
// preparation consumed most of the caller's deadline the remaining time is
// detached, because starting a PutObject with a few milliseconds left only
// produces a guaranteed "context deadline exceeded". Explicit cancellation of
// the caller is preserved when there is still budget to inherit, and a hard
// deadline (shutdown) always wins over the detached budget.
func (b checkpointBudget) uploadContext(ctx context.Context) (context.Context, context.CancelFunc) {
	deadline := b.stageDeadline(b.upload)
	if parent, ok := ctx.Deadline(); ok && parent.Before(deadline) {
		return context.WithDeadline(context.WithoutCancel(ctx), deadline)
	}
	return context.WithDeadline(ctx, deadline)
}

// stageDeadline clamps one stage budget to the absolute end-to-end deadline,
// when the budget carries one. Once that deadline has passed the stage context
// is already expired, which is the correct outcome: no stage may buy another
// budget beyond the shutdown bound.
func (b checkpointBudget) stageDeadline(stage time.Duration) time.Time {
	deadline := time.Now().Add(stage)
	if !b.hardDeadline.IsZero() && b.hardDeadline.Before(deadline) {
		return b.hardDeadline
	}
	return deadline
}

// logCheckpointStage records how long a checkpoint stage took and how much of
// the surrounding budget was left, so timeouts can be attributed to Git
// preparation or to the object store instead of to "the checkpoint".
func logCheckpointStage(ctx context.Context, stage, id string, started time.Time, err error) {
	elapsed := time.Since(started).Truncate(time.Millisecond)
	if err == nil && elapsed < workspaceCheckpointStageLogThreshold {
		return
	}
	remaining := "unbounded"
	if deadline, ok := ctx.Deadline(); ok {
		remaining = time.Until(deadline).Truncate(time.Millisecond).String()
	}
	if err != nil {
		log.Printf("WARN: workspace checkpoint stage %s (%s) failed after %s with %s of budget left: %v",
			stage, id, elapsed, remaining, err)
		return
	}
	log.Printf("Workspace checkpoint stage %s (%s) took %s with %s of budget left", stage, id, elapsed, remaining)
}

// nextCheckpointDelay jitters the periodic checkpoint schedule and backs off
// exponentially after consecutive failures. Security-scan fan-out starts many
// workers on the same repository at nearly the same moment; without jitter they
// repack and re-upload in lockstep and keep retrying on the same cadence.
func nextCheckpointDelay(consecutiveFailures int) time.Duration {
	base := workspaceCheckpointInterval
	for i := 0; i < consecutiveFailures && base < maxWorkspaceCheckpointInterval; i++ {
		base *= 2
	}
	if base > maxWorkspaceCheckpointInterval {
		base = maxWorkspaceCheckpointInterval
	}
	// Uniform +/-25% jitter around the (possibly backed-off) interval.
	spread := int64(base / 2)
	if spread <= 0 {
		return base
	}
	return base - base/4 + time.Duration(rand.Int63n(spread+1))
}

func envDurationOrDefault(name string, fallback time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback
	}
	value, err := time.ParseDuration(raw)
	if err != nil || value <= 0 {
		log.Printf("WARN: ignoring invalid %s=%q: using %s", name, raw, fallback)
		return fallback
	}
	return value
}
