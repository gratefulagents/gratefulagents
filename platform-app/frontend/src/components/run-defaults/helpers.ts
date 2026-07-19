import { create } from "@bufbuild/protobuf";

import { providerName } from "@/components/create-flow/providers";
import {
  AgentRunDefaultsSchema,
  type AgentRunDefaults,
  type Cron,
  type ProviderKeyRef,
} from "@/rpc/platform/service_pb";

export function emptyDefaults(): AgentRunDefaults {
  return create(AgentRunDefaultsSchema, {});
}

/**
 * cronToDefaults returns the cron's canonical `defaults` message when the
 * server provides one, falling back to the older flattened Cron fields for
 * servers that predate AgentRunDefaults.
 */
export function cronToDefaults(cron: Cron): AgentRunDefaults {
  if (cron.defaults) return cron.defaults;
  return create(AgentRunDefaultsSchema, {
    repoUrl: cron.repoUrl,
    baseBranch: cron.baseBranch,
    image: cron.image,
    model: cron.model,
    allowedModels: cron.allowedModels,
    provider: cron.provider,
    authMode: cron.authMode,
    timeout: cron.timeout,
    customInstructions: cron.customInstructions,
    claudeApiKeySecret: cron.claudeApiKeySecret,
    openaiOauthSecret: cron.openaiOauthSecret,
    githubTokenSecret: cron.githubTokenSecret,
    providerKeys: cron.providerKeys,
  });
}

/** True when the defaults reference any explicit credential secret. */
export function hasExplicitCredentials(defaults: AgentRunDefaults): boolean {
  return Boolean(
    defaults.claudeApiKeySecret.trim() ||
      defaults.openaiOauthSecret.trim() ||
      defaults.githubTokenSecret.trim() ||
      defaults.providerKeys.length,
  );
}

/**
 * Best guess at whether an existing cron was configured against the caller's
 * saved credentials (no explicit secret refs in its spec).
 */
export function cronUsesSavedCredentials(cron: Cron): boolean {
  return !hasExplicitCredentials(cronToDefaults(cron));
}

export type CronSpec = {
  namespace: string;
  name: string;
  schedule: string;
  timeZone: string;
  suspend: boolean;
  concurrencyPolicy: string;
  prompt: string;
  defaults: AgentRunDefaults;
  useSavedCredentials: boolean;
};

export type CronRequestInit = {
  namespace: string;
  name: string;
  schedule: string;
  timeZone: string;
  suspend: boolean;
  concurrencyPolicy: string;
  prompt: string;
  defaults: AgentRunDefaults;
  useSavedCredentials: boolean;
};

/**
 * buildCronRequest normalizes a cron spec into the shared field shape of
 * CreateCronRequest / UpdateCronRequest: trims free-text fields, drops empty
 * list entries, and clears explicit secret refs when the caller's saved
 * credentials are used instead.
 */
export function buildCronRequest(spec: CronSpec): CronRequestInit {
  const d = spec.defaults;
  const saved = spec.useSavedCredentials;
  const providerKeys: ProviderKeyRef[] = saved
    ? []
    : d.providerKeys.filter((key) => key.provider.trim() && key.secretName.trim());
  const defaults = create(AgentRunDefaultsSchema, {
    repoUrl: d.repoUrl.trim(),
    additionalRepoUrls: d.additionalRepoUrls.map((url) => url.trim()).filter(Boolean),
    baseBranch: d.baseBranch.trim(),
    image: d.image.trim(),
    model: d.model.trim(),
    allowedModels: d.allowedModels.map((m) => m.trim()).filter(Boolean),
    provider: d.provider,
    authMode: d.authMode,
    reasoningLevel: d.reasoningLevel,
    openaiBaseUrl: d.openaiBaseUrl.trim(),
    openaiApi: d.openaiApi,
    timeout: d.timeout.trim(),
    customInstructions: d.customInstructions.trim(),
    claudeApiKeySecret: saved ? "" : d.claudeApiKeySecret.trim(),
    openaiOauthSecret: saved ? "" : d.openaiOauthSecret.trim(),
    githubTokenSecret: saved ? "" : d.githubTokenSecret.trim(),
    providerKeys,
    runtimeProfileRef: d.runtimeProfileRef.trim(),
    mcpPolicyRef: d.mcpPolicyRef.trim(),
    mcpServerRefs: d.mcpServerRefs.map((ref) => ref.trim()).filter(Boolean),
    skillRefs: d.skillRefs.map((ref) => ref.trim()).filter(Boolean),
    workflowMode: "auto",
    modeRef: d.modeRef.trim(),
    executionMode: d.executionMode,
  });
  return {
    namespace: spec.namespace.trim(),
    name: spec.name.trim(),
    schedule: spec.schedule.trim(),
    timeZone: spec.timeZone.trim(),
    suspend: spec.suspend,
    concurrencyPolicy: spec.concurrencyPolicy,
    prompt: spec.prompt.trim(),
    defaults,
    useSavedCredentials: saved,
  };
}

/* ── Collapsed-row summaries ─────────────────────────────────────
   One-line receipts of an AgentRunDefaults group, shown on the
   collapsed OptionRows of create/edit trigger dialogs. */

/** "org/repo @ main · +2 more" | "No repository". */
export function repoSummary(d: AgentRunDefaults): string {
  const repo = d.repoUrl.trim();
  const extras = d.additionalRepoUrls.filter((url) => url.trim()).length;
  if (!repo && !extras) return "No repository";
  const short = repo.replace(/^https?:\/\/[^/]+\//, "").replace(/\.git$/, "") || "repository";
  const branch = d.baseBranch.trim();
  const parts = [branch ? `${short} @ ${branch}` : short];
  if (extras) parts.push(`+${extras} more`);
  return parts.join(" · ");
}

/** "Anthropic · saved credentials" | "OpenAI · gpt-5 · OAuth". */
export function modelSummary(d: AgentRunDefaults, useSavedCredentials: boolean): string {
  const parts = [providerName(d.provider)];
  if (d.model.trim()) parts.push(d.model.trim());
  parts.push(useSavedCredentials ? "saved credentials" : "explicit secrets");
  return parts.join(" · ");
}

/** "Default image" | "node:22 · 30m timeout". */
export function runtimeSummary(d: AgentRunDefaults): string {
  const image = d.image.trim();
  const short = image ? (image.split("/").pop() || image) : "";
  const parts = [short || "Default image"];
  if (d.timeout.trim()) parts.push(`${d.timeout.trim()} timeout`);
  return parts.join(" · ");
}

function count(n: number, noun: string): string {
  return `${n} ${noun}${n === 1 ? "" : "s"}`;
}

/** "None" | "2 MCP servers · 1 skill". */
export function toolsSummary(d: AgentRunDefaults): string {
  const servers = d.mcpServerRefs.filter(Boolean).length;
  const skills = d.skillRefs.filter(Boolean).length;
  if (!servers && !skills) return "None";
  const parts: string[] = [];
  if (servers) parts.push(count(servers, "MCP server"));
  if (skills) parts.push(count(skills, "skill"));
  return parts.join(" · ");
}

/** Number of advanced fields that differ from their defaults. */
export function advancedCustomizedCount(d: AgentRunDefaults): number {
  return [
    d.allowedModels.length > 0,
    Boolean(d.reasoningLevel),
    Boolean(d.openaiBaseUrl.trim()),
    Boolean(d.openaiApi),
    Boolean(d.executionMode),
    Boolean(d.modeRef.trim()),
    Boolean(d.customInstructions.trim()),
  ].filter(Boolean).length;
}

/** "Defaults" | "3 customized". */
export function advancedSummary(d: AgentRunDefaults): string {
  const n = advancedCustomizedCount(d);
  return n ? `${n} customized` : "Defaults";
}
