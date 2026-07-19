import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const stateStore: unknown[] = [];
const effectCleanups: Array<(() => void) | void> = [];
const refStore: unknown[] = [];
let stateIndex = 0;
let refIndex = 0;

vi.mock("react", async () => {
  const actual = await vi.importActual<typeof import("react")>("react");

  return {
    ...actual,
    useState<T>(initial: T) {
      const index = stateIndex++;
      if (stateStore.length <= index) {
        stateStore[index] = initial;
      }
      const setState = (value: T | ((prev: T) => T)) => {
        stateStore[index] = typeof value === "function" ? (value as (prev: T) => T)(stateStore[index] as T) : value;
      };
      return [stateStore[index] as T, setState] as const;
    },
    useReducer<S, A>(reducer: (state: S, action: A) => S, initial: S) {
      const index = stateIndex++;
      if (stateStore.length <= index) {
        stateStore[index] = initial;
      }
      const dispatch = (action: A) => {
        stateStore[index] = reducer(stateStore[index] as S, action);
      };
      return [stateStore[index] as S, dispatch] as const;
    },
    useEffect(effect: () => void | (() => void)) {
      effectCleanups.push(effect());
    },
    useCallback<T>(callback: T) {
      return callback;
    },
    useMemo<T>(factory: () => T) {
      return factory();
    },
    useSyncExternalStore<T>(_subscribe: (cb: () => void) => () => void, getSnapshot: () => T) {
      return getSnapshot();
    },
    useRef<T>(initial: T) {
      const index = refIndex++;
      if (refStore.length <= index) {
        refStore[index] = { current: initial };
      }
      return refStore[index] as { current: T };
    },
  };
});

const clientMock = vi.hoisted(() => ({
  watchActivityLog: vi.fn(),
  getActivityLog: vi.fn(),
  watchDiff: vi.fn(),
  getDiff: vi.fn(),
  watchAgentTrace: vi.fn(),
  getAgentTrace: vi.fn(),
  listAgentRuns: vi.fn(),
  watchAgentRuns: vi.fn(),
}));

vi.mock("@/lib/client", () => ({
  client: clientMock,
}));

import { useActivityLog } from "./useActivityLog";
import { useAgentRuns } from "./useAgentRuns";
import { useAgentTrace } from "./useAgentTrace";
import { useDiff } from "./useDiff";
import { resetWatchStoresForTesting } from "./watchStore";

function resetHooks(): void {
  stateStore.length = 0;
  refStore.length = 0;
  stateIndex = 0;
  refIndex = 0;
  while (effectCleanups.length > 0) {
    const cleanup = effectCleanups.pop();
    cleanup?.();
  }
}

function flushMicrotasks(): Promise<void> {
  return Promise.resolve().then(() => Promise.resolve());
}

function createAsyncIterable<T>(values: T[], error?: Error): AsyncIterable<T> {
  return {
    async *[Symbol.asyncIterator]() {
      for (const value of values) {
        yield value;
      }
      if (error) {
        throw error;
      }
    },
  };
}

function pendingIterable(): AsyncIterable<never> {
  return {
    [Symbol.asyncIterator]() {
      return {
        next: () => new Promise<IteratorResult<never>>(() => {}),
      };
    },
  };
}

describe("streaming hooks", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    vi.clearAllMocks();
    resetWatchStoresForTesting();
    resetHooks();
  });

  afterEach(() => {
    resetHooks();
    resetWatchStoresForTesting();
    vi.mocked(Math.random).mockRestore?.();
    vi.useRealTimers();
  });

  it("retries activity log streaming after a transient disconnect", async () => {
    const entryA = { timestampUnix: 1n, type: "text", toolUseId: "", message: "a", output: "" };
    const entryB = { timestampUnix: 2n, type: "text", toolUseId: "", message: "b", output: "" };
    clientMock.watchActivityLog
      .mockReturnValueOnce(createAsyncIterable([{ entries: [entryA], isComplete: false } as never], new Error("disconnect")))
      .mockReturnValueOnce(createAsyncIterable([{ entries: [entryB], isComplete: true } as never]));

    const result = useActivityLog("ns", "run");
    await flushMicrotasks();

    expect(result.entries).toEqual([]);
    expect(clientMock.watchActivityLog).toHaveBeenCalledTimes(1);
    expect(stateStore[0]).toEqual([entryA]);
    expect(stateStore[3]).toBe("disconnect");

    await vi.advanceTimersByTimeAsync(1000);
    await flushMicrotasks();

    expect(clientMock.watchActivityLog).toHaveBeenCalledTimes(2);
    expect(stateStore[0]).toEqual([entryB]);
    expect(stateStore[3]).toBeNull();
  });

  it("falls back to diff snapshot when the stream fails before any frame", async () => {
    clientMock.watchDiff.mockImplementationOnce(() => ({
      [Symbol.asyncIterator]() {
        return {
          async next() {
            throw new Error("stream down");
          },
        };
      },
    }));
    clientMock.getDiff.mockResolvedValue({
      diff: "snapshot",
      isComplete: true,
      truncated: false,
      source: "snapshot",
      newFiles: [],
      newFilesTruncated: false,
    });

    const result = useDiff("ns", "run");
    await flushMicrotasks();

    expect(result.loading).toBe(true);
    expect(clientMock.getDiff).toHaveBeenCalledTimes(1);
    expect(clientMock.watchDiff).toHaveBeenCalledTimes(1);
    expect(stateStore[0]).toEqual({
      diff: "snapshot",
      isComplete: true,
      truncated: false,
      source: "snapshot",
      newFiles: [],
      newFilesTruncated: false,
      loading: false,
      error: null,
    });

    await vi.advanceTimersByTimeAsync(1000);
    expect(clientMock.watchDiff).toHaveBeenCalledTimes(1);
  });

  it("probes the diff with slow unary fetches while the stream is disabled", async () => {
    clientMock.getDiff.mockResolvedValue({
      diff: "+ change",
      isComplete: false,
      truncated: false,
      source: "pod",
    });

    useDiff("ns", "run", "Running", "AgentRun", "", { enabled: false });
    await flushMicrotasks();

    // No watch stream — just one unary probe that fills diff state so the
    // header's Create PR condition works without opening the Diff tab.
    expect(clientMock.watchDiff).not.toHaveBeenCalled();
    expect(clientMock.getDiff).toHaveBeenCalledTimes(1);
    expect((stateStore[0] as { diff: string }).diff).toBe("+ change");

    // Active run: re-probe on the slow cadence only.
    await vi.advanceTimersByTimeAsync(60_000);
    await flushMicrotasks();
    expect(clientMock.getDiff).toHaveBeenCalledTimes(2);
    expect(clientMock.watchDiff).not.toHaveBeenCalled();
  });

  it("stops probing the disabled diff once the snapshot is complete", async () => {
    clientMock.getDiff.mockResolvedValue({
      diff: "+ final",
      isComplete: true,
      truncated: false,
      source: "s3",
    });

    useDiff("ns", "run", "Succeeded", "AgentRun", "", { enabled: false });
    await flushMicrotasks();

    expect(clientMock.getDiff).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(120_000);
    await flushMicrotasks();
    expect(clientMock.getDiff).toHaveBeenCalledTimes(1);
    expect((stateStore[0] as { diff: string; isComplete: boolean }).isComplete).toBe(true);
  });

  it("preserves existing activity entries when dependencies change within the same boundary", async () => {
    clientMock.watchActivityLog.mockReturnValue(createAsyncIterable([{ entries: ["kept"], isComplete: false } as never]));

    useActivityLog("ns", "run", "Running", "boundary-a");
    await flushMicrotasks();

    expect(stateStore[0]).toEqual(["kept"]);
    expect(clientMock.watchActivityLog).toHaveBeenCalledTimes(1);

    resetHooks();
    useActivityLog("ns", "run", "Running", "boundary-a");
    await flushMicrotasks();

    expect(stateStore[0]).toEqual(["kept"]);
    expect(clientMock.watchActivityLog).toHaveBeenCalledTimes(2);
  });

  it("clears activity entries when the boundary changes", async () => {
    let releaseSecondStream: (() => void) | undefined;
    clientMock.watchActivityLog
      .mockReturnValueOnce(createAsyncIterable([{ entries: ["old"], isComplete: false } as never]))
      .mockReturnValueOnce({
        [Symbol.asyncIterator]: async function* () {
          await new Promise<void>((resolve) => {
            releaseSecondStream = resolve;
          });
          yield { entries: ["new"], isComplete: false } as never;
        },
      });

    useActivityLog("ns", "run", "Running", "boundary-a");
    await flushMicrotasks();

    expect(stateStore[0]).toEqual(["old"]);

    resetHooks();
    useActivityLog("ns", "run", "Running", "boundary-b");

    expect(stateStore[0]).toEqual([]);
    expect(clientMock.watchActivityLog).toHaveBeenCalledTimes(2);

    if (!releaseSecondStream) {
      throw new Error("second stream did not start");
    }
    releaseSecondStream();
    await flushMicrotasks();

    expect(stateStore[0]).toEqual(["new"]);
  });

  it("stops reopening agent trace stream once a completed trace snapshot is received", async () => {
    clientMock.watchAgentTrace.mockReturnValueOnce(createAsyncIterable([]));
    clientMock.getAgentTrace.mockResolvedValue({ traceId: "trace-1", isComplete: true });

    const result = useAgentTrace("ns", "run", "trace-1");
    await flushMicrotasks();

    expect(result.loading).toBe(false);
    expect(clientMock.watchAgentTrace).toHaveBeenCalledTimes(1);
    expect(clientMock.getAgentTrace).toHaveBeenCalledTimes(1);
    expect(stateStore[0]).toEqual({ traceId: "trace-1", isComplete: true });

    await vi.advanceTimersByTimeAsync(1000);
    expect(clientMock.watchAgentTrace).toHaveBeenCalledTimes(1);
  });

  it("retries agent runs streaming after a transient disconnect", async () => {
    vi.spyOn(Math, "random").mockReturnValue(0.999);
    // After the disconnect the loop re-lists to resync missed events; the
    // second list result is the post-outage source of truth.
    clientMock.listAgentRuns
      .mockResolvedValueOnce({ runs: [] })
      .mockResolvedValueOnce({ runs: [{ namespace: "ns", name: "one" }] });
    clientMock.watchAgentRuns
      .mockReturnValueOnce(createAsyncIterable([{ type: "MODIFIED", run: { namespace: "ns", name: "one" } }], new Error("disconnect")))
      .mockReturnValueOnce(createAsyncIterable([{ type: "MODIFIED", run: { namespace: "ns", name: "two" } }]));

    useAgentRuns("ns");
    await flushMicrotasks();

    expect(clientMock.watchAgentRuns).toHaveBeenCalledTimes(1);
    let result = useAgentRuns("ns");
    expect(result.runs).toEqual([{ namespace: "ns", name: "one" }]);
    expect(result.error).toBe("disconnect");

    await vi.advanceTimersByTimeAsync(1000);
    await flushMicrotasks();

    expect(clientMock.watchAgentRuns).toHaveBeenCalledTimes(2);
    expect(clientMock.listAgentRuns).toHaveBeenCalledTimes(2);
    result = useAgentRuns("ns");
    expect(result.runs).toEqual([
      { namespace: "ns", name: "one" },
      { namespace: "ns", name: "two" },
    ]);
    expect(result.error).toBeNull();
  });

  it("shares one underlying agent runs stream between subscribers", async () => {
    clientMock.listAgentRuns.mockResolvedValue({ runs: [{ namespace: "ns", name: "one" }] });
    clientMock.watchAgentRuns.mockReturnValue(pendingIterable());

    useAgentRuns("ns");
    useAgentRuns("ns");
    await flushMicrotasks();

    expect(clientMock.listAgentRuns).toHaveBeenCalledTimes(1);
    expect(clientMock.watchAgentRuns).toHaveBeenCalledTimes(1);

    const first = useAgentRuns("ns");
    const second = useAgentRuns("ns");
    expect(first.runs).toBe(second.runs);
    expect(first.runs).toEqual([{ namespace: "ns", name: "one" }]);
  });

  it("reuses previous activity entry references for the stable snapshot prefix", async () => {
    const entry = (message: string, ts: bigint) => ({ timestampUnix: ts, type: "text", toolUseId: "", message, output: "" });
    const firstFrame = [entry("a", 1n), entry("b", 2n)];
    const secondFrame = [entry("a", 1n), entry("b", 2n), entry("c", 3n)];
    clientMock.watchActivityLog.mockReturnValueOnce(
      createAsyncIterable([
        { entries: firstFrame, isComplete: false } as never,
        { entries: secondFrame, isComplete: false } as never,
      ]),
    );
    clientMock.getActivityLog.mockResolvedValue({ entries: secondFrame, isComplete: true });

    useActivityLog("ns", "run");
    await flushMicrotasks();

    const merged = stateStore[0] as unknown[];
    expect(merged).toHaveLength(3);
    expect(merged[0]).toBe(firstFrame[0]);
    expect(merged[1]).toBe(firstFrame[1]);
    expect(merged[2]).toBe(secondFrame[2]);
  });

  describe("activity log delta protocol", () => {
    const dEntry = (id: bigint, message: string) => ({
      eventId: id,
      timestampUnix: id,
      type: "text",
      toolUseId: "",
      message,
      output: "",
    });

    function frameThenPending<T>(frames: T[]): AsyncIterable<T> {
      return {
        async *[Symbol.asyncIterator]() {
          for (const frame of frames) {
            yield frame;
          }
          await new Promise<never>(() => {});
        },
      };
    }

    it("requests delta frames with preview/limit and appends delta entries with reference reuse and dedupe", async () => {
      const e1 = dEntry(1n, "a");
      const e2 = dEntry(2n, "b");
      const e3 = dEntry(3n, "c");
      clientMock.watchActivityLog.mockReturnValueOnce(
        createAsyncIterable([
          { entries: [e1, e2], isComplete: false, delta: true, reset: true, lastEventId: 2n, firstEventId: 1n, hasMoreBefore: true } as never,
          // e2 is re-sent: the dedupe guard (eventId > lastEventId) must drop it.
          { entries: [dEntry(2n, "b"), e3], isComplete: false, delta: true, reset: false, lastEventId: 3n } as never,
          { entries: [], isComplete: true, delta: true, reset: false, lastEventId: 3n } as never,
        ]),
      );

      useActivityLog("ns", "run");
      await flushMicrotasks();
      await flushMicrotasks();

      expect(clientMock.watchActivityLog).toHaveBeenCalledWith(
        expect.objectContaining({ namespace: "ns", name: "run", delta: true, payloadPreviewBytes: 2048, limit: 1000, sinceEventId: 0n }),
        expect.anything(),
      );
      const merged = stateStore[0] as unknown[];
      expect(merged).toHaveLength(3);
      expect(merged[0]).toBe(e1);
      expect(merged[1]).toBe(e2);
      expect(merged[2]).toBe(e3);
      expect(stateStore[4]).toBe(true); // isComplete from the empty trailing frame
      expect(stateStore[5]).toBe(true); // hasMoreBefore from the reset frame
    });

    it("resumes with sinceEventId and appends a pure resume-continuation reset frame", async () => {
      const e1 = dEntry(1n, "a");
      const e2 = dEntry(2n, "b");
      const e3 = dEntry(3n, "c");
      clientMock.watchActivityLog
        .mockReturnValueOnce(
          createAsyncIterable(
            [{ entries: [e1, e2], isComplete: false, delta: true, reset: true, lastEventId: 2n, firstEventId: 1n, hasMoreBefore: false } as never],
            new Error("disconnect"),
          ),
        )
        .mockReturnValueOnce(
          createAsyncIterable([
            { entries: [e3], isComplete: true, delta: true, reset: true, lastEventId: 3n, firstEventId: 3n, hasMoreBefore: true } as never,
          ]),
        );

      useActivityLog("ns", "run");
      await flushMicrotasks();
      await vi.advanceTimersByTimeAsync(1000);
      await flushMicrotasks();

      expect(clientMock.watchActivityLog).toHaveBeenCalledTimes(2);
      expect(clientMock.watchActivityLog).toHaveBeenLastCalledWith(
        expect.objectContaining({ sinceEventId: 2n }),
        expect.anything(),
      );
      const merged = stateStore[0] as unknown[];
      expect(merged).toHaveLength(3);
      expect(merged[0]).toBe(e1);
      expect(merged[1]).toBe(e2);
      expect(merged[2]).toBe(e3);
      // hasMoreBefore is NOT taken from an appended continuation frame.
      expect(stateStore[5]).toBe(false);
    });

    it("keeps existing entries when a resume reset frame carries no new events", async () => {
      const e1 = dEntry(1n, "a");
      const e2 = dEntry(2n, "b");
      clientMock.watchActivityLog
        .mockReturnValueOnce(
          createAsyncIterable(
            [{ entries: [e1, e2], isComplete: false, delta: true, reset: true, lastEventId: 2n, firstEventId: 1n, hasMoreBefore: true } as never],
            new Error("disconnect"),
          ),
        )
        .mockReturnValueOnce(
          frameThenPending([
            // Empty resume reset at the same cursor: nothing new since the
            // reconnect — must NOT wipe the rendered timeline.
            { entries: [], isComplete: false, delta: true, reset: true, lastEventId: 2n, firstEventId: 0n, hasMoreBefore: false } as never,
          ]),
        );

      useActivityLog("ns", "run");
      await flushMicrotasks();
      await vi.advanceTimersByTimeAsync(1000);
      await flushMicrotasks();

      expect(clientMock.watchActivityLog).toHaveBeenCalledTimes(2);
      expect(clientMock.watchActivityLog).toHaveBeenLastCalledWith(
        expect.objectContaining({ sinceEventId: 2n }),
        expect.anything(),
      );
      const merged = stateStore[0] as unknown[];
      expect(merged).toHaveLength(2);
      expect(merged[0]).toBe(e1);
      expect(merged[1]).toBe(e2);
      // Pagination state from before the reconnect survives too.
      expect(stateStore[5]).toBe(true);
    });

    it("replaces the buffer on reset frames whose ids regress (source flip)", async () => {
      const e1 = dEntry(1n, "a");
      const e2 = dEntry(2n, "b");
      const f1 = dEntry(1n, "flipped");
      clientMock.watchActivityLog.mockReturnValueOnce(
        createAsyncIterable([
          { entries: [e1, e2], isComplete: false, delta: true, reset: true, lastEventId: 2n, firstEventId: 1n, hasMoreBefore: true } as never,
          { entries: [f1], isComplete: true, delta: true, reset: true, lastEventId: 1n, firstEventId: 1n, hasMoreBefore: false } as never,
        ]),
      );

      useActivityLog("ns", "run");
      await flushMicrotasks();

      const merged = stateStore[0] as unknown[];
      expect(merged).toHaveLength(1);
      expect(merged[0]).toBe(f1);
      expect(stateStore[5]).toBe(false);
    });

    it("keeps the legacy full-snapshot merge path for non-delta frames", async () => {
      const entry = (message: string, ts: bigint) => ({ timestampUnix: ts, type: "text", toolUseId: "", message, output: "" });
      const firstFrame = [entry("a", 1n)];
      const secondFrame = [entry("a", 1n), entry("b", 2n)];
      clientMock.watchActivityLog.mockReturnValueOnce(
        createAsyncIterable([
          { entries: firstFrame, isComplete: false, delta: false } as never,
          { entries: secondFrame, isComplete: true, delta: false } as never,
        ]),
      );

      useActivityLog("ns", "run");
      await flushMicrotasks();

      const merged = stateStore[0] as unknown[];
      expect(merged).toHaveLength(2);
      expect(merged[0]).toBe(firstFrame[0]);
      expect(merged[1]).toBe(secondFrame[1]);
    });

    it("loadOlder prepends the unary page and updates hasMoreBefore", async () => {
      const e2 = dEntry(2n, "b");
      const e1 = dEntry(1n, "a");
      clientMock.watchActivityLog.mockReturnValueOnce(
        frameThenPending([
          { entries: [e2], isComplete: false, delta: true, reset: true, lastEventId: 2n, firstEventId: 2n, hasMoreBefore: true } as never,
        ]),
      );
      clientMock.getActivityLog.mockResolvedValueOnce({ entries: [e1], hasMoreBefore: false, isComplete: false });

      const { loadOlder } = useActivityLog("ns", "run");
      await flushMicrotasks();

      await loadOlder();

      expect(clientMock.getActivityLog).toHaveBeenCalledWith({
        namespace: "ns",
        name: "run",
        beforeEventId: 2n,
        limit: 1000,
        payloadPreviewBytes: 2048,
      });
      const merged = stateStore[0] as unknown[];
      expect(merged).toHaveLength(2);
      expect(merged[0]).toBe(e1);
      expect(merged[1]).toBe(e2);
      expect(stateStore[5]).toBe(false);

      // hasMoreBefore is now false: further calls are no-ops.
      await loadOlder();
      expect(clientMock.getActivityLog).toHaveBeenCalledTimes(1);
    });

    it("loadOlder guards against concurrent calls", async () => {
      const e2 = dEntry(2n, "b");
      clientMock.watchActivityLog.mockReturnValueOnce(
        frameThenPending([
          { entries: [e2], isComplete: false, delta: true, reset: true, lastEventId: 2n, firstEventId: 2n, hasMoreBefore: true } as never,
        ]),
      );
      let release: (value: unknown) => void = () => {};
      clientMock.getActivityLog.mockReturnValueOnce(
        new Promise((resolve) => {
          release = resolve;
        }),
      );

      const { loadOlder } = useActivityLog("ns", "run");
      await flushMicrotasks();

      const first = loadOlder();
      const second = loadOlder();
      release({ entries: [], hasMoreBefore: false, isComplete: false });
      await first;
      await second;

      expect(clientMock.getActivityLog).toHaveBeenCalledTimes(1);
    });

    it("loadOlder is a no-op when entries lack meaningful ids", async () => {
      clientMock.watchActivityLog.mockReturnValueOnce(
        frameThenPending([
          { entries: [dEntry(0n, "synthetic")], isComplete: false, delta: true, reset: true, lastEventId: 0n, firstEventId: 0n, hasMoreBefore: true } as never,
        ]),
      );

      const { loadOlder } = useActivityLog("ns", "run");
      await flushMicrotasks();

      await loadOlder();

      expect(clientMock.getActivityLog).not.toHaveBeenCalled();
    });
  });
});
