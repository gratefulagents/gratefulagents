import { cn } from "@/lib/utils";

/** Segmented progress meter for onboarding cards: one pip per step. */
export function ProgressPips({
  done,
  total,
  className,
}: {
  done: number;
  total: number;
  className?: string;
}) {
  return (
    <span aria-hidden className={cn("flex items-center gap-[3px]", className)}>
      {Array.from({ length: total }, (_, i) => (
        <span
          key={i}
          className={cn(
            "h-[4px] w-3.5 rounded-full transition-colors",
            i < done ? "bg-primary" : "bg-foreground/[0.12]",
          )}
        />
      ))}
    </span>
  );
}
