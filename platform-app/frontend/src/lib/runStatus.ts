import type { AgentRun } from "@/rpc/platform/service_pb";
import { isDonePhase, isLivePhase, phaseTone, type StatusTone } from "@/lib/status";

const ACTIONABLE_INPUT_TYPES = new Set([
  "question",
  "approval",
  "plan_review",
  "turn_limit",
]);

const NON_COMPUTING_INPUT_TYPES = new Set([
  ...ACTIONABLE_INPUT_TYPES,
  "idle",
  "stopped",
  "circuit_breaker",
]);

export function isActionableInputType(type?: string): boolean {
  return ACTIONABLE_INPUT_TYPES.has(type || "");
}

export function isNonComputingInputType(type?: string): boolean {
  return NON_COMPUTING_INPUT_TYPES.has(type || "");
}

export function visibleInputType(run: AgentRun): string {
  // The CRD phase is authoritative once execution has paused or ended. A
  // durable session can still carry the input request from the preceding
  // turn, but that stale request must not present the run as ready/actionable.
  if (run.phase === "Paused" || isDonePhase(run.phase)) return "";
  return run.userInputRequest?.type || "";
}

export function isRunComputing(run: AgentRun): boolean {
  if (!isLivePhase(run.phase) || isDonePhase(run.phase)) return false;
  return !isNonComputingInputType(visibleInputType(run));
}

export function runStatusTone(run: AgentRun): StatusTone {
  switch (visibleInputType(run)) {
    case "idle":
    case "stopped":
      return "neutral";
    case "circuit_breaker":
      return "danger";
    case "question":
    case "approval":
    case "plan_review":
    case "turn_limit":
      return "purple";
    default:
      return phaseTone(run.phase);
  }
}

export function runStatusLabel(run: AgentRun): string {
  if (isDonePhase(run.phase)) return run.phase || "Unknown";
  switch (visibleInputType(run)) {
    case "idle":
      return "Ready";
    case "stopped":
      return "Stopped";
    case "circuit_breaker":
      return "Needs help";
    case "question":
      return "Awaiting input";
    case "approval":
      return "Awaiting approval";
    case "plan_review":
      return "Plan review";
    case "turn_limit":
      return "Turn limit";
    default:
      return run.phase || "Unknown";
  }
}

export function normalizeRunStep(step: string): string {
  return step.trim().toLowerCase().replace(/[\s_]+/g, "-");
}

const STEP_LABELS: Record<string, string> = {
  starting: "Starting run",
  "cloning-repository": "Cloning repository",
  "setting-up-workspace": "Preparing workspace",
  setup: "Preparing workspace",
  exploring: "Exploring codebase",
  planning: "Planning implementation",
  implementing: "Implementing changes",
  implement: "Implementing changes",
  "code-review": "Reviewing code",
  reviewing: "Reviewing code",
  review: "Reviewing code",
  committing: "Committing changes",
  pushing: "Pushing changes",
  push: "Pushing changes",
  "creating-pr": "Creating pull request",
  pr: "Creating pull request",
  "branch-setup": "Setting up branch",
  "chat-followup": "Processing follow-up",
  chatting: "Processing",
  auto: "Working autonomously",
};

export function runStepLabel(step: string): string {
  const normalized = normalizeRunStep(step);
  if (!normalized || normalized === "awaiting-user") return "";
  return STEP_LABELS[normalized] || normalized.replace(/-/g, " ").replace(/^./, (c) => c.toUpperCase());
}

export function runActivitySummary(run: AgentRun, nowMs = Date.now()): { summary: string; timestamp: bigint } {
  if (run.phase === "Succeeded") return { summary: "Completed successfully", timestamp: run.completedAtUnix };
  if (run.phase === "Completed" || run.phase === "Approved" || run.phase === "Cancelled") {
    return { summary: run.phase, timestamp: run.completedAtUnix };
  }
  if (run.phase === "Failed" || run.phase === "Error") {
    return { summary: run.lastError || "Run failed", timestamp: run.completedAtUnix };
  }

  const activity = [...run.recentActivity].sort((a, b) => Number(b.timestampUnix - a.timestampUnix))[0];
  const ageSeconds = activity?.timestampUnix
    ? Math.max(0, Math.floor(nowMs / 1000) - Number(activity.timestampUnix))
    : Number.POSITIVE_INFINITY;
  // Activity snapshots are refreshed frequently while a run is live. Treat
  // older events as history so stale setup text cannot masquerade as current.
  if (activity?.summary && ageSeconds <= 120) {
    return { summary: activity.summary, timestamp: activity.timestampUnix };
  }

  const step = runStepLabel(run.currentStep);
  if (step) return { summary: step, timestamp: 0n };
  if (activity?.summary) return { summary: `Last activity: ${activity.summary}`, timestamp: activity.timestampUnix };
  return { summary: "No recent activity reported", timestamp: 0n };
}
