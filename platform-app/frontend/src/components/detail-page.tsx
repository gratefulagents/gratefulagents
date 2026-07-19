import * as React from "react";
import { Link } from "react-router-dom";
import { ChevronLeft } from "lucide-react";

import { cn } from "@/lib/utils";
import { toneText } from "@/lib/status";

/**
 * Shared chrome for resource detail pages (Project / Linear / GitHub / Cron).
 * Deliberately flat: one breadcrumb + title header, a divider-separated stat
 * strip and an aligned fact list instead of grids of bordered cards, so the
 * only framed surface on a detail page is the data table itself.
 */

export function DetailHeader({
  parentLabel,
  parentTo,
  title,
  meta,
  actions,
}: {
  parentLabel: string;
  parentTo: string;
  title: string;
  meta?: React.ReactNode;
  actions?: React.ReactNode;
}) {
  return (
    <div className="space-y-1">
      <div className="flex items-center text-[12px] text-muted-foreground">
        <Link
          to={parentTo}
          className="group inline-flex items-center gap-0.5 rounded-sm transition-colors hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/60"
        >
          <ChevronLeft className="size-3 shrink-0 text-muted-foreground/50 transition-colors group-hover:text-foreground" />
          {parentLabel}
        </Link>
      </div>
      <div className="flex flex-wrap items-center justify-between gap-x-4 gap-y-2">
        <div className="flex min-w-0 items-center gap-2.5">
          <h1 className="min-w-0 truncate text-[22px] font-semibold leading-tight tracking-[-0.015em]">
            {title}
          </h1>
          {meta && <div className="flex shrink-0 items-center gap-2">{meta}</div>}
        </div>
        {actions && <div className="flex shrink-0 items-center gap-2">{actions}</div>}
      </div>
    </div>
  );
}

/**
 * Flat stat strip — values separated by hairline dividers instead of one
 * bordered card per number.
 */
export function StatBar({ children }: { children: React.ReactNode }) {
  return (
    <dl className="flex flex-wrap items-stretch gap-y-4 py-1">{children}</dl>
  );
}

export function Stat({
  label,
  value,
  sub,
  mono = true,
}: {
  label: string;
  value: React.ReactNode;
  sub?: React.ReactNode;
  mono?: boolean;
}) {
  return (
    <div className="flex min-w-[90px] flex-col gap-1.5 border-r border-border/50 px-4 first:pl-0 last:border-r-0 last:pr-0 sm:min-w-[110px] sm:px-7">
      <dt className="text-[10.5px] font-medium uppercase tracking-[0.07em] text-muted-foreground/70">
        {label}
      </dt>
      <dd
        className={cn(
          "text-[20px] font-semibold leading-none tracking-tight tabular-nums text-foreground",
          mono && "font-mono",
        )}
      >
        {value}
      </dd>
      <dd className="min-h-[15px] text-[11.5px] leading-tight text-muted-foreground">
        {sub ?? ""}
      </dd>
    </div>
  );
}

/**
 * Aligned label/value list for configuration facts. One fixed label column
 * keeps every value on the same axis; string values truncate with a tooltip
 * instead of overflowing.
 */
export function FactList({
  children,
  className,
}: {
  children: React.ReactNode;
  className?: string;
}) {
  return (
    <dl
      className={cn(
        "grid grid-cols-1 items-baseline gap-x-6 gap-y-2 sm:grid-cols-[minmax(110px,160px)_minmax(0,1fr)]",
        className,
      )}
    >
      {children}
    </dl>
  );
}

export function Fact({
  label,
  value,
  mono,
  wrap,
}: {
  label: string;
  value: React.ReactNode;
  /** Render the value in the mono stack (ids, branches, schedules). */
  mono?: boolean;
  /** Allow multi-line values (instructions, prompts) instead of truncating. */
  wrap?: boolean;
}) {
  const empty = value == null || value === "" || value === "-";
  return (
    <>
      <dt className="text-[12.5px] leading-6 text-muted-foreground">{label}</dt>
      <dd
        className={cn(
          "min-w-0 text-[13px] leading-6",
          mono && "font-mono text-[12.5px]",
          empty && "text-muted-foreground/50",
        )}
      >
        {empty ? (
          "—"
        ) : typeof value === "string" && !wrap ? (
          <span className="block truncate" title={value}>
            {value}
          </span>
        ) : wrap ? (
          <span className="block whitespace-pre-wrap break-words">{value}</span>
        ) : (
          value
        )}
      </dd>
    </>
  );
}

/** External link rendered as a truncating fact value. */
export function FactLink({ href, children }: { href: string; children?: React.ReactNode }) {
  return (
    <a
      href={href}
      target="_blank"
      rel="noopener noreferrer"
      title={href}
      className="block max-w-full truncate text-foreground underline-offset-2 hover:text-primary hover:underline"
    >
      {children ?? href}
    </a>
  );
}

/** Success / failed / running counts with semantic tone colors. */
export function RunCountSummary({
  success,
  failed,
  running,
}: {
  success: number;
  failed: number;
  running?: number;
}) {
  return (
    <span className="inline-flex items-center gap-1.5">
      <span className={cn("tabular-nums", toneText.success)}>
        {success} <span aria-hidden>✓</span><span className="sr-only"> succeeded</span>
      </span>
      <span className="text-muted-foreground/40">·</span>
      <span className={cn("tabular-nums", toneText.danger)}>
        {failed} <span aria-hidden>✗</span><span className="sr-only"> failed</span>
      </span>
      {running != null && running > 0 && (
        <>
          <span className="text-muted-foreground/40">·</span>
          <span className={cn("tabular-nums", toneText.running)}>
            {running} <span aria-hidden>⟳</span><span className="sr-only"> running</span>
          </span>
        </>
      )}
    </span>
  );
}

/** Boxless titled section: small heading, optional description, content. */
export function DetailSection({
  title,
  description,
  aside,
  children,
  className,
}: {
  title: string;
  description?: React.ReactNode;
  aside?: React.ReactNode;
  children: React.ReactNode;
  className?: string;
}) {
  return (
    <section className={cn("space-y-3", className)}>
      <div className="flex items-baseline justify-between gap-3">
        <div className="min-w-0">
          <h2 className="text-[13px] font-medium text-muted-foreground">{title}</h2>
          {description && (
            <p className="mt-1 max-w-[72ch] text-[11.5px] leading-relaxed text-muted-foreground/80">
              {description}
            </p>
          )}
        </div>
        {aside && <div className="shrink-0 text-xs text-muted-foreground">{aside}</div>}
      </div>
      {children}
    </section>
  );
}

export function RunsSection({
  count,
  loading,
  children,
}: {
  count: number;
  loading?: boolean;
  children: React.ReactNode;
}) {
  return (
    <DetailSection
      title="Runs"
      aside={
        !loading && count > 0 ? (
          <span className="tabular-nums">{count} total</span>
        ) : undefined
      }
    >
      {children}
    </DetailSection>
  );
}
