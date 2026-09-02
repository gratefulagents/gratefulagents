import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { create } from "@bufbuild/protobuf";

import { ActiveSubagentsDock } from "@/components/run-session/ActiveSubagentsDock";
import type { ActivityGroup } from "@/lib/activityGrouping";
import {
  ActivityEntrySchema,
  SubagentGraphEdgeSchema,
  SubagentGraphNodeSchema,
  SubagentGraphSchema,
} from "@/rpc/platform/service_pb";
import { SubagentDagCard } from "./SubagentDagCard";

afterEach(() => {
  cleanup();
});

type SubagentGroup = Extract<ActivityGroup, { kind: "subagent" }>;

function group(index: number, status = "running", model = ""): SubagentGroup {
  const taskId = `task_${index}`;
  return {
    kind: "subagent",
    entries: [
      create(ActivityEntrySchema, {
        timestampUnix: BigInt(index + 1),
        type: "subagent_started",
        taskId,
        toolUseId: "call_batch",
        parentCallId: "call_batch",
        subagentType: "reviewer",
        subagentStatus: status,
        subagentPrompt: `Review area ${index}`,
      }),
    ],
    taskId,
    subagentType: "reviewer",
    subagentDescription: `Review area ${index}`,
    subagentStatus: status,
    toolCount: 0,
    totalTokens: 0n,
    durationMs: 0n,
    subagentModel: model,
    subagentCostUsd: 0,
    subagentCostKnown: false,
    subagentNumTurns: 0,
    subagentStopReason: "",
    subagentPrompt: `Review area ${index}`,
    subagentResultText: "",
    parentCallId: "call_batch",
  };
}

describe("SubagentDagCard", () => {
  it.each([2, 8])("keeps a %i-task transcript delegation collapsed", (count) => {
    const groups = Array.from({ length: count }, (_, index) => group(index));

    render(<SubagentDagCard groups={groups} />);

    const summary = screen.getByRole("button", {
      name: new RegExp(`Delegated ${count} tasks`),
    });
    expect(summary.getAttribute("aria-expanded")).toBe("false");
    expect(screen.queryByTitle("#1 Review area 0")).toBeNull();
  });

  it("expands into a compact historical task roster instead of an inline DAG", () => {
    const groups = Array.from({ length: 4 }, (_, index) => group(index));

    render(<SubagentDagCard groups={groups} />);

    const summary = screen.getByRole("button", {
      name: new RegExp(`Delegated ${groups.length} tasks`),
    });
    fireEvent.click(summary);

    expect(summary.getAttribute("aria-expanded")).toBe("true");
    expect(screen.getAllByTestId("subagent-roster-row")).toHaveLength(groups.length);
    expect(screen.queryByTestId("subagent-dag-edge")).toBeNull();
  });

  it("shows each subagent model in the expanded task roster", () => {
    render(<SubagentDagCard groups={[group(0, "running", "gpt-5.4")]} />);

    fireEvent.click(screen.getByRole("button", { name: /Delegated 1 task/i }));

    expect(screen.getByTitle("gpt-5.4").textContent).toBe("gpt-5.4");
  });

  it.each([
    ["started", "running"],
    ["pending", "waiting"],
    ["queued", "waiting"],
    ["waiting", "waiting"],
  ])("does not show a completion icon for %s roster tasks", (status, label) => {
    const groups = Array.from({ length: 3 }, (_, index) =>
      group(index, index === 0 ? status : "running"),
    );

    render(<SubagentDagCard groups={groups} />);
    fireEvent.click(
      screen.getByRole("button", {
        name: new RegExp(`Delegated ${groups.length} tasks`),
      }),
    );

    const row = screen.getByTitle("#1 Review area 0");
    expect(row.textContent).toContain(label);
    expect(row.querySelector("svg.lucide-check")).toBeNull();
  });

  it("shows a completion progress bar only while tasks are in flight", () => {
    const inFlight = [group(0, "completed"), group(1, "running"), group(2, "waiting")];
    const { rerender } = render(<SubagentDagCard groups={inFlight} />);

    const bar = screen.getByRole("progressbar", { name: /1 of 3 tasks finished/i });
    expect(bar.getAttribute("aria-valuenow")).toBe("1");
    expect(bar.getAttribute("aria-valuemax")).toBe("3");

    rerender(
      <SubagentDagCard groups={[group(0, "completed"), group(1, "completed"), group(2, "failed")]} />,
    );
    expect(screen.queryByRole("progressbar")).toBeNull();
  });

  it("indents dependent tasks by wave and references their dependencies by ordinal", () => {
    const dependent = group(1, "completed");
    dependent.entries[0].subagentDependsOn = ["task_0"];
    const groups = [group(0, "completed"), dependent];

    render(<SubagentDagCard groups={groups} waves={[0, 1]} />);
    fireEvent.click(screen.getByRole("button", { name: /Delegated 2 tasks/ }));

    const rows = screen.getAllByTestId("subagent-roster-row");
    expect(rows[0].querySelector("svg.lucide-corner-down-right")).toBeNull();
    expect(rows[0].style.paddingLeft).toBe("");
    expect(rows[1].querySelector("svg.lucide-corner-down-right")).not.toBeNull();
    expect(rows[1].style.paddingLeft).toBe("24px");
    expect(rows[1].textContent).toContain("after #1");
  });

  it("surfaces per-task tokens, cost, and the live step in the roster", () => {
    const running = group(0, "running");
    running.entries.push(
      create(ActivityEntrySchema, {
        timestampUnix: 5n,
        type: "subagent_progress",
        taskId: running.taskId,
        subagentStatus: "running",
        subagentCurrentStep: "scanning the API surface",
      }),
    );
    const done = group(1, "completed");
    done.totalTokens = 12_400n;
    done.subagentCostUsd = 0.0123;
    done.subagentCostKnown = true;

    render(<SubagentDagCard groups={[running, done]} />);
    fireEvent.click(screen.getByRole("button", { name: /Delegated 2 tasks/ }));

    expect(screen.getByTitle("#1 Review area 0").textContent).toContain(
      "scanning the API surface",
    );
    const doneRow = screen.getByTitle("#2 Review area 1");
    expect(doneRow.textContent).toContain("12.4K tok");
    expect(doneRow.textContent).toContain("$0.0123");
  });

  it("numbers tasks wave-major, matching the active-agents dock for the same shape", () => {
    // Group order is A, B, C but B depends on A, so the visual (wave) order is
    // A, C, B — both surfaces must agree on that numbering.
    const a = group(0, "completed");
    const b = group(1, "running");
    b.entries[0].subagentDependsOn = ["task_0"];
    const c = group(2, "running");

    render(<SubagentDagCard groups={[a, b, c]} waves={[0, 1, 0]} />);
    fireEvent.click(screen.getByRole("button", { name: /Delegated 3 tasks/ }));
    const cardTitles = screen
      .getAllByTestId("subagent-roster-row")
      .map((row) => row.getAttribute("title"));
    expect(cardTitles).toEqual(["#1 Review area 0", "#2 Review area 2", "#3 Review area 1"]);
    cleanup();

    const node = (id: string, index: number, status: string) =>
      create(SubagentGraphNodeSchema, {
        id,
        taskId: id,
        kind: "subagent",
        label: `Review area ${index}`,
        subtitle: "reviewer",
        status,
        timestampUnix: BigInt(index + 1),
      });
    const spawn = (to: string) =>
      create(SubagentGraphEdgeSchema, { id: `root=>${to}`, from: "root", to, kind: "spawn" });
    const graph = create(SubagentGraphSchema, {
      hasSubagents: true,
      rootId: "root",
      nodes: [
        create(SubagentGraphNodeSchema, { id: "root", kind: "root", label: "Main", status: "running" }),
        node("task_0", 0, "completed"),
        node("task_1", 1, "running"),
        node("task_2", 2, "running"),
      ],
      edges: [
        spawn("task_0"),
        spawn("task_1"),
        spawn("task_2"),
        create(SubagentGraphEdgeSchema, {
          id: "task_0=>task_1",
          from: "task_0",
          to: "task_1",
          kind: "depends-on",
        }),
      ],
    });
    render(<ActiveSubagentsDock graph={graph} />);
    const toggle = screen.getByRole("button", { name: /2 active agents; 3 delegated tasks/i });
    if (toggle.getAttribute("aria-expanded") !== "true") fireEvent.click(toggle);
    const dockTitles = screen
      .getAllByTestId("subagent-dock-card")
      .map((card) => card.getAttribute("title"));
    expect(dockTitles).toEqual(cardTitles);
  });

  it("turns the #n chip into an 'open in graph' button when a callback is wired", () => {
    const onOpenGraph = vi.fn();
    render(<SubagentDagCard groups={[group(0)]} onOpenGraph={onOpenGraph} />);
    fireEvent.click(screen.getByRole("button", { name: /Delegated 1 task/ }));

    fireEvent.click(screen.getByRole("button", { name: "Open task #1 in graph" }));
    expect(onOpenGraph).toHaveBeenCalledOnce();
  });
});

describe("SubagentDagCard shared ordinals", () => {
  it("numbers rows from the run-wide graph when provided", async () => {
    const { SubagentContextProvider } = await import("./subagentContext");
    const graph = create(SubagentGraphSchema, {
      rootId: "root",
      hasSubagents: true,
      nodes: [
        create(SubagentGraphNodeSchema, { id: "root", kind: "root", label: "main", status: "running" }),
        create(SubagentGraphNodeSchema, { id: "task_a", kind: "subagent", label: "A", status: "completed", timestampUnix: 1n }),
        create(SubagentGraphNodeSchema, { id: "task_b", kind: "subagent", label: "B", status: "completed", timestampUnix: 2n }),
        create(SubagentGraphNodeSchema, { id: "task_c", kind: "subagent", label: "C", status: "completed", timestampUnix: 3n }),
      ],
      edges: ["task_a", "task_b", "task_c"].map((to) =>
        create(SubagentGraphEdgeSchema, { id: `root=>${to}`, from: "root", to, kind: "spawn" }),
      ),
    });
    // A card that only sees tasks b and c must still call them #2 and #3.
    const c = { ...group(2, "completed"), taskId: "task_c" };
    const b = { ...group(1, "completed"), taskId: "task_b" };
    render(
      <SubagentContextProvider graph={graph}>
        <SubagentDagCard groups={[c, b]} />
      </SubagentContextProvider>,
    );
    fireEvent.click(screen.getByRole("button", { expanded: false }));
    const rows = screen.getAllByTestId("subagent-roster-row");
    expect(rows.map((r) => r.getAttribute("title")?.slice(0, 2))).toEqual(["#2", "#3"]);
  });
});
