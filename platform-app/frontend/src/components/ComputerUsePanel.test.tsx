import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { ComputerUsePanel } from "./ComputerUsePanel";
import type { DesktopScope, DesktopSession, DesktopRequest, WindowCapture } from "@/lib/computer-use";
import { getComputerUseApprovalMode, setComputerUseApprovalMode } from "@/lib/computer-use-preferences";

const m = vi.hoisted(() => ({
  isTauri: true, user: "user-1", getRun: vi.fn(), permissions: vi.fn(), openPermission: vi.fn(), pick: vi.fn(),
  start: vi.fn(), status: vi.fn(), heartbeat: vi.fn(), pause: vi.fn(), resume: vi.fn(),
  stop: vi.fn(), capture: vi.fn(), relay: vi.fn(), queue: vi.fn(), arm: vi.fn(), approve: vi.fn(), cancel: vi.fn(),
}));
vi.mock("@/contexts/AuthContext", () => ({ useOptionalAuth: () => ({ user: { id: m.user } }) }));
vi.mock("@/lib/platform", () => ({ get isTauri() { return m.isTauri; }, backendBaseUrl: () => "https://operator.example" }));
vi.mock("@/lib/client", () => ({ client: { getAgentRun: m.getRun } }));
vi.mock("@/lib/computer-use", async (importOriginal) => ({
  hotkeyGlyphs: (await importOriginal<typeof import("@/lib/computer-use")>()).hotkeyGlyphs,
  computerUsePermissions: m.permissions, openComputerUsePermission: m.openPermission, pickComputerUseWindow: m.pick,
  startDesktopSession: m.start, desktopSessionStatus: m.status,
  heartbeatDesktopSession: m.heartbeat, pauseDesktopSession: m.pause,
  resumeDesktopSession: m.resume, stopDesktopSession: m.stop, captureDesktopWindow: m.capture,
  queueDesktopRequest: m.queue, armDesktopRequest: m.arm, approveDesktopRequest: m.approve, cancelDesktopRequest: m.cancel,
}));

vi.mock("@/lib/computer-use-relay", () => ({ exchangeDesktopRelay: m.relay }));

const target = { selectionId: "selection-1", windowId: 42, processId: 99, application: "TextEdit", title: "Notes" };
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
  m.permissions.mockResolvedValue({ supported: true, accessibility: true });
  m.pick.mockResolvedValue(target);
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
  const picker = screen.getByRole("button", { name: "Choose window with macOS" });
  await waitFor(() => expect((picker as HTMLButtonElement).disabled).toBe(false));
  fireEvent.click(picker);
  await screen.findByText("TextEdit — Notes");
  fireEvent.click(screen.getByRole("checkbox"));
}

async function startSession() {
  await selectAndConsent();
  fireEvent.click(screen.getByRole("button", { name: "Start supervised session" }));
  await screen.findByRole("status", { name: "Session active" });
  await waitFor(() => expect(m.heartbeat).toHaveBeenCalled());
}

describe("run-bound desktop preview", () => {
  it("requests broad OS permission only in agent-choice mode after explicit consent", async () => {
    await panel();
    expect(screen.queryByRole("button", { name: "Enable Screen Recording for agent choice" })).toBeNull();
    fireEvent.change(screen.getByRole("combobox", { name: "Window access" }), { target: { value: "agent_choice" } });
    const permission = screen.getByRole("button", { name: "Enable Screen Recording for agent choice" });
    expect(permission.hasAttribute("disabled")).toBe(true);
    expect(m.openPermission).not.toHaveBeenCalled();
    expect(m.pick).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("checkbox"));
    fireEvent.click(permission);
    await waitFor(() => expect(m.openPermission).toHaveBeenCalledWith("agent_screen_recording"));
    expect(m.start).not.toHaveBeenCalled();
    expect(m.capture).not.toHaveBeenCalled();
  });

  it("requires fresh consent for agent choice and connects without the picker", async () => {
    await panel();
    await selectAndConsent();
    fireEvent.change(screen.getByRole("combobox", { name: "Window access" }), { target: { value: "agent_choice" } });
    expect((screen.getByRole("checkbox") as HTMLInputElement).checked).toBe(false);
    expect(screen.queryByRole("combobox", { name: "Approved window" })).toBeNull();
    expect(screen.getByRole("button", { name: "Start supervised session" }).hasAttribute("disabled")).toBe(true);
    m.start.mockImplementation(async (scope: DesktopScope) => {
      native = { revision: 0, targetRevision: 0, target: null, phase: "active", sessionId: "session-1", scope, reason: "" };
      return native;
    });
    fireEvent.click(screen.getByRole("checkbox"));
    fireEvent.click(screen.getByRole("button", { name: "Start supervised session" }));
    await screen.findByRole("status", { name: "Session active" });
    expect(m.start).toHaveBeenCalledWith(expect.objectContaining({ mode: "agent_choice", application: "", windowId: 0, processId: 0 }), true, 0, "");
    expect(screen.getAllByText("Awaiting agent selection").length).toBeGreaterThan(0);
    expect(screen.getByRole("button", { name: "Local preview" }).hasAttribute("disabled")).toBe(true);
  });

  it.each([false, true])("selects an approved target and rejects stale delivery: stale=%s", async (stale) => {
    const chosen = { ref: "a".repeat(64), application: "Test application", title: "Harmless test document" };
    remotePending = { requestId: "listing", targetRevision: 0, action: { kind: "list_windows" } };
    m.start.mockImplementation(async (scope: DesktopScope) => {
      native = { revision: 0, targetRevision: 0, target: null, phase: "active", sessionId: "session-1", scope, reason: "" };
      return native;
    });
    m.approve.mockImplementation(async (_id: string, _scope: DesktopScope, requestId: string) => {
      if (requestId === "listing") return { requestId, status: "completed", message: "Listed", windows: [chosen] };
      native = { ...native, target: chosen, targetRevision: stale ? 2 : 1, revision: stale ? 2 : 1 };
      return { requestId, status: "completed", message: "Selected", target: chosen, targetRevision: 1 };
    });
    await panel();
    fireEvent.change(screen.getByRole("combobox", { name: "Window access" }), { target: { value: "agent_choice" } });
    fireEvent.click(screen.getByRole("checkbox"));
    fireEvent.click(screen.getByRole("button", { name: "Start supervised session" }));
    await screen.findByRole("status", { name: "Session active" });
    fireEvent.click(await screen.findByRole("checkbox", { name: /I reviewed/ }));
    fireEvent.click(screen.getByRole("button", { name: "Allow for this session" }));
    await waitFor(() => expect(m.relay.mock.calls.some((call) => call[2] === "resolve" && call[3] === "listing")).toBe(true));
    remotePending = { requestId: "selecting", targetRevision: 0, action: { kind: "select_window", targetRef: chosen.ref } };
    await screen.findByText(/Switch target to Test application/, {}, { timeout: 3000 });
    fireEvent.click(screen.getByRole("checkbox", { name: /I reviewed/ }));
    fireEvent.click(screen.getByRole("button", { name: "Allow once" }));
    if (stale) {
      await waitFor(() => expect(m.stop).toHaveBeenCalled());
      expect(m.relay.mock.calls.some((call) => call[2] === "resolve" && call[3] === "selecting")).toBe(false);
    } else {
      await waitFor(() => expect(m.relay.mock.calls.some((call) => call[2] === "resolve" && call[3] === "selecting")).toBe(true));
      expect(screen.getAllByText("Test application — Harmless test document").length).toBeGreaterThan(0);
      expect(screen.queryByText("Allowed for this session:")).toBeNull();
      expect(screen.queryByRole("img")).toBeNull();
    }
  });

  it("fails closed when old native start does not acknowledge agent mode", async () => {
    await panel();
    fireEvent.change(screen.getByRole("combobox", { name: "Window access" }), { target: { value: "agent_choice" } });
    fireEvent.click(screen.getByRole("checkbox"));
    fireEvent.click(screen.getByRole("button", { name: "Start supervised session" }));
    await waitFor(() => expect(m.stop).toHaveBeenCalled());
    expect(m.relay.mock.calls.some((call) => call[2] === "attach")).toBe(false);
  });
  it("uses only the native single-window picker and requires new consent on reselection", async () => {
    await panel();
    await selectAndConsent();
    expect(m.pick).toHaveBeenCalledWith(0);
    expect(screen.queryByRole("combobox", { name: "Approved window" })).toBeNull();
    expect(screen.queryByRole("combobox", { name: "Application" })).toBeNull();
    expect(screen.queryByRole("searchbox")).toBeNull();
    m.pick.mockResolvedValue({ ...target, selectionId: "selection-2", windowId: 43, application: "Firefox", title: "Google" });
    fireEvent.click(screen.getByRole("button", { name: "Choose window with macOS" }));
    await screen.findByText("Firefox — Google");
    expect((screen.getByRole("checkbox") as HTMLInputElement).checked).toBe(false);
    expect(m.start).not.toHaveBeenCalled();
    expect(m.capture).not.toHaveBeenCalled();
  });

  it("handles picker cancellation without an error, selection, or implicit consent", async () => {
    await panel();
    await selectAndConsent();
    m.pick.mockResolvedValue(null);
    fireEvent.click(screen.getByRole("button", { name: "Choose window with macOS" }));
    await waitFor(() => expect(m.pick).toHaveBeenCalledTimes(2));
    expect(screen.getByText("No window selected")).toBeTruthy();
    expect((screen.getByRole("checkbox") as HTMLInputElement).checked).toBe(false);
    expect(screen.queryByRole("alert")).toBeNull();
    expect(screen.getByRole("button", { name: "Start supervised session" }).hasAttribute("disabled")).toBe(true);
  });

  it("revokes a pending picker on navigation and ignores its late selection", async () => {
    let resolve!: (value: typeof target) => void;
    m.pick.mockImplementation(() => new Promise((done) => { resolve = done; }));
    const view = await panel();
    fireEvent.click(screen.getByRole("button", { name: "Choose window with macOS" }));
    await waitFor(() => expect(m.pick).toHaveBeenCalled());
    view.rerender(<ComputerUsePanel namespace="default" name="run-2" enabled model="test-model" />);
    await waitFor(() => expect(m.stop).toHaveBeenCalled());
    await act(async () => resolve(target));
    expect(screen.getByText("No window selected")).toBeTruthy();
    expect(m.start).not.toHaveBeenCalled();
  });

  it("clears the native grant before a session starts", async () => {
    await panel();
    await selectAndConsent();
    fireEvent.click(screen.getByRole("button", { name: "Clear selection" }));
    await waitFor(() => expect(m.stop).toHaveBeenCalledTimes(1));
    expect(screen.getByText("No window selected")).toBeTruthy();
    expect((screen.getByRole("checkbox") as HTMLInputElement).checked).toBe(false);
    expect(m.start).not.toHaveBeenCalled();
    expect(m.capture).not.toHaveBeenCalled();
  });

  it("reports picker failure without retaining an earlier selection", async () => {
    await panel();
    await selectAndConsent();
    m.pick.mockRejectedValue(new Error("Picker failed"));
    fireEvent.click(screen.getByRole("button", { name: "Choose window with macOS" }));
    expect((await screen.findByRole("alert")).textContent).toContain("Picker failed");
    expect(screen.getByText("No window selected")).toBeTruthy();
    expect((screen.getByRole("checkbox") as HTMLInputElement).checked).toBe(false);
  });

  it("explains the feature-only minimum on older macOS", async () => {
    m.permissions.mockResolvedValue({ supported: false, accessibility: true });
    render(<ComputerUsePanel namespace="default" name="run-1" enabled model="test-model" />);
    await screen.findByText(/Computer use requires macOS 15.2/);
    expect(screen.queryByRole("button")).toBeNull();
    expect(m.pick).not.toHaveBeenCalled();
  });

  it("reveals approval requests and keeps stop available when collapsed", async () => {
    remotePending = { requestId: "request-1", frameId: "frame-1", action: { kind: "click", x: 10, y: 20 } };
    const view = await panel();
    view.container.querySelector("details")?.removeAttribute("open");
    await startSession();
    await screen.findByText("Agent requests: click");
    expect(view.container.querySelector("details")?.open).toBe(true);
    view.container.querySelector("details")?.removeAttribute("open");
    const stop = screen.getByRole("button", { name: "Stop computer use" });
    expect(stop.closest("summary")).not.toBeNull();
    fireEvent.click(stop);
    await screen.findByRole("status", { name: "Session stopped" });
    expect(m.approve).not.toHaveBeenCalled();
  });

  it("requires window selection and explicit consent, without capturing automatically", async () => {
    await panel();
    expect((screen.getByRole("button", { name: "Start supervised session" }) as HTMLButtonElement).disabled).toBe(true);
    expect(m.pick).not.toHaveBeenCalled();
    expect(m.capture).not.toHaveBeenCalled();
    await startSession();
    expect(m.start).toHaveBeenCalledWith({
      backend: "https://operator.example", user: "user-1", namespace: "default", run: "run-1",
      application: "TextEdit", windowId: 42, processId: 99,
    }, true, 0, "selection-1");
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
    // The native reason travels to the agent and is visible in the timeline.
    expect(m.relay).toHaveBeenCalledWith("session-1", native.scope, "resolve", "request-1",
      expect.objectContaining({ status: "failed", message: expect.stringContaining("Frame expired") }));
    expect(screen.getByRole("list", { name: "Recent computer actions" }).textContent).toContain("Frame expired");
  });

  it("shows the native failure reason for an executed action in the timeline", async () => {
    remotePending = { requestId: "request-1", frameId: "frame-1", action: { kind: "key", key: "Cmd+L" } };
    m.approve.mockResolvedValue({ requestId: "request-1", status: "failed", message: "Approved window is not the frontmost application window" });
    await panel();
    await startSession();
    fireEvent.click(screen.getByRole("checkbox", { name: /I reviewed/ }));
    fireEvent.click(screen.getByRole("button", { name: "Allow once" }));
    await screen.findByText("key — failed");
    expect(screen.getByRole("list", { name: "Recent computer actions" }).textContent).toContain("Approved window is not the frontmost application window");
    expect(screen.getByRole("alert").textContent).toContain("Approved window is not the frontmost application window");
  });

  it("describes open_url as a background browser action that needs no frame", async () => {
    remotePending = { requestId: "request-1", action: { kind: "open_url", url: "https://www.google.com/search?q=gratefulagents" } };
    m.approve.mockResolvedValue({ requestId: "request-1", status: "completed", message: "Opened" });
    await panel();
    await startSession();
    await screen.findByText("Agent requests: open_url");
    expect(screen.getByText("https://www.google.com/search?q=gratefulagents")).toBeTruthy();
    expect(screen.getByText(/without bringing it forward/)).toBeTruthy();
    expect(screen.getByText("Enters input")).toBeTruthy();
    fireEvent.click(screen.getByRole("checkbox", { name: /I reviewed/ }));
    fireEvent.click(screen.getByRole("button", { name: "Allow once" }));
    await screen.findByText("open_url — completed");
    expect(screen.getByRole("list", { name: "Recent computer actions" }).textContent).toContain("Opened https://www.google.com/search?q=gratefulagents");
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

  // Each proposal is shown after an approved observation so the preview frame
  // matches the request frame and pointer markers can be checked.
  async function proposeAfterPreview(action: DesktopRequest["action"], frameId = "frame-1") {
    remotePending = { requestId: "request-1", action: { kind: "observe" } };
    m.approve.mockResolvedValue({ requestId: "request-1", status: "completed", message: "Captured", capture: image });
    await panel();
    await startSession();
    fireEvent.click(screen.getByRole("checkbox", { name: /I reviewed/ }));
    fireEvent.click(screen.getByRole("button", { name: "Allow once" }));
    await screen.findByText("observe — completed");
    remotePending = { requestId: "request-2", frameId, action };
    await screen.findByText(`Agent requests: ${action.kind}`, {}, { timeout: 3000 });
  }

  it("describes click variants and marks the proposed click in the preview", async () => {
    await proposeAfterPreview({ kind: "click", x: 800, y: 600, button: "right", count: 2 });
    expect(screen.getByText("Double right-click pixel (800, 600) in frame frame-1.")).toBeTruthy();
    expect(screen.getByLabelText("Proposed click location").getAttribute("style")).toContain("left: 50%");
    expect(screen.getByText("Enters input")).toBeTruthy();
  });

  it("describes hover as read-only with a dashed pointer marker", async () => {
    await proposeAfterPreview({ kind: "move", x: 400, y: 300 });
    expect(screen.getByText("Move the pointer to pixel (400, 300) without clicking (hover).")).toBeTruthy();
    expect(screen.getByLabelText("Proposed pointer location").getAttribute("style")).toContain("left: 25%");
    expect(screen.getByText("Read-only")).toBeTruthy();
  });

  it("describes a drag with start and destination markers only when the whole path is in frame", async () => {
    await proposeAfterPreview({ kind: "drag", x: 0, y: 0, toX: 1200, toY: 600 });
    expect(screen.getByText("Press the left button at (0, 0), drag to (1200, 600), and release.")).toBeTruthy();
    expect(screen.getByLabelText("Proposed drag start").getAttribute("style")).toContain("left: 0%");
    expect(screen.getByLabelText("Proposed drag destination").getAttribute("style")).toContain("left: 75%");
    expect(screen.getByText("Enters input")).toBeTruthy();
  });

  it("draws no drag markers when the destination lies outside the previewed frame", async () => {
    await proposeAfterPreview({ kind: "drag", x: 0, y: 0, toX: 1600, toY: 1200 });
    expect(screen.queryByLabelText("Proposed drag start")).toBeNull();
    expect(screen.queryByLabelText("Proposed drag destination")).toBeNull();
  });

  it("describes a positioned scroll and marks where it is aimed", async () => {
    await proposeAfterPreview({ kind: "scroll", deltaX: 0, deltaY: 300, x: 160, y: 120 });
    expect(screen.getByText(/with the pointer at \(160, 120\)/)).toBeTruthy();
    expect(screen.getByLabelText("Proposed scroll location").getAttribute("style")).toContain("left: 10%");
    expect(screen.getByText("Read-only")).toBeTruthy();
  });

  it("renders hotkeys as text plus Mac glyphs", async () => {
    await proposeAfterPreview({ kind: "key", key: "Shift+Cmd+Z" });
    expect(screen.getByText("Press Shift+Cmd+Z.")).toBeTruthy();
    expect(screen.getAllByText(/^[⇧⌘Z]$/).map((node) => node.textContent)).toEqual(["⇧", "⌘", "Z"]);
  });

  it("never draws a marker for a proposal bound to a different frame", async () => {
    await proposeAfterPreview({ kind: "click", x: 10, y: 10 }, "frame-0");
    expect(screen.getByText("Click pixel (10, 10) in frame frame-0.")).toBeTruthy();
    expect(screen.queryByLabelText("Proposed click location")).toBeNull();
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
    expect(screen.getByText("No window selected")).toBeTruthy();
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
    m.permissions.mockResolvedValue({ supported: false, accessibility: false });
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
