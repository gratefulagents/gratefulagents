import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";

import { ResourcePage } from "@/components/resources/ResourcePage";
import { client } from "@/lib/client";

vi.mock("@/contexts/AuthContext", () => ({
  useAuth: () => ({ user: { id: "u1", role: "member" } }),
}));

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
  },
}));

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

describe("ResourcePage mode templates", () => {
  it("lets a member create and edit templates without deleting the catalog", async () => {
    render(
      <MemoryRouter initialEntries={["/resources/modes"]}>
        <Routes>
          <Route path="/resources/:kind" element={<ResourcePage />} />
        </Routes>
      </MemoryRouter>,
    );

    expect(await screen.findByText("Autopilot")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Edit autopilot" })).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Delete autopilot" })).toBeNull();

    fireEvent.click(screen.getByRole("button", { name: "Edit autopilot" }));
    fireEvent.change(screen.getByLabelText("Version"), { target: { value: "v2" } });
    fireEvent.click(screen.getByRole("button", { name: "Save" }));

    await waitFor(() => {
      expect(client.updateModeTemplate).toHaveBeenCalledWith({
        template: expect.objectContaining({
          name: "autopilot",
          version: "v2",
          category: "direct",
          executionStrategy: "serial",
        }),
      });
    });

    fireEvent.click(screen.getByRole("button", { name: "Create" }));
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "my-mode" } });
    fireEvent.click(screen.getByRole("button", { name: "Save" }));

    await waitFor(() => {
      expect(client.createModeTemplate).toHaveBeenCalledWith({
        template: expect.objectContaining({
          name: "my-mode",
          version: "v1",
          category: "direct",
          executionStrategy: "serial",
        }),
      });
    });
  });
});
