import { cn } from "@/lib/utils";

export type LiveDotTone = "running" | "waiting" | "success" | "danger" | "info" | "idle";

const toneClass: Record<LiveDotTone, string> = {
  running: "bg-tone-running",
  waiting: "bg-tone-warning",
  success: "bg-tone-success",
  danger: "bg-tone-danger",
  info: "bg-tone-info",
  idle: "bg-muted-foreground/50",
};

/**
 * LiveDot is the single "is this alive?" glyph for the run screen. Every
 * surface that previously hand-rolled a ping/pulse/spinner dot uses this so
 * liveness reads the same in the timeline, the dock, the graph and the panes.
 *
 * `pulse` animates only while the referenced thing is actually in motion;
 * settled states render a static dot. The halo is a separate element so it
 * can ping without changing the dot's layout box.
 */
export function LiveDot({
  tone = "idle",
  pulse = false,
  size = "sm",
  className,
  label,
}: {
  tone?: LiveDotTone;
  pulse?: boolean;
  size?: "xs" | "sm" | "md";
  className?: string;
  /** Optional accessible name; omit when an adjacent label already describes state. */
  label?: string;
}) {
  const dim = size === "xs" ? "size-1.5" : size === "md" ? "size-2.5" : "size-2";
  return (
    <span
      className={cn("relative inline-flex shrink-0", dim, className)}
      role={label ? "img" : undefined}
      aria-label={label}
      aria-hidden={label ? undefined : true}
      data-tone={tone}
    >
      {pulse && (
        <span
          className={cn(
            "absolute inline-flex size-full animate-ping rounded-full opacity-60",
            toneClass[tone],
          )}
        />
      )}
      <span className={cn("relative inline-flex size-full rounded-full", toneClass[tone])} />
    </span>
  );
}
