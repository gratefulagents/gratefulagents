import { create } from "@bufbuild/protobuf";
import { Code, ConnectError } from "@connectrpc/connect";
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";

import { SecurityCatalogDialog } from "@/components/SecurityCatalogDialog";
import {
  SecurityCatalogInstallResponseSchema,
  SecurityCatalogInstallState,
  SecurityCatalogKind,
  SecurityCatalogSchema,
  SecurityCatalogEntrySchema,
} from "@/rpc/platform/service_pb";

const { listSecurityCatalog, dryRunSecurityCatalogInstall, applySecurityCatalogInstall } = vi.hoisted(() => ({
  listSecurityCatalog: vi.fn(),
  dryRunSecurityCatalogInstall: vi.fn(),
  applySecurityCatalogInstall: vi.fn(),
}));

vi.mock("@/lib/client", () => ({
  client: { listSecurityCatalog, dryRunSecurityCatalogInstall, applySecurityCatalogInstall },
}));

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

function catalogEntry(
  kind: SecurityCatalogKind,
  name: string,
  title: string,
  overrides: {
    dependencies?: { resource?: { kind: SecurityCatalogKind; name: string }; required: boolean }[];
    ready?: boolean;
    readinessMessage?: string;
    installState?: SecurityCatalogInstallState;
  } = {},
) {
  return create(SecurityCatalogEntrySchema, {
    resource: { kind, name },
    title,
    description: `${title} description`,
    ready: true,
    installState: SecurityCatalogInstallState.NOT_INSTALLED,
    ...overrides,
  });
}

function catalogFixture() {
  return create(SecurityCatalogSchema, {
    revision: "revision-7",
    ready: true,
    entries: [
      catalogEntry(SecurityCatalogKind.SKILL, "web-skill", "Web review skill"),
      catalogEntry(SecurityCatalogKind.WORKFLOW, "web-review", "Web review workflow", {
        dependencies: [{ resource: { kind: SecurityCatalogKind.SKILL, name: "web-skill" }, required: true }],
      }),
      catalogEntry(SecurityCatalogKind.RANKER, "severity", "Severity ranker", {
        installState: SecurityCatalogInstallState.INSTALLED,
      }),
      catalogEntry(SecurityCatalogKind.POST_SCRIPT, "verify", "Verification post-script"),
      catalogEntry(SecurityCatalogKind.POLICY_PACK, "bounty", "Bounty policy pack", {
        ready: false,
        readinessMessage: "policy source is not ready",
      }),
      catalogEntry(SecurityCatalogKind.PROGRAM, "acme", "Acme program", {
        installState: SecurityCatalogInstallState.CONFLICT,
      }),
    ],
  });
}

function renderDialog(onInstalled = vi.fn()) {
  render(
    <MemoryRouter>
      <SecurityCatalogDialog onInstalled={onInstalled} />
    </MemoryRouter>,
  );
  fireEvent.click(screen.getByRole("button", { name: "Add from shipped catalog" }));
  return onInstalled;
}

describe("SecurityCatalogDialog", () => {
  it("loads all server-provided kinds and filters searchable install/readiness details", async () => {
    listSecurityCatalog.mockResolvedValue(catalogFixture());
    renderDialog();

    expect(await screen.findByText("Web review skill")).toBeTruthy();
    expect(listSecurityCatalog).toHaveBeenCalledWith({});
    expect(screen.getByText("Web review workflow")).toBeTruthy();
    expect(screen.getByText("Severity ranker")).toBeTruthy();
    expect(screen.getByText("Verification post-script")).toBeTruthy();
    expect(screen.getByText("Bounty policy pack")).toBeTruthy();
    expect(screen.getByText("Acme program")).toBeTruthy();
    expect(screen.getByText("Installed")).toBeTruthy();
    expect(screen.getByText("Conflict")).toBeTruthy();
    expect(screen.getByText("policy source is not ready")).toBeTruthy();
    expect(screen.getByText(/Depends on Skills \/ web-skill \(required\)/)).toBeTruthy();
    expect(screen.getByRole("link", { name: "Settings" }).getAttribute("href")).toBe("/settings/skills");
    expect(screen.getByRole("link", { name: "Configurations" }).getAttribute("href")).toBe("/security/configs");

    const options = screen.getAllByRole("option").map((option) => option.textContent);
    expect(options).toEqual(["All kinds", "Skills", "Workflows", "Rankers", "Post-scripts", "Policy packs", "Programs"]);

    fireEvent.change(screen.getByLabelText("Catalog kind"), {
      target: { value: String(SecurityCatalogKind.PROGRAM) },
    });
    expect(screen.getByText("Acme program")).toBeTruthy();
    expect(screen.queryByText("Web review workflow")).toBeNull();

    fireEvent.change(screen.getByLabelText("Search catalog"), { target: { value: "missing" } });
    expect(screen.getByText("No catalog items match this search and kind.")).toBeTruthy();
  });

  it("applies independent items from a blocked plan and reports the partial outcome", async () => {
    listSecurityCatalog.mockResolvedValue(catalogFixture());
    const results = [
      { entry: catalogEntry(SecurityCatalogKind.SKILL, "web-skill", "Web review skill"), action: "create" },
      { entry: catalogEntry(SecurityCatalogKind.WORKFLOW, "web-review", "Web review workflow"), action: "create" },
      {
        entry: catalogEntry(SecurityCatalogKind.PROGRAM, "acme", "Acme program", {
          installState: SecurityCatalogInstallState.CONFLICT,
        }),
        action: "blocked",
        message: "an unrelated same-name resource already exists",
      },
    ];
    dryRunSecurityCatalogInstall.mockResolvedValue(create(SecurityCatalogInstallResponseSchema, {
      catalogRevision: "revision-7",
      planRevision: "plan-7",
      results,
    }));
    applySecurityCatalogInstall.mockResolvedValue(create(SecurityCatalogInstallResponseSchema, {
      catalogRevision: "revision-7",
      applied: true,
      results: [
        { ...results[0], action: "created" },
        { ...results[1], action: "created" },
        results[2],
      ],
    }));
    renderDialog();

    fireEvent.click(await screen.findByRole("checkbox", { name: "Select Programs Acme program" }));
    fireEvent.click(screen.getByRole("button", { name: "Review selection (1)" }));

    expect(await screen.findByText("Dependency-expanded installation plan")).toBeTruthy();
    expect(dryRunSecurityCatalogInstall).toHaveBeenCalledWith({
      catalogRevision: "revision-7",
      resources: [{ kind: SecurityCatalogKind.PROGRAM, name: "acme" }],
    });
    const plan = screen.getByRole("list", { name: "Catalog installation plan" });
    expect(plan.textContent).toContain("Web review skill");
    expect(plan.textContent).toContain("Web review workflow");
    expect(plan.textContent).toContain("an unrelated same-name resource already exists");
    expect(screen.getByText(/Blocked items will be skipped/)).toBeTruthy();
    const apply = screen.getByRole("button", { name: "Apply plan" });
    expect((apply as HTMLButtonElement).disabled).toBe(false);
    fireEvent.click(apply);

    await waitFor(() => expect(applySecurityCatalogInstall).toHaveBeenCalledWith({
      catalogRevision: "revision-7",
      resources: [{ kind: SecurityCatalogKind.PROGRAM, name: "acme" }],
      planRevision: "plan-7",
    }));
    expect(await screen.findByText("Installation completed with issues: 1 blocked. 2 other items were applied.")).toBeTruthy();
    expect(screen.queryByText(/Applied 2 catalog items\. The library/)).toBeNull();
  });

  it("applies only after review, then refreshes both the library and catalog", async () => {
    const catalog = catalogFixture();
    listSecurityCatalog.mockResolvedValue(catalog);
    dryRunSecurityCatalogInstall.mockResolvedValue(create(SecurityCatalogInstallResponseSchema, {
      catalogRevision: "revision-7",
      planRevision: "plan-7",
      results: [{ entry: catalogEntry(SecurityCatalogKind.WORKFLOW, "web-review", "Web review workflow"), action: "create" }],
    }));
    applySecurityCatalogInstall.mockResolvedValue(create(SecurityCatalogInstallResponseSchema, {
      catalogRevision: "revision-7",
      applied: true,
      results: [{ entry: catalogEntry(SecurityCatalogKind.WORKFLOW, "web-review", "Web review workflow"), action: "created" }],
    }));
    const onInstalled = renderDialog(vi.fn().mockResolvedValue(undefined));

    fireEvent.click(await screen.findByRole("checkbox", { name: "Select Workflows Web review workflow" }));
    expect(applySecurityCatalogInstall).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: "Review selection (1)" }));
    fireEvent.click(await screen.findByRole("button", { name: "Apply plan" }));

    await waitFor(() => expect(applySecurityCatalogInstall).toHaveBeenCalledWith({
      catalogRevision: "revision-7",
      resources: [{ kind: SecurityCatalogKind.WORKFLOW, name: "web-review" }],
      planRevision: "plan-7",
    }));
    await waitFor(() => expect(onInstalled).toHaveBeenCalledTimes(1));
    await waitFor(() => expect(listSecurityCatalog).toHaveBeenCalledTimes(2));
    expect(await screen.findByText(/Applied 1 catalog item\. The library and catalog are now refreshed\./)).toBeTruthy();
  });

  it("allows an unchanged plan to claim ownership and reports item failures honestly", async () => {
    const catalog = catalogFixture();
    listSecurityCatalog.mockResolvedValue(catalog);
    dryRunSecurityCatalogInstall.mockResolvedValue(create(SecurityCatalogInstallResponseSchema, {
      catalogRevision: "revision-7",
      planRevision: "plan-7",
      results: [{
        entry: catalogEntry(SecurityCatalogKind.SKILL, "web-skill", "Web review skill", {
          installState: SecurityCatalogInstallState.INSTALLED,
        }),
        action: "unchanged",
      }],
    }));
    applySecurityCatalogInstall.mockResolvedValue(create(SecurityCatalogInstallResponseSchema, {
      catalogRevision: "revision-7",
      planRevision: "plan-7",
      results: [{
        entry: catalogEntry(SecurityCatalogKind.SKILL, "web-skill", "Web review skill"),
        action: "failed",
        message: "ownership store unavailable",
      }],
    }));
    renderDialog();

    fireEvent.click(await screen.findByRole("checkbox", { name: "Select Skills Web review skill" }));
    fireEvent.click(screen.getByRole("button", { name: "Review selection (1)" }));

    const apply = await screen.findByRole("button", { name: "Apply plan" });
    expect((apply as HTMLButtonElement).disabled).toBe(false);
    fireEvent.click(apply);

    expect(await screen.findByText("Installation completed with issues: 1 failed. No catalog changes were applied.")).toBeTruthy();
    expect(screen.queryByText(/Applied 1 catalog item/)).toBeNull();
  });

  it("shows unavailable and stale catalog recovery states", async () => {
    listSecurityCatalog.mockRejectedValueOnce(new Error("catalog service offline"));
    listSecurityCatalog.mockResolvedValue(catalogFixture());
    renderDialog();

    expect(await screen.findByText("Catalog unavailable")).toBeTruthy();
    expect(screen.getByText(/catalog service offline/)).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    fireEvent.click(await screen.findByRole("checkbox", { name: "Select Skills Web review skill" }));

    dryRunSecurityCatalogInstall.mockRejectedValue(
      new ConnectError("security catalog revision is stale; refresh the catalog and try again", Code.FailedPrecondition),
    );
    fireEvent.click(screen.getByRole("button", { name: "Review selection (1)" }));

    expect(await screen.findByText(/catalog changed while you were reviewing it/i)).toBeTruthy();
    expect(screen.getByRole("button", { name: "Refresh catalog" })).toBeTruthy();
    expect(applySecurityCatalogInstall).not.toHaveBeenCalled();
  });
});
