import { createClient } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-web";
import { PlatformService } from "@/rpc/platform/service_pb";
import { createAuthInterceptor, type RefreshOutcome } from "./auth-interceptor";
import { createEnvironmentInterceptor } from "./environment-interceptor";
import { createRetryInterceptor } from "./retry-interceptor";
import { backendBaseUrl } from "./platform";
import { storeGet, storeSet, storeDelete } from "./secure-store";
import { getCloudflareAccessHeaders } from "./app-environment";
import { getActiveWorkspaceId } from "./workspaces";
import { runtimeFetch } from "./runtime-fetch";

const ACCESS_KEY = "access_token";
const REFRESH_KEY = "refresh_token";
const USER_KEY = "user";

// Per-workspace storage keys. Each workspace keeps its own auth session so the
// user can stay signed in to several backends at once (Slack-style).
function nsKeyForWorkspace(base: string, workspaceId: string): string {
  return `${base}::${workspaceId}`;
}

function nsKey(base: string): string {
  return nsKeyForWorkspace(base, getActiveWorkspaceId());
}

export function userStoreKey(): string {
  return nsKey(USER_KEY);
}

export function workspaceAuthStoreKeys(workspaceId: string): {
  accessToken: string;
  refreshToken: string;
  user: string;
} {
  return {
    accessToken: nsKeyForWorkspace(ACCESS_KEY, workspaceId),
    refreshToken: nsKeyForWorkspace(REFRESH_KEY, workspaceId),
    user: nsKeyForWorkspace(USER_KEY, workspaceId),
  };
}

// In-memory cache so sync getters (required by Connect) never block.
let accessTokenCache: string | null = null;
let refreshTokenCache: string | null = null;

export async function hydrateTokens(): Promise<void> {
  accessTokenCache = (await storeGet<string>(nsKey(ACCESS_KEY))) ?? null;
  refreshTokenCache = (await storeGet<string>(nsKey(REFRESH_KEY))) ?? null;
}

export function getAccessToken(): string | null {
  return accessTokenCache;
}
export function getRefreshToken(): string | null {
  return refreshTokenCache;
}

export async function setTokens(access: string, refresh?: string): Promise<void> {
  accessTokenCache = access;
  await storeSet(nsKey(ACCESS_KEY), access);
  if (refresh) {
    refreshTokenCache = refresh;
    await storeSet(nsKey(REFRESH_KEY), refresh);
  }
}

export async function clearTokens(): Promise<void> {
  accessTokenCache = null;
  refreshTokenCache = null;
  await storeDelete(nsKey(ACCESS_KEY));
  await storeDelete(nsKey(REFRESH_KEY));
  await storeDelete(nsKey(USER_KEY));
}

export async function getWorkspaceRefreshToken(workspaceId: string): Promise<string | null> {
  return (await storeGet<string>(workspaceAuthStoreKeys(workspaceId).refreshToken)) ?? null;
}

export async function clearWorkspaceSecrets(workspaceId: string): Promise<void> {
  const keys = workspaceAuthStoreKeys(workspaceId);
  await Promise.all([
    storeDelete(keys.accessToken),
    storeDelete(keys.refreshToken),
    storeDelete(keys.user),
  ]);
  if (workspaceId === getActiveWorkspaceId()) {
    accessTokenCache = null;
    refreshTokenCache = null;
  }
}

/**
 * Refresh the access token for the active workspace.
 *
 * Reports `sessionInvalid: true` only when the auth service definitively
 * rejected the refresh token (HTTP 401/403 — it expired or was revoked).
 * Every other failure (network error, proxy 424/5xx, HTML/garbage body) is
 * transient: the session is kept so callers can retry later. This is the
 * difference between "sign in again" and "a proxy blipped for one request".
 */
export async function refreshAccessToken(): Promise<RefreshOutcome> {
  const refreshToken = refreshTokenCache ?? (await storeGet<string>(nsKey(REFRESH_KEY)));
  if (!refreshToken) return { token: null, sessionInvalid: true };
  try {
    const resp = await runtimeFetch(`${backendBaseUrl()}/auth.v1.AuthService/RefreshToken`, {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
        ...getCloudflareAccessHeaders(),
      },
      body: JSON.stringify({ refreshToken }),
    });
    if (resp.status === 401 || resp.status === 403) {
      return { token: null, sessionInvalid: true };
    }
    if (!resp.ok) {
      return { token: null, sessionInvalid: false };
    }
    const data = (await resp.json()) as { accessToken?: unknown; refreshToken?: unknown };
    if (typeof data?.accessToken !== "string" || !data.accessToken) {
      // 2xx with an unexpected body (proxy interception) — treat as transient.
      return { token: null, sessionInvalid: false };
    }
    await setTokens(
      data.accessToken,
      typeof data.refreshToken === "string" && data.refreshToken ? data.refreshToken : undefined,
    );
    return { token: data.accessToken, sessionInvalid: false };
  } catch {
    // Network failure or non-JSON body — transient, keep the session.
    return { token: null, sessionInvalid: false };
  }
}

/**
 * Fired on window when the current session is definitively over (refresh
 * token expired/revoked). AuthContext listens and flips to the sign-in
 * screen in place — no hard reload, so no other in-app state is destroyed.
 */
export const SESSION_EXPIRED_EVENT = "operator:session-expired";

export function notifySessionExpired(): void {
  void clearTokens();
  window.dispatchEvent(new Event(SESSION_EXPIRED_EVENT));
}

function buildClient(useBinaryFormat: boolean) {
  const transport = createConnectTransport({
    baseUrl: backendBaseUrl(),
    useBinaryFormat,
    fetch: runtimeFetch,
    interceptors: [
      createEnvironmentInterceptor(getCloudflareAccessHeaders),
      // Retry sits outside auth so every retry attempt re-enters auth
      // handling (fresh Authorization header, 401 → refresh → retry).
      createRetryInterceptor(),
      createAuthInterceptor(getAccessToken, refreshAccessToken, notifySessionExpired),
    ],
  });
  return createClient(PlatformService, transport);
}

type PlatformClient = ReturnType<typeof buildClient>;
type WireFormat = "json" | "binary";

const clientCache: Record<WireFormat, PlatformClient | null> = { json: null, binary: null };
let clientCacheKey = "";

function getClientInstance(format: WireFormat): PlatformClient {
  const cacheKey = `${getActiveWorkspaceId()}|${backendBaseUrl()}`;
  if (clientCacheKey !== cacheKey) {
    clientCache.json = null;
    clientCache.binary = null;
    clientCacheKey = cacheKey;
  }
  let instance = clientCache[format];
  if (!instance) {
    instance = buildClient(format === "binary");
    clientCache[format] = instance;
  }
  return instance;
}

function clientProxy(format: WireFormat): PlatformClient {
  return new Proxy({} as PlatformClient, {
    get(_target, prop) {
      const target = getClientInstance(format);
      const value = target[prop as keyof PlatformClient];
      return typeof value === "function" ? value.bind(target) : value;
    },
  });
}

export const client = clientProxy("json");

// Binary-format twin of `client` for RPCs that move large byte payloads
// (e.g. the run archive export). The default JSON unary path base64-encodes
// `bytes` fields (~33% inflation) and materializes the whole payload as a
// JSON string before decoding; the binary protobuf wire format carries the
// bytes raw.
export const binaryClient = clientProxy("binary");
