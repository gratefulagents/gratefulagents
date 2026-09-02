import { useMemo, useState } from "react";
import { create } from "@bufbuild/protobuf";
import { Check, ChevronRight, CornerDownRight, GitFork, Maximize2 } from "lucide-react";

import { AgentTypeChip } from "@/components/ui/agent-type-chip";
import { LiveDot } from "@/components/ui/live-dot";
import { useSubagentContext } from "./subagentContext";
import { assignOrdinals, buildLayout, isWaitingStatus, type LayoutDims } from "@/lib/subagentGraphLayout";
import { cleanSubagentDescription,
  formatDuration,
  formatTokens,
  subagentTitleFromPrompt,
  type ActivityGroup,
} from "@/lib/activityGrouping";
import { formatUsd } from "@/lib/activityLogFormat";
import { toneText } from "@/lib/status";
import { cn } from "@/lib/utils";
import {
  SubagentGraphSchema,
  SubagentGraphEdgeSchema,
  SubagentGraphNodeSchema,
} from "@/rpc/platform/service_pb";
import { Collapse } from "./Collapse";
import { isLiveSubagentStatus, SubagentCard, SubagentStatusIcon, subagentLiveLine } from "./SubagentCards";

type SubagentGroup = Extract<ActivityGroup, { kind: "subagent" }>;

/** Compact geometry retained for estimating dependency-aware wall time. */
const MINI: LayoutDims = { nodeW: 208, nodeH: 56, hGap: 44, vGap: 12 };

function groupDependsOn(group: SubagentGroup): string[] {
  for (let i = group.entries.length - 1; i >= 0; i--) {
    const deps = group.entries[i].subagentDependsOn;
    if (deps && deps.length > 0) return deps;
  }
  return [];
}

function groupTitle(group: SubagentGroup): string {
  return (
    cleanSubagentDescription(group.subagentDescription) ||
    subagentTitleFromPrompt(group.subagentPrompt) ||
    (group.taskId ? `Task ${group.taskId.replace(/^task_/, "").slice(0, 8)}` : "task")
  );
}

function isGroupRunning(group: SubagentGroup): boolean {
  return isLiveSubagentStatus(group.subagentStatus);
}

function isGroupWaiting(group: SubagentGroup): boolean {
  return isWaitingStatus(group.subagentStatus);
}

function isGroupStopped(group: SubagentGroup): boolean {
  return (
    group.subagentStatus === "stopped" ||
    group.subagentStatus === "cancelled" ||
    group.subagentStatus === "canceled"
  );
}

function groupId(group: SubagentGroup, index: number): string {
  return group.taskId || `idx-${index}`;
}

/**
 * Historical summary for one delegation burst. The live DAG now stays pinned
 * above the composer; this transcript card expands into a compact task roster
 * with each task selectable for its full details.
 */
export function SubagentDagCard({
  groups,
  waves,
  onOpenGraph,
}: {
  groups: SubagentGroup[];
  /** Topological wave (dependency depth) per group, aligned with `groups`. */
  waves?: number[];
  /** Jump to the full graph tab; the `#n` chips become links when provided. */
  onOpenGraph?: () => void;
}) {
  // The live DAG is pinned above the composer; transcript delegations stay
  // compact and provide a historical task roster on demand.
  const [open, setOpen] = useState(false);
  const [selectedId, setSelectedId] = useState<string | null>(null);

  const byId = useMemo(() => {
    const m = new Map<string, SubagentGroup>();
    groups.forEach((g, i) => m.set(g.taskId || `idx-${i}`, g));
    return m;
  }, [groups]);

  const titleByTaskId = useMemo(() => {
    const m = new Map<string, string>();
    for (const g of groups) {
      if (g.taskId) m.set(g.taskId, groupTitle(g));
    }
    return m;
  }, [groups]);

  const waveOf = waves && waves.length === groups.length ? waves : groups.map(() => 0);

  // Synthesize a SubagentGraph for the burst (no root node — the feed row
  // itself is the parent) and lay it out with the graph tab's engine.
  const layout = useMemo(() => {
    const ids = new Map<SubagentGroup, string>();
    groups.forEach((g, i) => ids.set(g, g.taskId || `idx-${i}`));
    const graph = create(SubagentGraphSchema, {
      nodes: groups.map((g, i) =>
        create(SubagentGraphNodeSchema, {
          id: ids.get(g)!,
          kind: "subagent",
          label: groupTitle(g),
          subtitle: g.subagentType,
          status: g.subagentStatus,
          durationMs: g.durationMs,
          model: g.subagentModel,
          timestampUnix: g.entries[0]?.timestampUnix ?? BigInt(i),
          dependsOn: groupDependsOn(g),
          waitingOn:
            g.entries.findLast((e) => e.subagentStatus)?.subagentWaitingOn ?? [],
        }),
      ),
      edges: groups.flatMap((g) =>
        groupDependsOn(g)
          .filter((dep) => groups.some((o) => (o.taskId || "") === dep))
          .map((dep) =>
            create(SubagentGraphEdgeSchema, {
              id: `${dep}=>${ids.get(g)}`,
              from: dep,
              to: ids.get(g)!,
              kind: "depends-on",
            }),
          ),
      ),
    });
    return buildLayout(graph, MINI);
  }, [groups]);

  // Ordinals (#n) come from the shared layout numbering so the dock, this
  // card and the graph tab all mean the same task by "#3". Rows render in
  // ordinal order; ids are task ids (or `idx-i` for tasks without one).
  // Run-wide numbers win when the graph knows this task; the burst-local
  // layout numbering is the fallback for logs without a graph.
  const shared = useSubagentContext();
  const openGraph = onOpenGraph ?? shared.onOpenGraph;
  const ordinalById = useMemo(() => {
    const local = assignOrdinals(layout);
    const merged = new Map<string, number>();
    for (const [id, ordinal] of local) merged.set(id, shared.ordinalByTaskId.get(id) ?? ordinal);
    return merged;
  }, [layout, shared.ordinalByTaskId]);
  const rowOrder = useMemo(
    () =>
      groups
        .map((group, index) => ({ group, index, ordinal: ordinalById.get(groupId(group, index)) ?? index + 1 }))
        .sort((a, b) => a.ordinal - b.ordinal),
    [groups, ordinalById],
  );

  const running = groups.filter(isGroupRunning).length;
  const waiting = groups.filter(isGroupWaiting).length;
  const failed = groups.filter((g) => g.subagentStatus === "failed").length;
  const stopped = groups.filter(isGroupStopped).length;
  const completed = groups.length - running - waiting - failed - stopped;

  const totalTokens = groups.reduce((a, g) => a + Number(g.totalTokens || 0n), 0);
  const knownCost = groups.filter((g) => g.subagentCostKnown);
  const totalCost = knownCost.reduce((a, g) => a + (g.subagentCostUsd || 0), 0);
  // Wall-clock estimate: columns overlap in time, so sum each column's slowest.
  const colMax = new Map<number, number>();
  for (const ln of layout.order) {
    colMax.set(ln.depth, Math.max(colMax.get(ln.depth) ?? 0, Number(ln.node.durationMs || 0n)));
  }
  const wallMs = [...colMax.values()].reduce((a, b) => a + b, 0);

  const summary: string[] = [];
  if (totalTokens > 0) summary.push(`${formatTokens(BigInt(totalTokens))} tok`);
  if (wallMs > 0 && running === 0 && waiting === 0) summary.push(`${formatDuration(BigInt(wallMs))} wall`);
  if (knownCost.length > 0) summary.push(formatUsd(totalCost));

  const selected = selectedId ? byId.get(selectedId) : undefined;
  const finished = groups.length - running - waiting;
  const inFlight = running > 0 || waiting > 0;

  return (
    <div className="overflow-hidden rounded-lg border border-border/60 bg-muted/10">
      <button
        type="button"
        onClick={() => setOpen(!open)}
        aria-expanded={open}
        className="flex w-full items-center gap-2.5 px-3 py-2 text-left transition-colors hover:bg-muted/30 cursor-pointer focus-visible:outline-2 focus-visible:outline-ring focus-visible:-outline-offset-2"
      >
        <GitFork className="size-3.5 shrink-0 rotate-90 text-muted-foreground" />
        <span className="min-w-0 flex-1">
          <span className="flex flex-wrap items-center gap-x-2 gap-y-1 text-xs">
            <span className="font-medium text-foreground/90">Delegated {groups.length} tasks</span>
            {running > 0 && (
              <span className={`inline-flex items-center gap-1 ${toneText.running}`}>
                <LiveDot tone="running" pulse size="xs" />
                {running} running
              </span>
            )}
            {waiting > 0 && <span className={toneText.warning}>{waiting} waiting</span>}
            {completed > 0 && (
              <span className={toneText.success}>{completed} completed</span>
            )}
            {failed > 0 && <span className={toneText.danger}>{failed} failed</span>}
            {stopped > 0 && <span className={toneText.warning}>{stopped} stopped</span>}
            {completed === groups.length && (
              <Check className={`size-3 ${toneText.success}`} aria-label="All tasks completed" />
            )}
            {summary.length > 0 && (
              <span className="font-mono text-3xs tabular-nums text-muted-foreground/60">
                {summary.join(" · ")}
              </span>
            )}
          </span>
        </span>
        <ChevronRight
          aria-hidden="true"
          className={cn(
            "size-3.5 shrink-0 text-muted-foreground/50 transition-transform",
            open && "rotate-90",
          )}
        />
      </button>

      {inFlight && (
        <div
          className="h-0.5 w-full bg-muted/40"
          role="progressbar"
          aria-valuemin={0}
          aria-valuemax={groups.length}
          aria-valuenow={finished}
          aria-label={`${finished} of ${groups.length} tasks finished`}
        >
          <div
            className="h-full bg-[color:var(--color-primary)]/70 transition-[width] duration-500"
            style={{ width: `${Math.round((finished / Math.max(groups.length, 1)) * 100)}%` }}
          />
        </div>
      )}

      <Collapse open={open} className="border-t border-border/40">
        <div className="max-h-72 overflow-y-auto p-1.5">
          {rowOrder.map(({ group, index, ordinal }) => {
            const id = groupId(group, index);
            const deps = groupDependsOn(group)
              .filter((dep) => ordinalById.has(dep))
              .map((dep) => ({
                ordinal: ordinalById.get(dep)!,
                title: titleByTaskId.get(dep) ?? dep,
              }))
              .sort((a, b) => a.ordinal - b.ordinal);
            return (
              <RosterRow
                key={id}
                group={group}
                ordinal={ordinal}
                wave={waveOf[index] ?? 0}
                deps={deps}
                selected={selectedId === id}
                onSelect={() => setSelectedId((cur) => (cur === id ? null : id))}
                onOpenGraph={openGraph}
              />
            );
          })}
        </div>

        {selected && (
          <div className="border-t border-border/40 px-3 py-2.5">
            <SubagentCard group={selected} />
          </div>
        )}
      </Collapse>
    </div>
  );
}

function RosterRow({
  group,
  ordinal,
  wave,
  deps,
  selected,
  onSelect,
  onOpenGraph,
}: {
  group: SubagentGroup;
  ordinal: number;
  wave: number;
  deps: Array<{ ordinal: number; title: string }>;
  selected: boolean;
  onSelect: () => void;
  onOpenGraph?: () => void;
}) {
  const running = isGroupRunning(group);
  const waiting = isGroupWaiting(group);
  const stopped = isGroupStopped(group);
  const statusText = running
    ? "running"
    : waiting
      ? "waiting"
      : group.subagentStatus === "failed"
        ? "failed"
        : stopped
          ? "stopped"
          : group.durationMs > 0n
            ? formatDuration(group.durationMs)
            : "completed";
  const liveLine = running ? subagentLiveLine(group.entries) : "";

  const metrics: string[] = [];
  if (group.totalTokens > 0n) metrics.push(`${formatTokens(group.totalTokens)} tok`);
  if (group.subagentCostKnown) metrics.push(formatUsd(group.subagentCostUsd));

  const ordinalClass =
    "shrink-0 font-mono text-3xs font-semibold tabular-nums text-muted-foreground/80";

  return (
    <div
      data-testid="subagent-roster-row"
      title={`#${ordinal} ${groupTitle(group)}`}
      className={cn(
        "flex w-full items-center gap-2 rounded-md px-2 py-1.5 transition-colors",
        selected ? "bg-muted/60" : "hover:bg-muted/40",
      )}
      style={wave > 0 ? { paddingLeft: `${8 + Math.min(wave, 4) * 16}px` } : undefined}
    >
      {wave > 0 && (
        <CornerDownRight
          className="size-3 shrink-0 text-muted-foreground/50"
          aria-hidden="true"
        />
      )}
      <SubagentStatusIcon status={group.subagentStatus} />
      {onOpenGraph ? (
        <button
          type="button"
          onClick={onOpenGraph}
          aria-label={`Open task #${ordinal} in graph`}
          title="Open in graph"
          className={cn(
            ordinalClass,
            "inline-flex min-h-6 items-center gap-0.5 rounded-sm px-0.5 hover:text-foreground focus-visible:outline-2 focus-visible:outline-ring focus-visible:outline-offset-2",
          )}
        >
          #{ordinal}
          <Maximize2 className="size-2.5" aria-hidden="true" />
        </button>
      ) : (
        <span className={ordinalClass}>#{ordinal}</span>
      )}
      {group.subagentType && <AgentTypeChip type={group.subagentType} className="shrink-0" />}
      <button
        type="button"
        onClick={onSelect}
        aria-pressed={selected}
        className="flex min-h-6 min-w-0 flex-1 items-center gap-2 rounded-sm text-left focus-visible:outline-2 focus-visible:outline-ring focus-visible:outline-offset-2"
      >
        <span className="min-w-0 flex-1 truncate text-2xs text-foreground/85">
          {groupTitle(group)}
          {liveLine && (
            <span className="ml-1.5 text-3xs text-muted-foreground/80">· {liveLine}</span>
          )}
          {deps.length > 0 && (
            <span
              className={`ml-1.5 font-mono text-3xs tabular-nums ${
                waiting ? toneText.warning : "text-muted-foreground/60"
              }`}
              title={`Runs after: ${deps.map((d) => `#${d.ordinal} ${d.title}`).join(" · ")}`}
            >
              · after {deps.map((d) => `#${d.ordinal}`).join(", ")}
            </span>
          )}
        </span>
        {metrics.length > 0 && (
          <span className="hidden shrink-0 font-mono text-3xs tabular-nums text-muted-foreground/60 sm:inline">
            {metrics.join(" · ")}
          </span>
        )}
        {group.subagentModel && (
          <span
            className="hidden max-w-28 shrink-0 truncate font-mono text-3xs text-muted-foreground/70 md:inline"
            title={group.subagentModel}
          >
            {group.subagentModel}
          </span>
        )}
        <span
          className={`min-w-14 shrink-0 text-right font-mono text-3xs tabular-nums ${
            group.subagentStatus === "failed"
              ? toneText.danger
              : waiting || stopped
                ? toneText.warning
                : running
                  ? toneText.running
                  : "text-muted-foreground/70"
          }`}
        >
          {statusText}
        </span>
        <ChevronRight
          aria-hidden="true"
          className={cn(
            "size-3 shrink-0 text-muted-foreground/40 transition-transform",
            selected && "rotate-90",
          )}
        />
      </button>
    </div>
  );
}
