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

const { listSecurityScanConfigs, runSecurityScanNow, deleteSecurityScan, updateSecurityScan } =
  vi.hoisted(() => ({
    listSecurityScanConfigs: vi.fn(),
    runSecurityScanNow: vi.fn(),
    deleteSecurityScan: vi.fn(),
    updateSecurityScan: vi.fn(),
  }));

vi.mock("@/lib/client", () => ({
  client: { listSecurityScanConfigs, runSecurityScanNow, deleteSecurityScan, updateSecurityScan },
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

function configFixture(overrides: { name?: string; suspend?: boolean } = {}): SecurityScanConfig {
  return create(SecurityScanConfigSchema, {
    namespace: "user-alice",
    name: overrides.name ?? "nightly",
    spec: create(SecurityScanConfigSpecSchema, {
      repoUrl: "https://github.com/acme/payments.git",
      schedule: "@daily",
      suspend: overrides.suspend ?? false,
    }),
    phase: "Scheduled",
    conditionReady: "True",
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

  it("offers a duplicate dialog pre-filled from each configuration", async () => {
    listSecurityScanConfigs.mockResolvedValue({ configs: [configFixture()] });
    renderList();

    await screen.findByText("nightly");
    expect(screen.getByTestId("duplicate-dialog-nightly")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Duplicate nightly" })).toBeTruthy();
  });
});
