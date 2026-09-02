import { useMemo, useState } from "react";
import { AlertTriangle, Check, Copy, Loader2 } from "lucide-react";

import { toast } from "@/components/ui/toaster";
import { cn } from "@/lib/utils";
import type { AgentRunError } from "@/rpc/platform/service_pb";

interface RunSessionErrorsPaneProps {
  errors: AgentRunError[];
  loading: boolean;
  error: string | null;
  truncated: boolean;
}

type ErrorGroup = {
  key: string;
  first: AgentRunError;
  latest: AgentRunError;
  count: number;
};

/** Collapse identical source+message repeats (retries, poll loops) into one row. */
function groupRepeatedErrors(errors: AgentRunError[]): ErrorGroup[] {
  const groups: ErrorGroup[] = [];
  const byKey = new Map<string, ErrorGroup>();
  for (const entry of errors) {
    const key = `${entry.source}\u0000${entry.message}`;
    const existing = byKey.get(key);
    if (existing) {
      existing.count += 1;
      if (entry.timestampUnix > existing.latest.timestampUnix) existing.latest = entry;
      continue;
    }
    const group = { key, first: entry, latest: entry, count: 1 };
    byKey.set(key, group);
    groups.push(group);
  }
  return groups;
}

function CopyButton({ text }: { text: string }) {
  const [copied, setCopied] = useState(false);
  return (
    <button
      type="button"
      aria-label="Copy error"
      onClick={async () => {
        try {
          await navigator.clipboard.writeText(text);
          setCopied(true);
          setTimeout(() => setCopied(false), 1_200);
        } catch {
          toast.error("Could not copy error");
        }
      }}
      className="inline-flex size-6 shrink-0 items-center justify-center rounded text-muted-foreground opacity-0 transition-opacity group-hover:opacity-100 focus-visible:opacity-100 hover:bg-muted hover:text-foreground"
    >
      {copied ? <Check className="size-3" /> : <Copy className="size-3" />}
    </button>
  );
}

export function RunSessionErrorsPane({ errors, loading, error, truncated }: RunSessionErrorsPaneProps) {
  const groups = useMemo(() => groupRepeatedErrors(errors), [errors]);
  return (
    <section className="@container min-h-0 flex-1 overflow-y-auto px-3 py-4 @lg:px-6" aria-labelledby="run-errors-title">
      <div className="mx-auto max-w-5xl">
        <header className="mb-4 flex items-start justify-between gap-4 border-b border-border/70 pb-3">
          <div className="min-w-0">
            <div className="flex items-center gap-2">
              <AlertTriangle className="size-4 shrink-0 text-[color:var(--tone-danger-fg)]" aria-hidden="true" />
              <h2 id="run-errors-title" className="text-sm font-semibold text-foreground">
                Run errors
              </h2>
              {errors.length > 0 && (
                <span className="font-mono text-2xs text-muted-foreground tabular-nums">
                  {errors.length}
                  {groups.length !== errors.length ? ` · ${groups.length} distinct` : ""}
                </span>
              )}
            </div>
            <p className="mt-1 text-xs text-muted-foreground">
              Errors stay visible after retries. Routine pod output and trace data are excluded.
            </p>
          </div>
          {loading && <Loader2 className="mt-0.5 size-4 shrink-0 animate-spin text-muted-foreground" aria-label="Loading errors" />}
        </header>

        {error && (
          <p
            className="mb-4 rounded-md border border-[color-mix(in_oklch,var(--tone-danger)_35%,var(--color-border))] bg-[color-mix(in_oklch,var(--tone-danger)_8%,transparent)] px-3 py-2 text-xs text-[color:var(--tone-danger-fg)]"
            role="alert"
          >
            {error}
          </p>
        )}

        {!loading && errors.length === 0 && !error && (
          <div className="rounded-lg border border-dashed border-border px-5 py-10 text-center">
            <p className="text-sm font-medium text-foreground">No errors recorded</p>
            <p className="mt-1 text-xs text-muted-foreground">Recovered and terminal errors will appear here.</p>
          </div>
        )}

        {groups.length > 0 && (
          <ol className="space-y-2">
            {groups.map(({ key, first, latest, count }) => (
              <li
                key={key}
                className="group grid gap-x-3 gap-y-1.5 rounded-md border border-border/80 bg-card px-3 py-2.5 @md:grid-cols-[8rem_minmax(0,1fr)_auto]"
              >
                <div className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-0.5 text-2xs text-muted-foreground @md:block">
                  <time dateTime={isoTimestamp(latest.timestampUnix)} className="whitespace-nowrap">
                    {formatTimestamp(latest.timestampUnix)}
                  </time>
                  <span
                    className={cn(
                      "@md:mt-1 @md:block",
                      first.source === "status" && "text-[color:var(--tone-danger-fg)]",
                    )}
                  >
                    {sourceLabel(first.source)}
                    {first.kind ? ` · ${first.kind}` : ""}
                  </span>
                  {count > 1 && (
                    <span
                      className="inline-flex rounded-full bg-muted px-1.5 font-mono text-3xs text-foreground tabular-nums @md:mt-1"
                      title={`Repeated ${count} times · first ${formatTimestamp(first.timestampUnix)}`}
                    >
                      ×{count}
                    </span>
                  )}
                </div>
                <pre className="min-w-0 font-mono text-xs leading-5 break-words whitespace-pre-wrap text-foreground">
                  {first.message}
                </pre>
                <div className="flex items-start justify-end">
                  <CopyButton text={first.message} />
                </div>
              </li>
            ))}
          </ol>
        )}

        {truncated && <p className="mt-3 text-xs text-muted-foreground">Showing the 200 most recent errors.</p>}
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
