package dashboard

import (
	"context"
	"sync"
	"time"
)

// Probe TTLs. Watch streams poll on fixed tick intervals; keeping each TTL
// below the corresponding tick means a single stream still observes every
// tick's changes, while N concurrent streams for the same resource collapse
// to ~one query per TTL window instead of N.
const (
	// probeSummaryVersionsTTL caches per-namespace AgentRun summary versions
	// (WatchAgentRuns ticks every 2s per open fleet page).
	probeSummaryVersionsTTL = 1 * time.Second
	// probeFingerprintTTL caches per-session fingerprints (WatchAgentRun
	// ticks every 500ms per open run view).
	probeFingerprintTTL = 300 * time.Millisecond
	// probeLatestEventTTL caches per-session latest activity event IDs
	// (WatchActivityLog ticks every 500ms per open run view).
	probeLatestEventTTL = 300 * time.Millisecond
	// probeSessionTTL caches run→session lookups used by activity-log
	// surfaces. A run's session row is created once and its ID never
	// changes; the TTL only bounds staleness across run delete/recreate.
	probeSessionTTL = 2 * time.Second
	// probeACLTTL caches bulk ownership/share loads used by visibility
	// filters and watch enrichment. Ownership changes are rare; watch ticks
	// pick them up within a second.
	probeACLTTL = 1 * time.Second
	// probeDiffTTL caches built diff responses (WatchDiff ticks every 750ms
	// per open diff view; live builds exec git diff inside the sandbox pod).
	probeDiffTTL = 500 * time.Millisecond
	// probeTraceTTL caches built trace responses (WatchAgentTrace ticks
	// every 750ms per open trace view; each build is a Jaeger HTTP fetch).
	probeTraceTTL = 500 * time.Millisecond

	// probeCacheMaxEntries bounds the cache; keys are per-namespace,
	// per-session, per-run, or per-actor, so this is generous.
	probeCacheMaxEntries = 8192
	// probeCacheSweepInterval bounds how long an expired value can stay
	// referenced. Cached values can be large (built diff and trace
	// responses), so expired entries are swept on the next cache operation
	// at most this often instead of lingering until the size cap forces
	// eviction.
	probeCacheSweepInterval = time.Second
	// probeCacheFnTimeout bounds a detached computation. Probe queries are
	// single-row index reads; anything slower should fail the probe rather
	// than pile up.
	probeCacheFnTimeout = 10 * time.Second
)

// probeCache coalesces identical short-lived reads across concurrent
// requests and watch streams. Every open dashboard page polls the same
// Postgres probes (summary versions, session fingerprints, latest event IDs,
// ACL bulk loads) on its own tick; without coalescing the query load scales
// with the number of open streams instead of the number of distinct
// resources.
//
// Semantics:
//   - A fresh cached value is returned as-is.
//   - Concurrent callers for the same key join one in-flight computation
//     (singleflight) instead of issuing duplicate queries.
//   - Errors are returned to the callers that observed them but never
//     cached; the next caller recomputes.
//   - The computation runs on a context detached from the triggering
//     caller, so one stream disconnecting mid-query does not fail the
//     probe for every other stream that joined it.
type probeCache struct {
	mu        sync.Mutex
	entries   map[string]*probeCacheEntry
	lastSweep time.Time
}

type probeCacheEntry struct {
	ready      chan struct{} // closed once val/err are set
	val        any
	err        error
	expiresAt  time.Time
	lastAccess time.Time
}

// do implements the cache/singleflight protocol for one key. fn runs at most
// once per TTL window across all concurrent callers.
func (c *probeCache) do(ctx context.Context, key string, ttl time.Duration, fn func(context.Context) (any, error)) (any, error) {
	now := time.Now()
	c.mu.Lock()
	if c.entries == nil {
		c.entries = make(map[string]*probeCacheEntry)
	}
	c.sweepLocked(now)
	if e, ok := c.entries[key]; ok {
		select {
		case <-e.ready:
			if now.Before(e.expiresAt) {
				e.lastAccess = now
				val := e.val
				c.mu.Unlock()
				return val, nil
			}
			// Expired: fall through and recompute.
		default:
			// In-flight: wait outside the lock and share the result.
			c.mu.Unlock()
			select {
			case <-e.ready:
				if e.err != nil {
					return nil, e.err
				}
				return e.val, nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
	}

	e := &probeCacheEntry{ready: make(chan struct{}), lastAccess: now}
	c.evictLocked(now)
	c.entries[key] = e
	c.mu.Unlock()

	fnCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), probeCacheFnTimeout)
	val, err := fn(fnCtx)
	cancel()

	c.mu.Lock()
	e.val, e.err = val, err
	e.expiresAt = time.Now().Add(ttl)
	if err != nil && c.entries[key] == e {
		delete(c.entries, key) // never serve cached errors
	}
	close(e.ready)
	c.mu.Unlock()
	return val, err
}

// sweepLocked drops every expired entry, at most once per
// probeCacheSweepInterval. Expired entries are never served again (do
// recomputes them), so this only releases their values — which matters for
// keys that stop being polled (e.g. a closed diff view) and would otherwise
// pin their last payload until the size cap forces eviction. In-flight
// entries are kept. Callers must hold c.mu.
func (c *probeCache) sweepLocked(now time.Time) {
	if now.Sub(c.lastSweep) < probeCacheSweepInterval {
		return
	}
	c.lastSweep = now
	for k, e := range c.entries {
		select {
		case <-e.ready:
			if now.After(e.expiresAt) {
				delete(c.entries, k)
			}
		default: // in-flight, keep
		}
	}
}

// evictLocked keeps the cache bounded: expired entries first, then the
// least-recently-accessed one. Callers must hold c.mu.
func (c *probeCache) evictLocked(now time.Time) {
	if len(c.entries) < probeCacheMaxEntries {
		return
	}
	for k, e := range c.entries {
		select {
		case <-e.ready:
			if now.After(e.expiresAt) {
				delete(c.entries, k)
			}
		default: // in-flight, keep
		}
	}
	if len(c.entries) < probeCacheMaxEntries {
		return
	}
	var oldestKey string
	var oldest time.Time
	for k, e := range c.entries {
		select {
		case <-e.ready:
			if oldestKey == "" || e.lastAccess.Before(oldest) {
				oldestKey, oldest = k, e.lastAccess
			}
		default:
		}
	}
	if oldestKey != "" {
		delete(c.entries, oldestKey)
	}
}

// reset drops every cached entry. Tests use it to observe a mutation
// immediately instead of waiting out a probe TTL.
func (c *probeCache) reset() {
	c.mu.Lock()
	c.entries = nil
	c.mu.Unlock()
}

// probeCacheDo is the typed wrapper around probeCache.do.
func probeCacheDo[T any](ctx context.Context, c *probeCache, key string, ttl time.Duration, fn func(context.Context) (T, error)) (T, error) {
	v, err := c.do(ctx, key, ttl, func(ctx context.Context) (any, error) { return fn(ctx) })
	if err != nil {
		var zero T
		return zero, err
	}
	return v.(T), nil
}
