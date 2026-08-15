import { timestampDate, type Timestamp } from "@bufbuild/protobuf/wkt";

import type { FilterOption } from "@/components/ui/filter-bar";

/**
 * Shared vocabulary for every security filter surface.
 *
 * Severity, status, and time-range options were previously re-declared (with
 * subtly different labels and orderings) on each security page. They live here
 * so the runs list, configuration list, scan detail, config detail, and finding
 * detail all speak the same language and produce identical query strings.
 */

export const SEVERITY_ORDER = ["critical", "high", "medium", "low", "info"] as const;
export type Severity = (typeof SEVERITY_ORDER)[number];

export const SEVERITY_RANK: Record<string, number> = {
  critical: 5,
  high: 4,
  medium: 3,
  low: 2,
  info: 1,
};

export const SEVERITY_FILTER_OPTIONS: FilterOption[] = [
  { value: "all", label: "Any severity" },
  { value: "critical", label: "Critical" },
  { value: "high", label: "High" },
  { value: "medium", label: "Medium" },
  { value: "low", label: "Low" },
  { value: "info", label: "Info" },
];

export const SCAN_STATUS_FILTER_OPTIONS: FilterOption[] = [
  { value: "all", label: "Any status" },
  { value: "running", label: "Running" },
  { value: "completed", label: "Completed" },
  { value: "failed", label: "Failed" },
  { value: "pending", label: "Pending" },
];

export const TIME_RANGE_OPTIONS: FilterOption[] = [
  { value: "all", label: "Any time" },
  { value: "24h", label: "Last 24 hours" },
  { value: "7d", label: "Last 7 days" },
  { value: "30d", label: "Last 30 days" },
];

const RANGE_MS: Record<string, number> = {
  "24h": 24 * 60 * 60 * 1000,
  "7d": 7 * 24 * 60 * 60 * 1000,
  "30d": 30 * 24 * 60 * 60 * 1000,
};

/** True when `whenMs` falls inside the named range. "all" always matches. */
export function withinTimeRange(whenMs: number, range: string, nowMs: number): boolean {
  if (range === "all") return true;
  const window = RANGE_MS[range];
  if (!window) return true;
  if (!whenMs) return false;
  return whenMs >= nowMs - window;
}

/** Milliseconds for a protobuf timestamp, or 0 when absent. */
export function timestampMs(ts: Timestamp | undefined): number {
  return ts ? timestampDate(ts).getTime() : 0;
}

/** Highest severity present in a severity-count map, or "" when clean. */
export function topSeverity(counts: Record<string, number>): string {
  for (const severity of SEVERITY_ORDER) {
    const value = counts[`actionable_${severity}`] ?? counts[severity] ?? 0;
    if (value > 0) return severity;
  }
  return "";
}

/** Total actionable findings across severities in a counts map. */
export function severityCountTotal(counts: Record<string, number>): number {
  return SEVERITY_ORDER.reduce(
    (total, severity) => total + (counts[`actionable_${severity}`] ?? counts[severity] ?? 0),
    0,
  );
}

/** Does a counts map contain at least one finding of `severity` (or worse)? */
export function hasSeverityAtLeast(counts: Record<string, number>, severity: string): boolean {
  const floor = SEVERITY_RANK[severity] ?? 0;
  if (!floor) return severityCountTotal(counts) > 0;
  return SEVERITY_ORDER.some(
    (candidate) =>
      (SEVERITY_RANK[candidate] ?? 0) >= floor
      && (counts[`actionable_${candidate}`] ?? counts[candidate] ?? 0) > 0,
  );
}

/** Normalize a repository URL to `owner/name` for compact display + grouping. */
export function repoLabel(repository: string): string {
  if (!repository) return "";
  const trimmed = repository.replace(/\.git$/, "");
  const parts = trimmed.split("/").filter(Boolean);
  if (parts.length < 2) return trimmed;
  return parts.slice(-2).join("/");
}

/** Build `FilterOption`s from a value list, keeping "all" first and sorting. */
export function optionsFrom(values: Iterable<string>, allLabel: string): FilterOption[] {
  const unique = Array.from(new Set(Array.from(values).filter(Boolean))).sort((a, b) =>
    a.localeCompare(b),
  );
  return [{ value: "all", label: allLabel }, ...unique.map((value) => ({ value, label: value }))];
}
