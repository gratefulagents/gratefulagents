import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";

import { ImmunefiTargetImportDialog } from "@/components/ImmunefiTargetImportDialog";
import { client } from "@/lib/client";
import { IMMUNEFI_TARGET_CATALOG } from "@/lib/immunefiTargetCatalog";

vi.mock("@/lib/client", () => ({
  client: {
    createSecurityScan: vi.fn().mockResolvedValue({}),
  },
}));

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

function renderDialog(existingNames = new Set<string>()) {
  render(
    <ImmunefiTargetImportDialog
      existingNames={existingNames}
      trigger={<button>Import Immunefi targets</button>}
    />,
  );
}

describe("ImmunefiTargetImportDialog", () => {
  it("previews all approved targets and creates nothing before confirmation", () => {
    renderDialog();

    expect(client.createSecurityScan).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: "Import Immunefi targets" }));

    expect(screen.getByText(/Nothing runs automatically/)).toBeTruthy();
    expect(screen.getAllByRole("listitem")).toHaveLength(20);
    expect(screen.getByText("Arbitrum token-bridge-contracts")).toBeTruthy();
    expect(client.createSecurityScan).not.toHaveBeenCalled();
  });

  it("creates all 20 manual-only targets with the curated fields and never runs them", async () => {
    renderDialog();
    fireEvent.click(screen.getByRole("button", { name: "Import Immunefi targets" }));
    fireEvent.click(screen.getByRole("button", { name: "Import 20 missing targets" }));

    await waitFor(() => expect(client.createSecurityScan).toHaveBeenCalledTimes(20));
    const requests = vi.mocked(client.createSecurityScan).mock.calls.map(([request]) => request);

    expect(requests.map((request) => request.name)).toEqual(
      IMMUNEFI_TARGET_CATALOG.map((target) => target.name),
    );
    for (const [index, request] of requests.entries()) {
      const target = IMMUNEFI_TARGET_CATALOG[index];
      expect(request.namespace).toBe("");
      expect(request.useSavedCredentials).toBe(true);
      expect(request.spec?.repoUrl).toBe(target.repoUrl);
      expect(request.spec?.workflowRef).toBe(target.workflowRef);
      expect(request.spec?.policyPackRef).toBe("bug-bounty");
      expect(request.spec?.securityProgramRef).toBe(target.securityProgramRef);
      expect(request.spec?.schedule).toBe("");
      expect(request.spec?.triggers).toBeUndefined();
      expect(request.spec?.minSeverity).toBe("high");
      expect(request.spec?.parallelism).toBe(4);
      expect(request.spec?.dedupe?.enabled).toBe(true);
      expect(request.spec).toMatchObject({ manualOnly: true });
    }
    expect(screen.getByRole("status").textContent).toContain("Created 20; skipped 0; failed 0.");
  });

  it("skips existing names without modifying or replacing them", async () => {
    const existingNames = new Set([
      IMMUNEFI_TARGET_CATALOG[0].name,
      IMMUNEFI_TARGET_CATALOG[7].name,
    ]);
    renderDialog(existingNames);
    fireEvent.click(screen.getByRole("button", { name: "Import Immunefi targets" }));

    expect(screen.getAllByText("Existing name — skipped")).toHaveLength(2);
    fireEvent.click(screen.getByRole("button", { name: "Import 18 missing targets" }));

    await waitFor(() => expect(client.createSecurityScan).toHaveBeenCalledTimes(18));
    const names = vi.mocked(client.createSecurityScan).mock.calls.map(([request]) => request.name);
    expect(names).not.toContain(IMMUNEFI_TARGET_CATALOG[0].name);
    expect(names).not.toContain(IMMUNEFI_TARGET_CATALOG[7].name);
    expect(screen.getByRole("status").textContent).toContain("Created 18; skipped 2; failed 0.");
  });

  it("continues after errors and reports aggregate partial results", async () => {
    vi.mocked(client.createSecurityScan)
      .mockRejectedValueOnce(new Error("permission denied"))
      .mockResolvedValue({} as Awaited<ReturnType<typeof client.createSecurityScan>>);
    renderDialog();
    fireEvent.click(screen.getByRole("button", { name: "Import Immunefi targets" }));
    fireEvent.click(screen.getByRole("button", { name: "Import 20 missing targets" }));

    await waitFor(() => expect(client.createSecurityScan).toHaveBeenCalledTimes(20));
    const status = screen.getByRole("status");
    expect(status.textContent).toContain("Created 19; skipped 0; failed 1.");
    expect(status.textContent).toContain("immunefi-layerzero: permission denied");
  });
});
