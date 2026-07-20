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

interface SemanticVersion {
  core: number[];
  prerelease: string[];
}

function parseSemanticVersion(version: string): SemanticVersion | null {
  const match = /^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$/.exec(
    version,
  );
  if (!match) return null;

  return {
    core: match.slice(1, 4).map(Number),
    prerelease: match[4]?.split(".") ?? [],
  };
}

function compareSemanticVersions(left: SemanticVersion, right: SemanticVersion): number {
  for (let index = 0; index < left.core.length; index += 1) {
    if (left.core[index] !== right.core[index]) {
      return left.core[index] - right.core[index];
    }
  }

  if (left.prerelease.length === 0 || right.prerelease.length === 0) {
    return right.prerelease.length - left.prerelease.length;
  }

  const length = Math.max(left.prerelease.length, right.prerelease.length);
  for (let index = 0; index < length; index += 1) {
    const leftPart = left.prerelease[index];
    const rightPart = right.prerelease[index];
    if (leftPart === undefined) return -1;
    if (rightPart === undefined) return 1;
    if (leftPart === rightPart) continue;

    const leftNumber = /^\d+$/.test(leftPart) ? Number(leftPart) : null;
    const rightNumber = /^\d+$/.test(rightPart) ? Number(rightPart) : null;
    if (leftNumber !== null && rightNumber !== null) return leftNumber - rightNumber;
    if (leftNumber !== null) return -1;
    if (rightNumber !== null) return 1;
    return leftPart.localeCompare(rightPart);
  }

  return 0;
}

export function findVersionMismatch(
  appVersion: string,
  serverVersion: string,
): AppVersionMismatch | null {
  const app = normalizeAppVersion(appVersion);
  const server = normalizeAppVersion(serverVersion);
  if (!app || !server || server === "dev" || app === server) return null;

  const parsedApp = parseSemanticVersion(app);
  const parsedServer = parseSemanticVersion(server);
  if (!parsedApp || !parsedServer || compareSemanticVersions(parsedApp, parsedServer) >= 0) return null;

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
