import { memo, useMemo, useState } from "react";
import { AlertTriangle, ArrowRight, Brain, Check, CheckCircle, ChevronDown, ChevronRight, Cog, FileEdit, FilePlus, FileText, Loader2, Search, Terminal, Wrench, XCircle, Zap } from "lucide-react";

import { RowDetail } from "./DetailPanes";
import { useResolvedEntry } from "./detailContext";
import { entryIdentity, workUnitKey } from "./feedModel";
import type { WorkItem, WorkUnit } from "./types";
import { computeStats, liveVerb, statsSummary, workVerb } from "./workStats";
import { MarkdownViewer } from "@/components/MarkdownViewer";
import { formatDuration, shortPath } from "@/lib/activityGrouping";
import { bashCommand, fileTarget, firstLine, firstMeaningfulLine, formatClock, formatWall, genericTarget, searchPattern, systemLabel, wallSeconds } from "@/lib/activityLogFormat";
import { toneSoft, toneText } from "@/lib/status";
import type { ActivityEntry } from "@/rpc/platform/service_pb";

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

  return (
    <div>
      <button
        type="button"
        onClick={() => hasDetail && setOpen(!open)}
        aria-expanded={hasDetail ? open : undefined}
        aria-controls={panelId}
        title={formatClock(use.timestampUnix)}
        className={`group flex w-full items-center gap-2 rounded-md px-2 py-1 text-left transition-colors ${
          hasDetail ? "cursor-pointer hover:bg-muted/50" : "cursor-default"
        }`}
      >
        <Icon
          className={`size-3.5 shrink-0 ${
            isError ? "text-destructive" : "text-muted-foreground/70"
          }`}
        />
        <span
          className={`min-w-0 flex-1 truncate text-xs ${
            isError ? "text-destructive" : "text-foreground/85"
          }`}
        >
          <span className="text-muted-foreground">
            {verb}
            {target ? " " : ""}
          </span>
          {target && <span className={isMono ? "font-mono" : ""}>{target}</span>}
        </span>
        <RowStatusIcon use={use} result={result} />
        {duration && (
          <span className="shrink-0 font-mono text-[11px] tabular-nums text-muted-foreground">
            {duration}
          </span>
        )}
        {hasDetail && (
          <ChevronRight
            className={`size-3 shrink-0 text-muted-foreground/40 transition-transform group-hover:text-muted-foreground ${
              open ? "rotate-90" : ""
            }`}
          />
        )}
      </button>
      {open && hasDetail && (
        <div id={panelId} className="mb-1.5 ml-[1.4rem] mt-1">
          <WorkRowDetail use={use} result={result} />
        </div>
      )}
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
        <div className="flex items-center gap-1.5 text-[11px] text-muted-foreground">
          <Loader2 className="size-3 animate-spin" />
          <span>Loading full payload…</span>
        </div>
      )}
      {failed && (
        <p className="text-[11px] text-muted-foreground">
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
        className="group flex w-full items-center gap-2 rounded-md px-2 py-1 text-left transition-colors hover:bg-muted/50 cursor-pointer"
      >
        <Icon className="size-3.5 shrink-0 text-muted-foreground/70" />
        <span className="min-w-0 flex-1 truncate text-xs text-foreground/85">
          {label}
        </span>
        <ChevronRight
          className={`size-3 shrink-0 text-muted-foreground/40 transition-transform group-hover:text-muted-foreground ${
            open ? "rotate-90" : ""
          }`}
        />
      </button>
      {open && (
        <div className="mb-1.5 ml-[1.4rem] mt-1 space-y-px">
          {entries.map((e) => (
            <div key={entryIdentity(e)} className="flex items-center gap-2 px-2 py-0.5 text-xs">
              <span className="size-1 shrink-0 rounded-full bg-muted-foreground/40" />
              <span className="font-mono text-muted-foreground break-all">
                {isRead ? shortPath(fileTarget(e)) : searchPattern(e)}
              </span>
            </div>
          ))}
        </div>
      )}
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
        className="group flex w-full items-center gap-2 rounded-md px-2 py-1 text-left transition-colors hover:bg-muted/50 cursor-pointer"
      >
        <Brain className="size-3.5 shrink-0 text-muted-foreground/50" />
        <span className="min-w-0 flex-1 truncate text-xs italic text-muted-foreground/70">
          {preview}
        </span>
        <ChevronRight
          className={`size-3 shrink-0 text-muted-foreground/40 transition-transform group-hover:text-muted-foreground ${
            open ? "rotate-90" : ""
          }`}
        />
      </button>
      {open && (
        <div className="mb-1.5 ml-[1.4rem] mt-1 border-l-2 border-border/50 pl-3 text-xs leading-relaxed opacity-65">
          <MarkdownViewer content={text} />
        </div>
      )}
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
        className="group flex w-full items-center gap-2 rounded-md px-2 py-1 text-left transition-colors hover:bg-muted/50 cursor-pointer"
      >
        <Cog className="size-3.5 shrink-0 text-muted-foreground/40" />
        <span className="min-w-0 flex-1 truncate text-[11px] text-muted-foreground/60">
          {entries.length} system event{entries.length !== 1 ? "s" : ""} —{" "}
          {breakdown}
        </span>
        <ChevronRight
          className={`size-3 shrink-0 text-muted-foreground/40 transition-transform group-hover:text-muted-foreground ${
            open ? "rotate-90" : ""
          }`}
        />
      </button>
      {open && (
        <div className="mb-1.5 ml-[1.4rem] mt-1 space-y-px">
          {entries.map((e) => (
            <div
              key={entryIdentity(e)}
              className="flex items-center gap-2 px-2 py-0.5 text-[11px] text-muted-foreground"
              title={formatClock(e.timestampUnix)}
            >
              <span className="size-1 shrink-0 rounded-full bg-muted-foreground/40" />
              <span className="truncate">{systemLabel(e)}</span>
            </div>
          ))}
        </div>
      )}
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
        className="flex w-full items-center gap-2.5 px-3 py-2 text-left transition-colors hover:bg-muted/40 cursor-pointer"
      >
        {live ? (
          <Loader2 className={`size-3.5 shrink-0 animate-spin ${toneText.running}`} />
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
            className={`shrink-0 rounded-[4px] px-1.5 py-px text-[10px] font-medium ${toneSoft.danger}`}
          >
            {stats.errors} error{stats.errors !== 1 ? "s" : ""}
          </span>
        )}
        <span className="flex-1" />
        {!live && duration && !onlyReasoning && (
          <span className="shrink-0 font-mono text-[11px] tabular-nums text-muted-foreground">
            {duration}
          </span>
        )}
        <ChevronDown
          className={`size-3.5 shrink-0 text-muted-foreground/50 transition-transform ${
            open ? "rotate-180" : ""
          }`}
        />
      </button>
      {open && (
        <div className="space-y-px border-t border-border/40 px-1.5 py-1.5">
          {item.units.map((unit, i) => (
            <WorkUnitView key={workUnitKey(unit, i)} unit={unit} />
          ))}
        </div>
      )}
    </div>
  );
});
