import { create } from "@bufbuild/protobuf";
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";

import { MaintainerPanel } from "@/components/MaintainerPanel";
import {
  ActivityEntrySchema,
  GitHubRepositoryMaintainerStatusSchema,
  GitHubRepositorySchema,
  GitHubRepositoryTriggerSettingsSchema,
} from "@/rpc/platform/service_pb";

const { getActivityLog, listMaintainerWorkItems } = vi.hoisted(() => ({
  getActivityLog: vi.fn(),
  listMaintainerWorkItems: vi.fn(),
}));

vi.mock("@/lib/client", () => ({
  client: { getActivityLog, listMaintainerWorkItems },
}));

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

function repository(state = "healthy", enabled = true) {
  return create(GitHubRepositorySchema, {
    namespace: "user-alice",
    name: "acme-payments",
    triggerSettings: create(GitHubRepositoryTriggerSettingsSchema, {
      maintainerEnabled: enabled,
      maintainerMaxDispatchesPerDay: 10,
    }),
    maintainerStatus: create(GitHubRepositoryMaintainerStatusSchema, {
      runName: "acme-payments-maintainer",
      dispatchesToday: 3,
      lastReportState: state,
      lastReportSummary: "Maintainer report summary",
      lastReportTimeUnix: 1n,
      lastWakeUnix: 1n,
    }),
  });
}

function renderPanel(state = "healthy", enabled = true) {
  getActivityLog.mockResolvedValue({ entries: [] });
  listMaintainerWorkItems.mockResolvedValue({ items: [] });
  render(
    <MemoryRouter>
      <MaintainerPanel repo={repository(state, enabled)} />
    </MemoryRouter>,
  );
}

describe("MaintainerPanel", () => {
  it.each([
    ["healthy", "Healthy"],
    ["needs_attention", "Needs attention"],
    ["blocked", "Blocked"],
    ["", "No report yet"],
  ])("renders the %s report-state chip", (state, label) => {
    renderPanel(state);
    expect(screen.getByText(label)).toBeTruthy();
  });

  it("loads parsed maintainer report events and expands their decisions", async () => {
    const decisions = "The issue lacks reproduction steps, so I left it unassigned for now.";
    listMaintainerWorkItems.mockResolvedValue({ items: [] });
    getActivityLog
      .mockResolvedValueOnce({
        entries: [
          create(ActivityEntrySchema, {
            eventId: 8n,
            type: "maintainer_report",
            timestampUnix: 2n,
            message: JSON.stringify({
              state: "needs_attention",
              summary: "Triaged the payment retry issue",
              decisions,
              time: 2,
            }),
          }),
          create(ActivityEntrySchema, {
            eventId: 7n,
            type: "maintainer_report",
            timestampUnix: 1n,
            message: "not JSON",
          }),
        ],
        firstEventId: 7n,
        hasMoreBefore: true,
      })
      .mockResolvedValueOnce({
        entries: [
          create(ActivityEntrySchema, {
            eventId: 6n,
            type: "maintainer_report",
            timestampUnix: 1n,
            message: JSON.stringify({
              state: "healthy",
              summary: "Validated the deployment rollback",
              time: 1,
            }),
          }),
        ],
        firstEventId: 6n,
        hasMoreBefore: false,
      });

    render(
      <MemoryRouter>
        <MaintainerPanel repo={repository()} />
      </MemoryRouter>,
    );

    fireEvent.click(screen.getByRole("button", { name: /Report history/ }));
    expect(await screen.findByText("Triaged the payment retry issue")).toBeTruthy();
    expect(await screen.findByText("Validated the deployment rollback")).toBeTruthy();
    expect(getActivityLog).toHaveBeenNthCalledWith(1, {
      namespace: "user-alice",
      name: "acme-payments-maintainer",
      limit: 200,
      payloadPreviewBytes: 16384,
    });
    expect(getActivityLog).toHaveBeenNthCalledWith(2, {
      namespace: "user-alice",
      name: "acme-payments-maintainer",
      limit: 200,
      payloadPreviewBytes: 16384,
      beforeEventId: 7n,
    });

    const rationale = screen.getByText(decisions);
    expect(rationale.className).toContain("line-clamp-2");
    fireEvent.click(screen.getByRole("button", { name: "Show decisions" }));
    expect(rationale.className).not.toContain("line-clamp-2");
    expect(screen.getByRole("button", { name: "Hide decisions" })).toBeTruthy();
  });

  it("shows the disabled hint without loading history", () => {
    renderPanel("healthy", false);
    expect(screen.getByText("Enable it in repository settings.")).toBeTruthy();
    expect(getActivityLog).not.toHaveBeenCalled();
    expect(listMaintainerWorkItems).not.toHaveBeenCalled();
  });

  it("links to the standing maintainer run", () => {
    renderPanel();
    expect(screen.getByRole("link", { name: "acme-payments-maintainer" }).getAttribute("href")).toBe(
      "/runs/user-alice/acme-payments-maintainer",
    );
  });

  it("lists work items in board columns with phase, decision, PR facts, and run links", async () => {
    getActivityLog.mockResolvedValue({ entries: [] });
    listMaintainerWorkItems.mockResolvedValue({
      items: [
        {
          name: "acme-wi-42",
          namespace: "user-alice",
          repositoryName: "acme-payments",
          issueNumber: 42,
          issueTitle: "Fix login bug",
          issueUrl: "https://github.com/acme/payments/issues/42",
          phase: "AwaitingDecision",
          readyToDispatch: false,
          readyToMerge: false,
          unmetRequirements: [],
          acceptedScopeCriteria: [],
          children: [],
          dependencies: [],
          pendingDecision: { id: "d-1", question: "Merge now?", options: ["yes", "no"] },
          agentRuns: [{ name: "acme-wi-42-impl", role: "implementer", phase: "Running" }],
          pullRequests: [
            {
              repository: "acme/payments",
              number: 77,
              url: "https://github.com/acme/payments/pull/77",
              state: "open",
              checkState: "Passing",
              reviewDecision: "CHANGES_REQUESTED",
              draft: false,
              headSha: "",
            },
          ],
          childrenTotal: 0,
          childrenDelivered: 0,
          dependenciesTotal: 0,
          dependenciesDelivered: 0,
          projectionSequence: 1n,
          latestCommandPhase: "Rejected",
          latestCommandType: "RequestMerge",
          latestCommandMessage: "capacity exhausted",
          acceptedScopeStatement: "",
          evidenceSummary: "",
          deliverySummary: "",
          graphConfigured: false,
          observationFresh: false,
        },
        {
          name: "acme-wi-41",
          namespace: "user-alice",
          repositoryName: "acme-payments",
          issueNumber: 41,
          issueTitle: "Shipped thing",
          phase: "Delivered",
          deliverySummary: "Fixed and released",
          unmetRequirements: [],
          agentRuns: [],
          pullRequests: [],
          acceptedScopeCriteria: [],
          children: [],
          dependencies: [],
          childrenTotal: 0,
          childrenDelivered: 0,
          dependenciesTotal: 0,
          dependenciesDelivered: 0,
          projectionSequence: 2n,
          acceptedScopeStatement: "",
          evidenceSummary: "",
          graphConfigured: false,
          observationFresh: false,
        },
        {
          name: "acme-wi-40",
          namespace: "user-alice",
          repositoryName: "acme-payments",
          issueNumber: 40,
          issueTitle: "Rejected idea",
          phase: "Triaged",
          disposition: "NotActionable",
          closeReason: "not_planned",
          unmetRequirements: [],
          agentRuns: [],
          pullRequests: [],
          acceptedScopeCriteria: [],
          children: [],
          dependencies: [],
          childrenTotal: 0,
          childrenDelivered: 0,
          dependenciesTotal: 0,
          dependenciesDelivered: 0,
          projectionSequence: 3n,
          acceptedScopeStatement: "",
          evidenceSummary: "",
          deliverySummary: "",
          graphConfigured: false,
          observationFresh: false,
        },
      ],
    });
    render(
      <MemoryRouter>
        <MaintainerPanel repo={repository()} />
      </MemoryRouter>,
    );

    expect(listMaintainerWorkItems).toHaveBeenCalledWith({
      namespace: "user-alice",
      repositoryName: "acme-payments",
    });

    // AwaitingDecision item appears in the "Needs you" column
    expect(await screen.findByText("Needs decision")).toBeTruthy();
    expect(screen.getByText("Merge now?")).toBeTruthy();
    expect(screen.getByText("Options: yes · no")).toBeTruthy();
    expect(screen.getByText(/RequestMerge command rejected: capacity exhausted/)).toBeTruthy();
    expect(screen.getByRole("link", { name: "Fix login bug" }).getAttribute("href")).toBe(
      "https://github.com/acme/payments/issues/42",
    );
    // PR chip
    expect(
      screen.getByRole("link", { name: /acme\/payments#77/ }).getAttribute("href"),
    ).toBe("https://github.com/acme/payments/pull/77");
    expect(screen.getByText(/checks passing · changes requested/)).toBeTruthy();
    // Agent run link
    expect(
      screen.getByRole("link", { name: "acme-wi-42-impl (Running)" }).getAttribute("href"),
    ).toBe("/runs/user-alice/acme-wi-42-impl");

    // Expand the Shipped column to see delivered and not-actionable items
    fireEvent.click(screen.getByRole("button", { name: /Shipped/ }));
    expect(screen.getByText("Delivered")).toBeTruthy();
    expect(screen.getByText("Fixed and released")).toBeTruthy();
    // NotActionable in Shipped column
    expect(screen.getByText("Not actionable")).toBeTruthy();
  });

  it("shows the empty work-item state", async () => {
    renderPanel();
    expect(
      await screen.findByText("No work items yet — the maintainer files each triaged issue here."),
    ).toBeTruthy();
  });
});
