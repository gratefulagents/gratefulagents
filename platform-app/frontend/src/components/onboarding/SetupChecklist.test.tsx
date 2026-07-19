import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";

import { SetupChecklist } from "@/components/onboarding/SetupChecklist";
import { client } from "@/lib/client";
import { checklistDismissed } from "@/lib/onboarding";

const projectsState: { projects: { name: string }[]; loading: boolean } = {
  projects: [],
  loading: false,
};

vi.mock("@/lib/client", () => ({
  client: {
    listMyCredentials: vi.fn(),
  },
}));

vi.mock("@/contexts/AuthContext", () => ({
  useAuth: () => ({ user: { id: "u1", role: "member", name: "Dana Ops", username: "dana" } }),
}));

vi.mock("@/hooks/useWatchedList", () => ({
  useProjects: () => projectsState,
}));

function serverCreds(overrides: Record<string, unknown> = {}) {
  return {
    namespace: "dana-x",
    anthropicApiKeyPresent: false,
    openaiApiKeyPresent: false,
    anthropicOauthPresent: false,
    openaiOauthPresent: false,
    copilotOauthPresent: false,
    githubTokenPresent: false,
    ...overrides,
  };
}

afterEach(() => {
  cleanup();
  localStorage.clear();
  vi.mocked(client.listMyCredentials).mockReset();
  projectsState.projects = [];
});

function renderChecklist() {
  return render(
    <MemoryRouter>
      <SetupChecklist />
    </MemoryRouter>,
  );
}

describe("SetupChecklist", () => {
  it("shows the three steps with live completion state", async () => {
    vi.mocked(client.listMyCredentials).mockResolvedValue(
      serverCreds({ anthropicApiKeyPresent: true }) as never,
    );
    renderChecklist();

    await screen.findByText("Finish setting up");
    expect(screen.getByText("1/3")).toBeTruthy();

    // Completed provider row is inert; the remaining rows deep-link into the wizard.
    const github = screen.getByText("Add a GitHub token").closest("a");
    expect(github?.getAttribute("href")).toBe("/welcome?step=2");
    const project = screen.getByText("Create your first project").closest("a");
    expect(project?.getAttribute("href")).toBe("/welcome?step=3");
    expect(screen.getByText("Connect a model provider").closest("a")).toBeNull();
  });

  it("hides once a provider and a project exist, even without GitHub", async () => {
    vi.mocked(client.listMyCredentials).mockResolvedValue(
      serverCreds({ anthropicApiKeyPresent: true }) as never,
    );
    projectsState.projects = [{ name: "widget" }];
    renderChecklist();

    await waitFor(() => expect(client.listMyCredentials).toHaveBeenCalled());
    expect(screen.queryByText("Finish setting up")).toBeNull();
  });

  it("dismisses persistently for this user", async () => {
    vi.mocked(client.listMyCredentials).mockResolvedValue(serverCreds() as never);
    renderChecklist();

    await screen.findByText("Finish setting up");
    fireEvent.click(screen.getByLabelText("Dismiss setup checklist"));
    expect(screen.queryByText("Finish setting up")).toBeNull();
    expect(checklistDismissed("u1")).toBe(true);

    // A fresh mount stays hidden.
    cleanup();
    renderChecklist();
    await waitFor(() => expect(client.listMyCredentials).toHaveBeenCalled());
    expect(screen.queryByText("Finish setting up")).toBeNull();
  });
});
