import { create } from "@bufbuild/protobuf";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes, useLocation } from "react-router-dom";

import { SecurityScanDetail, blockingPolicyDisposition } from "@/components/SecurityScanDetail";
import {
  SecurityFindingEventSchema,
  SecurityFindingSchema,
  SecurityScanSchema,
} from "@/rpc/platform/service_pb";

const { getSecurityScan, getSecurityFinding, getSecurityFindingSummary, getSecurityScanConfig, listSecurityFindings, updateSecurityFindingStatus, getSecurityScanReport, getSecurityFindingSubmissionBundle, resumeSecurityScan, downloadBlob } =
  vi.hoisted(() => ({
    getSecurityScan: vi.fn(),
    getSecurityFinding: vi.fn(),
    getSecurityFindingSummary: vi.fn(),
    getSecurityScanConfig: vi.fn().mockRejectedValue(new Error("not configured")),
    listSecurityFindings: vi.fn(),
    updateSecurityFindingStatus: vi.fn(),
    getSecurityScanReport: vi.fn(),
    getSecurityFindingSubmissionBundle: vi.fn(),
    resumeSecurityScan: vi.fn(),
    downloadBlob: vi.fn(),
  }));

vi.mock("@/lib/client", () => ({
  client: {
    getSecurityScan,
    getSecurityFinding,
    getSecurityFindingSummary,
    getSecurityScanConfig,
    listSecurityFindings,
    updateSecurityFindingStatus,
    getSecurityScanReport,
    getSecurityFindingSubmissionBundle,
    resumeSecurityScan,
  },
}));

vi.mock("@/lib/download", () => ({
  downloadBlob,
}));

// The run panel has its own test suite (SecurityScanRunPanel.test.tsx) and
// opens watch streams; stub it out here.
vi.mock("@/components/SecurityScanRunPanel", () => ({
  SecurityScanRunPanel: () => <div data-testid="scan-run-panel" />,
}));

vi.mock("@/components/SecurityResearchPanel", () => ({
  SecurityResearchPanel: () => <div data-testid="security-research-panel" />,
}));

// The full create/edit form has its own test suite. This detail-page suite
// only verifies that the current scan config is handed to duplicate mode.
vi.mock("@/components/SecurityScanFormDialog", () => ({
  SecurityScanFormDialog: ({ duplicateFrom, trigger }: {
    duplicateFrom?: { name?: string };
    trigger: React.ReactElement;
  }) => <div data-testid={`duplicate-scan-${duplicateFrom?.name ?? "unknown"}`}>{trigger}</div>,
}));

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

const FINDING_ID = "22222222-2222-2222-2222-222222222222";

function scanFixture() {
  return create(SecurityScanSchema, {
    id: "11111111-1111-1111-1111-111111111111",
    namespace: "user-alice",
    scanName: "nightly",
    runName: "nightly-1",
    repository: "github.com/acme/payments",
    revision: "abc123def456",
    status: "completed",
    summary: "One critical finding in the payments service.",
    counts: { critical: 1, total: 1, open: 1 },
  });
}

function findingFixture(status = "open") {
  return create(SecurityFindingSchema, {
    id: FINDING_ID,
    namespace: "user-alice",
    scanName: "nightly",
    runName: "nightly-1",
    title: "SQL injection in payment lookup",
    category: "injection",
    severity: "critical",
    confidence: "high",
    repository: "github.com/acme/payments",
    filePath: "internal/db/query.go",
    startLine: 42,
    endLine: 48,
    cwe: ["CWE-89"],
    description: "User input is concatenated into a SQL string.",
    impact: "Full database read access.",
    attackVector: "Crafted payment ID in the lookup endpoint.",
    remediation: "Use parameterized queries.",
    references: ["https://example.com/advisory"],
    sourceAgent: "scanner-agent",
    score: 9.5,
    status,
    occurrences: 2,
    raw: '{"snippet":"SELECT * FROM payments WHERE id = \'" + id + "\'"}',
    firstSeenAt: timestampFromDate(new Date("2026-02-01T00:00:00Z")),
    lastSeenAt: timestampFromDate(new Date("2026-02-02T00:00:00Z")),
  });
}

function findingEventsResponse() {
  return {
    finding: findingFixture(),
    events: [
      create(SecurityFindingEventSchema, {
        id: 2n,
        eventType: "status_changed",
        actor: "alice",
        note: "confirmed exploitable",
        createdAt: timestampFromDate(new Date("2026-02-03T10:00:00Z")),
      }),
      create(SecurityFindingEventSchema, {
        id: 1n,
        eventType: "created",
        actor: "scanner-agent",
        createdAt: timestampFromDate(new Date("2026-02-01T00:00:00Z")),
      }),
    ],
  };
}

function LocationProbe() {
  return <span data-testid="search">{useLocation().search}</span>;
}

/** Current query string, as the router sees it. */
function search(): string {
  return screen.getByTestId("search").textContent ?? "";
}

function renderDetail(initialEntry = "/security/user-alice/nightly-1") {
  return render(
    <MemoryRouter initialEntries={[initialEntry]}>
      <Routes>
        <Route path="/security/:namespace/:runName" element={<SecurityScanDetail />} />
      </Routes>
      <LocationProbe />
    </MemoryRouter>,
  );
}

/** The findings table row whose title cell contains `title`. */
function findingRow(title: string): HTMLElement {
  const row = within(screen.getByRole("table")).getByText(title).closest("tr");
  if (!row) throw new Error(`no findings row for ${title}`);
  return row;
}

/** Open the split panel the way a mouse user does. */
function openFinding(title: string) {
  fireEvent.click(findingRow(title));
}

describe("SecurityScanDetail", () => {
  it("offers to create a new scan from the current scan configuration", async () => {
    getSecurityScan.mockResolvedValue(scanFixture());
    getSecurityFindingSummary.mockResolvedValue({ counts: {} });
    listSecurityFindings.mockResolvedValue({ findings: [] });
    getSecurityScanConfig.mockResolvedValue({
      namespace: "user-alice",
      name: "nightly",
      spec: { repoUrl: "https://github.com/acme/payments.git", schedule: "@daily" },
    });

    renderDetail();

    expect(await screen.findByRole("button", { name: "Duplicate scan" })).toBeTruthy();
    expect(screen.getByTestId("duplicate-scan-nightly")).toBeTruthy();
  });

  it("does not offer duplication for a stale scan configuration", async () => {
    getSecurityScan.mockResolvedValue(scanFixture());
    getSecurityFindingSummary.mockResolvedValue({ counts: {} });
    listSecurityFindings.mockResolvedValue({ findings: [] });
    getSecurityScanConfig.mockResolvedValue({
      namespace: "user-alice",
      name: "previous-scan",
      spec: { repoUrl: "https://github.com/acme/previous.git" },
    });

    renderDetail();

    expect(await screen.findByRole("heading", { name: "nightly-1" })).toBeTruthy();
    await waitFor(() => expect(getSecurityScanConfig).toHaveBeenCalled());
    expect(screen.queryByRole("button", { name: "Duplicate scan" })).toBeNull();
  });

  it("renders the scan header, severity summary, and findings table", async () => {
    getSecurityScan.mockResolvedValue(scanFixture());
    getSecurityFindingSummary.mockResolvedValue({ counts: { critical: 1, total: 1, open: 1 } });
    listSecurityFindings.mockResolvedValue({ findings: [findingFixture()] });

    renderDetail();

    expect(await screen.findByRole("heading", { name: "nightly-1" })).toBeTruthy();
    expect(screen.getByText(/github\.com\/acme\/payments/)).toBeTruthy();
    expect(screen.getByText("One critical finding in the payments service.")).toBeTruthy();
    expect(screen.getByText("Total")).toBeTruthy();
    expect(await screen.findByText("SQL injection in payment lookup")).toBeTruthy();
    // "injection" appears both as a table cell and as a category filter option.
    expect(screen.getAllByText("injection").length).toBeGreaterThan(0);
    expect(screen.getByText("9.5")).toBeTruthy();
    expect(getSecurityScan).toHaveBeenCalledWith({ namespace: "user-alice", runName: "nightly-1" });
  });

  it("opens the finding panel with details and CWE links", async () => {
    getSecurityScan.mockResolvedValue(scanFixture());
    getSecurityFindingSummary.mockResolvedValue({ counts: {} });
    listSecurityFindings.mockResolvedValue({ findings: [findingFixture()] });
    getSecurityFinding.mockResolvedValue(findingEventsResponse());

    renderDetail();

    await screen.findByText("SQL injection in payment lookup");
    openFinding("SQL injection in payment lookup");

    expect(screen.getByText("User input is concatenated into a SQL string.")).toBeTruthy();
    expect(screen.getByText("Full database read access.")).toBeTruthy();
    expect(screen.getByText("Crafted payment ID in the lookup endpoint.")).toBeTruthy();
    expect(screen.getByText("Use parameterized queries.")).toBeTruthy();
    expect(screen.getByText("scanner-agent")).toBeTruthy();
    expect(screen.getByRole("link", { name: "CWE-89" }).getAttribute("href")).toBe(
      "https://cwe.mitre.org/data/definitions/89.html",
    );

    fireEvent.click(screen.getByRole("button", { name: "Close finding details" }));
    expect(screen.queryByText("Full database read access.")).toBeNull();
  });

  it("spells out missing repository in the header instead of an empty subtitle", async () => {
    const scan = scanFixture();
    scan.repository = "";
    scan.revision = "";
    getSecurityScan.mockResolvedValue(scan);
    getSecurityFindingSummary.mockResolvedValue({ counts: {} });
    listSecurityFindings.mockResolvedValue({ findings: [] });

    renderDetail();

    expect(await screen.findByRole("heading", { name: "nightly-1" })).toBeTruthy();
    expect(screen.getByText("Repository not recorded")).toBeTruthy();
  });

  it("names every missing field explicitly in the finding panel", async () => {
    const empty = create(SecurityFindingSchema, {
      id: FINDING_ID,
      namespace: "user-alice",
      scanName: "nightly",
      runName: "nightly-1",
      title: "Bare finding",
      severity: "low",
      status: "open",
      score: 1.0,
      occurrences: 1,
    });
    getSecurityScan.mockResolvedValue(scanFixture());
    getSecurityFindingSummary.mockResolvedValue({ counts: {} });
    listSecurityFindings.mockResolvedValue({ findings: [empty] });
    getSecurityFinding.mockResolvedValue({ finding: empty, events: [] });

    renderDetail();

    await screen.findByText("Bare finding");
    openFinding("Bare finding");

    const panel = screen.getByRole("complementary", { name: "Finding details" });
    // Description, Impact, Attack Vector, and Remediation stay visible with an
    // explicit placeholder instead of vanishing.
    expect(within(panel).getByText("Remediation")).toBeTruthy();
    expect(within(panel).getAllByText("Not provided")).toHaveLength(4);
    expect(within(panel).getByText("Location not provided")).toBeTruthy();
    expect(within(panel).getByText("Confidence not provided")).toBeTruthy();
    expect(within(panel).getByText("Source agent not recorded")).toBeTruthy();
    expect(within(panel).getAllByText("Not recorded")).toHaveLength(2);
    expect(within(panel).getByText("No CWE assigned")).toBeTruthy();
    expect(within(panel).getByText("No references provided")).toBeTruthy();
  });

  it("re-queries findings when a filter changes", async () => {
    getSecurityScan.mockResolvedValue(scanFixture());
    getSecurityFindingSummary.mockResolvedValue({ counts: {} });
    listSecurityFindings.mockResolvedValue({ findings: [findingFixture()] });

    renderDetail();
    await screen.findByText("SQL injection in payment lookup");

    fireEvent.click(screen.getByRole("button", { name: "Critical" }));

    await waitFor(() => {
      expect(listSecurityFindings).toHaveBeenLastCalledWith({
        namespace: "user-alice",
        runName: "nightly-1",
        severity: "critical",
        status: "actionable",
        category: "",
        search: "",
        baselineState: "",
        assignee: "",
        suppressed: "exclude",
        includeDuplicates: false,
      });
    });
    expect(search()).toBe("?severity=critical");
  });

  it("links each finding to its full page carrying the active filters", async () => {
    getSecurityScan.mockResolvedValue(scanFixture());
    getSecurityFindingSummary.mockResolvedValue({ counts: {} });
    listSecurityFindings.mockResolvedValue({ findings: [findingFixture()] });

    renderDetail("/security/user-alice/nightly-1?tool=scanner-agent");
    await screen.findByText("SQL injection in payment lookup");

    fireEvent.click(screen.getByRole("button", { name: "Critical" }));

    await waitFor(() => {
      expect(screen.getByRole("link", { name: "Open full page" }).getAttribute("href")).toBe(
        `/security/user-alice/nightly-1/findings/${FINDING_ID}?severity=critical&tool=scanner-agent`,
      );
    });
  });

  it("updates the finding status from the panel and refreshes", async () => {
    getSecurityScan.mockResolvedValue(scanFixture());
    getSecurityFindingSummary.mockResolvedValue({ counts: {} });
    listSecurityFindings
      .mockResolvedValueOnce({ findings: [findingFixture()] })
      .mockResolvedValue({ findings: [findingFixture("triaged")] });
    getSecurityFinding.mockResolvedValue(findingEventsResponse());
    updateSecurityFindingStatus.mockResolvedValue(findingFixture("triaged"));

    renderDetail();

    await screen.findByText("SQL injection in payment lookup");
    openFinding("SQL injection in payment lookup");
    await waitFor(() => {
      expect(getSecurityFinding).toHaveBeenCalledWith({ id: FINDING_ID, namespace: "user-alice" });
    });

    // "Status" also names the filter dropdown; the panel control is the native select.
    fireEvent.change(screen.getByLabelText("Status", { selector: "select" }), {
      target: { value: "triaged" },
    });

    await waitFor(() => {
      expect(updateSecurityFindingStatus).toHaveBeenCalledWith({
        id: FINDING_ID,
        status: "triaged",
        note: "",
        namespace: "user-alice",
      });
    });
    // Optimistic update plus refetch leave the row in the new status.
    await waitFor(() => {
      expect(listSecurityFindings.mock.calls.length).toBeGreaterThan(1);
    });
    // The audit history is refetched after the status change.
    await waitFor(() => {
      expect(getSecurityFinding.mock.calls.length).toBeGreaterThan(1);
    });
    expect(
      (screen.getByLabelText("Status", { selector: "select" }) as HTMLSelectElement).value,
    ).toBe("triaged");
  });

  it("renders the finding's audit history in the panel", async () => {
    getSecurityScan.mockResolvedValue(scanFixture());
    getSecurityFindingSummary.mockResolvedValue({ counts: {} });
    listSecurityFindings.mockResolvedValue({ findings: [findingFixture()] });
    getSecurityFinding.mockResolvedValue(findingEventsResponse());

    renderDetail();

    await screen.findByText("SQL injection in payment lookup");
    openFinding("SQL injection in payment lookup");

    expect(await screen.findByText("status changed")).toBeTruthy();
    expect(screen.getByText(/· alice/)).toBeTruthy();
    expect(screen.getByText("confirmed exploitable")).toBeTruthy();
    expect(screen.getByText("created")).toBeTruthy();
    // sourceAgent also appears in the fact list, so the history adds a second match.
    expect(screen.getAllByText(/scanner-agent/).length).toBeGreaterThan(1);
    expect(getSecurityFinding).toHaveBeenCalledWith({ id: FINDING_ID, namespace: "user-alice" });
  });

  it("shows an error in the history section when the events fetch fails", async () => {
    getSecurityScan.mockResolvedValue(scanFixture());
    getSecurityFindingSummary.mockResolvedValue({ counts: {} });
    listSecurityFindings.mockResolvedValue({ findings: [findingFixture()] });
    getSecurityFinding.mockRejectedValue(new Error("events unavailable"));

    renderDetail();

    await screen.findByText("SQL injection in payment lookup");
    openFinding("SQL injection in payment lookup");

    expect(await screen.findByRole("alert")).toBeTruthy();
    expect(screen.getByText("events unavailable")).toBeTruthy();
  });

  it("links to the underlying agent run", async () => {
    getSecurityScan.mockResolvedValue(scanFixture());
    getSecurityFindingSummary.mockResolvedValue({ counts: {} });
    listSecurityFindings.mockResolvedValue({ findings: [] });

    renderDetail();

    expect(
      (await screen.findByRole("button", { name: /Agent run/ })).getAttribute("href"),
    ).toBe("/runs/user-alice/nightly-1");
  });

  it("downloads the Markdown report and SARIF artifact", async () => {
    getSecurityScan.mockResolvedValue(scanFixture());
    getSecurityFindingSummary.mockResolvedValue({ counts: {} });
    listSecurityFindings.mockResolvedValue({ findings: [] });
    getSecurityScanReport.mockResolvedValue({
      content: "# Security Scan Report",
      format: "markdown",
      filename: "nightly-nightly-1.md",
    });

    renderDetail();

    fireEvent.click(await screen.findByRole("button", { name: /Report/ }));

    await waitFor(() => {
      expect(getSecurityScanReport).toHaveBeenCalledWith({
        namespace: "user-alice",
        runName: "nightly-1",
        format: "markdown",
      });
    });
    await waitFor(() => {
      expect(downloadBlob).toHaveBeenCalledWith(
        "nightly-nightly-1.md",
        expect.anything(),
        "text/markdown",
      );
    });

    getSecurityScanReport.mockResolvedValue({
      content: "{}",
      format: "sarif",
      filename: "nightly-nightly-1.sarif",
    });
    fireEvent.click(screen.getByRole("button", { name: /SARIF/ }));

    await waitFor(() => {
      expect(getSecurityScanReport).toHaveBeenLastCalledWith({
        namespace: "user-alice",
        runName: "nightly-1",
        format: "sarif",
      });
    });
    await waitFor(() => {
      expect(downloadBlob).toHaveBeenLastCalledWith(
        "nightly-nightly-1.sarif",
        expect.anything(),
        "application/json",
      );
    });
  });

  it("shows helpful copy when the report is not available", async () => {
    getSecurityScan.mockResolvedValue(scanFixture());
    getSecurityFindingSummary.mockResolvedValue({ counts: {} });
    listSecurityFindings.mockResolvedValue({ findings: [] });
    getSecurityScanReport.mockRejectedValue(
      new Error("security scan user-alice/nightly-1 has no markdown report yet"),
    );

    renderDetail();

    fireEvent.click(await screen.findByRole("button", { name: /Report/ }));

    expect(
      await screen.findByText(/has no markdown report yet/),
    ).toBeTruthy();
    expect(downloadBlob).not.toHaveBeenCalled();
  });

  it("downloads a ready bounty submission bundle", async () => {
    getSecurityScan.mockResolvedValue(scanFixture());
    getSecurityFindingSummary.mockResolvedValue({ counts: {} });
    listSecurityFindings.mockResolvedValue({ findings: [findingFixture()] });
    getSecurityFinding.mockResolvedValue(findingEventsResponse());
    getSecurityFindingSubmissionBundle.mockResolvedValue({
      status: "ready",
      filename: "nightly-finding-bounty-submission.zip",
      content: new Uint8Array([80, 75, 3, 4]),
      sha256: "abc123",
    });

    renderDetail();
    await screen.findByText("SQL injection in payment lookup");
    openFinding("SQL injection in payment lookup");
    fireEvent.click(screen.getByRole("button", { name: "Download bounty bundle" }));

    await waitFor(() => {
      expect(getSecurityFindingSubmissionBundle.mock.calls[0]?.[0]).toEqual({
        namespace: "user-alice",
        findingId: FINDING_ID,
      });
      expect(downloadBlob).toHaveBeenCalledWith(
        "nightly-finding-bounty-submission.zip",
        new Uint8Array([80, 75, 3, 4]),
        "application/zip",
      );
    });
    expect(await screen.findByText("Downloaded. SHA-256: abc123")).toBeTruthy();
  });

  it("waits for a generating bounty bundle and downloads it when ready", async () => {
    getSecurityScan.mockResolvedValue(scanFixture());
    getSecurityFindingSummary.mockResolvedValue({ counts: {} });
    listSecurityFindings.mockResolvedValue({ findings: [findingFixture()] });
    getSecurityFinding.mockResolvedValue(findingEventsResponse());
    getSecurityFindingSubmissionBundle
      .mockResolvedValueOnce({ status: "generating", content: new Uint8Array() })
      .mockResolvedValueOnce({
        status: "ready",
        filename: "nightly-finding-bounty-submission.zip",
        content: new Uint8Array([80, 75, 3, 4]),
        sha256: "abc123",
      });

    renderDetail();
    await screen.findByText("SQL injection in payment lookup");
    openFinding("SQL injection in payment lookup");
    vi.useFakeTimers({ shouldAdvanceTime: true });
    try {
      fireEvent.click(screen.getByRole("button", { name: "Download bounty bundle" }));

      expect(await screen.findByText(/download will start automatically/)).toBeTruthy();
      await vi.advanceTimersByTimeAsync(2_000);

      await waitFor(() => expect(getSecurityFindingSubmissionBundle).toHaveBeenCalledTimes(2));
      await waitFor(() => expect(downloadBlob).toHaveBeenCalled());
      expect(await screen.findByText("Downloaded. SHA-256: abc123")).toBeTruthy();
    } finally {
      vi.useRealTimers();
    }
  });

  it("stops bundle polling when the finding panel unmounts", async () => {
    getSecurityScan.mockResolvedValue(scanFixture());
    getSecurityFindingSummary.mockResolvedValue({ counts: {} });
    listSecurityFindings.mockResolvedValue({ findings: [findingFixture()] });
    getSecurityFinding.mockResolvedValue(findingEventsResponse());
    getSecurityFindingSubmissionBundle.mockResolvedValue({
      status: "generating",
      content: new Uint8Array(),
    });

    const view = renderDetail();
    await screen.findByText("SQL injection in payment lookup");
    openFinding("SQL injection in payment lookup");
    vi.useFakeTimers({ shouldAdvanceTime: true });
    try {
      fireEvent.click(screen.getByRole("button", { name: "Download bounty bundle" }));
      expect(await screen.findByText(/download will start automatically/)).toBeTruthy();

      view.unmount();
      await vi.advanceTimersByTimeAsync(10_000);

      expect(getSecurityFindingSubmissionBundle).toHaveBeenCalledTimes(1);
      expect(downloadBlob).not.toHaveBeenCalled();
    } finally {
      vi.useRealTimers();
    }
  });

  it("stops polling when filtering removes the selected finding", async () => {
    getSecurityScan.mockResolvedValue(scanFixture());
    getSecurityFindingSummary.mockResolvedValue({ counts: {} });
    listSecurityFindings
      .mockResolvedValueOnce({ findings: [findingFixture()] })
      .mockResolvedValue({ findings: [] });
    getSecurityFinding.mockResolvedValue(findingEventsResponse());
    getSecurityFindingSubmissionBundle.mockResolvedValue({
      status: "generating",
      content: new Uint8Array(),
    });

    renderDetail();
    await screen.findByText("SQL injection in payment lookup");
    openFinding("SQL injection in payment lookup");
    vi.useFakeTimers({ shouldAdvanceTime: true });
    try {
      fireEvent.click(screen.getByRole("button", { name: "Download bounty bundle" }));
      expect(await screen.findByText(/download will start automatically/)).toBeTruthy();

      fireEvent.click(screen.getByRole("button", { name: "High" }));
      await waitFor(() => expect(screen.queryByText("Full database read access.")).toBeNull());
      await vi.advanceTimersByTimeAsync(10_000);

      expect(getSecurityFindingSubmissionBundle).toHaveBeenCalledTimes(1);
      expect(downloadBlob).not.toHaveBeenCalled();
    } finally {
      vi.useRealTimers();
    }
  });

  it("clears a generating notice when the selected finding changes", async () => {
    getSecurityScan.mockResolvedValue(scanFixture());
    getSecurityFindingSummary.mockResolvedValue({ counts: {} });
    listSecurityFindings.mockResolvedValue({ findings: [findingFixture()] });
    getSecurityFinding.mockResolvedValue(findingEventsResponse());
    getSecurityFindingSubmissionBundle.mockResolvedValue({
      status: "generating",
      content: new Uint8Array(),
    });

    renderDetail();
    await screen.findByText("SQL injection in payment lookup");
    openFinding("SQL injection in payment lookup");
    vi.useFakeTimers({ shouldAdvanceTime: true });
    try {
      fireEvent.click(screen.getByRole("button", { name: "Download bounty bundle" }));
      expect(await screen.findByText(/download will start automatically/)).toBeTruthy();

      fireEvent.click(screen.getByRole("button", { name: "Close finding details" }));
      openFinding("SQL injection in payment lookup");

      expect(screen.queryByText(/download will start automatically/)).toBeNull();
      expect(await screen.findByRole("button", { name: "Download bounty bundle" })).toBeTruthy();
    } finally {
      vi.useRealTimers();
    }
  });

  it("explains that reports arrive after the run finishes for running scans", async () => {
    const running = scanFixture();
    running.status = "running";
    getSecurityScan.mockResolvedValue(running);
    getSecurityFindingSummary.mockResolvedValue({ counts: {} });
    listSecurityFindings.mockResolvedValue({ findings: [] });

    renderDetail();

    const banner = await screen.findByTestId("scan-in-progress");
    expect(banner.textContent).toContain("Scan in progress");
    expect(banner.textContent).toMatch(/This scan run has not finished/);
    expect(banner.textContent).toMatch(/report, SARIF artifact, and audit export unlock/);
    // Counts that are still changing say so instead of reading as a clean result.
    expect(screen.getByText("Findings so far")).toBeTruthy();
    expect(screen.getByText(/Live · updates every 5s/)).toBeTruthy();
    expect(screen.getByTestId("run-tab-live")).toBeTruthy();
  });

  it("does not describe an in-flight scan as having reported nothing", async () => {
    const running = scanFixture();
    running.status = "running";
    getSecurityScan.mockResolvedValue(running);
    getSecurityFindingSummary.mockResolvedValue({ counts: {} });
    listSecurityFindings.mockResolvedValue({ findings: [] });

    renderDetail();

    expect(await screen.findByText("No findings yet")).toBeTruthy();
    expect(screen.queryByText("This scan reported no findings.")).toBeNull();
    expect(screen.getByText(/Findings appear here as soon as the run submits them/)).toBeTruthy();
  });

  it("jumps from the in-progress banner to the Run tab", async () => {
    const running = scanFixture();
    running.status = "running";
    getSecurityScan.mockResolvedValue(running);
    getSecurityFindingSummary.mockResolvedValue({ counts: {} });
    listSecurityFindings.mockResolvedValue({ findings: [] });

    renderDetail();

    const banner = await screen.findByTestId("scan-in-progress");
    fireEvent.click(within(banner).getByRole("button", { name: /View run progress/ }));

    await waitFor(() => expect(search()).toBe("?tab=run"));
    expect(await screen.findByTestId("scan-run-panel")).toBeTruthy();
    // The Run tab is in view, so the banner stops offering to go there.
    expect(within(banner).queryByRole("button", { name: /View run progress/ })).toBeNull();
  });

  it("keeps a finished scan quiet: no banner, no live caption", async () => {
    getSecurityScan.mockResolvedValue(scanFixture());
    getSecurityFindingSummary.mockResolvedValue({ counts: {} });
    listSecurityFindings.mockResolvedValue({ findings: [] });

    renderDetail();

    await screen.findByText("nightly-1");
    expect(screen.queryByTestId("scan-in-progress")).toBeNull();
    expect(screen.queryByText("Findings so far")).toBeNull();
    expect(screen.queryByTestId("run-tab-live")).toBeNull();
  });

  it("disables the artifact downloads with a reason while the run is unfinished", async () => {
    const running = scanFixture();
    running.status = "running";
    getSecurityScan.mockResolvedValue(running);
    getSecurityFindingSummary.mockResolvedValue({ counts: {} });
    listSecurityFindings.mockResolvedValue({ findings: [] });

    renderDetail();

    const report = (await screen.findByRole("button", { name: /Report/ })) as HTMLButtonElement;
    expect(report.disabled).toBe(true);
    expect(report.getAttribute("title")).toMatch(/once the scan run finishes/);
    expect((screen.getByRole("button", { name: /SARIF/ }) as HTMLButtonElement).disabled).toBe(true);
    expect((screen.getByRole("button", { name: /Audit CSV/ }) as HTMLButtonElement).disabled)
      .toBe(true);

    fireEvent.click(report);
    expect(getSecurityScanReport).not.toHaveBeenCalled();
  });

  it("keeps the artifact downloads enabled once the run has completed", async () => {
    getSecurityScan.mockResolvedValue(scanFixture());
    getSecurityFindingSummary.mockResolvedValue({ counts: {} });
    listSecurityFindings.mockResolvedValue({ findings: [] });

    renderDetail();

    const report = (await screen.findByRole("button", { name: /Report/ })) as HTMLButtonElement;
    expect(report.disabled).toBe(false);
    expect(report.getAttribute("title")).toBeNull();
  });

  it("documents what the actionable count includes", async () => {
    getSecurityScan.mockResolvedValue(scanFixture());
    getSecurityFindingSummary.mockResolvedValue({ counts: { total: 4, actionable: 2 } });
    listSecurityFindings.mockResolvedValue({ findings: [] });

    renderDetail();

    const help = await screen.findByRole("button", { name: 'What "Actionable" means' });
    expect(help.getAttribute("title")).toMatch(/open, triaged, or confirmed/);
  });
});

describe("SecurityScanDetail section tabs", () => {
  it("shows findings by default and moves the dossier and run panels behind tabs", async () => {
    getSecurityScan.mockResolvedValue(scanFixture());
    getSecurityFindingSummary.mockResolvedValue({ counts: { total: 4, actionable: 2 } });
    listSecurityFindings.mockResolvedValue({ findings: [] });
    getSecurityScanConfig.mockResolvedValue({ namespace: "user-alice", name: "nightly" });

    renderDetail();

    const tabs = await screen.findByRole("tablist", { name: "Scan sections" });
    const findings = within(tabs).getByRole("tab", { name: /Findings/ });
    expect(findings.getAttribute("aria-selected")).toBe("true");
    expect(findings.textContent).toContain("4");
    expect(screen.queryByTestId("security-research-panel")).toBeNull();
    expect(screen.queryByTestId("scan-run-panel")).toBeNull();
    // Nothing to show under Integration for this config, so the tab is absent.
    expect(within(tabs).queryByRole("tab", { name: /Integration/ })).toBeNull();

    fireEvent.click(within(tabs).getByRole("tab", { name: /Research/ }));
    expect(await screen.findByTestId("security-research-panel")).toBeTruthy();
    await waitFor(() => expect(search()).toBe("?tab=research"));

    fireEvent.click(within(tabs).getByRole("tab", { name: /^Run/ }));
    expect(await screen.findByTestId("scan-run-panel")).toBeTruthy();
    expect(screen.queryByTestId("security-research-panel")).toBeNull();
  });

  it("falls back to findings for an unknown or unavailable tab in the URL", async () => {
    getSecurityScan.mockResolvedValue(scanFixture());
    getSecurityFindingSummary.mockResolvedValue({ counts: {} });
    listSecurityFindings.mockResolvedValue({ findings: [] });
    getSecurityScanConfig.mockResolvedValue({ namespace: "user-alice", name: "nightly" });

    renderDetail("/security/user-alice/nightly-1?tab=settings");

    const tabs = await screen.findByRole("tablist", { name: "Scan sections" });
    expect(within(tabs).getByRole("tab", { name: /Findings/ }).getAttribute("aria-selected"))
      .toBe("true");
    expect(await screen.findByText("This scan reported no findings.")).toBeTruthy();
  });
});

describe("SecurityScanDetail repository integration state", () => {
  it("surfaces the last check publish state and notification state with errors", async () => {
    getSecurityScan.mockResolvedValue(scanFixture());
    getSecurityFindingSummary.mockResolvedValue({ counts: { critical: 1, total: 1, open: 1 } });
    listSecurityFindings.mockResolvedValue({ findings: [] });
    getSecurityScanConfig.mockResolvedValue({
      namespace: "user-alice",
      name: "nightly",
      lastCheck: {
        runName: "nightly-1",
        revision: "abc123def456789",
        conclusion: "failure",
        url: "",
        error: "publishing check: 502 from api.github.com",
        sarifUploaded: false,
        sarifError: "",
      },
      lastNotifications: {
        lastRunName: "nightly-1",
        sent: 3,
        suppressed: 2,
        lastError: "rule \"alerts\" slack: webhook returned 500",
      },
    });

    renderDetail("/security/user-alice/nightly-1?tab=settings");

    expect(await screen.findByText("Repository integration")).toBeTruthy();
    expect(screen.getByText(/publish failed — publishing check: 502/)).toBeTruthy();
    expect(screen.getAllByText(/retried automatically/).length).toBeGreaterThan(0);
    expect(screen.getByText(/3 sent/)).toBeTruthy();
    expect(screen.getByText(/2 suppressed as duplicates/)).toBeTruthy();
    expect(screen.getByText(/webhook returned 500/)).toBeTruthy();
  });

  it("hides the integration card when the scan config has no check or notification state", async () => {
    getSecurityScan.mockResolvedValue(scanFixture());
    getSecurityFindingSummary.mockResolvedValue({ counts: {} });
    listSecurityFindings.mockResolvedValue({ findings: [] });
    getSecurityScanConfig.mockResolvedValue({ namespace: "user-alice", name: "nightly" });

    renderDetail();

    await screen.findByText("nightly-1");
    expect(screen.queryByText("Repository integration")).toBeNull();
  });

  it("shows execution progress for a deterministic last execution", async () => {
    getSecurityScan.mockResolvedValue(scanFixture());
    getSecurityFindingSummary.mockResolvedValue({ counts: {} });
    listSecurityFindings.mockResolvedValue({ findings: [] });
    getSecurityScanConfig.mockResolvedValue({
      namespace: "user-alice",
      name: "nightly",
      lastExecution: {
        id: "abc",
        mode: "deterministic",
        phase: "Running",
        effectiveParallelism: 4,
        effectiveParallelismNote: "",
        startedAtUnix: 1767225600n,
        completedAtUnix: 0n,
        tasks: [
          {
            name: "recon",
            instance: 0,
            state: "Running",
            runName: "nightly-recon-1",
            attempts: 1,
            retries: [],
            nextRetryTimeUnix: 0n,
            lastError: "",
            startedAtUnix: 1767225600n,
            finishedAtUnix: 0n,
          },
        ],
      },
    });

    renderDetail("/security/user-alice/nightly-1?tab=run");

    expect(await screen.findByTestId("execution-progress")).toBeTruthy();
    expect(screen.getByTestId("execution-task-recon#0")).toBeTruthy();
  });

  it("draws the execution DAG from the config's inline workflow", async () => {
    getSecurityScan.mockResolvedValue(scanFixture());
    getSecurityFindingSummary.mockResolvedValue({ counts: {} });
    listSecurityFindings.mockResolvedValue({ findings: [] });
    getSecurityScanConfig.mockResolvedValue({
      namespace: "user-alice",
      name: "nightly",
      spec: {
        workflow: [
          { name: "recon", objective: "Map.", dependsOn: [], forEach: "" },
          { name: "triage", objective: "Triage.", dependsOn: ["recon"], forEach: "" },
        ],
      },
      lastExecution: {
        id: "abc",
        mode: "deterministic",
        phase: "Running",
        effectiveParallelism: 4,
        effectiveParallelismNote: "",
        startedAtUnix: 1767225600n,
        completedAtUnix: 0n,
        tasks: [
          {
            name: "recon",
            instance: 0,
            state: "Running",
            runName: "nightly-recon-1",
            attempts: 1,
            retries: [],
            nextRetryTimeUnix: 0n,
            lastError: "",
            startedAtUnix: 1767225600n,
            finishedAtUnix: 0n,
          },
        ],
      },
    });

    renderDetail("/security/user-alice/nightly-1?tab=run");

    expect(await screen.findByTestId("execution-dag")).toBeTruthy();
    expect(screen.getByTestId("execution-node-recon").textContent).toContain("Running");
    // triage has no instance yet: the planned node reads as waiting.
    expect(screen.getByTestId("execution-node-triage").textContent).toContain("Waiting");
  });

  it("resumes a failed deterministic execution via the RPC", async () => {
    getSecurityScan.mockResolvedValue(scanFixture());
    getSecurityFindingSummary.mockResolvedValue({ counts: {} });
    listSecurityFindings.mockResolvedValue({ findings: [] });
    getSecurityScanConfig.mockResolvedValue({
      namespace: "user-alice",
      name: "nightly",
      lastExecution: {
        id: "abc",
        mode: "deterministic",
        phase: "Failed",
        effectiveParallelism: 4,
        effectiveParallelismNote: "",
        startedAtUnix: 1767225600n,
        completedAtUnix: 1767229200n,
        tasks: [
          {
            name: "recon",
            instance: 0,
            state: "Failed",
            runName: "nightly-recon-1",
            attempts: 3,
            retries: [],
            nextRetryTimeUnix: 0n,
            lastError: "budget exhausted",
            startedAtUnix: 1767225600n,
            finishedAtUnix: 1767229200n,
          },
        ],
      },
    });
    resumeSecurityScan.mockResolvedValue({});

    renderDetail("/security/user-alice/nightly-1?tab=run");

    fireEvent.click(await screen.findByTestId("execution-resume"));
    await waitFor(() =>
      expect(resumeSecurityScan).toHaveBeenCalledWith({
        namespace: "user-alice",
        name: "nightly",
      }),
    );
  });

  it("hides execution progress when the last execution was the coordinator", async () => {
    getSecurityScan.mockResolvedValue(scanFixture());
    getSecurityFindingSummary.mockResolvedValue({ counts: {} });
    listSecurityFindings.mockResolvedValue({ findings: [] });
    getSecurityScanConfig.mockResolvedValue({
      namespace: "user-alice",
      name: "nightly",
      lastExecution: { mode: "coordinator", phase: "Succeeded", tasks: [] },
    });

    renderDetail();

    await screen.findByText("nightly-1");
    expect(screen.queryByTestId("execution-progress")).toBeNull();
  });
});

describe("SecurityScanDetail budgets, retention, and suppression", () => {
  it("warns when the budget is exceeded and shows effective budgets and the sweep state", async () => {
    getSecurityScan.mockResolvedValue(scanFixture());
    getSecurityFindingSummary.mockResolvedValue({ counts: {} });
    listSecurityFindings.mockResolvedValue({ findings: [] });
    getSecurityScanConfig.mockResolvedValue({
      namespace: "user-alice",
      name: "nightly",
      effectiveBudgets: {
        maxCostUsd: "5",
        maxTokens: 0n,
        maxModelJobs: 0,
        maxValidationJobs: 0,
        maxRuntime: "",
      },
      budgetExceeded: true,
      budgetMessage: "model spend $6 exceeds budgets.maxCostUsd $5",
      retention: {
        lastSweepTimeUnix: 1770000000n,
        scansPurged: 2n,
        findingsPurged: 7n,
        reportsPurged: 1n,
        evidenceRedacted: 3n,
        pocRedacted: 4n,
        auditEventsPurged: 9n,
        moreWork: false,
        lastError: "",
      },
    });

    renderDetail("/security/user-alice/nightly-1?tab=settings");

    const warning = await screen.findByTestId("budget-warning");
    expect(warning.textContent).toContain("Budget exceeded");
    expect(warning.textContent).toContain("model spend $6 exceeds budgets.maxCostUsd $5");
    const budgets = screen.getByTestId("effective-budgets");
    expect(budgets.textContent).toContain("$5");
    const sweep = screen.getByTestId("retention-sweep");
    expect(sweep.textContent).toContain("7 findings");
    expect(sweep.textContent).toContain("4 PoC entries redacted");
    expect(sweep.textContent).toContain("9 audit events purged");
  });

  it("omits the budget warning when no limit is exceeded", async () => {
    getSecurityScan.mockResolvedValue(scanFixture());
    getSecurityFindingSummary.mockResolvedValue({ counts: {} });
    listSecurityFindings.mockResolvedValue({ findings: [] });
    getSecurityScanConfig.mockResolvedValue({
      namespace: "user-alice",
      name: "nightly",
      effectiveBudgets: { maxCostUsd: "5" },
      budgetExceeded: false,
      budgetMessage: "",
    });

    renderDetail("/security/user-alice/nightly-1?tab=settings");

    await screen.findByTestId("effective-budgets");
    expect(screen.queryByTestId("budget-warning")).toBeNull();
  });

  it("round-trips the suppressed filter and renders the suppressed chip", async () => {
    getSecurityScan.mockResolvedValue(scanFixture());
    getSecurityFindingSummary.mockResolvedValue({ counts: {} });
    const suppressedFinding = findingFixture();
    suppressedFinding.suppressedBy = "prod-policy/vendored";
    suppressedFinding.suppressedReason = "third-party code";
    suppressedFinding.suppressedOwner = "sec-team";
    suppressedFinding.suppressionExpiresAt = timestampFromDate(new Date(Date.now() + 30 * 86400000));
    listSecurityFindings.mockResolvedValue({ findings: [suppressedFinding] });

    renderDetail("/security/user-alice/nightly-1?suppressed=only");
    await screen.findByText("SQL injection in payment lookup");

    await waitFor(() => {
      expect(listSecurityFindings).toHaveBeenLastCalledWith({
        namespace: "user-alice",
        runName: "nightly-1",
        severity: "",
        status: "actionable",
        category: "",
        search: "",
        baselineState: "",
        assignee: "",
        suppressed: "only",
        includeDuplicates: false,
      });
    });

    const chip = await screen.findByText("suppressed");
    expect(chip.getAttribute("title")).toContain("rule prod-policy/vendored");
    expect(chip.getAttribute("title")).toContain("owner sec-team");
    expect(chip.getAttribute("title")).toContain("until");

    // Selecting the row explains the suppression in the side panel.
    getSecurityFinding.mockResolvedValue({ finding: suppressedFinding, events: [] });
    openFinding("SQL injection in payment lookup");
    const note = await screen.findByTestId("finding-suppression-note");
    expect(note.textContent).toContain("rule prod-policy/vendored");
    expect(note.textContent).toContain("Reason: third-party code");
  });
});

function policyEvent(id: bigint, check: string, disposition: string) {
  return create(SecurityFindingEventSchema, {
    id,
    eventType: "policy_disposition",
    actor: "secscan-nightly-1-ps-poc-validator",
    detail: JSON.stringify({ execution_id: "exec-1", policy_check: check, policy_disposition: disposition }),
    createdAt: timestampFromDate(new Date("2026-02-03T10:00:00Z")),
  });
}

describe("blockingPolicyDisposition", () => {
  it("surfaces an environment block until a verdict or a bounty acceptance supersedes it", () => {
    const blocked = [policyEvent(2n, "reproduction", "unreproducible_env"), policyEvent(1n, "scope", "scope_eligible")];
    expect(blockingPolicyDisposition(blocked)).toEqual({ check: "reproduction", disposition: "unreproducible_env" });
    expect(blockingPolicyDisposition([policyEvent(3n, "reproduction", "reproduced"), ...blocked])).toBeNull();
    expect(blockingPolicyDisposition([policyEvent(3n, "bounty", "accepted"), ...blocked])).toBeNull();
    expect(blockingPolicyDisposition([policyEvent(3n, "bounty", "known_issue"), ...blocked])).toEqual({
      check: "bounty",
      disposition: "known_issue",
    });
    expect(blockingPolicyDisposition(findingEventsResponse().events)).toBeNull();
  });
});

describe("SecurityScanDetail policy disposition badge", () => {
  it("shows the blocking disposition for the selected finding and nothing when unblocked", async () => {
    getSecurityScan.mockResolvedValue(scanFixture());
    getSecurityFindingSummary.mockResolvedValue({ counts: {} });
    listSecurityFindings.mockResolvedValue({ findings: [findingFixture()] });
    getSecurityFinding.mockResolvedValue({
      finding: findingFixture(),
      events: [policyEvent(2n, "reproduction", "unreproducible_env"), ...findingEventsResponse().events],
    });

    renderDetail();
    await screen.findByText("SQL injection in payment lookup");
    openFinding("SQL injection in payment lookup");
    const badge = await screen.findByTestId("finding-policy-disposition");
    expect(badge.textContent).toBe("reproduction: unreproducible env");
    expect(badge.getAttribute("title")).toContain("stays actionable");
    cleanup();

    getSecurityFinding.mockResolvedValue(findingEventsResponse());
    renderDetail();
    await screen.findByText("SQL injection in payment lookup");
    openFinding("SQL injection in payment lookup");
    await screen.findByText("confirmed exploitable");
    expect(screen.queryByTestId("finding-policy-disposition")).toBeNull();
  });
});

const OTHER_ID = "33333333-3333-3333-3333-333333333333";

/** A second finding that differs on every client-side filter dimension. */
function otherFindingFixture() {
  const finding = findingFixture();
  finding.id = OTHER_ID;
  finding.title = "Path traversal in report export";
  finding.category = "traversal";
  finding.severity = "high";
  finding.filePath = "cmd/report/export.go";
  finding.sourceAgent = "sast-agent";
  return finding;
}

function mockFindingsPage(findings = [findingFixture(), otherFindingFixture()]) {
  getSecurityScan.mockResolvedValue(scanFixture());
  getSecurityFindingSummary.mockResolvedValue({ counts: {} });
  listSecurityFindings.mockResolvedValue({ findings });
  getSecurityFinding.mockResolvedValue(findingEventsResponse());
}

describe("SecurityScanDetail finding filters", () => {
  it("narrows by source agent client-side and reports the result count", async () => {
    mockFindingsPage();

    renderDetail("/security/user-alice/nightly-1?tool=sast-agent");

    expect(await screen.findByText("Path traversal in report export")).toBeTruthy();
    expect(screen.queryByText("SQL injection in payment lookup")).toBeNull();
    expect(screen.getByText("1 of 2 findings")).toBeTruthy();
    expect(screen.getByLabelText("1 active filter")).toBeTruthy();
    // `tool` has no server-side equivalent, so the request stays unfiltered.
    expect(listSecurityFindings.mock.calls[0][0]).toMatchObject({
      severity: "",
      status: "actionable",
    });
  });

  it("narrows by file path substring", async () => {
    mockFindingsPage();

    renderDetail("/security/user-alice/nightly-1?file=CMD/report");

    expect(await screen.findByText("Path traversal in report export")).toBeTruthy();
    expect(screen.queryByText("SQL injection in payment lookup")).toBeNull();
  });

  it("asks the server for duplicates when the dupes filter is on", async () => {
    mockFindingsPage();

    renderDetail("/security/user-alice/nightly-1?dupes=include");

    await waitFor(() => {
      expect(listSecurityFindings).toHaveBeenLastCalledWith(
        expect.objectContaining({ includeDuplicates: true }),
      );
    });
  });

  it("toggles duplicate visibility from the filter chip", async () => {
    mockFindingsPage();

    renderDetail();
    await screen.findByText("SQL injection in payment lookup");

    const chip = screen.getByRole("button", { name: "Duplicates" });
    expect(chip.getAttribute("aria-pressed")).toBe("false");

    fireEvent.click(chip);
    await waitFor(() => expect(search()).toBe("?dupes=include"));
    expect(
      screen.getByRole("button", { name: "Duplicates" }).getAttribute("aria-pressed"),
    ).toBe("true");

    fireEvent.click(screen.getByRole("button", { name: "Duplicates" }));
    await waitFor(() => expect(search()).toBe(""));
  });

  it("switches suppressed visibility from the segmented chips", async () => {
    mockFindingsPage();

    renderDetail();
    await screen.findByText("SQL injection in payment lookup");

    const group = screen.getByRole("group", { name: "Suppressed findings" });
    expect(within(group).getByRole("button", { name: "Hidden" }).getAttribute("aria-pressed"))
      .toBe("true");

    fireEvent.click(within(group).getByRole("button", { name: "Only" }));

    await waitFor(() => expect(search()).toBe("?suppressed=only"));
    await waitFor(() => {
      expect(listSecurityFindings).toHaveBeenLastCalledWith(
        expect.objectContaining({ suppressed: "only" }),
      );
    });
  });

  it("offers a filtered-empty state that clears the filters", async () => {
    mockFindingsPage([findingFixture()]);

    renderDetail("/security/user-alice/nightly-1?tool=nobody&severity=critical");

    expect(await screen.findByText("No findings match these filters")).toBeTruthy();
    expect(screen.queryByText("This scan reported no findings.")).toBeNull();

    fireEvent.click(screen.getByRole("button", { name: /Clear filters/ }));
    await waitFor(() => expect(search()).toBe(""));
    expect(await screen.findByText("SQL injection in payment lookup")).toBeTruthy();
  });

  it("keeps the plain empty state when the scan reported nothing", async () => {
    mockFindingsPage([]);

    renderDetail();

    expect(await screen.findByText("This scan reported no findings.")).toBeTruthy();
    expect(screen.queryByText("No findings match these filters")).toBeNull();
  });

  it("reconciles the actionable summary with the findings hidden by default", async () => {
    mockFindingsPage([findingFixture()]);
    getSecurityFindingSummary.mockResolvedValue({ counts: { total: 3, actionable: 3 } });

    renderDetail();

    const notice = await screen.findByTestId("hidden-findings-notice");
    expect(notice.textContent).toContain("Showing 1 of 3 actionable findings");
    expect(notice.textContent).toContain(
      "2 hidden — suppressed and duplicate findings are excluded by default",
    );

    fireEvent.click(within(notice).getByRole("button", { name: "Show hidden" }));

    await waitFor(() => expect(search()).toContain("suppressed=include"));
    expect(search()).toContain("dupes=include");
    await waitFor(() => {
      expect(listSecurityFindings).toHaveBeenLastCalledWith(
        expect.objectContaining({ suppressed: "include", includeDuplicates: true }),
      );
    });
  });

  it("names only the duplicates when suppressed findings are already shown", async () => {
    mockFindingsPage([findingFixture()]);
    getSecurityFindingSummary.mockResolvedValue({ counts: { total: 2, actionable: 2 } });

    renderDetail("/security/user-alice/nightly-1?suppressed=include");

    const notice = await screen.findByTestId("hidden-findings-notice");
    expect(notice.textContent).toContain(
      "1 hidden — duplicate findings are excluded by default",
    );
    expect(within(notice).getByRole("button", { name: "Show duplicates" })).toBeTruthy();
  });

  it("stays quiet when the table already lists every actionable finding", async () => {
    mockFindingsPage();
    getSecurityFindingSummary.mockResolvedValue({ counts: { total: 2, actionable: 2 } });

    renderDetail();

    await screen.findByText("SQL injection in payment lookup");
    expect(screen.queryByTestId("hidden-findings-notice")).toBeNull();
  });

  it("stays quiet while a narrowing filter explains the smaller count", async () => {
    mockFindingsPage();
    getSecurityFindingSummary.mockResolvedValue({ counts: { total: 2, actionable: 2 } });

    renderDetail("/security/user-alice/nightly-1?tool=sast-agent");

    await screen.findByText("Path traversal in report export");
    expect(screen.queryByTestId("hidden-findings-notice")).toBeNull();
  });
});

describe("SecurityScanDetail selected finding", () => {
  it("opens the panel from a `selected` deep link and marks the row selected", async () => {
    mockFindingsPage();

    renderDetail(`/security/user-alice/nightly-1?selected=${FINDING_ID}`);

    expect(await screen.findByText("Full database read access.")).toBeTruthy();
    await waitFor(() => {
      expect(getSecurityFinding).toHaveBeenCalledWith({ id: FINDING_ID, namespace: "user-alice" });
    });
    expect(findingRow("SQL injection in payment lookup").getAttribute("aria-selected")).toBe("true");
    expect(findingRow("Path traversal in report export").getAttribute("aria-selected")).toBe("false");
  });

  it("writes the selection into the URL and clears it again", async () => {
    mockFindingsPage();

    renderDetail();
    await screen.findByText("SQL injection in payment lookup");

    openFinding("SQL injection in payment lookup");
    await waitFor(() => expect(search()).toBe(`?selected=${FINDING_ID}`));

    fireEvent.click(screen.getByRole("button", { name: "Close finding details" }));
    await waitFor(() => expect(search()).toBe(""));
  });

  it("activates a row from the keyboard with Enter and Space", async () => {
    mockFindingsPage();

    renderDetail();
    await screen.findByText("SQL injection in payment lookup");

    const row = findingRow("SQL injection in payment lookup");
    expect(row.getAttribute("tabindex")).toBe("0");
    fireEvent.keyDown(row, { key: "Enter" });
    await waitFor(() => expect(search()).toBe(`?selected=${FINDING_ID}`));

    fireEvent.keyDown(findingRow("Path traversal in report export"), { key: " " });
    await waitFor(() => expect(search()).toBe(`?selected=${OTHER_ID}`));
  });

  it("carries the active filters on every link out to a finding", async () => {
    mockFindingsPage([findingFixture()]);

    renderDetail(`/security/user-alice/nightly-1?severity=critical&selected=${FINDING_ID}`);
    await screen.findByRole("complementary", { name: "Finding details" });

    const expected =
      `/security/user-alice/nightly-1/findings/${FINDING_ID}?severity=critical&selected=${FINDING_ID}`;
    expect(screen.getByRole("link", { name: "Open full page" }).getAttribute("href")).toBe(expected);
    const panel = screen.getByRole("complementary", { name: "Finding details" });
    expect(
      within(panel).getByRole("button", { name: "Open finding full page" }).getAttribute("href"),
    ).toBe(expected);
  });
});

describe("SecurityScanDetail dead ends", () => {
  it("shows a typed not-found state with recovery links and retries", async () => {
    getSecurityFindingSummary.mockResolvedValue({ counts: {} });
    listSecurityFindings.mockResolvedValue({ findings: [] });
    getSecurityScan.mockRejectedValue(new Error("not_found: security scan user-alice/nightly-1"));

    renderDetail();

    expect(await screen.findByText("Scan run not found")).toBeTruthy();
    expect(screen.getByText(/not_found: security scan/)).toBeTruthy();
    expect(screen.getByRole("link", { name: "Back to scan runs" }).getAttribute("href"))
      .toBe("/security/runs");
    expect(screen.getByRole("link", { name: "Security overview" }).getAttribute("href"))
      .toBe("/security");

    getSecurityScan.mockResolvedValue(scanFixture());
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));

    expect(await screen.findByRole("heading", { name: "nightly-1" })).toBeTruthy();
  });

  it("distinguishes a permission failure from a missing scan", async () => {
    getSecurityFindingSummary.mockResolvedValue({ counts: {} });
    listSecurityFindings.mockResolvedValue({ findings: [] });
    getSecurityScan.mockRejectedValue(new Error("permission_denied: not your namespace"));

    renderDetail();

    expect(await screen.findByText("You don't have access")).toBeTruthy();
    expect(screen.queryByText("Scan run not found")).toBeNull();
  });
});
