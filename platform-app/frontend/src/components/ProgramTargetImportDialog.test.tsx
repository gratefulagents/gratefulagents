import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";

import { ProgramTargetImportDialog } from "@/components/ProgramTargetImportDialog";
import type { SecurityProgramResource } from "@/rpc/platform/service_pb";

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

type TargetInput = Omit<SecurityProgramResource["scanTargets"][number], "$typeName">;

function legacyProgram(
  name: string,
  scanTarget: TargetInput,
): SecurityProgramResource {
  return { name, scanTarget: scanTarget as SecurityProgramResource["scanTarget"] } as SecurityProgramResource;
}

function program(name: string, scanTargets: TargetInput[]): SecurityProgramResource {
  return { name, scanTargets } as SecurityProgramResource;
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
  existingNames = new Set<string>(),
  availablePrograms: readonly SecurityProgramResource[] = programs,
) {
  render(
    <ProgramTargetImportDialog
      programs={availablePrograms}
      existingNames={existingNames}
      trigger={<button>Import scan target</button>}
      onTargetSelected={onTargetSelected}
    />,
  );
  return onTargetSelected;
}

describe("ProgramTargetImportDialog", () => {
  it("previews all importable targets in metadata order without creating anything", () => {
    renderDialog();
    fireEvent.click(screen.getByRole("button", { name: "Import scan target" }));

    expect(screen.getByText(/workspace-write access and unrestricted network egress/)).toBeTruthy();
    expect(screen.getByText(/Nothing is created or run/)).toBeTruthy();
    const items = screen.getAllByRole("listitem");
    expect(items).toHaveLength(3);
    expect(items[0].textContent).toContain("Hidden target");
    expect(items[1].textContent).toContain("First target");
    expect(items[1].textContent).toContain("https://example.com/first · main");
    expect(items[2].textContent).toContain("Later target");
  });

  it("selects one target for configuration and closes the chooser", () => {
    const onTargetSelected = renderDialog();
    fireEvent.click(screen.getByRole("button", { name: "Import scan target" }));
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

  it("does not allow selecting an existing configuration", () => {
    const onTargetSelected = renderDialog(vi.fn(), new Set(["scan-first"]));
    fireEvent.click(screen.getByRole("button", { name: "Import scan target" }));

    expect(screen.getByText("Existing configuration")).toBeTruthy();
    const button = screen.getByRole("button", {
      name: "Configure scan for First target",
    }) as HTMLButtonElement;
    expect(button.disabled).toBe(true);
    fireEvent.click(button);
    expect(onTargetSelected).not.toHaveBeenCalled();
  });

  it("communicates when no importable targets are available", () => {
    renderDialog(vi.fn(), new Set(), []);
    fireEvent.click(screen.getByRole("button", { name: "Import scan target" }));
    expect(screen.getByText("No importable scan targets are available.")).toBeTruthy();
  });
});
