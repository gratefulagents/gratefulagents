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

// Relaunches the desktop app so freshly granted OS permissions apply; the
// native side revokes any supervised session first.
export async function relaunchComputerUse(): Promise<void> {
  await nativeCommand("computer_use_relaunch");
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

export type MouseButton = "left" | "right" | "middle";

export type DesktopAction =
  | { kind: "observe"; question?: string }
  | { kind: "click"; x: number; y: number; button?: MouseButton; count?: 1 | 2 | 3 }
  | { kind: "move"; x: number; y: number }
  | { kind: "drag"; x: number; y: number; toX: number; toY: number }
  | { kind: "scroll"; deltaX: number; deltaY: number; x?: number; y?: number }
  | { kind: "type"; text: string }
  | { kind: "key"; key: string }
  | { kind: "activate" };

export interface Hotkey {
  control: boolean;
  option: boolean;
  shift: boolean;
  cmd: boolean;
  key: string;
}

const NAMED_KEYS = new Set(["Enter", "Tab", "Escape", "Backspace", "Delete", "ArrowUp", "ArrowDown", "ArrowLeft", "ArrowRight", "Home", "End", "PageUp", "PageDown", "Space"]);
const MODIFIERS: Record<string, keyof Omit<Hotkey, "key">> = {
  Control: "control", Ctrl: "control", Option: "option", Alt: "option", Shift: "shift", Cmd: "cmd", Command: "cmd", Meta: "cmd",
};

// Mirrors the Go relay and native validators: any modifier order, one base key
// (named key, Space, A-Z, 0-9). Letters and digits need Control, Option, or
// Cmd so a key press cannot become a text channel that bypasses proposed-text
// review. Combinations that quit, close, hide, minimize, switch apps/spaces,
// open Spotlight, take screenshots, force quit, or trigger the emergency stop
// are rejected. Returns null when unsupported; native validation is the final
// authority.
export function parseHotkey(raw: string): Hotkey | null {
  if (typeof raw !== "string" || !raw.length || raw.length > 40) return null;
  const h: Hotkey = { control: false, option: false, shift: false, cmd: false, key: "" };
  const parts = raw.split("+");
  for (const [index, part] of parts.entries()) {
    const last = index === parts.length - 1;
    if (!last) {
      const modifier = Object.hasOwn(MODIFIERS, part) ? MODIFIERS[part] : undefined;
      if (!modifier || h[modifier]) return null;
      h[modifier] = true;
    } else if (NAMED_KEYS.has(part)) {
      h.key = part;
    } else if (/^[A-Za-z0-9]$/.test(part)) {
      h.key = part.toUpperCase();
      if (!h.control && !h.option && !h.cmd) return null;
    } else {
      return null;
    }
  }
  const arrow = h.key.startsWith("Arrow");
  if (h.cmd && ["Q", "W", "H", "M", "Tab", "Space", "Escape"].includes(h.key)) return null;
  if (h.cmd && h.shift && ["3", "4", "5", "6"].includes(h.key)) return null;
  if ((h.cmd && h.option && h.key === "D") || (h.cmd && h.control && h.key === "F")) return null;
  if (h.control && (arrow || h.key === "Space")) return null;
  return h;
}

const KEY_GLYPHS: Record<string, string> = {
  Enter: "↩", Tab: "⇥", Escape: "⎋", Backspace: "⌫", Delete: "⌦", ArrowUp: "↑", ArrowDown: "↓", ArrowLeft: "←", ArrowRight: "→",
  Home: "↖", End: "↘", PageUp: "⇞", PageDown: "⇟", Space: "␣",
};

/** Mac-style glyph sequence for a hotkey, e.g. ⇧⌘Z; falls back to the raw string. */
export function hotkeyGlyphs(raw: string): string[] {
  const h = parseHotkey(raw);
  if (!h) return [raw];
  const glyphs: string[] = [];
  if (h.control) glyphs.push("⌃");
  if (h.option) glyphs.push("⌥");
  if (h.shift) glyphs.push("⇧");
  if (h.cmd) glyphs.push("⌘");
  glyphs.push(KEY_GLYPHS[h.key] ?? h.key);
  return glyphs;
}

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
