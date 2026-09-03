import type { Project } from "@/rpc/platform/service_pb";

/**
 * Per-run overrides layered on top of a project's defaults. Every field is
 * "inherit" until the user touches it, so an untouched composer submits the
 * bare minimum (request + project) and the server applies the project.
 */

export type OverseerOverrides = {
  enabled: boolean;
  modeRefName: string;
  modeRefVersion: string;
  modeRefChannel: string;
  model: string;
  authority: string;
  intervalMinutes: string;
  maxInterventions: string;
};

export type RunOverrides = {
  /** Empty means "the project's provider". */
  provider: string;
  model: string;
  reasoningLevel: string;
  /** `undefined` inherits; a value (even empty) is an explicit override. */
  repoUrl?: string;
  baseBranch?: string;
  additionalRepoUrls?: string[];
  image: string;
  claudeApiKeySecret: string;
  githubTokenSecret: string;
  namespace: string;
  overseer: OverseerOverrides;
};

export const DEFAULT_OVERSEER: OverseerOverrides = {
  enabled: false,
  modeRefName: "",
  modeRefVersion: "",
  modeRefChannel: "",
  model: "",
  authority: "advise",
  intervalMinutes: "10",
  maxInterventions: "5",
};

export function emptyRunOverrides(): RunOverrides {
  return {
    provider: "",
    model: "",
    reasoningLevel: "",
    image: "",
    claudeApiKeySecret: "",
    githubTokenSecret: "",
    namespace: "",
    overseer: { ...DEFAULT_OVERSEER },
  };
}

/** The project facts receipts read; partial because a pinned project may not have loaded yet. */
export type ProjectDefaults = Pick<Project, "namespace"> &
  Partial<Pick<Project, "provider" | "model" | "repoUrl" | "baseBranch" | "additionalRepoUrls" | "image">>;

/** "org/repo" from a git URL, for receipts. */
export function shortRepo(url: string): string {
  const trimmed = url.trim().replace(/\/+$/, "").replace(/\.git$/, "");
  return trimmed.replace(/^[a-z]+:\/\/[^/]+\//, "").replace(/^[^@]+@[^:]+:/, "") || trimmed;
}

export function effectiveProvider(o: RunOverrides, project?: ProjectDefaults): string {
  return o.provider || project?.provider || "";
}

/**
 * CreateAgentRunRequest has no provider field: the provider travels as the
 * model prefix. Bare model names get the chosen provider prefixed; already
 * prefixed ones pass through.
 */
export function prefixedModel(o: RunOverrides, project?: ProjectDefaults): string {
  const bare = o.model.trim();
  if (!bare) return "";
  const provider = effectiveProvider(o, project);
  return provider && !bare.includes("/") ? `${provider}/${bare}` : bare;
}

export function validateOverseer(ov: OverseerOverrides): string | null {
  if (!ov.enabled) return null;
  if ((ov.modeRefVersion.trim() || ov.modeRefChannel.trim()) && !ov.modeRefName.trim()) {
    return "Overseer mode name is required when a version or channel is set.";
  }
  const interval = Number(ov.intervalMinutes);
  if (!Number.isInteger(interval) || interval < 1 || interval > 1440) {
    return "Overseer interval must be a whole number between 1 and 1440 minutes.";
  }
  const max = Number(ov.maxInterventions);
  if (!Number.isInteger(max) || max < 0 || max > 100) {
    return "Overseer max interventions must be a whole number between 0 and 100.";
  }
  return null;
}

/** Request fields to spread into `createAgentRun` for these overrides. */
export function overrideRequestFields(o: RunOverrides, project?: ProjectDefaults) {
  return {
    model: prefixedModel(o, project),
    ...(o.reasoningLevel ? { reasoningLevel: o.reasoningLevel } : {}),
    // Project repository settings are defaults, not run-level overrides.
    // Send each field only after an explicit edit so changing one control
    // cannot submit stale cached values for the others.
    ...(o.repoUrl !== undefined ? { repoUrl: o.repoUrl.trim() } : {}),
    ...(o.baseBranch !== undefined ? { baseBranch: o.baseBranch.trim() } : {}),
    ...(o.additionalRepoUrls !== undefined
      ? { additionalRepoUrls: o.additionalRepoUrls.map((u) => u.trim()).filter(Boolean) }
      : {}),
    ...(o.image.trim() ? { image: o.image.trim() } : {}),
    ...(o.claudeApiKeySecret.trim() ? { claudeApiKeySecret: o.claudeApiKeySecret.trim() } : {}),
    ...(o.githubTokenSecret.trim() ? { githubTokenSecret: o.githubTokenSecret.trim() } : {}),
    ...(o.overseer.enabled
      ? {
          overseer: {
            modeRefName: o.overseer.modeRefName.trim(),
            modeRefVersion: o.overseer.modeRefVersion.trim(),
            modeRefChannel: o.overseer.modeRefChannel.trim(),
            model: o.overseer.model.trim(),
            authority: o.overseer.authority,
            intervalMinutes: Number(o.overseer.intervalMinutes),
            maxInterventions: Number(o.overseer.maxInterventions),
          },
        }
      : {}),
  };
}

/* ── Which rows carry an override (for the badge + receipts) ─── */

export type OverrideGroup = "model" | "repository" | "runtime" | "overseer" | "advanced";

export function activeGroups(o: RunOverrides, project?: ProjectDefaults): OverrideGroup[] {
  const groups: OverrideGroup[] = [];
  const projectProvider = project?.provider || "";
  if ((o.provider && o.provider !== projectProvider) || o.model.trim() || o.reasoningLevel) {
    groups.push("model");
  }
  if (o.repoUrl !== undefined || o.baseBranch !== undefined || o.additionalRepoUrls !== undefined) {
    groups.push("repository");
  }
  if (o.image.trim()) groups.push("runtime");
  if (o.overseer.enabled) groups.push("overseer");
  if (o.claudeApiKeySecret.trim() || o.githubTokenSecret.trim() || o.namespace.trim()) {
    groups.push("advanced");
  }
  return groups;
}

/** Drop a `provider/` prefix that merely repeats the provider shown beside it. */
export function bareModel(model: string, provider: string): string {
  const trimmed = model.trim();
  return provider && trimmed.toLowerCase().startsWith(`${provider.toLowerCase()}/`)
    ? trimmed.slice(provider.length + 1)
    : trimmed;
}

/** The project's model the run falls back to, or "" when overriding the provider. */
export function inheritedModel(o: RunOverrides, project?: ProjectDefaults): string {
  if (!project?.model?.trim()) return "";
  if (o.provider && o.provider !== (project.provider || "")) return "";
  return bareModel(project.model, project.provider || "");
}

export function modelReceipt(o: RunOverrides, project?: ProjectDefaults, providerLabel?: string): string {
  const provider = effectiveProvider(o, project);
  const model = o.model.trim() ? bareModel(o.model, provider) : inheritedModel(o, project);
  const parts = [providerLabel || provider || "Project provider"];
  parts.push(model || "provider default");
  if (o.reasoningLevel) parts.push(o.reasoningLevel);
  return parts.join(" · ");
}

export function repositoryReceipt(o: RunOverrides, project?: ProjectDefaults): string {
  const url = o.repoUrl ?? project?.repoUrl ?? "";
  const branch = o.baseBranch ?? project?.baseBranch ?? "";
  const extras = (o.additionalRepoUrls ?? project?.additionalRepoUrls ?? []).filter((u) => u.trim()).length;
  const head = url.trim() ? shortRepo(url) : "No repository";
  return [
    branch.trim() ? `${head} @ ${branch.trim()}` : head,
    ...(extras ? [`+${extras} more`] : []),
  ].join(" · ");
}

export function runtimeReceipt(o: RunOverrides, project?: ProjectDefaults): string {
  const image = o.image.trim() || project?.image?.trim() || "";
  return image ? (image.split("/").pop() ?? image) : "Project default";
}

export function overseerReceipt(o: RunOverrides): string {
  return o.overseer.enabled
    ? `${o.overseer.authority} · every ${o.overseer.intervalMinutes} min`
    : "Off";
}

export function advancedReceipt(o: RunOverrides, project?: ProjectDefaults): string {
  const bits = [
    ...(o.claudeApiKeySecret.trim() ? ["API key secret"] : []),
    ...(o.githubTokenSecret.trim() ? ["GitHub token secret"] : []),
  ];
  const ns = o.namespace.trim() || project?.namespace || "";
  if (ns && o.namespace.trim()) bits.push(`in ${ns}`);
  return bits.length ? bits.join(" · ") : "From project";
}
