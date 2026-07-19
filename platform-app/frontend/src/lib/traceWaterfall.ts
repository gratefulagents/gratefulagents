import type { TraceSpan } from "@/rpc/platform/service_pb";
import { traceTagValue } from "@/lib/traceUsage";

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

/** A span placed in the waterfall tree. */
export type WaterfallNode = {
  span: TraceSpan;
  /** Position in the depth-first nodes array. */
  index: number;
  /** Tree depth. Roots are 0. */
  depth: number;
  /** Direct children count. */
  childCount: number;
  /** Errors anywhere in this node's subtree (excluding the node itself). */
  descendantErrors: number;
  /** Total spans in this node's subtree (excluding the node itself). */
  descendantCount: number;
  /** Effective parent span id ("" for roots / orphans promoted to root). */
  parentId: string;
};

/** A synthetic "Turn N" group of root-level nodes. */
export type TurnGroup = {
  key: string;
  label: string;
  startUs: bigint;
  endUs: bigint;
  /** Root node span ids belonging to this group (subtrees included implicitly). */
  rootSpanIds: string[];
  spanCount: number;
  genCount: number;
  toolCount: number;
  costUsd: number;
  errorCount: number;
};

export type Waterfall = {
  /** Depth-first ordered nodes (children sorted by start time). */
  nodes: WaterfallNode[];
  byId: Map<string, WaterfallNode>;
  minStartUs: bigint;
  maxEndUs: bigint;
  /** Turn groups derived from gen.turn tags; empty when grouping is not meaningful. */
  groups: TurnGroup[];
  /** Root span ids that precede the first turn group (rendered ungrouped). */
  preludeRootIds: string[];
};

export type WaterfallRow =
  | { type: "group"; group: TurnGroup }
  | { type: "span"; node: WaterfallNode; matched: boolean };

// ---------------------------------------------------------------------------
// Kind classification
// ---------------------------------------------------------------------------

export type BaseKind =
  | "llm"
  | "tool"
  | "subagent"
  | "agent"
  | "session"
  | "handoff"
  | "guardrail"
  | "compaction"
  | "retry"
  | "phase"
  | "other";

export const KIND_ORDER: BaseKind[] = [
  "llm",
  "tool",
  "subagent",
  "agent",
  "session",
  "handoff",
  "guardrail",
  "compaction",
  "retry",
  "phase",
  "other",
];

export function baseKind(kind: string): BaseKind {
  if (kind === "llm.generation") return "llm";
  if (kind === "session" || kind.startsWith("session")) return "session";
  if (kind === "handoff") return "handoff";
  if (kind === "compaction") return "compaction";
  if (kind === "api.retry") return "retry";
  if (kind.startsWith("tool.") || kind === "tool" || kind === "function") return "tool";
  if (kind.startsWith("subagent.") || kind === "subagent") return "subagent";
  if (kind.startsWith("agent.") || kind === "agent") return "agent";
  if (kind.startsWith("guardrail.")) return "guardrail";
  if (kind.startsWith("phase.")) return "phase";
  return "other";
}

/** Short display name for a span, without the kind prefix noise. */
export function spanDisplayName(span: TraceSpan): string {
  const kind = span.kind;
  if (kind === "llm.generation") {
    return spanModel(span) ?? "generation";
  }
  if (kind.startsWith("tool.")) return kind.slice("tool.".length);
  if (kind.startsWith("subagent.")) return kind.slice("subagent.".length);
  if (kind.startsWith("agent.")) return kind.slice("agent.".length);
  if (kind.startsWith("guardrail.")) return kind.slice("guardrail.".length);
  if (kind.startsWith("phase.")) {
    return traceTagValue(span, "phase.name", "phase.kind") ?? kind.slice("phase.".length);
  }
  return span.operationName || kind;
}

export function spanModel(span: TraceSpan): string | undefined {
  return traceTagValue(
    span,
    "gen.model",
    "gen.resolved_model",
    "gen.requested_model",
    "session.model",
    "subagent.model",
  );
}

export function spanCostUsd(span: TraceSpan): number | undefined {
  const genCost = traceTagValue(span, "gen.cost_usd");
  const genKnown = traceTagValue(span, "gen.cost_known");
  if (genCost !== undefined && (genKnown === "true" || Number(genCost) > 0)) {
    const n = Number(genCost);
    if (Number.isFinite(n) && n > 0) return n;
  }
  for (const key of ["subagent.cost_usd", "session.cost_usd"]) {
    const raw = traceTagValue(span, key);
    if (raw !== undefined) {
      const n = Number(raw);
      if (Number.isFinite(n) && n > 0) return n;
    }
  }
  return undefined;
}

export type SpanTokens = { input: number; output: number };

export function spanTokens(span: TraceSpan): SpanTokens | undefined {
  const input = traceTagValue(
    span,
    "gen.input_tokens",
    "gen.prompt_tokens",
    "session.input_tokens",
    "subagent.input_tokens",
  );
  const output = traceTagValue(
    span,
    "gen.output_tokens",
    "gen.completion_tokens",
    "session.output_tokens",
    "subagent.output_tokens",
  );
  if (input === undefined && output === undefined) return undefined;
  const inN = Number(input ?? 0);
  const outN = Number(output ?? 0);
  if (!Number.isFinite(inN) && !Number.isFinite(outN)) return undefined;
  if (inN <= 0 && outN <= 0) return undefined;
  return { input: Math.max(inN, 0), output: Math.max(outN, 0) };
}

// ---------------------------------------------------------------------------
// Tree building
// ---------------------------------------------------------------------------

export function buildWaterfall(spans: TraceSpan[]): Waterfall {
  const byId = new Map<string, TraceSpan>();
  for (const s of spans) byId.set(s.spanId, s);

  const childrenOf = new Map<string, TraceSpan[]>();
  const roots: TraceSpan[] = [];
  for (const s of spans) {
    const parentKnown = s.parentSpanId !== "" && byId.has(s.parentSpanId) && s.parentSpanId !== s.spanId;
    if (parentKnown) {
      const list = childrenOf.get(s.parentSpanId);
      if (list) list.push(s);
      else childrenOf.set(s.parentSpanId, [s]);
    } else {
      roots.push(s);
    }
  }
  const byStart = (a: TraceSpan, b: TraceSpan) => {
    if (a.startTimeUnixUs === b.startTimeUnixUs) return a.spanId < b.spanId ? -1 : 1;
    return a.startTimeUnixUs < b.startTimeUnixUs ? -1 : 1;
  };
  roots.sort(byStart);
  for (const list of childrenOf.values()) list.sort(byStart);

  const nodes: WaterfallNode[] = [];
  const nodeById = new Map<string, WaterfallNode>();

  const walk = (span: TraceSpan, depth: number, parentId: string): { count: number; errors: number } => {
    const kids = childrenOf.get(span.spanId) ?? [];
    const node: WaterfallNode = {
      span,
      index: nodes.length,
      depth,
      childCount: kids.length,
      descendantErrors: 0,
      descendantCount: 0,
      parentId,
    };
    nodes.push(node);
    nodeById.set(span.spanId, node);
    let count = 0;
    let errors = 0;
    for (const kid of kids) {
      const sub = walk(kid, depth + 1, span.spanId);
      count += 1 + sub.count;
      errors += (kid.isError ? 1 : 0) + sub.errors;
    }
    node.descendantCount = count;
    node.descendantErrors = errors;
    return { count, errors };
  };
  for (const root of roots) walk(root, 0, "");

  let minStartUs = 0n;
  let maxEndUs = 0n;
  if (spans.length > 0) {
    minStartUs = spans[0].startTimeUnixUs;
    maxEndUs = spans[0].startTimeUnixUs + spans[0].durationUs;
    for (const s of spans) {
      if (s.startTimeUnixUs < minStartUs) minStartUs = s.startTimeUnixUs;
      const end = s.startTimeUnixUs + s.durationUs;
      if (end > maxEndUs) maxEndUs = end;
    }
  }

  const { groups, preludeRootIds } = buildTurnGroups(roots, nodeById, maxEndUs);

  return { nodes, byId: nodeById, minStartUs, maxEndUs, groups, preludeRootIds };
}

// ---------------------------------------------------------------------------
// Turn grouping
// ---------------------------------------------------------------------------

function buildTurnGroups(
  roots: TraceSpan[],
  nodeById: Map<string, WaterfallNode>,
  traceEndUs: bigint,
): { groups: TurnGroup[]; preludeRootIds: string[] } {
  // Anchor groups on root-level generation spans carrying gen.turn.
  const anchors = roots.filter(
    (s) => s.kind === "llm.generation" && traceTagValue(s, "gen.turn") !== undefined,
  );
  const turnValues = new Set(anchors.map((s) => traceTagValue(s, "gen.turn")));
  if (anchors.length < 2 || turnValues.size < 2) {
    return { groups: [], preludeRootIds: [] };
  }

  type Window = { turn: string; startUs: bigint; endUs: bigint; rootSpanIds: string[] };
  const windows: Window[] = [];
  for (const anchor of anchors) {
    const turn = traceTagValue(anchor, "gen.turn") ?? "?";
    const last = windows[windows.length - 1];
    if (last && last.turn === turn) continue; // retries share a turn
    windows.push({ turn, startUs: anchor.startTimeUnixUs, endUs: traceEndUs, rootSpanIds: [] });
    if (last) last.endUs = anchor.startTimeUnixUs;
  }

  const preludeRootIds: string[] = [];
  for (const root of roots) {
    const w = windows.find(
      (win) => root.startTimeUnixUs >= win.startUs && root.startTimeUnixUs < win.endUs,
    );
    // The anchor itself starts exactly at win.startUs and lands in its window.
    if (w) w.rootSpanIds.push(root.spanId);
    else if (root.startTimeUnixUs < windows[0].startUs) preludeRootIds.push(root.spanId);
    else windows[windows.length - 1].rootSpanIds.push(root.spanId);
  }

  const groups: TurnGroup[] = windows.map((w) => {
    let spanCount = 0;
    let genCount = 0;
    let toolCount = 0;
    let costUsd = 0;
    let errorCount = 0;
    let endUs = w.startUs;
    for (const id of w.rootSpanIds) {
      const node = nodeById.get(id);
      if (!node) continue;
      spanCount += 1 + node.descendantCount;
      errorCount += (node.span.isError ? 1 : 0) + node.descendantErrors;
      const kind = baseKind(node.span.kind);
      if (kind === "llm") genCount++;
      if (kind === "tool") toolCount++;
      const cost = spanCostUsd(node.span);
      if (cost !== undefined) costUsd += cost;
      const end = node.span.startTimeUnixUs + node.span.durationUs;
      if (end > endUs) endUs = end;
    }
    return {
      key: `turn-${w.turn}`,
      label: `Turn ${w.turn}`,
      startUs: w.startUs,
      endUs,
      rootSpanIds: w.rootSpanIds,
      spanCount,
      genCount,
      toolCount,
      costUsd,
      errorCount,
    };
  });

  return { groups, preludeRootIds };
}

// ---------------------------------------------------------------------------
// Row assembly (grouping + filtering + collapse)
// ---------------------------------------------------------------------------

export type RowFilter = {
  query: string;
  /** null = all kinds enabled. */
  kinds: Set<BaseKind> | null;
  errorsOnly: boolean;
};

export function nodeMatches(node: WaterfallNode, filter: RowFilter): boolean {
  if (filter.kinds && !filter.kinds.has(baseKind(node.span.kind))) return false;
  if (filter.errorsOnly && !node.span.isError) return false;
  if (filter.query) {
    const q = filter.query.toLowerCase();
    const hay = `${node.span.operationName} ${node.span.kind} ${spanModel(node.span) ?? ""}`.toLowerCase();
    if (!hay.includes(q)) return false;
  }
  return true;
}

const hasActiveFilter = (f: RowFilter): boolean =>
  f.query !== "" || f.errorsOnly || f.kinds !== null;

/**
 * Assemble the visible rows: optional turn-group headers, tree collapse, and
 * filters. Nodes that match are shown; ancestors of matches are kept for
 * context (marked matched=false). While a filter is active, collapse state is
 * ignored so matches are never hidden.
 */
export function assembleRows(
  wf: Waterfall,
  opts: {
    filter: RowFilter;
    collapsedSpans: ReadonlySet<string>;
    groupTurns: boolean;
    collapsedGroups: ReadonlySet<string>;
  },
): WaterfallRow[] {
  const { filter } = opts;
  const filtering = hasActiveFilter(filter);
  const rows: WaterfallRow[] = [];

  // Depth-first over a node's subtree honoring collapse + filters.
  const pushSubtree = (rootId: string) => {
    const root = wf.byId.get(rootId);
    if (!root) return;
    const start = root.index;
    // Subtree occupies a contiguous slice in depth-first order.
    const end = start + 1 + root.descendantCount;
    // visibility pass: which indices match?
    const slice = wf.nodes.slice(start, end);
    const visible = new Array<boolean>(slice.length).fill(false);
    const matched = new Array<boolean>(slice.length).fill(false);
    for (let i = slice.length - 1; i >= 0; i--) {
      matched[i] = nodeMatches(slice[i], filter);
      visible[i] = matched[i];
    }
    // Ancestors of visible nodes stay visible for context.
    const ancestorStack: number[] = [];
    for (let i = 0; i < slice.length; i++) {
      const node = slice[i];
      while (ancestorStack.length > 0 && slice[ancestorStack[ancestorStack.length - 1]].depth >= node.depth) {
        ancestorStack.pop();
      }
      if (visible[i]) for (const ai of ancestorStack) visible[ai] = true;
      ancestorStack.push(i);
    }
    // Emit respecting collapse (ignored while filtering).
    let skipDeeperThan: number | null = null;
    for (let i = 0; i < slice.length; i++) {
      const node = slice[i];
      if (skipDeeperThan !== null) {
        if (node.depth > skipDeeperThan) continue;
        skipDeeperThan = null;
      }
      if (!visible[i]) continue;
      rows.push({ type: "span", node, matched: matched[i] });
      if (!filtering && node.childCount > 0 && opts.collapsedSpans.has(node.span.spanId)) {
        skipDeeperThan = node.depth;
      }
    }
  };

  const useGroups = opts.groupTurns && wf.groups.length > 0;
  if (!useGroups) {
    for (const node of wf.nodes) {
      if (node.depth === 0) pushSubtree(node.span.spanId);
    }
    return rows;
  }

  for (const id of wf.preludeRootIds) pushSubtree(id);
  for (const group of wf.groups) {
    const before = rows.length;
    // Collapsed groups (while not filtering) show only the header.
    const collapsed = !filtering && opts.collapsedGroups.has(group.key);
    rows.push({ type: "group", group });
    if (!collapsed) {
      for (const id of group.rootSpanIds) pushSubtree(id);
    }
    // Drop the header when filtering removed every child row.
    if (filtering && rows.length === before + 1) rows.pop();
  }
  return rows;
}

// ---------------------------------------------------------------------------
// Time axis
// ---------------------------------------------------------------------------

export type TimeTick = { offsetUs: number; label: string };

const TICK_STEPS_US: number[] = (() => {
  const steps: number[] = [];
  for (let pow = 0; pow <= 12; pow++) {
    for (const m of [1, 2, 5]) steps.push(m * 10 ** pow);
  }
  return steps;
})();

/** Compute nice ticks (offsets relative to view start) for a µs range. */
export function computeTicks(rangeUs: number, maxTicks = 8): TimeTick[] {
  if (!(rangeUs > 0)) return [{ offsetUs: 0, label: fmtOffsetUs(0) }];
  const step = TICK_STEPS_US.find((s) => rangeUs / s <= maxTicks) ?? TICK_STEPS_US[TICK_STEPS_US.length - 1];
  const ticks: TimeTick[] = [];
  for (let t = 0; t <= rangeUs; t += step) {
    ticks.push({ offsetUs: t, label: fmtOffsetUs(t) });
  }
  return ticks;
}

// ---------------------------------------------------------------------------
// Formatting
// ---------------------------------------------------------------------------

export function fmtDurationUs(us: bigint | number): string {
  const ms = Number(us) / 1000;
  if (ms < 1) return "<1ms";
  if (ms < 1000) return `${Math.round(ms)}ms`;
  const s = ms / 1000;
  if (s < 60) return `${s.toFixed(s < 10 ? 1 : 0)}s`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}m ${Math.round(s % 60)}s`;
  const h = Math.floor(m / 60);
  return `${h}h ${String(m % 60).padStart(2, "0")}m`;
}

/** Offset label for the ruler: compact, unit-adaptive. */
export function fmtOffsetUs(us: number): string {
  if (us === 0) return "0";
  if (us < 1000) return `${Math.round(us)}µs`;
  const ms = us / 1000;
  if (ms < 1000) return `${Math.round(ms)}ms`;
  const s = ms / 1000;
  if (s < 60) return `${Number.isInteger(s) ? s : s.toFixed(1)}s`;
  const m = Math.floor(s / 60);
  const rest = Math.round(s % 60);
  return rest === 0 ? `${m}m` : `${m}m${String(rest).padStart(2, "0")}s`;
}

export function fmtTokensCompact(n: number): string {
  if (n < 1000) return String(n);
  if (n < 1_000_000) {
    const k = n / 1000;
    return `${k >= 100 ? Math.round(k) : k.toFixed(1)}k`;
  }
  const m = n / 1_000_000;
  return `${m >= 100 ? Math.round(m) : m.toFixed(1)}M`;
}

export function fmtUsd(n: number): string {
  if (!Number.isFinite(n)) return "—";
  if (n >= 1) return `$${n.toFixed(2)}`;
  return `$${n.toFixed(4)}`;
}

// ---------------------------------------------------------------------------
// Geometry
// ---------------------------------------------------------------------------

export type ViewWindow = { startUs: number; endUs: number };

/** Percent position of a span bar inside a view window; null when outside. */
export function barGeometry(
  span: TraceSpan,
  minStartUs: bigint,
  view: ViewWindow,
): { leftPct: number; widthPct: number } | null {
  const range = view.endUs - view.startUs;
  if (!(range > 0)) return null;
  const start = Number(span.startTimeUnixUs - minStartUs);
  const end = start + Number(span.durationUs);
  if (end < view.startUs || start > view.endUs) return null;
  const clampedStart = Math.max(start, view.startUs);
  const clampedEnd = Math.min(Math.max(end, clampedStart), view.endUs);
  const leftPct = ((clampedStart - view.startUs) / range) * 100;
  const widthPct = Math.max(((clampedEnd - clampedStart) / range) * 100, 0.25);
  return { leftPct, widthPct: Math.min(widthPct, 100 - leftPct) };
}
