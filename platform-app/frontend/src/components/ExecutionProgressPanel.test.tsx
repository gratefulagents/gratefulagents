import { afterEach, describe, expect, it } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { create } from "@bufbuild/protobuf";

import { ExecutionProgressPanel, aggregateState } from "@/components/ExecutionProgressPanel";
import {
  SecurityScanExecutionPlanNodeSchema,
  SecurityScanExecutionStateSchema,
  SecurityScanFanOutStateSchema,
  SecurityScanPostScriptJobStateSchema,
  SecurityScanTaskConfigSchema,
  type SecurityScanExecutionState,
  type SecurityScanTaskConfig,
} from "@/rpc/platform/service_pb";

afterEach(() => cleanup());

function execution(): SecurityScanExecutionState {
  return create(SecurityScanExecutionStateSchema, {
    id: "20260101-abc",
    mode: "deterministic",
    phase: "Running",
    evidenceOutcome: "partial",
    effectiveParallelism: 3,
    effectiveParallelismNote: "capped by policy pack",
    startedAtUnix: 1767225600n,
    tasks: [
      {
        name: "recon",
        instance: 0,
        state: "Succeeded",
        runName: "scan-recon-1",
        attempts: 1,
        startedAtUnix: 1767225600n,
        finishedAtUnix: 1767226600n,
      },
      {
        name: "injection-hunt",
        instance: 1,
        state: "Failed",
        runName: "scan-injection-3",
        attempts: 2,
        nextRetryTimeUnix: 1767230000n,
        lastError: "pod evicted while cloning the repository",
        retries: [
          {
            runName: "scan-injection-2",
            startedAtUnix: 1767226700n,
            finishedAtUnix: 1767227000n,
            reason: "run failed: OOMKilled",
            class: "retryable",
          },
        ],
      },
      { name: "triage", instance: 0, state: "Blocked", attempts: 0 },
    ],
  });
}

function renderPanel(
  state: SecurityScanExecutionState = execution(),
  workflowTasks?: SecurityScanTaskConfig[],
) {
  render(
    <MemoryRouter>
      <ExecutionProgressPanel
        namespace="user-alice"
        execution={state}
        workflowTasks={workflowTasks}
      />
    </MemoryRouter>,
  );
}

/** The planned workflow matching the execution fixture, plus fan-out. */
function workflowFixture(): SecurityScanTaskConfig[] {
  return [
    create(SecurityScanTaskConfigSchema, { name: "recon", objective: "Map." }),
    create(SecurityScanTaskConfigSchema, {
      name: "injection-hunt",
      objective: "Hunt.",
      dependsOn: ["recon"],
      forEach: "recon",
    }),
    create(SecurityScanTaskConfigSchema, {
      name: "triage",
      objective: "Triage.",
      dependsOn: ["injection-hunt"],
    }),
  ];
}

describe("ExecutionProgressPanel", () => {
  it("does not count vacuous or skipped chunk ranges as reviewed", () => {
    const state = execution();
    state.tasks = [
      { ...state.tasks[0], name: "chunk-hunt", state: "Succeeded", recordStart: 0, recordEnd: 0 },
      { ...state.tasks[0], name: "skipped-hunt", state: "Skipped", recordStart: 0, recordEnd: 12 },
    ];
    state.fanOuts = [
      create(SecurityScanFanOutStateSchema, {
        name: "chunk-hunt",
        strategy: "chunk-v1",
        recordCount: 0,
        chunkCount: 0,
      }),
      create(SecurityScanFanOutStateSchema, {
        name: "skipped-hunt",
        strategy: "chunk-v1",
        recordCount: 12,
        chunkCount: 1,
      }),
    ];
    renderPanel(state);
    expect(screen.getByTestId("execution-chunk-progress-chunk-hunt").textContent).toContain(
      "0/0 records · 0/0 chunks",
    );
    expect(screen.getByTestId("execution-chunk-progress-skipped-hunt").textContent).toContain(
      "0/12 records · 0/1 chunks",
    );
  });

  it("shows phase, effective parallelism, and timing chips", () => {
    renderPanel();
    const panel = screen.getByTestId("execution-progress");
    expect(panel.textContent).toContain("Running");
    expect(screen.getByTestId("execution-evidence-outcome").textContent).toBe("evidence partial");
    const parallelism = screen.getByTestId("execution-parallelism");
    expect(parallelism.textContent).toContain("parallelism 3");
    expect(parallelism.textContent).toContain("capped by policy pack");
    expect(panel.textContent).toContain("started ");
    expect(panel.textContent).toContain("completed —");
  });

  it("renders one row per task instance with state, attempts, and run links", () => {
    renderPanel();
    const succeeded = screen.getByTestId("execution-task-recon#0");
    expect(succeeded.textContent).toContain("Succeeded");
    const link = screen.getByRole("link", { name: "scan-recon-1" });
    expect(link.getAttribute("href")).toBe("/runs/user-alice/scan-recon-1");

    const failed = screen.getByTestId("execution-task-injection-hunt#1");
    expect(failed.textContent).toContain("injection-hunt #1");
    expect(failed.textContent).toContain("Failed");
    expect(failed.textContent).toContain("2");

    const blocked = screen.getByTestId("execution-task-triage#0");
    expect(blocked.textContent).toContain("Blocked");
  });

  it("shows complete chunk progress and half-open record ranges", () => {
    const state = execution();
    state.tasks = [
      { ...state.tasks[0], name: "chunk-hunt", instance: 0, state: "Succeeded" },
      { ...state.tasks[0], name: "chunk-hunt", instance: 1, state: "Running" },
    ];
    const chunkedTasks = state.tasks as Array<
      (typeof state.tasks)[number] & {
        recordStart: number;
        recordEnd: number;
        inputSha256: string;
      }
    >;
    Object.assign(chunkedTasks[0], { recordStart: 0, recordEnd: 30, inputSha256: "chunk-1" });
    Object.assign(chunkedTasks[1], { recordStart: 30, recordEnd: 60, inputSha256: "chunk-2" });
    state.fanOuts = [
      create(SecurityScanFanOutStateSchema, {
        name: "chunk-hunt",
        sourceTask: "recon",
        sourceRunName: "scan-recon-1",
        strategy: "chunk-v1",
        sourceOutputSha256: "source-sha",
        recordCount: 60,
        chunkCount: 2,
      }),
    ];
    renderPanel(state, [
      create(SecurityScanTaskConfigSchema, {
        name: "chunk-hunt",
        objective: "Hunt every record.",
        forEach: "recon",
      }),
    ]);

    expect(screen.getByTestId("execution-chunk-progress-chunk-hunt").textContent).toContain(
      "30/60 records · 1/2 chunks",
    );
    expect(screen.getByTestId("execution-node-chunk-hunt").textContent).toContain(
      "30/60 records · 1/2 chunks",
    );
    expect(screen.getByTestId("execution-task-chunk-hunt#0").textContent).toContain(
      "records [0, 30)",
    );
  });

  it("truncates the last error but exposes the full text as a title", () => {
    renderPanel();
    const error = screen.getByTitle("pod evicted while cloning the repository");
    expect(error.textContent).toContain("pod evicted");
  });

  it("expands retry history with reason, class, and timestamps", () => {
    renderPanel();
    expect(screen.queryByTestId("execution-retries-injection-hunt#1")).toBeNull();
    fireEvent.click(
      screen.getByRole("button", { name: "Show details for injection-hunt #1" }),
    );
    const retries = screen.getByTestId("execution-retries-injection-hunt#1");
    expect(retries.textContent).toContain("scan-injection-2");
    expect(retries.textContent).toContain("run failed: OOMKilled");
    expect(retries.textContent).toContain("retryable");
    fireEvent.click(
      screen.getByRole("button", { name: "Hide details for injection-hunt #1" }),
    );
    expect(screen.queryByTestId("execution-retries-injection-hunt#1")).toBeNull();
  });

  it("offers no retry toggle for tasks without retries", () => {
    renderPanel();
    expect(screen.queryByRole("button", { name: /details for recon/ })).toBeNull();
  });

  it("expands a succeeded task's structured output", () => {
    const state = execution();
    state.tasks[0].outputJson = '{"targets":["/api","/admin"]}';
    renderPanel(state);
    fireEvent.click(screen.getByRole("button", { name: "Show details for recon" }));
    const output = screen.getByTestId("execution-output-recon#0");
    expect(output.textContent).toContain("Structured output");
    // Pretty-printed for readability.
    expect(output.textContent).toContain('"/admin"');
    expect(output.querySelector("pre")?.textContent).toContain("\n");
  });

  it("offers Resume only for a failed execution and reports the click", () => {
    let resumed = 0;
    const running = execution();
    render(
      <MemoryRouter>
        <ExecutionProgressPanel
          namespace="user-alice"
          execution={running}
          onResume={() => {
            resumed += 1;
          }}
        />
      </MemoryRouter>,
    );
    expect(screen.queryByTestId("execution-resume")).toBeNull();
    cleanup();

    const failed = execution();
    failed.phase = "Failed";
    render(
      <MemoryRouter>
        <ExecutionProgressPanel
          namespace="user-alice"
          execution={failed}
          onResume={() => {
            resumed += 1;
          }}
        />
      </MemoryRouter>,
    );
    fireEvent.click(screen.getByTestId("execution-resume"));
    expect(resumed).toBe(1);
  });

  it("shows overall instance progress and the execution id", () => {
    renderPanel();
    expect(screen.getByTestId("execution-instance-progress").textContent).toBe(
      "1/3 tasks done",
    );
    expect(screen.getByTitle("Execution ID").textContent).toBe("20260101-abc");
  });

  it("renders no DAG when the planned workflow is unknown", () => {
    renderPanel();
    expect(screen.queryByTestId("execution-dag")).toBeNull();
  });

  it("renders the live DAG with per-task state when workflow tasks are provided", () => {
    renderPanel(execution(), workflowFixture());
    const dag = screen.getByTestId("execution-dag");
    expect(dag).toBeTruthy();
    expect(screen.getByTestId("execution-node-recon").textContent).toContain("Succeeded");
    // injection-hunt fans out over recon's output: instance progress shows.
    const hunt = screen.getByTestId("execution-node-injection-hunt");
    expect(hunt.textContent).toContain("Failed");
    expect(hunt.textContent).toContain("0/1 instances");
    expect(hunt.textContent).toContain("retried");
    expect(screen.getByTestId("execution-node-triage").textContent).toContain("Blocked");
  });

  it("prefers the execution's plan snapshot over the live workflow", () => {
    const state = execution();
    // The plan snapshot (built-in default workflow, or the workflow as it
    // was when planned) differs from the live workflow fixture: no
    // injection-hunt fan-out, plus a plan-only task.
    state.plan = [
      create(SecurityScanExecutionPlanNodeSchema, { name: "recon" }),
      create(SecurityScanExecutionPlanNodeSchema, { name: "injection-hunt", dependsOn: ["recon"] }),
      create(SecurityScanExecutionPlanNodeSchema, { name: "triage", dependsOn: ["injection-hunt"] }),
      create(SecurityScanExecutionPlanNodeSchema, { name: "report", dependsOn: ["triage"] }),
    ];
    renderPanel(state, workflowFixture());
    // The plan-only node renders (as never-started), proving the plan wins.
    expect(screen.getByTestId("execution-node-report").textContent).toContain("Waiting");
  });

  it("renders the DAG from the plan alone when no workflow is passed", () => {
    const state = execution();
    state.plan = [
      create(SecurityScanExecutionPlanNodeSchema, { name: "recon" }),
      create(SecurityScanExecutionPlanNodeSchema, {
        name: "injection-hunt",
        dependsOn: ["recon"],
        forEach: "recon",
      }),
      create(SecurityScanExecutionPlanNodeSchema, { name: "triage", dependsOn: ["injection-hunt"] }),
    ];
    renderPanel(state);
    expect(screen.getByTestId("execution-dag")).toBeTruthy();
    expect(screen.getByTestId("execution-node-recon").textContent).toContain("Succeeded");
  });

  it("focuses one task's instances when its node is clicked", () => {
    renderPanel(execution(), workflowFixture());
    fireEvent.click(screen.getByTestId("execution-node-injection-hunt"));
    expect(screen.getByTestId("execution-focus").textContent).toContain("injection-hunt");
    expect(screen.getByTestId("execution-task-injection-hunt#1")).toBeTruthy();
    expect(screen.queryByTestId("execution-task-recon#0")).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: /Show all/ }));
    expect(screen.getByTestId("execution-task-recon#0")).toBeTruthy();
  });

  it("lists per-finding post-script pipelines with ordered scripts and one run", () => {
    const state = execution();
    state.postScriptJobs = [
      create(SecurityScanPostScriptJobStateSchema, {
        scripts: ["false-positive-check", "poc-builder"],
        findingId: "22222222-2222-2222-2222-222222222222",
        fingerprint: "sqli-users-list",
        state: "Succeeded",
        runName: "scan-ps-1",
        attempts: 1,
        result: "confirmed",
        startedAtUnix: 1767225600n,
        finishedAtUnix: 1767226600n,
      }),
    ];
    render(
      <MemoryRouter>
        <ExecutionProgressPanel
          namespace="user-alice"
          execution={state}
          findingLinkBase="/security/user-alice/nightly-1/findings"
        />
      </MemoryRouter>,
    );
    expect(screen.getByTestId("execution-post-scripts").textContent).toContain(
      "Post-script pipelines",
    );
    expect(screen.getByTestId("execution-post-script-progress").textContent).toBe(
      "1/1 post-script pipelines done",
    );
    const pipeline = screen.getByTestId(
      "execution-post-script-pipeline-22222222-2222-2222-2222-222222222222#0",
    );
    expect(pipeline.textContent).toContain("false-positive-check → poc-builder");
    expect(pipeline.textContent).toContain("Succeeded");
    expect(pipeline.textContent).toContain("confirmed");
    const findingLink = screen.getByRole("link", { name: "sqli-users-list" });
    expect(findingLink.getAttribute("href")).toBe(
      "/security/user-alice/nightly-1/findings/22222222-2222-2222-2222-222222222222",
    );
    const runLinks = screen.getAllByRole("link", { name: "scan-ps-1" });
    expect(runLinks).toHaveLength(1);
    expect(runLinks[0].getAttribute("href")).toBe(
      "/runs/user-alice/scan-ps-1",
    );
  });

  it("warns about coverage gaps", () => {
    const state = execution();
    state.coverageGaps = ["forEach inventory truncated to 50 instances"];
    renderPanel(state);
    const alert = screen.getByTestId("execution-coverage-gaps");
    expect(alert.textContent).toContain("Partial coverage");
    expect(alert.textContent).toContain("forEach inventory truncated to 50 instances");
  });

  it("shows neither post-script section nor coverage alert when absent", () => {
    renderPanel();
    expect(screen.queryByTestId("execution-post-scripts")).toBeNull();
    expect(screen.queryByTestId("execution-post-script-progress")).toBeNull();
    expect(screen.queryByTestId("execution-coverage-gaps")).toBeNull();
  });
});

describe("aggregateState", () => {
  const instance = (state: string) =>
    execution().tasks[0] && { ...execution().tasks[0], state };

  it("ranks failure over any other instance state", () => {
    expect(aggregateState([instance("Succeeded"), instance("Failed")])).toBe("Failed");
  });

  it("reads running while any instance is live", () => {
    expect(aggregateState([instance("Succeeded"), instance("Running")])).toBe("Running");
  });

  it("reads succeeded only when every instance finished", () => {
    expect(aggregateState([instance("Succeeded"), instance("Succeeded")])).toBe("Succeeded");
  });
});
