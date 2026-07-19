import * as React from "react";
import { CornerDownRight, GitBranch, Maximize2, Minus, Plus, Route } from "lucide-react";

import { ActivityLogTable } from "@/components/FullActivityLog";
import { ScrollArea } from "@/components/ui/scroll-area";
import { useNow } from "@/hooks/useNow";
import { formatDuration, formatTokens } from "@/lib/activityGrouping";
import { getSubagentColor } from "@/lib/subagentColors";
import {
  buildLayout,
  edgePath,
  isTerminalStatus,
  isWaitingStatus,
  type LaidNode,
  type Layout,
  NODE_H,
  NODE_W,
  nodeIDToTaskID,
  PAD,
} from "@/lib/subagentGraphLayout";
import { cn } from "@/lib/utils";
import type {
  ActivityEntry,
  SubagentGraph,
  SubagentGraphNode,
} from "@/rpc/platform/service_pb";

// ═══════════════════════════════════════════════════════════════════════════
// Subagent graph — a real node-and-edge DAG.
//
// The spawn hierarchy (who launched whom) drives a tidy left-to-right layout.
// `depends-on` relationships are overlaid as dashed edges AND push their targets
// into later columns, so dependencies always read left→right. Toggle the
// critical-path overlay to highlight the chain of agents that drove the run's
// wall-clock duration. Click any node to pin a rich detail panel.
//
// All pure layout + DAG analysis lives in @/lib/subagentGraphLayout (unit-tested
// in isolation); this file is the React rendering layer.
// ═══════════════════════════════════════════════════════════════════════════

// ───────────────────────── helpers ───────────────────────────────

function shortKind(node: SubagentGraphNode): string {
  const raw = (node.subtitle || node.kind || "").toUpperCase();
  if (!raw) return "•";
  const m = raw.match(/[A-Z0-9]+/);
  return (m ? m[0] : raw).slice(0, 3);
}

function displayNodeName(node: SubagentGraphNode | undefined, fallback: string): string {
  if (!node) return nodeIDToTaskID(fallback);
  return node.subtitle ? `${node.subtitle}: ${node.label}` : node.label;
}

// ───────────────────────── small pieces ──────────────────────────

function KindChip({ node }: { node: SubagentGraphNode }) {
  const color = getSubagentColor(node.kind === "subagent" ? node.subtitle : node.label);
  const isRoot = node.kind === "root";
  return (
    <span
      className={cn(
        "inline-flex h-[18px] min-w-[34px] items-center justify-center px-1.5",
        "rounded-[4px] font-mono text-[10px] font-semibold tracking-[0.06em]",
        "ring-1 ring-inset",
        isRoot
          ? "bg-muted text-muted-foreground ring-border"
          : cn(color.bg, color.text, color.border.replace("border-", "ring-")),
      )}
    >
      {isRoot ? "ROOT" : shortKind(node)}
    </span>
  );
}

function StatusGlyph({ status, running, waiting }: { status: string; running: boolean; waiting?: boolean }) {
  const base = "inline-block size-[8px] rounded-full shrink-0";
  if (waiting) return <span className={cn(base, "bg-amber-500 animate-pulse")} />;
  if (running) return <span className={cn(base, "bg-[color:var(--color-primary)] animate-pulse")} />;
  if (status === "failed") return <span className={cn(base, "bg-destructive")} />;
  if (status === "stopped" || status === "cancelled" || status === "canceled")
    return <span className={cn(base, "bg-amber-500")} />;
  if (isTerminalStatus(status)) return <span className={cn(base, "bg-emerald-500")} />;
  return <span className={cn(base, "bg-muted-foreground/50")} />;
}

function MiniChip({ children }: { children: React.ReactNode }) {
  return (
    <span className="inline-flex items-center gap-0.5 font-mono text-[10px] tabular-nums text-muted-foreground/90">
      {children}
    </span>
  );
}

// ───────────────────────── node card ─────────────────────────────

function NodeCard({
  laid,
  selected,
  dimmed,
  onCritical,
  onSelect,
  now,
}: {
  laid: LaidNode;
  selected: boolean;
  dimmed: boolean;
  onCritical: boolean;
  onSelect: (id: string) => void;
  now: number;
}) {
  const { node } = laid;
  const isRoot = node.kind === "root";
  const color = getSubagentColor(node.kind === "subagent" ? node.subtitle : node.label);
  // A live node gated on dependencies is waiting, not working: present it in
  // the amber "queued" language instead of the primary "live" treatment.
  const waiting = laid.running && (isWaitingStatus(node.status) || laid.waitingIds.length > 0);
  const working = laid.running && !waiting;

  const liveLine = working
    ? node.currentStep || (node.lastTool ? `running ${node.lastTool}` : "running…")
    : waiting
      ? "waiting for dependencies…"
      : node.description && node.description !== node.label
        ? node.description
        : node.subtitle || "";

  const liveDurationMs =
    node.timestampUnix > 0n ? Math.max(0, now - Number(node.timestampUnix) * 1_000) : 0;
  const durText = laid.running
    ? liveDurationMs > 0
      ? formatDuration(liveDurationMs)
      : waiting
        ? "waiting"
        : "live"
    : node.durationMs > 0n
      ? formatDuration(Number(node.durationMs))
      : "—";

  return (
    <button
      type="button"
      onClick={() => onSelect(node.id)}
      className={cn(
        "absolute flex flex-col gap-1 rounded-lg border p-2.5 text-left",
        "transition-[box-shadow,opacity,border-color] duration-[var(--dur-fast)]",
        "bg-card/95 backdrop-blur-sm",
        isRoot ? "border-border" : color.border,
        onCritical && !selected && "border-[color:var(--color-primary)]/70",
        selected
          ? "shadow-[0_0_0_1px_var(--color-primary),0_0_0_5px_color-mix(in_oklch,var(--color-primary)_18%,transparent)]"
          : onCritical
            ? "shadow-[0_0_0_1px_color-mix(in_oklch,var(--color-primary)_55%,transparent)]"
            : "hover:border-[color:var(--color-primary)]/60 hover:shadow-md",
        dimmed && "opacity-35",
      )}
      style={{ left: laid.x + PAD, top: laid.y + PAD, width: NODE_W, height: NODE_H }}
      aria-pressed={selected}
    >
      {working && (
        <span className="absolute inset-x-0 top-0 h-[2px] overflow-hidden rounded-t-lg">
          <span className="block h-full w-full bg-[linear-gradient(90deg,transparent,var(--color-primary),transparent)] bg-[length:50%_100%] animate-[shimmer_1.6s_linear_infinite]" />
        </span>
      )}

      <div className="flex items-center gap-1.5">
        <StatusGlyph status={node.status} running={working} waiting={waiting} />
        <KindChip node={node} />
        <span className="min-w-0 flex-1 truncate text-[12.5px] font-medium tracking-tight text-foreground">
          {node.label}
        </span>
        <span
          className={cn(
            "shrink-0 font-mono text-[10px] tabular-nums",
            waiting ? "text-amber-500" : "text-muted-foreground/80",
          )}
        >
          {durText}
        </span>
      </div>

      {liveLine && (
        <p className="line-clamp-1 text-[11px] leading-snug text-muted-foreground">{liveLine}</p>
      )}

      <div className="mt-auto flex min-w-0 flex-wrap items-center gap-x-2.5 gap-y-0.5">
        {node.model && (
          <MiniChip>
            <span className="max-w-[110px] truncate" title={node.model}>
              {node.model}
            </span>
          </MiniChip>
        )}
        {node.totalTokens > 0n && <MiniChip>{formatTokens(Number(node.totalTokens))} tok</MiniChip>}
        {node.toolCount > 0 && <MiniChip>{node.toolCount} tools</MiniChip>}
        {node.costUsd > 0 && <MiniChip>${node.costUsd.toFixed(3)}</MiniChip>}
        {node.filesWritten > 0 && <MiniChip>{node.filesWritten} files</MiniChip>}
        {laid.waitingIds.length > 0 ? (
          <span className="rounded-[4px] bg-amber-500/10 px-1.5 py-px text-[10px] text-amber-500 ring-1 ring-inset ring-amber-500/25">
            waiting {laid.waitingIds.length}
          </span>
        ) : (
          laid.dependencyIds.length > 0 && (
            <span className="inline-flex items-center gap-0.5 rounded-[4px] bg-muted/60 px-1.5 py-px text-[10px] text-muted-foreground ring-1 ring-inset ring-border/60">
              <GitBranch className="size-2.5" />
              {laid.dependencyIds.length}
            </span>
          )
        )}
      </div>
    </button>
  );
}

// ───────────────────────── canvas ────────────────────────────────

function GraphCanvas({
  layout,
  selectedId,
  showCritical,
  onSelect,
}: {
  layout: Layout;
  selectedId: string | null;
  showCritical: boolean;
  onSelect: (id: string) => void;
}) {
  const now = useNow(1_000);
  const [scale, setScale] = React.useState(1);
  const scrollRef = React.useRef<HTMLDivElement>(null);

  const fullW = layout.width + PAD * 2;
  const fullH = layout.height + PAD * 2;

  const fit = React.useCallback(() => {
    const el = scrollRef.current;
    if (!el) return;
    const sx = el.clientWidth / fullW;
    const sy = el.clientHeight / fullH;
    setScale(Math.min(1, Math.max(0.4, Math.min(sx, sy))));
  }, [fullW, fullH]);

  React.useEffect(() => {
    fit();
  }, [fit]);

  // Native listener: React registers root wheel listeners as passive, so
  // preventDefault from an onWheel prop can't stop browser page zoom.
  React.useEffect(() => {
    const el = scrollRef.current;
    if (!el) return;
    const handleWheel = (e: WheelEvent) => {
      if (!e.ctrlKey && !e.metaKey) return;
      e.preventDefault();
      setScale((current) => Math.min(1.6, Math.max(0.4, +(current - e.deltaY * 0.001).toFixed(3))));
    };
    el.addEventListener("wheel", handleWheel, { passive: false });
    return () => el.removeEventListener("wheel", handleWheel);
  }, []);

  // Edges connected to the selection stay vivid; everything else dims.
  const connected = React.useMemo(() => {
    const ids = new Set<string>();
    if (!selectedId) return ids;
    ids.add(selectedId);
    for (const e of layout.edges) {
      if (e.from === selectedId) ids.add(e.to);
      if (e.to === selectedId) ids.add(e.from);
    }
    return ids;
  }, [layout.edges, selectedId]);

  const criticalActive = showCritical && layout.criticalIds.size > 0;
  const isCriticalEdge = React.useCallback(
    (from: string, to: string) =>
      criticalActive && layout.criticalIds.has(from) && layout.criticalIds.has(to),
    [criticalActive, layout.criticalIds],
  );

  return (
    <div className="relative flex min-h-0 flex-1 flex-col">
      <div className="absolute right-3 top-3 z-10 flex items-center gap-1 rounded-md border border-border/60 bg-card/90 p-0.5 shadow-sm backdrop-blur">
        <button
          type="button"
          onClick={() => setScale((s) => Math.max(0.4, +(s - 0.1).toFixed(2)))}
          className="grid size-6 place-items-center rounded text-muted-foreground hover:bg-muted hover:text-foreground"
          aria-label="Zoom out"
        >
          <Minus className="size-3.5" />
        </button>
        <span className="w-9 text-center font-mono text-[10px] tabular-nums text-muted-foreground">
          {Math.round(scale * 100)}%
        </span>
        <button
          type="button"
          onClick={() => setScale((s) => Math.min(1.6, +(s + 0.1).toFixed(2)))}
          className="grid size-6 place-items-center rounded text-muted-foreground hover:bg-muted hover:text-foreground"
          aria-label="Zoom in"
        >
          <Plus className="size-3.5" />
        </button>
        <button
          type="button"
          onClick={fit}
          className="grid size-6 place-items-center rounded text-muted-foreground hover:bg-muted hover:text-foreground"
          aria-label="Fit to view"
        >
          <Maximize2 className="size-3.5" />
        </button>
      </div>

      <div
        ref={scrollRef}
        className="min-h-0 flex-1 overflow-auto bg-[radial-gradient(circle_at_1px_1px,color-mix(in_oklch,var(--color-border)_60%,transparent)_1px,transparent_0)] [background-size:22px_22px]"
      >
        <div style={{ width: fullW * scale, height: fullH * scale }}>
          <div
            className="relative origin-top-left"
            style={{ width: fullW, height: fullH, transform: `scale(${scale})` }}
          >
            <svg className="pointer-events-none absolute inset-0" width={fullW} height={fullH} fill="none">
              <defs>
                <marker
                  id="arrow-spawn"
                  viewBox="0 0 8 8"
                  refX="6"
                  refY="4"
                  markerWidth="6"
                  markerHeight="6"
                  orient="auto-start-reverse"
                >
                  <path d="M0,0 L8,4 L0,8 z" className="fill-border" />
                </marker>
                <marker
                  id="arrow-dep"
                  viewBox="0 0 8 8"
                  refX="6"
                  refY="4"
                  markerWidth="6"
                  markerHeight="6"
                  orient="auto-start-reverse"
                >
                  <path d="M0,0 L8,4 L0,8 z" className="fill-amber-500" />
                </marker>
                <marker
                  id="arrow-critical"
                  viewBox="0 0 8 8"
                  refX="6"
                  refY="4"
                  markerWidth="6"
                  markerHeight="6"
                  orient="auto-start-reverse"
                >
                  <path d="M0,0 L8,4 L0,8 z" className="fill-[color:var(--color-primary)]" />
                </marker>
              </defs>

              {layout.edges.map((e) => {
                const a = layout.nodes.get(e.from)!;
                const b = layout.nodes.get(e.to)!;
                const onCritical = isCriticalEdge(e.from, e.to);
                const selectionActive =
                  !selectedId || connected.has(e.from) || connected.has(e.to);
                const active = onCritical || selectionActive;
                const isDep = e.kind === "depends-on";
                const x1 = a.x + NODE_W + PAD;
                const y1 = a.y + NODE_H / 2 + PAD;
                const x2 = b.x + PAD;
                const y2 = b.y + NODE_H / 2 + PAD;
                const marker = onCritical
                  ? "url(#arrow-critical)"
                  : isDep
                    ? "url(#arrow-dep)"
                    : "url(#arrow-spawn)";
                return (
                  <path
                    key={e.id}
                    d={edgePath(x1, y1, x2, y2)}
                    className={cn(
                      onCritical
                        ? "stroke-[color:var(--color-primary)]"
                        : isDep
                          ? "stroke-amber-500"
                          : "stroke-border",
                      active ? "opacity-90" : criticalActive ? "opacity-10" : "opacity-20",
                    )}
                    strokeWidth={onCritical ? 2.5 : isDep ? 1.5 : 1.75}
                    strokeDasharray={isDep && !onCritical ? "4 4" : undefined}
                    markerEnd={marker}
                  />
                );
              })}
            </svg>

            {layout.order.map((ln) => {
              const onCritical = criticalActive && layout.criticalIds.has(ln.node.id);
              const dimmed = selectedId
                ? !connected.has(ln.node.id)
                : criticalActive && !onCritical;
              return (
                <NodeCard
                  key={ln.node.id}
                  laid={ln}
                  selected={selectedId === ln.node.id}
                  dimmed={dimmed}
                  onCritical={onCritical}
                  onSelect={onSelect}
                  now={now}
                />
              );
            })}
          </div>
        </div>
      </div>
    </div>
  );
}

// ───────────────────────── summary + legend ──────────────────────

function SummaryStrip({ graph, layout }: { graph: SubagentGraph; layout: Layout }) {
  const flats = layout.order;
  const totalTokens = flats.reduce((a, f) => a + Number(f.node.totalTokens || 0n), 0);
  const isWaiting = (f: LaidNode) =>
    f.running && (isWaitingStatus(f.node.status) || f.waitingIds.length > 0);
  const waiting = flats.filter(isWaiting).length;
  const running = flats.filter((f) => f.running && !isWaiting(f)).length;
  const failed = flats.filter((f) => f.node.status === "failed").length;
  const dependencies = graph.edges.filter((e) => e.kind === "depends-on").length;
  const subagents = graph.nodes.filter((n) => n.kind !== "root").length;

  const pill = (label: string, value: React.ReactNode, extra?: string) => (
    <span className="inline-flex items-center gap-1.5 rounded-[5px] bg-muted/40 px-2 py-1 text-[11px] text-muted-foreground ring-1 ring-inset ring-border/60">
      <span className="uppercase tracking-[0.08em] text-muted-foreground/70">{label}</span>
      <span className={cn("font-mono tabular-nums", extra ?? "text-foreground")}>{value}</span>
    </span>
  );

  return (
    <div className="flex flex-wrap items-center gap-1.5">
      {pill("agents", subagents)}
      {dependencies > 0 && pill("deps", dependencies)}
      {pill("tokens", formatTokens(totalTokens))}
      {running > 0 && pill("running", running, "text-[color:var(--color-primary)]")}
      {waiting > 0 && pill("waiting", waiting, "text-amber-500")}
      {failed > 0 && pill("failed", failed, "text-destructive")}
    </div>
  );
}

function Legend() {
  return (
    <div className="flex items-center gap-3 text-[10px] text-muted-foreground/80">
      <span className="inline-flex items-center gap-1">
        <svg width="22" height="8" className="overflow-visible">
          <line x1="0" y1="4" x2="20" y2="4" className="stroke-border" strokeWidth="1.75" />
        </svg>
        spawned
      </span>
      <span className="inline-flex items-center gap-1">
        <svg width="22" height="8" className="overflow-visible">
          <line
            x1="0"
            y1="4"
            x2="20"
            y2="4"
            className="stroke-amber-500"
            strokeWidth="1.5"
            strokeDasharray="4 4"
          />
        </svg>
        depends-on
      </span>
    </div>
  );
}

// ───────────────────────── detail panel ──────────────────────────

function NodeDetail({
  node,
  entries,
  graph,
}: {
  node: SubagentGraphNode;
  entries: ActivityEntry[];
  graph: SubagentGraph;
}) {
  const detailEntries = React.useMemo(() => {
    // Prefer durable event-id references: with delta streaming and
    // pagination the local entries buffer is an arbitrary slice of the run's
    // history, so server-computed positional indices may not line up.
    if (node.detailEntryEventIds.length > 0) {
      const byId = new Map<bigint, ActivityEntry>();
      for (const e of entries) {
        if (e.eventId !== 0n) byId.set(e.eventId, e);
      }
      return node.detailEntryEventIds
        .map((id) => byId.get(id))
        .filter((e): e is ActivityEntry => e !== undefined);
    }
    if (!node.detailEntryIndices.length) return [];
    return node.detailEntryIndices
      .filter((i) => i >= 0 && i < entries.length)
      .map((i) => entries[i]);
  }, [node, entries]);
  const nodesById = React.useMemo(() => {
    const out: Record<string, SubagentGraphNode> = {};
    for (const n of graph.nodes) out[n.id] = n;
    return out;
  }, [graph]);
  const dependencyIds = React.useMemo(
    () => graph.edges.filter((e) => e.kind === "depends-on" && e.to === node.id).map((e) => e.from),
    [graph, node.id],
  );

  const metrics: { label: string; value: string }[] = [];
  const push = (label: string, value: string | undefined | null) => {
    if (value) metrics.push({ label, value });
  };
  push("Status", node.status);
  push("Model", node.model);
  push("Tokens", node.totalTokens > 0n ? formatTokens(Number(node.totalTokens)) : "");
  push("Duration", node.durationMs > 0n ? formatDuration(Number(node.durationMs)) : "");
  push("Cost", node.costUsd > 0 ? `$${node.costUsd.toFixed(4)}` : "");
  push("Tools", node.toolCount > 0 ? String(node.toolCount) : "");
  push("Turns", node.numTurns > 0 ? String(node.numTurns) : "");
  push("Stop", node.stopReason);
  push("Current step", node.currentStep);
  push("Last tool", node.lastTool);
  push("Files", node.filesWritten > 0 ? String(node.filesWritten) : "");
  push("Messages", node.messagesReceived > 0 ? String(node.messagesReceived) : "");
  push("Task ID", node.taskId);

  return (
    <div className="border-t border-border/50 bg-card/40 px-4 py-3">
      <div className="mb-2 flex items-start gap-2">
        <KindChip node={node} />
        <div className="min-w-0 flex-1">
          <div className="truncate text-[13px] font-medium tracking-tight text-foreground">
            {node.label}
          </div>
          {node.description && node.description !== node.label && (
            <p className="mt-0.5 text-[12px] leading-relaxed text-muted-foreground">
              {node.description}
            </p>
          )}
        </div>
      </div>

      {node.lineageReason && (
        <p className="mb-2 border-l-2 border-amber-500/60 pl-2 text-[11.5px] text-amber-500">
          {node.lineageReason}
        </p>
      )}

      {node.lastParentMessage && (
        <p className="mb-2 border-l-2 border-border pl-2 text-[11.5px] leading-relaxed text-muted-foreground">
          {node.lastParentMessage}
        </p>
      )}

      {metrics.length > 0 && (
        <dl className="grid grid-cols-2 gap-x-6 gap-y-1 text-[11.5px] sm:grid-cols-3 lg:grid-cols-4">
          {metrics.map((m) => (
            <div key={m.label} className="flex min-w-0 items-baseline gap-1.5">
              <dt className="shrink-0 text-muted-foreground/80">{m.label}</dt>
              <dd className="min-w-0 truncate font-mono tabular-nums text-foreground/90">{m.value}</dd>
            </div>
          ))}
        </dl>
      )}

      {node.handoffs?.length > 0 && (
        <div className="mt-2 flex flex-wrap items-center gap-1.5">
          <span className="text-[10px] uppercase tracking-[0.08em] text-muted-foreground/70">Handoffs</span>
          {node.handoffs.map((h, i) => (
            <span
              key={i}
              className="rounded-[4px] bg-purple-500/10 px-1.5 py-px font-mono text-[10.5px] text-purple-400 ring-1 ring-inset ring-purple-500/30"
            >
              ↗ {h}
            </span>
          ))}
        </div>
      )}

      {(dependencyIds.length > 0 || node.waitingOn.length > 0) && (
        <div className="mt-2 flex flex-wrap items-center gap-1.5">
          <span className="text-[10px] uppercase tracking-[0.08em] text-muted-foreground/70">DAG</span>
          {dependencyIds.map((id) => (
            <span
              key={`dep-${id}`}
              className="rounded-[4px] bg-muted/50 px-1.5 py-px font-mono text-[10.5px] text-muted-foreground ring-1 ring-inset ring-border/60"
            >
              after {displayNodeName(nodesById[id], id)}
            </span>
          ))}
          {node.waitingOn.map((id) => (
            <span
              key={`wait-${id}`}
              className="rounded-[4px] bg-amber-500/10 px-1.5 py-px font-mono text-[10.5px] text-amber-500 ring-1 ring-inset ring-amber-500/25"
            >
              waiting {id}
            </span>
          ))}
        </div>
      )}

      {detailEntries.length > 0 && (
        <div className="mt-3">
          <p className="mb-1 flex items-center gap-1 text-[10px] font-semibold uppercase tracking-[0.1em] text-muted-foreground/70">
            <CornerDownRight className="size-3" /> Related activity
          </p>
          <ActivityLogTable entries={detailEntries} loading={false} error={null} />
        </div>
      )}
    </div>
  );
}

// ───────────────────────── main ──────────────────────────────────

export function SubagentGraphView({
  graph,
  entries,
}: {
  graph?: SubagentGraph;
  entries: ActivityEntry[];
}) {
  const empty = !graph || graph.nodes.length === 0 || !graph.hasSubagents;

  const layout = React.useMemo(() => {
    if (!graph || graph.nodes.length === 0) return null;
    return buildLayout(graph);
  }, [graph]);

  const firstSubagent = layout?.order.find((f) => f.node.kind !== "root");
  const [selectedId, setSelectedId] = React.useState<string | null>(null);
  const [showCritical, setShowCritical] = React.useState(false);

  const resolvedId =
    selectedId && layout?.nodes.has(selectedId)
      ? selectedId
      : firstSubagent?.node.id ?? graph?.rootId ?? null;
  const selected = resolvedId ? layout?.nodes.get(resolvedId)?.node ?? null : null;

  if (empty || !layout) {
    return (
      <div className="px-4 py-6 text-[12.5px] text-muted-foreground">
        <p className="font-medium text-foreground">No subagents observed</p>
        <p className="mt-1 text-[11.5px]">
          The graph appears once this run spawns subagents or records inline subagent activity.
        </p>
      </div>
    );
  }

  const criticalAvailable = layout.criticalIds.size > 1;

  return (
    <div className="flex h-full flex-col">
      <div className="flex shrink-0 flex-wrap items-center justify-between gap-3 border-b border-border/50 px-3 py-2.5">
        <div className="flex items-center gap-3">
          <h3 className="text-[12px] font-medium tracking-tight text-foreground">Subagent graph</h3>
          <Legend />
        </div>
        <div className="flex items-center gap-2">
          {criticalAvailable && (
            <button
              type="button"
              onClick={() => setShowCritical((v) => !v)}
              aria-pressed={showCritical}
              title="Highlight the chain of agents that drove the run's wall-clock duration"
              className={cn(
                "inline-flex items-center gap-1.5 rounded-[5px] px-2 py-1 text-[11px] ring-1 ring-inset transition-colors",
                "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/60",
                showCritical
                  ? "bg-[color:var(--color-primary)]/15 text-[color:var(--color-primary)] ring-[color:var(--color-primary)]/40"
                  : "bg-muted/40 text-muted-foreground ring-border/60 hover:text-foreground",
              )}
            >
              <Route className="size-3" />
              <span className="uppercase tracking-[0.08em]">critical path</span>
              {showCritical && layout.criticalDurationMs > 0 && (
                <span className="font-mono tabular-nums">
                  {formatDuration(layout.criticalDurationMs)}
                </span>
              )}
            </button>
          )}
          <SummaryStrip graph={graph!} layout={layout} />
        </div>
      </div>

      <GraphCanvas
        layout={layout}
        selectedId={resolvedId}
        showCritical={showCritical}
        onSelect={setSelectedId}
      />

      {selected && (
        <ScrollArea className="max-h-[42%] shrink-0">
          <NodeDetail node={selected} entries={entries} graph={graph!} />
        </ScrollArea>
      )}
    </div>
  );
}
