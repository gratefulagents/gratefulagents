import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";

import { CreateProjectDialog } from "@/components/CreateProjectDialog";
import { client } from "@/lib/client";

const { navigate } = vi.hoisted(() => ({ navigate: vi.fn() }));

vi.mock("react-router-dom", async () => {
  const actual = await vi.importActual<typeof import("react-router-dom")>("react-router-dom");
  return { ...actual, useNavigate: () => navigate };
});

vi.mock("@/lib/client", () => ({
  client: {
    listMyCredentials: vi.fn().mockResolvedValue({
      namespace: "user-alice",
      anthropicApiKeyPresent: true,
      openaiApiKeyPresent: true,
      openrouterApiKeyPresent: true,
      xaiApiKeyPresent: false,
      anthropicOauthPresent: false,
      openaiOauthPresent: false,
      copilotOauthPresent: false,
      githubTokenPresent: true,
      secrets: [],
    }),
    getMyModelDefaults: vi.fn().mockResolvedValue({ provider: "", model: "", reasoningLevel: "", disabled: false }),
    listAvailableModels: vi.fn().mockImplementation(({ provider }: { provider: string }) =>
      Promise.resolve({
        models:
          provider === "openrouter"
            ? ["z-ai/glm-4.7", "openai/gpt-5.4"]
            : provider === "anthropic"
              ? ["claude-sonnet-4-6"]
              : [],
      }),
    ),
    listRuntimeImages: vi.fn().mockResolvedValue({ images: [] }),
    listMCPServers: vi.fn().mockResolvedValue({ servers: [] }),
    listModeTemplates: vi.fn().mockResolvedValue({ templates: [] }),
    listSkills: vi.fn().mockResolvedValue({ skills: [] }),
    createGitHubRemoteRepository: vi.fn().mockResolvedValue({
      repoUrl: "https://github.com/alice/payments-api",
      defaultBranch: "trunk",
    }),
    createProject: vi.fn().mockResolvedValue({ namespace: "user-alice", name: "payments-api" }),
  },
}));

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

function openDialog() {
  render(
    <MemoryRouter>
      <CreateProjectDialog />
    </MemoryRouter>,
  );
  fireEvent.click(screen.getByRole("button", { name: "New project" }));
}

describe("CreateProjectDialog", () => {
  it("derives the name from the repository URL until the user types one", async () => {
    openDialog();
    await waitFor(() => expect(client.listMyCredentials).toHaveBeenCalledTimes(1));

    const url = screen.getByLabelText("Repository URL");
    const name = screen.getByLabelText(/^Name/) as HTMLInputElement;
    expect(name.value).toBe("");
    expect(screen.getByRole("button", { name: "Create project" }).hasAttribute("disabled")).toBe(true);

    fireEvent.change(url, { target: { value: "https://github.com/acme/Payments-API.git" } });
    expect(name.value).toBe("payments-api");
    expect(screen.getByText("user-alice/payments-api")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Create project" }).hasAttribute("disabled")).toBe(false);

    fireEvent.change(name, { target: { value: "billing" } });
    fireEvent.change(url, { target: { value: "https://github.com/acme/other" } });
    expect(name.value).toBe("billing");
  });

  it("keeps everything but the model receipt behind More options", async () => {
    openDialog();
    await waitFor(() => expect(client.listMyCredentials).toHaveBeenCalledTimes(1));

    expect(screen.getByRole("button", { name: /^Model/ })).toBeTruthy();
    expect(screen.queryByRole("button", { name: /^Runtime/ })).toBeNull();
    expect(screen.queryByLabelText("Timeout")).toBeNull();

    fireEvent.click(screen.getByRole("button", { name: /More options/ }));
    expect(screen.getByRole("button", { name: /^Repository/ })).toBeTruthy();
    expect(screen.getByRole("button", { name: /^Agent/ })).toBeTruthy();
    expect(screen.getByRole("button", { name: /^Runtime/ })).toBeTruthy();
    expect(screen.getByRole("button", { name: /^Tools/ })).toBeTruthy();
    expect(screen.queryByRole("button", { name: /More options/ })).toBeNull();
  });

  it("loads the live catalog for the chosen provider through saved credentials", async () => {
    openDialog();
    await waitFor(() => expect(client.listMyCredentials).toHaveBeenCalledTimes(1));
    fireEvent.click(screen.getByRole("button", { name: /^Model/ }));
    fireEvent.click(screen.getByRole("button", { name: "OpenRouter" }));

    await waitFor(() => {
      expect(client.listAvailableModels).toHaveBeenCalledWith(
        { namespace: "user-alice", provider: "openrouter", authMode: "api-key" },
        expect.anything(),
      );
    });
    expect(await screen.findByText("2 OpenRouter models available")).toBeTruthy();
    const input = screen.getByLabelText("Model");
    expect(input.getAttribute("list")).toBe("project-model-options");
    expect(document.querySelector('#project-model-options option[value="z-ai/glm-4.7"]')).toBeTruthy();
  });

  it("creates with derived defaults and navigates to the project", async () => {
    openDialog();
    await waitFor(() => expect(client.listMyCredentials).toHaveBeenCalledTimes(1));
    fireEvent.change(screen.getByLabelText("Repository URL"), {
      target: { value: "https://github.com/acme/payments-api" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Create project" }));

    await waitFor(() => expect(client.createProject).toHaveBeenCalledTimes(1));
    const request = vi.mocked(client.createProject).mock.calls[0][0];
    expect(request.name).toBe("payments-api");
    expect(request.displayName).toBe("payments-api");
    expect(request.repoUrl).toBe("https://github.com/acme/payments-api");
    expect(request.useSavedCredentials).toBe(true);
    expect(request.configureRuntimeProfile).toBe(true);
    expect(navigate).toHaveBeenCalledWith("/projects/user-alice/payments-api");
  });

  it("opens the model receipt when saved credentials cannot cover the provider", async () => {
    vi.mocked(client.listMyCredentials).mockResolvedValueOnce({
      namespace: "user-alice",
      anthropicApiKeyPresent: false,
      openaiApiKeyPresent: false,
      openrouterApiKeyPresent: false,
      xaiApiKeyPresent: false,
      anthropicOauthPresent: false,
      openaiOauthPresent: false,
      copilotOauthPresent: false,
      githubTokenPresent: false,
      secrets: [],
    } as never);
    openDialog();
    expect(await screen.findByText(/No saved Anthropic credential yet/)).toBeTruthy();
    expect(screen.getByRole("switch", { name: "Use my saved credentials" })).toBeTruthy();
  });
});

describe("new GitHub repositories", () => {
  async function fillNewRepository() {
    openDialog();
    await waitFor(() => expect(client.listMyCredentials).toHaveBeenCalledTimes(1));
    fireEvent.click(screen.getByRole("button", { name: "Create GitHub repository" }));
    fireEvent.change(screen.getByLabelText(/^Repository name/), { target: { value: "payments-api" } });
  }

  it.each([false, true])("creates with public=%s and uses the returned URL and branch", async (isPublic) => {
    await fillNewRepository();
    expect((screen.getByLabelText("Visibility") as HTMLSelectElement).value).toBe("private");
    if (isPublic) {
      fireEvent.change(screen.getByLabelText("Visibility"), { target: { value: "public" } });
      fireEvent.change(screen.getByLabelText("GitHub organization"), { target: { value: "acme" } });
    }
    fireEvent.click(screen.getByRole("button", { name: "Create project" }));
    await waitFor(() => expect(client.createProject).toHaveBeenCalledTimes(1));
    expect(client.createGitHubRemoteRepository).toHaveBeenCalledWith({
      name: "payments-api", organization: isPublic ? "acme" : "", public: isPublic,
    });
    expect(vi.mocked(client.createProject).mock.calls[0][0]).toMatchObject({
      name: "payments-api", repoUrl: "https://github.com/alice/payments-api", baseBranch: "trunk",
    });
  });

  it("does not create a project when GitHub rejects creation", async () => {
    vi.mocked(client.createGitHubRemoteRepository).mockRejectedValueOnce(new Error("Repository already exists"));
    await fillNewRepository();
    fireEvent.click(screen.getByRole("button", { name: "Create project" }));
    expect((await screen.findByRole("alert")).textContent).toContain("Repository already exists");
    expect(client.createProject).not.toHaveBeenCalled();
    expect((screen.getByLabelText(/^Repository name/) as HTMLInputElement).value).toBe("payments-api");
  });

  it("asks the user to check GitHub after an ambiguous network failure", async () => {
    vi.mocked(client.createGitHubRemoteRepository).mockRejectedValueOnce(new TypeError("Failed to fetch"));
    await fillNewRepository();
    fireEvent.click(screen.getByRole("button", { name: "Create project" }));
    expect((await screen.findByRole("alert")).textContent).toContain("Check GitHub before retrying");
    expect(client.createGitHubRemoteRepository).toHaveBeenCalledTimes(1);
    expect(client.createProject).not.toHaveBeenCalled();
  });

  it("retains the created repository and does not recreate it on project retry", async () => {
    vi.mocked(client.createProject).mockRejectedValueOnce(new Error("Project failed"));
    await fillNewRepository();
    fireEvent.click(screen.getByRole("button", { name: "Create project" }));
    await screen.findByRole("alert");
    expect(screen.getByRole("status").textContent).toContain("Created https://github.com/alice/payments-api");
    expect((screen.getByLabelText("Repository URL") as HTMLInputElement).value).toBe("https://github.com/alice/payments-api");
    fireEvent.click(screen.getByRole("button", { name: "Create project" }));
    await waitFor(() => expect(client.createProject).toHaveBeenCalledTimes(2));
    expect(client.createGitHubRemoteRepository).toHaveBeenCalledTimes(1);
  });

  it("disables creation without a saved GitHub token", async () => {
    vi.mocked(client.listMyCredentials).mockResolvedValueOnce({
      namespace: "user-alice", anthropicApiKeyPresent: true, githubTokenPresent: false, secrets: [],
    } as never);
    await fillNewRepository();
    expect(screen.getByRole("button", { name: "Create project" }).hasAttribute("disabled")).toBe(true);
    expect(screen.getByRole("alert").textContent).toContain("Save a GitHub token");
  });
});
