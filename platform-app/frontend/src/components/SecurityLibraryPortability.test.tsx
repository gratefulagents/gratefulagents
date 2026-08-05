import { create } from "@bufbuild/protobuf";
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";

import { SecurityLibraryPage } from "@/components/SecurityLibraryPage";
import {
  SecurityDraftStatus,
  SecurityPackCollisionPolicy,
  SecurityWorkflowResourceSchema,
} from "@/rpc/platform/service_pb";

const {
  listSecurityWorkflows,
  listSecurityRankers,
  listSecurityPostScripts,
  listSecurityScanConfigs,
  generateSecurityDraft,
  getSecurityDraft,
  exportSecurityPack,
  importSecurityPack,
  validateSecurityWorkflow,
  createSecurityWorkflow,
  downloadBlob,
} = vi.hoisted(() => ({
  listSecurityWorkflows: vi.fn(),
  listSecurityRankers: vi.fn(),
  listSecurityPostScripts: vi.fn(),
  listSecurityScanConfigs: vi.fn(),
  generateSecurityDraft: vi.fn(),
  getSecurityDraft: vi.fn(),
  exportSecurityPack: vi.fn(),
  importSecurityPack: vi.fn(),
  validateSecurityWorkflow: vi.fn(),
  createSecurityWorkflow: vi.fn(),
  downloadBlob: vi.fn(),
}));

vi.mock("@/lib/client", () => ({
  client: {
    listSecurityWorkflows,
    listSecurityRankers,
    listSecurityPostScripts,
    listSecurityScanConfigs,
    generateSecurityDraft,
    getSecurityDraft,
    exportSecurityPack,
    importSecurityPack,
    validateSecurityWorkflow,
    createSecurityWorkflow,
    deleteSecurityWorkflow: vi.fn(),
    deleteSecurityRanker: vi.fn(),
    deleteSecurityPostScript: vi.fn(),
    updateSecurityWorkflow: vi.fn(),
    createSecurityRanker: vi.fn(),
    updateSecurityRanker: vi.fn(),
    createSecurityPostScript: vi.fn(),
    updateSecurityPostScript: vi.fn(),
  },
}));

vi.mock("@/lib/download", () => ({ downloadBlob }));

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
  vi.useRealTimers();
});

function seed() {
  listSecurityWorkflows.mockResolvedValue({ workflows: [] });
  listSecurityRankers.mockResolvedValue({ rankers: [] });
  listSecurityPostScripts.mockResolvedValue({ postScripts: [] });
  listSecurityScanConfigs.mockResolvedValue({ configs: [{ name: "nightly-scan" }] });
}

function renderPage() {
  return render(
    <MemoryRouter>
      <SecurityLibraryPage />
    </MemoryRouter>,
  );
}

describe("AI-assisted security authoring", () => {
  it("generates a workflow draft and opens it in the editor for review", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    seed();
    generateSecurityDraft.mockResolvedValue({ namespace: "user-alice", runName: "security-draft-abc123" });
    getSecurityDraft
      .mockResolvedValueOnce({
        status: SecurityDraftStatus.RUNNING,
        phase: "running",
        validationErrors: [],
      })
      .mockResolvedValueOnce({
        status: SecurityDraftStatus.COMPLETED,
        phase: "Succeeded",
        workflow: create(SecurityWorkflowResourceSchema, {
          name: "payments-deep-dive",
          description: "drafted",
          tasks: [{ name: "recon", objective: "map payment flows" }],
        }),
        validationErrors: [{ field: "tasks[0].role", message: "unknown role" }],
      });

    renderPage();
    fireEvent.click(await screen.findByTestId("generate-workflow-draft"));
    fireEvent.change(screen.getByLabelText(/What should it do/), {
      target: { value: "deep dive the payments service" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Generate draft" }));

    await waitFor(() => expect(generateSecurityDraft).toHaveBeenCalledTimes(1));
    expect(generateSecurityDraft.mock.calls[0][0].requestText).toBe("deep dive the payments service");

    // First poll reports progress, the second delivers the draft.
    await vi.advanceTimersByTimeAsync(3100);
    expect(await screen.findByTestId("draft-progress")).toBeTruthy();
    await vi.advanceTimersByTimeAsync(3100);

    // The draft opens in the normal editor with a review banner and the
    // server's validation errors, and nothing has been saved.
    expect(await screen.findByText(/AI draft — review before saving/)).toBeTruthy();
    expect((await screen.findByTestId("draft-validation-errors")).textContent).toContain("unknown role");
    expect((screen.getByLabelText(/^Name/) as HTMLInputElement).value).toBe("payments-deep-dive");
    expect(createSecurityWorkflow).not.toHaveBeenCalled();
  });

  it("surfaces a failed generation run", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    seed();
    generateSecurityDraft.mockResolvedValue({ namespace: "user-alice", runName: "security-draft-bad" });
    getSecurityDraft.mockResolvedValue({
      status: SecurityDraftStatus.FAILED,
      phase: "Failed",
      error: "the generation run did not produce a JSON draft",
      validationErrors: [],
    });

    renderPage();
    fireEvent.click(await screen.findByTestId("generate-workflow-draft"));
    fireEvent.change(screen.getByLabelText(/What should it do/), { target: { value: "something" } });
    fireEvent.click(screen.getByRole("button", { name: "Generate draft" }));
    await waitFor(() => expect(generateSecurityDraft).toHaveBeenCalled());
    await vi.advanceTimersByTimeAsync(3100);

    expect((await screen.findByTestId("draft-error")).textContent).toContain("did not produce a JSON draft");
  });
});

describe("security pack portability", () => {
  it("exports selected resources and downloads the pack", async () => {
    seed();
    listSecurityWorkflows.mockResolvedValue({
      workflows: [create(SecurityWorkflowResourceSchema, { name: "payments-workflow", tasks: [] })],
    });
    exportSecurityPack.mockResolvedValue({
      data: new Uint8Array([123, 125]),
      filename: "security-pack-user-alice-20260101.json",
      itemCount: 2,
    });

    renderPage();
    fireEvent.click(await screen.findByTestId("export-pack"));
    fireEvent.click(await screen.findByLabelText("Workflows: payments-workflow"));
    fireEvent.click(screen.getByLabelText("Scan configurations: nightly-scan"));
    fireEvent.click(screen.getByRole("button", { name: /^Export/ }));

    await waitFor(() => expect(exportSecurityPack).toHaveBeenCalledTimes(1));
    expect(exportSecurityPack.mock.calls[0][0]).toMatchObject({
      workflows: ["payments-workflow"],
      scanConfigs: ["nightly-scan"],
    });
    await waitFor(() => expect(downloadBlob).toHaveBeenCalledWith(
      "security-pack-user-alice-20260101.json",
      expect.anything(),
      "application/json",
    ));
  });

  it("dry-runs an import, blocks apply on failures, and applies a clean pack", async () => {
    seed();
    importSecurityPack
      .mockResolvedValueOnce({
        applied: false,
        items: [
          { kind: "SecurityWorkflow", name: "wf", action: "failed", finalName: "", error: "spec failed validation", validationErrors: [{ field: "tasks", message: "at least one task is required" }] },
        ],
      })
      .mockResolvedValueOnce({
        applied: false,
        items: [{ kind: "SecurityRanker", name: "rk", action: "would-create", finalName: "rk", error: "", validationErrors: [] }],
      })
      .mockResolvedValueOnce({
        applied: true,
        items: [{ kind: "SecurityRanker", name: "rk", action: "created", finalName: "rk", error: "", validationErrors: [] }],
      });

    renderPage();
    fireEvent.click(await screen.findByTestId("import-pack"));
    const input = screen.getByLabelText(/Pack file/) as HTMLInputElement;
    const file = new File(['{"schemaVersion":"security-pack/v1"}'], "pack.json", { type: "application/json" });
    fireEvent.change(input, { target: { files: [file] } });

    await waitFor(() =>
      expect((screen.getByRole("button", { name: "Dry run" }) as HTMLButtonElement).disabled).toBe(false),
    );
    fireEvent.click(screen.getByRole("button", { name: "Dry run" }));

    const results = await screen.findByTestId("import-results");
    expect(results.textContent).toContain("Dry run");
    expect(results.textContent).toContain("at least one task is required");
    // A failing item blocks apply.
    expect((screen.getByRole("button", { name: "Apply import" }) as HTMLButtonElement).disabled).toBe(true);

    fireEvent.click(screen.getByRole("button", { name: "Dry run" }));
    await waitFor(() =>
      expect((screen.getByRole("button", { name: "Apply import" }) as HTMLButtonElement).disabled).toBe(false),
    );
    fireEvent.click(screen.getByRole("button", { name: "Apply import" }));

    await waitFor(() => expect(importSecurityPack).toHaveBeenCalledTimes(3));
    expect(importSecurityPack.mock.calls[2][0]).toMatchObject({ apply: true });
    expect((await screen.findByTestId("import-results")).textContent).toContain("Import applied");
  });

  it("passes the selected collision policy", async () => {
    seed();
    importSecurityPack.mockResolvedValue({ applied: false, items: [] });
    renderPage();
    fireEvent.click(await screen.findByTestId("import-pack"));
    fireEvent.change(screen.getByLabelText(/If a name already exists/), {
      target: { value: String(SecurityPackCollisionPolicy.RENAME) },
    });
    const file = new File(["{}"], "pack.json", { type: "application/json" });
    fireEvent.change(screen.getByLabelText(/Pack file/), { target: { files: [file] } });
    await waitFor(() =>
      expect((screen.getByRole("button", { name: "Dry run" }) as HTMLButtonElement).disabled).toBe(false),
    );
    fireEvent.click(screen.getByRole("button", { name: "Dry run" }));
    await waitFor(() => expect(importSecurityPack).toHaveBeenCalled());
    expect(importSecurityPack.mock.calls[0][0].collisionPolicy).toBe(SecurityPackCollisionPolicy.RENAME);
  });
});
