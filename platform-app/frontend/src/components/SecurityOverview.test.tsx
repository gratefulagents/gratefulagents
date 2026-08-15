import { create } from "@bufbuild/protobuf";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, useLocation } from "react-router-dom";

import { SecurityOverview } from "@/components/SecurityOverview";
import {
  GetSecurityConfigPosturesResponseSchema,
  GetSecurityOverviewResponseSchema,
  SecurityScanSchema,
  SecuritySkillsStatusSchema,
  type SecuritySkillsStatus,
} from "@/rpc/platform/service_pb";

const { getSecurityOverview, getSecurityConfigPostures, getSecuritySkillsStatus, installSecuritySkills } = vi.hoisted(() => ({
  getSecurityOverview: vi.fn(),
  getSecurityConfigPostures: vi.fn(),
  getSecuritySkillsStatus: vi.fn(),
  installSecuritySkills: vi.fn(),
}));

vi.mock("@/lib/client", () => ({
  client: { getSecurityOverview, getSecurityConfigPostures, getSecuritySkillsStatus, installSecuritySkills },
}));

beforeEach(() => {
  getSecuritySkillsStatus.mockResolvedValue(create(SecuritySkillsStatusSchema, {
    state: "installed",
    installedCount: 55,
    availableCount: 55,
  }));
  getSecurityConfigPostures.mockResolvedValue(
    create(GetSecurityConfigPosturesResponseSchema, { storeSupported: true, postures: [] }),
  );
});

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

const MINUTE = 60 * 1000;
const DAY = 24 * 60 * MINUTE;

function scan(
  runName: string,
  opts: { completed?: boolean; status?: string; repository?: string; ageMs?: number } = {},
) {
  const age = opts.ageMs ?? 30 * MINUTE;
  return create(SecurityScanSchema, {
    id: "11111111-1111-1111-1111-111111111111",
    namespace: "user-alice",
    scanName: "nightly",
    runName,
    repository: opts.repository ?? "github.com/acme/payments",
    status: opts.status ?? (opts.completed ? "completed" : "running"),
    startedAt: timestampFromDate(new Date(Date.now() - age - 5 * MINUTE)),
    ...(opts.completed ? { completedAt: timestampFromDate(new Date(Date.now() - age)) } : {}),
    counts: opts.completed ? { critical: 2, total: 3, open: 3 } : {},
  });
}

function overviewFixture() {
  return create(GetSecurityOverviewResponseSchema, {
    storeSupported: true,
    activeScans: [scan("nightly-2")],
    recentScans: [
      scan("nightly-1", { completed: true }),
      scan("weekly-9", {
        completed: true,
        repository: "github.com/acme/billing",
        ageMs: 10 * DAY,
      }),
    ],
    findingCounts: { total: 9, open: 3, open_critical: 2, open_high: 1 },
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

function LocationProbe() {
  const location = useLocation();
  return <span data-testid="location">{location.search}</span>;
}

function renderOverview(initialEntry = "/security") {
  render(
    <MemoryRouter initialEntries={[initialEntry]}>
      <SecurityOverview />
      <LocationProbe />
    </MemoryRouter>,
  );
}

function href(name: string | RegExp): string | null {
  return screen.getByRole("link", { name }).getAttribute("href");
}

/** Drive a base-ui Select the way a pointer user does. */
async function pickFilter(label: string, option: string | RegExp) {
  fireEvent.click(screen.getByRole("combobox", { name: label }));
  const item = await screen.findByRole("option", { name: option });
  fireEvent.pointerDown(item, { pointerType: "mouse", button: 0 });
  fireEvent.pointerUp(item, { pointerType: "mouse", button: 0 });
  fireEvent.click(item);
}

describe("SecurityOverview", () => {
  it("renders the posture row, both scan tables, and configuration issues", async () => {
    getSecurityOverview.mockResolvedValue(overviewFixture());
    getSecurityConfigPostures.mockResolvedValue(
      create(GetSecurityConfigPosturesResponseSchema, {
        storeSupported: true,
        postures: [
          {
            scanName: "nightly",
            findingCounts: { total: 3, open: 3, open_critical: 2 },
            repository: "github.com/acme/payments",
            lastRunName: "nightly-1",
            lastRunStatus: "completed",
          },
        ],
      }),
    );

    renderOverview();

    expect(await screen.findByRole("link", { name: "Critical: 2" })).toBeTruthy();
    expect(href("High: 1")).toBe("/security/runs?severity=high");
    expect(href("Total findings: 9")).toBe("/security/runs");
    // The header search fits its fixed-width input.
    expect(screen.getByPlaceholderText("Search scans…")).toBeTruthy();
    // Scan rows deep-link into their run detail.
    expect(href("nightly-2")).toBe("/security/user-alice/nightly-2");
    expect(href("nightly-1")).toBe("/security/user-alice/nightly-1");
    // A failing configuration links to that configuration's detail page.
    expect(href("failing-scan")).toBe("/security/configs/user-alice/failing-scan");
    expect(screen.getByText("RunCreationFailed")).toBeTruthy();
    expect(screen.getByText("run creation failed")).toBeTruthy();
    // Per-configuration posture still renders from its own RPC.
    await waitFor(() =>
      expect(getSecurityConfigPostures).toHaveBeenCalledWith({ namespace: "", activityLimit: 0 }),
    );
    // The shared security sub-navigation.
    expect(href(/Scan runs/)).toBe("/security/runs");
    expect(href(/Configurations/)).toBe("/security/configs");
    expect(href(/Library/)).toBe("/security/library");
    expect(getSecurityOverview).toHaveBeenCalledWith({ namespace: "" });
  });

  it("links every headline tile into a filtered view", async () => {
    getSecurityOverview.mockResolvedValue(overviewFixture());

    renderOverview();

    expect(await screen.findByRole("link", { name: "Critical: 2" })).toBeTruthy();
    expect(href("Critical: 2")).toBe("/security/runs?severity=critical");
    // A tile has to look like the link it is: pointer cursor plus a chevron.
    const criticalTile = screen.getByRole("link", { name: "Critical: 2" });
    expect(criticalTile.className).toContain("cursor-pointer");
    expect(criticalTile.querySelector("svg")).toBeTruthy();
    expect(href("Actionable findings: 3")).toBe("/security/runs");
    expect(href("Active scans: 1")).toBe("/security/runs?status=running");
    expect(href("Configs needing attention: 1")).toBe("/security/configs?status=attention");
    // Without baseline data the baseline metric still leads somewhere useful.
    expect(href("New since baseline: 0")).toBe("/security/user-alice/nightly-1");
    // The in-flight count is announced as scans start and finish.
    expect(
      screen
        .getByRole("link", { name: "Active scans: 1" })
        .querySelector('[aria-live="polite"]')?.textContent,
    ).toBe("1");
  });

  it("spells the actionable total out across all five severities", async () => {
    getSecurityOverview.mockResolvedValue(overviewFixture());

    renderOverview();

    expect(await screen.findByRole("link", { name: "Actionable findings: 3" })).toBeTruthy();
    // Every severity in the sum is present and links into its filtered view,
    // so 2 + 1 + 0 + 0 + 0 visibly reconciles with the total.
    expect(href("Critical findings: 2")).toBe("/security/runs?severity=critical");
    expect(href("High findings: 1")).toBe("/security/runs?severity=high");
    expect(href("Medium findings: 0")).toBe("/security/runs?severity=medium");
    expect(href("Low findings: 0")).toBe("/security/runs?severity=low");
    expect(href("Info findings: 0")).toBe("/security/runs?severity=info");
  });

  it("carries the active range and repository filters into tile links", async () => {
    getSecurityOverview.mockResolvedValue(overviewFixture());

    renderOverview("/security?range=7d&repo=acme%2Fpayments");

    expect(await screen.findByRole("link", { name: "Critical: 2" })).toBeTruthy();
    expect(href("Critical: 2")).toBe(
      "/security/runs?repo=acme%2Fpayments&range=7d&severity=critical",
    );
    expect(href("Active scans: 1")).toBe(
      "/security/runs?repo=acme%2Fpayments&range=7d&status=running",
    );
    // Configuration health is not a scan lens, so it stays unfiltered.
    expect(href("Configs needing attention: 1")).toBe("/security/configs?status=attention");
    // The filters are applied to the rendered rows, not just the links.
    expect(screen.getByRole("link", { name: "nightly-1" })).toBeTruthy();
    expect(screen.queryByRole("link", { name: "weekly-9" })).toBeNull();
  });

  it("round-trips the time range through the URL", async () => {
    getSecurityOverview.mockResolvedValue(overviewFixture());

    renderOverview();

    // Both completed runs are visible with no range filter.
    expect(await screen.findByRole("link", { name: "weekly-9" })).toBeTruthy();

    await pickFilter("Time range", "Last 24 hours");

    await waitFor(() =>
      expect(screen.getByTestId("location").textContent).toBe("?range=24h"),
    );
    await waitFor(() => expect(screen.queryByRole("link", { name: "weekly-9" })).toBeNull());
    expect(screen.getByRole("link", { name: "nightly-1" })).toBeTruthy();
    expect(screen.getByText("1 of 2 recent scans")).toBeTruthy();
  });

  it("restores the range filter from the URL after a reload", async () => {
    getSecurityOverview.mockResolvedValue(overviewFixture());

    renderOverview("/security?range=24h");

    expect(await screen.findByRole("link", { name: "nightly-1" })).toBeTruthy();
    expect(screen.queryByRole("link", { name: "weekly-9" })).toBeNull();
    expect(screen.getByRole("combobox", { name: "Time range" }).textContent).toContain(
      "Last 24 hours",
    );
  });

  it("derives the repository filter from the returned scans", async () => {
    getSecurityOverview.mockResolvedValue(overviewFixture());

    renderOverview();

    expect(await screen.findByRole("link", { name: "weekly-9" })).toBeTruthy();
    await pickFilter("Repository", "acme/billing");

    await waitFor(() =>
      expect(screen.getByTestId("location").textContent).toBe("?repo=acme%2Fbilling"),
    );
    await waitFor(() => expect(screen.queryByRole("link", { name: "nightly-1" })).toBeNull());
    expect(screen.getByRole("link", { name: "weekly-9" })).toBeTruthy();
    // The active scan belongs to another repository, so the tile drops to zero.
    expect(href("Active scans: 0")).toBe("/security/runs?repo=acme%2Fbilling&status=running");
  });

  it("explains an over-narrow filter instead of looking like an empty account", async () => {
    getSecurityOverview.mockResolvedValue(overviewFixture());

    renderOverview("/security?repo=acme%2Fbilling&range=24h");

    expect(await screen.findByText("No recent scans match the current filters.")).toBeTruthy();
    expect(screen.queryByText("No security scans yet")).toBeNull();

    fireEvent.click(screen.getByRole("button", { name: "Clear filters" }));

    await waitFor(() => expect(screen.getByTestId("location").textContent).toBe(""));
    expect(await screen.findByRole("link", { name: "weekly-9" })).toBeTruthy();
  });

  it("shows baseline deltas when baseline data is available", async () => {
    const fixture = overviewFixture();
    fixture.baselineAvailable = true;
    fixture.newFindings = 2;
    fixture.recurringFindings = 5;
    fixture.resolvedFindings = 1;
    getSecurityOverview.mockResolvedValue(fixture);

    renderOverview();

    const deltas = await screen.findByLabelText("Baseline changes");
    expect(deltas.textContent).toContain("Since the last baseline:");
    expect(deltas.textContent).toContain("new2");
    expect(deltas.textContent).toContain("recurring5");
    expect(deltas.textContent).toContain("resolved1");
    // The headline metric points at the newest run's baseline view, and the
    // chip row is the only baseline treatment on the page.
    expect(href("New since baseline: 2")).toBe(
      "/security/user-alice/nightly-1?baseline=new",
    );
    expect(screen.getAllByRole("link", { name: /since baseline/ })).toHaveLength(5);
  });

  it("makes relative times unambiguous and dashes meaningful", async () => {
    const fixture = overviewFixture();
    // A queued run has neither a start time nor findings yet.
    fixture.activeScans = [
      create(SecurityScanSchema, {
        namespace: "user-alice",
        runName: "queued-1",
        scanName: "nightly",
        repository: "github.com/acme/payments",
        status: "pending",
      }),
    ];
    getSecurityOverview.mockResolvedValue(fixture);

    renderOverview();

    const completedRow = (await screen.findByRole("link", { name: "nightly-1" })).closest("tr")!;
    const completedAge = completedRow.querySelector("td:last-child span")!;
    expect(completedAge.textContent).toBe("30m ago");
    expect(completedAge.getAttribute("title")).toBeTruthy();

    const queuedRow = screen.getByRole("link", { name: "queued-1" }).closest("tr")!;
    expect(queuedRow.querySelector("td:last-child span")?.getAttribute("title")).toBe(
      "Not started yet",
    );
    expect(screen.getByTitle("No findings reported")).toBeTruthy();
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
    expect(screen.queryByTestId("security-posture")).toBeNull();
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
    // The rest of the dashboard still renders.
    expect(screen.getByRole("link", { name: "Critical: 2" })).toBeTruthy();
  });

  it("keeps the dashboard when the per-configuration posture request fails", async () => {
    getSecurityOverview.mockResolvedValue(overviewFixture());
    getSecurityConfigPostures.mockRejectedValue(new Error("postures offline"));

    renderOverview();

    expect(await screen.findByRole("link", { name: "Critical: 2" })).toBeTruthy();
    expect(screen.getByRole("link", { name: "nightly-1" })).toBeTruthy();
    expect(screen.queryByText("No security scans yet")).toBeNull();
    await waitFor(() => expect(getSecurityConfigPostures).toHaveBeenCalled());
  });

  it("shows the first-run empty state with an explanation and a configure action", async () => {
    getSecurityOverview.mockResolvedValue(
      create(GetSecurityOverviewResponseSchema, { storeSupported: true }),
    );

    renderOverview();

    expect(await screen.findByText("No security scans yet")).toBeTruthy();
    expect(screen.getByText(/hunt for vulnerabilities/)).toBeTruthy();
    expect(
      screen.getByRole("button", { name: /Configure a scan/ }).getAttribute("href"),
    ).toBe("/security/configs");
    // No filters to offer when there is nothing to filter.
    expect(screen.queryByRole("combobox", { name: "Time range" })).toBeNull();
  });

  it("offers a recoverable error state when the overview fails to load", async () => {
    getSecurityOverview.mockRejectedValueOnce(new Error("dashboard offline"));
    getSecurityOverview.mockResolvedValue(overviewFixture());

    renderOverview();

    expect(await screen.findByText("Couldn't load the security overview")).toBeTruthy();
    expect(screen.getByText("dashboard offline")).toBeTruthy();
    expect(href("Scan configurations")).toBe("/security/configs");
    // Never an empty state when the request simply failed.
    expect(screen.queryByText("No security scans yet")).toBeNull();

    fireEvent.click(screen.getByRole("button", { name: "Try again" }));

    expect(await screen.findByRole("link", { name: "Critical: 2" })).toBeTruthy();
    expect(screen.queryByText("Couldn't load the security overview")).toBeNull();
  });

  it("does not install security skills on page load", async () => {
    getSecurityOverview.mockResolvedValue(overviewFixture());
    getSecuritySkillsStatus.mockResolvedValue(create(SecuritySkillsStatusSchema, {
      state: "not_installed",
      availableCount: 55,
    }));

    renderOverview();

    expect(await screen.findByRole("button", { name: "Install security skills" })).toBeTruthy();
    expect(getSecuritySkillsStatus).toHaveBeenCalledTimes(1);
    expect(installSecuritySkills).not.toHaveBeenCalled();
  });

  it("installs security skills only after an explicit click", async () => {
    getSecurityOverview.mockResolvedValue(overviewFixture());
    getSecuritySkillsStatus.mockResolvedValue(create(SecuritySkillsStatusSchema, {
      state: "not_installed",
      availableCount: 55,
    }));
    let resolveInstall!: (value: SecuritySkillsStatus) => void;
    installSecuritySkills.mockImplementation(() => new Promise((resolve) => {
      resolveInstall = resolve;
    }));

    renderOverview();

    fireEvent.click(await screen.findByRole("button", { name: "Install security skills" }));
    const installing = await screen.findByRole("button", { name: "Installing…" });
    expect((installing as HTMLButtonElement).disabled).toBe(true);
    expect(installSecuritySkills).toHaveBeenCalledTimes(1);

    resolveInstall(create(SecuritySkillsStatusSchema, {
      state: "installed",
      installedCount: 55,
      availableCount: 55,
    }));
    expect(await screen.findByText("Security skills · Installed")).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Install security skills" })).toBeNull();
  });

  it("shows partial installation progress and keeps missing skills actionable", async () => {
    getSecurityOverview.mockResolvedValue(overviewFixture());
    getSecuritySkillsStatus.mockResolvedValue(create(SecuritySkillsStatusSchema, {
      state: "partially_installed",
      installedCount: 7,
      availableCount: 10,
    }));

    renderOverview();

    expect(await screen.findByText("Security skills · 7 of 10 installed")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Install security skills" })).toBeTruthy();
  });

  it("keeps the dashboard usable when security skill status is unavailable", async () => {
    getSecurityOverview.mockResolvedValue(overviewFixture());
    getSecuritySkillsStatus.mockRejectedValueOnce(new Error("skills offline"));

    renderOverview();

    expect(await screen.findByRole("link", { name: "Critical: 2" })).toBeTruthy();
    expect(screen.getByRole("link", { name: "nightly-1" })).toBeTruthy();
    expect(screen.getByText("Security skills · Status unavailable")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Retry" }));
    await waitFor(() => expect(getSecuritySkillsStatus).toHaveBeenCalledTimes(2));
    expect(installSecuritySkills).not.toHaveBeenCalled();
  });
});
