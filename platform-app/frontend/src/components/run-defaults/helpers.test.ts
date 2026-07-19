import { describe, expect, it } from "vitest";
import { create } from "@bufbuild/protobuf";

import {
  AgentRunDefaultsSchema,
  CronSchema,
  type Cron,
} from "@/rpc/platform/service_pb";
import {
  buildCronRequest,
  cronToDefaults,
  cronUsesSavedCredentials,
  emptyDefaults,
  hasExplicitCredentials,
} from "./helpers";

function flattenedCron(): Cron {
  return create(CronSchema, {
    namespace: "team-a",
    name: "nightly",
    schedule: "@daily",
    prompt: "do the thing",
    repoUrl: "https://github.com/org/repo",
    baseBranch: "develop",
    model: "gpt-5",
    image: "runtime:node",
    timeout: "45m",
    provider: "openai",
    authMode: "oauth",
    allowedModels: ["gpt-5", "gpt-4.1"],
    customInstructions: "be careful",
    claudeApiKeySecret: "claude-secret",
    openaiOauthSecret: "oauth-secret",
    githubTokenSecret: "gh-secret",
    providerKeys: [{ provider: "groq", secretName: "groq-key", secretKey: "api-key" }],
  });
}

describe("cronToDefaults", () => {
  it("prefers the canonical defaults message when set", () => {
    const cron = flattenedCron();
    cron.defaults = create(AgentRunDefaultsSchema, {
      repoUrl: "https://github.com/org/canonical",
      model: "claude-sonnet-4-6",
      workflowMode: "chat",
    });
    const defaults = cronToDefaults(cron);
    expect(defaults.repoUrl).toBe("https://github.com/org/canonical");
    expect(defaults.model).toBe("claude-sonnet-4-6");
    expect(defaults.workflowMode).toBe("chat");
    // Flattened fields are ignored when defaults is present.
    expect(defaults.baseBranch).toBe("");
  });

  it("falls back to flattened Cron fields for older servers", () => {
    const defaults = cronToDefaults(flattenedCron());
    expect(defaults.repoUrl).toBe("https://github.com/org/repo");
    expect(defaults.baseBranch).toBe("develop");
    expect(defaults.model).toBe("gpt-5");
    expect(defaults.image).toBe("runtime:node");
    expect(defaults.timeout).toBe("45m");
    expect(defaults.provider).toBe("openai");
    expect(defaults.authMode).toBe("oauth");
    expect(defaults.allowedModels).toEqual(["gpt-5", "gpt-4.1"]);
    expect(defaults.customInstructions).toBe("be careful");
    expect(defaults.claudeApiKeySecret).toBe("claude-secret");
    expect(defaults.openaiOauthSecret).toBe("oauth-secret");
    expect(defaults.githubTokenSecret).toBe("gh-secret");
    expect(defaults.providerKeys).toHaveLength(1);
    expect(defaults.providerKeys[0].secretName).toBe("groq-key");
  });
});

describe("hasExplicitCredentials / cronUsesSavedCredentials", () => {
  it("detects explicit secret refs", () => {
    expect(hasExplicitCredentials(emptyDefaults())).toBe(false);
    expect(
      hasExplicitCredentials(create(AgentRunDefaultsSchema, { githubTokenSecret: "gh" })),
    ).toBe(true);
    expect(
      hasExplicitCredentials(
        create(AgentRunDefaultsSchema, {
          providerKeys: [{ provider: "openai", secretName: "s", secretKey: "api-key" }],
        }),
      ),
    ).toBe(true);
  });

  it("treats crons without secret refs as using saved credentials", () => {
    const cron = create(CronSchema, { namespace: "team-a", name: "n", schedule: "@daily" });
    expect(cronUsesSavedCredentials(cron)).toBe(true);
    expect(cronUsesSavedCredentials(flattenedCron())).toBe(false);
  });
});

describe("buildCronRequest", () => {
  const spec = {
    namespace: " team-a ",
    name: " nightly ",
    schedule: " @daily ",
    timeZone: " America/New_York ",
    suspend: true,
    concurrencyPolicy: "Allow",
    prompt: " do it ",
    defaults: create(AgentRunDefaultsSchema, {
      repoUrl: " https://github.com/org/repo ",
      additionalRepoUrls: [" https://github.com/org/extra ", "  "],
      allowedModels: [" a ", "", "b"],
      claudeApiKeySecret: "claude-secret",
      openaiOauthSecret: "oauth-secret",
      githubTokenSecret: "gh-secret",
      providerKeys: [
        { provider: "groq", secretName: "groq-key", secretKey: "api-key" },
        { provider: "", secretName: "orphan", secretKey: "api-key" },
      ],
    }),
    useSavedCredentials: false,
  };

  it("trims fields and drops empty list entries", () => {
    const request = buildCronRequest(spec);
    expect(request.namespace).toBe("team-a");
    expect(request.name).toBe("nightly");
    expect(request.schedule).toBe("@daily");
    expect(request.timeZone).toBe("America/New_York");
    expect(request.suspend).toBe(true);
    expect(request.concurrencyPolicy).toBe("Allow");
    expect(request.prompt).toBe("do it");
    expect(request.defaults.repoUrl).toBe("https://github.com/org/repo");
    expect(request.defaults.additionalRepoUrls).toEqual(["https://github.com/org/extra"]);
    expect(request.defaults.allowedModels).toEqual(["a", "b"]);
  });

  it("keeps explicit secrets and complete provider keys when not using saved credentials", () => {
    const request = buildCronRequest(spec);
    expect(request.useSavedCredentials).toBe(false);
    expect(request.defaults.claudeApiKeySecret).toBe("claude-secret");
    expect(request.defaults.openaiOauthSecret).toBe("oauth-secret");
    expect(request.defaults.githubTokenSecret).toBe("gh-secret");
    expect(request.defaults.providerKeys).toHaveLength(1);
    expect(request.defaults.providerKeys[0].provider).toBe("groq");
  });

  it("clears explicit secret refs when using saved credentials", () => {
    const request = buildCronRequest({ ...spec, useSavedCredentials: true });
    expect(request.useSavedCredentials).toBe(true);
    expect(request.defaults.claudeApiKeySecret).toBe("");
    expect(request.defaults.openaiOauthSecret).toBe("");
    expect(request.defaults.githubTokenSecret).toBe("");
    expect(request.defaults.providerKeys).toEqual([]);
  });
});

describe("collapsed-row summaries", () => {
  it("summarizes an empty defaults message with quiet defaults", async () => {
    const { repoSummary, modelSummary, runtimeSummary, toolsSummary, advancedSummary } =
      await import("./helpers");
    const d = emptyDefaults();
    expect(repoSummary(d)).toBe("No repository");
    expect(modelSummary(d, true)).toBe("Anthropic · saved credentials");
    expect(runtimeSummary(d)).toBe("Default image");
    expect(toolsSummary(d)).toBe("None");
    expect(advancedSummary(d)).toBe("Defaults");
  });

  it("summarizes customized defaults as a receipt", async () => {
    const { repoSummary, modelSummary, runtimeSummary, toolsSummary, advancedSummary } =
      await import("./helpers");
    const d = create(AgentRunDefaultsSchema, {
      repoUrl: "https://github.com/org/repo.git",
      additionalRepoUrls: ["https://github.com/org/other"],
      baseBranch: "develop",
      provider: "openai",
      model: "gpt-5",
      image: "registry.example.com/worker/node:22",
      timeout: "45m",
      mcpServerRefs: ["fetch", "github"],
      skillRefs: ["search-web"],
      customInstructions: "be careful",
      reasoningLevel: "high",
    });
    expect(repoSummary(d)).toBe("org/repo @ develop · +1 more");
    expect(modelSummary(d, false)).toBe("OpenAI · gpt-5 · explicit secrets");
    expect(runtimeSummary(d)).toBe("node:22 · 45m timeout");
    expect(toolsSummary(d)).toBe("2 MCP servers · 1 skill");
    expect(advancedSummary(d)).toBe("2 customized");
  });
});
