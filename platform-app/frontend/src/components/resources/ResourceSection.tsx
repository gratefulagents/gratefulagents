import * as React from "react";
import { Pencil, Trash2, Eye } from "lucide-react";

import { cn } from "@/lib/utils";
import { toneSoft, type StatusTone } from "@/lib/status";
import { Button } from "@/components/ui/button";
import { ListRowSkeleton, ListState } from "@/components/ui/list-state";
import { ListSearchInput } from "@/components/ui/list-search";

/**
 * Shared shell for one Resources tab: intro line, search + actions toolbar,
 * and the framed list with loading / error / empty surfaces. Sections only
 * provide rows (see ResourceRow) and the dialogs they open.
 */
export function ResourceSection({
  description,
  hint,
  count,
  query,
  onQuery,
  searchPlaceholder,
  actions,
  loading,
  error,
  onRetry,
  empty,
  emptyIcon,
  emptyTitle,
  emptyDescription,
  emptyAction,
  noMatches,
  children,
}: {
  description: React.ReactNode;
  /** Quiet second line, e.g. a permissions note. */
  hint?: React.ReactNode;
  count: number;
  query: string;
  onQuery: (value: string) => void;
  searchPlaceholder: string;
  actions?: React.ReactNode;
  loading: boolean;
  error: string | null;
  onRetry: () => void;
  /** True when the server list is empty (not merely filtered to nothing). */
  empty: boolean;
  emptyIcon: React.ReactNode;
  emptyTitle: string;
  emptyDescription: string;
  emptyAction?: React.ReactNode;
  /** True when a search query filtered every row out. */
  noMatches?: boolean;
  children: React.ReactNode;
}) {
  const showSearch = !empty || query.length > 0;
  return (
    <section className="space-y-4">
      <div className="flex flex-col gap-3 lg:flex-row lg:items-start lg:justify-between">
        <div className="min-w-0 max-w-[72ch]">
          <p className="text-[12.5px] leading-relaxed text-muted-foreground">{description}</p>
          {hint && <p className="mt-1 text-[11.5px] leading-relaxed text-muted-foreground/80">{hint}</p>}
        </div>
        <div className="flex w-full min-w-0 flex-wrap items-center gap-2 lg:w-auto lg:shrink-0 lg:justify-end">
          {showSearch && (
            <ListSearchInput
              value={query}
              onChange={onQuery}
              placeholder={searchPlaceholder}
              className="min-w-0 flex-1 sm:w-[240px] sm:flex-none"
            />
          )}
          {actions}
        </div>
      </div>

      <ListState
        loading={loading}
        error={error}
        onRetry={onRetry}
        empty={empty}
        skeleton={<ListRowSkeleton rows={4} />}
        emptyIcon={emptyIcon}
        emptyTitle={emptyTitle}
        emptyDescription={emptyDescription}
        emptyAction={emptyAction}
      >
        {noMatches ? (
          <div className="surface-card rounded-xl border border-border/60 px-6 py-10 text-center">
            <p className="text-[13px] font-medium">No matches</p>
            <p className="mt-1 text-[12px] text-muted-foreground">
              Nothing matches “{query}”.{" "}
              <button type="button" className="underline underline-offset-2 hover:text-foreground" onClick={() => onQuery("")}>
                Clear the search
              </button>
            </p>
          </div>
        ) : (
          <div className="space-y-2">
            <ul role="list" className="surface-card divide-y divide-border/60 overflow-hidden rounded-xl border border-border/60">
              {children}
            </ul>
            {!loading && count > 0 && (
              <p className="px-1 text-[11.5px] tabular-nums text-muted-foreground/80" aria-live="polite">
                {query ? `${React.Children.count(children)} of ${count}` : count} {count === 1 && !query ? "item" : "items"}
              </p>
            )}
          </div>
        )}
      </ListState>
    </section>
  );
}

export type ResourceRowAction = {
  label: string;
  onClick: () => void;
  destructive?: boolean;
  disabled?: boolean;
};

/**
 * One list entry: leading icon chip, title + badges, description, a mono
 * facts line, and trailing edit/delete controls. The title is a button that
 * opens the editor (or viewer) so the whole row reads as one target.
 */
export function ResourceRow({
  name,
  icon,
  title,
  badges,
  description,
  facts,
  status,
  onOpen,
  openLabel = "Edit",
  onEdit,
  onDelete,
  busy,
}: {
  name: string;
  icon: React.ReactNode;
  title?: React.ReactNode;
  badges?: React.ReactNode;
  description?: React.ReactNode;
  /** Small mono details under the description (command, URL, limits). */
  facts?: React.ReactNode;
  /** Warning or error line rendered in the danger tone. */
  status?: React.ReactNode;
  onOpen?: () => void;
  openLabel?: "Edit" | "View";
  onEdit?: () => void;
  onDelete?: () => void;
  busy?: boolean;
}) {
  const OpenIcon = openLabel === "View" ? Eye : Pencil;
  return (
    <li className="group/row relative flex items-start gap-3 px-4 py-3 transition-colors hover:bg-muted/30">
      <div className="mt-0.5 grid size-8 shrink-0 place-items-center rounded-lg bg-muted/60 text-muted-foreground ring-1 ring-inset ring-border/60 [&_svg]:size-4">
        {icon}
      </div>
      <div className="min-w-0 flex-1 space-y-0.5">
        <div className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1">
          {onOpen ? (
            <button
              type="button"
              onClick={onOpen}
              className="min-w-0 truncate text-left text-[13.5px] font-medium text-foreground underline-offset-4 hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/60 rounded-sm"
            >
              {title ?? name}
            </button>
          ) : (
            <span className="min-w-0 truncate text-[13.5px] font-medium">{title ?? name}</span>
          )}
          {badges}
        </div>
        {description && (
          <p className="line-clamp-2 max-w-[90ch] text-[12.5px] leading-relaxed text-muted-foreground">{description}</p>
        )}
        {facts && (
          <div className="flex min-w-0 flex-wrap items-center gap-x-3 gap-y-0.5 pt-0.5 font-mono text-[11px] text-muted-foreground/90">
            {facts}
          </div>
        )}
        {status && <p className="pt-0.5 text-[11.5px] text-destructive">{status}</p>}
      </div>
      {(onEdit || onDelete) && (
        <div className="flex shrink-0 items-center gap-0.5 self-center">
          {onEdit && (
            <Button
              variant="ghost"
              size="icon-sm"
              aria-label={`${openLabel} ${name}`}
              title={openLabel}
              disabled={busy}
              onClick={onEdit}
              className="text-muted-foreground hover:text-foreground"
            >
              <OpenIcon />
            </Button>
          )}
          {onDelete && (
            <Button
              variant="ghost"
              size="icon-sm"
              aria-label={`Delete ${name}`}
              title="Delete"
              disabled={busy}
              onClick={onDelete}
              className="text-muted-foreground hover:bg-destructive/10 hover:text-destructive"
            >
              <Trash2 />
            </Button>
          )}
        </div>
      )}
    </li>
  );
}

/** Compact mono fact inside ResourceRow.facts. */
export function Fact({ children, className, title }: { children: React.ReactNode; className?: string; title?: string }) {
  return (
    <span className={cn("inline-flex min-w-0 max-w-full items-center gap-1 truncate", className)} title={title}>
      {children}
    </span>
  );
}

/** Small status pill for ResourceRow.badges; tone comes from the shared status palette. */
export function Pill({ tone = "neutral", children, className }: { tone?: StatusTone; children: React.ReactNode; className?: string }) {
  return (
    <span className={cn("inline-flex h-[18px] items-center rounded-full px-1.5 text-[10.5px] font-medium", toneSoft[tone], className)}>
      {children}
    </span>
  );
}
