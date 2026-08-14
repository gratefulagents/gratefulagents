import { describe, expect, it } from "vitest";

import { importableImmunefiTargets } from "@/lib/immunefiTargetCatalog";
import type { SecurityProgramResource } from "@/rpc/platform/service_pb";

function program(
  name: string,
  scanTarget: Omit<NonNullable<SecurityProgramResource["scanTarget"]>, "$typeName">,
): SecurityProgramResource {
  return { name, scanTarget: scanTarget as SecurityProgramResource["scanTarget"] } as SecurityProgramResource;
}

describe("importableImmunefiTargets", () => {
  it("maps featured and non-featured scan targets and sorts by priority then display name", () => {
    const targets = importableImmunefiTargets([
      program("program-zebra", {
        featured: true,
        priority: 20,
        displayName: "Zebra",
        scanName: "scan-zebra",
        repositoryUrl: "https://example.com/zebra",
        baseBranch: "main",
        workflowRef: "workflow-zebra",
        policyPackRef: "policy-zebra",
      }),
      program("program-hidden", {
        featured: false,
        priority: 0,
        displayName: "Hidden",
        scanName: "scan-hidden",
        repositoryUrl: "https://example.com/hidden",
        baseBranch: "main",
        workflowRef: "workflow-hidden",
        policyPackRef: "policy-hidden",
      }),
      program("program-alpha", {
        featured: true,
        priority: 20,
        displayName: "Alpha",
        scanName: "scan-alpha",
        repositoryUrl: "https://example.com/alpha",
        baseBranch: "develop",
        workflowRef: "workflow-alpha",
        policyPackRef: "policy-alpha",
      }),
      program("program-first", {
        featured: true,
        priority: 5,
        displayName: "First",
        scanName: "scan-first",
        repositoryUrl: "https://example.com/first",
        baseBranch: "master",
        workflowRef: "workflow-first",
        policyPackRef: "policy-first",
      }),
    ]);

    expect(targets).toEqual([
      {
        name: "scan-hidden",
        displayName: "Hidden",
        repoUrl: "https://example.com/hidden",
        baseBranch: "main",
        workflowRef: "workflow-hidden",
        policyPackRef: "policy-hidden",
        securityProgramRef: "program-hidden",
        priority: 0,
      },
      {
        name: "scan-first",
        displayName: "First",
        repoUrl: "https://example.com/first",
        baseBranch: "master",
        workflowRef: "workflow-first",
        policyPackRef: "policy-first",
        securityProgramRef: "program-first",
        priority: 5,
      },
      {
        name: "scan-alpha",
        displayName: "Alpha",
        repoUrl: "https://example.com/alpha",
        baseBranch: "develop",
        workflowRef: "workflow-alpha",
        policyPackRef: "policy-alpha",
        securityProgramRef: "program-alpha",
        priority: 20,
      },
      {
        name: "scan-zebra",
        displayName: "Zebra",
        repoUrl: "https://example.com/zebra",
        baseBranch: "main",
        workflowRef: "workflow-zebra",
        policyPackRef: "policy-zebra",
        securityProgramRef: "program-zebra",
        priority: 20,
      },
    ]);
  });

  it("excludes programs without scan-target metadata", () => {
    const unavailable = { name: "program-unavailable" } as SecurityProgramResource;

    expect(importableImmunefiTargets([unavailable])).toEqual([]);
  });
});
