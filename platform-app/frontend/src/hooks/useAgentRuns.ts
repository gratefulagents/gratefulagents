import { useEffect, useMemo, useSyncExternalStore } from "react";
import { client } from "@/lib/client";
import type { AgentRun, ListAgentRunsResponse, AgentRunEvent } from "@/rpc/platform/service_pb";
import { applyAgentRunEvent } from "@/hooks/useAgentRuns.helpers";
import { getWatchStore } from "@/hooks/watchStore";

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
 * List+watch over agent runs. The underlying stream is shared per namespace
 * (see watchStore.ts): the store holds the unfiltered runs and each caller
 * filters by source, so any number of components mounting this hook opens a
 * single ListAgentRuns/WatchAgentRuns loop.
 */
export function useAgentRuns(namespace = "", sourceName = "", sourceKind = "") {
  const store = getWatchStore<AgentRun, ListAgentRunsResponse, AgentRunEvent>(
    `AgentRuns:${namespace}`,
    () => ({
      list: () => client.listAgentRuns({ namespace }),
      extractList: (res) => res.runs,
      watch: (options) => client.watchAgentRuns({ namespace }, options),
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

  return { runs, loading: snapshot.loading, error: snapshot.error, refetch: store.refetch };
}
