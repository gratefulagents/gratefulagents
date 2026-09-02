/**
 * Color system for subagent types.
 *
 * Every entry resolves to the theme-aware `--agent-<hue>` / `--agent-<hue>-fg`
 * tokens in index.css, so chips pass contrast in both light and dark mode.
 */

// Class names are spelled out literally so Tailwind's scanner emits them.
const AGENT_COLORS = [
  { text: "text-agent-blue-fg", bg: "bg-agent-blue/10", border: "border-agent-blue/30", ring: "ring-agent-blue/30", dot: "bg-agent-blue", cssVar: "var(--agent-blue)" },
  { text: "text-agent-amber-fg", bg: "bg-agent-amber/10", border: "border-agent-amber/30", ring: "ring-agent-amber/30", dot: "bg-agent-amber", cssVar: "var(--agent-amber)" },
  { text: "text-agent-red-fg", bg: "bg-agent-red/10", border: "border-agent-red/30", ring: "ring-agent-red/30", dot: "bg-agent-red", cssVar: "var(--agent-red)" },
  { text: "text-agent-teal-fg", bg: "bg-agent-teal/10", border: "border-agent-teal/30", ring: "ring-agent-teal/30", dot: "bg-agent-teal", cssVar: "var(--agent-teal)" },
  { text: "text-agent-yellow-fg", bg: "bg-agent-yellow/10", border: "border-agent-yellow/30", ring: "ring-agent-yellow/30", dot: "bg-agent-yellow", cssVar: "var(--agent-yellow)" },
  { text: "text-agent-purple-fg", bg: "bg-agent-purple/10", border: "border-agent-purple/30", ring: "ring-agent-purple/30", dot: "bg-agent-purple", cssVar: "var(--agent-purple)" },
  { text: "text-agent-pink-fg", bg: "bg-agent-pink/10", border: "border-agent-pink/30", ring: "ring-agent-pink/30", dot: "bg-agent-pink", cssVar: "var(--agent-pink)" },
  { text: "text-agent-cyan-fg", bg: "bg-agent-cyan/10", border: "border-agent-cyan/30", ring: "ring-agent-cyan/30", dot: "bg-agent-cyan", cssVar: "var(--agent-cyan)" },
] as const;

export type SubagentColor = {
  /** AA-readable text (`--agent-*-fg`). */
  text: string;
  /** Soft tinted fill. */
  bg: string;
  /** Tinted border; callers may swap the `border-` prefix for `ring-`. */
  border: string;
  /** Tinted focus/inset ring, same hue as `border`. */
  ring: string;
  /** Solid hue anchor for dots and bars. */
  dot: string;
  /** Raw CSS variable of the hue anchor for inline styles (SVG, gradients). */
  cssVar: string;
};

const DEFAULT_COLOR: SubagentColor = { text: "text-agent-gray-fg", bg: "bg-agent-gray/10", border: "border-agent-gray/30", ring: "ring-agent-gray/30", dot: "bg-agent-gray", cssVar: "var(--agent-gray)" };

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
