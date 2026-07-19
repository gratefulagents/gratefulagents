import * as React from "react";
import { cn } from "@/lib/utils";

export interface SegmentedOption<T extends string = string> {
  value: T;
  label: React.ReactNode;
  icon?: React.ReactNode;
}

export interface SegmentedControlProps<T extends string = string> {
  value: T;
  onChange: (value: T) => void;
  options: SegmentedOption<T>[];
  size?: "sm" | "md";
  className?: string;
}

export function SegmentedControl<T extends string = string>({
  value,
  onChange,
  options,
  size = "md",
  className,
}: SegmentedControlProps<T>) {
  const idx = Math.max(0, options.findIndex((o) => o.value === value));
  const ref = React.useRef<HTMLDivElement>(null);
  const [indicator, setIndicator] = React.useState<{ left: number; width: number } | null>(null);

  React.useLayoutEffect(() => {
    const container = ref.current;
    if (!container) return;
    const btn = container.querySelectorAll<HTMLButtonElement>("[data-seg-btn]")[idx];
    if (!btn) return;
    const cRect = container.getBoundingClientRect();
    const bRect = btn.getBoundingClientRect();
    setIndicator({ left: bRect.left - cRect.left, width: bRect.width });
  }, [idx, options.length]);

  return (
    <div
      ref={ref}
      className={cn(
        "relative inline-flex items-center",
        "rounded-[8px] p-[3px]",
        "bg-muted/50 ring-1 ring-inset ring-border/60",
        size === "sm" ? "text-[11.5px]" : "text-[12.5px]",
        className,
      )}
      role="tablist"
    >
      {indicator && (
        <div
          aria-hidden
          className={cn(
            "absolute top-[3px] bottom-[3px] rounded-[6px]",
            "bg-background",
            "shadow-[0_1px_0_0_color-mix(in_oklch,var(--color-foreground)_6%,transparent)_inset,0_1px_2px_0_oklch(0_0_0_/_0.18)]",
            "transition-[left,width] duration-[var(--dur-fast)] ease-[var(--ease-out-quart)]",
          )}
          style={{ left: indicator.left, width: indicator.width }}
        />
      )}
      {options.map((opt, i) => {
        const active = opt.value === value;
        return (
          <button
            key={opt.value}
            type="button"
            data-seg-btn
            role="tab"
            aria-selected={active}
            tabIndex={active ? 0 : -1}
            onClick={() => onChange(opt.value)}
            onKeyDown={(e) => {
              if (e.key !== "ArrowRight" && e.key !== "ArrowLeft") return;
              e.preventDefault();
              const dir = e.key === "ArrowRight" ? 1 : -1;
              const next = (i + dir + options.length) % options.length;
              onChange(options[next].value);
              const btns = ref.current?.querySelectorAll<HTMLButtonElement>("[data-seg-btn]");
              btns?.[next]?.focus();
            }}
            className={cn(
              "relative z-[1] inline-flex items-center gap-1.5",
              size === "sm" ? "h-[22px] px-2" : "h-[26px] px-2.5",
              "rounded-[6px] font-medium",
              "transition-colors duration-[var(--dur-fast)]",
              "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/60",
              active ? "text-foreground" : "text-muted-foreground hover:text-foreground/90",
            )}
          >
            {opt.icon}
            <span className="tracking-tight">{opt.label}</span>
          </button>
        );
      })}
    </div>
  );
}
