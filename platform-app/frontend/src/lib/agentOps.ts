import type { AgentRun } from "@/rpc/platform/service_pb";
import { isDonePhase, isLivePhase, type StatusTone } from "@/lib/status";
import { isActionableInputType } from "@/lib/runStatus";

export type OpsAttentionKind =
  | "input"
  | "review"
  | "failed"
  | "blocked"
  | "runtime"
  | "none";

export type OpsAttention = {
  kind: OpsAttentionKind;
  label: string;
  detail: string;
  tone: StatusTone;
  rank: number;
};

export type OpsBucket = "attention" | "active" | "queued" | "completed";
export type OpsAction = "stop" | "extend" | "promote" | "retry";

const REVIEW_CHANGE_VERDICTS = new Set([
  "changes_requested",
  "request_changes",
  "requested_changes",
]);

export function runModeLabel(run: AgentRun): string {
  return run.modeName || run.workflowMode || "Unknown";
}

export function runRepoLabel(run: AgentRun): string {
  if (!run.repoUrl) return "No repository";
  return run.repoUrl
    .replace(/^[a-z]+:\/\/[^/]+\//i, "")
    .replace(/^git@[^:]+:/i, "")
    .replace(/\.git$/i, "")
    .replace(/\/+$/, "");
}

export function runSourceKind(run: AgentRun): string {
  return run.trigger?.kind || run.project?.kind || "Direct";
}

export function runSourceName(run: AgentRun): string {
  return run.trigger?.name || run.project?.name || "Direct";
}

export function runSourceLabel(run: AgentRun): string {
  const kindLabels: Record<string, string> = {
    Project: "Project",
    LinearProject: "Linear",
    GitHubRepository: "GitHub",
    SlackAgent: "Slack",
    Cron: "Cron",
  };
  const kind = runSourceKind(run);
  const name = runSourceName(run);
  return name === "Direct" ? name : `${kindLabels[kind] || kind} · ${name}`;
}

export function runSourcePath(run: AgentRun): string | null {
  const name = run.trigger?.name || run.project?.name;
  const kind = run.trigger?.kind || run.project?.kind;
  if (!name || !kind) return null;
  const prefix: Record<string, string> = {
    Project: "/projects",
    LinearProject: "/projects",
    GitHubRepository: "/github",
    SlackAgent: "/slack",
    Cron: "/cron",
  };
  return prefix[kind] ? `${prefix[kind]}/${run.namespace}/${name}` : null;
}

function userInputLabel(type: string): string {
  switch (type) {
    case "approval":
    case "plan_review":
      return "Approval";
    case "turn_limit":
      return "Turn limit";
    case "idle":
      return "Idle";
    case "circuit_breaker":
      return "Circuit breaker";
    case "stopped":
      return "Stopped";
    default:
      return "Input needed";
  }
}

/**
 * Derives a single, operator-focused reason to inspect a run. The ordering is
 * deliberate: direct user requests beat automated failure and blocker signals.
 */
export function getRunAttention(run: AgentRun): OpsAttention {
  // Interactive and PR-loop fields can briefly outlive the worker that produced
  // them. Once a run has reached a non-failure terminal phase, those signals
  // are historical context—not work an operator can still act on.
  if (isDonePhase(run.phase) && run.phase !== "Failed" && run.phase !== "Error") {
    return { kind: "none", label: "", detail: "", tone: "neutral", rank: 99 };
  }

  const request = run.userInputRequest;
  const requestType = request?.type || "";
  if (requestType === "circuit_breaker") {
    return {
      kind: "blocked",
      label: "Agent needs help",
      detail: request?.message || run.blockedReason || "The circuit breaker stopped this run.",
      tone: "danger",
      rank: 0,
    };
  }
  // Legacy sessions can surface pending actions or a prompt without a typed
  // input request; treat those as actionable input too. Typed non-actionable
  // requests (idle/stopped) are deliberately excluded.
  const legacyUntypedInput = !requestType && (Boolean(request?.message) || run.pendingActions.length > 0);
  if (isActionableInputType(requestType) || legacyUntypedInput) {
    return {
      kind: "input",
      label: userInputLabel(request?.type || "question"),
      detail: request?.message || "The agent is waiting for a response.",
      tone: "purple",
      rank: 0,
    };
  }

  const loop = run.prLoop;
  const loopState = loop?.state || "";
  const verdict = loop?.reviewVerdict.toLowerCase() || "";
  if (
    loopState === "blocked" ||
    loopState === "resolving" ||
    REVIEW_CHANGE_VERDICTS.has(verdict)
  ) {
    return {
      kind: "review",
      label: loopState === "blocked" ? "Review blocked" : "Changes requested",
      detail: loop?.reviewSummary || "The pull request review needs another pass.",
      tone: loopState === "blocked" ? "danger" : "warning",
      rank: 1,
    };
  }

  // A timeout-paused run is intentionally dormant and can be resumed later by
  // extending its runtime. The timeout alone is not an operator alert.
  if (
    run.phase === "Paused" &&
    /\b(?:timeout|time(?:d)?\s+out|runtime\s+(?:expired|expiration)|maxruntime)\b/i.test(run.blockedReason)
  ) {
    return { kind: "none", label: "", detail: "", tone: "neutral", rank: 99 };
  }

  if (run.phase === "Failed" || run.phase === "Error") {
    return {
      kind: "failed",
      label: "Failed",
      detail: run.lastError || "The run ended with an error.",
      tone: "danger",
      rank: 2,
    };
  }

  if (run.phase === "Paused" || /runtime|expired|expiration/i.test(run.blockedReason)) {
    return {
      kind: "runtime",
      label: "Runtime expired",
      detail: run.blockedReason || "Extend the runtime to resume this run.",
      tone: "warning",
      rank: 3,
    };
  }

  // Non-computing input requests (idle/stopped) mirror their type into the
  // queue's blocked reason. That mirror is presentation state, not a blocker:
  // idle runs are ready for input and stopped runs are intentionally dormant.
  const nonComputingMirror =
    (requestType === "idle" || requestType === "stopped") &&
    run.blockedReason.toLowerCase() === requestType;
  if (run.phase === "Blocked" || (run.blockedReason && run.queueState !== "Queued" && !nonComputingMirror)) {
    return {
      kind: "blocked",
      label: "Blocked",
      detail: run.blockedReason || "The run cannot make progress.",
      tone: "warning",
      rank: 4,
    };
  }

  // Spend remains visible and sortable in Agent Ops, but does not require an
  // operator response by itself.
  return { kind: "none", label: "", detail: "", tone: "neutral", rank: 99 };
}

export function getRunBucket(run: AgentRun): OpsBucket {
  if (getRunAttention(run).kind !== "none") return "attention";
  if (run.queueState === "Queued" || run.phase === "Pending" || run.phase === "Queued") return "queued";
  if (isLivePhase(run.phase) || !isDonePhase(run.phase)) return "active";
  return "completed";
}

export function canRunAction(run: AgentRun, action: OpsAction): boolean {
  const owner = run.myPermission === "owner" || run.myPermission === "admin";
  const canCollaborate = run.myPermission !== "viewer";
  switch (action) {
    case "stop":
    case "promote":
      return owner && !isDonePhase(run.phase);
    case "retry":
      return owner && (run.phase === "Failed" || run.phase === "Cancelled");
    case "extend":
      return canCollaborate && Boolean(run.phase) && !isDonePhase(run.phase);
  }
}

export function latestRunActivity(run: AgentRun) {
  return [...run.recentActivity].sort((a, b) => Number(b.timestampUnix - a.timestampUnix))[0];
}

/** Stable key for recognizing multiple runs created for the same external task. */
export function runComparisonKey(run: AgentRun): string {
  const trigger = run.trigger;
  if (!trigger) return "";
  if (trigger.externalId) return `${trigger.kind || "source"}:id:${trigger.externalId}`;
  if (trigger.externalUrl) return `${trigger.kind || "source"}:url:${trigger.externalUrl.trim().toLowerCase()}`;
  if (trigger.externalIdentifier && trigger.name) {
    return `${run.namespace}:${trigger.kind || "source"}:${trigger.name}:${trigger.externalIdentifier}`;
  }
  return "";
}

export function runDurationSeconds(run: AgentRun, nowMs = Date.now()): number {
  const start = run.startedAtUnix || run.createdAtUnix;
  if (start === 0n) return 0;
  const end = run.completedAtUnix || BigInt(Math.floor(nowMs / 1000));
  return Math.max(0, Number(end - start));
}
