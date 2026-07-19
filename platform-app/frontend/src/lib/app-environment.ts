// gratefulagents — app environment, backed by the active workspace.
//
// Historically this stored a single endpoint + Cloudflare Access credentials.
// It now delegates to the active workspace (see workspaces.ts) so the rest of the
// app keeps using the same small API while supporting multiple backends.

import { isTauri } from "./platform";
import {
  getActiveWorkspace,
  hydrateWorkspaces,
  upsertActiveWorkspace,
} from "./workspaces";

export interface AppEnvironment {
  endpointUrl: string;
  cfAccessClientId: string;
  cfAccessClientSecret: string;
}

function normalize(value: string | null | undefined): string {
  return (value ?? "").trim();
}

export function getAppEnvironment(): AppEnvironment {
  const ws = getActiveWorkspace();
  return {
    endpointUrl: ws?.endpointUrl ?? "",
    cfAccessClientId: ws?.cfAccessClientId ?? "",
    cfAccessClientSecret: ws?.cfAccessClientSecret ?? "",
  };
}

export function isBackendConfigured(): boolean {
  // The web build is served by the gratefulagents backend itself, so it is always
  // reachable at the same origin (backendBaseUrl() === ""). No URL to enter.
  if (!isTauri) return true;
  return getAppEnvironment().endpointUrl.length > 0;
}

export function getCloudflareAccessHeaders(): Record<string, string> {
  const env = getAppEnvironment();
  const headers: Record<string, string> = {};
  if (env.cfAccessClientId) {
    headers["CF-Access-Client-Id"] = env.cfAccessClientId;
  }
  if (env.cfAccessClientSecret) {
    headers["CF-Access-Client-Secret"] = env.cfAccessClientSecret;
  }
  return headers;
}

export async function hydrateAppEnvironment(): Promise<AppEnvironment> {
  await hydrateWorkspaces();
  return getAppEnvironment();
}

// Persist the active workspace's environment, creating one if needed.
export async function saveAppEnvironment(
  nextEnvironment: AppEnvironment,
): Promise<AppEnvironment> {
  upsertActiveWorkspace({
    endpointUrl: normalize(nextEnvironment.endpointUrl),
    cfAccessClientId: normalize(nextEnvironment.cfAccessClientId),
    cfAccessClientSecret: normalize(nextEnvironment.cfAccessClientSecret),
  });
  return getAppEnvironment();
}
