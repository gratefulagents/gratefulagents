import { useEffect, useReducer, useRef, useState } from "react";

import { client } from "@/lib/client";
import { refreshOnUnauthenticated } from "@/lib/auth-interceptor";
import { isDonePhase } from "@/lib/status";
import { backoffDelayMs } from "@/hooks/backoff";

export interface DiffResult {
  diff: string;
  isComplete: boolean;
  truncated: boolean;
  source: string;
  newFiles: string[];
  newFilesTruncated: boolean;
  loading: boolean;
  error: string | null;
}

export interface UseDiffOptions {
  /**
   * When false, the 750ms watch stream is not opened; instead a lightweight
   * unary probe keeps `diff` fresh enough for presence checks (e.g. the
   * header's Create PR button), polling slowly while the run is active and
   * refreshing once on the terminal transition.
   */
  enabled?: boolean;
}

/** How often the disabled-tab probe refetches the diff for an active run. */
export const DIFF_PROBE_INTERVAL_MS = 60_000;

type DiffState = DiffResult;

type DiffAction =
  | { type: "start" }
  | {
      type: "success";
      diff: string;
      isComplete: boolean;
      truncated: boolean;
      source: string;
      newFiles: string[];
      newFilesTruncated: boolean;
    }
  | { type: "error"; error: string; preserveData: boolean };

const initialState: DiffState = {
  diff: "",
  isComplete: false,
  truncated: false,
  source: "",
  newFiles: [],
  newFilesTruncated: false,
  loading: true,
  error: null,
};

function diffReducer(state: DiffState, action: DiffAction): DiffState {
  switch (action.type) {
    case "start":
      return {
        ...initialState,
        loading: true,
      };
    case "success":
      return {
        diff: action.diff,
        isComplete: action.isComplete,
        truncated: action.truncated,
        source: action.source,
        newFiles: action.newFiles,
        newFilesTruncated: action.newFilesTruncated,
        loading: false,
        error: null,
      };
    case "error":
      if (action.preserveData) {
        return {
          ...state,
          loading: false,
          error: action.error,
        };
      }
      return {
        ...initialState,
        loading: false,
        error: action.error,
      };
    default:
      return state;
  }
}

type DiffResponse = {
  diff: string;
  isComplete: boolean;
  truncated: boolean;
  source: string;
  newFiles: string[];
  newFilesTruncated: boolean;
};

export function useDiff(
  namespace: string,
  name: string,
  phase?: string,
  resourceType = "AgentRun",
  repoPath = "",
  options?: UseDiffOptions,
): DiffResult {
  const enabled = options?.enabled ?? true;
  const [state, dispatch] = useReducer(diffReducer, initialState);
  // Phase transitions must not tear down / reopen the stream (the stream
  // itself follows the run); the only phase-driven behavior is one refresh
  // once the run reaches a terminal phase, so the final diff is fetched even
  // if the previous stream attempt had already stopped.
  const phaseRef = useRef(phase);
  const previousPhaseRef = useRef(phase);
  const previousResourceRef = useRef<string | undefined>(undefined);
  const [terminalRefresh, setTerminalRefresh] = useState(0);

  useEffect(() => {
    phaseRef.current = phase;
    const previousPhase = previousPhaseRef.current;
    previousPhaseRef.current = phase;
    if (phase && phase !== previousPhase && isDonePhase(phase)) {
      setTerminalRefresh((n) => n + 1);
    }
  }, [phase]);

  const resourceKey = `${namespace}/${name}/${resourceType}/${repoPath}`;

  // Disabled-tab probe: one unary getDiff on mount / resource change /
  // terminal transition, plus a slow poll while the run is active, so
  // diff-presence UI (Create PR button) works without opening the Diff tab.
  useEffect(() => {
    if (enabled) {
      return;
    }

    let cancelled = false;
    let timer: ReturnType<typeof setTimeout> | null = null;

    if (previousResourceRef.current !== resourceKey) {
      previousResourceRef.current = resourceKey;
      dispatch({ type: "start" });
    }

    const schedule = () => {
      if (!cancelled && !isDonePhase(phaseRef.current ?? "")) {
        timer = setTimeout(() => void probe(), DIFF_PROBE_INTERVAL_MS);
      }
    };

    async function probe(): Promise<void> {
      try {
        const response = await client.getDiff({ namespace, name, resourceType, repoPath });
        if (cancelled) {
          return;
        }
        dispatch({
          type: "success",
          diff: response.diff,
          isComplete: response.isComplete,
          truncated: response.truncated,
          source: response.source,
          newFiles: response.newFiles ?? [],
          newFilesTruncated: response.newFilesTruncated ?? false,
        });
        if (!response.isComplete) {
          schedule();
        }
      } catch {
        // Keep whatever data we have; retry on the slow cadence while the
        // run is active (the watch path owns real error surfacing).
        schedule();
      }
    }

    void probe();

    return () => {
      cancelled = true;
      if (timer !== null) {
        clearTimeout(timer);
      }
    };
  }, [namespace, name, resourceType, repoPath, resourceKey, enabled, terminalRefresh]);

  useEffect(() => {
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

    function applyResponse(response: DiffResponse): void {
      if (isCancelled) {
        return;
      }

      retryAttempt = 0;
      latestIsComplete = response.isComplete;
      dispatch({
        type: "success",
        diff: response.diff,
        isComplete: response.isComplete,
        truncated: response.truncated,
        source: response.source,
        newFiles: response.newFiles ?? [],
        newFilesTruncated: response.newFilesTruncated ?? false,
      });
    }

    function applyError(error: string, preserveData: boolean): void {
      if (isCancelled) {
        return;
      }

      dispatch({ type: "error", error, preserveData });
    }

    async function fetchFallback(): Promise<DiffResponse | null> {
      try {
        const response = await client.getDiff({ namespace, name, resourceType, repoPath });
        applyResponse(response);
        return response;
      } catch (fallbackError) {
        applyError(fallbackError instanceof Error ? fallbackError.message : "Failed to fetch diff", false);
        return null;
      }
    }

    async function run(): Promise<void> {
      while (!isCancelled) {
        let sawFrame = false;
        const controller = new AbortController();
        activeController = controller;

        try {
          for await (const update of client.watchDiff({ namespace, name, resourceType, repoPath }, { signal: controller.signal })) {
            sawFrame = true;
            applyResponse(update);
            if (update.isComplete) {
              return;
            }
          }

          if (isCancelled || controller.signal.aborted) {
            return;
          }

          const response = await fetchFallback();
          if (response?.isComplete) {
            return;
          }
        } catch (streamError) {
          if (isCancelled || controller.signal.aborted) {
            return;
          }

          const refreshed = await refreshOnUnauthenticated(streamError);
          if (refreshed) {
            continue;
          }

          if (!sawFrame) {
            const response = await fetchFallback();
            if (response?.isComplete) {
              return;
            }
          } else {
            applyError(streamError instanceof Error ? streamError.message : "Failed to stream diff", true);
          }
        } finally {
          if (activeController === controller) {
            activeController = null;
          }
        }

        if (latestIsComplete || isCancelled) {
          return;
        }

        // A closed stream on a finished run means there is nothing left to
        // poll for — the terminal-refresh effect already triggered the final
        // snapshot fetch.
        if (sawFrame && isDonePhase(phaseRef.current ?? "")) {
          return;
        }

        await waitForRetry();
      }
    }

    // Only wipe the current diff when the underlying resource changed;
    // enable-toggles and terminal refreshes keep showing cached data while
    // the fresh snapshot loads.
    if (previousResourceRef.current !== resourceKey) {
      previousResourceRef.current = resourceKey;
      dispatch({ type: "start" });
    }
    latestIsComplete = false;
    void run();

    return () => {
      isCancelled = true;
      clearRetryTimer();
      activeController?.abort();
    };
  }, [namespace, name, resourceType, repoPath, resourceKey, enabled, terminalRefresh]);

  return state;
}
