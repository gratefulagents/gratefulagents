import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";

import { RunInspector, isInspectorTab, type InspectorTabDef } from "./RunInspector";

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

  it("closes from the panel header", () => {
    const { onOpenChange } = renderInspector();
    fireEvent.click(screen.getByRole("button", { name: "Close inspector" }));
    expect(onOpenChange).toHaveBeenCalledWith(false);
  });
});
