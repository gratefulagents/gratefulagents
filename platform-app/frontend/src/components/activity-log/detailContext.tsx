import { createContext, useContext, useEffect, useState } from "react";

import type {
  ActivityEntryDetail,
  FetchActivityEntryDetail,
} from "@/hooks/useActivityEntryDetail";
import type { ActivityEntry } from "@/rpc/platform/service_pb";

/**
 * Provides the lazy full-payload fetcher (see useActivityEntryDetail) to the
 * deeply nested activity-log rows without threading the run identity through
 * every layer. When no provider is present truncated payloads simply render
 * as-is.
 */
const ActivityDetailContext = createContext<FetchActivityEntryDetail | null>(null);

export const ActivityDetailProvider = ActivityDetailContext.Provider;

export interface ResolvedEntryState {
  /** The entry with full payloads substituted once the fetch resolves. */
  entry: ActivityEntry | undefined;
  loading: boolean;
  failed: boolean;
}

function isTruncated(entry: ActivityEntry | undefined): entry is ActivityEntry {
  return Boolean(entry && (entry.inputTruncated || entry.outputTruncated));
}

/**
 * Resolves an entry's full payloads while a detail pane is expanded. For
 * non-truncated entries (or without a provider) this is a pass-through.
 */
export function useResolvedEntry(entry: ActivityEntry | undefined, active: boolean): ResolvedEntryState {
  const fetchDetail = useContext(ActivityDetailContext);
  const [result, setResult] = useState<{ entry: ActivityEntry; detail?: ActivityEntryDetail; failed?: boolean } | null>(
    null,
  );
  const wantsDetail = active && isTruncated(entry) && fetchDetail !== null;

  useEffect(() => {
    if (!wantsDetail || !entry) {
      return;
    }
    let cancelled = false;
    fetchDetail(entry)
      .then((detail) => {
        if (!cancelled) {
          setResult({ entry, detail });
        }
      })
      .catch(() => {
        if (!cancelled) {
          setResult({ entry, failed: true });
        }
      });
    return () => {
      cancelled = true;
    };
  }, [wantsDetail, entry, fetchDetail]);

  if (!wantsDetail || !entry) {
    return { entry, loading: false, failed: false };
  }
  if (result?.entry !== entry) {
    return { entry, loading: true, failed: false };
  }
  if (result.detail) {
    return {
      entry: {
        ...entry,
        inputRaw: entry.inputTruncated ? result.detail.inputRaw : entry.inputRaw,
        output: entry.outputTruncated ? result.detail.output : entry.output,
        inputTruncated: false,
        outputTruncated: false,
      },
      loading: false,
      failed: false,
    };
  }
  return { entry, loading: false, failed: true };
}
