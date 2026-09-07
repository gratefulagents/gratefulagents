import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { create } from "@bufbuild/protobuf";

import { AgentRunSchema, SubagentGraphSchema, SubagentGraphNodeSchema, type SubagentGraph } from "@/rpc/platform/service_pb";
import { RunSessionView } from "./RunSessionView";

const run = create(AgentRunSchema, {
  namespace: "demo",
  name: "run-1",
  phase: "Running",
  myPermission: "owner",
  sandboxRef: "sandbox-1",
  sendReady: true,
});

const activity = vi.hoisted(() => ({ graph: undefined as SubagentGraph | undefined }));

const runErrors = vi.hoisted(() => ({
  errors: [] as unknown[],
  loading: false,
  error: null as string | null,
  truncated: false,
}));

vi.mock("@/hooks/useAgentRun", () => ({
  useAgentRun: () => ({ run, loading: false, error: null, starting: false }),
}));
vi.mock("@/hooks/useRunActivityLog", () => ({
  useRunActivityLog: () => ({
    entries: [],
    subagentGraph: activity.graph,
    isComplete: true,
    hasMoreBefore: false,
    loadOlder: vi.fn(),
  }),
}));
vi.mock("@/hooks/useActivityEntryDetail", () => ({ useActivityEntryDetail: () => vi.fn() }));
vi.mock("@/hooks/useAgentRunErrors", () => ({ useAgentRunErrors: () => runErrors }));
vi.mock("@/hooks/useAgentRunLogs", () => ({
  useAgentRunLogs: () => ({
    content: "",
    podName: "",
    available: false,
    truncated: false,
    loading: false,
    error: null,
    lastUpdated: null,
    refresh: vi.fn(),
  }),
}));
vi.mock("@/hooks/useAgentTrace", () => ({ useAgentTrace: () => ({ trace: null, loading: false, error: null }) }));
vi.mock("@/hooks/useDiff", () => ({
  useDiff: () => ({
    diff: "",
    isComplete: true,
    truncated: false,
    source: "",
    newFiles: [],
    newFilesTruncated: false,
    loading: false,
    error: null,
  }),
}));
vi.mock("@/hooks/useRepositories", () => ({
  useRepositories: () => ({ repositories: [], loading: false, error: null, refresh: vi.fn() }),
}));
vi.mock("@/hooks/usePresence", () => ({ usePresence: () => ({ viewers: [] }) }));
vi.mock("@/hooks/useAgentRunUsage", () => ({
  useAgentRunUsage: () => ({ usage: undefined, loading: false, error: null }),
}));
vi.mock("@/hooks/useAvailableModes", () => ({ useAvailableModes: () => ({ modes: [], loading: false }) }));
vi.mock("@/lib/client", () => ({ client: {} }));
vi.mock("@/components/ui/toaster", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));

function renderView() {
  return render(
    <MemoryRouter>
      <RunSessionView namespace="demo" name="run-1" />
    </MemoryRouter>,
  );
}

beforeEach(() => {
  localStorage.clear();
  runErrors.errors = [];
  activity.graph = undefined;
  // Wide viewport so the inspector docks instead of opening as a sheet.
  window.matchMedia = vi.fn((query: string) => ({
    matches: query.includes("min-width"),
    addEventListener: vi.fn(),
    removeEventListener: vi.fn(),
    addListener: vi.fn(),
    removeListener: vi.fn(),
  })) as unknown as typeof window.matchMedia;
});

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

describe("RunSessionView inspector", () => {
  it("opens a dock task in the list and reselects it after switching to graph mode", () => {
    activity.graph = create(SubagentGraphSchema, {
      hasSubagents: true,
      nodes: ["a", "b"].map((taskId) => create(SubagentGraphNodeSchema, {
        id: `task:${taskId}`, taskId, kind: "subagent", label: `Task ${taskId}`,
        status: "running", timestampUnix: 100n,
      })),
    });
    renderView();
    const dockToggle = screen.getByRole("button", { name: /2 active agents/ });
    if (dockToggle.getAttribute("aria-expanded") !== "true") fireEvent.click(dockToggle);
    for (let attempt = 0; attempt < 2; attempt++) {
      fireEvent.click(screen.getByRole("button", { name: "Open task #2 in Subagents" }));
      expect(screen.getByRole("tab", { name: /Subagents/ }).getAttribute("aria-selected")).toBe("true");
      expect(within(screen.getByRole("region", { name: "Subagent detail" })).getByRole("heading", { name: "Task b" })).toBeTruthy();
      const tasks = within(screen.getByRole("region", { name: "Subagent tasks" })).getAllByRole("button");
      expect(tasks[1].getAttribute("aria-pressed")).toBe("true");
      fireEvent.click(tasks[0]);
      fireEvent.click(screen.getByRole("button", { name: "View graph" }));
      fireEvent.click(screen.getByRole("button", { name: "Hide inspector" }));
    }
  });

  it("labels the inspector Subagents and opens the task-first view", async () => {
    renderView();
    await act(async () => {
      fireEvent.keyDown(window, { key: ".", code: "Period", metaKey: true });
    });
    fireEvent.click(screen.getByRole("tab", { name: /Subagents/ }));
    expect(screen.getByText("No subagents observed")).toBeTruthy();
    expect(screen.getByText("Tasks appear when this run delegates work to subagents.")).toBeTruthy();
    expect(screen.queryByRole("tab", { name: /^Agents$/ })).toBeNull();
  });

  it("toggles the inspector with Mod+.", async () => {
    renderView();
    expect(screen.queryByRole("tablist", { name: "Inspector sections" })).toBeNull();
    await act(async () => {
      fireEvent.keyDown(window, { key: ".", code: "Period", metaKey: true });
    });
    expect(screen.getByRole("tablist", { name: "Inspector sections" })).toBeTruthy();
    await act(async () => {
      fireEvent.keyDown(window, { key: ".", code: "Period", ctrlKey: true });
    });
    expect(screen.queryByRole("tablist", { name: "Inspector sections" })).toBeNull();
  });

  it("offers a Context tab and shows the error count on the Errors tab", async () => {
    runErrors.errors = [{}, {}];
    localStorage.setItem("gratefulagents.inspectorOpen", "true");
    renderView();
    expect(screen.getByRole("tab", { name: /Context/ })).toBeTruthy();
    expect(screen.getByRole("tab", { name: /Errors/ }).textContent).toContain("2");
    expect(screen.getByRole("button", { name: "Hide inspector" })).toBeTruthy();
  });
});
