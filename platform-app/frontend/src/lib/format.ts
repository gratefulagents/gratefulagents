/** Shared formatting helpers used across list/detail screens. */

/**
 * Compact relative age from a unix-seconds bigint: "3m", "5h", "2d".
 * Pass `nowMs` (e.g. from useNow) so the value re-renders live.
 */
export function formatAge(unix: bigint, nowMs = Date.now()): string {
  if (unix === 0n) return "-";
  const seconds = Math.floor(nowMs / 1000 - Number(unix));
  if (seconds < 0) return "0s";
  if (seconds < 60) return `${seconds}s`;
  if (seconds < 3600) return `${Math.floor(seconds / 60)}m`;
  if (seconds < 86400) return `${Math.floor(seconds / 3600)}h`;
  return `${Math.floor(seconds / 86400)}d`;
}

/** Relative "last polled" time: "45s ago", "12m ago", "3h ago". */
export function formatPollTime(unix: bigint, nowMs = Date.now()): string {
  if (unix === 0n) return "Never";
  const seconds = Math.floor((nowMs - Number(unix) * 1000) / 1000);
  if (seconds < 60) return `${Math.max(seconds, 0)}s ago`;
  if (seconds < 3600) return `${Math.floor(seconds / 60)}m ago`;
  if (seconds < 86400) return `${Math.floor(seconds / 3600)}h ago`;
  return new Date(Number(unix) * 1000).toLocaleString();
}

/** Past/future schedule time: "in 2h", "3m ago"; full date beyond a day. */
export function formatScheduleTime(unix: bigint, nowMs = Date.now()): string {
  if (unix === 0n) return "Never";
  const d = new Date(Number(unix) * 1000);
  const seconds = Math.floor((d.getTime() - nowMs) / 1000);
  const elapsed = Math.abs(seconds);
  if (seconds >= 0) {
    if (elapsed < 60) return `in ${elapsed}s`;
    if (elapsed < 3600) return `in ${Math.floor(elapsed / 60)}m`;
    if (elapsed < 86400) return `in ${Math.floor(elapsed / 3600)}h`;
  } else {
    if (elapsed < 60) return `${elapsed}s ago`;
    if (elapsed < 3600) return `${Math.floor(elapsed / 60)}m ago`;
    if (elapsed < 86400) return `${Math.floor(elapsed / 3600)}h ago`;
  }
  return d.toLocaleString();
}

/** Compact token/number count: 1.2K, 3.4M. */
export function formatCount(n: number | bigint): string {
  const v = Number(n);
  if (v >= 1_000_000) return `${(v / 1_000_000).toFixed(1)}M`;
  if (v >= 1_000) return `${(v / 1_000).toFixed(1)}K`;
  return v.toLocaleString();
}

/** USD cost; returns "-" for zero. */
export function formatCostUsd(n: number): string {
  if (!n) return "-";
  return `$${n.toFixed(2)}`;
}

/**
 * Success rate over *finished* runs only; "—" until at least one run has
 * completed (so a project with only running runs doesn't read as "0%").
 */
export function formatSuccessRate(success: number, failed: number): string {
  const finished = success + failed;
  if (finished <= 0) return "—";
  return `${Math.round((success / finished) * 100)}%`;
}

/**
 * Compact repo display: strips protocol/host (and trailing ".git") from
 * common forge URLs so tables show "owner/repo" instead of a full URL.
 */
export function formatRepoShort(url: string): string {
  if (!url) return "";
  return url
    .replace(/^[a-z]+:\/\/[^/]+\//i, "")
    .replace(/^git@[^:]+:/i, "")
    .replace(/\.git$/i, "")
    .replace(/\/+$/, "");
}
