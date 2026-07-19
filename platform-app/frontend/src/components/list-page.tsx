import * as React from "react";

import { ListState } from "@/components/ui/list-state";
import { ListSearchInput } from "@/components/ui/list-search";

/**
 * Shared shell for resource index pages (Projects / Linear / GitHub / Cron).
 * Owns the page header, search field and the loading/error/empty surfaces so
 * each list only has to provide its table. Removes the header + ListState
 * boilerplate that was duplicated across four files.
 */
export function ResourceListPage({
  title,
  description,
  query,
  onQuery,
  searchPlaceholder,
  actions,
  loading,
  error,
  onRetry,
  empty,
  skeleton,
  emptyIcon,
  emptyTitle,
  emptyDescription,
  emptyAction,
  children,
}: {
  title: string;
  description?: string;
  query: string;
  onQuery: (v: string) => void;
  searchPlaceholder: string;
  actions?: React.ReactNode;
  loading?: boolean;
  error?: string | null;
  onRetry?: () => void;
  empty?: boolean;
  skeleton?: React.ReactNode;
  emptyIcon?: React.ReactNode;
  emptyTitle?: string;
  emptyDescription?: string;
  emptyAction?: React.ReactNode;
  children: React.ReactNode;
}) {
  return (
    <div className="space-y-5">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0 space-y-0.5">
          <h1 className="text-[22px] font-semibold leading-tight tracking-[-0.015em]">{title}</h1>
          {description && (
            <p className="text-[13px] text-muted-foreground">{description}</p>
          )}
        </div>
        <div className="flex w-full min-w-0 flex-wrap items-center gap-2 sm:w-auto sm:flex-shrink-0">
          <ListSearchInput
            value={query}
            onChange={onQuery}
            placeholder={searchPlaceholder}
            className="min-w-0 flex-1 sm:flex-none"
          />
          {actions}
        </div>
      </div>
      <ListState
        loading={loading}
        error={error}
        onRetry={onRetry}
        empty={empty}
        skeleton={skeleton}
        emptyIcon={emptyIcon}
        emptyTitle={emptyTitle}
        emptyDescription={emptyDescription}
        emptyAction={emptyAction}
      >
        {children}
      </ListState>
    </div>
  );
}
