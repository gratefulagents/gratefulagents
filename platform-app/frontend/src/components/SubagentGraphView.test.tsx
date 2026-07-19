import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { create } from "@bufbuild/protobuf";

import {
  SubagentGraphEdgeSchema,
  SubagentGraphNodeSchema,
  SubagentGraphSchema,
} from "@/rpc/platform/service_pb";
import { SubagentGraphView } from "./SubagentGraphView";

afterEach(() => {
  cleanup();
  vi.useRealTimers();
});

describe("SubagentGraphView", () => {
  it("shows the model on each subagent DAG node", () => {
    const graph = create(SubagentGraphSchema, {
      rootId: "root",
      hasSubagents: true,
      nodes: [
        create(SubagentGraphNodeSchema, {
          id: "root",
          kind: "root",
          label: "Main agent",
          status: "running",
        }),
        create(SubagentGraphNodeSchema, {
          id: "task:review",
          kind: "subagent",
          parentId: "root",
          label: "Review changes",
          subtitle: "reviewer",
          status: "running",
          model: "gpt-5.4",
        }),
      ],
      edges: [
        create(SubagentGraphEdgeSchema, {
          id: "root=>task:review",
          from: "root",
          to: "task:review",
          kind: "spawned",
        }),
      ],
    });

    render(<SubagentGraphView graph={graph} entries={[]} />);

    expect(screen.getAllByText("gpt-5.4").length).toBeGreaterThan(0);
  });

  it("shows elapsed time and live usage metrics while a subagent is running", () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2026-04-06T10:00:10Z"));
    const graph = create(SubagentGraphSchema, {
      rootId: "root",
      hasSubagents: true,
      nodes: [
        create(SubagentGraphNodeSchema, {
          id: "root",
          kind: "root",
          label: "Main agent",
          status: "running",
        }),
        create(SubagentGraphNodeSchema, {
          id: "task:live",
          kind: "subagent",
          parentId: "root",
          label: "Live work",
          subtitle: "executor",
          status: "running",
          timestampUnix: BigInt(Date.parse("2026-04-06T10:00:00Z") / 1_000),
          model: "gpt-5.4",
          totalTokens: 1_250n,
          costUsd: 0.0123,
        }),
      ],
      edges: [
        create(SubagentGraphEdgeSchema, {
          id: "root=>task:live",
          from: "root",
          to: "task:live",
          kind: "spawned",
        }),
      ],
    });

    render(<SubagentGraphView graph={graph} entries={[]} />);

    const liveNode = screen.getByRole("button", { name: /Live work/ });
    expect(liveNode.textContent).toContain("10.0s");
    expect(liveNode.textContent).toContain("gpt-5.4");
    expect(liveNode.textContent).toContain("1.3K tok");
    expect(liveNode.textContent).toContain("$0.012");
  });

  it("presents dependency-gated live nodes as waiting instead of live", () => {
    const graph = create(SubagentGraphSchema, {
      rootId: "root",
      hasSubagents: true,
      nodes: [
        create(SubagentGraphNodeSchema, {
          id: "root",
          kind: "root",
          label: "Main agent",
          status: "running",
        }),
        create(SubagentGraphNodeSchema, {
          id: "task:build",
          kind: "subagent",
          parentId: "root",
          label: "Build the feature",
          subtitle: "executor",
          status: "running",
          currentStep: "editing files",
        }),
        create(SubagentGraphNodeSchema, {
          id: "task:verify",
          kind: "subagent",
          parentId: "root",
          label: "Verify the feature",
          subtitle: "verifier",
          status: "waiting",
          waitingOn: ["build"],
        }),
      ],
      edges: [
        create(SubagentGraphEdgeSchema, {
          id: "root=>task:build",
          from: "root",
          to: "task:build",
          kind: "spawned",
        }),
        create(SubagentGraphEdgeSchema, {
          id: "root=>task:verify",
          from: "root",
          to: "task:verify",
          kind: "spawned",
        }),
        create(SubagentGraphEdgeSchema, {
          id: "task:build=>task:verify",
          from: "task:build",
          to: "task:verify",
          kind: "depends-on",
        }),
      ],
    });

    render(<SubagentGraphView graph={graph} entries={[]} />);

    const workingNode = screen.getByRole("button", { name: /Build the feature/ });
    expect(workingNode.textContent).toContain("live");
    expect(workingNode.textContent).toContain("editing files");

    const waitingNode = screen.getByRole("button", { name: /Verify the feature/ });
    expect(waitingNode.textContent).toContain("waiting");
    expect(waitingNode.textContent).not.toContain("live");
    expect(waitingNode.textContent).toContain("waiting for dependencies…");
  });
});
