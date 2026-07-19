import type { AgentRun } from "@/rpc/platform/service_pb";

type AgentRunEventLike = { type: string; run?: AgentRun };

export function applyAgentRunEvent(prev: AgentRun[], event: AgentRunEventLike) {
  if (!event.run) return prev;
  const idx = prev.findIndex(
    (run) => run.name === event.run!.name && run.namespace === event.run!.namespace
  );
  if (event.type === "DELETED") {
    if (idx < 0) return prev;
    const next = [...prev];
    next.splice(idx, 1);
    return next;
  }
  if (idx >= 0) {
    const next = [...prev];
    next[idx] = event.run;
    return next;
  }
  return [...prev, event.run];
}
