import { cn } from "@/lib/utils";
import { fmtTokens } from "@/components/run-session/helpers";

/**
 * Slim context-budget meter: how full the model's context window is and where
 * history compaction kicks in. Labeled "Context" and expressed as a percentage
 * of the compaction trigger so it reads as window pressure, not as the
 * cumulative input/output token stats shown elsewhere. Hidden until both the
 * current usage and the compaction trigger (agent-published) are known.
 */
export function ContextUsageBar({
  usedTokens,
  triggerTokens,
  targetTokens,
  className,
}: {
  usedTokens: number | null;
  triggerTokens: number;
  targetTokens: number;
  className?: string;
}) {
  if (!usedTokens || usedTokens <= 0 || triggerTokens <= 0) return null;

  const ratio = usedTokens / triggerTokens;
  const percent = Math.min(100, Math.round(ratio * 100));
  const tone =
    ratio >= 0.9
      ? "bg-[color:var(--tone-danger)]"
      : ratio >= 0.7
        ? "bg-[color:var(--tone-warning)]"
        : "bg-[color:var(--color-primary)]";

  const detail =
    `Context window: ${fmtTokens(usedTokens)} of the ${fmtTokens(triggerTokens)} compaction trigger (${percent}%). ` +
    `When it fills up, older history is compacted` +
    (targetTokens > 0 ? ` down to ~${fmtTokens(targetTokens)}.` : ".");

  return (
    <span
      className={cn("flex items-center gap-1.5 font-mono text-[11px] tabular-nums text-muted-foreground", className)}
      title={detail}
      role="meter"
      aria-valuemin={0}
      aria-valuemax={100}
      aria-valuenow={percent}
      aria-label="Context window usage until compaction"
    >
      <span className="font-sans text-muted-foreground/70">Context</span>
      <span className="relative h-[5px] w-[72px] overflow-hidden rounded-full bg-muted/70 ring-1 ring-inset ring-border/50">
        <span
          className={cn("absolute inset-y-0 left-0 rounded-full transition-[width] duration-500", tone)}
          style={{ width: `${Math.max(2, percent)}%` }}
        />
      </span>
      <span>
        {percent}%
        <span className="hidden md:inline">
          {percent >= 100 ? " · compacting" : ` · compacts at ${fmtTokens(triggerTokens)}`}
        </span>
      </span>
    </span>
  );
}
