import type { ActivityEntry, SubagentGraphNode } from "@/rpc/platform/service_pb";

export function subagentDetailEntries(node: SubagentGraphNode, entries: ActivityEntry[]): ActivityEntry[] {
  // Event IDs remain valid when pagination changes the local buffer's positions.
  if (node.detailEntryEventIds.length > 0) {
    const byId = new Map(entries.filter((entry) => entry.eventId !== 0n).map((entry) => [entry.eventId, entry]));
    return node.detailEntryEventIds.flatMap((id) => {
      const entry = byId.get(id);
      return entry ? [entry] : [];
    });
  }
  return node.detailEntryIndices
    .filter((index) => index >= 0 && index < entries.length)
    .map((index) => entries[index]);
}
