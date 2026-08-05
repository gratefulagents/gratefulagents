import { create } from "@bufbuild/protobuf";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";

import { SecurityOverview } from "@/components/SecurityOverview";
import {
  GetSecurityOverviewResponseSchema,
  SecurityScanSchema,
} from "@/rpc/platform/service_pb";

const { getSecurityOverview } = vi.hoisted(() => ({
  getSecurityOverview: vi.fn(),
}));

vi.mock("@/lib/client", () => ({
  client: { getSecurityOverview },
}));

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

function scan(runName: string, opts: { completed?: boolean; status?: string } = {}) {
  return create(SecurityScanSchema, {
    id: "11111111-1111-1111-1111-111111111111",
    namespace: "user-alice",
    scanName: "nightly",
    runName,
    repository: "github.com/acme/payments",
    status: opts.status ?? (opts.completed ? "completed" : "running"),
    startedAt: timestampFromDate(new Date(Date.now() - 60 * 60 * 1000)),
    ...(opts.completed
      ? { completedAt: timestampFromDate(new Date(Date.now() - 30 * 60 * 1000)) }
      : {}),
    counts: opts.completed ? { critical: 2, total: 3, open: 3 } : {},
  });
}

function overviewFixture() {
  return create(GetSecurityOverviewResponseSchema, {
    storeSupported: true,
    activeScans: [scan("nightly-2")],
    recentScans: [scan("nightly-1", { completed: true })],
    findingCounts: { total: 3, open: 3, open_critical: 2, open_high: 1 },
    configCount: 2,
    configIssues: [
      {
        namespace: "user-alice",
        name: "failing-scan",
        phase: "Error",
        readyReason: "RunCreationFailed",
        message: "run creation failed",
        suspended: false,
      },
    ],
  });
}

function renderOverview() {
  render(
    <MemoryRouter>
      <SecurityOverview />
    </MemoryRouter>,
  );
}

describe("SecurityOverview", () => {
  it("renders counts, active and recent scans, and config issues", async () => {
    getSecurityOverview.mockResolvedValue(overviewFixture());

    renderOverview();

    expect(await screen.findByText("Open critical")).toBeTruthy();
    expect(screen.getByText("Open high")).toBeTruthy();
    // Active scan links to its detail page.
    expect(
      screen.getByRole("link", { name: "nightly-2" }).getAttribute("href"),
    ).toBe("/security/user-alice/nightly-2");
    expect(
      screen.getByRole("link", { name: "nightly-1" }).getAttribute("href"),
    ).toBe("/security/user-alice/nightly-1");
    // Failing configuration surfaces with its reason.
    expect(screen.getByText("RunCreationFailed")).toBeTruthy();
    expect(screen.getByText("run creation failed")).toBeTruthy();
    // Navigation actions to configurations and run history.
    expect(
      screen.getByRole("button", { name: /Run history/ }).getAttribute("href"),
    ).toBe("/security/runs");
    expect(
      screen.getByRole("button", { name: /Scan configurations/ }).getAttribute("href"),
    ).toBe("/security/configs");
    // Baseline deltas are hidden until baseline data exists.
    expect(
      screen.getByText(/baseline comparisons are available/),
    ).toBeTruthy();
    expect(getSecurityOverview).toHaveBeenCalledWith({ namespace: "" });
  });

  it("shows baseline deltas when baseline data is available", async () => {
    const fixture = overviewFixture();
    fixture.baselineAvailable = true;
    fixture.newFindings = 2;
    fixture.recurringFindings = 5;
    fixture.resolvedFindings = 1;
    getSecurityOverview.mockResolvedValue(fixture);

    renderOverview();

    expect(
      await screen.findByText(/2 new, 5 recurring, 1 resolved/),
    ).toBeTruthy();
  });

  it("degrades to configurations when the store lacks security support", async () => {
    getSecurityOverview.mockResolvedValue(
      create(GetSecurityOverviewResponseSchema, {
        storeSupported: false,
        configCount: 1,
        configIssues: [],
      }),
    );

    renderOverview();

    expect(
      await screen.findByText(/does not support\s+security findings/),
    ).toBeTruthy();
    expect(screen.getByText("All scan configurations are healthy.")).toBeTruthy();
    expect(screen.queryByText("Open critical")).toBeNull();
  });

  it("shows a partial-failure banner when one aggregation fails", async () => {
    const fixture = overviewFixture();
    fixture.warnings = ["listing security scans: scan table offline"];
    getSecurityOverview.mockResolvedValue(fixture);

    renderOverview();

    expect(
      await screen.findByText(/Partial data — some sources failed to load\./),
    ).toBeTruthy();
    expect(screen.getByText(/scan table offline/)).toBeTruthy();
  });

  it("shows the empty state with a configure action", async () => {
    getSecurityOverview.mockResolvedValue(
      create(GetSecurityOverviewResponseSchema, { storeSupported: true }),
    );

    renderOverview();

    expect(await screen.findByText("No security scans yet")).toBeTruthy();
    expect(
      screen.getByRole("button", { name: /Configure a scan/ }).getAttribute("href"),
    ).toBe("/security/configs");
  });

  it("shows the error state when the overview fails to load", async () => {
    getSecurityOverview.mockRejectedValue(new Error("dashboard offline"));

    renderOverview();

    expect(await screen.findByText("dashboard offline")).toBeTruthy();
  });
});
