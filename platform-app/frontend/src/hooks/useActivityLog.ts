/* eslint-disable react-hooks/set-state-in-effect */
import { useCallback, useEffect, useRef, useState } from "react";

import { client } from "@/lib/client";
import { refreshOnUnauthenticated } from "@/lib/auth-interceptor";
import { backoffDelayMs } from "@/hooks/backoff";
import { mergeActivityEntries, subagentGraphFingerprint } from "@/hooks/snapshotMerge";
import type { ActivityEntry, SubagentGraph } from "@/rpc/platform/service_pb";

export const ACTIVITY_PAYLOAD_PREVIEW_BYTES = 2048;
export const ACTIVITY_PAGE_LIMIT = 1000;
/** A stream that has not produced a frame for this long when the page
 * returns to the foreground is reopened rather than trusted. */
export const STREAM_RESUME_IDLE_MS = 5_000;

/**
 * A cancellable retry delay. `cancel()` clears the pending timer and resolves
 * the waiter so the owning loop wakes up and re-checks its exit conditions.
 */
export function createRetryWaiter(): { wait(ms: number): Promise<void>; cancel(): void } {
  let timer: ReturnType<typeof setTimeout> | null = null;
  let resolvePending: (() => void) | null = null;
  return {
    wait(ms: number): Promise<void> {
      return new Promise((resolve) => {
        resolvePending = resolve;
        timer = setTimeout(() => {
          timer = null;
          resolvePending = null;
          resolve();
        }, ms);
      });
    },
    cancel(): void {
      if (timer !== null) {
        clearTimeout(timer);
        timer = null;
      }
      resolvePending?.();
      resolvePending = null;
    },
  };
}

/**
 * Calls `onResume` when the document becomes visible while `isIdle()` reports
 * the stream has been silent for too long (a connection left open across a
 * background suspension is often dead without ever erroring). Returns the
 * uninstaller.
 */
export function installVisibilityResume(isIdle: () => boolean, onResume: () => void): () => void {
  if (typeof document === "undefined") {
    return () => {};
  }
  const handler = (): void => {
    if (document.visibilityState === "visible" && isIdle()) {
      onResume();
    }
  };
  document.addEventListener("visibilitychange", handler);
  return () => document.removeEventListener("visibilitychange", handler);
}

type ActivityLogFrame = {
  entries: ActivityEntry[];
  subagentGraph?: SubagentGraph;
  isComplete: boolean;
  delta?: boolean;
  reset?: boolean;
  lastEventId?: bigint;
  firstEventId?: bigint;
  hasMoreBefore?: boolean;
  resume?: boolean;
  eventIdsDurable?: boolean;
};

/**
 * Apply delta-frame entries to the buffer. Entries at or below lastEventId
 * are dropped (dedupe guard). While a model streams reasoning, the backend
 * re-sends a single live-growing assistant_thinking entry: same type, same
 * non-empty toolUseId, but a growing message and eventId — that pair is
 * unique per reasoning stream, so the buffered version is removed and the
 * new one appended at the end. The server snapshot places the merged entry
 * at its newest constituent's slot, so live and reload order agree. Returns
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
      entries.splice(upsertIdx, 1);
    }
    entries.push(e);
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
  // Whether lastEventIdRef holds durable ids that are valid as a
  // since_event_id cursor across reconnects (synthetic ordinals are not).
  const cursorDurableRef = useRef(false);
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
    let retryAttempt = 0;
    let generation = 0;
    let lastFrameAt = Date.now();
    const retry = createRetryWaiter();
    const shouldReset = previousBoundaryRef.current !== resetKey;
    let latestIsComplete = shouldReset ? false : isCompleteRef.current;

    previousBoundaryRef.current = resetKey;

    function waitForRetry(): Promise<void> {
      return retry.wait(backoffDelayMs(retryAttempt++));
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
        // A reset frame replaces the buffer (first frame, source flip, id
        // regression) unless the server marks it as continuing our
        // since_event_id cursor — then appending keeps the older,
        // already-loaded prefix intact.
        if (frame.resume === true && entriesRef.current.length > 0) {
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

    function applyLegacyFrame(frame: ActivityLogFrame, wasDurable: boolean): void {
      commitEntries(
        mergeActivityEntries(entriesRef.current, frame.entries, {
          replace: wasDurable && !(frame.eventIdsDurable ?? false),
        }),
      );
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
      lastFrameAt = Date.now();
      latestIsComplete = frame.isComplete;
      isCompleteRef.current = frame.isComplete;
      const wasDurable = cursorDurableRef.current;
      cursorDurableRef.current = frame.eventIdsDurable ?? false;
      if (frame.delta) {
        applyDeltaFrame(frame);
      } else {
        applyLegacyFrame(frame, wasDurable);
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

    async function run(myGeneration: number): Promise<void> {
      while (!isCancelled && myGeneration === generation) {
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
            // Only durable ids survive a reconnect; otherwise ask for a full
            // snapshot, which arrives as a replacing reset frame.
            sinceEventId:
              cursorDurableRef.current && entriesRef.current.length > 0 ? lastEventIdRef.current : 0n,
          };
          for await (const update of client.watchActivityLog(request, { signal: controller.signal })) {
            if (myGeneration !== generation) {
              return;
            }
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

        if (latestIsComplete || isCancelled || myGeneration !== generation) {
          return;
        }

        await waitForRetry();
      }
    }

    const removeVisibilityResume = installVisibilityResume(
      () => Date.now() - lastFrameAt > STREAM_RESUME_IDLE_MS,
      () => {
        if (isCancelled || latestIsComplete) {
          return;
        }
        const next = ++generation;
        activeController?.abort();
        activeController = null;
        retry.cancel();
        retryAttempt = 0;
        lastFrameAt = Date.now();
        void run(next);
      },
    );

    if (shouldReset) {
      entriesRef.current = [];
      lastEventIdRef.current = 0n;
      cursorDurableRef.current = false;
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

    void run(generation);

    return () => {
      isCancelled = true;
      retry.cancel();
      removeVisibilityResume();
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
