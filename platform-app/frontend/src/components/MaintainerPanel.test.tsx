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

const { getActivityLog } = vi.hoisted(() => ({
  getActivityLog: vi.fn(),
}));

vi.mock("@/lib/client", () => ({
  client: { getActivityLog },
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
  });

  it("links to the standing maintainer run", () => {
    renderPanel();
    expect(screen.getByRole("link", { name: "acme-payments-maintainer" }).getAttribute("href")).toBe(
      "/runs/user-alice/acme-payments-maintainer",
    );
  });
});
