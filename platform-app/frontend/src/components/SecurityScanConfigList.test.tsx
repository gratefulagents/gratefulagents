import { create } from "@bufbuild/protobuf";
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, useLocation } from "react-router-dom";

import {
  filterScanConfigs,
  scheduleKind,
  SecurityScanConfigList,
  sortScanConfigs,
  workflowFilterValue,
} from "@/components/SecurityScanConfigList";
import {
  SecurityScanConfigSchema,
  SecurityScanConfigSpecSchema,
  type SecurityScanConfig,
} from "@/rpc/platform/service_pb";

const { listSecurityScanConfigs, listSecurityPrograms, listMyCredentials, createSecurityScan, runSecurityScanNow, cancelSecurityScanRun, deleteSecurityScan, updateSecurityScan, toastSuccess, toastError, toastWarning } =
  vi.hoisted(() => ({
    listSecurityScanConfigs: vi.fn(),
    listSecurityPrograms: vi.fn().mockResolvedValue({ programs: [] }),
    listMyCredentials: vi.fn().mockResolvedValue({ namespace: "user-alice" }),
    createSecurityScan: vi.fn().mockResolvedValue({}),
    runSecurityScanNow: vi.fn(),
    cancelSecurityScanRun: vi.fn(),
    deleteSecurityScan: vi.fn(),
    updateSecurityScan: vi.fn(),
    toastSuccess: vi.fn(),
    toastError: vi.fn(),
    toastWarning: vi.fn(),
  }));

vi.mock("@/lib/client", () => ({
  client: { listSecurityScanConfigs, listSecurityPrograms, listMyCredentials, createSecurityScan, runSecurityScanNow, cancelSecurityScanRun, deleteSecurityScan, updateSecurityScan },
}));

vi.mock("@/components/ui/toaster", () => ({
  toast: { success: toastSuccess, error: toastError, warning: toastWarning },
}));

// The full form dialog is covered by SecurityScanFormDialog.test.tsx; here a
// stub records which config each dialog instance edits or duplicates.
vi.mock("@/components/SecurityScanFormDialog", () => ({
  SecurityScanFormDialog: ({
    config,
    duplicateFrom,
    initialConfig,
    trigger,
  }: {
    config?: SecurityScanConfig;
    duplicateFrom?: SecurityScanConfig;
    initialConfig?: SecurityScanConfig;
    trigger?: React.ReactElement;
  }) => (
    <div
      data-testid={
        duplicateFrom
          ? `duplicate-dialog-${duplicateFrom.name}`
          : config
            ? `edit-dialog-${config.name}`
            : initialConfig
              ? `seed-dialog-${initialConfig.name}`
              : "create-dialog"
      }
      data-repo-url={initialConfig?.spec?.repoUrl}
      data-target-url={initialConfig?.spec?.targetUrl}
      data-workflow-ref={initialConfig?.spec?.workflowRef}
      data-policy-pack-ref={initialConfig?.spec?.policyPackRef}
      data-program-ref={initialConfig?.spec?.securityProgramRef}
      data-base-branch={initialConfig?.spec?.baseBranch}
      data-parameter-values={JSON.stringify(initialConfig?.spec?.parameterValues ?? {})}
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
  namespace?: string;
  suspend?: boolean;
  securityProgramRef?: string;
  repoUrl?: string;
  targetUrl?: string;
  workflowRef?: string;
  inlineWorkflow?: boolean;
  policyPackRef?: string;
  schedule?: string;
  manualOnly?: boolean;
  conditionReady?: string;
  lastScanTimeUnix?: bigint;
  findingCounts?: Record<string, number>;
  phase?: string;
  lastExecutionPhase?: string;
} = {}): SecurityScanConfig {
  return create(SecurityScanConfigSchema, {
    namespace: overrides.namespace ?? "user-alice",
    name: overrides.name ?? "nightly",
    spec: create(SecurityScanConfigSpecSchema, {
      repoUrl: overrides.targetUrl
        ? ""
        : overrides.repoUrl ?? "https://github.com/acme/payments.git",
      targetUrl: overrides.targetUrl ?? "",
      workflowRef: overrides.workflowRef ?? "",
      workflow: overrides.inlineWorkflow ? [{ name: "recon", objective: "recon" }] : [],
      policyPackRef: overrides.policyPackRef ?? "",
      schedule: overrides.schedule ?? "@daily",
      manualOnly: overrides.manualOnly ?? false,
      suspend: overrides.suspend ?? false,
      securityProgramRef: overrides.securityProgramRef ?? "",
    }),
    phase: overrides.phase ?? "Scheduled",
    lastExecution: overrides.lastExecutionPhase
      ? { phase: overrides.lastExecutionPhase }
      : undefined,
    conditionReady: overrides.conditionReady ?? "True",
    lastScanTimeUnix: overrides.lastScanTimeUnix ?? BigInt(Math.floor(Date.now() / 1000)),
    findingCounts: overrides.findingCounts ?? {},
  });
}

function LocationProbe() {
  const location = useLocation();
  return <span data-testid="location">{location.search}</span>;
}

function renderList(initialEntry = "/security/configs") {
  render(
    <MemoryRouter initialEntries={[initialEntry]}>
      <SecurityScanConfigList />
      <LocationProbe />
    </MemoryRouter>,
  );
}

function search(): string {
  return screen.getByTestId("location").textContent ?? "";
}

/** Base UI selects commit on pointer events, not a bare click. */
async function chooseFilter(label: string, option: RegExp) {
  fireEvent.click(screen.getByRole("combobox", { name: label }));
  const item = await screen.findByRole("option", { name: option });
  fireEvent.pointerDown(item, { pointerType: "mouse", button: 0 });
  fireEvent.pointerUp(item, { pointerType: "mouse", button: 0 });
  fireEvent.click(item);
}

async function openRowMenu(name: string) {
  // Base UI menus open on pointer events; a bare click sometimes lands before
  // the trigger is wired up, which made this helper flake. Drive the full
  // pointer sequence and assert the menu really opened.
  const trigger = screen.getByRole("button", { name: `More actions for ${name}` });
  fireEvent.pointerDown(trigger, { pointerType: "mouse", button: 0 });
  fireEvent.pointerUp(trigger, { pointerType: "mouse", button: 0 });
  fireEvent.click(trigger);
  await screen.findByRole("menu");
}

function rowNames(): string[] {
  return screen
    .getAllByRole("link", { name: /^(nightly|weekly|paused|recent|old|never|matching|clean-once|alpha|beta|zeta)$/ })
    .map((link) => link.textContent ?? "");
}

describe("filterScanConfigs", () => {
  const now = Date.now();
  const configs = [
    configFixture({ name: "ready-critical", findingCounts: { critical: 1 } }),
    configFixture({ name: "paused", suspend: true, findingCounts: { high: 3 } }),
    configFixture({
      name: "broken",
      conditionReady: "False",
      repoUrl: "https://github.com/acme/ledger",
      schedule: "",
      lastScanTimeUnix: 0n,
    }),
  ];

  it("separates manual-only targets from one-time runs", () => {
    // A program-imported target has manualOnly=true and no schedule; bucketing
    // it under "One-time" made the filter contradict the "Manual only" label
    // the same row renders.
    const scheduled = configs;
    const manual = configFixture({ name: "imported", manualOnly: true, schedule: "" });
    const all = [...scheduled, manual];

    expect(filterScanConfigs(all, filters({ schedule: "manual" }), now).map((c) => c.name))
      .toEqual(["imported"]);
    expect(filterScanConfigs(all, filters({ schedule: "once" }), now).map((c) => c.name))
      .toEqual(["broken"]);
    expect(filterScanConfigs(all, filters({ schedule: "recurring" }), now).map((c) => c.name))
      .toEqual(["ready-critical", "paused"]);
    expect(scheduleKind(manual)).toBe("manual");
    expect(scheduleKind(scheduled[2])).toBe("once");
    expect(scheduleKind(scheduled[0])).toBe("recurring");
  });

  it("filters by status", () => {
    expect(filterScanConfigs(configs, filters({ status: "ready" }), now).map((c) => c.name))
      .toEqual(["ready-critical"]);
    expect(filterScanConfigs(configs, filters({ status: "suspended" }), now).map((c) => c.name))
      .toEqual(["paused"]);
    expect(filterScanConfigs(configs, filters({ status: "attention" }), now).map((c) => c.name))
      .toEqual(["broken"]);
  });

  it("filters by findings, schedule, repository and last scan", () => {
    expect(filterScanConfigs(configs, filters({ findings: "none" }), now).map((c) => c.name))
      .toEqual(["broken"]);
    expect(filterScanConfigs(configs, filters({ findings: "any" }), now).map((c) => c.name))
      .toEqual(["ready-critical", "paused"]);
    // Severity filters read as "or worse": critical satisfies "high".
    expect(filterScanConfigs(configs, filters({ findings: "high" }), now).map((c) => c.name))
      .toEqual(["ready-critical", "paused"]);
    expect(filterScanConfigs(configs, filters({ schedule: "once" }), now).map((c) => c.name))
      .toEqual(["broken"]);
    expect(filterScanConfigs(configs, filters({ repo: "acme/ledger" }), now).map((c) => c.name))
      .toEqual(["broken"]);
    expect(filterScanConfigs(configs, filters({ scanned: "never" }), now).map((c) => c.name))
      .toEqual(["broken"]);
    expect(filterScanConfigs(configs, filters({ scanned: "24h" }), now).map((c) => c.name))
      .toEqual(["ready-critical", "paused"]);
  });

  it("filters by workflow, target type and policy pack", () => {
    const items = [
      configFixture({ name: "ref-scan", workflowRef: "deep-audit", policyPackRef: "strict" }),
      configFixture({ name: "inline-scan", inlineWorkflow: true }),
      configFixture({ name: "web-scan", targetUrl: "https://example.com" }),
    ];
    const names = (overrides: Parameters<typeof filters>[0]) =>
      filterScanConfigs(items, filters(overrides), now).map((c) => c.name);

    // Named refs travel prefixed so a workflow literally called "inline" or
    // "default" cannot collide with the built-in buckets.
    expect(workflowFilterValue(items[0])).toBe("ref:deep-audit");
    expect(names({ workflow: "ref:deep-audit" })).toEqual(["ref-scan"]);
    expect(names({ workflow: "inline" })).toEqual(["inline-scan"]);
    expect(names({ workflow: "default" })).toEqual(["web-scan"]);

    expect(names({ target: "repository" })).toEqual(["ref-scan", "inline-scan"]);
    expect(names({ target: "web" })).toEqual(["web-scan"]);

    expect(names({ policy: "strict" })).toEqual(["ref-scan"]);
    expect(names({ policy: "none" })).toEqual(["inline-scan", "web-scan"]);
  });

  it("sorts by name, findings severity and last scan", () => {
    const nowUnix = BigInt(Math.floor(Date.now() / 1000));
    const items = [
      configFixture({ name: "zeta", lastScanTimeUnix: nowUnix, findingCounts: { low: 9 } }),
      configFixture({ name: "alpha", lastScanTimeUnix: nowUnix - 600n, findingCounts: { critical: 1 } }),
      configFixture({ name: "beta", lastScanTimeUnix: 0n, findingCounts: {} }),
    ];
    expect(sortScanConfigs(items, "name").map((c) => c.name)).toEqual(["alpha", "beta", "zeta"]);
    expect(sortScanConfigs(items, "findings").map((c) => c.name)).toEqual(["alpha", "zeta", "beta"]);
    expect(sortScanConfigs(items, "scanned").map((c) => c.name)).toEqual(["zeta", "alpha", "beta"]);
  });
});

function filters(overrides: Partial<Parameters<typeof filterScanConfigs>[1]> = {}) {
  return {
    status: "all",
    findings: "all",
    scanned: "all",
    schedule: "all",
    workflow: "all",
    program: "all",
    repo: "all",
    target: "all",
    policy: "all",
    ...overrides,
  };
}

describe("SecurityScanConfigList", () => {
  it("offers the program-target import as a secondary action without creating by default", async () => {
    listSecurityScanConfigs.mockResolvedValue({ configs: [] });
    renderList();

    const importButton = await screen.findByRole("button", { name: "Import scan targets" });
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

    fireEvent.click(await screen.findByRole("button", { name: "Import scan targets" }));

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
          baseBranch: "main",
          workflowRef: "custom-workflow",
          policyPackRef: "bug-bounty",
          parameterValues: {
            project_root: ".",
            deployment_manifest: "operator-verified deployment required",
          },
        },
      }],
    });
    renderList();

    fireEvent.click(await screen.findByRole("button", { name: "Import scan targets" }));
    expect(screen.queryByText("Existing configuration")).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Configure scan for Custom target" }));

    const seed = screen.getByTestId("seed-dialog-custom-scan");
    expect(seed.getAttribute("data-repo-url")).toBe("https://example.com/custom");
    expect(seed.getAttribute("data-base-branch")).toBe("main");
    expect(seed.getAttribute("data-workflow-ref")).toBe("custom-workflow");
    expect(seed.getAttribute("data-policy-pack-ref")).toBe("bug-bounty");
    expect(seed.getAttribute("data-program-ref")).toBe("custom-program");
    expect(JSON.parse(seed.getAttribute("data-parameter-values") ?? "{}")).toEqual({
      project_root: ".",
      deployment_manifest: "operator-verified deployment required",
    });
    expect(createSecurityScan).not.toHaveBeenCalled();
  });

  it("seeds each website program URL as its own repoless scan", async () => {
    listSecurityScanConfigs.mockResolvedValue({ configs: [] });
    listSecurityPrograms.mockResolvedValue({
      programs: [{
        name: "web-bounty",
        scanTargets: [
          {
            priority: 1, displayName: "Web app", scanName: "web-app",
            targetUrl: "https://app.example.com", workflowRef: "web-app-full-assessment",
            policyPackRef: "web-application",
          },
          {
            priority: 2, displayName: "API", scanName: "web-api",
            targetUrl: "https://api.example.com", workflowRef: "web-api-assessment",
            policyPackRef: "web-application",
          },
        ],
      }],
    });
    renderList();

    fireEvent.click(await screen.findByRole("button", { name: "Import scan targets" }));
    expect(screen.getByText("Web app")).toBeTruthy();
    expect(screen.getByText("API")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Configure scan for API" }));

    const seed = screen.getByTestId("seed-dialog-web-api");
    expect(seed.getAttribute("data-target-url")).toBe("https://api.example.com");
    expect(seed.getAttribute("data-repo-url")).toBe("");
    expect(seed.getAttribute("data-base-branch")).toBe("");
  });

  it("shows Run now for active configurations and Resume for suspended ones", async () => {
    listSecurityScanConfigs.mockResolvedValue({
      configs: [configFixture(), configFixture({ name: "paused", suspend: true })],
    });
    renderList();

    await waitFor(() => {
      expect(screen.getByText("nightly")).toBeTruthy();
      expect(screen.getByText("paused")).toBeTruthy();
    });
    expect(screen.getAllByRole("button", { name: /Run now/ })).toHaveLength(1);
    expect(screen.getByRole("button", { name: "Resume" })).toBeTruthy();
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

  it("links out to the runs list scoped to the configuration", async () => {
    listSecurityScanConfigs.mockResolvedValue({ configs: [configFixture({ name: "nightly scan" })] });
    renderList();

    await screen.findByText("nightly scan");
    // The per-row "View runs" text link was noise on every row; it now lives in
    // the row's overflow menu, still reachable by keyboard and named per row.
    await openRowMenu("nightly scan");
    const link = await screen.findByRole("menuitem", { name: "View runs for nightly scan" });
    expect(link.getAttribute("href")).toBe("/security/runs?q=nightly%20scan");
  });

  it("gives the placeholder cells a spoken meaning", async () => {
    listSecurityScanConfigs.mockResolvedValue({
      configs: [configFixture({ name: "never", lastScanTimeUnix: 0n })],
    });
    renderList();

    await screen.findByText("never");
    expect(screen.getAllByTitle("Never scanned").length).toBeGreaterThan(0);
    expect(screen.getByText("No security program linked")).toBeTruthy();
    // Once in the findings cell, once in the mobile-only row summary.
    expect(screen.getAllByText("No findings reported")).toHaveLength(2);
  });

  it("folds the hidden columns into a mobile-only summary in the primary cell", async () => {
    listSecurityScanConfigs.mockResolvedValue({
      configs: [configFixture({ name: "paused", suspend: true, findingCounts: { high: 2 } })],
    });
    renderList();

    await screen.findByText("paused");
    // At 390px the status and findings columns are hidden, so they read here
    // instead of being clipped at the card edge.
    const summary = screen.getByTestId("config-summary");
    expect(summary.className).toContain("sm:hidden");
    expect(within(summary).getByText("Suspended")).toBeTruthy();
    expect(within(summary).getByText("high")).toBeTruthy();
    expect(summary.textContent).toContain("Scanned");
    const cells = within(screen.getAllByRole("row")[1]).getAllByRole("cell");
    // Cell 0 is the selection checkbox, cell 1 the primary name cell.
    expect(cells[1].contains(summary)).toBe(true);
    // Status, findings and last scan fold away below `sm`; actions stay.
    for (const cell of cells.slice(3, 6)) {
      expect(cell.className).toContain("hidden");
      expect(cell.className).toContain("sm:table-cell");
    }
  });

  it("keeps secondary actions in an overflow menu: duplicate, suspend and delete", async () => {
    listSecurityScanConfigs.mockResolvedValue({ configs: [configFixture()] });
    updateSecurityScan.mockResolvedValue({});
    deleteSecurityScan.mockResolvedValue({});
    renderList();

    await screen.findByText("nightly");
    // Edit stays inline as the second primary action.
    expect(screen.getByRole("button", { name: "Edit nightly" })).toBeTruthy();
    expect(screen.getByTestId("edit-dialog-nightly")).toBeTruthy();
    expect(screen.queryByTestId("duplicate-dialog-nightly")).toBeNull();

    await openRowMenu("nightly");
    fireEvent.click(await screen.findByRole("menuitem", { name: /Duplicate/ }));
    await waitFor(() => expect(screen.getByTestId("duplicate-dialog-nightly")).toBeTruthy());

    await openRowMenu("nightly");
    fireEvent.click(await screen.findByRole("menuitem", { name: /Suspend/ }));
    await waitFor(() => expect(updateSecurityScan).toHaveBeenCalledTimes(1));
    expect(updateSecurityScan.mock.calls[0][0].spec.suspend).toBe(true);

    await openRowMenu("nightly");
    fireEvent.click(await screen.findByRole("menuitem", { name: /Delete/ }));
    const dialog = await screen.findByRole("dialog");
    // Deleting a scan purges its recorded runs and findings, so the dialog
    // must not promise the findings survive.
    expect(dialog.textContent).toContain("also removes its recorded scan runs and findings");
    fireEvent.click(await screen.findByRole("button", { name: "Delete" }));
    await waitFor(() =>
      expect(deleteSecurityScan).toHaveBeenCalledWith({ namespace: "user-alice", name: "nightly" }),
    );
  });

  it("resumes a suspended configuration from the inline action", async () => {
    listSecurityScanConfigs.mockResolvedValue({
      configs: [configFixture({ name: "paused", suspend: true })],
    });
    updateSecurityScan.mockResolvedValue({});
    renderList();

    fireEvent.click(await screen.findByRole("button", { name: "Resume" }));
    await waitFor(() => expect(updateSecurityScan).toHaveBeenCalledTimes(1));
    expect(updateSecurityScan.mock.calls[0][0].spec.suspend).toBe(false);
  });

  it("reads every filter from the URL", async () => {
    listSecurityScanConfigs.mockResolvedValue({
      configs: [
        configFixture({ name: "matching", securityProgramRef: "acme-bounty", findingCounts: { high: 2 } }),
        configFixture({ name: "paused", suspend: true, findingCounts: { high: 2 } }),
        configFixture({
          name: "clean-once",
          schedule: "",
          repoUrl: "https://github.com/acme/ledger",
          lastScanTimeUnix: 0n,
        }),
      ],
    });
    listSecurityPrograms.mockResolvedValue({
      programs: [{ name: "acme-bounty", programUrl: "https://hackerone.com/acme" }],
    });
    renderList(
      "/security/configs?status=ready&findings=high&scanned=24h&schedule=recurring&program=acme-bounty&repo=acme%2Fpayments",
    );

    await screen.findByText("matching");
    expect(screen.queryByText("paused")).toBeNull();
    expect(screen.queryByText("clean-once")).toBeNull();
    expect(screen.getByText("Showing 1 of 3 configurations")).toBeTruthy();
    expect(screen.getByLabelText("6 active filters")).toBeTruthy();
    // The controls mirror the URL rather than resetting to their defaults.
    expect(screen.getByRole("combobox", { name: "Status" }).textContent).toContain("Ready");
    expect(screen.getByRole("combobox", { name: "Repository" }).textContent).toContain("acme/payments");
  });

  it("writes each filter change back to the query string and clears it again", async () => {
    listSecurityScanConfigs.mockResolvedValue({
      configs: [
        configFixture({ name: "matching", securityProgramRef: "acme-bounty", findingCounts: { high: 2 } }),
        configFixture({ name: "clean-once", schedule: "", repoUrl: "https://github.com/acme/ledger" }),
      ],
    });
    listSecurityPrograms.mockResolvedValue({
      programs: [{ name: "acme-bounty", programUrl: "https://hackerone.com/acme" }],
    });
    renderList();

    await screen.findByText("matching");
    await chooseFilter("Status", /Ready/);
    await waitFor(() => expect(search()).toBe("?status=ready"));
    await chooseFilter("Findings", /Has findings/);
    await chooseFilter("Last scan", /Last 24 hours/);
    await chooseFilter("Schedule", /Recurring/);
    await chooseFilter("Program", /acme-bounty/);
    await chooseFilter("Repository", /acme\/payments/);

    await waitFor(() => {
      const params = new URLSearchParams(search());
      expect(Object.fromEntries(params)).toEqual({
        status: "ready",
        findings: "any",
        scanned: "24h",
        schedule: "recurring",
        program: "acme-bounty",
        repo: "acme/payments",
      });
    });
    expect(screen.getByText("Showing 1 of 2 configurations")).toBeTruthy();

    fireEvent.click(screen.getByRole("button", { name: /Clear/ }));
    await waitFor(() => expect(search()).toBe(""));
    expect(screen.getByText("clean-once")).toBeTruthy();
  });

  it("round-trips the search box through the q parameter", async () => {
    listSecurityScanConfigs.mockResolvedValue({
      configs: [
        configFixture({ name: "matching", repoUrl: "https://github.com/acme/payments" }),
        configFixture({ name: "clean-once", repoUrl: "https://github.com/acme/ledger" }),
      ],
    });
    renderList("/security/configs?q=ledger");

    await screen.findByText("clean-once");
    expect(screen.queryByText("matching")).toBeNull();

    fireEvent.change(screen.getByRole("searchbox"), { target: { value: "matching" } });
    await waitFor(() => expect(search()).toBe("?q=matching"));
    await waitFor(() => expect(screen.queryByText("clean-once")).toBeNull());
  });

  it("keeps unrelated query parameters when a filter changes", async () => {
    listSecurityScanConfigs.mockResolvedValue({ configs: [configFixture()] });
    renderList("/security/configs?tab=inventory");

    await screen.findByText("nightly");
    await chooseFilter("Status", /Ready/);
    await waitFor(() => {
      expect(new URLSearchParams(search()).get("tab")).toBe("inventory");
      expect(new URLSearchParams(search()).get("status")).toBe("ready");
    });
  });

  it("sorts from the URL and from the column headers", async () => {
    const nowUnix = BigInt(Math.floor(Date.now() / 1000));
    listSecurityScanConfigs.mockResolvedValue({
      configs: [
        configFixture({ name: "zeta", lastScanTimeUnix: nowUnix, findingCounts: { low: 4 } }),
        configFixture({ name: "alpha", lastScanTimeUnix: nowUnix - 3600n, findingCounts: { critical: 1 } }),
        configFixture({ name: "beta", lastScanTimeUnix: 0n }),
      ],
    });
    renderList();

    await screen.findByText("zeta");
    // Default sort: most recently scanned first, never-scanned last.
    expect(rowNames()).toEqual(["zeta", "alpha", "beta"]);
    expect(screen.getByRole("columnheader", { name: /Last Scan/ }).getAttribute("aria-sort"))
      .toBe("descending");
    expect(screen.getByRole("columnheader", { name: /Name/ }).getAttribute("aria-sort")).toBe("none");

    fireEvent.click(screen.getByRole("button", { name: /Name/ }));
    await waitFor(() => expect(search()).toBe("?sort=name"));
    expect(rowNames()).toEqual(["alpha", "beta", "zeta"]);
    expect(screen.getByRole("columnheader", { name: /Name/ }).getAttribute("aria-sort"))
      .toBe("ascending");

    fireEvent.click(screen.getByRole("button", { name: /Findings/ }));
    await waitFor(() => expect(search()).toBe("?sort=findings"));
    expect(rowNames()).toEqual(["alpha", "zeta", "beta"]);

    cleanup();
    renderList("/security/configs?sort=name");
    await screen.findByText("alpha");
    expect(rowNames()).toEqual(["alpha", "beta", "zeta"]);
  });

  it("distinguishes an empty account from an over-filtered list", async () => {
    listSecurityScanConfigs.mockResolvedValue({ configs: [] });
    renderList();

    await screen.findByText("No scan configurations yet");
    expect(screen.getByRole("button", { name: /Create your first scan/ })).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Clear filters" })).toBeNull();
    // Nothing to search and nothing to narrow: no search box, no filter bar.
    expect(screen.queryByRole("searchbox")).toBeNull();
    expect(screen.queryByRole("group", { name: "Configuration filters" })).toBeNull();

    cleanup();
    listSecurityScanConfigs.mockResolvedValue({ configs: [configFixture()] });
    renderList("/security/configs?status=suspended");

    await screen.findByText("No configurations match these filters");
    expect(screen.getByRole("searchbox")).toBeTruthy();
    expect(screen.getByText("Showing 0 of 1 configurations")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Clear filters" }));
    await waitFor(() => expect(search()).toBe(""));
    expect(screen.getByText("nightly")).toBeTruthy();
  });

  it("runs every selected configuration once from the bulk toolbar", async () => {
    listSecurityScanConfigs.mockResolvedValue({
      configs: [configFixture({ name: "nightly" }), configFixture({ name: "weekly" })],
    });
    runSecurityScanNow.mockResolvedValue({});
    renderList();

    await screen.findByText("weekly");
    // Nothing selected: no toolbar over the list.
    expect(screen.queryByRole("toolbar", { name: "Bulk actions" })).toBeNull();

    fireEvent.click(screen.getByLabelText("Select all configurations"));
    const toolbar = screen.getByRole("toolbar", { name: "Bulk actions" });
    expect(within(toolbar).getByText("2 selected")).toBeTruthy();

    fireEvent.click(within(toolbar).getByRole("button", { name: /Run now/ }));

    await waitFor(() => expect(runSecurityScanNow).toHaveBeenCalledTimes(2));
    expect(runSecurityScanNow.mock.calls.map(([request]) => request)).toEqual([
      { namespace: "user-alice", name: "nightly" },
      { namespace: "user-alice", name: "weekly" },
    ]);
    expect(toastSuccess).toHaveBeenCalledWith("Run now applied to 2 configurations");
    // Everything succeeded, so the selection — and the toolbar — clears.
    await waitFor(() =>
      expect(screen.queryByRole("toolbar", { name: "Bulk actions" })).toBeNull(),
    );
  });

  it("stops only the selected configurations that have a run in flight", async () => {
    listSecurityScanConfigs.mockResolvedValue({
      configs: [
        configFixture({ name: "nightly", phase: "Running" }),
        configFixture({ name: "weekly", lastExecutionPhase: "Running" }),
        configFixture({ name: "recent" }),
      ],
    });
    cancelSecurityScanRun.mockResolvedValue({});
    renderList();

    await screen.findByText("recent");
    fireEvent.click(screen.getByLabelText("Select all configurations"));
    const toolbar = screen.getByRole("toolbar", { name: "Bulk actions" });
    // Only two of the three selected configurations are running; the button
    // says so and the hint explains the mismatch.
    expect(
      within(toolbar).getByText("Mixed actions apply only to eligible configurations."),
    ).toBeTruthy();
    fireEvent.click(within(toolbar).getByRole("button", { name: "Stop (2)" }));

    await waitFor(() => expect(cancelSecurityScanRun).toHaveBeenCalledTimes(2));
    expect(cancelSecurityScanRun.mock.calls.map(([request]) => request.name)).toEqual([
      "nightly",
      "weekly",
    ]);
    expect(toastSuccess).toHaveBeenCalledWith("Stop applied to 2 configurations");
    // The idle row was never sent — and never fails — so no error surfaces.
    expect(screen.queryByRole("alert")).toBeNull();
    // The row the action could not apply to stays selected.
    expect(
      within(screen.getByRole("toolbar", { name: "Bulk actions" })).getByText("1 selected"),
    ).toBeTruthy();
    expect((screen.getByLabelText("Select recent") as HTMLInputElement).checked).toBe(true);
    expect((screen.getByLabelText("Select nightly") as HTMLInputElement).checked).toBe(false);
  });

  it("disables Stop when no selected configuration is running", async () => {
    listSecurityScanConfigs.mockResolvedValue({
      configs: [configFixture({ name: "nightly" }), configFixture({ name: "weekly" })],
    });
    renderList();

    await screen.findByText("weekly");
    fireEvent.click(screen.getByLabelText("Select all configurations"));
    const toolbar = screen.getByRole("toolbar", { name: "Bulk actions" });
    const stop = within(toolbar).getByRole("button", { name: "Stop (0)" }) as HTMLButtonElement;
    expect(stop.disabled).toBe(true);

    fireEvent.click(stop);
    expect(cancelSecurityScanRun).not.toHaveBeenCalled();
  });

  it("sends Run now only to the selected configurations that are not suspended", async () => {
    listSecurityScanConfigs.mockResolvedValue({
      configs: [
        configFixture({ name: "nightly" }),
        configFixture({ name: "paused", suspend: true }),
      ],
    });
    runSecurityScanNow.mockResolvedValue({});
    renderList();

    await screen.findByText("paused");
    fireEvent.click(screen.getByLabelText("Select all configurations"));
    const toolbar = screen.getByRole("toolbar", { name: "Bulk actions" });
    expect(
      within(toolbar).getByText("Mixed actions apply only to eligible configurations."),
    ).toBeTruthy();
    fireEvent.click(within(toolbar).getByRole("button", { name: "Run now (1)" }));

    await waitFor(() => expect(runSecurityScanNow).toHaveBeenCalledTimes(1));
    expect(runSecurityScanNow).toHaveBeenCalledWith({ namespace: "user-alice", name: "nightly" });
    expect(toastSuccess).toHaveBeenCalledWith("Run now applied to 1 configuration");
    // The suspended row was never sent, so no per-row failure surfaces; it
    // stays selected because the action never applied to it.
    expect(screen.queryByRole("alert")).toBeNull();
    await waitFor(() =>
      expect(
        within(screen.getByRole("toolbar", { name: "Bulk actions" })).getByText("1 selected"),
      ).toBeTruthy(),
    );
    expect((screen.getByLabelText("Select paused") as HTMLInputElement).checked).toBe(true);
  });

  it("lists the configuration a bulk action failed on while the others succeed", async () => {
    listSecurityScanConfigs.mockResolvedValue({
      configs: [configFixture({ name: "nightly" }), configFixture({ name: "weekly" })],
    });
    runSecurityScanNow.mockImplementation(({ name }: { name: string }) =>
      name === "weekly"
        ? Promise.reject(new Error("scan is already running"))
        : Promise.resolve({}),
    );
    renderList();

    await screen.findByText("weekly");
    fireEvent.click(screen.getByLabelText("Select nightly"));
    fireEvent.click(screen.getByLabelText("Select weekly"));
    fireEvent.click(
      within(screen.getByRole("toolbar", { name: "Bulk actions" })).getByRole("button", { name: /Run now/ }),
    );

    await waitFor(() => expect(runSecurityScanNow).toHaveBeenCalledTimes(2));
    expect(toastWarning).toHaveBeenCalledWith(
      "Run now applied to 1 configuration · 1 failed",
      expect.anything(),
    );
    const failure = screen.getByRole("alert");
    expect(failure.textContent).toContain("weekly: scan is already running");
    expect(failure.textContent).not.toContain("nightly");
    expect((screen.getByLabelText("Select weekly") as HTMLInputElement).checked).toBe(true);
    expect((screen.getByLabelText("Select nightly") as HTMLInputElement).checked).toBe(false);
  });

  it("suspends and deletes the selection, naming the count before deleting", async () => {
    listSecurityScanConfigs.mockResolvedValue({
      configs: [configFixture({ name: "nightly" }), configFixture({ name: "paused", suspend: true })],
    });
    updateSecurityScan.mockResolvedValue({});
    deleteSecurityScan.mockResolvedValue({});
    renderList();

    await screen.findByText("paused");
    fireEvent.click(screen.getByLabelText("Select all configurations"));
    fireEvent.click(
      within(screen.getByRole("toolbar", { name: "Bulk actions" })).getByRole("button", { name: /Suspend/ }),
    );

    // "paused" is already suspended, so only the active configuration is sent.
    await waitFor(() => expect(updateSecurityScan).toHaveBeenCalledTimes(1));
    expect(updateSecurityScan.mock.calls[0][0].name).toBe("nightly");
    expect(updateSecurityScan.mock.calls[0][0].spec.suspend).toBe(true);

    fireEvent.click(screen.getByLabelText("Select all configurations"));
    fireEvent.click(
      within(screen.getByRole("toolbar", { name: "Bulk actions" })).getByRole("button", { name: /Delete/ }),
    );
    const dialog = await screen.findByRole("dialog");
    expect(dialog.textContent).toContain("Delete 2 configurations?");
    expect(dialog.textContent).toContain("also removes their recorded scan runs and findings");
    fireEvent.click(within(dialog).getByRole("button", { name: "Delete" }));

    await waitFor(() => expect(deleteSecurityScan).toHaveBeenCalledTimes(2));
    expect(deleteSecurityScan.mock.calls.map(([request]) => request.name)).toEqual([
      "nightly",
      "paused",
    ]);
  });

  it("applies a bulk action to the whole selection, including rows the filters hide", async () => {
    listSecurityScanConfigs.mockResolvedValue({
      configs: [
        configFixture({ name: "matching", repoUrl: "https://github.com/acme/payments" }),
        configFixture({ name: "clean-once", repoUrl: "https://github.com/acme/ledger" }),
      ],
    });
    runSecurityScanNow.mockResolvedValue({});
    renderList();

    await screen.findByText("clean-once");
    fireEvent.click(screen.getByLabelText("Select all configurations"));

    // Narrowing the list hides one selected row; the selection is unchanged,
    // so the count — and the action — still cover both configurations.
    fireEvent.change(screen.getByRole("searchbox"), { target: { value: "ledger" } });
    await waitFor(() => expect(screen.queryByText("matching")).toBeNull());
    const toolbar = screen.getByRole("toolbar", { name: "Bulk actions" });
    expect(within(toolbar).getByText("2 selected")).toBeTruthy();
    // The hidden row is still counted, and the toolbar says where it went.
    expect(within(toolbar).getByText("· 1 hidden by filters")).toBeTruthy();

    fireEvent.click(within(toolbar).getByRole("button", { name: /Run now/ }));

    await waitFor(() => expect(runSecurityScanNow).toHaveBeenCalledTimes(2));
    expect(runSecurityScanNow.mock.calls.map(([request]) => request.name)).toEqual([
      "matching",
      "clean-once",
    ]);
    expect(toastSuccess).toHaveBeenCalledWith("Run now applied to 2 configurations");
  });

  it("stops a suspended configuration whose run is still in flight", async () => {
    listSecurityScanConfigs.mockResolvedValue({
      configs: [configFixture({ name: "paused", suspend: true, phase: "Running" })],
    });
    cancelSecurityScanRun.mockResolvedValue({});
    renderList();

    await screen.findByText("paused");
    fireEvent.click(screen.getByLabelText("Select paused"));
    fireEvent.click(
      within(screen.getByRole("toolbar", { name: "Bulk actions" })).getByRole("button", { name: /Stop/ }),
    );

    // Suspension stops future runs; it does not cancel the one already going.
    await waitFor(() => expect(cancelSecurityScanRun).toHaveBeenCalledTimes(1));
    expect(cancelSecurityScanRun).toHaveBeenCalledWith({ namespace: "user-alice", name: "paused" });
    expect(toastSuccess).toHaveBeenCalledWith("Stop applied to 1 configuration");
    expect(screen.queryByRole("alert")).toBeNull();
  });

  it("locks the row actions while a bulk action runs", async () => {
    listSecurityScanConfigs.mockResolvedValue({
      configs: [configFixture({ name: "nightly" }), configFixture({ name: "weekly" })],
    });
    let resolveRun: (value: unknown) => void = () => {};
    runSecurityScanNow.mockReturnValue(new Promise((resolve) => { resolveRun = resolve; }));
    renderList();

    await screen.findByText("weekly");
    fireEvent.click(screen.getByLabelText("Select nightly"));
    fireEvent.click(
      within(screen.getByRole("toolbar", { name: "Bulk actions" })).getByRole("button", { name: /Run now/ }),
    );

    await waitFor(() => {
      const rowRuns = within(screen.getByRole("table")).getAllByRole("button", {
        name: /Run now/,
      }) as HTMLButtonElement[];
      expect(rowRuns.length).toBe(2);
      expect(rowRuns.every((button) => button.disabled)).toBe(true);
    });

    resolveRun({});
    await waitFor(() => expect(listSecurityScanConfigs).toHaveBeenCalledTimes(2));
  });

  it("marks the select-all checkbox mixed for a partial selection and reports the result as a toast", async () => {
    listSecurityScanConfigs.mockResolvedValue({
      configs: [configFixture({ name: "nightly" }), configFixture({ name: "weekly" })],
    });
    runSecurityScanNow.mockResolvedValue({});
    renderList();

    await screen.findByText("weekly");
    const selectAll = screen.getByLabelText("Select all configurations") as HTMLInputElement;
    expect(selectAll.indeterminate).toBe(false);

    fireEvent.click(screen.getByLabelText("Select nightly"));
    await waitFor(() => expect(selectAll.indeterminate).toBe(true));
    expect(selectAll.getAttribute("aria-checked")).toBe("mixed");

    fireEvent.click(screen.getByLabelText("Select weekly"));
    await waitFor(() => expect(selectAll.indeterminate).toBe(false));
    expect(selectAll.checked).toBe(true);

    fireEvent.click(
      within(screen.getByRole("toolbar", { name: "Bulk actions" })).getByRole("button", { name: /Run now/ }),
    );
    await waitFor(() =>
      expect(toastSuccess).toHaveBeenCalledWith("Run now applied to 2 configurations"),
    );
  });

  it("offers Stop instead of Run now while a scan is running", async () => {
    listSecurityScanConfigs.mockResolvedValue({
      configs: [
        configFixture({ name: "nightly", phase: "Running" }),
        configFixture({ name: "weekly" }),
      ],
    });
    let resolveStop: (value: unknown) => void = () => {};
    cancelSecurityScanRun.mockReturnValue(new Promise((resolve) => { resolveStop = resolve; }));
    renderList();

    await screen.findByText("nightly");
    const stop = screen.getByRole("button", { name: /^Stop$/ });
    // The running row drops Run now; the idle row keeps it.
    expect(screen.getAllByRole("button", { name: /Run now/ })).toHaveLength(1);

    fireEvent.click(stop);
    expect(cancelSecurityScanRun).toHaveBeenCalledWith({ namespace: "user-alice", name: "nightly" });
    const pending = screen.getByRole("button", { name: /Stopping…/ }) as HTMLButtonElement;
    expect(pending.disabled).toBe(true);

    resolveStop({});
    await waitFor(() => expect(listSecurityScanConfigs).toHaveBeenCalledTimes(2));
  });
});
