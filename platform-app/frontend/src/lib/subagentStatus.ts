/**
 * Single status vocabulary for every sub-agent surface (transcript cards, DAG
 * card, active dock, graph tab). Mirrors internal/dashboard/subagent_status.go:
 * a raw status the SDK has never emitted before must never render as success
 * or as work that runs forever — it lands in "unknown" and is shown neutrally.
 */
export type SubagentStatusCategory =
  | "live"
  | "waiting"
  | "succeeded"
  | "failed"
  | "stopped"
  | "unknown";

const CATEGORIES: Record<string, SubagentStatusCategory> = {
  "": "live",
  running: "live",
  started: "live",
  initializing: "live",
  resumed: "live",

  waiting: "waiting",
  pending: "waiting",
  queued: "waiting",
  reconciling: "waiting",
  dependency_wait: "waiting",
  parent_wait: "waiting",
  managed_wait: "waiting",

  completed: "succeeded",
  succeeded: "succeeded",
  success: "succeeded",
  done: "succeeded",

  failed: "failed",
  failure: "failed",
  error: "failed",
  errored: "failed",
  timeout: "failed",
  timed_out: "failed",
  killed: "failed",
  panicked: "failed",

  stopped: "stopped",
  cancelled: "stopped",
  canceled: "stopped",
  interrupted: "stopped",
};

export function classifySubagentStatus(status: string | undefined | null): SubagentStatusCategory {
  const key = (status ?? "").trim().toLowerCase();
  return Object.hasOwn(CATEGORIES, key) ? CATEGORIES[key] : "unknown";
}

/** Finished work: success, failure, or a stop. Unknown strings are not terminal. */
export function isTerminalSubagentStatus(status: string | undefined | null): boolean {
  const category = classifySubagentStatus(status);
  return category === "succeeded" || category === "failed" || category === "stopped";
}

/** Actively working (a missing status means the task has started but not reported). */
export function isLiveSubagentStatus(status: string | undefined | null): boolean {
  return classifySubagentStatus(status) === "live";
}

/** Alive but gated on other work (DAG scheduling, reconciliation). */
export function isWaitingSubagentStatus(status: string | undefined | null): boolean {
  return classifySubagentStatus(status) === "waiting";
}

export function isFailedSubagentStatus(status: string | undefined | null): boolean {
  return classifySubagentStatus(status) === "failed";
}

export function isStoppedSubagentStatus(status: string | undefined | null): boolean {
  return classifySubagentStatus(status) === "stopped";
}

export function isSucceededSubagentStatus(status: string | undefined | null): boolean {
  return classifySubagentStatus(status) === "succeeded";
}

/** Short badge text for a non-live status; unknown statuses show their raw value. */
export function subagentStatusLabel(status: string | undefined | null): string {
  const category = classifySubagentStatus(status);
  switch (category) {
    case "live":
      return "running";
    case "waiting":
      return "waiting";
    case "succeeded":
      return "completed";
    case "failed":
      return (status ?? "").trim().toLowerCase() === "failed" ? "failed" : `failed (${(status ?? "").trim()})`;
    case "stopped":
      return "stopped";
    default:
      return `unknown status: ${(status ?? "").trim() || "(empty)"}`;
  }
}
