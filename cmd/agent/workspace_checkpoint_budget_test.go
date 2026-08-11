package main

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestUploadContextGetsFullBudgetWhenPreparationExhaustedDeadline(t *testing.T) {
	budget := checkpointBudget{prepare: time.Minute, upload: 20 * time.Second}
	parent, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()

	uploadCtx, cancelUpload := budget.uploadContext(parent)
	defer cancelUpload()
	deadline, ok := uploadCtx.Deadline()
	if !ok {
		t.Fatal("upload context has no deadline")
	}
	if remaining := time.Until(deadline); remaining < 15*time.Second {
		t.Fatalf("upload budget = %s, want close to %s", remaining, budget.upload)
	}
	time.Sleep(20 * time.Millisecond)
	if err := uploadCtx.Err(); err != nil {
		t.Fatalf("upload context expired with the exhausted parent deadline: %v", err)
	}
}

func TestUploadContextInheritsCancellationWhenBudgetRemains(t *testing.T) {
	budget := checkpointBudget{prepare: time.Minute, upload: time.Second}
	parent, cancel := context.WithTimeout(context.Background(), time.Minute)
	uploadCtx, cancelUpload := budget.uploadContext(parent)
	defer cancelUpload()
	if deadline, ok := uploadCtx.Deadline(); !ok || time.Until(deadline) > budget.upload {
		t.Fatalf("upload deadline = %v (ok=%v), want at most %s out", deadline, ok, budget.upload)
	}
	cancel()
	select {
	case <-uploadCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("upload context ignored parent cancellation")
	}
}

func TestShutdownCheckpointBudgetFitsTerminationGrace(t *testing.T) {
	start := time.Now()
	hardDeadline := start.Add(workspaceCheckpointTimeout + shutdownWorkspaceCheckpointUploadTimeout)
	budget := shutdownCheckpointBudget(hardDeadline)
	cappedPrepare := budget.prepare > shutdownWorkspaceCheckpointPrepareTimeout
	if cappedPrepare || budget.upload > shutdownWorkspaceCheckpointUploadTimeout {
		t.Fatalf("shutdown budget %+v exceeds the capped stage timeouts", budget)
	}
	total := hardDeadline.Sub(start) + metaHarnessFinalizeTimeout
	if total >= 60*time.Second {
		t.Fatalf("shutdown checkpoint plus trace finalization = %s, want inside the 60s grace period", total)
	}
}

// A shutdown checkpoint uploads an anchor, a bundle and a manifest. Each upload
// may detach an exhausted caller deadline, so the absolute deadline must keep
// the sequence inside the termination grace period.
func TestShutdownUploadsCannotExtendPastHardDeadline(t *testing.T) {
	hardDeadline := time.Now().Add(300 * time.Millisecond)
	budget := shutdownCheckpointBudget(hardDeadline)
	parent, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()

	for i := range 3 {
		uploadCtx, cancelUpload := budget.uploadContext(parent)
		deadline, ok := uploadCtx.Deadline()
		cancelUpload()
		if !ok {
			t.Fatalf("upload %d has no deadline", i)
		}
		if deadline.After(hardDeadline) {
			t.Fatalf("upload %d deadline %s is past the shutdown hard deadline %s", i, deadline, hardDeadline)
		}
	}
	prepCtx, cancelPrep := budget.prepareContext(context.Background())
	defer cancelPrep()
	if deadline, ok := prepCtx.Deadline(); !ok || deadline.After(hardDeadline) {
		t.Fatalf("preparation deadline %v (ok=%v) is past the shutdown hard deadline %s", deadline, ok, hardDeadline)
	}

	// Once the hard deadline has passed, further stages are already expired
	// instead of silently buying another budget.
	time.Sleep(320 * time.Millisecond)
	expired, cancelExpired := budget.uploadContext(context.Background())
	defer cancelExpired()
	if err := expired.Err(); err == nil {
		t.Fatal("upload after the shutdown hard deadline got a fresh budget")
	}
}

func TestCheckpointBudgetHonoursEnvironmentOverrides(t *testing.T) {
	t.Setenv(workspaceCheckpointPrepareTimeoutEnv, "12m")
	t.Setenv(workspaceCheckpointUploadTimeoutEnv, "90s")
	budget := defaultCheckpointBudget()
	if budget.prepare != 12*time.Minute || budget.upload != 90*time.Second {
		t.Fatalf("budget = %+v, want 12m/90s", budget)
	}
	t.Setenv(workspaceCheckpointPrepareTimeoutEnv, "not-a-duration")
	if got := defaultCheckpointBudget().prepare; got != defaultWorkspaceCheckpointPrepareTimeout {
		t.Fatalf("invalid override produced %s, want the %s default", got, defaultWorkspaceCheckpointPrepareTimeout)
	}
}

func TestNextCheckpointDelayJittersAndBacksOff(t *testing.T) {
	seen := make(map[time.Duration]struct{})
	for range 64 {
		delay := nextCheckpointDelay(0)
		low := workspaceCheckpointInterval - workspaceCheckpointInterval/4
		high := workspaceCheckpointInterval + workspaceCheckpointInterval/4
		if delay < low || delay > high {
			t.Fatalf("delay %s outside +/-25%% of %s", delay, workspaceCheckpointInterval)
		}
		seen[delay] = struct{}{}
	}
	if len(seen) < 2 {
		t.Fatal("periodic checkpoints are not jittered; concurrent workers would retry in lockstep")
	}
	if backoff := nextCheckpointDelay(4); backoff <= workspaceCheckpointInterval {
		t.Fatalf("delay after 4 failures = %s, want backoff beyond %s", backoff, workspaceCheckpointInterval)
	}
	if capped := nextCheckpointDelay(30); capped > maxWorkspaceCheckpointInterval+maxWorkspaceCheckpointInterval/4 {
		t.Fatalf("backoff %s exceeds the %s cap", capped, maxWorkspaceCheckpointInterval)
	}
}

// slowWorkspaceObjectStore makes the first Put outlast the caller's original
// deadline and records the budget every upload actually received.
type slowWorkspaceObjectStore struct {
	*memoryWorkspaceObjectStore
	mu            sync.Mutex
	firstPutSleep time.Duration
	expiredPuts   []string
	observedPuts  int
	minRemaining  time.Duration
}

func (s *slowWorkspaceObjectStore) Put(ctx context.Context, key string, body []byte) error {
	s.mu.Lock()
	first := s.observedPuts == 0
	s.observedPuts++
	remaining := time.Duration(0)
	if deadline, ok := ctx.Deadline(); ok {
		remaining = time.Until(deadline)
	}
	if s.minRemaining == 0 || remaining < s.minRemaining {
		s.minRemaining = remaining
	}
	s.mu.Unlock()
	if first {
		select {
		case <-time.After(s.firstPutSleep):
		case <-ctx.Done():
		}
	}
	if err := ctx.Err(); err != nil {
		s.mu.Lock()
		s.expiredPuts = append(s.expiredPuts, key)
		s.mu.Unlock()
		return err
	}
	return s.memoryWorkspaceObjectStore.Put(context.Background(), key, body)
}

// A large repository can consume nearly the whole checkpoint budget while Git
// packs its anchor tree. The upload that follows must still get a usable
// deadline instead of inheriting an exhausted one.
func TestWorkspaceCheckpointUploadsAfterPreparationDrainsBudget(t *testing.T) {
	requireGit(t)
	origin := newOriginWithSeed(t)
	store := &slowWorkspaceObjectStore{
		memoryWorkspaceObjectStore: newMemoryWorkspaceObjectStore(),
		firstPutSleep:              3 * time.Second,
	}
	work := cloneAndCheckout(t, origin, false)
	writeFile(t, work, "notes.md", "wip\n")

	s := newSnapshotter(work, store)
	s.setBudget(checkpointBudget{prepare: 30 * time.Second, upload: 30 * time.Second})

	// Stand in for slow Git packing: the caller's shared deadline runs out
	// while the first upload is still in flight.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := s.snapshotLocked(ctx, "slow-pack"); err != nil {
		t.Fatalf("checkpoint with slow uploads failed: %v", err)
	}
	store.mu.Lock()
	expired, puts, minRemaining := append([]string(nil), store.expiredPuts...), store.observedPuts, store.minRemaining
	store.mu.Unlock()
	if len(expired) > 0 {
		t.Fatalf("uploads started with an exhausted context: %s", strings.Join(expired, ", "))
	}
	if puts == 0 {
		t.Fatal("no checkpoint payload was uploaded")
	}
	if minRemaining < 20*time.Second {
		t.Fatalf("smallest upload budget = %s, want close to the 30s upload timeout", minRemaining)
	}
	manifest := loadTestCheckpoint(t, store.memoryWorkspaceObjectStore)
	if len(manifest.Repositories) != 1 {
		t.Fatalf("manifest repositories = %d, want 1", len(manifest.Repositories))
	}
}

func TestWorkspaceCheckpointReusesPublishedAnchorPayload(t *testing.T) {
	requireGit(t)
	origin := newOriginWithSeed(t)
	store := newMemoryWorkspaceObjectStore()
	work := cloneAndCheckout(t, origin, false)
	writeFile(t, work, "notes.md", "first\n")

	s := newSnapshotter(work, store)
	if err := s.snapshotLocked(testCtx(t), "first"); err != nil {
		t.Fatal(err)
	}
	anchorKey := loadTestCheckpoint(t, store).Repositories[0].AnchorObjectKey
	if anchorKey == "" {
		t.Fatal("checkpoint published no anchor payload")
	}
	writeFile(t, work, "notes.md", "second\n")
	if err := s.snapshotLocked(testCtx(t), "second"); err != nil {
		t.Fatal(err)
	}
	if got := store.puts[anchorKey]; got != 1 {
		t.Fatalf("anchor payload PUT count = %d, want 1 (anchor repacking is not deduplicated)", got)
	}
}

func TestCheckpointFailureBackoffResetsAfterSuccess(t *testing.T) {
	s := &workspaceSnapshotter{}
	s.recordAttempt(context.DeadlineExceeded)
	s.recordAttempt(context.DeadlineExceeded)
	if got := s.failures.Load(); got != 2 {
		t.Fatalf("consecutive failures = %d, want 2", got)
	}
	if nextCheckpointDelay(int(s.failures.Load())) <= workspaceCheckpointInterval {
		t.Fatal("repeated failures did not back off the periodic schedule")
	}
	s.recordAttempt(nil)
	if got := s.failures.Load(); got != 0 {
		t.Fatalf("consecutive failures after success = %d, want 0", got)
	}
}
