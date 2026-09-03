import { create, type MessageInitShape } from "@bufbuild/protobuf";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";

import {
  SecuritySubmissionQueue, acceptanceRate, awaitingHandoff, statusLabel,
} from "@/components/SecuritySubmissionQueue";
import {
  SecuritySubmissionPrecisionRollupSchema,
  SecuritySubmissionPrecisionSchema,
  SecuritySubmissionQueueItemSchema,
} from "@/rpc/platform/service_pb";

const mocks = vi.hoisted(() => ({
  listSecuritySubmissionQueue: vi.fn(),
  getSecuritySubmissionPrecisionRollup: vi.fn(),
  getSecurityFindingSubmissionBundle: vi.fn(),
  markSecuritySubmissionSubmitted: vi.fn(),
  recordSecuritySubmissionOutcome: vi.fn(),
  downloadBlob: vi.fn(),
}));

vi.mock("@/lib/client", () => ({ client: mocks }));
vi.mock("@/lib/download", () => ({ downloadBlob: mocks.downloadBlob }));

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

const HOUR = 60 * 60 * 1000;

function item(overrides: MessageInitShape<typeof SecuritySubmissionQueueItemSchema> = {}) {
  return create(SecuritySubmissionQueueItemSchema, {
    findingId: "11111111-1111-4111-8111-111111111111",
    namespace: "user-alice",
    scanName: "nightly",
    runName: "nightly-3",
    title: "Reentrancy in withdraw",
    severity: "critical",
    findingStatus: "confirmed",
    fingerprint: "fp-withdraw",
    repository: "github.com/acme/vault",
    bundleReadyAt: timestampFromDate(new Date(Date.now() - 3 * HOUR)),
    bundleFilename: "fp-withdraw-bounty-submission.zip",
    submissionId: "55555555-5555-4555-8555-555555555555",
    submissionStatus: "packaged",
    targetKey: "nightly",
    revision: "abc123def456",
    workflow: "bounty",
    ...overrides,
  });
}

beforeEach(() => {
  mocks.listSecuritySubmissionQueue.mockResolvedValue({
    items: [
      item(),
      item({
        findingId: "22222222-2222-4222-8222-222222222222",
        title: "Stale oracle price",
        severity: "high",
        fingerprint: "fp-oracle",
        submissionId: "66666666-6666-4666-8666-666666666666",
        submissionStatus: "submitted",
        program: "immunefi",
        externalReference: "IMM-42",
        submittedBy: "alice",
        submittedAt: timestampFromDate(new Date(Date.now() - HOUR)),
        latestOutcome: "accepted",
      }),
      item({
        findingId: "33333333-3333-4333-8333-333333333333",
        title: "Legacy bundle without a durable row",
        severity: "medium",
        fingerprint: "fp-legacy",
        submissionId: "",
        submissionStatus: "",
        targetKey: "",
        revision: "",
      }),
    ],
    truncated: false,
  });
  mocks.getSecuritySubmissionPrecisionRollup.mockResolvedValue(create(SecuritySubmissionPrecisionRollupSchema, {
    total: { submitted: 5n, accepted: 3n, duplicate: 1n, rejected: 1n },
    byProgram: [
      { key: "immunefi", precision: { submitted: 4n, accepted: 3n, duplicate: 1n } },
      { key: "hackerone", precision: { submitted: 1n, rejected: 1n } },
    ],
    byWorkflow: [{ key: "bounty", precision: { submitted: 5n, accepted: 3n, duplicate: 1n, rejected: 1n } }],
  }));
  mocks.markSecuritySubmissionSubmitted.mockResolvedValue({});
  mocks.recordSecuritySubmissionOutcome.mockResolvedValue({ created: true });
});

function renderQueue() {
  return render(
    <MemoryRouter initialEntries={["/security/queue"]}>
      <SecuritySubmissionQueue />
    </MemoryRouter>,
  );
}

async function openActions(title: string) {
  fireEvent.click(await screen.findByRole("button", { name: `Actions for ${title}` }));
  return screen.findByRole("menu");
}

describe("SecuritySubmissionQueue", () => {
  it("renders the cross-scan queue with precision by program and links to finding detail", async () => {
    renderQueue();
    expect(await screen.findByText("Reentrancy in withdraw")).toBeTruthy();
    expect(mocks.listSecuritySubmissionQueue).toHaveBeenCalledWith({ namespace: "", limit: 200 });
    expect(mocks.getSecuritySubmissionPrecisionRollup).toHaveBeenCalledWith({ namespace: "" });

    const link = screen.getByRole("link", { name: "Reentrancy in withdraw" });
    expect(link.getAttribute("href")).toBe("/security/user-alice/nightly-3/findings/11111111-1111-4111-8111-111111111111");

    const table = screen.getByRole("table");
    const rows = within(table).getAllByRole("row").slice(1);
    expect(rows).toHaveLength(3);
    expect(within(rows[0]).getByText("Ready to submit")).toBeTruthy();
    expect(within(rows[1]).getByText("Submitted")).toBeTruthy();
    expect(within(rows[1]).getByText("immunefi")).toBeTruthy();
    expect(within(rows[1]).getByText("IMM-42")).toBeTruthy();
    expect(rows[1].className).toContain("text-muted-foreground");
    expect(rows[0].className).not.toContain("text-muted-foreground");
    expect(within(rows[2]).getByText("Bundle only")).toBeTruthy();
    expect(within(rows[0]).getByText("3h ago")).toBeTruthy();

    const precision = screen.getByRole("region", { name: "Submission precision" });
    expect(within(precision).getByText("immunefi")).toBeTruthy();
    expect(within(precision).getByText("hackerone")).toBeTruthy();
    expect(within(precision).getByText("acceptance 75%")).toBeTruthy();
    expect(within(precision).getByText("acceptance 0%")).toBeTruthy();
    expect(within(precision).getByText("All programs")).toBeTruthy();
    expect(screen.getByRole("link", { name: /Submission queue/ }).getAttribute("aria-current")).toBe("page");
  });

  it("marks a packaged bundle submitted with the program and reference", async () => {
    renderQueue();
    const menu = await openActions("Reentrancy in withdraw");
    fireEvent.click(within(menu).getByRole("menuitem", { name: "Mark submitted" }));

    fireEvent.click(await screen.findByRole("button", { name: "Mark submitted" }));
    expect((await screen.findByRole("alert")).textContent).toContain("Program is required");
    expect(mocks.markSecuritySubmissionSubmitted).not.toHaveBeenCalled();

    fireEvent.change(screen.getByLabelText("Program"), { target: { value: " Immunefi " } });
    fireEvent.change(screen.getByLabelText("External reference"), { target: { value: "IMM-99" } });
    fireEvent.click(screen.getByRole("button", { name: "Mark submitted" }));

    await waitFor(() => expect(mocks.markSecuritySubmissionSubmitted).toHaveBeenCalledTimes(1));
    expect(mocks.markSecuritySubmissionSubmitted.mock.calls[0]?.[0]).toEqual(expect.objectContaining({
      namespace: "user-alice",
      submissionId: "55555555-5555-4555-8555-555555555555",
      program: "Immunefi",
      externalReference: "IMM-99",
    }));
    expect(mocks.markSecuritySubmissionSubmitted.mock.calls[0]?.[0].idempotencyKey).toBeTruthy();
    await waitFor(() => expect(mocks.listSecuritySubmissionQueue).toHaveBeenCalledTimes(2));
  });

  it("disables handoff for filed rows and records outcomes with the research scope", async () => {
    renderQueue();
    const menu = await openActions("Stale oracle price");
    expect(within(menu).getByRole("menuitem", { name: "Mark submitted" }).getAttribute("aria-disabled")).toBe("true");
    fireEvent.click(within(menu).getByRole("menuitem", { name: "Record outcome" }));

    fireEvent.change(await screen.findByLabelText("Outcome"), { target: { value: "duplicate" } });
    fireEvent.change(screen.getByLabelText("Rationale"), { target: { value: "Triager linked an earlier report" } });
    fireEvent.click(screen.getByRole("button", { name: "Record outcome" }));

    await waitFor(() => expect(mocks.recordSecuritySubmissionOutcome).toHaveBeenCalledTimes(1));
    expect(mocks.recordSecuritySubmissionOutcome.mock.calls[0]?.[0]).toEqual(expect.objectContaining({
      scope: { namespace: "user-alice", targetKey: "nightly", revision: "abc123def456" },
      submissionId: "66666666-6666-4666-8666-666666666666",
      outcome: "duplicate",
      externalReference: "IMM-42",
      rationale: "Triager linked an earlier report",
    }));
  });

  it("downloads the finding's bundle through the existing bundle RPC", async () => {
    mocks.getSecurityFindingSubmissionBundle.mockResolvedValue({
      status: "ready", content: new Uint8Array([1, 2, 3]), filename: "fp-withdraw.zip", sha256: "abc", error: "",
    });
    renderQueue();
    const menu = await openActions("Reentrancy in withdraw");
    fireEvent.click(within(menu).getByRole("menuitem", { name: "Download bundle" }));

    await waitFor(() => expect(mocks.downloadBlob).toHaveBeenCalledTimes(1));
    expect(mocks.getSecurityFindingSubmissionBundle).toHaveBeenCalledWith({
      namespace: "user-alice", findingId: "11111111-1111-4111-8111-111111111111",
    });
    expect(mocks.downloadBlob.mock.calls[0]?.[0]).toBe("fp-withdraw.zip");
    expect((await screen.findByRole("status")).textContent).toContain("SHA-256: abc");
  });

  it("filters to bundles still awaiting handoff", async () => {
    renderQueue();
    await screen.findByText("Reentrancy in withdraw");
    fireEvent.change(screen.getByLabelText("Status filter"), { target: { value: "ready" } });
    expect(screen.queryByText("Stale oracle price")).toBeNull();
    expect(screen.queryByText("Legacy bundle without a durable row")).toBeNull();
    expect(screen.getByText("Reentrancy in withdraw")).toBeTruthy();
    expect(screen.getByText("1 of 3 bundles · 1 ready to submit")).toBeTruthy();
  });

  it("shows the cold-start message when nothing has been filed yet", async () => {
    mocks.listSecuritySubmissionQueue.mockResolvedValue({ items: [], truncated: false });
    mocks.getSecuritySubmissionPrecisionRollup.mockResolvedValue(create(SecuritySubmissionPrecisionRollupSchema, {}));
    renderQueue();
    expect(await screen.findByText("No bundles are ready to submit")).toBeTruthy();
    expect(screen.getByText(/No reports have been marked submitted yet/)).toBeTruthy();
  });
});

describe("queue helpers", () => {
  it("treats only a human handoff as submitted", () => {
    expect(statusLabel(item({ submissionStatus: "packaged" }))).toBe("Ready to submit");
    expect(statusLabel(item({ submissionStatus: "submitted" }))).toBe("Submitted");
    expect(awaitingHandoff(item({ submissionStatus: "packaged" }))).toBe(true);
    expect(awaitingHandoff(item({ submissionStatus: "submitted" }))).toBe(false);
    expect(awaitingHandoff(item({ submissionId: "", submissionStatus: "" }))).toBe(false);
  });

  it("computes acceptance over adjudicated outcomes only", () => {
    expect(acceptanceRate(create(SecuritySubmissionPrecisionSchema, { submitted: 10n }))).toBe("—");
    expect(acceptanceRate(create(SecuritySubmissionPrecisionSchema, { submitted: 10n, accepted: 1n, rejected: 3n }))).toBe("25%");
  });
});
