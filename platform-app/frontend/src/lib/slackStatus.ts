import type { StatusTone } from "@/lib/status";

/**
 * Shared Slack-agent status mapping — single source of truth for the Slack
 * list, editor, and detail pages so the same state always renders the same
 * tone + label.
 */
export interface SlackAgentStatusInput {
  suspended: boolean;
  connected: boolean;
  ready: boolean;
  /** Pass false for a not-yet-created draft so it reads "New". */
  configured?: boolean;
}

export interface SlackAgentStatus {
  tone: StatusTone;
  label: string;
}

export function slackAgentStatus(agent: SlackAgentStatusInput): SlackAgentStatus {
  if (agent.suspended) return { tone: "neutral", label: "Suspended" };
  if (agent.connected) return { tone: "success", label: "Connected" };
  if (agent.ready) return { tone: "info", label: "Ready" };
  if (agent.configured === false) return { tone: "purple", label: "New" };
  return { tone: "warning", label: "Pending" };
}
