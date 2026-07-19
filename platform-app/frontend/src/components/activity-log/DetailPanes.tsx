import { useMemo, type ReactNode } from "react";
import { FileEdit, FilePlus } from "lucide-react";

import { ScrollArea } from "@/components/ui/scroll-area";
import { classifyDiffLine, type UnifiedDiffLineKind } from "@/components/diff/UnifiedDiffViewer";
import type { ActivityEntry } from "@/rpc/platform/service_pb";
import { bashCommand, extractUnifiedDiff, fileTarget, parseInput } from "@/lib/activityLogFormat";

function renderJsonValue(value: unknown, indent: number): ReactNode {
  if (value === null) return <span className="text-orange-400/80">null</span>;
  if (typeof value === "boolean")
    return <span className="text-orange-400/80">{String(value)}</span>;
  if (typeof value === "number")
    return <span className="text-cyan-400/90">{String(value)}</span>;
  if (typeof value === "string")
    return <span className="text-emerald-400/80">&quot;{value}&quot;</span>;
  if (Array.isArray(value)) {
    if (value.length === 0)
      return <span className="text-muted-foreground">[]</span>;
    const pad = "  ".repeat(indent + 1);
    const closePad = "  ".repeat(indent);
    return (
      <>
        <span className="text-muted-foreground">[</span>
        {"\n"}
        {value.map((item, i) => (
          <span key={i}>
            {pad}
            {renderJsonValue(item, indent + 1)}
            {i < value.length - 1 ? (
              <span className="text-muted-foreground/60">,</span>
            ) : null}
            {"\n"}
          </span>
        ))}
        {closePad}
        <span className="text-muted-foreground">]</span>
      </>
    );
  }
  if (typeof value === "object") {
    const entries = Object.entries(value as Record<string, unknown>);
    if (entries.length === 0)
      return <span className="text-muted-foreground">{"{}"}</span>;
    const pad = "  ".repeat(indent + 1);
    const closePad = "  ".repeat(indent);
    return (
      <>
        <span className="text-muted-foreground">{"{"}</span>
        {"\n"}
        {entries.map(([k, v], i) => (
          <span key={k}>
            {pad}
            <span className="text-indigo-400/90">&quot;{k}&quot;</span>
            <span className="text-muted-foreground/60">: </span>
            {renderJsonValue(v, indent + 1)}
            {i < entries.length - 1 ? (
              <span className="text-muted-foreground/60">,</span>
            ) : null}
            {"\n"}
          </span>
        ))}
        {closePad}
        <span className="text-muted-foreground">{"}"}</span>
      </>
    );
  }
  return <span>{String(value)}</span>;
}

function isJsonLike(s: string): boolean {
  const trimmed = s.trimStart();
  return trimmed.startsWith("{") || trimmed.startsWith("[");
}

export function CodePane({
  text,
  tone = "default",
  maxHeight = 360,
}: {
  text: string;
  tone?: "default" | "error";
  maxHeight?: number;
}) {
  const json = useMemo(() => {
    if (tone === "error" || !isJsonLike(text)) return null;
    try {
      return renderJsonValue(JSON.parse(text), 0);
    } catch {
      return null;
    }
  }, [text, tone]);

  return (
    <div
      className={`rounded-md border ${
        tone === "error"
          ? "border-destructive/25 bg-destructive/5"
          : "border-border/60 bg-muted/30"
      }`}
    >
      <ScrollArea className="overflow-auto" style={{ maxHeight }}>
        <pre
          className={`p-3 text-xs font-mono whitespace-pre-wrap break-all leading-relaxed ${
            tone === "error" ? "text-destructive" : "text-muted-foreground"
          }`}
        >
          {json ?? text}
        </pre>
      </ScrollArea>
    </div>
  );
}

export function TerminalPane({
  command,
  output,
  isError,
}: {
  command: string;
  output?: string;
  isError?: boolean;
}) {
  return (
    <div className="overflow-hidden rounded-md border border-border/60 bg-muted/40">
      <pre className="px-3 py-2 text-xs font-mono whitespace-pre-wrap break-all leading-relaxed text-emerald-500 dark:text-emerald-400/90">
        <span className="select-none text-muted-foreground/60">$ </span>
        {command}
      </pre>
      {output && (
        <ScrollArea className="overflow-auto border-t border-border/60" style={{ maxHeight: 300 }}>
          <pre
            className={`px-3 py-2 text-xs font-mono whitespace-pre-wrap break-all leading-relaxed ${
              isError ? "text-destructive" : "text-muted-foreground"
            }`}
          >
            {output}
          </pre>
        </ScrollArea>
      )}
    </div>
  );
}

const diffLineStyles: Record<UnifiedDiffLineKind, string> = {
  file: "text-sky-600 dark:text-sky-300",
  hunk: "bg-violet-500/10 text-violet-700 dark:text-violet-300",
  add: "bg-emerald-500/10 text-emerald-700 dark:text-emerald-400",
  delete: "bg-red-500/10 text-red-600 dark:text-red-400",
  context: "text-muted-foreground",
  meta: "text-muted-foreground/70",
};

/**
 * DiffPane renders a unified diff (as returned by the SDK Edit tool) with
 * per-line add/delete/context coloring, compact enough for the activity log.
 */
export function DiffPane({
  diff,
  maxHeight = 300,
}: {
  diff: string;
  maxHeight?: number;
}) {
  const lines = useMemo(
    () => diff.replace(/\r\n?/g, "\n").split("\n"),
    [diff],
  );
  return (
    <div className="overflow-hidden rounded-md border border-border/60 bg-muted/20">
      <ScrollArea className="overflow-auto" style={{ maxHeight }}>
        <div className="py-1 text-xs font-mono leading-relaxed">
          {lines.map((line, i) => (
            <div
              key={i}
              data-kind={classifyDiffLine(line)}
              className={`px-3 whitespace-pre-wrap break-all ${diffLineStyles[classifyDiffLine(line)]}`}
            >
              {line || " "}
            </div>
          ))}
        </div>
      </ScrollArea>
    </div>
  );
}

export function RowDetail({
  use,
  result,
}: {
  use: ActivityEntry;
  result?: ActivityEntry;
}) {
  const tool = use.tool?.toLowerCase() ?? "";
  const isError = result?.isError ?? false;

  if (tool === "bash" || tool === "execute") {
    return (
      <TerminalPane
        command={bashCommand(use)}
        output={result?.output}
        isError={isError}
      />
    );
  }

  if (tool === "edit") {
    const parsed = parseInput(use);
    // The SDK Edit tool appends a unified diff of what changed to its result;
    // prefer it (exact line numbers and context) over echoing the input.
    const diff = extractUnifiedDiff(result?.output ?? "");
    const oldStr = (parsed?.old_string ?? parsed?.old_str) as string | undefined;
    const newStr = (parsed?.new_string ?? parsed?.new_str) as string | undefined;
    return (
      <div className="space-y-2">
        <div className="flex items-center gap-2 rounded-md border border-border/60 bg-muted/20 px-3 py-1.5">
          <FileEdit className="size-3 shrink-0 text-muted-foreground" />
          <span className="font-mono text-xs text-foreground/90 break-all">
            {fileTarget(use)}
          </span>
        </div>
        {diff ? (
          <DiffPane diff={diff} />
        ) : (
          <>
            {oldStr !== undefined && (
              <div className="overflow-hidden rounded-md border border-red-500/20 bg-red-500/5">
                <ScrollArea className="overflow-auto" style={{ maxHeight: 220 }}>
                  <pre className="px-3 py-2 text-xs font-mono whitespace-pre-wrap break-all leading-relaxed text-red-400/90">
                    {oldStr}
                  </pre>
                </ScrollArea>
              </div>
            )}
            {newStr !== undefined && (
              <div className="overflow-hidden rounded-md border border-emerald-500/20 bg-emerald-500/5">
                <ScrollArea className="overflow-auto" style={{ maxHeight: 220 }}>
                  <pre className="px-3 py-2 text-xs font-mono whitespace-pre-wrap break-all leading-relaxed text-emerald-500 dark:text-emerald-400/90">
                    {newStr}
                  </pre>
                </ScrollArea>
              </div>
            )}
          </>
        )}
        {isError && result?.output && <CodePane text={result.output} tone="error" />}
      </div>
    );
  }

  if (tool === "write") {
    const parsed = parseInput(use);
    const content = parsed?.content as string | undefined;
    return (
      <div className="space-y-2">
        <div className="flex items-center gap-2 rounded-md border border-border/60 bg-muted/20 px-3 py-1.5">
          <FilePlus className="size-3 shrink-0 text-muted-foreground" />
          <span className="font-mono text-xs text-foreground/90 break-all">
            {fileTarget(use)}
          </span>
        </div>
        {content && <CodePane text={content} maxHeight={260} />}
        {isError && result?.output && <CodePane text={result.output} tone="error" />}
      </div>
    );
  }

  const input = use.inputRaw || use.input || "";
  return (
    <div className="space-y-2">
      {input && <CodePane text={input} maxHeight={260} />}
      {result?.output && (
        <CodePane text={result.output} tone={isError ? "error" : "default"} />
      )}
    </div>
  );
}
