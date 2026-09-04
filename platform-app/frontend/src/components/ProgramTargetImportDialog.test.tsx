import { afterEach, describe, expect, it, vi } from "vitest";
import { create, type MessageInitShape } from "@bufbuild/protobuf";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";

import { ProgramTargetImportDialog } from "@/components/ProgramTargetImportDialog";
import { client } from "@/lib/client";
import {
  SecurityScanConfigSchema,
  SecurityScanConfigSpecSchema,
  type SecurityProgramResource,
  type SecurityScanConfig,
} from "@/rpc/platform/service_pb";

vi.mock("@/lib/client", () => ({
  client: {
    createSecurityScan: vi.fn().mockResolvedValue({ namespace: "ns", name: "scan" }),
    updateSecurityScan: vi.fn().mockResolvedValue({ namespace: "ns", name: "scan" }),
    getMyModelDefaults: vi
      .fn()
      .mockResolvedValue({ provider: "openai", authMode: "api-key", model: "gpt-5", reasoningLevel: "high", disabled: false }),
  },
}));

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
  vi.mocked(client.createSecurityScan).mockResolvedValue(
    {} as Awaited<ReturnType<typeof client.createSecurityScan>>,
  );
  vi.mocked(client.updateSecurityScan).mockResolvedValue(
    {} as Awaited<ReturnType<typeof client.updateSecurityScan>>,
  );
  vi.mocked(client.getMyModelDefaults).mockResolvedValue({
    provider: "openai",
    authMode: "api-key",
    model: "gpt-5",
    reasoningLevel: "high",
    disabled: false,
  } as Awaited<ReturnType<typeof client.getMyModelDefaults>>);
});

type TargetInput = Omit<
  SecurityProgramResource["scanTargets"][number],
  "$typeName" | "parameterValues" | "targetUrl"
> & { parameterValues?: Record<string, string>; targetUrl?: string };

function legacyProgram(
  name: string,
  scanTarget: TargetInput,
): SecurityProgramResource {
  return { name, scanTarget: scanTarget as SecurityProgramResource["scanTarget"] } as SecurityProgramResource;
}

function program(name: string, scanTargets: TargetInput[]): SecurityProgramResource {
  return { name, scanTargets } as SecurityProgramResource;
}

/** existingConfig builds a personal-namespace configuration; `spec` overrides the up-to-date defaults for `scan-first`. */
function existingConfig(
  name: string,
  spec: MessageInitShape<typeof SecurityScanConfigSpecSchema> = {},
): SecurityScanConfig {
  return create(SecurityScanConfigSchema, {
    namespace: "personal",
    name,
    useSavedCredentials: true,
    spec: {
      repoUrl: "https://example.com/first",
      targetUrl: "",
      baseBranch: "main",
      workflowRef: "workflow-first",
      policyPackRef: "policy-first",
      securityProgramRef: "program-multi",
      parameterValues: {},
      schedule: "0 9 * * 1",
      manualOnly: false,
      ...spec,
    },
  });
}

const programs = [
  program("program-multi", [
    {
      featured: true,
      priority: 20,
      displayName: "Later target",
      scanName: "scan-later",
      repositoryUrl: "https://example.com/later",
      baseBranch: "master",
      workflowRef: "workflow-later",
      policyPackRef: "policy-later",
    },
    {
      featured: true,
      priority: 10,
      displayName: "First target",
      scanName: "scan-first",
      repositoryUrl: "https://example.com/first",
      baseBranch: "main",
      workflowRef: "workflow-first",
      policyPackRef: "policy-first",
    },
  ]),
  legacyProgram("program-hidden", {
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
  onTargetSelected = vi.fn(),
  existingConfigs: readonly SecurityScanConfig[] = [],
  availablePrograms: readonly SecurityProgramResource[] = programs,
  onImported?: (summary: { created: number; updated: number; failed: number }) => void,
) {
  render(
    <ProgramTargetImportDialog
      programs={availablePrograms}
      existingConfigs={existingConfigs}
      trigger={<button>Import scan target</button>}
      onTargetSelected={onTargetSelected}
      onImported={onImported}
    />,
  );
  return onTargetSelected;
}

function openDialog() {
  fireEvent.click(screen.getByRole("button", { name: "Import scan target" }));
}

describe("ProgramTargetImportDialog", () => {
  it("previews all importable targets in metadata order without creating anything", () => {
    renderDialog();
    openDialog();

    expect(screen.getByText(/workspace-write access/)).toBeTruthy();
    expect(screen.getByText(/no scan is run/)).toBeTruthy();
    const items = within(screen.getByRole("list", { name: "Importable scan targets" })).getAllByRole("listitem");
    expect(items).toHaveLength(3);
    expect(items[0].textContent).toContain("Hidden target");
    expect(items[1].textContent).toContain("First target");
    expect(items[1].textContent).toContain("https://example.com/first · main");
    expect(items[2].textContent).toContain("Later target");
    expect(items.every((item) => item.textContent?.includes("New"))).toBe(true);
    expect(client.createSecurityScan).not.toHaveBeenCalled();
  });

  it("selects one target for configuration and closes the chooser", () => {
    const onTargetSelected = renderDialog();
    openDialog();
    fireEvent.click(screen.getByRole("button", { name: "Configure scan for First target" }));

    expect(onTargetSelected).toHaveBeenCalledTimes(1);
    expect(onTargetSelected).toHaveBeenCalledWith(expect.objectContaining({
      name: "scan-first",
      repoUrl: "https://example.com/first",
      baseBranch: "main",
      workflowRef: "workflow-first",
      policyPackRef: "policy-first",
      securityProgramRef: "program-multi",
    }));
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("does not allow selecting an up-to-date configuration", () => {
    const onTargetSelected = renderDialog(vi.fn(), [existingConfig("scan-first")]);
    openDialog();

    expect(screen.getByText("Up to date")).toBeTruthy();
    expect((screen.getByLabelText("Select First target") as HTMLInputElement).disabled).toBe(true);
    const button = screen.getByRole("button", {
      name: "Configure scan for First target",
    }) as HTMLButtonElement;
    expect(button.disabled).toBe(true);
    fireEvent.click(button);
    expect(onTargetSelected).not.toHaveBeenCalled();
  });

  it("communicates when no importable targets are available", () => {
    renderDialog(vi.fn(), [], []);
    openDialog();
    expect(screen.getByText("No importable scan targets are available.")).toBeTruthy();
  });

  it("select all skips targets that are already up to date", () => {
    renderDialog(vi.fn(), [existingConfig("scan-first")]);
    openDialog();

    expect((screen.getByLabelText("Select First target") as HTMLInputElement).disabled).toBe(true);
    fireEvent.click(screen.getByLabelText("Select all"));

    expect(screen.getByRole("toolbar", { name: "Selection" }).textContent).toContain(
      "2 selected",
    );
    expect((screen.getByLabelText("Select First target") as HTMLInputElement).checked).toBe(false);
    expect((screen.getByLabelText("Select Later target") as HTMLInputElement).checked).toBe(true);
    expect((screen.getByLabelText("Select Hidden target") as HTMLInputElement).checked).toBe(true);

    fireEvent.click(screen.getByRole("button", { name: "Clear selection" }));
    expect(screen.getByRole("toolbar", { name: "Selection" }).textContent).toContain("0 selected");
  });

  it("flags drifted configurations, quick-selects only them, and updates the target fields in place", async () => {
    const onImported = vi.fn();
    renderDialog(vi.fn(), [
      existingConfig("scan-first", { workflowRef: "workflow-old", baseBranch: "develop" }),
      existingConfig("scan-later", {
        repoUrl: "https://example.com/later",
        baseBranch: "master",
        workflowRef: "workflow-later",
        policyPackRef: "policy-later",
      }),
    ], programs, onImported);
    openDialog();

    expect(screen.getByRole("status").textContent).toContain("1 existing configuration no longer matches the program target");
    expect(screen.getByText("Changed: base branch, workflow")).toBeTruthy();
    expect(screen.getByText("Up to date")).toBeTruthy();
    expect((screen.getByRole("button", { name: "Configure scan for First target" }) as HTMLButtonElement).disabled).toBe(true);

    fireEvent.click(screen.getByRole("button", { name: "Updates available (1)" }));
    expect((screen.getByLabelText("Select First target") as HTMLInputElement).checked).toBe(true);
    expect((screen.getByLabelText("Select Hidden target") as HTMLInputElement).checked).toBe(false);
    fireEvent.click(screen.getByRole("button", { name: "Update 1 scan" }));

    await waitFor(() => expect(client.updateSecurityScan).toHaveBeenCalledTimes(1));
    expect(client.createSecurityScan).not.toHaveBeenCalled();
    expect(client.getMyModelDefaults).not.toHaveBeenCalled();
    const request = vi.mocked(client.updateSecurityScan).mock.calls[0][0] as {
      namespace: string;
      name: string;
      useSavedCredentials: boolean;
      spec?: Record<string, unknown>;
    };
    expect(request.namespace).toBe("personal");
    expect(request.name).toBe("scan-first");
    expect(request.useSavedCredentials).toBe(true);
    expect(request.spec).toMatchObject({
      workflowRef: "workflow-first",
      baseBranch: "main",
      policyPackRef: "policy-first",
      // Operator settings survive the refresh.
      schedule: "0 9 * * 1",
      manualOnly: false,
    });
    expect(await screen.findByText("Updated 1 scan")).toBeTruthy();
    expect(onImported).toHaveBeenCalledWith({ created: 0, updated: 1, failed: 0 });
  });

  it("filters targets by status and by search", () => {
    renderDialog(vi.fn(), [existingConfig("scan-first", { policyPackRef: "policy-old" })]);
    openDialog();

    fireEvent.change(screen.getByLabelText("Target status"), { target: { value: "new" } });
    expect(screen.queryByText("First target")).toBeNull();
    expect(screen.getByText("Later target")).toBeTruthy();
    expect(screen.getByLabelText("Select all visible")).toBeTruthy();

    fireEvent.change(screen.getByLabelText("Search scan targets"), { target: { value: "nothing-here" } });
    expect(screen.getByText("No scan targets match this search and status.")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Clear filters" }));
    expect(screen.getByText("First target")).toBeTruthy();
  });

  it("bulk imports every selected target with the import defaults", async () => {
    const onImported = vi.fn();
    renderDialog(vi.fn(), [], programs, onImported);
    openDialog();
    fireEvent.click(screen.getByLabelText("Select all"));
    fireEvent.click(screen.getByRole("button", { name: "Import 3 scans" }));

    await waitFor(() => expect(client.createSecurityScan).toHaveBeenCalledTimes(3));
    expect(client.getMyModelDefaults).toHaveBeenCalledTimes(1);

    const names = vi
      .mocked(client.createSecurityScan)
      .mock.calls.map((call) => (call[0] as { name: string }).name);
    expect(names).toEqual(["scan-hidden", "scan-first", "scan-later"]);

    const request = vi.mocked(client.createSecurityScan).mock.calls[1][0] as {
      name: string;
      useSavedCredentials: boolean;
      spec?: Record<string, unknown>;
    };
    expect(request.name).toBe("scan-first");
    expect(request.useSavedCredentials).toBe(true);
    expect(request.spec).toMatchObject({
      repoUrl: "https://example.com/first",
      baseBranch: "main",
      workflowRef: "workflow-first",
      policyPackRef: "policy-first",
      securityProgramRef: "program-multi",
      manualOnly: true,
      minSeverity: "",
      parallelism: 4,
    });
    expect(request.spec?.defaults).toMatchObject({ provider: "openai", model: "gpt-5" });

    await screen.findByText("Imported 3 scans");
    expect(onImported).toHaveBeenCalledTimes(1);
    expect(onImported).toHaveBeenCalledWith({ created: 3, updated: 0, failed: 0 });
  });

  it("reports a failed item and keeps the scans that were created", async () => {
    const onImported = vi.fn();
    vi.mocked(client.createSecurityScan).mockImplementation(((request: { name: string }) =>
      request.name === "scan-first"
        ? Promise.reject(new Error("quota exceeded"))
        : Promise.resolve({})) as typeof client.createSecurityScan);
    renderDialog(vi.fn(), [], programs, onImported);
    openDialog();
    fireEvent.click(screen.getByLabelText("Select all"));
    fireEvent.click(screen.getByRole("button", { name: "Import 3 scans" }));

    await waitFor(() => expect(onImported).toHaveBeenCalledWith({ created: 2, updated: 0, failed: 1 }));
    expect(client.createSecurityScan).toHaveBeenCalledTimes(3);
    expect(screen.getByRole("alert").textContent).toContain("scan-first: quota exceeded");
    expect(screen.getByText("Imported 2 scans")).toBeTruthy();
    // The failed target stays selected for a retry.
    expect((screen.getByLabelText("Select First target") as HTMLInputElement).checked).toBe(true);
  });

  it("falls back to empty model defaults when the lookup fails", async () => {
    vi.mocked(client.getMyModelDefaults).mockRejectedValue(new Error("nope"));
    renderDialog();
    openDialog();
    fireEvent.click(screen.getByLabelText("Select First target"));
    fireEvent.click(screen.getByRole("button", { name: "Import 1 scan" }));

    await waitFor(() => expect(client.createSecurityScan).toHaveBeenCalledTimes(1));
    const request = vi.mocked(client.createSecurityScan).mock.calls[0][0] as {
      spec?: { defaults?: Record<string, unknown> };
    };
    expect(request.spec?.defaults).toMatchObject({ provider: "", model: "" });
  });
});
