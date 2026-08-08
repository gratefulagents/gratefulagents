import { describe, expect, it } from "vitest";

import {
  DAG_CANVAS_PAD,
  DAG_LAYER_GAP,
  DAG_NODE_WIDTH,
  dagEdges,
  dagLayers,
  dagLayout,
  edgeMidpoint,
} from "@/components/security-dag";

const nodes = [
  { name: "recon", dependsOn: [] },
  { name: "hunt", dependsOn: ["recon"], forEach: "recon" },
  { name: "authz", dependsOn: ["recon"] },
  { name: "triage", dependsOn: ["hunt", "authz"] },
];

describe("dagLayers", () => {
  it("groups nodes into topological layers", () => {
    const layers = dagLayers(nodes);
    expect(layers.map((layer) => layer.map((n) => n.name))).toEqual([
      ["recon"],
      ["hunt", "authz"],
      ["triage"],
    ]);
  });

  it("appends cycle members as a final layer so the graph still renders", () => {
    const layers = dagLayers([
      { name: "a", dependsOn: ["b"] },
      { name: "b", dependsOn: ["a"] },
      { name: "root", dependsOn: [] },
    ]);
    expect(layers[0].map((n) => n.name)).toEqual(["root"]);
    expect(layers[1].map((n) => n.name).sort()).toEqual(["a", "b"]);
  });

  it("ignores unknown dependency names", () => {
    const layers = dagLayers([{ name: "solo", dependsOn: ["ghost"] }]);
    expect(layers).toHaveLength(1);
    expect(layers[0][0].name).toBe("solo");
  });
});

describe("dagLayout", () => {
  it("positions layers left-to-right and sizes the canvas", () => {
    const layout = dagLayout(dagLayers(nodes));
    expect(layout.positions.get("recon")?.x).toBe(DAG_CANVAS_PAD);
    expect(layout.positions.get("hunt")?.x).toBe(
      DAG_CANVAS_PAD + DAG_NODE_WIDTH + DAG_LAYER_GAP,
    );
    expect(layout.width).toBeGreaterThan(0);
    expect(layout.height).toBeGreaterThan(0);
  });

  it("vertically centers sparse columns", () => {
    const layout = dagLayout(dagLayers(nodes));
    // recon (1-node column) sits lower than hunt (first of a 2-node column).
    expect(layout.positions.get("recon")!.y).toBeGreaterThan(
      layout.positions.get("hunt")!.y,
    );
  });

  it("returns an empty layout for no nodes", () => {
    const layout = dagLayout(dagLayers([]));
    expect(layout.width).toBe(0);
    expect(layout.height).toBe(0);
  });
});

describe("dagEdges", () => {
  it("lists one edge per dependency and marks forEach fan-out", () => {
    const edges = dagEdges(nodes);
    expect(edges).toHaveLength(4);
    const fanout = edges.find((e) => e.from === "recon" && e.to === "hunt");
    expect(fanout?.emphasis).toBe("fanout");
    const plain = edges.find((e) => e.from === "recon" && e.to === "authz");
    expect(plain?.emphasis).toBe("default");
  });
});

describe("edgeMidpoint", () => {
  it("returns null when an endpoint is not positioned", () => {
    const layout = dagLayout(dagLayers(nodes));
    expect(edgeMidpoint(layout, "recon", "ghost")).toBeNull();
    expect(edgeMidpoint(layout, "recon", "hunt")).not.toBeNull();
  });
});
