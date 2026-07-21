import { create } from "@bufbuild/protobuf";
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";

import { AgentOpsConsole } from "@/components/AgentOpsConsole";
import { client } from "@/lib/client";
import { AgentRunSchema } from "@/rpc/platform/service_pb";

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

vi.mock("@/hooks/useAgentRuns", () => ({
  useAgentRuns: () => ({ runs, loading: false, error: null, refetch: vi.fn() }),
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
