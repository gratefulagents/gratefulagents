import { create } from "@bufbuild/protobuf";
import { describe, expect, it } from "vitest";

import { buildLayout } from "@/lib/subagentGraphLayout";
import {
  SubagentGraphSchema,
  SubagentGraphNodeSchema,
  SubagentGraphEdgeSchema,
} from "@/rpc/platform/service_pb";

function node(init: {
  id: string;
  kind?: string;
  parentId?: string;
  status?: string;
  durationMs?: number;
  timestampUnix?: number;
  dependsOn?: string[];
}) {
  return create(SubagentGraphNodeSchema, {
    id: init.id,
    kind: init.kind ?? "subagent",
    parentId: init.parentId ?? "",
    status: init.status ?? "completed",
    durationMs: BigInt(init.durationMs ?? 0),
    timestampUnix: BigInt(init.timestampUnix ?? 0),
    dependsOn: init.dependsOn ?? [],
  });
}

function spawnEdge(from: string, to: string) {
  return create(SubagentGraphEdgeSchema, { id: `${from}->${to}`, from, to, kind: "spawned" });
}

function depEdge(from: string, to: string) {
  return create(SubagentGraphEdgeSchema, { id: `${from}=>${to}`, from, to, kind: "depends-on" });
}

describe("buildLayout", () => {
  it("places depends-on targets to the right of their dependencies", () => {
    // root -> a, root -> b ; b depends-on a. Without dependency-aware columns a
    // and b would share a column and the dep edge would run vertically.
    const graph = create(SubagentGraphSchema, {
      rootId: "root",
      hasSubagents: true,
      nodes: [
        node({ id: "root", kind: "root", status: "running" }),
        node({ id: "a", parentId: "root", timestampUnix: 1 }),
        node({ id: "b", parentId: "root", timestampUnix: 2, dependsOn: ["a"] }),
      ],
      edges: [spawnEdge("root", "a"), spawnEdge("root", "b"), depEdge("a", "b")],
    });

    const layout = buildLayout(graph);
    const a = layout.nodes.get("a")!;
    const b = layout.nodes.get("b")!;
    expect(b.x).toBeGreaterThan(a.x);
  });

  it("computes the critical path as the longest-duration chain", () => {
    // root -> slow(100) -> tail(10)
    //      -> fast(5)
    // critical path is root, slow, tail.
    const graph = create(SubagentGraphSchema, {
      rootId: "root",
      hasSubagents: true,
      nodes: [
        node({ id: "root", kind: "root", status: "running", durationMs: 0 }),
        node({ id: "slow", parentId: "root", durationMs: 100, timestampUnix: 1 }),
        node({ id: "fast", parentId: "root", durationMs: 5, timestampUnix: 2 }),
        node({ id: "tail", parentId: "slow", durationMs: 10, timestampUnix: 3 }),
      ],
      edges: [
        spawnEdge("root", "slow"),
        spawnEdge("root", "fast"),
        spawnEdge("slow", "tail"),
      ],
    });

    const layout = buildLayout(graph);
    expect(layout.criticalDurationMs).toBe(110);
    expect(layout.criticalIds.has("slow")).toBe(true);
    expect(layout.criticalIds.has("tail")).toBe(true);
    expect(layout.criticalIds.has("fast")).toBe(false);
  });

  it("does not loop forever when dependencies form a cycle", () => {
    const graph = create(SubagentGraphSchema, {
      rootId: "root",
      hasSubagents: true,
      nodes: [
        node({ id: "root", kind: "root", status: "running" }),
        node({ id: "a", parentId: "root", durationMs: 10, dependsOn: ["b"] }),
        node({ id: "b", parentId: "root", durationMs: 10, dependsOn: ["a"] }),
      ],
      edges: [spawnEdge("root", "a"), spawnEdge("root", "b"), depEdge("b", "a"), depEdge("a", "b")],
    });

    const layout = buildLayout(graph);
    expect(layout.nodes.size).toBe(3);
  });
});

  it("centers a fan-in join on its dependencies instead of stacking it below them", () => {
    // root spawns 5 parallel researchers + 1 writer that depends on all 5 —
    // the writer must sit to the right, vertically centered on the fan,
    // not dangling at the bottom like a sequential pipeline.
    const researchers = ["r1", "r2", "r3", "r4", "r5"];
    const graph = create(SubagentGraphSchema, {
      rootId: "root",
      nodes: [
        node({ id: "root", kind: "root" }),
        ...researchers.map((id, i) => node({ id, timestampUnix: i + 1 })),
        node({ id: "writer", timestampUnix: 10, dependsOn: researchers }),
      ],
      edges: [
        ...researchers.map((id) => spawnEdge("root", id)),
        spawnEdge("root", "writer"),
        ...researchers.map((id) => depEdge(id, "writer")),
      ],
    });

    const layout = buildLayout(graph);
    const writer = layout.nodes.get("writer")!;
    const rs = researchers.map((id) => layout.nodes.get(id)!);

    // Pushed right of every dependency.
    for (const r of rs) {
      expect(writer.depth).toBeGreaterThan(r.depth);
    }

    // Vertically centered on the fan (within one row of the mean), never below
    // the last researcher.
    const ys = rs.map((r) => r.y);
    const mean = ys.reduce((a, b) => a + b, 0) / ys.length;
    expect(Math.abs(writer.y - mean)).toBeLessThanOrEqual(118); // ≤ NODE_H + V_GAP
    expect(writer.y).toBeLessThan(Math.max(...ys));
    expect(writer.y).toBeGreaterThan(Math.min(...ys));

    // Researchers keep distinct, non-overlapping rows.
    const sorted = [...ys].sort((a, b) => a - b);
    for (let i = 1; i < sorted.length; i++) {
      expect(sorted[i] - sorted[i - 1]).toBeGreaterThanOrEqual(96); // ≥ NODE_H
    }
  });
