import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { ComputerUseSettings } from "./ComputerUseSettings";
import { computerUsePermissions, openComputerUsePermission, relaunchComputerUse } from "@/lib/computer-use";

vi.mock("@/lib/computer-use", () => ({
  computerUsePermissions: vi.fn(),
  openComputerUsePermission: vi.fn(),
  relaunchComputerUse: vi.fn(),
}));

const denied = { supported: true, screenRecording: false, accessibility: false };

beforeEach(() => {
  vi.resetAllMocks();
  vi.mocked(computerUsePermissions).mockResolvedValue(denied);
  vi.mocked(openComputerUsePermission).mockResolvedValue();
  vi.mocked(relaunchComputerUse).mockResolvedValue();
});
afterEach(cleanup);

describe("computer use permission setup", () => {
  it("checks permissions without prompting or implying control is enabled", async () => {
    render(<ComputerUseSettings />);
    expect(await screen.findAllByText("Not granted")).toHaveLength(2);
    expect(screen.getByText(/does not capture your screen/)).toBeTruthy();
    expect(openComputerUsePermission).not.toHaveBeenCalled();
    expect(relaunchComputerUse).not.toHaveBeenCalled();
    expect(screen.getByRole("note").textContent).toContain("different build");
  });

  it("polls OS status while visible and relaunches only on request", async () => {
    vi.useFakeTimers();
    try {
      render(<ComputerUseSettings />);
      await vi.waitFor(() => expect(computerUsePermissions).toHaveBeenCalledTimes(1));
      vi.mocked(computerUsePermissions).mockResolvedValue({ supported: true, screenRecording: true, accessibility: true });
      await vi.advanceTimersByTimeAsync(2100);
      expect(computerUsePermissions).toHaveBeenCalledTimes(2);
      await vi.waitFor(() => expect(screen.getAllByText("Granted")).toHaveLength(2));
      expect(screen.queryByRole("note")).toBeNull();
      expect(screen.queryByRole("button", { name: "Relaunch gratefulagents" })).toBeNull();
    } finally {
      vi.useRealTimers();
    }
  });

  it("offers a relaunch while a permission is missing", async () => {
    render(<ComputerUseSettings />);
    fireEvent.click(await screen.findByRole("button", { name: "Relaunch gratefulagents" }));
    await waitFor(() => expect(relaunchComputerUse).toHaveBeenCalledTimes(1));
  });

  it.each([
    ["Screen Recording settings", "screen_recording"],
    ["Accessibility settings", "accessibility"],
  ])("opens %s only after a click", async (label, permission) => {
    render(<ComputerUseSettings />);
    fireEvent.click(await screen.findByRole("button", { name: label }));
    await waitFor(() => expect(openComputerUsePermission).toHaveBeenCalledWith(permission));
  });

  it("refreshes OS status when returning from Settings", async () => {
    render(<ComputerUseSettings />);
    await screen.findAllByText("Not granted");
    vi.mocked(computerUsePermissions).mockResolvedValue({
      supported: true, screenRecording: true, accessibility: true,
    });
    fireEvent.focus(window);
    expect(await screen.findAllByText("Granted")).toHaveLength(2);
  });

  it("does not show permission controls on unsupported platforms", async () => {
    vi.mocked(computerUsePermissions).mockResolvedValue({ ...denied, supported: false });
    render(<ComputerUseSettings />);
    await screen.findByText(/unavailable on this platform/);
    expect(screen.queryByRole("button")).toBeNull();
  });

  it("clears stale permission status when refresh fails", async () => {
    render(<ComputerUseSettings />);
    await screen.findAllByText("Not granted");
    vi.mocked(computerUsePermissions).mockRejectedValue(new Error("IPC unavailable"));
    fireEvent.focus(window);
    expect((await screen.findByRole("alert")).textContent).toContain("IPC unavailable");
    expect(screen.queryByText("Not granted")).toBeNull();
  });

  it("reports a failed settings launch", async () => {
    vi.mocked(openComputerUsePermission).mockRejectedValue(new Error("Launch failed"));
    render(<ComputerUseSettings />);
    fireEvent.click(await screen.findByRole("button", { name: "Accessibility settings" }));
    expect((await screen.findByRole("alert")).textContent).toContain("Launch failed");
  });

  it("removes the refresh listener on unmount", async () => {
    const view = render(<ComputerUseSettings />);
    await screen.findAllByText("Not granted");
    view.unmount();
    vi.mocked(computerUsePermissions).mockClear();
    fireEvent.focus(window);
    expect(computerUsePermissions).not.toHaveBeenCalled();
  });
});
