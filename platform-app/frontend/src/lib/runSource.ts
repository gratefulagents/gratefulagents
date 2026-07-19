import type { AgentRun } from "@/rpc/platform/service_pb";

/**
 * A run's `project` ref carries the source it was created from: dashboard
 * Projects use kind "Project", while trigger-owned runs carry their trigger
 * kind (SlackAgent, Cron, GitHubRepository, LinearProject). Only true
 * Project runs belong in the Projects tree — trigger runs live under their
 * Sources pages.
 */
const TRIGGER_KIND_LABELS: Record<string, string> = {
  SlackAgent: "Slack",
  GitHubRepository: "GitHub",
  LinearProject: "Linear",
  Cron: "Cron",
};

/**
 * Grouping key for a run inside the Projects tree, or null when the run is
 * trigger-owned. The ref has no namespace, so scope by the run's own
 * namespace to avoid cross-namespace name collisions.
 */
export function projectRunKey(run: AgentRun): string | null {
  const ref = run.project;
  if (!ref?.name) return null;
  if (ref.kind && ref.kind !== "Project") return null;
  return `${run.namespace}/${ref.name}`;
}

/**
 * Short human label for where a run came from: the project name for Project
 * runs, or "Slack · my-agent" style for trigger runs.
 */
export function runSourceLabel(run: AgentRun): string {
  const ref = run.project;
  if (!ref?.name) return "";
  if (projectRunKey(run)) return ref.name;
  const kind = TRIGGER_KIND_LABELS[ref.kind] ?? ref.kind;
  return `${kind} · ${ref.name}`;
}
