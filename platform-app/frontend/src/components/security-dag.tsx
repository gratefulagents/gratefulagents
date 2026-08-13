/**
 * Shared DAG layout and rendering primitives for the security tab.
 *
 * The security tab draws the same workflow graph in two places: the workflow
 * builder (editable draft tasks) and the deterministic execution view (live
 * per-task state). Both use the algorithmic layered layout below — nodes are
 * grouped into topological layers left-to-right, no drag repositioning — so
 * the authored graph and the running graph look identical.
 */

import { useEffect, useId, useRef, useState, type ReactNode } from "react";

/** DagNodeInput is the minimal shape the layout needs. */
export interface DagNodeInput {
  name: string;
  dependsOn: string[];
}

/** Node geometry shared by the builder and the execution view. */
export const DAG_NODE_WIDTH = 200;
export const DAG_NODE_HEIGHT = 64;
export const DAG_LAYER_GAP = 72;
export const DAG_NODE_GAP = 20;
export const DAG_CANVAS_PAD = 24;

/**
 * dagLayers groups nodes into topological layers: layer 0 has no
 * dependencies, each next layer depends only on earlier ones. Nodes trapped
 * in a cycle are appended as a final layer so the graph still renders while
 * validation reports the cycle. Unknown dependency names are ignored.
 */
export function dagLayers<T extends DagNodeInput>(nodes: T[]): T[][] {
  const known = new Set(nodes.map((t) => t.name.trim()));
  const placed = new Map<string, number>();
  const remaining = [...nodes];
  const layers: T[][] = [];
  for (let depth = 0; remaining.length > 0 && depth < nodes.length; depth++) {
    const ready = remaining.filter((t) =>
      t.dependsOn.every((dep) => !known.has(dep) || placed.has(dep)),
    );
    if (ready.length === 0) break;
    layers.push(ready);
    for (const t of ready) {
      placed.set(t.name.trim(), depth);
      remaining.splice(remaining.indexOf(t), 1);
    }
  }
  if (remaining.length > 0) layers.push(remaining);
  return layers;
}

export interface DagLayout {
  /** Top-left corner per node name. */
  positions: Map<string, { x: number; y: number }>;
  width: number;
  height: number;
}

/** dagLayout converts topological layers into absolute canvas positions. */
export function dagLayout<T extends DagNodeInput>(layers: T[][]): DagLayout {
  const positions = new Map<string, { x: number; y: number }>();
  const maxRows = layers.reduce((acc, layer) => Math.max(acc, layer.length), 0);
  const height =
    layers.length > 0
      ? maxRows * (DAG_NODE_HEIGHT + DAG_NODE_GAP) - DAG_NODE_GAP + DAG_CANVAS_PAD * 2
      : 0;
  layers.forEach((layer, layerIndex) => {
    // Vertically center each column so sparse columns sit mid-canvas instead
    // of hugging the top edge.
    const columnHeight = layer.length * (DAG_NODE_HEIGHT + DAG_NODE_GAP) - DAG_NODE_GAP;
    const yStart = DAG_CANVAS_PAD + Math.max(0, (height - DAG_CANVAS_PAD * 2 - columnHeight) / 2);
    layer.forEach((node, nodeIndex) => {
      positions.set(node.name.trim(), {
        x: DAG_CANVAS_PAD + layerIndex * (DAG_NODE_WIDTH + DAG_LAYER_GAP),
        y: yStart + nodeIndex * (DAG_NODE_HEIGHT + DAG_NODE_GAP),
      });
    });
  });
  const width =
    layers.length > 0
      ? layers.length * (DAG_NODE_WIDTH + DAG_LAYER_GAP) - DAG_LAYER_GAP + DAG_CANVAS_PAD * 2
      : 0;
  return { positions, width, height };
}

export interface DagEdge {
  from: string;
  to: string;
  /** Highlighted edges render with the primary color (e.g. forEach fan-out). */
  emphasis?: "default" | "fanout";
}

/** dagEdges lists every drawable dependency edge for the given nodes. */
export function dagEdges<T extends DagNodeInput & { forEach?: string }>(nodes: T[]): DagEdge[] {
  const edges: DagEdge[] = [];
  for (const node of nodes) {
    const to = node.name.trim();
    for (const dep of node.dependsOn) {
      edges.push({ from: dep, to, emphasis: node.forEach === dep ? "fanout" : "default" });
    }
  }
  return edges;
}

/** Horizontal inset so arrowheads land just outside the target node's port. */
const EDGE_END_INSET = 5;

/**
 * DagEdgeLayer renders dependency edges as cubic curves with arrowheads in
 * one SVG sized to the canvas. Edges whose endpoints are not positioned are
 * skipped. Fan-out edges are dashed and tinted with the primary color.
 */
export function DagEdgeLayer({
  edges,
  layout,
  label,
}: {
  edges: DagEdge[];
  layout: DagLayout;
  label: string;
}) {
  // Marker ids must be document-unique: two canvases (builder + execution
  // preview) can be mounted at once.
  const markerId = useId();
  const arrowDefault = `${markerId}-arrow`;
  const arrowFanout = `${markerId}-arrow-fanout`;
  return (
    <svg
      role="img"
      aria-label={label}
      width={layout.width}
      height={layout.height}
      viewBox={`0 0 ${layout.width} ${layout.height}`}
      className="absolute inset-0"
    >
      <defs>
        <marker
          id={arrowDefault}
          viewBox="0 0 8 8"
          refX="7"
          refY="4"
          markerWidth="7"
          markerHeight="7"
          orient="auto-start-reverse"
        >
          <path d="M 0.5 0.5 L 7.5 4 L 0.5 7.5 z" className="fill-muted-foreground/80" />
        </marker>
        <marker
          id={arrowFanout}
          viewBox="0 0 8 8"
          refX="7"
          refY="4"
          markerWidth="7"
          markerHeight="7"
          orient="auto-start-reverse"
        >
          <path d="M 0.5 0.5 L 7.5 4 L 0.5 7.5 z" className="fill-primary/80" />
        </marker>
      </defs>
      {edges
        .filter((edge) => layout.positions.has(edge.from) && layout.positions.has(edge.to))
        .map((edge) => {
          const from = layout.positions.get(edge.from)!;
          const to = layout.positions.get(edge.to)!;
          const x1 = from.x + DAG_NODE_WIDTH;
          const y1 = from.y + DAG_NODE_HEIGHT / 2;
          const x2 = to.x - EDGE_END_INSET;
          const y2 = to.y + DAG_NODE_HEIGHT / 2;
          const mid = (x1 + x2) / 2;
          const fanout = edge.emphasis === "fanout";
          return (
            <path
              key={`${edge.from}->${edge.to}`}
              d={`M ${x1} ${y1} C ${mid} ${y1}, ${mid} ${y2}, ${x2} ${y2}`}
              fill="none"
              strokeWidth={fanout ? 1.75 : 1.5}
              strokeDasharray={fanout ? "5 4" : undefined}
              strokeLinecap="round"
              markerEnd={`url(#${fanout ? arrowFanout : arrowDefault})`}
              className={fanout ? "stroke-primary/80" : "stroke-muted-foreground/70"}
            />
          );
        })}
    </svg>
  );
}

/** edgeMidpoint gives the visual midpoint of an edge (for overlay buttons). */
export function edgeMidpoint(
  layout: DagLayout,
  from: string,
  to: string,
): { x: number; y: number } | null {
  const a = layout.positions.get(from);
  const b = layout.positions.get(to);
  if (!a || !b) return null;
  return {
    x: (a.x + DAG_NODE_WIDTH + b.x) / 2,
    y: (a.y + b.y + DAG_NODE_HEIGHT) / 2,
  };
}

/**
 * DagCanvas is the positioned surface both graph views share: a bordered
 * dot-grid canvas with an absolutely positioned inner area sized by the
 * layout. Children place themselves with the layout's positions. Wide graphs
 * scale down to fit the available width (down to a floor, then scroll) so the
 * whole workflow stays visible without panning. It is a `group/dag` hover
 * group so overlays (e.g. edge-remove buttons) can stay hidden until the
 * pointer is over the canvas.
 */
export function DagCanvas({
  layout,
  children,
  testId,
}: {
  layout: DagLayout;
  children: ReactNode;
  testId?: string;
}) {
  const ref = useRef<HTMLDivElement>(null);
  const [containerWidth, setContainerWidth] = useState(0);

  useEffect(() => {
    const el = ref.current;
    if (!el || typeof ResizeObserver === "undefined") return;
    const observer = new ResizeObserver((entries) => {
      for (const entry of entries) setContainerWidth(entry.contentRect.width);
    });
    observer.observe(el);
    return () => observer.disconnect();
  }, []);

  // Scale to fit the container, but never below the floor — a graph deep
  // enough to hit it scrolls horizontally instead of becoming unreadable.
  const MIN_FIT_SCALE = 0.55;
  const scale =
    containerWidth > 0 && layout.width > containerWidth
      ? Math.max(containerWidth / layout.width, MIN_FIT_SCALE)
      : 1;

  return (
    <div
      ref={ref}
      className="group/dag overflow-auto rounded-xl border bg-muted/20"
      style={{
        backgroundImage:
          "radial-gradient(color-mix(in oklch, var(--muted-foreground) 18%, transparent) 1px, transparent 1px)",
        backgroundSize: "16px 16px",
      }}
      data-testid={testId}
    >
      <div
        className="relative mx-auto"
        style={{ width: layout.width * scale, height: layout.height * scale }}
      >
        <div
          className="absolute left-0 top-0"
          style={{
            width: layout.width,
            height: layout.height,
            transform: scale === 1 ? undefined : `scale(${scale})`,
            transformOrigin: "0 0",
          }}
        >
          {children}
        </div>
      </div>
    </div>
  );
}
