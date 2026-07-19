import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";

import { SlackAgentsPage } from "@/components/SlackAgentSection";
import { SlackAgentCreateDialog } from "@/components/SlackAgentCreateDialog";
import { SlackAgentDetail } from "@/components/SlackAgentDetail";
import { client } from "@/lib/client";

const agent = {
  configured: true,
  namespace: "demo",
  name: "ops-agent",
  botTokenPresent: true,
  userTokenPresent: false,
  appTokenPresent: true,
  githubTokenPresent: true,
  slackUserId: "U0DANA",
  channelReplyMode: "require-approval",
  commanders: ["U0RILEY"],
  appHomeHeader: "",
  appHomeText: "",
  mcpServerRefs: [],
  skillRefs: [],
  sessionIdleMinutes: 240,
  suspended: false,
  model: "claude-sonnet-4-6",
  provider: "anthropic",
  teamId: "T0ACME",
  botUserId: "B0OPS",
  ready: true,
  tokenValid: true,
  connected: true,
  lastError: "",
  runtimeProfileRef: "",
  permissionMode: "workspace-write",
  egressMode: "unrestricted",
  mcpPolicyRef: "",
  mcpPolicyDefaultAction: "Deny",
  mcpPolicyAllowedServers: [],
  workspaceRefName: "",
  workspaceRefNamespace: "",
  image: "",
};

vi.mock("@/lib/client", () => ({
  client: {
    listSlackAgents: vi.fn(),
    listSlackWorkspaces: vi.fn().mockResolvedValue({ workspaces: [] }),
    listSlackDrafts: vi.fn().mockResolvedValue({ drafts: [] }),
    listAvailableModels: vi.fn().mockResolvedValue({ models: [] }),
    listMCPServers: vi.fn().mockResolvedValue({ servers: [] }),
    listSkills: vi.fn().mockResolvedValue({ skills: [] }),
    listRuntimeImages: vi.fn().mockResolvedValue({ images: [] }),
    updateSlackAgent: vi.fn().mockResolvedValue({ namespace: "demo", name: "ops-agent" }),
    deleteSlackAgent: vi.fn(),
  },
}));

vi.mock("@/hooks/useAgentRuns", () => ({
  useAgentRuns: () => ({ runs: [], loading: false }),
}));

const mocked = vi.mocked(client);

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

describe("SlackAgentsPage", () => {
  it("lists agents as rows linking to the agent page", async () => {
    mocked.listSlackAgents.mockResolvedValue({ namespace: "demo", agents: [agent] } as never);

    render(
      <MemoryRouter>
        <SlackAgentsPage />
      </MemoryRouter>,
    );

    const link = await screen.findByRole("link", { name: /ops-agent/ });
    expect(link.getAttribute("href")).toBe("/slack/demo/ops-agent");
    expect(screen.getByText("Connected")).toBeTruthy();
    expect(screen.getByText("dedicated app")).toBeTruthy();
    expect(screen.getByText("T0ACME · B0OPS")).toBeTruthy();
    // Shared workspace apps stay a quiet, collapsed section.
    expect(screen.getByRole("button", { name: /Shared workspace apps/ })).toBeTruthy();
    expect(screen.queryByLabelText("Workspace app name")).toBeNull();
  });

  it("shows an onboarding empty state with the create action", async () => {
    mocked.listSlackAgents.mockResolvedValue({ namespace: "demo", agents: [] } as never);

    render(
      <MemoryRouter>
        <SlackAgentsPage />
      </MemoryRouter>,
    );

    expect(await screen.findByText("No Slack agents yet")).toBeTruthy();
    expect(screen.getAllByRole("button", { name: /New agent/ }).length).toBeGreaterThan(0);
  });
});

describe("SlackAgentCreateDialog", () => {
  it("refreshes models when the authentication mode changes", async () => {
    render(
      <SlackAgentCreateDialog
        namespace="demo"
        workspaces={[]}
        trigger={<button>New Slack agent</button>}
      />,
    );

    fireEvent.click(screen.getByRole("button", { name: "New Slack agent" }));
    await waitFor(() => {
      expect(mocked.listAvailableModels).toHaveBeenCalledWith(
        { namespace: "demo", provider: "anthropic", authMode: "api-key" },
        expect.anything(),
      );
    });

    const authGroup = screen.getByRole("group", { name: "Auth mode" });
    const oauthButton = Array.from(authGroup.querySelectorAll("button")).find(
      (button) => button.textContent === "oauth",
    );
    expect(oauthButton).toBeTruthy();
    fireEvent.click(oauthButton as HTMLButtonElement);

    await waitFor(() => {
      expect(mocked.listAvailableModels).toHaveBeenCalledWith(
        { namespace: "demo", provider: "anthropic", authMode: "oauth" },
        expect.anything(),
      );
    });
  });

  it("creates a managed runtime profile by default", async () => {
    render(
      <SlackAgentCreateDialog
        namespace="demo"
        workspaces={[]}
        trigger={<button>New Slack agent</button>}
      />,
    );

    fireEvent.click(screen.getByRole("button", { name: "New Slack agent" }));
    fireEvent.change(screen.getByLabelText(/Agent name/), {
      target: { value: "ops-agent" },
    });
    fireEvent.submit(document.querySelector("form") as HTMLFormElement);

    await waitFor(() => expect(mocked.updateSlackAgent).toHaveBeenCalledTimes(1));
    expect(mocked.updateSlackAgent.mock.calls[0][0].configureRuntimeProfile).toBe(true);
  });
});

describe("SlackAgentDetail settings tab", () => {
  function renderDetail(entry = "/slack/demo/ops-agent?tab=settings") {
    render(
      <MemoryRouter initialEntries={[entry]}>
        <Routes>
          <Route path="/slack/:namespace/:name" element={<SlackAgentDetail />} />
        </Routes>
      </MemoryRouter>,
    );
  }

  it("sends the whole config on save, keeping stored tokens by default", async () => {
    mocked.listSlackAgents.mockResolvedValue({ namespace: "demo", agents: [agent] } as never);

    renderDetail();

    await waitFor(() => {
      expect(mocked.listAvailableModels).toHaveBeenCalledWith(
        { namespace: "demo", provider: "anthropic", authMode: "api-key" },
        expect.anything(),
      );
    });
    fireEvent.click(await screen.findByRole("button", { name: "Save changes" }));

    await waitFor(() => expect(mocked.updateSlackAgent).toHaveBeenCalled());
    const req = mocked.updateSlackAgent.mock.calls[0][0] as Record<string, unknown>;
    expect(req).toMatchObject({
      name: "ops-agent",
      slackUserId: "U0DANA",
      botToken: "",
      commanders: ["U0RILEY"],
      channelReplyMode: "require-approval",
      sessionIdleMinutes: 240,
      model: "claude-sonnet-4-6",
      provider: "anthropic",
      useSavedCredentials: true,
      githubToken: "",
      clear: [],
    });
  });

  it("clears the agent-specific GitHub token via clear: [github-token]", async () => {
    mocked.listSlackAgents.mockResolvedValue({ namespace: "demo", agents: [agent] } as never);

    renderDetail();

    fireEvent.click(await screen.findByRole("button", { name: "Use saved token instead" }));
    expect(screen.getByText(/will be removed on save/)).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Save changes" }));

    await waitFor(() => expect(mocked.updateSlackAgent).toHaveBeenCalled());
    const req = mocked.updateSlackAgent.mock.calls[0][0] as Record<string, unknown>;
    expect(req).toMatchObject({ githubToken: "", clear: ["github-token"] });
  });
});
