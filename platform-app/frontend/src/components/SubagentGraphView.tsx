import * as React from "react";
import { CornerDownRight, GitBranch, Maximize2, Minus, Plus, Route } from "lucide-react";

import { ActivityLogTable } from "@/components/FullActivityLog";
import { AgentTypeChip } from "@/components/ui/agent-type-chip";
import { LiveDot, type LiveDotTone } from "@/components/ui/live-dot";
import { ResizableHandle, ResizablePanel, ResizablePanelGroup } from "@/components/ui/resizable";
import { useNow } from "@/hooks/useNow";
import { formatDuration, formatTokens } from "@/lib/activityGrouping";
import { getSubagentColor } from "@/lib/subagentColors";
import {
  assignOrdinals,
  buildLayout,
  COMPACT_DIMS,
  DEFAULT_DIMS,
  edgePath,
  isTerminalStatus,
  isWaitingStatus,
  type LaidNode,
  type Layout,
  type LayoutDims,
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

const MIN_SCALE = 0.35;
const MAX_SCALE = 1.6;
const COMPACT_BREAKPOINT = 560;
// Room reserved above/below the graph so the zoom controls and legend overlays
// never sit on top of a node.
const CANVAS_PT = 40;
const CANVAS_PB = 32;

// ───────────────────────── helpers ───────────────────────────────

function clampScale(s: number): number {
  return Math.min(MAX_SCALE, Math.max(MIN_SCALE, +s.toFixed(3)));
}

function displayNodeName(node: SubagentGraphNode | undefined, fallback: string): string {
  if (!node) return nodeIDToTaskID(fallback);
  return node.subtitle ? `${node.subtitle}: ${node.label}` : node.label;
}

function agentType(node: SubagentGraphNode): string {
  return node.kind === "subagent" ? node.subtitle : node.label;
}

function isWaitingNode(laid: LaidNode): boolean {
  return laid.running && (isWaitingStatus(laid.node.status) || laid.waitingIds.length > 0);
}

function isWorkingNode(laid: LaidNode): boolean {
  return laid.running && !isWaitingNode(laid);
}

function domSafeId(id: string): string {
  return id.replace(/[^a-zA-Z0-9_-]/g, "_");
}

function toneFor(laid: LaidNode): { tone: LiveDotTone; pulse: boolean } {
  const { status } = laid.node;
  if (isWaitingNode(laid)) return { tone: "waiting", pulse: true };
  if (laid.running) return { tone: "running", pulse: true };
  if (status === "failed") return { tone: "danger", pulse: false };
  if (status === "stopped" || status === "cancelled" || status === "canceled")
    return { tone: "waiting", pulse: false };
  if (isTerminalStatus(status)) return { tone: "success", pulse: false };
  return { tone: "idle", pulse: false };
}

function durationText(laid: LaidNode, now: number): string {
  const { node } = laid;
  const liveMs = node.timestampUnix > 0n ? Math.max(0, now - Number(node.timestampUnix) * 1_000) : 0;
  if (laid.running) {
    if (liveMs > 0) return formatDuration(liveMs);
    return isWaitingNode(laid) ? "waiting" : "live";
  }
  return node.durationMs > 0n ? formatDuration(Number(node.durationMs)) : "—";
}

// ───────────────────────── small pieces ──────────────────────────

function MiniChip({ children }: { children: React.ReactNode }) {
  return (
    <span className="inline-flex items-center gap-0.5 font-mono text-3xs tabular-nums text-muted-foreground/90">
      {children}
    </span>
  );
}

function Ordinal({ n, className }: { n: number | undefined; className?: string }) {
  if (n === undefined) return null;
  return (
    <span className={cn("shrink-0 font-mono text-3xs tabular-nums text-muted-foreground/70", className)}>
      #{n}
    </span>
  );
}

// ───────────────────────── node card ─────────────────────────────

function NodeCard({
  laid,
  dims,
  compact,
  ordinal,
  selected,
  dimmed,
  onCritical,
  describedBy,
  onSelect,
  onZoomTo,
  now,
}: {
  laid: LaidNode;
  dims: LayoutDims;
  compact: boolean;
  ordinal: number | undefined;
  selected: boolean;
  dimmed: boolean;
  onCritical: boolean;
  describedBy: string;
  onSelect: (id: string) => void;
  onZoomTo: (laid: LaidNode) => void;
  now: number;
}) {
  const { node } = laid;
  const isRoot = node.kind === "root";
  const color = getSubagentColor(agentType(node));
  // A live node gated on dependencies is waiting, not working: present it in
  // the warning "queued" language instead of the primary "live" treatment.
  const waiting = isWaitingNode(laid);
  const working = isWorkingNode(laid);
  const { tone, pulse } = toneFor(laid);
  // row-in fills forwards with `transform: none`, which would pin the card and
  // defeat the selected lift / dimmed scale; drop it once the entrance is done.
  const [entered, setEntered] = React.useState(false);

  const liveLine = working
    ? node.currentStep || (node.lastTool ? `running ${node.lastTool}` : "running…")
    : waiting
      ? "waiting for dependencies…"
      : node.description && node.description !== node.label
        ? node.description
        : node.subtitle || "";

  const durText = durationText(laid, now);

  const elevation = laid.depth > 0 ? "var(--elevation-mid)" : "var(--elevation-low)";
  const boxShadow = selected
    ? `0 0 0 1px var(--color-primary), 0 0 0 5px color-mix(in oklch, var(--color-primary) 18%, transparent), ${elevation}`
    : onCritical
      ? `0 0 0 1px color-mix(in oklch, var(--color-primary) 55%, transparent), ${elevation}`
      : elevation;

  return (
    <button
      type="button"
      data-node-id={node.id}
      onClick={() => onSelect(node.id)}
      onDoubleClick={() => onZoomTo(laid)}
      onAnimationEnd={(e) => {
        if (e.target === e.currentTarget) setEntered(true);
      }}
      className={cn(
        "absolute flex cursor-pointer flex-col rounded-lg border text-left",
        compact ? "gap-0.5 p-2" : "gap-1 p-2.5",
        "transition-[left,top,box-shadow,opacity,border-color,transform] duration-[var(--dur-slow)]",
        !entered && "animate-row-in",
        "bg-card/95 backdrop-blur-sm",
        "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/60",
        isRoot ? "border-border" : color.border,
        node.status === "failed" && "border-tone-danger/70",
        onCritical && !selected && "border-[color:var(--color-primary)]/70",
        !selected && !onCritical && "hover:border-[color:var(--color-primary)]/60",
        selected && "-translate-y-px",
        dimmed && "opacity-40 scale-[0.98]",
      )}
      style={{ left: laid.x + PAD, top: laid.y + PAD, width: dims.nodeW, height: dims.nodeH, boxShadow }}
      aria-pressed={selected}
      aria-describedby={describedBy}
    >
      {working && (
        <span className="absolute inset-x-0 top-0 h-[2px] overflow-hidden rounded-t-lg">
          <span className="block h-full w-full animate-shimmer bg-[linear-gradient(90deg,transparent,var(--color-primary),transparent)] bg-[length:50%_100%]" />
        </span>
      )}

      <div className="flex items-center gap-1.5">
        <LiveDot tone={tone} pulse={pulse} />
        <AgentTypeChip type={agentType(node)} short root={isRoot} />
        <Ordinal n={ordinal} />
        <span className="min-w-0 flex-1 truncate text-xs font-medium tracking-tight text-foreground">
          {node.label}
        </span>
        <span
          className={cn(
            "shrink-0 font-mono text-3xs tabular-nums",
            waiting ? "text-tone-warning-fg" : "text-muted-foreground/80",
          )}
        >
          {durText}
        </span>
      </div>

      {liveLine && (
        <p className="line-clamp-1 text-2xs leading-snug text-muted-foreground">{liveLine}</p>
      )}

      {!compact && (
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
            <span className="rounded-sm bg-tone-warning/10 px-1.5 py-px text-3xs text-tone-warning-fg ring-1 ring-inset ring-tone-warning/25">
              waiting {laid.waitingIds.length}
            </span>
          ) : (
            laid.dependencyIds.length > 0 && (
              <span className="inline-flex items-center gap-0.5 rounded-sm bg-muted/60 px-1.5 py-px text-3xs text-muted-foreground ring-1 ring-inset ring-border/60">
                <GitBranch className="size-2.5" />
                {laid.dependencyIds.length}
              </span>
            )
          )}
        </div>
      )}
    </button>
  );
}

// ───────────────────────── canvas ────────────────────────────────

function GraphCanvas({
  layout,
  dims,
  compact,
  ordinals,
  selectedId,
  showCritical,
  onSelect,
  onClear,
  onWidthChange,
}: {
  layout: Layout;
  dims: LayoutDims;
  compact: boolean;
  ordinals: Map<string, number>;
  selectedId: string | null;
  showCritical: boolean;
  onSelect: (id: string) => void;
  onClear: () => void;
  onWidthChange: (width: number) => void;
}) {
  const anyRunning = layout.order.some((n) => n.running);
  const now = useNow(anyRunning ? 1_000 : 0);
  const idPrefix = domSafeId(React.useId());
  const [scale, setScale] = React.useState(1);
  const [dragging, setDragging] = React.useState(false);
  const rootRef = React.useRef<HTMLDivElement>(null);
  const scrollRef = React.useRef<HTMLDivElement>(null);
  const userZoomedRef = React.useRef(false);
  // Scroll offsets to apply once the scaled content has re-rendered, so a zoom
  // keeps its anchor point (cursor / viewport centre / node) in place.
  const pendingScrollRef = React.useRef<{ left: number; top: number } | null>(null);
  const scaleRef = React.useRef(scale);
  React.useLayoutEffect(() => {
    scaleRef.current = scale;
  }, [scale]);

  const fullW = layout.width + PAD * 2;
  const fullH = layout.height + PAD * 2;

  const fit = React.useCallback(() => {
    const el = scrollRef.current;
    if (!el) return;
    const sx = el.clientWidth / fullW;
    const sy = Math.max(0, el.clientHeight - CANVAS_PT - CANVAS_PB) / fullH;
    setScale(Math.min(1, clampScale(Math.min(sx, sy))));
  }, [fullW, fullH]);

  const fitAndReset = React.useCallback(() => {
    userZoomedRef.current = false;
    fit();
  }, [fit]);

  React.useEffect(() => {
    fit();
  }, [fit]);

  React.useEffect(() => {
    const el = scrollRef.current;
    if (!el) return;
    onWidthChange(el.clientWidth);
    if (typeof ResizeObserver === "undefined") return;
    let timer = 0;
    const ro = new ResizeObserver((entries) => {
      const width = entries[0]?.contentRect.width ?? el.clientWidth;
      onWidthChange(width);
      window.clearTimeout(timer);
      timer = window.setTimeout(() => {
        if (!userZoomedRef.current) fit();
      }, 100);
    });
    ro.observe(el);
    return () => {
      window.clearTimeout(timer);
      ro.disconnect();
    };
  }, [fit, onWidthChange]);

  React.useLayoutEffect(() => {
    const el = scrollRef.current;
    const pending = pendingScrollRef.current;
    if (!el || !pending) return;
    pendingScrollRef.current = null;
    el.scrollLeft = pending.left;
    el.scrollTop = pending.top;
  }, [scale]);

  /** Zoom to `next`, keeping the content point under viewport offset (vx, vy) fixed. */
  const zoomAnchored = React.useCallback((next: number, vx: number, vy: number) => {
    const el = scrollRef.current;
    if (!el) return;
    const s = scaleRef.current;
    const target = clampScale(next);
    if (target === s) return;
    const ratio = target / s;
    const cx = el.scrollLeft + vx;
    // The top padding is unscaled, so the scaled content origin sits CANVAS_PT below scrollTop 0.
    const cy = el.scrollTop + vy - CANVAS_PT;
    pendingScrollRef.current = {
      left: el.scrollLeft + (ratio - 1) * cx,
      top: el.scrollTop + (ratio - 1) * cy,
    };
    userZoomedRef.current = true;
    setScale(target);
  }, []);

  const zoomAtCenter = React.useCallback(
    (delta: number) => {
      const el = scrollRef.current;
      if (!el) return;
      zoomAnchored(scaleRef.current + delta, el.clientWidth / 2, el.clientHeight / 2);
    },
    [zoomAnchored],
  );

  const zoomToNode = React.useCallback((laid: LaidNode) => {
    const el = scrollRef.current;
    if (!el) return;
    const target = 1;
    const cx = (laid.x + PAD + dims.nodeW / 2) * target;
    const cy = (laid.y + PAD + dims.nodeH / 2) * target + CANVAS_PT;
    pendingScrollRef.current = {
      left: cx - el.clientWidth / 2,
      top: cy - el.clientHeight / 2,
    };
    userZoomedRef.current = true;
    if (scaleRef.current === target) {
      el.scrollLeft = pendingScrollRef.current.left;
      el.scrollTop = pendingScrollRef.current.top;
      pendingScrollRef.current = null;
    } else {
      setScale(target);
    }
  }, [dims.nodeW, dims.nodeH]);

  // Native listener: React registers root wheel listeners as passive, so
  // preventDefault from an onWheel prop can't stop browser page zoom.
  React.useEffect(() => {
    const el = scrollRef.current;
    if (!el) return;
    const handleWheel = (e: WheelEvent) => {
      if (!e.ctrlKey && !e.metaKey) return;
      e.preventDefault();
      const rect = el.getBoundingClientRect();
      zoomAnchored(scaleRef.current - e.deltaY * 0.001, e.clientX - rect.left, e.clientY - rect.top);
    };
    el.addEventListener("wheel", handleWheel, { passive: false });
    return () => el.removeEventListener("wheel", handleWheel);
  }, [zoomAnchored]);

  // Dotted-grid parallax: the grid drifts at 60% of scroll speed. Driven off a
  // ref + rAF so scrolling never re-renders the React tree.
  const parallaxFrame = React.useRef(0);
  const handleScroll = React.useCallback(() => {
    const el = scrollRef.current;
    if (!el || parallaxFrame.current) return;
    parallaxFrame.current = window.requestAnimationFrame(() => {
      parallaxFrame.current = 0;
      const target = scrollRef.current;
      if (!target) return;
      target.style.backgroundPosition = `${-target.scrollLeft * 0.6}px ${-target.scrollTop * 0.6}px`;
    });
  }, []);
  React.useEffect(() => () => window.cancelAnimationFrame(parallaxFrame.current), []);

  // Drag-to-pan on empty canvas + two-pointer pinch zoom.
  const pointers = React.useRef(new Map<number, { x: number; y: number }>());
  const dragRef = React.useRef<{
    x: number;
    y: number;
    left: number;
    top: number;
    moved: boolean;
  } | null>(null);
  const pinchRef = React.useRef<{ dist: number; scale: number } | null>(null);
  const suppressClickRef = React.useRef(false);

  const isEmptyCanvasTarget = (target: EventTarget | null) =>
    !(target instanceof Element && target.closest("button"));

  const onPointerDown = (e: React.PointerEvent<HTMLDivElement>) => {
    const el = scrollRef.current;
    if (!el) return;
    if (e.pointerType === "touch") {
      pointers.current.set(e.pointerId, { x: e.clientX, y: e.clientY });
      if (pointers.current.size === 2) {
        const [a, b] = [...pointers.current.values()];
        pinchRef.current = { dist: Math.hypot(a.x - b.x, a.y - b.y), scale: scaleRef.current };
        dragRef.current = null;
        setDragging(false);
        return;
      }
    }
    if (e.button !== 0 || !isEmptyCanvasTarget(e.target)) return;
    dragRef.current = { x: e.clientX, y: e.clientY, left: el.scrollLeft, top: el.scrollTop, moved: false };
    el.setPointerCapture?.(e.pointerId);
    setDragging(true);
  };

  const onPointerMove = (e: React.PointerEvent<HTMLDivElement>) => {
    const el = scrollRef.current;
    if (!el) return;
    if (e.pointerType === "touch" && pointers.current.has(e.pointerId)) {
      pointers.current.set(e.pointerId, { x: e.clientX, y: e.clientY });
      const pinch = pinchRef.current;
      if (pinch && pointers.current.size === 2) {
        const [a, b] = [...pointers.current.values()];
        const dist = Math.hypot(a.x - b.x, a.y - b.y);
        if (dist > 0 && pinch.dist > 0) {
          const rect = el.getBoundingClientRect();
          zoomAnchored(
            pinch.scale * (dist / pinch.dist),
            (a.x + b.x) / 2 - rect.left,
            (a.y + b.y) / 2 - rect.top,
          );
        }
        return;
      }
    }
    const drag = dragRef.current;
    if (!drag) return;
    const dx = e.clientX - drag.x;
    const dy = e.clientY - drag.y;
    if (!drag.moved && Math.hypot(dx, dy) > 3) drag.moved = true;
    el.scrollLeft = drag.left - dx;
    el.scrollTop = drag.top - dy;
  };

  const endPointer = (e: React.PointerEvent<HTMLDivElement>) => {
    pointers.current.delete(e.pointerId);
    if (pointers.current.size < 2) pinchRef.current = null;
    const drag = dragRef.current;
    if (drag) {
      if (drag.moved) suppressClickRef.current = true;
      dragRef.current = null;
      setDragging(false);
    }
  };

  const onCanvasClick = (e: React.MouseEvent<HTMLDivElement>) => {
    if (suppressClickRef.current) {
      suppressClickRef.current = false;
      return;
    }
    if (isEmptyCanvasTarget(e.target)) onClear();
  };

  // Keyboard: zoom, clear, and arrow-key traversal of the DAG.
  const onKeyDown = (e: React.KeyboardEvent<HTMLDivElement>) => {
    const key = e.key;
    let handled = true;
    if (key === "Escape") onClear();
    else if (key === "+" || key === "=") zoomAtCenter(0.1);
    else if (key === "-") zoomAtCenter(-0.1);
    else if (key === "0") fitAndReset();
    else if (key === "ArrowUp" || key === "ArrowDown") {
      const order = layout.order;
      const idx = selectedId ? order.findIndex((n) => n.node.id === selectedId) : -1;
      const next =
        idx < 0 ? (key === "ArrowDown" ? order[0] : order[order.length - 1]) : order[idx + (key === "ArrowDown" ? 1 : -1)];
      if (next) onSelect(next.node.id);
    } else if (key === "ArrowLeft" || key === "ArrowRight") {
      if (!selectedId) {
        if (layout.order[0]) onSelect(layout.order[0].node.id);
      } else if (key === "ArrowLeft") {
        const parent = layout.edges.find((ed) => ed.kind === "spawn" && ed.to === selectedId);
        if (parent) onSelect(parent.from);
      } else {
        const kids = layout.edges.filter((ed) => ed.kind === "spawn" && ed.from === selectedId).map((ed) => ed.to);
        const first = layout.order.find((n) => kids.includes(n.node.id));
        if (first) onSelect(first.node.id);
      }
    } else handled = false;
    if (handled) {
      e.preventDefault();
      e.stopPropagation();
    }
  };

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

  const describe = (laid: LaidNode): string => {
    const parts: string[] = [];
    parts.push(isWorkingNode(laid) ? "working" : isWaitingNode(laid) ? "waiting" : laid.node.status || "unknown");
    parts.push(durationText(laid, now));
    const parent = layout.edges.find((ed) => ed.kind === "spawn" && ed.to === laid.node.id);
    if (parent) parts.push(`spawned by ${displayNodeName(layout.nodes.get(parent.from)?.node, parent.from)}`);
    if (laid.dependencyIds.length > 0) {
      parts.push(
        `depends on ${laid.dependencyIds.map((id) => displayNodeName(layout.nodes.get(id)?.node, id)).join(", ")}`,
      );
    }
    return parts.join(", ");
  };

  const markerSpawn = `${idPrefix}-arrow-spawn`;
  const markerDep = `${idPrefix}-arrow-dep`;
  const markerCritical = `${idPrefix}-arrow-critical`;
  const zoomBtn =
    "grid size-6 place-items-center rounded text-muted-foreground hover:bg-muted hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/60";

  return (
    <div
      ref={rootRef}
      role="group"
      aria-label="Sub-agent graph"
      tabIndex={0}
      onKeyDown={onKeyDown}
      className="relative flex h-full min-h-0 flex-1 flex-col outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring/40"
    >
      <div className="absolute right-3 top-3 z-10 flex items-center gap-1 rounded-md border border-border/60 bg-card/90 p-0.5 shadow-sm backdrop-blur">
        <button type="button" onClick={() => zoomAtCenter(-0.1)} className={zoomBtn} aria-label="Zoom out">
          <Minus className="size-3.5" />
        </button>
        <span className="w-9 text-center font-mono text-3xs tabular-nums text-muted-foreground">
          {Math.round(scale * 100)}%
        </span>
        <button type="button" onClick={() => zoomAtCenter(0.1)} className={zoomBtn} aria-label="Zoom in">
          <Plus className="size-3.5" />
        </button>
        <button type="button" onClick={fitAndReset} className={zoomBtn} aria-label="Fit to view">
          <Maximize2 className="size-3.5" />
        </button>
      </div>

      <Legend className="absolute bottom-3 left-3 z-10" />

      <div
        ref={scrollRef}
        onScroll={handleScroll}
        onClick={onCanvasClick}
        onPointerDown={onPointerDown}
        onPointerMove={onPointerMove}
        onPointerUp={endPointer}
        onPointerCancel={endPointer}
        className={cn(
          "min-h-0 flex-1 overflow-auto [touch-action:pan-x_pan-y]",
          "bg-[radial-gradient(circle_at_1px_1px,color-mix(in_oklch,var(--color-border)_60%,transparent)_1px,transparent_0)] [background-size:22px_22px]",
          dragging ? "cursor-grabbing select-none" : "cursor-grab",
        )}
      >
        <div style={{ paddingTop: CANVAS_PT, paddingBottom: CANVAS_PB }}>
          <div style={{ width: fullW * scale, height: fullH * scale }}>
            <div
              className="relative origin-top-left"
              style={{ width: fullW, height: fullH, transform: `scale(${scale})` }}
            >
              <svg className="pointer-events-none absolute inset-0" width={fullW} height={fullH} fill="none">
                <defs>
                  <marker
                    id={markerSpawn}
                    viewBox="0 0 8 8"
                    refX="6"
                    refY="4"
                    markerWidth="6"
                    markerHeight="6"
                    orient="auto-start-reverse"
                  >
                    <path d="M0,0 L8,4 L0,8 z" className="fill-muted-foreground/70" />
                  </marker>
                  <marker
                    id={markerDep}
                    viewBox="0 0 8 8"
                    refX="6"
                    refY="4"
                    markerWidth="6"
                    markerHeight="6"
                    orient="auto-start-reverse"
                  >
                    <path d="M0,0 L8,4 L0,8 z" className="fill-tone-warning" />
                  </marker>
                  <marker
                    id={markerCritical}
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
                  const flowing = !onCritical && isWorkingNode(b);
                  const x1 = a.x + dims.nodeW + PAD;
                  const y1 = a.y + dims.nodeH / 2 + PAD;
                  const x2 = b.x + PAD;
                  const y2 = b.y + dims.nodeH / 2 + PAD;
                  const marker = onCritical
                    ? `url(#${markerCritical})`
                    : isDep
                      ? `url(#${markerDep})`
                      : `url(#${markerSpawn})`;
                  return (
                    <path
                      key={e.id}
                      data-edge-id={e.id}
                      d={edgePath(x1, y1, x2, y2)}
                      className={cn(
                        onCritical
                          ? "stroke-[color:var(--color-primary)]"
                          : isDep
                            ? "stroke-tone-warning"
                            : "stroke-muted-foreground/60",
                        active ? "opacity-90" : criticalActive ? "opacity-10" : "opacity-45",
                        flowing && "animate-dash-flow",
                      )}
                      strokeWidth={onCritical ? 2.5 : isDep ? 1.5 : 1.75}
                      strokeDasharray={flowing ? "6 6" : isDep && !onCritical ? "4 4" : undefined}
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
                const descId = `${idPrefix}-${domSafeId(ln.node.id)}-desc`;
                return (
                  <React.Fragment key={ln.node.id}>
                    <NodeCard
                      laid={ln}
                      dims={dims}
                      compact={compact}
                      ordinal={ordinals.get(ln.node.id)}
                      selected={selectedId === ln.node.id}
                      dimmed={dimmed}
                      onCritical={onCritical}
                      describedBy={descId}
                      onSelect={onSelect}
                      onZoomTo={zoomToNode}
                      now={now}
                    />
                    <span id={descId} className="sr-only">
                      {describe(ln)}
                    </span>
                  </React.Fragment>
                );
              })}
            </div>
          </div>
        </div>
      </div>
    </div>
  );
}

// ───────────────────────── summary + legend ──────────────────────

function Pill({
  label,
  value,
  valueClassName,
}: {
  label: string;
  value: React.ReactNode;
  valueClassName?: string;
}) {
  return (
    <span className="inline-flex shrink-0 items-center gap-1.5 rounded-[5px] bg-muted/40 px-2 py-1 text-2xs text-muted-foreground ring-1 ring-inset ring-border/60">
      <span className="uppercase tracking-[0.08em] text-muted-foreground/70">{label}</span>
      <span className={cn("font-mono tabular-nums", valueClassName ?? "text-foreground")}>{value}</span>
    </span>
  );
}

function SummaryStrip({ graph, layout }: { graph: SubagentGraph; layout: Layout }) {
  const flats = layout.order;
  const totalTokens = flats.reduce((a, f) => a + Number(f.node.totalTokens || 0n), 0);
  const waiting = flats.filter(isWaitingNode).length;
  const running = flats.filter(isWorkingNode).length;
  const failed = flats.filter((f) => f.node.status === "failed").length;
  const dependencies = graph.edges.filter((e) => e.kind === "depends-on").length;
  const subagents = graph.nodes.filter((n) => n.kind !== "root").length;

  return (
    <div className="flex min-w-0 flex-1 items-center gap-1.5 overflow-x-auto [scrollbar-width:none]">
      <Pill label="subagents" value={subagents} />
      {dependencies > 0 && <Pill label="deps" value={dependencies} />}
      <Pill label="tokens" value={formatTokens(totalTokens)} />
      {running > 0 && <Pill label="running" value={running} valueClassName="text-[color:var(--color-primary)]" />}
      {waiting > 0 && <Pill label="waiting" value={waiting} valueClassName="text-tone-warning-fg" />}
      {failed > 0 && <Pill label="failed" value={failed} valueClassName="text-tone-danger-fg" />}
    </div>
  );
}

function Legend({ className }: { className?: string }) {
  return (
    <div
      className={cn(
        "flex items-center gap-3 rounded-md border border-border/60 bg-card/90 px-2 py-1 text-3xs text-muted-foreground/80 backdrop-blur",
        className,
      )}
    >
      <span className="inline-flex items-center gap-1">
        <svg width="22" height="8" className="overflow-visible">
          <line x1="0" y1="4" x2="20" y2="4" className="stroke-muted-foreground/60" strokeWidth="1.75" />
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
            className="stroke-tone-warning"
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

function RunSummary({ graph, layout }: { graph: SubagentGraph; layout: Layout }) {
  const root = layout.nodes.get(graph.rootId)?.node ?? layout.order.find((n) => n.node.kind === "root")?.node;
  const subs = layout.order.filter((n) => n.node.kind !== "root");
  const working = subs.filter(isWorkingNode).length;
  const waiting = subs.filter(isWaitingNode).length;
  const failed = subs.filter((n) => n.node.status === "failed").length;
  const done = subs.filter((n) => !n.running && isTerminalStatus(n.node.status) && n.node.status !== "failed").length;
  const tokens = subs.reduce((a, n) => a + Number(n.node.totalTokens || 0n), 0);

  const stats: { label: string; value: string; className?: string }[] = [
    { label: "Sub-agents", value: String(subs.length) },
    { label: "Working", value: String(working), className: working > 0 ? "text-[color:var(--color-primary)]" : undefined },
    { label: "Waiting", value: String(waiting), className: waiting > 0 ? "text-tone-warning-fg" : undefined },
    { label: "Done", value: String(done), className: done > 0 ? "text-tone-success" : undefined },
    { label: "Failed", value: String(failed), className: failed > 0 ? "text-tone-danger-fg" : undefined },
  ];
  if (tokens > 0) stats.push({ label: "Sub-agent tokens", value: formatTokens(tokens) });
  if (layout.criticalDurationMs > 0)
    stats.push({ label: "Critical path", value: formatDuration(layout.criticalDurationMs) });

  return (
    <div className="@container px-4 py-3">
      <div className="mb-2 flex items-center gap-2">
        <AgentTypeChip type={root?.label ?? "root"} short root />
        <span className="min-w-0 flex-1 truncate text-[13px] font-medium tracking-tight text-foreground">
          {root?.label || "Run"} · delegation summary
        </span>
        <span className="shrink-0 text-2xs text-muted-foreground/70">Click a node for details</span>
      </div>
      <dl className="grid grid-cols-2 gap-x-6 gap-y-1 text-2xs @[420px]:grid-cols-3 @[640px]:grid-cols-4">
        {stats.map((s) => (
          <div key={s.label} className="flex min-w-0 items-baseline gap-1.5">
            <dt className="shrink-0 text-muted-foreground/80">{s.label}</dt>
            <dd className={cn("min-w-0 truncate font-mono tabular-nums text-foreground/90", s.className)}>
              {s.value}
            </dd>
          </div>
        ))}
      </dl>
    </div>
  );
}

function NodeDetail({
  node,
  ordinal,
  entries,
  graph,
}: {
  node: SubagentGraphNode;
  ordinal: number | undefined;
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
    <div className="px-4 py-3">
      <div className="mb-2 flex items-start gap-2">
        <AgentTypeChip type={agentType(node)} short root={node.kind === "root"} />
        <Ordinal n={ordinal} className="mt-px text-2xs" />
        <div className="min-w-0 flex-1">
          <div className="truncate text-[13px] font-medium tracking-tight text-foreground">
            {node.label}
          </div>
          {node.description && node.description !== node.label && (
            <p className="mt-0.5 text-xs leading-relaxed text-muted-foreground">
              {node.description}
            </p>
          )}
        </div>
      </div>

      {node.lineageReason && (
        <p className="mb-2 border-l-2 border-tone-warning/60 pl-2 text-2xs text-tone-warning-fg">
          {node.lineageReason}
        </p>
      )}

      {node.lastParentMessage && (
        <p className="mb-2 border-l-2 border-border pl-2 text-2xs leading-relaxed text-muted-foreground">
          {node.lastParentMessage}
        </p>
      )}

      {metrics.length > 0 && (
        <dl className="grid grid-cols-2 gap-x-6 gap-y-1 text-2xs sm:grid-cols-3 lg:grid-cols-4">
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
          <span className="text-3xs uppercase tracking-[0.08em] text-muted-foreground/70">Handoffs</span>
          {node.handoffs.map((h, i) => (
            <span
              key={i}
              className="rounded-sm bg-tone-purple/10 px-1.5 py-px font-mono text-3xs text-tone-purple-fg ring-1 ring-inset ring-tone-purple/30"
            >
              ↗ {h}
            </span>
          ))}
        </div>
      )}

      {(dependencyIds.length > 0 || node.waitingOn.length > 0) && (
        <div className="mt-2 flex flex-wrap items-center gap-1.5">
          <span className="text-3xs uppercase tracking-[0.08em] text-muted-foreground/70">DAG</span>
          {dependencyIds.map((id) => (
            <span
              key={`dep-${id}`}
              className="rounded-sm bg-muted/50 px-1.5 py-px font-mono text-3xs text-muted-foreground ring-1 ring-inset ring-border/60"
            >
              after {displayNodeName(nodesById[id], id)}
            </span>
          ))}
          {node.waitingOn.map((id) => (
            <span
              key={`wait-${id}`}
              className="rounded-sm bg-tone-warning/10 px-1.5 py-px font-mono text-3xs text-tone-warning-fg ring-1 ring-inset ring-tone-warning/25"
            >
              waiting {id}
            </span>
          ))}
        </div>
      )}

      {detailEntries.length > 0 && (
        <div className="mt-3">
          <p className="mb-1 flex items-center gap-1 text-3xs font-semibold uppercase tracking-[0.1em] text-muted-foreground/70">
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

  // Width 0 means "not measured yet" (or jsdom); keep the full card until we know.
  const [containerWidth, setContainerWidth] = React.useState(0);
  const compact = containerWidth > 0 && containerWidth < COMPACT_BREAKPOINT;
  const dims = compact ? COMPACT_DIMS : DEFAULT_DIMS;

  const layout = React.useMemo(() => {
    if (!graph || graph.nodes.length === 0) return null;
    return buildLayout(graph, dims);
  }, [graph, dims]);
  const ordinals = React.useMemo(() => (layout ? assignOrdinals(layout) : new Map<string, number>()), [layout]);

  const [selectedId, setSelectedId] = React.useState<string | null>(null);
  const [showCritical, setShowCritical] = React.useState(false);

  const resolvedId = selectedId && layout?.nodes.has(selectedId) ? selectedId : null;
  const selected = resolvedId ? layout?.nodes.get(resolvedId)?.node ?? null : null;

  const toggleSelect = React.useCallback((id: string) => {
    setSelectedId((current) => (current === id ? null : id));
  }, []);
  const clearSelection = React.useCallback(() => setSelectedId(null), []);

  if (empty || !layout) {
    return (
      <div className="px-4 py-6 text-xs text-muted-foreground">
        <p className="font-medium text-foreground">No subagents observed</p>
        <p className="mt-1 text-2xs">
          The graph appears once this run spawns subagents or records inline subagent activity.
        </p>
      </div>
    );
  }

  const criticalAvailable = layout.criticalIds.size > 1;
  // react-resizable-panels needs ResizeObserver; without it (jsdom) fall back to a fixed split.
  const resizable = typeof ResizeObserver !== "undefined";

  const canvas = (
    <GraphCanvas
      layout={layout}
      dims={dims}
      compact={compact}
      ordinals={ordinals}
      selectedId={resolvedId}
      showCritical={showCritical}
      onSelect={toggleSelect}
      onClear={clearSelection}
      onWidthChange={setContainerWidth}
    />
  );
  const detail = selected ? (
    <NodeDetail node={selected} ordinal={ordinals.get(selected.id)} entries={entries} graph={graph!} />
  ) : (
    <RunSummary graph={graph!} layout={layout} />
  );
  const detailClass = "min-h-0 overflow-auto border-t border-border/50 bg-card/40";

  return (
    <div className="flex h-full flex-col">
      <div className="flex shrink-0 items-center gap-3 border-b border-border/50 px-3 py-2">
        <h3 className="shrink-0 text-xs font-medium tracking-tight text-foreground">Subagent graph</h3>
        {criticalAvailable && (
          <button
            type="button"
            onClick={() => setShowCritical((v) => !v)}
            aria-pressed={showCritical}
            title="Highlight the chain of agents that drove the run's wall-clock duration"
            className={cn(
              "inline-flex shrink-0 items-center gap-1.5 rounded-[5px] px-2 py-1 text-2xs ring-1 ring-inset transition-colors",
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

      {resizable ? (
        <ResizablePanelGroup orientation="vertical" className="min-h-0 flex-1">
          <ResizablePanel id="graph-canvas" defaultSize="62%" minSize="30%" className="flex min-h-0 flex-col">
            {canvas}
          </ResizablePanel>
          <ResizableHandle withHandle />
          <ResizablePanel id="graph-detail" minSize="20%" className={detailClass}>
            {detail}
          </ResizablePanel>
        </ResizablePanelGroup>
      ) : (
        <>
          {canvas}
          <div className={cn(detailClass, "max-h-[38%] shrink-0")}>{detail}</div>
        </>
      )}
    </div>
  );
}
