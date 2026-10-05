import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";

import { ResourcePage } from "@/components/resources/ResourcePage";
import { client } from "@/lib/client";

vi.mock("@/contexts/AuthContext", () => ({
  useAuth: () => ({ user: { id: "u1", role: "member" } }),
}));

vi.mock("@/components/MCPServerPicker", () => ({ MCPServerPicker: () => <div data-testid="mcp-picker" /> }));
vi.mock("@/components/SkillPicker", () => ({ SkillPicker: () => <div data-testid="skill-picker" /> }));

vi.mock("@/lib/client", () => ({
  client: {
    listModeTemplates: vi.fn().mockResolvedValue({
      templates: [{
        name: "autopilot",
        version: "v1",
        displayName: "Autopilot",
        description: "Built-in mode",
        category: "direct",
        executionStrategy: "serial",
        instructions: "",
        autonomous: true,
        permissionMode: "workspace-write",
        allowedMutatingTools: [],
        defaultMcpServerRefs: [],
        defaultSkillRefs: [],
      }],
    }),
    createModeTemplate: vi.fn().mockResolvedValue({}),
    updateModeTemplate: vi.fn().mockResolvedValue({}),
    deleteModeTemplate: vi.fn().mockResolvedValue({}),
  },
}));

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

function renderModes() {
  return render(
    <MemoryRouter initialEntries={["/resources/modes"]}>
      <Routes>
        <Route path="/resources/:kind" element={<ResourcePage />} />
      </Routes>
    </MemoryRouter>,
  );
}

describe("ResourcePage mode templates", () => {
  it("shows the tab strip and lets a member edit without deleting the catalog", async () => {
    renderModes();

    const nav = screen.getByRole("navigation", { name: "Resource types" });
    expect(nav.textContent).toContain("Skills");
    expect(nav.textContent).toContain("Modes");
    expect(nav.textContent).toContain("Roles");

    expect(await screen.findByText("Autopilot")).toBeTruthy();
    expect(screen.getByText("Autonomous")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Edit autopilot" })).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Delete autopilot" })).toBeNull();
    expect(screen.getByText(/Only administrators can delete them/)).toBeTruthy();

    fireEvent.click(screen.getByRole("button", { name: "Edit autopilot" }));
    const save = screen.getByRole("button", { name: "Save changes" });
    expect((save as HTMLButtonElement).disabled).toBe(true);
    fireEvent.change(screen.getByLabelText("Version"), { target: { value: "v2" } });
    expect((save as HTMLButtonElement).disabled).toBe(false);
    fireEvent.click(save);

    await waitFor(() => {
      expect(client.updateModeTemplate).toHaveBeenCalledWith({
        template: expect.objectContaining({
          name: "autopilot",
          version: "v2",
          category: "direct",
          executionStrategy: "serial",
          autonomous: true,
          permissionMode: "workspace-write",
        }),
      });
    });
  });

  it("creates a mode with sensible defaults", async () => {
    renderModes();
    expect(await screen.findByText("Autopilot")).toBeTruthy();

    fireEvent.click(screen.getByRole("button", { name: "New mode" }));
    const create = screen.getByRole("button", { name: "Create mode" });
    expect((create as HTMLButtonElement).disabled).toBe(true);
    fireEvent.change(screen.getByLabelText(/^Name/), { target: { value: "my-mode" } });
    expect((create as HTMLButtonElement).disabled).toBe(false);
    fireEvent.click(create);

    await waitFor(() => {
      expect(client.createModeTemplate).toHaveBeenCalledWith({
        template: expect.objectContaining({
          name: "my-mode",
          version: "v1",
          category: "direct",
          executionStrategy: "serial",
          permissionMode: "",
        }),
      });
    });
    const template = vi.mocked(client.createModeTemplate).mock.calls[0][0].template;
    expect(template?.constraints).toBeUndefined();
  });

  it("rejects invalid names before contacting the server", async () => {
    renderModes();
    expect(await screen.findByText("Autopilot")).toBeTruthy();

    fireEvent.click(screen.getByRole("button", { name: "New mode" }));
    fireEvent.change(screen.getByLabelText(/^Name/), { target: { value: "Bad Name" } });
    expect(screen.getByRole("alert").textContent).toMatch(/lowercase/);
    expect((screen.getByRole("button", { name: "Create mode" }) as HTMLButtonElement).disabled).toBe(true);
    expect(client.createModeTemplate).not.toHaveBeenCalled();
  });
});
