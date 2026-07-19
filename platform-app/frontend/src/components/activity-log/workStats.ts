import type { ActivityEntry } from "@/rpc/platform/service_pb";
import { SYSTEM_TYPES } from "@/lib/activityLogFormat";

export type ToolCount = { name: string; count: number };

export type WorkStats = {
  /**
   * Per-tool call counts keyed by the tool's actual name as emitted by the
   * agent (e.g. "grep", "read_file", "Write"). No categorization: every tool
   * call is surfaced under its real name so nothing hides behind a generic
   * "N tool calls" bucket. Sorted by count (desc), then name for stability.
   */
  tools: ToolCount[];
  /** Total number of tool_use entries. */
  toolTotal: number;
  errors: number;
  thoughts: number;
  systems: number;
};

export function computeStats(entries: ActivityEntry[]): WorkStats {
  const counts = new Map<string, number>();
  let toolTotal = 0;
  let errors = 0;
  let thoughts = 0;
  let systems = 0;
  for (const e of entries) {
    if (e.type === "assistant_thinking") {
      thoughts += 1;
      continue;
    }
    if (e.type === "tool_result") {
      if (e.isError) errors += 1;
      continue;
    }
    if (SYSTEM_TYPES.has(e.type)) {
      systems += 1;
      continue;
    }
    if (e.type !== "tool_use") continue;
    const name = e.tool || "tool";
    counts.set(name, (counts.get(name) ?? 0) + 1);
    toolTotal += 1;
  }
  const tools = [...counts.entries()]
    .map(([name, count]) => ({ name, count }))
    .sort(
      (a, b) =>
        b.count - a.count || (a.name < b.name ? -1 : a.name > b.name ? 1 : 0),
    );
  return { tools, toolTotal, errors, thoughts, systems };
}

export function statsSummary(s: WorkStats): string {
  const parts = s.tools.map((t) => `${t.count}× ${t.name}`);
  if (parts.length === 0 && s.thoughts > 0) return "reasoning";
  return parts.join(" · ");
}

export function liveVerb(entries: ActivityEntry[]): string {
  for (let i = entries.length - 1; i >= 0; i--) {
    const e = entries[i];
    if (e.type === "assistant_thinking") return "Thinking";
    if (e.type !== "tool_use") continue;
    return e.tool ? `Running ${e.tool}` : "Running a tool";
  }
  // No concrete action to describe (e.g. only system bookkeeping like
  // system init). The run header's "Preparing work…" status covers this.
  return "";
}

// ─── Detail panes ───────────────────────────────────────────────────────────
