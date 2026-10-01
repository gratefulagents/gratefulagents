import { create, type MessageInitShape } from "@bufbuild/protobuf";
import { describe, expect, it } from "vitest";

import { cleanSubagentDescription, groupActivityEntries, subagentPromptMarkdown, subagentTitleFromPrompt } from "@/lib/activityGrouping";
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

describe("groupActivityEntries batch spawns", () => {
  function batchEntries() {
    const tasks = ["task_a", "task_b", "task_c"];
    return [
      entry({ timestampUnix: 1n, type: "tool_use", tool: "agent_batch", toolUseId: "call_batch", message: "fan out" }),
      ...tasks.map((taskId, n) =>
        entry({
          timestampUnix: BigInt(2 + n),
          type: "subagent_started",
          taskId,
          toolUseId: "call_batch",
          parentCallId: "call_batch",
          subagentType: "worker",
          subagentDescription: `Job ${taskId}`,
        }),
      ),
      ...tasks.map((taskId, n) =>
        entry({
          timestampUnix: BigInt(5 + n),
          type: "subagent_completed",
          taskId,
          parentCallId: "call_batch",
          subagentType: "worker",
          subagentStatus: "completed",
        }),
      ),
      entry({ timestampUnix: 9n, type: "tool_result", tool: "agent_batch", toolUseId: "call_batch", output: "all done" }),
    ];
  }

  it("does not attach a shared parent call to the last task of a 3-task batch", () => {
    const entries = batchEntries();
    const groups = groupActivityEntries(entries);

    expect(groups.map((g) => g.kind)).toEqual(["tool-pair", "subagent", "subagent", "subagent"]);
    const pair = groups[0];
    if (pair.kind !== "tool-pair") throw new Error("expected tool-pair");
    expect(pair.toolUse).toBe(entries[0]);
    expect(pair.toolResult).toBe(entries[7]);

    const cards = groups.slice(1);
    expect(cards.map((g) => (g.kind === "subagent" ? g.taskId : ""))).toEqual(["task_a", "task_b", "task_c"]);
    for (const card of cards) {
      if (card.kind !== "subagent") throw new Error("expected subagent group");
      expect(card.entries.map((e) => e.type)).toEqual(["subagent_started", "subagent_completed"]);
      expect(card.subagentStatus).toBe("completed");
    }
  });

  it("does not re-render task-card entries as inline-subagent children", () => {
    const entries = batchEntries();
    const stray = entry({ timestampUnix: 8n, type: "tool_use", tool: "Read", toolUseId: "call_read", parentCallId: "call_batch" });
    entries.splice(7, 0, stray);
    const groups = groupActivityEntries(entries);

    const inline = groups.find((g) => g.kind === "inline-subagent");
    if (inline?.kind !== "inline-subagent") throw new Error("expected inline-subagent group");
    expect(inline.children).toEqual([stray]);
    const rendered = groups.flatMap((g) => {
      switch (g.kind) {
        case "subagent":
          return g.entries;
        case "inline-subagent":
          return [g.parentEntry, ...g.children, ...(g.resultEntry ? [g.resultEntry] : [])];
        case "tool-pair":
          return [g.toolUse, g.toolResult];
        case "single":
          return [g.entry];
        default:
          return g.entries;
      }
    });
    expect(rendered).toHaveLength(entries.length);
    expect(new Set(rendered).size).toBe(entries.length);
  });

  it("still maps a single task's spawn call into its card", () => {
    const entries = batchEntries().filter((e) => !e.taskId || e.taskId === "task_a");
    const groups = groupActivityEntries(entries);
    expect(groups).toHaveLength(1);
    const card = groups[0];
    if (card.kind !== "subagent") throw new Error("expected subagent group");
    expect(card.entries).toEqual(entries);
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

describe("subagentPromptMarkdown", () => {
  it("returns plain-text prompts unchanged", () => {
    expect(subagentPromptMarkdown("Fix the failing lint\n\n- item one")).toBe(
      "Fix the failing lint\n\n- item one",
    );
  });

  it("returns empty string for undefined input", () => {
    expect(subagentPromptMarkdown(undefined)).toBe("");
  });

  it("extracts the message from a single-message payload", () => {
    const raw = JSON.stringify({ mode: "sync", message: "Do the **thing**\n\n1. step" });
    expect(subagentPromptMarkdown(raw)).toBe("Do the **thing**\n\n1. step");
  });

  it("extracts the sole task message from a tasks payload", () => {
    const raw = JSON.stringify({
      mode: "sync",
      tasks: [{ agent_name: "executor", key: "backend", message: "Fix CI lint" }],
    });
    expect(subagentPromptMarkdown(raw)).toBe("`backend` · **executor**\n\nFix CI lint");
  });

  it("renders multi-task payloads as separated markdown sections", () => {
    const raw = JSON.stringify({
      mode: "background",
      tasks: [
        { key: "a", agent_name: "executor", message: "First task" },
        { key: "b", agent_name: "test-engineer", message: "Second task", depends_on: ["a"] },
      ],
    });
    const md = subagentPromptMarkdown(raw);
    expect(md).toContain("`a` · **executor**\n\nFirst task");
    expect(md).toContain("\n\n---\n\n");
    expect(md).toContain("`b` · **test-engineer** · _after a_\n\nSecond task");
  });

  it("falls back to the raw text for unparseable or unrecognized JSON", () => {
    expect(subagentPromptMarkdown('{"broken": ')).toBe('{"broken":');
    expect(subagentPromptMarkdown('{"other": true}')).toBe('{"other": true}');
  });
});

describe("cleanSubagentDescription", () => {
  it("drops lifecycle markers, including ones not in the explicit list", () => {
    for (const noise of ["parent_wait", "dependency_wait", "spawned", "future_marker_x", " running "]) {
      expect(cleanSubagentDescription(noise)).toBe("");
    }
  });

  it("keeps real objectives", () => {
    expect(cleanSubagentDescription("Review the diff viewer")).toBe("Review the diff viewer");
    expect(cleanSubagentDescription("fix_bug in parser")).toBe("fix_bug in parser");
  });

  it("titles a parent_wait snapshot from the prompt, not the marker", () => {
    const groups = groupActivityEntries([
      entry({
        type: "subagent_started",
        taskId: "task_1",
        subagentType: "executor",
        subagentDescription: "spawned",
        subagentPrompt: "## Task: Inspector shell & tabs overhaul\nDetails…",
        timestampUnix: 1n,
      }),
      entry({
        type: "subagent_progress",
        taskId: "task_1",
        subagentType: "executor",
        subagentDescription: "parent_wait",
        subagentStatus: "running",
        timestampUnix: 2n,
      }),
    ]);
    const group = groups.find((g) => g.kind === "subagent");
    expect(group && group.kind === "subagent" ? group.subagentDescription : "x").toBe("");
    expect(subagentTitleFromPrompt(group && group.kind === "subagent" ? group.subagentPrompt : "")).toBe(
      "Inspector shell & tabs overhaul",
    );
  });
});

describe("subagentTitleFromPrompt", () => {
  it("strips markdown heading, bullet and emphasis syntax", () => {
    expect(subagentTitleFromPrompt("## Review the graph")).toBe("Review the graph");
    expect(subagentTitleFromPrompt("- **Audit tokens**")).toBe("Audit tokens");
    expect(subagentTitleFromPrompt("\n\n1. First step\nsecond")).toBe("First step");
  });
});
