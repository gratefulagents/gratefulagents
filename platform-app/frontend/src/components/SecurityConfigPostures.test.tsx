import { create } from "@bufbuild/protobuf";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";

import { SecurityConfigPostures } from "@/components/SecurityConfigPostures";
import {
  GetSecurityConfigPosturesResponseSchema,
  SecurityConfigPostureSchema,
  SecurityScanConfigSchema,
  type SecurityConfigPosture,
} from "@/rpc/platform/service_pb";

const { getSecurityConfigPostures, listSecurityScanConfigs } = vi.hoisted(() => ({
  getSecurityConfigPostures: vi.fn(),
  listSecurityScanConfigs: vi.fn(),
}));

vi.mock("@/lib/client", () => ({
  client: { getSecurityConfigPostures, listSecurityScanConfigs },
}));

beforeEach(() => {
  listSecurityScanConfigs.mockResolvedValue({
    configs: ["api-scan", "web-scan"].map((name) =>
      create(SecurityScanConfigSchema, { namespace: "user-alice", name }),
    ),
  });
});

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

function posture(
  scanName: string,
  findingCounts: Record<string, number>,
  opts: { activityTotals?: number[]; ageMinutes?: number } = {},
): SecurityConfigPosture {
  const completedAt = timestampFromDate(
    new Date(Date.now() - (opts.ageMinutes ?? 30) * 60 * 1000),
  );
  return create(SecurityConfigPostureSchema, {
    scanName,
    findingCounts,
    repository: "github.com/acme/payments",
    lastRunName: `${scanName}-run-9`,
    lastRunStatus: "completed",
    lastStartedAt: timestampFromDate(new Date(Date.now() - 60 * 60 * 1000)),
    lastCompletedAt: completedAt,
    activity: (opts.activityTotals ?? []).map((total, i) => ({
      runName: `${scanName}-run-${i + 1}`,
      completedAt,
      severityCounts: {},
      total,
    })),
  });
}

function fixture() {
  return create(GetSecurityConfigPosturesResponseSchema, {
    storeSupported: true,
    postures: [
      posture(
        "web-scan",
        { total: 4, open: 2, open_high: 2 },
        { activityTotals: [4], ageMinutes: 10 },
      ),
      posture(
        "api-scan",
        {
          total: 9,
          open: 5,
          open_critical: 3,
          open_high: 1,
          open_low: 1,
          baseline_new: 2,
          baseline_regressed: 1,
        },
        { activityTotals: [7, 9, 5], ageMinutes: 120 },
      ),
    ],
  });
}

function renderPostures() {
  render(
    <MemoryRouter>
      <SecurityConfigPostures />
    </MemoryRouter>,
  );
}

function rowNames(): string[] {
  return screen
    .getAllByRole("row")
    .slice(1)
    .map((row) => (row.textContent ?? "").includes("api-scan") ? "api-scan" : "web-scan");
}

describe("SecurityConfigPostures", () => {
  it("renders one row per configuration ordered by open critical findings", async () => {
    getSecurityConfigPostures.mockResolvedValue(fixture());

    renderPostures();

    expect(await screen.findByText("Configurations")).toBeTruthy();
    expect(
      screen.getByText("Per-configuration posture across recent runs."),
    ).toBeTruthy();
    expect(
      screen.getByRole("link", { name: "Manage configurations" }).getAttribute("href"),
    ).toBe("/security/configs");
    // api-scan has open criticals, so it sorts first by default.
    expect(rowNames()).toEqual(["api-scan", "web-scan"]);
    // Configuration names deep-link into that configuration's detail page.
    await waitFor(() =>
      expect(
        screen.getByRole("link", { name: "api-scan" }).getAttribute("href"),
      ).toBe("/security/configs/user-alice/api-scan"),
    );
    // Baseline changes show only non-zero states.
    expect(screen.getByText("new")).toBeTruthy();
    expect(screen.getByText("regressed")).toBeTruthy();
    expect(screen.queryByText("resolved")).toBeNull();
    expect(getSecurityConfigPostures).toHaveBeenCalledWith({
      namespace: "",
      activityLimit: 0,
    });
  });

  it("deep-links every posture metric into that configuration's findings view", async () => {
    getSecurityConfigPostures.mockResolvedValue(fixture());

    renderPostures();

    // The actionable count opens the configuration's actionable findings.
    const actionable = await screen.findByRole("link", {
      name: "5 actionable findings in api-scan",
    });
    await waitFor(() =>
      expect(actionable.getAttribute("href")).toBe(
        "/security/configs/user-alice/api-scan?status=actionable",
      ),
    );
    // Baseline changes widen the status filter: a resolved finding is not
    // actionable, so the default view would hide what the badge counted.
    expect(
      screen
        .getByRole("link", { name: "2 new findings in api-scan" })
        .getAttribute("href"),
    ).toBe("/security/configs/user-alice/api-scan?baseline=new&status=all");
    expect(
      screen
        .getByRole("link", { name: "1 regressed findings in api-scan" })
        .getAttribute("href"),
    ).toBe("/security/configs/user-alice/api-scan?baseline=regressed&status=all");
    expect(listSecurityScanConfigs).toHaveBeenCalledWith({ namespace: "" });
  });

  it("falls back to the filtered configurations list when the namespace is unknown", async () => {
    getSecurityConfigPostures.mockResolvedValue(fixture());
    listSecurityScanConfigs.mockRejectedValue(new Error("configs offline"));

    renderPostures();

    // A cosmetic lookup failure must not break the section or strand a link.
    expect(
      (await screen.findByRole("link", { name: "api-scan" })).getAttribute("href"),
    ).toBe("/security/configs?q=api-scan");
    expect(
      screen
        .getByRole("link", { name: "5 actionable findings in api-scan" })
        .getAttribute("href"),
    ).toBe("/security/configs?q=api-scan");
    expect(screen.getByText("Configurations")).toBeTruthy();
  });

  it("renders the stacked severity bar and the trend sparkline", async () => {
    getSecurityConfigPostures.mockResolvedValue(fixture());

    renderPostures();

    expect(
      await screen.findByRole("img", {
        name: "Actionable findings for api-scan by severity: 3 critical, 1 high, 1 low",
      }),
    ).toBeTruthy();
    expect(
      screen.getByRole("img", {
        name: "Finding trend for api-scan: api-scan-run-1: 7 findings; api-scan-run-2: 9 findings; api-scan-run-3: 5 findings",
      }),
    ).toBeTruthy();
    // Fewer than two activity points renders a muted dash instead of a sparkline.
    expect(
      screen.queryByRole("img", { name: /Finding trend for web-scan/ }),
    ).toBeNull();
  });

  it("toggles the sort direction when a column header is clicked", async () => {
    getSecurityConfigPostures.mockResolvedValue(fixture());

    renderPostures();

    await screen.findByText("Configurations");
    expect(rowNames()).toEqual(["api-scan", "web-scan"]);

    // Default sort is already Open desc, so the first click flips to asc.
    fireEvent.click(screen.getByRole("button", { name: "Actionable" }));
    expect(rowNames()).toEqual(["web-scan", "api-scan"]);

    fireEvent.click(screen.getByRole("button", { name: "Actionable" }));
    expect(rowNames()).toEqual(["api-scan", "web-scan"]);

    // Last run sorts by recency: desc puts the newest run first.
    fireEvent.click(screen.getByRole("button", { name: "Last run" }));
    expect(rowNames()).toEqual(["web-scan", "api-scan"]);
  });

  it("shows the empty state when no postures are persisted", async () => {
    getSecurityConfigPostures.mockResolvedValue(
      create(GetSecurityConfigPosturesResponseSchema, { storeSupported: true }),
    );

    renderPostures();

    expect(await screen.findByText("No persisted scan data yet.")).toBeTruthy();
  });

  it("surfaces server warnings instead of the empty state and offers a retry", async () => {
    getSecurityConfigPostures.mockResolvedValueOnce(
      create(GetSecurityConfigPosturesResponseSchema, {
        storeSupported: true,
        warnings: ["aggregating security configuration postures: db offline"],
      }),
    );
    getSecurityConfigPostures.mockResolvedValueOnce(fixture());

    renderPostures();

    expect(
      await screen.findByText("aggregating security configuration postures: db offline"),
    ).toBeTruthy();
    // A partial-result outage is not presented as a legitimate empty dataset.
    expect(screen.queryByText("No persisted scan data yet.")).toBeNull();

    fireEvent.click(screen.getByRole("button", { name: "Retry" }));

    expect(await screen.findByText("api-scan")).toBeTruthy();
    expect(getSecurityConfigPostures).toHaveBeenCalledTimes(2);
  });

  it("shows an inline error with a retry button", async () => {
    getSecurityConfigPostures.mockRejectedValueOnce(new Error("postures offline"));
    getSecurityConfigPostures.mockResolvedValueOnce(fixture());

    renderPostures();

    expect(await screen.findByText("postures offline")).toBeTruthy();

    fireEvent.click(screen.getByRole("button", { name: "Retry" }));

    expect(await screen.findByText("api-scan")).toBeTruthy();
    expect(getSecurityConfigPostures).toHaveBeenCalledTimes(2);
  });
});
