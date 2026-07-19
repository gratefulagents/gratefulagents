import { cn } from "@/lib/utils";
import type { ReactNode } from "react";

export function Kbd({
  children,
  className,
}: {
  children: ReactNode;
  className?: string;
}) {
  return (
    <kbd
      className={cn(
        "inline-flex items-center justify-center",
        "min-w-[1.25rem] h-[18px] px-[5px]",
        "font-mono text-[10.5px] tracking-tight tabular-nums",
        "bg-muted/60 text-muted-foreground",
        "border border-border/70 rounded-[5px]",
        "shadow-[inset_0_-1px_0_0_oklch(0_0_0_/_0.25)]",
        className,
      )}
    >
      {children}
    </kbd>
  );
}

export function KbdGroup({ keys }: { keys: string[] }) {
  return (
    <span className="inline-flex items-center gap-0.5">
      {keys.map((k, i) => (
        <Kbd key={i}>{k}</Kbd>
      ))}
    </span>
  );
}
