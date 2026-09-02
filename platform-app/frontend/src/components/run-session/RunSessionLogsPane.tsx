import { useLayoutEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { ArrowDown, Check, Clipboard, Loader2, RefreshCw, Search, SquareTerminal, WrapText } from "lucide-react";

import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
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

const STICKY_BOTTOM_PX = 24;

function highlight(line: string, needle: string): ReactNode {
  if (!needle) return line;
  const lower = line.toLowerCase();
  const n = needle.toLowerCase();
  const parts: ReactNode[] = [];
  let from = 0;
  for (;;) {
    const at = lower.indexOf(n, from);
    if (at < 0) break;
    if (at > from) parts.push(line.slice(from, at));
    parts.push(
      <mark key={at} className="rounded-sm bg-tone-warning/40 text-foreground">
        {line.slice(at, at + needle.length)}
      </mark>,
    );
    from = at + needle.length;
  }
  if (from < line.length) parts.push(line.slice(from));
  return parts;
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
  const [wrap, setWrap] = useState(true);
  const [copied, setCopied] = useState(false);
  const [filter, setFilter] = useState("");
  const [following, setFollowing] = useState(true);
  const scrollerRef = useRef<HTMLDivElement>(null);

  const lines = useMemo(() => {
    if (!content) return [];
    const all = content.split("\n");
    if (all.length > 1 && all[all.length - 1] === "") all.pop();
    return all.map((text, i) => ({ n: i + 1, text }));
  }, [content]);
  const needle = filter.trim();
  const shown = useMemo(
    () => (needle ? lines.filter((l) => l.text.toLowerCase().includes(needle.toLowerCase())) : lines),
    [lines, needle],
  );
  const gutterWidth = `${Math.max(String(lines.length).length, 2)}ch`;

  // Follow-tail: while the user is pinned to the bottom, new content keeps the
  // scroller at the bottom; scrolling up detaches and shows the jump pill.
  useLayoutEffect(() => {
    const el = scrollerRef.current;
    if (!el || !following) return;
    el.scrollTop = el.scrollHeight;
  }, [content, wrap, following, shown.length]);

  function onScroll() {
    const el = scrollerRef.current;
    if (!el) return;
    const atBottom = el.scrollHeight - el.scrollTop - el.clientHeight <= STICKY_BOTTOM_PX;
    if (atBottom !== following) setFollowing(atBottom);
  }

  function jumpToLatest() {
    const el = scrollerRef.current;
    if (el) el.scrollTop = el.scrollHeight;
    setFollowing(true);
  }

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
    <section className="@container flex min-h-0 flex-1 flex-col" aria-labelledby="run-logs-title">
      <header className="flex shrink-0 flex-wrap items-center justify-between gap-x-3 gap-y-2 border-b border-border/70 px-4 py-3 @lg:px-5">
        <div className="min-w-0 flex-1">
          <div className="flex min-w-0 items-center gap-2">
            <SquareTerminal className="size-4 shrink-0 text-muted-foreground" aria-hidden="true" />
            <h2 id="run-logs-title" className="shrink-0 text-sm font-semibold text-foreground">
              Worker logs
            </h2>
            {podName && (
              <span className="min-w-0 truncate text-2xs text-muted-foreground">
                Pod: <span className="font-mono text-foreground/80">{podName}</span> · container:{" "}
                <span className="font-mono text-foreground/80">worker</span>
              </span>
            )}
          </div>
          <p className="mt-0.5 min-w-0 truncate text-2xs text-muted-foreground" aria-live="polite">
            {truncated ? "Most recent 2,000 lines" : "Full output"} from the worker container
            {lastUpdated ? ` · updated ${lastUpdated.toLocaleTimeString()}` : ""}
          </p>
        </div>
        <div className="flex shrink-0 items-center gap-1.5">
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
        <p
          className="m-4 rounded-md border border-[color-mix(in_oklch,var(--tone-danger)_35%,var(--color-border))] bg-[color-mix(in_oklch,var(--tone-danger)_8%,transparent)] px-3 py-2 text-xs text-[color:var(--tone-danger-fg)]"
          role="alert"
        >
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
        <>
          <div className="relative shrink-0 border-b border-border/60 px-3 py-1.5">
            <Search className="pointer-events-none absolute top-1/2 left-5 size-3 -translate-y-1/2 text-muted-foreground" />
            <Input
              value={filter}
              onChange={(e) => setFilter(e.target.value)}
              placeholder="Filter lines…"
              aria-label="Filter log lines"
              className="h-6 w-full min-w-0 pl-6 font-mono text-xs"
            />
            {needle && (
              <span className="pointer-events-none absolute top-1/2 right-5 -translate-y-1/2 font-mono text-3xs text-muted-foreground tabular-nums">
                {shown.length}/{lines.length}
              </span>
            )}
          </div>
          <div className="relative min-h-0 flex-1">
            <div
              ref={scrollerRef}
              onScroll={onScroll}
              data-testid="log-scroller"
              className="h-full overflow-auto bg-[color-mix(in_oklch,var(--color-background)_88%,black)] py-3"
            >
              <ol
                className={cn(
                  "min-w-max font-mono text-xs leading-5 text-foreground",
                  wrap && "min-w-0",
                )}
                style={{ ["--gutter" as string]: gutterWidth }}
              >
                {shown.map((line) => (
                  <li key={line.n} className="grid grid-cols-[var(--gutter)_minmax(0,1fr)] gap-3 px-3 hover:bg-foreground/[0.03]">
                    <span
                      aria-hidden
                      className="shrink-0 select-none text-right text-muted-foreground/50 tabular-nums"
                    >
                      {line.n}
                    </span>
                    <span className={cn("whitespace-pre", wrap && "min-w-0 whitespace-pre-wrap break-all")}>
                      {highlight(line.text, needle) || " "}
                    </span>
                  </li>
                ))}
                {needle && shown.length === 0 && (
                  <li className="px-3 text-muted-foreground">No lines match “{needle}”.</li>
                )}
              </ol>
            </div>
            {!following && (
              <button
                type="button"
                onClick={jumpToLatest}
                className="absolute right-3 bottom-3 inline-flex items-center gap-1 rounded-full border border-border bg-background/95 px-2.5 py-1 text-2xs font-medium text-foreground shadow-md backdrop-blur transition-colors hover:bg-muted"
              >
                <ArrowDown className="size-3" />
                Jump to latest
              </button>
            )}
          </div>
        </>
      )}
    </section>
  );
}
