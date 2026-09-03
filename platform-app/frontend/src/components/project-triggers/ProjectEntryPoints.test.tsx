import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { MemoryRouter } from "react-router-dom";

import { ProjectEntryPoints } from "@/components/project-triggers/ProjectEntryPoints";
import type { ProjectConnection, ProjectTrigger } from "@/components/project-triggers/types";

const listConnections = vi.fn();

vi.mock("@/lib/client", () => ({
  client: {
    listConnections: (...args: unknown[]) => listConnections(...args),
  },
}));

vi.mock("@/lib/native", () => ({
  openExternal: vi.fn().mockResolvedValue(undefined),
}));

vi.mock("@/components/ui/select", () => {
  type P = { children?: ReactNode; placeholder?: string; [k: string]: unknown };
  return {
    Select: ({ children }: P) => <>{children}</>,
    SelectTrigger: ({ children, ...props }: P) => <div {...props}>{children}</div>,
    SelectValue: ({ placeholder }: P) => <span>{placeholder}</span>,
    SelectContent: ({ children }: P) => <div>{children}</div>,
    SelectItem: ({ children }: P) => <button type="button">{children}</button>,
  };
});

// Base UI's dropdown menu relies on pointer/positioning APIs that jsdom
// lacks; render the menu items inline so "Edit" is directly clickable.
vi.mock("@/components/ui/dropdown-menu", () => {
  type P = { children?: ReactNode; onClick?: () => void };
  return {
    DropdownMenu: ({ children }: P) => <>{children}</>,
    DropdownMenuTrigger: () => null,
    DropdownMenuContent: ({ children }: P) => <div>{children}</div>,
    DropdownMenuItem: ({ children, onClick }: P) => (
      <button type="button" onClick={onClick}>
        {children}
      </button>
    ),
  };
});

const GITHUB_CONNECTION: ProjectConnection = {
  name: "my-github",
  type: "github",
  github: { tokenSecret: "some-secret" },
};

const GITHUB_TRIGGER: ProjectTrigger = {
  name: "gh",
  type: "github",
  enabled: true,
  github: { connectionRef: "my-github", owner: "acme", repo: "payments", issues: true, comments: false },
};

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

describe("ProjectEntryPoints", () => {
  it("loads connections before opening the edit dialog so an existing trigger can be saved", async () => {
    listConnections.mockResolvedValue({ connections: [GITHUB_CONNECTION] });
    render(
      <MemoryRouter>
        <ProjectEntryPoints
          namespace="ns"
          projectName="proj"
          triggers={[GITHUB_TRIGGER]}
          canEdit
          onChanged={vi.fn()}
        />
      </MemoryRouter>,
    );

    fireEvent.click(screen.getByRole("button", { name: /edit/i }));

    await waitFor(() => expect(screen.getByText("Edit gh")).toBeTruthy());
    expect(listConnections).toHaveBeenCalledWith({ namespace: "ns" });
    expect(screen.queryByText(/Connect GitHub first/)).toBeNull();
    const save = screen.getByRole("button", { name: "Save changes" }) as HTMLButtonElement;
    expect(save.disabled).toBe(false);
  });
});
