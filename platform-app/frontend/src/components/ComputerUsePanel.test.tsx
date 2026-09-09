import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { ComputerUsePanel } from "./ComputerUsePanel";
import type { DesktopScope, DesktopSession, DesktopRequest, WindowCapture } from "@/lib/computer-use";
import { getComputerUseApprovalMode, setComputerUseApprovalMode } from "@/lib/computer-use-preferences";

const m = vi.hoisted(() => ({
  isTauri: true, user: "user-1", getRun: vi.fn(), permissions: vi.fn(), windows: vi.fn(),
  start: vi.fn(), status: vi.fn(), heartbeat: vi.fn(), pause: vi.fn(), resume: vi.fn(),
  stop: vi.fn(), capture: vi.fn(), relay: vi.fn(), queue: vi.fn(), arm: vi.fn(), approve: vi.fn(), cancel: vi.fn(),
}));
vi.mock("@/contexts/AuthContext", () => ({ useOptionalAuth: () => ({ user: { id: m.user } }) }));
vi.mock("@/lib/platform", () => ({ get isTauri() { return m.isTauri; }, backendBaseUrl: () => "https://operator.example" }));
vi.mock("@/lib/client", () => ({ client: { getAgentRun: m.getRun } }));
vi.mock("@/lib/computer-use", () => ({
  computerUsePermissions: m.permissions, computerUseWindows: m.windows,
  startDesktopSession: m.start, desktopSessionStatus: m.status,
  heartbeatDesktopSession: m.heartbeat, pauseDesktopSession: m.pause,
  resumeDesktopSession: m.resume, stopDesktopSession: m.stop, captureDesktopWindow: m.capture,
  queueDesktopRequest: m.queue, armDesktopRequest: m.arm, approveDesktopRequest: m.approve, cancelDesktopRequest: m.cancel,
}));

vi.mock("@/lib/computer-use-relay", () => ({ exchangeDesktopRelay: m.relay }));

const target = { windowId: 42, processId: 99, application: "TextEdit", title: "Notes" };
const run = { namespace: "default", name: "run-1", myPermission: "owner", phase: "Running" };
const image: WindowCapture = {
  frameId: "frame-1",
  geometry: { x: 0, y: 0, width: 800, height: 600 }, pixelWidth: 1600, pixelHeight: 1200,
  dataUrl: "data:image/png;base64,cHJldmlldw==",
};
let native: DesktopSession;
let remotePending: DesktopRequest | null;

beforeEach(() => {
  vi.resetAllMocks();
  setComputerUseApprovalMode("manual");
  m.isTauri = true;
  m.user = "user-1";
  remotePending = null;
  m.relay.mockImplementation(async (_id: string, _scope: DesktopScope, operation: string) => {
    if (operation === "claim") remotePending = null;
    return { active: operation !== "stop", visionAvailable: true, pending: remotePending ?? undefined };
  });
  m.queue.mockResolvedValue(undefined);
  m.arm.mockResolvedValue({ permit: "permit-1" });
  m.approve.mockImplementation(async (_id: string, _scope: DesktopScope, requestId: string) => ({ requestId, status: "completed", message: "Completed" }));
  m.cancel.mockResolvedValue(undefined);
  native = { revision: 0, phase: "stopped", sessionId: null, scope: null, reason: "" };
  m.getRun.mockResolvedValue(run);
  m.permissions.mockResolvedValue({ supported: true, screenRecording: true, accessibility: true });
  m.windows.mockResolvedValue([target]);
  m.status.mockImplementation(async () => ({ ...native }));
  m.start.mockImplementation(async (scope: DesktopScope) => {
    native = { revision: 0, phase: "active", sessionId: "session-1", scope, reason: "" };
    return native;
  });
  m.heartbeat.mockResolvedValue(undefined);
  m.stop.mockImplementation(async () => {
    native = { revision: native.revision + 1, phase: "stopped", sessionId: null, scope: null, reason: "Stopped" };
  });
  m.pause.mockImplementation(async () => { native = { ...native, revision: native.revision + 1, phase: "paused" }; });
  m.resume.mockImplementation(async () => { native = { ...native, phase: "active" }; return native; });
  m.capture.mockResolvedValue(image);
});
afterEach(cleanup);

async function panel(enabled = true) {
  const view = render(<ComputerUsePanel namespace="default" name="run-1" enabled={enabled} model="test-model" />);
  await screen.findByRole("status", { name: "Session stopped" });
  view.container.querySelector("details")?.setAttribute("open", "");
  return view;
}

async function selectAndConsent() {
  fireEvent.click(screen.getByRole("button", { name: "List open windows" }));
  await screen.findByRole("option", { name: "TextEdit — Notes" });
  fireEvent.change(screen.getByRole("combobox", { name: "Approved window" }), { target: { value: "42" } });
  fireEvent.click(screen.getByRole("checkbox"));
}

async function startSession() {
  await selectAndConsent();
  fireEvent.click(screen.getByRole("button", { name: "Start supervised session" }));
  await screen.findByRole("status", { name: "Session active" });
  await waitFor(() => expect(m.heartbeat).toHaveBeenCalled());
}

describe("run-bound desktop preview", () => {
  it("requires window selection and explicit consent, without capturing automatically", async () => {
    await panel();
    expect((screen.getByRole("button", { name: "Start supervised session" }) as HTMLButtonElement).disabled).toBe(true);
    expect(m.windows).not.toHaveBeenCalled();
    expect(m.capture).not.toHaveBeenCalled();
    await startSession();
    expect(m.start).toHaveBeenCalledWith({
      backend: "https://operator.example", user: "user-1", namespace: "default", run: "run-1",
      application: "TextEdit", windowId: 42, processId: 99,
    }, true, 0);
    expect(m.capture).not.toHaveBeenCalled();
  });

  it("previews an approved window without uploading it", async () => {
    await panel();
    await startSession();
    fireEvent.click(screen.getByRole("button", { name: "Local preview" }));
    const preview = await screen.findByAltText("Preview of the approved desktop window");
    expect(preview.getAttribute("src")).toBe(image.dataUrl);
    expect(m.capture).toHaveBeenCalledWith("session-1", native.scope);
    expect(m.getRun).toHaveBeenCalledWith({ namespace: "default", name: "run-1" }, { timeoutMs: 4000 });
  });

  it("denies start after run ownership changes", async () => {
    await panel();
    await selectAndConsent();
    m.getRun.mockResolvedValue({ ...run, myPermission: "viewer" });
    fireEvent.click(screen.getByRole("button", { name: "Start supervised session" }));
    expect((await screen.findByRole("alert")).textContent).toContain("Only a run owner or admin");
    expect(m.start).not.toHaveBeenCalled();
  });

  it("denies start when the run has ended", async () => {
    await panel();
    await selectAndConsent();
    m.getRun.mockResolvedValue({ ...run, phase: "Succeeded" });
    fireEvent.click(screen.getByRole("button", { name: "Start supervised session" }));
    await screen.findByRole("alert");
    expect(m.start).not.toHaveBeenCalled();
  });

  it("stops rather than renewing the native lease after a backend failure", async () => {
    await panel();
    await selectAndConsent();
    m.relay.mockResolvedValueOnce({ active: true, visionAvailable: true }).mockRejectedValue(new Error("Disconnected"));
    fireEvent.click(screen.getByRole("button", { name: "Start supervised session" }));
    expect((await screen.findByRole("alert")).textContent).toContain("Disconnected");
    await waitFor(() => expect(m.stop).toHaveBeenCalled());
    // Exactly one renewal, after the backend confirmed attach; none after the failure.
    expect(m.heartbeat).toHaveBeenCalledTimes(1);
  });

  it("clears preview and requires fresh consent after stop", async () => {
    await panel();
    await startSession();
    fireEvent.click(screen.getByRole("button", { name: "Local preview" }));
    await screen.findByRole("img");
    fireEvent.click(screen.getByRole("button", { name: "Stop computer use" }));
    await screen.findByRole("status", { name: "Session stopped" });
    expect(screen.queryByRole("img")).toBeNull();
    expect((screen.getByRole("checkbox") as HTMLInputElement).checked).toBe(false);
  });

  it("does not display a late capture after stop", async () => {
    await panel();
    await startSession();
    let complete: (capture: WindowCapture) => void = () => {};
    m.capture.mockImplementation(() => new Promise<WindowCapture>((resolve) => { complete = resolve; }));
    fireEvent.click(screen.getByRole("button", { name: "Local preview" }));
    await waitFor(() => expect(m.capture).toHaveBeenCalled());
    fireEvent.click(screen.getByRole("button", { name: "Stop computer use" }));
    await screen.findByRole("status", { name: "Session stopped" });
    await act(async () => complete(image));
    expect(screen.queryByRole("img")).toBeNull();
  });

  it("clears previews while paused and requires explicit resume", async () => {
    await panel();
    await startSession();
    fireEvent.click(screen.getByRole("button", { name: "Local preview" }));
    await screen.findByRole("img");
    fireEvent.click(screen.getByRole("button", { name: "Pause" }));
    await screen.findByRole("status", { name: "Session paused" });
    expect(screen.queryByRole("img")).toBeNull();
    expect(m.resume).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: "Resume" }));
    await screen.findByRole("status", { name: "Session active" });
    expect(m.resume).toHaveBeenCalledWith("session-1", native.scope);
  });

  it("revokes authorization when leaving the run view", async () => {
    const view = await panel();
    await startSession();
    view.unmount();
    await waitFor(() => expect(m.stop).toHaveBeenCalled());
  });

  it("reports unconfirmed native stop rather than declaring the session stopped", async () => {
    await panel();
    await startSession();
    m.stop.mockRejectedValue(new Error("IPC failed"));
    fireEvent.click(screen.getByRole("button", { name: "Stop computer use" }));
    expect((await screen.findByRole("alert")).textContent).toContain("Cannot confirm native stop");
    expect(screen.queryByRole("status", { name: "Session stopped" })).toBeNull();
  });

  it("requires per-action consent, claims after native validation, and arms only for execution", async () => {
    remotePending = { requestId: "request-1", frameId: "frame-1", action: { kind: "type", text: "Draft text <not markup>" } };
    const events: string[] = [];
    m.queue.mockImplementation(async () => { events.push("queue"); });
    m.arm.mockImplementation(async () => { events.push("arm"); return { permit: "permit-1" }; });
    m.approve.mockImplementation(async () => { events.push("approve"); return { requestId: "request-1", status: "completed", message: "Completed" }; });
    m.relay.mockImplementation(async (_id: string, _scope: DesktopScope, operation: string) => {
      if (["claim", "resolve"].includes(operation)) events.push(operation);
      if (operation === "claim") remotePending = null;
      return { active: true, visionAvailable: true, pending: remotePending ?? undefined };
    });
    await panel();
    await startSession();
    await screen.findByText("Agent requests: type");
    expect(screen.getByLabelText("Proposed text").textContent).toBe("Draft text <not markup>");
    expect((screen.getByRole("button", { name: "Allow once" }) as HTMLButtonElement).disabled).toBe(true);
    expect(m.queue).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("checkbox", { name: /I reviewed/ }));
    fireEvent.click(screen.getByRole("button", { name: "Allow once" }));
    await screen.findByText("type — completed");
    expect(events).toEqual(["queue", "claim", "arm", "approve", "resolve"]);
    expect(m.approve).toHaveBeenCalledWith("session-1", native.scope, "request-1", "permit-1");
    expect(screen.getByRole("list", { name: "Recent computer actions" }).textContent).not.toContain("Draft text");
  });

  it("denies without queueing or executing native input", async () => {
    remotePending = { requestId: "request-1", frameId: "frame-1", action: { kind: "key", key: "Enter" } };
    await panel();
    await startSession();
    fireEvent.click(await screen.findByRole("button", { name: "Deny" }));
    await screen.findByText("key — denied");
    expect(m.queue).not.toHaveBeenCalled();
    expect(m.arm).not.toHaveBeenCalled();
    expect(m.approve).not.toHaveBeenCalled();
    expect(m.relay).toHaveBeenCalledWith("session-1", native.scope, "resolve", "request-1", {
      requestId: "request-1", status: "denied", message: "Denied by supervisor",
    });
  });

  it("discards a late backend claim after the supervisor stops", async () => {
    remotePending = { requestId: "request-1", frameId: "frame-1", action: { kind: "click", x: 5, y: 5 } };
    let complete: () => void = () => {};
    m.relay.mockImplementation(async (_id: string, _scope: DesktopScope, operation: string) => {
      if (operation === "claim") return new Promise((resolve) => { complete = () => resolve({ active: true, visionAvailable: true }); });
      return { active: operation !== "stop", visionAvailable: true, pending: remotePending };
    });
    await panel();
    await startSession();
    fireEvent.click(screen.getByRole("checkbox", { name: /I reviewed/ }));
    fireEvent.click(screen.getByRole("button", { name: "Allow once" }));
    await waitFor(() => expect(m.relay).toHaveBeenCalledWith("session-1", native.scope, "claim", "request-1"));
    fireEvent.click(screen.getByRole("button", { name: "Stop computer use" }));
    await screen.findByRole("status", { name: "Session stopped" });
    await act(async () => complete());
    expect(m.arm).not.toHaveBeenCalled();
    expect(m.approve).not.toHaveBeenCalled();
  });

  it("reports native validation failure without executing or automatically retrying", async () => {
    remotePending = { requestId: "request-1", frameId: "old-frame", action: { kind: "click", x: 5, y: 5 } };
    m.queue.mockRejectedValue(new Error("Frame expired"));
    await panel();
    await startSession();
    fireEvent.click(screen.getByRole("checkbox", { name: /I reviewed/ }));
    fireEvent.click(screen.getByRole("button", { name: "Allow once" }));
    await screen.findByText("click — failed");
    expect(m.approve).not.toHaveBeenCalled();
    expect(m.queue).toHaveBeenCalledTimes(1);
    expect(m.cancel).toHaveBeenCalledWith("session-1", native.scope, "request-1");
  });

  it("stops on an uncertain result delivery and does not retry the input", async () => {
    remotePending = { requestId: "request-1", frameId: "frame-1", action: { kind: "key", key: "Enter" } };
    m.relay.mockImplementation(async (_id: string, _scope: DesktopScope, operation: string) => {
      if (operation === "resolve") throw new Error("Disconnected");
      return { active: operation !== "stop", visionAvailable: true, pending: remotePending };
    });
    await panel();
    await startSession();
    fireEvent.click(screen.getByRole("checkbox", { name: /I reviewed/ }));
    fireEvent.click(screen.getByRole("button", { name: "Allow once" }));
    expect((await screen.findByRole("alert")).textContent).toContain("may already have happened");
    await screen.findByRole("status", { name: "Session stopped" });
    expect(m.approve).toHaveBeenCalledTimes(1);
  });

  it("sends a capture for vision only after human approval of observe", async () => {
    remotePending = { requestId: "request-1", action: { kind: "observe", question: "Describe the test document" } };
    m.approve.mockResolvedValue({ requestId: "request-1", status: "completed", message: "Captured", capture: image });
    await panel();
    await startSession();
    expect(m.approve).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("checkbox", { name: /I reviewed/ }));
    fireEvent.click(screen.getByRole("button", { name: "Allow once" }));
    await screen.findByText("observe — completed");
    expect(m.relay).toHaveBeenCalledWith("session-1", native.scope, "resolve", "request-1", expect.objectContaining({ capture: image }));
    expect(screen.getByRole("img").getAttribute("src")).toBe(image.dataUrl);
  });

  it("fails closed when the configured vision analyzer is unavailable", async () => {
    m.relay.mockResolvedValue({ active: true, visionAvailable: false });
    await panel();
    await selectAndConsent();
    fireEvent.click(screen.getByRole("button", { name: "Start supervised session" }));
    expect((await screen.findByRole("alert")).textContent).toContain("no supported vision analyzer");
    expect(m.heartbeat).not.toHaveBeenCalled();
    expect(m.stop).toHaveBeenCalled();
  });

  it("requires fresh confirmation for the next request", async () => {
    remotePending = { requestId: "request-1", frameId: "frame-1", action: { kind: "key", key: "Tab" } };
    await panel();
    await startSession();
    fireEvent.click(screen.getByRole("checkbox", { name: /I reviewed/ }));
    fireEvent.click(screen.getByRole("button", { name: "Allow once" }));
    await screen.findByText("key — completed");
    remotePending = { requestId: "request-2", frameId: "frame-1", action: { kind: "key", key: "Enter" } };
    await screen.findByText("Press Enter.", {}, { timeout: 3000 });
    expect((screen.getByRole("button", { name: "Allow once" }) as HTMLButtonElement).disabled).toBe(true);
    expect(m.approve).toHaveBeenCalledTimes(1);
  });

  it.each(["run", "user", "enabled", "model"])("clears scoped UI and consent when %s changes", async (change) => {
    remotePending = { requestId: "request-1", frameId: "frame-1", action: { kind: "key", key: "Enter" } };
    const view = await panel();
    await startSession();
    fireEvent.click(screen.getByRole("button", { name: "Local preview" }));
    await screen.findByRole("img");
    fireEvent.click(screen.getByRole("checkbox", { name: /I reviewed/ }));
    if (change === "user") m.user = "user-2";
    view.rerender(<ComputerUsePanel namespace="default" name={change === "run" ? "run-2" : "run-1"}
      enabled={change !== "enabled"} model={change === "model" ? "other-model" : "test-model"} />);
    if (change === "enabled") {
      // Losing ownership or finishing the run revokes the session and hides the controls.
      await waitFor(() => expect(m.stop).toHaveBeenCalled());
      await waitFor(() => expect(screen.queryByRole("status", { name: /^Session / })).toBeNull());
      expect(screen.queryByRole("img")).toBeNull();
      return;
    }
    await screen.findByRole("status", { name: "Session stopped" });
    expect(screen.queryByRole("img")).toBeNull();
    expect(screen.queryByRole("region", { name: "Action awaiting approval" })).toBeNull();
    expect((screen.getByRole("checkbox") as HTMLInputElement).checked).toBe(false);
    expect((screen.getByRole("combobox", { name: "Approved window" }) as HTMLSelectElement).value).toBe("");
    expect(m.stop).toHaveBeenCalled();
  });

  it("does not retry an uncertain claim or execute native input", async () => {
    remotePending = { requestId: "request-1", frameId: "frame-1", action: { kind: "key", key: "Enter" } };
    const relay = m.relay.getMockImplementation()!;
    m.relay.mockImplementation(async (...args) => {
      if (args[2] === "claim") throw new Error("Claim response lost");
      return relay(...args);
    });
    await panel();
    await startSession();
    fireEvent.click(screen.getByRole("checkbox", { name: /I reviewed/ }));
    fireEvent.click(screen.getByRole("button", { name: "Allow once" }));
    await screen.findByRole("status", { name: "Session stopped" });
    expect(m.relay.mock.calls.filter((call) => call[2] === "claim")).toHaveLength(1);
    expect(m.approve).not.toHaveBeenCalled();
  });

  it("ignores an old poll arriving after an action completes", async () => {
    remotePending = { requestId: "request-1", frameId: "frame-1", action: { kind: "key", key: "Enter" } };
    const staleRequest = remotePending;
    const relay = m.relay.getMockImplementation()!;
    let complete: () => void = () => {};
    m.relay.mockImplementation(async (...args) => {
      if (args[2] === "poll") return new Promise((resolve) => {
        complete = () => resolve({ active: true, visionAvailable: true, pending: staleRequest });
      });
      return relay(...args);
    });
    await panel();
    await selectAndConsent();
    fireEvent.click(screen.getByRole("button", { name: "Start supervised session" }));
    await screen.findByText("Press Enter.");
    await waitFor(() => expect(m.relay).toHaveBeenCalledWith("session-1", native.scope, "poll"));
    fireEvent.click(screen.getByRole("checkbox", { name: /I reviewed/ }));
    fireEvent.click(screen.getByRole("button", { name: "Allow once" }));
    await screen.findByText("key — completed");
    await act(async () => complete());
    expect(screen.queryByText("Press Enter.")).toBeNull();
    expect(m.approve).toHaveBeenCalledTimes(1);
  });

  it("ignores a previous stop completion after reconnecting", async () => {
    await panel();
    await startSession();
    let complete: () => void = () => {};
    m.stop.mockImplementationOnce(() => new Promise<void>((resolve) => { complete = resolve; }));
    fireEvent.click(screen.getByRole("button", { name: "Stop computer use" }));
    fireEvent.click(screen.getByRole("button", { name: "Stop computer use" }));
    await screen.findByRole("status", { name: "Session stopped" });
    await startSession();
    await act(async () => complete());
    expect(screen.getByRole("status", { name: "Session active" })).toBeDefined();
    expect(screen.getByRole("button", { name: "Local preview" }).hasAttribute("disabled")).toBe(false);
  });

  it("blocks reconnect when canceling a connection cannot confirm native stop", async () => {
    await panel();
    await selectAndConsent();
    let complete: () => void = () => {};
    m.relay.mockImplementationOnce(() => new Promise((resolve) => {
      complete = () => resolve({ active: true, visionAvailable: true });
    }));
    fireEvent.click(screen.getByRole("button", { name: "Start supervised session" }));
    await waitFor(() => expect(m.relay).toHaveBeenCalled());
    m.stop.mockRejectedValueOnce(new Error("IPC failed"));
    fireEvent.click(screen.getByRole("button", { name: "Cancel connection" }));
    expect((await screen.findByRole("alert")).textContent).toContain("Cannot confirm native stop");
    fireEvent.click(screen.getByRole("checkbox"));
    expect(screen.getByRole("button", { name: "Start supervised session" }).hasAttribute("disabled")).toBe(true);
    await act(async () => complete());
    expect(screen.queryByRole("status", { name: "Session active" })).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Retry native stop" }));
    await waitFor(() => expect(screen.queryByRole("button", { name: "Retry native stop" })).toBeNull());
  });

  it.each(["queue", "arm", "approve"] as const)("discards a late native %s after stop", async (stage) => {
    remotePending = { requestId: "request-1", action: { kind: "observe" } };
    let complete: () => void = () => {};
    m[stage].mockImplementationOnce(() => new Promise((resolve) => {
      complete = () => resolve(stage === "arm" ? { permit: "permit-1" } : stage === "approve"
        ? { requestId: "request-1", status: "completed", message: "Captured", capture: image } : undefined);
    }));
    await panel();
    await startSession();
    fireEvent.click(screen.getByRole("checkbox", { name: /I reviewed/ }));
    fireEvent.click(screen.getByRole("button", { name: "Allow once" }));
    await waitFor(() => expect(m[stage]).toHaveBeenCalled());
    fireEvent.click(screen.getByRole("button", { name: "Stop computer use" }));
    await screen.findByRole("status", { name: "Session stopped" });
    await act(async () => complete());
    expect(m.relay.mock.calls.filter((call) => call[2] === "resolve")).toHaveLength(0);
    expect(screen.queryByRole("img")).toBeNull();
    if (stage !== "approve") expect(m.approve).not.toHaveBeenCalled();
  });

  it("requires fresh confirmation if a pending request changes under the same ID", async () => {
    remotePending = { requestId: "request-1", frameId: "frame-1", action: { kind: "key", key: "Tab" } };
    await panel();
    await startSession();
    fireEvent.click(screen.getByRole("checkbox", { name: /I reviewed/ }));
    remotePending = { ...remotePending, action: { kind: "key", key: "Enter" } };
    await screen.findByText("Press Enter.", {}, { timeout: 3000 });
    expect((screen.getByRole("checkbox", { name: /I reviewed/ }) as HTMLInputElement).checked).toBe(false);
    expect(screen.getByRole("button", { name: "Allow once" }).hasAttribute("disabled")).toBe(true);
    expect(m.approve).not.toHaveBeenCalled();
  });

  it("discards a pending local capture after native pause is observed", async () => {
    await panel();
    await startSession();
    let complete: () => void = () => {};
    m.capture.mockImplementationOnce(() => new Promise((resolve) => { complete = () => resolve(image); }));
    fireEvent.click(screen.getByRole("button", { name: "Local preview" }));
    await waitFor(() => expect(m.capture).toHaveBeenCalled());
    native = { ...native, phase: "paused", revision: native.revision + 1 };
    await screen.findByRole("status", { name: "Session paused" }, { timeout: 3000 });
    await act(async () => complete());
    expect(screen.queryByRole("img")).toBeNull();
  });

  it("hides all controls on the web", () => {
    m.isTauri = false;
    const view = render(<ComputerUsePanel namespace="default" name="run-1" enabled model="test" />);
    expect(view.container.textContent).toBe("");
    expect(m.permissions).not.toHaveBeenCalled();
  });

  it("never issues a native stop or shows an emergency-stop alert without a session", async () => {
    m.permissions.mockResolvedValue({ supported: false, screenRecording: false, accessibility: false });
    m.stop.mockRejectedValue(new Error("Computer use requires the macOS desktop app"));
    const view = render(<ComputerUsePanel namespace="default" name="run-1" enabled model="test-model" />);
    await waitFor(() => expect(m.permissions).toHaveBeenCalled());
    view.rerender(<ComputerUsePanel namespace="default" name="run-1" enabled={false} model="test-model" />);
    view.rerender(<ComputerUsePanel namespace="default" name="run-1" enabled={false} model="other-model" />);
    await act(async () => {});
    expect(m.stop).not.toHaveBeenCalled();
    expect(screen.queryByRole("alert")).toBeNull();
    expect(view.container.textContent).toBe("");
  });

  it("hides controls from non-owners and finished runs without a session", async () => {
    const view = render(<ComputerUsePanel namespace="default" name="run-1" enabled={false} model="test-model" />);
    await waitFor(() => expect(m.permissions).toHaveBeenCalled());
    expect(view.container.textContent).toBe("");
  });

  it("revokes a native session that started after the panel was invalidated", async () => {
    await panel();
    await selectAndConsent();
    let complete: () => void = () => {};
    m.start.mockImplementationOnce((scope: DesktopScope) => new Promise((resolve) => {
      complete = () => {
        native = { revision: 0, phase: "active", sessionId: "session-1", scope, reason: "" };
        resolve(native);
      };
    }));
    fireEvent.click(screen.getByRole("button", { name: "Start supervised session" }));
    await waitFor(() => expect(m.start).toHaveBeenCalled());
    fireEvent.click(screen.getByRole("button", { name: "Cancel connection" }));
    await waitFor(() => expect(m.stop).toHaveBeenCalledTimes(1));
    await act(async () => complete());
    await waitFor(() => expect(m.stop).toHaveBeenCalledTimes(2));
    expect(m.relay).not.toHaveBeenCalledWith("session-1", expect.anything(), "attach");
    expect(screen.queryByRole("status", { name: "Session active" })).toBeNull();
  });

  it("reports an arming failure as failed without executing or stopping the session", async () => {
    remotePending = { requestId: "request-1", frameId: "frame-1", action: { kind: "key", key: "Enter" } };
    m.arm.mockRejectedValue(new Error("Frame is stale"));
    await panel();
    await startSession();
    fireEvent.click(screen.getByRole("checkbox", { name: /I reviewed/ }));
    fireEvent.click(screen.getByRole("button", { name: "Allow once" }));
    await screen.findByText("key — failed");
    expect(m.approve).not.toHaveBeenCalled();
    expect(m.cancel).toHaveBeenCalledWith("session-1", native.scope, "request-1");
    expect(m.relay).toHaveBeenCalledWith("session-1", native.scope, "resolve", "request-1", expect.objectContaining({ status: "failed" }));
    expect(m.stop).not.toHaveBeenCalled();
    expect(screen.getByRole("status", { name: "Session active" })).toBeTruthy();
  });

  it("does not forward a completed observation that has no capture", async () => {
    remotePending = { requestId: "request-1", action: { kind: "observe", question: "What is shown?" } };
    m.approve.mockResolvedValue({ requestId: "request-1", status: "completed", message: "Completed" });
    await panel();
    await startSession();
    fireEvent.click(screen.getByRole("checkbox", { name: /I reviewed/ }));
    fireEvent.click(screen.getByRole("button", { name: "Allow once" }));
    await screen.findByText("observe — failed");
    expect(m.relay).toHaveBeenCalledWith("session-1", native.scope, "resolve", "request-1", expect.objectContaining({ status: "failed" }));
    expect(m.stop).not.toHaveBeenCalled();
  });

  describe("approval modes", () => {
    it("skips approvals entirely in auto mode, still going through validation, claim, arm and permit", async () => {
      setComputerUseApprovalMode("auto");
      remotePending = { requestId: "request-1", frameId: "frame-1", action: { kind: "key", key: "Enter" } };
      const events: string[] = [];
      m.queue.mockImplementation(async () => { events.push("queue"); });
      m.arm.mockImplementation(async () => { events.push("arm"); return { permit: "permit-1" }; });
      m.approve.mockImplementation(async () => { events.push("approve"); return { requestId: "request-1", status: "completed", message: "Completed" }; });
      m.relay.mockImplementation(async (_id: string, _scope: DesktopScope, operation: string) => {
        if (["claim", "resolve"].includes(operation)) events.push(operation);
        if (operation === "claim") remotePending = null;
        return { active: true, visionAvailable: true, pending: remotePending ?? undefined };
      });
      await panel();
      expect(screen.getAllByText("Skip approvals").length).toBeGreaterThan(0);
      await startSession();
      await screen.findByText("key — completed");
      expect(events).toEqual(["queue", "claim", "arm", "approve", "resolve"]);
      expect(m.approve).toHaveBeenCalledWith("session-1", native.scope, "request-1", "permit-1");
      expect(screen.getByText("Pressed Enter")).toBeTruthy();
      expect(screen.queryByRole("checkbox", { name: /I reviewed/ })).toBeNull();
      expect(screen.queryByRole("button", { name: "Allow once" })).toBeNull();
    });

    it("assisted mode auto-approves read-only observations but asks before input", async () => {
      setComputerUseApprovalMode("assisted");
      remotePending = { requestId: "request-1", action: { kind: "observe", question: "What is shown?" } };
      m.approve.mockResolvedValue({ requestId: "request-1", status: "completed", message: "Completed", capture: image });
      await panel();
      await startSession();
      await screen.findByText("observe — completed");
      expect(m.approve).toHaveBeenCalledTimes(1);
      remotePending = { requestId: "request-2", frameId: "frame-1", action: { kind: "type", text: "hello" } };
      await screen.findByText("Agent requests: type", {}, { timeout: 3000 });
      await act(async () => { await new Promise((resolve) => setTimeout(resolve, 50)); });
      expect(m.approve).toHaveBeenCalledTimes(1);
      expect(screen.getByRole("button", { name: "Allow once" }).hasAttribute("disabled")).toBe(true);
      expect(screen.getByText("Enters input")).toBeTruthy();
    });

    it("does not auto-approve while paused, and resumes approving after resume", async () => {
      setComputerUseApprovalMode("auto");
      await panel();
      await startSession();
      fireEvent.click(screen.getByRole("button", { name: "Pause" }));
      await screen.findByRole("status", { name: "Session paused" });
      remotePending = { requestId: "request-1", frameId: "frame-1", action: { kind: "key", key: "Enter" } };
      await screen.findByText("Agent requests: key", {}, { timeout: 3000 });
      await act(async () => { await new Promise((resolve) => setTimeout(resolve, 50)); });
      expect(m.queue).not.toHaveBeenCalled();
      expect(m.approve).not.toHaveBeenCalled();
      fireEvent.click(screen.getByRole("button", { name: "Resume" }));
      await screen.findByText("key — completed", {}, { timeout: 3000 });
      expect(m.approve).toHaveBeenCalledTimes(1);
    });

    it("switching back to manual from the panel restores per-action confirmation", async () => {
      setComputerUseApprovalMode("auto");
      await panel();
      await startSession();
      fireEvent.click(screen.getByRole("button", { name: "Switch to manual" }));
      remotePending = { requestId: "request-1", frameId: "frame-1", action: { kind: "key", key: "Enter" } };
      await screen.findByText("Agent requests: key", {}, { timeout: 3000 });
      expect(screen.getByRole("button", { name: "Allow once" }).hasAttribute("disabled")).toBe(true);
      expect(screen.getByRole("checkbox", { name: /I reviewed/ })).toBeTruthy();
      expect(m.approve).not.toHaveBeenCalled();
    });

    it("stepping up to skip-all from the panel requires confirmation", async () => {
      await panel();
      await startSession();
      fireEvent.change(screen.getByRole("combobox", { name: "Approval mode" }), { target: { value: "auto" } });
      await screen.findByRole("dialog");
      expect(getComputerUseApprovalMode()).toBe("manual");
      fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
      await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
      expect(getComputerUseApprovalMode()).toBe("manual");
      expect(screen.getByRole("status", { name: "Session active" })).toBeTruthy();
      expect(m.stop).not.toHaveBeenCalled();
    });

    it("never auto-approves in manual mode", async () => {
      remotePending = { requestId: "request-1", frameId: "frame-1", action: { kind: "observe" } };
      await panel();
      await startSession();
      await screen.findByText("Agent requests: observe");
      await act(async () => { await new Promise((resolve) => setTimeout(resolve, 50)); });
      expect(m.queue).not.toHaveBeenCalled();
      expect(m.approve).not.toHaveBeenCalled();
    });
  });

  describe("session allowances", () => {
    it("allows a kind for the rest of the session after one reviewed approval, and resets on stop", async () => {
      remotePending = { requestId: "request-1", frameId: "frame-1", action: { kind: "key", key: "Tab" } };
      await panel();
      await startSession();
      fireEvent.click(await screen.findByRole("checkbox", { name: /I reviewed/ }));
      fireEvent.click(screen.getByRole("button", { name: "Allow for this session" }));
      await screen.findByText("key — completed");
      expect(screen.getByText("Allowed for this session:")).toBeTruthy();
      remotePending = { requestId: "request-2", frameId: "frame-1", action: { kind: "key", key: "Enter" } };
      await screen.findByText("Pressed Enter", {}, { timeout: 3000 });
      expect(m.approve).toHaveBeenCalledTimes(2);
      // A different kind still asks.
      remotePending = { requestId: "request-3", frameId: "frame-1", action: { kind: "click", x: 1, y: 1 } };
      await screen.findByText("Agent requests: click", {}, { timeout: 3000 });
      await act(async () => { await new Promise((resolve) => setTimeout(resolve, 50)); });
      expect(m.approve).toHaveBeenCalledTimes(2);
      fireEvent.click(screen.getByRole("button", { name: "Stop computer use" }));
      await screen.findByRole("status", { name: "Session stopped" });
      expect(screen.queryByText("Allowed for this session:")).toBeNull();
    });

    it("can be reset mid-session", async () => {
      remotePending = { requestId: "request-1", frameId: "frame-1", action: { kind: "key", key: "Tab" } };
      await panel();
      await startSession();
      fireEvent.click(await screen.findByRole("checkbox", { name: /I reviewed/ }));
      fireEvent.click(screen.getByRole("button", { name: "Allow for this session" }));
      await screen.findByText("key — completed");
      fireEvent.click(screen.getByRole("button", { name: "Reset" }));
      remotePending = { requestId: "request-2", frameId: "frame-1", action: { kind: "key", key: "Enter" } };
      await screen.findByText("Agent requests: key", {}, { timeout: 3000 });
      await act(async () => { await new Promise((resolve) => setTimeout(resolve, 50)); });
      expect(m.approve).toHaveBeenCalledTimes(1);
    });
  });
});
