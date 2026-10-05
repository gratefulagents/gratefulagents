import { useEffect, useState } from "react";
import { createPortal } from "react-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { create } from "@bufbuild/protobuf";

import { AgentRunSchema } from "@/rpc/platform/service_pb";
import { useAgentRunErrors } from "@/hooks/useAgentRunErrors";
import { useAgentRunLogs } from "@/hooks/useAgentRunLogs";
import { useAgentTrace } from "@/hooks/useAgentTrace";
import { RunSessionView } from "./RunSessionView";

const run = create(AgentRunSchema, {
  namespace: "demo",
  name: "run-1",
  phase: "Running",
  myPermission: "owner",
  sandboxRef: "sandbox-1",
  sendReady: true,
});

const computer = vi.hoisted(() => ({ mount: vi.fn(), unmount: vi.fn() }));
vi.mock("@/lib/platform", async (original) => ({
  ...await original<typeof import("@/lib/platform")>(), isTauri: true,
}));
vi.mock("@/components/ComputerUsePanel", () => ({
  ComputerUsePanel: ({ view }: { view: { panel: HTMLElement | null; shortcut: HTMLElement | null; open: () => void } }) => {
    const [count, setCount] = useState(0);
    useEffect(() => {
      computer.mount();
      return () => { computer.unmount(); };
    }, []);
    return <>
      {view.shortcut && createPortal(<button onClick={view.open}>Computer shortcut</button>, view.shortcut)}
      {view.panel && createPortal(<button onClick={() => setCount(count + 1)}>Desktop state {count}</button>, view.panel)}
    </>;
  },
}));

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
    subagentGraph: undefined,
    isComplete: true,
    hasMoreBefore: false,
    loadOlder: vi.fn(),
  }),
}));
vi.mock("@/hooks/useActivityEntryDetail", () => ({ useActivityEntryDetail: () => vi.fn() }));
vi.mock("@/hooks/useAgentRunErrors", () => ({ useAgentRunErrors: vi.fn(() => runErrors) }));
vi.mock("@/hooks/useAgentRunLogs", () => ({
  useAgentRunLogs: vi.fn(() => ({
    content: "",
    podName: "",
    available: false,
    truncated: false,
    loading: false,
    error: null,
    lastUpdated: null,
    refresh: vi.fn(),
  })),
}));
vi.mock("@/hooks/useAgentTrace", () => ({ useAgentTrace: vi.fn(() => ({ trace: null, loading: false, error: null })) }));
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
  run.traceId = "trace-1";
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
  it.each(["logs", "errors", "trace"])("gates %s and its stream behind Debug, including a stored selection", (tab) => {
    localStorage.setItem("gratefulagents.inspectorOpen", "true");
    localStorage.setItem("gratefulagents.inspectorTab", tab);
    renderView();
    const checkbox = screen.getByRole<HTMLInputElement>("checkbox", { name: "Debug" });
    const expectStreams = (active?: string) => {
      expect(useAgentRunLogs).toHaveBeenLastCalledWith("demo", "run-1", "Running", { enabled: active === "logs" });
      expect(useAgentRunErrors).toHaveBeenLastCalledWith("demo", "run-1", "Running", { enabled: active === "errors" });
      expect(useAgentTrace).toHaveBeenLastCalledWith("demo", "run-1", "trace-1", "Running", { enabled: active === "trace" });
    };
    expect(checkbox.checked).toBe(false);
    for (const name of ["Logs", "Errors", "Trace"]) {
      expect(screen.queryByRole("tab", { name })).toBeNull();
    }
    expect(screen.getByRole("tab", { name: "Changes" }).getAttribute("aria-selected")).toBe("true");
    expectStreams();

    fireEvent.click(checkbox);
    expect(checkbox.checked).toBe(true);
    for (const name of ["Logs", "Errors", "Trace"]) {
      expect(screen.getByRole("tab", { name })).toBeTruthy();
    }
    expectStreams(tab);

    fireEvent.click(checkbox);
    expect(checkbox.checked).toBe(false);
    for (const name of ["Logs", "Errors", "Trace"]) {
      expect(screen.queryByRole("tab", { name })).toBeNull();
    }
    expect(screen.getByRole("tab", { name: "Changes" }).getAttribute("aria-selected")).toBe("true");
    expectStreams();
  });

  it("keeps Trace unavailable without a trace ID even with Debug checked", () => {
    run.traceId = "";
    localStorage.setItem("gratefulagents.inspectorOpen", "true");
    renderView();
    fireEvent.click(screen.getByRole("checkbox", { name: "Debug" }));
    expect(screen.getByRole("tab", { name: "Logs" })).toBeTruthy();
    expect(screen.getByRole("tab", { name: "Errors" })).toBeTruthy();
    expect(screen.queryByRole("tab", { name: "Trace" })).toBeNull();
  });

  it("uses only available tabs for numbered shortcuts as Debug changes", () => {
    localStorage.setItem("gratefulagents.inspectorOpen", "true");
    renderView();
    const selectThird = () => fireEvent.keyDown(window, { key: "#", code: "Digit3", metaKey: true, shiftKey: true });
    selectThird();
    expect(screen.getByRole("tab", { name: "Context" }).getAttribute("aria-selected")).toBe("true");
    fireEvent.click(screen.getByRole("checkbox", { name: "Debug" }));
    selectThird();
    expect(screen.getByRole("tab", { name: "Logs" }).getAttribute("aria-selected")).toBe("true");
    fireEvent.click(screen.getByRole("checkbox", { name: "Debug" }));
    selectThird();
    expect(screen.getByRole("tab", { name: "Context" }).getAttribute("aria-selected")).toBe("true");
  });

  it("places Computer outside Chat and preserves its controller across tabs and inspector closure", async () => {
    renderView();
    expect(screen.queryByRole("button", { name: /Desktop state/ })).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Computer shortcut" }));
    expect(screen.getByRole("tab", { name: "Computer" }).getAttribute("aria-selected")).toBe("true");
    fireEvent.click(within(screen.getByRole("tabpanel")).getByRole("button", { name: "Desktop state 0" }));
    fireEvent.click(screen.getByRole("tab", { name: "Changes" }));
    expect(screen.queryByRole("button", { name: /Desktop state/ })).toBeNull();
    fireEvent.click(screen.getByRole("tab", { name: "Computer" }));
    expect(screen.getByRole("button", { name: "Desktop state 1" })).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Close inspector" }));
    expect(screen.queryByRole("button", { name: /Desktop state/ })).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Computer shortcut" }));
    expect(screen.getByRole("button", { name: "Desktop state 1" })).toBeTruthy();
    expect(computer.mount).toHaveBeenCalledTimes(1);
    expect(computer.unmount).not.toHaveBeenCalled();
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
    fireEvent.click(screen.getByRole("checkbox", { name: "Debug" }));
    expect(screen.getByRole("tab", { name: /Errors/ }).textContent).toContain("2");
    expect(screen.getByRole("button", { name: "Hide inspector" })).toBeTruthy();
  });
});
