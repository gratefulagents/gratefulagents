import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, useLocation } from "react-router-dom";

import { CommandPalette } from "@/components/shell/CommandPalette";

vi.mock("@/contexts/AuthContext", () => ({
  useAuth: () => ({ logout: vi.fn() }),
}));

vi.mock("@/hooks/useRecents", () => ({
  useRecents: () => [],
}));

vi.mock("@/lib/platform", () => ({
  isTauri: false,
}));

function LocationProbe() {
  const location = useLocation();
  return <div data-testid="location">{location.pathname}</div>;
}

function renderPalette() {
  return render(
    <MemoryRouter initialEntries={["/"]}>
      <CommandPalette open onOpenChange={vi.fn()} />
      <LocationProbe />
    </MemoryRouter>,
  );
}

describe("CommandPalette", () => {
  beforeEach(() => {
    // cmdk scrolls the selected item into view and observes list size; jsdom
    // implements neither.
    Element.prototype.scrollIntoView = vi.fn();
    globalThis.ResizeObserver ??= class {
      observe() {}
      unobserve() {}
      disconnect() {}
    };
    Object.defineProperty(window, "matchMedia", {
      configurable: true,
      value: vi.fn().mockReturnValue({
        matches: false,
        addEventListener: vi.fn(),
        removeEventListener: vi.fn(),
        addListener: vi.fn(),
        removeListener: vi.fn(),
      }),
    });
  });
  afterEach(cleanup);

  it("offers a Security Scans entry that navigates to /security", async () => {
    renderPalette();

    fireEvent.change(screen.getByPlaceholderText("Search or run a command…"), {
      target: { value: "security" },
    });

    const item = await screen.findByText("Security Scans");
    expect(screen.getByText("Scan runs and finding triage")).toBeTruthy();

    fireEvent.click(item);

    await waitFor(() => {
      expect(screen.getByTestId("location").textContent).toBe("/security");
    });
  });

  it("matches the entry by finding-related keywords", async () => {
    renderPalette();

    fireEvent.change(screen.getByPlaceholderText("Search or run a command…"), {
      target: { value: "vulnerabilities" },
    });

    expect(await screen.findByText("Security Scans")).toBeTruthy();
  });
});
