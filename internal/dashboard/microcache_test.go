package dashboard

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestProbeCacheCoalescesConcurrentCallers(t *testing.T) {
	var c probeCache
	var computes atomic.Int32
	gate := make(chan struct{})
	started := make(chan struct{})
	var startOnce sync.Once

	const callers = 16
	results := make(chan string, callers)
	errs := make(chan error, callers)
	var wg sync.WaitGroup
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, err := probeCacheDo(context.Background(), &c, "k", time.Minute, func(context.Context) (string, error) {
				startOnce.Do(func() { close(started) })
				computes.Add(1)
				<-gate
				return "value", nil
			})
			results <- v
			errs <- err
		}()
	}

	// Release the single in-flight computation only after it started, so
	// late-arriving callers must join it rather than racing ahead.
	<-started
	time.Sleep(20 * time.Millisecond)
	close(gate)
	wg.Wait()

	if got := computes.Load(); got != 1 {
		t.Errorf("computes = %d, want 1", got)
	}
	for i := 0; i < callers; i++ {
		if err := <-errs; err != nil {
			t.Fatalf("caller error: %v", err)
		}
		if v := <-results; v != "value" {
			t.Errorf("caller value = %q, want value", v)
		}
	}
}

func TestProbeCacheServesFreshAndRecomputesAfterTTL(t *testing.T) {
	var c probeCache
	var computes atomic.Int32
	fn := func(context.Context) (int, error) {
		return int(computes.Add(1)), nil
	}

	v1, err := probeCacheDo(context.Background(), &c, "k", 50*time.Millisecond, fn)
	if err != nil || v1 != 1 {
		t.Fatalf("first call = (%d, %v), want (1, nil)", v1, err)
	}
	v2, err := probeCacheDo(context.Background(), &c, "k", 50*time.Millisecond, fn)
	if err != nil || v2 != 1 {
		t.Fatalf("cached call = (%d, %v), want (1, nil)", v2, err)
	}

	time.Sleep(60 * time.Millisecond)
	v3, err := probeCacheDo(context.Background(), &c, "k", 50*time.Millisecond, fn)
	if err != nil || v3 != 2 {
		t.Fatalf("post-TTL call = (%d, %v), want (2, nil)", v3, err)
	}
}

func TestProbeCacheKeysAreIndependent(t *testing.T) {
	var c probeCache
	va, _ := probeCacheDo(context.Background(), &c, "a", time.Minute, func(context.Context) (string, error) { return "va", nil })
	vb, _ := probeCacheDo(context.Background(), &c, "b", time.Minute, func(context.Context) (string, error) { return "vb", nil })
	if va != "va" || vb != "vb" {
		t.Errorf("values = (%q, %q), want (va, vb)", va, vb)
	}
}

func TestProbeCacheDoesNotCacheErrors(t *testing.T) {
	var c probeCache
	wantErr := errors.New("boom")
	if _, err := probeCacheDo(context.Background(), &c, "k", time.Minute, func(context.Context) (int, error) {
		return 0, wantErr
	}); !errors.Is(err, wantErr) {
		t.Fatalf("first call error = %v, want %v", err, wantErr)
	}
	v, err := probeCacheDo(context.Background(), &c, "k", time.Minute, func(context.Context) (int, error) {
		return 42, nil
	})
	if err != nil || v != 42 {
		t.Fatalf("retry after error = (%d, %v), want (42, nil)", v, err)
	}
}

func TestProbeCacheWaiterHonorsContextCancellation(t *testing.T) {
	var c probeCache
	gate := make(chan struct{})
	started := make(chan struct{})
	go func() {
		_, _ = probeCacheDo(context.Background(), &c, "k", time.Minute, func(context.Context) (string, error) {
			close(started)
			<-gate
			return "late", nil
		})
	}()
	<-started

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := probeCacheDo(ctx, &c, "k", time.Minute, func(context.Context) (string, error) {
		t.Error("waiter must join the in-flight computation, not start its own")
		return "", nil
	}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled waiter error = %v, want context.Canceled", err)
	}
	close(gate)
}

func TestProbeCacheComputationDetachedFromCallerContext(t *testing.T) {
	var c probeCache
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // caller is already gone; the shared probe must still complete
	v, err := probeCacheDo(ctx, &c, "k", time.Minute, func(fnCtx context.Context) (string, error) {
		if err := fnCtx.Err(); err != nil {
			return "", err
		}
		return "computed", nil
	})
	if err != nil || v != "computed" {
		t.Fatalf("detached compute = (%q, %v), want (computed, nil)", v, err)
	}
}

func TestProbeCacheSweepsExpiredEntriesOnNextOperation(t *testing.T) {
	var c probeCache
	ctx := context.Background()

	// An in-flight computation must survive the sweep.
	gate := make(chan struct{})
	started := make(chan struct{})
	inflightDone := make(chan error, 1)
	go func() {
		_, err := probeCacheDo(ctx, &c, "k-inflight", time.Minute, func(context.Context) (string, error) {
			close(started)
			<-gate
			return "late", nil
		})
		inflightDone <- err
	}()
	<-started

	if _, err := probeCacheDo(ctx, &c, "k-expired", time.Millisecond, func(context.Context) (string, error) {
		return "big payload", nil
	}); err != nil {
		t.Fatalf("seed expired entry: %v", err)
	}
	if _, err := probeCacheDo(ctx, &c, "k-fresh", time.Minute, func(context.Context) (string, error) {
		return "fresh", nil
	}); err != nil {
		t.Fatalf("seed fresh entry: %v", err)
	}

	time.Sleep(5 * time.Millisecond) // let k-expired pass its TTL
	c.mu.Lock()
	c.lastSweep = time.Now().Add(-2 * probeCacheSweepInterval) // make the next operation sweep
	c.mu.Unlock()

	// Any cache operation triggers the sweep; expired values must not stay
	// referenced until the size cap forces eviction (they can be megabytes
	// of diff/trace payload for views nobody is polling anymore).
	if _, err := probeCacheDo(ctx, &c, "k-other", time.Minute, func(context.Context) (string, error) {
		return "other", nil
	}); err != nil {
		t.Fatalf("trigger sweep: %v", err)
	}

	c.mu.Lock()
	_, expiredKept := c.entries["k-expired"]
	_, freshKept := c.entries["k-fresh"]
	_, inflightKept := c.entries["k-inflight"]
	c.mu.Unlock()
	if expiredKept {
		t.Error("expired entry still referenced after sweep")
	}
	if !freshKept {
		t.Error("fresh entry must survive the sweep")
	}
	if !inflightKept {
		t.Error("in-flight entry must survive the sweep")
	}

	close(gate)
	if err := <-inflightDone; err != nil {
		t.Fatalf("in-flight computation after sweep: %v", err)
	}
}

func TestProbeCacheEvictsWhenFull(t *testing.T) {
	var c probeCache
	ctx := context.Background()
	for i := 0; i < probeCacheMaxEntries+10; i++ {
		key := "k" + string(rune('a'+i%26)) + time.Now().String() + string(rune(i))
		if _, err := probeCacheDo(ctx, &c, key, time.Minute, func(context.Context) (int, error) { return i, nil }); err != nil {
			t.Fatalf("fill: %v", err)
		}
	}
	c.mu.Lock()
	n := len(c.entries)
	c.mu.Unlock()
	if n > probeCacheMaxEntries {
		t.Errorf("entries = %d, want <= %d", n, probeCacheMaxEntries)
	}
}
