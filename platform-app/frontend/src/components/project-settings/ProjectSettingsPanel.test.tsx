import { create } from "@bufbuild/protobuf";
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";

import { ProjectSettingsPanel } from "@/components/project-settings/ProjectSettingsPanel";
import { client } from "@/lib/client";
import { ProjectSchema } from "@/rpc/platform/service_pb";

vi.mock("@/lib/client", () => ({
  client: {
    listMyCredentials: vi.fn().mockResolvedValue({
      namespace: "user-alice",
      anthropicApiKeyPresent: false,
      openaiApiKeyPresent: true,
      openrouterApiKeyPresent: true,
      xaiApiKeyPresent: false,
      anthropicOauthPresent: false,
      openaiOauthPresent: false,
      copilotOauthPresent: false,
      githubTokenPresent: false,
      secrets: [],
    }),
    listAvailableModels: vi.fn().mockImplementation(({ provider }: { provider: string }) =>
      Promise.resolve({
        models: provider === "openrouter" ? ["z-ai/glm-4.7", "openai/gpt-5.4"] : [],
      }),
    ),
    listRuntimeImages: vi.fn().mockResolvedValue({ images: [] }),
    listMCPServers: vi.fn().mockResolvedValue({ servers: [] }),
    listModeTemplates: vi.fn().mockResolvedValue({ templates: [] }),
    updateProject: vi.fn(),
  },
}));

const { authUser } = vi.hoisted(() => ({ authUser: { role: "member" } }));
vi.mock("@/contexts/AuthContext", () => ({
  useAuth: () => ({ user: { id: "u1", name: "Alice", username: "alice", ...authUser } }),
}));

const project = create(ProjectSchema, {
  namespace: "user-alice",
  name: "payments",
  displayName: "Payments",
  repoUrl: "https://github.com/acme/payments",
  baseBranch: "main",
  provider: "openrouter",
  authMode: "api-key",
  model: "z-ai/glm-4.7",
  providerKeys: [{ provider: "openrouter", secretName: "usercred-openrouter", secretKey: "api-key" }],
  mcpServerRefs: ["github"],
  reviewLoopDisabled: true,
});

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
  authUser.role = "member";
});

function renderPanel(onUpdated = vi.fn()) {
  render(
    <MemoryRouter>
      <ProjectSettingsPanel project={project} onUpdated={onUpdated} />
    </MemoryRouter>,
  );
  return onUpdated;
}

describe("ProjectSettingsPanel", () => {
  it("shows every section expanded with the project's values and no save bar", async () => {
    renderPanel();
    await waitFor(() => expect(client.listMyCredentials).toHaveBeenCalledTimes(1));

    for (const title of ["General", "Model & credentials", "Agent behavior", "Runtime", "Tools"]) {
      expect(screen.getByRole("heading", { name: title })).toBeTruthy();
    }
    expect(screen.queryByRole("heading", { name: "Privileged access" })).toBeNull();

    expect((screen.getByLabelText(/Display name/) as HTMLInputElement).value).toBe("Payments");
    expect((screen.getByLabelText("Repository URL") as HTMLInputElement).value).toBe(
      "https://github.com/acme/payments",
    );
    expect((screen.getByLabelText("Base branch") as HTMLInputElement).value).toBe("main");
    expect((screen.getByLabelText("Model") as HTMLInputElement).value).toBe("z-ai/glm-4.7");
    expect(
      screen.getByRole("switch", { name: "Use my saved credentials" }).getAttribute("aria-checked"),
    ).toBe("true");
    expect(
      screen.getByRole("switch", { name: "Autonomous PR review loop" }).getAttribute("aria-checked"),
    ).toBe("false");
    expect(screen.queryByRole("button", { name: "Save changes" })).toBeNull();
  });

  it("loads the live catalog through the project's own credentials", async () => {
    renderPanel();
    await waitFor(() => {
      expect(client.listAvailableModels).toHaveBeenCalledWith(
        { namespace: "user-alice", source: { kind: "Project", name: "payments" }, provider: "openrouter" },
        expect.anything(),
      );
    });
    expect(await screen.findByText("2 OpenRouter models available")).toBeTruthy();
    expect(screen.getByLabelText("Model").getAttribute("list")).toBe("project-settings-model-options");
  });

  it("surfaces a save bar naming the changed sections, resets per section, and saves", async () => {
    const onUpdated = renderPanel();
    await waitFor(() => expect(client.listMyCredentials).toHaveBeenCalledTimes(1));

    fireEvent.change(screen.getByLabelText("Timeout"), { target: { value: "45m" } });
    fireEvent.change(screen.getByLabelText(/Display name/), { target: { value: "Payments API" } });

    expect(screen.getByText("Unsaved changes in General, Runtime.")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Reset Runtime" }));
    expect((screen.getByLabelText("Timeout") as HTMLInputElement).value).toBe("");
    expect(screen.getByText("Unsaved changes in General.")).toBeTruthy();

    const updated = create(ProjectSchema, { ...project, displayName: "Payments API" });
    vi.mocked(client.updateProject).mockResolvedValueOnce(updated);
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));

    await waitFor(() => expect(client.updateProject).toHaveBeenCalledTimes(1));
    const request = vi.mocked(client.updateProject).mock.calls[0][0];
    expect(request.namespace).toBe("user-alice");
    expect(request.name).toBe("payments");
    expect(request.displayName).toBe("Payments API");
    expect(request.useSavedCredentials).toBe(true);
    expect(request.mcpServerRefs).toEqual(["github"]);
    expect(request.bugSquasher).toBeUndefined();
    expect(onUpdated).toHaveBeenCalledWith(updated);
    expect(await screen.findByText("Saved.")).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Save changes" })).toBeNull();
  });

  it("discards all pending edits at once", async () => {
    renderPanel();
    await waitFor(() => expect(client.listMyCredentials).toHaveBeenCalledTimes(1));
    fireEvent.change(screen.getByLabelText("Timeout"), { target: { value: "45m" } });
    fireEvent.click(screen.getByRole("button", { name: "Discard" }));
    expect((screen.getByLabelText("Timeout") as HTMLInputElement).value).toBe("");
    expect(screen.queryByRole("button", { name: "Save changes" })).toBeNull();
  });

  it("reports server errors inline and keeps the edits", async () => {
    renderPanel();
    await waitFor(() => expect(client.listMyCredentials).toHaveBeenCalledTimes(1));
    fireEvent.change(screen.getByLabelText("Timeout"), { target: { value: "45m" } });
    vi.mocked(client.updateProject).mockRejectedValueOnce(new Error("boom"));
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));
    expect(await screen.findByText(/boom/)).toBeTruthy();
    expect((screen.getByLabelText("Timeout") as HTMLInputElement).value).toBe("45m");
  });

  it("exposes privileged access to admins only", async () => {
    authUser.role = "admin";
    renderPanel();
    await waitFor(() => expect(client.listMyCredentials).toHaveBeenCalledTimes(1));
    expect(screen.getByRole("heading", { name: "Privileged access" })).toBeTruthy();
    fireEvent.click(screen.getByRole("switch", { name: "Kubernetes admin" }));
    expect(screen.getByText("Unsaved changes in Privileged access.")).toBeTruthy();
  });
});
