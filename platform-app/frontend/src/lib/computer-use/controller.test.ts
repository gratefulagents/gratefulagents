import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { NativeSession } from "./native";
import type { RelayRequest, RelayResponse, RelayResult } from "./relay";

const mocks = vi.hoisted(() => ({
  executeAction: vi.fn(),
  onNativeSession: vi.fn(),
  setNativePaused: vi.fn(),
  startNativeSession: vi.fn(),
  stopNativeSession: vi.fn(),
  exchange: vi.fn(),
}));

vi.mock("@/lib/client", () => ({ client: {} }));
vi.mock("./native", () => ({
  executeAction: mocks.executeAction,
  onNativeSession: mocks.onNativeSession,
  setNativePaused: mocks.setNativePaused,
  startNativeSession: mocks.startNativeSession,
  stopNativeSession: mocks.stopNativeSession,
}));
vi.mock("./relay", async (importOriginal) => ({ ...(await importOriginal<typeof import("./relay")>()), exchange: mocks.exchange }));

type Controller = typeof import("./controller");
let controller: Controller;

const run = { namespace: "ns", name: "run" };
const active: RelayResponse = { protocol: 2, active: true, available: true, reason: "" };
const shot = { mediaType: "image/jpeg", data: "QUJD", width: 1183, height: 768 };
const click: RelayRequest = { id: "r1", action: { action: "left_click", coordinate: [512, 300] } };

let queue: RelayResponse[];
let waiting: ((response: RelayResponse) => void) | null;
let results: RelayResult[];
let nativeHandler: (session: NativeSession) => void;

function pushNext(response: RelayResponse) {
  if (waiting) {
    const resolve = waiting;
    waiting = null;
    resolve(response);
  } else {
    queue.push(response);
  }
}

const operations = () => mocks.exchange.mock.calls.map((call) => call[2]);

beforeEach(async () => {
  vi.resetModules();
  vi.clearAllMocks();
  localStorage.clear();
  queue = [];
  waiting = null;
  results = [];
  mocks.onNativeSession.mockImplementation(async (handler: (session: NativeSession) => void) => {
    nativeHandler = handler;
    return () => {};
  });
  mocks.startNativeSession.mockResolvedValue({ active: true, paused: false, displayId: 1 });
  mocks.stopNativeSession.mockResolvedValue(undefined);
  mocks.setNativePaused.mockImplementation(async (paused: boolean) => ({ active: true, paused }));
  mocks.executeAction.mockResolvedValue({ screenshot: shot, cursor: { x: 1, y: 2 } });
  mocks.exchange.mockImplementation(async (_run, _session, operation: string, options?: { result?: RelayResult; signal?: AbortSignal }) => {
    switch (operation) {
      case "connect": return active;
      case "result":
        results.push(options!.result!);
        return active;
      case "disconnect": return { ...active, active: false };
      default:
        if (queue.length) return queue.shift()!;
        return new Promise((resolve, reject) => {
          waiting = resolve;
          options?.signal?.addEventListener("abort", () => reject(new Error("aborted")));
        });
    }
  });
  controller = await import("./controller");
});

afterEach(async () => {
  await controller.stopComputerUse();
  vi.useRealTimers();
});

describe("computer use controller", () => {
  it("connects, executes the next request and posts the result", async () => {
    pushNext({ ...active, request: click });
    await controller.startComputerUse(run, 1);
    expect(mocks.startNativeSession).toHaveBeenCalledWith(1, "ns/run");
    await vi.waitFor(() => expect(results).toHaveLength(1));
    expect(operations().slice(0, 3)).toEqual(["connect", "next", "result"]);
    expect(mocks.executeAction).toHaveBeenCalledWith(click.action);
    expect(results[0]).toEqual({ requestId: "r1", ok: true, screenshot: shot, cursor: { x: 1, y: 2 } });
    const state = controller.getComputerUseState();
    expect(state.phase).toBe("active");
    expect(state.timeline).toMatchObject([{ id: "r1", status: "done", thumbnail: "data:image/jpeg;base64,QUJD" }]);
    expect(state.latest).toMatchObject({ dataUrl: "data:image/jpeg;base64,QUJD", width: 1183, height: 768 });
    expect(mocks.exchange.mock.calls[0][1]).toBe(state.sessionId);
  });

  it("reports native failures as failed results", async () => {
    mocks.executeAction.mockRejectedValue("Accessibility permission is missing");
    pushNext({ ...active, request: click });
    await controller.startComputerUse(run, 1);
    await vi.waitFor(() => expect(results).toHaveLength(1));
    expect(results[0]).toEqual({ requestId: "r1", ok: false, error: "Accessibility permission is missing" });
    expect(controller.getComputerUseState().timeline[0]).toMatchObject({ status: "failed", error: "Accessibility permission is missing" });
  });

  it("asks before input in ask mode and runs observations without asking", async () => {
    localStorage.setItem("computer-use-approval-mode-v2", "ask");
    pushNext({ ...active, request: { id: "s1", action: { action: "screenshot" } } });
    await controller.startComputerUse(run, 1);
    await vi.waitFor(() => expect(results).toHaveLength(1));
    pushNext({ ...active, request: click });
    await vi.waitFor(() => expect(controller.getComputerUseState().pending).toEqual({ request: click, needsApproval: true }));
    expect(mocks.executeAction).toHaveBeenCalledTimes(1);
    controller.decideComputerUse(true);
    await vi.waitFor(() => expect(results).toHaveLength(2));
    expect(mocks.executeAction).toHaveBeenLastCalledWith(click.action);
    expect(results[1].ok).toBe(true);
  });

  it("posts a denial without executing", async () => {
    localStorage.setItem("computer-use-approval-mode-v2", "ask");
    pushNext({ ...active, request: click });
    await controller.startComputerUse(run, 1);
    await vi.waitFor(() => expect(controller.getComputerUseState().pending?.needsApproval).toBe(true));
    controller.decideComputerUse(false);
    await vi.waitFor(() => expect(results).toHaveLength(1));
    expect(results[0]).toMatchObject({ requestId: "r1", ok: false, denied: true });
    expect(results[0].error).toBeTruthy();
    expect(mocks.executeAction).not.toHaveBeenCalled();
    expect(controller.getComputerUseState().timeline[0].status).toBe("denied");
  });

  it("approves a waiting request when switched to autonomous", async () => {
    localStorage.setItem("computer-use-approval-mode-v2", "ask");
    pushNext({ ...active, request: click });
    await controller.startComputerUse(run, 1);
    await vi.waitFor(() => expect(controller.getComputerUseState().pending?.needsApproval).toBe(true));
    controller.setSessionApprovalMode("auto");
    await vi.waitFor(() => expect(results).toHaveLength(1));
    expect(results[0].ok).toBe(true);
  });

  it("holds requests while paused", async () => {
    await controller.startComputerUse(run, 1);
    await controller.pauseComputerUse();
    expect(controller.getComputerUseState().phase).toBe("paused");
    await vi.waitFor(() => expect(waiting).not.toBeNull());
    pushNext({ ...active, request: click });
    await vi.waitFor(() => expect(controller.getComputerUseState().pending?.request).toEqual(click));
    await new Promise((resolve) => setTimeout(resolve, 20));
    expect(mocks.executeAction).not.toHaveBeenCalled();
    await controller.resumeComputerUse();
    await vi.waitFor(() => expect(results).toHaveLength(1));
    expect(mocks.setNativePaused.mock.calls).toEqual([[true], [false]]);
  });

  it("ends the session when the native side stops", async () => {
    await controller.startComputerUse(run, 1);
    nativeHandler({ active: false, paused: false, stoppedReason: "Emergency stop" });
    await vi.waitFor(() => expect(controller.getComputerUseState().phase).toBe("idle"));
    expect(controller.getComputerUseState().stoppedReason).toBe("Emergency stop");
    expect(operations()).toContain("disconnect");
    expect(mocks.stopNativeSession).not.toHaveBeenCalled();
  });

  it("stops when the relay reports the session inactive", async () => {
    pushNext({ ...active, active: false, reason: "run finished" });
    await controller.startComputerUse(run, 1);
    await vi.waitFor(() => expect(controller.getComputerUseState().phase).toBe("idle"));
    expect(controller.getComputerUseState().stoppedReason).toBe("run finished");
    expect(mocks.stopNativeSession).toHaveBeenCalled();
  });

  it("refuses a run that cannot use the tool", async () => {
    mocks.exchange.mockImplementation(async (_r, _s, operation: string) =>
      operation === "connect" ? { ...active, active: false, available: false } : active);
    await controller.startComputerUse(run, 1);
    const state = controller.getComputerUseState();
    expect(state).toMatchObject({ phase: "error", available: false });
    expect(state.error).toMatch(/can't use the computer/);
    expect(mocks.stopNativeSession).toHaveBeenCalled();
  });

  it("stops the first session when another run starts", async () => {
    await controller.startComputerUse(run, 1);
    const first = controller.getComputerUseState().sessionId;
    await controller.startComputerUse({ namespace: "ns", name: "other" }, 2);
    const disconnect = mocks.exchange.mock.calls.find((call) => call[2] === "disconnect")!;
    expect(disconnect[0]).toEqual(run);
    expect(disconnect[1]).toBe(first);
    expect(mocks.stopNativeSession).toHaveBeenCalledTimes(1);
    expect(controller.getComputerUseState()).toMatchObject({ phase: "active", run: { name: "other" }, displayId: 2 });
  });

  it("retries RPC failures with exponential backoff, then gives up", async () => {
    vi.useFakeTimers();
    let nexts = 0;
    mocks.exchange.mockImplementation(async (_r, _s, operation: string) => {
      if (operation !== "next") return active;
      nexts += 1;
      throw new Error("upstream unavailable");
    });
    await controller.startComputerUse(run, 1);
    await vi.advanceTimersByTimeAsync(0);
    expect(nexts).toBe(1);
    await vi.advanceTimersByTimeAsync(250);
    expect(nexts).toBe(2);
    await vi.advanceTimersByTimeAsync(500);
    expect(nexts).toBe(3);
    await vi.advanceTimersByTimeAsync(1000);
    expect(nexts).toBe(4);
    expect(controller.getComputerUseState().phase).toBe("active");
    await vi.advanceTimersByTimeAsync(46_000);
    expect(nexts).toBeLessThan(16);
    expect(controller.getComputerUseState().phase).toBe("error");
    expect(controller.getComputerUseState().error).toMatch(/Lost connection to the run: upstream unavailable/);
  });

  it("fails fast on a protocol mismatch", async () => {
    const { RelayProtocolError } = await import("./relay");
    mocks.exchange.mockImplementation(async (_r, _s, operation: string) => {
      if (operation === "next") throw new RelayProtocolError("Update the desktop app and recreate the run.");
      return active;
    });
    await controller.startComputerUse(run, 1);
    await vi.waitFor(() => expect(controller.getComputerUseState().phase).toBe("error"));
    expect(controller.getComputerUseState().error).toMatch(/recreate the run/);
  });
});
