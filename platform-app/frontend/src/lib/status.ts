/**
 * Canonical semantic status system.
 *
 * Single source of truth for every status surface in the app (badges, pills,
 * dots, banners, phase rails). Components must map their domain concept to a
 * `StatusTone` and pull classes from here instead of hardcoding tailwind
 * palette colors — this keeps the dark-first graphite/indigo theme coherent
 * and matches the visual language of the graph tab.
 */

export type StatusTone =
  | "neutral"
  | "info"
  | "running"
  | "success"
  | "warning"
  | "danger"
  | "purple";

/** Soft pill treatment: tinted bg + readable text + hairline ring. */
export const toneSoft: Record<StatusTone, string> = {
  neutral: "bg-muted/60 text-muted-foreground ring-1 ring-inset ring-border/70",
  info: "bg-[color-mix(in_oklch,var(--tone-info)_12%,transparent)] text-[color:var(--tone-info-fg)] ring-1 ring-inset ring-[color-mix(in_oklch,var(--tone-info)_28%,transparent)]",
  running:
    "bg-[color-mix(in_oklch,var(--tone-running)_12%,transparent)] text-[color:var(--tone-running-fg)] ring-1 ring-inset ring-[color-mix(in_oklch,var(--tone-running)_30%,transparent)]",
  success:
    "bg-[color-mix(in_oklch,var(--tone-success)_12%,transparent)] text-[color:var(--tone-success-fg)] ring-1 ring-inset ring-[color-mix(in_oklch,var(--tone-success)_28%,transparent)]",
  warning:
    "bg-[color-mix(in_oklch,var(--tone-warning)_12%,transparent)] text-[color:var(--tone-warning-fg)] ring-1 ring-inset ring-[color-mix(in_oklch,var(--tone-warning)_30%,transparent)]",
  danger:
    "bg-[color-mix(in_oklch,var(--tone-danger)_12%,transparent)] text-[color:var(--tone-danger-fg)] ring-1 ring-inset ring-[color-mix(in_oklch,var(--tone-danger)_30%,transparent)]",
  purple:
    "bg-[color-mix(in_oklch,var(--tone-purple)_12%,transparent)] text-[color:var(--tone-purple-fg)] ring-1 ring-inset ring-[color-mix(in_oklch,var(--tone-purple)_30%,transparent)]",
};

/** Solid treatment for the single most-active state (a running run). */
export const toneSolid: Record<StatusTone, string> = {
  neutral: "bg-muted text-muted-foreground",
  info: "bg-[color:var(--tone-info)] text-background",
  running: "bg-[color:var(--tone-running)] text-background",
  success: "bg-[color:var(--tone-success)] text-background",
  warning: "bg-[color:var(--tone-warning)] text-background",
  danger: "bg-[color:var(--tone-danger)] text-background",
  purple: "bg-[color:var(--tone-purple)] text-background",
};

/** Bare foreground color (for dots, icons, connector lines). */
export const toneText: Record<StatusTone, string> = {
  neutral: "text-muted-foreground",
  info: "text-[color:var(--tone-info-fg)]",
  running: "text-[color:var(--tone-running-fg)]",
  success: "text-[color:var(--tone-success-fg)]",
  warning: "text-[color:var(--tone-warning-fg)]",
  danger: "text-[color:var(--tone-danger-fg)]",
  purple: "text-[color:var(--tone-purple-fg)]",
};

/**
 * Raw color values — for inline SVG/style where a class can't be used.
 * CSS vars resolve in inline `style` (not bare SVG attributes); theme-aware.
 */
export const toneColor: Record<StatusTone, string> = {
  neutral: "var(--color-muted-foreground)",
  info: "var(--tone-info)",
  running: "var(--tone-running)",
  success: "var(--tone-success)",
  warning: "var(--tone-warning)",
  danger: "var(--tone-danger)",
  purple: "var(--tone-purple)",
};

const PHASE_TONES: Record<string, StatusTone> = {
  Running: "running",
  Streaming: "running",
  Admitted: "info",
  Provisioning: "info",
  Exploring: "info",
  Refining: "info",
  Pending: "info",
  Queued: "info",
  Question: "purple",
  WaitingApproval: "purple",
  WaitingForUser: "purple",
  Blocked: "warning",
  Paused: "warning",
  Retrying: "warning",
  Succeeded: "success",
  Completed: "success",
  Approved: "success",
  Cancelled: "neutral",
  Failed: "danger",
  Error: "danger",
};

/** Map an AgentRun phase string to a semantic tone. */
export function phaseTone(phase: string): StatusTone {
  return PHASE_TONES[phase] ?? "neutral";
}

const PR_LOOP_TONES: Record<string, StatusTone> = {
  in_review: "info",
  resolving: "warning",
  approved: "success",
  blocked: "danger",
};

/** Map autonomous PR review-loop state to a semantic tone. */
export function prLoopTone(state: string): StatusTone {
  return PR_LOOP_TONES[state] ?? "neutral";
}

/** Phases that represent live, in-flight work (used to drive pulse/shimmer). */
export function isLivePhase(phase: string): boolean {
  const t = phaseTone(phase);
  return t === "running" || phase === "Provisioning" || phase === "Admitted";
}

const DONE_PHASES = new Set([
  "Succeeded",
  "Completed",
  "Approved",
  "Cancelled",
  "Failed",
  "Error",
]);

/** Terminal phases: the run is finished and needs no further attention. */
export function isDonePhase(phase: string): boolean {
  return DONE_PHASES.has(phase);
}
