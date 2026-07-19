import { useState, useEffect } from "react";
import { Code, ConnectError } from "@connectrpc/connect";
import { client } from "@/lib/client";
import { refreshOnUnauthenticated } from "@/lib/auth-interceptor";
import { isDonePhase } from "@/lib/status";
import { backoffDelayMs } from "@/hooks/backoff";
import { mergeAgentRun } from "@/hooks/snapshotMerge";
import type { AgentRun } from "@/rpc/platform/service_pb";

// A freshly created run can briefly be reported as NotFound while the backend
// cache catches up with the create. Within this window we keep showing the
// loading state and retry instead of surfacing a "run not found" error that
// looks like the run disappeared.
const NOT_FOUND_GRACE_MS = 15_000;

function isNotFound(err: unknown): boolean {
  return ConnectError.from(err).code === Code.NotFound;
}

export function useAgentRun(namespace: string, name: string) {
  const [run, setRun] = useState<AgentRun | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  // True while the run is NotFound but still within the startup grace window.
  const [starting, setStarting] = useState(false);

  useEffect(() => {
    let cancelled = false;
    let retryTimer: ReturnType<typeof setTimeout> | null = null;
    let activeController: AbortController | null = null;
    let sawRun = false;
    // Phase of the last received run: once terminal, the backend has sent the
    // final snapshot and closed the stream — reconnecting would just
    // re-download the full run forever.
    let lastPhase: string | null = null;
    const notFoundDeadline = Date.now() + NOT_FOUND_GRACE_MS;

    // NotFound before we ever saw the run, within the grace window, means the
    // run is most likely still being created — keep loading and retry.
    function isStillStarting(err: unknown): boolean {
      return !sawRun && isNotFound(err) && Date.now() < notFoundDeadline;
    }

    function waitForRetry(delayMs: number): Promise<void> {
      return new Promise((resolve) => {
        retryTimer = setTimeout(() => {
          retryTimer = null;
          resolve();
        }, delayMs);
      });
    }

    (async () => {
      await Promise.resolve();
      if (cancelled) {
        return;
      }
      setRun(null);
      setLoading(true);
      setStarting(false);
      setError(null);
      while (!cancelled) {
        try {
          const res = await client.getAgentRun({ namespace, name });
          if (!cancelled) {
            sawRun = true;
            setRun((prev) => mergeAgentRun(prev, res));
            setStarting(false);
            setLoading(false);
          }
          return;
        } catch (e) {
          if (cancelled || sawRun) {
            // The watch stream may have delivered the run already.
            return;
          }
          if (isStillStarting(e)) {
            setStarting(true);
            await waitForRetry(1000);
            continue;
          }
          setError(e instanceof Error ? e.message : "Failed to fetch agent run");
          setStarting(false);
          setLoading(false);
          return;
        }
      }
    })();

    (async () => {
      let attempt = 0;
      while (!cancelled) {
        const controller = new AbortController();
        activeController = controller;
        try {
          for await (const update of client.watchAgentRun(
            { namespace, name },
            { signal: controller.signal }
          )) {
            if (!cancelled) {
              sawRun = true;
              attempt = 0;
              lastPhase = update.phase ?? null;
              setRun((prev) => mergeAgentRun(prev, update));
              setStarting(false);
              setLoading(false);
              setError(null);
            }
          }
        } catch (e) {
          if (cancelled || controller.signal.aborted) {
            return;
          }
          if (!(await refreshOnUnauthenticated(e)) && !isStillStarting(e)) {
            setError(e instanceof Error ? e.message : "Failed to stream agent run");
          }
        } finally {
          if (activeController === controller) {
            activeController = null;
          }
        }
        if (lastPhase !== null && isDonePhase(lastPhase)) {
          // Terminal run: the stream delivered the final snapshot; don't
          // reconnect.
          return;
        }
        if (!cancelled) {
          await waitForRetry(backoffDelayMs(attempt++));
        }
      }
    })();

    return () => {
      cancelled = true;
      if (retryTimer) {
        clearTimeout(retryTimer);
      }
      activeController?.abort();
    };
  }, [namespace, name]);

  return { run, loading, error, starting };
}
