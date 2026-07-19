import { afterEach, describe, expect, it } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { create } from "@bufbuild/protobuf";

import type { ActivityGroup } from "@/lib/activityGrouping";
import { ActivityEntrySchema } from "@/rpc/platform/service_pb";
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
    expect(screen.queryByTitle("Review area 0")).toBeNull();
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

    const row = screen.getByTitle("Review area 0");
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

  it("indents dependent tasks by wave and names their dependencies", () => {
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
    expect(rows[1].textContent).toContain("after Review area 0");
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

    expect(screen.getByTitle("Review area 0").textContent).toContain(
      "scanning the API surface",
    );
    const doneRow = screen.getByTitle("Review area 1");
    expect(doneRow.textContent).toContain("12.4K tok");
    expect(doneRow.textContent).toContain("$0.0123");
  });
});
