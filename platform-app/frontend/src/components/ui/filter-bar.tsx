import * as React from "react";
import { Filter, X } from "lucide-react";

import { Button } from "@/components/ui/button";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
} from "@/components/ui/select";
import { cn } from "@/lib/utils";

/**
 * Shared filter toolbar for list surfaces.
 *
 * One visual language for every filtered page: a labelled control strip, an
 * active-filter count, a live result count for screen readers, and a single
 * "Clear" affordance. Pages supply `FilterSelect` / `FilterToggle` children and
 * keep their state in the URL (see `useUrlFilters`), so filtered views are
 * shareable and survive reloads.
 */
export function FilterBar({
  label = "Filters",
  activeCount = 0,
  onClear,
  resultLabel,
  children,
  className,
}: {
  /** Accessible name of the control group, e.g. "Scan run filters". */
  label?: string;
  activeCount?: number;
  onClear?: () => void;
  /** Live-region summary, e.g. "12 of 40 scan runs". */
  resultLabel?: React.ReactNode;
  children: React.ReactNode;
  className?: string;
}) {
  return (
    <div
      role="group"
      aria-label={label}
      className={cn(
        "flex flex-wrap items-center gap-1.5 rounded-xl border border-border/70 bg-muted/25 px-2 py-1.5",
        className,
      )}
    >
      {/* The funnel is decoration, not a control: an uppercase "FILTERS" word
          read as a button and stole attention from the real controls. The count
          badge only appears once filters are on, so "this list is narrowed" is
          visible at a glance (and announced) without shouting when it isn't. */}
      <span className="ml-0.5 mr-0.5 inline-flex shrink-0 items-center gap-1">
        <Filter
          className={cn("size-3.5", activeCount > 0 ? "text-primary" : "text-muted-foreground/70")}
          aria-hidden
        />
        {activeCount > 0 && (
          <span
            className="rounded-full bg-primary/15 px-1.5 py-px text-[10px] font-semibold tabular-nums text-primary"
            aria-label={`${activeCount} active ${activeCount === 1 ? "filter" : "filters"}`}
          >
            {activeCount}
          </span>
        )}
      </span>
      {children}
      {/* Count and Clear sit immediately after the controls rather than pinned
          to the far edge, where a wide viewport left them stranded across an
          empty gap and disconnected from what they describe. */}
      {resultLabel !== undefined && (
        <span
          className="pl-1 text-[11.5px] tabular-nums text-muted-foreground"
          aria-live="polite"
        >
          {resultLabel}
        </span>
      )}
      {activeCount > 0 && onClear && (
        <Button
          variant="ghost"
          size="sm"
          className="h-7 gap-1 px-2 text-[11.5px] text-muted-foreground hover:text-foreground"
          onClick={onClear}
        >
          <X className="size-3" aria-hidden />
          Clear
        </Button>
      )}
    </div>
  );
}

export type FilterOption = {
  value: string;
  label: string;
  /** Optional trailing count rendered inside the menu item. */
  count?: number;
};

/**
 * Single-choice filter dropdown. Renders the shared Select so filters look and
 * behave the same everywhere (keyboard, dark mode, overflow) instead of the
 * raw `<select>` each page used to hand-roll. Active (non-default) filters get
 * a primary-tinted trigger so a narrowed list is obvious at a glance.
 */
export function FilterSelect({
  label,
  value,
  defaultValue = "all",
  onChange,
  options,
  className,
}: {
  label: string;
  value: string;
  /** Value considered "no filter"; used for the active highlight. */
  defaultValue?: string;
  onChange: (value: string) => void;
  options: FilterOption[];
  className?: string;
}) {
  const active = value !== defaultValue;
  const selected = options.find((option) => option.value === value);
  return (
    <Select
      value={value}
      onValueChange={(next) => {
        if (typeof next === "string") onChange(next);
      }}
    >
      <SelectTrigger
        size="sm"
        aria-label={label}
        className={cn(
          "h-7 max-w-[240px] gap-1 border-border/80 bg-background/70 text-[12px] dark:bg-input/40",
          active && "border-primary/50 bg-primary/10 text-foreground",
          className,
        )}
      >
        {/* Label and value are separated by weight, not just a colon: at 12px
            on a dark surface "Status: Any status" otherwise reads as one blur. */}
        <span className="truncate">
          <span className="text-muted-foreground/80">{label}</span>
          <span className="px-1 text-muted-foreground/40">·</span>
          <span className={cn(active ? "font-medium text-foreground" : "text-foreground/90")}>
            {selected?.label ?? value}
          </span>
        </span>
      </SelectTrigger>
      <SelectContent>
        {options.map((option) => (
          <SelectItem key={option.value} value={option.value}>
            <span className="flex-1 truncate">{option.label}</span>
            {option.count !== undefined && (
              <span className="ml-2 shrink-0 text-[11px] tabular-nums text-muted-foreground">
                {option.count}
              </span>
            )}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  );
}

/**
 * Inline chip row for a small, high-traffic filter (severity, status) where a
 * dropdown would cost an extra click. Multi-select: clicking a chip toggles it.
 */
export function FilterChips({
  label,
  options,
  selected,
  onToggle,
  className,
}: {
  label: string;
  options: FilterOption[];
  selected: string[];
  onToggle: (value: string) => void;
  className?: string;
}) {
  return (
    <div role="group" aria-label={label} className={cn("flex flex-wrap items-center gap-1", className)}>
      {options.map((option) => {
        const on = selected.includes(option.value);
        return (
          <button
            key={option.value}
            type="button"
            aria-pressed={on}
            onClick={() => onToggle(option.value)}
            className={cn(
              "inline-flex h-7 items-center gap-1 rounded-lg border px-2 text-[12px] transition-colors",
              "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/60",
              on
                ? "border-primary/45 bg-primary/10 text-foreground"
                : "border-input bg-background text-muted-foreground hover:text-foreground dark:bg-input/30",
            )}
          >
            {option.label}
            {option.count !== undefined && (
              <span className="tabular-nums opacity-70">{option.count}</span>
            )}
          </button>
        );
      })}
    </div>
  );
}
