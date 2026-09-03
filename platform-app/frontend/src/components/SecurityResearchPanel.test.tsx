import { create } from "@bufbuild/protobuf";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";

import { SecurityResearchPanel } from "@/components/SecurityResearchPanel";
import {
  SecurityCampaignResearchStatusSchema,
  SecurityResearchCoverageSchema,
  SecurityResearchDossierSchema,
  SecurityResearchHypothesisSchema,
  SecurityResearchSubmissionSchema,
  SecurityResearchVariantSweepSchema,
  SecuritySubmissionOutcomeEventSchema,
} from "@/rpc/platform/service_pb";

const mocks = vi.hoisted(() => ({
  getSecurityCampaignResearchStatus: vi.fn(),
  getSecurityResearchDossier: vi.fn(),
  listSecurityResearchHypotheses: vi.fn(),
  listSecurityResearchCoverage: vi.fn(),
  listSecurityResearchVariantSweeps: vi.fn(),
  amendSecurityResearchDossier: vi.fn(),
  createSecurityResearchHypothesis: vi.fn(),
  transitionSecurityResearchHypothesis: vi.fn(),
  recordSecurityResearchCoverage: vi.fn(),
  createSecurityResearchVariantSweep: vi.fn(),
  completeSecurityResearchVariantSweep: vi.fn(),
  listSecurityResearchSubmissions: vi.fn(),
  listSecuritySubmissionOutcomeHistory: vi.fn(),
  recordSecuritySubmissionOutcome: vi.fn(),
  correctSecuritySubmissionOutcome: vi.fn(),
}));

vi.mock("@/lib/client", () => ({ client: mocks }));

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

beforeEach(() => {
  mocks.getSecurityCampaignResearchStatus.mockResolvedValue(create(SecurityCampaignResearchStatusSchema, {
    targetKey: "nightly",
    revision: "abc123def456",
    workflow: "bounty",
    dossierVersion: 2,
    hypothesisStatusCounts: { investigating: 1 },
    hypothesisResultCounts: { pending: 1 },
    coverageVerdictCounts: { adequately_tested: 1 },
    variantSweepStatusCounts: { running: 1 },
    precision: { submitted: 3n, accepted: 2n, duplicate: 1n },
  }));
  mocks.getSecurityResearchDossier.mockResolvedValue(create(SecurityResearchDossierSchema, {
    id: "11111111-1111-4111-8111-111111111111",
    version: 2,
    contentJson: '{"scope":"contracts"}',
    changeSummary: "Bound deployed contracts",
    actor: "alice",
  }));
  mocks.listSecurityResearchHypotheses.mockResolvedValue({
    hypotheses: [create(SecurityResearchHypothesisSchema, {
      id: "22222222-2222-4222-8222-222222222222",
      hypothesisKey: "H-01",
      title: "Authorization bypass",
      invariant: "Only owners can withdraw",
      status: "investigating",
      result: "pending",
      detailJson: "{}",
      version: 1,
    })],
  });
  mocks.listSecurityResearchCoverage.mockResolvedValue({
    coverage: [create(SecurityResearchCoverageSchema, {
      id: "33333333-3333-4333-8333-333333333333",
      dimension: "invariant",
      subjectKey: "withdraw-owner-check",
      verdict: "adequately_tested",
      actor: "alice",
    })],
  });
  mocks.listSecurityResearchVariantSweeps.mockResolvedValue({
    sweeps: [create(SecurityResearchVariantSweepSchema, {
      id: "44444444-4444-4444-8444-444444444444",
      rootCause: "Missing ownership check",
      status: "running",
    })],
  });
  mocks.listSecurityResearchSubmissions.mockResolvedValue({
    submissions: [
      create(SecurityResearchSubmissionSchema, {
        id: "55555555-5555-4555-8555-555555555555",
        findingId: "66666666-6666-4666-8666-666666666666",
        findingFingerprint: "fp-withdraw",
        findingTitle: "Unauthorized withdraw",
        workflow: "bounty",
        candidateKey: "fp-withdraw",
        rank: 1,
        status: "submitted",
        latestOutcome: "accepted",
      }),
      create(SecurityResearchSubmissionSchema, {
        id: "77777777-7777-4777-8777-777777777777",
        findingFingerprint: "fp-oracle",
        findingTitle: "Stale oracle price",
        workflow: "bounty",
        candidateKey: "fp-oracle",
        rank: 2,
        status: "candidate",
      }),
    ],
    truncated: false,
  });
  mocks.listSecuritySubmissionOutcomeHistory.mockResolvedValue({
    events: [create(SecuritySubmissionOutcomeEventSchema, {
      id: 1n,
      submissionId: "55555555-5555-4555-8555-555555555555",
      outcome: "accepted",
      rationale: "Triager confirmed the report",
      actor: "alice",
      idempotencyKey: "outcome-1",
    })],
    truncated: false,
  });
});

function renderPanel(permission = "owner") {
  return render(
    <SecurityResearchPanel
      namespace="user-alice"
      targetKey="nightly"
      revision="abc123def456"
      workflow="bounty"
      permission={permission}
    />,
  );
}

function sectionButton(label: string, button: string) {
  const summary = screen.getByText(label).closest("summary");
  if (!summary) throw new Error(`missing section ${label}`);
  return within(summary).getByRole("button", { name: button });
}

describe("SecurityResearchPanel", () => {
  it("renders campaign status and durable research records for the selected revision", async () => {
    renderPanel();

    expect(await screen.findByText("Authorization bypass")).toBeTruthy();
    expect(screen.getByText(/nightly@abc123def456/)).toBeTruthy();
    expect(screen.getByText(/"scope": "contracts"/)).toBeTruthy();
    expect(screen.getByText("withdraw-owner-check")).toBeTruthy();
    expect(screen.getByText("Missing ownership check")).toBeTruthy();
    expect(screen.getByText(/submissions 3 · accepted 2 · duplicates 1/)).toBeTruthy();
    expect(mocks.getSecurityCampaignResearchStatus).toHaveBeenCalledWith(expect.objectContaining({ workflow: "bounty" }));
    expect(mocks.listSecurityResearchHypotheses).toHaveBeenCalledWith(expect.objectContaining({ limit: 200 }));
  });

  it("keeps viewer research access read-only", async () => {
    renderPanel("viewer");
    await screen.findByText("Authorization bypass");

    expect(screen.getByText("Viewer access can inspect research but cannot change it.")).toBeTruthy();
    expect(sectionButton("Hypotheses · 1", "Create").hasAttribute("disabled")).toBe(true);
    expect(sectionButton("Variant sweeps · 1", "Create").hasAttribute("disabled")).toBe(true);
    expect(screen.getByRole("button", { name: "Record outcome" }).hasAttribute("disabled")).toBe(true);
  });

  it("fails closed when permission is missing", async () => {
    renderPanel("");
    await screen.findByText("Authorization bypass");

    expect(sectionButton("Hypotheses · 1", "Create").hasAttribute("disabled")).toBe(true);
    expect(screen.getByRole("button", { name: "Record outcome" }).hasAttribute("disabled")).toBe(true);
  });

  it("validates hypothesis JSON before sending a create request", async () => {
    renderPanel();
    await screen.findByText("Authorization bypass");

    fireEvent.click(sectionButton("Hypotheses · 1", "Create"));
    fireEvent.change(screen.getByLabelText("Hypothesis key"), { target: { value: "H-02" } });
    fireEvent.change(screen.getByLabelText("Title"), { target: { value: "Reentrancy" } });
    fireEvent.change(screen.getByLabelText("Invariant"), { target: { value: "State changes precede calls" } });
    fireEvent.change(screen.getByLabelText("Detail (JSON)"), { target: { value: "{" } });
    fireEvent.click(screen.getByRole("button", { name: "Create hypothesis" }));

    expect((await screen.findByRole("alert")).textContent).toContain("Detail must be valid JSON.");
    expect(mocks.createSecurityResearchHypothesis).not.toHaveBeenCalled();
  });

  it("requires completed sweep evidence before calling the RPC", async () => {
    renderPanel();
    await screen.findByText("Missing ownership check");

    fireEvent.click(screen.getByRole("button", { name: "Complete" }));
    fireEvent.click(screen.getByRole("button", { name: "Complete sweep" }));

    expect((await screen.findByRole("alert")).textContent).toContain("Completed results require");
    expect(mocks.completeSecurityResearchVariantSweep).not.toHaveBeenCalled();
  });

  it("rejects a null completed-sweep result without crashing", async () => {
    renderPanel();
    await screen.findByText("Missing ownership check");

    fireEvent.click(screen.getByRole("button", { name: "Complete" }));
    fireEvent.change(screen.getByLabelText("Result (JSON)"), { target: { value: "null" } });
    fireEvent.click(screen.getByRole("button", { name: "Complete sweep" }));

    expect((await screen.findByRole("alert")).textContent).toContain("must be a JSON object");
    expect(mocks.completeSecurityResearchVariantSweep).not.toHaveBeenCalled();
  });

  it("lists packaged submissions and fills the submission ID from a row", async () => {
    renderPanel();
    await screen.findByText("Authorization bypass");
    expect(mocks.listSecurityResearchSubmissions).not.toHaveBeenCalled();

    fireEvent.click(screen.getByRole("button", { name: "Refresh submissions" }));

    expect(await screen.findByText("Unauthorized withdraw")).toBeTruthy();
    expect(screen.getByText("Stale oracle price")).toBeTruthy();
    expect(mocks.listSecurityResearchSubmissions).toHaveBeenCalledWith(expect.objectContaining({ limit: 200 }));
    const table = screen.getByRole("table");
    expect(within(table).getAllByRole("row")).toHaveLength(3);
    expect(within(table).getByText("accepted")).toBeTruthy();
    expect(within(table).getByText("pending")).toBeTruthy();

    fireEvent.click(screen.getByRole("button", { name: "Use submission 55555555-5555-4555-8555-555555555555" }));

    expect((screen.getByLabelText("Submission ID") as HTMLInputElement).value).toBe("55555555-5555-4555-8555-555555555555");
    expect(await screen.findByText("Triager confirmed the report", { exact: false })).toBeTruthy();
    expect(mocks.listSecuritySubmissionOutcomeHistory).toHaveBeenCalledWith(expect.objectContaining({ submissionId: "55555555-5555-4555-8555-555555555555", limit: 200 }));
  });

  it("shows an empty state when no submissions were packaged", async () => {
    mocks.listSecurityResearchSubmissions.mockResolvedValue({ submissions: [], truncated: false });
    renderPanel();
    await screen.findByText("Authorization bypass");

    fireEvent.click(screen.getByRole("button", { name: "Refresh submissions" }));

    expect(await screen.findByText("No packaged submissions for this revision yet")).toBeTruthy();
    expect(screen.queryByRole("table")).toBeNull();
  });

  it("validates submission IDs before loading outcome history", async () => {
    renderPanel();
    await screen.findByText("Authorization bypass");

    fireEvent.change(screen.getByLabelText("Submission ID"), { target: { value: "not-a-uuid" } });
    fireEvent.click(screen.getByRole("button", { name: "Load history" }));

    await waitFor(() => expect(screen.getByText("Submission ID must be a valid UUID.")).toBeTruthy());
    expect(mocks.listSecuritySubmissionOutcomeHistory).not.toHaveBeenCalled();
  });
});
