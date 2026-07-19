import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";

import { OnboardingWizard } from "@/components/onboarding/OnboardingWizard";
import { client } from "@/lib/client";
import { onboardingDismissed } from "@/lib/onboarding";

vi.mock("@/lib/client", () => ({
  client: {
    listMyCredentials: vi.fn(),
    updateMyCredentials: vi.fn(),
    createProject: vi.fn(),
    listAvailableModels: vi.fn().mockResolvedValue({ models: [] }),
    listRuntimeImages: vi.fn().mockResolvedValue({ images: [] }),
    getMyGitIdentity: vi.fn(),
    updateMyGitIdentity: vi.fn(),
  },
}));

vi.mock("@/contexts/AuthContext", () => ({
  useAuth: () => ({
    user: {
      id: "u1",
      role: "member",
      name: "Dana Ops",
      username: "dana",
      email: "dana@example.com",
    },
  }),
}));

vi.mock("@/hooks/useWatchedList", () => ({
  useProjects: () => ({ projects: [], loading: false }),
}));

function serverCreds(overrides: Record<string, unknown> = {}) {
  return {
    namespace: "dana-x",
    anthropicApiKeyPresent: false,
    openaiApiKeyPresent: false,
    openrouterApiKeyPresent: false,
    anthropicOauthPresent: false,
    openaiOauthPresent: false,
    copilotOauthPresent: false,
    githubTokenPresent: false,
    ...overrides,
  };
}

beforeEach(() => {
  vi.mocked(client.getMyGitIdentity).mockResolvedValue({ name: "", email: "" } as never);
  vi.mocked(client.listRuntimeImages).mockResolvedValue({ images: [] } as never);
});

afterEach(() => {
  cleanup();
  localStorage.clear();
  vi.mocked(client.listMyCredentials).mockReset();
  vi.mocked(client.updateMyCredentials).mockReset();
  vi.mocked(client.createProject).mockReset();
  vi.mocked(client.listAvailableModels).mockReset();
  vi.mocked(client.listRuntimeImages).mockReset();
  vi.mocked(client.getMyGitIdentity).mockReset();
  vi.mocked(client.updateMyGitIdentity).mockReset();
});

function renderWizard(entry = "/welcome") {
  return render(
    <MemoryRouter initialEntries={[entry]}>
      <Routes>
        <Route path="/welcome" element={<OnboardingWizard />} />
        <Route path="/" element={<div>home-screen</div>} />
        <Route path="/projects/:namespace/:name" element={<div>project-page</div>} />
      </Routes>
    </MemoryRouter>,
  );
}

describe("OnboardingWizard", () => {
  it("walks provider → GitHub → git identity → project and lands on Home", async () => {
    vi.mocked(client.listMyCredentials).mockResolvedValue(serverCreds() as never);
    vi.mocked(client.listAvailableModels).mockResolvedValue({ models: ["z-ai/glm-4.7"] } as never);
    vi.mocked(client.updateMyCredentials)
      .mockResolvedValueOnce(serverCreds({ openrouterApiKeyPresent: true }) as never)
      .mockResolvedValueOnce(
        serverCreds({ openrouterApiKeyPresent: true, githubTokenPresent: true }) as never,
      );
    vi.mocked(client.updateMyGitIdentity).mockResolvedValue({
      name: "Dana Ops",
      email: "dana@example.com",
    } as never);
    vi.mocked(client.createProject).mockResolvedValue({
      namespace: "dana-x",
      name: "widget",
      displayName: "widget",
    } as never);

    renderWizard();
    await screen.findByText("Connect a model provider");
    // Nothing connected yet: the primary action offers skipping.
    expect(screen.getByRole("button", { name: /Skip for now/ })).toBeTruthy();

    fireEvent.change(screen.getByPlaceholderText("sk-or-v1-..."), {
      target: { value: "openrouter-test-key" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Save API key" }));
    await screen.findByText("API key saved");
    expect(client.updateMyCredentials).toHaveBeenCalledWith({
      anthropicApiKey: "",
      openaiApiKey: "",
      openrouterApiKey: "openrouter-test-key",
    });

    // Step satisfied: continue to GitHub.
    fireEvent.click(await screen.findByRole("button", { name: /Continue/ }));
    await screen.findByText("Connect GitHub");
    fireEvent.change(screen.getByPlaceholderText("ghp_... / github_pat_..."), {
      target: { value: "sample-github-token" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Save token" }));
    await screen.findByText("GitHub token saved");
    expect(client.updateMyCredentials).toHaveBeenLastCalledWith({ githubToken: "sample-github-token" });

    fireEvent.click(screen.getByRole("button", { name: /Continue/ }));
    await screen.findByText("Set your git identity");
    expect((screen.getByLabelText("Commit author name") as HTMLInputElement).value).toBe("Dana Ops");
    expect((screen.getByLabelText("Commit author email") as HTMLInputElement).value).toBe(
      "dana@example.com",
    );
    expect(screen.getByText("Dana Ops <dana@example.com>")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Save git identity" }));
    await screen.findByText("Git identity saved");
    expect(client.updateMyGitIdentity).toHaveBeenCalledWith({
      name: "Dana Ops",
      email: "dana@example.com",
    });

    fireEvent.click(screen.getByRole("button", { name: /Continue/ }));
    await screen.findByText("Create your first project");
    fireEvent.change(screen.getByPlaceholderText("https://github.com/you/repo"), {
      target: { value: "https://github.com/acme/Widget.git" },
    });
    fireEvent.change(screen.getByLabelText("Timeout"), {
      target: { value: "45m" },
    });
    fireEvent.change(await screen.findByPlaceholderText("registry/repo:tag"), {
      target: { value: "ghcr.io/acme/agent:latest" },
    });
    await screen.findByText(
      (_, el) => el?.tagName === "CODE" && el.textContent === "Creates dana-x/widget",
    );
    await screen.findByText("1 OpenRouter model available");
    expect(client.listAvailableModels).toHaveBeenCalledWith(
      { namespace: "dana-x", provider: "openrouter", authMode: "api-key" },
      expect.anything(),
    );

    // OpenRouter has no server-side default, so project creation requires an
    // explicit model from the live catalog (or a manually entered model ID).
    fireEvent.click(screen.getByRole("button", { name: "Create project" }));
    await screen.findByText("Choose a model for this project.");
    expect(client.createProject).not.toHaveBeenCalled();
    fireEvent.change(screen.getByLabelText("Model"), {
      target: { value: "z-ai/glm-4.7" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Create project" }));

    await screen.findByText("You're all set");
    expect(client.createProject).toHaveBeenCalledTimes(1);
    const request = vi.mocked(client.createProject).mock.calls[0][0] as Record<string, unknown>;
    expect(request.name).toBe("widget");
    expect(request.displayName).toBe("widget");
    expect(request.repoUrl).toBe("https://github.com/acme/Widget.git");
    expect(request.image).toBe("ghcr.io/acme/agent:latest");
    expect(request.timeout).toBe("45m");
    expect(request.provider).toBe("openrouter");
    expect(request.model).toBe("z-ai/glm-4.7");
    expect(request.authMode).toBe("api-key");
    expect(request.useSavedCredentials).toBe(true);

    fireEvent.click(screen.getByRole("button", { name: "Start chatting" }));
    await screen.findByText("home-screen");
    expect(onboardingDismissed("u1")).toBe(true);
  });

  it("loads models dynamically for the selected provider", async () => {
    vi.mocked(client.listMyCredentials).mockResolvedValue(
      serverCreds({ anthropicApiKeyPresent: true, openaiApiKeyPresent: true }) as never,
    );
    vi.mocked(client.listAvailableModels).mockImplementation(({ provider }) =>
      Promise.resolve({
        models: provider === "anthropic" ? ["claude-opus-4-6"] : ["gpt-5", "gpt-5-mini"],
      } as never),
    );

    renderWizard("/welcome?step=3");
    await screen.findByText("1 Anthropic · Claude model available");
    expect(client.listAvailableModels).toHaveBeenCalledWith(
      { namespace: "dana-x", provider: "anthropic", authMode: "api-key" },
      expect.anything(),
    );

    fireEvent.change(screen.getByLabelText("Model"), {
      target: { value: "claude-opus-4-6" },
    });
    fireEvent.click(screen.getByRole("button", { name: "OpenAI · GPT" }));
    expect((screen.getByLabelText("Model") as HTMLInputElement).value).toBe("");
    await screen.findByText("2 OpenAI · GPT models available");
    expect(client.listAvailableModels).toHaveBeenLastCalledWith(
      { namespace: "dana-x", provider: "openai", authMode: "api-key" },
      expect.anything(),
    );
    expect(
      document.querySelector('#onboarding-project-model-options option[value="gpt-5"]'),
    ).toBeTruthy();
  });

  it("blocks project creation until a provider is connected", async () => {
    vi.mocked(client.listMyCredentials).mockResolvedValue(serverCreds() as never);
    renderWizard("/welcome?step=3");

    await screen.findByText("Create your first project");
    expect(screen.getByText(/No model provider connected yet/)).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Create project" })).toBeNull();

    // The inline shortcut jumps back to step 1.
    fireEvent.click(screen.getByRole("button", { name: "Connect one in step 1" }));
    await screen.findByText("Connect a model provider");
  });

  it("marks edits to a saved identity as unsaved until they are persisted", async () => {
    vi.mocked(client.listMyCredentials).mockResolvedValue(serverCreds() as never);
    vi.mocked(client.getMyGitIdentity).mockResolvedValue({
      name: "Dana Ops",
      email: "dana@users.noreply.github.com",
    } as never);
    vi.mocked(client.updateMyGitIdentity).mockResolvedValue({
      name: "Dana Operator",
      email: "dana@users.noreply.github.com",
    } as never);

    renderWizard("/welcome?step=git-identity");
    const name = (await screen.findByDisplayValue("Dana Ops")) as HTMLInputElement;
    expect(screen.getByRole("button", { name: /Continue/ })).toBeTruthy();

    fireEvent.change(name, { target: { value: "Dana Operator" } });
    expect(screen.getByText("Unsaved changes")).toBeTruthy();
    expect(screen.getByRole("button", { name: /Skip for now/ })).toBeTruthy();

    fireEvent.click(screen.getByRole("button", { name: "Save git identity" }));
    await screen.findByText("Git identity saved");
    expect(screen.getByRole("button", { name: /Continue/ })).toBeTruthy();
  });

  it("keeps identity fields locked after a load failure and supports retry", async () => {
    vi.mocked(client.listMyCredentials).mockResolvedValue(serverCreds() as never);
    vi.mocked(client.getMyGitIdentity)
      .mockReset()
      .mockRejectedValueOnce(new Error("identity unavailable"))
      .mockResolvedValueOnce({
        name: "Dana Ops",
        email: "dana@users.noreply.github.com",
      } as never);

    renderWizard("/welcome?step=git-identity");
    await screen.findByText("identity unavailable");
    expect((screen.getByLabelText("Commit author name") as HTMLInputElement).disabled).toBe(true);
    expect((screen.getByRole("button", { name: "Save git identity" }) as HTMLButtonElement).disabled).toBe(
      true,
    );

    fireEvent.click(screen.getByRole("button", { name: "Retry" }));
    const name = (await screen.findByDisplayValue("Dana Ops")) as HTMLInputElement;
    expect(name.disabled).toBe(false);
    expect(client.getMyGitIdentity).toHaveBeenCalledTimes(2);
  });

  it("skip setup exits to Home and never re-offers on this device", async () => {
    vi.mocked(client.listMyCredentials).mockResolvedValue(serverCreds() as never);
    renderWizard();

    await screen.findByText("Connect a model provider");
    fireEvent.click(screen.getByRole("button", { name: "Skip setup" }));
    await screen.findByText("home-screen");
    expect(onboardingDismissed("u1")).toBe(true);
  });

  it("deep-links to the GitHub step via its original numeric key", async () => {
    vi.mocked(client.listMyCredentials).mockResolvedValue(serverCreds() as never);
    renderWizard("/welcome?step=2");
    await screen.findByText("Connect GitHub");
  });

  it("deep-links to git identity with a stable semantic key", async () => {
    vi.mocked(client.listMyCredentials).mockResolvedValue(serverCreds() as never);
    renderWizard("/welcome?step=git-identity");
    await screen.findByText("Set your git identity");
  });
});
