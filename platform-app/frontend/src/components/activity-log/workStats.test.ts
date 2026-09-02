import { describe, expect, it } from "vitest";

import { completedEntries, computeStats, liveActivity, liveVerb, statsSummary } from "./workStats";
import type { ActivityEntry } from "@/rpc/platform/service_pb";

function entry(overrides: Partial<ActivityEntry>): ActivityEntry {
  return {
    type: "tool_use",
    tool: "",
    input: "",
    inputRaw: "",
    message: "",
    output: "",
    timestampUnix: 1n,
    toolDurationMs: 0n,
    ...overrides,
  } as ActivityEntry;
}

describe("computeStats", () => {
  it("counts every tool call by its actual name with no generic bucket", () => {
    const entries = [
      ...Array.from({ length: 6 }, () => entry({ tool: "grep" })),
      entry({ tool: "read_file" }),
      entry({ tool: "read_file" }),
      entry({ tool: "Write" }),
      entry({ tool: "Terminal" }),
      entry({ tool: "subagent" }),
      entry({ tool: "task_create" }),
    ];
    const stats = computeStats(entries);
    expect(stats.toolTotal).toBe(12);
    expect(stats.tools).toEqual([
      { name: "grep", count: 6 },
      { name: "read_file", count: 2 },
      { name: "Terminal", count: 1 },
      { name: "Write", count: 1 },
      { name: "subagent", count: 1 },
      { name: "task_create", count: 1 },
    ]);
  });

  it("labels tool calls without a name as \"tool\"", () => {
    const stats = computeStats([entry({ tool: "" })]);
    expect(stats.tools).toEqual([{ name: "tool", count: 1 }]);
  });

  it("still tracks errors, thoughts, and system events", () => {
    const stats = computeStats([
      entry({ type: "assistant_thinking" }),
      entry({ type: "tool_result", isError: true }),
      entry({ tool: "Bash" }),
    ]);
    expect(stats.thoughts).toBe(1);
    expect(stats.errors).toBe(1);
    expect(stats.toolTotal).toBe(1);
  });
});

describe("statsSummary", () => {
  it("renders raw per-tool counts", () => {
    const stats = computeStats([
      ...Array.from({ length: 6 }, () => entry({ tool: "grep" })),
      entry({ tool: "read_file" }),
      entry({ tool: "read_file" }),
      entry({ tool: "Write" }),
    ]);
    expect(statsSummary(stats)).toBe("6× grep · 2× read_file · 1× Write");
  });

  it("falls back to reasoning when only thoughts exist", () => {
    const stats = computeStats([entry({ type: "assistant_thinking" })]);
    expect(statsSummary(stats)).toBe("reasoning");
  });
});

describe("liveVerb", () => {
  it("uses the actual tool name", () => {
    expect(liveVerb([entry({ tool: "Write" })])).toBe("Running Write");
    expect(liveVerb([entry({ tool: "read_file" })])).toBe("Running read_file");
  });

  it("reports thinking for reasoning entries", () => {
    expect(liveVerb([entry({ type: "assistant_thinking" })])).toBe("Thinking");
  });
});

describe("liveActivity", () => {
  it("treats an unanswered tool call as in flight", () => {
    const use = entry({ tool: "submit_report", toolUseId: "t1" });
    const live = liveActivity([entry({ tool: "wait", toolUseId: "t0" }), entry({ type: "tool_result", toolUseId: "t0" }), use]);
    expect(live).toEqual({ kind: "tool", use });
    expect(liveVerb([use])).toBe("Running submit_report");
  });

  it("stops claiming Running once the call has returned", () => {
    const use = entry({ tool: "submit_report", toolUseId: "t1" });
    const result = entry({ type: "tool_result", toolUseId: "t1", toolDurationMs: 250n });
    const live = liveActivity([use, result]);
    expect(live).toEqual({ kind: "idle", last: { use, result } });
    expect(liveVerb([use, result])).toBe("Working");
  });

  it("excludes only the in-flight call from the completed summary", () => {
    const running = entry({ tool: "submit_report", toolUseId: "t2" });
    const entries = [
      entry({ tool: "finalize", toolUseId: "t1" }),
      entry({ type: "tool_result", toolUseId: "t1" }),
      running,
    ];
    const live = liveActivity(entries);
    const stats = computeStats(completedEntries(entries, live));
    expect(statsSummary(stats)).toBe("1× finalize");
  });

  it("falls back to ordering when calls carry no ids", () => {
    expect(liveActivity([entry({ tool: "Bash" }), entry({ type: "tool_result" })]).kind).toBe("idle");
    expect(liveActivity([entry({ type: "tool_result" }), entry({ tool: "Bash" })]).kind).toBe("tool");
  });
});
