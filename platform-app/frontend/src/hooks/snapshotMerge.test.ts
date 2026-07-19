import { create } from "@bufbuild/protobuf";
import { describe, expect, it } from "vitest";

import {
  agentRunFingerprint,
  mergeActivityEntries,
  mergeAgentRun,
  mergeConversation,
  subagentGraphFingerprint,
} from "./snapshotMerge";
import { UserInputRequestSchema } from "@/rpc/platform/service_pb";
import type {
  ActivityEntry,
  AgentRun,
  AgentRunOverseerConfig,
  AgentRunOverseerSummary,
  ChatMessage,
  SubagentGraph,
} from "@/rpc/platform/service_pb";

function entry(overrides: Partial<ActivityEntry>): ActivityEntry {
  return { timestampUnix: 1n, type: "text", toolUseId: "", message: "", output: "", ...overrides } as ActivityEntry;
}

function message(overrides: Partial<ChatMessage>): ChatMessage {
  return { role: "assistant", content: "", timestampUnix: 1n, pending: false, ...overrides } as ChatMessage;
}

function run(overrides: Partial<AgentRun>): AgentRun {
  return { namespace: "ns", name: "run", phase: "Running", conversation: [], ...overrides } as AgentRun;
}

function overseer(overrides: Partial<AgentRunOverseerConfig>): AgentRunOverseerConfig {
  return { authority: "advise", intervalMinutes: 10, maxInterventions: 5, ...overrides } as AgentRunOverseerConfig;
}

function overseerSummary(overrides: Partial<AgentRunOverseerSummary>): AgentRunOverseerSummary {
  return { state: "active", checkpointsHandled: 1n, ...overrides } as AgentRunOverseerSummary;
}

describe("mergeActivityEntries", () => {
  it("returns prev unchanged when the snapshot is content-identical", () => {
    const prev = [entry({ message: "a" }), entry({ message: "b", timestampUnix: 2n })];
    const next = [entry({ message: "a" }), entry({ message: "b", timestampUnix: 2n })];
    expect(mergeActivityEntries(prev, next)).toBe(prev);
  });

  it("reuses previous entry references for the stable prefix", () => {
    const prev = [entry({ message: "a" }), entry({ message: "b", timestampUnix: 2n })];
    const next = [
      entry({ message: "a" }),
      entry({ message: "b", timestampUnix: 2n }),
      entry({ message: "c", timestampUnix: 3n }),
    ];
    const merged = mergeActivityEntries(prev, next);
    expect(merged).toHaveLength(3);
    expect(merged[0]).toBe(prev[0]);
    expect(merged[1]).toBe(prev[1]);
    expect(merged[2]).toBe(next[2]);
  });

  it("takes the new snapshot when the last previous entry grew", () => {
    const prev = [entry({ message: "partial" })];
    const next = [entry({ message: "partial plus more" }), entry({ timestampUnix: 2n })];
    const merged = mergeActivityEntries(prev, next);
    expect(merged).toBe(next);
  });

  it("adopts the snapshot when a non-last assistant_thinking entry grew", () => {
    const prev = [
      entry({ type: "assistant_thinking", toolUseId: "think-1", message: "par" }),
      entry({ timestampUnix: 2n, message: "tool" }),
    ];
    const next = [
      entry({ type: "assistant_thinking", toolUseId: "think-1", message: "partial plus more" }),
      entry({ timestampUnix: 2n, message: "tool" }),
    ];
    expect(mergeActivityEntries(prev, next)).toBe(next);
  });

  it("returns prev when a non-last assistant_thinking entry is unchanged", () => {
    const prev = [
      entry({ type: "assistant_thinking", toolUseId: "think-1", message: "done" }),
      entry({ timestampUnix: 2n, message: "tool" }),
    ];
    const next = [
      entry({ type: "assistant_thinking", toolUseId: "think-1", message: "done" }),
      entry({ timestampUnix: 2n, message: "tool" }),
    ];
    expect(mergeActivityEntries(prev, next)).toBe(prev);
  });

  it("falls back to the new snapshot on a rewrite", () => {
    const prev = [entry({ toolUseId: "t1" }), entry({ timestampUnix: 2n })];
    const next = [entry({ toolUseId: "t2" }), entry({ timestampUnix: 2n })];
    expect(mergeActivityEntries(prev, next)).toBe(next);
  });

  it("falls back to the new snapshot when it shrank", () => {
    const prev = [entry({}), entry({ timestampUnix: 2n })];
    const next = [entry({})];
    expect(mergeActivityEntries(prev, next)).toBe(next);
  });
});

describe("subagentGraphFingerprint", () => {
  it("changes when a node status changes and is stable otherwise", () => {
    const graph = (status: string) =>
      ({ nodes: [{ id: "a", status }], edges: [] }) as unknown as SubagentGraph;
    expect(subagentGraphFingerprint(graph("running"))).toBe(subagentGraphFingerprint(graph("running")));
    expect(subagentGraphFingerprint(graph("running"))).not.toBe(subagentGraphFingerprint(graph("completed")));
    expect(subagentGraphFingerprint(undefined)).toBe("");
  });
});

describe("mergeConversation", () => {
  it("reuses previous message references for the stable prefix", () => {
    const prev = [message({ content: "hi" }), message({ content: "there", timestampUnix: 2n })];
    const next = [
      message({ content: "hi" }),
      message({ content: "there", timestampUnix: 2n }),
      message({ content: "new", timestampUnix: 3n }),
    ];
    const merged = mergeConversation(prev, next);
    expect(merged[0]).toBe(prev[0]);
    expect(merged[1]).toBe(prev[1]);
    expect(merged[2]).toBe(next[2]);
  });

  it("takes the snapshot when the streaming tail message grew", () => {
    const prev = [message({ content: "str" })];
    const next = [message({ content: "streaming" })];
    expect(mergeConversation(prev, next)).toBe(next);
  });

  it("takes the snapshot when a non-final message is delivered", () => {
    const prev = [
      message({ id: 1n, role: "user", pending: true, deliveredAtUnix: 0n }),
      message({ id: 2n, content: "tail", timestampUnix: 2n }),
    ];
    const next = [
      message({ id: 1n, role: "user", pending: false, deliveredAtUnix: 3n }),
      message({ id: 2n, content: "tail", timestampUnix: 2n }),
    ];
    expect(mergeConversation(prev, next)).toBe(next);
  });
});

describe("mergeAgentRun", () => {
  it("returns prev when nothing user-visible changed", () => {
    const prev = run({ conversation: [message({ content: "hi" })] });
    const next = run({ conversation: [message({ content: "hi" })] });
    expect(agentRunFingerprint(prev)).toBe(agentRunFingerprint(next));
    expect(mergeAgentRun(prev, next)).toBe(prev);
  });

  it("returns the new run with reused conversation prefix on change", () => {
    const prev = run({ conversation: [message({ content: "hi" })] });
    const next = run({
      phase: "Succeeded",
      conversation: [message({ content: "hi" }), message({ content: "done", timestampUnix: 2n })],
    });
    const merged = mergeAgentRun(prev, next);
    expect(merged).not.toBe(prev);
    expect(merged.phase).toBe("Succeeded");
    expect(merged.conversation[0]).toBe(prev.conversation[0]);
    expect(merged.conversation[1]).toBe(next.conversation[1]);
  });

  it("returns next as-is when there is no previous run", () => {
    const next = run({});
    expect(mergeAgentRun(null, next)).toBe(next);
  });

  it("does not drop updates that only change the mode", () => {
    const prev = run({ modeName: "build", modeInstructions: "ship it" } as Partial<AgentRun>);
    const next = run({ modeName: "plan", modeInstructions: "plan first" } as Partial<AgentRun>);
    expect(agentRunFingerprint(prev)).not.toBe(agentRunFingerprint(next));
    const merged = mergeAgentRun(prev, next);
    expect(merged).not.toBe(prev);
    expect(merged.modeName).toBe("plan");
    expect(merged.modeInstructions).toBe("plan first");
  });

  it("does not drop updates that only change the pending input state", () => {
    const idle = run({ userInputRequest: create(UserInputRequestSchema, { type: "idle" }) });
    const question = run({ userInputRequest: create(UserInputRequestSchema, { type: "question", message: "Choose" }) });
    expect(agentRunFingerprint(idle)).not.toBe(agentRunFingerprint(question));
    expect(mergeAgentRun(idle, question)).not.toBe(idle);
  });

  it("does not drop updates that only change the model or review artifact", () => {
    const prev = run({ model: "sonnet", reviewArtifactKind: "" } as Partial<AgentRun>);
    const modelSwitch = run({ model: "opus", reviewArtifactKind: "" } as Partial<AgentRun>);
    expect(mergeAgentRun(prev, modelSwitch)).not.toBe(prev);
    const prBound = run({ model: "sonnet", reviewArtifactKind: "PullRequest" } as Partial<AgentRun>);
    expect(mergeAgentRun(prev, prBound)).not.toBe(prev);
  });

  it("does not drop overseer config or status-only updates", () => {
    const detached = run({});
    const attached = run({ overseer: overseer({ maxInterventions: 0 }) });
    expect(mergeAgentRun(detached, attached)).not.toBe(detached);
    const detaching = run({ overseerDetaching: true });
    expect(mergeAgentRun(detached, detaching)).not.toBe(detached);

    const checking = run({
      overseer: overseer({ maxInterventions: 0 }),
      overseerSummary: overseerSummary({ state: "checking", checkpointsHandled: 1n }),
    });
    const completed = run({
      overseer: overseer({ maxInterventions: 0 }),
      overseerSummary: overseerSummary({
        state: "active",
        checkpointsHandled: 2n,
        lastVerdict: "all_clear",
        lastSummary: "Ready to finish",
      }),
    });
    expect(mergeAgentRun(checking, completed)).not.toBe(checking);
  });
});
