import { afterEach, beforeEach, describe, expect, it } from "vitest";

import {
  runTabFromPath,
  getRunTabs,
  openRunTab,
  closeRunTab,
  closeOtherRunTabs,
  closeAllRunTabs,
  moveRunTab,
} from "@/hooks/useRunTabs";

const KEY = "gratefulagents.runtabs.v1";

describe("useRunTabs store", () => {
  beforeEach(() => localStorage.clear());
  afterEach(() => localStorage.clear());

  it("parses run detail paths only", () => {
    expect(runTabFromPath("/runs/demo/run-a")).toEqual({
      path: "/runs/demo/run-a",
      namespace: "demo",
      name: "run-a",
    });
    expect(runTabFromPath("/runs")).toBeNull();
    expect(runTabFromPath("/projects/demo/x")).toBeNull();
    expect(runTabFromPath("/runs/demo/run-a/extra")).toBeNull();
  });

  it("opens tabs in visit order and dedupes", () => {
    openRunTab("/runs/demo/run-a");
    openRunTab("/runs/demo/run-b");
    openRunTab("/runs/demo/run-a"); // revisit — keeps position
    openRunTab("/settings"); // not a run page — ignored
    expect(getRunTabs().map((t) => t.name)).toEqual(["run-a", "run-b"]);
  });

  it("close returns the right neighbor, then left, then null", () => {
    openRunTab("/runs/demo/run-a");
    openRunTab("/runs/demo/run-b");
    openRunTab("/runs/demo/run-c");
    expect(closeRunTab("/runs/demo/run-b")).toBe("/runs/demo/run-c");
    expect(closeRunTab("/runs/demo/run-c")).toBe("/runs/demo/run-a");
    expect(closeRunTab("/runs/demo/run-a")).toBeNull();
    expect(getRunTabs()).toEqual([]);
  });

  it("close of an unknown tab is a no-op", () => {
    openRunTab("/runs/demo/run-a");
    expect(closeRunTab("/runs/demo/other")).toBeNull();
    expect(getRunTabs()).toHaveLength(1);
  });

  it("closeOthers and closeAll", () => {
    openRunTab("/runs/demo/run-a");
    openRunTab("/runs/demo/run-b");
    openRunTab("/runs/demo/run-c");
    closeOtherRunTabs("/runs/demo/run-b");
    expect(getRunTabs().map((t) => t.name)).toEqual(["run-b"]);
    closeAllRunTabs();
    expect(getRunTabs()).toEqual([]);
  });

  it("reorders tabs", () => {
    openRunTab("/runs/demo/run-a");
    openRunTab("/runs/demo/run-b");
    openRunTab("/runs/demo/run-c");
    moveRunTab(0, 2);
    expect(getRunTabs().map((t) => t.name)).toEqual(["run-b", "run-c", "run-a"]);
    moveRunTab(2, 0);
    expect(getRunTabs().map((t) => t.name)).toEqual(["run-a", "run-b", "run-c"]);
    moveRunTab(0, 5); // out of range — no-op
    expect(getRunTabs().map((t) => t.name)).toEqual(["run-a", "run-b", "run-c"]);
  });

  it("evicts the oldest tab at capacity", () => {
    for (let i = 0; i < 21; i++) openRunTab(`/runs/demo/run-${i}`);
    const tabs = getRunTabs();
    expect(tabs).toHaveLength(20);
    expect(tabs[0].name).toBe("run-1");
    expect(tabs[19].name).toBe("run-20");
  });

  it("survives malformed storage", () => {
    localStorage.setItem(KEY, "not json");
    expect(getRunTabs()).toEqual([]);
    localStorage.setItem(KEY, JSON.stringify({ tabs: [{ path: 42 }, null, { path: "/nope" }, { path: "/runs/a/b" }, { path: "/runs/a/b" }] }));
    expect(getRunTabs().map((t) => t.path)).toEqual(["/runs/a/b"]);
  });
});
