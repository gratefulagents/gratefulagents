import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { Link, MemoryRouter, Route, Routes } from "react-router-dom";

import { ProjectDetail } from "@/components/ProjectDetail";

const { useProjects } = vi.hoisted(() => ({ useProjects: vi.fn() }));

vi.mock("@/hooks/useWatchedList", () => ({ useProjects }));
vi.mock("@/hooks/useAgentRuns", () => ({ useAgentRuns: () => ({ runs: [], loading: false }) }));
vi.mock("@/components/ProjectSettingsDialog", () => ({ ProjectSettingsDialog: () => null }));
vi.mock("@/components/CreateRunDialog", async () => {
  const { useState } = await import("react");
  return {
    CreateRunDialog: ({ defaultSource }: { defaultSource: string }) => {
      // Mirror the real dialog's mount-scoped form initialization so route
      // reuse regressions are visible in this parent integration test.
      const [mountedSource] = useState(defaultSource);
      return <div data-testid="create-run-project">{mountedSource}</div>;
    },
  };
});
vi.mock("@/components/project-content/ProjectContentSection", () => ({
  ProjectContentSection: () => <div data-testid="project-content-section" />,
}));
vi.mock("@/components/projectCredentials", () => ({ ProjectCredentialBadges: () => null }));
vi.mock("@/lib/client", () => ({
  client: {
    listConnections: vi.fn().mockResolvedValue({ connections: [] }),
    getActivityLog: vi.fn().mockResolvedValue({ entries: [], hasMoreBefore: false }),
  },
}));

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

describe("ProjectDetail triggers", () => {
  it("renders dashboard chat and a disabled trigger on the Entry points tab", () => {
    useProjects.mockReturnValue({
      projects: [{
        namespace: "team",
        name: "platform",
        displayName: "Platform",
        additionalRepoUrls: [],
        allowedModels: [],
        mcpPolicyAllowedServers: [],
        metrics: {},
        triggers: [{
          name: "nightly-triage",
          type: "cron",
          enabled: false,
          cron: { schedule: "0 9 * * 1-5", timeZone: "UTC" },
        }],
      }],
      loading: false,
      error: null,
      refetch: vi.fn(),
    });

    render(
      <MemoryRouter initialEntries={["/projects/team/platform?tab=entry-points"]}>
        <Routes>
          <Route path="/projects/:namespace/:name" element={<ProjectDetail />} />
        </Routes>
      </MemoryRouter>,
    );

    expect(screen.getByRole("heading", { name: "Entry points" })).toBeTruthy();
    expect(screen.getByText("Dashboard chat")).toBeTruthy();
    expect(screen.getByText("nightly-triage")).toBeTruthy();
    expect(screen.getByText("disabled")).toBeTruthy();
    fireEvent.click(screen.getByRole("link", { name: /Dashboard chat/ }));
    expect(localStorage.getItem("gratefulagents.lastProject.v1")).toBe("team/platform");
  });

  it("shows a compact entry-points preview on Overview that jumps to the tab", () => {
    useProjects.mockReturnValue({
      projects: [{
        namespace: "team",
        name: "platform",
        displayName: "Platform",
        additionalRepoUrls: [],
        allowedModels: [],
        mcpPolicyAllowedServers: [],
        metrics: {},
        triggers: [{
          name: "nightly-triage",
          type: "cron",
          enabled: false,
          cron: { schedule: "0 9 * * 1-5", timeZone: "UTC" },
        }],
      }],
      loading: false,
      error: null,
      refetch: vi.fn(),
    });

    render(
      <MemoryRouter initialEntries={["/projects/team/platform"]}>
        <Routes>
          <Route path="/projects/:namespace/:name" element={<ProjectDetail />} />
        </Routes>
      </MemoryRouter>,
    );

    // Overview shows the compact preview: chat pill + trigger pill.
    expect(screen.getByRole("heading", { name: "Entry points" })).toBeTruthy();
    expect(screen.getByText("Dashboard chat")).toBeTruthy();
    expect(screen.getByText("nightly-triage")).toBeTruthy();
    // Management controls only live on the Entry points tab.
    expect(screen.queryByRole("button", { name: "New trigger" })).toBeNull();

    fireEvent.click(screen.getByRole("button", { name: /Manage/ }));
    expect(screen.getByRole("button", { name: /New trigger/ })).toBeTruthy();
    expect(screen.getByRole("button", { name: "Manage connections" })).toBeTruthy();
  });

  it("switches tabs and keeps the header visible", () => {
    useProjects.mockReturnValue({
      projects: [{
        namespace: "team",
        name: "platform",
        displayName: "Platform",
        additionalRepoUrls: [],
        allowedModels: [],
        mcpPolicyAllowedServers: [],
        metrics: {},
        triggers: [],
      }],
      loading: false,
      error: null,
      refetch: vi.fn(),
    });

    render(
      <MemoryRouter initialEntries={["/projects/team/platform"]}>
        <Routes>
          <Route path="/projects/:namespace/:name" element={<ProjectDetail />} />
        </Routes>
      </MemoryRouter>,
    );

    // Overview is the default tab: entry points visible, no files section.
    expect(screen.getByRole("heading", { name: "Entry points" })).toBeTruthy();
    expect(screen.queryByTestId("project-content-section")).toBeNull();

    fireEvent.click(screen.getByRole("tab", { name: "Files" }));
    expect(screen.getByTestId("project-content-section")).toBeTruthy();
    expect(screen.queryByRole("heading", { name: "Entry points" })).toBeNull();
    expect(screen.getByRole("heading", { name: "Platform" })).toBeTruthy();

    fireEvent.click(screen.getByRole("tab", { name: "Configuration" }));
    expect(screen.getByRole("heading", { name: "Configuration" })).toBeTruthy();
  });

  it("remounts New Run with the current project when the project route changes", () => {
    useProjects.mockReturnValue({
      projects: [
        {
          namespace: "team",
          name: "alpha",
          displayName: "Alpha",
          additionalRepoUrls: [],
          allowedModels: [],
          mcpPolicyAllowedServers: [],
          metrics: {},
          triggers: [],
        },
        {
          namespace: "team",
          name: "beta",
          displayName: "Beta",
          additionalRepoUrls: [],
          allowedModels: [],
          mcpPolicyAllowedServers: [],
          metrics: {},
          triggers: [],
        },
      ],
      loading: false,
      error: null,
      refetch: vi.fn(),
    });

    render(
      <MemoryRouter initialEntries={["/projects/team/alpha"]}>
        <Link to="/projects/team/beta">Open Beta</Link>
        <Routes>
          <Route path="/projects/:namespace/:name" element={<ProjectDetail />} />
        </Routes>
      </MemoryRouter>,
    );

    expect(screen.getByTestId("create-run-project").textContent).toBe("alpha");
    fireEvent.click(screen.getByRole("link", { name: "Open Beta" }));
    expect(screen.getByTestId("create-run-project").textContent).toBe("beta");
  });

  it("opens the tab named in the URL", () => {
    useProjects.mockReturnValue({
      projects: [{
        namespace: "team",
        name: "platform",
        displayName: "Platform",
        additionalRepoUrls: [],
        allowedModels: [],
        mcpPolicyAllowedServers: [],
        metrics: {},
        triggers: [],
      }],
      loading: false,
      error: null,
      refetch: vi.fn(),
    });

    render(
      <MemoryRouter initialEntries={["/projects/team/platform?tab=files"]}>
        <Routes>
          <Route path="/projects/:namespace/:name" element={<ProjectDetail />} />
        </Routes>
      </MemoryRouter>,
    );

    expect(screen.getByTestId("project-content-section")).toBeTruthy();
    expect(screen.queryByRole("heading", { name: "Entry points" })).toBeNull();
  });
});

describe("ProjectDetail maintainer", () => {
  const projectWithMaintainer = (maintainerEnabled: boolean, triggerEnabled = true) => ({
    namespace: "team",
    name: "platform",
    displayName: "Platform",
    additionalRepoUrls: [],
    allowedModels: [],
    mcpPolicyAllowedServers: [],
    metrics: {},
    triggers: [
      {
        name: "github-issues",
        type: "github",
        enabled: triggerEnabled,
        github: {
          owner: "acme",
          repo: "payments",
          maintainerEnabled,
          maintainerMaxDispatchesPerDay: 12,
          maintainerAllowPrMerge: false,
        },
        maintainerStatus: maintainerEnabled
          ? {
              runName: "project-platform-github-issues-maintainer",
              dispatchesToday: 3,
              lastReportState: "needs_attention",
              lastReportSummary: "Two PRs await review.",
              lastReportTimeUnix: BigInt(Math.floor(Date.now() / 1000) - 3600),
            }
          : undefined,
      },
    ],
  });

  const renderProject = () =>
    render(
      <MemoryRouter initialEntries={["/projects/team/platform"]}>
        <Routes>
          <Route path="/projects/:namespace/:name" element={<ProjectDetail />} />
        </Routes>
      </MemoryRouter>,
    );

  it("shows the maintainer card for github triggers with the maintainer enabled", () => {
    useProjects.mockReturnValue({
      projects: [projectWithMaintainer(true)],
      loading: false,
      error: null,
      refetch: vi.fn(),
    });
    renderProject();

    expect(screen.getByRole("heading", { name: "Maintainer" })).toBeTruthy();
    expect(screen.getByText("Two PRs await review.")).toBeTruthy();
    expect(screen.getByText("Needs attention")).toBeTruthy();
    expect(screen.getByText("3 / 12")).toBeTruthy();
    expect(
      screen
        .getByRole("link", { name: "project-platform-github-issues-maintainer" })
        .getAttribute("href"),
    ).toBe("/runs/team/project-platform-github-issues-maintainer");
  });

  it("hides the maintainer section when no trigger enables it", () => {
    useProjects.mockReturnValue({
      projects: [projectWithMaintainer(false)],
      loading: false,
      error: null,
      refetch: vi.fn(),
    });
    renderProject();

    expect(screen.queryByRole("heading", { name: "Maintainer" })).toBeNull();
  });

  it("hides the maintainer when its project trigger is disabled", () => {
    useProjects.mockReturnValue({
      projects: [projectWithMaintainer(true, false)],
      loading: false,
      error: null,
      refetch: vi.fn(),
    });
    renderProject();

    expect(screen.queryByRole("heading", { name: "Maintainer" })).toBeNull();
  });
});
