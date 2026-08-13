import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";

import { ImmunefiTargetImportDialog } from "@/components/ImmunefiTargetImportDialog";
import type { SecurityProgramResource } from "@/rpc/platform/service_pb";

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
    workflowRef: "workflow-later",
    policyPackRef: "policy-later",
  }),
  program("program-first", {
    featured: true,
    priority: 10,
    displayName: "First target",
    scanName: "scan-first",
    repositoryUrl: "https://example.com/first",
    workflowRef: "workflow-first",
    policyPackRef: "policy-first",
  }),
  program("program-hidden", {
    featured: false,
    priority: 0,
    displayName: "Hidden target",
    scanName: "scan-hidden",
    repositoryUrl: "https://example.com/hidden",
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
    <ImmunefiTargetImportDialog
      programs={availablePrograms}
      existingNames={existingNames}
      trigger={<button>Add Immunefi scan</button>}
      onTargetSelected={onTargetSelected}
    />,
  );
  return onTargetSelected;
}

describe("ImmunefiTargetImportDialog", () => {
  it("previews only featured targets in metadata order without creating anything", () => {
    renderDialog();
    fireEvent.click(screen.getByRole("button", { name: "Add Immunefi scan" }));

    expect(screen.getByText(/Nothing is created or run/)).toBeTruthy();
    const items = screen.getAllByRole("listitem");
    expect(items).toHaveLength(2);
    expect(items[0].textContent).toContain("First target");
    expect(items[1].textContent).toContain("Later target");
    expect(screen.queryByText("Hidden target")).toBeNull();
  });

  it("selects one target for configuration and closes the chooser", () => {
    const onTargetSelected = renderDialog();
    fireEvent.click(screen.getByRole("button", { name: "Add Immunefi scan" }));
    fireEvent.click(screen.getByRole("button", { name: "Configure scan for First target" }));

    expect(onTargetSelected).toHaveBeenCalledTimes(1);
    expect(onTargetSelected).toHaveBeenCalledWith(expect.objectContaining({
      name: "scan-first",
      repoUrl: "https://example.com/first",
      workflowRef: "workflow-first",
      policyPackRef: "policy-first",
      securityProgramRef: "program-first",
    }));
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("does not allow selecting an existing configuration", () => {
    const onTargetSelected = renderDialog(vi.fn(), new Set(["scan-first"]));
    fireEvent.click(screen.getByRole("button", { name: "Add Immunefi scan" }));

    expect(screen.getByText("Existing configuration")).toBeTruthy();
    const buttons = screen.getAllByRole("button", { name: /Configure scan for/ }) as HTMLButtonElement[];
    expect(buttons[0].disabled).toBe(true);
    fireEvent.click(buttons[0]);
    expect(onTargetSelected).not.toHaveBeenCalled();
  });

  it("communicates when no featured targets are available", () => {
    renderDialog(vi.fn(), new Set(), []);
    fireEvent.click(screen.getByRole("button", { name: "Add Immunefi scan" }));
    expect(screen.getByText("No featured Immunefi targets are available.")).toBeTruthy();
  });
});
