import { useId, useRef, useState, type KeyboardEvent, type ReactNode } from "react";
import { ArrowLeft, Copy, FilePlus2, Loader2 } from "lucide-react";

import { highlightLine, languageForPath } from "@/components/diff/UnifiedDiffViewer";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { toast } from "@/components/ui/toaster";
import { client } from "@/lib/client";
import { cn } from "@/lib/utils";

type LoadedFile = {
  content: string;
  truncated: boolean;
};

type Mode = "tracked" | "new";

export type NewFilesBrowserProps = {
  namespace: string;
  name: string;
  resourceType?: string;
  repoPath?: string;
  files: string[];
  filesTruncated?: boolean;
  /** The tracked diff pane shown until a new file is selected. */
  children: ReactNode;
};

const modes: Mode[] = ["tracked", "new"];

/**
 * Lists untracked paths without loading their contents. A bounded ReadFile RPC
 * is issued only after the user selects a path, then cached for this view.
 * A segmented control switches between the tracked diff and the new-file
 * browser; the tracked pane stays mounted so its scroll position survives.
 */
export function NewFilesBrowser({
  namespace,
  name,
  resourceType = "AgentRun",
  repoPath = "",
  files,
  filesTruncated = false,
  children,
}: NewFilesBrowserProps): ReactNode {
  const [mode, setMode] = useState<Mode>("tracked");
  const [selected, setSelected] = useState<string | null>(null);
  const [loaded, setLoaded] = useState(() => new Map<string, LoadedFile>());
  const [loading, setLoading] = useState(() => new Set<string>());
  const pending = useRef(new Set<string>());
  const [errors, setErrors] = useState(() => new Map<string, string>());
  const baseId = useId();
  const tabRefs = useRef<Partial<Record<Mode, HTMLButtonElement | null>>>({});

  async function selectFile(path: string): Promise<void> {
    setSelected(path);
    if (loaded.has(path) || pending.current.has(path)) return;

    pending.current.add(path);
    setLoading((current) => new Set(current).add(path));
    setErrors((current) => {
      const next = new Map(current);
      next.delete(path);
      return next;
    });
    try {
      const response = await client.readFile({
        namespace,
        name,
        resourceType,
        repoPath,
        path,
        maxLines: 1000,
      });
      setLoaded((current) => new Map(current).set(path, {
        content: response.content,
        truncated: response.truncated,
      }));
    } catch (error) {
      setErrors((current) => new Map(current).set(
        path,
        error instanceof Error ? error.message : "Failed to load file",
      ));
    } finally {
      pending.current.delete(path);
      setLoading((current) => {
        const next = new Set(current);
        next.delete(path);
        return next;
      });
    }
  }

  if (files.length === 0) return children;

  const selectedFile = selected ? loaded.get(selected) : undefined;
  const selectedError = selected ? errors.get(selected) : undefined;

  const focusMode = (next: Mode): void => {
    setMode(next);
    tabRefs.current[next]?.focus();
  };

  const handleTabKeyDown = (event: KeyboardEvent<HTMLDivElement>): void => {
    const index = modes.indexOf(mode);
    if (event.key === "ArrowRight" || event.key === "ArrowDown") {
      focusMode(modes[(index + 1) % modes.length]);
    } else if (event.key === "ArrowLeft" || event.key === "ArrowUp") {
      focusMode(modes[(index - 1 + modes.length) % modes.length]);
    } else if (event.key === "Home") {
      focusMode(modes[0]);
    } else if (event.key === "End") {
      focusMode(modes[modes.length - 1]);
    } else {
      return;
    }
    event.preventDefault();
  };

  const tabClass = (active: boolean): string =>
    cn(
      "inline-flex h-6 items-center gap-1.5 rounded-[6px] px-2 text-xs font-medium whitespace-nowrap outline-none transition-colors focus-visible:ring-2 focus-visible:ring-ring/50",
      active ? "bg-background text-foreground shadow-xs" : "text-muted-foreground hover:text-foreground",
    );

  const copyFile = async (): Promise<void> => {
    if (!selectedFile) return;
    try {
      await navigator.clipboard?.writeText(selectedFile.content);
      toast.success("File copied");
    } catch {
      toast.error("Copy failed");
    }
  };

  return (
    <div className="flex min-h-0 min-w-0 flex-1 flex-col">
      <div
        role="tablist"
        aria-label="Change sets"
        className="flex h-9 shrink-0 items-center border-b px-2"
        onKeyDown={handleTabKeyDown}
      >
        <div className="inline-flex items-center gap-0.5 rounded-lg bg-muted p-0.5">
          <button
            type="button"
            role="tab"
            id={`${baseId}-tab-tracked`}
            aria-selected={mode === "tracked"}
            aria-controls={`${baseId}-panel-tracked`}
            tabIndex={mode === "tracked" ? 0 : -1}
            ref={(el) => {
              tabRefs.current.tracked = el;
            }}
            className={tabClass(mode === "tracked")}
            onClick={() => setMode("tracked")}
          >
            Tracked changes
          </button>
          <button
            type="button"
            role="tab"
            id={`${baseId}-tab-new`}
            aria-selected={mode === "new"}
            aria-controls={`${baseId}-panel-new`}
            tabIndex={mode === "new" ? 0 : -1}
            ref={(el) => {
              tabRefs.current.new = el;
            }}
            className={tabClass(mode === "new")}
            onClick={() => setMode("new")}
          >
            <FilePlus2 className="size-3 text-diff-add-fg" aria-hidden />
            New files
            <Badge variant="secondary" className="h-4 min-w-4 px-1 text-3xs">
              {files.length}{filesTruncated ? "+" : ""}
            </Badge>
          </button>
        </div>
      </div>

      <div
        role="tabpanel"
        id={`${baseId}-panel-tracked`}
        aria-labelledby={`${baseId}-tab-tracked`}
        className={cn("flex min-h-0 min-w-0 flex-1 flex-col", mode !== "tracked" && "hidden")}
      >
        {children}
      </div>

      {mode === "new" && (
        <div
          role="tabpanel"
          id={`${baseId}-panel-new`}
          aria-labelledby={`${baseId}-tab-new`}
          className="@container flex min-h-0 min-w-0 flex-1 flex-col @sm:flex-row"
        >
          <aside className="max-h-40 shrink-0 overflow-auto border-b bg-muted/20 @sm:max-h-none @sm:w-56 @sm:border-r @sm:border-b-0">
            <ul className="py-1" aria-label="New files">
              {files.map((path) => (
                <li key={path}>
                  <button
                    type="button"
                    className={cn(
                      "flex w-full items-center gap-2 px-3 py-1.5 text-left font-mono text-xs outline-none hover:bg-muted focus-visible:ring-2 focus-visible:ring-ring/50 focus-visible:ring-inset",
                      selected === path && "bg-muted text-foreground",
                    )}
                    aria-pressed={selected === path}
                    onClick={() => void selectFile(path)}
                  >
                    {loading.has(path) ? (
                      <Loader2 className="size-3 shrink-0 animate-spin" />
                    ) : (
                      <FilePlus2 className="size-3 shrink-0 text-diff-add-fg" />
                    )}
                    <span className="truncate" title={path}>{path}</span>
                  </button>
                </li>
              ))}
            </ul>
            {filesTruncated && (
              <p className="border-t px-3 py-2 text-2xs text-muted-foreground">
                File list truncated.
              </p>
            )}
          </aside>

          {!selected ? (
            <div
              className="flex min-h-24 flex-1 items-center justify-center p-6 text-center text-sm text-muted-foreground"
              role="status"
            >
              Select a file to preview it.
            </div>
          ) : (
            <div className="allow-context-menu flex min-h-0 min-w-0 flex-1 flex-col bg-background">
              <div className="sticky top-0 z-10 flex h-8 shrink-0 items-center gap-1 border-b bg-diff-file/10 pr-1.5 pl-2 text-xs backdrop-blur">
                <FilePlus2 className="size-3.5 shrink-0 text-diff-add-fg" aria-hidden />
                <span className="min-w-0 flex-1 truncate font-mono font-medium" title={selected}>{selected}</span>
                <Button
                  variant="ghost"
                  size="icon-xs"
                  className="text-muted-foreground"
                  aria-label="Copy file contents"
                  title="Copy file contents"
                  disabled={!selectedFile}
                  onClick={() => void copyFile()}
                >
                  <Copy />
                </Button>
                <Button
                  variant="ghost"
                  size="xs"
                  className="text-muted-foreground"
                  title="Back to tracked changes"
                  onClick={() => setMode("tracked")}
                >
                  <ArrowLeft data-icon="inline-start" />
                  Back
                </Button>
              </div>
              {loading.has(selected) ? (
                <div className="flex flex-1 items-center justify-center gap-2 text-sm text-muted-foreground" role="status">
                  <Loader2 className="size-4 animate-spin" /> Loading file…
                </div>
              ) : selectedError ? (
                <div className="flex flex-1 items-center justify-center p-6 text-center text-sm text-destructive" role="alert">
                  Error loading file: {selectedError}
                </div>
              ) : selectedFile ? (
                <>
                  {selectedFile.truncated && (
                    <div className="border-b border-tone-warning/30 bg-tone-warning/12 px-3 py-1.5 text-xs text-tone-warning-fg">
                      File truncated after 1,000 lines.
                    </div>
                  )}
                  <FilePreview path={selected} content={selectedFile.content} />
                </>
              ) : null}
            </div>
          )}
        </div>
      )}
    </div>
  );
}

function FilePreview({ path, content }: { path: string; content: string }): ReactNode {
  if (!content) {
    return <pre className="p-3 font-mono text-xs text-muted-foreground">(empty file)</pre>;
  }
  const lang = languageForPath(path);
  const lines = content.replace(/\r\n?/g, "\n").split("\n");
  if (lines[lines.length - 1] === "") lines.pop();
  const gutter = `${Math.max(2, String(lines.length).length)}ch`;
  return (
    <pre className="min-h-0 flex-1 overflow-auto py-1 font-mono text-xs leading-5 whitespace-pre select-text">
      {lines.map((line, index) => (
        <div key={index} className="flex min-h-5 hover:bg-muted/40">
          <span
            className="shrink-0 pr-2 pl-2 text-right text-3xs leading-5 text-muted-foreground/60 tabular-nums select-none"
            style={{ minWidth: `calc(${gutter} + 1rem)` }}
            aria-hidden
          >
            {index + 1}
          </span>
          <span className="min-w-0 flex-1 pr-3">{highlightLine(line, lang)}</span>
        </div>
      ))}
    </pre>
  );
}
