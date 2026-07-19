import { Code, type Interceptor } from "@connectrpc/connect";

/**
 * Outcome of a token refresh attempt.
 *
 * `sessionInvalid` must only be true when the backend definitively rejected
 * the refresh token (it expired or was revoked). Transient failures — network
 * errors, proxy 424/5xx pages, garbage bodies — must report
 * `{ token: null, sessionInvalid: false }` so a flaky connection never logs
 * the user out (or, worse, reloads the app and destroys in-progress work).
 */
export interface RefreshOutcome {
  token: string | null;
  sessionInvalid: boolean;
}

type RefreshAccessToken = () => Promise<RefreshOutcome>;
type UnauthorizedHandler = () => void;

let refreshInFlight: Promise<RefreshOutcome> | null = null;
let latestRefreshAccessToken: RefreshAccessToken | null = null;
let latestOnUnauthorized: UnauthorizedHandler | null = null;

function isUnauthenticated(err: unknown): boolean {
  return Boolean(
    err &&
      typeof err === "object" &&
      "code" in err &&
      (err as { code: number }).code === Code.Unauthenticated,
  );
}

async function refreshOnce(refreshAccessToken: RefreshAccessToken): Promise<RefreshOutcome> {
  refreshInFlight ??= refreshAccessToken().finally(() => {
    refreshInFlight = null;
  });
  return refreshInFlight;
}

/**
 * If `err` is an Unauthenticated RPC failure, try to refresh the access
 * token. Returns true when a fresh token is available and the caller should
 * retry. Only a *definitive* refresh rejection tears the session down;
 * transient refresh failures leave the session alone so the caller's normal
 * retry/reconnect path can try again later.
 */
export async function refreshOnUnauthenticated(err: unknown): Promise<boolean> {
  if (!isUnauthenticated(err) || !latestRefreshAccessToken) {
    return false;
  }

  const outcome = await refreshOnce(latestRefreshAccessToken);
  if (outcome.token) {
    return true;
  }
  if (outcome.sessionInvalid) {
    latestOnUnauthorized?.();
  }
  return false;
}

export function createAuthInterceptor(
  getAccessToken: () => string | null,
  refreshAccessToken: RefreshAccessToken,
  onUnauthorized: UnauthorizedHandler,
): Interceptor {
  latestRefreshAccessToken = refreshAccessToken;
  latestOnUnauthorized = onUnauthorized;

  return (next) => async (req) => {
    const token = getAccessToken();
    if (token) {
      req.header.set("Authorization", `Bearer ${token}`);
    }
    try {
      return await next(req);
    } catch (err: unknown) {
      // Streaming requests carry a one-shot request iterable: it was consumed
      // by the failed attempt, so re-invoking next(req) would send an empty
      // request ("missing request message" client-side, or a JSON decode
      // error server-side). Refresh the token so the caller's reconnect loop
      // succeeds, then let the error propagate.
      if (req.stream) {
        await refreshOnUnauthenticated(err);
        throw err;
      }
      if (await refreshOnUnauthenticated(err)) {
        const newToken = getAccessToken();
        if (newToken) {
          req.header.set("Authorization", `Bearer ${newToken}`);
          return await next(req);
        }
      }
      throw err;
    }
  };
}
