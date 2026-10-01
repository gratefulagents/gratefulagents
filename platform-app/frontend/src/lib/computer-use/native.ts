import { isTauri, platform } from "@/lib/platform";

export type Permission = "accessibility" | "screen_recording";

export interface Display {
  id: number;
  name: string;
  /** Points, not pixels. */
  width: number;
  height: number;
  scale: number;
  primary: boolean;
}

export interface NativeSession {
  active: boolean;
  paused: boolean;
  displayId?: number;
  runKey?: string;
  frameWidth?: number;
  frameHeight?: number;
  stoppedReason?: string;
}

export interface NativeStatus {
  supported: boolean;
  unsupportedReason?: string;
  accessibility: boolean;
  screenRecording: boolean;
  emergencyStop: boolean;
  displays: Display[];
  session: NativeSession;
}

export interface Screenshot {
  mediaType: "image/jpeg" | "image/png";
  /** Base64 without a `data:` prefix. */
  data: string;
  width: number;
  height: number;
}

export interface ExecResult {
  screenshot: Screenshot;
  cursor?: { x: number; y: number };
}

export type ActionName =
  | "screenshot" | "left_click" | "right_click" | "middle_click" | "double_click" | "triple_click"
  | "mouse_move" | "left_click_drag" | "left_mouse_down" | "left_mouse_up" | "scroll" | "type" | "key"
  | "wait" | "cursor_position" | "zoom" | "open_url";

export type Point = [number, number];

/** A `computer_use` tool action (COMPUTER_USE.md §1), snake_case as on the wire. */
export interface ComputerAction {
  action: ActionName;
  coordinate?: Point;
  start_coordinate?: Point;
  text?: string;
  scroll_direction?: "up" | "down" | "left" | "right";
  scroll_amount?: number;
  repeat?: number;
  duration?: number;
  region?: [number, number, number, number];
  url?: string;
}

export const SESSION_EVENT = "computer-use://session";

const UNSUPPORTED = "Computer use is available in the macOS desktop app.";

async function isSupportedHost(): Promise<boolean> {
  return isTauri && (await platform()) === "macos";
}

async function command<T>(name: string, args?: Record<string, unknown>): Promise<T> {
  if (!(await isSupportedHost())) throw new Error(UNSUPPORTED);
  const { invoke } = await import("@tauri-apps/api/core");
  try {
    return await invoke<T>(name, args);
  } catch (error) {
    throw error instanceof Error ? error : new Error(String(error));
  }
}

export async function nativeStatus(): Promise<NativeStatus> {
  if (!(await isSupportedHost())) {
    return {
      supported: false, unsupportedReason: UNSUPPORTED, accessibility: false, screenRecording: false,
      emergencyStop: false, displays: [], session: { active: false, paused: false },
    };
  }
  return command<NativeStatus>("computer_use_status");
}

export const requestPermission = (permission: Permission) =>
  command<void>("computer_use_request_permission", { permission });

export const relaunchApp = () => command<void>("computer_use_relaunch");

export const startNativeSession = (displayId: number, runKey: string) =>
  command<NativeSession>("computer_use_start", { displayId, runKey });

export const stopNativeSession = (reason?: string) =>
  command<void>("computer_use_stop", { reason: reason ?? null });

export const setNativePaused = (paused: boolean) =>
  command<NativeSession>("computer_use_set_paused", { paused });

export const executeAction = (action: ComputerAction) =>
  command<ExecResult>("computer_use_execute", { action });

/** Subscribes to native session changes; resolves to an unsubscribe function. */
export async function onNativeSession(handler: (session: NativeSession) => void): Promise<() => void> {
  if (!(await isSupportedHost())) return () => {};
  const { listen } = await import("@tauri-apps/api/event");
  return listen<NativeSession>(SESSION_EVENT, (event) => handler(event.payload));
}
