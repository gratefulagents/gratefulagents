import { isTauri, platform } from "./platform";

export interface ComputerUsePermissions {
  supported: boolean;
  screenRecording: boolean;
  accessibility: boolean;
}

export type ComputerUsePermission = "screen_recording" | "accessibility";

export interface WindowTarget {
  windowId: number;
  processId: number;
  application: string;
  title: string;
}

export interface DesktopScope {
  backend: string;
  user: string;
  namespace: string;
  run: string;
  application: string;
  windowId: number;
  processId: number;
}

export interface DesktopSession {
  revision: number;
  phase: "stopped" | "active" | "paused";
  sessionId: string | null;
  scope: DesktopScope | null;
  reason: string;
}

export interface WindowCapture {
  frameId: string;
  geometry: { x: number; y: number; width: number; height: number };
  pixelWidth: number;
  pixelHeight: number;
  dataUrl: string;
}

async function nativeCommand<T>(command: string, args?: Record<string, unknown>): Promise<T> {
  if (!isTauri || (await platform()) !== "macos") {
    throw new Error("Computer use requires the macOS desktop app");
  }
  const { invoke } = await import("@tauri-apps/api/core");
  return invoke<T>(command, args);
}

export async function computerUsePermissions(): Promise<ComputerUsePermissions> {
  if (!isTauri || (await platform()) !== "macos") {
    return { supported: false, screenRecording: false, accessibility: false };
  }
  const { invoke } = await import("@tauri-apps/api/core");
  return invoke<ComputerUsePermissions>("computer_use_permissions");
}

export async function openComputerUsePermission(permission: ComputerUsePermission): Promise<void> {
  await nativeCommand("computer_use_open_permission", { permission });
}

export const computerUseWindows = () => nativeCommand<WindowTarget[]>("computer_use_windows");
export const desktopSessionStatus = () => nativeCommand<DesktopSession>("computer_use_session_status");
export const startDesktopSession = (scope: DesktopScope, consentToScreenSharing: boolean, expectedRevision: number) =>
  nativeCommand<DesktopSession>("computer_use_session_start", { scope, consentToScreenSharing, expectedRevision });
export const heartbeatDesktopSession = (sessionId: string, scope: DesktopScope) =>
  nativeCommand<void>("computer_use_session_heartbeat", { sessionId, scope });
export const pauseDesktopSession = () => nativeCommand<void>("computer_use_session_pause");
export const resumeDesktopSession = (sessionId: string, scope: DesktopScope) =>
  nativeCommand<DesktopSession>("computer_use_session_resume", { sessionId, scope });
export const stopDesktopSession = () => nativeCommand<void>("computer_use_session_stop");
export const captureDesktopWindow = (sessionId: string, scope: DesktopScope) =>
  nativeCommand<WindowCapture>("computer_use_capture_window", { sessionId, scope });

export type DesktopAction =
  | { kind: "observe"; question?: string }
  | { kind: "click"; x: number; y: number }
  | { kind: "scroll"; deltaX: number; deltaY: number }
  | { kind: "type"; text: string }
  | { kind: "key"; key: string }
  | { kind: "activate" };

export interface DesktopRequest {
  requestId: string;
  frameId?: string;
  action: DesktopAction;
}

export interface DesktopOutcome {
  requestId: string;
  status: "completed" | "failed" | "denied";
  message: string;
  capture?: WindowCapture;
}

export const queueDesktopRequest = (sessionId: string, scope: DesktopScope, request: DesktopRequest) =>
  nativeCommand<void>("computer_use_queue_request", { sessionId, scope, request });
export const armDesktopRequest = (sessionId: string, scope: DesktopScope, requestId: string) =>
  nativeCommand<{ permit: string }>("computer_use_arm_request", { sessionId, scope, requestId });
export const approveDesktopRequest = (sessionId: string, scope: DesktopScope, requestId: string, permit: string) =>
  nativeCommand<DesktopOutcome>("computer_use_approve_request", { sessionId, scope, requestId, permit });
export const cancelDesktopRequest = (sessionId: string, scope: DesktopScope, requestId: string) =>
  nativeCommand<void>("computer_use_cancel_request", { sessionId, scope, requestId });
