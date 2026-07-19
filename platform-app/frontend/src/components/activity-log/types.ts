import type { ActivityEntry } from "@/rpc/platform/service_pb";
import type { ActivityGroup } from "@/lib/activityGrouping";

export type WorkUnit =
  | { kind: "row"; use: ActivityEntry; result?: ActivityEntry }
  | { kind: "batch"; tool: string; entries: ActivityEntry[] }
  | { kind: "thinking"; entry: ActivityEntry }
  | { kind: "system"; entries: ActivityEntry[] }
  | { kind: "step"; entry: ActivityEntry };

export type WorkItem = { kind: "work"; units: WorkUnit[]; entries: ActivityEntry[] };

export type FeedItem =
  | { kind: "prose"; entry: ActivityEntry }
  | { kind: "reasoning"; entries: ActivityEntry[] }
  | WorkItem
  | { kind: "subagent"; group: Extract<ActivityGroup, { kind: "subagent" }> }
  | {
      kind: "subagent-dag";
      groups: Array<Extract<ActivityGroup, { kind: "subagent" }>>;
      /** Topological depth per group (parallel index aligned with groups). */
      waves: number[];
    }
  | {
      kind: "inline-subagent";
      group: Extract<ActivityGroup, { kind: "inline-subagent" }>;
    }
  | { kind: "question"; use: ActivityEntry; result?: ActivityEntry }
  | { kind: "plan"; use: ActivityEntry; result?: ActivityEntry }
  | { kind: "phase"; entry: ActivityEntry }
  | { kind: "meta"; entry: ActivityEntry };
