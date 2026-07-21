import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";

import { ProjectDetail } from "@/components/ProjectDetail";

const { useProjects } = vi.hoisted(() => ({ useProjects: vi.fn() }));

vi.mock("@/hooks/useWatchedList", () => ({ useProjects }));
vi.mock("@/hooks/useAgentRuns", () => ({ useAgentRuns: () => ({ runs: [], loading: false }) }));
vi.mock("@/components/ProjectSettingsDialog", () => ({ ProjectSettingsDialog: () => null }));
vi.mock("@/components/CreateRunDialog", () => ({ CreateRunDialog: () => null }));
vi.mock("@/components/NewChatComposer", () => ({
  NewChatComposer: () => <div data-testid="new-chat-composer" />,
}));
vi.mock("@/components/project-content/ProjectContentSection", () => ({
  ProjectContentSection: () => <div data-testid="project-content-section" />,
}));
vi.mock("@/components/projectCredentials", () => ({ ProjectCredentialBadges: () => null }));
vi.mock("@/lib/client", () => ({ client: { listConnections: vi.fn().mockResolvedValue({ connections: [] }) } }));

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

describe("ProjectDetail triggers", () => {
  it("renders dashboard chat and a disabled trigger", () => {
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

    expect(screen.getByRole("heading", { name: "Entry points" })).toBeTruthy();
    expect(screen.getByText("Dashboard chat")).toBeTruthy();
    expect(screen.getByText("nightly-triage")).toBeTruthy();
    expect(screen.getByText("disabled")).toBeTruthy();
    fireEvent.click(screen.getByRole("link", { name: /Dashboard chat/ }));
    expect(localStorage.getItem("gratefulagents.lastProject.v1")).toBe("team/platform");
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

    // Overview is the default tab: composer + entry points, no files section.
    expect(screen.getByTestId("new-chat-composer")).toBeTruthy();
    expect(screen.getByRole("heading", { name: "Entry points" })).toBeTruthy();
    expect(screen.queryByTestId("project-content-section")).toBeNull();

    fireEvent.click(screen.getByRole("tab", { name: "Files" }));
    expect(screen.getByTestId("project-content-section")).toBeTruthy();
    expect(screen.queryByTestId("new-chat-composer")).toBeNull();
    expect(screen.getByRole("heading", { name: "Platform" })).toBeTruthy();

    fireEvent.click(screen.getByRole("tab", { name: "Configuration" }));
    expect(screen.getByRole("heading", { name: "Configuration" })).toBeTruthy();
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
    expect(screen.queryByTestId("new-chat-composer")).toBeNull();
  });
});
