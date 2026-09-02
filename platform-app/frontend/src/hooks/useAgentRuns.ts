import { useEffect, useMemo, useSyncExternalStore } from "react";
import { client } from "@/lib/client";
import type { AgentRun, ListAgentRunsResponse, AgentRunEvent } from "@/rpc/platform/service_pb";
import { applyAgentRunEvent } from "@/hooks/useAgentRuns.helpers";
import { getWatchStore } from "@/hooks/watchStore";

/**
 * Default fleet window: the server returns every non-terminal run plus the
 * newest `limit` terminal runs; older terminal runs are paged in on demand.
 */
export const DEFAULT_FLEET_WINDOW = 200;

function matchesRunFilters(
  run: AgentRun,
  namespace: string,
  sourceName: string,
  sourceKind: string,
) {
  if (namespace && run.namespace !== namespace) {
    return false;
  }
  if (!sourceName && !sourceKind) {
    return true;
  }

  const matchesProject =
    (!sourceName || run.project?.name === sourceName) &&
    (!sourceKind || run.project?.kind === sourceKind);
  const matchesTrigger =
    (!sourceName || run.trigger?.name === sourceName) &&
    (!sourceKind || run.trigger?.kind === sourceKind);

  return matchesProject || matchesTrigger;
}

/**
 * List+watch over agent runs. The underlying stream is shared per
 * namespace/source filter/window (see watchStore.ts), so any number of
 * components mounting this hook with the same arguments opens a single
 * ListAgentRuns/WatchAgentRuns loop. Source filters are applied server-side;
 * the client-side filter remains as a safeguard.
 */
export function useAgentRuns(namespace = "", sourceName = "", sourceKind = "", options?: { limit?: number }) {
  const limit = options?.limit ?? DEFAULT_FLEET_WINDOW;
  const store = getWatchStore<AgentRun, ListAgentRunsResponse, AgentRunEvent>(
    `AgentRuns:${namespace}:${sourceKind}:${sourceName}:${limit}`,
    () => ({
      list: () => client.listAgentRuns({ namespace, limit, sourceKind, sourceName }),
      listPage: (pageToken) => client.listAgentRuns({ namespace, limit, sourceKind, sourceName, pageToken }),
      extractList: (res) => res.runs,
      extractPage: (res) => ({ nextPageToken: res.nextPageToken, totalCount: res.totalCount }),
      itemKey: (run) => `${run.namespace}/${run.name}`,
      watch: (watchOptions) => client.watchAgentRuns({ namespace, limit, sourceKind, sourceName }, watchOptions),
      applyEvent: applyAgentRunEvent,
      label: "AgentRuns",
    }),
  );

  useEffect(() => {
    store.acquire();
    return () => store.release();
  }, [store]);

  const snapshot = useSyncExternalStore(store.subscribe, store.getSnapshot, store.getSnapshot);

  const runs = useMemo(
    () =>
      !sourceName && !sourceKind
        ? snapshot.items
        : snapshot.items.filter((run) => matchesRunFilters(run, namespace, sourceName, sourceKind)),
    [snapshot.items, namespace, sourceName, sourceKind],
  );

  return {
    runs,
    loading: snapshot.loading,
    error: snapshot.error,
    refetch: store.refetch,
    totalCount: snapshot.totalCount,
    hasMore: snapshot.nextPageToken !== "",
    loadingMore: snapshot.loadingMore,
    loadMore: store.loadMore,
  };
}
