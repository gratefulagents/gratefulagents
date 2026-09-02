import { useState, useEffect } from "react";
import { Code, ConnectError } from "@connectrpc/connect";
import { client } from "@/lib/client";
import { refreshOnUnauthenticated } from "@/lib/auth-interceptor";
import { isDonePhase } from "@/lib/status";
import { backoffDelayMs } from "@/hooks/backoff";
import { mergeAgentRun } from "@/hooks/snapshotMerge";
import { createRetryWaiter, installVisibilityResume, STREAM_RESUME_IDLE_MS } from "@/hooks/useActivityLog";
import type { AgentRun } from "@/rpc/platform/service_pb";

// A freshly created run can briefly be reported as NotFound while the backend
// cache catches up with the create. Within this window we keep showing the
// loading state and retry instead of surfacing a "run not found" error that
// looks like the run disappeared.
const NOT_FOUND_GRACE_MS = 15_000;

// The first watch frame is a full snapshot, so the unary GET is only a
// fallback for a watch that stays silent (proxy buffering, slow handshake).
export const SNAPSHOT_FALLBACK_MS = 1_500;

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
    let activeController: AbortController | null = null;
    let sawRun = false;
    let lastFrameAt = Date.now();
    // Bumped whenever the watch loop is replaced (foreground resume); a loop
    // whose generation is stale exits instead of reconnecting.
    let watchGeneration = 0;
    // Phase of the last received run: once terminal, the backend has sent the
    // final snapshot and closed the stream — reconnecting would just
    // re-download the full run forever.
    let lastPhase: string | null = null;
    const notFoundDeadline = Date.now() + NOT_FOUND_GRACE_MS;
    const snapshotRetry = createRetryWaiter();
    const watchRetry = createRetryWaiter();
    let fallbackTimer: ReturnType<typeof setTimeout> | null = null;

    // NotFound before we ever saw the run, within the grace window, means the
    // run is most likely still being created — keep loading and retry.
    function isStillStarting(err: unknown): boolean {
      return !sawRun && isNotFound(err) && Date.now() < notFoundDeadline;
    }

    function clearFallbackTimer(): void {
      if (fallbackTimer !== null) {
        clearTimeout(fallbackTimer);
        fallbackTimer = null;
      }
    }

    async function fetchSnapshot(): Promise<void> {
      while (!cancelled && !sawRun) {
        try {
          const res = await client.getAgentRun({ namespace, name });
          if (!cancelled && !sawRun) {
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
            await snapshotRetry.wait(1000);
            continue;
          }
          setError(e instanceof Error ? e.message : "Failed to fetch agent run");
          setStarting(false);
          setLoading(false);
          return;
        }
      }
    }

    async function watchLoop(generation: number): Promise<void> {
      let attempt = 0;
      while (!cancelled && generation === watchGeneration) {
        const controller = new AbortController();
        activeController = controller;
        try {
          for await (const update of client.watchAgentRun(
            { namespace, name },
            { signal: controller.signal }
          )) {
            if (!cancelled && generation === watchGeneration) {
              sawRun = true;
              clearFallbackTimer();
              lastFrameAt = Date.now();
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
        if (!cancelled && generation === watchGeneration) {
          await watchRetry.wait(backoffDelayMs(attempt++));
        }
      }
    }

    const removeVisibilityResume = installVisibilityResume(
      () => Date.now() - lastFrameAt > STREAM_RESUME_IDLE_MS,
      () => {
        if (cancelled || (lastPhase !== null && isDonePhase(lastPhase))) {
          return;
        }
        // A stream left open while the page was hidden may be dead without
        // erroring; retire it and open a fresh one right away.
        const generation = ++watchGeneration;
        activeController?.abort();
        activeController = null;
        watchRetry.cancel();
        lastFrameAt = Date.now();
        void watchLoop(generation);
      },
    );

    (async () => {
      await Promise.resolve();
      if (cancelled) {
        return;
      }
      setRun(null);
      setLoading(true);
      setStarting(false);
      setError(null);
      fallbackTimer = setTimeout(() => {
        fallbackTimer = null;
        void fetchSnapshot();
      }, SNAPSHOT_FALLBACK_MS);
      void watchLoop(watchGeneration);
    })();

    return () => {
      cancelled = true;
      clearFallbackTimer();
      snapshotRetry.cancel();
      watchRetry.cancel();
      removeVisibilityResume();
      activeController?.abort();
    };
  }, [namespace, name]);

  return { run, loading, error, starting };
}
