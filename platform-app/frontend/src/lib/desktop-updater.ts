// Desktop auto-update — frontend facade for the Rust updater commands
// (tauri/src-tauri/src/updater.rs). Release manifests and artifacts are public,
// so checks require no user credentials.

import { isTauri } from "./is-tauri";
import { BUILD_COMMIT } from "./build-info";
import { storeDelete, storeGet, storeSet } from "./secure-store";

/** Repo the desktop app updates from (shown in UI hints/links). */
export const DISTRIBUTION_REPO = "gratefulagents/gratefulagents";
export const DISTRIBUTION_RELEASES_URL = `https://github.com/${DISTRIBUTION_REPO}/releases`;

const INTERVAL_KEY = "desktopUpdaterCheckIntervalHours";
const LEGACY_TOKEN_KEY = "desktopUpdaterGithubToken";

/** Default periodic re-check cadence while the app runs (builds ship ~hourly). */
export const DEFAULT_UPDATE_CHECK_INTERVAL_HOURS = 1;

/** Upper bound for the stored interval: one week. */
const MAX_UPDATE_CHECK_INTERVAL_HOURS = 168;

/** Fired on `window` whenever the stored check interval changes. */
export const UPDATE_CHECK_INTERVAL_EVENT = "desktop-updater:interval-changed";

/** Cadence choices offered in Settings → Desktop updates. 0 = launch only. */
export const UPDATE_CHECK_INTERVAL_OPTIONS: ReadonlyArray<{ hours: number; label: string }> = [
  { hours: 0, label: "Launch only" },
  { hours: 1, label: "Hourly" },
  { hours: 6, label: "6h" },
  { hours: 12, label: "12h" },
  { hours: 24, label: "Daily" },
];

/** Junk falls back to the default; numbers clamp to 0..168 (0 = launch only). */
function sanitizeIntervalHours(value: unknown): number {
  if (typeof value !== "number" || !Number.isFinite(value)) {
    return DEFAULT_UPDATE_CHECK_INTERVAL_HOURS;
  }
  return Math.min(MAX_UPDATE_CHECK_INTERVAL_HOURS, Math.max(0, value));
}

/** Get the periodic update-check interval in hours (0 = check on launch only). */
export async function getUpdateCheckIntervalHours(): Promise<number> {
  const stored = await storeGet<unknown>(INTERVAL_KEY);
  if (stored == null) return DEFAULT_UPDATE_CHECK_INTERVAL_HOURS;
  return sanitizeIntervalHours(stored);
}

/**
 * Store the periodic update-check interval (hours; 0 disables re-checks) and
 * notify live listeners via UPDATE_CHECK_INTERVAL_EVENT. Returns the
 * sanitized value that was stored.
 */
export async function setUpdateCheckIntervalHours(hours: number): Promise<number> {
  const sanitized = sanitizeIntervalHours(hours);
  await storeSet(INTERVAL_KEY, sanitized);
  window.dispatchEvent(
    new CustomEvent(UPDATE_CHECK_INTERVAL_EVENT, { detail: { hours: sanitized } }),
  );
  return sanitized;
}

export interface UpdateCheckResult {
  available: boolean;
  currentVersion: string;
  version: string | null;
  notes: string | null;
  canAutoInstall: boolean;
  installHint: string | null;
  releaseUrl: string | null;
}

export interface UpdateProgress {
  downloaded: number;
  total: number | null;
}

/** Best-effort removal of the PAT stored by older private-release builds. */
export async function removeLegacyUpdaterToken(): Promise<void> {
  try {
    await storeDelete(LEGACY_TOKEN_KEY);
  } catch {
    // Secure-store cleanup must never block app startup or update checks.
  }
}

/** Check the public distribution release for a newer desktop build. */
export async function checkForDesktopUpdate(): Promise<UpdateCheckResult> {
  const { invoke } = await import("@tauri-apps/api/core");
  return await invoke<UpdateCheckResult>("updater_check");
}

/**
 * Download + install the update found by the last check. Resolves once the
 * update is staged; call `restartApp()` to apply it. `onProgress` receives
 * throttled download progress.
 */
export async function installDesktopUpdate(
  onProgress?: (progress: UpdateProgress) => void,
): Promise<void> {
  const [{ invoke }, { listen }] = await Promise.all([
    import("@tauri-apps/api/core"),
    import("@tauri-apps/api/event"),
  ]);
  const unlisten = onProgress
    ? await listen<UpdateProgress>("updater-progress", (event) => onProgress(event.payload))
    : null;
  try {
    await invoke("updater_install");
  } finally {
    unlisten?.();
  }
}

/** Relaunch the app to finish applying an installed update. */
export async function restartApp(): Promise<void> {
  const { invoke } = await import("@tauri-apps/api/core");
  await invoke("updater_restart");
}

/** Current desktop app version (stamped `0.1.<run>` in CI builds). */
export async function getAppVersion(): Promise<string | null> {
  if (!isTauri) return null;
  try {
    const { getVersion } = await import("@tauri-apps/api/app");
    return await getVersion();
  } catch {
    return null;
  }
}

/** Whether silent checks should run (commit-stamped desktop builds only). */
export async function shouldAutoCheck(): Promise<boolean> {
  if (!isTauri || BUILD_COMMIT === "dev") return false;
  try {
    const { platform } = await import("@tauri-apps/plugin-os");
    const os = await platform();
    return os !== "ios" && os !== "android";
  } catch {
    return false;
  }
}
