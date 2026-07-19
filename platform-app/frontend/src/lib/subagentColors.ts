/** Color system for subagent types. */

const AGENT_COLORS = [
  { text: "text-blue-400", bg: "bg-blue-500/10", border: "border-blue-500/30", dot: "bg-blue-500" },
  { text: "text-amber-400", bg: "bg-amber-500/10", border: "border-amber-500/30", dot: "bg-amber-500" },
  { text: "text-red-400", bg: "bg-red-500/10", border: "border-red-500/30", dot: "bg-red-500" },
  { text: "text-teal-400", bg: "bg-teal-500/10", border: "border-teal-500/30", dot: "bg-teal-500" },
  { text: "text-yellow-400", bg: "bg-yellow-500/10", border: "border-yellow-500/30", dot: "bg-yellow-500" },
  { text: "text-purple-400", bg: "bg-purple-500/10", border: "border-purple-500/30", dot: "bg-purple-500" },
  { text: "text-pink-400", bg: "bg-pink-500/10", border: "border-pink-500/30", dot: "bg-pink-500" },
  { text: "text-cyan-400", bg: "bg-cyan-500/10", border: "border-cyan-500/30", dot: "bg-cyan-500" },
] as const;

export type SubagentColor = { text: string; bg: string; border: string; dot: string };

const DEFAULT_COLOR: SubagentColor = { text: "text-gray-400", bg: "bg-gray-500/10", border: "border-gray-500/30", dot: "bg-gray-500" };

/** Well-known agent types get stable, hand-picked color assignments. */
const KNOWN_AGENTS: Record<string, number> = {
  "Explore": 0,            // blue
  "Plan": 7,               // cyan
  "general-purpose": 5,    // purple
  "code-reviewer": 1,      // amber
  "security-reviewer": 2,  // red
  "go-reviewer": 3,        // teal
  "go-build-resolver": 3,  // teal
  "python-reviewer": 4,    // yellow
  "build-error-resolver": 1, // amber
};

/**
 * Deterministic FNV-1a hash so an agent type always maps to the same color,
 * independent of render order, session, or which agents were observed first.
 */
function hashAgentType(agentType: string): number {
  let hash = 0x811c9dc5;
  for (let i = 0; i < agentType.length; i++) {
    hash ^= agentType.charCodeAt(i);
    hash = Math.imul(hash, 0x01000193);
  }
  return hash >>> 0;
}

export function getSubagentColor(agentType: string | undefined): SubagentColor {
  if (!agentType) return DEFAULT_COLOR;

  // Well-known agents get their curated color.
  if (agentType in KNOWN_AGENTS) {
    return AGENT_COLORS[KNOWN_AGENTS[agentType]];
  }

  // Everything else maps deterministically by hash.
  return AGENT_COLORS[hashAgentType(agentType) % AGENT_COLORS.length];
}
