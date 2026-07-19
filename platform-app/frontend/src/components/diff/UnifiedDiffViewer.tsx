/* eslint-disable react-refresh/only-export-components */
import { useMemo, type ReactNode } from "react";

import { Badge } from "@/components/ui/badge";
import { cn } from "@/lib/utils";

export type UnifiedDiffLineKind = "file" | "hunk" | "add" | "delete" | "context" | "meta";

export type UnifiedDiffLine = {
  lineNumber: number;
  content: string;
  kind: UnifiedDiffLineKind;
};

export type UnifiedDiffViewerProps = {
  diff: string;
  loading?: boolean;
  error?: Error | string | null;
  isComplete?: boolean;
  truncated?: boolean;
  source?: string;
  emptyMessage?: string;
  /** Optional extra header content (e.g. a repository selector). */
  toolbar?: ReactNode;
  /** Optional wrapper that adds navigation around the diff body. */
  bodyWrapper?: (body: ReactNode) => ReactNode;
};

const fileModeLinePattern = /^(?:new file mode|deleted file mode|old mode|new mode) /;

const lineStyles: Record<UnifiedDiffLineKind, string> = {
  file: "border-l-sky-500 bg-sky-500/10 font-semibold text-sky-800 dark:text-sky-200",
  hunk: "border-l-violet-500 bg-violet-500/10 font-semibold text-violet-800 dark:text-violet-200",
  add: "border-l-emerald-500 bg-emerald-500/10 text-emerald-800 dark:text-emerald-200",
  delete: "border-l-red-500 bg-red-500/10 text-red-800 dark:text-red-200",
  context: "border-l-transparent text-foreground",
  meta: "border-l-muted-foreground/40 bg-muted/40 text-muted-foreground",
};

export function classifyDiffLine(line: string): UnifiedDiffLineKind {
  if (line.startsWith("diff --git ")) return "file";
  if (line.startsWith("@@")) return "hunk";
  if (line.startsWith("+++") || line.startsWith("---")) return "meta";
  if (
    line.startsWith("index ") ||
    fileModeLinePattern.test(line) ||
    line.startsWith("rename from ") ||
    line.startsWith("rename to ") ||
    line.startsWith("similarity index ") ||
    line.startsWith("dissimilarity index ") ||
    line.startsWith("\\ No newline")
  ) {
    return "meta";
  }
  if (line.startsWith("+")) return "add";
  if (line.startsWith("-")) return "delete";
  return "context";
}

export function parseUnifiedDiffLines(diff: string): UnifiedDiffLine[] {
  if (!diff) return [];

  return diff.replace(/\r\n?/g, "\n").split("\n").map((content, index) => ({
    lineNumber: index + 1,
    content,
    kind: classifyDiffLine(content),
  }));
}

function errorMessage(error: Error | string | null | undefined): string {
  if (!error) return "";
  return error instanceof Error ? error.message : error;
}

function StatusBadge({
  loading,
  error,
  isComplete,
}: Pick<UnifiedDiffViewerProps, "loading" | "error" | "isComplete">): ReactNode {
  if (loading) return <Badge variant="secondary">Loading</Badge>;
  if (error) return <Badge variant="destructive">Error</Badge>;
  if (isComplete) return <Badge variant="outline">Final</Badge>;
  return <Badge variant="secondary">Live</Badge>;
}

function StateMessage({
  children,
  role,
}: {
  children: ReactNode;
  role?: "alert" | "status";
}): ReactNode {
  return (
    <div
      className="flex min-h-48 flex-1 items-center justify-center p-6 text-center text-sm text-muted-foreground"
      role={role}
      aria-live={role === "status" ? "polite" : undefined}
    >
      {children}
    </div>
  );
}

export function UnifiedDiffViewer({
  diff,
  loading = false,
  error = null,
  isComplete = false,
  truncated = false,
  source,
  emptyMessage = "No changes to display.",
  toolbar,
  bodyWrapper,
}: UnifiedDiffViewerProps): ReactNode {
  const hasDiff = diff.trim().length > 0;
  const message = errorMessage(error);
  const lines = useMemo(() => (hasDiff ? parseUnifiedDiffLines(diff) : []), [diff, hasDiff]);

  let body: ReactNode;

  if (hasDiff) {
    body = (
      <div className="allow-context-menu min-h-0 flex-1 overflow-auto border-t bg-background font-mono text-xs leading-5 whitespace-pre select-text">
        <div className="min-w-max py-2">
          {lines.map((line) => (
            <div
              key={line.lineNumber}
              data-kind={line.kind}
              className={cn("flex min-h-5 border-l-2 px-3", lineStyles[line.kind])}
            >
              <span className="w-10 shrink-0 select-none pr-2 text-right font-mono text-[10.5px] leading-5 text-muted-foreground/50 tabular-nums">
                {line.lineNumber}
              </span>
              <span className="min-w-0 flex-1 whitespace-pre-wrap break-words">{line.content}</span>
            </div>
          ))}
        </div>
      </div>
    );
  } else if (loading) {
    body = <StateMessage role="status">Loading diff…</StateMessage>;
  } else if (message) {
    body = <StateMessage role="alert">Error loading diff: {message}</StateMessage>;
  } else if (source === "unavailable") {
    body = <StateMessage role="status">Diff source unavailable.</StateMessage>;
  } else {
    body = <StateMessage role="status">{emptyMessage}</StateMessage>;
  }

  return (
    <section className="flex h-full min-h-0 min-w-0 flex-col overflow-hidden rounded-lg border bg-background">
      <div className="flex shrink-0 items-center justify-between gap-3 px-3 py-2">
        <div className="flex min-w-0 items-center gap-2">
          <div className="min-w-0">
            <h3 className="truncate text-sm font-medium text-foreground">Diff</h3>
            {source && source !== "unavailable" && (
              <p className="truncate text-xs text-muted-foreground">Source: {source}</p>
            )}
          </div>
          {toolbar}
        </div>
        <div className="flex shrink-0 items-center gap-1.5">
          {truncated && <Badge variant="outline">Truncated</Badge>}
          <StatusBadge loading={loading} error={error} isComplete={isComplete} />
        </div>
      </div>

      {hasDiff && message && (
        <div className="border-t border-amber-500/30 bg-amber-500/10 px-3 py-2 text-xs text-amber-800 dark:text-amber-200">
          Warning: {message}
        </div>
      )}
      {hasDiff && truncated && (
        <div className="border-t border-amber-500/30 bg-amber-500/10 px-3 py-2 text-xs text-amber-800 dark:text-amber-200">
          Diff truncated. Showing the available portion.
        </div>
      )}

      {bodyWrapper ? bodyWrapper(body) : body}
    </section>
  );
}
