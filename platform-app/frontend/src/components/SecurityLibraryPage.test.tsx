import { create } from "@bufbuild/protobuf";
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { ConnectError, Code } from "@connectrpc/connect";

import { SecurityLibraryPage } from "@/components/SecurityLibraryPage";
import {
  SecurityPostScriptResourceSchema,
  SecurityRankerResourceSchema,
  SecurityWorkflowResourceSchema,
} from "@/rpc/platform/service_pb";

const {
  listSecurityWorkflows,
  listSecurityRankers,
  listSecurityPostScripts,
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
} = vi.hoisted(() => ({
  listSecurityWorkflows: vi.fn(),
  listSecurityRankers: vi.fn(),
  listSecurityPostScripts: vi.fn(),
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
}));

vi.mock("@/lib/client", () => ({
  client: {
    listSecurityWorkflows,
    listSecurityRankers,
    listSecurityPostScripts,
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
        tasks: [{ name: "a", objective: "t" }],
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
        prompt: "write a poc",
        runOn: "high-and-above",
        usageCount: 1,
        referencingScans: ["scan-a"],
      }),
    ],
  });
}

function renderPage() {
  return render(
    <MemoryRouter>
      <SecurityLibraryPage />
    </MemoryRouter>,
  );
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
    expect(row.textContent).toContain("high-and-above");
    expect(row.textContent).toContain("1 scan");
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
    fireEvent.click(screen.getByRole("button", { name: "Delete payments-workflow" }));
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

  it("duplicating a workflow prefills tasks but clears the name", async () => {
    seedLists();
    renderPage();
    await screen.findByTestId("workflow-row-payments-workflow");
    fireEvent.click(screen.getByRole("button", { name: "Duplicate payments-workflow" }));
    const nameInput = (await screen.findByPlaceholderText("payments-deep-dive")) as HTMLInputElement;
    expect(nameInput.value).toBe("");
    // The single task from the source workflow is prefilled.
    expect(screen.getByTestId("workflow-task-0")).toBeTruthy();
  });
});
