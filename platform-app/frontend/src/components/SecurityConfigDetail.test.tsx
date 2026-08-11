import { create } from "@bufbuild/protobuf";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";

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
} = vi.hoisted(() => ({
  getSecurityScanConfig: vi.fn(),
  getSecurityFindingSummary: vi.fn(),
  listSecurityFindings: vi.fn(),
  listSecurityScans: vi.fn(),
  runSecurityScanNow: vi.fn(),
}));

vi.mock("@/lib/client", () => ({
  client: {
    getSecurityScanConfig,
    getSecurityFindingSummary,
    listSecurityFindings,
    listSecurityScans,
    runSecurityScanNow,
  },
}));

// The full create/edit form has its own test suite; this page only hands the
// current config to edit mode.
vi.mock("@/components/SecurityScanFormDialog", () => ({
  SecurityScanFormDialog: ({ config, trigger }: {
    config?: { name?: string };
    trigger: React.ReactElement;
  }) => <div data-testid={`edit-scan-${config?.name ?? "unknown"}`}>{trigger}</div>,
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
  overrides: Partial<{ title: string; severity: string; scanId: string }> = {},
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
    filePath: "internal/db/query.go",
    startLine: 42,
    score: 9.5,
    status: "open",
    lastSeenAt: timestampFromDate(new Date("2026-02-02T00:00:00Z")),
  });
}

function renderDetail() {
  render(
    <MemoryRouter initialEntries={["/security/configs/user-alice/nightly"]}>
      <Routes>
        <Route path="/security/configs/:namespace/:name" element={<SecurityConfigDetail />} />
      </Routes>
    </MemoryRouter>,
  );
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
    // name appears both in the findings' Run column and the runs table.
    for (const link of screen.getAllByRole("link", { name: "nightly-1" })) {
      expect(link.getAttribute("href")).toBe("/security/user-alice/nightly-1");
    }
    for (const link of screen.getAllByRole("link", { name: "nightly-2" })) {
      expect(link.getAttribute("href")).toBe("/security/user-alice/nightly-2");
    }
    expect(screen.getByText("2 total")).toBeTruthy();
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

  it("pages findings past the server's 200-row limit via Load more", async () => {
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
    fireEvent.click(loadMore);

    await waitFor(() => {
      expect(listSecurityFindings).toHaveBeenCalledWith(
        expect.objectContaining({ limit: 200, offset: 200 }),
      );
    });
    await screen.findByText("Finding 200");
    // The short second page means there is nothing further to load.
    expect(screen.queryByRole("button", { name: "Load more" })).toBeNull();
  });

  it("re-queries findings when a severity filter is chosen", async () => {
    getSecurityScanConfig.mockResolvedValue(configFixture());
    getSecurityFindingSummary.mockResolvedValue({ counts: { total: 1 } });
    listSecurityFindings.mockResolvedValue({ findings: [findingFixture("f-1", "nightly-1")] });
    listSecurityScans.mockResolvedValue({ scans: [runFixture("nightly-1")] });
    renderDetail();

    const select = await screen.findByLabelText("Filter by severity");
    fireEvent.change(select, { target: { value: "critical" } });

    await waitFor(() => {
      expect(listSecurityFindings).toHaveBeenCalledWith(
        expect.objectContaining({ scanName: "nightly", severity: "critical" }),
      );
    });
  });

  it("starts a run from the header and hides Run now while suspended", async () => {
    getSecurityScanConfig.mockResolvedValue(configFixture());
    getSecurityFindingSummary.mockResolvedValue({ counts: {} });
    listSecurityFindings.mockResolvedValue({ findings: [] });
    listSecurityScans.mockResolvedValue({ scans: [] });
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
  });
});
