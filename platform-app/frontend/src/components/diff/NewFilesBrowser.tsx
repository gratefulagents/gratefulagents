import { useRef, useState, type ReactNode } from "react";
import { FilePlus2, Loader2 } from "lucide-react";

import { Badge } from "@/components/ui/badge";
import { client } from "@/lib/client";
import { cn } from "@/lib/utils";

type LoadedFile = {
  content: string;
  truncated: boolean;
};

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

/**
 * Lists untracked paths without loading their contents. A bounded ReadFile RPC
 * is issued only after the user selects a path, then cached for this view.
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
  const [selected, setSelected] = useState<string | null>(null);
  const [loaded, setLoaded] = useState(() => new Map<string, LoadedFile>());
  const [loading, setLoading] = useState(() => new Set<string>());
  const pending = useRef(new Set<string>());
  const [errors, setErrors] = useState(() => new Map<string, string>());

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

  return (
    <div className="flex min-h-0 flex-1 flex-col border-t md:flex-row">
      <aside className="max-h-40 shrink-0 overflow-auto border-b bg-muted/20 md:max-h-none md:w-64 md:border-r md:border-b-0">
        <div className="sticky top-0 z-10 flex items-center gap-1.5 border-b bg-muted/90 px-3 py-2 text-xs font-medium backdrop-blur">
          <FilePlus2 className="size-3.5 text-emerald-600 dark:text-emerald-400" />
          <span>New files</span>
          <Badge variant="secondary" className="h-5 px-1.5 text-[10px]">
            {files.length}{filesTruncated ? "+" : ""}
          </Badge>
        </div>
        <ul className="py-1" aria-label="New files">
          {files.map((path) => (
            <li key={path}>
              <button
                type="button"
                className={cn(
                  "flex w-full items-center gap-2 px-3 py-1.5 text-left font-mono text-xs hover:bg-muted",
                  selected === path && "bg-muted text-foreground",
                )}
                aria-pressed={selected === path}
                onClick={() => void selectFile(path)}
              >
                {loading.has(path) ? (
                  <Loader2 className="size-3 shrink-0 animate-spin" />
                ) : (
                  <FilePlus2 className="size-3 shrink-0 text-emerald-600 dark:text-emerald-400" />
                )}
                <span className="truncate" title={path}>{path}</span>
              </button>
            </li>
          ))}
        </ul>
        {filesTruncated && (
          <p className="border-t px-3 py-2 text-[11px] text-muted-foreground">
            File list truncated.
          </p>
        )}
      </aside>

      {!selected ? children : (
        <div className="allow-context-menu flex min-h-0 min-w-0 flex-1 flex-col bg-background">
          <div className="flex shrink-0 items-center justify-between gap-2 border-b px-3 py-2">
            <span className="truncate font-mono text-xs font-medium" title={selected}>{selected}</span>
            <button
              type="button"
              className="shrink-0 text-xs text-muted-foreground hover:text-foreground"
              onClick={() => setSelected(null)}
            >
              Back to diff
            </button>
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
                <div className="border-b border-amber-500/30 bg-amber-500/10 px-3 py-2 text-xs text-amber-800 dark:text-amber-200">
                  File truncated after 1,000 lines.
                </div>
              )}
              <pre className="min-h-0 flex-1 overflow-auto p-3 font-mono text-xs leading-5 whitespace-pre select-text">
                {selectedFile.content || "(empty file)"}
              </pre>
            </>
          ) : null}
        </div>
      )}
    </div>
  );
}
