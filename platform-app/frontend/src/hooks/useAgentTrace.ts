/* eslint-disable react-hooks/set-state-in-effect */
import { useEffect, useRef, useState } from "react";

import { client } from "@/lib/client";
import { refreshOnUnauthenticated } from "@/lib/auth-interceptor";
import { backoffDelayMs } from "@/hooks/backoff";
import type { GetAgentTraceResponse, TraceSpan } from "@/rpc/platform/service_pb";

export interface UseAgentTraceOptions {
  /** When false, no stream is opened; the last received data is kept. */
  enabled?: boolean;
}

export function useAgentTrace(
  namespace: string,
  name: string,
  traceId?: string,
  phase?: string,
  options?: UseAgentTraceOptions,
) {
  const enabled = options?.enabled ?? true;
  const [trace, setTrace] = useState<GetAgentTraceResponse | undefined>(undefined);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const previousTraceKeyRef = useRef<string | undefined>(undefined);

  const traceKey = traceId ? `${namespace}/${name}/${traceId}` : undefined;

  useEffect(() => {
    if (!traceId) {
      previousTraceKeyRef.current = undefined;
      setTrace(undefined);
      setLoading(false);
      setError(null);
      return;
    }

    if (!enabled) {
      return;
    }

    let isCancelled = false;
    let activeController: AbortController | null = null;
    let retryTimer: ReturnType<typeof setTimeout> | null = null;
    let retryAttempt = 0;
    let latestIsComplete = false;

    function clearRetryTimer(): void {
      if (retryTimer !== null) {
        clearTimeout(retryTimer);
        retryTimer = null;
      }
    }

    function waitForRetry(): Promise<void> {
      return new Promise((resolve) => {
        retryTimer = setTimeout(() => {
          retryTimer = null;
          resolve();
        }, backoffDelayMs(retryAttempt++));
      });
    }

    // The backend marks a trace complete only once the agent run reaches a
    // terminal phase. We must keep streaming until then — otherwise the
    // waterfall freezes on the first frame (which often has zero / partial
    // spans while the run is still warming up).
    function isTraceComplete(response: GetAgentTraceResponse): boolean {
      return response.isComplete;
    }

    function applyResponse(response: GetAgentTraceResponse): void {
      if (isCancelled) {
        return;
      }

      retryAttempt = 0;
      latestIsComplete = isTraceComplete(response);
      setTrace(response);
      setLoading(false);
      setError(null);
    }

    async function fetchFallback(): Promise<GetAgentTraceResponse | null> {
      try {
        const response = await client.getAgentTrace({ namespace, name });
        applyResponse(response);
        return response;
      } catch (fallbackError) {
        if (isCancelled) {
          return null;
        }

        setError(fallbackError instanceof Error ? fallbackError.message : "Failed to fetch trace");
        setLoading(false);
        return null;
      }
    }

    async function run(): Promise<void> {
      while (!isCancelled) {
        let sawFrame = false;
        const controller = new AbortController();
        activeController = controller;

        try {
          for await (const update of client.watchAgentTrace({ namespace, name }, { signal: controller.signal })) {
            sawFrame = true;
            applyResponse(update);
            if (isTraceComplete(update)) {
              return;
            }
          }

          if (isCancelled || controller.signal.aborted) {
            return;
          }

          const response = await fetchFallback();
          if (response && isTraceComplete(response)) {
            return;
          }
        } catch (streamError) {
          if (isCancelled || controller.signal.aborted) {
            return;
          }

          const refreshed = await refreshOnUnauthenticated(streamError);
          if (refreshed) {
            setError(null);
            continue;
          }

          if (!sawFrame) {
            const response = await fetchFallback();
            if (response && isTraceComplete(response)) {
              return;
            }
          } else {
            setError(streamError instanceof Error ? streamError.message : "Failed to stream trace");
            setLoading(false);
          }
        } finally {
          if (activeController === controller) {
            activeController = null;
          }
        }

        if (latestIsComplete || isCancelled) {
          return;
        }

        await waitForRetry();
      }
    }

    // Only wipe the cached trace when the trace itself changed; re-enabling
    // the same trace shows cached data while the fresh snapshot streams in.
    if (previousTraceKeyRef.current !== traceKey) {
      previousTraceKeyRef.current = traceKey;
      setTrace(undefined);
    }
    latestIsComplete = false;
    setLoading(true);
    setError(null);

    void run();

    return () => {
      isCancelled = true;
      clearRetryTimer();
      activeController?.abort();
    };
  }, [namespace, name, traceId, traceKey, phase, enabled]);

  return { trace, loading, error };
}

export type { TraceSpan };
