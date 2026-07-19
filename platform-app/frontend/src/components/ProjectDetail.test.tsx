import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";

import { ProjectDetail } from "@/components/ProjectDetail";

const { useProjects } = vi.hoisted(() => ({ useProjects: vi.fn() }));

vi.mock("@/hooks/useWatchedList", () => ({ useProjects }));
vi.mock("@/hooks/useAgentRuns", () => ({ useAgentRuns: () => ({ runs: [], loading: false }) }));
vi.mock("@/components/ProjectSettingsDialog", () => ({ ProjectSettingsDialog: () => null }));
vi.mock("@/components/CreateRunDialog", () => ({ CreateRunDialog: () => null }));
vi.mock("@/components/project-content/ProjectContentSection", () => ({ ProjectContentSection: () => null }));
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
});
