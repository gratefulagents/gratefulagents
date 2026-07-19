import { create, type MessageInitShape } from "@bufbuild/protobuf";
import { describe, expect, it } from "vitest";

import { groupActivityEntries } from "@/lib/activityGrouping";
import { ActivityEntrySchema } from "@/rpc/platform/service_pb";

function entry(partial: MessageInitShape<typeof ActivityEntrySchema>) {
  return create(ActivityEntrySchema, partial);
}

describe("groupActivityEntries", () => {
  it("merges agent-tool wrapper calls into the spawned subagent group", () => {
    const entries = [
      entry({
        timestampUnix: 1n,
        type: "tool_use",
        tool: "agent_debugger",
        toolUseId: "call_parent",
        message: "Investigate why the test flakes",
      }),
      entry({
        timestampUnix: 2n,
        type: "subagent_started",
        taskId: "task_debugger",
        toolUseId: "call_parent",
        subagentType: "debugger",
        subagentDescription: "Investigate why the test flakes",
      }),
      entry({
        timestampUnix: 3n,
        type: "tool_use",
        taskId: "task_debugger",
        tool: "Read",
        toolUseId: "call_child",
        input: "/repo/main.go",
      }),
      entry({
        timestampUnix: 4n,
        type: "tool_result",
        taskId: "task_debugger",
        tool: "Read",
        toolUseId: "call_child",
        output: "package main",
      }),
      entry({
        timestampUnix: 5n,
        type: "tool_result",
        tool: "agent_debugger",
        toolUseId: "call_parent",
        output: "done",
      }),
      entry({
        timestampUnix: 6n,
        type: "subagent_completed",
        taskId: "task_debugger",
        subagentType: "debugger",
        subagentStatus: "completed",
        subagentDescription: "Investigation complete",
      }),
    ];

    const groups = groupActivityEntries(entries);

    expect(groups).toHaveLength(1);
    expect(groups[0]?.kind).toBe("subagent");
    if (groups[0]?.kind !== "subagent") {
      throw new Error("expected subagent group");
    }
    expect(groups[0].taskId).toBe("task_debugger");
    expect(groups[0].parentCallId).toBe("call_parent");
    expect(
      groups[0].entries.some(
        (groupEntry) =>
          groupEntry.type === "tool_use" &&
          groupEntry.tool === "agent_debugger" &&
          groupEntry.toolUseId === "call_parent",
      ),
    ).toBe(true);
    expect(
      groups[0].entries.some(
        (groupEntry) =>
          groupEntry.type === "tool_result" &&
          groupEntry.tool === "agent_debugger" &&
          groupEntry.toolUseId === "call_parent",
      ),
    ).toBe(true);
  });

  it("groups task-tagged child work when the spawn event is outside the live page", () => {
    const entries = [
      entry({
        timestampUnix: 1n,
        type: "llm_attempt",
        taskId: "task_paginated",
        agentName: "code-reviewer",
        subagentStatus: "",
      }),
      entry({
        timestampUnix: 2n,
        type: "tool_use",
        taskId: "task_paginated",
        agentName: "code-reviewer",
        tool: "read_file",
        toolUseId: "child_read",
      }),
    ];

    const groups = groupActivityEntries(entries);
    const subagent = groups.find((group) => group.kind === "subagent");

    expect(subagent?.kind).toBe("subagent");
    if (subagent?.kind !== "subagent") throw new Error("expected subagent group");
    expect(subagent.taskId).toBe("task_paginated");
    expect(subagent.subagentType).toBe("code-reviewer");
    expect(subagent.entries).toHaveLength(2);
  });

  it("does not create phantom subagent groups from llm attempts that only carry a spawn call id", () => {
    const entries = [
      entry({
        timestampUnix: 1n,
        type: "llm_attempt",
        taskId: "call_spawn_parent",
        llmAttemptId: "attempt-1",
        llmAttemptInputTokens: 120n,
        llmAttemptOutputTokens: 48n,
        llmAttemptTokensKnown: true,
      }),
      entry({
        timestampUnix: 2n,
        type: "tool_use",
        tool: "agent_analyst",
        toolUseId: "call_spawn_parent",
        message: "Investigate the realtime lag",
      }),
      entry({
        timestampUnix: 3n,
        type: "subagent_started",
        taskId: "task_analyst",
        toolUseId: "call_spawn_parent",
        subagentType: "analyst",
        subagentDescription: "Investigate the realtime lag",
      }),
      entry({
        timestampUnix: 4n,
        type: "subagent_completed",
        taskId: "task_analyst",
        subagentType: "analyst",
        subagentStatus: "completed",
        subagentDescription: "Investigation complete",
      }),
    ];

    const groups = groupActivityEntries(entries);
    const subagentGroups = groups.filter((group) => group.kind === "subagent");

    expect(subagentGroups).toHaveLength(1);
    if (subagentGroups[0]?.kind !== "subagent") {
      throw new Error("expected a subagent group");
    }
    expect(subagentGroups[0].taskId).toBe("task_analyst");
  });
});

describe("registry task snapshots (consolidated subagent engine)", () => {
  it("titles from the task prompt, not lifecycle noise, and detects terminal status", () => {
    const entries = [
      entry({
        timestampUnix: 1n,
        type: "subagent_progress",
        taskId: "task_res",
        subagentType: "researcher",
        subagentDescription: "spawned",
        subagentPrompt: "Fetch the weather for Tokyo\nUse wttr.in.",
        subagentStatus: "running",
      }),
      entry({
        timestampUnix: 2n,
        type: "subagent_completed",
        taskId: "task_res",
        subagentType: "researcher",
        subagentDescription: "completed",
        subagentStatus: "cancelled",
      }),
    ];

    const groups = groupActivityEntries(entries);
    const group = groups.find((g) => g.kind === "subagent");
    expect(group).toBeDefined();
    expect(group?.subagentDescription).toBe("");
    expect(group?.subagentPrompt).toBe("Fetch the weather for Tokyo\nUse wttr.in.");
    expect(group?.subagentStatus).toBe("cancelled");
  });

  it("keeps a real description when present", () => {
    const entries = [
      entry({
        timestampUnix: 1n,
        type: "subagent_started",
        taskId: "task_x",
        subagentType: "writer",
        subagentDescription: "Summarize the findings",
        subagentStatus: "started",
      }),
    ];
    const groups = groupActivityEntries(entries);
    expect(groups.find((g) => g.kind === "subagent")?.subagentDescription).toBe(
      "Summarize the findings",
    );
  });
});
