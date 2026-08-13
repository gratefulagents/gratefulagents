import { create } from "@bufbuild/protobuf";
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";

import { SecurityScanConfigList } from "@/components/SecurityScanConfigList";
import {
  SecurityScanConfigSchema,
  SecurityScanConfigSpecSchema,
  type SecurityScanConfig,
} from "@/rpc/platform/service_pb";

const { listSecurityScanConfigs, listSecurityPrograms, listMyCredentials, createSecurityScan, runSecurityScanNow, deleteSecurityScan, updateSecurityScan } =
  vi.hoisted(() => ({
    listSecurityScanConfigs: vi.fn(),
    listSecurityPrograms: vi.fn().mockResolvedValue({ programs: [] }),
    listMyCredentials: vi.fn().mockResolvedValue({ namespace: "user-alice" }),
    createSecurityScan: vi.fn().mockResolvedValue({}),
    runSecurityScanNow: vi.fn(),
    deleteSecurityScan: vi.fn(),
    updateSecurityScan: vi.fn(),
  }));

vi.mock("@/lib/client", () => ({
  client: { listSecurityScanConfigs, listSecurityPrograms, listMyCredentials, createSecurityScan, runSecurityScanNow, deleteSecurityScan, updateSecurityScan },
}));

// The full form dialog is covered by SecurityScanFormDialog.test.tsx; here a
// stub records which config each dialog instance edits or duplicates.
vi.mock("@/components/SecurityScanFormDialog", () => ({
  SecurityScanFormDialog: ({
    config,
    duplicateFrom,
    trigger,
  }: {
    config?: SecurityScanConfig;
    duplicateFrom?: SecurityScanConfig;
    trigger: React.ReactElement;
  }) => (
    <div
      data-testid={
        duplicateFrom
          ? `duplicate-dialog-${duplicateFrom.name}`
          : config
            ? `edit-dialog-${config.name}`
            : "create-dialog"
      }
    >
      {trigger}
    </div>
  ),
  scanConfigUsesSavedCredentials: () => true,
}));

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

function configFixture(overrides: {
  name?: string;
  suspend?: boolean;
  securityProgramRef?: string;
  schedule?: string;
  conditionReady?: string;
  lastScanTimeUnix?: bigint;
  findingCounts?: Record<string, number>;
} = {}): SecurityScanConfig {
  return create(SecurityScanConfigSchema, {
    namespace: "user-alice",
    name: overrides.name ?? "nightly",
    spec: create(SecurityScanConfigSpecSchema, {
      repoUrl: "https://github.com/acme/payments.git",
      schedule: overrides.schedule ?? "@daily",
      suspend: overrides.suspend ?? false,
      securityProgramRef: overrides.securityProgramRef ?? "",
    }),
    phase: "Scheduled",
    conditionReady: overrides.conditionReady ?? "True",
    lastScanTimeUnix: overrides.lastScanTimeUnix ?? BigInt(Math.floor(Date.now() / 1000)),
    findingCounts: overrides.findingCounts ?? {},
  });
}

function renderList() {
  render(
    <MemoryRouter>
      <SecurityScanConfigList />
    </MemoryRouter>,
  );
}

describe("SecurityScanConfigList", () => {
  it("offers the Immunefi import as a secondary action without creating by default", async () => {
    listSecurityScanConfigs.mockResolvedValue({ configs: [] });
    renderList();

    const importButton = await screen.findByRole("button", { name: "Import Immunefi targets" });
    expect(importButton.className).toContain("border-border");
    expect(screen.getByRole("button", { name: "New scan" })).toBeTruthy();
    expect(createSecurityScan).not.toHaveBeenCalled();
  });

  it("passes discovered security program targets to the importer", async () => {
    listSecurityScanConfigs.mockResolvedValue({ configs: [] });
    listSecurityPrograms.mockResolvedValue({
      programs: [
        {
          name: "custom-program",
          scanTarget: {
            featured: true,
            priority: 3,
            displayName: "Custom metadata target",
            scanName: "custom-scan",
            repositoryUrl: "https://example.com/custom",
            baseBranch: "main",
            workflowRef: "custom-workflow",
            policyPackRef: "custom-policy",
          },
        },
      ],
    });
    renderList();

    fireEvent.click(await screen.findByRole("button", { name: "Import Immunefi targets" }));

    expect(screen.getByText("Custom metadata target")).toBeTruthy();
    expect(screen.getByText("https://example.com/custom · main")).toBeTruthy();
    expect(screen.getByText("custom-workflow")).toBeTruthy();
  });

  it("does not treat a same-named scan in another namespace as already imported", async () => {
    listSecurityScanConfigs.mockResolvedValue({
      configs: [create(SecurityScanConfigSchema, { namespace: "shared-team", name: "custom-scan" })],
    });
    listSecurityPrograms.mockResolvedValue({
      programs: [{
        name: "custom-program",
        scanTarget: {
          featured: true,
          priority: 1,
          displayName: "Custom target",
          scanName: "custom-scan",
          repositoryUrl: "https://example.com/custom",
          workflowRef: "custom-workflow",
          policyPackRef: "bug-bounty",
        },
      }],
    });
    renderList();

    fireEvent.click(await screen.findByRole("button", { name: "Import Immunefi targets" }));
    expect(screen.queryByText("Existing name — skipped")).toBeNull();
    expect(screen.getByRole("button", { name: "Import 1 missing target" })).toBeTruthy();
  });

  it("shows Run now only for non-suspended configurations", async () => {
    listSecurityScanConfigs.mockResolvedValue({
      configs: [configFixture(), configFixture({ name: "paused", suspend: true })],
    });
    renderList();

    await waitFor(() => {
      expect(screen.getByText("nightly")).toBeTruthy();
      expect(screen.getByText("paused")).toBeTruthy();
    });
    // One active config → exactly one Run now button.
    expect(screen.getAllByRole("button", { name: /Run now/ })).toHaveLength(1);
  });

  it("starts a run, disabling the button while the mutation is in flight", async () => {
    listSecurityScanConfigs.mockResolvedValue({ configs: [configFixture()] });
    let resolveRun: (value: unknown) => void = () => {};
    runSecurityScanNow.mockReturnValue(new Promise((resolve) => { resolveRun = resolve; }));
    renderList();

    const button = await screen.findByRole("button", { name: /Run now/ });
    fireEvent.click(button);

    const pending = screen.getByRole("button", { name: /Starting…/ }) as HTMLButtonElement;
    expect(pending.disabled).toBe(true);
    expect(runSecurityScanNow).toHaveBeenCalledWith({ namespace: "user-alice", name: "nightly" });

    resolveRun({});
    await waitFor(() => {
      expect((screen.getByRole("button", { name: /Run now/ }) as HTMLButtonElement).disabled).toBe(false);
    });
    // The list refreshes so the new run/phase shows up.
    expect(listSecurityScanConfigs).toHaveBeenCalledTimes(2);
  });

  it("surfaces run-now errors and re-enables the button", async () => {
    listSecurityScanConfigs.mockResolvedValue({ configs: [configFixture()] });
    runSecurityScanNow.mockRejectedValue(
      new Error("security scan user-alice/nightly is suspended; resume it before requesting a run"),
    );
    renderList();

    fireEvent.click(await screen.findByRole("button", { name: /Run now/ }));

    await waitFor(() => {
      expect(screen.getByRole("alert").textContent).toContain("resume it before requesting a run");
    });
    expect((screen.getByRole("button", { name: /Run now/ }) as HTMLButtonElement).disabled).toBe(false);
  });

  it("shows the linked security program provenance URL", async () => {
    listSecurityScanConfigs.mockResolvedValue({
      configs: [configFixture({ securityProgramRef: "acme-bounty" })],
    });
    listSecurityPrograms.mockResolvedValue({
      programs: [
        {
          name: "acme-bounty",
          programUrl: "https://hackerone.com/acme",
        },
      ],
    });
    renderList();

    const link = await screen.findByRole("link", { name: "https://hackerone.com/acme" });
    expect(link.getAttribute("href")).toBe("https://hackerone.com/acme");
  });

  it("offers a duplicate dialog pre-filled from each configuration", async () => {
    listSecurityScanConfigs.mockResolvedValue({ configs: [configFixture()] });
    renderList();

    await screen.findByText("nightly");
    expect(screen.getByTestId("duplicate-dialog-nightly")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Duplicate nightly" })).toBeTruthy();
  });

  it("filters configurations by last scan date", async () => {
    const now = Math.floor(Date.now() / 1000);
    listSecurityScanConfigs.mockResolvedValue({
      configs: [
        configFixture({ name: "recent", lastScanTimeUnix: BigInt(now - 60 * 60) }),
        configFixture({ name: "old", lastScanTimeUnix: BigInt(now - 40 * 24 * 60 * 60) }),
        configFixture({ name: "never", lastScanTimeUnix: 0n }),
      ],
    });
    renderList();

    await screen.findByText("recent");
    fireEvent.change(screen.getByRole("combobox", { name: "Last scan" }), { target: { value: "24h" } });

    expect(screen.getByText("recent")).toBeTruthy();
    expect(screen.queryByText("old")).toBeNull();
    expect(screen.queryByText("never")).toBeNull();
    expect(screen.getByText("Showing 1 of 3 configurations")).toBeTruthy();

    fireEvent.change(screen.getByRole("combobox", { name: "Last scan" }), { target: { value: "never" } });
    expect(screen.getByText("never")).toBeTruthy();
    expect(screen.queryByText("recent")).toBeNull();
  });

  it("combines status, findings, schedule, and program filters and clears them", async () => {
    listSecurityScanConfigs.mockResolvedValue({
      configs: [
        configFixture({
          name: "matching",
          securityProgramRef: "acme-bounty",
          findingCounts: { high: 2 },
        }),
        configFixture({ name: "paused", suspend: true, findingCounts: { high: 2 } }),
        configFixture({ name: "clean-once", schedule: "", findingCounts: {} }),
      ],
    });
    listSecurityPrograms.mockResolvedValue({
      programs: [{ name: "acme-bounty", programUrl: "https://hackerone.com/acme" }],
    });
    renderList();

    await screen.findByText("matching");
    fireEvent.change(screen.getByRole("combobox", { name: "Status" }), { target: { value: "ready" } });
    fireEvent.change(screen.getByRole("combobox", { name: "Findings" }), { target: { value: "high" } });
    fireEvent.change(screen.getByRole("combobox", { name: "Schedule" }), { target: { value: "recurring" } });
    fireEvent.change(screen.getByRole("combobox", { name: "Program" }), { target: { value: "acme-bounty" } });

    expect(screen.getByText("matching")).toBeTruthy();
    expect(screen.queryByText("paused")).toBeNull();
    expect(screen.queryByText("clean-once")).toBeNull();
    expect(screen.getByLabelText("4 active filters")).toBeTruthy();

    fireEvent.click(screen.getByRole("button", { name: "Clear filters" }));
    expect(screen.getByText("paused")).toBeTruthy();
    expect(screen.getByText("clean-once")).toBeTruthy();
    expect((screen.getByRole("combobox", { name: "Status" }) as HTMLSelectElement).value).toBe("all");
  });
});
