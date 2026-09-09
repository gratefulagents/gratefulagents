import { beforeEach, describe, expect, it, vi } from "vitest";
import {
  computerUsePermissions, openComputerUsePermission, computerUseWindows,
  desktopSessionStatus, startDesktopSession, heartbeatDesktopSession,
  pauseDesktopSession, resumeDesktopSession, stopDesktopSession, captureDesktopWindow,
  queueDesktopRequest, armDesktopRequest, approveDesktopRequest, cancelDesktopRequest,
} from "./computer-use";

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

const scope = {
  backend: "https://operator.example", user: "user-1", namespace: "default", run: "run-1",
  application: "TextEdit", windowId: 42, processId: 99,
};

describe("computer use bridge", () => {
  it("binds single-action approval commands to the native session, request and permit", async () => {
    const request = { requestId: "r", frameId: "f", action: { kind: "type" as const, text: "Proposed text" } };
    await queueDesktopRequest("s", scope, request);
    await armDesktopRequest("s", scope, "r");
    await approveDesktopRequest("s", scope, "r", "permit");
    await cancelDesktopRequest("s", scope, "r");
    expect(native.invoke.mock.calls).toEqual([
      ["computer_use_queue_request", { sessionId: "s", scope, request }],
      ["computer_use_arm_request", { sessionId: "s", scope, requestId: "r" }],
      ["computer_use_approve_request", { sessionId: "s", scope, requestId: "r", permit: "permit" }],
      ["computer_use_cancel_request", { sessionId: "s", scope, requestId: "r" }],
    ]);
  });
  it("preserves native command names, session bindings and consent revision", async () => {
    await computerUseWindows();
    await desktopSessionStatus();
    await startDesktopSession(scope, true, 7);
    await heartbeatDesktopSession("session-1", scope);
    await pauseDesktopSession();
    await resumeDesktopSession("session-1", scope);
    await captureDesktopWindow("session-1", scope);
    await stopDesktopSession();
    expect(native.invoke.mock.calls).toEqual([
      ["computer_use_windows", undefined],
      ["computer_use_session_status", undefined],
      ["computer_use_session_start", { scope, consentToScreenSharing: true, expectedRevision: 7 }],
      ["computer_use_session_heartbeat", { sessionId: "session-1", scope }],
      ["computer_use_session_pause", undefined],
      ["computer_use_session_resume", { sessionId: "session-1", scope }],
      ["computer_use_capture_window", { sessionId: "session-1", scope }],
      ["computer_use_session_stop", undefined],
    ]);
  });
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
    for (const command of [
      computerUseWindows, desktopSessionStatus, pauseDesktopSession, stopDesktopSession,
      () => startDesktopSession(scope, true, 0), () => heartbeatDesktopSession("s", scope),
      () => resumeDesktopSession("s", scope), () => captureDesktopWindow("s", scope),
      () => queueDesktopRequest("s", scope, { requestId: "r", action: { kind: "observe" } }),
      () => armDesktopRequest("s", scope, "r"), () => approveDesktopRequest("s", scope, "r", "permit"),
      () => cancelDesktopRequest("s", scope, "r"),
    ]) {
      await expect(command()).rejects.toThrow(/macOS/);
    }
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
