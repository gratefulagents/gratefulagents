import { create } from "@bufbuild/protobuf";
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";

import { AgentOpsConsole } from "@/components/AgentOpsConsole";
import { client } from "@/lib/client";
import { AgentRunSchema, type AgentRun } from "@/rpc/platform/service_pb";

const runs = [
  create(AgentRunSchema, {
    namespace: "demo",
    name: "waiting-plan",
    displayName: "Approve data migration",
    phase: "Running",
    modeName: "plan",
    repoUrl: "https://github.com/acme/console",
    costUsd: "1.20",
    createdAtUnix: BigInt(Math.floor(Date.now() / 1000) - 300),
    myPermission: "owner",
    userInputRequest: { type: "plan_review", message: "Review the migration plan", actions: [] },
    trigger: { kind: "GitHubRepository", name: "console", externalIdentifier: "#42", externalId: "42", externalUrl: "https://github.com/acme/console/issues/42" },
    recentActivity: [{ timestampUnix: BigInt(Math.floor(Date.now() / 1000) - 30), eventType: "status", summary: "Plan ready" }],
  }),
  create(AgentRunSchema, {
    namespace: "demo",
    name: "failed-build",
    displayName: "Fix broken build",
    phase: "Failed",
    workflowMode: "auto",
    repoUrl: "https://github.com/acme/api",
    costUsd: "2.80",
    createdAtUnix: BigInt(Math.floor(Date.now() / 1000) - 600),
    completedAtUnix: BigInt(Math.floor(Date.now() / 1000) - 60),
    myPermission: "owner",
    lastError: "Typecheck failed",
  }),
  create(AgentRunSchema, {
    namespace: "demo",
    name: "active-run",
    displayName: "Investigate API latency",
    phase: "Running",
    workflowMode: "chat",
    costUsd: "72.17",
    createdAtUnix: BigInt(Math.floor(Date.now() / 1000) - 180),
    myPermission: "owner",
    currentStep: "Inspecting traces",
  }),
  create(AgentRunSchema, {
    namespace: "demo",
    name: "successful-idle",
    displayName: "Ship dashboard polish",
    phase: "Succeeded",
    workflowMode: "chat",
    createdAtUnix: BigInt(Math.floor(Date.now() / 1000) - 900),
    completedAtUnix: BigInt(Math.floor(Date.now() / 1000) - 120),
    myPermission: "owner",
    userInputRequest: { type: "idle", message: "The agent is waiting for a response.", actions: [] },
  }),
];

let mockRuns: AgentRun[] = runs;

vi.mock("@/hooks/useAgentRuns", () => ({
  useAgentRuns: () => ({ runs: mockRuns, loading: false, error: null, refetch: vi.fn() }),
}));

vi.mock("@/components/OwnerAvatar", () => ({ OwnerAvatar: () => null }));
vi.mock("@/components/ShareDialog", () => ({ ShareDialog: () => <div>share dialog</div> }));

vi.mock("@/lib/client", () => ({
  client: {
    cancelAgentRun: vi.fn().mockResolvedValue({}),
    retryAgentRun: vi.fn().mockResolvedValue({}),
    promoteAgentRun: vi.fn().mockResolvedValue({}),
    extendAgentRunRuntime: vi.fn().mockResolvedValue({}),
  },
}));

const mocked = vi.mocked(client);

afterEach(() => {
  cleanup();
  localStorage.clear();
  vi.clearAllMocks();
  mockRuns = runs;
});

describe("AgentOpsConsole", () => {
  it("defaults to active runs and resets back to that view", () => {
    render(<MemoryRouter><AgentOpsConsole /></MemoryRouter>);

    expect(screen.getByRole("button", { name: /^Active/, pressed: true })).toBeTruthy();
    expect(screen.getByText("Investigate API latency")).toBeTruthy();
    expect(screen.queryByText("High cost")).toBeNull();
    expect(screen.queryByText("Approve data migration")).toBeNull();
    expect(screen.queryByText("Fix broken build")).toBeNull();
    expect(screen.queryByText("Ship dashboard polish")).toBeNull();

    fireEvent.click(screen.getByRole("button", { name: /All runs/ }));
    fireEvent.click(screen.getByRole("button", { name: "Clear filters" }));
    expect(screen.getByRole("button", { name: /^Active/, pressed: true })).toBeTruthy();
  });

  it("shows attention reasons, live summaries, and deep links", () => {
    render(<MemoryRouter><AgentOpsConsole /></MemoryRouter>);
    fireEvent.click(screen.getByRole("button", { name: /All runs/ }));

    expect(screen.getByRole("heading", { name: "Agent Ops" })).toBeTruthy();
    expect(screen.queryByRole("heading", { name: "Observability" })).toBeNull();
    expect(screen.getAllByText("Needs attention").length).toBeGreaterThan(0);
    expect(screen.getAllByText("Approval").length).toBeGreaterThan(0);
    expect(screen.getAllByText("Failed").length).toBeGreaterThan(0);
    expect(screen.getByText("Review the migration plan")).toBeTruthy();
    expect(screen.getByRole("link", { name: "Approve data migration" }).getAttribute("href")).toBe("/runs/demo/waiting-plan");
    expect(screen.getByRole("link", { name: "GitHub · console" }).getAttribute("href")).toBe("/github/demo/console");
  });

  it("filters the fleet by attention rail and excludes successful runs with stale input", async () => {
    render(<MemoryRouter><AgentOpsConsole /></MemoryRouter>);

    fireEvent.click(screen.getByRole("button", { name: /Needs attention/ }));
    expect(screen.getByText("Approve data migration")).toBeTruthy();
    expect(screen.getByText("Fix broken build")).toBeTruthy();
    expect(screen.queryByText("Investigate API latency")).toBeNull();
    expect(screen.queryByText("Ship dashboard polish")).toBeNull();

    fireEvent.click(screen.getByRole("button", { name: /All runs/ }));
    expect(screen.getByText("Completed successfully")).toBeTruthy();
    fireEvent.change(screen.getByPlaceholderText("Search runs…"), { target: { value: "migration" } });
    await waitFor(() => expect(screen.queryByText("Fix broken build")).toBeNull());
    expect(screen.getByText("Approve data migration")).toBeTruthy();
  });

  it("confirms and retries only an eligible failed selection", async () => {
    render(<MemoryRouter><AgentOpsConsole /></MemoryRouter>);
    fireEvent.click(screen.getByRole("button", { name: /Needs attention/ }));

    fireEvent.click(screen.getByLabelText("Select Fix broken build"));
    fireEvent.click(screen.getByRole("button", { name: /Retry \(1\)/ }));
    expect(screen.getByText("Retry 1 run?")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Retry 1 run" }));

    await waitFor(() => expect(mocked.retryAgentRun).toHaveBeenCalledWith(expect.objectContaining({ namespace: "demo", name: "failed-build" })));
  });

  it("prevents duplicate non-idempotent runtime extensions", async () => {
    let resolveExtension: (value: unknown) => void = () => {};
    mocked.extendAgentRunRuntime.mockImplementationOnce(
      () => new Promise((resolve) => { resolveExtension = resolve; }) as never,
    );
    render(<MemoryRouter><AgentOpsConsole /></MemoryRouter>);
    fireEvent.click(screen.getByRole("button", { name: /Needs attention/ }));

    fireEvent.click(screen.getByLabelText("Select Approve data migration"));
    fireEvent.click(screen.getByRole("button", { name: /Extend \(1\)/ }));
    const extendButton = screen.getByRole("button", { name: "Extend" });
    fireEvent.click(extendButton);
    fireEvent.click(extendButton);

    expect(mocked.extendAgentRunRuntime).toHaveBeenCalledTimes(1);
    expect(screen.getByRole("button", { name: "Extending…" }).hasAttribute("disabled")).toBe(true);
    resolveExtension({});
    await waitFor(() => expect(screen.queryByRole("button", { name: "Extending…" })).toBeNull());
  });
});

describe("AgentOpsConsole security scan grouping", () => {
  const nowUnix = Math.floor(Date.now() / 1000);

  function scanTaskRun(task: string, phase: string): AgentRun {
    return create(AgentRunSchema, {
      namespace: "demo",
      name: `secscan-nightly-${task}`,
      displayName: `Scan task ${task}`,
      phase,
      modeName: "security-task",
      costUsd: "1.00",
      createdAtUnix: BigInt(nowUnix - 120),
      completedAtUnix: phase === "Succeeded" ? BigInt(nowUnix - 30) : 0n,
      myPermission: "owner",
      trigger: {
        kind: "SecurityScan",
        name: "nightly",
        externalId: "exec-1",
        externalIdentifier: `exec-1/${task}[0]`,
      },
    });
  }

  const scanTaskRuns = [
    scanTaskRun("semgrep", "Running"),
    scanTaskRun("codeql", "Running"),
    scanTaskRun("triage", "Succeeded"),
  ];

  it("collapses one execution's task runs into a single row without touching other runs", () => {
    mockRuns = [...runs, ...scanTaskRuns];
    render(<MemoryRouter><AgentOpsConsole /></MemoryRouter>);

    const groupLink = screen.getByRole("link", { name: "Security scan nightly" });
    expect(groupLink.getAttribute("href")).toBe("/security/runs");
    expect(screen.getByText("1 of 3 tasks done")).toBeTruthy();
    expect(screen.getByText("$3.00")).toBeTruthy();
    expect(screen.queryByText("Scan task semgrep")).toBeNull();
    expect(screen.queryByText("Scan task codeql")).toBeNull();
    expect(screen.getByText("Investigate API latency")).toBeTruthy();
  });

  it("expands the group to reveal individual task runs with run links", () => {
    mockRuns = [...runs, ...scanTaskRuns];
    render(<MemoryRouter><AgentOpsConsole /></MemoryRouter>);

    fireEvent.click(screen.getByRole("button", { name: "Expand Security scan nightly task runs" }));
    const child = screen.getByRole("link", { name: "Scan task semgrep" });
    expect(child.getAttribute("href")).toBe("/runs/demo/secscan-nightly-semgrep");
    expect(screen.getByRole("link", { name: "Scan task codeql" })).toBeTruthy();

    fireEvent.click(screen.getByRole("button", { name: "Collapse Security scan nightly task runs" }));
    expect(screen.queryByText("Scan task semgrep")).toBeNull();
  });

  it("selects every visible member run through the group checkbox", () => {
    mockRuns = [...runs, ...scanTaskRuns];
    render(<MemoryRouter><AgentOpsConsole /></MemoryRouter>);

    fireEvent.click(screen.getByLabelText("Select Security scan nightly task runs"));
    expect(screen.getByText("2 selected")).toBeTruthy();
  });

  it("renders coordinator-mode scans without an execution id as plain rows", () => {
    mockRuns = [
      ...runs,
      create(AgentRunSchema, {
        namespace: "demo",
        name: "secscan-coordinator",
        displayName: "Coordinator scan",
        phase: "Running",
        createdAtUnix: BigInt(nowUnix - 60),
        myPermission: "owner",
        trigger: { kind: "SecurityScan", name: "adhoc", externalId: "" },
      }),
    ];
    render(<MemoryRouter><AgentOpsConsole /></MemoryRouter>);

    const row = screen.getByRole("link", { name: "Coordinator scan" });
    expect(row.getAttribute("href")).toBe("/runs/demo/secscan-coordinator");
    expect(screen.queryByRole("link", { name: "Security scan adhoc" })).toBeNull();
  });
});
