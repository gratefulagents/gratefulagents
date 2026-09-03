import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { Home, Radio, Shield } from "lucide-react";

import { NavRail, type RailGroup } from "@/components/shell/NavRail";
import { SidebarProvider } from "@/components/ui/sidebar";
import { TooltipProvider } from "@/components/ui/tooltip";

vi.mock("@/contexts/AuthContext", () => ({
  useAuth: () => ({
    user: { name: "Dana Demo", email: "dana@example.com" },
    workspaces: [],
    activeWorkspaceId: "",
    switchWorkspace: vi.fn(),
    addWorkspace: vi.fn(),
  }),
}));

const groups: RailGroup[] = [
  {
    id: "primary",
    items: [
      { to: "/", label: "Home", icon: Home },
      { to: "/runs", label: "Agent Ops", icon: Radio, attention: { label: "Runs need attention" } },
    ],
  },
  {
    id: "workspace",
    items: [{ to: "/security", label: "Security", icon: Shield, match: (p) => p.startsWith("/security") }],
  },
];

function renderRail(path: string) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <TooltipProvider>
        <SidebarProvider>
          <NavRail groups={groups} />
        </SidebarProvider>
      </TooltipProvider>
    </MemoryRouter>,
  );
}

describe("NavRail", () => {
  beforeEach(() => {
    Object.defineProperty(window, "matchMedia", {
      configurable: true,
      value: vi.fn().mockReturnValue({ matches: false, addEventListener: vi.fn(), removeEventListener: vi.fn() }),
    });
  });
  afterEach(cleanup);

  it("renders every destination as a labelled link and marks the current one", () => {
    renderRail("/security/runs");
    expect(screen.getByRole("link", { name: "Home" }).getAttribute("aria-current")).toBeNull();
    expect(screen.getByRole("link", { name: "Security" }).getAttribute("aria-current")).toBe("page");
    expect(screen.getByRole("navigation", { name: "Primary" })).toBeTruthy();
  });

  it("surfaces the attention marker and the settings avatar", () => {
    renderRail("/settings/usage");
    expect(screen.getByRole("img", { name: "Runs need attention" })).toBeTruthy();
    const settings = screen.getByRole("link", { name: "Settings" });
    expect(settings.getAttribute("aria-current")).toBe("page");
    expect(settings.textContent).toBe("D");
  });

  it("offers a panel toggle that reflects the sidebar state", () => {
    renderRail("/");
    expect(screen.getByRole("button", { name: "Hide projects panel" }).getAttribute("aria-pressed")).toBe("true");
  });
});
