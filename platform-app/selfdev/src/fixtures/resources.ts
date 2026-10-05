// Reusable resource fixtures for the Resources page (MCP servers, runtime
// profiles, guardrail policies, role instructions, and the skills.sh catalog).
// Built from the generated proto schemas so they break loudly when the API
// contract changes.

import { create } from "@bufbuild/protobuf";
import {
  GuardrailPolicySchema,
  GuardrailRuleSchema,
  MCPServerInfoSchema,
  MCPServerSecretEnvSchema,
  RoleInstructionSchema,
  RuntimeProfileSchema,
  SkillCatalogEntrySchema,
  SkillInfoSchema,
} from "../../../frontend/src/rpc/platform/service_pb";
import { NAMESPACE as NS } from "./common";

export function mcpServerCatalog() {
  return [
    create(MCPServerInfoSchema, {
      name: "duckduckgo",
      version: "0.4.1",
      description: "Web search and page fetch tools backed by DuckDuckGo.",
      command: "uvx",
      args: ["duckduckgo-mcp-server"],
      trustReadOnlyHint: true,
      allowNetwork: true,
    }),
    create(MCPServerInfoSchema, {
      name: "grafana",
      version: "0.17.2",
      description: "Query dashboards, alerts, and Loki logs from the ops Grafana.",
      command: "uvx",
      args: ["mcp-grafana==0.17.2", "--disable-oncall"],
      env: { GRAFANA_URL: "https://grafana.internal.example.com" },
      allowEnv: ["GRAFANA_SERVICE_ACCOUNT_TOKEN"],
      secretEnv: [
        create(MCPServerSecretEnvSchema, {
          name: "GRAFANA_SERVICE_ACCOUNT_TOKEN",
          secretName: "usercred-grafana",
          secretKey: "token",
          required: true,
        }),
      ],
      trustReadOnlyHint: true,
      allowNetwork: true,
    }),
    create(MCPServerInfoSchema, {
      name: "postgres-readonly",
      description: "Read-only SQL against the analytics replica.",
      command: "npx",
      args: ["-y", "@modelcontextprotocol/server-postgres"],
      secretEnv: [
        create(MCPServerSecretEnvSchema, { name: "DATABASE_URL", secretName: "usercred-analytics-db", secretKey: "url" }),
      ],
      trustReadOnlyHint: false,
      allowNetwork: false,
    }),
  ];
}

export function runtimeProfileCatalog() {
  return [
    create(RuntimeProfileSchema, {
      namespace: NS,
      name: "default",
      permissionMode: "workspace-write",
      gitRemoteWrites: "enabled",
      egressMode: "restricted",
      defaultTimeout: "2h0m0s",
      resourceRequests: { cpu: "500m", memory: "2Gi" },
      resourceLimits: { cpu: "2", memory: "6Gi" },
    }),
    create(RuntimeProfileSchema, {
      namespace: NS,
      name: "browser-testing",
      permissionMode: "workspace-write",
      gitRemoteWrites: "disabled",
      egressMode: "unrestricted",
      defaultTimeout: "1h0m0s",
      persistWorkspace: true,
      workspaceSize: "20Gi",
      enablePrivateProcfs: true,
      runtimeClassName: "gvisor",
      commandPathAppend: ["/opt/playwright/bin"],
      extraWritablePaths: ["/cache/ms-playwright"],
      commandEnv: { PLAYWRIGHT_BROWSERS_PATH: "/cache/ms-playwright" },
      maxConcurrentRuns: 4,
      perNamespaceMaxConcurrentRuns: 2,
      staleRunTimeout: "30m0s",
    }),
    create(RuntimeProfileSchema, {
      namespace: NS,
      name: "review-only",
      permissionMode: "read-only",
      gitRemoteWrites: "disabled",
      egressMode: "disabled",
      defaultTimeout: "30m0s",
    }),
  ];
}

export function guardrailCatalog() {
  return [
    create(GuardrailPolicySchema, {
      namespace: NS,
      name: "no-secrets-in-output",
      rules: [
        create(GuardrailRuleSchema, {
          name: "aws-access-keys",
          type: "tool-output",
          toolPattern: "*",
          regex: "AKIA[0-9A-Z]{16}",
          action: "block",
          message: "AWS access keys must never be printed.",
        }),
        create(GuardrailRuleSchema, {
          name: "private-keys",
          type: "tool-output",
          toolPattern: "*",
          regex: "-----BEGIN (RSA|EC|OPENSSH) PRIVATE KEY-----",
          action: "block",
          message: "Private key material detected in tool output.",
        }),
        create(GuardrailRuleSchema, {
          name: "bearer-tokens",
          type: "tool-output",
          toolPattern: "bash*",
          regex: "Bearer [A-Za-z0-9._-]{20,}",
          action: "warn",
          message: "Looks like a bearer token; redact before sharing.",
        }),
      ],
    }),
    create(GuardrailPolicySchema, {
      namespace: NS,
      name: "safe-shell",
      rules: [
        create(GuardrailRuleSchema, {
          name: "no-rm-rf-root",
          type: "tool-input",
          toolPattern: "bash*",
          regex: "rm\\s+-rf\\s+/(\\s|$)",
          action: "block",
          message: "Refusing to delete the filesystem root.",
        }),
        create(GuardrailRuleSchema, {
          name: "force-push",
          type: "tool-input",
          toolPattern: "bash*",
          regex: "git\\s+push\\s+.*--force",
          action: "warn",
          message: "Force pushes rewrite shared history.",
        }),
        create(GuardrailRuleSchema, {
          name: "curl-pipe-sh",
          type: "tool-input",
          toolPattern: "bash*",
          regex: "curl[^|]*\\|\\s*(sudo\\s+)?(ba)?sh",
          action: "log",
        }),
      ],
    }),
  ];
}

export function roleCatalog() {
  return [
    create(RoleInstructionSchema, {
      name: "executor",
      description: "Implements a scoped change end to end and verifies it with the project's test suite.",
      instructions:
        "<identity>\nYou are the executor: you turn an approved plan into working code.\n</identity>\n\n<constraints>\n- Stay inside the files the plan names; ask the lead before widening scope.\n- Run the narrowest test command that proves the change.\n</constraints>\n\n<execution_loop>\n1. Read the plan step. 2. Implement. 3. Test. 4. Report what changed and what you ran.\n</execution_loop>",
      toolAccess: "execution",
      modelsByProvider: { anthropic: "claude-opus-5-5", openai: "gpt-5.6-sol" },
      reasoningLevel: "high",
    }),
    create(RoleInstructionSchema, {
      name: "code-reviewer",
      description: "Reviews a diff for correctness, regressions, and missing tests without editing code.",
      instructions:
        "<identity>\nYou are a meticulous reviewer. You never edit files.\n</identity>\n\n<style>\nLead with the most severe finding. Cite file:line for every claim.\n</style>",
      toolAccess: "read-only",
      modelsByProvider: { anthropic: "claude-sonnet-5-5" },
      reasoningLevel: "medium",
    }),
    create(RoleInstructionSchema, {
      name: "researcher",
      description: "Gathers evidence from the codebase and docs before the team commits to an approach.",
      instructions:
        "<identity>\nYou are the researcher. You read widely and summarize precisely.\n</identity>\n\n<delegation>\nHand concrete implementation work back to the lead with a short brief.\n</delegation>",
      toolAccess: "analysis",
    }),
  ];
}

/** A skill installed from skills.sh, with catalog provenance populated. */
export function catalogInstalledSkill() {
  return create(SkillInfoSchema, {
    name: "astro",
    description:
      "Skill for building with the Astro web framework. Helps create components and pages, configure SSR adapters, and deploy static sites.",
    gitUrl: "https://github.com/astrolicious/agent-skills/tree/main/astro",
    gitRef: "main",
    gitPath: "astro",
    phase: "Ready",
    resolvedName: "astro",
    resolvedSha: "4f1c2a9",
    catalogSource: "astrolicious/agent-skills",
    catalogSkillId: "astro",
    catalogUrl: "https://skills.sh/astrolicious/agent-skills/astro",
    catalogHash: "sha256:8c1d",
  });
}

export function skillCatalogEntries() {
  const entries: Array<[string, string, string, number, boolean]> = [
    ["anthropics/skills", "pdf", "pdf", 48210, true],
    ["anthropics/skills", "docx", "docx", 39130, true],
    ["anthropics/skills", "frontend-design", "frontend-design", 27004, true],
    ["astrolicious/agent-skills", "astro", "astro", 9120, false],
    ["vercel/skills", "nextjs-app-router", "nextjs-app-router", 18433, false],
    ["supabase/agent-skills", "supabase-postgres", "supabase-postgres", 7311, false],
    ["grafana/skills", "grafana-dashboards", "grafana-dashboards", 2210, false],
    ["acme/skills", "release-notes", "release-notes", 412, false],
  ];
  return entries.map(([source, skillId, name, installs, isOfficial]) =>
    create(SkillCatalogEntrySchema, {
      source,
      skillId,
      name,
      installs: BigInt(installs),
      isOfficial,
      catalogUrl: `https://skills.sh/${source}/${skillId}`,
    }),
  );
}
