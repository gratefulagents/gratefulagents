import { refreshOnUnauthenticated } from "@/lib/auth-interceptor";
import { describeRpcError } from "@/lib/rpc-errors";
import { getActiveWorkspaceId, subscribeWorkspaces } from "@/lib/workspaces";
import { backoffDelayMs } from "./backoff";

/**
 * Module-level shared list+watch store with refcounting. The first subscriber
 * for a given key starts the underlying list+watch loop, later subscribers
 * reuse it, and the last unsubscribe stops it after a short linger so quick
 * remounts (navigation, StrictMode) don't tear the stream down.
 */

export interface WatchStoreSnapshot<T> {
  items: T[];
  loading: boolean;
  error: string | null;
}

export interface WatchStoreConfig<T, L, E> {
  list(): Promise<L>;
  extractList(res: L): T[];
  watch(options: { signal: AbortSignal }): AsyncIterable<E>;
  /** Pure merge of one watch event into the current items (never mutates). */
  applyEvent(prev: T[], event: E): T[];
  label: string;
}

const LINGER_MS = 5000;

export class WatchStore<T, L = unknown, E = unknown> {
  private listeners = new Set<() => void>();
  private snapshot: WatchStoreSnapshot<T> = { items: [], loading: true, error: null };
  private controller: AbortController | null = null;
  private retryTimer: ReturnType<typeof setTimeout> | null = null;
  private resolveRetry: (() => void) | null = null;
  private running = false;
  private generation = 0;
  private refCount = 0;
  private lingerTimer: ReturnType<typeof setTimeout> | null = null;
  private wasBackgrounded = false;

  constructor(
    private readonly config: WatchStoreConfig<T, L, E>,
    private readonly onStopped: () => void,
  ) {}

  getSnapshot = (): WatchStoreSnapshot<T> => this.snapshot;

  subscribe = (listener: () => void): (() => void) => {
    this.listeners.add(listener);
    return () => {
      this.listeners.delete(listener);
    };
  };

  acquire(): void {
    this.refCount++;
    if (this.lingerTimer !== null) {
      clearTimeout(this.lingerTimer);
      this.lingerTimer = null;
    }
    if (!this.running) {
      this.running = true;
      this.installForegroundListeners();
      void this.refetch();
      void this.watchLoop(this.generation);
    }
  }

  release(): void {
    this.refCount--;
    if (this.refCount <= 0 && this.lingerTimer === null) {
      this.lingerTimer = setTimeout(() => {
        this.lingerTimer = null;
        if (this.refCount <= 0) {
          this.stop();
        }
      }, LINGER_MS);
    }
  }

  refetch = async (): Promise<void> => {
    const generation = this.generation;
    try {
      const res = await this.config.list();
      if (!this.running || generation !== this.generation) return;
      this.emit({ items: this.config.extractList(res), error: null, loading: false });
    } catch (e) {
      if (!this.running || generation !== this.generation) return;
      this.emit({
        error: describeRpcError(e, `load ${this.config.label}`),
        loading: false,
      });
    }
  };

  private emit(patch: Partial<WatchStoreSnapshot<T>>): void {
    this.snapshot = { ...this.snapshot, ...patch };
    for (const listener of this.listeners) {
      listener();
    }
  }

  private waitForRetry(attempt: number): Promise<void> {
    return new Promise((resolve) => {
      this.resolveRetry = resolve;
      this.retryTimer = setTimeout(() => {
        this.retryTimer = null;
        this.resolveRetry = null;
        resolve();
      }, backoffDelayMs(attempt));
    });
  }

  private cancelRetry(): void {
    if (this.retryTimer !== null) {
      clearTimeout(this.retryTimer);
      this.retryTimer = null;
    }
    const resolve = this.resolveRetry;
    this.resolveRetry = null;
    resolve?.();
  }

  private async watchLoop(loopGeneration: number): Promise<void> {
    let attempt = 0;
    let resync = false;
    while (this.running && loopGeneration === this.generation) {
      // After a dropped stream, re-list before re-watching: events that fired
      // while we were disconnected would otherwise be lost until a remount.
      if (resync) {
        await this.refetch();
        if (!this.running || loopGeneration !== this.generation) {
          return;
        }
      }
      const controller = new AbortController();
      this.controller = controller;
      try {
        for await (const event of this.config.watch({ signal: controller.signal })) {
          if (!this.running || loopGeneration !== this.generation || controller.signal.aborted) {
            return;
          }
          attempt = 0;
          const next = this.config.applyEvent(this.snapshot.items, event);
          if (next !== this.snapshot.items || this.snapshot.error !== null) {
            this.emit({ items: next, error: null });
          }
        }
      } catch (e) {
        if (!this.running || loopGeneration !== this.generation || controller.signal.aborted) {
          return;
        }
        const refreshed = await refreshOnUnauthenticated(e);
        if (!this.running || loopGeneration !== this.generation || controller.signal.aborted) {
          return;
        }
        if (!refreshed) {
          this.emit({
            error: describeRpcError(e, `stream ${this.config.label}`),
          });
        }
      } finally {
        if (this.controller === controller) {
          this.controller = null;
        }
      }
      if (!this.running || loopGeneration !== this.generation) {
        return;
      }
      resync = true;
      await this.waitForRetry(attempt++);
    }
  }

  private installForegroundListeners(): void {
    if (typeof window === "undefined" || typeof document === "undefined") return;
    window.addEventListener("blur", this.handleBackground);
    window.addEventListener("focus", this.handleForeground);
    document.addEventListener("visibilitychange", this.handleVisibilityChange);
  }

  private removeForegroundListeners(): void {
    if (typeof window === "undefined" || typeof document === "undefined") return;
    window.removeEventListener("blur", this.handleBackground);
    window.removeEventListener("focus", this.handleForeground);
    document.removeEventListener("visibilitychange", this.handleVisibilityChange);
  }

  private handleBackground = (): void => {
    this.wasBackgrounded = true;
  };

  private handleForeground = (): void => {
    if (!this.wasBackgrounded || !this.running) return;
    this.wasBackgrounded = false;

    // A response body that was open while WKWebView was suspended is not safe
    // to keep decoding. Retire that loop without surfacing its abort error,
    // then immediately re-list and open a fresh stream. The generation also
    // prevents late responses from the suspended connection changing state.
    const generation = ++this.generation;
    this.controller?.abort();
    this.controller = null;
    this.cancelRetry();
    void this.refetch();
    void this.watchLoop(generation);
  };

  private handleVisibilityChange = (): void => {
    if (document.visibilityState === "hidden") {
      this.handleBackground();
    } else {
      this.handleForeground();
    }
  };

  invalidate(): void {
    this.emit({ items: [], loading: true, error: null });
    this.stop();
  }

  private stop(): void {
    this.running = false;
    this.generation++;
    this.controller?.abort();
    this.controller = null;
    this.cancelRetry();
    this.removeForegroundListeners();
    this.wasBackgrounded = false;
    this.onStopped();
  }
}

const stores = new Map<string, WatchStore<unknown>>();
let activeWorkspaceId = getActiveWorkspaceId();

// Workspace changes are hard cache boundaries. Invalidate synchronously so
// mounted consumers stop showing the old backend's snapshot, abort its stream,
// and ignore any unary response that completes after the switch.
subscribeWorkspaces(() => {
  const nextWorkspaceId = getActiveWorkspaceId();
  if (nextWorkspaceId === activeWorkspaceId) return;
  activeWorkspaceId = nextWorkspaceId;
  for (const store of stores.values()) store.invalidate();
  stores.clear();
});

/**
 * Get (or lazily create) the shared store for `key`. The store only starts
 * its list+watch loop once a component acquires it via effect.
 */
export function getWatchStore<T, L, E>(key: string, makeConfig: () => WatchStoreConfig<T, L, E>): WatchStore<T, L, E> {
  // Namespaces and resource names are only unique within a backend workspace.
  // Keeping the workspace in the registry key prevents a lingering stream or
  // cached snapshot from the previous backend being reused after a switch.
  const workspaceKey = `${getActiveWorkspaceId()}:${key}`;
  let store = stores.get(workspaceKey);
  if (!store) {
    store = new WatchStore(makeConfig() as WatchStoreConfig<unknown, unknown, unknown>, () => {
      if (stores.get(workspaceKey) === store) {
        stores.delete(workspaceKey);
      }
    });
    stores.set(workspaceKey, store);
  }
  return store as WatchStore<T, L, E>;
}

/** Test-only: abort all shared watch loops and clear the registry. */
export function resetWatchStoresForTesting(): void {
  for (const store of stores.values()) store.invalidate();
  stores.clear();
}
