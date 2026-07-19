import { Code, ConnectError } from "@connectrpc/connect";

/**
 * Shared RPC error classification and humanization.
 *
 * The app talks to backends that sit behind proxies/ingresses which can emit
 * non-Connect responses while the backend restarts or is briefly unreachable:
 * bare HTTP statuses (424/502/503/…), HTML or empty error bodies, dropped
 * connections. Those surface from connect-web as ConnectError with code
 * `unknown`/`unavailable`, as raw `TypeError` (fetch network failure), or as
 * raw `SyntaxError` (`response.json()` on a non-JSON body). None of them mean
 * the request was invalid — they are transient infrastructure hiccups and
 * should be retried (for reads) and described to the user as such.
 */

/** Fetch-level network failures across engines (Chromium/WebKit/Gecko/reqwest). */
const NETWORK_MESSAGE_SNIPPETS = [
  "failed to fetch", // Chromium
  "load failed", // Safari / WKWebView (Tauri shell)
  "networkerror", // Firefox
  "network connection was lost",
  "error sending request", // tauri plugin-http (reqwest)
  "connection refused",
  "connection reset",
  "connection closed",
  "socket hang up",
  "fetch failed",
];

/** Malformed/truncated response bodies (proxy HTML pages, cut streams, …). */
const GARBAGE_RESPONSE_SNIPPETS = [
  "unexpected token", // JSON.parse on an HTML error page
  "unexpected end of json",
  "is not valid json", // Chromium JSON.parse
  "json parse error", // WebKit JSON.parse
  "unterminated string", // truncated JSON body
  "cannot decode", // connect-es errorFromJson fallbacks
  "missing response body",
  "missing endstreamresponse",
  "missing request message", // consumed one-shot stream request re-sent
  "unsupported content type", // proxy served text/html instead of JSON
  "premature close",
];

function messageOf(err: unknown): string {
  if (err instanceof ConnectError) return err.rawMessage;
  if (err instanceof Error) return err.message;
  return String(err ?? "");
}

function includesAny(message: string, snippets: readonly string[]): boolean {
  const lower = message.toLowerCase();
  return snippets.some((s) => lower.includes(s));
}

/** The Connect code of an error, or null when it isn't a ConnectError. */
export function connectCodeOf(err: unknown): Code | null {
  return err instanceof ConnectError ? err.code : null;
}

/** True when the caller aborted the request (never retry, never surface). */
export function isAbortError(err: unknown): boolean {
  if (connectCodeOf(err) === Code.Canceled) return true;
  return err instanceof Error && (err.name === "AbortError" || err.name === "TimeoutError");
}

/**
 * The numeric HTTP status embedded in connect-web's fallback errors
 * ("HTTP 424"), or null. Useful for messaging only — classification should go
 * through {@link isTransientRpcError}.
 */
export function httpStatusFromError(err: unknown): number | null {
  const match = /\bHTTP (\d{3})\b/.exec(messageOf(err));
  return match ? Number(match[1]) : null;
}

/**
 * True for errors that never carry a definitive server answer: network
 * failures, proxy error pages (424/5xx/HTML), truncated or non-JSON bodies,
 * dropped streams. Safe to retry for idempotent (read) calls; must never be
 * treated as an invalid session or invalid input.
 */
export function isTransientRpcError(err: unknown): boolean {
  if (isAbortError(err)) return false;
  const code = connectCodeOf(err);
  if (code !== null) {
    // `unavailable` (429/502/503/504), timeouts, and `unknown` (unlisted HTTP
    // statuses like 424, non-Connect bodies, thrown strings) are all "the
    // infrastructure hiccuped", not "the server answered".
    return (
      code === Code.Unavailable ||
      code === Code.DeadlineExceeded ||
      code === Code.Unknown ||
      code === Code.ResourceExhausted
    );
  }
  if (err instanceof TypeError) return true; // fetch network failure
  if (err instanceof SyntaxError) return true; // JSON.parse on a garbage body
  return (
    includesAny(messageOf(err), NETWORK_MESSAGE_SNIPPETS) ||
    includesAny(messageOf(err), GARBAGE_RESPONSE_SNIPPETS)
  );
}

/**
 * Whether an idempotent read may be retried. Slightly broader than
 * {@link isTransientRpcError}: `internal` (k8s apiserver blips, HTTP 400 from
 * proxies) is safe to retry when the call has no side effects.
 */
export function isRetryableReadError(err: unknown): boolean {
  return isTransientRpcError(err) || connectCodeOf(err) === Code.Internal;
}

/**
 * A human-readable, actionable message for an RPC failure. Falls back to the
 * raw server message for meaningful errors (validation, permissions, …).
 */
export function describeRpcError(err: unknown, what = "reach the server"): string {
  const code = connectCodeOf(err);
  if (typeof navigator !== "undefined" && navigator.onLine === false) {
    return `Couldn't ${what}: you appear to be offline. We'll keep retrying — check your connection.`;
  }
  if (isTransientRpcError(err)) {
    const status = httpStatusFromError(err);
    const statusNote = status ? ` (HTTP ${status})` : "";
    return `Couldn't ${what}: the server is temporarily unreachable${statusNote}. This is usually a brief hiccup — retrying should fix it.`;
  }
  if (code === Code.Unauthenticated) {
    return "Your session has expired. Please sign in again.";
  }
  if (code === Code.PermissionDenied) {
    return messageOf(err) || "You don't have permission to do that.";
  }
  const message = messageOf(err);
  return message ? message : `Couldn't ${what}.`;
}
