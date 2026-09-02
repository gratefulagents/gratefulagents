import { getSubagentColor } from "@/lib/subagentColors";
import { cn } from "@/lib/utils";

/**
 * Compact sub-agent type label used everywhere an agent is named: timeline
 * cards, the active-agents dock, the graph nodes, trace rows. One component so
 * the chip is the same size, radius and hue in every surface.
 *
 * `short` renders the 3-letter mono code the graph uses at small scales;
 * otherwise the full type name is shown.
 */
export function AgentTypeChip({
  type,
  short = false,
  root = false,
  className,
}: {
  /** Agent type such as "explore" or "code-reviewer". */
  type: string;
  short?: boolean;
  /** Root/orchestrator styling (neutral). */
  root?: boolean;
  className?: string;
}) {
  const color = getSubagentColor(type);
  const label = root ? "ROOT" : short ? shortAgentCode(type) : type;
  return (
    <span
      title={root ? "Root agent" : type}
      className={cn(
        "inline-flex h-[18px] max-w-[140px] items-center truncate rounded-sm px-1.5 font-mono text-3xs font-semibold tracking-[0.06em] ring-1 ring-inset",
        short && "min-w-[34px] justify-center uppercase",
        root ? "bg-muted text-muted-foreground ring-border" : cn(color.bg, color.text, color.ring),
        className,
      )}
    >
      {label}
    </span>
  );
}

/** First alphanumeric run of the type, upper-cased and clipped to 3 chars. */
export function shortAgentCode(type: string): string {
  const raw = (type || "").toUpperCase();
  if (!raw) return "•";
  const m = raw.match(/[A-Z0-9]+/);
  return (m ? m[0] : raw).slice(0, 3);
}
