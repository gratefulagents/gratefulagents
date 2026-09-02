import { memo, useMemo, useState } from "react";
import { AlertTriangle, ArrowRight, Brain, Check, CheckCircle, ChevronRight, Cog, FileEdit, FilePlus, FileText, Loader2, Search, Terminal, Wrench, XCircle, Zap } from "lucide-react";

import { Collapse } from "./Collapse";
import { RowDetail } from "./DetailPanes";
import { useResolvedEntry } from "./detailContext";
import { entryIdentity, workUnitKey } from "./feedModel";
import type { WorkItem, WorkUnit } from "./types";
import { computeStats, liveVerb, statsSummary, workVerb } from "./workStats";
import { MarkdownViewer } from "@/components/MarkdownViewer";
import { formatDuration, shortPath } from "@/lib/activityGrouping";
import { bashCommand, fileTarget, firstLine, firstMeaningfulLine, formatClock, formatWall, genericTarget, searchPattern, systemLabel, wallSeconds } from "@/lib/activityLogFormat";
import { LiveDot } from "@/components/ui/live-dot";
import { toneSoft, toneText } from "@/lib/status";
import { cn } from "@/lib/utils";
import type { ActivityEntry } from "@/rpc/platform/service_pb";

const ROW_CLASS =
  "group flex w-full items-center gap-2 rounded-md px-2 py-1 text-left transition-colors";
const INTERACTIVE_ROW_CLASS = cn(
  ROW_CLASS,
  "cursor-pointer hover:bg-muted/50 focus-visible:outline-2 focus-visible:outline-ring focus-visible:outline-offset-2",
);
const DETAIL_CLASS = "mb-1.5 ml-5.5 mt-1";

function Chevron({ open, className }: { open: boolean; className?: string }) {
  return (
    <ChevronRight
      aria-hidden="true"
      className={cn(
        "size-3 shrink-0 text-muted-foreground/40 transition-transform group-hover:text-muted-foreground",
        open && "rotate-90",
        className,
      )}
    />
  );
}

export function rowPresentation(use: ActivityEntry): {
  Icon: typeof Terminal;
  verb: string;
  target: string;
} {
  if (use.type === "agent_spawn" || (use.type === "tool_use" && use.tool === "Agent")) {
    return {
      Icon: Zap,
      verb: use.subagentType || "Agent",
      target: use.subagentDescription || use.message || "",
    };
  }
  if (use.type === "tool_result") {
    return {
      Icon: use.isError ? XCircle : CheckCircle,
      verb: use.isError ? "Error" : "Result",
      target: firstLine(use.output || ""),
    };
  }
  if (use.type !== "tool_use") {
    return {
      Icon: Cog,
      verb: use.type,
      target: firstLine(use.message || use.input || ""),
    };
  }
  // Every tool call is shown under its actual name; the icon and target
  // extraction are cosmetic hints for well-known tools, with a generic
  // input summary as fallback so no call renders as an opaque blob.
  const name = use.tool || "tool";
  const t = name.toLowerCase();
  if (t === "bash" || t === "execute")
    return { Icon: Terminal, verb: name, target: firstMeaningfulLine(bashCommand(use)) };
  if (t === "read" || t === "read_file")
    return { Icon: FileText, verb: name, target: shortPath(fileTarget(use)) };
  if (t === "write")
    return { Icon: FilePlus, verb: name, target: shortPath(fileTarget(use)) };
  if (t === "edit")
    return { Icon: FileEdit, verb: name, target: shortPath(fileTarget(use)) };
  if (t === "grep" || t === "glob")
    return { Icon: Search, verb: name, target: searchPattern(use) };
  return { Icon: Wrench, verb: name, target: genericTarget(use) };
}

export function RowStatusIcon({
  use,
  result,
}: {
  use: ActivityEntry;
  result?: ActivityEntry;
}) {
  if (result?.isError || use.isError)
    return <XCircle className="size-3 shrink-0 text-destructive" />;
  return null;
}

function entryPanelId(entry: ActivityEntry): string {
  return `work-row-detail-${entryIdentity(entry).replace(/[^a-zA-Z0-9_-]/g, "-")}`;
}

export function WorkRowView({
  use,
  result,
}: {
  use: ActivityEntry;
  result?: ActivityEntry;
}) {
  const [open, setOpen] = useState(false);
  const { Icon, verb, target } = useMemo(() => rowPresentation(use), [use]);
  const isError = (result?.isError ?? false) || use.isError;
  const hasDetail = Boolean(
    use.inputRaw || use.input || result?.output || use.output,
  );
  const duration = result ? formatDuration(result.toolDurationMs) : "";
  const isMono = use.type === "tool_use" && use.tool !== "Agent";
  const panelId = hasDetail ? entryPanelId(use) : undefined;

  const content = (
    <>
      <Icon
        className={cn("size-3.5 shrink-0", isError ? "text-destructive" : "text-muted-foreground/70")}
      />
      <span
        className={cn("min-w-0 flex-1 truncate text-xs", isError ? "text-destructive" : "text-foreground/85")}
      >
        <span className="text-muted-foreground">
          {verb}
          {target ? " " : ""}
        </span>
        {target && <span className={isMono ? "font-mono" : ""}>{target}</span>}
      </span>
      <RowStatusIcon use={use} result={result} />
      {duration && (
        <span className="shrink-0 font-mono text-2xs tabular-nums text-muted-foreground">
          {duration}
        </span>
      )}
      {hasDetail && <Chevron open={open} />}
    </>
  );

  if (!hasDetail) {
    return (
      <div className={ROW_CLASS} title={formatClock(use.timestampUnix)}>
        {content}
      </div>
    );
  }

  return (
    <div>
      <button
        type="button"
        onClick={() => setOpen(!open)}
        aria-expanded={open}
        aria-controls={panelId}
        title={formatClock(use.timestampUnix)}
        className={INTERACTIVE_ROW_CLASS}
      >
        {content}
      </button>
      <Collapse open={open} id={panelId} className={DETAIL_CLASS}>
        <WorkRowDetail use={use} result={result} />
      </Collapse>
    </div>
  );
}

/**
 * Detail pane wrapper that lazily swaps in the full payloads when the server
 * sent truncated previews (input_truncated/output_truncated).
 */
function WorkRowDetail({
  use,
  result,
}: {
  use: ActivityEntry;
  result?: ActivityEntry;
}) {
  const resolvedUse = useResolvedEntry(use, true);
  const resolvedResult = useResolvedEntry(result, true);
  const loading = resolvedUse.loading || resolvedResult.loading;
  const failed = resolvedUse.failed || resolvedResult.failed;

  return (
    <div className="space-y-1.5">
      {loading && (
        <div className="flex items-center gap-1.5 text-2xs text-muted-foreground">
          <Loader2 className="size-3 animate-spin" />
          <span>Loading full payload…</span>
        </div>
      )}
      {failed && (
        <p className="text-2xs text-muted-foreground">
          Couldn't load the full payload — showing the truncated preview.
        </p>
      )}
      <RowDetail use={resolvedUse.entry ?? use} result={resolvedResult.entry ?? result} />
    </div>
  );
}

export function BatchRowView({ tool, entries }: { tool: string; entries: ActivityEntry[] }) {
  const [open, setOpen] = useState(false);
  const t = tool.toLowerCase();
  const isRead = t === "read" || t === "read_file";
  const Icon = isRead ? FileText : Search;
  const label = `${entries.length}× ${tool}`;

  return (
    <div>
      <button
        type="button"
        onClick={() => setOpen(!open)}
        aria-expanded={open}
        title={formatClock(entries[0].timestampUnix)}
        className={INTERACTIVE_ROW_CLASS}
      >
        <Icon className="size-3.5 shrink-0 text-muted-foreground/70" />
        <span className="min-w-0 flex-1 truncate text-xs text-foreground/85">
          {label}
        </span>
        <Chevron open={open} />
      </button>
      <Collapse open={open} className={cn(DETAIL_CLASS, "space-y-px")}>
        {entries.map((e) => (
          <div key={entryIdentity(e)} className="flex items-center gap-2 px-2 py-0.5 text-xs">
            <span className="size-1 shrink-0 rounded-full bg-muted-foreground/40" />
            <span className="font-mono text-muted-foreground break-all">
              {isRead ? shortPath(fileTarget(e)) : searchPattern(e)}
            </span>
          </div>
        ))}
      </Collapse>
    </div>
  );
}

export function ThinkingRowView({ entry }: { entry: ActivityEntry }) {
  const [open, setOpen] = useState(false);
  const text = entry.message || "";
  const preview = firstLine(text).slice(0, 140);

  return (
    <div>
      <button
        type="button"
        onClick={() => setOpen(!open)}
        aria-expanded={open}
        title={formatClock(entry.timestampUnix)}
        className={INTERACTIVE_ROW_CLASS}
      >
        <Brain className="size-3.5 shrink-0 text-muted-foreground/50" />
        <span className="min-w-0 flex-1 truncate text-xs italic text-muted-foreground/70">
          {preview}
        </span>
        <Chevron open={open} />
      </button>
      <Collapse
        open={open}
        className={cn(DETAIL_CLASS, "border-l-2 border-border/50 pl-3 text-xs leading-relaxed opacity-65")}
      >
        <MarkdownViewer content={text} />
      </Collapse>
    </div>
  );
}

export function SystemRowView({ entries }: { entries: ActivityEntry[] }) {
  const [open, setOpen] = useState(false);
  const breakdown = useMemo(() => {
    const counts = new Map<string, number>();
    for (const e of entries) {
      const label = e.type.replaceAll("_", " ");
      counts.set(label, (counts.get(label) || 0) + 1);
    }
    return [...counts.entries()]
      .map(([label, count]) => (count > 1 ? `${label} ×${count}` : label))
      .join(", ");
  }, [entries]);

  return (
    <div>
      <button
        type="button"
        onClick={() => setOpen(!open)}
        aria-expanded={open}
        title={formatClock(entries[0].timestampUnix)}
        className={INTERACTIVE_ROW_CLASS}
      >
        <Cog className="size-3.5 shrink-0 text-muted-foreground/40" />
        <span className="min-w-0 flex-1 truncate text-2xs text-muted-foreground/60">
          {entries.length} system event{entries.length !== 1 ? "s" : ""} —{" "}
          {breakdown}
        </span>
        <Chevron open={open} />
      </button>
      <Collapse open={open} className={cn(DETAIL_CLASS, "space-y-px")}>
        {entries.map((e) => (
          <div
            key={entryIdentity(e)}
            className="flex items-center gap-2 px-2 py-0.5 text-2xs text-muted-foreground"
            title={formatClock(e.timestampUnix)}
          >
            <span className="size-1 shrink-0 rounded-full bg-muted-foreground/40" />
            <span className="truncate">{systemLabel(e)}</span>
          </div>
        ))}
      </Collapse>
    </div>
  );
}

export function StepRowView({ entry }: { entry: ActivityEntry }) {
  const label = entry.step || entry.message || "Step";
  return (
    <div
      className="flex items-center gap-2 px-2 py-1"
      title={formatClock(entry.timestampUnix)}
    >
      <ArrowRight className={`size-3.5 shrink-0 ${toneText.warning}`} />
      <span className={`text-xs font-medium ${toneText.warning}`}>
        {label}
      </span>
    </div>
  );
}

export function WorkUnitView({ unit }: { unit: WorkUnit }) {
  switch (unit.kind) {
    case "row":
      return <WorkRowView use={unit.use} result={unit.result} />;
    case "batch":
      return <BatchRowView tool={unit.tool} entries={unit.entries} />;
    case "thinking":
      return <ThinkingRowView entry={unit.entry} />;
    case "system":
      return <SystemRowView entries={unit.entries} />;
    case "step":
      return <StepRowView entry={unit.entry} />;
  }
}

// ─── Work card ──────────────────────────────────────────────────────────────

function latestActionLine(entries: ActivityEntry[]): string {
  for (let i = entries.length - 1; i >= 0; i--) {
    const e = entries[i];
    if (e.type !== "tool_use") continue;
    const { verb, target } = rowPresentation(e);
    return target ? `${verb} ${target}` : verb;
  }
  return "";
}

export const WorkCard = memo(function WorkCard({ item, live }: { item: WorkItem; live: boolean }) {
  const [userOpen, setUserOpen] = useState<boolean | null>(null);
  const open = userOpen ?? false;
  const stats = useMemo(() => computeStats(item.entries), [item.entries]);
  const summary = statsSummary(stats);
  const duration = formatWall(wallSeconds(item.entries));
  const onlyReasoning = stats.toolTotal === 0 && stats.thoughts > 0;

  const liveLabel = live ? liveVerb(item.entries) : "";
  // A live work unit with no concrete action (e.g. only system bookkeeping
  // like system init) has nothing to show; the run header's "Preparing work…"
  // status already conveys this, so skip the redundant "Working…" card.
  if (live && liveLabel === "") {
    return null;
  }

  // Collapsed live cards still need to communicate progress: surface the
  // most recent concrete action (last tool target) as a one-line peek.
  const peek = live && !open ? latestActionLine(item.entries) : "";

  const title = live
    ? `${liveLabel}…`
    : onlyReasoning
      ? "Reasoned"
      : duration
        ? `${workVerb(stats)} for ${duration}`
        : workVerb(stats);

  return (
    <div className="overflow-hidden rounded-lg border border-border/50 bg-muted/[0.15]">
      <button
        type="button"
        onClick={() => setUserOpen(!open)}
        aria-expanded={open}
        title={formatClock(item.entries[0]?.timestampUnix ?? 0n)}
        className="group flex w-full flex-col gap-0.5 px-3 py-2 text-left transition-colors hover:bg-muted/40 cursor-pointer focus-visible:outline-2 focus-visible:outline-ring focus-visible:-outline-offset-2"
      >
        <span className="flex w-full items-center gap-2.5">
          {live ? (
            <span className="flex size-3.5 shrink-0 items-center justify-center">
              <LiveDot tone="running" pulse />
            </span>
          ) : stats.errors > 0 ? (
            <AlertTriangle className={`size-3.5 shrink-0 ${toneText.warning}`} />
          ) : onlyReasoning ? (
            <Brain className="size-3.5 shrink-0 text-muted-foreground/60" />
          ) : (
            <Check className={`size-3.5 shrink-0 ${toneText.success}`} />
          )}
          <span className="shrink-0 text-xs font-medium text-foreground/90">
            {title}
          </span>
          {summary && !onlyReasoning && (
            <span className="min-w-0 truncate text-xs text-muted-foreground">
              {summary}
            </span>
          )}
          {stats.errors > 0 && (
            <span
              className={`shrink-0 rounded-sm px-1.5 py-px text-3xs font-medium ${toneSoft.danger}`}
            >
              {stats.errors} error{stats.errors !== 1 ? "s" : ""}
            </span>
          )}
          <span className="flex-1" />
          {!live && duration && !onlyReasoning && (
            <span className="shrink-0 font-mono text-2xs tabular-nums text-muted-foreground">
              {duration}
            </span>
          )}
          <Chevron open={open} className="size-3.5 text-muted-foreground/50" />
        </span>
        {peek && (
          <span className="pl-6 text-2xs text-muted-foreground/80 line-clamp-1" title={peek}>
            {peek}
          </span>
        )}
      </button>
      <Collapse open={open} className="space-y-px border-t border-border/40 px-1.5 py-1.5">
        {item.units.map((unit, i) => (
          <WorkUnitView key={workUnitKey(unit, i)} unit={unit} />
        ))}
      </Collapse>
    </div>
  );
});
