import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { ComputerUsePanel } from "./ComputerUsePanel";
import type { DesktopScope, DesktopSession, WindowCapture } from "@/lib/computer-use";

const m = vi.hoisted(() => ({
  isTauri: true, user: "user-1", getRun: vi.fn(), permissions: vi.fn(), windows: vi.fn(),
  start: vi.fn(), status: vi.fn(), heartbeat: vi.fn(), pause: vi.fn(), resume: vi.fn(),
  stop: vi.fn(), capture: vi.fn(),
}));
vi.mock("@/contexts/AuthContext", () => ({ useOptionalAuth: () => ({ user: { id: m.user } }) }));
vi.mock("@/lib/platform", () => ({ get isTauri() { return m.isTauri; }, backendBaseUrl: () => "https://operator.example" }));
vi.mock("@/lib/client", () => ({ client: { getAgentRun: m.getRun } }));
vi.mock("@/lib/computer-use", () => ({
  computerUsePermissions: m.permissions, computerUseWindows: m.windows,
  startDesktopSession: m.start, desktopSessionStatus: m.status,
  heartbeatDesktopSession: m.heartbeat, pauseDesktopSession: m.pause,
  resumeDesktopSession: m.resume, stopDesktopSession: m.stop, captureDesktopWindow: m.capture,
}));

const target = { windowId: 42, processId: 99, application: "TextEdit", title: "Notes" };
const run = { namespace: "default", name: "run-1", myPermission: "owner", phase: "Running" };
const image: WindowCapture = {
  geometry: { x: 0, y: 0, width: 800, height: 600 }, pixelWidth: 1600, pixelHeight: 1200,
  dataUrl: "data:image/png;base64,cHJldmlldw==",
};
let native: DesktopSession;

beforeEach(() => {
  vi.resetAllMocks();
  m.isTauri = true;
  m.user = "user-1";
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
  await screen.findByText("Desktop preview — stopped");
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
  fireEvent.click(screen.getByRole("button", { name: "Start preview session" }));
  await screen.findByText("Desktop preview — active");
  await waitFor(() => expect(m.heartbeat).toHaveBeenCalled());
}

describe("run-bound desktop preview", () => {
  it("requires window selection and explicit consent, without capturing automatically", async () => {
    await panel();
    expect((screen.getByRole("button", { name: "Start preview session" }) as HTMLButtonElement).disabled).toBe(true);
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
    fireEvent.click(screen.getByRole("button", { name: "Capture preview" }));
    const preview = await screen.findByAltText("Preview of the approved desktop window");
    expect(preview.getAttribute("src")).toBe(image.dataUrl);
    expect(m.capture).toHaveBeenCalledWith("session-1", native.scope);
    expect(m.getRun).toHaveBeenCalledWith({ namespace: "default", name: "run-1" });
  });

  it("denies start after run ownership changes", async () => {
    await panel();
    await selectAndConsent();
    m.getRun.mockResolvedValue({ ...run, myPermission: "viewer" });
    fireEvent.click(screen.getByRole("button", { name: "Start preview session" }));
    expect((await screen.findByRole("alert")).textContent).toContain("Only a run owner or admin");
    expect(m.start).not.toHaveBeenCalled();
  });

  it("denies start when the run has ended", async () => {
    await panel();
    await selectAndConsent();
    m.getRun.mockResolvedValue({ ...run, phase: "Succeeded" });
    fireEvent.click(screen.getByRole("button", { name: "Start preview session" }));
    await screen.findByRole("alert");
    expect(m.start).not.toHaveBeenCalled();
  });

  it("stops rather than renewing the native lease after a backend failure", async () => {
    await panel();
    await selectAndConsent();
    m.getRun.mockResolvedValueOnce(run).mockRejectedValue(new Error("Disconnected"));
    fireEvent.click(screen.getByRole("button", { name: "Start preview session" }));
    expect((await screen.findByRole("alert")).textContent).toContain("Disconnected");
    await waitFor(() => expect(m.stop).toHaveBeenCalled());
    expect(m.heartbeat).not.toHaveBeenCalled();
  });

  it("clears preview and requires fresh consent after stop", async () => {
    await panel();
    await startSession();
    fireEvent.click(screen.getByRole("button", { name: "Capture preview" }));
    await screen.findByRole("img");
    fireEvent.click(screen.getByRole("button", { name: "Stop desktop session" }));
    await screen.findByText("Desktop preview — stopped");
    expect(screen.queryByRole("img")).toBeNull();
    expect((screen.getByRole("checkbox") as HTMLInputElement).checked).toBe(false);
  });

  it("does not display a late capture after stop", async () => {
    await panel();
    await startSession();
    let complete: (capture: WindowCapture) => void = () => {};
    m.capture.mockImplementation(() => new Promise<WindowCapture>((resolve) => { complete = resolve; }));
    fireEvent.click(screen.getByRole("button", { name: "Capture preview" }));
    await waitFor(() => expect(m.capture).toHaveBeenCalled());
    fireEvent.click(screen.getByRole("button", { name: "Stop desktop session" }));
    await screen.findByText("Desktop preview — stopped");
    await act(async () => complete(image));
    expect(screen.queryByRole("img")).toBeNull();
  });

  it("clears previews while paused and requires explicit resume", async () => {
    await panel();
    await startSession();
    fireEvent.click(screen.getByRole("button", { name: "Capture preview" }));
    await screen.findByRole("img");
    fireEvent.click(screen.getByRole("button", { name: "Pause" }));
    await screen.findByText("Desktop preview — paused");
    expect(screen.queryByRole("img")).toBeNull();
    expect(m.resume).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: "Resume" }));
    await screen.findByText("Desktop preview — active");
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
    fireEvent.click(screen.getByRole("button", { name: "Stop desktop session" }));
    expect((await screen.findByRole("alert")).textContent).toContain("Cannot confirm native stop");
    expect(screen.queryByText("Desktop preview — stopped")).toBeNull();
  });

  it("hides all controls on the web", () => {
    m.isTauri = false;
    const view = render(<ComputerUsePanel namespace="default" name="run-1" enabled model="test" />);
    expect(view.container.textContent).toBe("");
    expect(m.permissions).not.toHaveBeenCalled();
  });
});
