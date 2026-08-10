import { create, equals, toJson } from "@bufbuild/protobuf";
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";

vi.mock("@/lib/client", () => ({
  client: {
    listSkills: vi.fn().mockResolvedValue({
      skills: [
        {
          name: "api-authz-hunting",
          version: "1",
          description: "Hunt API authorization flaws",
          resolvedDescription: "",
          mcpServerRefs: [],
        },
      ],
    }),
  },
}));

import {
  SecurityWorkflowBuilder,
  WorkflowParametersEditor,
  emptyWorkflowTask,
  validateWorkflowParameters,
  validateWorkflowTasks,
  workflowCycle,
  workflowLayers,
  workflowParametersFromProto,
  workflowParametersToProto,
  workflowTasksFromProto,
  workflowTasksToProto,
  type WorkflowParameterDraft,
  type WorkflowTaskDraft,
} from "@/components/SecurityWorkflowBuilder";
import {
  SecurityScanTaskConfigSchema,
  SecurityWorkflowParameterSchema,
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
      skillRefs: ["api-authz-hunting"],
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
      maxRetries: 0,
      timeout: "20m",
    }),
    create(SecurityScanTaskConfigSchema, {
      name: "triage",
      objective: "Triage and report.",
      category: "triage",
      role: "finding-triager",
      model: "claude-opus-4-6",
      dependsOn: ["injection-hunt", "authz-hunt"],
      maxRetries: 2,
      timeout: "45m",
      maxTurns: 80,
      maxCostUsd: "2.50",
      tools: { allowed: ["read_file", "grep"], denied: ["Bash"] },
      outputSchema: '{"type":"object","properties":{"items":{"type":"array"}}}',
      forEach: "injection-hunt",
      maxInstances: 12,
      repeats: 3,
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

  it("preserves task skill refs in the backend resource shape", () => {
    const drafts = workflowTasksFromProto(advancedProtoTasks());
    expect(drafts[0].skillRefs).toEqual(["api-authz-hunting"]);

    const serialized = workflowTasksToProto(drafts);
    expect(serialized[0].skillRefs).toEqual(["api-authz-hunting"]);
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

  it("adds and removes skills for an individual task", async () => {
    let latest = [draft({ name: "a" })];
    const { rerender } = render(
      <SecurityWorkflowBuilder tasks={latest} onChange={(next) => (latest = next)} />,
    );

    expect(screen.getByText(/Reusable instructions loaded only for this task/)).toBeTruthy();
    fireEvent.click(await screen.findByRole("switch", { name: "Attach api-authz-hunting" }));
    expect(latest[0].skillRefs).toEqual(["api-authz-hunting"]);

    rerender(<SecurityWorkflowBuilder tasks={latest} onChange={(next) => (latest = next)} />);
    fireEvent.click(screen.getByRole("switch", { name: "Attach api-authz-hunting" }));
    expect(latest[0].skillRefs).toEqual([]);
  });
});

describe("validateWorkflowTasks advanced fields", () => {
  it("rejects out-of-range or malformed execution fields", () => {
    const errors = validateWorkflowTasks([
      draft({
        name: "a",
        maxRetries: "11",
        timeout: "30 minutes",
        maxTurns: "-1",
        maxCostUsd: "$5",
        maxInstances: "51",
        repeats: "6",
      }),
    ]);
    const fields = errors.map((e) => e.field);
    expect(fields).toContain("tasks[0].maxRetries");
    expect(fields).toContain("tasks[0].timeout");
    expect(fields).toContain("tasks[0].maxTurns");
    expect(fields).toContain("tasks[0].maxCostUsd");
    expect(fields).toContain("tasks[0].maxInstances");
    expect(fields).toContain("tasks[0].repeats");
  });

  it("rejects an output schema that is not a JSON object", () => {
    const arrays = validateWorkflowTasks([draft({ name: "a", outputSchema: "[1,2]" })]);
    expect(arrays.some((e) => e.field === "tasks[0].outputSchema")).toBe(true);
    const invalid = validateWorkflowTasks([draft({ name: "a", outputSchema: "{nope" })]);
    expect(invalid.some((e) => e.field === "tasks[0].outputSchema")).toBe(true);
    const valid = validateWorkflowTasks([draft({ name: "a", outputSchema: '{"type":"object"}' })]);
    expect(valid).toEqual([]);
  });

  it("rejects forEach naming a task that is not a dependency", () => {
    const errors = validateWorkflowTasks([
      draft({ name: "a" }),
      draft({ name: "b", forEach: "a" }),
    ]);
    expect(errors.some((e) => e.field === "tasks[1].forEach")).toBe(true);
    expect(
      validateWorkflowTasks([draft({ name: "a" }), draft({ name: "b", dependsOn: ["a"], forEach: "a" })]),
    ).toEqual([]);
  });

  it("rejects forEach chained onto a multi-instance source", () => {
    const chained = validateWorkflowTasks([
      draft({ name: "src" }),
      draft({ name: "fan1", dependsOn: ["src"], forEach: "src" }),
      draft({ name: "fan2", dependsOn: ["fan1"], forEach: "fan1" }),
    ]);
    expect(chained.some((e) => e.field === "tasks[2].forEach" && e.message.includes("single-instance"))).toBe(true);

    const repeated = validateWorkflowTasks([
      draft({ name: "rep", repeats: "2" }),
      draft({ name: "fan", dependsOn: ["rep"], forEach: "rep" }),
    ]);
    expect(repeated.some((e) => e.field === "tasks[1].forEach" && e.message.includes("single-instance"))).toBe(true);
  });

  it("rejects single-field output references to multi-instance tasks", () => {
    const errors = validateWorkflowTasks([
      draft({ name: "a", repeats: "3", outputSchema: '{"type":"object"}' }),
      draft({ name: "b", dependsOn: ["a"], objective: "use {{tasks.a.output.summary}}" }),
    ]);
    expect(errors.some((e) => e.field === "tasks[1].objective" && e.message.includes("multi-instance"))).toBe(true);
    expect(
      validateWorkflowTasks([
        draft({ name: "a", repeats: "3", outputSchema: '{"type":"object"}' }),
        draft({ name: "b", dependsOn: ["a"], objective: "use {{tasks.a.output}}" }),
      ]),
    ).toEqual([]);
  });

  it("rejects output references to tasks without an output schema", () => {
    const errors = validateWorkflowTasks([
      draft({ name: "recon" }),
      draft({ name: "hunt", dependsOn: ["recon"], objective: "dig into {{tasks.recon.output}}" }),
    ]);
    const objectiveError = errors.find((e) => e.field === "tasks[1].objective");
    expect(objectiveError?.message).toContain("output schema");
    expect(objectiveError?.message).toContain('"hunt"');
    expect(objectiveError?.message).toContain('"recon"');

    expect(
      validateWorkflowTasks([
        draft({ name: "recon", outputSchema: '{"type":"object"}' }),
        draft({ name: "hunt", dependsOn: ["recon"], objective: "dig into {{tasks.recon.output}}" }),
      ]),
    ).toEqual([]);
    // Referencing the task itself rather than its output needs no schema.
    expect(
      validateWorkflowTasks([
        draft({ name: "recon" }),
        draft({ name: "hunt", dependsOn: ["recon"], objective: "continue {{tasks.recon}}" }),
      ]),
    ).toEqual([]);
  });
});

describe("workflow parameters", () => {
  function protoParameters() {
    return [
      create(SecurityWorkflowParameterSchema, {
        name: "target_service",
        description: "Service under test",
        default: "payments-api",
      }),
      create(SecurityWorkflowParameterSchema, { name: "depth", required: true }),
    ];
  }

  it("round-trips untouched parameters identically", () => {
    const original = protoParameters();
    const roundTripped = workflowParametersToProto(workflowParametersFromProto(original));
    original.forEach((param, i) => {
      expect(equals(SecurityWorkflowParameterSchema, roundTripped[i], param)).toBe(true);
    });
  });

  it("validates parameter names and duplicates", () => {
    const bad: WorkflowParameterDraft[] = [
      { name: "9lives", description: "", default: "", required: false },
      { name: "depth", description: "", default: "", required: false },
      { name: "depth", description: "", default: "", required: true },
    ];
    const errors = validateWorkflowParameters(bad);
    expect(errors.some((e) => e.message.includes("Invalid parameter name"))).toBe(true);
    expect(errors.some((e) => e.message.includes("Duplicate parameter name"))).toBe(true);
    expect(validateWorkflowParameters(workflowParametersFromProto(protoParameters()))).toEqual([]);
  });

  it("adds, edits, and removes rows in the editor", () => {
    let latest: WorkflowParameterDraft[] = [];
    const { rerender } = render(
      <WorkflowParametersEditor parameters={[]} onChange={(next) => (latest = next)} />,
    );
    fireEvent.click(screen.getByRole("button", { name: "Add parameter" }));
    expect(latest).toHaveLength(1);
    rerender(<WorkflowParametersEditor parameters={latest} onChange={(next) => (latest = next)} />);
    fireEvent.change(document.getElementById("wf-param-name-0")!, {
      target: { value: "target_service" },
    });
    expect(latest[0].name).toBe("target_service");
    rerender(<WorkflowParametersEditor parameters={latest} onChange={(next) => (latest = next)} />);
    fireEvent.click(screen.getByRole("checkbox", { name: /required/i }));
    expect(latest[0].required).toBe(true);
    rerender(<WorkflowParametersEditor parameters={latest} onChange={(next) => (latest = next)} />);
    fireEvent.click(screen.getByRole("button", { name: /Remove parameter/ }));
    expect(latest).toHaveLength(0);
  });
});

describe("WorkflowDagEditor interactions", () => {
  it("adds a dependency edge via the connect handle", () => {
    let latest: WorkflowTaskDraft[] | null = null;
    render(
      <SecurityWorkflowBuilder
        tasks={[draft({ name: "a" }), draft({ name: "b" })]}
        onChange={(next) => (latest = next)}
      />,
    );
    fireEvent.click(screen.getByTestId("dag-connect-a"));
    expect(screen.getByTestId("dag-connect-hint").textContent).toContain("Linking from");
    fireEvent.click(screen.getByTestId("dag-node-b"));
    expect(latest).not.toBeNull();
    expect(latest![1].dependsOn).toEqual(["a"]);
    expect(latest![0].dependsOn).toEqual([]);
  });

  it("rejects a duplicate edge with a message", () => {
    let called = 0;
    render(
      <SecurityWorkflowBuilder
        tasks={[draft({ name: "a" }), draft({ name: "b", dependsOn: ["a"] })]}
        onChange={() => called++}
      />,
    );
    fireEvent.click(screen.getByTestId("dag-connect-a"));
    fireEvent.click(screen.getByTestId("dag-node-b"));
    expect(screen.getByTestId("dag-message").textContent).toContain("already depends on");
    expect(called).toBe(0);
  });

  it("rejects an edge that would create a cycle and explains why", () => {
    let called = 0;
    render(
      <SecurityWorkflowBuilder
        tasks={[draft({ name: "a" }), draft({ name: "b", dependsOn: ["a"] })]}
        onChange={() => called++}
      />,
    );
    // b already runs after a; making a run after b closes the loop.
    fireEvent.click(screen.getByTestId("dag-connect-b"));
    fireEvent.click(screen.getByTestId("dag-node-a"));
    expect(screen.getByTestId("dag-message").textContent).toContain("dependency cycle");
    expect(called).toBe(0);
  });

  it("removes an edge via the × affordance", () => {
    let latest: WorkflowTaskDraft[] | null = null;
    render(
      <SecurityWorkflowBuilder
        tasks={[draft({ name: "a" }), draft({ name: "b", dependsOn: ["a"], forEach: "a" })]}
        onChange={(next) => (latest = next)}
      />,
    );
    fireEvent.click(screen.getByRole("button", { name: "Remove dependency a → b" }));
    expect(latest![1].dependsOn).toEqual([]);
    // A fan-out over the removed dependency is cleared too.
    expect(latest![1].forEach).toBe("");
  });

  it("deletes a node from the inspector and detaches dependents", () => {
    let latest: WorkflowTaskDraft[] | null = null;
    render(
      <SecurityWorkflowBuilder
        tasks={[draft({ name: "a" }), draft({ name: "b", dependsOn: ["a"] })]}
        onChange={(next) => (latest = next)}
      />,
    );
    fireEvent.click(screen.getByTestId("dag-node-a"));
    expect(screen.getByTestId("dag-inspector")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Delete task a" }));
    expect(latest).toHaveLength(1);
    expect(latest![0].name).toBe("b");
    expect(latest![0].dependsOn).toEqual([]);
  });

  it("deletes a node with the Delete key", () => {
    let latest: WorkflowTaskDraft[] | null = null;
    render(
      <SecurityWorkflowBuilder
        tasks={[draft({ name: "a" }), draft({ name: "b", dependsOn: ["a"] })]}
        onChange={(next) => (latest = next)}
      />,
    );
    fireEvent.keyDown(screen.getByTestId("dag-node-a"), { key: "Delete" });
    expect(latest).toHaveLength(1);
    expect(latest![0].dependsOn).toEqual([]);
  });

  it("renames a task via the inspector and rewrites dependents", () => {
    let latest: WorkflowTaskDraft[] | null = null;
    render(
      <SecurityWorkflowBuilder
        tasks={[draft({ name: "a" }), draft({ name: "b", dependsOn: ["a"], forEach: "a" })]}
        onChange={(next) => (latest = next)}
      />,
    );
    fireEvent.click(screen.getByTestId("dag-node-a"));
    fireEvent.change(document.getElementById("wf-inspector-name")!, {
      target: { value: "alpha" },
    });
    expect(latest![0].name).toBe("alpha");
    expect(latest![1].dependsOn).toEqual(["alpha"]);
    expect(latest![1].forEach).toBe("alpha");
  });

  it("adds an auto-named task from the canvas button", () => {
    let latest: WorkflowTaskDraft[] | null = null;
    render(
      <SecurityWorkflowBuilder
        tasks={[draft({ name: "task-1" })]}
        onChange={(next) => (latest = next)}
      />,
    );
    fireEvent.click(screen.getByRole("button", { name: /New task/ }));
    expect(latest).toHaveLength(2);
    expect(latest![1].name).toBe("task-2");
  });
});
