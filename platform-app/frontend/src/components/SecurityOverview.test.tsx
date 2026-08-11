import { create } from "@bufbuild/protobuf";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";

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
});

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

    expect(await screen.findByText("Actionable critical")).toBeTruthy();
    expect(screen.getByText("Actionable high")).toBeTruthy();
    // Active scan links to its detail page.
    expect(
      screen.getByRole("link", { name: "nightly-2" }).getAttribute("href"),
    ).toBe("/security/user-alice/nightly-2");
    expect(
      screen.getByRole("link", { name: "nightly-1" }).getAttribute("href"),
    ).toBe("/security/user-alice/nightly-1");
    // The per-configuration posture section renders from its own RPC.
    expect(
      await screen.findByText("Per-configuration posture across recent runs."),
    ).toBeTruthy();
    // Both scan tables surface the owning configuration in a dedicated column
    // (the postures table adds a third "Configuration" header).
    await waitFor(() => expect(screen.getAllByText("Configuration").length).toBe(3));
    expect(screen.getAllByText("nightly").length).toBeGreaterThanOrEqual(2);
    expect(
      screen.getByRole("link", { name: "nightly" }).getAttribute("href"),
    ).toBe("/security/configs");
    expect(getSecurityConfigPostures).toHaveBeenCalledWith({
      namespace: "",
      activityLimit: 0,
    });
    // Failing configuration surfaces with its reason.
    expect(screen.getByText("RunCreationFailed")).toBeTruthy();
    expect(screen.getByText("run creation failed")).toBeTruthy();
    // The shared security sub-navigation replaces the old header buttons.
    expect(
      screen.getByRole("link", { name: /Scan runs/ }).getAttribute("href"),
    ).toBe("/security/runs");
    expect(
      screen.getByRole("link", { name: /Configurations/ }).getAttribute("href"),
    ).toBe("/security/configs");
    expect(
      screen.getByRole("link", { name: /Library/ }).getAttribute("href"),
    ).toBe("/security/library");
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

    const deltas = await screen.findByLabelText("Baseline changes");
    expect(deltas.textContent).toContain("Since the last baseline:");
    expect(deltas.textContent).toContain("new2");
    expect(deltas.textContent).toContain("recurring5");
    expect(deltas.textContent).toContain("resolved1");
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
    expect(screen.queryByText("Actionable critical")).toBeNull();
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

  it("keeps the overview usable when security skill status is unavailable", async () => {
    getSecurityOverview.mockResolvedValue(overviewFixture());
    getSecuritySkillsStatus.mockRejectedValueOnce(new Error("skills offline"));

    renderOverview();

    expect(await screen.findByText("Actionable critical")).toBeTruthy();
    expect(screen.getByText("Security skills · Status unavailable")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Retry" }));
    await waitFor(() => expect(getSecuritySkillsStatus).toHaveBeenCalledTimes(2));
    expect(installSecuritySkills).not.toHaveBeenCalled();
  });
});
