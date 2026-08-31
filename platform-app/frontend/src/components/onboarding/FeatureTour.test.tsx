import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";

import { FeatureTour } from "@/components/onboarding/FeatureTour";
import { client } from "@/lib/client";
import { writeLastProject } from "@/lib/lastProject";
import { featureTourDismissed } from "@/lib/onboarding";

interface FakeProject {
  namespace: string;
  name: string;
  displayName?: string;
  triggers?: Array<{ name: string; type: string }>;
}

const projectsState: { projects: FakeProject[]; loading: boolean } = {
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

function project(overrides: Partial<FakeProject> = {}): FakeProject {
  return { namespace: "dana-x", name: "widget", displayName: "Widget", ...overrides };
}

afterEach(() => {
  cleanup();
  localStorage.clear();
  vi.mocked(client.listMyCredentials).mockReset();
  projectsState.projects = [];
});

function renderTour() {
  return render(
    <MemoryRouter>
      <FeatureTour />
    </MemoryRouter>,
  );
}

describe("FeatureTour", () => {
  it("teaches the four entry-point features once the essentials exist", async () => {
    vi.mocked(client.listMyCredentials).mockResolvedValue(
      serverCreds({ anthropicApiKeyPresent: true }) as never,
    );
    projectsState.projects = [project()];

    renderTour();
    await screen.findByText("Do more with your agents");
    expect(screen.getByText("0/4")).toBeTruthy();
    expect(screen.getByText("Set up a GitHub trigger")).toBeTruthy();
    expect(screen.getByText("Set up a Slack trigger")).toBeTruthy();
    expect(screen.getByText("Schedule recurring runs")).toBeTruthy();
    expect(screen.getByText("Connect Linear")).toBeTruthy();

    // Every "Set up" action deep-links into the project's Entry points tab.
    const setup = screen.getAllByRole("button", { name: "Set up" });
    expect(setup).toHaveLength(4);
    for (const link of setup) {
      expect(link.getAttribute("href")).toBe("/projects/dana-x/widget?tab=entry-points");
    }

    // Each pending feature links to its step-by-step guide.
    const guide = screen.getByRole("button", { name: "Open the set up a slack trigger guide" });
    expect(guide.getAttribute("href")).toBe("https://gratefulagents.dev/docs/integrations/slack/");
  });

  it("stays hidden until the setup essentials are complete", async () => {
    vi.mocked(client.listMyCredentials).mockResolvedValue(
      serverCreds({ anthropicApiKeyPresent: true }) as never,
    );
    projectsState.projects = [];

    renderTour();
    await waitFor(() => expect(client.listMyCredentials).toHaveBeenCalled());
    expect(screen.queryByText("Do more with your agents")).toBeNull();
  });

  it("checks off features that already have a trigger and hides when all are done", async () => {
    vi.mocked(client.listMyCredentials).mockResolvedValue(
      serverCreds({ anthropicApiKeyPresent: true }) as never,
    );
    projectsState.projects = [
      project({
        triggers: [
          { name: "standup", type: "slack" },
          { name: "nightly", type: "cron" },
        ],
      }),
    ];

    renderTour();
    await screen.findByText("Do more with your agents");
    expect(screen.getByText("2/4")).toBeTruthy();
    // Done rows collapse to a crossed-off title without a Set up action.
    expect(screen.getAllByRole("button", { name: "Set up" })).toHaveLength(2);

    cleanup();
    projectsState.projects = [
      project({
        triggers: [
          { name: "a", type: "github" },
          { name: "b", type: "slack" },
          { name: "c", type: "cron" },
          { name: "d", type: "linear" },
        ],
      }),
    ];
    renderTour();
    await waitFor(() => expect(client.listMyCredentials).toHaveBeenCalled());
    expect(screen.queryByText("Do more with your agents")).toBeNull();
  });

  it("prefers the most recently used project for deep links", async () => {
    vi.mocked(client.listMyCredentials).mockResolvedValue(
      serverCreds({ anthropicApiKeyPresent: true }) as never,
    );
    projectsState.projects = [project(), project({ name: "gadget", displayName: "Gadget" })];
    writeLastProject({ namespace: "dana-x", name: "gadget" });

    renderTour();
    await screen.findByText("Do more with your agents");
    const link = screen.getAllByRole("button", { name: "Set up" })[0];
    expect(link.getAttribute("href")).toBe("/projects/dana-x/gadget?tab=entry-points");
  });

  it("dismisses persistently for this user", async () => {
    vi.mocked(client.listMyCredentials).mockResolvedValue(
      serverCreds({ anthropicApiKeyPresent: true }) as never,
    );
    projectsState.projects = [project()];

    renderTour();
    await screen.findByText("Do more with your agents");
    fireEvent.click(screen.getByRole("button", { name: "Dismiss feature tour" }));
    expect(screen.queryByText("Do more with your agents")).toBeNull();
    expect(featureTourDismissed("u1")).toBe(true);
  });
});
