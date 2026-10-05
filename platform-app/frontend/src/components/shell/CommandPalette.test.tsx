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

  it("keeps project navigation available", async () => {
    renderPalette();
    fireEvent.change(screen.getByPlaceholderText("Search or run a command…"), {
      target: { value: "projects" },
    });
    fireEvent.click(await screen.findByText("Projects"));
    await waitFor(() => {
      expect(screen.getByTestId("location").textContent).toBe("/projects");
    });
  });

  it("does not offer retired scan or bug-report pages", () => {
    renderPalette();
    expect(screen.queryByText("Security")).toBeNull();
    expect(screen.queryByText("Bug Reports")).toBeNull();
    fireEvent.change(screen.getByPlaceholderText("Search or run a command…"), {
      target: { value: "vulnerabilities" },
    });
    expect(screen.queryByText("Security")).toBeNull();
  });

  it("does not offer the retired SOUL settings command", () => {
    renderPalette();
    expect(screen.queryByText("Settings: SOUL")).toBeNull();
    fireEvent.change(screen.getByPlaceholderText("Search or run a command…"), {
      target: { value: "persona" },
    });
    expect(screen.queryByText("Settings: SOUL")).toBeNull();
  });
});
