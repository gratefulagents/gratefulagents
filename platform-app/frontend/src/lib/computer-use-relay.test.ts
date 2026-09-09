import { beforeEach, describe, expect, it, vi } from "vitest";
import { exchangeDesktopRelay, parseDesktopRelay } from "./computer-use-relay";

const m = vi.hoisted(() => ({ exchange: vi.fn(), backend: "https://operator.example" }));
vi.mock("./client", () => ({ client: { exchangeComputerUse: m.exchange } }));
vi.mock("./platform", () => ({ backendBaseUrl: () => m.backend }));
const scope = { backend: "https://operator.example", user: "local-user", namespace: "default", run: "run-1", application: "TextEdit", windowId: 42, processId: 99 };
const status = { active: true, visionAvailable: true };
const wrap = (action: unknown, extra = {}) => JSON.stringify({ ...status, pending: { requestId: "request-1", frameId: "frame-1", action, ...extra } });

beforeEach(() => {
  vi.resetAllMocks();
  m.backend = scope.backend;
  m.exchange.mockResolvedValue({ responseJson: JSON.stringify(status) });
});

describe("computer use relay contract", () => {
  it("sends run/session binding with a bounded RPC timeout, never a caller-supplied owner", async () => {
    expect(await exchangeDesktopRelay("session-1", scope, "claim", "request-1")).toEqual(status);
    expect(m.exchange).toHaveBeenCalledWith({ namespace: "default", name: "run-1", sessionId: "session-1", operation: "claim", requestId: "request-1", outcomeJson: "" }, { timeoutMs: 6000 });
    await exchangeDesktopRelay("session-1", scope, "poll");
    expect(m.exchange).toHaveBeenLastCalledWith(expect.objectContaining({ operation: "poll" }), { timeoutMs: 4000 });
  });

  it("gives resolve a longer bounded timeout so a PNG capture can be delivered", async () => {
    const outcome = { requestId: "request-1", status: "completed" as const, message: "ok" };
    await exchangeDesktopRelay("session-1", scope, "resolve", "request-1", outcome);
    expect(m.exchange).toHaveBeenCalledWith(expect.objectContaining({ operation: "resolve", outcomeJson: JSON.stringify(outcome) }), { timeoutMs: 20_000 });
  });

  it("rejects a backend switch before sending any session data", async () => {
    m.backend = "https://different.example";
    await expect(exchangeDesktopRelay("s", scope, "poll")).rejects.toThrow("backend changed");
    expect(m.exchange).not.toHaveBeenCalled();
  });

  it.each([
    { kind: "observe", question: "Describe the test document" },
    { kind: "click", x: 0, y: 10 },
    { kind: "scroll", deltaX: 0, deltaY: 100 },
    { kind: "type", text: "Proposed text 😀" },
    { kind: "type", text: "family \u{1F468}\u200d\u{1F469}\u200d\u{1F467} and a\u200cb" },
    { kind: "type", text: "non-breaking\u00a0space" },
    { kind: "key", key: "Shift+Tab" },
    { kind: "activate" },
  ])("accepts the native action shape $kind", (action) => {
    expect(parseDesktopRelay(wrap(action)).pending?.action).toEqual(action);
  });

  it.each([
    { kind: "shell", command: "anything" },
    { kind: ["observe"] },
    { kind: "__proto__" },
    { kind: "constructor" },
    { kind: "observe", command: "anything" },
    { kind: "click", x: -1, y: 0 },
    { kind: "click", x: "1", y: 0 },
    { kind: "scroll", deltaX: 0.5, deltaY: 0 },
    { kind: "scroll", deltaX: 0, deltaY: 0 },
    { kind: "scroll", deltaY: 5 },
    { kind: "type", text: "" },
    { kind: "type", text: "x".repeat(1001) },
    { kind: "type", text: "not\na single input" },
    { kind: "type", text: "zero\u200bwidth" },
    { kind: "type", text: "\ufeffbom" },
    { kind: "type", text: "abc\u202efed" },
    { kind: "type", text: "a\u2066b\u2069" },
    { kind: "type", text: "line\u2028separator" },
    { kind: "type", text: "soft\u00adhyphen" },
    { kind: "type", text: "private\ue000use" },
    { kind: "type", text: "unassigned\u{E0080}" },
    { kind: "key", key: "Command+V" },
  ])("rejects malformed or unsupported action %#", (action) => {
    expect(() => parseDesktopRelay(wrap(action))).toThrow();
  });

  it("rejects non-observation actions without a frame and malformed IDs", () => {
    expect(() => parseDesktopRelay(wrap({ kind: "key", key: "Enter" }, { frameId: undefined }))).toThrow(/frame/);
    expect(() => parseDesktopRelay(wrap({ kind: "observe" }, { requestId: "../other" }))).toThrow(/binding/);
    expect(parseDesktopRelay(wrap({ kind: "observe" }, { frameId: undefined })).pending).toBeDefined();
  });

  it.each(["{}", "null", "[]", "not json", JSON.stringify({ ...status, active: "yes" }), "x".repeat(16385)])("rejects malformed relay envelopes", (raw) => {
    expect(() => parseDesktopRelay(raw)).toThrow();
  });

  it.each([false, 0, ""])("rejects malformed pending value %s", (pending) => {
    expect(() => parseDesktopRelay(JSON.stringify({ ...status, pending }))).toThrow();
  });

  it("rejects responses from a backend that changed during the exchange", async () => {
    m.exchange.mockImplementation(async () => {
      m.backend = "https://different.example";
      return { responseJson: JSON.stringify(status) };
    });
    await expect(exchangeDesktopRelay("s", scope, "poll")).rejects.toThrow("backend changed");
  });

  it("propagates claim errors instead of authorizing execution", async () => {
    m.exchange.mockRejectedValue(new Error("Claim rejected"));
    await expect(exchangeDesktopRelay("s", scope, "claim", "r")).rejects.toThrow("Claim rejected");
  });
});
