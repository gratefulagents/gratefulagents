import { create } from "@bufbuild/protobuf";
import { timestampDate, timestampFromDate } from "@bufbuild/protobuf/wkt";
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";

import { SecurityProgramDialog } from "@/components/SecurityProgramDialog";
import { SecurityProgramResourceSchema } from "@/rpc/platform/service_pb";

const { createSecurityProgram, updateSecurityProgram } = vi.hoisted(() => ({
  createSecurityProgram: vi.fn(),
  updateSecurityProgram: vi.fn(),
}));

vi.mock("@/lib/client", () => ({
  client: { createSecurityProgram, updateSecurityProgram },
}));

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

describe("SecurityProgramDialog", () => {
  it("creates an explicit verified scope snapshot", async () => {
    createSecurityProgram.mockResolvedValue({});
    render(
      <SecurityProgramDialog
        trigger={<button>New program</button>}
        onSaved={vi.fn()}
      />,
    );
    fireEvent.click(screen.getByRole("button", { name: "New program" }));

    expect(screen.getByText(/URL is provenance only and does not authorize network testing/i)).toBeTruthy();
    fireEvent.change(screen.getByLabelText(/^Name/), { target: { value: "acme-bounty" } });
    fireEvent.change(screen.getByLabelText(/^Provider/), { target: { value: "HackerOne" } });
    fireEvent.change(screen.getByLabelText(/^Display name/), {
      target: { value: "Acme public bug bounty" },
    });
    fireEvent.change(screen.getByLabelText(/^Program URL/), {
      target: { value: "https://hackerone.com/acme" },
    });
    const scopeInput = screen.getByLabelText(/^Scope policy snapshot/) as HTMLTextAreaElement;
    fireEvent.change(scopeInput, {
      target: { value: "In scope:\n- api.example.com\n\nOut of scope:\n- denial-of-service" },
    });
    fireEvent.change(screen.getByLabelText(/^Verified at/), {
      target: { value: "2026-03-01T12:00" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Create security program" }));

    await waitFor(() => expect(createSecurityProgram).toHaveBeenCalledTimes(1));
    const program = createSecurityProgram.mock.calls[0][0].program;
    expect(program).toMatchObject({
      name: "acme-bounty",
      provider: "HackerOne",
      displayName: "Acme public bug bounty",
      programUrl: "https://hackerone.com/acme",
      scopePolicy: "In scope:\n- api.example.com\n\nOut of scope:\n- denial-of-service",
      scanTargets: [],
    });
    expect(program.verifiedAt).toBeTruthy();
  });

  it("rejects a scope policy over the character limit", () => {
    render(
      <SecurityProgramDialog
        trigger={<button>New program</button>}
        onSaved={vi.fn()}
      />,
    );
    fireEvent.click(screen.getByRole("button", { name: "New program" }));
    fireEvent.change(screen.getByLabelText(/^Scope policy snapshot/), {
      target: { value: "界".repeat(131073) },
    });

    expect(screen.getByText(/at most 131,072 characters/i)).toBeTruthy();
    expect((screen.getByRole("button", { name: "Create security program" }) as HTMLButtonElement).disabled).toBe(true);
  });

  it("rejects a non-HTTPS provenance URL", () => {
    render(
      <SecurityProgramDialog
        trigger={<button>New program</button>}
        onSaved={vi.fn()}
      />,
    );
    fireEvent.click(screen.getByRole("button", { name: "New program" }));
    fireEvent.change(screen.getByLabelText(/^Program URL/), {
      target: { value: "http://hackerone.com/acme" },
    });

    expect(screen.getByText(/absolute HTTPS URL/i)).toBeTruthy();
    expect((screen.getByRole("button", { name: "Create security program" }) as HTMLButtonElement).disabled).toBe(true);
  });

  it("migrates a legacy target and adds another repository", async () => {
    const verifiedAt = timestampFromDate(new Date("2026-03-01T12:00:37Z"));
    const source = create(SecurityProgramResourceSchema, {
      namespace: "user-alice",
      name: "acme-bounty",
      provider: "HackerOne",
      displayName: "Acme public bug bounty",
      programUrl: "https://hackerone.com/acme",
      scopePolicy: "In scope: api.example.com",
      verifiedAt,
      scanTarget: {
        repositoryUrl: "https://github.com/acme/widget",
        workflowRef: "smart-contract-review",
        policyPackRef: "bug-bounty",
        scanName: "acme-widget",
        displayName: "Acme widget",
        priority: 1,
        featured: true,
      },
    });
    updateSecurityProgram.mockResolvedValue({});
    const view = render(
      <SecurityProgramDialog
        source={source}
        trigger={<button>Edit program</button>}
        onSaved={vi.fn()}
      />,
    );
    fireEvent.click(screen.getByRole("button", { name: "Edit program" }));

    expect((screen.getByLabelText(/^Name/) as HTMLInputElement).disabled).toBe(true);
    expect((screen.getByLabelText(/^Provider/) as HTMLInputElement).value).toBe("HackerOne");
    expect((screen.getByLabelText(/^Scope policy snapshot/) as HTMLTextAreaElement).value).toBe(
      "In scope: api.example.com",
    );
    expect((screen.getByLabelText(/^Repository URL/) as HTMLInputElement).value).toBe(
      "https://github.com/acme/widget",
    );
    expect((screen.getByLabelText(/^Default branch/) as HTMLInputElement).value).toBe("main");

    fireEvent.click(screen.getByRole("button", { name: "Add target" }));
    const repositoryInputs = screen.getAllByLabelText(/^Repository URL/);
    const displayNameInputs = screen.getAllByLabelText(/^Target display name/);
    const scanNameInputs = screen.getAllByLabelText(/^Scan name/);
    const workflowInputs = screen.getAllByLabelText(/^Workflow/);
    fireEvent.change(repositoryInputs[1], { target: { value: "https://github.com/acme/contracts" } });
    fireEvent.change(displayNameInputs[1], { target: { value: "Acme contracts" } });
    fireEvent.change(scanNameInputs[1], { target: { value: "acme-widget" } });
    fireEvent.change(workflowInputs[1], { target: { value: "smart-contract-review" } });

    expect(screen.getAllByText(/Scan names must be unique/i)).toHaveLength(2);
    expect((screen.getByRole("button", { name: "Save security program" }) as HTMLButtonElement).disabled).toBe(true);
    fireEvent.change(scanNameInputs[1], { target: { value: "acme-contracts" } });
    fireEvent.click(screen.getByRole("button", { name: "Save security program" }));

    await waitFor(() => expect(updateSecurityProgram).toHaveBeenCalledTimes(1));
    const program = updateSecurityProgram.mock.calls[0][0].program;
    expect(program.namespace).toBe("user-alice");
    expect(program.name).toBe("acme-bounty");
    expect(timestampDate(program.verifiedAt).toISOString()).toBe("2026-03-01T12:00:37.000Z");
    expect(program.scanTarget).toBeUndefined();
    expect(program.scanTargets).toHaveLength(2);
    expect(program.scanTargets[0]).toMatchObject({
      repositoryUrl: "https://github.com/acme/widget",
      baseBranch: "main",
      workflowRef: "smart-contract-review",
      policyPackRef: "bug-bounty",
      scanName: "acme-widget",
      displayName: "Acme widget",
      priority: 1,
      featured: true,
    });
    expect(program.scanTargets[1]).toMatchObject({
      repositoryUrl: "https://github.com/acme/contracts",
      baseBranch: "main",
      workflowRef: "smart-contract-review",
      policyPackRef: "bug-bounty",
      scanName: "acme-contracts",
      displayName: "Acme contracts",
      priority: 1,
      featured: false,
    });

    const refreshed = create(SecurityProgramResourceSchema, {
      ...source,
      displayName: "Acme updated bounty",
      scopePolicy: "Updated in-scope assets",
      generation: 2n,
    });
    view.rerender(
      <SecurityProgramDialog
        source={refreshed}
        trigger={<button>Edit program</button>}
        onSaved={vi.fn()}
      />,
    );
    fireEvent.click(screen.getByRole("button", { name: "Edit program" }));
    expect((screen.getByLabelText(/^Display name/) as HTMLInputElement).value).toBe("Acme updated bounty");
    expect((screen.getByLabelText(/^Scope policy snapshot/) as HTMLTextAreaElement).value).toBe("Updated in-scope assets");
  });
});

describe("SecurityProgramDialog typed scope", () => {
  function fillRequiredFields() {
    fireEvent.change(screen.getByLabelText(/^Name/), { target: { value: "acme-bounty" } });
    fireEvent.change(screen.getByLabelText(/^Provider/), { target: { value: "Immunefi" } });
    fireEvent.change(screen.getByLabelText(/^Display name/), {
      target: { value: "Acme public bug bounty" },
    });
    fireEvent.change(screen.getByLabelText(/^Program URL/), {
      target: { value: "https://immunefi.com/bounty/acme" },
    });
    fireEvent.change(screen.getByLabelText(/^Scope policy snapshot/), {
      target: { value: "In scope: contracts" },
    });
    fireEvent.change(screen.getByLabelText(/^Verified at/), {
      target: { value: "2026-03-01T12:00" },
    });
  }

  it("records the program's published scope facts", async () => {
    createSecurityProgram.mockResolvedValue({});
    render(
      <SecurityProgramDialog trigger={<button>New program</button>} onSaved={vi.fn()} />,
    );
    fireEvent.click(screen.getByRole("button", { name: "New program" }));
    fillRequiredFields();

    expect(screen.getByText(/may only claim an impact the program published/i)).toBeTruthy();
    fireEvent.change(screen.getByLabelText(/^Severity system/), { target: { value: "immunefi-v2.3" } });
    fireEvent.change(screen.getByLabelText(/^Primacy/), { target: { value: "impact" } });
    fireEvent.click(screen.getByRole("switch", { name: "PoC required" }));
    fireEvent.change(screen.getByLabelText(/^PoC environment/), { target: { value: "mainnet-fork" } });
    fireEvent.click(screen.getByRole("switch", { name: "KYC required" }));

    fireEvent.click(screen.getByRole("button", { name: "Add impact" }));
    fireEvent.change(screen.getByLabelText(/^Impact clause/), {
      target: { value: "Permanent freezing of funds" },
    });
    fireEvent.change(screen.getByLabelText(/^Program severity/), { target: { value: "critical" } });
    fireEvent.change(screen.getByLabelText(/^Asset category/), { target: { value: "Smart Contract" } });

    fireEvent.change(screen.getByLabelText(/^Out of scope/), {
      target: { value: "Attacks requiring leaked keys\n\nBest-practice recommendations" },
    });
    fireEvent.change(screen.getByLabelText(/^Prohibited testing/), {
      target: { value: "Testing on mainnet or public testnet" },
    });

    fireEvent.click(screen.getByRole("button", { name: "Add asset" }));
    fireEvent.change(screen.getByLabelText(/^Chain ID/), { target: { value: "1" } });
    fireEvent.change(screen.getByLabelText(/^Address/), { target: { value: "0xabc" } });
    fireEvent.change(screen.getByLabelText(/^Asset repository URL/), {
      target: { value: "https://github.com/acme/contracts" },
    });
    fireEvent.change(screen.getByLabelText(/^Asset name/), { target: { value: "Acme vault" } });
    fireEvent.change(screen.getByLabelText(/^Added on/), { target: { value: "2026-07-01" } });

    fireEvent.click(screen.getByRole("button", { name: "Add known issue" }));
    fireEvent.change(screen.getByLabelText(/^Source/), { target: { value: "prior audit" } });
    fireEvent.change(screen.getByLabelText(/^Summary/), { target: { value: "Acknowledged rounding" } });
    fireEvent.change(screen.getByLabelText(/^Reference/), {
      target: { value: "https://acme.example/audit.pdf" },
    });

    fireEvent.change(screen.getByLabelText(/^Reports per period/), { target: { value: "2" } });
    fireEvent.change(screen.getByLabelText(/^Period \(days\)/), { target: { value: "30" } });
    fireEvent.click(screen.getByRole("switch", { name: "Cap lifts with reputation" }));

    fireEvent.click(screen.getByRole("button", { name: "Create security program" }));

    await waitFor(() => expect(createSecurityProgram).toHaveBeenCalledTimes(1));
    const program = createSecurityProgram.mock.calls[0][0].program;
    expect(program).toMatchObject({
      severitySystem: "immunefi-v2.3",
      primacy: "impact",
      pocRequired: true,
      pocEnvironment: "mainnet-fork",
      kycRequired: true,
      outOfScope: ["Attacks requiring leaked keys", "Best-practice recommendations"],
      prohibitedTesting: ["Testing on mainnet or public testnet"],
    });
    expect(program.inScopeImpacts).toEqual([
      { $typeName: "platform.v1.SecurityProgramImpact", impact: "Permanent freezing of funds", level: "critical", assetType: "Smart Contract" },
    ]);
    expect(program.assets[0]).toMatchObject({
      chainId: "1",
      address: "0xabc",
      repositoryUrl: "https://github.com/acme/contracts",
      displayName: "Acme vault",
      addedOn: "2026-07-01",
    });
    expect(program.knownIssues[0]).toMatchObject({
      source: "prior audit",
      summary: "Acknowledged rounding",
      reference: "https://acme.example/audit.pdf",
    });
    expect(program.submissionBudget).toMatchObject({
      maxPerPeriod: 2,
      periodDays: 30,
      unrestrictedRequiresReputation: true,
    });
  });

  it("blocks an impact clause with no severity and a severity its system does not judge", () => {
    render(
      <SecurityProgramDialog trigger={<button>New program</button>} onSaved={vi.fn()} />,
    );
    fireEvent.click(screen.getByRole("button", { name: "New program" }));
    fillRequiredFields();
    fireEvent.click(screen.getByRole("button", { name: "Add impact" }));
    fireEvent.change(screen.getByLabelText(/^Impact clause/), {
      target: { value: "Permanent freezing of funds" },
    });

    expect(screen.getByText(/severity the program itself assigns/i)).toBeTruthy();
    expect((screen.getByRole("button", { name: "Create security program" }) as HTMLButtonElement).disabled).toBe(true);

    fireEvent.change(screen.getByLabelText(/^Severity system/), { target: { value: "sherlock" } });
    fireEvent.change(screen.getByLabelText(/^Program severity/), { target: { value: "low" } });
    expect(screen.getByText(/Sherlock judges only high and medium/i)).toBeTruthy();
    expect((screen.getByRole("button", { name: "Create security program" }) as HTMLButtonElement).disabled).toBe(true);

    fireEvent.change(screen.getByLabelText(/^Program severity/), { target: { value: "high" } });
    expect(screen.queryByText(/Sherlock judges only high and medium/i)).toBeNull();
    expect((screen.getByRole("button", { name: "Create security program" }) as HTMLButtonElement).disabled).toBe(false);
  });

  it("blocks an unidentifiable asset and a period without a report cap", () => {
    render(
      <SecurityProgramDialog trigger={<button>New program</button>} onSaved={vi.fn()} />,
    );
    fireEvent.click(screen.getByRole("button", { name: "New program" }));
    fillRequiredFields();

    fireEvent.click(screen.getByRole("button", { name: "Add asset" }));
    fireEvent.change(screen.getByLabelText(/^Asset name/), { target: { value: "Acme vault" } });
    expect(screen.getByText(/Identify the asset by chain ID and address/i)).toBeTruthy();
    expect((screen.getByRole("button", { name: "Create security program" }) as HTMLButtonElement).disabled).toBe(true);
    fireEvent.change(screen.getByLabelText(/^Address/), { target: { value: "0xabc" } });
    expect(screen.getByText(/Chain ID is required when an address is set/i)).toBeTruthy();
    fireEvent.change(screen.getByLabelText(/^Chain ID/), { target: { value: "1" } });
    expect((screen.getByRole("button", { name: "Create security program" }) as HTMLButtonElement).disabled).toBe(false);

    fireEvent.change(screen.getByLabelText(/^Period \(days\)/), { target: { value: "30" } });
    expect(screen.getByText(/A period length needs a report cap/i)).toBeTruthy();
    expect((screen.getByRole("button", { name: "Create security program" }) as HTMLButtonElement).disabled).toBe(true);
  });

  it("round-trips transcribed scope through the editor and clears what an operator removes", async () => {
    updateSecurityProgram.mockResolvedValue({});
    const source = create(SecurityProgramResourceSchema, {
      namespace: "user-alice",
      name: "acme-bounty",
      provider: "Immunefi",
      displayName: "Acme public bug bounty",
      programUrl: "https://immunefi.com/bounty/acme",
      scopePolicy: "In scope: contracts",
      verifiedAt: timestampFromDate(new Date("2026-03-01T12:00:37Z")),
      severitySystem: "immunefi-v2.3",
      primacy: "impact",
      pocRequired: true,
      pocEnvironment: "mainnet-fork",
      kycRequired: true,
      inScopeImpacts: [
        { impact: "Permanent freezing of funds", level: "critical", assetType: "Smart Contract" },
      ],
      outOfScope: ["Attacks requiring leaked keys"],
      prohibitedTesting: ["Testing on mainnet or public testnet"],
      assets: [{ chainId: "1", address: "0xabc", displayName: "Acme vault" }],
      knownIssues: [{ source: "prior audit", summary: "Acknowledged rounding" }],
      submissionBudget: { maxPerPeriod: 2, periodDays: 30, unrestrictedRequiresReputation: true },
    });
    render(
      <SecurityProgramDialog source={source} trigger={<button>Edit program</button>} onSaved={vi.fn()} />,
    );
    fireEvent.click(screen.getByRole("button", { name: "Edit program" }));

    expect((screen.getByLabelText(/^Severity system/) as HTMLSelectElement).value).toBe("immunefi-v2.3");
    expect((screen.getByLabelText(/^Primacy/) as HTMLSelectElement).value).toBe("impact");
    expect((screen.getByLabelText(/^PoC environment/) as HTMLSelectElement).value).toBe("mainnet-fork");
    expect(screen.getByRole("switch", { name: "PoC required" }).getAttribute("aria-checked")).toBe("true");
    expect(screen.getByRole("switch", { name: "KYC required" }).getAttribute("aria-checked")).toBe("true");
    expect((screen.getByLabelText(/^Impact clause/) as HTMLTextAreaElement).value).toBe("Permanent freezing of funds");
    expect((screen.getByLabelText(/^Program severity/) as HTMLSelectElement).value).toBe("critical");
    expect((screen.getByLabelText(/^Out of scope/) as HTMLTextAreaElement).value).toBe("Attacks requiring leaked keys");
    expect((screen.getByLabelText(/^Prohibited testing/) as HTMLTextAreaElement).value).toBe("Testing on mainnet or public testnet");
    expect((screen.getByLabelText(/^Chain ID/) as HTMLInputElement).value).toBe("1");
    expect((screen.getByLabelText(/^Source/) as HTMLInputElement).value).toBe("prior audit");
    expect((screen.getByLabelText(/^Reports per period/) as HTMLInputElement).value).toBe("2");

    fireEvent.click(screen.getByRole("button", { name: "Save security program" }));
    await waitFor(() => expect(updateSecurityProgram).toHaveBeenCalledTimes(1));
    const resaved = updateSecurityProgram.mock.calls[0][0].program;
    expect(resaved).toMatchObject({
      severitySystem: "immunefi-v2.3",
      primacy: "impact",
      pocRequired: true,
      pocEnvironment: "mainnet-fork",
      kycRequired: true,
      outOfScope: ["Attacks requiring leaked keys"],
      prohibitedTesting: ["Testing on mainnet or public testnet"],
    });
    expect(resaved.inScopeImpacts).toHaveLength(1);
    expect(resaved.assets).toHaveLength(1);
    expect(resaved.knownIssues).toHaveLength(1);
    expect(resaved.submissionBudget).toMatchObject({ maxPerPeriod: 2, periodDays: 30 });

    // Saving closes the editor; reopening re-reads the source resource.
    fireEvent.click(screen.getByRole("button", { name: "Edit program" }));
    fireEvent.click(screen.getByRole("button", { name: "Remove impact 1" }));
    fireEvent.click(screen.getByRole("button", { name: "Remove asset 1" }));
    fireEvent.click(screen.getByRole("button", { name: "Remove known issue 1" }));
    fireEvent.change(screen.getByLabelText(/^Out of scope/), { target: { value: "" } });
    fireEvent.change(screen.getByLabelText(/^Prohibited testing/), { target: { value: "" } });
    fireEvent.change(screen.getByLabelText(/^Reports per period/), { target: { value: "" } });
    fireEvent.change(screen.getByLabelText(/^Period \(days\)/), { target: { value: "" } });
    fireEvent.click(screen.getByRole("switch", { name: "Cap lifts with reputation" }));
    fireEvent.change(screen.getByLabelText(/^Severity system/), { target: { value: "" } });
    fireEvent.click(screen.getByRole("button", { name: "Save security program" }));

    await waitFor(() => expect(updateSecurityProgram).toHaveBeenCalledTimes(2));
    const cleared = updateSecurityProgram.mock.calls[1][0].program;
    expect(cleared.severitySystem).toBe("");
    expect(cleared.inScopeImpacts).toEqual([]);
    expect(cleared.outOfScope).toEqual([]);
    expect(cleared.prohibitedTesting).toEqual([]);
    expect(cleared.assets).toEqual([]);
    expect(cleared.knownIssues).toEqual([]);
    expect(cleared.submissionBudget).toBeUndefined();
  });
});
