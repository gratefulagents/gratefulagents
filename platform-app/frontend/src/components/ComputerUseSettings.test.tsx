import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { nativeStatus, relaunchApp, requestPermission } from "@/lib/computer-use/native";
import { getDefaultApprovalMode } from "@/lib/computer-use/preferences";
import { ComputerUseSettings } from "./ComputerUseSettings";

vi.mock("@/lib/computer-use/native", () => ({ nativeStatus: vi.fn(), requestPermission: vi.fn(), relaunchApp: vi.fn() }));

const missing = {
  supported: true, accessibility: false, screenRecording: false, emergencyStop: true, displays: [],
  session: { active: false, paused: false },
};

beforeEach(() => {
  vi.clearAllMocks();
  localStorage.clear();
  vi.mocked(nativeStatus).mockResolvedValue(missing);
  vi.mocked(requestPermission).mockResolvedValue();
  vi.mocked(relaunchApp).mockResolvedValue();
});
afterEach(cleanup);

describe("ComputerUseSettings", () => {
  it("sets the default approval mode", async () => {
    render(<ComputerUseSettings />);
    expect(getDefaultApprovalMode()).toBe("auto");
    expect(screen.getByText(/Autonomous: actions run/)).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Ask first" }));
    expect(getDefaultApprovalMode()).toBe("ask");
    expect(await screen.findByText(/Ask first: you allow/)).toBeTruthy();
  });

  it("shows permission status and grants on request", async () => {
    render(<ComputerUseSettings />);
    fireEvent.click(await screen.findByRole("button", { name: "Grant Accessibility" }));
    await waitFor(() => expect(requestPermission).toHaveBeenCalledWith("accessibility"));
    vi.mocked(nativeStatus).mockResolvedValue({ ...missing, accessibility: true });
    fireEvent.click(screen.getByRole("button", { name: "Grant Screen Recording" }));
    await waitFor(() => expect(requestPermission).toHaveBeenCalledWith("screen_recording"));
    expect(await screen.findByText("Granted")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: /Relaunch app/ }));
    await waitFor(() => expect(relaunchApp).toHaveBeenCalled());
  });

  it("surfaces native errors", async () => {
    vi.mocked(requestPermission).mockRejectedValue(new Error("System Settings could not be opened"));
    render(<ComputerUseSettings />);
    fireEvent.click(await screen.findByRole("button", { name: "Grant Accessibility" }));
    expect((await screen.findByRole("alert")).textContent).toContain("System Settings could not be opened");
  });

  it("explains unsupported platforms", async () => {
    vi.mocked(nativeStatus).mockResolvedValue({ ...missing, supported: false });
    render(<ComputerUseSettings />);
    expect(await screen.findByText(/macOS desktop app/)).toBeTruthy();
    expect(screen.queryByRole("button", { name: /Grant/ })).toBeNull();
  });
});
