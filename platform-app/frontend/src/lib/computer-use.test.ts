import { beforeEach, describe, expect, it, vi } from "vitest";
import { computerUsePermissions, openComputerUsePermission } from "./computer-use";

const native = vi.hoisted(() => ({ isTauri: true, platform: vi.fn(), invoke: vi.fn() }));
vi.mock("./platform", () => ({
  get isTauri() { return native.isTauri; },
  platform: native.platform,
}));
vi.mock("@tauri-apps/api/core", () => ({ invoke: native.invoke }));

beforeEach(() => {
  vi.resetAllMocks();
  native.isTauri = true;
  native.platform.mockResolvedValue("macos");
});

describe("computer use bridge", () => {
  it("gets native macOS permissions", async () => {
    const result = { supported: true, screenRecording: true, accessibility: false };
    native.invoke.mockResolvedValue(result);
    expect(await computerUsePermissions()).toEqual(result);
    expect(native.invoke).toHaveBeenCalledWith("computer_use_permissions");
  });

  it.each(["web", "ios", "linux", "windows"])("fails closed on %s", async (os) => {
    native.isTauri = os !== "web";
    native.platform.mockResolvedValue(os);
    expect(await computerUsePermissions()).toEqual({
      supported: false, screenRecording: false, accessibility: false,
    });
    await expect(openComputerUsePermission("accessibility")).rejects.toThrow(/macOS/);
    expect(native.invoke).not.toHaveBeenCalled();
  });

  it("passes only the selected permission to the native command", async () => {
    await openComputerUsePermission("screen_recording");
    expect(native.invoke).toHaveBeenCalledWith("computer_use_open_permission", {
      permission: "screen_recording",
    });
  });

  it("propagates IPC errors instead of reporting permissions as granted", async () => {
    native.invoke.mockRejectedValue(new Error("IPC failed"));
    await expect(computerUsePermissions()).rejects.toThrow("IPC failed");
  });
});
