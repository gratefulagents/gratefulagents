import { afterEach, describe, expect, it } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { create } from "@bufbuild/protobuf";

import { ExecutionProgressPanel } from "@/components/ExecutionProgressPanel";
import {
  SecurityScanExecutionStateSchema,
  type SecurityScanExecutionState,
} from "@/rpc/platform/service_pb";

afterEach(() => cleanup());

function execution(): SecurityScanExecutionState {
  return create(SecurityScanExecutionStateSchema, {
    id: "20260101-abc",
    mode: "deterministic",
    phase: "Running",
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

function renderPanel(state: SecurityScanExecutionState = execution()) {
  render(
    <MemoryRouter>
      <ExecutionProgressPanel namespace="user-alice" execution={state} />
    </MemoryRouter>,
  );
}

describe("ExecutionProgressPanel", () => {
  it("shows phase, effective parallelism, and timing chips", () => {
    renderPanel();
    const panel = screen.getByTestId("execution-progress");
    expect(panel.textContent).toContain("Running");
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

  it("truncates the last error but exposes the full text as a title", () => {
    renderPanel();
    const error = screen.getByTitle("pod evicted while cloning the repository");
    expect(error.textContent).toContain("pod evicted");
  });

  it("expands retry history with reason, class, and timestamps", () => {
    renderPanel();
    expect(screen.queryByTestId("execution-retries-injection-hunt#1")).toBeNull();
    fireEvent.click(
      screen.getByRole("button", { name: "Show retries for injection-hunt #1" }),
    );
    const retries = screen.getByTestId("execution-retries-injection-hunt#1");
    expect(retries.textContent).toContain("scan-injection-2");
    expect(retries.textContent).toContain("run failed: OOMKilled");
    expect(retries.textContent).toContain("retryable");
    fireEvent.click(
      screen.getByRole("button", { name: "Hide retries for injection-hunt #1" }),
    );
    expect(screen.queryByTestId("execution-retries-injection-hunt#1")).toBeNull();
  });

  it("offers no retry toggle for tasks without retries", () => {
    renderPanel();
    expect(screen.queryByRole("button", { name: /retries for recon/ })).toBeNull();
  });
});
