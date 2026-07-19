// gratefulagents — platform detection & native bridge helpers.
//
// Keeps `window.__TAURI__` checks in one place so the rest of the app can
// stay platform-agnostic. When running in a browser (dev proxy only), all
// helpers degrade gracefully.

import { getAppEnvironment } from "./app-environment";
import { isTauri } from "./is-tauri";

export { isTauri };

let _platform: string | null = null;

export async function platform(): Promise<string> {
  if (_platform) return _platform;
  if (!isTauri) {
    _platform = "web";
    return _platform;
  }
  try {
    const { platform: osPlatform } = await import("@tauri-apps/plugin-os");
    _platform = await osPlatform();
    return _platform;
  } catch {
    _platform = "web";
    return _platform;
  }
}

export async function isMac(): Promise<boolean> {
  return (await platform()) === "macos";
}

export async function isIpad(): Promise<boolean> {
  return (await platform()) === "ios";
}

export async function isLinux(): Promise<boolean> {
  return (await platform()) === "linux";
}

/**
 * Backend base URL. In dev, Vite proxies /auth.* and /platform.v1.* to
 * localhost:8090, so "" works. In packaged builds we point at a configurable
 * endpoint (persisted in the Tauri store).
 */
export function backendBaseUrl(): string {
  const stored = getAppEnvironment().endpointUrl;
  if (stored && stored.trim().length > 0) return stored.trim().replace(/\/$/, "");
  // Same-origin works in dev (Vite proxy) and in web builds.
  if (!isTauri) return "";
  // Default for packaged Tauri builds — overridable in Settings.
  return (
    import.meta.env.VITE_OPERATOR_ENDPOINT ?? "http://localhost:8090"
  ).replace(/\/$/, "");
}
