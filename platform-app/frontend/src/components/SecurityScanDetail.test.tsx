import { create } from "@bufbuild/protobuf";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";

import { SecurityScanDetail } from "@/components/SecurityScanDetail";
import {
  SecurityFindingSchema,
  SecurityScanSchema,
} from "@/rpc/platform/service_pb";

const { getSecurityScan, getSecurityFindingSummary, listSecurityFindings, updateSecurityFindingStatus } =
  vi.hoisted(() => ({
    getSecurityScan: vi.fn(),
    getSecurityFindingSummary: vi.fn(),
    listSecurityFindings: vi.fn(),
    updateSecurityFindingStatus: vi.fn(),
  }));

vi.mock("@/lib/client", () => ({
  client: {
    getSecurityScan,
    getSecurityFindingSummary,
    listSecurityFindings,
    updateSecurityFindingStatus,
  },
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
      });
    });
  });

  it("updates the finding status from the panel and refreshes", async () => {
    getSecurityScan.mockResolvedValue(scanFixture());
    getSecurityFindingSummary.mockResolvedValue({ counts: {} });
    listSecurityFindings
      .mockResolvedValueOnce({ findings: [findingFixture()] })
      .mockResolvedValue({ findings: [findingFixture("triaged")] });
    updateSecurityFindingStatus.mockResolvedValue(findingFixture("triaged"));

    renderDetail();

    fireEvent.click(await screen.findByRole("button", { name: "SQL injection in payment lookup" }));
    fireEvent.change(screen.getByLabelText("Status"), { target: { value: "triaged" } });

    await waitFor(() => {
      expect(updateSecurityFindingStatus).toHaveBeenCalledWith({
        id: FINDING_ID,
        status: "triaged",
        note: "",
      });
    });
    // Optimistic update plus refetch leave the row in the new status.
    await waitFor(() => {
      expect(listSecurityFindings.mock.calls.length).toBeGreaterThan(1);
    });
    expect((screen.getByLabelText("Status") as HTMLSelectElement).value).toBe("triaged");
  });
});
