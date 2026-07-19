import { AlertTriangle, Loader2 } from "lucide-react";

import { cn } from "@/lib/utils";
import type { AgentRunError } from "@/rpc/platform/service_pb";

interface RunSessionErrorsPaneProps {
  errors: AgentRunError[];
  loading: boolean;
  error: string | null;
  truncated: boolean;
}

export function RunSessionErrorsPane({ errors, loading, error, truncated }: RunSessionErrorsPaneProps) {
  return (
    <section className="min-h-0 flex-1 overflow-y-auto px-4 py-5 md:px-7" aria-labelledby="run-errors-title">
      <div className="mx-auto max-w-5xl">
        <header className="mb-5 flex items-start justify-between gap-4 border-b border-border/70 pb-4">
          <div>
            <div className="flex items-center gap-2">
              <AlertTriangle className="size-4 text-[color:var(--tone-danger-fg)]" aria-hidden="true" />
              <h2 id="run-errors-title" className="text-sm font-semibold text-foreground">Run errors</h2>
            </div>
            <p className="mt-1 text-xs text-muted-foreground">
              Errors stay visible after retries. Routine pod output and trace data are excluded.
            </p>
          </div>
          {loading && <Loader2 className="mt-0.5 size-4 animate-spin text-muted-foreground" aria-label="Loading errors" />}
        </header>

        {error && (
          <p className="mb-4 rounded-md border border-[color-mix(in_oklch,var(--tone-danger)_35%,var(--color-border))] bg-[color-mix(in_oklch,var(--tone-danger)_8%,transparent)] px-3 py-2 text-xs text-[color:var(--tone-danger-fg)]" role="alert">
            {error}
          </p>
        )}

        {!loading && errors.length === 0 && !error && (
          <div className="rounded-lg border border-dashed border-border px-5 py-10 text-center">
            <p className="text-sm font-medium text-foreground">No errors recorded</p>
            <p className="mt-1 text-xs text-muted-foreground">Recovered and terminal errors will appear here.</p>
          </div>
        )}

        {errors.length > 0 && (
          <ol className="space-y-2">
            {errors.map((entry, index) => (
              <li
                key={`${entry.source}:${entry.message}:${index}`}
                className="grid gap-2 rounded-md border border-border/80 bg-card px-3 py-3 sm:grid-cols-[8rem_minmax(0,1fr)]"
              >
                <div className="flex items-center gap-2 text-[11px] text-muted-foreground sm:block">
                  <time dateTime={isoTimestamp(entry.timestampUnix)}>{formatTimestamp(entry.timestampUnix)}</time>
                  <span className={cn("sm:mt-1 sm:block", entry.source === "status" && "text-[color:var(--tone-danger-fg)]") }>
                    {sourceLabel(entry.source)}{entry.kind ? ` · ${entry.kind}` : ""}
                  </span>
                </div>
                <pre className="min-w-0 whitespace-pre-wrap break-words font-mono text-xs leading-5 text-foreground">{entry.message}</pre>
              </li>
            ))}
          </ol>
        )}

        {truncated && (
          <p className="mt-3 text-xs text-muted-foreground">Showing the 200 most recent errors.</p>
        )}
      </div>
    </section>
  );
}

function sourceLabel(source: string): string {
  if (source === "pod") return "worker pod";
  if (source === "status") return "run status";
  return "activity";
}

function isoTimestamp(timestamp: bigint): string | undefined {
  if (timestamp <= 0n) return undefined;
  return new Date(Number(timestamp) * 1000).toISOString();
}

function formatTimestamp(timestamp: bigint): string {
  if (timestamp <= 0n) return "Time unavailable";
  return new Intl.DateTimeFormat(undefined, {
    month: "short",
    day: "numeric",
    hour: "numeric",
    minute: "2-digit",
    second: "2-digit",
  }).format(new Date(Number(timestamp) * 1000));
}
