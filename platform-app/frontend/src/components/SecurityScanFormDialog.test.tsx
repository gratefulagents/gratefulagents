import { afterEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { create } from "@bufbuild/protobuf";

import {
  SecurityScanFormDialog,
  scanConfigUsesSavedCredentials,
} from "@/components/SecurityScanFormDialog";
import { client } from "@/lib/client";
import {
  SecurityScanConfigSchema,
  SecurityScanConfigSpecSchema,
  type SecurityScanConfig,
} from "@/rpc/platform/service_pb";

vi.mock("@/lib/client", () => ({
  client: {
    createSecurityScan: vi.fn().mockResolvedValue({ namespace: "ns", name: "nightly-scan" }),
    updateSecurityScan: vi.fn(),
    listMyCredentials: vi.fn().mockResolvedValue({
      namespace: "ns",
      anthropicApiKeyPresent: false,
      openaiApiKeyPresent: false,
      anthropicOauthPresent: false,
      openaiOauthPresent: false,
      copilotOauthPresent: false,
      githubTokenPresent: false,
    }),
    listAvailableModels: vi.fn().mockResolvedValue({ models: [] }),
    listSSHTunnels: vi.fn().mockResolvedValue({ namespace: "user-alice", tunnels: [] }),
    getMyModelDefaults: vi
      .fn()
      .mockResolvedValue({ provider: "", model: "", reasoningLevel: "", disabled: false }),
    listMCPServers: vi.fn().mockResolvedValue({ servers: [] }),
    listSkills: vi.fn().mockResolvedValue({ skills: [] }),
    listRuntimeImages: vi.fn().mockResolvedValue({ images: [] }),
    listSecurityWorkflows: vi.fn().mockResolvedValue({
      workflows: [{ name: "payments-workflow", tasks: [{ name: "a" }], usageCount: 0, referencingScans: [] }],
    }),
    listSecurityRankers: vi.fn().mockResolvedValue({
      rankers: [{ name: "payments-ranker", rules: ["r"], usageCount: 0, referencingScans: [] }],
    }),
    listSecurityPostScripts: vi.fn().mockResolvedValue({
      postScripts: [{ name: "write-poc", prompt: "p", usageCount: 0, referencingScans: [] }],
    }),
    listSecurityPrograms: vi.fn().mockResolvedValue({
      programs: [
        {
          name: "acme-bounty",
          provider: "HackerOne",
          displayName: "Acme public bug bounty",
          programUrl: "https://hackerone.com/acme",
          scopePolicy: "In scope: api.example.com",
        },
      ],
    }),
    listSecurityPolicyPacks: vi.fn().mockResolvedValue({
      policyPacks: [
        {
          name: "baseline",
          description: "general scan defaults",
          minSeverity: "low",
          failOnSeverity: "critical",
          requiredCategories: [],
          allowedRuntimeProfiles: [],
          defaultRankerRefs: ["default-severity"],
          defaultPostScriptRefs: ["validate-finding"],
          enforced: [],
          suppressions: [],
          budgets: {},
          usageCount: 0,
          referencingScans: [],
        },
        {
          name: "prod-policy",
          description: "prod floors",
          minSeverity: "medium",
          failOnSeverity: "high",
          requiredCategories: ["injection"],
          allowedRuntimeProfiles: [],
          defaultRankerRefs: [],
          defaultPostScriptRefs: [],
          enforced: ["minSeverity", "budgets"],
          suppressions: [{ name: "vendored", reason: "r", owner: "o", matcher: { pathGlob: "vendor/**" } }],
          budgets: { maxCostUsd: "5" },
          usageCount: 0,
          referencingScans: [],
        },
      ],
    }),
  },
}));

vi.mock("@/contexts/AuthContext", () => ({
  useOptionalAuth: () => ({ user: { role: "admin" } }),
}));

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

function renderDialog() {
  render(<SecurityScanFormDialog trigger={<button>New scan</button>} defaultOpen />);
}

describe("SecurityScanFormDialog", () => {
  it("enables Docker-in-Docker by default for a new admin-authored scan", () => {
    renderDialog();
    fireEvent.click(screen.getByRole("button", { name: /Privileged runtime/ }));
    expect(screen.getByRole("switch", { name: "Docker-in-Docker" }).getAttribute("aria-checked")).toBe("true");
  });

  it("shows only the essentials up front; advanced pieces stay collapsed", () => {
    renderDialog();

    // Essentials.
    expect(screen.getByLabelText(/Repository URL/)).toBeTruthy();
    expect(screen.getByLabelText(/Schedule/)).toBeTruthy();
    expect(screen.getByLabelText(/Name/)).toBeTruthy();

    // Collapsed option rows render as summaries…
    expect(screen.getByRole("button", { name: /Workflow tasks/ })).toBeTruthy();
    expect(screen.getByText("Default workflow")).toBeTruthy();
    expect(screen.getByRole("button", { name: /Reporting/ })).toBeTruthy();
    expect(screen.getByText("min low · dedupe on")).toBeTruthy();
    expect(screen.getByRole("button", { name: /Scope/ })).toBeTruthy();
    expect(screen.getByText("Whole repository")).toBeTruthy();

    // …and their fields stay out of the DOM until expanded.
    expect(screen.queryByLabelText("Minimum severity")).toBeNull();
    expect(screen.queryByLabelText("Focus")).toBeNull();
  });

  it("expands an option row in place", () => {
    renderDialog();

    fireEvent.click(screen.getByRole("button", { name: /Reporting/ }));

    expect(screen.getByLabelText("Minimum severity")).toBeTruthy();
    expect(screen.getByLabelText("Fail on severity")).toBeTruthy();
    expect(screen.getByLabelText("Parallelism")).toBeTruthy();
  });

  it("offers actionable-only post-scripts and explains when they are skipped", () => {
    renderDialog();

    fireEvent.click(screen.getByRole("button", { name: /Rankers & post-scripts/ }));
    fireEvent.click(screen.getByRole("button", { name: "Add post-script" }));
    fireEvent.change(screen.getByLabelText("Run against"), {
      target: { value: "low-and-above-actionable" },
    });

    expect(screen.getByRole("option", { name: "low and above, while actionable" })).toBeTruthy();
    expect((screen.getByLabelText("Run against") as HTMLSelectElement).value).toBe("low-and-above-actionable");
    expect(screen.getByText(/successful earlier stage has already marked/i).textContent).toContain("fixed");
  });

  it("explains deterministic routing when workflow tasks are empty", () => {
    renderDialog();

    fireEvent.click(screen.getByRole("button", { name: /Workflow tasks/ }));

    const helpText = screen.getByText(/deterministically infer blockchain routing/i).textContent;
    expect(helpText).toContain("smart-contract-review");
    expect(helpText).toContain("blockchain-protocol-audit");
    expect(helpText).toContain("cosmos-abci-halt-review");
    expect(helpText).toContain("default-deep-scan");
  });

  it("creates a scan from a repository URL with saved credentials by default", async () => {
    renderDialog();

    fireEvent.change(screen.getByLabelText(/Repository URL/), {
      target: { value: "https://github.com/acme/payments.git" },
    });
    fireEvent.click(screen.getByRole("button", { name: "@daily" }));

    const form = document.querySelector("form");
    expect(form).toBeTruthy();
    fireEvent.submit(form as HTMLFormElement);

    await waitFor(() => {
      expect(client.createSecurityScan).toHaveBeenCalledTimes(1);
    });
    const request = vi.mocked(client.createSecurityScan).mock.calls[0][0];
    expect(request.spec?.repoUrl).toBe("https://github.com/acme/payments.git");
    expect(request.spec?.schedule).toBe("@daily");
    expect(request.spec?.workflow).toEqual([]);
    expect(request.spec?.securityProgramRef).toBe("");
    expect(request.spec?.dedupe?.enabled).toBe(true);
    expect(request.useSavedCredentials).toBe(true);
    expect(request.policies?.configureRuntimeProfile).toBe(true);
  });

  it("creates a repoless website scan and normalizes a bare domain to HTTPS", async () => {
    renderDialog();

    expect(screen.getByText("Scan target")).toBeTruthy();
    expect(screen.getByText("Repository events")).toBeTruthy();
    fireEvent.change(screen.getByLabelText(/Target type/), {
      target: { value: "website" },
    });
    expect(screen.queryByText("Scan target")).toBeNull();
    expect(screen.queryByText("Repository events")).toBeNull();
    fireEvent.change(screen.getByLabelText(/Website URL or domain/), {
      target: { value: "staging.example.com" },
    });

    const form = document.querySelector("form");
    expect(form).toBeTruthy();
    fireEvent.submit(form as HTMLFormElement);

    await waitFor(() => expect(client.createSecurityScan).toHaveBeenCalledTimes(1));
    const request = vi.mocked(client.createSecurityScan).mock.calls[0][0];
    expect(request.spec?.repoUrl).toBe("");
    expect(request.spec?.targetUrl).toBe("https://staging.example.com");
    expect(request.spec?.baseBranch).toBe("");
    expect(request.spec?.additionalRepos).toEqual([]);
  });

  it("creates manual-only scans without schedule or suspend semantics", async () => {
    renderDialog();
    fireEvent.change(screen.getByLabelText(/Repository URL/), {
      target: { value: "https://github.com/acme/payments.git" },
    });
    fireEvent.click(screen.getByRole("button", { name: "@daily" }));
    fireEvent.click(screen.getByRole("switch", { name: "Manual-only" }));

    expect((screen.getByLabelText(/Schedule/) as HTMLInputElement).disabled).toBe(true);
    expect((screen.getByLabelText(/Schedule/) as HTMLInputElement).value).toBe("");
    fireEvent.click(screen.getByRole("button", { name: /Scheduling/ }));
    expect(screen.getByRole("switch", { name: "Suspend" }).hasAttribute("data-disabled")).toBe(true);
    fireEvent.submit(document.querySelector("form") as HTMLFormElement);

    await waitFor(() => expect(client.createSecurityScan).toHaveBeenCalledTimes(1));
    expect(vi.mocked(client.createSecurityScan).mock.calls[0][0].spec).toMatchObject({
      manualOnly: true,
      schedule: "",
      suspend: false,
    });
  });

  it("submits configured severity and scope fields", async () => {
    renderDialog();

    fireEvent.change(screen.getByLabelText(/Repository URL/), {
      target: { value: "https://github.com/acme/payments.git" },
    });
    fireEvent.click(screen.getByRole("button", { name: /Reporting/ }));
    fireEvent.change(screen.getByLabelText("Minimum severity"), { target: { value: "medium" } });
    fireEvent.change(screen.getByLabelText("Fail on severity"), { target: { value: "high" } });
    fireEvent.click(screen.getByRole("button", { name: /Scope/ }));
    fireEvent.change(screen.getByLabelText("Exclude paths"), {
      target: { value: "vendor/**, testdata/**" },
    });
    fireEvent.change(screen.getByLabelText("Authorized network targets"), {
      target: { value: "staging.example.com, https://api.example.com\n10.0.0.0/8" },
    });

    fireEvent.submit(document.querySelector("form") as HTMLFormElement);

    await waitFor(() => {
      expect(client.createSecurityScan).toHaveBeenCalledTimes(1);
    });
    const request = vi.mocked(client.createSecurityScan).mock.calls[0][0];
    expect(request.spec?.minSeverity).toBe("medium");
    expect(request.spec?.failOnSeverity).toBe("high");
    expect(request.spec?.scope?.excludePaths).toEqual(["vendor/**", "testdata/**"]);
    expect(request.spec?.scope?.authorizedNetworkTargets).toEqual([
      "staging.example.com",
      "https://api.example.com",
      "10.0.0.0/8",
    ]);
  });

  it("selects a security program while keeping network authorization explicit", async () => {
    renderDialog();
    fireEvent.change(screen.getByLabelText(/Repository URL/), {
      target: { value: "https://github.com/acme/payments.git" },
    });
    fireEvent.click(screen.getByRole("button", { name: /Scope/ }));

    const programSelect = await screen.findByLabelText("Security program");
    fireEvent.change(programSelect, { target: { value: "acme-bounty" } });
    const summary = await screen.findByTestId("security-program-summary");
    expect(summary.textContent).toContain("https://hackerone.com/acme");
    expect(summary.textContent).toContain("In scope: api.example.com");
    expect(screen.getAllByText(/does not authorize network testing/i).length).toBeGreaterThan(0);

    fireEvent.submit(document.querySelector("form") as HTMLFormElement);
    await waitFor(() => expect(client.createSecurityScan).toHaveBeenCalledTimes(1));
    const request = vi.mocked(client.createSecurityScan).mock.calls[0][0];
    expect(request.spec?.securityProgramRef).toBe("acme-bounty");
    expect(request.spec?.scope?.authorizedNetworkTargets).toEqual([]);
  });

  it("omits network authorization when the targets field is left empty", async () => {
    renderDialog();

    fireEvent.change(screen.getByLabelText(/Repository URL/), {
      target: { value: "https://github.com/acme/payments.git" },
    });

    fireEvent.submit(document.querySelector("form") as HTMLFormElement);

    await waitFor(() => {
      expect(client.createSecurityScan).toHaveBeenCalledTimes(1);
    });
    const request = vi.mocked(client.createSecurityScan).mock.calls[0][0];
    expect(request.spec?.scope?.authorizedNetworkTargets).toEqual([]);
  });

  it("keeps validation errors inline when the repository URL is missing", async () => {
    renderDialog();

    const form = document.querySelector("form");
    fireEvent.submit(form as HTMLFormElement);

    expect(await screen.findByRole("alert")).toBeTruthy();
    expect(client.createSecurityScan).not.toHaveBeenCalled();
  });

  it("round-trips a security program reference when editing", async () => {
    vi.mocked(client.updateSecurityScan).mockResolvedValue(
      create(SecurityScanConfigSchema, { namespace: "user-alice", name: "nightly" }),
    );
    const config = create(SecurityScanConfigSchema, {
      namespace: "user-alice",
      name: "nightly",
      spec: create(SecurityScanConfigSpecSchema, {
        repoUrl: "https://github.com/acme/payments.git",
        securityProgramRef: "acme-bounty",
      }),
    });
    render(<SecurityScanFormDialog config={config} trigger={<button>Edit</button>} defaultOpen />);
    fireEvent.click(screen.getByRole("button", { name: /Scope/ }));

    expect((await screen.findByLabelText("Security program") as HTMLSelectElement).value).toBe(
      "acme-bounty",
    );
    fireEvent.submit(document.querySelector("form") as HTMLFormElement);

    await waitFor(() => expect(client.updateSecurityScan).toHaveBeenCalledTimes(1));
    expect(vi.mocked(client.updateSecurityScan).mock.calls[0][0].spec?.securityProgramRef).toBe(
      "acme-bounty",
    );
  });
});

describe("SecurityScanFormDialog model defaults", () => {
  it("seeds saved model defaults for a scan prefilled from an imported program target", async () => {
    vi.mocked(client.getMyModelDefaults).mockResolvedValueOnce({
      provider: "openai",
      model: "gpt-5.2",
      reasoningLevel: "high",
      disabled: false,
    } as never);
    render(
      <SecurityScanFormDialog
        initialConfig={create(SecurityScanConfigSchema, {
          name: "acme-protocol",
          spec: create(SecurityScanConfigSpecSchema, {
            repoUrl: "https://github.com/acme/protocol",
            workflowRef: "blockchain-protocol-audit",
            securityProgramRef: "acme-protocol",
            manualOnly: true,
          }),
        })}
        trigger={<button>Configure</button>}
        defaultOpen
      />,
    );

    await waitFor(() => expect(client.getMyModelDefaults).toHaveBeenCalled());
    await act(async () => {}); // flush the resolved defaults into state
    fireEvent.submit(document.querySelector("form") as HTMLFormElement);
    await waitFor(() => expect(client.createSecurityScan).toHaveBeenCalledTimes(1));
    expect(vi.mocked(client.createSecurityScan).mock.calls[0][0].spec?.defaults).toMatchObject({
      provider: "openai",
      model: "gpt-5.2",
      reasoningLevel: "high",
    });
  });

  it("does not seed model defaults when duplicating an existing scan", async () => {
    vi.mocked(client.getMyModelDefaults).mockResolvedValue({
      provider: "openai",
      model: "gpt-5.2",
      reasoningLevel: "high",
      disabled: false,
    } as never);
    render(
      <SecurityScanFormDialog
        duplicateFrom={create(SecurityScanConfigSchema, {
          namespace: "user-alice",
          name: "existing-scan",
          spec: create(SecurityScanConfigSpecSchema, {
            repoUrl: "https://github.com/acme/protocol",
            workflowRef: "blockchain-protocol-audit",
          }),
        })}
        trigger={<button>Duplicate</button>}
        defaultOpen
      />,
    );

    fireEvent.change(screen.getByLabelText(/Name/), { target: { value: "copy-scan" } });
    fireEvent.submit(document.querySelector("form") as HTMLFormElement);
    await waitFor(() => expect(client.createSecurityScan).toHaveBeenCalledTimes(1));
    const defaults = vi.mocked(client.createSecurityScan).mock.calls[0][0].spec?.defaults;
    expect(defaults?.provider ?? "").toBe("");
    expect(defaults?.model ?? "").toBe("");
    expect(client.getMyModelDefaults).not.toHaveBeenCalled();
  });
});

describe("SecurityScanFormDialog duplicate mode", () => {
  it("opens a normal create form prefilled from a bounty target", async () => {
    const initialConfig = create(SecurityScanConfigSchema, {
      name: "immunefi-optimism",
      spec: create(SecurityScanConfigSpecSchema, {
        repoUrl: "https://github.com/ethereum-optimism/optimism",
        baseBranch: "develop",
        workflowRef: "blockchain-protocol-audit",
        policyPackRef: "bug-bounty",
        securityProgramRef: "immunefi-optimism",
        manualOnly: true,
        minSeverity: "high",
        parallelism: 4,
        dedupe: { enabled: true },
      }),
    });
    render(
      <SecurityScanFormDialog
        initialConfig={initialConfig}
        trigger={<button>Configure</button>}
        defaultOpen
      />,
    );

    expect(screen.getByText("New security scan")).toBeTruthy();
    expect((screen.getByLabelText(/Name/) as HTMLInputElement).value).toBe("immunefi-optimism");
    expect((screen.getByLabelText(/Repository URL/) as HTMLInputElement).value).toBe(
      "https://github.com/ethereum-optimism/optimism",
    );
    expect(screen.getByRole("switch", { name: "Manual-only" }).getAttribute("aria-checked")).toBe("true");

    fireEvent.submit(document.querySelector("form") as HTMLFormElement);
    await waitFor(() => expect(client.createSecurityScan).toHaveBeenCalledTimes(1));
    expect(client.updateSecurityScan).not.toHaveBeenCalled();
    expect(vi.mocked(client.createSecurityScan).mock.calls[0][0]).toMatchObject({
      name: "immunefi-optimism",
      useSavedCredentials: true,
      spec: {
        repoUrl: "https://github.com/ethereum-optimism/optimism",
        baseBranch: "develop",
        workflowRef: "blockchain-protocol-audit",
        policyPackRef: "bug-bounty",
        securityProgramRef: "immunefi-optimism",
        manualOnly: true,
        minSeverity: "high",
        parallelism: 4,
      },
      policies: {
        configureRuntimeProfile: true,
        permissionMode: "workspace-write",
        egressMode: "unrestricted",
      },
    });
  });

  it("cannot be dismissed while creating", async () => {
    let resolveCreate: (value: SecurityScanConfig) => void = () => {};
    vi.mocked(client.createSecurityScan).mockReturnValueOnce(
      new Promise<SecurityScanConfig>((resolve) => { resolveCreate = resolve; }),
    );
    const onOpenChange = vi.fn();
    render(
      <SecurityScanFormDialog
        initialConfig={create(SecurityScanConfigSchema, {
          name: "pending-scan",
          spec: create(SecurityScanConfigSpecSchema, { repoUrl: "https://github.com/acme/repo" }),
        })}
        defaultOpen
        onOpenChange={onOpenChange}
      />,
    );

    fireEvent.submit(document.querySelector("form") as HTMLFormElement);
    await waitFor(() => expect(client.createSecurityScan).toHaveBeenCalledTimes(1));
    expect((screen.getByRole("button", { name: "Cancel" }) as HTMLButtonElement).disabled).toBe(true);
    fireEvent.keyDown(document, { key: "Escape" });
    expect(screen.getByRole("dialog")).toBeTruthy();
    expect(onOpenChange).not.toHaveBeenCalledWith(false);

    resolveCreate(create(SecurityScanConfigSchema, { name: "pending-scan" }));
    await waitFor(() => expect(onOpenChange).toHaveBeenCalledWith(false));
  });

  it("preserves manual-only when editing an imported configuration", async () => {
    vi.mocked(client.updateSecurityScan).mockResolvedValue(
      create(SecurityScanConfigSchema, { namespace: "user-alice", name: "immunefi-layerzero" }),
    );
    const importedSpec = create(SecurityScanConfigSpecSchema, {
      repoUrl: "https://github.com/LayerZero-Labs/LayerZero-v2",
    });
    Object.assign(importedSpec, { manualOnly: true });
    const config = create(SecurityScanConfigSchema, {
      namespace: "user-alice",
      name: "immunefi-layerzero",
      spec: importedSpec,
    });
    render(<SecurityScanFormDialog config={config} trigger={<button>Edit</button>} defaultOpen />);

    expect(screen.getByRole("switch", { name: "Manual-only" }).getAttribute("aria-checked")).toBe("true");
    fireEvent.submit(document.querySelector("form") as HTMLFormElement);

    await waitFor(() => expect(client.updateSecurityScan).toHaveBeenCalledTimes(1));
    expect(vi.mocked(client.updateSecurityScan).mock.calls[0][0].spec).toMatchObject({ manualOnly: true });
  });

  function sourceConfig() {
    return create(SecurityScanConfigSchema, {
      namespace: "user-alice",
      name: "nightly",
      spec: create(SecurityScanConfigSpecSchema, {
        repoUrl: "https://github.com/acme/payments.git",
        schedule: "@daily",
        minSeverity: "medium",
      }),
    });
  }

  function renderDuplicateDialog() {
    render(
      <SecurityScanFormDialog
        duplicateFrom={sourceConfig()}
        trigger={<button>Duplicate</button>}
        defaultOpen
      />,
    );
  }

  it("pre-fills from the source config but requires a new name", async () => {
    renderDuplicateDialog();

    expect(screen.getByText("Duplicate nightly")).toBeTruthy();
    expect((screen.getByLabelText(/Repository URL/) as HTMLInputElement).value).toBe(
      "https://github.com/acme/payments.git",
    );
    expect((screen.getByLabelText(/Schedule/) as HTMLInputElement).value).toBe("@daily");
    expect((screen.getByLabelText(/Name/) as HTMLInputElement).value).toBe("");

    // Submitting without a name is a review-step validation error, not a create.
    fireEvent.submit(document.querySelector("form") as HTMLFormElement);
    expect((await screen.findByRole("alert")).textContent).toContain(
      "Give the duplicated scan a new name.",
    );
    expect(client.createSecurityScan).not.toHaveBeenCalled();
  });

  it("creates a new scan with the copied spec and never updates the source", async () => {
    renderDuplicateDialog();

    fireEvent.change(screen.getByLabelText(/Name/), { target: { value: "nightly-copy" } });
    fireEvent.submit(document.querySelector("form") as HTMLFormElement);

    await waitFor(() => {
      expect(client.createSecurityScan).toHaveBeenCalledTimes(1);
    });
    const request = vi.mocked(client.createSecurityScan).mock.calls[0][0];
    expect(request.name).toBe("nightly-copy");
    expect(request.spec?.repoUrl).toBe("https://github.com/acme/payments.git");
    expect(request.spec?.minSeverity).toBe("medium");
    expect(client.updateSecurityScan).not.toHaveBeenCalled();
  });

  it("surfaces a name collision instead of overwriting the existing scan", async () => {
    vi.mocked(client.createSecurityScan).mockRejectedValueOnce(
      new Error("[already_exists] SecurityScan user-alice/nightly already exists"),
    );
    renderDuplicateDialog();

    fireEvent.change(screen.getByLabelText(/Name/), { target: { value: "nightly" } });
    fireEvent.submit(document.querySelector("form") as HTMLFormElement);

    expect((await screen.findByRole("alert")).textContent).toContain("already exists");
    expect(client.updateSecurityScan).not.toHaveBeenCalled();
  });

  it("selects a library workflow and refs, sending workflowRef and appended refs", async () => {
    renderDialog();

    fireEvent.change(screen.getByLabelText(/Repository URL/), {
      target: { value: "https://github.com/acme/payments.git" },
    });

    fireEvent.click(screen.getByRole("button", { name: /Workflow tasks/ }));
    const workflowSelect = (await screen.findByLabelText("Library workflow")) as HTMLSelectElement;
    fireEvent.change(workflowSelect, { target: { value: "payments-workflow" } });
    // Snapshot semantics are explained next to the picker.
    expect(screen.getAllByText(/snapshotted/i).length).toBeGreaterThan(0);
    // Inline task editing is disabled while a library workflow is selected.
    expect(screen.queryByRole("button", { name: "Add workflow task" })).toBeNull();

    fireEvent.click(screen.getByRole("button", { name: /Rankers & post-scripts/ }));
    fireEvent.click(await screen.findByRole("checkbox", { name: /payments-ranker/ }));
    fireEvent.click(screen.getByRole("checkbox", { name: /write-poc/ }));

    const form = document.querySelector("form");
    fireEvent.submit(form as HTMLFormElement);

    await waitFor(() => {
      expect(client.createSecurityScan).toHaveBeenCalledTimes(1);
    });
    const request = vi.mocked(client.createSecurityScan).mock.calls[0][0];
    expect(request.spec?.workflowRef).toBe("payments-workflow");
    expect(request.spec?.workflow).toEqual([]);
    expect(request.spec?.rankerRefs).toEqual(["payments-ranker"]);
    expect(request.spec?.postScriptRefs).toEqual(["write-poc"]);
  });
});

describe("SecurityScanFormDialog repository events and notifications", () => {
  it("submits repository event triggers, checks, and notification rules", async () => {
    renderDialog();

    fireEvent.change(screen.getByLabelText(/Repository URL/), {
      target: { value: "https://github.com/acme/payments.git" },
    });

    fireEvent.click(screen.getByRole("button", { name: /Repository events/ }));
    fireEvent.change(screen.getByLabelText("GitHub repository connection"), {
      target: { value: "widget-repo" },
    });
    fireEvent.click(screen.getByRole("switch", { name: "Scan pull requests" }));
    fireEvent.click(screen.getByRole("switch", { name: "Scan pushes" }));
    fireEvent.change(screen.getByLabelText("Push branch filters"), {
      target: { value: "main, release/*" },
    });
    fireEvent.click(screen.getByRole("switch", { name: "Diff scope" }));
    fireEvent.click(screen.getByRole("switch", { name: "Publish GitHub checks" }));
    fireEvent.click(screen.getByRole("switch", { name: "Upload SARIF to code scanning" }));

    fireEvent.click(screen.getByRole("button", { name: /Notifications/ }));
    fireEvent.click(screen.getByRole("button", { name: "Add notification rule" }));
    fireEvent.change(screen.getByLabelText(/Rule name/), { target: { value: "critical-alerts" } });
    fireEvent.change(screen.getByLabelText("Minimum severity"), { target: { value: "critical" } });
    fireEvent.change(screen.getByLabelText("Slack webhook secret"), {
      target: { value: "slack-webhook" },
    });

    fireEvent.submit(document.querySelector("form") as HTMLFormElement);
    await waitFor(() => {
      expect(client.createSecurityScan).toHaveBeenCalledTimes(1);
    });
    const request = vi.mocked(client.createSecurityScan).mock.calls[0][0];
    expect(request.spec?.triggers?.repositoryRef).toBe("widget-repo");
    expect(request.spec?.triggers?.onPullRequest).toBe(true);
    expect(request.spec?.triggers?.onPush).toBe(true);
    expect(request.spec?.triggers?.branches).toEqual(["main", "release/*"]);
    expect(request.spec?.triggers?.diffScope).toBe(true);
    expect(request.spec?.triggers?.allowForks).toBe(false);
    expect(request.spec?.checks?.enabled).toBe(true);
    expect(request.spec?.checks?.uploadSarif).toBe(true);
    expect(request.spec?.checks?.includeFindingSummaries).toBe(false);
    expect(request.spec?.notifications).toHaveLength(1);
    expect(request.spec?.notifications?.[0].name).toBe("critical-alerts");
    expect(request.spec?.notifications?.[0].minSeverity).toBe("critical");
    expect(request.spec?.notifications?.[0].slackWebhookSecretRef).toBe("slack-webhook");
  });

  it("explains fork and credential safety in the section help text", () => {
    renderDialog();
    fireEvent.click(screen.getByRole("button", { name: /Repository events/ }));
    expect(screen.getByText(/the scan run itself never receives them/)).toBeTruthy();
    expect(screen.getByText(/GitHub credential is stripped/)).toBeTruthy();
  });

  it("blocks event triggers without a repository connection", async () => {
    renderDialog();
    fireEvent.change(screen.getByLabelText(/Repository URL/), {
      target: { value: "https://github.com/acme/payments.git" },
    });
    fireEvent.click(screen.getByRole("button", { name: /Repository events/ }));
    fireEvent.click(screen.getByRole("switch", { name: "Scan pull requests" }));

    fireEvent.submit(document.querySelector("form") as HTMLFormElement);

    expect(await screen.findByRole("alert")).toBeTruthy();
    expect(screen.getByRole("alert").textContent).toMatch(/repository reference/i);
    expect(client.createSecurityScan).not.toHaveBeenCalled();
  });

  it("blocks notification rules without a channel", async () => {
    renderDialog();
    fireEvent.change(screen.getByLabelText(/Repository URL/), {
      target: { value: "https://github.com/acme/payments.git" },
    });
    fireEvent.click(screen.getByRole("button", { name: /Notifications/ }));
    fireEvent.click(screen.getByRole("button", { name: "Add notification rule" }));
    fireEvent.change(screen.getByLabelText(/Rule name/), { target: { value: "no-channel" } });

    fireEvent.submit(document.querySelector("form") as HTMLFormElement);

    expect(await screen.findByRole("alert")).toBeTruthy();
    expect(screen.getByRole("alert").textContent).toMatch(/at least one channel/i);
    expect(client.createSecurityScan).not.toHaveBeenCalled();
  });

  it("requires Linear key and team together", async () => {
    renderDialog();
    fireEvent.change(screen.getByLabelText(/Repository URL/), {
      target: { value: "https://github.com/acme/payments.git" },
    });
    fireEvent.click(screen.getByRole("button", { name: /Notifications/ }));
    fireEvent.click(screen.getByRole("button", { name: "Add notification rule" }));
    fireEvent.change(screen.getByLabelText(/Rule name/), { target: { value: "linear-only" } });
    fireEvent.change(screen.getByLabelText("Linear API key secret"), {
      target: { value: "linear-key" },
    });

    fireEvent.submit(document.querySelector("form") as HTMLFormElement);

    expect(await screen.findByRole("alert")).toBeTruthy();
    expect(screen.getByRole("alert").textContent).toMatch(/both the API key secret and the team ID/i);
    expect(client.createSecurityScan).not.toHaveBeenCalled();
  });
});

describe("SecurityScanFormDialog policy pack & budgets", () => {
  it("waits for policy discovery when a scratch scan is submitted immediately", async () => {
    let resolvePacks!: (value: Awaited<ReturnType<typeof client.listSecurityPolicyPacks>>) => void;
    vi.mocked(client.listSecurityPolicyPacks).mockImplementationOnce(
      () => new Promise((resolve) => { resolvePacks = resolve; }),
    );

    renderDialog();
    fireEvent.change(screen.getByLabelText(/Repository URL/), {
      target: { value: "https://github.com/acme/payments.git" },
    });
    fireEvent.submit(document.querySelector("form") as HTMLFormElement);
    expect(client.createSecurityScan).not.toHaveBeenCalled();

    await act(async () => {
      resolvePacks({
        policyPacks: [{
          name: "baseline",
          description: "general scan defaults",
          minSeverity: "low",
          failOnSeverity: "critical",
          requiredCategories: [],
          allowedRuntimeProfiles: [],
          defaultRankerRefs: ["default-severity"],
          defaultPostScriptRefs: ["validate-finding"],
          enforced: [],
          suppressions: [],
          budgets: {},
          usageCount: 0,
          referencingScans: [],
        }],
      } as unknown as Awaited<ReturnType<typeof client.listSecurityPolicyPacks>>);
    });

    await waitFor(() => expect(client.createSecurityScan).toHaveBeenCalledTimes(1));
    const request = vi.mocked(client.createSecurityScan).mock.calls[0][0];
    expect(request.spec?.policyPackRef).toBe("baseline");
  });

  it("defaults a scratch scan to baseline and shows its effective post-processing", async () => {
    renderDialog();

    fireEvent.click(screen.getByRole("button", { name: /Policy pack & budgets/ }));
    const select = await screen.findByLabelText("Policy pack");
    await waitFor(() => expect((select as HTMLSelectElement).value).toBe("baseline"));

    expect(screen.getByRole("button", { name: /Rankers & post-scripts/ }).textContent)
      .toContain("1 ranker · 1 post-script");
  });

  it("shows the pack's inherited values and enforced fields when selected", async () => {
    renderDialog();

    fireEvent.click(screen.getByRole("button", { name: /Policy pack & budgets/ }));
    const select = await screen.findByLabelText("Policy pack");
    await waitFor(() => {
      expect(select.querySelectorAll("option").length).toBeGreaterThan(1);
    });
    fireEvent.change(select, { target: { value: "prod-policy" } });

    const summary = await screen.findByTestId("policy-pack-summary");
    expect(summary.textContent).toContain("Inherited from prod-policy");
    expect(summary.textContent).toContain("Minimum severity: medium");
    expect(summary.textContent).toContain("Fail on severity: high");
    expect(summary.textContent).toContain("Required categories: injection");
    expect(summary.textContent).toContain("Governed suppressions: 1 rule");

    const enforced = screen.getByTestId("policy-pack-enforced");
    expect(enforced.textContent).toContain("this scan may not relax");
    expect(enforced.textContent).toContain("minimum severity");
    expect(enforced.textContent).toContain("budgets");

    // The pack enforces budgets, so the budget inputs carry a warning.
    const warning = screen.getByTestId("policy-pack-budget-warning");
    expect(warning.textContent).toContain("enforces budgets");
  });

  it("hides the budget warning when no pack is selected", async () => {
    renderDialog();

    fireEvent.click(screen.getByRole("button", { name: /Policy pack & budgets/ }));
    const select = await screen.findByLabelText("Policy pack");
    await waitFor(() => expect((select as HTMLSelectElement).value).toBe("baseline"));
    fireEvent.change(select, { target: { value: "" } });
    expect(screen.queryByTestId("policy-pack-budget-warning")).toBeNull();
    expect(screen.queryByTestId("policy-pack-summary")).toBeNull();
  });

  it("submits the policy pack ref and per-scan budgets", async () => {
    renderDialog();

    fireEvent.change(screen.getByLabelText(/Repository URL/), {
      target: { value: "https://github.com/acme/payments.git" },
    });
    fireEvent.click(screen.getByRole("button", { name: /Policy pack & budgets/ }));
    const select = await screen.findByLabelText("Policy pack");
    await waitFor(() => {
      expect(select.querySelectorAll("option").length).toBeGreaterThan(1);
    });
    fireEvent.change(select, { target: { value: "prod-policy" } });
    fireEvent.change(screen.getByLabelText("Max cost (USD)"), { target: { value: "2.50" } });
    fireEvent.submit(document.querySelector("form") as HTMLFormElement);

    await waitFor(() => {
      expect(client.createSecurityScan).toHaveBeenCalledTimes(1);
    });
    const request = vi.mocked(client.createSecurityScan).mock.calls[0][0];
    expect(request.spec?.policyPackRef).toBe("prod-policy");
    expect(request.spec?.budgets?.maxCostUsd).toBe("2.50");
  });

  it("rejects an invalid budget cost client-side", async () => {
    renderDialog();

    fireEvent.change(screen.getByLabelText(/Repository URL/), {
      target: { value: "https://github.com/acme/payments.git" },
    });
    fireEvent.click(screen.getByRole("button", { name: /Policy pack & budgets/ }));
    await screen.findByLabelText("Policy pack");
    fireEvent.change(screen.getByLabelText("Max cost (USD)"), { target: { value: "$5" } });

    fireEvent.submit(document.querySelector("form") as HTMLFormElement);

    expect(await screen.findByRole("alert")).toBeTruthy();
    expect(screen.getByRole("alert").textContent).toMatch(/plain decimal/i);
    expect(client.createSecurityScan).not.toHaveBeenCalled();
  });
});

describe("SecurityScanFormDialog execution & parameter values", () => {
  it("omits execution settings for the deterministic default", async () => {
    renderDialog();

    fireEvent.change(screen.getByLabelText(/Repository URL/), {
      target: { value: "https://github.com/acme/payments.git" },
    });
    fireEvent.click(screen.getByRole("button", { name: /Execution/ }));
    expect((screen.getByLabelText("Execution mode") as HTMLSelectElement).value).toBe("");
    expect(screen.getByRole("option", { name: "deterministic (default)" })).toBeTruthy();

    fireEvent.submit(document.querySelector("form") as HTMLFormElement);
    await waitFor(() => {
      expect(client.createSecurityScan).toHaveBeenCalledTimes(1);
    });
    const request = vi.mocked(client.createSecurityScan).mock.calls[0][0];
    expect(request.spec?.execution).toBeUndefined();
  });

  it("submits an explicit coordinator override", async () => {
    renderDialog();

    fireEvent.change(screen.getByLabelText(/Repository URL/), {
      target: { value: "https://github.com/acme/payments.git" },
    });
    fireEvent.click(screen.getByRole("button", { name: /Execution/ }));
    fireEvent.change(screen.getByLabelText("Execution mode"), {
      target: { value: "coordinator" },
    });

    fireEvent.submit(document.querySelector("form") as HTMLFormElement);
    await waitFor(() => {
      expect(client.createSecurityScan).toHaveBeenCalledTimes(1);
    });
    const request = vi.mocked(client.createSecurityScan).mock.calls[0][0];
    expect(request.spec?.execution?.mode).toBe("coordinator");
  });

  it("submits execution settings and parameter values", async () => {
    renderDialog();

    fireEvent.change(screen.getByLabelText(/Repository URL/), {
      target: { value: "https://github.com/acme/payments.git" },
    });
    fireEvent.click(screen.getByRole("button", { name: /Execution/ }));
    fireEvent.change(screen.getByLabelText("Task max retries"), { target: { value: "3" } });
    fireEvent.change(screen.getByLabelText("Retry backoff"), { target: { value: "45s" } });
    fireEvent.click(screen.getByRole("button", { name: "Add parameter value" }));
    fireEvent.change(screen.getByLabelText("Parameter 1 name"), {
      target: { value: "target_service" },
    });
    fireEvent.change(screen.getByLabelText("Parameter 1 value"), {
      target: { value: "payments-api" },
    });

    fireEvent.submit(document.querySelector("form") as HTMLFormElement);
    await waitFor(() => {
      expect(client.createSecurityScan).toHaveBeenCalledTimes(1);
    });
    const request = vi.mocked(client.createSecurityScan).mock.calls[0][0];
    expect(request.spec?.execution?.mode).toBe("");
    expect(request.spec?.execution?.taskMaxRetries).toBe(3);
    expect(request.spec?.execution?.retryBackoff).toBe("45s");
    expect(request.spec?.parameterValues).toEqual({ target_service: "payments-api" });
  });

  it("rejects a malformed retry backoff before submitting", async () => {
    renderDialog();
    fireEvent.change(screen.getByLabelText(/Repository URL/), {
      target: { value: "https://github.com/acme/payments.git" },
    });
    fireEvent.click(screen.getByRole("button", { name: /Execution/ }));
    fireEvent.change(screen.getByLabelText("Retry backoff"), {
      target: { value: "30 seconds" },
    });

    fireEvent.submit(document.querySelector("form") as HTMLFormElement);

    expect((await screen.findByRole("alert")).textContent).toMatch(/Go duration/);
    expect(client.createSecurityScan).not.toHaveBeenCalled();
  });

  it("round-trips execution, parameter values, and advanced task fields when editing", async () => {
    vi.mocked(client.updateSecurityScan).mockResolvedValue(
      create(SecurityScanConfigSchema, { namespace: "user-alice", name: "nightly" }),
    );
    const config = create(SecurityScanConfigSchema, {
      namespace: "user-alice",
      name: "nightly",
      spec: create(SecurityScanConfigSpecSchema, {
        repoUrl: "https://github.com/acme/payments.git",
        workflow: [
          {
            name: "recon",
            objective: "Map the surface.",
            maxRetries: 2,
            timeout: "30m",
            maxTurns: 40,
            maxCostUsd: "2.50",
            tools: { allowed: ["read_file"], denied: ["Bash"] },
            outputSchema: '{"type":"object"}',
            forEach: "",
            maxInstances: 5,
            repeats: 2,
          },
        ],
        execution: { mode: "deterministic", taskMaxRetries: 2, retryBackoff: "1m" },
        parameterValues: { depth: "full" },
      }),
    });
    render(<SecurityScanFormDialog config={config} trigger={<button>Edit</button>} defaultOpen />);

    fireEvent.submit(document.querySelector("form") as HTMLFormElement);
    await waitFor(() => {
      expect(client.updateSecurityScan).toHaveBeenCalledTimes(1);
    });
    const request = vi.mocked(client.updateSecurityScan).mock.calls[0][0];
    expect(request.spec?.execution?.mode).toBe("deterministic");
    expect(request.spec?.execution?.taskMaxRetries).toBe(2);
    expect(request.spec?.execution?.retryBackoff).toBe("1m");
    expect(request.spec?.parameterValues).toEqual({ depth: "full" });
    // Advanced task fields the inline editor does not expose survive the edit.
    const task = request.spec?.workflow?.[0];
    expect(task?.maxRetries).toBe(2);
    expect(task?.timeout).toBe("30m");
    expect(task?.maxTurns).toBe(40);
    expect(task?.maxCostUsd).toBe("2.50");
    expect(task?.tools?.allowed).toEqual(["read_file"]);
    expect(task?.tools?.denied).toEqual(["Bash"]);
    expect(task?.outputSchema).toBe('{"type":"object"}');
    expect(task?.maxInstances).toBe(5);
    expect(task?.repeats).toBe(2);
  });

  it("recognizes materialized saved refs from servers without the response marker", () => {
    const legacy = create(SecurityScanConfigSchema, {
      spec: create(SecurityScanConfigSpecSchema, {
        defaults: {
          openaiOauthSecret: "usercred-openai",
          githubTokenSecret: "usercred-github",
        },
      }),
    });
    const explicitlyDisabled = create(SecurityScanConfigSchema, {
      useSavedCredentials: false,
      spec: legacy.spec,
    });
    const prefixedExplicit = create(SecurityScanConfigSchema, {
      spec: create(SecurityScanConfigSpecSchema, {
        defaults: { openaiOauthSecret: "usercred-team-openai" },
      }),
    });

    expect(scanConfigUsesSavedCredentials(legacy)).toBe(true);
    expect(scanConfigUsesSavedCredentials(explicitlyDisabled)).toBe(false);
    expect(scanConfigUsesSavedCredentials(prefixedExplicit)).toBe(false);
  });

  it("keeps saved credentials checked when editing a materialized bulk import", async () => {
    const config = create(SecurityScanConfigSchema, {
      namespace: "ns",
      name: "bulk-imported-scan",
      useSavedCredentials: true,
      spec: create(SecurityScanConfigSpecSchema, {
        repoUrl: "https://github.com/acme/repo",
        defaults: {
          provider: "openai",
          authMode: "oauth",
          openaiOauthSecret: "usercred-openai",
        },
      }),
    });
    vi.mocked(client.updateSecurityScan).mockResolvedValue(config);

    render(<SecurityScanFormDialog config={config} trigger={<button>Edit scan</button>} defaultOpen />);
    fireEvent.click(screen.getByRole("button", { name: /^Model/ }));

    expect(
      screen.getByRole("switch", { name: "Use my saved credentials" }).getAttribute("aria-checked"),
    ).toBe("true");

    fireEvent.submit(document.querySelector("form") as HTMLFormElement);
    await waitFor(() => expect(client.updateSecurityScan).toHaveBeenCalledTimes(1));
    expect(vi.mocked(client.updateSecurityScan).mock.calls[0][0].useSavedCredentials).toBe(true);
  });
});
