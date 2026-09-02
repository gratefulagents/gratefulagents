import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";

import {
  RunInspector,
  inspectorShortcut,
  inspectorTabMeta,
  isInspectorTab,
  type InspectorTab,
  type InspectorTabDef,
} from "./RunInspector";

const tabs: InspectorTabDef[] = [
  { id: "diff", dot: true },
  { id: "logs" },
  { id: "errors", count: 3 },
];

function renderInspector(overrides: Partial<Parameters<typeof RunInspector>[0]> = {}) {
  const onTabChange = vi.fn();
  const onOpenChange = vi.fn();
  render(
    <RunInspector
      split
      open
      onOpenChange={onOpenChange}
      tabs={tabs}
      activeTab="diff"
      onTabChange={onTabChange}
      {...overrides}
    >
      <div>pane content</div>
    </RunInspector>,
  );
  return { onTabChange, onOpenChange };
}

afterEach(cleanup);

describe("isInspectorTab", () => {
  it("accepts the MainView vocabulary minus chat", () => {
    expect(isInspectorTab("diff")).toBe(true);
    expect(isInspectorTab("trace")).toBe(true);
    // "chat" is the page itself, never an inspector tab.
    expect(isInspectorTab("chat")).toBe(false);
    expect(isInspectorTab(null)).toBe(false);
  });

  it("offers the run context as its own tab", () => {
    expect(isInspectorTab("context")).toBe(true);
    expect(inspectorTabMeta.context.label).toBe("Context");
  });
});

describe("inspectorShortcut", () => {
  const order: InspectorTab[] = ["diff", "graph", "errors"];
  const key = (overrides: Partial<Parameters<typeof inspectorShortcut>[0]>) => ({
    key: "",
    code: "",
    metaKey: false,
    ctrlKey: false,
    shiftKey: false,
    altKey: false,
    ...overrides,
  });

  it("toggles on Mod+.", () => {
    expect(inspectorShortcut(key({ key: ".", code: "Period", metaKey: true }), order)).toEqual({ type: "toggle" });
    expect(inspectorShortcut(key({ key: ".", code: "Period", ctrlKey: true }), order)).toEqual({ type: "toggle" });
    expect(inspectorShortcut(key({ key: ".", code: "Period" }), order)).toBeNull();
  });

  it("selects the n-th tab on Mod+Shift+digit", () => {
    expect(inspectorShortcut(key({ key: "@", code: "Digit2", metaKey: true, shiftKey: true }), order)).toEqual({
      type: "select",
      tab: "graph",
    });
    // Digits without Shift belong to the browser (tab switching).
    expect(inspectorShortcut(key({ key: "2", code: "Digit2", metaKey: true }), order)).toBeNull();
    // Out of range digits do nothing.
    expect(inspectorShortcut(key({ key: "$", code: "Digit4", metaKey: true, shiftKey: true }), order)).toBeNull();
  });
});

describe("RunInspector", () => {
  it("renders one tab per section and marks the active one", () => {
    renderInspector();
    expect(screen.getByRole("tab", { name: /Changes/ }).getAttribute("aria-selected")).toBe("true");
    expect(screen.getByRole("tab", { name: /Logs/ }).getAttribute("aria-selected")).toBe("false");
    expect(screen.getByText("pane content")).toBeTruthy();
  });

  it("surfaces an error count on the tab", () => {
    renderInspector();
    expect(screen.getByRole("tab", { name: /Errors/ }).textContent).toContain("3");
  });

  it("reports tab changes", () => {
    const { onTabChange } = renderInspector();
    fireEvent.click(screen.getByRole("tab", { name: /Logs/ }));
    expect(onTabChange).toHaveBeenCalledWith("logs");
  });

  it("wires tabs to the panel with the ARIA tabs pattern", () => {
    renderInspector();
    const active = screen.getByRole("tab", { name: /Changes/ });
    const inactive = screen.getByRole("tab", { name: /Logs/ });
    const panel = screen.getByRole("tabpanel");
    expect(active.getAttribute("tabindex")).toBe("0");
    expect(inactive.getAttribute("tabindex")).toBe("-1");
    expect(active.getAttribute("aria-controls")).toBe(panel.id);
    expect(panel.getAttribute("aria-labelledby")).toBe(active.id);
  });

  it("moves selection and focus with the arrow keys", () => {
    const { onTabChange } = renderInspector();
    const tablist = screen.getByRole("tablist", { name: "Inspector sections" });
    fireEvent.keyDown(tablist, { key: "ArrowRight" });
    expect(onTabChange).toHaveBeenLastCalledWith("logs");
    expect(document.activeElement).toBe(screen.getByRole("tab", { name: /Logs/ }));
    // ArrowLeft from the first tab wraps to the last one.
    fireEvent.keyDown(tablist, { key: "ArrowLeft" });
    expect(onTabChange).toHaveBeenLastCalledWith("errors");
    fireEvent.keyDown(tablist, { key: "End" });
    expect(onTabChange).toHaveBeenLastCalledWith("errors");
    fireEvent.keyDown(tablist, { key: "Home" });
    expect(onTabChange).toHaveBeenLastCalledWith("diff");
  });

  it("keeps persistent panes mounted next to the active pane", () => {
    render(
      <RunInspector
        split
        open
        onOpenChange={vi.fn()}
        tabs={tabs}
        activeTab="logs"
        onTabChange={vi.fn()}
        persistent={<div hidden>graph pane</div>}
      >
        <div>logs pane</div>
      </RunInspector>,
    );
    expect(screen.getByText("logs pane")).toBeTruthy();
    expect(screen.getByText("graph pane", { ignore: false })).toBeTruthy();
  });

  it("closes from the panel header", () => {
    const { onOpenChange } = renderInspector();
    fireEvent.click(screen.getByRole("button", { name: "Close inspector" }));
    expect(onOpenChange).toHaveBeenCalledWith(false);
  });
});
