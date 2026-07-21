import { create } from "@bufbuild/protobuf";
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";

import { CreateProjectDialog } from "@/components/CreateProjectDialog";
import { ProjectSettingsDialog } from "@/components/ProjectSettingsDialog";
import { client } from "@/lib/client";
import { ProjectSchema } from "@/rpc/platform/service_pb";

vi.mock("@/lib/client", () => ({
  client: {
    listMyCredentials: vi.fn().mockResolvedValue({
      namespace: "user-alice",
      anthropicApiKeyPresent: false,
      openaiApiKeyPresent: true,
      openrouterApiKeyPresent: true,
      anthropicOauthPresent: false,
      openaiOauthPresent: false,
      copilotOauthPresent: false,
      githubTokenPresent: false,
    }),
    listAvailableModels: vi.fn().mockImplementation(({ provider }: { provider: string }) =>
      Promise.resolve({
        models: provider === "openrouter" ? ["z-ai/glm-4.7", "openai/gpt-5.4"] : [],
      }),
    ),
    listRuntimeImages: vi.fn().mockResolvedValue({ images: [] }),
    listMCPServers: vi.fn().mockResolvedValue({ servers: [] }),
    listSkills: vi.fn().mockResolvedValue({ skills: [] }),
    createProject: vi.fn(),
    updateProject: vi.fn(),
  },
}));

vi.mock("@/contexts/AuthContext", () => ({
  useAuth: () => ({ user: { id: "u1", role: "member", name: "Alice", username: "alice" } }),
}));

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

describe("OpenRouter project model pickers", () => {
  it("loads the live OpenRouter catalog while creating a project", async () => {
    render(
      <MemoryRouter>
        <CreateProjectDialog />
      </MemoryRouter>,
    );

    fireEvent.click(screen.getByRole("button", { name: "Create Project" }));
    await waitFor(() => expect(client.listMyCredentials).toHaveBeenCalledTimes(1));
    fireEvent.click(screen.getByRole("button", { name: "OpenRouter" }));

    await waitFor(() => {
      expect(client.listAvailableModels).toHaveBeenCalledWith(
        {
          namespace: "user-alice",
          provider: "openrouter",
          authMode: "api-key",
        },
        expect.anything(),
      );
    });

    expect(await screen.findByText("2 OpenRouter models available")).toBeTruthy();
    const input = screen.getByLabelText("Default model");
    expect(input.getAttribute("list")).toBe("project-model-options");
    expect(document.querySelector('#project-model-options option[value="z-ai/glm-4.7"]')).toBeTruthy();
  });

  it("loads the live OpenRouter catalog from an existing project's credentials", async () => {
    const project = create(ProjectSchema, {
      namespace: "user-alice",
      name: "payments",
      displayName: "Payments",
      provider: "openrouter",
      authMode: "api-key",
      model: "z-ai/glm-4.7",
      providerKeys: [
        { provider: "openrouter", secretName: "usercred-openrouter", secretKey: "api-key" },
      ],
    });

    render(
      <MemoryRouter>
        <ProjectSettingsDialog project={project} />
      </MemoryRouter>,
    );

    fireEvent.click(screen.getByRole("button", { name: /Settings/ }));

    await waitFor(() => {
      expect(client.listAvailableModels).toHaveBeenCalledWith(
        {
          namespace: "user-alice",
          source: { kind: "Project", name: "payments" },
          provider: "openrouter",
        },
        expect.anything(),
      );
    });

    expect(await screen.findByText("2 OpenRouter models available")).toBeTruthy();
    const input = screen.getByLabelText("Model") as HTMLInputElement;
    expect(input.value).toBe("z-ai/glm-4.7");
    expect(input.getAttribute("list")).toBe("project-settings-model-options");
    expect(
      document.querySelector('#project-settings-model-options option[value="openai/gpt-5.4"]'),
    ).toBeTruthy();
    expect(screen.getByRole("switch", { name: "Use my saved provider credentials" }).getAttribute("aria-checked"))
      .toBe("true");
  });
});

describe("Project settings flow", () => {
  it("groups project defaults into the shared create-flow disclosures", async () => {
    const project = create(ProjectSchema, {
      namespace: "user-alice",
      name: "payments",
      displayName: "Payments",
      repoUrl: "https://github.com/acme/payments",
      baseBranch: "main",
      provider: "openrouter",
      authMode: "api-key",
      model: "z-ai/glm-4.7",
      providerKeys: [
        { provider: "openrouter", secretName: "usercred-openrouter", secretKey: "api-key" },
      ],
      mcpServerRefs: ["github"],
      skillRefs: ["code-review"],
    });

    render(
      <MemoryRouter>
        <ProjectSettingsDialog project={project} />
      </MemoryRouter>,
    );

    fireEvent.click(screen.getByRole("button", { name: /Settings/ }));

    expect(screen.getByRole("heading", { name: "Project settings" })).toBeTruthy();
    expect(
      screen.getByText("Update the defaults inherited by new agent runs from this project."),
    ).toBeTruthy();
    expect(screen.getByLabelText(/Display name/)).toBeTruthy();
    expect(screen.getByLabelText("Repository URL")).toBeTruthy();
    expect(screen.getByRole("button", { name: /Model & credentials/ })).toBeTruthy();
    expect(screen.getByRole("button", { name: /Repository details/ })).toBeTruthy();
    expect(screen.getByRole("button", { name: /Runtime/ })).toBeTruthy();
    expect(screen.getByRole("button", { name: /Tools/ }).textContent).toContain("1 MCP server");
    expect(screen.queryByRole("switch", { name: "Attach code-review" })).toBeNull();
    expect(screen.getByRole("button", { name: /MCP policy/ })).toBeTruthy();
    expect(screen.getByRole("button", { name: /Advanced/ })).toBeTruthy();
    expect(screen.getByText("Editing")).toBeTruthy();
    expect(screen.getByText("user-alice/payments")).toBeTruthy();
  });
});
