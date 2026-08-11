import { create } from "@bufbuild/protobuf";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter, Routes, Route, useLocation } from "react-router-dom";

import { RunTabs } from "@/components/shell/RunTabs";
import { openRunTab } from "@/hooks/useRunTabs";
import { AgentRunSchema, type AgentRun } from "@/rpc/platform/service_pb";

const runs: AgentRun[] = [
  create(AgentRunSchema, {
    namespace: "demo",
    name: "run-a",
    displayName: "Fix login flow",
    phase: "Running",
  }),
  create(AgentRunSchema, {
    namespace: "demo",
    name: "run-b",
    displayName: "Ship tabs",
    phase: "Completed",
  }),
];

function LocationProbe() {
  const location = useLocation();
  return <div data-testid="location">{location.pathname}</div>;
}

function renderTabs(initialPath = "/runs/demo/run-a") {
  return render(
    <MemoryRouter initialEntries={[initialPath]}>
      <RunTabs runs={runs} />
      <Routes>
        <Route path="*" element={<LocationProbe />} />
      </Routes>
    </MemoryRouter>,
  );
}

describe("RunTabs", () => {
  beforeEach(() => {
    localStorage.clear();
    Element.prototype.scrollIntoView = () => {};
  });
  afterEach(() => {
    cleanup();
    localStorage.clear();
  });

  it("renders nothing when no tabs are open", () => {
    renderTabs("/projects");
    expect(screen.queryByRole("tablist")).toBeNull();
  });

  it("shows open tabs with run display names and marks the active one", () => {
    openRunTab("/runs/demo/run-a");
    openRunTab("/runs/demo/run-b");
    renderTabs("/runs/demo/run-a");

    const tabs = screen.getAllByRole("tab");
    expect(tabs).toHaveLength(2);
    expect(tabs[0].textContent).toContain("Fix login flow");
    expect(tabs[1].textContent).toContain("Ship tabs");
    expect(tabs[0].getAttribute("aria-selected")).toBe("true");
    expect(tabs[1].getAttribute("aria-selected")).toBe("false");
  });

  it("falls back to the run name when the run is unknown", () => {
    openRunTab("/runs/demo/run-mystery");
    renderTabs("/runs/demo/run-mystery");
    expect(screen.getByRole("tab", { name: "run-mystery" })).toBeTruthy();
  });

  it("clicking a tab navigates to its run", () => {
    openRunTab("/runs/demo/run-a");
    openRunTab("/runs/demo/run-b");
    renderTabs("/runs/demo/run-a");

    fireEvent.click(screen.getByRole("tab", { name: "Ship tabs" }));
    expect(screen.getByTestId("location").textContent).toBe("/runs/demo/run-b");
  });

  it("closing the active tab navigates to a neighbor", () => {
    openRunTab("/runs/demo/run-a");
    openRunTab("/runs/demo/run-b");
    renderTabs("/runs/demo/run-a");

    fireEvent.click(screen.getByRole("button", { name: "Close Fix login flow" }));
    expect(screen.getByTestId("location").textContent).toBe("/runs/demo/run-b");
    expect(screen.getAllByRole("tab")).toHaveLength(1);
  });

  it("closing the last tab falls back to /runs and hides the strip", () => {
    openRunTab("/runs/demo/run-a");
    renderTabs("/runs/demo/run-a");

    fireEvent.click(screen.getByRole("button", { name: "Close Fix login flow" }));
    expect(screen.getByTestId("location").textContent).toBe("/runs");
    expect(screen.queryByRole("tablist")).toBeNull();
  });

  it("closing an inactive tab does not navigate", () => {
    openRunTab("/runs/demo/run-a");
    openRunTab("/runs/demo/run-b");
    renderTabs("/runs/demo/run-a");

    fireEvent.click(screen.getByRole("button", { name: "Close Ship tabs" }));
    expect(screen.getByTestId("location").textContent).toBe("/runs/demo/run-a");
    expect(screen.getAllByRole("tab")).toHaveLength(1);
  });

  it("middle-click closes a tab", () => {
    openRunTab("/runs/demo/run-a");
    openRunTab("/runs/demo/run-b");
    renderTabs("/runs/demo/run-a");

    const tab = screen.getByRole("tab", { name: "Ship tabs" });
    fireEvent(
      tab,
      new MouseEvent("auxclick", { bubbles: true, cancelable: true, button: 1 }),
    );
    expect(screen.getAllByRole("tab")).toHaveLength(1);
  });

  it("cycles tabs with mod+alt+brackets", () => {
    openRunTab("/runs/demo/run-a");
    openRunTab("/runs/demo/run-b");
    renderTabs("/runs/demo/run-a");

    fireEvent.keyDown(window, { code: "BracketRight", metaKey: true, altKey: true });
    expect(screen.getByTestId("location").textContent).toBe("/runs/demo/run-b");
    fireEvent.keyDown(window, { code: "BracketRight", metaKey: true, altKey: true });
    expect(screen.getByTestId("location").textContent).toBe("/runs/demo/run-a");
    fireEvent.keyDown(window, { code: "BracketLeft", metaKey: true, altKey: true });
    expect(screen.getByTestId("location").textContent).toBe("/runs/demo/run-b");
  });

  it("closes the active tab with mod+alt+W", () => {
    openRunTab("/runs/demo/run-a");
    openRunTab("/runs/demo/run-b");
    renderTabs("/runs/demo/run-b");

    fireEvent.keyDown(window, { code: "KeyW", metaKey: true, altKey: true });
    expect(screen.getByTestId("location").textContent).toBe("/runs/demo/run-a");
    expect(screen.getAllByRole("tab")).toHaveLength(1);
  });

  it("keeps the strip visible on non-run routes", () => {
    openRunTab("/runs/demo/run-a");
    renderTabs("/settings");
    const tab = screen.getByRole("tab", { name: "Fix login flow" });
    expect(tab.getAttribute("aria-selected")).toBe("false");
    fireEvent.click(tab);
    expect(screen.getByTestId("location").textContent).toBe("/runs/demo/run-a");
  });
});
