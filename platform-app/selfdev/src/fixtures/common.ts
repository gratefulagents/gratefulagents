// Shared fixture building blocks (owner, metrics, catalog data) used by all
// scenarios so they stay consistent with each other.

import { create } from "@bufbuild/protobuf";
import {
  type ModeTemplate,
  ModeTemplateSchema,
  ProjectMetricsSchema,
  type ProjectMetrics,
  type ResourceOwner,
  ResourceOwnerSchema,
  SecurityCatalogInstallState,
  SecurityCatalogKind,
  type SecurityCatalog,
  SecurityCatalogSchema,
  type RuntimeImageOption,
  RuntimeImageOptionSchema,
} from "../../../frontend/src/rpc/platform/service_pb";
import type { ScenarioUser } from "../scenario";
import { unix, daysAgo } from "../time";

export const NAMESPACE = "demo";

export const USER: ScenarioUser = {
  id: "user-dana",
  username: "dana",
  email: "dana@example.com",
  name: "Dana Demo",
  // Empty picture keeps screenshots hermetic (no external image fetches).
  picture: "",
  role: "admin",
};

export const OWNER: ResourceOwner = create(ResourceOwnerSchema, {
  userId: USER.id,
  email: USER.email,
  name: USER.name,
  picture: USER.picture,
});

export const TEAMMATE: ResourceOwner = create(ResourceOwnerSchema, {
  userId: "user-riley",
  email: "riley@example.com",
  name: "Riley Rivera",
  picture: "",
});

export function securityCatalogFixture(): SecurityCatalog {
  return create(SecurityCatalogSchema, {
    revision: "selfdev-security-catalog-v1",
    ready: true,
    entries: [
      {
        resource: { kind: SecurityCatalogKind.PROGRAM, name: "open-ledger-bounty" },
        title: "Open Ledger bounty program",
        description: "Verified program scope with two importable repository targets.",
        ready: true,
        installState: SecurityCatalogInstallState.NOT_INSTALLED,
        dependencies: [
          { resource: { kind: SecurityCatalogKind.WORKFLOW, name: "smart-contract-review" }, required: true },
          { resource: { kind: SecurityCatalogKind.POLICY_PACK, name: "bug-bounty" }, required: true },
        ],
      },
      {
        resource: { kind: SecurityCatalogKind.WORKFLOW, name: "smart-contract-review" },
        title: "Smart contract review",
        description: "Parallel review workflow for authorization, accounting, and integration risks.",
        ready: true,
        installState: SecurityCatalogInstallState.UPDATE_AVAILABLE,
        dependencies: [
          { resource: { kind: SecurityCatalogKind.SKILL, name: "security-scan" }, required: true },
          { resource: { kind: SecurityCatalogKind.SKILL, name: "evm-low-level-and-deployment-review" }, required: true },
        ],
      },
      {
        resource: { kind: SecurityCatalogKind.POLICY_PACK, name: "bug-bounty" },
        title: "Bug bounty policy",
        description: "Submission-focused validation, eligibility, and report-quality defaults.",
        ready: true,
        installState: SecurityCatalogInstallState.NOT_INSTALLED,
        dependencies: [
          { resource: { kind: SecurityCatalogKind.RANKER, name: "bug-bounty-triage" }, required: true },
          { resource: { kind: SecurityCatalogKind.POST_SCRIPT, name: "validate-finding" }, required: true },
        ],
      },
      {
        resource: { kind: SecurityCatalogKind.SKILL, name: "security-scan" },
        title: "Security scan",
        description: "Core vulnerability discovery and evidence discipline.",
        ready: true,
        installState: SecurityCatalogInstallState.INSTALLED,
      },
      {
        resource: { kind: SecurityCatalogKind.SKILL, name: "evm-low-level-and-deployment-review" },
        title: "EVM deployment review",
        description: "Low-level EVM, proxy, signature, and deployment analysis.",
        ready: true,
        installState: SecurityCatalogInstallState.NOT_INSTALLED,
      },
      {
        resource: { kind: SecurityCatalogKind.RANKER, name: "bug-bounty-triage" },
        title: "Bug bounty triage",
        description: "Ranks findings by accepted impact and submission readiness.",
        ready: true,
        installState: SecurityCatalogInstallState.NOT_INSTALLED,
      },
      {
        resource: { kind: SecurityCatalogKind.POST_SCRIPT, name: "validate-finding" },
        title: "Validate finding",
        description: "Reproduces candidate findings and rejects false positives.",
        ready: true,
        installState: SecurityCatalogInstallState.NOT_INSTALLED,
      },
    ],
  });
}

export function metrics(overrides: Partial<ProjectMetrics> = {}): ProjectMetrics {
  return create(ProjectMetricsSchema, {
    totalRuns: 42,
    successfulRuns: 35,
    failedRuns: 4,
    runningRuns: 3,
    totalCostUsd: 118.42,
    averageCostPerRun: 2.82,
    totalInputTokens: 48_211_034n,
    totalOutputTokens: 1_922_410n,
    totalToolCalls: 1_204,
    lastRunAtUnix: unix(daysAgo(0.04)),
    ...overrides,
  });
}

export function runtimeImageCatalog(): RuntimeImageOption[] {
  return [
    create(RuntimeImageOptionSchema, {
      id: "default",
      label: "Default (multi-language)",
      description: "The gratefulagents batteries-included worker image (Go, Node, Python, Elixir, …)",
      isDefault: true,
      versions: [{ version: "latest", image: "", isDefault: true }],
    }),
    create(RuntimeImageOptionSchema, {
      id: "node",
      label: "Node.js",
      description: "Official Node.js image (Debian)",
      versions: [
        { version: "24", image: "docker.io/library/node:24", isDefault: true },
        { version: "22", image: "docker.io/library/node:22" },
      ],
    }),
    create(RuntimeImageOptionSchema, {
      id: "go",
      label: "Go",
      description: "Official Go image (Debian)",
      versions: [
        { version: "1.26", image: "docker.io/library/golang:1.26", isDefault: true },
        { version: "1.25", image: "docker.io/library/golang:1.25" },
      ],
    }),
    create(RuntimeImageOptionSchema, {
      id: "python",
      label: "Python",
      description: "Official Python image (Debian)",
      versions: [{ version: "3.14", image: "docker.io/library/python:3.14", isDefault: true }],
    }),
  ];
}

export function modeCatalog(): ModeTemplate[] {
  return [
    create(ModeTemplateSchema, {
      name: "chat",
      version: "1",
      displayName: "Chat",
      description: "Interactive pair-programming chat session.",
      category: "direct",
      executionStrategy: "serial",
    }),
    create(ModeTemplateSchema, {
      name: "autopilot",
      version: "1",
      displayName: "Autopilot",
      description: "Autonomous end-to-end execution with PR delivery.",
      category: "direct",
      executionStrategy: "serial",
    }),
    create(ModeTemplateSchema, {
      name: "plan",
      version: "1",
      displayName: "Plan",
      description: "Read-only investigation that produces an implementation plan.",
      category: "direct",
      executionStrategy: "serial",
    }),
    create(ModeTemplateSchema, {
      name: "team",
      version: "1",
      displayName: "Team",
      description: "Orchestrated multi-agent delivery lanes.",
      category: "orchestrated",
      executionStrategy: "parallel",
    }),
  ];
}

export const MODEL_LIST = {
  provider: "anthropic",
  baseUrl: "https://api.anthropic.com",
  models: [
    "claude-opus-4-6",
    "claude-sonnet-4-6",
    "claude-haiku-4-5",
  ],
};
