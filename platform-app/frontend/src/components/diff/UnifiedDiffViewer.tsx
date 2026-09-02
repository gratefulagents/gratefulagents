/* eslint-disable react-refresh/only-export-components */
import {
  useCallback,
  useMemo,
  useRef,
  useState,
  type KeyboardEvent,
  type ReactNode,
} from "react";
import { GroupedVirtuoso, type GroupedVirtuosoHandle, type ListRange } from "react-virtuoso";
import { common, createLowlight } from "lowlight";
import {
  Check,
  ChevronDown,
  ChevronsDownUp,
  ChevronsUpDown,
  Copy,
  FileDiff,
  FileMinus2,
  FilePen,
  FilePlus2,
  FileSymlink,
  FileX2,
  WrapText,
} from "lucide-react";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Empty, EmptyDescription, EmptyHeader, EmptyMedia, EmptyTitle } from "@/components/ui/empty";
import { LiveDot } from "@/components/ui/live-dot";
import { Skeleton } from "@/components/ui/skeleton";
import { toast } from "@/components/ui/toaster";
import {
  pairChangedRows,
  parseUnifiedDiff,
  summarizeDiff,
  type DiffFile,
  type DiffHunk,
  type DiffRow,
  type RowHighlights,
  type TextRange,
} from "@/lib/unifiedDiff";
import { cn } from "@/lib/utils";

export type {
  DiffFile,
  DiffFileStatus,
  DiffHunk,
  DiffRow,
  DiffRowKind,
  DiffSummary,
  RowHighlights,
  TextRange,
} from "@/lib/unifiedDiff";
export { pairChangedRows, parseUnifiedDiff, summarizeDiff } from "@/lib/unifiedDiff";

// ─── Legacy line classifier (still used by the activity-log detail panes) ───

export type UnifiedDiffLineKind = "file" | "hunk" | "add" | "delete" | "context" | "meta";

export type UnifiedDiffLine = {
  lineNumber: number;
  content: string;
  kind: UnifiedDiffLineKind;
};

const fileModeLinePattern = /^(?:new file mode|deleted file mode|old mode|new mode) /;

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

// ─── Syntax highlighting ───

const lowlight = createLowlight(common);

type HastRoot = ReturnType<typeof lowlight.highlight>;
type HastNode = HastRoot["children"][number];

const extensionLanguages: Record<string, string> = {
  ts: "typescript",
  tsx: "typescript",
  mts: "typescript",
  cts: "typescript",
  js: "javascript",
  jsx: "javascript",
  mjs: "javascript",
  cjs: "javascript",
  go: "go",
  py: "python",
  rs: "rust",
  css: "css",
  scss: "scss",
  less: "less",
  json: "json",
  md: "markdown",
  mdx: "markdown",
  yaml: "yaml",
  yml: "yaml",
  sh: "bash",
  bash: "bash",
  zsh: "bash",
  html: "xml",
  htm: "xml",
  xml: "xml",
  svg: "xml",
  sql: "sql",
  toml: "ini",
  ini: "ini",
  java: "java",
  kt: "kotlin",
  c: "c",
  h: "c",
  cpp: "cpp",
  cc: "cpp",
  hpp: "cpp",
  rb: "ruby",
  php: "php",
  swift: "swift",
  lua: "lua",
  diff: "diff",
  patch: "diff",
};

const fileNameLanguages: Record<string, string> = {
  makefile: "makefile",
  dockerfile: "dockerfile",
};

/** Maps a file path to a lowlight grammar name, or undefined when unknown. */
export function languageForPath(path: string): string | undefined {
  const base = path.split("/").pop()?.toLowerCase() ?? "";
  const byName = fileNameLanguages[base];
  if (byName) return lowlight.registered(byName) ? byName : undefined;
  const dot = base.lastIndexOf(".");
  if (dot === -1) return undefined;
  const lang = extensionLanguages[base.slice(dot + 1)];
  return lang && lowlight.registered(lang) ? lang : undefined;
}

const HIGHLIGHT_MAX_CHARS = 2000;
const HIGHLIGHT_CACHE_LIMIT = 5000;
// Keyed by grammar + text; identical lines recur across live re-parses so the
// cache stays warm as the diff grows.
const highlightCache = new Map<string, ReactNode>();

function hastToReact(nodes: HastNode[], keyPrefix: string): ReactNode[] {
  return nodes.map((node, index) => {
    if (node.type === "text") return node.value;
    if (node.type !== "element") return null;
    const className = node.properties?.className;
    const key = `${keyPrefix}${index}`;
    return (
      <span key={key} className={Array.isArray(className) ? className.join(" ") : undefined}>
        {hastToReact(node.children as HastNode[], `${key}.`)}
      </span>
    );
  });
}

/** Highlights one line of code; returns the plain text when no grammar applies. */
export function highlightLine(text: string, lang: string | undefined): ReactNode {
  if (!lang || !text || text.length > HIGHLIGHT_MAX_CHARS) return text;
  const cacheKey = `${lang}\u0000${text}`;
  const cached = highlightCache.get(cacheKey);
  if (cached !== undefined) return cached;
  let result: ReactNode;
  try {
    result = hastToReact(lowlight.highlight(lang, text).children, "");
  } catch {
    result = text;
  }
  if (highlightCache.size >= HIGHLIGHT_CACHE_LIMIT) highlightCache.clear();
  highlightCache.set(cacheKey, result);
  return result;
}

// ─── Viewer ───

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

type FlatItem =
  | { type: "hunk"; key: string; file: DiffFile; hunk: DiffHunk; hunkKey: string }
  | {
      type: "row";
      key: string;
      file: DiffFile;
      row: DiffRow;
      lang: string | undefined;
      highlights: TextRange[] | undefined;
    }
  | { type: "note"; key: string; file: DiffFile; text: string };

type DiffModel = {
  items: FlatItem[];
  groupCounts: number[];
  /** Item index (excluding group headers) where each file's rows begin. */
  fileStarts: number[];
  /** Item indices of every hunk header row, in order. */
  hunkIndices: number[];
  gutterDigits: number;
  hunkKeys: Set<string>;
};

const rowHighlightCache = new WeakMap<DiffHunk, RowHighlights>();

function highlightsFor(hunk: DiffHunk): RowHighlights {
  let cached = rowHighlightCache.get(hunk);
  if (!cached) {
    cached = pairChangedRows(hunk);
    rowHighlightCache.set(hunk, cached);
  }
  return cached;
}

function hunkKey(file: DiffFile, hunk: DiffHunk): string {
  return `${file.id}\u0000${hunk.header}`;
}

function fileNote(file: DiffFile): string {
  if (file.status === "binary") return "Binary file changed";
  if (file.status === "renamed") return "File renamed without content changes";
  const modeLine = file.headerLines.find((line) => line.startsWith("new mode "));
  if (modeLine) return `File mode changed (${modeLine.slice("new mode ".length)})`;
  return "No content changes";
}

function buildModel(files: DiffFile[], collapsed: Set<string>): DiffModel {
  const items: FlatItem[] = [];
  const groupCounts: number[] = [];
  const fileStarts: number[] = [];
  const hunkIndices: number[] = [];
  const hunkKeys = new Set<string>();
  let maxLineNo = 1;

  for (const file of files) {
    fileStarts.push(items.length);
    const lang = languageForPath(file.newPath || file.oldPath);
    for (const hunk of file.hunks) {
      hunkKeys.add(hunkKey(file, hunk));
      maxLineNo = Math.max(maxLineNo, hunk.oldStart + hunk.oldLines, hunk.newStart + hunk.newLines);
    }
    if (collapsed.has(file.id)) {
      groupCounts.push(0);
      continue;
    }
    const start = items.length;
    if (file.hunks.length === 0) {
      items.push({ type: "note", key: `${file.id}:note`, file, text: fileNote(file) });
    }
    file.hunks.forEach((hunk, hunkIndex) => {
      hunkIndices.push(items.length);
      items.push({ type: "hunk", key: `${file.id}:h${hunkIndex}`, file, hunk, hunkKey: hunkKey(file, hunk) });
      const highlights = highlightsFor(hunk);
      hunk.lines.forEach((row, rowIndex) => {
        items.push({
          type: "row",
          key: `${file.id}:h${hunkIndex}:r${rowIndex}`,
          file,
          row,
          lang,
          highlights: highlights.get(rowIndex),
        });
      });
    });
    groupCounts.push(items.length - start);
  }

  return {
    items,
    groupCounts,
    fileStarts,
    hunkIndices,
    gutterDigits: Math.max(2, String(maxLineNo).length),
    hunkKeys,
  };
}

function errorMessage(error: Error | string | null | undefined): string {
  if (!error) return "";
  return error instanceof Error ? error.message : error;
}

function splitPath(path: string): { dir: string; base: string } {
  const slash = path.lastIndexOf("/");
  if (slash === -1) return { dir: "", base: path };
  return { dir: path.slice(0, slash + 1), base: path.slice(slash + 1) };
}

async function copyText(text: string, successMessage: string): Promise<void> {
  try {
    await navigator.clipboard?.writeText(text);
    toast.success(successMessage);
  } catch {
    toast.error("Copy failed");
  }
}

function StatusBadge({
  loading,
  error,
  isComplete,
  source,
}: Pick<UnifiedDiffViewerProps, "loading" | "error" | "isComplete" | "source">): ReactNode {
  const title = source && source !== "unavailable" ? `Source: ${source}` : undefined;
  if (loading) {
    return (
      <Badge variant="secondary" title={title} className="gap-1.5">
        <LiveDot tone="idle" pulse size="xs" />
        Loading
      </Badge>
    );
  }
  if (error) return <Badge variant="destructive" title={title}>Error</Badge>;
  if (isComplete) return <Badge variant="outline" title={title}>Final</Badge>;
  return (
    <Badge variant="secondary" title={title} className="gap-1.5">
      <LiveDot tone="running" pulse size="xs" />
      Live
    </Badge>
  );
}

const statusIcons: Record<DiffFile["status"], { icon: typeof FilePen; className: string; label: string }> = {
  modified: { icon: FilePen, className: "text-diff-file-fg", label: "Modified" },
  added: { icon: FilePlus2, className: "text-diff-add-fg", label: "Added" },
  deleted: { icon: FileMinus2, className: "text-diff-del-fg", label: "Deleted" },
  renamed: { icon: FileSymlink, className: "text-diff-hunk-fg", label: "Renamed" },
  binary: { icon: FileX2, className: "text-muted-foreground", label: "Binary" },
};

function FileHeader({
  file,
  collapsed,
  onToggle,
}: {
  file: DiffFile;
  collapsed: boolean;
  onToggle: () => void;
}): ReactNode {
  const { icon: Icon, className, label } = statusIcons[file.status];
  const { dir, base } = splitPath(file.displayPath);
  return (
    // Opaque backing under the tint so rows never bleed through the sticky header.
    <div data-slot="diff-file-header" className="bg-background">
      <div className="flex h-8 items-center gap-1 border-b border-border/60 bg-diff-file/10 pr-1.5 pl-1 text-xs">
        <button
          type="button"
          className="flex min-w-0 flex-1 items-center gap-1.5 rounded-md py-1 pr-1 pl-0.5 text-left outline-none focus-visible:ring-2 focus-visible:ring-ring/50"
          aria-expanded={!collapsed}
          aria-label={`${collapsed ? "Expand" : "Collapse"} ${file.displayPath}`}
          title={collapsed ? "Expand file" : "Collapse file"}
          onClick={onToggle}
        >
          <ChevronDown
            className={cn(
              "size-3.5 shrink-0 text-muted-foreground transition-transform",
              collapsed && "-rotate-90",
            )}
            aria-hidden
          />
          <Icon className={cn("size-3.5 shrink-0", className)} aria-label={label} />
          <span className="min-w-0 flex-1 truncate font-mono" title={file.displayPath}>
            {dir && <span className="text-muted-foreground">{dir}</span>}
            <span className="font-semibold text-foreground">{base}</span>
          </span>
        </button>
        {(file.additions > 0 || file.deletions > 0) && (
          <span className="shrink-0 font-mono text-2xs tabular-nums">
            <span className="text-diff-add-fg">+{file.additions}</span>{" "}
            <span className="text-diff-del-fg">−{file.deletions}</span>
          </span>
        )}
        <Button
          variant="ghost"
          size="icon-xs"
          className="shrink-0 text-muted-foreground"
          aria-label={`Copy path ${file.displayPath}`}
          title="Copy path"
          onClick={() => void copyText(file.newPath || file.oldPath, "Path copied")}
        >
          <Copy />
        </Button>
      </div>
    </div>
  );
}

function CodeText({
  text,
  lang,
  highlights,
  markClassName,
}: {
  text: string;
  lang: string | undefined;
  highlights: TextRange[] | undefined;
  markClassName: string;
}): ReactNode {
  if (!highlights || highlights.length === 0) return highlightLine(text, lang);
  const parts: ReactNode[] = [];
  let cursor = 0;
  highlights.forEach((range, index) => {
    if (range.start > cursor) {
      parts.push(<span key={`p${index}`}>{highlightLine(text.slice(cursor, range.start), lang)}</span>);
    }
    parts.push(
      <mark key={`m${index}`} className={cn("rounded-[2px] text-inherit", markClassName)}>
        {highlightLine(text.slice(range.start, range.end), lang)}
      </mark>,
    );
    cursor = range.end;
  });
  if (cursor < text.length) parts.push(<span key="tail">{highlightLine(text.slice(cursor), lang)}</span>);
  return parts;
}

const rowTints: Record<DiffRow["kind"], string> = {
  add: "bg-diff-add/12 text-diff-add-fg",
  delete: "bg-diff-del/12 text-diff-del-fg",
  context: "text-foreground",
  meta: "text-muted-foreground italic",
};

const rowMarkers: Record<DiffRow["kind"], string> = {
  add: "+",
  delete: "−",
  context: " ",
  meta: "\\",
};

function DiffRowView({ item, wrap }: { item: Extract<FlatItem, { type: "row" }>; wrap: boolean }): ReactNode {
  const { row, lang, highlights } = item;
  const gutterClass =
    "w-[calc(var(--diff-gutter)*1ch+0.75rem)] shrink-0 select-none pr-1.5 text-right text-3xs leading-5 text-muted-foreground/60 tabular-nums";
  return (
    <div data-kind={row.kind} className={cn("flex min-h-5 font-mono text-xs leading-5", rowTints[row.kind])}>
      <span className={gutterClass} aria-hidden>{row.oldNo ?? ""}</span>
      <span className={gutterClass} aria-hidden>{row.newNo ?? ""}</span>
      <span className="w-4 shrink-0 select-none text-center" aria-hidden>{rowMarkers[row.kind]}</span>
      <span className={cn("min-w-0 flex-1 pr-3", wrap ? "whitespace-pre-wrap break-words" : "whitespace-pre")}>
        {row.kind === "meta" ? (
          row.text
        ) : (
          <CodeText
            text={row.text}
            lang={lang}
            highlights={highlights}
            markClassName={row.kind === "add" ? "bg-diff-add/30" : "bg-diff-del/30"}
          />
        )}
      </span>
    </div>
  );
}

function HunkHeaderRow({ hunk, fresh }: { hunk: DiffHunk; fresh: boolean }): ReactNode {
  return (
    <div
      data-kind="hunk"
      className={cn(
        "flex min-h-5 items-center bg-diff-hunk/10 px-2 font-mono text-2xs leading-5 text-diff-hunk-fg select-none",
        fresh && "animate-row-in animate-edge-glow",
      )}
    >
      <span className="min-w-0 whitespace-pre-wrap break-all" title={hunk.header}>{hunk.header}</span>
    </div>
  );
}

function LoadingRows(): ReactNode {
  const widths = ["w-2/3", "w-1/2", "w-5/6", "w-1/3", "w-3/4", "w-2/5", "w-1/2", "w-2/3"];
  return (
    <div className="flex flex-1 flex-col gap-1.5 p-3" role="status" aria-live="polite" aria-label="Loading diff">
      <Skeleton className="mb-1 h-6 w-3/4" />
      {widths.map((width, index) => (
        <div key={index} className="flex items-center gap-2">
          <Skeleton className="h-3 w-6" />
          <Skeleton className="h-3 w-6" />
          <Skeleton className={cn("h-3", width)} />
        </div>
      ))}
    </div>
  );
}

function StateMessage({ children, role }: { children: ReactNode; role: "alert" | "status" }): ReactNode {
  return (
    <div
      className="flex min-h-32 flex-1 items-center justify-center p-6 text-center text-sm text-muted-foreground"
      role={role}
      aria-live={role === "status" ? "polite" : undefined}
    >
      {children}
    </div>
  );
}

function isEditableTarget(target: EventTarget | null): boolean {
  if (!(target instanceof HTMLElement)) return false;
  return target.isContentEditable || /^(INPUT|TEXTAREA|SELECT)$/.test(target.tagName);
}

export function UnifiedDiffViewer({
  diff,
  loading = false,
  error = null,
  isComplete = false,
  truncated = false,
  source,
  emptyMessage,
  toolbar,
  bodyWrapper,
}: UnifiedDiffViewerProps): ReactNode {
  const hasDiff = diff.trim().length > 0;
  const message = errorMessage(error);
  const files = useMemo(() => (hasDiff ? parseUnifiedDiff(diff) : []), [diff, hasDiff]);
  const summary = useMemo(() => summarizeDiff(files), [files]);

  // Wrapped by default: the viewer lives in a narrow inspector pane where
  // clipped lines hide the very edits the user came to read.
  const [wrap, setWrap] = useState(true);
  const [collapsed, setCollapsed] = useState<Set<string>>(() => new Set());
  const [copied, setCopied] = useState(false);
  const model = useMemo(() => buildModel(files, collapsed), [files, collapsed]);

  // Hunks that were not present on the previous parse animate in. Tracking the
  // previous key set as state (adjusted during render) keeps the first paint
  // still and avoids reading refs while rendering.
  const [hunkTracking, setHunkTracking] = useState<{ keys: Set<string>; fresh: Set<string> }>(() => ({
    keys: model.hunkKeys,
    fresh: new Set<string>(),
  }));
  if (hunkTracking.keys !== model.hunkKeys) {
    const fresh = new Set<string>();
    for (const key of model.hunkKeys) if (!hunkTracking.keys.has(key)) fresh.add(key);
    setHunkTracking({ keys: model.hunkKeys, fresh });
  }
  const freshHunks = hunkTracking.fresh;

  const virtuosoRef = useRef<GroupedVirtuosoHandle>(null);
  const visibleRange = useRef<ListRange>({ startIndex: 0, endIndex: 0 });

  const toggleFile = useCallback((id: string) => {
    setCollapsed((current) => {
      const next = new Set(current);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  }, []);

  const allCollapsed = files.length > 0 && files.every((file) => collapsed.has(file.id));
  const toggleAll = (): void => {
    setCollapsed(allCollapsed ? new Set() : new Set(files.map((file) => file.id)));
  };

  const copyPatch = async (): Promise<void> => {
    await copyText(diff, "Patch copied");
    setCopied(true);
    window.setTimeout(() => setCopied(false), 1500);
  };

  const currentFileIndex = (): number => {
    const start = visibleRange.current.startIndex;
    let index = 0;
    for (let i = 0; i < model.fileStarts.length; i++) {
      if (model.fileStarts[i] <= start && model.groupCounts[i] > 0) index = i;
    }
    return index;
  };

  const handleKeyDown = (event: KeyboardEvent<HTMLElement>): void => {
    if (isEditableTarget(event.target) || files.length === 0) return;
    const handle = virtuosoRef.current;
    if (!handle) return;
    if (event.key === "[" || event.key === "]") {
      const delta = event.key === "]" ? 1 : -1;
      const target = Math.max(0, Math.min(files.length - 1, currentFileIndex() + delta));
      handle.scrollToIndex({ groupIndex: target, align: "start" });
      event.preventDefault();
      return;
    }
    if (event.altKey && (event.key === "ArrowDown" || event.key === "ArrowUp")) {
      const start = visibleRange.current.startIndex;
      const target =
        event.key === "ArrowDown"
          ? model.hunkIndices.find((index) => index > start)
          : [...model.hunkIndices].reverse().find((index) => index < start);
      if (target !== undefined) handle.scrollToIndex({ index: target, align: "start" });
      event.preventDefault();
    }
  };

  let body: ReactNode;

  if (files.length > 0) {
    body = (
      <div
        className="allow-context-menu min-h-0 min-w-0 flex-1 select-text"
        style={{ "--diff-gutter": model.gutterDigits } as React.CSSProperties}
        data-testid="diff-body"
      >
        <GroupedVirtuoso
          ref={virtuosoRef}
          className="h-full overflow-x-auto"
          groupCounts={model.groupCounts}
          computeItemKey={(index) => model.items[index]?.key ?? index}
          rangeChanged={(range) => {
            visibleRange.current = range;
          }}
          components={{
            List: ListContainer,
          }}
          context={{ wrap }}
          groupContent={(groupIndex) => {
            const file = files[groupIndex];
            return (
              <FileHeader
                file={file}
                collapsed={collapsed.has(file.id)}
                onToggle={() => toggleFile(file.id)}
              />
            );
          }}
          itemContent={(index) => {
            const item = model.items[index];
            if (!item) return null;
            if (item.type === "hunk") {
              return <HunkHeaderRow hunk={item.hunk} fresh={freshHunks.has(item.hunkKey)} />;
            }
            if (item.type === "note") {
              return (
                <div className="px-3 py-2 font-mono text-2xs text-muted-foreground italic">{item.text}</div>
              );
            }
            return <DiffRowView item={item} wrap={wrap} />;
          }}
        />
      </div>
    );
  } else if (loading) {
    body = <LoadingRows />;
  } else if (message) {
    body = <StateMessage role="alert">Error loading diff: {message}</StateMessage>;
  } else if (source === "unavailable") {
    body = <StateMessage role="status">Diff source unavailable</StateMessage>;
  } else {
    body = (
      <Empty className="min-h-32 border-0 py-8" role="status" aria-live="polite">
        <EmptyHeader>
          <EmptyMedia variant="icon">
            <FileDiff />
          </EmptyMedia>
          <EmptyTitle>No changes yet</EmptyTitle>
          <EmptyDescription>
            {emptyMessage ??
              (isComplete
                ? "The run finished without modifying tracked files."
                : "Changes appear here as the agent edits files.")}
          </EmptyDescription>
        </EmptyHeader>
      </Empty>
    );
  }

  return (
    <section
      className="flex h-full w-full min-h-0 min-w-0 flex-1 flex-col overflow-hidden bg-background"
      aria-label="Diff"
      onKeyDown={handleKeyDown}
    >
      <div className="flex h-10 shrink-0 items-center gap-2 border-b px-2">
        {toolbar}
        {files.length > 0 && (
          <span className="truncate text-xs text-muted-foreground tabular-nums" aria-label="Diff summary">
            {summary.files} {summary.files === 1 ? "file" : "files"}
            <span className="mx-1 text-muted-foreground/50">·</span>
            <span className="text-diff-add-fg">+{summary.additions}</span>{" "}
            <span className="text-diff-del-fg">−{summary.deletions}</span>
          </span>
        )}
        <div className="flex-1" />
        <div className="flex shrink-0 items-center gap-0.5">
          <Button
            variant="ghost"
            size="icon-xs"
            aria-pressed={wrap}
            aria-label="Wrap lines"
            title="Wrap lines"
            className={cn("text-muted-foreground", wrap && "bg-muted text-foreground")}
            onClick={() => setWrap((current) => !current)}
          >
            <WrapText />
          </Button>
          <Button
            variant="ghost"
            size="icon-xs"
            aria-label="Copy patch"
            title="Copy patch"
            className="text-muted-foreground"
            disabled={!hasDiff}
            onClick={() => void copyPatch()}
          >
            {copied ? <Check className="text-tone-success" /> : <Copy />}
          </Button>
          <Button
            variant="ghost"
            size="icon-xs"
            aria-label={allCollapsed ? "Expand all files" : "Collapse all files"}
            title={`${allCollapsed ? "Expand all" : "Collapse all"} · [ / ] previous/next file · Alt+↑/↓ previous/next hunk`}
            className="text-muted-foreground"
            disabled={files.length === 0}
            onClick={toggleAll}
          >
            {allCollapsed ? <ChevronsUpDown /> : <ChevronsDownUp />}
          </Button>
        </div>
        <StatusBadge loading={loading} error={error} isComplete={isComplete} source={source} />
      </div>

      {hasDiff && message && (
        <div className="border-b border-tone-warning/30 bg-tone-warning/12 px-3 py-1.5 text-xs text-tone-warning-fg" role="alert">
          Warning: {message}
        </div>
      )}
      {hasDiff && truncated && (
        <div className="border-b border-tone-warning/30 bg-tone-warning/12 px-3 py-1.5 text-xs text-tone-warning-fg">
          Diff truncated. Showing the available portion.
        </div>
      )}

      {bodyWrapper ? bodyWrapper(body) : body}
    </section>
  );
}

type ListContext = { wrap: boolean };

// Virtuoso passes the list container sizing inline; widening it to the
// longest row lets every row tint span the full horizontal scroll width.
function ListContainer({
  context,
  ...props
}: React.ComponentPropsWithRef<"div"> & { context?: ListContext }) {
  return <div {...props} className={cn("min-w-full", context?.wrap ? "w-full" : "w-max")} />;
}
