import { afterEach, beforeEach, describe, expect, it } from "vitest";

import {
  runTabFromPath,
  runTabsScope,
  getRunTabs,
  openRunTab,
  closeRunTab,
  closeOtherRunTabs,
  closeAllRunTabs,
  moveRunTab,
} from "@/hooks/useRunTabs";

const SCOPE = "ws-a:user-1";
const KEY = `gratefulagents.runtabs.v1.${SCOPE}`;

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
    openRunTab(SCOPE, "/runs/demo/run-a");
    openRunTab(SCOPE, "/runs/demo/run-b");
    openRunTab(SCOPE, "/runs/demo/run-a"); // revisit — keeps position
    openRunTab(SCOPE, "/settings"); // not a run page — ignored
    expect(getRunTabs(SCOPE).map((t) => t.name)).toEqual(["run-a", "run-b"]);
  });

  it("close returns the right neighbor, then left, then null", () => {
    openRunTab(SCOPE, "/runs/demo/run-a");
    openRunTab(SCOPE, "/runs/demo/run-b");
    openRunTab(SCOPE, "/runs/demo/run-c");
    expect(closeRunTab(SCOPE, "/runs/demo/run-b")).toBe("/runs/demo/run-c");
    expect(closeRunTab(SCOPE, "/runs/demo/run-c")).toBe("/runs/demo/run-a");
    expect(closeRunTab(SCOPE, "/runs/demo/run-a")).toBeNull();
    expect(getRunTabs(SCOPE)).toEqual([]);
  });

  it("close of an unknown tab is a no-op", () => {
    openRunTab(SCOPE, "/runs/demo/run-a");
    expect(closeRunTab(SCOPE, "/runs/demo/other")).toBeNull();
    expect(getRunTabs(SCOPE)).toHaveLength(1);
  });

  it("closeOthers and closeAll", () => {
    openRunTab(SCOPE, "/runs/demo/run-a");
    openRunTab(SCOPE, "/runs/demo/run-b");
    openRunTab(SCOPE, "/runs/demo/run-c");
    closeOtherRunTabs(SCOPE, "/runs/demo/run-b");
    expect(getRunTabs(SCOPE).map((t) => t.name)).toEqual(["run-b"]);
    closeAllRunTabs(SCOPE);
    expect(getRunTabs(SCOPE)).toEqual([]);
  });

  it("reorders tabs", () => {
    openRunTab(SCOPE, "/runs/demo/run-a");
    openRunTab(SCOPE, "/runs/demo/run-b");
    openRunTab(SCOPE, "/runs/demo/run-c");
    moveRunTab(SCOPE, 0, 2);
    expect(getRunTabs(SCOPE).map((t) => t.name)).toEqual(["run-b", "run-c", "run-a"]);
    moveRunTab(SCOPE, 2, 0);
    expect(getRunTabs(SCOPE).map((t) => t.name)).toEqual(["run-a", "run-b", "run-c"]);
    moveRunTab(SCOPE, 0, 5); // out of range — no-op
    expect(getRunTabs(SCOPE).map((t) => t.name)).toEqual(["run-a", "run-b", "run-c"]);
  });

  it("evicts the oldest tab at capacity", () => {
    for (let i = 0; i < 21; i++) openRunTab(SCOPE, `/runs/demo/run-${i}`);
    const tabs = getRunTabs(SCOPE);
    expect(tabs).toHaveLength(20);
    expect(tabs[0].name).toBe("run-1");
    expect(tabs[19].name).toBe("run-20");
  });

  it("survives malformed storage", () => {
    localStorage.setItem(KEY, "not json");
    expect(getRunTabs(SCOPE)).toEqual([]);
    localStorage.setItem(KEY, JSON.stringify({ tabs: [{ path: 42 }, null, { path: "/nope" }, { path: "/runs/a/b" }, { path: "/runs/a/b" }] }));
    expect(getRunTabs(SCOPE).map((t) => t.path)).toEqual(["/runs/a/b"]);
  });

  it("isolates tabs per workspace/user scope", () => {
    const other = runTabsScope("ws-b", "user-2");
    openRunTab(SCOPE, "/runs/demo/run-a");
    openRunTab(other, "/runs/other/run-z");
    expect(getRunTabs(SCOPE).map((t) => t.name)).toEqual(["run-a"]);
    expect(getRunTabs(other).map((t) => t.name)).toEqual(["run-z"]);
    // A fresh identity (e.g. logout, or another user logging in) starts empty.
    expect(getRunTabs(runTabsScope("ws-a", undefined))).toEqual([]);
    closeAllRunTabs(SCOPE);
    expect(getRunTabs(other)).toHaveLength(1);
  });
});
