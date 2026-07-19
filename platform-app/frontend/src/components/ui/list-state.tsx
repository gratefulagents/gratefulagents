import * as React from "react";

import { Skeleton } from "@/components/ui/skeleton";
import { cn } from "@/lib/utils";

/**
 * ListState renders one of: loading skeletons, error card, empty state, or
 * the children. Centralises the three async surfaces every list page repeats.
 *
 * Usage:
 *   <ListState loading={loading} error={error} empty={!items.length}
 *              skeleton={<ListRowSkeleton rows={6} />}
 *              emptyTitle="No projects yet"
 *              emptyDescription="Connect a source to get started."
 *              emptyAction={<Button>Connect</Button>}>
 *     {items.map(...)}
 *   </ListState>
 */
export function ListState({
  loading,
  error,
  empty,
  onRetry,
  skeleton,
  emptyIcon,
  emptyTitle,
  emptyDescription,
  emptyAction,
  children,
}: {
  loading?: boolean;
  error?: string | null;
  empty?: boolean;
  onRetry?: () => void;
  skeleton?: React.ReactNode;
  emptyIcon?: React.ReactNode;
  emptyTitle?: string;
  emptyDescription?: string;
  emptyAction?: React.ReactNode;
  children: React.ReactNode;
}) {
  if (loading) {
    return <div role="status" aria-live="polite">{skeleton ?? <ListRowSkeleton />}</div>;
  }
  if (error && empty) {
    return (
      <div
        role="alert"
        className="surface-card flex flex-col items-start gap-3 rounded-xl border border-destructive/40 bg-destructive/5 px-4 py-4 text-[12.5px] text-foreground"
      >
        <p className="font-medium">Couldn't load</p>
        <p className="text-muted-foreground">{error}</p>
        {onRetry && (
          <button
            type="button"
            onClick={onRetry}
            className="rounded-[5px] border border-border/70 bg-background px-2.5 py-1 text-[11.5px] hover:bg-muted"
          >
            Try again
          </button>
        )}
      </div>
    );
  }
  if (empty) {
    return (
      <div className="surface-card flex flex-col items-center justify-center gap-2.5 rounded-xl border border-border/60 bg-muted/20 px-6 py-16 text-center">
        {emptyIcon && (
          <div className="mb-1 flex size-11 items-center justify-center rounded-full bg-muted/60 text-muted-foreground/70 ring-1 ring-inset ring-border/60 [&_svg]:size-5">
            {emptyIcon}
          </div>
        )}
        {emptyTitle && (
          <p className="text-[14px] font-medium text-foreground">{emptyTitle}</p>
        )}
        {emptyDescription && (
          <p className="max-w-[48ch] text-[12.5px] leading-relaxed text-muted-foreground">
            {emptyDescription}
          </p>
        )}
        {emptyAction && <div className="pt-2">{emptyAction}</div>}
      </div>
    );
  }
  // Content path. The banner slot ALWAYS exists (null when healthy) so a
  // transient stream/refresh failure only toggles the banner in place —
  // `children` keep the same position and element type in the tree, and React
  // never unmounts them. Swapping to a differently-shaped tree here used to
  // remount the whole page (destroying open dialogs, form edits, and scroll
  // position) every time a watch stream dropped and again when it recovered.
  return (
    <>
      {error ? (
        <div
          role="alert"
          className="mb-3 flex flex-wrap items-center gap-x-2 gap-y-1 rounded-lg border border-amber-500/40 bg-amber-500/10 px-3 py-2 text-[12px]"
        >
          <span className="font-medium text-foreground">Connection trouble — showing the last loaded data.</span>
          <span className="min-w-0 flex-1 basis-48 truncate text-muted-foreground" title={error}>
            {error}
          </span>
          {onRetry && (
            <button
              type="button"
              onClick={onRetry}
              className="rounded-[5px] border border-border/70 bg-background px-2 py-0.5 text-[11.5px] hover:bg-muted"
            >
              Retry now
            </button>
          )}
        </div>
      ) : null}
      {children}
    </>
  );
}

/** Generic row skeletons — three lines of varying width per row. */
export function ListRowSkeleton({
  rows = 5,
  className,
}: {
  rows?: number;
  className?: string;
}) {
  return (
    <div className={cn("space-y-2", className)} aria-hidden>
      {Array.from({ length: rows }).map((_, i) => (
        <div
          key={i}
          className="flex items-center gap-3 rounded-[6px] border border-border/40 bg-muted/10 px-3 py-2.5"
        >
          <Skeleton className="size-5 shrink-0 rounded-full" />
          <Skeleton className="h-3 w-[28%]" />
          <Skeleton className="h-3 w-[18%]" />
          <Skeleton className="ml-auto h-3 w-[8%]" />
        </div>
      ))}
    </div>
  );
}

/** Row skeletons sized for dense tables (AgentRunTable etc). */
export function TableRowSkeleton({ rows = 6, cols = 6 }: { rows?: number; cols?: number }) {
  return (
    <div aria-hidden className="overflow-hidden rounded-xl border border-border/60 bg-card/30">
      {Array.from({ length: rows }).map((_, row) => (
        <div
          key={row}
          className="flex items-center border-b border-border/40 last:border-b-0"
        >
          {Array.from({ length: cols }).map((__, col) => (
            <div key={col} className="flex flex-1 px-3 py-2.5">
              <Skeleton
                className={cn(
                  "h-3",
                  col === 0 ? "w-[70%]" : col === cols - 1 ? "ml-auto w-[38%]" : "w-[55%]",
                )}
              />
            </div>
          ))}
        </div>
      ))}
    </div>
  );
}
