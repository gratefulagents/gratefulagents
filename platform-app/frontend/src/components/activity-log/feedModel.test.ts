import { create, type MessageInitShape } from "@bufbuild/protobuf";
import { describe, expect, it } from "vitest";

import { groupActivityEntries } from "@/lib/activityGrouping";
import { buildFeed } from "@/components/activity-log/feedModel";
import { ActivityEntrySchema } from "@/rpc/platform/service_pb";

function entry(partial: MessageInitShape<typeof ActivityEntrySchema>) {
  return create(ActivityEntrySchema, partial);
}

function subagentOrder(feedKinds: ReturnType<typeof buildFeed>): string[] {
  const order: string[] = [];
  for (const f of feedKinds) {
    if (f.kind === "subagent") order.push(f.group.taskId);
    if (f.kind === "subagent-dag") order.push(...f.groups.map((g) => g.taskId));
  }
  return order;
}

describe("buildFeed subagent DAG ordering", () => {
  it("coalesces a burst into one DAG item with dependencies before dependents", () => {
    // Concurrent task goroutines interleave events arbitrarily: here the
    // writer (join) task's snapshots land BEFORE the researchers'.
    const entries = [
      entry({ timestampUnix: 100n, type: "tool_use", tool: "subagent", toolUseId: "call_1" }),
      entry({
        timestampUnix: 100n,
        type: "subagent_progress",
        taskId: "t_writer",
        subagentType: "writer",
        subagentStatus: "waiting",
        subagentDependsOn: ["t_tokyo", "t_nairobi"],
        subagentWaitingOn: ["t_tokyo", "t_nairobi"],
      }),
      entry({
        timestampUnix: 100n,
        type: "subagent_progress",
        taskId: "t_tokyo",
        subagentType: "researcher",
        subagentStatus: "running",
      }),
      entry({
        timestampUnix: 100n,
        type: "subagent_progress",
        taskId: "t_nairobi",
        subagentType: "researcher",
        subagentStatus: "running",
      }),
      entry({ timestampUnix: 108n, type: "subagent_completed", taskId: "t_tokyo", subagentType: "researcher", subagentStatus: "completed" }),
      entry({ timestampUnix: 109n, type: "subagent_completed", taskId: "t_nairobi", subagentType: "researcher", subagentStatus: "completed" }),
      entry({ timestampUnix: 115n, type: "subagent_completed", taskId: "t_writer", subagentType: "writer", subagentStatus: "completed" }),
    ];

    const feed = buildFeed(groupActivityEntries(entries));
    expect(subagentOrder(feed)).toEqual(["t_tokyo", "t_nairobi", "t_writer"]);

    const dag = feed.find((f) => f.kind === "subagent-dag");
    expect(dag).toBeDefined();
    if (dag?.kind !== "subagent-dag") throw new Error("unreachable");
    // Researchers share wave 0; the join lands in wave 1.
    expect(dag.waves).toEqual([0, 0, 1]);
    // The burst renders as ONE feed item, not three cards.
    expect(feed.filter((f) => f.kind === "subagent")).toHaveLength(0);
  });

  it("keeps standalone cards for single spawns and separate bursts", () => {
    const entries = [
      entry({ timestampUnix: 1n, type: "subagent_completed", taskId: "a", subagentType: "explore", subagentStatus: "completed" }),
      entry({ timestampUnix: 3n, type: "assistant_text", message: "between bursts" }),
      entry({ timestampUnix: 4n, type: "subagent_completed", taskId: "c", subagentType: "explore", subagentStatus: "completed" }),
    ];
    const feed = buildFeed(groupActivityEntries(entries));
    expect(subagentOrder(feed)).toEqual(["a", "c"]);
    expect(feed.filter((f) => f.kind === "subagent")).toHaveLength(2);
    expect(feed.filter((f) => f.kind === "subagent-dag")).toHaveLength(0);
    // The prose stays between the two cards.
    const kinds = feed.map((f) => f.kind);
    expect(kinds.indexOf("prose")).toBeGreaterThan(kinds.indexOf("subagent"));
  });

  it("coalesces a high-cardinality batch even when child work interleaves", () => {
    const parentCallId = "call_batch";
    const entries = [
      entry({
        timestampUnix: 1n,
        type: "tool_use",
        tool: "subagent",
        toolUseId: parentCallId,
      }),
    ];

    for (let i = 0; i < 12; i++) {
      const taskId = `task_${i}`;
      entries.push(
        entry({
          timestampUnix: BigInt(i + 2),
          type: "subagent_started",
          taskId,
          toolUseId: parentCallId,
          parentCallId,
          subagentType: "reviewer",
          subagentStatus: "started",
          subagentPrompt: `Review area ${i}`,
        }),
        entry({
          timestampUnix: BigInt(i + 20),
          type: "tool_use",
          taskId,
          tool: "read_file",
          toolUseId: `read_${i}`,
          input: `file_${i}.ts`,
        }),
        // Parent work arriving between task lifecycle events used to split the
        // delegation into a stack of standalone subagent cards.
        entry({
          timestampUnix: BigInt(i + 40),
          type: "tool_use",
          tool: "Bash",
          toolUseId: `parent_work_${i}`,
          input: `echo ${i}`,
        }),
      );
    }

    const feed = buildFeed(groupActivityEntries(entries));
    const dags = feed.filter((item) => item.kind === "subagent-dag");

    expect(dags).toHaveLength(1);
    expect(feed.filter((item) => item.kind === "subagent")).toHaveLength(0);
    expect(dags[0]?.groups.map((group) => group.taskId).sort()).toEqual(
      Array.from({ length: 12 }, (_, i) => `task_${i}`).sort(),
    );
  });

  it("coalesces paginated task work without lifecycle entries", () => {
    const entries = Array.from({ length: 12 }, (_, i) =>
      entry({
        timestampUnix: BigInt(i + 1),
        type: "tool_use",
        taskId: `task_page_${i}`,
        agentName: "reviewer",
        tool: "read_file",
        toolUseId: `page_read_${i}`,
      }),
    );

    const feed = buildFeed(groupActivityEntries(entries));
    const dag = feed.find((item) => item.kind === "subagent-dag");

    expect(dag?.kind).toBe("subagent-dag");
    if (dag?.kind !== "subagent-dag") throw new Error("expected delegation group");
    expect(dag.groups).toHaveLength(12);
    expect(feed.filter((item) => item.kind === "work")).toHaveLength(0);
  });

  it("does not merge tasks from separate parent calls", () => {
    const entries = [
      entry({ timestampUnix: 1n, type: "subagent_started", taskId: "a", toolUseId: "call_a", parentCallId: "call_a", subagentType: "explore" }),
      entry({ timestampUnix: 2n, type: "subagent_started", taskId: "b", toolUseId: "call_b", parentCallId: "call_b", subagentType: "explore" }),
    ];

    const feed = buildFeed(groupActivityEntries(entries));
    expect(feed.filter((item) => item.kind === "subagent")).toHaveLength(2);
    expect(feed.filter((item) => item.kind === "subagent-dag")).toHaveLength(0);
  });

  it("does not loop on dependency cycles", () => {
    const entries = [
      entry({ timestampUnix: 1n, type: "subagent_progress", taskId: "x", subagentType: "a", subagentStatus: "running", subagentDependsOn: ["y"] }),
      entry({ timestampUnix: 2n, type: "subagent_progress", taskId: "y", subagentType: "b", subagentStatus: "running", subagentDependsOn: ["x"] }),
    ];
    const feed = buildFeed(groupActivityEntries(entries));
    expect(subagentOrder(feed)).toHaveLength(2);
  });
});
