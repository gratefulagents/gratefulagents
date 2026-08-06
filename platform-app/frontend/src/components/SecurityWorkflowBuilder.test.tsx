import { create, equals, toJson } from "@bufbuild/protobuf";
import { afterEach, describe, expect, it } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";

import {
  SecurityWorkflowBuilder,
  emptyWorkflowTask,
  validateWorkflowTasks,
  workflowCycle,
  workflowLayers,
  workflowTasksFromProto,
  workflowTasksToProto,
  type WorkflowTaskDraft,
} from "@/components/SecurityWorkflowBuilder";
import {
  SecurityScanTaskConfigSchema,
  type SecurityScanTaskConfig,
} from "@/rpc/platform/service_pb";

afterEach(() => cleanup());

/** An advanced workflow exercising every optional SecurityScanTask field. */
function advancedProtoTasks(): SecurityScanTaskConfig[] {
  return [
    create(SecurityScanTaskConfigSchema, {
      name: "recon",
      objective: "Map the attack surface,\nincluding multi-line notes.",
      category: "recon",
      role: "threat-modeler",
      model: "gpt-5.2-pro",
      maxFindings: 3,
    }),
    create(SecurityScanTaskConfigSchema, {
      name: "injection-hunt",
      objective: "Hunt injections.",
      category: "injection",
      role: "vulnerability-hunter",
      dependsOn: ["recon"],
      maxFindings: 25,
    }),
    create(SecurityScanTaskConfigSchema, {
      name: "authz-hunt",
      objective: "Hunt authz flaws.",
      dependsOn: ["recon"],
    }),
    create(SecurityScanTaskConfigSchema, {
      name: "triage",
      objective: "Triage and report.",
      category: "triage",
      role: "finding-triager",
      model: "claude-opus-4-6",
      dependsOn: ["injection-hunt", "authz-hunt"],
    }),
  ];
}

describe("workflow round-trip", () => {
  it("serializes an untouched advanced workflow identically", () => {
    const original = advancedProtoTasks();
    const roundTripped = workflowTasksToProto(workflowTasksFromProto(original));
    expect(roundTripped).toHaveLength(original.length);
    original.forEach((task, i) => {
      expect(
        equals(SecurityScanTaskConfigSchema, roundTripped[i], task),
        `task ${task.name} drifted: ${JSON.stringify(toJson(SecurityScanTaskConfigSchema, roundTripped[i]))}`,
      ).toBe(true);
    });
  });

  it("survives a second round-trip (stable fixed point)", () => {
    const once = workflowTasksFromProto(advancedProtoTasks());
    const twice = workflowTasksFromProto(workflowTasksToProto(once));
    expect(twice).toEqual(once);
  });
});

function draft(overrides: Partial<WorkflowTaskDraft>): WorkflowTaskDraft {
  return { ...emptyWorkflowTask(), name: "task", objective: "do things", ...overrides };
}

describe("validateWorkflowTasks", () => {
  it("accepts the advanced workflow", () => {
    expect(validateWorkflowTasks(workflowTasksFromProto(advancedProtoTasks()))).toEqual([]);
  });

  it("rejects an empty workflow", () => {
    expect(validateWorkflowTasks([])[0].field).toBe("tasks");
  });

  it("rejects duplicate names", () => {
    const errors = validateWorkflowTasks([draft({ name: "a" }), draft({ name: "a" })]);
    expect(errors.some((e) => e.message.includes("Duplicate task name"))).toBe(true);
  });

  it("rejects invalid names, roles, models, and maxFindings", () => {
    const errors = validateWorkflowTasks([
      draft({ name: "Bad Name!", role: "Not A Role", model: "two words", maxFindings: "-2" }),
    ]);
    const fields = errors.map((e) => e.field);
    expect(fields).toContain("tasks[0].name");
    expect(fields).toContain("tasks[0].role");
    expect(fields).toContain("tasks[0].model");
    expect(fields).toContain("tasks[0].maxFindings");
  });

  it("rejects missing objectives", () => {
    const errors = validateWorkflowTasks([draft({ objective: "  " })]);
    expect(errors.some((e) => e.field === "tasks[0].objective")).toBe(true);
  });

  it("rejects dangling and self dependencies", () => {
    const errors = validateWorkflowTasks([
      draft({ name: "a", dependsOn: ["ghost", "a"] }),
    ]);
    expect(errors.some((e) => e.message.includes("unknown task"))).toBe(true);
    expect(errors.some((e) => e.message.includes("depend on itself"))).toBe(true);
  });

  it("rejects dependency cycles", () => {
    const errors = validateWorkflowTasks([
      draft({ name: "a", dependsOn: ["c"] }),
      draft({ name: "b", dependsOn: ["a"] }),
      draft({ name: "c", dependsOn: ["b"] }),
    ]);
    expect(errors).toHaveLength(1);
    expect(errors[0].message).toContain("cycle");
  });
});

describe("workflowCycle / workflowLayers", () => {
  it("finds no cycle in a DAG and layers it topologically", () => {
    const tasks = workflowTasksFromProto(advancedProtoTasks());
    expect(workflowCycle(tasks)).toEqual([]);
    const layers = workflowLayers(tasks).map((layer) => layer.map((t) => t.name));
    expect(layers).toEqual([["recon"], ["injection-hunt", "authz-hunt"], ["triage"]]);
  });

  it("still layers a cyclic graph without hanging", () => {
    const tasks = [draft({ name: "a", dependsOn: ["b"] }), draft({ name: "b", dependsOn: ["a"] })];
    expect(workflowCycle(tasks).length).toBeGreaterThan(0);
    const layers = workflowLayers(tasks);
    expect(layers.flat()).toHaveLength(2);
  });
});

describe("SecurityWorkflowBuilder component", () => {
  it("renders tasks, the DAG, and no errors for a valid workflow", () => {
    const tasks = workflowTasksFromProto(advancedProtoTasks());
    render(<SecurityWorkflowBuilder tasks={tasks} onChange={() => {}} />);
    expect(screen.getByTestId("workflow-dag")).toBeTruthy();
    expect(screen.getByTestId("dag-node-recon")).toBeTruthy();
    expect(screen.getByTestId("dag-node-triage")).toBeTruthy();
    expect(screen.queryByTestId("workflow-errors")).toBeNull();
  });

  it("shows inline validation errors for a cyclic workflow", () => {
    const tasks = [draft({ name: "a", dependsOn: ["b"] }), draft({ name: "b", dependsOn: ["a"] })];
    render(<SecurityWorkflowBuilder tasks={tasks} onChange={() => {}} />);
    expect(screen.getByTestId("workflow-errors").textContent).toContain("cycle");
  });

  it("adds a task via the Add task button", () => {
    let latest: WorkflowTaskDraft[] = [];
    render(
      <SecurityWorkflowBuilder
        tasks={[draft({ name: "a" })]}
        onChange={(next) => {
          latest = next;
        }}
      />,
    );
    fireEvent.click(screen.getByRole("button", { name: "Add task" }));
    expect(latest).toHaveLength(2);
  });

  it("removing a task also detaches it from other tasks' dependencies", () => {
    let latest: WorkflowTaskDraft[] = [];
    render(
      <SecurityWorkflowBuilder
        tasks={[draft({ name: "a" }), draft({ name: "b", dependsOn: ["a"] })]}
        onChange={(next) => {
          latest = next;
        }}
      />,
    );
    fireEvent.click(screen.getAllByRole("button", { name: "Remove task" })[0]);
    expect(latest).toHaveLength(1);
    expect(latest[0].name).toBe("b");
    expect(latest[0].dependsOn).toEqual([]);
  });

  it("renaming a task follows into dependents", () => {
    let latest: WorkflowTaskDraft[] = [];
    render(
      <SecurityWorkflowBuilder
        tasks={[draft({ name: "a" }), draft({ name: "b", dependsOn: ["a"] })]}
        onChange={(next) => {
          latest = next;
        }}
      />,
    );
    const nameInput = document.getElementById("wf-task-name-0");
    expect(nameInput).not.toBeNull();
    fireEvent.change(nameInput!, {
      target: { value: "alpha" },
    });
    expect(latest[0].name).toBe("alpha");
    expect(latest[1].dependsOn).toEqual(["alpha"]);
  });
});
