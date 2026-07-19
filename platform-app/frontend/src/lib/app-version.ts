import { getCloudflareAccessHeaders } from "./app-environment";
import { BUILD_COMMIT } from "./build-info";
import { isTauri } from "./is-tauri";
import { backendBaseUrl } from "./platform";
import { runtimeFetch } from "./runtime-fetch";

export interface AppVersionMismatch {
  appVersion: string;
  serverVersion: string;
}

export function normalizeAppVersion(version: string): string {
  return version.trim().replace(/^v/i, "");
}

export function findVersionMismatch(
  appVersion: string,
  serverVersion: string,
): AppVersionMismatch | null {
  const app = normalizeAppVersion(appVersion);
  const server = normalizeAppVersion(serverVersion);
  if (!app || !server || server === "dev" || app === server) return null;
  return { appVersion: app, serverVersion: server };
}

/**
 * Compare a packaged native app with the server release it is connected to.
 * Browser and unstamped development builds intentionally skip this check.
 */
export async function checkNativeAppVersion(): Promise<AppVersionMismatch | null> {
  if (!isTauri || BUILD_COMMIT === "dev") return null;

  const [{ getVersion }, response] = await Promise.all([
    import("@tauri-apps/api/app"),
    runtimeFetch(`${backendBaseUrl()}/api/version`, {
      headers: getCloudflareAccessHeaders(),
    }),
  ]);
  if (!response.ok) {
    throw new Error(`Version check returned HTTP ${response.status}`);
  }

  const payload = (await response.json()) as { version?: unknown };
  if (typeof payload.version !== "string") {
    throw new Error("Version check returned an invalid response");
  }

  return findVersionMismatch(await getVersion(), payload.version);
}
