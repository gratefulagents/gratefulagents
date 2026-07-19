import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { create } from "@bufbuild/protobuf";

import {
  SubagentGraphEdgeSchema,
  SubagentGraphNodeSchema,
  SubagentGraphSchema,
  type SubagentGraphNode,
} from "@/rpc/platform/service_pb";
import { ActiveSubagentsDock } from "./ActiveSubagentsDock";

// Node ≥22 ships an experimental global localStorage stub that shadows
// jsdom's; a Map-backed fake keeps expansion-persistence assertions
// deterministic across Node versions.
beforeEach(() => {
  const storage = new Map<string, string>();
  vi.stubGlobal("localStorage", {
    getItem: (key: string) => storage.get(key) ?? null,
    setItem: (key: string, value: string) => void storage.set(key, String(value)),
    removeItem: (key: string) => void storage.delete(key),
    clear: () => storage.clear(),
  });
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

type NodeOverrides = Partial<
  Pick<
    SubagentGraphNode,
    | "kind"
    | "label"
    | "subtitle"
    | "description"
    | "currentStep"
    | "lastTool"
    | "waitingOn"
    | "durationMs"
    | "timestampUnix"
    | "model"
    | "totalTokens"
    | "costUsd"
  >
>;

function node(id: string, status: string, values: NodeOverrides = {}): SubagentGraphNode {
  return create(SubagentGraphNodeSchema, {
    id,
    taskId: id,
    kind: "subagent",
    label: `Task ${id}`,
    subtitle: "executor",
    status,
    ...values,
  });
}

function graph(nodes: SubagentGraphNode[], dependencies: Array<[string, string]> = []) {
  return create(SubagentGraphSchema, {
    hasSubagents: true,
    rootId: "root",
    nodes: [
      node("root", "running", { kind: "root", label: "Main agent", subtitle: "" }),
      ...nodes,
    ],
    edges: [
      ...nodes.map((child) =>
        create(SubagentGraphEdgeSchema, {
          id: `root=>${child.id}`,
          from: "root",
          to: child.id,
          kind: "spawn",
        }),
      ),
      ...dependencies.map(([from, to]) =>
        create(SubagentGraphEdgeSchema, {
          id: `${from}=>${to}`,
          from,
          to,
          kind: "depends-on",
        }),
      ),
    ],
  });
}

describe("ActiveSubagentsDock", () => {
  it("renders nothing when every delegated agent is terminal", () => {
    render(
      <ActiveSubagentsDock
        graph={graph([
          node("done", "completed"),
          node("failed", "failed"),
          node("stopped", "cancelled"),
        ])}
      />,
    );

    expect(screen.queryByRole("region", { name: "Active delegated agents" })).toBeNull();
  });

  it("starts collapsed with a summary row, expanding into the complete DAG", () => {
    render(
      <ActiveSubagentsDock
        graph={graph(
          [
            node("running", "running", {
              currentStep: "Editing the API",
              model: "claude-sonnet-4-6",
              totalTokens: 2_500n,
              costUsd: 0.045,
            }),
            node("waiting", "pending", { waitingOn: ["running"] }),
            node("done", "completed", { durationMs: 1_000n }),
            node("failed", "failed", { durationMs: 500n }),
          ],
          [["running", "waiting"]],
        )}
      />,
    );

    const toggle = screen.getByRole("button", { name: /2 active agents; 4 delegated tasks/i });
    expect(toggle.getAttribute("aria-expanded")).toBe("false");
    expect(toggle.textContent).toContain("1 running");
    expect(toggle.textContent).toContain("1 waiting");
    expect(toggle.textContent).toContain("1 completed");
    expect(toggle.textContent).toContain("1 failed");
    // The collapsed summary still surfaces what the busiest agent is doing.
    expect(toggle.textContent).toContain("executor · Editing the API");
    expect(screen.queryByTestId("subagent-dag-edge")).toBeNull();

    fireEvent.click(toggle);
    expect(toggle.getAttribute("aria-expanded")).toBe("true");
    expect(screen.getByText("Task running")).toBeTruthy();
    expect(screen.getByText("Editing the API")).toBeTruthy();
    expect(screen.getByText("claude-sonnet-4-6")).toBeTruthy();
    expect(screen.getByText("2.5K tok")).toBeTruthy();
    expect(screen.getByText("$0.045")).toBeTruthy();
    expect(screen.getByText("Task waiting")).toBeTruthy();
    expect(screen.getByText("Waiting on 1 task")).toBeTruthy();
    expect(screen.getByText("Task done")).toBeTruthy();
    expect(screen.getByText("Task failed")).toBeTruthy();
    expect(screen.queryByText("Main agent")).toBeNull();
    expect(screen.getAllByTestId("subagent-dag-edge")).toHaveLength(1);
  });

  it("lets the pinned DAG be expanded and collapsed again, remembering the choice", () => {
    const dagGraph = graph([node("running", "started", { label: "Implement composer DAG" })]);
    const { unmount } = render(<ActiveSubagentsDock graph={dagGraph} />);

    const toggle = screen.getByRole("button", { name: /1 active agent; 1 delegated task/i });
    expect(toggle.getAttribute("aria-expanded")).toBe("false");
    expect(screen.queryByText("Implement composer DAG")).toBeNull();

    fireEvent.click(toggle);
    expect(toggle.getAttribute("aria-expanded")).toBe("true");
    expect(screen.getByText("Implement composer DAG")).toBeTruthy();

    // An expanded dock stays expanded on the next mount.
    unmount();
    render(<ActiveSubagentsDock graph={dagGraph} />);
    const remounted = screen.getByRole("button", { name: /1 active agent; 1 delegated task/i });
    expect(remounted.getAttribute("aria-expanded")).toBe("true");

    fireEvent.click(remounted);
    expect(remounted.getAttribute("aria-expanded")).toBe("false");
    expect(screen.queryByText("Implement composer DAG")).toBeNull();
  });

  it("ignores duration-bearing nodes with stale nonterminal status", () => {
    render(
      <ActiveSubagentsDock
        graph={graph([node("stale", "running", { durationMs: 12_300n })])}
      />,
    );

    expect(screen.queryByRole("region", { name: "Active delegated agents" })).toBeNull();
  });

  it("normalizes a duration-bearing stale status inside an active graph", () => {
    render(
      <ActiveSubagentsDock
        graph={graph([
          node("active", "running"),
          node("stale", "running", {
            durationMs: 12_300n,
            currentStep: "An outdated running step",
          }),
        ])}
      />,
    );

    const toggle = screen.getByRole("button", { name: /1 active agent; 2 delegated tasks/i });
    expect(toggle.textContent).toContain("1 running");
    expect(toggle.textContent).toContain("1 completed");
    fireEvent.click(toggle);
    const stale = screen.getByTitle("Task stale");
    expect(stale.textContent).toContain("Completed");
    expect(stale.textContent).not.toContain("An outdated running step");
  });

  it("announces only a concise status summary", () => {
    render(
      <ActiveSubagentsDock
        graph={graph([
          node("running", "running", { currentStep: "A frequently changing detail" }),
          node("waiting", "pending"),
        ])}
      />,
    );

    const status = screen.getByRole("status");
    expect(status.getAttribute("aria-atomic")).toBe("true");
    expect(status.textContent).toContain("2 delegated agents are active. 1 running. 1 waiting.");
    expect(status.textContent).not.toContain("A frequently changing detail");
  });

  it("opens the full graph from the dock", () => {
    const onOpenGraph = vi.fn();
    render(
      <ActiveSubagentsDock
        graph={graph([node("running", "initializing")])}
        onOpenGraph={onOpenGraph}
      />,
    );

    fireEvent.click(screen.getByRole("button", { name: "View full subagent graph" }));
    expect(onOpenGraph).toHaveBeenCalledOnce();
  });
});
