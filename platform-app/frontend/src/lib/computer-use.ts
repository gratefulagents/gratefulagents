import { isTauri, platform } from "./platform";

export interface ComputerUsePermissions {
  supported: boolean;
  screenRecording: boolean;
  accessibility: boolean;
}

export type ComputerUsePermission = "screen_recording" | "accessibility";

export async function computerUsePermissions(): Promise<ComputerUsePermissions> {
  if (!isTauri || (await platform()) !== "macos") {
    return { supported: false, screenRecording: false, accessibility: false };
  }
  const { invoke } = await import("@tauri-apps/api/core");
  return invoke<ComputerUsePermissions>("computer_use_permissions");
}

export async function openComputerUsePermission(permission: ComputerUsePermission): Promise<void> {
  if (!isTauri || (await platform()) !== "macos") {
    throw new Error("Computer use requires the macOS desktop app");
  }
  const { invoke } = await import("@tauri-apps/api/core");
  await invoke("computer_use_open_permission", { permission });
}
