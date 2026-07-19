/**
 * Pure layout + DAG-analysis logic for the subagent graph.
 *
 * Kept free of React and heavy app imports so it can be unit-tested in
 * isolation and reused by other clients. The component layer
 * (SubagentGraphView.tsx) consumes these helpers for rendering.
 */
import type { SubagentGraph, SubagentGraphEdge, SubagentGraphNode } from "@/rpc/platform/service_pb";

// ───────────────────────── layout constants ──────────────────────
export const NODE_W = 264;
export const NODE_H = 96;
export const H_GAP = 76; // horizontal gap between generations
export const V_GAP = 22; // vertical gap between sibling rows
export const PAD = 28; // canvas padding around the graph

/** Node/gap geometry for a layout pass. The graph tab uses the defaults; the
 * chat feed's embedded mini-DAG passes smaller dimensions. */
export interface LayoutDims {
  nodeW: number;
  nodeH: number;
  hGap: number;
  vGap: number;
}

export const DEFAULT_DIMS: LayoutDims = { nodeW: NODE_W, nodeH: NODE_H, hGap: H_GAP, vGap: V_GAP };

// ───────────────────────── helpers ───────────────────────────────

export function unique(values: string[]): string[] {
  return Array.from(new Set(values.filter(Boolean)));
}

export function taskIDToNodeID(id: string): string {
  return id.startsWith("task:") ? id : `task:${id}`;
}

export function nodeIDToTaskID(id: string): string {
  return id.startsWith("task:") ? id.slice("task:".length) : id;
}

export function isTerminalStatus(s: string): boolean {
  return (
    s === "completed" ||
    s === "succeeded" ||
    s === "failed" ||
    s === "stopped" ||
    s === "cancelled" ||
    s === "canceled"
  );
}

/** Match the graph renderer's definition of live work. Duration is populated
 * only when a task finishes, but protects clients from stale terminal status. */
export function isRunningSubagentNode(
  node: Pick<SubagentGraphNode, "durationMs" | "status">,
): boolean {
  return node.durationMs === 0n && !isTerminalStatus(node.status);
}

/** Statuses for tasks that are alive but gated on other work (DAG scheduling). */
export function isWaitingStatus(s: string): boolean {
  const status = s.toLowerCase();
  return status === "waiting" || status === "pending" || status === "queued";
}

export function dependencyIdsForNode(
  node: SubagentGraphNode,
  edgeDependencyIds: string[],
): string[] {
  const waiting = node.waitingOn.map((id) => taskIDToNodeID(id));
  return unique([
    ...waiting,
    ...edgeDependencyIds.filter((id) => node.waitingOn.includes(nodeIDToTaskID(id))),
  ]);
}

// ───────────────────────── edge geometry ─────────────────────────

export function edgePath(x1: number, y1: number, x2: number, y2: number): string {
  const dx = Math.max(36, Math.abs(x2 - x1) * 0.5);
  return `M${x1},${y1} C${x1 + dx},${y1} ${x2 - dx},${y2} ${x2},${y2}`;
}

// ───────────────────────── layout types ──────────────────────────

export interface LaidNode {
  node: SubagentGraphNode;
  x: number;
  y: number;
  depth: number;
  running: boolean;
  dependencyIds: string[];
  waitingIds: string[];
}

export interface LaidEdge {
  id: string;
  kind: "spawn" | "depends-on";
  from: string;
  to: string;
}

export interface Layout {
  nodes: Map<string, LaidNode>;
  order: LaidNode[];
  edges: LaidEdge[];
  width: number;
  height: number;
  criticalIds: Set<string>;
  criticalDurationMs: number;
}

/**
 * Longest-duration chain through the combined DAG (spawn + depends-on edges).
 * This is the wall-clock "critical path": the sequence of agents that gated the
 * run's total duration. Returns the node ids on that chain and its total.
 */
export function computeCriticalPath(
  nodes: Map<string, LaidNode>,
  edges: LaidEdge[],
): { ids: Set<string>; durationMs: number } {
  const preds: Record<string, string[]> = {};
  for (const e of edges) (preds[e.to] ??= []).push(e.from);

  const durOf = (id: string) => {
    const n = nodes.get(id)?.node;
    return n ? Number(n.durationMs || 0n) : 0;
  };

  const finish = new Map<string, number>();
  const prev = new Map<string, string | null>();
  const state = new Map<string, 0 | 1 | 2>();

  const visit = (id: string): number => {
    if (state.get(id) === 2) return finish.get(id) ?? 0;
    if (state.get(id) === 1) return 0; // cycle guard
    state.set(id, 1);
    let best = 0;
    let bestPrev: string | null = null;
    for (const p of preds[id] ?? []) {
      if (!nodes.has(p)) continue;
      const f = visit(p);
      if (f > best) {
        best = f;
        bestPrev = p;
      }
    }
    const total = best + durOf(id);
    finish.set(id, total);
    prev.set(id, bestPrev);
    state.set(id, 2);
    return total;
  };

  let endId: string | null = null;
  let endVal = 0;
  for (const id of nodes.keys()) {
    const f = visit(id);
    if (f > endVal) {
      endVal = f;
      endId = id;
    }
  }

  const ids = new Set<string>();
  if (endVal <= 0) return { ids, durationMs: 0 };
  for (let cur = endId; cur; cur = prev.get(cur) ?? null) ids.add(cur);
  return { ids, durationMs: endVal };
}

export function buildLayout(graph: SubagentGraph, dims: LayoutDims = DEFAULT_DIMS): Layout {
  const { nodeW, nodeH, hGap, vGap } = dims;
  const byId: Record<string, SubagentGraphNode> = {};
  const childrenOf: Record<string, string[]> = {};
  const parentOf: Record<string, string> = {};
  const dependenciesOf: Record<string, string[]> = {};

  for (const n of graph.nodes) byId[n.id] = n;

  for (const e of graph.edges as SubagentGraphEdge[]) {
    if (e.kind === "depends-on") {
      dependenciesOf[e.to] = unique([...(dependenciesOf[e.to] ?? []), e.from]);
      continue;
    }
    (childrenOf[e.from] ??= []).push(e.to);
    parentOf[e.to] = e.from;
  }

  const tsOf = (id: string) => (byId[id] ? Number(byId[id].timestampUnix) : 0);
  for (const pid of Object.keys(childrenOf)) {
    childrenOf[pid] = unique(childrenOf[pid]).sort((a, b) => tsOf(a) - tsOf(b));
  }

  // Roots = nodes that nobody spawned. Surface the declared root first.
  const roots = graph.nodes
    .map((n) => n.id)
    .filter((id) => !parentOf[id])
    .sort((a, b) => {
      if (a === graph.rootId) return -1;
      if (b === graph.rootId) return 1;
      const ra = byId[a]?.kind === "root" ? 0 : 1;
      const rb = byId[b]?.kind === "root" ? 0 : 1;
      if (ra !== rb) return ra - rb;
      return tsOf(a) - tsOf(b);
    });

  const pos = new Map<string, { x: number; y: number; depth: number }>();
  const seen = new Set<string>();
  let leaf = 0;

  const place = (id: string, depth: number): number => {
    if (seen.has(id)) return pos.get(id)?.y ?? 0;
    seen.add(id);
    const kids = (childrenOf[id] ?? []).filter((k) => !seen.has(k) && byId[k]);
    const x = depth * (nodeW + hGap);
    let y: number;
    if (kids.length === 0) {
      y = leaf * (nodeH + vGap);
      leaf += 1;
    } else {
      const childYs = kids.map((k) => place(k, depth + 1));
      y = (childYs[0] + childYs[childYs.length - 1]) / 2;
    }
    pos.set(id, { x, y, depth });
    return y;
  };

  for (const r of roots) place(r, 0);
  // Anything left (cycles / detached) gets stacked at depth 0.
  for (const n of graph.nodes) if (!seen.has(n.id)) place(n.id, 0);

  // Dependency-aware columns: a node must sit at least one column to the right
  // of everything it depends on, so `depends-on` edges always read left→right
  // instead of looping backwards. Relaxation is bounded by node count to stay
  // safe even if the data contains an accidental cycle.
  const colOf = new Map<string, number>();
  for (const [id, p] of pos) colOf.set(id, p.depth);
  const depEntries = Object.entries(dependenciesOf);
  for (let pass = 0; pass < graph.nodes.length; pass++) {
    let changed = false;
    for (const [to, deps] of depEntries) {
      if (!colOf.has(to)) continue;
      for (const from of deps) {
        if (!colOf.has(from)) continue;
        const want = colOf.get(from)! + 1;
        if (want > colOf.get(to)!) {
          colOf.set(to, want);
          changed = true;
        }
      }
    }
    if (!changed) break;
  }

  // Fan-in re-centering: the spawn-tree pass stacks every spawned sibling as a
  // leaf, so a join node (one that depends on its siblings) ends up at the
  // bottom of the stack and the whole DAG reads like a sequential pipeline.
  // Center each dependent node on its dependencies instead, then resolve
  // overlaps per column so parallel branches keep their spacing.
  const yOf = new Map<string, number>();
  for (const [id, p] of pos) yOf.set(id, p.y);
  const columns = new Map<number, string[]>();
  for (const [id] of pos) {
    const col = colOf.get(id) ?? 0;
    const bucket = columns.get(col);
    if (bucket) bucket.push(id);
    else columns.set(col, [id]);
  }
  for (const col of [...columns.keys()].sort((a, b) => a - b)) {
    const ids = columns.get(col)!;
    for (const id of ids) {
      const deps = (dependenciesOf[id] ?? []).filter((d) => yOf.has(d));
      if (deps.length === 0) continue;
      const mean = deps.reduce((a, d) => a + yOf.get(d)!, 0) / deps.length;
      yOf.set(id, mean);
    }
    // De-overlap top-to-bottom, preserving the desired ordering.
    ids.sort((a, b) => yOf.get(a)! - yOf.get(b)! || tsOf(a) - tsOf(b));
    let minY = -Infinity;
    for (const id of ids) {
      const y = Math.max(yOf.get(id)!, minY);
      yOf.set(id, y);
      minY = y + nodeH + vGap;
    }
  }

  const nodes = new Map<string, LaidNode>();
  for (const n of graph.nodes) {
    const p = pos.get(n.id);
    if (!p) continue;
    const col = colOf.get(n.id) ?? p.depth;
    const running = isRunningSubagentNode(n);
    nodes.set(n.id, {
      node: n,
      x: col * (nodeW + hGap),
      y: yOf.get(n.id) ?? p.y,
      depth: col,
      running,
      dependencyIds: dependenciesOf[n.id] ?? [],
      waitingIds: dependencyIdsForNode(n, dependenciesOf[n.id] ?? []),
    });
  }

  const edges: LaidEdge[] = [];
  for (const [from, kids] of Object.entries(childrenOf)) {
    for (const to of kids) {
      if (nodes.has(from) && nodes.has(to)) {
        edges.push({ id: `spawn:${from}->${to}`, kind: "spawn", from, to });
      }
    }
  }
  for (const [to, deps] of Object.entries(dependenciesOf)) {
    for (const from of deps) {
      if (nodes.has(from) && nodes.has(to)) {
        edges.push({ id: `dep:${from}->${to}`, kind: "depends-on", from, to });
      }
    }
  }

  let width = 0;
  let height = 0;
  for (const ln of nodes.values()) {
    width = Math.max(width, ln.x + nodeW);
    height = Math.max(height, ln.y + nodeH);
  }

  const order = [...nodes.values()].sort((a, b) => a.y - b.y || a.x - b.x);
  const critical = computeCriticalPath(nodes, edges);
  return {
    nodes,
    order,
    edges,
    width,
    height,
    criticalIds: critical.ids,
    criticalDurationMs: critical.durationMs,
  };
}
