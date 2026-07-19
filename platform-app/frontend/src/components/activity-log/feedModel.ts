import type { ActivityEntry } from "@/rpc/platform/service_pb";
import type { ActivityGroup } from "@/lib/activityGrouping";
import { SYSTEM_TYPES } from "@/lib/activityLogFormat";
import type { FeedItem, WorkItem, WorkUnit } from "./types";

// Identity must stay stable while an entry's body streams in (message/output
// grow across snapshots), so it deliberately excludes content. keyedFeedItems
// disambiguates genuine collisions with an occurrence counter.
export function entryIdentity(entry: ActivityEntry): string {
  return [
    entry.timestampUnix.toString(),
    entry.type,
    entry.toolUseId || "-",
    entry.tool || "-",
  ].join(":");
}

export function workUnitKey(unit: WorkUnit, index: number): string {
  switch (unit.kind) {
    case "row":
      return `row:${entryIdentity(unit.use)}`;
    case "batch":
      return `batch:${unit.tool}:${unit.entries[0] ? entryIdentity(unit.entries[0]) : index}`;
    case "thinking":
    case "step":
      return `${unit.kind}:${entryIdentity(unit.entry)}`;
    case "system":
      return `system:${unit.entries[0] ? entryIdentity(unit.entries[0]) : index}`;
    default:
      return `unit:${index}`;
  }
}

export function feedItemBaseKey(item: FeedItem): string {
  switch (item.kind) {
    case "prose":
    case "phase":
    case "meta":
      return `${item.kind}:${entryIdentity(item.entry)}`;
    case "reasoning":
      return `reasoning:${item.entries[0] ? entryIdentity(item.entries[0]) : "empty"}`;
    case "work":
      return `work:${item.entries[0] ? entryIdentity(item.entries[0]) : "empty"}`;
    case "subagent":
      return `${item.kind}:${item.group.taskId || (item.group.entries[0] ? entryIdentity(item.group.entries[0]) : "empty")}`;
    case "subagent-dag":
      return `${item.kind}:${item.groups
        .map((g) => g.taskId || (g.entries[0] ? entryIdentity(g.entries[0]) : ""))
        .join("|")}`;
    case "inline-subagent":
      return `${item.kind}:${entryIdentity(item.group.parentEntry)}`;
    case "question":
    case "plan":
      return `${item.kind}:${entryIdentity(item.use)}`;
  }
}

export function keyedFeedItems(feed: FeedItem[]): Array<{ item: FeedItem; key: string }> {
  const seen = new Map<string, number>();
  return feed.map((item) => {
    const base = feedItemBaseKey(item);
    const occurrence = seen.get(base) ?? 0;
    seen.set(base, occurrence + 1);
    return { item, key: `${base}:${occurrence}` };
  });
}

export function buildFeed(groups: ActivityGroup[]): FeedItem[] {
  const feed: FeedItem[] = [];
  let work: WorkItem | null = null;

  const flush = () => {
    if (work && work.units.length > 0) feed.push(work);
    work = null;
  };
  const pushUnit = (unit: WorkUnit, entries: ActivityEntry[]) => {
    if (!work) work = { kind: "work", units: [], entries: [] };
    if (unit.kind === "system") {
      // Merge consecutive system units to keep the card tidy.
      const last = work.units[work.units.length - 1];
      if (last?.kind === "system") {
        last.entries.push(...unit.entries);
        work.entries.push(...entries);
        return;
      }
    }
    work.units.push(unit);
    work.entries.push(...entries);
  };

  for (const g of groups) {
    switch (g.kind) {
      case "single": {
        const e = g.entry;
        if (e.type === "assistant_text") {
          flush();
          if ((e.message || "").trim()) feed.push({ kind: "prose", entry: e });
          break;
        }
        if (e.type === "assistant_thinking") {
          flush();
          const last = feed[feed.length - 1];
          if (last && last.kind === "reasoning") {
            last.entries.push(e);
          } else {
            feed.push({ kind: "reasoning", entries: [e] });
          }
          break;
        }
        if (e.type === "phase_transition") {
          flush();
          feed.push({ kind: "phase", entry: e });
          break;
        }
        if (e.type === "session_complete") {
          flush();
          feed.push({ kind: "meta", entry: e });
          break;
        }
        if (e.type === "tool_use" && e.tool === "AskUserQuestion") {
          flush();
          feed.push({ kind: "question", use: e });
          break;
        }
        if (e.type === "tool_use" && e.tool === "present_plan") {
          flush();
          feed.push({ kind: "plan", use: e });
          break;
        }
        if (e.type === "step_change") {
          pushUnit({ kind: "step", entry: e }, [e]);
          break;
        }
        if (SYSTEM_TYPES.has(e.type)) {
          pushUnit({ kind: "system", entries: [e] }, [e]);
          break;
        }
        pushUnit({ kind: "row", use: e }, [e]);
        break;
      }
      case "tool-pair": {
        if (g.toolUse.tool === "AskUserQuestion") {
          flush();
          feed.push({ kind: "question", use: g.toolUse, result: g.toolResult });
          break;
        }
        if (g.toolUse.tool === "present_plan") {
          flush();
          feed.push({ kind: "plan", use: g.toolUse, result: g.toolResult });
          break;
        }
        pushUnit({ kind: "row", use: g.toolUse, result: g.toolResult }, [
          g.toolUse,
          g.toolResult,
        ]);
        break;
      }
      case "tool-batch":
        pushUnit({ kind: "batch", tool: g.tool, entries: g.entries }, g.entries);
        break;
      case "secondary-batch":
        pushUnit({ kind: "system", entries: g.entries }, g.entries);
        break;
      case "subagent":
        flush();
        feed.push({ kind: "subagent", group: g });
        break;
      case "inline-subagent":
        flush();
        feed.push({ kind: "inline-subagent", group: g });
        break;
    }
  }
  flush();
  coalesceSubagentDelegations(feed);
  return feed;
}

type SubagentFeedItem = Extract<FeedItem, { kind: "subagent" }>;
type SubagentDagItem = Extract<FeedItem, { kind: "subagent-dag" }>;

/**
 * Collapses tasks spawned by one parent tool call into a single delegation
 * item. Child tool events from concurrent tasks interleave in the raw stream,
 * so adjacency is not a reliable batch boundary. Newer SDK events stamp the
 * shared parentCallId onto every task; the consecutive fallback preserves
 * compact grouping for older activity logs that do not have that metadata.
 */
function coalesceSubagentDelegations(feed: FeedItem[]): void {
  const batches = new Map<string, SubagentFeedItem[]>();
  for (const item of feed) {
    if (item.kind !== "subagent" || !item.group.parentCallId) continue;
    const batch = batches.get(item.group.parentCallId) ?? [];
    batch.push(item);
    batches.set(item.group.parentCallId, batch);
  }

  for (const batch of batches.values()) {
    if (batch.length < 2) continue;
    const batchItems = new Set<FeedItem>(batch);
    const insertionIndex = feed.findIndex((item) => batchItems.has(item));
    if (insertionIndex < 0) continue;
    for (let i = feed.length - 1; i >= 0; i--) {
      if (batchItems.has(feed[i])) feed.splice(i, 1);
    }
    feed.splice(insertionIndex, 0, buildSubagentDagItem(batch));
  }

  // Legacy activity logs may not carry a parent call ID. Keep the old safe
  // adjacency heuristic for those entries only.
  for (let start = 0; start < feed.length; ) {
    const first = feed[start];
    if (first.kind !== "subagent" || first.group.parentCallId) {
      start++;
      continue;
    }
    let end = start;
    while (
      end < feed.length &&
      feed[end].kind === "subagent" &&
      !(feed[end] as SubagentFeedItem).group.parentCallId
    ) {
      end++;
    }
    if (end - start > 1) {
      const burst = feed.slice(start, end) as SubagentFeedItem[];
      feed.splice(start, end - start, buildSubagentDagItem(burst));
      end = start + 1;
    }
    start = end;
  }
}

function buildSubagentDagItem(burst: SubagentFeedItem[]): SubagentDagItem {
  const items = topoSortSubagentItems(burst);
  const groups = items.map((i) => i.group);

  const byTaskId = new Map<string, number>();
  groups.forEach((g, i) => {
    if (g.taskId) byTaskId.set(g.taskId, i);
  });
  // Wave = longest dependency chain length up to this task; deps outside the
  // burst contribute nothing. Groups are already topologically ordered, so a
  // single left-to-right pass converges.
  const waves = groups.map(() => 0);
  groups.forEach((g, i) => {
    for (const dep of subagentGroupDependsOn(g)) {
      const from = byTaskId.get(dep);
      if (from !== undefined && from !== i) {
        waves[i] = Math.max(waves[i], waves[from] + 1);
      }
    }
  });

  return { kind: "subagent-dag", groups, waves };
}

function subagentGroupDependsOn(group: SubagentFeedItem["group"]): string[] {
  for (let i = group.entries.length - 1; i >= 0; i--) {
    const deps = group.entries[i].subagentDependsOn;
    if (deps && deps.length > 0) return deps;
  }
  return [];
}

function topoSortSubagentItems(items: SubagentFeedItem[]): SubagentFeedItem[] {
  const byTaskId = new Map<string, number>();
  items.forEach((item, i) => {
    if (item.group.taskId) byTaskId.set(item.group.taskId, i);
  });

  const indegree = items.map(() => 0);
  const dependents = new Map<number, number[]>();
  items.forEach((item, i) => {
    for (const dep of subagentGroupDependsOn(item.group)) {
      const from = byTaskId.get(dep);
      if (from === undefined || from === i) continue;
      indegree[i]++;
      const list = dependents.get(from) ?? [];
      list.push(i);
      dependents.set(from, list);
    }
  });

  const ready: number[] = [];
  for (let i = 0; i < items.length; i++) if (indegree[i] === 0) ready.push(i);
  const out: SubagentFeedItem[] = [];
  while (ready.length > 0) {
    ready.sort((a, b) => a - b); // stable: original order among ready nodes
    const next = ready.shift()!;
    out.push(items[next]);
    for (const dep of dependents.get(next) ?? []) {
      indegree[dep]--;
      if (indegree[dep] === 0) ready.push(dep);
    }
  }
  // Cycle guard: append anything unresolved in original order.
  if (out.length < items.length) {
    const emitted = new Set(out);
    for (const item of items) if (!emitted.has(item)) out.push(item);
  }
  return out;
}

/** Flatten nested (subagent) groups into renderable work units. */
export function groupsToUnits(groups: ActivityGroup[]): WorkUnit[] {
  const units: WorkUnit[] = [];
  for (const g of groups) {
    switch (g.kind) {
      case "single": {
        const e = g.entry;
        if (e.type === "assistant_text") break; // rendered separately
        if (e.type === "assistant_thinking") {
          units.push({ kind: "thinking", entry: e });
          break;
        }
        if (e.type === "step_change") {
          units.push({ kind: "step", entry: e });
          break;
        }
        if (SYSTEM_TYPES.has(e.type)) {
          units.push({ kind: "system", entries: [e] });
          break;
        }
        units.push({ kind: "row", use: e });
        break;
      }
      case "tool-pair":
        units.push({ kind: "row", use: g.toolUse, result: g.toolResult });
        break;
      case "tool-batch":
        units.push({ kind: "batch", tool: g.tool, entries: g.entries });
        break;
      case "secondary-batch":
        units.push({ kind: "system", entries: g.entries });
        break;
      case "subagent":
        for (const e of g.entries) {
          if (e.type === "tool_use") units.push({ kind: "row", use: e });
        }
        break;
      case "inline-subagent":
        units.push({ kind: "row", use: g.parentEntry, result: g.resultEntry });
        for (const e of g.children) {
          if (e.type === "tool_use") units.push({ kind: "row", use: e });
        }
        break;
    }
  }
  return units;
}
