import { create } from "@bufbuild/protobuf";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";

import { SecurityScanDetail } from "@/components/SecurityScanDetail";
import { SecurityFindingDetail } from "@/components/SecurityFindingDetail";
import {
  acceptedRiskExpiry,
  formatDurationSeconds,
} from "@/components/security-baseline";
import {
  SecurityFindingSchema,
  SecurityScanSchema,
  SecuritySavedFilterSchema,
} from "@/rpc/platform/service_pb";

const mocks = vi.hoisted(() => ({
  getSecurityScan: vi.fn(),
  getSecurityFinding: vi.fn(),
  getSecurityFindingSummary: vi.fn(),
  listSecurityFindings: vi.fn(),
  listSecurityFindingEvents: vi.fn(),
  updateSecurityFindingStatus: vi.fn(),
  updateSecurityFindingAssignee: vi.fn(),
  updateSecurityFindingTicket: vi.fn(),
  createSecurityFindingTicket: vi.fn(),
  bulkUpdateSecurityFindingStatus: vi.fn(),
  listSecuritySavedFilters: vi.fn(),
  saveSecuritySavedFilter: vi.fn(),
  deleteSecuritySavedFilter: vi.fn(),
  exportSecurityFindingAuditLog: vi.fn(),
  getSecurityScanReport: vi.fn(),
  addSecurityFindingComment: vi.fn(),
  downloadBlob: vi.fn(),
  toastSuccess: vi.fn(),
  toastError: vi.fn(),
}));

vi.mock("@/lib/client", () => ({
  client: {
    getSecurityScan: mocks.getSecurityScan,
    getSecurityFinding: mocks.getSecurityFinding,
    getSecurityFindingSummary: mocks.getSecurityFindingSummary,
    listSecurityFindings: mocks.listSecurityFindings,
    listSecurityFindingEvents: mocks.listSecurityFindingEvents,
    updateSecurityFindingStatus: mocks.updateSecurityFindingStatus,
    updateSecurityFindingAssignee: mocks.updateSecurityFindingAssignee,
    updateSecurityFindingTicket: mocks.updateSecurityFindingTicket,
    createSecurityFindingTicket: mocks.createSecurityFindingTicket,
    bulkUpdateSecurityFindingStatus: mocks.bulkUpdateSecurityFindingStatus,
    listSecuritySavedFilters: mocks.listSecuritySavedFilters,
    saveSecuritySavedFilter: mocks.saveSecuritySavedFilter,
    deleteSecuritySavedFilter: mocks.deleteSecuritySavedFilter,
    exportSecurityFindingAuditLog: mocks.exportSecurityFindingAuditLog,
    getSecurityScanReport: mocks.getSecurityScanReport,
    addSecurityFindingComment: mocks.addSecurityFindingComment,
  },
}));

vi.mock("@/lib/download", () => ({ downloadBlob: mocks.downloadBlob }));

vi.mock("@/components/ui/toaster", () => ({
  toast: { success: mocks.toastSuccess, error: mocks.toastError },
}));

vi.mock("@/components/SecurityScanRunPanel", () => ({
  SecurityScanRunPanel: () => <div data-testid="scan-run-panel" />,
}));

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

const FINDING_ID = "22222222-2222-2222-2222-222222222222";
const OTHER_ID = "33333333-3333-3333-3333-333333333333";

function scanFixture() {
  return create(SecurityScanSchema, {
    id: "11111111-1111-1111-1111-111111111111",
    namespace: "user-alice",
    scanName: "nightly",
    runName: "nightly-1",
    repository: "github.com/acme/payments",
    status: "completed",
  });
}

function findingFixture(overrides: Record<string, unknown> = {}) {
  return create(SecurityFindingSchema, {
    id: FINDING_ID,
    namespace: "user-alice",
    scanName: "nightly",
    runName: "nightly-1",
    title: "SQL injection in payment lookup",
    category: "injection",
    severity: "critical",
    filePath: "internal/db/query.go",
    startLine: 42,
    score: 9.5,
    status: "open",
    occurrences: 1,
    baselineState: "regressed",
    assignee: "alice",
    ...overrides,
  });
}

function renderScanDetail() {
  render(
    <MemoryRouter initialEntries={["/security/user-alice/nightly-1"]}>
      <Routes>
        <Route path="/security/:namespace/:runName" element={<SecurityScanDetail />} />
      </Routes>
    </MemoryRouter>,
  );
}

function renderFindingDetail() {
  render(
    <MemoryRouter initialEntries={[`/security/user-alice/nightly-1/findings/${FINDING_ID}`]}>
      <Routes>
        <Route
          path="/security/:namespace/:runName/findings/:findingId"
          element={<SecurityFindingDetail />}
        />
      </Routes>
    </MemoryRouter>,
  );
}

function mockScanPage(findings = [findingFixture()]) {
  mocks.getSecurityScan.mockResolvedValue(scanFixture());
  mocks.getSecurityFindingSummary.mockResolvedValue({ counts: {} });
  mocks.listSecurityFindings.mockResolvedValue({ findings });
  mocks.listSecuritySavedFilters.mockResolvedValue({ filters: [] });
}

describe("security baseline helpers", () => {
  it("classifies accepted-risk expiries relative to now", () => {
    const now = Date.now();
    const future = acceptedRiskExpiry(timestampFromDate(new Date(now + 3 * 86400000 + 1000)), now);
    expect(future).toEqual({ label: "expires in 4d", expired: false });
    const past = acceptedRiskExpiry(timestampFromDate(new Date(now - 1000)), now);
    expect(past).toEqual({ label: "expired", expired: true });
    expect(acceptedRiskExpiry(undefined, now)).toBeNull();
  });

  it("formats trend durations", () => {
    expect(formatDurationSeconds(0)).toBe("—");
    expect(formatDurationSeconds(120)).toBe("2m");
    expect(formatDurationSeconds(7200)).toBe("2h");
    expect(formatDurationSeconds(3 * 86400)).toBe("3d");
  });
});

describe("SecurityScanDetail collaboration", () => {
  it("renders baseline chips, assignee column, and expiry badge", async () => {
    mockScanPage([
      findingFixture(),
      findingFixture({
        id: OTHER_ID,
        title: "Stale accepted risk",
        status: "accepted_risk",
        baselineState: "recurring",
        assignee: "",
        acceptedRiskExpiresAt: timestampFromDate(new Date(Date.now() - 86400000)),
      }),
    ]);
    renderScanDetail();

    await screen.findByText("SQL injection in payment lookup");
    const row1 = screen.getByText("SQL injection in payment lookup").closest("tr");
    expect(row1?.textContent).toContain("regressed");
    expect(row1?.textContent).toContain("alice");
    const row2 = screen.getByText("Stale accepted risk").closest("tr");
    expect(row2?.textContent).toContain("recurring");
    expect(row2?.textContent).toContain("expired");
  });

  it("bulk-updates selected findings and clears the selection on success", async () => {
    mockScanPage([findingFixture(), findingFixture({ id: OTHER_ID, title: "Second finding" })]);
    mocks.bulkUpdateSecurityFindingStatus.mockResolvedValue({
      results: [
        { id: FINDING_ID, ok: true, error: "" },
        { id: OTHER_ID, ok: true, error: "" },
      ],
      updated: 2,
    });
    renderScanDetail();
    await screen.findByText("SQL injection in payment lookup");

    fireEvent.click(screen.getByLabelText("Select all findings"));
    const toolbar = await screen.findByRole("toolbar", { name: "Bulk actions" });
    expect(toolbar.textContent).toContain("2 selected");

    fireEvent.change(screen.getByLabelText("Bulk status"), { target: { value: "triaged" } });
    fireEvent.change(screen.getByLabelText("Bulk note"), { target: { value: "sweep" } });
    fireEvent.click(screen.getByRole("button", { name: "Apply status" }));

    await waitFor(() => {
      expect(mocks.bulkUpdateSecurityFindingStatus).toHaveBeenCalledWith(
        expect.objectContaining({
          namespace: "user-alice",
          scanName: "nightly",
          status: "triaged",
          note: "sweep",
          setAssignee: false,
        }),
      );
    });
    const call = mocks.bulkUpdateSecurityFindingStatus.mock.calls[0][0] as { ids: string[] };
    expect([...call.ids].sort()).toEqual([FINDING_ID, OTHER_ID]);
    await waitFor(() => {
      expect(screen.queryByRole("toolbar", { name: "Bulk actions" })).toBeNull();
    });
  });

  it("reports per-item outcomes when a bulk update aborts", async () => {
    mockScanPage([findingFixture()]);
    mocks.bulkUpdateSecurityFindingStatus.mockResolvedValue({
      results: [{ id: FINDING_ID, ok: false, error: "aborted: batch rolled back" }],
      updated: 0,
    });
    renderScanDetail();
    await screen.findByText("SQL injection in payment lookup");

    fireEvent.click(screen.getByLabelText("Select all findings"));
    fireEvent.change(await screen.findByLabelText("Bulk status"), { target: { value: "fixed" } });
    fireEvent.click(screen.getByRole("button", { name: "Apply status" }));

    const alert = await screen.findByRole("alert");
    expect(alert.textContent).toContain("no findings were changed");
    expect(alert.textContent).toContain("aborted: batch rolled back");
  });

  it("saves, applies, and deletes saved filter views", async () => {
    mockScanPage();
    mocks.listSecuritySavedFilters.mockResolvedValue({
      filters: [
        create(SecuritySavedFilterSchema, {
          namespace: "user-alice",
          owner: "alice",
          name: "criticals",
          query: JSON.stringify({ severity: "critical" }),
        }),
      ],
    });
    mocks.saveSecuritySavedFilter.mockResolvedValue({});
    mocks.deleteSecuritySavedFilter.mockResolvedValue({});
    renderScanDetail();
    await screen.findByText("SQL injection in payment lookup");

    // Save the current view.
    fireEvent.change(screen.getByLabelText("New saved view name"), { target: { value: "my view" } });
    fireEvent.click(screen.getByRole("button", { name: "Save view" }));
    await waitFor(() => {
      expect(mocks.saveSecuritySavedFilter).toHaveBeenCalledWith({
        namespace: "user-alice",
        name: "my view",
        query: "{}",
      });
    });

    // Apply a saved view: its query lands in the filters.
    fireEvent.change(screen.getByLabelText("Saved views"), { target: { value: "criticals" } });
    await waitFor(() => {
      const severity = screen.getByLabelText("Filter by severity") as HTMLSelectElement;
      expect(severity.value).toBe("critical");
    });

    // Delete the applied view.
    fireEvent.click(screen.getByRole("button", { name: "Delete saved view criticals" }));
    await waitFor(() => {
      expect(mocks.deleteSecuritySavedFilter).toHaveBeenCalledWith({
        namespace: "user-alice",
        name: "criticals",
      });
    });
  });

  it("downloads the audit log as CSV", async () => {
    mockScanPage();
    mocks.exportSecurityFindingAuditLog.mockResolvedValue({
      content: new TextEncoder().encode("event_id,created_at\n"),
      filename: "security-audit-nightly.csv",
      contentType: "text/csv",
      eventCount: 1,
    });
    renderScanDetail();
    await screen.findByText("SQL injection in payment lookup");

    fireEvent.click(screen.getByRole("button", { name: /Audit CSV/ }));
    await waitFor(() => {
      expect(mocks.exportSecurityFindingAuditLog).toHaveBeenCalledWith({
        namespace: "user-alice",
        scanName: "nightly",
        format: "csv",
      });
      expect(mocks.downloadBlob).toHaveBeenCalledWith(
        "security-audit-nightly.csv",
        expect.anything(),
        "text/csv",
      );
    });
  });
});

describe("SecurityFindingDetail collaboration", () => {
  function mockFindingPage(finding = findingFixture()) {
    mocks.getSecurityScan.mockResolvedValue(scanFixture());
    mocks.getSecurityFinding.mockResolvedValue({ finding, events: [] });
    mocks.listSecurityFindings.mockResolvedValue({ findings: [finding] });
    mocks.listSecurityFindingEvents.mockResolvedValue({ events: [] });
  }

  it("sets the assignee through the triage editor", async () => {
    const finding = findingFixture({ assignee: "" });
    mockFindingPage(finding);
    mocks.updateSecurityFindingAssignee.mockResolvedValue(findingFixture({ assignee: "bob" }));
    renderFindingDetail();
    await screen.findByLabelText("Assignee");

    fireEvent.change(screen.getByLabelText("Assignee"), { target: { value: "bob" } });
    fireEvent.click(screen.getByRole("button", { name: "Set assignee" }));

    await waitFor(() => {
      expect(mocks.updateSecurityFindingAssignee).toHaveBeenCalledWith({
        id: FINDING_ID,
        namespace: "user-alice",
        assignee: "bob",
      });
      expect(mocks.toastSuccess).toHaveBeenCalledWith("Assigned to bob");
    });
  });

  it("shows the expiry picker for accepted risk and sends the timestamp", async () => {
    mockFindingPage();
    mocks.updateSecurityFindingStatus.mockResolvedValue(findingFixture({ status: "accepted_risk" }));
    renderFindingDetail();
    await screen.findByLabelText("Status");

    expect(screen.queryByLabelText(/Accepted until/)).toBeNull();
    fireEvent.change(screen.getByLabelText("Status"), { target: { value: "accepted_risk" } });
    const picker = await screen.findByLabelText(/Accepted until/);
    fireEvent.change(picker, { target: { value: "2999-01-01T00:00" } });
    fireEvent.click(screen.getByRole("button", { name: "Update status" }));

    await waitFor(() => {
      expect(mocks.updateSecurityFindingStatus).toHaveBeenCalledWith(
        expect.objectContaining({
          id: FINDING_ID,
          status: "accepted_risk",
          acceptedRiskExpiresAt: timestampFromDate(new Date("2999-01-01T00:00")),
        }),
      );
    });
  });

  it("links and unlinks tickets and creates GitHub issues", async () => {
    const finding = findingFixture({ ticketUrl: "", ticketProvider: "" });
    mockFindingPage(finding);
    mocks.createSecurityFindingTicket.mockResolvedValue(
      findingFixture({
        ticketUrl: "https://github.com/acme/payments/issues/9",
        ticketProvider: "github",
      }),
    );
    renderFindingDetail();
    await screen.findByLabelText(/Create a GitHub issue/);

    fireEvent.change(screen.getByLabelText(/Create a GitHub issue/), {
      target: { value: "payments" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Create GitHub issue" }));
    await waitFor(() => {
      expect(mocks.createSecurityFindingTicket).toHaveBeenCalledWith({
        id: FINDING_ID,
        namespace: "user-alice",
        provider: "github",
        repositoryRef: "payments",
      });
    });

    // Once linked, the section shows the link with an unlink control.
    const link = await screen.findByRole("link", {
      name: "https://github.com/acme/payments/issues/9",
    });
    expect(link.getAttribute("href")).toBe("https://github.com/acme/payments/issues/9");
    mocks.updateSecurityFindingTicket.mockResolvedValue(findingFixture({ ticketUrl: "" }));
    fireEvent.click(screen.getByRole("button", { name: "Unlink" }));
    await waitFor(() => {
      expect(mocks.updateSecurityFindingTicket).toHaveBeenCalledWith({
        id: FINDING_ID,
        namespace: "user-alice",
        ticketUrl: "",
      });
    });
  });

  it("shows the baseline badge in the header", async () => {
    mockFindingPage(findingFixture({ baselineState: "reopened" }));
    renderFindingDetail();
    await screen.findByText("SQL injection in payment lookup");
    expect(screen.getAllByText("reopened").length).toBeGreaterThan(0);
  });
});
