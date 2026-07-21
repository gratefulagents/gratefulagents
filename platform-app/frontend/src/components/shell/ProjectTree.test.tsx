import { create } from "@bufbuild/protobuf";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";

import { ProjectTree } from "@/components/shell/ProjectTree";
import { SidebarProvider } from "@/components/ui/sidebar";
import { AgentRunSchema, ProjectSchema, type AgentRun } from "@/rpc/platform/service_pb";

const projects = [
  create(ProjectSchema, { namespace: "team", name: "platform", displayName: "Platform" }),
  create(ProjectSchema, { namespace: "team", name: "website", displayName: "Website" }),
];

function renderTree(
  workspaceId = "workspace-a",
  runs: AgentRun[] = [],
) {
  return render(
    <MemoryRouter>
      <SidebarProvider>
        <ProjectTree
          projects={projects}
          runs={runs}
          workspaceId={workspaceId}
          onNewChat={vi.fn()}
        />
      </SidebarProvider>
    </MemoryRouter>,
  );
}

describe("ProjectTree hidden projects", () => {
  beforeEach(() => {
    localStorage.clear();
    Object.defineProperty(window, "matchMedia", {
      configurable: true,
      value: vi.fn().mockReturnValue({
        matches: false,
        addEventListener: vi.fn(),
        removeEventListener: vi.fn(),
      }),
    });
  });
  afterEach(cleanup);

  it("hides a project and lets the user restore it", async () => {
    renderTree();

    fireEvent.click(screen.getByRole("button", { name: "Actions for Platform (team/platform)" }));
    fireEvent.click(await screen.findByRole("menuitem", {
      name: "Hide Platform (team/platform) from sidebar",
    }));

    expect(screen.queryByRole("link", { name: "Platform" })).toBeNull();
    expect(screen.getByRole("button", { name: "1 hidden project" })).toBeTruthy();
    expect(localStorage.getItem("gratefulagents.sidebar.hiddenProjects.v1.workspace-a"))
      .toBe('["team/platform"]');
    await waitFor(() => expect(document.activeElement).toBe(
      screen.getByRole("link", { name: "Website" }),
    ));

    fireEvent.click(screen.getByRole("button", { name: "1 hidden project" }));
    fireEvent.click(await screen.findByRole("menuitem", {
      name: "Show Platform (team/platform) in sidebar",
    }));

    const restoredLink = screen.getByRole("link", { name: "Platform" });
    expect(restoredLink).toBeTruthy();
    expect(screen.queryByRole("button", { name: "1 hidden project" })).toBeNull();
    await waitFor(() => expect(document.activeElement).toBe(restoredLink));
  });

  it("does not count completed runs from hidden projects", async () => {
    const completedRun = create(AgentRunSchema, {
      namespace: "team",
      name: "completed-run",
      phase: "Succeeded",
      project: { name: "platform", kind: "Project" },
    });
    renderTree("workspace-a", [completedRun]);

    expect(screen.getByText("Show completed (1)")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Actions for Platform (team/platform)" }));
    fireEvent.click(await screen.findByRole("menuitem", {
      name: "Hide Platform (team/platform) from sidebar",
    }));

    expect(screen.queryByText("Show completed (1)")).toBeNull();
  });

  it("keeps hidden project preferences scoped to a workspace", () => {
    localStorage.setItem(
      "gratefulagents.sidebar.hiddenProjects.v1.workspace-a",
      '["team/platform"]',
    );

    const { unmount } = renderTree("workspace-a");
    expect(screen.queryByRole("link", { name: "Platform" })).toBeNull();
    unmount();

    renderTree("workspace-b");
    expect(screen.getByRole("link", { name: "Platform" })).toBeTruthy();
  });
});
