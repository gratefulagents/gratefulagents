import { useEffect, useId, useMemo, useState } from "react";
import {
  AlertTriangle,
  Check,
  ChevronRight,
  Clock3,
  CornerDownRight,
  GitFork,
  Maximize2,
  XCircle,
} from "lucide-react";

import { AgentTypeChip } from "@/components/ui/agent-type-chip";
import { LiveDot } from "@/components/ui/live-dot";
import { useNow } from "@/hooks/useNow";
import { formatDuration, formatTokens } from "@/lib/activityGrouping";
import { getSubagentColor } from "@/lib/subagentColors";
import {
  assignOrdinals,
  buildLayout,
  isRunningSubagentNode,
  isWaitingStatus,
  taskIDToNodeID,
  type LayoutDims,
} from "@/lib/subagentGraphLayout";
import { toneText } from "@/lib/status";
import { cn } from "@/lib/utils";
import type { SubagentGraph, SubagentGraphNode } from "@/rpc/platform/service_pb";

const STOPPED_STATUSES = new Set(["stopped", "cancelled", "canceled"]);
const DOCK_EXPANDED_KEY = "gratefulagents.subagentDockExpanded";
/** How long the dock stays visible after the last agent finishes. */
export const DOCK_LINGER_MS = 1_200;
const FOCUS_RING =
  "focus-visible:outline-2 focus-visible:outline-ring focus-visible:outline-offset-2";

/** Geometry only feeds the layout pass we reuse for dependency depths. */
const WAVE_DIMS: LayoutDims = { nodeW: 220, nodeH: 64, hGap: 44, vGap: 12 };

type NodeState = "running" | "waiting" | "completed" | "failed" | "stopped";

function nodeState(node: SubagentGraphNode): NodeState {
  if (isRunningSubagentNode(node)) {
    return isWaitingStatus(node.status) || node.waitingOn.length > 0
      ? "waiting"
      : "running";
  }
  const status = node.status.toLowerCase();
  if (status === "failed") return "failed";
  if (STOPPED_STATUSES.has(status)) return "stopped";
  // A duration is authoritative completion evidence even if status delivery is stale.
  return "completed";
}

function agentType(node: SubagentGraphNode): string {
  return node.subtitle || (node.kind === "inline-subagent" ? "inline" : "agent");
}

/** One task's place in the roster: a stable ordinal plus exact dependencies. */
interface RosterEntry {
  node: SubagentGraphNode;
  state: NodeState;
  /** 1-based number shown as #n, assigned in visual (wave) order. */
  ordinal: number;
  /** Ordinals of the tasks this one runs after, e.g. [1, 3] → "after #1, #3". */
  dependsOn: number[];
  /** Labels of those dependency tasks, for the hover tooltip. */
  dependsOnLabels: string[];
  /** Ordinals of the dependencies that are still unfinished (gating this task). */
  waitingRefs: number[];
}

function formatRefs(ordinals: number[]): string {
  return ordinals.map((n) => `#${n}`).join(", ");
}

function currentActivity(entry: RosterEntry): string {
  const { node, state, waitingRefs, dependsOn } = entry;
  if (state === "waiting") {
    const refs = waitingRefs.length > 0 ? waitingRefs : dependsOn;
    if (refs.length > 0) return `Waiting on ${formatRefs(refs)}`;
    return node.waitingOn.length > 0
      ? `Waiting on ${node.waitingOn.length} task${node.waitingOn.length === 1 ? "" : "s"}`
      : "Waiting to start";
  }
  if (state === "completed") return "Completed";
  if (state === "failed") return "Failed";
  if (state === "stopped") return "Stopped";
  if (node.currentStep) return node.currentStep;
  if (node.lastTool) return `Using ${node.lastTool}`;
  if (node.description && node.description !== node.label) return node.description;
  return "Running…";
}

function NodeStatusIcon({ state }: { state: NodeState }) {
  if (state === "waiting") {
    return <Clock3 className={cn("size-3.5 shrink-0", toneText.warning)} aria-hidden="true" />;
  }
  if (state === "running") {
    return (
      <span className="flex size-3.5 shrink-0 items-center justify-center">
        <LiveDot tone="running" pulse />
      </span>
    );
  }
  if (state === "failed") {
    return <XCircle className={cn("size-3.5 shrink-0", toneText.danger)} aria-hidden="true" />;
  }
  if (state === "stopped") {
    return (
      <AlertTriangle className={cn("size-3.5 shrink-0", toneText.warning)} aria-hidden="true" />
    );
  }
  return <Check className={cn("size-3.5 shrink-0", toneText.success)} aria-hidden="true" />;
}

/**
 * Build the numbered roster: tasks bucketed into dependency waves, each task
 * carrying a stable #ordinal (assigned top-to-bottom through the waves) and
 * the exact ordinals of the tasks it runs after. Ordinals make dependencies
 * unambiguous even when batch delegations share an identical prompt prefix.
 */
function buildWaves(graph: SubagentGraph): RosterEntry[][] {
  const layout = buildLayout(graph, WAVE_DIMS);
  // Shared numbering with the transcript DAG card and the graph tab.
  const ordinalById = assignOrdinals(layout);

  // Bucket by dependency depth so parallel tasks share a wave, ordered by
  // ordinal within each wave.
  const buckets = new Map<number, SubagentGraphNode[]>();
  for (const laid of layout.order) {
    if (!ordinalById.has(laid.node.id)) continue;
    const bucket = buckets.get(laid.depth);
    if (bucket) bucket.push(laid.node);
    else buckets.set(laid.depth, [laid.node]);
  }
  const nodeWaves = [...buckets.entries()]
    .sort((a, b) => a[0] - b[0])
    .map(([, nodes]) => nodes.sort((a, b) => ordinalById.get(a.id)! - ordinalById.get(b.id)!));

  // Exact dependencies: depends-on edges, plus live waiting-on task ids.
  const depIdsById = new Map<string, Set<string>>();
  const addDep = (to: string, from: string) => {
    if (!ordinalById.has(to) || !ordinalById.has(from) || to === from) return;
    const set = depIdsById.get(to);
    if (set) set.add(from);
    else depIdsById.set(to, new Set([from]));
  };
  for (const edge of graph.edges) {
    if (edge.kind === "depends-on") addDep(edge.to, edge.from);
  }
  const labelById = new Map(graph.nodes.map((node) => [node.id, node.label]));
  const resolveWaitingId = (taskId: string) =>
    ordinalById.has(taskId) ? taskId : taskIDToNodeID(taskId);
  for (const node of graph.nodes) {
    for (const taskId of node.waitingOn) addDep(node.id, resolveWaitingId(taskId));
  }

  return nodeWaves.map((wave) =>
    wave.map((node) => {
      const depIds = [...(depIdsById.get(node.id) ?? [])].sort(
        (a, b) => ordinalById.get(a)! - ordinalById.get(b)!,
      );
      const waitingRefs = [
        ...new Set(
          node.waitingOn
            .map((taskId) => ordinalById.get(resolveWaitingId(taskId)))
            .filter((ordinal): ordinal is number => ordinal !== undefined),
        ),
      ].sort((a, b) => a - b);
      return {
        node,
        state: nodeState(node),
        ordinal: ordinalById.get(node.id)!,
        dependsOn: depIds.map((id) => ordinalById.get(id)!),
        dependsOnLabels: depIds.map((id) => labelById.get(id) ?? id),
        waitingRefs,
      } satisfies RosterEntry;
    }),
  );
}

/**
 * A pinned, compact rendering of the complete subagent DAG. It stays next to
 * the composer while work is active, so users do not need to scroll back to a
 * delegation event to understand completed, running, and waiting branches.
 *
 * The expanded body groups tasks by dependency wave: each wave is a responsive
 * grid of numbered status cards (#1, #2, …), and every dependent task states
 * exactly which tasks it runs after ("after #1, #3") so the execution order is
 * unambiguous even when sibling tasks share near-identical titles.
 * Collapsed by default — the summary row stays visible and the roster is
 * revealed on demand (the choice persists across runs).
 */
export function ActiveSubagentsDock({
  graph,
  onOpenGraph,
}: {
  graph?: SubagentGraph;
  onOpenGraph?: () => void;
}) {
  const [expanded, setExpanded] = useState(() => {
    try {
      return localStorage.getItem(DOCK_EXPANDED_KEY) === "true";
    } catch {
      return false;
    }
  });
  const toggleExpanded = () =>
    setExpanded((value) => {
      const next = !value;
      try {
        localStorage.setItem(DOCK_EXPANDED_KEY, String(next));
      } catch {
        // Ignore storage failures (private mode, etc.) — session-only state.
      }
      return next;
    });
  const rosterId = useId();
  const now = useNow(1_000);

  const waves = useMemo(() => {
    const subagents = graph?.nodes.filter((node) => node.kind !== "root") ?? [];
    if (!graph || subagents.length === 0) return [] as RosterEntry[][];
    const ids = new Set(subagents.map((node) => node.id));
    return buildWaves({
      ...graph,
      rootId: "",
      nodes: subagents,
      edges: graph.edges.filter((edge) => ids.has(edge.from) && ids.has(edge.to)),
    });
  }, [graph]);
  const roster = useMemo(() => waves.flat(), [waves]);
  const active = roster.filter(({ state }) => state === "running" || state === "waiting");
  const hasActive = active.length > 0;

  // Linger briefly after the last agent finishes so the completion registers
  // instead of the dock vanishing mid-glance: active → finished (1.2s) → gone.
  const [wasActive, setWasActive] = useState(hasActive);
  const [finishedVisible, setFinishedVisible] = useState(false);
  if (hasActive !== wasActive) {
    setWasActive(hasActive);
    setFinishedVisible(!hasActive);
  }
  useEffect(() => {
    if (!finishedVisible) return;
    const timer = setTimeout(() => setFinishedVisible(false), DOCK_LINGER_MS);
    return () => clearTimeout(timer);
  }, [finishedVisible]);

  // Keep the complete roster pinned while any delegated work is live. Once all
  // tasks are terminal it remains available in the transcript and Graph tab.
  if (!hasActive && !finishedVisible) return null;

  if (!hasActive) {
    return (
      <section
        className="shrink-0 border-t border-border/70 bg-card/35"
        aria-label="Active delegated agents"
      >
        <div className="flex min-h-9 items-center gap-2 px-3 text-xs text-muted-foreground md:px-4">
          <LiveDot tone="success" />
          <span role="status">All agents finished</span>
        </div>
      </section>
    );
  }

  const count = (state: NodeState) =>
    roster.filter((entry) => entry.state === state).length;
  const running = count("running");
  const waiting = count("waiting");
  const completed = count("completed");
  const failed = count("failed");
  const stopped = count("stopped");

  // While collapsed the roster is hidden, so surface the most informative
  // live line (a running node's current step) directly in the summary row.
  const livePreviewEntry = expanded
    ? undefined
    : roster.find(
        ({ node, state }) => state === "running" && (node.currentStep || node.lastTool),
      ) ?? roster.find(({ state }) => state === "running");
  const livePreview = livePreviewEntry
    ? `${agentType(livePreviewEntry.node)} · ${currentActivity(livePreviewEntry)}`
    : "";

  return (
    <section
      className="shrink-0 border-t border-border/70 bg-card/35"
      aria-label="Active delegated agents"
    >
      <span className="sr-only" role="status" aria-live="polite" aria-atomic="true">
        {active.length} delegated agent{active.length === 1 ? " is" : "s are"} active. {running}{" "}
        running.{waiting > 0 ? ` ${waiting} waiting.` : ""}
      </span>
      <div className="flex min-h-9 items-center gap-1 px-3 md:px-4">
        <button
          type="button"
          className={cn("flex min-w-0 flex-1 items-center gap-2 rounded-sm py-1.5 text-left", FOCUS_RING)}
          aria-expanded={expanded}
          aria-controls={expanded ? rosterId : undefined}
          aria-label={`${active.length} active agent${active.length === 1 ? "" : "s"}; ${roster.length} delegated task${roster.length === 1 ? "" : "s"}`}
          onClick={toggleExpanded}
        >
          <GitFork className="size-3.5 shrink-0 rotate-90 text-muted-foreground" />
          <span className="shrink-0 text-xs font-medium text-foreground">
            Delegated {roster.length} task{roster.length === 1 ? "" : "s"}
          </span>
          <span className="hidden min-w-0 items-center gap-2 overflow-hidden text-2xs sm:flex">
            {running > 0 && (
              <span className={cn("inline-flex shrink-0 items-center gap-1", toneText.running)}>
                <LiveDot tone="running" pulse size="xs" />
                {running} running
              </span>
            )}
            {waiting > 0 && (
              <span className={cn("inline-flex shrink-0 items-center gap-1", toneText.warning)}>
                <Clock3 className="size-3" aria-hidden="true" />
                {waiting} waiting
              </span>
            )}
            {completed > 0 && (
              <span className={cn("hidden shrink-0 lg:inline", toneText.success)}>
                {completed} completed
              </span>
            )}
            {failed > 0 && (
              <span className={cn("hidden shrink-0 lg:inline", toneText.danger)}>
                {failed} failed
              </span>
            )}
            {stopped > 0 && (
              <span className={cn("hidden shrink-0 lg:inline", toneText.warning)}>
                {stopped} stopped
              </span>
            )}
            {livePreview && (
              <span
                className="hidden min-w-0 truncate text-muted-foreground/80 md:inline"
                title={livePreview}
              >
                {livePreview}
              </span>
            )}
          </span>
          <ChevronRight
            className={cn(
              "ml-auto size-3.5 shrink-0 text-muted-foreground transition-transform",
              expanded && "rotate-90",
            )}
            aria-hidden="true"
          />
        </button>
        {onOpenGraph && (
          <button
            type="button"
            className={cn(
              "ml-1 inline-flex min-h-6 shrink-0 items-center gap-1 rounded p-1.5 text-2xs text-muted-foreground transition-colors hover:bg-muted hover:text-foreground sm:px-2",
              FOCUS_RING,
            )}
            onClick={onOpenGraph}
            aria-label="View full subagent graph"
            title="View full subagent graph"
          >
            <Maximize2 className="size-3" aria-hidden="true" />
            <span className="hidden sm:inline">View graph</span>
          </button>
        )}
      </div>

      {expanded && (
        <div
          id={rosterId}
          className="max-h-80 overflow-y-auto border-t border-border/50 px-3 py-2 md:px-4"
        >
          {waves.map((wave, waveIndex) => (
            <div key={waveIndex}>
              {waveIndex > 0 && (
                <div
                  data-testid="subagent-wave-divider"
                  className="my-2 flex items-center gap-2"
                  aria-hidden="true"
                >
                  <CornerDownRight className="size-3 shrink-0 text-muted-foreground/60" />
                  <span className="shrink-0 text-3xs text-muted-foreground/70">
                    {(() => {
                      const refs = unique(wave.flatMap((e) => e.dependsOn));
                      return refs.length > 0
                        ? `runs after ${formatRefs(refs)}`
                        : "runs after tasks above";
                    })()}
                  </span>
                  <span className="h-px flex-1 bg-border/60" />
                </div>
              )}
              <ul className="grid list-none gap-1.5 [grid-template-columns:repeat(auto-fill,minmax(280px,1fr))]">
                {wave.map((entry) => (
                  <DockTaskCard
                    key={entry.node.id}
                    entry={entry}
                    now={now}
                    onOpenGraph={onOpenGraph}
                  />
                ))}
              </ul>
            </div>
          ))}
        </div>
      )}
    </section>
  );
}

function unique(ordinals: number[]): number[] {
  return [...new Set(ordinals)].sort((a, b) => a - b);
}

function DockTaskCard({
  entry,
  now,
  onOpenGraph,
}: {
  entry: RosterEntry;
  now: number;
  onOpenGraph?: () => void;
}) {
  const { node, state, ordinal, dependsOn, dependsOnLabels } = entry;
  const color = getSubagentColor(agentType(node));
  const elapsed =
    state === "running" && node.timestampUnix > 0n
      ? formatDuration(Math.max(0, now - Number(node.timestampUnix) * 1_000))
      : node.durationMs > 0n
        ? formatDuration(node.durationMs)
        : "";

  return (
    <li
      data-testid="subagent-dock-card"
      title={`#${ordinal} ${node.label}`}
      className={cn(
        "relative flex flex-col justify-center gap-1 overflow-hidden rounded-md border border-border/60 border-l-2 bg-card/90 px-2.5 py-2",
        color.border,
        state === "failed" && "bg-tone-danger/5",
      )}
    >
      {state === "running" && (
        <span className="absolute inset-x-0 top-0 h-[2px] overflow-hidden rounded-t-md">
          <span className="block h-full w-full bg-[linear-gradient(90deg,transparent,var(--color-primary),transparent)] bg-[length:50%_100%] animate-shimmer motion-reduce:animate-none" />
        </span>
      )}
      <span className="flex min-w-0 items-center gap-1.5">
        <NodeStatusIcon state={state} />
        {onOpenGraph ? (
          <button
            type="button"
            onClick={onOpenGraph}
            aria-label={`Open task #${ordinal} in graph`}
            title="Open in graph"
            className={cn(
              "inline-flex min-h-6 shrink-0 items-center gap-0.5 rounded-sm px-0.5 font-mono text-3xs font-semibold tabular-nums text-muted-foreground/90 hover:text-foreground",
              FOCUS_RING,
            )}
          >
            #{ordinal}
            <Maximize2 className="size-2.5" aria-hidden="true" />
          </button>
        ) : (
          <span className="shrink-0 font-mono text-3xs font-semibold tabular-nums text-muted-foreground/90">
            #{ordinal}
          </span>
        )}
        <AgentTypeChip type={agentType(node)} className="shrink-0" />
        <span className="min-w-0 flex-1 truncate text-2xs font-medium text-foreground/90">
          {node.label}
        </span>
        {elapsed && (
          <span className="shrink-0 font-mono text-3xs tabular-nums text-muted-foreground/80">
            {elapsed}
          </span>
        )}
      </span>
      <span className="flex min-w-0 items-center gap-2 pl-5 text-3xs text-muted-foreground">
        {dependsOn.length > 0 && (
          <span
            data-testid="subagent-dep-ref"
            className={cn(
              "inline-flex shrink-0 items-center gap-1 font-mono text-3xs tabular-nums",
              state === "waiting" ? toneText.warning : "text-muted-foreground/70",
            )}
            title={`Runs after: ${dependsOnLabels
              .map((label, i) => `#${dependsOn[i]} ${label}`)
              .join(" · ")}`}
          >
            <CornerDownRight className="size-2.5" aria-hidden="true" />
            after {formatRefs(dependsOn)}
          </span>
        )}
        <span className="min-w-0 flex-1 truncate">{currentActivity(entry)}</span>
        <span className="flex shrink-0 items-center gap-1.5 font-mono text-3xs tabular-nums text-muted-foreground/80">
          {node.model && (
            <span className="max-w-28 truncate" title={node.model}>
              {node.model}
            </span>
          )}
          {node.totalTokens > 0n && <span>{formatTokens(Number(node.totalTokens))} tok</span>}
          {node.costUsd > 0 && <span>${node.costUsd.toFixed(3)}</span>}
        </span>
      </span>
    </li>
  );
}
