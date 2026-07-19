/* eslint-disable react-hooks/set-state-in-effect */
import { useCallback, useEffect, useRef, useState } from "react";

import { client } from "@/lib/client";
import { refreshOnUnauthenticated } from "@/lib/auth-interceptor";
import { backoffDelayMs } from "@/hooks/backoff";
import { mergeActivityEntries, subagentGraphFingerprint } from "@/hooks/snapshotMerge";
import type { ActivityEntry, SubagentGraph } from "@/rpc/platform/service_pb";

export const ACTIVITY_PAYLOAD_PREVIEW_BYTES = 2048;
export const ACTIVITY_PAGE_LIMIT = 1000;

type ActivityLogFrame = {
  entries: ActivityEntry[];
  subagentGraph?: SubagentGraph;
  isComplete: boolean;
  delta?: boolean;
  reset?: boolean;
  lastEventId?: bigint;
  firstEventId?: bigint;
  hasMoreBefore?: boolean;
};

/**
 * Apply delta-frame entries to the buffer. Entries at or below lastEventId
 * are dropped (dedupe guard). While a model streams reasoning, the backend
 * re-sends a single live-growing assistant_thinking entry: same type, same
 * non-empty toolUseId and timestampUnix, but a growing message and eventId —
 * that pair is unique per reasoning stream, so such entries replace the
 * buffered version in place instead of appending a duplicate row. Returns
 * `existing` unchanged when nothing new was processed.
 */
export function applyDeltaEntries(
  existing: ActivityEntry[],
  incoming: ActivityEntry[],
  lastEventId: bigint,
): { entries: ActivityEntry[]; lastEventId: bigint } {
  let entries = existing;
  let maxEventId = lastEventId;
  for (const e of incoming) {
    if (e.eventId <= lastEventId) {
      continue;
    }
    if (e.eventId > maxEventId) {
      maxEventId = e.eventId;
    }
    if (entries === existing) {
      entries = [...existing];
    }
    const upsertIdx =
      e.type === "assistant_thinking" && e.toolUseId !== ""
        ? entries.findIndex((p) => p.type === e.type && p.toolUseId === e.toolUseId)
        : -1;
    if (upsertIdx >= 0) {
      entries[upsertIdx] = e;
    } else {
      entries.push(e);
    }
  }
  return { entries, lastEventId: maxEventId };
}

export interface UseActivityLogOptions {
  /** When false, no stream is opened; the last received data is kept. */
  enabled?: boolean;
}

export function useActivityLog(
  namespace: string,
  name: string,
  phase?: string,
  refreshKey?: string,
  options?: UseActivityLogOptions,
) {
  const enabled = options?.enabled ?? true;
  const previousBoundaryRef = useRef<string | undefined>(undefined);
  const [entries, setEntries] = useState<ActivityEntry[]>([]);
  const [subagentGraph, setSubagentGraph] = useState<SubagentGraph | undefined>(undefined);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [isComplete, setIsComplete] = useState(false);
  const [hasMoreBefore, setHasMoreBefore] = useState(false);
  // Mirrors of the entries/pagination state so the stream loop and loadOlder
  // can read the current buffer without re-subscribing on every frame.
  const entriesRef = useRef<ActivityEntry[]>([]);
  const lastEventIdRef = useRef<bigint>(0n);
  const hasMoreBeforeRef = useRef(false);
  const isCompleteRef = useRef(false);
  const loadingOlderRef = useRef(false);

  const boundaryKey = refreshKey ?? phase;
  const resetKey = `${namespace}/${name}/${boundaryKey ?? ""}`;

  useEffect(() => {
    if (!enabled) {
      return;
    }

    let isCancelled = false;
    let activeController: AbortController | null = null;
    let retryTimer: ReturnType<typeof setTimeout> | null = null;
    let retryAttempt = 0;
    const shouldReset = previousBoundaryRef.current !== resetKey;
    let latestIsComplete = shouldReset ? false : isCompleteRef.current;

    previousBoundaryRef.current = resetKey;

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

    function commitEntries(next: ActivityEntry[]): void {
      entriesRef.current = next;
      setEntries(next);
    }

    function commitHasMoreBefore(next: boolean): void {
      hasMoreBeforeRef.current = next;
      setHasMoreBefore(next);
    }

    /** Upsert/append only entries newer than the delta cursor (dedupe guard). */
    function appendNewer(incoming: ActivityEntry[]): void {
      const result = applyDeltaEntries(entriesRef.current, incoming, lastEventIdRef.current);
      lastEventIdRef.current = result.lastEventId;
      if (result.entries === entriesRef.current) {
        return;
      }
      commitEntries(result.entries);
    }

    function applyDeltaFrame(frame: ActivityLogFrame): void {
      if (frame.reset) {
        const frameLast = frame.lastEventId ?? 0n;
        // An empty reset while we already render entries is a "nothing new"
        // resume (reconnect with since_event_id and no new events) or a
        // transiently source-less snapshot — never wipe the timeline for it.
        if (frame.entries.length === 0 && entriesRef.current.length > 0) {
          if (frameLast > lastEventIdRef.current) {
            lastEventIdRef.current = frameLast;
          }
          if (frame.subagentGraph) {
            setSubagentGraph(frame.subagentGraph);
          }
          return;
        }
        // A reset frame normally replaces the buffer (first frame, source
        // flip, id regression). The one exception is a pure resume
        // continuation: we reconnected with since_event_id and the snapshot
        // starts strictly after what we already have — then appending keeps
        // the older, already-loaded prefix intact.
        const isResumeContinuation =
          entriesRef.current.length > 0 &&
          frame.entries.length > 0 &&
          (frame.firstEventId ?? 0n) > lastEventIdRef.current;
        if (isResumeContinuation) {
          appendNewer(frame.entries);
        } else {
          commitEntries(frame.entries);
          commitHasMoreBefore(frame.hasMoreBefore ?? false);
        }
      } else {
        appendNewer(frame.entries);
      }
      const frameLast = frame.lastEventId ?? 0n;
      if (frameLast > 0n || frame.reset) {
        lastEventIdRef.current = frameLast;
      }
      // Delta frames omit the graph when it is unchanged.
      if (frame.subagentGraph) {
        setSubagentGraph(frame.subagentGraph);
      }
    }

    function applyLegacyFrame(frame: ActivityLogFrame): void {
      commitEntries(mergeActivityEntries(entriesRef.current, frame.entries));
      const frameLast = frame.lastEventId ?? 0n;
      if (frameLast > 0n) {
        lastEventIdRef.current = frameLast;
      }
      setSubagentGraph((prev) =>
        subagentGraphFingerprint(prev) === subagentGraphFingerprint(frame.subagentGraph)
          ? prev
          : frame.subagentGraph,
      );
    }

    function applyResponse(frame: ActivityLogFrame): void {
      if (isCancelled) {
        return;
      }

      retryAttempt = 0;
      latestIsComplete = frame.isComplete;
      isCompleteRef.current = frame.isComplete;
      if (frame.delta) {
        applyDeltaFrame(frame);
      } else {
        applyLegacyFrame(frame);
      }
      setIsComplete(frame.isComplete);
      setLoading(false);
      setError(null);
    }

    async function fetchFallback(): Promise<ActivityLogFrame | null> {
      try {
        const response = await client.getActivityLog({ namespace, name });
        applyResponse(response);
        return response;
      } catch (fallbackError) {
        if (isCancelled) {
          return null;
        }

        setError(fallbackError instanceof Error ? fallbackError.message : "Failed to fetch activity log");
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
          const request = {
            namespace,
            name,
            delta: true,
            payloadPreviewBytes: ACTIVITY_PAYLOAD_PREVIEW_BYTES,
            limit: ACTIVITY_PAGE_LIMIT,
            sinceEventId: entriesRef.current.length > 0 ? lastEventIdRef.current : 0n,
          };
          for await (const update of client.watchActivityLog(request, { signal: controller.signal })) {
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
            setError(null);
            continue;
          }

          if (!sawFrame) {
            const response = await fetchFallback();
            if (response?.isComplete) {
              return;
            }
          } else {
            setError(streamError instanceof Error ? streamError.message : "Failed to stream activity log");
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

    if (shouldReset) {
      entriesRef.current = [];
      lastEventIdRef.current = 0n;
      hasMoreBeforeRef.current = false;
      setEntries([]);
      setSubagentGraph(undefined);
      latestIsComplete = false;
      isCompleteRef.current = false;
      setIsComplete(false);
      setHasMoreBefore(false);
    }
    setLoading(true);
    setError(null);

    void run();

    return () => {
      isCancelled = true;
      clearRetryTimer();
      activeController?.abort();
    };
  }, [namespace, name, boundaryKey, resetKey, enabled]);

  const loadOlder = useCallback(async (): Promise<void> => {
    if (loadingOlderRef.current || !hasMoreBeforeRef.current) {
      return;
    }
    const first = entriesRef.current[0];
    // Entries from non-delta-capable sources carry no meaningful ids;
    // pagination is impossible there.
    if (!first || first.eventId === 0n) {
      return;
    }
    loadingOlderRef.current = true;
    try {
      const response = await client.getActivityLog({
        namespace,
        name,
        beforeEventId: first.eventId,
        limit: ACTIVITY_PAGE_LIMIT,
        payloadPreviewBytes: ACTIVITY_PAYLOAD_PREVIEW_BYTES,
      });
      // A reset frame may have replaced the buffer while the page was in
      // flight; drop the stale page in that case.
      if (entriesRef.current[0] !== first) {
        return;
      }
      const older = response.entries ?? [];
      if (older.length > 0) {
        entriesRef.current = [...older, ...entriesRef.current];
        setEntries(entriesRef.current);
      }
      hasMoreBeforeRef.current = response.hasMoreBefore ?? false;
      setHasMoreBefore(hasMoreBeforeRef.current);
    } catch {
      // Keep hasMoreBefore so the user can retry by scrolling again.
    } finally {
      loadingOlderRef.current = false;
    }
  }, [namespace, name]);

  return { entries, subagentGraph, loading, error, isComplete, hasMoreBefore, loadOlder };
}
