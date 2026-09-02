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

const WRITE_TOOLS = new Set(["edit", "write", "multiedit", "apply_patch", "notebookedit", "str_replace_editor"]);
const COMMAND_TOOLS = new Set(["bash", "execute", "shell", "run_command", "terminal"]);
const EXPLORE_TOOLS = new Set(["read", "read_file", "grep", "glob", "search", "list_files", "ls", "lsp", "webfetch", "web_search", "codebase_search"]);

/**
 * Human verb describing what a completed work unit mostly did, so collapsed
 * cards read as "Edited files" / "Ran commands" / "Explored" instead of a
 * generic "Worked". Falls back to "Worked" for unknown or mixed tool sets.
 */
export function workVerb(s: WorkStats): string {
  if (s.toolTotal === 0) return "Worked";
  let writes = false;
  let commands = false;
  let explores = false;
  let other = false;
  for (const t of s.tools) {
    const name = t.name.toLowerCase();
    if (WRITE_TOOLS.has(name)) writes = true;
    else if (COMMAND_TOOLS.has(name)) commands = true;
    else if (EXPLORE_TOOLS.has(name)) explores = true;
    else other = true;
  }
  if (writes) return "Edited files";
  if (other) return "Worked";
  if (commands) return "Ran commands";
  if (explores) return "Explored";
  return "Worked";
}

/**
 * What a live work unit is doing *right now*, distinguished from what it has
 * already finished. A tool_use is in flight only while no tool_result has
 * answered it; once it returns, the agent is composing (or thinking), and the
 * card must not keep claiming "Running X".
 */
export type LiveActivity =
  | { kind: "tool"; use: ActivityEntry }
  | { kind: "thinking"; entry: ActivityEntry }
  | { kind: "idle"; last?: { use: ActivityEntry; result?: ActivityEntry } }
  | { kind: "none" };

export function liveActivity(entries: ActivityEntry[]): LiveActivity {
  const answered = new Set<string>();
  let lastResult: ActivityEntry | undefined;
  for (const e of entries) {
    if (e.type === "tool_result") {
      if (e.toolUseId) answered.add(e.toolUseId);
      lastResult = e;
    }
  }
  for (let i = entries.length - 1; i >= 0; i--) {
    const e = entries[i];
    if (e.type === "assistant_thinking") return { kind: "thinking", entry: e };
    if (e.type !== "tool_use") continue;
    // Without ids, a tool_result that arrived after the call counts as its answer.
    const done = e.toolUseId
      ? answered.has(e.toolUseId)
      : entries.slice(i + 1).some((x) => x.type === "tool_result");
    if (!done) return { kind: "tool", use: e };
    const result = e.toolUseId
      ? entries.find((x) => x.type === "tool_result" && x.toolUseId === e.toolUseId)
      : lastResult;
    return { kind: "idle", last: { use: e, result } };
  }
  return { kind: "none" };
}

/** Entries minus the in-flight tool call, so "what's done" stats exclude "what's running". */
export function completedEntries(entries: ActivityEntry[], live: LiveActivity): ActivityEntry[] {
  if (live.kind !== "tool") return entries;
  return entries.filter((e) => e !== live.use);
}

export function liveVerb(entries: ActivityEntry[]): string {
  const live = liveActivity(entries);
  switch (live.kind) {
    case "tool":
      return live.use.tool ? `Running ${live.use.tool}` : "Running a tool";
    case "thinking":
      return "Thinking";
    case "idle":
      return "Working";
    case "none":
      // No concrete action to describe (e.g. only system bookkeeping like
      // system init). The run header's "Preparing work…" status covers this.
      return "";
  }
}

// ─── Detail panes ───────────────────────────────────────────────────────────
