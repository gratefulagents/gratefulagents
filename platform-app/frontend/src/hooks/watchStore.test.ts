import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("@/lib/auth-interceptor", () => ({
  refreshOnUnauthenticated: vi.fn().mockResolvedValue(false),
}));

const workspaceState = vi.hoisted(() => ({
  activeId: "workspace-a",
  listeners: new Set<() => void>(),
}));

vi.mock("@/lib/workspaces", () => ({
  getActiveWorkspaceId: () => workspaceState.activeId,
  subscribeWorkspaces: (listener: () => void) => {
    workspaceState.listeners.add(listener);
    return () => workspaceState.listeners.delete(listener);
  },
}));

import { refreshOnUnauthenticated } from "@/lib/auth-interceptor";
import { getWatchStore, resetWatchStoresForTesting, type WatchStoreConfig } from "./watchStore";

type Item = { namespace: string; name: string };
type Event = { type: string; item: Item };

function pendingIterable(signal: AbortSignal, onAbort: () => void): AsyncIterable<Event> {
  return {
    [Symbol.asyncIterator]() {
      return {
        next: () =>
          new Promise<IteratorResult<Event>>((_, reject) => {
            signal.addEventListener("abort", () => {
              onAbort();
              reject(new Error("aborted"));
            });
          }),
      };
    },
  };
}

function flushMicrotasks(): Promise<void> {
  return Promise.resolve().then(() => Promise.resolve());
}

function switchWorkspace(activeId: string): void {
  workspaceState.activeId = activeId;
  for (const listener of workspaceState.listeners) listener();
}

describe("watchStore", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    vi.mocked(refreshOnUnauthenticated).mockReset().mockResolvedValue(false);
    switchWorkspace("workspace-a");
    resetWatchStoresForTesting();
  });

  afterEach(() => {
    resetWatchStoresForTesting();
    vi.useRealTimers();
  });

  function makeConfig() {
    let aborted = 0;
    const list = vi.fn().mockResolvedValue({ items: [{ namespace: "ns", name: "one" }] });
    const watch = vi.fn((options: { signal: AbortSignal }) =>
      pendingIterable(options.signal, () => {
        aborted++;
      }),
    );
    const config: WatchStoreConfig<Item, { items: Item[] }, Event> = {
      list,
      extractList: (res) => res.items,
      watch,
      applyEvent: (prev, event) => [...prev, event.item],
      label: "Items",
    };
    return { config, list, watch, abortedCount: () => aborted };
  }

  it("shares one underlying stream between two subscribers", async () => {
    const { config, list, watch } = makeConfig();
    const storeA = getWatchStore("Items:ns", () => config);
    const storeB = getWatchStore("Items:ns", () => config);
    expect(storeB).toBe(storeA);

    storeA.acquire();
    storeB.acquire();
    await flushMicrotasks();

    expect(list).toHaveBeenCalledTimes(1);
    expect(watch).toHaveBeenCalledTimes(1);
    expect(storeA.getSnapshot().items).toEqual([{ namespace: "ns", name: "one" }]);
    expect(storeB.getSnapshot()).toBe(storeA.getSnapshot());
  });

  it("invalidates cached data and streams on every workspace activation", async () => {
    const workspaceA = makeConfig();
    const storeA = getWatchStore("Items:ns", () => workspaceA.config);
    storeA.acquire();
    await flushMicrotasks();

    expect(storeA.getSnapshot().items).toEqual([{ namespace: "ns", name: "one" }]);

    switchWorkspace("workspace-b");

    expect(workspaceA.abortedCount()).toBe(1);
    expect(storeA.getSnapshot()).toEqual({ items: [], loading: true, error: null });

    const workspaceB = makeConfig();
    workspaceB.list.mockResolvedValue({
      items: [{ namespace: "ns", name: "two" }],
    });
    const storeB = getWatchStore("Items:ns", () => workspaceB.config);
    expect(storeB).not.toBe(storeA);
    expect(storeB.getSnapshot().items).toEqual([]);

    storeB.acquire();
    await flushMicrotasks();
    expect(storeB.getSnapshot().items).toEqual([{ namespace: "ns", name: "two" }]);

    switchWorkspace("workspace-a");

    expect(workspaceB.abortedCount()).toBe(1);
    const returningA = makeConfig();
    const freshStoreA = getWatchStore("Items:ns", () => returningA.config);
    expect(freshStoreA).not.toBe(storeA);
    expect(freshStoreA.getSnapshot()).toEqual({ items: [], loading: true, error: null });

    freshStoreA.acquire();
    await flushMicrotasks();
    expect(returningA.list).toHaveBeenCalledTimes(1);
    expect(returningA.watch).toHaveBeenCalledTimes(1);
  });

  it("ignores list responses that complete after a workspace switch", async () => {
    const { config, list } = makeConfig();
    let resolveList!: (value: { items: Item[] }) => void;
    list.mockReturnValueOnce(new Promise((resolve) => {
      resolveList = resolve;
    }));
    const store = getWatchStore("Items:ns", () => config);

    store.acquire();
    switchWorkspace("workspace-b");
    resolveList({ items: [{ namespace: "ns", name: "stale" }] });
    await flushMicrotasks();

    expect(store.getSnapshot()).toEqual({ items: [], loading: true, error: null });
  });

  it("ignores stream errors that finish authentication handling after a switch", async () => {
    const { config, watch } = makeConfig();
    watch.mockReturnValueOnce({
      [Symbol.asyncIterator]() {
        return { next: () => Promise.reject(new Error("old stream failed")) };
      },
    });
    let resolveRefresh!: (value: boolean) => void;
    vi.mocked(refreshOnUnauthenticated).mockReturnValueOnce(new Promise((resolve) => {
      resolveRefresh = resolve;
    }));
    const store = getWatchStore("Items:ns", () => config);

    store.acquire();
    await flushMicrotasks();
    expect(refreshOnUnauthenticated).toHaveBeenCalledTimes(1);

    switchWorkspace("workspace-b");
    resolveRefresh(false);
    await flushMicrotasks();

    expect(store.getSnapshot()).toEqual({ items: [], loading: true, error: null });
  });

  it("keeps the stream open until the last subscriber releases, then lingers", async () => {
    const { config, watch, abortedCount } = makeConfig();
    const store = getWatchStore("Items:ns", () => config);

    store.acquire();
    store.acquire();
    await flushMicrotasks();
    expect(watch).toHaveBeenCalledTimes(1);

    store.release();
    await vi.advanceTimersByTimeAsync(10_000);
    expect(abortedCount()).toBe(0);

    store.release();
    await vi.advanceTimersByTimeAsync(4000);
    expect(abortedCount()).toBe(0);

    await vi.advanceTimersByTimeAsync(2000);
    expect(abortedCount()).toBe(1);

    // A fresh subscriber after teardown gets a new loop.
    const fresh = getWatchStore("Items:ns", () => config);
    expect(fresh).not.toBe(store);
  });

  it("cancels teardown when a subscriber re-acquires within the linger window", async () => {
    const { config, abortedCount } = makeConfig();
    const store = getWatchStore("Items:ns", () => config);

    store.acquire();
    await flushMicrotasks();
    store.release();
    await vi.advanceTimersByTimeAsync(2000);
    store.acquire();
    await vi.advanceTimersByTimeAsync(20_000);

    expect(abortedCount()).toBe(0);
    expect(getWatchStore("Items:ns", () => config)).toBe(store);
  });

  it("notifies subscribers when the snapshot changes", async () => {
    const { config } = makeConfig();
    const store = getWatchStore("Items:ns", () => config);
    const listener = vi.fn();
    store.subscribe(listener);
    store.acquire();
    await flushMicrotasks();

    expect(listener).toHaveBeenCalled();
    expect(store.getSnapshot().loading).toBe(false);
  });

  it("re-lists after a dropped stream so missed events are not lost", async () => {
    let listCalls = 0;
    const list = vi.fn(() => {
      listCalls++;
      return Promise.resolve({
        items: [{ namespace: "ns", name: listCalls === 1 ? "one" : "two" }],
      });
    });
    // First watch attempt dies immediately (proxy drop); later attempts hang.
    let watchCalls = 0;
    const watch = vi.fn((options: { signal: AbortSignal }) => {
      watchCalls++;
      if (watchCalls === 1) {
        return {
          [Symbol.asyncIterator]() {
            return {
              next: () => Promise.reject(new Error("HTTP 424")),
            };
          },
        } as AsyncIterable<Event>;
      }
      return pendingIterable(options.signal, () => {});
    });
    const config: WatchStoreConfig<Item, { items: Item[] }, Event> = {
      list,
      extractList: (res) => res.items,
      watch,
      applyEvent: (prev, event) => [...prev, event.item],
      label: "Items",
    };

    const store = getWatchStore("Items:resync", () => config);
    store.acquire();
    await flushMicrotasks();

    // Initial list + failed first watch → error surfaced, data kept.
    expect(store.getSnapshot().items).toEqual([{ namespace: "ns", name: "one" }]);
    expect(store.getSnapshot().error).not.toBeNull();

    // Backoff elapses → loop re-lists (resync) and re-watches.
    await vi.advanceTimersByTimeAsync(30_000);
    expect(list.mock.calls.length).toBeGreaterThanOrEqual(2);
    expect(watch).toHaveBeenCalledTimes(2);
    expect(store.getSnapshot().items).toEqual([{ namespace: "ns", name: "two" }]);
    expect(store.getSnapshot().error).toBeNull();

    store.release();
  });

  it("keeps the last good items when a refetch fails", async () => {
    const { ConnectError, Code } = await import("@connectrpc/connect");
    let listCalls = 0;
    const list = vi.fn(() => {
      listCalls++;
      if (listCalls === 1) {
        return Promise.resolve({ items: [{ namespace: "ns", name: "one" }] });
      }
      // What connect-web actually throws when a proxy answers 424.
      return Promise.reject(new ConnectError("HTTP 424", Code.Unknown));
    });
    const watch = vi.fn((options: { signal: AbortSignal }) =>
      pendingIterable(options.signal, () => {}),
    );
    const config: WatchStoreConfig<Item, { items: Item[] }, Event> = {
      list,
      extractList: (res) => res.items,
      watch,
      applyEvent: (prev, event) => [...prev, event.item],
      label: "Items",
    };

    const store = getWatchStore("Items:staleness", () => config);
    store.acquire();
    await flushMicrotasks();
    expect(store.getSnapshot().items).toEqual([{ namespace: "ns", name: "one" }]);

    await store.refetch();
    // Data survives; the failure is reported alongside it.
    expect(store.getSnapshot().items).toEqual([{ namespace: "ns", name: "one" }]);
    expect(store.getSnapshot().error).toContain("temporarily unreachable");

    store.release();
  });
});
