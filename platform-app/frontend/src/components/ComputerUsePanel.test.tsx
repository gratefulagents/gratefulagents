import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import type { ComputerUseState } from "@/lib/computer-use/controller";
import type { NativeStatus } from "@/lib/computer-use/native";
import { nativeStatus, relaunchApp, requestPermission } from "@/lib/computer-use/native";
import { ComputerUsePanel } from "./ComputerUsePanel";

const cu = vi.hoisted(() => ({
  state: {} as ComputerUseState,
  listeners: new Set<() => void>(),
  start: vi.fn(),
  stop: vi.fn(),
  pause: vi.fn(),
  resume: vi.fn(),
  decide: vi.fn(),
  setMode: vi.fn(),
}));

vi.mock("@/lib/computer-use/controller", async () => {
  const { useSyncExternalStore } = await import("react");
  const subscribe = (listener: () => void) => {
    cu.listeners.add(listener);
    return () => cu.listeners.delete(listener);
  };
  return {
    useComputerUse: () => useSyncExternalStore(subscribe, () => cu.state),
    startComputerUse: cu.start,
    stopComputerUse: cu.stop,
    pauseComputerUse: cu.pause,
    resumeComputerUse: cu.resume,
    decideComputerUse: cu.decide,
    setSessionApprovalMode: cu.setMode,
    UNAVAILABLE_MESSAGE: "This run can't use the computer (read-only or tool unavailable).",
  };
});
vi.mock("@/lib/computer-use/native", () => ({ nativeStatus: vi.fn(), requestPermission: vi.fn(), relaunchApp: vi.fn() }));

const idle: ComputerUseState = { phase: "idle", approvalMode: "auto", timeline: [], available: true };
const run = { namespace: "ns", name: "run" };
const displays = [
  { id: 7, name: "LG UltraFine", width: 2560, height: 1440, scale: 1, primary: false },
  { id: 1, name: "Built-in Retina Display", width: 1512, height: 982, scale: 2, primary: true },
];
const status = (patch: Partial<NativeStatus> = {}): NativeStatus => ({
  supported: true, accessibility: true, screenRecording: true, emergencyStop: true, displays,
  session: { active: false, paused: false }, ...patch,
});
const frame = { dataUrl: "data:image/jpeg;base64,QUJD", width: 1183, height: 768, at: Date.now() };
const click = { id: "r1", action: { action: "left_click" as const, coordinate: [512, 300] as [number, number] } };

function setState(patch: Partial<ComputerUseState>) {
  act(() => {
    cu.state = { ...cu.state, ...patch };
    for (const listener of cu.listeners) listener();
  });
}

function renderPanel(enabled = true) {
  const panel = document.createElement("div");
  const shortcut = document.createElement("div");
  document.body.append(panel, shortcut);
  const open = vi.fn();
  render(<ComputerUsePanel namespace="ns" name="run" enabled={enabled} view={{ panel, shortcut, open }} />);
  return { panel, shortcut, open };
}

beforeEach(() => {
  vi.clearAllMocks();
  cu.state = idle;
  vi.mocked(nativeStatus).mockResolvedValue(status());
  vi.mocked(requestPermission).mockResolvedValue();
  vi.mocked(relaunchApp).mockResolvedValue();
});
afterEach(() => {
  cleanup();
  document.body.innerHTML = "";
});

describe("ComputerUsePanel setup", () => {
  it("guides through permissions and offers a relaunch after Screen Recording", async () => {
    vi.mocked(nativeStatus).mockResolvedValue(status({ accessibility: false, screenRecording: false }));
    renderPanel();
    expect(await screen.findByRole("heading", { name: "Let the agent use this Mac" })).toBeTruthy();
    const start = screen.getByRole("button", { name: "Start" }) as HTMLButtonElement;
    expect(start.disabled).toBe(true);
    fireEvent.click(screen.getByRole("button", { name: "Grant Accessibility" }));
    await waitFor(() => expect(requestPermission).toHaveBeenCalledWith("accessibility"));
    expect(screen.queryByRole("button", { name: /Relaunch app/ })).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Grant Screen Recording" }));
    fireEvent.click(await screen.findByRole("button", { name: /Relaunch app/ }));
    await waitFor(() => expect(relaunchApp).toHaveBeenCalled());
    expect(screen.getByText(/can control the mouse and keyboard/)).toBeTruthy();
  });

  it("preselects the primary display and starts on the chosen one", async () => {
    renderPanel();
    const primary = await screen.findByRole("radio", { name: /Built-in Retina Display/ });
    expect(primary.getAttribute("aria-checked")).toBe("true");
    expect(screen.getByText("2560×1440")).toBeTruthy();
    fireEvent.click(screen.getByRole("radio", { name: /LG UltraFine/ }));
    const start = screen.getByRole("button", { name: "Start" }) as HTMLButtonElement;
    expect(start.disabled).toBe(false);
    fireEvent.click(start);
    expect(cu.start).toHaveBeenCalledWith(run, 7);
  });

  it("re-polls permissions every 2 s while visible", async () => {
    vi.useFakeTimers();
    try {
      const { panel } = renderPanel();
      await vi.advanceTimersByTimeAsync(0);
      expect(nativeStatus).toHaveBeenCalledTimes(1);
      await vi.advanceTimersByTimeAsync(2000);
      expect(nativeStatus).toHaveBeenCalledTimes(2);
      panel.hidden = true;
      await vi.advanceTimersByTimeAsync(4000);
      expect(nativeStatus).toHaveBeenCalledTimes(2);
    } finally {
      vi.useRealTimers();
    }
  });

  it("explains an unsupported platform", async () => {
    vi.mocked(nativeStatus).mockResolvedValue(status({ supported: false, displays: [] }));
    renderPanel();
    expect(await screen.findByText(/desktop app on macOS/)).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Start" })).toBeNull();
  });

  it("warns when the run cannot use the computer", async () => {
    cu.state = { ...idle, phase: "error", run, available: false, error: "x" };
    renderPanel();
    expect(await screen.findByText(/can't use the computer/)).toBeTruthy();
  });

  it("disables Start for viewers and finished runs", async () => {
    renderPanel(false);
    await screen.findByRole("radio", { name: /Built-in/ });
    expect((screen.getByRole("button", { name: "Start" }) as HTMLButtonElement).disabled).toBe(true);
    expect(screen.getByText(/Only the run owner/)).toBeTruthy();
  });

  it("hides the chat shortcut while idle", () => {
    const { shortcut } = renderPanel();
    expect(shortcut.childElementCount).toBe(0);
  });
});

describe("ComputerUsePanel active session", () => {
  const active: ComputerUseState = {
    ...idle, phase: "active", run, displayId: 1, sessionId: "s", latest: frame,
    timeline: [
      { id: "r2", action: { action: "type", text: "hello" }, status: "failed", error: "Secure input is on", startedAt: Date.now(), finishedAt: Date.now() },
      { id: "r1", action: click.action, status: "done", startedAt: Date.now() - 5000, finishedAt: Date.now(), thumbnail: frame.dataUrl },
    ],
  };

  it("shows status, live view, marker and timeline with controls", async () => {
    cu.state = active;
    const { panel } = renderPanel();
    const view = within(panel);
    expect(view.getByRole("status", { name: "Computer use: Agent in control" })).toBeTruthy();
    expect((view.getByRole("img", { name: /Latest screenshot/ }) as HTMLImageElement).src).toContain("QUJD");
    const marker = view.getByTestId("action-marker");
    expect(marker.getAttribute("viewBox")).toBe("0 0 1183 768");
    expect(marker.getAttribute("data-kind")).toBe("point");
    expect(view.getByText("Waiting for the agent…")).toBeTruthy();
    expect(view.getByText("Type “hello”")).toBeTruthy();
    expect(view.getByText("Secure input is on")).toBeTruthy();
    expect(view.getByText("Click at 512, 300")).toBeTruthy();
    expect(await view.findByText("Built-in Retina Display")).toBeTruthy();
    fireEvent.click(view.getByRole("button", { name: "Pause" }));
    expect(cu.pause).toHaveBeenCalled();
    fireEvent.click(view.getByRole("button", { name: "Stop" }));
    expect(cu.stop).toHaveBeenCalled();
    fireEvent.click(view.getByRole("button", { name: "Ask first" }));
    expect(cu.setMode).toHaveBeenCalledWith("ask");
  });

  it("offers Resume while paused", () => {
    cu.state = { ...active, phase: "paused" };
    renderPanel();
    expect(screen.getAllByRole("status", { name: /Paused/ })).toHaveLength(2);
    fireEvent.click(screen.getByRole("button", { name: "Resume" }));
    expect(cu.resume).toHaveBeenCalled();
  });

  it("renders the chat shortcut with Open and Stop", () => {
    cu.state = active;
    const { shortcut, open } = renderPanel();
    const pill = within(shortcut);
    expect(pill.getByRole("status", { name: "Computer: Agent in control" })).toBeTruthy();
    fireEvent.click(pill.getByRole("button", { name: "Open" }));
    expect(open).toHaveBeenCalled();
    fireEvent.click(pill.getByRole("button", { name: "Stop computer use" }));
    expect(cu.stop).toHaveBeenCalled();
  });

  it("ignores sessions of other runs", () => {
    cu.state = { ...active, run: { namespace: "ns", name: "other" } };
    const { shortcut } = renderPanel();
    expect(shortcut.childElementCount).toBe(0);
  });

  it("asks for approval with Allow, Deny and Enter", async () => {
    cu.state = { ...active, approvalMode: "ask", pending: { request: click, needsApproval: true } };
    const { shortcut, panel } = renderPanel();
    expect(within(shortcut).getByText("Needs approval")).toBeTruthy();
    const card = within(panel).getByRole("alertdialog", { name: /Approve/ });
    expect(within(card).getByText("Click at 512, 300")).toBeTruthy();
    expect(within(panel).queryByText("Waiting for the agent…")).toBeNull();
    fireEvent.click(within(card).getByRole("button", { name: /Allow/ }));
    expect(cu.decide).toHaveBeenLastCalledWith(true);
    fireEvent.click(within(card).getByRole("button", { name: /Deny/ }));
    expect(cu.decide).toHaveBeenLastCalledWith(false);
    cu.decide.mockClear();
    const composer = document.createElement("textarea");
    document.body.append(composer);
    fireEvent.keyDown(composer, { key: "Enter" });
    expect(cu.decide).not.toHaveBeenCalled();
    fireEvent.keyDown(window, { key: "Enter" });
    expect(cu.decide).toHaveBeenCalledWith(true);
  });

  it("shows typed text in full before approval", () => {
    const long = "x".repeat(120);
    cu.state = { ...active, pending: { request: { id: "r3", action: { action: "type", text: long } }, needsApproval: true } };
    renderPanel();
    expect(screen.getByText(long)).toBeTruthy();
  });

  it("updates live when the store changes", () => {
    cu.state = active;
    const { panel } = renderPanel();
    setState({ current: click });
    expect(within(panel).queryByText("Waiting for the agent…")).toBeNull();
    setState({ phase: "idle", current: undefined });
    expect(within(panel).queryByRole("status", { name: /Agent in control/ })).toBeNull();
  });
});
