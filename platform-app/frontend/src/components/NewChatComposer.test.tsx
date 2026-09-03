import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { Code, ConnectError } from "@connectrpc/connect";

import { NewChatComposer } from "./NewChatComposer";
import { client } from "@/lib/client";

const navigate = vi.fn();
const watched = vi.hoisted(() => ({
  projects: [] as Array<{
    namespace: string;
    name: string;
    displayName: string;
    runtimeProfileRef?: string;
  }>,
  loading: false,
  error: null as string | null,
}));
const credentialHook = vi.hoisted(() => ({ namespace: "alice-123", loading: false }));

vi.mock("react-router-dom", async (load) => {
  const actual = await load<typeof import("react-router-dom")>();
  return { ...actual, useNavigate: () => navigate };
});
vi.mock("@/hooks/useWatchedList", () => ({
  useProjects: () => watched,
}));
vi.mock("@/hooks/useMyCredentials", () => ({
  useMyCredentials: () => ({
    presence: { namespace: credentialHook.namespace },
    loading: credentialHook.loading,
    error: null,
  }),
}));
vi.mock("@/lib/client", () => ({
  client: {
    listMyCredentials: vi.fn(),
    listAvailableModels: vi.fn(),
    listProjects: vi.fn(),
    createProject: vi.fn(),
    createAgentRun: vi.fn(),
  },
}));

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
  localStorage.clear();
  watched.projects = [];
  watched.loading = false;
  watched.error = null;
  credentialHook.namespace = "alice-123";
  credentialHook.loading = false;
});

describe("NewChatComposer", () => {
  it("starts a chat in an existing project", async () => {
    watched.projects = [{ namespace: "team", name: "briefs", displayName: "Briefs" }];
    vi.mocked(client.createAgentRun).mockResolvedValue({ namespace: "team", name: "run-1" } as never);

    render(<MemoryRouter><NewChatComposer /></MemoryRouter>);
    fireEvent.change(screen.getByPlaceholderText("Describe a task, or ask anything…"), {
      target: { value: "Summarize our launch notes" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Start" }));

    await waitFor(() => expect(client.createAgentRun).toHaveBeenCalledWith({
      namespace: "team",
      userRequest: "Summarize our launch notes",
      model: "",
      source: { kind: "Project", name: "briefs" },
      imageDataUrls: [],
      videoDataUrls: [],
    }));
    expect(client.createProject).not.toHaveBeenCalled();
    expect(navigate).toHaveBeenCalledWith("/runs/team/run-1");
  });

  it("starts an image-only chat from an attached file", async () => {
    watched.projects = [{ namespace: "team", name: "briefs", displayName: "Briefs" }];
    vi.mocked(client.createAgentRun).mockResolvedValue({ namespace: "team", name: "run-image" } as never);

    render(<MemoryRouter><NewChatComposer /></MemoryRouter>);
    const image = new File([new Uint8Array([1, 2, 3])], "diagram.png", { type: "image/png" });
    fireEvent.change(screen.getByLabelText("Choose images or video"), { target: { files: [image] } });

    expect(await screen.findByAltText("diagram.png")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Start" }));

    await waitFor(() => expect(client.createAgentRun).toHaveBeenCalledWith({
      namespace: "team",
      userRequest: "",
      model: "",
      source: { kind: "Project", name: "briefs" },
      imageDataUrls: ["data:image/png;base64,AQID"],
      videoDataUrls: [],
    }));
    expect(navigate).toHaveBeenCalledWith("/runs/team/run-image");
  });

  it("starts a video-only chat from an attached file", async () => {
    watched.projects = [{ namespace: "team", name: "briefs", displayName: "Briefs" }];
    vi.mocked(client.createAgentRun).mockResolvedValue({ namespace: "team", name: "run-video" } as never);

    render(<MemoryRouter><NewChatComposer /></MemoryRouter>);
    const video = new File([new Uint8Array([1, 2, 3])], "walkthrough.mp4", { type: "video/mp4" });
    fireEvent.change(screen.getByLabelText("Choose images or video"), { target: { files: [video] } });

    expect(await screen.findByLabelText("walkthrough.mp4")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Start" }));

    await waitFor(() => expect(client.createAgentRun).toHaveBeenCalledWith({
      namespace: "team",
      userRequest: "",
      model: "",
      source: { kind: "Project", name: "briefs" },
      imageDataUrls: [],
      videoDataUrls: ["data:video/mp4;base64,AQID"],
    }));
    expect(navigate).toHaveBeenCalledWith("/runs/team/run-video");
  });

  it("accepts pasted images and lets the user remove them", async () => {
    watched.projects = [{ namespace: "team", name: "briefs", displayName: "Briefs" }];

    render(<MemoryRouter><NewChatComposer /></MemoryRouter>);
    const image = new File([new Uint8Array([1, 2, 3])], "pasted.png", { type: "image/png" });
    fireEvent.paste(screen.getByPlaceholderText("Describe a task, or ask anything…"), {
      clipboardData: {
        items: [{ kind: "file", type: "image/png", getAsFile: () => image }],
      },
    });

    expect(await screen.findByAltText("pasted.png")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Remove pasted.png" }));
    expect(screen.queryByAltText("pasted.png")).toBeNull();
    expect(screen.getByRole("button", { name: "Start" })).toHaveProperty("disabled", true);
  });

  it("provisions a repository-free personal workspace for a first chat", async () => {
    vi.mocked(client.listMyCredentials).mockResolvedValue({
      namespace: "alice-123",
      anthropicApiKeyPresent: true,
    } as never);
    vi.mocked(client.createProject).mockResolvedValue({
      namespace: "alice-123",
      name: "personal-workspace",
      displayName: "Personal workspace",
      runtimeProfileRef: "personal-workspace-runtime",
    } as never);
    vi.mocked(client.createAgentRun).mockResolvedValue({ namespace: "alice-123", name: "run-1" } as never);

    render(<MemoryRouter><NewChatComposer /></MemoryRouter>);
    const input = screen.getByPlaceholderText("Describe a task, or ask anything…");
    expect(input).not.toHaveProperty("disabled", true);
    fireEvent.change(input, { target: { value: "Draft a client update" } });
    fireEvent.click(screen.getByRole("button", { name: "Start" }));

    await waitFor(() => expect(client.createProject).toHaveBeenCalledWith(expect.objectContaining({
      name: "personal-workspace",
      displayName: "Personal workspace",
      provider: "anthropic",
      authMode: "api-key",
      useSavedCredentials: true,
    })));
    expect(vi.mocked(client.createProject).mock.calls[0][0].repoUrl ?? "").toBe("");
    expect(client.createProject).toHaveBeenNthCalledWith(2, expect.objectContaining({
      name: "gratefulagents",
      displayName: "Grateful Agents",
      repoUrl: "https://github.com/gratefulagents/gratefulagents.git",
      additionalRepoUrls: ["https://github.com/gratefulagents/sdk.git"],
      baseBranch: "main",
      provider: "anthropic",
      authMode: "api-key",
      useSavedCredentials: true,
      runtimeProfileRef: "personal-workspace-runtime",
      reviewLoopDisabled: true,
    }));
    expect(client.createAgentRun).toHaveBeenCalledWith({
      namespace: "alice-123",
      userRequest: "Draft a client update",
      model: "",
      source: { kind: "Project", name: "personal-workspace" },
      imageDataUrls: [],
      videoDataUrls: [],
    });
    expect(navigate).toHaveBeenCalledWith("/runs/alice-123/run-1");
  });

  it("backfills the Grateful Agents project beside an existing personal workspace", async () => {
    watched.projects = [{
      namespace: "alice-123",
      name: "personal-workspace",
      displayName: "Personal workspace",
      runtimeProfileRef: "personal-workspace-runtime",
    }];
    vi.mocked(client.listMyCredentials).mockResolvedValue({
      namespace: "alice-123",
      openaiOauthPresent: true,
    } as never);
    vi.mocked(client.createProject).mockResolvedValue({
      namespace: "alice-123",
      name: "gratefulagents",
    } as never);
    vi.mocked(client.createAgentRun).mockResolvedValue({ namespace: "alice-123", name: "run-1" } as never);

    render(<MemoryRouter><NewChatComposer /></MemoryRouter>);
    fireEvent.change(screen.getByPlaceholderText("Describe a task, or ask anything…"), {
      target: { value: "The install button is broken" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Start" }));

    await waitFor(() => expect(client.createProject).toHaveBeenCalledTimes(1));
    expect(client.createProject).toHaveBeenCalledWith(expect.objectContaining({
      name: "gratefulagents",
      repoUrl: "https://github.com/gratefulagents/gratefulagents.git",
      additionalRepoUrls: ["https://github.com/gratefulagents/sdk.git"],
      provider: "openai",
      authMode: "oauth",
      runtimeProfileRef: "personal-workspace-runtime",
    }));
    const bootstrapRequest = vi.mocked(client.createProject).mock.calls[0][0];
    expect(bootstrapRequest.configureRuntimeProfile).toBeUndefined();
    expect(bootstrapRequest.permissionMode).toBeUndefined();
    expect(bootstrapRequest.egressMode).toBeUndefined();
    expect(client.createAgentRun).toHaveBeenCalledWith(expect.objectContaining({
      source: { kind: "Project", name: "personal-workspace" },
    }));
  });

  it("does not bootstrap from a personal workspace in another namespace", async () => {
    watched.projects = [{
      namespace: "shared-team",
      name: "personal-workspace",
      displayName: "Shared personal workspace",
      runtimeProfileRef: "shared-runtime",
    }];
    vi.mocked(client.createAgentRun).mockResolvedValue({ namespace: "shared-team", name: "run-1" } as never);

    render(<MemoryRouter><NewChatComposer /></MemoryRouter>);
    fireEvent.change(screen.getByPlaceholderText("Describe a task, or ask anything…"), {
      target: { value: "Summarize this shared project" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Start" }));

    await waitFor(() => expect(client.createAgentRun).toHaveBeenCalled());
    expect(client.createProject).not.toHaveBeenCalled();
    expect(client.listMyCredentials).not.toHaveBeenCalled();
  });

  it("does not recreate an existing Grateful Agents project", async () => {
    watched.projects = [
      { namespace: "alice-123", name: "gratefulagents", displayName: "Grateful Agents" },
      {
        namespace: "alice-123",
        name: "personal-workspace",
        displayName: "Personal workspace",
        runtimeProfileRef: "personal-workspace-runtime",
      },
    ];
    vi.mocked(client.createAgentRun).mockResolvedValue({ namespace: "alice-123", name: "run-1" } as never);

    render(<MemoryRouter><NewChatComposer /></MemoryRouter>);
    fireEvent.change(screen.getByPlaceholderText("Describe a task, or ask anything…"), {
      target: { value: "Continue working" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Start" }));

    await waitFor(() => expect(client.createAgentRun).toHaveBeenCalled());
    expect(client.createProject).not.toHaveBeenCalled();
    expect(client.listMyCredentials).not.toHaveBeenCalled();
    expect(client.createAgentRun).toHaveBeenCalledWith(expect.objectContaining({
      source: { kind: "Project", name: "personal-workspace" },
    }));
  });

  it("selects a model before provisioning providers without platform defaults", async () => {
    vi.mocked(client.listMyCredentials).mockResolvedValue({
      namespace: "alice-123",
      openrouterApiKeyPresent: true,
    } as never);
    vi.mocked(client.listAvailableModels).mockResolvedValue({ models: ["openai/gpt-4.1"] } as never);
    vi.mocked(client.createProject).mockResolvedValue({
      namespace: "alice-123",
      name: "personal-workspace",
    } as never);
    vi.mocked(client.createAgentRun).mockResolvedValue({ namespace: "alice-123", name: "run-model" } as never);

    render(<MemoryRouter><NewChatComposer /></MemoryRouter>);
    fireEvent.change(screen.getByPlaceholderText("Describe a task, or ask anything…"), {
      target: { value: "Research a market" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Start" }));

    await waitFor(() => expect(client.createProject).toHaveBeenCalledWith(expect.objectContaining({
      provider: "openrouter",
      model: "openai/gpt-4.1",
    })));
  });

  it("recovers when another client already created the personal workspace", async () => {
    const existing = {
      namespace: "alice-123",
      name: "personal-workspace",
      displayName: "Personal workspace",
    };
    vi.mocked(client.listMyCredentials).mockResolvedValue({
      namespace: "alice-123",
      openaiOauthPresent: true,
    } as never);
    vi.mocked(client.createProject).mockRejectedValue(
      new ConnectError("already exists", Code.AlreadyExists),
    );
    vi.mocked(client.listProjects).mockResolvedValue({ projects: [existing] } as never);
    vi.mocked(client.createAgentRun).mockResolvedValue({ namespace: "alice-123", name: "run-2" } as never);

    render(<MemoryRouter><NewChatComposer /></MemoryRouter>);
    fireEvent.change(screen.getByPlaceholderText("Describe a task, or ask anything…"), {
      target: { value: "Continue in my workspace" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Start" }));

    await waitFor(() => expect(client.createAgentRun).toHaveBeenCalledWith(expect.objectContaining({
      namespace: "alice-123",
      source: { kind: "Project", name: "personal-workspace" },
    })));
    expect(client.createProject).toHaveBeenCalledWith(expect.objectContaining({
      provider: "openai",
      authMode: "oauth",
    }));
  });

  it("explains that a provider connection is required", async () => {
    vi.mocked(client.listMyCredentials).mockResolvedValue({ namespace: "alice-123" } as never);

    render(<MemoryRouter><NewChatComposer /></MemoryRouter>);
    fireEvent.change(screen.getByPlaceholderText("Describe a task, or ask anything…"), {
      target: { value: "Help me plan a workshop" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Start" }));

    expect(await screen.findByText("Connect a model provider in Settings to start chatting.")).toBeTruthy();
    expect(client.createProject).not.toHaveBeenCalled();
    expect(client.createAgentRun).not.toHaveBeenCalled();
  });

  it("keeps run options one popover away and sends overrides only when set", async () => {
    watched.projects = [{
      namespace: "team",
      name: "briefs",
      displayName: "Briefs",
      provider: "anthropic",
      model: "claude-sonnet",
      repoUrl: "https://github.com/acme/repo",
      baseBranch: "main",
      additionalRepoUrls: [],
    } as never];
    vi.mocked(client.listAvailableModels).mockResolvedValue({ models: ["claude-sonnet", "claude-opus"] } as never);
    vi.mocked(client.createAgentRun).mockResolvedValue({ namespace: "team", name: "run-2" } as never);

    render(<MemoryRouter><NewChatComposer /></MemoryRouter>);
    fireEvent.click(screen.getByRole("button", { name: "Options" }));

    // Receipts read the project's defaults so the popover answers "what will this run use?".
    expect(await screen.findByText("Run options")).toBeTruthy();
    expect(screen.getByRole("button", { name: /^Model/ }).textContent).toContain("claude-sonnet");
    expect(screen.getByRole("button", { name: /^Repository/ }).textContent).toContain("acme/repo @ main");
    await waitFor(() => expect(client.listAvailableModels).toHaveBeenCalledWith(
      { namespace: "team", source: { kind: "Project", name: "briefs" }, provider: "anthropic" },
      expect.anything(),
    ));

    fireEvent.click(screen.getByRole("button", { name: /^Model/ }));
    fireEvent.change(screen.getByLabelText("Model"), { target: { value: "claude-opus" } });
    fireEvent.click(screen.getByRole("button", { name: /^Repository/ }));
    fireEvent.change(screen.getByLabelText("Base branch"), { target: { value: "feature" } });

    fireEvent.click(screen.getByRole("button", { name: /^Overseer/ }));
    fireEvent.click(screen.getByRole("switch", { name: "Enable overseer" }));
    fireEvent.change(screen.getByLabelText("Mode name"), { target: { value: "review" } });
    fireEvent.change(screen.getByLabelText("Interval (minutes)"), { target: { value: "30" } });
    fireEvent.change(screen.getByLabelText("Max interventions"), { target: { value: "0" } });

    fireEvent.keyDown(document.activeElement ?? document.body, { key: "Escape" });
    expect(await screen.findByRole("button", { name: "Options (3 overrides)" })).toBeTruthy();

    fireEvent.change(screen.getByPlaceholderText("Describe a task, or ask anything…"), {
      target: { value: "Ship it" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Start" }));

    await waitFor(() => expect(client.createAgentRun).toHaveBeenCalledTimes(1));
    const request = vi.mocked(client.createAgentRun).mock.calls[0][0];
    expect(request).toMatchObject({
      namespace: "team",
      userRequest: "Ship it",
      model: "anthropic/claude-opus",
      baseBranch: "feature",
      source: { kind: "Project", name: "briefs" },
      overseer: expect.objectContaining({ modeRefName: "review", intervalMinutes: 30, maxInterventions: 0 }),
    });
    // Untouched repository fields stay inherited rather than echoing cached values.
    expect(request.repoUrl).toBeUndefined();
    expect(request.additionalRepoUrls).toBeUndefined();
  });

  it("blocks an invalid overseer before submitting", async () => {
    watched.projects = [{ namespace: "team", name: "briefs", displayName: "Briefs" }];
    render(<MemoryRouter><NewChatComposer /></MemoryRouter>);
    fireEvent.click(screen.getByRole("button", { name: "Options" }));
    fireEvent.click(await screen.findByRole("button", { name: /^Overseer/ }));
    fireEvent.click(screen.getByRole("switch", { name: "Enable overseer" }));
    fireEvent.change(screen.getByLabelText("Interval (minutes)"), { target: { value: "0" } });
    fireEvent.keyDown(document.activeElement ?? document.body, { key: "Escape" });

    fireEvent.change(screen.getByPlaceholderText("Describe a task, or ask anything…"), {
      target: { value: "Ship it" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Start" }));
    expect(await screen.findByText(/Overseer interval/)).toBeTruthy();
    expect(client.createAgentRun).not.toHaveBeenCalled();
  });

  it("pins the project and hides the picker when embedded on a project page", () => {
    watched.projects = [
      { namespace: "team", name: "briefs", displayName: "Briefs" },
      { namespace: "team", name: "other", displayName: "Other" },
    ];
    render(
      <MemoryRouter>
        <NewChatComposer fixedNamespace="team" fixedProject="briefs" variant="compact" placeholder="Start a run in Briefs…" />
      </MemoryRouter>,
    );
    expect(screen.getByPlaceholderText("Start a run in Briefs…")).toBeTruthy();
    expect(screen.queryByRole("button", { name: /Briefs/ })).toBeNull();
    expect(screen.getByRole("button", { name: "Options" })).toBeTruthy();
  });
});
