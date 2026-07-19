import { useId, useMemo, useState } from "react";
import {
  AlertTriangle,
  Check,
  ChevronDown,
  Clock3,
  GitFork,
  Loader2,
  Maximize2,
  XCircle,
} from "lucide-react";

import { useNow } from "@/hooks/useNow";
import { formatDuration, formatTokens } from "@/lib/activityGrouping";
import { getSubagentColor } from "@/lib/subagentColors";
import {
  buildLayout,
  edgePath,
  isRunningSubagentNode,
  isWaitingStatus,
  type LayoutDims,
} from "@/lib/subagentGraphLayout";
import { toneText } from "@/lib/status";
import { cn } from "@/lib/utils";
import type { SubagentGraph, SubagentGraphNode } from "@/rpc/platform/service_pb";

const STOPPED_STATUSES = new Set(["stopped", "cancelled", "canceled"]);

const MINI: LayoutDims = { nodeW: 220, nodeH: 64, hGap: 44, vGap: 12 };
const MINI_PAD = 10;

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

function currentActivity(node: SubagentGraphNode, state: NodeState): string {
  if (state === "waiting") {
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
      <Loader2
        className={cn("size-3.5 shrink-0 animate-spin", toneText.running)}
        aria-hidden="true"
      />
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

const DOCK_EXPANDED_KEY = "gratefulagents.subagentDockExpanded";

/**
 * A pinned, compact rendering of the complete subagent DAG. It stays next to
 * the composer while work is active, so users do not need to scroll back to a
 * delegation event to understand completed, running, and waiting branches.
 * Collapsed by default — the summary row stays visible and the DAG canvas is
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
  const graphId = useId();
  const now = useNow(1_000);

  const subagents = useMemo(
    () => graph?.nodes.filter((node) => node.kind !== "root") ?? [],
    [graph],
  );
  const active = useMemo(() => subagents.filter(isRunningSubagentNode), [subagents]);
  const visibleGraph = useMemo(() => {
    if (!graph || subagents.length === 0) return undefined;
    const ids = new Set(subagents.map((node) => node.id));
    return {
      ...graph,
      rootId: "",
      nodes: subagents,
      edges: graph.edges.filter((edge) => ids.has(edge.from) && ids.has(edge.to)),
    } satisfies SubagentGraph;
  }, [graph, subagents]);
  const layout = useMemo(
    () => (visibleGraph ? buildLayout(visibleGraph, MINI) : undefined),
    [visibleGraph],
  );

  // Keep the complete graph pinned while any delegated work is live. Once all
  // tasks are terminal it remains available in the transcript and Graph tab.
  if (active.length === 0 || !layout) return null;

  const states = subagents.map(nodeState);
  const waiting = states.filter((state) => state === "waiting").length;
  const running = states.filter((state) => state === "running").length;
  const completed = states.filter((state) => state === "completed").length;
  const failed = states.filter((state) => state === "failed").length;
  const stopped = states.filter((state) => state === "stopped").length;
  const canvasW = layout.width + MINI_PAD * 2;
  const canvasH = layout.height + MINI_PAD * 2;
  // While collapsed the DAG canvas is hidden, so surface the most informative
  // live line (a running node's current step) directly in the summary row.
  const livePreviewNode = expanded
    ? undefined
    : subagents.find((node, i) => states[i] === "running" && (node.currentStep || node.lastTool)) ??
      subagents.find((_, i) => states[i] === "running");
  const livePreview = livePreviewNode
    ? `${agentType(livePreviewNode)} · ${currentActivity(livePreviewNode, "running")}`
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
          className="flex min-w-0 flex-1 items-center gap-2 rounded-sm py-1.5 text-left focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/60"
          aria-expanded={expanded}
          aria-controls={graphId}
          aria-label={`${active.length} active agent${active.length === 1 ? "" : "s"}; ${subagents.length} delegated task${subagents.length === 1 ? "" : "s"}`}
          onClick={toggleExpanded}
        >
          <GitFork className="size-3.5 shrink-0 rotate-90 text-muted-foreground" />
          <span className="shrink-0 text-xs font-medium text-foreground">
            Delegated {subagents.length} task{subagents.length === 1 ? "" : "s"}
          </span>
          <span className="hidden min-w-0 items-center gap-2 overflow-hidden text-[11px] sm:flex">
            {running > 0 && (
              <span className={cn("inline-flex shrink-0 items-center gap-1", toneText.running)}>
                <Loader2 className="size-3 animate-spin" aria-hidden="true" />
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
          <ChevronDown
            className={cn(
              "ml-auto size-3.5 shrink-0 text-muted-foreground transition-transform",
              expanded && "rotate-180",
            )}
            aria-hidden="true"
          />
        </button>
        {onOpenGraph && (
          <button
            type="button"
            className="ml-1 inline-flex shrink-0 items-center gap-1 rounded p-1.5 text-[11px] text-muted-foreground transition-colors hover:bg-muted hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/60 sm:px-2"
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
        <div id={graphId} className="max-h-72 overflow-auto border-t border-border/50">
          <div className="relative" style={{ width: canvasW, height: canvasH, minWidth: "100%" }}>
            <svg
              className="pointer-events-none absolute inset-0"
              width={canvasW}
              height={canvasH}
              aria-hidden="true"
            >
              {layout.edges.map((edge) => {
                const from = layout.nodes.get(edge.from);
                const to = layout.nodes.get(edge.to);
                if (!from || !to) return null;
                return (
                  <path
                    key={edge.id}
                    data-testid="subagent-dag-edge"
                    d={edgePath(
                      from.x + MINI.nodeW + MINI_PAD,
                      from.y + MINI.nodeH / 2 + MINI_PAD,
                      to.x + MINI_PAD,
                      to.y + MINI.nodeH / 2 + MINI_PAD,
                    )}
                    fill="none"
                    stroke={edge.kind === "depends-on" ? "var(--tone-warning)" : "currentColor"}
                    className={edge.kind === "depends-on" ? undefined : "text-border"}
                    strokeOpacity={edge.kind === "depends-on" ? 0.6 : 0.45}
                    strokeWidth={1.25}
                    strokeDasharray={edge.kind === "depends-on" ? "4 3" : undefined}
                  />
                );
              })}
            </svg>
            {layout.order.map(({ node, x, y }) => {
              const state = nodeState(node);
              const color = getSubagentColor(agentType(node));
              return (
                <div
                  key={node.id}
                  title={node.label}
                  className={cn(
                    "absolute flex flex-col justify-center gap-1 rounded-md border border-l-2 bg-card/95 px-2.5 py-1.5",
                    color.border,
                  )}
                  style={{
                    left: x + MINI_PAD,
                    top: y + MINI_PAD,
                    width: MINI.nodeW,
                    height: MINI.nodeH,
                  }}
                >
                  {state === "running" && (
                    <span className="absolute inset-x-0 top-0 h-[2px] overflow-hidden rounded-t-md">
                      <span className="block h-full w-full bg-[linear-gradient(90deg,transparent,var(--color-primary),transparent)] bg-[length:50%_100%] animate-[shimmer_1.6s_linear_infinite]" />
                    </span>
                  )}
                  <span className="flex min-w-0 items-center gap-1.5">
                    <NodeStatusIcon state={state} />
                    <span
                      className={cn(
                        "shrink-0 rounded border px-1 py-px font-mono text-[9.5px] font-semibold",
                        color.border,
                        color.bg,
                        color.text,
                      )}
                    >
                      {agentType(node)}
                    </span>
                    <span className="min-w-0 flex-1 truncate text-[11px] font-medium text-foreground/90">
                      {node.label}
                    </span>
                  </span>
                  <span className="flex min-w-0 items-center gap-2 pl-5 text-[10px] text-muted-foreground">
                    <span className="min-w-0 flex-1 truncate">{currentActivity(node, state)}</span>
                    <span className="flex shrink-0 items-center gap-1.5 font-mono text-[9.5px] tabular-nums text-muted-foreground/80">
                      {state === "running" && node.timestampUnix > 0n && (
                        <span>{formatDuration(Math.max(0, now - Number(node.timestampUnix) * 1_000))}</span>
                      )}
                      {node.model && (
                        <span className="max-w-[72px] truncate" title={node.model}>
                          {node.model}
                        </span>
                      )}
                      {node.totalTokens > 0n && <span>{formatTokens(Number(node.totalTokens))} tok</span>}
                      {node.costUsd > 0 && <span>${node.costUsd.toFixed(3)}</span>}
                    </span>
                  </span>
                </div>
              );
            })}
          </div>
        </div>
      )}
    </section>
  );
}
