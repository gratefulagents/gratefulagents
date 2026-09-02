/* eslint-disable react-refresh/only-export-components */
import { createContext, useContext, useMemo, type ReactNode } from "react";

import { nodeIDToTaskID, ordinalsForGraph } from "@/lib/subagentGraphLayout";
import type { SubagentGraph } from "@/rpc/platform/service_pb";

/**
 * Run-wide sub-agent context for the transcript. Delegation cards only see
 * their own burst, so left alone every card would number its tasks #1, #2, …
 * and "#2" would mean a different agent in each card, in the dock and in the
 * graph tab. Numbering from the run's full graph gives every task one stable
 * ordinal wherever it appears. `onOpenGraph` lets a card jump to that tab.
 */
export type SubagentContextValue = {
  /** Task id → run-wide ordinal. Empty until the graph has loaded. */
  ordinalByTaskId: ReadonlyMap<string, number>;
  onOpenGraph?: () => void;
};

const EMPTY: SubagentContextValue = { ordinalByTaskId: new Map() };

const SubagentContext = createContext<SubagentContextValue>(EMPTY);

export function SubagentContextProvider({
  graph,
  onOpenGraph,
  children,
}: {
  graph?: SubagentGraph;
  onOpenGraph?: () => void;
  children: ReactNode;
}) {
  const value = useMemo<SubagentContextValue>(() => {
    const ordinalByTaskId = new Map<string, number>();
    if (graph && graph.nodes.length > 0) {
      for (const [nodeId, ordinal] of ordinalsForGraph(graph)) {
        ordinalByTaskId.set(nodeIDToTaskID(nodeId), ordinal);
        ordinalByTaskId.set(nodeId, ordinal);
      }
    }
    return { ordinalByTaskId, onOpenGraph };
  }, [graph, onOpenGraph]);
  return <SubagentContext.Provider value={value}>{children}</SubagentContext.Provider>;
}

export function useSubagentContext(): SubagentContextValue {
  return useContext(SubagentContext);
}
