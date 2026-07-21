import { useState } from "react";
import { Check, Clipboard, Loader2, RefreshCw, SquareTerminal, WrapText } from "lucide-react";

import { Button } from "@/components/ui/button";
import { toast } from "@/components/ui/toaster";
import { cn } from "@/lib/utils";

interface RunSessionLogsPaneProps {
  content: string;
  podName: string;
  available: boolean;
  loading: boolean;
  error: string | null;
  truncated: boolean;
  lastUpdated: Date | null;
  onRefresh: () => void | Promise<void>;
}

export function RunSessionLogsPane({
  content,
  podName,
  available,
  loading,
  error,
  truncated,
  lastUpdated,
  onRefresh,
}: RunSessionLogsPaneProps) {
  const [wrap, setWrap] = useState(false);
  const [copied, setCopied] = useState(false);

  async function copyLogs() {
    if (!content || !navigator.clipboard) return;
    try {
      await navigator.clipboard.writeText(content);
      setCopied(true);
      setTimeout(() => setCopied(false), 1_500);
    } catch {
      toast.error("Could not copy logs");
    }
  }

  return (
    <section className="flex min-h-0 flex-1 flex-col" aria-labelledby="run-logs-title">
      <header className="flex shrink-0 flex-wrap items-center justify-between gap-3 border-b border-border/70 px-4 py-3 md:px-5">
        <div className="min-w-0">
          <div className="flex items-center gap-2">
            <SquareTerminal className="size-4 text-muted-foreground" aria-hidden="true" />
            <h2 id="run-logs-title" className="text-sm font-semibold text-foreground">Worker logs</h2>
            {podName && (
              <span className="truncate text-[11px] text-muted-foreground">
                Pod: <span className="font-mono text-foreground/80">{podName}</span> · container: <span className="font-mono text-foreground/80">worker</span>
              </span>
            )}
          </div>
          <p className="mt-0.5 text-[11px] text-muted-foreground">
            Latest 2,000 lines from the AgentRun worker container
            {lastUpdated ? ` · updated ${lastUpdated.toLocaleTimeString()}` : ""}
          </p>
        </div>
        <div className="flex items-center gap-1.5">
          <Button
            type="button"
            variant={wrap ? "secondary" : "ghost"}
            size="sm"
            className="h-7 gap-1.5 px-2 text-xs"
            aria-pressed={wrap}
            onClick={() => setWrap((current) => !current)}
          >
            <WrapText className="size-3.5" />
            Wrap
          </Button>
          <Button
            type="button"
            variant="ghost"
            size="sm"
            className="h-7 gap-1.5 px-2 text-xs"
            disabled={!content}
            onClick={() => void copyLogs()}
          >
            {copied ? <Check className="size-3.5" /> : <Clipboard className="size-3.5" />}
            {copied ? "Copied" : "Copy"}
          </Button>
          <Button
            type="button"
            variant="ghost"
            size="sm"
            className="h-7 gap-1.5 px-2 text-xs"
            disabled={loading}
            onClick={() => void onRefresh()}
          >
            <RefreshCw className={cn("size-3.5", loading && "animate-spin")} />
            Refresh
          </Button>
        </div>
      </header>

      {error && (
        <p className="m-4 rounded-md border border-[color-mix(in_oklch,var(--tone-danger)_35%,var(--color-border))] bg-[color-mix(in_oklch,var(--tone-danger)_8%,transparent)] px-3 py-2 text-xs text-[color:var(--tone-danger-fg)]" role="alert">
          {error}
        </p>
      )}

      {!error && loading && !available && (
        <div className="flex flex-1 items-center justify-center gap-2 text-xs text-muted-foreground">
          <Loader2 className="size-4 animate-spin" />
          Loading worker logs…
        </div>
      )}

      {!error && !loading && !available && (
        <div className="m-4 flex flex-1 items-center justify-center rounded-lg border border-dashed border-border px-5 py-10 text-center">
          <div>
            <p className="text-sm font-medium text-foreground">Worker logs are unavailable</p>
            <p className="mt-1 max-w-md text-xs text-muted-foreground">
              The worker pod may still be starting, or it may have already been removed.
            </p>
          </div>
        </div>
      )}

      {available && !content && (
        <div className="flex flex-1 items-center justify-center text-xs text-muted-foreground">No worker output yet.</div>
      )}

      {available && content && (
        <div className="min-h-0 flex-1 overflow-auto bg-[color-mix(in_oklch,var(--color-background)_88%,black)] p-4">
          <pre className={cn("min-w-max font-mono text-xs leading-5 text-foreground", wrap && "min-w-0 whitespace-pre-wrap break-all")}>
            {content}
          </pre>
        </div>
      )}

      {truncated && (
        <p className="shrink-0 border-t px-4 py-2 text-[11px] text-muted-foreground">
          Showing the most recent 2,000 lines. Older output was omitted.
        </p>
      )}
    </section>
  );
}
