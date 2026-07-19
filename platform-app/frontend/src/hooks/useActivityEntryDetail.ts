import { useCallback, useRef } from "react";

import { client } from "@/lib/client";
import type { ActivityEntry } from "@/rpc/platform/service_pb";

export interface ActivityEntryDetail {
  inputRaw: string;
  output: string;
}

export type FetchActivityEntryDetail = (entry: ActivityEntry) => Promise<ActivityEntryDetail>;

function detailCacheKey(entry: ActivityEntry): string {
  return entry.eventId !== 0n ? `id:${entry.eventId}` : `tool:${entry.toolUseId}:${entry.type}`;
}

/**
 * Lazy full-payload fetcher for truncated activity entries. Results are
 * cached per (namespace, name) by event id (falling back to tool-use id) so
 * repeated expands of the same row hit the network once; failed fetches are
 * evicted so they can be retried.
 */
export function useActivityEntryDetail(namespace: string, name: string): FetchActivityEntryDetail {
  const cacheRef = useRef(new Map<string, Promise<ActivityEntryDetail>>());

  return useCallback(
    (entry: ActivityEntry) => {
      const key = `${namespace}/${name}|${detailCacheKey(entry)}`;
      const cached = cacheRef.current.get(key);
      if (cached) {
        return cached;
      }
      const promise = client
        .getActivityEntryDetail({
          namespace,
          name,
          eventId: entry.eventId,
          toolUseId: entry.toolUseId,
        })
        .then((response) => ({ inputRaw: response.inputRaw, output: response.output }));
      cacheRef.current.set(key, promise);
      promise.catch(() => {
        if (cacheRef.current.get(key) === promise) {
          cacheRef.current.delete(key);
        }
      });
      return promise;
    },
    [namespace, name],
  );
}
