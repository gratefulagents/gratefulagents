import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { Code, ConnectError } from "@connectrpc/connect";

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
    useEffect(effect: () => void | (() => void)) {
      effectCleanups.push(effect());
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
  getAgentRun: vi.fn(),
  watchAgentRun: vi.fn(),
}));

vi.mock("@/lib/client", () => ({
  client: clientMock,
}));

vi.mock("@/lib/auth-interceptor", () => ({
  refreshOnUnauthenticated: vi.fn().mockResolvedValue(false),
}));

import { useAgentRun } from "./useAgentRun";

// State slot indices matching the useState order inside useAgentRun.
const RUN = 0;
const LOADING = 1;
const ERROR = 2;
const STARTING = 3;

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

function notFoundError(): ConnectError {
  return new ConnectError("get AgentRun ns/run: not found", Code.NotFound);
}

/** An async iterable that never yields and never completes. */
function pendingStream(): AsyncIterable<never> {
  return {
    [Symbol.asyncIterator]() {
      return {
        next: () => new Promise<IteratorResult<never>>(() => {}),
      };
    },
  };
}

function yieldingStream<T>(values: T[]): AsyncIterable<T> {
  return {
    async *[Symbol.asyncIterator]() {
      for (const value of values) {
        yield value;
      }
      await new Promise<void>(() => {});
    },
  };
}

/** An async iterable that yields its values and then ends (or throws). */
function endingStream<T>(values: T[], error?: Error): AsyncIterable<T> {
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

describe("useAgentRun", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    vi.clearAllMocks();
    resetHooks();
  });

  afterEach(() => {
    resetHooks();
    vi.mocked(Math.random).mockRestore?.();
    vi.useRealTimers();
  });

  it("keeps loading and retries when a fresh run is briefly NotFound", async () => {
    clientMock.getAgentRun
      .mockRejectedValueOnce(notFoundError())
      .mockResolvedValueOnce({ name: "run", namespace: "ns" });
    clientMock.watchAgentRun.mockImplementation(() => pendingStream());

    useAgentRun("ns", "run");
    await flushMicrotasks();

    // Still within the grace window: no error, still loading, marked starting.
    expect(stateStore[RUN]).toBeNull();
    expect(stateStore[LOADING]).toBe(true);
    expect(stateStore[ERROR]).toBeNull();
    expect(stateStore[STARTING]).toBe(true);

    await vi.advanceTimersByTimeAsync(1000);
    await flushMicrotasks();

    expect(clientMock.getAgentRun).toHaveBeenCalledTimes(2);
    expect(stateStore[RUN]).toEqual({ name: "run", namespace: "ns" });
    expect(stateStore[LOADING]).toBe(false);
    expect(stateStore[ERROR]).toBeNull();
    expect(stateStore[STARTING]).toBe(false);
  });

  it("surfaces the NotFound error once the grace window expires", async () => {
    clientMock.getAgentRun.mockImplementation(() => Promise.reject(notFoundError()));
    clientMock.watchAgentRun.mockImplementation(() => pendingStream());

    useAgentRun("ns", "run");
    await flushMicrotasks();

    expect(stateStore[LOADING]).toBe(true);
    expect(stateStore[ERROR]).toBeNull();

    await vi.advanceTimersByTimeAsync(16_000);
    await flushMicrotasks();

    expect(stateStore[LOADING]).toBe(false);
    expect(stateStore[STARTING]).toBe(false);
    expect(String(stateStore[ERROR])).toContain("not found");
  });

  it("fails fast on non-NotFound errors", async () => {
    clientMock.getAgentRun.mockRejectedValue(new ConnectError("boom", Code.Internal));
    clientMock.watchAgentRun.mockImplementation(() => pendingStream());

    useAgentRun("ns", "run");
    await flushMicrotasks();

    expect(stateStore[LOADING]).toBe(false);
    expect(stateStore[STARTING]).toBe(false);
    expect(String(stateStore[ERROR])).toContain("boom");
  });

  it("recovers via the watch stream while the snapshot is still NotFound", async () => {
    clientMock.getAgentRun.mockImplementation(() => Promise.reject(notFoundError()));
    clientMock.watchAgentRun.mockImplementation(() =>
      yieldingStream([{ name: "run", namespace: "ns", phase: "Pending" }]),
    );

    useAgentRun("ns", "run");
    await flushMicrotasks();

    expect(stateStore[RUN]).toEqual({ name: "run", namespace: "ns", phase: "Pending" });
    expect(stateStore[LOADING]).toBe(false);
    expect(stateStore[ERROR]).toBeNull();
    expect(stateStore[STARTING]).toBe(false);

    // A trailing NotFound from the snapshot fetch must not clobber the run.
    await vi.advanceTimersByTimeAsync(1000);
    await flushMicrotasks();
    expect(stateStore[RUN]).toEqual({ name: "run", namespace: "ns", phase: "Pending" });
    expect(stateStore[ERROR]).toBeNull();
  });

  it("stops reconnecting once the run reaches a terminal phase", async () => {
    clientMock.getAgentRun.mockResolvedValue({ name: "run", namespace: "ns", phase: "Succeeded" });
    clientMock.watchAgentRun.mockImplementation(() =>
      endingStream([{ name: "run", namespace: "ns", phase: "Succeeded" }]),
    );

    useAgentRun("ns", "run");
    await flushMicrotasks();

    expect(stateStore[RUN]).toEqual({ name: "run", namespace: "ns", phase: "Succeeded" });
    expect(stateStore[LOADING]).toBe(false);
    expect(clientMock.watchAgentRun).toHaveBeenCalledTimes(1);

    await vi.advanceTimersByTimeAsync(60_000);
    await flushMicrotasks();

    expect(clientMock.watchAgentRun).toHaveBeenCalledTimes(1);
  });

  it("keeps reconnecting with growing backoff after stream errors on a non-terminal run", async () => {
    vi.spyOn(Math, "random").mockReturnValue(0.999);
    clientMock.getAgentRun.mockResolvedValue({ name: "run", namespace: "ns", phase: "Running" });
    clientMock.watchAgentRun
      .mockImplementationOnce(() =>
        endingStream([{ name: "run", namespace: "ns", phase: "Running" }], new Error("disconnect")),
      )
      .mockImplementation(() => endingStream([], new Error("still down")));

    useAgentRun("ns", "run");
    await flushMicrotasks();

    expect(clientMock.watchAgentRun).toHaveBeenCalledTimes(1);

    // First retry: the received frame reset the attempt counter, delay < 1s.
    await vi.advanceTimersByTimeAsync(1000);
    await flushMicrotasks();
    expect(clientMock.watchAgentRun).toHaveBeenCalledTimes(2);

    // The reconnect failed without a frame, so the next delay grows to < 2s:
    // one more second is not enough, two are.
    await vi.advanceTimersByTimeAsync(1000);
    await flushMicrotasks();
    expect(clientMock.watchAgentRun).toHaveBeenCalledTimes(2);

    await vi.advanceTimersByTimeAsync(2000);
    await flushMicrotasks();
    expect(clientMock.watchAgentRun).toHaveBeenCalledTimes(3);
  });
});
