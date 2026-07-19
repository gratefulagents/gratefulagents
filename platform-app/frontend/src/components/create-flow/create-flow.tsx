import { createContext, useContext, useId, useState } from "react";
import { ChevronDown } from "lucide-react";

import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from "@/components/ui/collapsible";
import { Label } from "@/components/ui/label";
import { cn } from "@/lib/utils";

/**
 * Create-flow kit — the shared visual language for every "create X" surface.
 *
 * Philosophy: a create dialog shows only the few fields that matter and folds
 * everything else into flat, hairline-separated OptionRows whose collapsed
 * state reads as a live summary of the current values. One surface, no nested
 * boxes.
 */

/* ── Field ────────────────────────────────────────────────────── */

export function FlowField({
  id,
  label,
  hint,
  required,
  aside,
  className,
  children,
}: {
  id?: string;
  label: string;
  /** Muted helper line under the control. */
  hint?: React.ReactNode;
  required?: boolean;
  /** Small element on the label line's right edge (e.g. a Saved pill). */
  aside?: React.ReactNode;
  className?: string;
  children: React.ReactNode;
}) {
  return (
    <div className={cn("space-y-1.5", className)}>
      <div className="flex min-h-4 items-center justify-between gap-2">
        <Label htmlFor={id} className="text-[12.5px]">
          {label}
          {required ? <span className="text-destructive"> *</span> : null}
        </Label>
        {aside}
      </div>
      {children}
      {hint ? (
        <p className="text-[11px] leading-relaxed text-muted-foreground">{hint}</p>
      ) : null}
    </div>
  );
}

/* ── Chips — quiet pill choices (presets, providers) ──────────── */

export function Chip({
  selected,
  onSelect,
  mono,
  children,
  className,
}: {
  selected: boolean;
  onSelect: () => void;
  mono?: boolean;
  children: React.ReactNode;
  className?: string;
}) {
  return (
    <button
      type="button"
      onClick={onSelect}
      aria-pressed={selected}
      className={cn(
        "inline-flex h-6.5 items-center gap-1.5 rounded-full px-2.5 text-[11.5px] transition-colors duration-[var(--dur-fast)]",
        "ring-1 ring-inset focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/60",
        mono && "font-mono text-[11px]",
        selected
          ? "bg-[color:var(--color-primary)]/12 text-foreground ring-[color:var(--color-primary)]/40"
          : "text-muted-foreground ring-border/70 hover:text-foreground hover:ring-ring/40",
        className,
      )}
    >
      {children}
    </button>
  );
}

/* ── Segmented — small two/three-way switch ───────────────────── */

export function Segmented<T extends string>({
  value,
  onChange,
  options,
  className,
  "aria-label": ariaLabel,
}: {
  value: T;
  onChange: (value: T) => void;
  options: { value: T; label: string }[];
  className?: string;
  "aria-label"?: string;
}) {
  return (
    <div
      className={cn("inline-flex items-center rounded-lg bg-muted p-0.5", className)}
      role="group"
      aria-label={ariaLabel}
    >
      {options.map((opt) => (
        <button
          key={opt.value}
          type="button"
          aria-pressed={value === opt.value}
          onClick={() => onChange(opt.value)}
          className={cn(
            "rounded-md px-3 py-1 text-xs font-medium transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/60",
            value === opt.value
              ? "bg-background text-foreground shadow-sm"
              : "text-muted-foreground hover:text-foreground",
          )}
        >
          {opt.label}
        </button>
      ))}
    </div>
  );
}

/* ── Option rows — flat disclosure stack ──────────────────────── */

const OptionRowsContext = createContext(false);

/**
 * Stack of OptionRow disclosures separated by hairlines. Deliberately
 * borderless on the sides so it reads as part of the page, not another box.
 */
export function OptionRows({
  label,
  className,
  children,
}: {
  /** Small eyebrow above the stack, e.g. "Options". */
  label?: string;
  className?: string;
  children: React.ReactNode;
}) {
  return (
    <div className={className}>
      {label ? (
        <div className="pb-1 text-[11px] font-medium tracking-wide text-muted-foreground/80 uppercase">
          {label}
        </div>
      ) : null}
      <div className="divide-y divide-border/60 border-y border-border/60">
        <OptionRowsContext.Provider value={true}>{children}</OptionRowsContext.Provider>
      </div>
    </div>
  );
}

/**
 * One collapsible row: icon · title · live summary · chevron. Expands in
 * place with no extra border. `summary` should read like a receipt of the
 * current values ("Anthropic · saved credentials"); set `modified` when the
 * group differs from its defaults to light up the indicator dot.
 */
export function OptionRow({
  icon: Icon,
  title,
  summary,
  modified,
  defaultOpen = false,
  children,
}: {
  icon?: React.ComponentType<{ className?: string }>;
  title: string;
  summary?: React.ReactNode;
  modified?: boolean;
  defaultOpen?: boolean;
  children: React.ReactNode;
}) {
  const inStack = useContext(OptionRowsContext);
  const [open, setOpen] = useState(defaultOpen);
  const panelId = useId();

  return (
    <Collapsible open={open} onOpenChange={setOpen} className={cn(!inStack && "border-y border-border/60")}>
      <CollapsibleTrigger
        render={
          <button
            type="button"
            aria-controls={panelId}
            className={cn(
              "group flex w-full items-center gap-2.5 py-2.5 text-left",
              "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/60 focus-visible:ring-inset",
            )}
          />
        }
      >
        {Icon ? (
          <Icon className="size-4 shrink-0 text-muted-foreground transition-colors group-hover:text-foreground" />
        ) : null}
        <span className="flex min-w-0 flex-1 items-baseline gap-2">
          <span className="shrink-0 text-[13px] font-medium">{title}</span>
          {modified ? (
            <span
              aria-hidden
              className="size-1.5 shrink-0 self-center rounded-full bg-[color:var(--color-primary)]/70"
            />
          ) : null}
        </span>
        {!open && summary ? (
          <span className="min-w-0 max-w-[55%] truncate text-right text-[12px] text-muted-foreground">
            {summary}
          </span>
        ) : null}
        <ChevronDown
          className={cn(
            "size-3.5 shrink-0 text-muted-foreground/70 transition-transform duration-[var(--dur-fast)]",
            open && "rotate-180",
          )}
        />
      </CollapsibleTrigger>
      <CollapsibleContent id={panelId}>
        <div className={cn("space-y-4 pb-4 pt-1", Icon && "pl-6.5")}>{children}</div>
      </CollapsibleContent>
    </Collapsible>
  );
}

/* ── Switch-style inline row label ────────────────────────────── */

export function FlowSwitchRow({
  id,
  label,
  hint,
  control,
}: {
  id?: string;
  label: string;
  hint?: React.ReactNode;
  control: React.ReactNode;
}) {
  return (
    <div className="flex items-start justify-between gap-4">
      <div className="min-w-0">
        <Label htmlFor={id} className="text-[12.5px]">
          {label}
        </Label>
        {hint ? (
          <p className="mt-0.5 max-w-[56ch] text-[11px] leading-relaxed text-muted-foreground">
            {hint}
          </p>
        ) : null}
      </div>
      <div className="shrink-0">{control}</div>
    </div>
  );
}
