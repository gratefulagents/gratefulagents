import { create } from "@bufbuild/protobuf";
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";

import { SecurityScanRunPanel } from "@/components/SecurityScanRunPanel";
import {
  AgentRunSchema,
  SubagentGraphNodeSchema,
  SubagentGraphSchema,
  type AgentRun,
  type SubagentGraph,
} from "@/rpc/platform/service_pb";

const { cancelAgentRun, retryAgentRun, useAgentRunMock, useActivityLogMock, useAgentRunUsageMock } =
  vi.hoisted(() => ({
    cancelAgentRun: vi.fn(),
    retryAgentRun: vi.fn(),
    useAgentRunMock: vi.fn(),
    useActivityLogMock: vi.fn(),
    useAgentRunUsageMock: vi.fn(),
  }));

vi.mock("@/lib/client", () => ({
  client: { cancelAgentRun, retryAgentRun },
}));
vi.mock("@/hooks/useAgentRun", () => ({
  useAgentRun: useAgentRunMock,
}));
vi.mock("@/hooks/useActivityLog", () => ({
  useActivityLog: useActivityLogMock,
}));
vi.mock("@/hooks/useAgentRunUsage", () => ({
  useAgentRunUsage: useAgentRunUsageMock,
}));

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

function runFixture(overrides: Record<string, unknown> = {}): AgentRun {
  return create(AgentRunSchema, {
    namespace: "user-alice",
    name: "secscan-nightly-1",
    phase: "Running",
    myPermission: "owner",
    model: "claude-sonnet-4-6",
    resolvedModel: "claude-sonnet-4-6",
    retryCount: 2,
    startedAtUnix: 1_700_000_000n,
    inputTokens: 1200n,
    outputTokens: 340n,
    costUsd: "1.25",
    ...overrides,
  });
}

function graphFixture(): SubagentGraph {
  return create(SubagentGraphSchema, {
    rootId: "root",
    hasSubagents: true,
    nodes: [
      create(SubagentGraphNodeSchema, { id: "root", kind: "root", label: "Main agent", status: "running" }),
      create(SubagentGraphNodeSchema, {
        id: "task:injection",
        kind: "subagent",
        parentId: "root",
        label: "injection-and-input-handling",
        status: "running",
      }),
    ],
  });
}

function arrange({
  run = runFixture(),
  graph,
}: { run?: AgentRun | null; graph?: SubagentGraph } = {}) {
  useAgentRunMock.mockReturnValue({ run, loading: false, error: null, starting: false });
  useActivityLogMock.mockReturnValue({
    entries: [],
    subagentGraph: graph,
    loading: false,
    error: null,
    isComplete: false,
    hasMoreBefore: false,
    loadOlder: vi.fn(),
  });
  useAgentRunUsageMock.mockReturnValue({ usage: null, loading: false, error: null });
}

function renderPanel(onRunSettled?: (phase: string) => void) {
  return render(
    <MemoryRouter>
      <SecurityScanRunPanel namespace="user-alice" runName="secscan-nightly-1" onRunSettled={onRunSettled} />
    </MemoryRouter>,
  );
}

describe("SecurityScanRunPanel", () => {
  it("shows diagnostics and the workflow graph for a running scan", () => {
    arrange({ graph: graphFixture() });
    renderPanel();

    expect(screen.getByText("Running")).toBeTruthy();
    expect(screen.getAllByText("claude-sonnet-4-6").length).toBeGreaterThan(0);
    expect(screen.getByText("Retries").nextElementSibling?.textContent).toContain("2");
    expect(screen.getByText("$1.25")).toBeTruthy();
    expect(screen.getAllByText(/injection-and-input-handling/).length).toBeGreaterThan(0);
  });

  it("offers Stop (not Retry) to the owner while the run is active", () => {
    arrange();
    renderPanel();

    expect(screen.getByRole("button", { name: /Stop scan/ })).toBeTruthy();
    expect(screen.queryByRole("button", { name: /Retry scan/ })).toBeNull();
  });

  it("hides all controls for viewers", () => {
    arrange({ run: runFixture({ myPermission: "viewer" }) });
    renderPanel();

    expect(screen.queryByRole("button", { name: /Stop scan/ })).toBeNull();
    expect(screen.queryByRole("button", { name: /Retry scan/ })).toBeNull();
  });

  it("hides Stop for finished runs and offers Retry only for failed runs", () => {
    arrange({ run: runFixture({ phase: "Succeeded" }) });
    renderPanel();
    expect(screen.queryByRole("button", { name: /Stop scan/ })).toBeNull();
    expect(screen.queryByRole("button", { name: /Retry scan/ })).toBeNull();
    cleanup();

    arrange({ run: runFixture({ phase: "Failed", lastError: "model quota exhausted" }) });
    renderPanel();
    expect(screen.queryByRole("button", { name: /Stop scan/ })).toBeNull();
    expect(screen.getByRole("button", { name: /Retry scan/ })).toBeTruthy();
    // Preserved-findings copy is explicit.
    expect(screen.getByText(/Findings already recorded by this scan are preserved/)).toBeTruthy();
    // The error is actionable and links to the run page.
    expect(screen.getByText("model quota exhausted")).toBeTruthy();
    expect(screen.getAllByRole("link", { name: /agent run/i }).length).toBeGreaterThan(0);
  });

  it("labels retry as Resume for cancelled runs", () => {
    arrange({ run: runFixture({ phase: "Cancelled" }) });
    renderPanel();
    expect(screen.getByRole("button", { name: /Resume scan/ })).toBeTruthy();
  });

  it("disables Stop while the cancel mutation is in flight", async () => {
    arrange();
    let resolveCancel: (value: unknown) => void = () => {};
    cancelAgentRun.mockReturnValue(new Promise((resolve) => { resolveCancel = resolve; }));
    renderPanel();

    fireEvent.click(screen.getByRole("button", { name: /Stop scan/ }));
    const pending = screen.getByRole("button", { name: /Stopping…/ }) as HTMLButtonElement;
    expect(pending.disabled).toBe(true);
    expect(cancelAgentRun).toHaveBeenCalledWith({ namespace: "user-alice", name: "secscan-nightly-1" });

    resolveCancel({});
    await waitFor(() => {
      expect((screen.getByRole("button", { name: /Stop scan/ }) as HTMLButtonElement).disabled).toBe(false);
    });
  });

  it("re-enables the control and surfaces the error when cancelling fails", async () => {
    arrange();
    cancelAgentRun.mockRejectedValue(new Error("cannot stop terminal run in phase Succeeded"));
    renderPanel();

    fireEvent.click(screen.getByRole("button", { name: /Stop scan/ }));

    await waitFor(() => {
      expect(screen.getByText(/cannot stop terminal run/)).toBeTruthy();
    });
    expect((screen.getByRole("button", { name: /Stop scan/ }) as HTMLButtonElement).disabled).toBe(false);
  });

  it("retries a failed run with an idempotency key and rolls back on error", async () => {
    arrange({ run: runFixture({ phase: "Failed" }) });
    retryAgentRun.mockRejectedValue(new Error("can only retry failed or stopped runs"));
    renderPanel();

    fireEvent.click(screen.getByRole("button", { name: /Retry scan/ }));

    await waitFor(() => {
      expect(screen.getByText(/can only retry failed or stopped runs/)).toBeTruthy();
    });
    expect(retryAgentRun).toHaveBeenCalledTimes(1);
    const request = vi.mocked(retryAgentRun).mock.calls[0][0] as {
      namespace: string;
      name: string;
      idempotencyKey: string;
    };
    expect(request.namespace).toBe("user-alice");
    expect(request.name).toBe("secscan-nightly-1");
    expect(request.idempotencyKey.length).toBeGreaterThan(0);
    expect((screen.getByRole("button", { name: /Retry scan/ }) as HTMLButtonElement).disabled).toBe(false);
  });

  it("notifies the parent exactly once when it observes the run settling", () => {
    const onRunSettled = vi.fn();
    arrange();
    const view = renderPanel(onRunSettled);
    expect(onRunSettled).not.toHaveBeenCalled();

    arrange({ run: runFixture({ phase: "Succeeded" }) });
    view.rerender(
      <MemoryRouter>
        <SecurityScanRunPanel namespace="user-alice" runName="secscan-nightly-1" onRunSettled={onRunSettled} />
      </MemoryRouter>,
    );
    expect(onRunSettled).toHaveBeenCalledTimes(1);
    expect(onRunSettled).toHaveBeenCalledWith("Succeeded");

    view.rerender(
      <MemoryRouter>
        <SecurityScanRunPanel namespace="user-alice" runName="secscan-nightly-1" onRunSettled={onRunSettled} />
      </MemoryRouter>,
    );
    expect(onRunSettled).toHaveBeenCalledTimes(1);
  });

  it("never notifies for a run that is already terminal on mount (no remount refresh loop)", () => {
    const onRunSettled = vi.fn();
    arrange({ run: runFixture({ phase: "Succeeded" }) });
    const view = renderPanel(onRunSettled);
    view.rerender(
      <MemoryRouter>
        <SecurityScanRunPanel namespace="user-alice" runName="secscan-nightly-1" onRunSettled={onRunSettled} />
      </MemoryRouter>,
    );
    expect(onRunSettled).not.toHaveBeenCalled();
  });

  it("renders nothing for a missing run when hideWhenMissing is set (execution-level scan records)", () => {
    useAgentRunMock.mockReturnValue({ run: null, loading: false, error: "run not found", starting: false });
    useActivityLogMock.mockReturnValue({
      entries: [],
      subagentGraph: undefined,
      loading: false,
      error: null,
      isComplete: false,
      hasMoreBefore: false,
      loadOlder: vi.fn(),
    });
    useAgentRunUsageMock.mockReturnValue({ usage: null, loading: false, error: null });
    const { container } = render(
      <MemoryRouter>
        <SecurityScanRunPanel namespace="user-alice" runName="secscan-nightly-generation-1" hideWhenMissing />
      </MemoryRouter>,
    );
    expect(container.textContent).toBe("");
    cleanup();

    // Without the flag the error panel still surfaces broken runs.
    render(
      <MemoryRouter>
        <SecurityScanRunPanel namespace="user-alice" runName="secscan-nightly-generation-1" />
      </MemoryRouter>,
    );
    expect(screen.getByText(/Failed to load the scan/)).toBeTruthy();
  });
});
