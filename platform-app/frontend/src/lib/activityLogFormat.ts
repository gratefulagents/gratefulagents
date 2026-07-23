import type { ActivityEntry } from "@/rpc/platform/service_pb";
import { formatTokens } from "@/lib/activityGrouping";

export function isCostKnown(entry: ActivityEntry): boolean {
  const maybeEntry = entry as ActivityEntry & { costKnown?: boolean };
  return maybeEntry.costKnown ?? entry.costUsd > 0;
}

export function formatUsd(costUsd: number): string {
  return `$${costUsd.toFixed(4)}`;
}

export function formatClock(unix: bigint): string {
  if (unix === 0n) return "";
  return new Date(Number(unix) * 1000).toLocaleTimeString();
}

const inputParseCache = new WeakMap<ActivityEntry, Record<string, unknown> | null>();

export function parseInput(e: ActivityEntry): Record<string, unknown> | null {
  if (inputParseCache.has(e)) {
    return inputParseCache.get(e) ?? null;
  }
  const raw = e.inputRaw || e.input || "";
  if (!raw) {
    inputParseCache.set(e, null);
    return null;
  }
  try {
    const parsed = JSON.parse(raw);
    if (parsed && typeof parsed === "object" && !Array.isArray(parsed)) {
      const result = parsed as Record<string, unknown>;
      inputParseCache.set(e, result);
      return result;
    }
  } catch {
    /* not JSON */
  }
  inputParseCache.set(e, null);
  return null;
}

export function bashCommand(e: ActivityEntry): string {
  const parsed = parseInput(e);
  const cmd = (parsed?.command || parsed?.cmd) as string | undefined;
  return (cmd || e.input || e.inputRaw || e.message || "").trim();
}

export function fileTarget(e: ActivityEntry): string {
  const parsed = parseInput(e);
  const p = (parsed?.path || parsed?.file_path || parsed?.filename) as
    | string
    | undefined;
  return (p || e.input || e.message || "").trim();
}

export function searchPattern(e: ActivityEntry): string {
  const parsed = parseInput(e);
  const p = (parsed?.pattern || parsed?.query) as string | undefined;
  return (p || e.input || e.message || "").trim();
}

const HUNK_HEADER_RE = /^@@ -\d+(?:,\d+)? \+\d+(?:,\d+)? @@/;

/**
 * Extracts the unified-diff portion of an Edit tool result. The SDK Edit tool
 * returns a summary line ("Successfully edited …") followed by @@-prefixed
 * hunks describing exactly what changed. Returns "" when the output carries
 * no diff (errors, older runs, non-Edit tools).
 */
export function extractUnifiedDiff(output: string): string {
  if (!output || !output.includes("@@ -")) return "";
  const lines = output.split("\n");
  const start = lines.findIndex((l) => HUNK_HEADER_RE.test(l));
  if (start === -1) return "";
  return lines.slice(start).join("\n");
}

/**
 * Best-effort one-line summary of a tool call's input for tools without a
 * dedicated extractor. Prefers well-known argument keys, then falls back to
 * the first string value in the input object.
 */
const GENERIC_TARGET_KEYS = [
  "command",
  "cmd",
  "path",
  "file_path",
  "filename",
  "pattern",
  "query",
  "url",
  "title",
  "name",
  "id",
  "question",
  "message",
  "description",
  "prompt",
];

export function genericTarget(e: ActivityEntry): string {
  if (e.message) return firstLine(e.message);
  const parsed = parseInput(e);
  if (parsed) {
    for (const key of GENERIC_TARGET_KEYS) {
      const v = parsed[key];
      if (typeof v === "string" && v.trim()) return firstLine(v.trim());
    }
    for (const v of Object.values(parsed)) {
      if (typeof v === "string" && v.trim()) return firstLine(v.trim());
    }
  }
  return firstLine(e.input || "");
}

export function firstLine(s: string): string {
  return s.trim().split("\n")[0];
}

/**
 * First line of a shell script that says what it actually does: skips shebangs,
 * comments, blank lines, and `set -euo pipefail`-style option boilerplate so a
 * multi-line command (e.g. a heredoc file write) doesn't render as "set -euo
 * pipefail". Falls back to the first line when nothing else remains.
 */
export function firstMeaningfulLine(command: string): string {
  const lines = command.split("\n");
  for (const raw of lines) {
    const line = raw.trim();
    if (!line) continue;
    if (line.startsWith("#")) continue; // comments and shebangs
    if (/^set\s+[-+]/.test(line)) continue; // shell option boilerplate
    return line;
  }
  return firstLine(command);
}

export function wallSeconds(entries: ActivityEntry[]): number {
  let min = 0n;
  let max = 0n;
  for (const e of entries) {
    const t = e.timestampUnix;
    // Skip missing/invalid timestamps. A zero or negative value (e.g. an unset
    // time serialized as year 1) would otherwise blow up the span.
    if (t <= 0n) continue;
    if (min === 0n || t < min) min = t;
    if (t > max) max = t;
  }
  if (min === 0n) return 0;
  return Number(max - min);
}

export function formatWall(seconds: number): string {
  if (seconds <= 0) return "";
  if (seconds < 60) return `${seconds}s`;
  const m = Math.floor(seconds / 60);
  const s = seconds % 60;
  if (m < 60) return s > 0 ? `${m}m ${s}s` : `${m}m`;
  const h = Math.floor(m / 60);
  return `${h}h ${m % 60}m`;
}

// ─── Question / plan parsing ────────────────────────────────────────────────

export function parseQuestion(e: ActivityEntry): {
  question: string;
  choices: string[];
} {
  const parsed = parseInput(e);
  if (parsed) {
    const q =
      (parsed.question as string) ||
      (parsed.questions as Array<{ question?: string }> | undefined)?.[0]
        ?.question ||
      "";
    const choices =
      (parsed.choices as string[]) ||
      (
        parsed.questions as
          | Array<{ options?: Array<{ label: string }> }>
          | undefined
      )?.[0]?.options?.map((o) => o.label) ||
      [];
    if (q || choices.length) return { question: q, choices };
  }
  return { question: e.message || e.input || "", choices: [] };
}

export function parsePlan(e: ActivityEntry): {
  summary: string;
  plan: string;
  capturedPlan: string;
  recommended: string;
} {
  const parsed = parseInput(e);
  const capturedPlan = (parsed?.plan as string) || "";
  return {
    summary: (parsed?.summary as string) || "",
    plan: capturedPlan || e.message || e.input || "",
    capturedPlan,
    recommended: (parsed?.recommended as string) || "",
  };
}

export function extractUserAnswer(result: ActivityEntry): string | null {
  const raw = result.output || result.message || "";
  try {
    const parsed = JSON.parse(raw);
    if (parsed.answer) return parsed.answer;
    if (parsed.result) return parsed.result;
    if (parsed.question) return null;
  } catch {
    /* not JSON */
  }
  return raw || null;
}

// ─── System / meta entry labels ─────────────────────────────────────────────

export function systemLabel(e: ActivityEntry): string {
  switch (e.type) {
    case "llm_attempt": {
      const status = e.statusCategory || "started";
      const model = e.llmAttemptModel || e.model || "model";
      if ((status === "retrying" || status === "failed") && e.reason)
        return `LLM ${status}: ${e.reason} (${model})`;
      return `LLM ${status}: ${model}`;
    }
    case "api_retry": {
      const parts: string[] = [];
      if (e.errorCode) parts.push(e.errorCode);
      if (e.errorStatus) parts.push(`HTTP ${e.errorStatus}`);
      if (e.attempt && e.maxRetries) parts.push(`attempt ${e.attempt}/${e.maxRetries}`);
      return parts.length ? `API retry: ${parts.join(", ")}` : e.message || "API retry";
    }
    case "hook_response": {
      const hookLabel = e.hookName || e.hookId || "";
      const prefix = hookLabel ? `[${hookLabel}] ` : "";
      if (e.decision && e.reason) return `${prefix}Hook ${e.decision}: ${e.reason}`;
      return firstLine(e.message || "Hook");
    }
    case "tool_progress": {
      const elapsed = e.elapsedSec ? ` (${e.elapsedSec}s)` : "";
      return firstLine(e.message || "Progress") + elapsed;
    }
    case "system_init":
      return e.model ? `Initialized: ${e.model}` : e.message || "Session initialized";
    case "session_start":
      return e.message || "Session started";
    case "session_state_changed":
      return e.sessionState || e.message || "State changed";
    case "compact_boundary":
      return e.tokensBefore > 0
        ? `Context compacted: ${formatTokens(e.tokensBefore)} → ${formatTokens(e.tokensAfter)} tokens`
        : "Context compacted";
    case "lifecycle_event":
      return e.message || "Lifecycle event";
    case "post_turn_summary":
      return e.summaryTitle || firstLine(e.message || "Summary");
    default:
      return firstLine(e.message || e.input || e.type);
  }
}

export const SYSTEM_TYPES = new Set([
  "llm_attempt",
  "api_retry",
  "hook_response",
  "tool_progress",
  "system_init",
  "session_start",
  "session_state_changed",
  "compact_boundary",
  "lifecycle_event",
  "post_turn_summary",
  "task_result",
]);

