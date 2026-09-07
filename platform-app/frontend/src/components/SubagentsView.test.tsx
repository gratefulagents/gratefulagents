import { create, type MessageInitShape } from "@bufbuild/protobuf";
import { cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ActivityEntrySchema, SubagentGraphEdgeSchema, SubagentGraphNodeSchema, SubagentGraphSchema, type SubagentGraphNode } from "@/rpc/platform/service_pb";
import { SubagentsView } from "./SubagentsView";

vi.mock("@/components/FullActivityLog", () => ({
  ActivityLogTable: ({ entries }: { entries: { message: string }[] }) => <div data-testid="activity">{entries.map((entry) => entry.message).join(" | ")}</div>,
}));

afterEach(cleanup);

function node(id: string, overrides: MessageInitShape<typeof SubagentGraphNodeSchema> = {}) {
  return create(SubagentGraphNodeSchema, {
    id: `task:${id}`, taskId: id, kind: "subagent", label: `Task ${id}`, subtitle: "executor",
    status: "running", timestampUnix: 100n, ...overrides,
  });
}

function graph(nodes: SubagentGraphNode[]) {
  return create(SubagentGraphSchema, {
    hasSubagents: true, rootId: "root", nodes: [node("root", { id: "root", kind: "root", label: "Main agent" }), ...nodes],
  });
}

function rows() {
  return within(screen.getByRole("region", { name: "Subagent tasks" })).queryAllByRole("button");
}

function filter(name: string) {
  fireEvent.click(within(screen.getByRole("group", { name: "Filter subagents" })).getByRole("button", { name: new RegExp(`^${name} `) }));
}

describe("SubagentsView", () => {
  it.each(["b", "task:b"])("opens requested task %s in the list after graph mode, including repeated requests", (taskId) => {
    const data = graph([node("a"), node("b", { status: "completed" })]);
    const { rerender } = render(<SubagentsView graph={data} entries={[]} />);
    for (let attempt = 0; attempt < 2; attempt++) {
      filter("Running");
      fireEvent.click(screen.getByRole("button", { name: "View graph" }));
      const selectionRequest = { taskId };
      rerender(<SubagentsView graph={data} entries={[]} selectionRequest={selectionRequest} />);
      expect(screen.getByRole("button", { name: "View graph" })).toBeTruthy();
      expect(rows()).toHaveLength(2);
      expect(rows()[1].getAttribute("aria-pressed")).toBe("true");
      expect(within(screen.getByRole("region", { name: "Subagent detail" })).getByRole("heading", { name: "Task b" })).toBeTruthy();
      fireEvent.click(rows()[0]);
      rerender(<SubagentsView graph={data} entries={[]} selectionRequest={selectionRequest} />);
      expect(rows()[0].getAttribute("aria-pressed")).toBe("true");
    }
  });

  it("applies pending selection when the requested task arrives", () => {
    const selectionRequest = { taskId: "b" };
    const { rerender } = render(<SubagentsView entries={[]} selectionRequest={selectionRequest} />);
    rerender(<SubagentsView graph={graph([node("a")])} entries={[]} selectionRequest={selectionRequest} />);
    expect(rows()[0].getAttribute("aria-pressed")).toBe("true");
    fireEvent.click(screen.getByRole("button", { name: "View graph" }));
    rerender(<SubagentsView graph={graph([node("a"), node("b")])} entries={[]} selectionRequest={selectionRequest} />);
    expect(rows()[1].getAttribute("aria-pressed")).toBe("true");
    expect(screen.getByRole("button", { name: "View graph" })).toBeTruthy();
  });

  it("defaults to readable tasks, latest activity and assignment, with the graph secondary", () => {
    render(<SubagentsView graph={graph([node("a", { description: "# Objective: Build a readable inspector", currentStep: "Updating the task list" })])} entries={[]} />);
    expect(rows()).toHaveLength(1);
    expect(rows()[0].textContent).toContain("Build a readable inspector");
    expect(rows()[0].textContent).toContain("Running");
    expect(rows()[0].textContent).toContain("Latest: Updating the task list");
    expect(screen.getByRole("region", { name: "Assignment" })).toBeTruthy();
    expect(screen.queryByText("Subagent graph")).toBeNull();
    expect(screen.queryByText("Result")).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "View graph" }));
    expect(screen.getByText("Subagent graph")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "View list" }));
    expect(rows()[0].getAttribute("aria-pressed")).toBe("true");
  });

  it("filters normalized statuses without treating stopped, unknown or duration as successful completion", () => {
    render(<SubagentsView graph={graph([
      node("live", { status: " ReSuMeD " }),
      node("wait", { status: "dependency_wait" }),
      node("blocked", { waitingOn: ["live"] }),
      node("fail", { status: "timed_out" }),
      node("done", { status: "DONE" }),
      node("stop", { status: "cancelled" }),
      node("unknown", { status: "future_status" }),
      node("stale", { durationMs: 500n }),
    ])} entries={[]} />);
    expect(rows()).toHaveLength(8);
    expect(rows().some((row) => row.textContent?.includes("Finished · status unconfirmed"))).toBe(true);
    expect(rows().some((row) => row.textContent?.includes("Unknown status: future_status"))).toBe(true);
    for (const [name, count] of [["Running", 1], ["Waiting", 2], ["Failed", 1], ["Completed", 1]] as const) {
      filter(name);
      expect(rows()).toHaveLength(count);
    }
    expect(rows()[0].textContent).toContain("Task done");
    expect(screen.queryByText("Result")).toBeNull();
    filter("All");
    expect(rows()).toHaveLength(8);
  });

  it("keeps ordering and selection through reordered snapshots, status changes, filters and removal", () => {
    const a = node("a");
    const b = node("b");
    const { rerender } = render(<SubagentsView graph={graph([b, a])} entries={[]} />);
    expect(rows()[0].textContent).toContain("Task a");
    fireEvent.click(rows()[1]);
    rerender(<SubagentsView graph={graph([node("b", { status: "completed" }), a, node("c", { timestampUnix: 101n })])} entries={[]} />);
    expect(rows()[1].getAttribute("aria-pressed")).toBe("true");
    expect(rows()[1].textContent).toContain("Task b");
    filter("Running");
    expect(screen.getByText("Selected task is outside this filter.")).toBeTruthy();
    expect(within(screen.getByRole("region", { name: "Subagent detail" })).getByRole("heading", { name: "Task b" })).toBeTruthy();
    filter("All");
    expect(rows()[1].getAttribute("aria-pressed")).toBe("true");
    rerender(<SubagentsView graph={graph([a])} entries={[]} />);
    expect(rows()[0].getAttribute("aria-pressed")).toBe("true");
  });

  it("names blockers and dependencies and marks missing dependency data explicitly", () => {
    const data = graph([node("a", { label: "Build the feature" }), node("b", { status: "queued", waitingOn: ["a", "missing"], dependsOn: ["a"] })]);
    data.edges.push(create(SubagentGraphEdgeSchema, { from: "task:a", to: "task:b", kind: "depends-on" }));
    render(<SubagentsView graph={data} entries={[]} />);
    expect(rows()[1].textContent).toContain("Waiting on: Build the feature, Unavailable task (missing)");
    fireEvent.click(rows()[1]);
    expect(screen.getByText("Depends on: Build the feature")).toBeTruthy();
  });

  it("uses durable event IDs, never unrelated positional results, and updates as activity arrives", () => {
    const data = graph([node("a", { status: "completed", detailEntryEventIds: [20n, 30n], detailEntryIndices: [0] })]);
    const unrelated = create(ActivityEntrySchema, { eventId: 1n, subagentResultText: "Unrelated result", message: "Unrelated activity" });
    const progress = create(ActivityEntrySchema, { eventId: 20n, type: "subagent_progress", subagentCurrentStep: "Checking files", message: "Assignment activity", subagentPrompt: "Implement the task list and regression tests." });
    const result = create(ActivityEntrySchema, { eventId: 30n, type: "subagent_notification", subagentResultText: "Changed three components.", message: "Returned findings" });
    const { rerender } = render(<SubagentsView graph={data} entries={[unrelated, progress]} />);
    expect(screen.queryByText("Result")).toBeNull();
    expect(screen.getByTestId("activity").textContent).toBe("Assignment activity");
    expect(screen.getByText("Implement the task list and regression tests.")).toBeTruthy();
    rerender(<SubagentsView graph={data} entries={[unrelated, progress, result]} />);
    expect(screen.getByText("Result")).toBeTruthy();
    expect(screen.getByText("Changed three components.")).toBeTruthy();
    expect(screen.queryByText("Unrelated result")).toBeNull();
    expect(rows()[0].textContent).toContain("Latest: Returned findings");
    expect(screen.queryByText(/accepted|success/i)).toBeNull();
  });

  it("supports inline agents and renders only the correlated tool result, including errors", () => {
    const entries = [
      create(ActivityEntrySchema, { type: "tool_result", toolUseId: "child", output: "Child command output" }),
      create(ActivityEntrySchema, { type: "tool_result", toolUseId: "spawn", output: "Could not finish the assignment", isError: true }),
    ];
    render(<SubagentsView graph={graph([node("inline", { kind: "inline-subagent", toolUseId: "spawn", status: "failed", detailEntryIndices: [0, 1, 99] })])} entries={entries} />);
    expect(screen.getByText("Error")).toBeTruthy();
    expect(screen.getByText("Could not finish the assignment")).toBeTruthy();
    expect(screen.queryByText("Child command output")).toBeNull();
  });

  it("shows empty graph and empty filter states", () => {
    const { rerender } = render(<SubagentsView entries={[]} />);
    expect(screen.getByText("No subagents observed")).toBeTruthy();
    rerender(<SubagentsView graph={graph([node("a")])} entries={[]} />);
    filter("Failed");
    expect(screen.getByText("No failed subagents")).toBeTruthy();
    expect(rows()).toHaveLength(0);
  });
});
