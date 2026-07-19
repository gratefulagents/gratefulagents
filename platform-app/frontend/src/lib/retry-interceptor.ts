import type { Interceptor } from "@connectrpc/connect";
import { isRetryableReadError } from "./rpc-errors";

/**
 * Automatic retry for idempotent unary reads.
 *
 * Backends behind proxies intermittently answer with junk while restarting
 * (HTTP 424/5xx, HTML bodies, dropped connections). For calls with no side
 * effects — Get, List, Watch, Read, Export, Search, Wait — a failed attempt
 * can simply be re-sent, which absorbs the blip instead of surfacing
 * "[unknown] HTTP 424" or raw JSON parse errors to the UI.
 *
 * Mutations (Create/Update/Delete/…) are never retried automatically: a
 * "transient" proxy error can arrive after the backend already applied the
 * change, and re-sending could apply it twice.
 *
 * Placement: list this interceptor *before* the auth interceptor so every
 * retry attempt re-enters auth handling (fresh token, 401→refresh→retry).
 * Streaming calls are excluded — their one-shot request iterable cannot be
 * re-sent; stream consumers own reconnection (see hooks/watchStore.ts).
 */

/** Verbs (from rpc/platform + rpc/auth) that are safe to re-send. */
const READ_METHOD_PATTERN = /^(Get|List|Watch|Read|Export|Search|BatchGet|Wait)[A-Z0-9]?/;

export interface RetryOptions {
  /** Total attempts including the first one. */
  maxAttempts?: number;
  /** First backoff ceiling in ms; doubles per attempt (full jitter). */
  baseDelayMs?: number;
  /** Upper bound for a single backoff wait in ms. */
  maxDelayMs?: number;
  /** Injectable RNG for tests. */
  random?: () => number;
}

function retryDelayMs(attempt: number, opts: Required<RetryOptions>): number {
  const ceiling = Math.min(opts.maxDelayMs, opts.baseDelayMs * 2 ** Math.max(0, attempt));
  return Math.floor(opts.random() * ceiling);
}

function sleep(ms: number, signal?: AbortSignal): Promise<void> {
  return new Promise((resolve, reject) => {
    if (signal?.aborted) {
      reject(signal.reason instanceof Error ? signal.reason : new Error("aborted"));
      return;
    }
    const timer = setTimeout(() => {
      signal?.removeEventListener("abort", onAbort);
      resolve();
    }, ms);
    function onAbort(this: AbortSignal) {
      clearTimeout(timer);
      reject(this.reason instanceof Error ? this.reason : new Error("aborted"));
    }
    signal?.addEventListener("abort", onAbort, { once: true });
  });
}

export function isRetryableReadMethod(methodName: string): boolean {
  return READ_METHOD_PATTERN.test(methodName);
}

export function createRetryInterceptor(options: RetryOptions = {}): Interceptor {
  const opts: Required<RetryOptions> = {
    maxAttempts: options.maxAttempts ?? 4,
    baseDelayMs: options.baseDelayMs ?? 250,
    maxDelayMs: options.maxDelayMs ?? 2000,
    random: options.random ?? Math.random,
  };

  return (next) => async (req) => {
    // Streams get a single shot here; unary mutations too.
    if (req.stream || !isRetryableReadMethod(req.method.name)) {
      return next(req);
    }
    let attempt = 0;
    for (;;) {
      try {
        return await next(req);
      } catch (err) {
        attempt++;
        if (
          attempt >= opts.maxAttempts ||
          req.signal.aborted ||
          !isRetryableReadError(err)
        ) {
          throw err;
        }
        // Waits are aborted (and the original error re-thrown) if the caller
        // cancels the request while we're backing off.
        try {
          await sleep(retryDelayMs(attempt - 1, opts), req.signal);
        } catch {
          throw err;
        }
      }
    }
  };
}
