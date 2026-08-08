/**
 * Shared DAG layout and rendering primitives for the security tab.
 *
 * The security tab draws the same workflow graph in two places: the workflow
 * builder (editable draft tasks) and the deterministic execution view (live
 * per-task state). Both use the algorithmic layered layout below — nodes are
 * grouped into topological layers left-to-right, no drag repositioning — so
 * the authored graph and the running graph look identical.
 */

import type { ReactNode } from "react";

/** DagNodeInput is the minimal shape the layout needs. */
export interface DagNodeInput {
  name: string;
  dependsOn: string[];
}

/** Node geometry shared by the builder and the execution view. */
export const DAG_NODE_WIDTH = 176;
export const DAG_NODE_HEIGHT = 52;
export const DAG_LAYER_GAP = 56;
export const DAG_NODE_GAP = 12;
export const DAG_CANVAS_PAD = 12;

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

/**
 * DagEdgeLayer renders dependency edges as cubic curves in one SVG sized to
 * the canvas. Edges whose endpoints are not positioned are skipped.
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
  return (
    <svg
      role="img"
      aria-label={label}
      width={layout.width}
      height={layout.height}
      viewBox={`0 0 ${layout.width} ${layout.height}`}
      className="absolute inset-0"
    >
      {edges
        .filter((edge) => layout.positions.has(edge.from) && layout.positions.has(edge.to))
        .map((edge) => {
          const from = layout.positions.get(edge.from)!;
          const to = layout.positions.get(edge.to)!;
          const x1 = from.x + DAG_NODE_WIDTH;
          const y1 = from.y + DAG_NODE_HEIGHT / 2;
          const x2 = to.x;
          const y2 = to.y + DAG_NODE_HEIGHT / 2;
          const mid = (x1 + x2) / 2;
          const fanout = edge.emphasis === "fanout";
          return (
            <path
              key={`${edge.from}->${edge.to}`}
              d={`M ${x1} ${y1} C ${mid} ${y1}, ${mid} ${y2}, ${x2} ${y2}`}
              fill="none"
              strokeWidth={fanout ? 1.5 : 1.25}
              strokeDasharray={fanout ? "4 3" : undefined}
              className={fanout ? "stroke-primary/60" : "stroke-muted-foreground/40"}
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
 * DagCanvas is the scrollable positioned surface both graph views share:
 * a bordered muted canvas with an absolutely positioned inner area sized by
 * the layout. Children place themselves with the layout's positions.
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
  return (
    <div className="overflow-x-auto rounded-lg border bg-muted/30" data-testid={testId}>
      <div className="relative" style={{ width: layout.width, height: layout.height }}>
        {children}
      </div>
    </div>
  );
}
