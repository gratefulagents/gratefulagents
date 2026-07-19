import { create } from "@bufbuild/protobuf";
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";

import { GitHubRepositoryDetail } from "@/components/GitHubRepositoryDetail";
import {
  GitHubRepositoryMaintainerStatusSchema,
  GitHubRepositorySchema,
  GitHubRepositoryTriggerSettingsSchema,
} from "@/rpc/platform/service_pb";

const { useGitHubRepositories } = vi.hoisted(() => ({
  useGitHubRepositories: vi.fn(),
}));

vi.mock("@/hooks/useWatchedList", () => ({
  useGitHubRepositories,
}));

vi.mock("@/hooks/useAgentRuns", () => ({
  useAgentRuns: () => ({ runs: [], loading: false }),
}));

vi.mock("@/lib/client", () => ({
  client: {
    getActivityLog: vi.fn().mockResolvedValue({ entries: [] }),
  },
}));

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

function renderDetail(state: string, label: string) {
  useGitHubRepositories.mockReturnValue({
    repositories: [
      create(GitHubRepositorySchema, {
        namespace: "user-alice",
        name: "acme-payments",
        owner: "acme",
        repo: "payments",
        triggerSettings: create(GitHubRepositoryTriggerSettingsSchema, {
          maintainerEnabled: true,
          maintainerMaxDispatchesPerDay: 10,
        }),
        maintainerStatus: create(GitHubRepositoryMaintainerStatusSchema, {
          runName: "acme-payments-maintainer",
          lastWakeUnix: 1n,
          dispatchesToday: 3,
          lastReportTimeUnix: 1n,
          lastReportState: state,
          lastReportSummary: "Maintainer report summary",
        }),
      }),
    ],
    loading: false,
    error: null,
    refetch: vi.fn(),
  });

  render(
    <MemoryRouter initialEntries={["/github/user-alice/acme-payments"]}>
      <Routes>
        <Route path="/github/:namespace/:name" element={<GitHubRepositoryDetail />} />
      </Routes>
    </MemoryRouter>,
  );

  expect(screen.getByRole("heading", { name: "Maintainer" })).toBeTruthy();
  expect(screen.getByText(label)).toBeTruthy();
  expect(screen.getByText("Maintainer report summary")).toBeTruthy();
  expect(screen.getByText("3 / 10")).toBeTruthy();
  expect(screen.getByRole("link", { name: "acme-payments-maintainer" }).getAttribute("href")).toBe(
    "/runs/user-alice/acme-payments-maintainer",
  );
}

describe("GitHubRepositoryDetail maintainer status", () => {
  it.each([
    ["healthy", "Healthy"],
    ["needs_attention", "Needs attention"],
    ["blocked", "Blocked"],
    ["", "No report yet"],
  ])("renders the %s maintainer state", (state, label) => {
    renderDetail(state, label);
  });
});
