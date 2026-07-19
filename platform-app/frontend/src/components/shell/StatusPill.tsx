import * as React from "react";
import { cn } from "@/lib/utils";
import { toneSoft, type StatusTone } from "@/lib/status";

/** Legacy variant names kept for back-compat; each maps to a semantic tone. */
type Variant = "default" | "active" | "warning" | "danger" | "success" | "muted";

const variantToTone: Record<Variant, StatusTone> = {
  default: "neutral",
  active: "running",
  warning: "warning",
  danger: "danger",
  success: "success",
  muted: "neutral",
};

export interface StatusPillProps extends React.HTMLAttributes<HTMLSpanElement> {
  /** Preferred: a semantic tone from the status system. */
  tone?: StatusTone;
  /** Legacy alias; ignored when `tone` is provided. */
  variant?: Variant;
  dot?: boolean;
  pulse?: boolean;
}

export function StatusPill({
  tone,
  variant = "default",
  dot = false,
  pulse = false,
  className,
  children,
  ...rest
}: StatusPillProps) {
  const resolved: StatusTone = tone ?? variantToTone[variant];
  return (
    <span
      {...rest}
      className={cn(
        "inline-flex items-center gap-1.5",
        "h-[20px] px-[7px]",
        "text-[11px] font-medium tracking-tight",
        "rounded-full whitespace-nowrap select-none",
        toneSoft[resolved],
        className,
      )}
    >
      {dot && (
        <span className="relative inline-flex size-1.5 rounded-full bg-current">
          {pulse && (
            <span className="absolute inset-0 rounded-full bg-current opacity-60 motion-safe:animate-ping" />
          )}
        </span>
      )}
      {children}
    </span>
  );
}
