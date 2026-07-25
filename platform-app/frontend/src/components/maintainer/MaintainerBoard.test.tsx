import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";

import { MaintainerBoard } from "@/components/maintainer/MaintainerBoard";
import { TriageDialog } from "@/components/maintainer/commandDialogs";
import type { MaintainerWorkItem, MaintainerWorkItemDecision, MaintainerWorkItemPullRequest } from "@/rpc/platform/service_pb";

const { issueMaintainerCommand } = vi.hoisted(() => ({
  issueMaintainerCommand: vi.fn(),
}));

vi.mock("@/lib/client", () => ({
  client: { issueMaintainerCommand },
}));

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

function makeItem(
  overrides: Partial<MaintainerWorkItem> & Pick<MaintainerWorkItem, "name" | "issueNumber" | "issueTitle" | "phase">,
): MaintainerWorkItem {
  return {
    namespace: "user-alice",
    repositoryName: "acme-payments",
    issueUrl: "",
    issueState: "open",
    disposition: "",
    closeReason: "",
    evidenceSummary: "",
    readyToDispatch: true,
    readyToMerge: false,
    unmetRequirements: [],
    pendingDecision: undefined,
    agentRuns: [],
    pullRequests: [],
    deliverySummary: "",
    deliveredAtUnix: 0n,
    createdAtUnix: 0n,
    childrenTotal: 0,
    childrenDelivered: 0,
    dependenciesTotal: 0,
    dependenciesDelivered: 0,
    latestCommandType: "",
    latestCommandPhase: "",
    latestCommandMessage: "",
    projectionSequence: 5n,
    resourceVersion: "",
    acceptedScopeStatement: "",
    acceptedScopeCriteria: [],
    children: [],
    dependencies: [],
    graphConfigured: false,
    observationFresh: false,
    ...overrides,
  } as unknown as MaintainerWorkItem;
}

function renderBoard(items: MaintainerWorkItem[]) {
  render(
    <MemoryRouter>
      <MaintainerBoard
        items={items}
        namespace="user-alice"
        onRefetch={() => undefined}
      />
    </MemoryRouter>,
  );
}

describe("MaintainerBoard column assignment", () => {
  it("puts AwaitingDecision items in the Needs you column", () => {
    renderBoard([
      makeItem({ name: "wi-1", issueNumber: 1, issueTitle: "Decision needed", phase: "AwaitingDecision" }),
    ]);
    // "Needs you" heading visible
    expect(screen.getByRole("heading", { name: "Needs you" })).toBeTruthy();
    // item visible under it
    expect(screen.getByText("Decision needed")).toBeTruthy();
    // Phase badge
    expect(screen.getByText("Needs decision")).toBeTruthy();
  });

  it("puts PendingTriage items in the Triage column", () => {
    renderBoard([
      makeItem({ name: "wi-2", issueNumber: 2, issueTitle: "Needs triage", phase: "PendingTriage" }),
    ]);
    expect(screen.getByRole("heading", { name: "Triage" })).toBeTruthy();
    expect(screen.getByText("Needs triage")).toBeTruthy();
    expect(screen.getByText("Pending triage")).toBeTruthy();
  });

  it("puts ReadyToDispatch items in the Ready column", () => {
    renderBoard([
      makeItem({ name: "wi-3", issueNumber: 3, issueTitle: "Ready item", phase: "ReadyToDispatch" }),
    ]);
    expect(screen.getByRole("heading", { name: "Ready" })).toBeTruthy();
    expect(screen.getByText("Ready item")).toBeTruthy();
    expect(screen.getByText("Ready to dispatch")).toBeTruthy();
  });

  it("puts Dispatched and Implementing items in the In flight column", () => {
    renderBoard([
      makeItem({ name: "wi-4", issueNumber: 4, issueTitle: "Dispatched item", phase: "Dispatched" }),
      makeItem({ name: "wi-5", issueNumber: 5, issueTitle: "Implementing item", phase: "Implementing" }),
    ]);
    expect(screen.getByRole("heading", { name: "In flight" })).toBeTruthy();
    expect(screen.getByText("Dispatched item")).toBeTruthy();
    expect(screen.getByText("Implementing item")).toBeTruthy();
  });

  it("puts ReadyToMerge items in the Ready to merge column", () => {
    renderBoard([
      makeItem({ name: "wi-6", issueNumber: 6, issueTitle: "Merge me", phase: "ReadyToMerge" }),
    ]);
    expect(screen.getByRole("heading", { name: "Ready to merge" })).toBeTruthy();
    expect(screen.getByText("Merge me")).toBeTruthy();
  });

  it("puts Delivered and NotActionable items in the Shipped column (collapsed by default, shows count)", () => {
    renderBoard([
      makeItem({ name: "wi-7", issueNumber: 7, issueTitle: "Shipped thing", phase: "Delivered", deliverySummary: "Done!" }),
      makeItem({ name: "wi-8", issueNumber: 8, issueTitle: "Not actionable", phase: "Triaged", disposition: "NotActionable" }),
    ]);
    // Column header visible with count
    expect(screen.getByRole("button", { name: /Shipped \(2 items/ })).toBeTruthy();
    // Items NOT visible while collapsed
    expect(screen.queryByText("Shipped thing")).toBeNull();
    // Expand
    fireEvent.click(screen.getByRole("button", { name: /Shipped/ }));
    expect(screen.getByText("Shipped thing")).toBeTruthy();
    // "Not actionable" is both this card's title and its disposition badge.
    expect(screen.getAllByText("Not actionable").length).toBeGreaterThan(0);
    expect(screen.getByText("Done!")).toBeTruthy();
  });
});

describe("MaintainerBoard AwaitingDecision card", () => {
  it("renders the pending question and an Answer button", () => {
    renderBoard([
      makeItem({
        name: "wi-42",
        issueNumber: 42,
        issueTitle: "Login broken",
        phase: "AwaitingDecision",
        pendingDecision: { id: "dec-1", question: "Should we revert?", options: ["yes", "no"] } as unknown as MaintainerWorkItemDecision,
      }),
    ]);
    expect(screen.getByText("Should we revert?")).toBeTruthy();
    expect(screen.getByText("Options: yes · no")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Answer decision" })).toBeTruthy();
  });

  it("answering with an option calls issueMaintainerCommand with decisionId, answer, and projectionSequence", async () => {
    issueMaintainerCommand.mockResolvedValue({ commandName: "rc-1", phase: "Accepted", message: "" });
    const onRefetch = vi.fn();

    render(
      <MemoryRouter>
        <MaintainerBoard
          items={[
            makeItem({
              name: "wi-42",
              issueNumber: 42,
              issueTitle: "Login broken",
              phase: "AwaitingDecision",
              projectionSequence: 7n,
              pendingDecision: { id: "dec-1", question: "Should we revert?", options: ["yes", "no"] } as unknown as MaintainerWorkItemDecision,
            }),
          ]}
          namespace="user-alice"
          onRefetch={onRefetch}
        />
      </MemoryRouter>,
    );

    // Open the answer dialog
    fireEvent.click(screen.getByRole("button", { name: "Answer decision" }));

    // Click the "yes" option button inside the dialog
    const yesBtn = await screen.findByRole("button", { name: "yes" });
    fireEvent.click(yesBtn);

    await waitFor(() => {
      expect(issueMaintainerCommand).toHaveBeenCalledWith(
        expect.objectContaining({
          namespace: "user-alice",
          repositoryName: "acme-payments",
          workItemName: "wi-42",
          expectedProjectionSequence: 7n,
          type: "ResolveDecision",
          resolveDecision: expect.objectContaining({
            decisionId: "dec-1",
            answer: "yes",
          }),
        }),
      );
    });
    await waitFor(() => expect(onRefetch).toHaveBeenCalled());
  });

  it("answering with free text calls issueMaintainerCommand with the typed answer", async () => {
    issueMaintainerCommand.mockResolvedValue({ commandName: "rc-2", phase: "Accepted", message: "" });
    const onRefetch = vi.fn();

    render(
      <MemoryRouter>
        <MaintainerBoard
          items={[
            makeItem({
              name: "wi-43",
              issueNumber: 43,
              issueTitle: "Color scheme",
              phase: "AwaitingDecision",
              projectionSequence: 3n,
              pendingDecision: { id: "dec-2", question: "Which color?", options: [] } as unknown as MaintainerWorkItemDecision,
            }),
          ]}
          namespace="user-alice"
          onRefetch={onRefetch}
        />
      </MemoryRouter>,
    );

    fireEvent.click(screen.getByRole("button", { name: "Answer decision" }));
    const input = await screen.findByRole("textbox", { name: "Custom answer" });
    fireEvent.change(input, { target: { value: "blue" } });
    fireEvent.submit(input.closest("form")!);

    await waitFor(() => {
      expect(issueMaintainerCommand).toHaveBeenCalledWith(
        expect.objectContaining({
          workItemName: "wi-43",
          expectedProjectionSequence: 3n,
          type: "ResolveDecision",
          resolveDecision: expect.objectContaining({ decisionId: "dec-2", answer: "blue" }),
        }),
      );
    });
  });
});

describe("MaintainerBoard request-decision action", () => {
  it("submits a typed RequestDecision command from the active item drawer", async () => {
    issueMaintainerCommand.mockResolvedValue({ commandName: "rc-4", phase: "Accepted", message: "" });
    const onRefetch = vi.fn();

    render(
      <MemoryRouter>
        <MaintainerBoard
          items={[
            makeItem({
              name: "wi-44",
              issueNumber: 44,
              issueTitle: "Choose a rollout",
              phase: "Implementing",
              projectionSequence: 11n,
            }),
          ]}
          namespace="user-alice"
          onRefetch={onRefetch}
        />
      </MemoryRouter>,
    );

    fireEvent.click(screen.getByRole("button", { name: "Work item #44: Choose a rollout" }));
    fireEvent.click(await screen.findByRole("button", { name: "Ask a question" }));

    const form = screen
      .getByText("Ask a question", { selector: "[data-slot='dialog-title']" })
      .closest("form")!;
    fireEvent.submit(form);
    expect(screen.getByRole("alert").textContent).toBe("Decision ID and question are required.");
    expect(issueMaintainerCommand).not.toHaveBeenCalled();

    fireEvent.change(screen.getByRole("textbox", { name: "Decision ID" }), {
      target: { value: "rollout-strategy" },
    });
    fireEvent.change(screen.getByRole("textbox", { name: "Question" }), {
      target: { value: "Which rollout should we use?" },
    });
    fireEvent.change(screen.getByRole("textbox", { name: "Options (optional)" }), {
      target: { value: "Canary\nFull rollout" },
    });
    fireEvent.submit(form);

    await waitFor(() => {
      expect(issueMaintainerCommand).toHaveBeenCalledWith(
        expect.objectContaining({
          namespace: "user-alice",
          repositoryName: "acme-payments",
          workItemName: "wi-44",
          expectedProjectionSequence: 11n,
          type: "RequestDecision",
          requestDecision: expect.objectContaining({
            decisionId: "rollout-strategy",
            question: "Which rollout should we use?",
            options: ["Canary", "Full rollout"],
          }),
        }),
      );
    });
    await waitFor(() => expect(onRefetch).toHaveBeenCalled());
  });
});

describe("MaintainerBoard rejected command receipt", () => {
  it("renders the rejection message on a card", () => {
    renderBoard([
      makeItem({
        name: "wi-50",
        issueNumber: 50,
        issueTitle: "Risky merge",
        phase: "ReadyToMerge",
        latestCommandPhase: "Rejected",
        latestCommandType: "RequestMerge",
        latestCommandMessage: "capacity exhausted",
      }),
    ]);
    expect(screen.getByText(/RequestMerge command rejected: capacity exhausted/)).toBeTruthy();
  });

  it("renders a failed command receipt", () => {
    renderBoard([
      makeItem({
        name: "wi-51",
        issueNumber: 51,
        issueTitle: "Failed dispatch",
        phase: "Dispatched",
        latestCommandPhase: "Failed",
        latestCommandType: "DispatchWorkItem",
        latestCommandMessage: "sandbox error",
      }),
    ]);
    expect(screen.getByText(/DispatchWorkItem command failed: sandbox error/)).toBeTruthy();
  });
});

describe("MaintainerBoard dispatch action", () => {
  it("dispatch button is disabled when item has unmet requirements", () => {
    renderBoard([
      makeItem({
        name: "wi-60",
        issueNumber: 60,
        issueTitle: "Not ready",
        phase: "ReadyToDispatch",
        readyToDispatch: false,
        unmetRequirements: ["graph not configured", "evidence missing"],
      }),
    ]);
    const btn = screen.getByRole("button", { name: "Dispatch now" }) as HTMLButtonElement;
    expect(btn.disabled).toBe(true);
  });

  it("dispatch button shows unmet requirements as tooltip title", () => {
    renderBoard([
      makeItem({
        name: "wi-61",
        issueNumber: 61,
        issueTitle: "Needs work",
        phase: "ReadyToDispatch",
        readyToDispatch: false,
        unmetRequirements: ["evidence missing"],
      }),
    ]);
    const btn = screen.getByRole("button", { name: "Dispatch now" });
    expect(btn.getAttribute("title")).toContain("evidence missing");
  });
});

describe("MaintainerBoard merge dialog", () => {
  it("sends head SHA and merge method when merging", async () => {
    issueMaintainerCommand.mockResolvedValue({ commandName: "rc-3", phase: "Accepted", message: "" });
    const onRefetch = vi.fn();

    render(
      <MemoryRouter>
        <MaintainerBoard
          items={[
            makeItem({
              name: "wi-70",
              issueNumber: 70,
              issueTitle: "Feature branch",
              phase: "ReadyToMerge",
              readyToMerge: true,
              projectionSequence: 9n,
              pullRequests: [
                {
                  repository: "acme/payments",
                  number: 99,
                  url: "https://github.com/acme/payments/pull/99",
                  state: "open",
                  checkState: "Passing",
                  reviewDecision: "APPROVED",
                  draft: false,
                  headSha: "abc1234def5678",
                } as unknown as MaintainerWorkItemPullRequest,
              ],
            }),
          ]}
          namespace="user-alice"
          onRefetch={onRefetch}
        />
      </MemoryRouter>,
    );

    fireEvent.click(screen.getByRole("button", { name: "Merge" }));

    // The dialog should show the HEAD sha (short form) for reference
    expect(await screen.findByText(/abc1234/)).toBeTruthy();

    // Submit with default squash method — find form by dialog heading
    const form = screen.getByText("Merge PR for #70").closest("form")!;
    fireEvent.submit(form);

    await waitFor(() => {
      expect(issueMaintainerCommand).toHaveBeenCalledWith(
        expect.objectContaining({
          workItemName: "wi-70",
          expectedProjectionSequence: 9n,
          type: "RequestMerge",
          requestMerge: expect.objectContaining({
            pullRequestNumber: 99,
            expectedHeadSha: "abc1234def5678",
            mergeMethod: "squash",
          }),
        }),
      );
    });
    await waitFor(() => expect(onRefetch).toHaveBeenCalled());
  });
});

describe("TriageDialog polled defaults", () => {
  it("uses the latest work-item fields when a mounted dialog is opened", () => {
    const original = makeItem({
      name: "wi-triage",
      issueNumber: 70,
      issueTitle: "Triage me",
      phase: "Triaged",
      disposition: "Bounded",
      evidenceSummary: "old evidence",
      acceptedScopeStatement: "old scope",
      acceptedScopeCriteria: ["old criterion"],
      projectionSequence: 5n,
    });
    const updated = makeItem({
      ...original,
      evidenceSummary: "fresh evidence",
      acceptedScopeStatement: "fresh scope",
      acceptedScopeCriteria: ["fresh criterion"],
      projectionSequence: 6n,
    });

    const view = render(
      <MemoryRouter>
        <TriageDialog item={original} trigger={<button type="button">Open triage</button>} onSuccess={() => undefined} />
      </MemoryRouter>,
    );
    view.rerender(
      <MemoryRouter>
        <TriageDialog item={updated} trigger={<button type="button">Open triage</button>} onSuccess={() => undefined} />
      </MemoryRouter>,
    );

    fireEvent.click(screen.getByRole("button", { name: "Open triage" }));
    expect((screen.getByLabelText("Evidence summary") as HTMLTextAreaElement).value).toBe("fresh evidence");
    expect((screen.getByLabelText("Scope statement") as HTMLTextAreaElement).value).toBe("fresh scope");
    expect((screen.getByLabelText("Acceptance criteria") as HTMLTextAreaElement).value).toBe("fresh criterion");
  });
});
