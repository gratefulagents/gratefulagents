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
