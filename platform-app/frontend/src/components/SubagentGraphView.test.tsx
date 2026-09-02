import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { create } from "@bufbuild/protobuf";

import {
  SubagentGraphEdgeSchema,
  SubagentGraphNodeSchema,
  SubagentGraphSchema,
} from "@/rpc/platform/service_pb";
import { SubagentGraphView } from "./SubagentGraphView";

beforeEach(() => {
  // jsdom has no ResizeObserver; both the graph canvas and react-resizable-panels need one.
  globalThis.ResizeObserver ??= class {
    observe() {}
    unobserve() {}
    disconnect() {}
  };
});

afterEach(() => {
  cleanup();
  vi.useRealTimers();
});

function node(id: string, extra: Record<string, unknown> = {}) {
  return create(SubagentGraphNodeSchema, {
    id,
    kind: "subagent",
    parentId: "root",
    label: id,
    subtitle: "executor",
    status: "running",
    ...extra,
  });
}

function spawn(from: string, to: string) {
  return create(SubagentGraphEdgeSchema, { id: `${from}=>${to}`, from, to, kind: "spawned" });
}

const root = create(SubagentGraphNodeSchema, {
  id: "root",
  kind: "root",
  label: "Main agent",
  status: "running",
});

/** root → build (working) and root → verify (waiting on build). */
function dagGraph() {
  return create(SubagentGraphSchema, {
    rootId: "root",
    hasSubagents: true,
    nodes: [
      root,
      node("task:build", { label: "Build the feature", currentStep: "editing files" }),
      node("task:verify", {
        label: "Verify the feature",
        subtitle: "verifier",
        status: "waiting",
        waitingOn: ["build"],
      }),
    ],
    edges: [
      spawn("root", "task:build"),
      spawn("root", "task:verify"),
      create(SubagentGraphEdgeSchema, {
        id: "task:build=>task:verify",
        from: "task:build",
        to: "task:verify",
        kind: "depends-on",
      }),
    ],
  });
}

const nodeButtons = () =>
  screen.getAllByRole("button").filter((b) => b.hasAttribute("data-node-id"));

describe("SubagentGraphView", () => {
  it("shows the model on each subagent DAG node", () => {
    const graph = create(SubagentGraphSchema, {
      rootId: "root",
      hasSubagents: true,
      nodes: [
        root,
        node("task:review", { label: "Review changes", subtitle: "reviewer", model: "gpt-5.4" }),
      ],
      edges: [spawn("root", "task:review")],
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
        root,
        node("task:live", {
          label: "Live work",
          timestampUnix: BigInt(Date.parse("2026-04-06T10:00:00Z") / 1_000),
          model: "gpt-5.4",
          totalTokens: 1_250n,
          costUsd: 0.0123,
        }),
      ],
      edges: [spawn("root", "task:live")],
    });

    render(<SubagentGraphView graph={graph} entries={[]} />);

    const liveNode = screen.getByRole("button", { name: /Live work/ });
    expect(liveNode.textContent).toContain("10.0s");
    expect(liveNode.textContent).toContain("gpt-5.4");
    expect(liveNode.textContent).toContain("1.3K tok");
    expect(liveNode.textContent).toContain("$0.012");
  });

  it("presents dependency-gated live nodes as waiting instead of live", () => {
    render(<SubagentGraphView graph={dagGraph()} entries={[]} />);

    const workingNode = screen.getByRole("button", { name: /Build the feature/ });
    expect(workingNode.textContent).toContain("live");
    expect(workingNode.textContent).toContain("editing files");

    const waitingNode = screen.getByRole("button", { name: /Verify the feature/ });
    expect(waitingNode.textContent).toContain("waiting");
    expect(waitingNode.textContent).not.toContain("live");
    expect(waitingNode.textContent).toContain("waiting for dependencies…");
  });

  it("starts with nothing selected and nothing dimmed", () => {
    render(<SubagentGraphView graph={dagGraph()} entries={[]} />);

    for (const b of nodeButtons()) {
      expect(b.getAttribute("aria-pressed")).toBe("false");
      expect(b.className).not.toContain("opacity-40");
    }
    expect(screen.getByText("Click a node for details")).toBeTruthy();
  });

  it("selects on click, dims non-adjacent nodes, and clears on a second click", () => {
    render(<SubagentGraphView graph={dagGraph()} entries={[]} />);

    const build = screen.getByRole("button", { name: /Build the feature/ });
    fireEvent.click(build);
    expect(build.getAttribute("aria-pressed")).toBe("true");
    expect(screen.queryByText("Click a node for details")).toBeNull();

    fireEvent.click(build);
    expect(build.getAttribute("aria-pressed")).toBe("false");
    expect(nodeButtons().some((b) => b.className.includes("opacity-40"))).toBe(false);
    expect(screen.getByText("Click a node for details")).toBeTruthy();
  });

  it("dims nodes not adjacent to the selection", () => {
    const graph = dagGraph();
    graph.nodes.push(node("task:far", { label: "Far away", parentId: "task:build" }));
    graph.edges.push(spawn("task:build", "task:far"));
    render(<SubagentGraphView graph={graph} entries={[]} />);

    fireEvent.click(screen.getByRole("button", { name: /Far away/ }));
    expect(screen.getByRole("button", { name: /Verify the feature/ }).className).toContain("opacity-40");
    expect(screen.getByRole("button", { name: /Build the feature/ }).className).not.toContain("opacity-40");
  });

  it("clears the selection with Escape", () => {
    render(<SubagentGraphView graph={dagGraph()} entries={[]} />);

    const build = screen.getByRole("button", { name: /Build the feature/ });
    fireEvent.click(build);
    expect(build.getAttribute("aria-pressed")).toBe("true");

    fireEvent.keyDown(screen.getByRole("group", { name: "Sub-agent graph" }), { key: "Escape" });
    expect(build.getAttribute("aria-pressed")).toBe("false");
  });

  it("moves the selection with arrow keys", () => {
    render(<SubagentGraphView graph={dagGraph()} entries={[]} />);
    const group = screen.getByRole("group", { name: "Sub-agent graph" });

    fireEvent.keyDown(group, { key: "ArrowDown" });
    // Layout order is top-to-bottom: the first child sits at y=0, above the centred root.
    expect(screen.getByRole("button", { name: /Build the feature/ }).getAttribute("aria-pressed")).toBe("true");

    fireEvent.keyDown(group, { key: "ArrowLeft" });
    expect(screen.getByRole("button", { name: /Main agent/ }).getAttribute("aria-pressed")).toBe("true");

    fireEvent.keyDown(group, { key: "ArrowRight" });
    expect(screen.getByRole("button", { name: /Build the feature/ }).getAttribute("aria-pressed")).toBe("true");

    fireEvent.keyDown(group, { key: "ArrowDown" });
    expect(screen.getByRole("button", { name: /Build the feature/ }).getAttribute("aria-pressed")).toBe("false");
    expect(nodeButtons().filter((b) => b.getAttribute("aria-pressed") === "true")).toHaveLength(1);
  });

  it("shows the shared #n ordinal on sub-agent nodes and in the detail panel", () => {
    render(<SubagentGraphView graph={dagGraph()} entries={[]} />);

    const build = screen.getByRole("button", { name: /Build the feature/ });
    const verify = screen.getByRole("button", { name: /Verify the feature/ });
    expect(build.textContent).toContain("#1");
    expect(verify.textContent).toContain("#2");
    expect(screen.getByRole("button", { name: /Main agent/ }).textContent).not.toMatch(/#\d/);

    fireEvent.click(verify);
    expect(screen.getAllByText("#2").length).toBe(2);
  });

  it("animates edges whose target is working and leaves others static", () => {
    const { container } = render(<SubagentGraphView graph={dagGraph()} entries={[]} />);

    const toBuild = container.querySelector('path[data-edge-id="spawn:root->task:build"]')!;
    expect(toBuild.getAttribute("class")).toContain("animate-dash-flow");
    expect(toBuild.getAttribute("stroke-dasharray")).toBe("6 6");

    const toVerify = container.querySelector('path[data-edge-id="spawn:root->task:verify"]')!;
    expect(toVerify.getAttribute("class")).not.toContain("animate-dash-flow");
    expect(toVerify.getAttribute("stroke-dasharray")).toBeNull();
  });

  it("describes each node for assistive tech", () => {
    render(<SubagentGraphView graph={dagGraph()} entries={[]} />);

    const verify = screen.getByRole("button", { name: /Verify the feature/ });
    const desc = document.getElementById(verify.getAttribute("aria-describedby")!)!;
    expect(desc.textContent).toContain("spawned by Main agent");
    expect(desc.textContent).toContain("depends on executor: Build the feature");
  });

  it("switches to compact node dimensions when the container is narrow", () => {
    const spy = vi.spyOn(HTMLElement.prototype, "clientWidth", "get").mockReturnValue(400);
    try {
      render(<SubagentGraphView graph={dagGraph()} entries={[]} />);
      const build = screen.getByRole("button", { name: /Build the feature/ });
      expect(build.style.width).toBe("200px");
      expect(build.style.height).toBe("64px");
    } finally {
      spy.mockRestore();
    }
  });

  it("uses full node dimensions when the container is wide", () => {
    const spy = vi.spyOn(HTMLElement.prototype, "clientWidth", "get").mockReturnValue(1200);
    try {
      render(<SubagentGraphView graph={dagGraph()} entries={[]} />);
      const build = screen.getByRole("button", { name: /Build the feature/ });
      expect(build.style.width).toBe("264px");
    } finally {
      spy.mockRestore();
    }
  });
});
