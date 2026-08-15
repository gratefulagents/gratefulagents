import { create } from "@bufbuild/protobuf";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes, useLocation } from "react-router-dom";

import { SecurityConfigDetail } from "@/components/SecurityConfigDetail";
import {
  SecurityFindingSchema,
  SecurityScanConfigSchema,
  SecurityScanConfigSpecSchema,
  SecurityScanSchema,
} from "@/rpc/platform/service_pb";

const {
  getSecurityScanConfig,
  getSecurityFindingSummary,
  listSecurityFindings,
  listSecurityScans,
  runSecurityScanNow,
  updateSecurityScan,
} = vi.hoisted(() => ({
  getSecurityScanConfig: vi.fn(),
  getSecurityFindingSummary: vi.fn(),
  listSecurityFindings: vi.fn(),
  listSecurityScans: vi.fn(),
  runSecurityScanNow: vi.fn(),
  updateSecurityScan: vi.fn(),
}));

vi.mock("@/lib/client", () => ({
  client: {
    getSecurityScanConfig,
    getSecurityFindingSummary,
    listSecurityFindings,
    listSecurityScans,
    runSecurityScanNow,
    updateSecurityScan,
  },
}));

// The full create/edit form has its own test suite; this page only hands the
// current config to edit mode.
vi.mock("@/components/SecurityScanFormDialog", () => ({
  SecurityScanFormDialog: ({ config, trigger }: {
    config?: { name?: string };
    trigger: React.ReactElement;
  }) => <div data-testid={`edit-scan-${config?.name ?? "unknown"}`}>{trigger}</div>,
  scanConfigUsesSavedCredentials: () => true,
}));

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

function configFixture(overrides: { suspend?: boolean } = {}) {
  return create(SecurityScanConfigSchema, {
    namespace: "user-alice",
    name: "nightly",
    spec: create(SecurityScanConfigSpecSchema, {
      repoUrl: "https://github.com/acme/payments.git",
      schedule: "@daily",
      suspend: overrides.suspend ?? false,
    }),
    phase: "Scheduled",
    conditionReady: "True",
    lastRunName: "nightly-2",
  });
}

function runFixture(runName: string, status = "completed") {
  return create(SecurityScanSchema, {
    id: `id-${runName}`,
    namespace: "user-alice",
    scanName: "nightly",
    runName,
    repository: "github.com/acme/payments",
    status,
    counts: { critical: 1, total: 1 },
    completedAt: timestampFromDate(new Date("2026-02-02T00:00:00Z")),
  });
}

function findingFixture(
  id: string,
  runName: string,
  overrides: Partial<{
    title: string;
    severity: string;
    scanId: string;
    sourceAgent: string;
    filePath: string;
  }> = {},
) {
  return create(SecurityFindingSchema, {
    id,
    scanId: overrides.scanId ?? "",
    namespace: "user-alice",
    scanName: "nightly",
    runName,
    title: overrides.title ?? "SQL injection in payment lookup",
    category: "injection",
    severity: overrides.severity ?? "critical",
    sourceAgent: overrides.sourceAgent ?? "semgrep",
    filePath: overrides.filePath ?? "internal/db/query.go",
    startLine: 42,
    score: 9.5,
    status: "open",
    lastSeenAt: timestampFromDate(new Date("2026-02-02T00:00:00Z")),
  });
}

function LocationProbe() {
  const location = useLocation();
  return <div data-testid="location">{`${location.pathname}${location.search}`}</div>;
}

function currentLocation(): string {
  return screen.getByTestId("location").textContent ?? "";
}

function renderDetail(initialEntry = "/security/configs/user-alice/nightly") {
  render(
    <MemoryRouter initialEntries={[initialEntry]}>
      <Routes>
        <Route path="/security/configs/:namespace/:name" element={<SecurityConfigDetail />} />
        <Route path="*" element={<div>elsewhere</div>} />
      </Routes>
      <LocationProbe />
    </MemoryRouter>,
  );
}

/** Drive a base-ui Select the way a pointer user does. */
async function pickFilter(label: string, option: string | RegExp) {
  fireEvent.click(screen.getByRole("combobox", { name: label }));
  const item = await screen.findByRole("option", { name: option });
  fireEvent.pointerDown(item, { pointerType: "mouse", button: 0 });
  fireEvent.pointerUp(item, { pointerType: "mouse", button: 0 });
  fireEvent.click(item);
}

function mockHappyPath(findings = [findingFixture("f-1", "nightly-1")]) {
  getSecurityScanConfig.mockResolvedValue(configFixture());
  getSecurityFindingSummary.mockResolvedValue({ counts: { total: findings.length } });
  listSecurityFindings.mockResolvedValue({ findings });
  listSecurityScans.mockResolvedValue({ scans: [runFixture("nightly-1")] });
}

describe("SecurityConfigDetail", () => {
  it("aggregates findings and runs across every run of the configuration", async () => {
    getSecurityScanConfig.mockResolvedValue(configFixture());
    getSecurityFindingSummary.mockResolvedValue({
      counts: { total: 2, open: 2, critical: 1, high: 1 },
    });
    listSecurityFindings.mockResolvedValue({
      findings: [
        findingFixture("f-1", "nightly-1"),
        findingFixture("f-2", "nightly-2", { title: "Hardcoded credential", severity: "high" }),
      ],
    });
    listSecurityScans.mockResolvedValue({
      scans: [runFixture("nightly-2"), runFixture("nightly-1")],
    });
    renderDetail();

    // Findings from both runs appear on one page, linked to their run's
    // finding detail route.
    const first = await screen.findByRole("link", { name: "SQL injection in payment lookup" });
    expect(first.getAttribute("href")).toBe("/security/user-alice/nightly-1/findings/f-1");
    const second = screen.getByRole("link", { name: "Hardcoded credential" });
    expect(second.getAttribute("href")).toBe("/security/user-alice/nightly-2/findings/f-2");

    // The findings query is config-scoped (scanName), not run-scoped.
    expect(listSecurityFindings).toHaveBeenCalledWith(
      expect.objectContaining({ namespace: "user-alice", scanName: "nightly" }),
    );
    expect(getSecurityFindingSummary).toHaveBeenCalledWith({
      namespace: "user-alice",
      scanName: "nightly",
    });

    // Both runs are listed with links to their run detail pages. The run
    // name appears both in the findings' Run column and the runs list.
    for (const link of screen.getAllByRole("link", { name: "nightly-1" })) {
      expect(link.getAttribute("href")).toBe("/security/user-alice/nightly-1");
    }
    for (const link of screen.getAllByRole("link", { name: "nightly-2" })) {
      expect(link.getAttribute("href")).toBe("/security/user-alice/nightly-2");
    }
    expect(screen.getByText("2 total")).toBeTruthy();
    expect(
      screen
        .getByRole("link", { name: "View all runs for this configuration" })
        .getAttribute("href"),
    ).toBe("/security/runs?q=nightly");

    // Detail-page header: name, repo (short form), schedule, status, sections.
    expect(screen.getByRole("heading", { level: 1, name: "nightly" })).toBeTruthy();
    expect(screen.getByText("acme/payments")).toBeTruthy();
    expect(screen.getAllByText("@daily").length).toBeGreaterThan(0);
    expect(screen.getByRole("heading", { level: 2, name: "Findings" })).toBeTruthy();
    expect(screen.getByRole("heading", { level: 2, name: "Runs" })).toBeTruthy();
    expect(screen.getByRole("group", { name: "Configuration actions" })).toBeTruthy();
  });

  it("resolves finding links through the owning persisted scan run", async () => {
    // Deterministic executions report findings from per-task AgentRuns whose
    // names have no scan record; links must use the run whose id matches the
    // finding's scanId.
    getSecurityScanConfig.mockResolvedValue(configFixture());
    getSecurityFindingSummary.mockResolvedValue({ counts: { total: 1 } });
    listSecurityFindings.mockResolvedValue({
      findings: [
        findingFixture("f-1", "nightly-1-injection-hunt-1-0", { scanId: "id-nightly-1" }),
      ],
    });
    listSecurityScans.mockResolvedValue({ scans: [runFixture("nightly-1")] });
    renderDetail();

    const title = await screen.findByRole("link", { name: "SQL injection in payment lookup" });
    expect(title.getAttribute("href")).toBe("/security/user-alice/nightly-1/findings/f-1");
    // The Run column shows the resolved scan run, not the task AgentRun.
    expect(screen.queryByText("nightly-1-injection-hunt-1-0")).toBeNull();
  });

  it("sends every canonical filter param to the findings query", async () => {
    mockHappyPath();
    renderDetail(
      "/security/configs/user-alice/nightly?q=sql&severity=high&status=all&category=injection"
      + "&baseline=new&assignee=alice&suppressed=only&dupes=include",
    );

    await waitFor(() => expect(listSecurityFindings).toHaveBeenCalled());
    expect(listSecurityFindings).toHaveBeenCalledWith({
      namespace: "user-alice",
      scanName: "nightly",
      severity: "high",
      status: "",
      category: "injection",
      search: "sql",
      baselineState: "new",
      assignee: "alice",
      suppressed: "only",
      includeDuplicates: true,
      limit: 200,
      offset: 0,
    });
    // Eight non-default filters are reported by the shared filter bar.
    expect(await screen.findByLabelText("8 active filters")).toBeTruthy();
  });

  it("narrows loaded findings client-side by tool and file path", async () => {
    mockHappyPath([
      findingFixture("f-1", "nightly-1", { sourceAgent: "semgrep" }),
      findingFixture("f-2", "nightly-1", {
        title: "Hardcoded credential",
        sourceAgent: "trufflehog",
        filePath: "cmd/main.go",
      }),
    ]);
    renderDetail();

    await screen.findByRole("link", { name: "Hardcoded credential" });
    expect(screen.getByText("2 of 2 findings")).toBeTruthy();

    fireEvent.change(screen.getByLabelText("Filter by file path"), {
      target: { value: "cmd/" },
    });
    await waitFor(() => {
      expect(screen.queryByRole("link", { name: "SQL injection in payment lookup" })).toBeNull();
    });
    expect(screen.getByRole("link", { name: "Hardcoded credential" })).toBeTruthy();
    expect(screen.getByText("1 of 2 findings")).toBeTruthy();

    // Tool is a loaded-page filter too: it never re-queries the server.
    const callsBefore = listSecurityFindings.mock.calls.length;
    await pickFilter("Tool", "semgrep");
    await waitFor(() => {
      expect(screen.queryByRole("link", { name: "Hardcoded credential" })).toBeNull();
    });
    expect(listSecurityFindings.mock.calls.length).toBe(callsBefore);
  });

  it("clears every filter and the URL from the filter bar", async () => {
    mockHappyPath();
    renderDetail("/security/configs/user-alice/nightly?severity=critical&assignee=alice");

    fireEvent.click(await screen.findByRole("button", { name: /Clear/ }));

    await waitFor(() => {
      expect(currentLocation()).toBe("/security/configs/user-alice/nightly");
    });
  });

  it("pages findings past the server's 200-row limit and shows the loaded count", async () => {
    getSecurityScanConfig.mockResolvedValue(configFixture());
    getSecurityFindingSummary.mockResolvedValue({ counts: { total: 201 } });
    listSecurityScans.mockResolvedValue({ scans: [runFixture("nightly-1")] });
    const fullPage = Array.from({ length: 200 }, (_, i) =>
      findingFixture(`f-${i}`, "nightly-1", { title: `Finding ${i}` }),
    );
    listSecurityFindings
      .mockResolvedValueOnce({ findings: fullPage })
      .mockResolvedValueOnce({ findings: fullPage })
      .mockResolvedValueOnce({ findings: [findingFixture("f-200", "nightly-1", { title: "Finding 200" })] });
    renderDetail();

    const loadMore = await screen.findByRole("button", { name: "Load more" });
    expect(listSecurityFindings).toHaveBeenCalledWith(
      expect.objectContaining({ limit: 200, offset: 0 }),
    );
    expect(screen.getByText("Showing 200 of 201 findings")).toBeTruthy();
    fireEvent.click(loadMore);

    await waitFor(() => {
      expect(listSecurityFindings).toHaveBeenCalledWith(
        expect.objectContaining({ limit: 200, offset: 200 }),
      );
    });
    await screen.findByText("Finding 200");
    // The short second page means there is nothing further to load.
    expect(screen.queryByRole("button", { name: "Load more" })).toBeNull();
    expect(screen.getByText("Showing all 201 findings")).toBeTruthy();
  });

  it("restarts paging at the first page when a filter changes", async () => {
    getSecurityScanConfig.mockResolvedValue(configFixture());
    getSecurityFindingSummary.mockResolvedValue({ counts: { total: 400 } });
    listSecurityScans.mockResolvedValue({ scans: [runFixture("nightly-1")] });
    const page = (start: number) =>
      Array.from({ length: 200 }, (_, i) =>
        findingFixture(`f-${start + i}`, "nightly-1", { title: `Finding ${start + i}` }),
      );
    listSecurityFindings.mockImplementation((request: { offset: number }) => ({
      findings: page(request.offset),
    }));
    renderDetail();

    fireEvent.click(await screen.findByRole("button", { name: "Load more" }));
    await waitFor(() => {
      expect(listSecurityFindings).toHaveBeenCalledWith(
        expect.objectContaining({ offset: 200 }),
      );
    });

    listSecurityFindings.mockClear();
    listSecurityFindings.mockResolvedValue({
      findings: [findingFixture("f-1", "nightly-1", { title: "Only critical" })],
    });
    fireEvent.click(screen.getByRole("button", { name: "Critical" }));

    await waitFor(() => {
      expect(listSecurityFindings).toHaveBeenCalledWith(
        expect.objectContaining({ severity: "critical", offset: 0 }),
      );
    });
    // The accumulated second page is dropped, not re-requested at its old
    // offset against a different query.
    expect(listSecurityFindings.mock.calls).toHaveLength(1);
    expect(currentLocation()).toBe("/security/configs/user-alice/nightly?severity=critical");
    await screen.findByText("Showing all 1 matching findings");
  });

  it("opens a finding from the row with the keyboard, keeping the filters", async () => {
    mockHappyPath();
    renderDetail("/security/configs/user-alice/nightly?severity=critical");

    await screen.findByRole("link", { name: "SQL injection in payment lookup" });
    const row = screen.getAllByRole("row")[1];
    fireEvent.keyDown(row, { key: "Enter" });

    await waitFor(() => {
      expect(currentLocation()).toBe(
        "/security/user-alice/nightly-1/findings/f-1?severity=critical",
      );
    });
  });

  it("shows a typed not-found state with recovery links for a missing config", async () => {
    getSecurityScanConfig.mockRejectedValue(new Error("[not_found] scan config not found"));
    getSecurityFindingSummary.mockResolvedValue({ counts: {} });
    listSecurityFindings.mockResolvedValue({ findings: [] });
    listSecurityScans.mockResolvedValue({ scans: [] });
    renderDetail();

    expect(await screen.findByText("Scan configuration not found")).toBeTruthy();
    expect(screen.getByText(/No scan configuration named "nightly" exists in user-alice/)).toBeTruthy();
    expect(
      screen.getByRole("link", { name: "Configurations" }).getAttribute("href"),
    ).toBe("/security/configs");
    expect(
      screen.getByRole("link", { name: "Security overview" }).getAttribute("href"),
    ).toBe("/security");

    // Retry re-issues the request.
    getSecurityScanConfig.mockResolvedValue(configFixture());
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByRole("heading", { level: 1, name: "nightly" })).toBeTruthy();
  });

  it("distinguishes a forbidden configuration from a missing one", async () => {
    getSecurityScanConfig.mockRejectedValue(
      new Error("[permission_denied] you do not own this scan"),
    );
    getSecurityFindingSummary.mockResolvedValue({ counts: {} });
    listSecurityFindings.mockResolvedValue({ findings: [] });
    listSecurityScans.mockResolvedValue({ scans: [] });
    renderDetail();

    expect(await screen.findByText("You don't have access")).toBeTruthy();
    expect(screen.queryByText("Scan configuration not found")).toBeNull();
    expect(screen.getByRole("link", { name: "Configurations" })).toBeTruthy();
  });

  it("keeps the configuration visible when only the findings query fails", async () => {
    getSecurityScanConfig.mockResolvedValue(configFixture());
    getSecurityFindingSummary.mockResolvedValue({ counts: { total: 3 } });
    listSecurityScans.mockResolvedValue({ scans: [runFixture("nightly-1")] });
    listSecurityFindings.mockRejectedValueOnce(new Error("findings store offline"));
    renderDetail();

    expect(await screen.findByRole("heading", { level: 1, name: "nightly" })).toBeTruthy();
    expect(await screen.findByText(/findings store offline/)).toBeTruthy();
    // Not a dead end: the page is intact and the failure is retryable.
    expect(screen.getByRole("link", { name: "nightly-1" })).toBeTruthy();
    expect(screen.queryByText("Scan configuration not found")).toBeNull();

    listSecurityFindings.mockResolvedValue({ findings: [findingFixture("f-1", "nightly-1")] });
    fireEvent.click(screen.getByRole("button", { name: "Retry" }));

    expect(
      await screen.findByRole("link", { name: "SQL injection in payment lookup" }),
    ).toBeTruthy();
    expect(screen.queryByText(/findings store offline/)).toBeNull();
  });

  it("starts a run from the header and swaps Run now for Resume while suspended", async () => {
    mockHappyPath([]);
    runSecurityScanNow.mockResolvedValue({});
    renderDetail();

    fireEvent.click(await screen.findByRole("button", { name: /Run now/ }));
    await waitFor(() => {
      expect(runSecurityScanNow).toHaveBeenCalledWith({ namespace: "user-alice", name: "nightly" });
    });

    cleanup();
    getSecurityScanConfig.mockResolvedValue(configFixture({ suspend: true }));
    renderDetail();
    await screen.findByText("Suspended");
    expect(screen.queryByRole("button", { name: /Run now/ })).toBeNull();
    expect(screen.getByRole("button", { name: /Resume/ })).toBeTruthy();
  });

  it("suspends the configuration from the header actions", async () => {
    mockHappyPath([]);
    updateSecurityScan.mockResolvedValue({});
    renderDetail();

    fireEvent.click(await screen.findByRole("button", { name: /Suspend/ }));

    await waitFor(() => expect(updateSecurityScan).toHaveBeenCalled());
    const request = updateSecurityScan.mock.calls[0][0] as {
      namespace: string;
      name: string;
      spec?: { suspend: boolean };
    };
    expect(request.namespace).toBe("user-alice");
    expect(request.name).toBe("nightly");
    expect(request.spec?.suspend).toBe(true);
  });
});
