import { create } from "@bufbuild/protobuf";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter, Routes, Route, useLocation } from "react-router-dom";

import { RunTabs } from "@/components/shell/RunTabs";
import { openRunTab } from "@/hooks/useRunTabs";

const SCOPE = "ws-test:user-test";
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
      <RunTabs runs={runs} scope={SCOPE} />
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
    openRunTab(SCOPE, "/runs/demo/run-a");
    openRunTab(SCOPE, "/runs/demo/run-b");
    renderTabs("/runs/demo/run-a");

    const tabs = screen.getAllByRole("tab");
    expect(tabs).toHaveLength(2);
    expect(tabs[0].textContent).toContain("Fix login flow");
    expect(tabs[1].textContent).toContain("Ship tabs");
    expect(tabs[0].getAttribute("aria-selected")).toBe("true");
    expect(tabs[1].getAttribute("aria-selected")).toBe("false");
  });

  it("falls back to the run name when the run is unknown", () => {
    openRunTab(SCOPE, "/runs/demo/run-mystery");
    renderTabs("/runs/demo/run-mystery");
    expect(screen.getByRole("tab", { name: "run-mystery" })).toBeTruthy();
  });

  it("clicking a tab navigates to its run", () => {
    openRunTab(SCOPE, "/runs/demo/run-a");
    openRunTab(SCOPE, "/runs/demo/run-b");
    renderTabs("/runs/demo/run-a");

    fireEvent.click(screen.getByRole("tab", { name: "Ship tabs" }));
    expect(screen.getByTestId("location").textContent).toBe("/runs/demo/run-b");
  });

  it("closing the active tab navigates to a neighbor", () => {
    openRunTab(SCOPE, "/runs/demo/run-a");
    openRunTab(SCOPE, "/runs/demo/run-b");
    renderTabs("/runs/demo/run-a");

    fireEvent.click(screen.getByRole("button", { name: "Close Fix login flow" }));
    expect(screen.getByTestId("location").textContent).toBe("/runs/demo/run-b");
    expect(screen.getAllByRole("tab")).toHaveLength(1);
  });

  it("closing the last tab falls back to /runs and hides the strip", () => {
    openRunTab(SCOPE, "/runs/demo/run-a");
    renderTabs("/runs/demo/run-a");

    fireEvent.click(screen.getByRole("button", { name: "Close Fix login flow" }));
    expect(screen.getByTestId("location").textContent).toBe("/runs");
    expect(screen.queryByRole("tablist")).toBeNull();
  });

  it("closing an inactive tab does not navigate", () => {
    openRunTab(SCOPE, "/runs/demo/run-a");
    openRunTab(SCOPE, "/runs/demo/run-b");
    renderTabs("/runs/demo/run-a");

    fireEvent.click(screen.getByRole("button", { name: "Close Ship tabs" }));
    expect(screen.getByTestId("location").textContent).toBe("/runs/demo/run-a");
    expect(screen.getAllByRole("tab")).toHaveLength(1);
  });

  it("middle-click closes a tab", () => {
    openRunTab(SCOPE, "/runs/demo/run-a");
    openRunTab(SCOPE, "/runs/demo/run-b");
    renderTabs("/runs/demo/run-a");

    const tab = screen.getByRole("tab", { name: "Ship tabs" });
    fireEvent(
      tab,
      new MouseEvent("auxclick", { bubbles: true, cancelable: true, button: 1 }),
    );
    expect(screen.getAllByRole("tab")).toHaveLength(1);
  });

  it("cycles tabs with mod+alt+brackets", () => {
    openRunTab(SCOPE, "/runs/demo/run-a");
    openRunTab(SCOPE, "/runs/demo/run-b");
    renderTabs("/runs/demo/run-a");

    fireEvent.keyDown(window, { code: "BracketRight", metaKey: true, altKey: true });
    expect(screen.getByTestId("location").textContent).toBe("/runs/demo/run-b");
    fireEvent.keyDown(window, { code: "BracketRight", metaKey: true, altKey: true });
    expect(screen.getByTestId("location").textContent).toBe("/runs/demo/run-a");
    fireEvent.keyDown(window, { code: "BracketLeft", metaKey: true, altKey: true });
    expect(screen.getByTestId("location").textContent).toBe("/runs/demo/run-b");
  });

  it("closes the active tab with mod+alt+W", () => {
    openRunTab(SCOPE, "/runs/demo/run-a");
    openRunTab(SCOPE, "/runs/demo/run-b");
    renderTabs("/runs/demo/run-b");

    fireEvent.keyDown(window, { code: "KeyW", metaKey: true, altKey: true });
    expect(screen.getByTestId("location").textContent).toBe("/runs/demo/run-a");
    expect(screen.getAllByRole("tab")).toHaveLength(1);
  });

  it("ignores AltGr keystrokes (reported as Ctrl+Alt on Windows layouts)", () => {
    openRunTab(SCOPE, "/runs/demo/run-a");
    openRunTab(SCOPE, "/runs/demo/run-b");
    renderTabs("/runs/demo/run-b");

    fireEvent(
      window,
      new KeyboardEvent("keydown", {
        code: "KeyW",
        ctrlKey: true,
        altKey: true,
        // EventModifierInit flag jsdom maps to getModifierState("AltGraph").
        modifierAltGraph: true,
      } as KeyboardEventInit),
    );
    expect(screen.getByTestId("location").textContent).toBe("/runs/demo/run-b");
    expect(screen.getAllByRole("tab")).toHaveLength(2);
  });

  it("ignores tab shortcuts while typing in an editable field", () => {
    openRunTab(SCOPE, "/runs/demo/run-a");
    openRunTab(SCOPE, "/runs/demo/run-b");
    const { container } = renderTabs("/runs/demo/run-b");

    const input = document.createElement("textarea");
    container.appendChild(input);
    fireEvent.keyDown(input, { code: "KeyW", metaKey: true, altKey: true });
    fireEvent.keyDown(input, { code: "BracketRight", metaKey: true, altKey: true });
    expect(screen.getByTestId("location").textContent).toBe("/runs/demo/run-b");
    expect(screen.getAllByRole("tab")).toHaveLength(2);
  });

  it("close-others on an inactive tab navigates to the surviving tab", () => {
    openRunTab(SCOPE, "/runs/demo/run-a");
    openRunTab(SCOPE, "/runs/demo/run-b");
    renderTabs("/runs/demo/run-a");

    // ⌥-right-click on the inactive "Ship tabs" tab closes the others,
    // including the tab for the current route — so we must move to B.
    fireEvent.contextMenu(screen.getByRole("tab", { name: "Ship tabs" }), { altKey: true });
    expect(screen.getByTestId("location").textContent).toBe("/runs/demo/run-b");
    const tabs = screen.getAllByRole("tab");
    expect(tabs).toHaveLength(1);
    expect(tabs[0].getAttribute("aria-selected")).toBe("true");
  });

  it("close-others on the active tab stays put", () => {
    openRunTab(SCOPE, "/runs/demo/run-a");
    openRunTab(SCOPE, "/runs/demo/run-b");
    renderTabs("/runs/demo/run-a");

    fireEvent.contextMenu(screen.getByRole("tab", { name: "Fix login flow" }), { altKey: true });
    expect(screen.getByTestId("location").textContent).toBe("/runs/demo/run-a");
    expect(screen.getAllByRole("tab")).toHaveLength(1);
  });

  it("keeps the strip visible on non-run routes", () => {
    openRunTab(SCOPE, "/runs/demo/run-a");
    renderTabs("/settings");
    const tab = screen.getByRole("tab", { name: "Fix login flow" });
    expect(tab.getAttribute("aria-selected")).toBe("false");
    fireEvent.click(tab);
    expect(screen.getByTestId("location").textContent).toBe("/runs/demo/run-a");
  });
});
