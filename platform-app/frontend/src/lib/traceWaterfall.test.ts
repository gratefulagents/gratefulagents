import { create } from "@bufbuild/protobuf";
import { describe, expect, it } from "vitest";

import {
  assembleRows,
  barGeometry,
  baseKind,
  buildWaterfall,
  computeTicks,
  fmtDurationUs,
  fmtOffsetUs,
  fmtTokensCompact,
  fmtUsd,
  spanCostUsd,
  spanDisplayName,
  type RowFilter,
} from "@/lib/traceWaterfall";
import { TraceSpanSchema, type TraceSpan } from "@/rpc/platform/service_pb";

function mkSpan(opts: {
  id: string;
  parent?: string;
  kind?: string;
  name?: string;
  start?: number;
  dur?: number;
  tags?: Record<string, string>;
  isError?: boolean;
}): TraceSpan {
  return create(TraceSpanSchema, {
    spanId: opts.id,
    parentSpanId: opts.parent ?? "",
    operationName: opts.name ?? opts.kind ?? opts.id,
    kind: opts.kind ?? "tool.bash",
    startTimeUnixUs: BigInt(opts.start ?? 0),
    durationUs: BigInt(opts.dur ?? 1000),
    isError: opts.isError ?? false,
    tags: Object.entries(opts.tags ?? {}).map(([key, value]) => ({ key, value })),
  });
}

const NO_FILTER: RowFilter = { query: "", kinds: null, errorsOnly: false };

describe("buildWaterfall", () => {
  it("builds a depth-first tree ordered by start time with subtree stats", () => {
    const wf = buildWaterfall([
      mkSpan({ id: "b-child", parent: "b", start: 250, isError: true }),
      mkSpan({ id: "a", start: 0 }),
      mkSpan({ id: "b", start: 200 }),
      mkSpan({ id: "b-child-2", parent: "b", start: 300 }),
    ]);
    expect(wf.nodes.map((n) => n.span.spanId)).toEqual(["a", "b", "b-child", "b-child-2"]);
    expect(wf.nodes.map((n) => n.depth)).toEqual([0, 0, 1, 1]);
    const b = wf.byId.get("b")!;
    expect(b.childCount).toBe(2);
    expect(b.descendantCount).toBe(2);
    expect(b.descendantErrors).toBe(1);
    expect(wf.byId.get("b-child")!.index).toBe(2);
  });

  it("promotes orphans (unknown parent) to roots", () => {
    const wf = buildWaterfall([
      mkSpan({ id: "x", parent: "missing", start: 10 }),
      mkSpan({ id: "y", start: 0 }),
    ]);
    expect(wf.nodes.map((n) => n.depth)).toEqual([0, 0]);
    expect(wf.nodes[0].span.spanId).toBe("y");
  });

  it("computes the trace time range", () => {
    const wf = buildWaterfall([
      mkSpan({ id: "a", start: 100, dur: 50 }),
      mkSpan({ id: "b", start: 20, dur: 500 }),
    ]);
    expect(wf.minStartUs).toBe(20n);
    expect(wf.maxEndUs).toBe(520n);
  });
});

describe("turn grouping", () => {
  const turnTrace = [
    mkSpan({ id: "pre", start: 0, kind: "session" }),
    mkSpan({ id: "gen1", start: 100, dur: 100, kind: "llm.generation", tags: { "gen.turn": "1", "gen.cost_usd": "0.01", "gen.cost_known": "true" } }),
    mkSpan({ id: "tool1", start: 220, dur: 30, kind: "tool.bash" }),
    mkSpan({ id: "gen2", start: 300, dur: 100, kind: "llm.generation", tags: { "gen.turn": "2" } }),
    mkSpan({ id: "tool2", start: 420, dur: 30, kind: "tool.grep", isError: true }),
  ];

  it("derives turn windows from root-level gen.turn anchors", () => {
    const wf = buildWaterfall(turnTrace);
    expect(wf.groups.map((g) => g.label)).toEqual(["Turn 1", "Turn 2"]);
    expect(wf.preludeRootIds).toEqual(["pre"]);
    expect(wf.groups[0].rootSpanIds).toEqual(["gen1", "tool1"]);
    expect(wf.groups[1].rootSpanIds).toEqual(["gen2", "tool2"]);
    expect(wf.groups[0].genCount).toBe(1);
    expect(wf.groups[0].toolCount).toBe(1);
    expect(wf.groups[0].costUsd).toBeCloseTo(0.01);
    expect(wf.groups[1].errorCount).toBe(1);
    expect(wf.groups[0].endUs).toBe(250n); // max end within the window
  });

  it("merges consecutive same-turn anchors (retries) into one group", () => {
    const wf = buildWaterfall([
      mkSpan({ id: "g1", start: 0, kind: "llm.generation", tags: { "gen.turn": "1" } }),
      mkSpan({ id: "g1b", start: 50, kind: "llm.generation", tags: { "gen.turn": "1" } }),
      mkSpan({ id: "g2", start: 200, kind: "llm.generation", tags: { "gen.turn": "2" } }),
    ]);
    expect(wf.groups.map((g) => g.key)).toEqual(["turn-1", "turn-2"]);
    expect(wf.groups[0].rootSpanIds).toEqual(["g1", "g1b"]);
  });

  it("skips grouping when fewer than two distinct turns exist", () => {
    const wf = buildWaterfall([
      mkSpan({ id: "g1", start: 0, kind: "llm.generation", tags: { "gen.turn": "1" } }),
      mkSpan({ id: "t1", start: 100, kind: "tool.bash" }),
    ]);
    expect(wf.groups).toEqual([]);
  });
});

describe("assembleRows", () => {
  const spans = [
    mkSpan({ id: "root", start: 0, dur: 1000, kind: "agent.captain" }),
    mkSpan({ id: "gen", parent: "root", start: 10, kind: "llm.generation" }),
    mkSpan({ id: "tool", parent: "root", start: 120, kind: "tool.bash", isError: true }),
    mkSpan({ id: "nested", parent: "tool", start: 130, kind: "tool.grep" }),
  ];

  it("emits the full tree without filters", () => {
    const wf = buildWaterfall(spans);
    const rows = assembleRows(wf, {
      filter: NO_FILTER,
      collapsedSpans: new Set(),
      groupTurns: false,
      collapsedGroups: new Set(),
    });
    expect(rows).toHaveLength(4);
    expect(rows.every((r) => r.type === "span" && r.matched)).toBe(true);
  });

  it("collapse hides the subtree", () => {
    const wf = buildWaterfall(spans);
    const rows = assembleRows(wf, {
      filter: NO_FILTER,
      collapsedSpans: new Set(["tool"]),
      groupTurns: false,
      collapsedGroups: new Set(),
    });
    expect(rows.map((r) => (r.type === "span" ? r.node.span.spanId : "group"))).toEqual([
      "root",
      "gen",
      "tool",
    ]);
  });

  it("keeps ancestors of matches visible but unmatched, ignoring collapse", () => {
    const wf = buildWaterfall(spans);
    const rows = assembleRows(wf, {
      filter: { query: "grep", kinds: null, errorsOnly: false },
      collapsedSpans: new Set(["tool"]), // ignored while filtering
      groupTurns: false,
      collapsedGroups: new Set(),
    });
    const ids = rows.map((r) => (r.type === "span" ? r.node.span.spanId : "group"));
    expect(ids).toEqual(["root", "tool", "nested"]);
    const matchFlags = rows.map((r) => (r.type === "span" ? r.matched : null));
    expect(matchFlags).toEqual([false, false, true]);
  });

  it("errorsOnly keeps error spans and their ancestors", () => {
    const wf = buildWaterfall(spans);
    const rows = assembleRows(wf, {
      filter: { query: "", kinds: null, errorsOnly: true },
      collapsedSpans: new Set(),
      groupTurns: false,
      collapsedGroups: new Set(),
    });
    const ids = rows.map((r) => (r.type === "span" ? r.node.span.spanId : "group"));
    expect(ids).toEqual(["root", "tool"]);
  });

  it("drops group headers whose children are fully filtered out", () => {
    const wf = buildWaterfall([
      mkSpan({ id: "g1", start: 0, kind: "llm.generation", tags: { "gen.turn": "1" } }),
      mkSpan({ id: "g2", start: 100, kind: "llm.generation", tags: { "gen.turn": "2" } }),
      mkSpan({ id: "t2", start: 150, kind: "tool.bash", isError: true }),
    ]);
    const rows = assembleRows(wf, {
      filter: { query: "", kinds: null, errorsOnly: true },
      collapsedSpans: new Set(),
      groupTurns: true,
      collapsedGroups: new Set(),
    });
    expect(rows.map((r) => (r.type === "group" ? r.group.key : r.node.span.spanId))).toEqual([
      "turn-2",
      "t2",
    ]);
  });

  it("collapsed groups render only their header", () => {
    const wf = buildWaterfall([
      mkSpan({ id: "g1", start: 0, kind: "llm.generation", tags: { "gen.turn": "1" } }),
      mkSpan({ id: "g2", start: 100, kind: "llm.generation", tags: { "gen.turn": "2" } }),
    ]);
    const rows = assembleRows(wf, {
      filter: NO_FILTER,
      collapsedSpans: new Set(),
      groupTurns: true,
      collapsedGroups: new Set(["turn-1"]),
    });
    expect(rows.map((r) => (r.type === "group" ? r.group.key : r.node.span.spanId))).toEqual([
      "turn-1",
      "turn-2",
      "g2",
    ]);
  });
});

describe("time axis and geometry", () => {
  it("computes nice ticks bounded by maxTicks", () => {
    const ticks = computeTicks(10_000_000); // 10s
    expect(ticks.length).toBeGreaterThan(2);
    expect(ticks.length).toBeLessThanOrEqual(9);
    expect(ticks[0].offsetUs).toBe(0);
    const step = ticks[1].offsetUs - ticks[0].offsetUs;
    expect([1, 2, 5]).toContain(step / 10 ** Math.floor(Math.log10(step)));
  });

  it("positions bars inside the view window and clips outside", () => {
    const span = mkSpan({ id: "a", start: 500, dur: 250 });
    const full = barGeometry(span, 0n, { startUs: 0, endUs: 1000 });
    expect(full?.leftPct).toBeCloseTo(50);
    expect(full?.widthPct).toBeCloseTo(25);
    expect(barGeometry(span, 0n, { startUs: 800, endUs: 1000 })).toBeNull();
    const clamped = barGeometry(span, 0n, { startUs: 600, endUs: 700 })!;
    expect(clamped.leftPct).toBe(0);
    expect(clamped.widthPct).toBeCloseTo(100);
  });
});

describe("formatting + classification", () => {
  it("formats durations adaptively", () => {
    expect(fmtDurationUs(500)).toBe("<1ms");
    expect(fmtDurationUs(2_000)).toBe("2ms");
    expect(fmtDurationUs(2_500_000)).toBe("2.5s");
    expect(fmtDurationUs(95_000_000)).toBe("1m 35s");
    expect(fmtDurationUs(3_900_000_000)).toBe("1h 05m");
  });

  it("formats offsets adaptively", () => {
    expect(fmtOffsetUs(0)).toBe("0");
    expect(fmtOffsetUs(500)).toBe("500µs");
    expect(fmtOffsetUs(30_000)).toBe("30ms");
    expect(fmtOffsetUs(12_500_000)).toBe("12.5s");
    expect(fmtOffsetUs(90_000_000)).toBe("1m30s");
    expect(fmtOffsetUs(120_000_000)).toBe("2m");
  });

  it("formats tokens and cost", () => {
    expect(fmtTokensCompact(950)).toBe("950");
    expect(fmtTokensCompact(12_340)).toBe("12.3k");
    expect(fmtTokensCompact(2_500_000)).toBe("2.5M");
    expect(fmtUsd(0.0432)).toBe("$0.0432");
    expect(fmtUsd(1.5)).toBe("$1.50");
  });

  it("classifies span kinds", () => {
    expect(baseKind("llm.generation")).toBe("llm");
    expect(baseKind("tool.Bash")).toBe("tool");
    expect(baseKind("subagent.explore")).toBe("subagent");
    expect(baseKind("api.retry")).toBe("retry");
    expect(baseKind("session")).toBe("session");
    expect(baseKind("weird")).toBe("other");
  });

  it("derives display names and cost", () => {
    const gen = mkSpan({
      id: "g",
      kind: "llm.generation",
      tags: { "gen.resolved_model": "claude-opus-4", "gen.cost_usd": "0.02", "gen.cost_known": "true" },
    });
    expect(spanDisplayName(gen)).toBe("claude-opus-4");
    expect(spanCostUsd(gen)).toBeCloseTo(0.02);
    expect(spanDisplayName(mkSpan({ id: "t", kind: "tool.Bash" }))).toBe("Bash");
    expect(spanCostUsd(mkSpan({ id: "t2", kind: "tool.Bash" }))).toBeUndefined();
  });
});
