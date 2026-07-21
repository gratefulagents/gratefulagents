import type { ActivityEntry } from "@/rpc/platform/service_pb";

export type ActivityGroup =
  | { kind: "single"; entry: ActivityEntry }
  | { kind: "tool-pair"; toolUse: ActivityEntry; toolResult: ActivityEntry }
  | { kind: "tool-batch"; tool: string; entries: ActivityEntry[] }
  | {
      kind: "subagent";
      entries: ActivityEntry[];
      taskId: string;
      subagentType: string;
      subagentDescription: string;
      subagentStatus: string;
      toolCount: number;
      totalTokens: bigint;
      durationMs: bigint;
      subagentModel: string;
      subagentCostUsd: number;
      subagentCostKnown: boolean;
      subagentNumTurns: number;
      subagentStopReason: string;
      subagentPrompt: string;
      subagentResultText: string;
      /** Parent subagent tool-call ID. Shared by every task spawned in one batch. */
      parentCallId: string;
    }
  | {
      kind: "inline-subagent";
      parentEntry: ActivityEntry;
      children: ActivityEntry[];
      resultEntry?: ActivityEntry;
    }
  | { kind: "secondary-batch"; entries: ActivityEntry[] };

const SECONDARY_TYPES = new Set([
  "tool_progress",
  "api_retry",
  "hook_response",
  "session_state_changed",
]);

const BATCHABLE_TOOLS = new Set(["read", "read_file", "grep", "glob"]);

const SUBAGENT_EVIDENCE_TYPES = new Set([
  "subagent_started",
  "subagent_progress",
  "subagent_notification",
  "subagent_completed",
]);

/**
 * Lifecycle/status strings that ride in the description field of registry task
 * snapshots ("spawned", "dependency_wait", …). Never usable as a title — the
 * task's objective (subagentPrompt) is the real description.
 */
const SUBAGENT_STATUS_NOISE = new Set([
  "spawned",
  "dependency_wait",
  "managed_wait",
  "final_join",
  "resumed",
  "pending",
  "waiting",
  "running",
  "started",
  "completed",
  "succeeded",
  "failed",
  "stopped",
  "cancelled",
  "canceled",
]);

export function cleanSubagentDescription(raw: string | undefined): string {
  const s = (raw ?? "").trim();
  if (!s || SUBAGENT_STATUS_NOISE.has(s.toLowerCase())) return "";
  return s;
}

/** First non-empty line of a task prompt, ellipsized into a card title. Slices
 * by code points so surrogate pairs (emoji) are never split. */
export function subagentTitleFromPrompt(prompt: string | undefined): string {
  for (const line of (prompt ?? "").split("\n")) {
    const trimmed = line.trim();
    if (!trimmed) continue;
    const points = Array.from(trimmed);
    return points.length > 90 ? `${points.slice(0, 89).join("").trimEnd()}…` : trimmed;
  }
  return "";
}

function hasSubagentGroupingEvidence(entry: ActivityEntry): boolean {
  return (
    SUBAGENT_EVIDENCE_TYPES.has(entry.type) ||
    Boolean(
      entry.subagentType ||
        entry.subagentDescription ||
        entry.subagentStatus ||
        entry.subagentModel ||
        entry.subagentPrompt ||
        entry.subagentResultText,
    ) ||
    // Live activity is paginated to the newest 1,000 events. In a large
    // fan-out, a task's spawn event can fall outside that window while its
    // task-tagged tool/LLM events remain. SDK-managed task IDs use this prefix;
    // accepting them keeps child work inside its subagent card without reviving
    // the legacy phantom groups caused by parent call IDs (call_*).
    (entry.taskId.startsWith("task_") &&
      Boolean(entry.agentName) &&
      entry.agentName !== "main")
  );
}

export function groupActivityEntries(
  entries: ActivityEntry[],
  opts?: { skipSubagentGrouping?: boolean },
): ActivityGroup[] {
  const subagentByTaskId = new Map<string, ActivityEntry[]>();
  const validSubagentTaskIds = new Set<string>();

  if (!opts?.skipSubagentGrouping) {
    for (const e of entries) {
      if (e.taskId && hasSubagentGroupingEvidence(e)) {
        validSubagentTaskIds.add(e.taskId);
      }
    }

    for (const e of entries) {
      if (e.taskId && validSubagentTaskIds.has(e.taskId)) {
        const arr = subagentByTaskId.get(e.taskId) ?? [];
        arr.push(e);
        subagentByTaskId.set(e.taskId, arr);
      }
    }
  }

  const toolUseIdToTaskId = new Map<string, string>();
  for (const [tid, arr] of subagentByTaskId) {
    for (const e of arr) {
      if (e.type === "subagent_started" && e.toolUseId) {
        toolUseIdToTaskId.set(e.toolUseId, tid);
      }
    }
  }

  for (const e of entries) {
    if (e.taskId || !e.toolUseId) continue;
    const tid = toolUseIdToTaskId.get(e.toolUseId);
    if (!tid) continue;
    const bucket = subagentByTaskId.get(tid);
    if (!bucket || bucket.includes(e)) continue;
    bucket.push(e);
  }

  const toolResultByUseId = new Map<string, ActivityEntry>();
  for (const e of entries) {
    if (e.type === "tool_result" && e.toolUseId) {
      toolResultByUseId.set(e.toolUseId, e);
    }
  }

  const childrenByParentCallId = new Map<string, ActivityEntry[]>();
  for (const e of entries) {
    if (e.parentCallId) {
      const arr = childrenByParentCallId.get(e.parentCallId) ?? [];
      arr.push(e);
      childrenByParentCallId.set(e.parentCallId, arr);
    }
  }

  const emittedTaskIds = new Set<string>();
  const consumedIndices = new Set<number>();
  const groups: ActivityGroup[] = [];

  const entryToIndex = new Map<ActivityEntry, number>();
  entries.forEach((e, i) => entryToIndex.set(e, i));

  for (const [, arr] of subagentByTaskId) {
    for (const e of arr) {
      const idx = entryToIndex.get(e);
      if (idx !== undefined) consumedIndices.add(idx);
      if (e.type === "tool_use" && e.tool === "Agent" && e.toolUseId) {
        const result = toolResultByUseId.get(e.toolUseId);
        if (result) {
          const resultIdx = entryToIndex.get(result);
          if (resultIdx !== undefined) consumedIndices.add(resultIdx);
          if (!arr.includes(result)) arr.push(result);
        }
      }
    }
  }

  let i = 0;
  while (i < entries.length) {
    const e = entries[i];

    if (consumedIndices.has(i)) {
      const tid = e.taskId || toolUseIdToTaskId.get(e.toolUseId ?? "");
      if (tid && !emittedTaskIds.has(tid)) {
        emittedTaskIds.add(tid);
        const batch = subagentByTaskId.get(tid) ?? [e];
        const agentType =
          batch.find((b) => b.subagentType)?.subagentType ??
          batch.find((b) => b.agentName)?.agentName ??
          "";
        let desc = "";
        for (const b of batch) {
          desc = cleanSubagentDescription(b.subagentDescription);
          if (desc) break;
        }

        const startedEntry = batch.find((b) => b.type === "subagent_started");
        const parentCallId =
          batch.find((b) => b.parentCallId)?.parentCallId ||
          startedEntry?.toolUseId ||
          "";
        const terminalStatuses = new Set(["completed", "succeeded", "failed", "stopped", "cancelled", "canceled"]);
        const last = batch[batch.length - 1];
        const terminalByStatus = batch.find((b) =>
          terminalStatuses.has(b.subagentStatus),
        );
        const terminalByStep = batch.find(
          (b) =>
            b.type === "subagent_notification" && terminalStatuses.has(b.step),
        );
        const notificationEntry = batch.find(
          (b) => b.type === "subagent_notification",
        );
        const terminalEntry =
          terminalByStatus ?? terminalByStep ?? notificationEntry;
        const status =
          terminalByStatus?.subagentStatus ??
          terminalByStep?.step ??
          (notificationEntry ? "completed" : null) ??
          (last.subagentStatus || "running");
        const metricsEntry = terminalEntry ?? last;

        groups.push({
          kind: "subagent",
          entries: batch,
          taskId: tid,
          subagentType: agentType,
          subagentDescription: desc,
          subagentStatus: status,
          toolCount: metricsEntry.subagentToolCount ?? 0,
          totalTokens: metricsEntry.subagentTotalTokens ?? 0n,
          durationMs: metricsEntry.subagentDurationMs ?? 0n,
          subagentModel: metricsEntry.subagentModel ?? "",
          subagentCostUsd: metricsEntry.subagentCostUsd ?? 0,
          subagentCostKnown:
            (metricsEntry as ActivityEntry & { subagentCostKnown?: boolean })
              .subagentCostKnown ?? (metricsEntry.subagentCostUsd ?? 0) > 0,
          subagentNumTurns: metricsEntry.subagentNumTurns ?? 0,
          subagentStopReason: metricsEntry.subagentStopReason ?? "",
          subagentPrompt:
            startedEntry?.subagentPrompt ||
            batch.find((b) => b.subagentPrompt)?.subagentPrompt ||
            "",
          subagentResultText: metricsEntry.subagentResultText ?? "",
          parentCallId,
        });
      }
      i++;
      continue;
    }

    if (e.type === "tool_use" && e.toolUseId && e.tool?.startsWith("agent_")) {
      const children = childrenByParentCallId.get(e.toolUseId);
      if (children && children.length > 0) {
        for (const child of children) {
          const childIdx = entryToIndex.get(child);
          if (childIdx !== undefined) consumedIndices.add(childIdx);
          if (child.type === "tool_use" && child.toolUseId) {
            const childResult = toolResultByUseId.get(child.toolUseId);
            if (childResult) {
              const childResultIdx = entryToIndex.get(childResult);
              if (childResultIdx !== undefined)
                consumedIndices.add(childResultIdx);
            }
          }
        }
        const parentResult = toolResultByUseId.get(e.toolUseId);
        if (parentResult) {
          const parentResultIdx = entryToIndex.get(parentResult);
          if (parentResultIdx !== undefined)
            consumedIndices.add(parentResultIdx);
        }
        groups.push({
          kind: "inline-subagent",
          parentEntry: e,
          children,
          resultEntry: parentResult,
        });
        i++;
        continue;
      }
    }

    if (e.type === "tool_use" && e.toolUseId) {
      const result = toolResultByUseId.get(e.toolUseId);
      if (result) {
        const resultIdx = entryToIndex.get(result);
        if (resultIdx !== undefined) consumedIndices.add(resultIdx);
        groups.push({ kind: "tool-pair", toolUse: e, toolResult: result });
        i++;
        continue;
      }
    }

    if (e.type === "tool_result" && e.toolUseId && consumedIndices.has(i)) {
      i++;
      continue;
    }

    if (SECONDARY_TYPES.has(e.type)) {
      const batch: ActivityEntry[] = [e];
      let j = i + 1;
      while (
        j < entries.length &&
        SECONDARY_TYPES.has(entries[j].type) &&
        !consumedIndices.has(j)
      ) {
        batch.push(entries[j]);
        j++;
      }
      groups.push({ kind: "secondary-batch", entries: batch });
      i = j;
      continue;
    }

    if (
      e.type === "tool_use" &&
      BATCHABLE_TOOLS.has(e.tool?.toLowerCase() ?? "")
    ) {
      const tool = e.tool!.toLowerCase();
      const batch: ActivityEntry[] = [e];
      let j = i + 1;
      while (
        j < entries.length &&
        entries[j].type === "tool_use" &&
        entries[j].tool?.toLowerCase() === tool &&
        !consumedIndices.has(j)
      ) {
        batch.push(entries[j]);
        j++;
      }
      if (batch.length > 1) {
        for (const batchEntry of batch) {
          if (batchEntry.toolUseId) {
            const result = toolResultByUseId.get(batchEntry.toolUseId);
            if (result) {
              const resultIdx = entryToIndex.get(result);
              if (resultIdx !== undefined) consumedIndices.add(resultIdx);
            }
          }
        }
        groups.push({ kind: "tool-batch", tool, entries: batch });
        i = j;
        continue;
      }
    }

    groups.push({ kind: "single", entry: e });
    i++;
  }

  return groups;
}

// --- Helpers ---

export function shortPath(p: string): string {
  const cleaned = p.trim().split("\n")[0];
  const parts = cleaned.split("/").filter(Boolean);
  if (parts.length <= 2) return cleaned;
  return parts.slice(-2).join("/");
}

export function formatTokens(n: number | bigint): string {
  const num = Number(n);
  if (num >= 1_000_000) return `${(num / 1_000_000).toFixed(1)}M`;
  if (num >= 1_000) return `${(num / 1_000).toFixed(1)}K`;
  if (num === 0) return "";
  return String(num);
}

export function formatDuration(ms: number | bigint): string {
  const n = Number(ms);
  if (n <= 0) return "";
  if (n < 1000) return `${n}ms`;
  const seconds = n / 1000;
  if (seconds < 60) return `${seconds.toFixed(1)}s`;
  // Above a minute, humanize like wall-clock durations ("2m 52s", "1h 4m")
  // so tool, subagent, and work-card durations all read the same way.
  const total = Math.round(seconds);
  const m = Math.floor(total / 60);
  const s = total % 60;
  if (m < 60) return s > 0 ? `${m}m ${s}s` : `${m}m`;
  const h = Math.floor(m / 60);
  return m % 60 > 0 ? `${h}h ${m % 60}m` : `${h}h`;
}
