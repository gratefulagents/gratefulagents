import { create } from "@bufbuild/protobuf";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";

import { SecurityScanDetail } from "@/components/SecurityScanDetail";
import {
  SecurityFindingEventSchema,
  SecurityFindingSchema,
  SecurityScanSchema,
} from "@/rpc/platform/service_pb";

const { getSecurityScan, getSecurityFinding, getSecurityFindingSummary, getSecurityScanConfig, listSecurityFindings, updateSecurityFindingStatus, getSecurityScanReport, resumeSecurityScan, downloadBlob } =
  vi.hoisted(() => ({
    getSecurityScan: vi.fn(),
    getSecurityFinding: vi.fn(),
    getSecurityFindingSummary: vi.fn(),
    getSecurityScanConfig: vi.fn().mockRejectedValue(new Error("not configured")),
    listSecurityFindings: vi.fn(),
    updateSecurityFindingStatus: vi.fn(),
    getSecurityScanReport: vi.fn(),
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

function renderDetail() {
  render(
    <MemoryRouter initialEntries={["/security/user-alice/nightly-1"]}>
      <Routes>
        <Route path="/security/:namespace/:runName" element={<SecurityScanDetail />} />
      </Routes>
    </MemoryRouter>,
  );
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

    fireEvent.click(await screen.findByRole("button", { name: "SQL injection in payment lookup" }));

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

  it("re-queries findings when a filter changes", async () => {
    getSecurityScan.mockResolvedValue(scanFixture());
    getSecurityFindingSummary.mockResolvedValue({ counts: {} });
    listSecurityFindings.mockResolvedValue({ findings: [findingFixture()] });

    renderDetail();
    await screen.findByText("SQL injection in payment lookup");

    fireEvent.change(screen.getByLabelText("Filter by severity"), { target: { value: "critical" } });

    await waitFor(() => {
      expect(listSecurityFindings).toHaveBeenLastCalledWith({
        namespace: "user-alice",
        runName: "nightly-1",
        severity: "critical",
        status: "",
        category: "",
        search: "",
        baselineState: "",
        assignee: "",
        suppressed: "",
      });
    });
  });

  it("links each finding to its full page carrying the active filters", async () => {
    getSecurityScan.mockResolvedValue(scanFixture());
    getSecurityFindingSummary.mockResolvedValue({ counts: {} });
    listSecurityFindings.mockResolvedValue({ findings: [findingFixture()] });

    renderDetail();
    await screen.findByText("SQL injection in payment lookup");

    fireEvent.change(screen.getByLabelText("Filter by severity"), { target: { value: "critical" } });

    await waitFor(() => {
      expect(screen.getByRole("link", { name: "Open full page" }).getAttribute("href")).toBe(
        `/security/user-alice/nightly-1/findings/${FINDING_ID}?severity=critical`,
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

    fireEvent.click(await screen.findByRole("button", { name: "SQL injection in payment lookup" }));
    await waitFor(() => {
      expect(getSecurityFinding).toHaveBeenCalledWith({ id: FINDING_ID, namespace: "user-alice" });
    });

    fireEvent.change(screen.getByLabelText("Status"), { target: { value: "triaged" } });

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
    expect((screen.getByLabelText("Status") as HTMLSelectElement).value).toBe("triaged");
  });

  it("renders the finding's audit history in the panel", async () => {
    getSecurityScan.mockResolvedValue(scanFixture());
    getSecurityFindingSummary.mockResolvedValue({ counts: {} });
    listSecurityFindings.mockResolvedValue({ findings: [findingFixture()] });
    getSecurityFinding.mockResolvedValue(findingEventsResponse());

    renderDetail();

    fireEvent.click(await screen.findByRole("button", { name: "SQL injection in payment lookup" }));

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

    fireEvent.click(await screen.findByRole("button", { name: "SQL injection in payment lookup" }));

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

  it("explains that reports arrive after the run finishes for running scans", async () => {
    const running = scanFixture();
    running.status = "running";
    getSecurityScan.mockResolvedValue(running);
    getSecurityFindingSummary.mockResolvedValue({ counts: {} });
    listSecurityFindings.mockResolvedValue({ findings: [] });

    renderDetail();

    expect(
      await screen.findByText(/This scan run has not finished/),
    ).toBeTruthy();
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

    renderDetail();

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

    renderDetail();

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

    renderDetail();

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

    renderDetail();

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

    renderDetail();

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

    renderDetail();

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

    renderDetail();
    await screen.findByText("SQL injection in payment lookup");

    fireEvent.change(screen.getByLabelText("Filter suppressed findings"), {
      target: { value: "only" },
    });

    await waitFor(() => {
      expect(listSecurityFindings).toHaveBeenLastCalledWith({
        namespace: "user-alice",
        runName: "nightly-1",
        severity: "",
        status: "",
        category: "",
        search: "",
        baselineState: "",
        assignee: "",
        suppressed: "only",
      });
    });

    const chip = await screen.findByText("suppressed");
    expect(chip.getAttribute("title")).toContain("rule prod-policy/vendored");
    expect(chip.getAttribute("title")).toContain("owner sec-team");
    expect(chip.getAttribute("title")).toContain("until");

    // Selecting the row explains the suppression in the side panel.
    getSecurityFinding.mockResolvedValue({ finding: suppressedFinding, events: [] });
    fireEvent.click(screen.getByRole("button", { name: "SQL injection in payment lookup" }));
    const note = await screen.findByTestId("finding-suppression-note");
    expect(note.textContent).toContain("rule prod-policy/vendored");
    expect(note.textContent).toContain("Reason: third-party code");
  });
});
