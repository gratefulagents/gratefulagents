import { create } from "@bufbuild/protobuf";
import { describe, expect, it } from "vitest";

import { importableProgramTargets } from "@/lib/programTargetCatalog";
import {
  SecurityProgramResourceSchema,
  SecurityProgramScanTargetSchema,
  type SecurityProgramResource,
  type SecurityProgramScanTarget,
} from "@/rpc/platform/service_pb";

type TargetInput = Omit<SecurityProgramScanTarget, "$typeName" | "parameterValues" | "targetUrl"> & {
  parameterValues?: Record<string, string>;
  targetUrl?: string;
};

function target(input: TargetInput): SecurityProgramScanTarget {
  return create(SecurityProgramScanTargetSchema, input);
}

function program(name: string, scanTargets: TargetInput[]): SecurityProgramResource {
  return create(SecurityProgramResourceSchema, {
    name,
    scanTargets: scanTargets.map(target),
  });
}

describe("importableProgramTargets", () => {
  it("flattens every repository target and sorts by priority then display name", () => {
    const targets = importableProgramTargets([
      program("program-multi", [
        {
          featured: true,
          priority: 20,
          displayName: "Zebra",
          scanName: "scan-zebra",
          repositoryUrl: "https://example.com/zebra",
          baseBranch: "main",
          workflowRef: "workflow-zebra",
          policyPackRef: "policy-zebra",
        },
        {
          featured: true,
          priority: 20,
          displayName: "Alpha",
          scanName: "scan-alpha",
          repositoryUrl: "https://example.com/alpha",
          baseBranch: "develop",
          workflowRef: "workflow-alpha",
          policyPackRef: "policy-alpha",
          parameterValues: { project_root: "contracts" },
        },
      ]),
      program("program-hidden", [{
        featured: false,
        priority: 0,
        displayName: "Hidden",
        scanName: "scan-hidden",
        repositoryUrl: "https://example.com/hidden",
        baseBranch: "main",
        workflowRef: "workflow-hidden",
        policyPackRef: "policy-hidden",
      }]),
      program("program-first", [{
        featured: true,
        priority: 5,
        displayName: "First",
        scanName: "scan-first",
        repositoryUrl: "https://example.com/first",
        baseBranch: "master",
        workflowRef: "workflow-first",
        policyPackRef: "policy-first",
      }]),
    ]);

    expect(targets).toEqual([
      {
        name: "scan-hidden",
        displayName: "Hidden",
        repoUrl: "https://example.com/hidden",
        targetUrl: "",
        baseBranch: "main",
        workflowRef: "workflow-hidden",
        policyPackRef: "policy-hidden",
        securityProgramRef: "program-hidden",
        priority: 0,
        parameterValues: {},
      },
      {
        name: "scan-first",
        displayName: "First",
        repoUrl: "https://example.com/first",
        targetUrl: "",
        baseBranch: "master",
        workflowRef: "workflow-first",
        policyPackRef: "policy-first",
        securityProgramRef: "program-first",
        priority: 5,
        parameterValues: {},
      },
      {
        name: "scan-alpha",
        displayName: "Alpha",
        repoUrl: "https://example.com/alpha",
        targetUrl: "",
        baseBranch: "develop",
        workflowRef: "workflow-alpha",
        policyPackRef: "policy-alpha",
        securityProgramRef: "program-multi",
        priority: 20,
        parameterValues: { project_root: "contracts" },
      },
      {
        name: "scan-zebra",
        displayName: "Zebra",
        repoUrl: "https://example.com/zebra",
        targetUrl: "",
        baseBranch: "main",
        workflowRef: "workflow-zebra",
        policyPackRef: "policy-zebra",
        securityProgramRef: "program-multi",
        priority: 20,
        parameterValues: {},
      },
    ]);
  });

  it("maps every website URL to an independent scan target", () => {
    const targets = importableProgramTargets([
      program("web-program", [
        {
          featured: true, priority: 1, displayName: "Web", scanName: "web",
          targetUrl: "https://app.example.com", repositoryUrl: "", baseBranch: "",
          workflowRef: "web-app-full-assessment", policyPackRef: "web-application",
        },
        {
          featured: true, priority: 2, displayName: "API", scanName: "api",
          targetUrl: "https://api.example.com", repositoryUrl: "", baseBranch: "",
          workflowRef: "web-api-assessment", policyPackRef: "web-application",
        },
      ]),
    ]);

    expect(targets).toHaveLength(2);
    expect(targets[0]).toMatchObject({ name: "web", targetUrl: "https://app.example.com", repoUrl: "", baseBranch: "" });
    expect(targets[1]).toMatchObject({ name: "api", targetUrl: "https://api.example.com", repoUrl: "", baseBranch: "" });
  });

  it("imports the deprecated single target and defaults its branch", () => {
    const legacy = create(SecurityProgramResourceSchema, {
      name: "legacy-program",
      scanTarget: target({
        featured: true,
        priority: 1,
        displayName: "Legacy",
        scanName: "legacy-scan",
        repositoryUrl: "https://example.com/legacy",
        baseBranch: "",
        workflowRef: "legacy-workflow",
        policyPackRef: "legacy-policy",
      }),
    });

    expect(importableProgramTargets([legacy])).toEqual([
      expect.objectContaining({ name: "legacy-scan", baseBranch: "main" }),
    ]);
  });

  it("excludes programs without scan-target metadata", () => {
    const unavailable = create(SecurityProgramResourceSchema, { name: "program-unavailable" });

    expect(importableProgramTargets([unavailable])).toEqual([]);
  });
});
