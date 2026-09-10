import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { ComputerUseSettings } from "./ComputerUseSettings";
import { computerUsePermissions, openComputerUsePermission, relaunchComputerUse } from "@/lib/computer-use";
import { getComputerUseApprovalMode, setComputerUseApprovalMode } from "@/lib/computer-use-preferences";

vi.mock("@/lib/computer-use", () => ({
  computerUsePermissions: vi.fn(),
  openComputerUsePermission: vi.fn(),
  relaunchComputerUse: vi.fn(),
}));

const denied = { supported: true, accessibility: false };

beforeEach(() => {
  vi.resetAllMocks();
  setComputerUseApprovalMode("manual");
  vi.mocked(computerUsePermissions).mockResolvedValue(denied);
  vi.mocked(openComputerUsePermission).mockResolvedValue();
  vi.mocked(relaunchComputerUse).mockResolvedValue();
});
afterEach(cleanup);

describe("computer use permission setup", () => {
  it("checks permissions without prompting or implying control is enabled", async () => {
    render(<ComputerUseSettings />);
    expect(await screen.findAllByText("Not granted")).toHaveLength(1);
    expect(screen.getByText(/does not capture your screen/)).toBeTruthy();
    expect(openComputerUsePermission).not.toHaveBeenCalled();
    expect(screen.queryByRole("button", { name: "Screen Recording settings" })).toBeNull();
    expect(screen.getByText(/No broad Screen Recording permission/)).toBeTruthy();
    expect(relaunchComputerUse).not.toHaveBeenCalled();
    expect(screen.getByRole("note").textContent).toContain("different build");
  });

  it("polls OS status while visible and relaunches only on request", async () => {
    vi.useFakeTimers();
    try {
      render(<ComputerUseSettings />);
      await vi.waitFor(() => expect(computerUsePermissions).toHaveBeenCalledTimes(1));
      vi.mocked(computerUsePermissions).mockResolvedValue({ supported: true, accessibility: true });
      await vi.advanceTimersByTimeAsync(2100);
      expect(computerUsePermissions).toHaveBeenCalledTimes(2);
      await vi.waitFor(() => expect(screen.getAllByText("Granted")).toHaveLength(1));
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
      supported: true, accessibility: true,
    });
    fireEvent.focus(window);
    expect(await screen.findAllByText("Granted")).toHaveLength(1);
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

  describe("approval mode", () => {
    it("defaults to manual and requires confirmation before skipping all approvals", async () => {
      render(<ComputerUseSettings />);
      const group = await screen.findByRole("radiogroup", { name: "Approval mode" });
      expect(screen.getByRole("radio", { name: /Manually approve/ }).getAttribute("aria-checked")).toBe("true");
      fireEvent.click(screen.getByRole("radio", { name: /Skip all approvals/ }));
      expect(getComputerUseApprovalMode()).toBe("manual");
      const dialog = await screen.findByRole("dialog");
      expect(dialog.textContent).toContain("Skip all approvals?");
      expect(dialog.textContent).toContain("remain responsible");
      fireEvent.click(screen.getByRole("button", { name: "Skip all approvals" }));
      await waitFor(() => expect(getComputerUseApprovalMode()).toBe("auto"));
      await waitFor(() => expect(screen.getByRole("radio", { name: /Skip all approvals/ }).getAttribute("aria-checked")).toBe("true"));
      expect(group.textContent).toContain("Skip all approvals");
      expect(screen.getByText(/can send, submit, delete, or purchase/)).toBeTruthy();
    });

    it("cancelling the confirmation leaves the mode unchanged", async () => {
      render(<ComputerUseSettings />);
      fireEvent.click(await screen.findByRole("radio", { name: /Skip all approvals/ }));
      fireEvent.click(await screen.findByRole("button", { name: "Cancel" }));
      await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
      expect(getComputerUseApprovalMode()).toBe("manual");
    });

    it("switches to assisted, and back down, without confirmation", async () => {
      setComputerUseApprovalMode("auto");
      render(<ComputerUseSettings />);
      fireEvent.click(await screen.findByRole("radio", { name: /Automatically approve read-only/ }));
      await waitFor(() => expect(getComputerUseApprovalMode()).toBe("assisted"));
      expect(screen.queryByRole("dialog")).toBeNull();
      fireEvent.click(screen.getByRole("radio", { name: /Manually approve/ }));
      await waitFor(() => expect(getComputerUseApprovalMode()).toBe("manual"));
    });

    it("is hidden on unsupported platforms", async () => {
      vi.mocked(computerUsePermissions).mockResolvedValue({ ...denied, supported: false });
      render(<ComputerUseSettings />);
      await screen.findByText(/unavailable on this platform/);
      expect(screen.queryByRole("radiogroup")).toBeNull();
    });
  });
});
