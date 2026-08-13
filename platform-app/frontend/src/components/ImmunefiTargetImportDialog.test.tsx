import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";

import { ImmunefiTargetImportDialog } from "@/components/ImmunefiTargetImportDialog";
import { client } from "@/lib/client";
import type { SecurityProgramResource } from "@/rpc/platform/service_pb";

vi.mock("@/lib/client", () => ({
  client: {
    createSecurityScan: vi.fn().mockResolvedValue({}),
  },
}));

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

function program(
  name: string,
  scanTarget: Omit<NonNullable<SecurityProgramResource["scanTarget"]>, "$typeName">,
): SecurityProgramResource {
  return { name, scanTarget: scanTarget as SecurityProgramResource["scanTarget"] } as SecurityProgramResource;
}

const programs = [
  program("program-later", {
    featured: true,
    priority: 20,
    displayName: "Later target",
    scanName: "scan-later",
    repositoryUrl: "https://example.com/later",
    baseBranch: "master",
    workflowRef: "workflow-later",
    policyPackRef: "policy-later",
  }),
  program("program-first", {
    featured: true,
    priority: 10,
    displayName: "First target",
    scanName: "scan-first",
    repositoryUrl: "https://example.com/first",
    baseBranch: "main",
    workflowRef: "workflow-first",
    policyPackRef: "policy-first",
  }),
  program("program-hidden", {
    featured: false,
    priority: 0,
    displayName: "Hidden target",
    scanName: "scan-hidden",
    repositoryUrl: "https://example.com/hidden",
    baseBranch: "develop",
    workflowRef: "workflow-hidden",
    policyPackRef: "policy-hidden",
  }),
];

function renderDialog(
  existingNames = new Set<string>(),
  availablePrograms: readonly SecurityProgramResource[] = programs,
) {
  render(
    <ImmunefiTargetImportDialog
      programs={availablePrograms}
      existingNames={existingNames}
      trigger={<button>Import Immunefi targets</button>}
    />,
  );
}

describe("ImmunefiTargetImportDialog", () => {
  it("previews only featured program targets in metadata order before confirmation", () => {
    renderDialog();

    expect(client.createSecurityScan).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: "Import Immunefi targets" }));

    expect(screen.getByText(/workspace-write access and unrestricted network egress/)).toBeTruthy();
    const items = screen.getAllByRole("listitem");
    expect(items).toHaveLength(2);
    expect(items[0].textContent).toContain("First target");
    expect(items[0].textContent).toContain("https://example.com/first · main");
    expect(items[1].textContent).toContain("Later target");
    expect(screen.queryByText("Hidden target")).toBeNull();
    expect(client.createSecurityScan).not.toHaveBeenCalled();
  });

  it("creates arbitrary program-driven targets as manual-only without running them", async () => {
    renderDialog();
    fireEvent.click(screen.getByRole("button", { name: "Import Immunefi targets" }));
    fireEvent.click(screen.getByRole("button", { name: "Import 2 missing targets" }));

    await waitFor(() => expect(client.createSecurityScan).toHaveBeenCalledTimes(2));
    const requests = vi.mocked(client.createSecurityScan).mock.calls.map(([request]) => request);

    expect(requests.map((request) => request.name)).toEqual(["scan-first", "scan-later"]);
    expect(requests[0]).toMatchObject({
      namespace: "",
      useSavedCredentials: true,
      spec: {
        repoUrl: "https://example.com/first",
        baseBranch: "main",
        workflowRef: "workflow-first",
        policyPackRef: "policy-first",
        securityProgramRef: "program-first",
        schedule: "",
        manualOnly: true,
        minSeverity: "high",
        parallelism: 4,
      },
    });
    expect(requests[0].spec?.triggers).toBeUndefined();
    expect(requests[0].spec?.dedupe?.enabled).toBe(true);
    expect(requests[0].policies).toMatchObject({
      configureRuntimeProfile: true,
      permissionMode: "workspace-write",
      egressMode: "unrestricted",
    });
    expect(screen.getByRole("status").textContent).toContain("Created 2; skipped 0; failed 0.");
  });

  it("skips existing names without modifying or replacing them", async () => {
    renderDialog(new Set(["scan-first"]));
    fireEvent.click(screen.getByRole("button", { name: "Import Immunefi targets" }));

    expect(screen.getAllByText("Existing name — skipped")).toHaveLength(1);
    fireEvent.click(screen.getByRole("button", { name: "Import 1 missing target" }));

    await waitFor(() => expect(client.createSecurityScan).toHaveBeenCalledTimes(1));
    expect(vi.mocked(client.createSecurityScan).mock.calls[0][0].name).toBe("scan-later");
    expect(screen.getByRole("status").textContent).toContain("Created 1; skipped 1; failed 0.");
  });

  it("continues after errors and reports aggregate partial results", async () => {
    vi.mocked(client.createSecurityScan)
      .mockRejectedValueOnce(new Error("permission denied"))
      .mockResolvedValue({} as Awaited<ReturnType<typeof client.createSecurityScan>>);
    renderDialog();
    fireEvent.click(screen.getByRole("button", { name: "Import Immunefi targets" }));
    fireEvent.click(screen.getByRole("button", { name: "Import 2 missing targets" }));

    await waitFor(() => expect(client.createSecurityScan).toHaveBeenCalledTimes(2));
    const status = screen.getByRole("status");
    expect(status.textContent).toContain("Created 1; skipped 0; failed 1.");
    expect(status.textContent).toContain("scan-first: permission denied");
  });

  it("communicates when no featured targets are available", () => {
    renderDialog(new Set(), []);
    fireEvent.click(screen.getByRole("button", { name: "Import Immunefi targets" }));

    expect(screen.getByText("No featured Immunefi targets are available.")).toBeTruthy();
    expect(
      (screen.getByRole("button", { name: "Import 0 missing targets" }) as HTMLButtonElement)
        .disabled,
    ).toBe(true);
  });
});
