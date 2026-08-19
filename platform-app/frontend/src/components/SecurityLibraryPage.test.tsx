import { create } from "@bufbuild/protobuf";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter, useLocation, useNavigate } from "react-router-dom";
import { ConnectError, Code } from "@connectrpc/connect";

import { SecurityLibraryPage } from "@/components/SecurityLibraryPage";
import {
  SecurityPolicyPackResourceSchema,
  SecurityPostScriptResourceSchema,
  SecurityProgramResourceSchema,
  SecurityRankerResourceSchema,
  SecurityWorkflowResourceSchema,
} from "@/rpc/platform/service_pb";

const {
  listSecurityWorkflows,
  listSecurityRankers,
  listSecurityPostScripts,
  listSecurityPolicyPacks,
  listSecurityPrograms,
  deleteSecurityWorkflow,
  validateSecurityWorkflow,
  createSecurityWorkflow,
  updateSecurityWorkflow,
  createSecurityRanker,
  updateSecurityRanker,
  createSecurityPostScript,
  updateSecurityPostScript,
  deleteSecurityRanker,
  deleteSecurityPostScript,
  createSecurityPolicyPack,
  updateSecurityPolicyPack,
  deleteSecurityPolicyPack,
  deleteSecurityProgram,
} = vi.hoisted(() => ({
  listSecurityWorkflows: vi.fn(),
  listSecurityRankers: vi.fn(),
  listSecurityPostScripts: vi.fn(),
  listSecurityPolicyPacks: vi.fn(),
  listSecurityPrograms: vi.fn(),
  deleteSecurityWorkflow: vi.fn(),
  validateSecurityWorkflow: vi.fn(),
  createSecurityWorkflow: vi.fn(),
  updateSecurityWorkflow: vi.fn(),
  createSecurityRanker: vi.fn(),
  updateSecurityRanker: vi.fn(),
  createSecurityPostScript: vi.fn(),
  updateSecurityPostScript: vi.fn(),
  deleteSecurityRanker: vi.fn(),
  deleteSecurityPostScript: vi.fn(),
  createSecurityPolicyPack: vi.fn(),
  updateSecurityPolicyPack: vi.fn(),
  deleteSecurityPolicyPack: vi.fn(),
  deleteSecurityProgram: vi.fn(),
}));

vi.mock("@/lib/client", () => ({
  client: {
    listSecurityWorkflows,
    listSecurityRankers,
    listSecurityPostScripts,
    listSecurityPolicyPacks,
    listSecurityPrograms,
    deleteSecurityWorkflow,
    validateSecurityWorkflow,
    createSecurityWorkflow,
    updateSecurityWorkflow,
    createSecurityRanker,
    updateSecurityRanker,
    createSecurityPostScript,
    updateSecurityPostScript,
    deleteSecurityRanker,
    deleteSecurityPostScript,
    createSecurityPolicyPack,
    updateSecurityPolicyPack,
    deleteSecurityPolicyPack,
    deleteSecurityProgram,
  },
}));

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

function seedLists() {
  listSecurityWorkflows.mockResolvedValue({
    workflows: [
      create(SecurityWorkflowResourceSchema, {
        namespace: "user-alice",
        name: "payments-workflow",
        description: "payments plan",
        usageCount: 2,
        referencingScans: ["scan-a", "scan-b"],
        tasks: [
          { name: "recon", objective: "map the sql injection surface", role: "vulnerability-hunter" },
        ],
      }),
    ],
  });
  listSecurityRankers.mockResolvedValue({
    rankers: [
      create(SecurityRankerResourceSchema, {
        namespace: "user-alice",
        name: "payments-ranker",
        rules: ["severity-floor: injection=high"],
        usageCount: 0,
      }),
    ],
  });
  listSecurityPostScripts.mockResolvedValue({
    postScripts: [
      create(SecurityPostScriptResourceSchema, {
        namespace: "user-alice",
        name: "write-poc",
        prompt: "write a proof of concept for the finding",
        runOn: "low-and-above-actionable",
        usageCount: 1,
        referencingScans: ["scan-a"],
      }),
    ],
  });
  listSecurityPolicyPacks.mockResolvedValue({
    policyPacks: [
      create(SecurityPolicyPackResourceSchema, {
        namespace: "user-alice",
        name: "prod-policy",
        description: "prod floors",
        minSeverity: "medium",
        failOnSeverity: "high",
        enforced: ["minSeverity", "budgets"],
        suppressions: [
          {
            name: "vendored",
            reason: "third-party",
            owner: "sec-team",
            matcher: { pathGlob: "vendor/**" },
          },
        ],
        retention: { findingDays: 90 },
        budgets: { maxCostUsd: "5" },
        usageCount: 1,
        referencingScans: ["scan-a"],
      }),
    ],
  });
  listSecurityPrograms.mockResolvedValue({
    programs: [
      create(SecurityProgramResourceSchema, {
        namespace: "user-alice",
        name: "acme-bounty",
        provider: "HackerOne",
        displayName: "Acme public bug bounty",
        programUrl: "https://hackerone.com/acme",
        scopePolicy: "In scope: api.example.com\nOut of scope: denial-of-service testing",
        verifiedAt: timestampFromDate(new Date("2026-03-01T12:00:00Z")),
        scanTargets: [
          { scanName: "acme-api", repositoryUrl: "https://github.com/acme/api" },
          { scanName: "acme-contracts", repositoryUrl: "https://github.com/acme/contracts" },
        ],
        usageCount: 1,
        referencingScans: ["scan-a"],
      }),
    ],
  });
}

/** Exposes the query string and a real history "back" for URL assertions. */
function LocationProbe() {
  const location = useLocation();
  const navigate = useNavigate();
  return (
    <>
      <span data-testid="location-search">{location.search}</span>
      <button type="button" onClick={() => navigate(-1)}>
        history back
      </button>
    </>
  );
}

/** Destructive row actions live behind the row's overflow menu. */
async function deleteFromRowMenu(name: string) {
  // Base UI menus open on pointer events; clicking alone can land before the
  // trigger is wired up, so drive the full sequence and wait for the item.
  const trigger = screen.getByRole("button", { name: `More actions for ${name}` });
  fireEvent.pointerDown(trigger, { pointerType: "mouse", button: 0 });
  fireEvent.pointerUp(trigger, { pointerType: "mouse", button: 0 });
  fireEvent.click(trigger);
  await screen.findByRole("menu");
  fireEvent.click(await screen.findByRole("menuitem", { name: /Delete/ }));
}

function renderPage(path = "/security/library") {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <SecurityLibraryPage />
      <LocationProbe />
    </MemoryRouter>,
  );
}

function workflowRowNames(): string[] {
  return screen
    .getAllByTestId(/^workflow-row-/)
    .map((row) => row.getAttribute("data-testid")!.replace("workflow-row-", ""));
}

function seedSortableWorkflows() {
  seedLists();
  listSecurityWorkflows.mockResolvedValue({
    workflows: [
      create(SecurityWorkflowResourceSchema, {
        name: "gamma-flow",
        usageCount: 0,
        createdAtUnix: 300n,
      }),
      create(SecurityWorkflowResourceSchema, {
        name: "alpha-flow",
        usageCount: 1,
        referencingScans: ["scan-a"],
        createdAtUnix: 900n,
      }),
      create(SecurityWorkflowResourceSchema, {
        name: "beta-flow",
        usageCount: 5,
        referencingScans: ["scan-a", "scan-b"],
        createdAtUnix: 100n,
      }),
    ],
  });
}

describe("SecurityLibraryPage", () => {
  it("lists workflows with task and usage counts", async () => {
    seedLists();
    renderPage();
    const row = await screen.findByTestId("workflow-row-payments-workflow");
    expect(row.textContent).toContain("payments-workflow");
    expect(row.textContent).toContain("payments plan");
    expect(row.textContent).toContain("2 scans");
  });

  it("shows rankers and post-scripts in their tabs", async () => {
    seedLists();
    renderPage();
    await screen.findByTestId("workflow-row-payments-workflow");
    fireEvent.click(screen.getByRole("tab", { name: /Rankers/ }));
    expect((await screen.findByTestId("ranker-row-payments-ranker")).textContent).toContain("unused");
    fireEvent.click(screen.getByRole("tab", { name: /Post-scripts/ }));
    const row = await screen.findByTestId("post-script-row-write-poc");
    expect(row.textContent).toContain("low-and-above-actionable");
    expect(row.textContent).toContain("1 scan");

    fireEvent.click(screen.getByRole("button", { name: "Edit write-poc" }));
    const runOn = await screen.findByLabelText("Runs on");
    expect((runOn as HTMLSelectElement).value).toBe("low-and-above-actionable");
    expect(screen.getByRole("option", { name: "low-and-above while actionable" })).toBeTruthy();
    expect(screen.getByText(/successful earlier stage already marked/i).textContent).toContain("finalizers");
  });

  it("surfaces the delete guard error naming referencing scans", async () => {
    seedLists();
    deleteSecurityWorkflow.mockRejectedValue(
      new ConnectError(
        'SecurityWorkflow "payments-workflow" is still referenced by security scans: scan-a, scan-b',
        Code.FailedPrecondition,
      ),
    );
    renderPage();
    await screen.findByTestId("workflow-row-payments-workflow");
    await deleteFromRowMenu("payments-workflow");
    fireEvent.click(await screen.findByRole("button", { name: "Delete" }));
    await waitFor(() => {
      expect(screen.getByTestId("library-action-error").textContent).toContain("scan-a, scan-b");
    });
    expect(deleteSecurityWorkflow).toHaveBeenCalledWith({ namespace: "", name: "payments-workflow" });
  });

  it("blocks saving an invalid workflow in the editor", async () => {
    seedLists();
    renderPage();
    await screen.findByTestId("workflow-row-payments-workflow");
    fireEvent.click(screen.getByRole("button", { name: /New workflow/ }));
    // Empty name + empty tasks: the save button must be disabled.
    const save = await screen.findByRole("button", { name: "Create workflow" });
    expect((save as HTMLButtonElement).disabled).toBe(true);
    expect(createSecurityWorkflow).not.toHaveBeenCalled();
  });

  it("saves workflow parameters through validate and update", async () => {
    seedLists();
    validateSecurityWorkflow.mockResolvedValue({ valid: true, errors: [] });
    updateSecurityWorkflow.mockResolvedValue({});
    renderPage();
    await screen.findByTestId("workflow-row-payments-workflow");
    fireEvent.click(screen.getByRole("button", { name: "Edit payments-workflow" }));
    fireEvent.click(await screen.findByRole("button", { name: "Add parameter" }));
    fireEvent.change(document.getElementById("wf-param-name-0")!, {
      target: { value: "target_service" },
    });
    fireEvent.change(document.getElementById("wf-param-default-0")!, {
      target: { value: "payments-api" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Save workflow" }));

    await waitFor(() => expect(updateSecurityWorkflow).toHaveBeenCalledTimes(1));
    expect(validateSecurityWorkflow.mock.calls[0][0].parameters).toMatchObject([
      { name: "target_service", default: "payments-api", required: false },
    ]);
    expect(updateSecurityWorkflow.mock.calls[0][0].workflow.parameters).toMatchObject([
      { name: "target_service", default: "payments-api", required: false },
    ]);
  });

  it("duplicating a workflow prefills tasks but clears the name", async () => {
    seedLists();
    renderPage();
    await screen.findByTestId("workflow-row-payments-workflow");
    fireEvent.click(screen.getByRole("button", { name: "Duplicate payments-workflow" }));
    const nameInput = (await screen.findByPlaceholderText("payments-deep-dive")) as HTMLInputElement;
    expect(nameInput.value).toBe("");
    // The single task from the source workflow is prefilled on the graph.
    expect(screen.getByTestId("workflow-dag")).toBeTruthy();
  });

  it("lists policy packs with enforced badges, suppressions, and summaries", async () => {
    seedLists();
    renderPage();
    await screen.findByTestId("workflow-row-payments-workflow");
    fireEvent.click(screen.getByRole("tab", { name: /Policy packs/ }));
    const row = await screen.findByTestId("policy-pack-row-prod-policy");
    expect(row.textContent).toContain("prod-policy");
    expect(row.textContent).toContain("prod floors");
    expect(row.textContent).toContain("minSeverity");
    expect(row.textContent).toContain("budgets");
    expect(row.textContent).toContain("1 rule");
    expect(row.textContent).toContain("findings 90d");
    expect(row.textContent).toContain("$5");
    expect(row.textContent).toContain("1 scan");
  });

  it("lists verified security programs with provenance and scope", async () => {
    seedLists();
    renderPage();
    await screen.findByTestId("workflow-row-payments-workflow");
    fireEvent.click(screen.getByRole("tab", { name: /Programs/ }));

    const row = await screen.findByTestId("program-row-acme-bounty");
    expect(row.textContent).toContain("Acme public bug bounty");
    expect(row.textContent).toContain("HackerOne");
    expect(row.textContent).toContain("https://hackerone.com/acme");
    expect(row.textContent).toContain("api.example.com");
    expect(row.textContent).toContain("2 repos");
    expect(row.textContent).toContain("1 scan");
    expect(screen.getByText(/provenance only and does not authorize network testing/i)).toBeTruthy();
  });

  it("keeps existing library resources visible when programs are unavailable", async () => {
    seedLists();
    listSecurityPrograms.mockRejectedValue(new Error("program API unavailable"));
    renderPage();

    expect(await screen.findByTestId("workflow-row-payments-workflow")).toBeTruthy();
    fireEvent.click(screen.getByRole("tab", { name: /Programs/ }));
    expect((await screen.findByRole("alert")).textContent).toContain("program API unavailable");
  });

  it("deletes an unused security program", async () => {
    seedLists();
    deleteSecurityProgram.mockResolvedValue({});
    renderPage();
    await screen.findByTestId("workflow-row-payments-workflow");
    fireEvent.click(screen.getByRole("tab", { name: /Programs/ }));
    await screen.findByTestId("program-row-acme-bounty");
    await deleteFromRowMenu("acme-bounty");
    fireEvent.click(await screen.findByRole("button", { name: "Delete" }));

    await waitFor(() => expect(deleteSecurityProgram).toHaveBeenCalledWith({
      namespace: "",
      name: "acme-bounty",
    }));
  });

  it("creates a policy pack from the dialog", async () => {
    seedLists();
    createSecurityPolicyPack.mockResolvedValue({});
    renderPage();
    await screen.findByTestId("workflow-row-payments-workflow");
    fireEvent.click(screen.getByRole("tab", { name: /Policy packs/ }));
    fireEvent.click(await screen.findByRole("button", { name: /New policy pack/ }));
    fireEvent.change(await screen.findByLabelText(/^Name/), { target: { value: "team-pack" } });
    fireEvent.change(screen.getByLabelText("Minimum severity", { selector: "select" }), {
      target: { value: "high" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Create policy pack" }));
    await waitFor(() => expect(createSecurityPolicyPack).toHaveBeenCalledTimes(1));
    const request = createSecurityPolicyPack.mock.calls[0][0];
    expect(request.policyPack.name).toBe("team-pack");
    expect(request.policyPack.minSeverity).toBe("high");
  });

  it("blocks a suppression rule without a matcher client-side", async () => {
    seedLists();
    renderPage();
    await screen.findByTestId("workflow-row-payments-workflow");
    fireEvent.click(screen.getByRole("tab", { name: /Policy packs/ }));
    fireEvent.click(await screen.findByRole("button", { name: /New policy pack/ }));
    fireEvent.change(await screen.findByLabelText(/^Name/), { target: { value: "team-pack" } });
    fireEvent.click(screen.getByRole("button", { name: "Add suppression rule" }));
    fireEvent.change(screen.getByLabelText(/Rule name/), { target: { value: "noisy" } });
    fireEvent.click(screen.getByRole("button", { name: "Create policy pack" }));
    const errors = await screen.findByTestId("pp-validation-errors");
    expect(errors.textContent).toContain("at least one matcher field");
    expect(errors.textContent).toContain("reason");
    expect(errors.textContent).toContain("owner");
    expect(createSecurityPolicyPack).not.toHaveBeenCalled();
  });

  it("surfaces server validation errors verbatim in the pack dialog", async () => {
    seedLists();
    createSecurityPolicyPack.mockRejectedValue(
      new ConnectError(
        'enforced[0]: enforcing budgets requires at least one budget limit to be set',
        Code.InvalidArgument,
      ),
    );
    renderPage();
    await screen.findByTestId("workflow-row-payments-workflow");
    fireEvent.click(screen.getByRole("tab", { name: /Policy packs/ }));
    fireEvent.click(await screen.findByRole("button", { name: /New policy pack/ }));
    fireEvent.change(await screen.findByLabelText(/^Name/), { target: { value: "team-pack" } });
    fireEvent.click(screen.getByRole("button", { name: "Create policy pack" }));
    const error = await screen.findByTestId("pp-server-error");
    expect(error.textContent).toContain("enforcing budgets requires at least one budget limit");
  });

  it("surfaces the policy pack delete guard naming referencing scans", async () => {
    seedLists();
    deleteSecurityPolicyPack.mockRejectedValue(
      new ConnectError(
        'SecurityPolicyPack "prod-policy" is still referenced by security scans: scan-a',
        Code.FailedPrecondition,
      ),
    );
    renderPage();
    await screen.findByTestId("workflow-row-payments-workflow");
    fireEvent.click(screen.getByRole("tab", { name: /Policy packs/ }));
    await screen.findByTestId("policy-pack-row-prod-policy");
    await deleteFromRowMenu("prod-policy");
    fireEvent.click(await screen.findByRole("button", { name: "Delete" }));
    await waitFor(() => {
      expect(screen.getByTestId("library-action-error").textContent).toContain("scan-a");
    });
    expect(deleteSecurityPolicyPack).toHaveBeenCalledWith({ namespace: "", name: "prod-policy" });
  });
});

describe("SecurityLibraryPage URL state", () => {
  it("opens the deep-linked tab and query", async () => {
    seedLists();
    renderPage("/security/library?tab=policy-packs&q=vendor");

    // The query matches only through the pack's suppression matcher.
    expect(await screen.findByTestId("policy-pack-row-prod-policy")).toBeTruthy();
    expect(
      screen.getByRole("tab", { name: /Policy packs/ }).getAttribute("aria-selected"),
    ).toBe("true");
    expect((screen.getByPlaceholderText("Search the library…") as HTMLInputElement).value).toBe(
      "vendor",
    );
    // Tab labels carry the count of what the current filters leave visible,
    // rendered as a badge beside the label.
    expect(screen.getByRole("tab", { name: /Policy packs/ }).textContent).toBe("Policy packs1");
    expect(screen.getByRole("tab", { name: /Workflows/ }).textContent).toBe("Workflows0");
  });

  it("falls back to the first tab for an unknown tab value", async () => {
    seedLists();
    renderPage("/security/library?tab=bogus");

    expect(await screen.findByTestId("workflow-row-payments-workflow")).toBeTruthy();
    expect(screen.getByRole("tab", { name: /Workflows/ }).getAttribute("aria-selected")).toBe(
      "true",
    );
  });

  it("pushes the tab into history so the back button walks tabs", async () => {
    seedLists();
    renderPage();
    await screen.findByTestId("workflow-row-payments-workflow");

    fireEvent.click(screen.getByRole("tab", { name: /Rankers/ }));
    await waitFor(() =>
      expect(screen.getByTestId("location-search").textContent).toBe("?tab=rankers"),
    );
    expect(await screen.findByTestId("ranker-row-payments-ranker")).toBeTruthy();

    fireEvent.click(screen.getByRole("button", { name: "history back" }));
    await waitFor(() => expect(screen.getByTestId("location-search").textContent).toBe(""));
    expect(await screen.findByTestId("workflow-row-payments-workflow")).toBeTruthy();
  });

  it("searches the content that identifies each kind", async () => {
    seedLists();
    // Workflow task objective.
    renderPage("/security/library?q=sql injection");
    expect(await screen.findByTestId("workflow-row-payments-workflow")).toBeTruthy();
    cleanup();

    // Ranker rule text.
    renderPage("/security/library?tab=rankers&q=severity-floor");
    expect(await screen.findByTestId("ranker-row-payments-ranker")).toBeTruthy();
    cleanup();

    // Post-script prompt.
    renderPage("/security/library?tab=post-scripts&q=proof of concept");
    expect(await screen.findByTestId("post-script-row-write-poc")).toBeTruthy();
    cleanup();

    // Program provider.
    renderPage("/security/library?tab=programs&q=hackerone");
    expect(await screen.findByTestId("program-row-acme-bounty")).toBeTruthy();
  });

  it("filters a tab down to unused library content", async () => {
    seedLists();
    renderPage("/security/library?tab=rankers&usage=unused");

    expect(await screen.findByTestId("ranker-row-payments-ranker")).toBeTruthy();
    cleanup();

    // The same ranker is excluded when only referenced content is wanted.
    renderPage("/security/library?tab=rankers&usage=in-use");
    expect(await screen.findByText("Nothing matches these filters")).toBeTruthy();
    expect(screen.queryByTestId("ranker-row-payments-ranker")).toBeNull();
  });

  it("clears the filters of the current tab without leaving it", async () => {
    seedLists();
    renderPage("/security/library?tab=post-scripts&usage=unused&q=poc");

    expect(await screen.findByText("Nothing matches these filters")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Clear filters" }));

    expect(await screen.findByTestId("post-script-row-write-poc")).toBeTruthy();
    await waitFor(() =>
      expect(screen.getByTestId("location-search").textContent).toBe("?tab=post-scripts"),
    );
  });

  it("sorts by name, most used, and recently created", async () => {
    seedSortableWorkflows();
    renderPage();
    await screen.findByTestId("workflow-row-alpha-flow");
    expect(workflowRowNames()).toEqual(["alpha-flow", "beta-flow", "gamma-flow"]);
    cleanup();

    seedSortableWorkflows();
    renderPage("/security/library?sort=usage");
    await screen.findByTestId("workflow-row-beta-flow");
    expect(workflowRowNames()).toEqual(["beta-flow", "alpha-flow", "gamma-flow"]);
    cleanup();

    seedSortableWorkflows();
    renderPage("/security/library?sort=recent");
    await screen.findByTestId("workflow-row-alpha-flow");
    expect(workflowRowNames()).toEqual(["alpha-flow", "gamma-flow", "beta-flow"]);
  });

  it("offers the create action when a tab has nothing in it", async () => {
    seedLists();
    listSecurityWorkflows.mockResolvedValue({ workflows: [] });
    renderPage();

    expect(await screen.findByText("No reusable workflows yet")).toBeTruthy();
    expect(screen.getByRole("button", { name: /Create your first workflow/ })).toBeTruthy();
    // The rich empty state replaces the table, not the toolbar.
    expect(screen.getByRole("button", { name: "Add from shipped catalog" })).toBeTruthy();
    expect(screen.getByTestId("export-pack")).toBeTruthy();
  });
  it("writes a filter picked in the filter bar into the URL", async () => {
    seedLists();
    renderPage();
    await screen.findByTestId("workflow-row-payments-workflow");

    fireEvent.click(screen.getByRole("combobox", { name: "Usage" }));
    const option = await screen.findByRole("option", { name: /Unused/ });
    fireEvent.pointerDown(option, { pointerType: "mouse", button: 0 });
    fireEvent.pointerUp(option, { pointerType: "mouse", button: 0 });
    fireEvent.click(option);

    await waitFor(() =>
      expect(screen.getByTestId("location-search").textContent).toBe("?usage=unused"),
    );
    expect(screen.queryByTestId("workflow-row-payments-workflow")).toBeNull();
  });
});
