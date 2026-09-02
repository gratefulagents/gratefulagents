import { act, cleanup, renderHook } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";

const clientMock = vi.hoisted(() => ({
  watchActivityLog: vi.fn(),
  getActivityLog: vi.fn(),
}));

vi.mock("@/lib/client", () => ({ client: clientMock }));
vi.mock("@/lib/auth-interceptor", () => ({
  refreshOnUnauthenticated: vi.fn().mockResolvedValue(false),
}));

import { applyDeltaEntries, createRetryWaiter, useActivityLog } from "./useActivityLog";
import { useRunActivityLog } from "./useRunActivityLog";
import type { ActivityEntry } from "@/rpc/platform/service_pb";

/** A stream that yields its frames and then stays open. */
function openStream<T>(values: T[]): AsyncIterable<T> {
  return {
    async *[Symbol.asyncIterator]() {
      for (const value of values) {
        yield value;
      }
      await new Promise<void>(() => {});
    },
  };
}

function flush(): Promise<void> {
  return act(async () => {
    await Promise.resolve();
    await Promise.resolve();
  });
}

function entry(overrides: Partial<ActivityEntry>): ActivityEntry {
  return {
    eventId: 1n,
    timestampUnix: 1n,
    type: "text",
    toolUseId: "",
    message: "",
    output: "",
    ...overrides,
  } as ActivityEntry;
}

describe("applyDeltaEntries", () => {
  it("replaces a grown assistant_thinking entry in place without duplicating", () => {
    const thinking = entry({ eventId: 2n, type: "assistant_thinking", toolUseId: "think-1", message: "par" });
    const tool = entry({ eventId: 3n, timestampUnix: 2n, type: "tool_use", toolUseId: "tool-1" });
    const existing = [entry({ eventId: 1n }), thinking, tool];
    const grown = entry({
      eventId: 4n,
      type: "assistant_thinking",
      toolUseId: "think-1",
      message: "partial plus more",
    });
    const result = applyDeltaEntries(existing, [grown], 3n);
    expect(result.entries).toHaveLength(3);
    expect(result.entries[1]).toBe(grown);
    expect(result.entries[1]).not.toBe(thinking);
    expect(result.entries[0]).toBe(existing[0]);
    expect(result.entries[2]).toBe(tool);
    expect(result.lastEventId).toBe(4n);
  });

  it("appends a normal new entry", () => {
    const existing = [entry({ eventId: 1n })];
    const incoming = entry({ eventId: 2n, timestampUnix: 2n, message: "new" });
    const result = applyDeltaEntries(existing, [incoming], 1n);
    expect(result.entries).toHaveLength(2);
    expect(result.entries[0]).toBe(existing[0]);
    expect(result.entries[1]).toBe(incoming);
    expect(result.lastEventId).toBe(2n);
  });

  it("does not upsert an entry with the same toolUseId but a different type", () => {
    const thinking = entry({ eventId: 1n, type: "assistant_thinking", toolUseId: "shared", message: "thought" });
    const existing = [thinking];
    const attempt = entry({ eventId: 2n, type: "llm_attempt", toolUseId: "shared" });
    const result = applyDeltaEntries(existing, [attempt], 1n);
    expect(result.entries).toHaveLength(2);
    expect(result.entries[0]).toBe(thinking);
    expect(result.entries[1]).toBe(attempt);
  });

  it("does not upsert assistant_thinking entries with an empty toolUseId", () => {
    const existing = [entry({ eventId: 1n, type: "assistant_thinking", toolUseId: "", message: "a" })];
    const incoming = entry({ eventId: 2n, type: "assistant_thinking", toolUseId: "", message: "b" });
    const result = applyDeltaEntries(existing, [incoming], 1n);
    expect(result.entries).toHaveLength(2);
  });

  it("drops entries at or below the cursor and returns existing unchanged", () => {
    const existing = [entry({ eventId: 2n })];
    const result = applyDeltaEntries(existing, [entry({ eventId: 2n }), entry({ eventId: 1n })], 2n);
    expect(result.entries).toBe(existing);
    expect(result.lastEventId).toBe(2n);
  });
});

describe("useActivityLog stream lifecycle", () => {
  afterEach(() => {
    cleanup();
    vi.useRealTimers();
    clientMock.watchActivityLog.mockReset();
    clientMock.getActivityLog.mockReset();
  });

  it("opens a single stream when the refresh key settles after the run loads", async () => {
    clientMock.watchActivityLog.mockImplementation(() => openStream([]));

    // Mirrors RunSessionView: the key is empty until the run snapshot arrives,
    // and the stream is gated on the run being known.
    const { rerender } = renderHook(
      ({ key, enabled }: { key: string; enabled: boolean }) =>
        useRunActivityLog("ns", "run", "Running", key, { enabled }),
      { initialProps: { key: "", enabled: false } },
    );
    await flush();
    expect(clientMock.watchActivityLog).not.toHaveBeenCalled();

    rerender({ key: "ns::run::1::0", enabled: true });
    await flush();
    expect(clientMock.watchActivityLog).toHaveBeenCalledTimes(1);

    rerender({ key: "ns::run::1::0", enabled: true });
    await flush();
    expect(clientMock.watchActivityLog).toHaveBeenCalledTimes(1);
  });

  it("reconnects an idle stream when the page becomes visible again", async () => {
    vi.useFakeTimers();
    const signals: AbortSignal[] = [];
    clientMock.watchActivityLog.mockImplementation((_req: unknown, opts: { signal: AbortSignal }) => {
      signals.push(opts.signal);
      return openStream([
        { entries: [], isComplete: false, delta: true, reset: true, lastEventId: 0n },
      ]);
    });
    Object.defineProperty(document, "visibilityState", { value: "visible", configurable: true });

    renderHook(() => useActivityLog("ns", "run", "Running", "k"));
    await flush();
    expect(clientMock.watchActivityLog).toHaveBeenCalledTimes(1);

    // Fresh frame: the stream is trusted.
    act(() => {
      document.dispatchEvent(new Event("visibilitychange"));
    });
    await flush();
    expect(clientMock.watchActivityLog).toHaveBeenCalledTimes(1);

    await act(async () => {
      await vi.advanceTimersByTimeAsync(6000);
    });
    act(() => {
      document.dispatchEvent(new Event("visibilitychange"));
    });
    await flush();
    expect(signals[0].aborted).toBe(true);
    expect(clientMock.watchActivityLog).toHaveBeenCalledTimes(2);
  });
});

describe("createRetryWaiter", () => {
  afterEach(() => {
    vi.useRealTimers();
  });

  it("resolves after the delay and immediately on cancel", async () => {
    vi.useFakeTimers();
    const waiter = createRetryWaiter();
    let resolved = false;
    void waiter.wait(1000).then(() => {
      resolved = true;
    });
    await vi.advanceTimersByTimeAsync(999);
    expect(resolved).toBe(false);
    await vi.advanceTimersByTimeAsync(1);
    expect(resolved).toBe(true);

    let cancelled = false;
    void waiter.wait(5000).then(() => {
      cancelled = true;
    });
    waiter.cancel();
    await Promise.resolve();
    expect(cancelled).toBe(true);
    expect(vi.getTimerCount()).toBe(0);
  });
});
