import { create } from "@bufbuild/protobuf";
import { describe, expect, it } from "vitest";
import { applyAgentRunEvent } from "@/hooks/useAgentRuns.helpers";
import { AgentRunSchema, type AgentRun } from "@/rpc/platform/service_pb";

function run(name: string, phase = "Running"): AgentRun {
  return create(AgentRunSchema, {
    name,
    namespace: "default",
    phase,
    conversation: [],
    gateResults: [],
    pendingActions: [],
    recentActivity: [],
    children: [],
  });
}

describe("applyAgentRunEvent", () => {
  it("removes deleted runs from the list", () => {
    const prev = [run("one"), run("two")];

    const next = applyAgentRunEvent(prev, {
      type: "DELETED",
      run: run("one"),
    });

    expect(next.map((r) => r.name)).toEqual(["two"]);
  });

  it("upserts modified runs", () => {
    const prev = [run("one", "Running")];

    const next = applyAgentRunEvent(prev, {
      type: "MODIFIED",
      run: run("one", "Succeeded"),
    });

    expect(next).toHaveLength(1);
    expect(next[0].phase).toBe("Succeeded");
  });
});
