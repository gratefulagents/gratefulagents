import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";

import SettingsLayout from "@/components/settings/SettingsLayout";

vi.mock("@/contexts/AuthContext", () => ({
  useAuth: () => ({
    user: { id: "u1", username: "alice", name: "Alice", email: "alice@x.dev" },
  }),
}));

vi.mock("@/lib/platform", () => ({
  isTauri: false,
}));

function renderAt(path: string) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        <Route path="/settings" element={<SettingsLayout />}>
          <Route index element={<div>general-pane</div>} />
          <Route path="credentials" element={<div>credentials-pane</div>} />
          <Route path="usage" element={<div>usage-pane</div>} />
        </Route>
      </Routes>
    </MemoryRouter>,
  );
}

afterEach(() => {
  cleanup();
});

describe("SettingsLayout", () => {
  it("shows identity, section nav, and the active pane", () => {
    renderAt("/settings");
    expect(screen.getByText("Alice")).toBeTruthy();
    expect(screen.getByText("@alice")).toBeTruthy();
    expect(screen.getByRole("link", { name: /General/ })).toBeTruthy();
    expect(screen.getByRole("link", { name: /Credentials/ })).toBeTruthy();
    expect(screen.getByRole("link", { name: /Usage/ })).toBeTruthy();
    expect(screen.getByRole("link", { name: /Skills/ })).toBeTruthy();
    expect(screen.getByText("general-pane")).toBeTruthy();
    // Web build: desktop-only sections stay hidden.
    expect(screen.queryByRole("link", { name: /Connection/ })).toBeNull();
  });

  it("renders the section route in the content pane", () => {
    renderAt("/settings/usage");
    expect(screen.getByText("usage-pane")).toBeTruthy();
    expect(screen.queryByText("general-pane")).toBeNull();
  });

  it("filters sections by search, including keywords", () => {
    renderAt("/settings");
    const search = screen.getByLabelText("Search settings");

    fireEvent.change(search, { target: { value: "git" } });
    expect(screen.getByRole("link", { name: /Git identity/ })).toBeTruthy();
    expect(screen.queryByRole("link", { name: /Credentials/ })).toBeNull();

    // Keyword match: "theme" lives on General.
    fireEvent.change(search, { target: { value: "theme" } });
    expect(screen.getByRole("link", { name: /General/ })).toBeTruthy();

    fireEvent.change(search, { target: { value: "zzz-nothing" } });
    expect(screen.getByText(/No settings match/)).toBeTruthy();
  });
});
