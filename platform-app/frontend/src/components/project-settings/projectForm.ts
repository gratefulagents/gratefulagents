import { create } from "@bufbuild/protobuf";

import { providerName } from "@/components/create-flow/providers";
import {
  oauthProviders,
  projectUsesSavedCredentials,
  providerKeyFor,
  savedCredentialProviders,
} from "@/lib/projectCredentialForm";
import {
  CreateProjectRequestSchema,
  UpdateProjectRequestSchema,
  type CreateProjectRequest,
  type Project,
  type UpdateProjectRequest,
} from "@/rpc/platform/service_pb";

/**
 * One form model for creating and editing a project.
 *
 * Create and edit used to be two ~1000-line dialogs with drifting field
 * sets. Both now read and write this state; the host decides which
 * credential inputs apply (`mode`): a new project accepts inline API keys,
 * an existing one only references Secrets already in the namespace.
 */

export type ProjectFormMode = "create" | "edit";
export type AuthMode = "api-key" | "oauth";

export type ProjectFormState = {
  name: string;
  displayName: string;
  repoUrl: string;
  additionalRepoUrls: string[];
  baseBranch: string;

  provider: string;
  authMode: AuthMode;
  model: string;
  reasoningLevel: string;
  useSavedCredentials: boolean;
  /** Create-only inline secrets, stored by the server on the user's behalf. */
  githubToken: string;
  anthropicApiKey: string;
  openaiApiKey: string;
  /** Secret references (edit; the OAuth ref is also accepted at create). */
  openaiOauthSecret: string;
  githubTokenSecret: string;
  claudeApiKeySecret: string;
  providerKeySecret: string;
  providerKeyKey: string;

  modeRef: string;
  reviewLoopDisabled: boolean;
  customInstructions: string;
  allowedModels: string;
  bugSquasher: boolean;

  image: string;
  timeout: string;
  configureRuntimeProfile: boolean;
  runtimeProfileRef: string;
  permissionMode: string;
  egressMode: string;

  mcpServerRefs: string[];
  configureMcpPolicy: boolean;
  mcpPolicyRef: string;
  mcpPolicyDefaultAction: string;
  mcpPolicyAllowedServers: string;

  kubernetesAdmin: boolean;
  dockerInDocker: boolean;
};

export const DEFAULT_PERMISSION_MODE = "workspace-write";
export const DEFAULT_EGRESS_MODE = "unrestricted";

export function emptyProjectForm(): ProjectFormState {
  return {
    name: "",
    displayName: "",
    repoUrl: "",
    additionalRepoUrls: [],
    baseBranch: "",
    provider: "anthropic",
    authMode: "api-key",
    model: "",
    reasoningLevel: "",
    useSavedCredentials: true,
    githubToken: "",
    anthropicApiKey: "",
    openaiApiKey: "",
    openaiOauthSecret: "",
    githubTokenSecret: "",
    claudeApiKeySecret: "",
    providerKeySecret: "",
    providerKeyKey: "api-key",
    modeRef: "",
    reviewLoopDisabled: true,
    customInstructions: "",
    allowedModels: "",
    bugSquasher: false,
    image: "",
    timeout: "",
    // A fresh project gets its own RuntimeProfile so sandbox policy is
    // explicit from day one; editing never re-creates one unless asked.
    configureRuntimeProfile: true,
    runtimeProfileRef: "",
    permissionMode: DEFAULT_PERMISSION_MODE,
    egressMode: DEFAULT_EGRESS_MODE,
    mcpServerRefs: [],
    configureMcpPolicy: false,
    mcpPolicyRef: "",
    mcpPolicyDefaultAction: "Deny",
    mcpPolicyAllowedServers: "",
    kubernetesAdmin: false,
    dockerInDocker: false,
  };
}

export function projectFormFromProject(project: Project): ProjectFormState {
  const provider = project.provider || "openai";
  const key = providerKeyFor(project, provider);
  const authMode: AuthMode =
    provider === "copilot" ? "oauth" : ((project.authMode as AuthMode) || "api-key");
  return {
    ...emptyProjectForm(),
    name: project.name,
    displayName: project.displayName || project.name,
    repoUrl: project.repoUrl || "",
    additionalRepoUrls: [...(project.additionalRepoUrls ?? [])],
    baseBranch: project.baseBranch || "",
    provider,
    authMode,
    model: project.model || "",
    reasoningLevel: project.reasoningLevel || "",
    useSavedCredentials: projectUsesSavedCredentials(project, provider, authMode),
    openaiOauthSecret: project.openaiOauthSecret || "",
    githubTokenSecret: project.githubTokenSecret || "",
    claudeApiKeySecret: project.claudeApiKeySecret || "",
    providerKeySecret: key?.secretName || "",
    providerKeyKey: key?.secretKey || "api-key",
    modeRef: project.modeRef || "",
    reviewLoopDisabled: project.reviewLoopDisabled,
    customInstructions: project.customInstructions || "",
    allowedModels: project.allowedModels.join(", "),
    bugSquasher: project.bugSquasher,
    image: project.image || "",
    timeout: project.timeout || "",
    configureRuntimeProfile: false,
    runtimeProfileRef: project.runtimeProfileRef || "",
    permissionMode: project.permissionMode || DEFAULT_PERMISSION_MODE,
    egressMode: project.egressMode || DEFAULT_EGRESS_MODE,
    mcpServerRefs: [...(project.mcpServerRefs ?? [])],
    configureMcpPolicy: false,
    mcpPolicyRef: project.mcpPolicyRef || "",
    mcpPolicyDefaultAction: project.mcpPolicyDefaultAction || "Deny",
    mcpPolicyAllowedServers: (project.mcpPolicyAllowedServers ?? []).join(", "),
    kubernetesAdmin: project.kubernetesAdmin,
    dockerInDocker: project.dockerInDocker,
  };
}

/* ── Derivations ──────────────────────────────────────────────── */

export function splitCommaList(value: string): string[] {
  return value
    .split(",")
    .map((part) => part.trim())
    .filter(Boolean);
}

function listKey(values: readonly string[]): string {
  return values.map((value) => value.trim()).filter(Boolean).join("\u0000");
}

/**
 * Suggest a Kubernetes-safe project name from a repository URL so the user
 * only has to paste a URL: `https://github.com/acme/Payments-API.git` and
 * `git@github.com:acme/Payments-API.git` both become `payments-api`.
 */
export function deriveProjectName(repoUrl: string): string {
  const trimmed = repoUrl.trim();
  if (!trimmed) return "";
  let path = trimmed;
  const scp = /^[^/@:]+@[^:/]+:(.+)$/.exec(trimmed);
  if (scp) {
    path = scp[1];
  } else {
    try {
      path = new URL(trimmed).pathname;
    } catch {
      // Not a URL — treat the whole string as a path.
    }
  }
  const segments = path.split("/").filter(Boolean);
  let last = segments[segments.length - 1] ?? "";
  try {
    last = decodeURIComponent(last);
  } catch {
    // Keep the raw segment; the slug step strips anything unsafe.
  }
  const slug = last
    .replace(/\.git$/i, "")
    .toLowerCase()
    .replace(/[^a-z0-9-]+/g, "-")
    .replace(/-{2,}/g, "-")
    .replace(/^-+|-+$/g, "")
    .slice(0, 63)
    .replace(/-+$/g, "");
  return slug;
}

export function effectiveAuthModeFor(form: ProjectFormState): AuthMode {
  if (form.provider === "copilot") return "oauth";
  return oauthProviders.has(form.provider) ? form.authMode : "api-key";
}

export function savedSupportedFor(form: ProjectFormState): boolean {
  return savedCredentialProviders.has(form.provider);
}

export function usesSavedCredentials(form: ProjectFormState): boolean {
  return form.useSavedCredentials && savedSupportedFor(form);
}

/* ── Validation ───────────────────────────────────────────────── */

export function validateProjectForm(
  form: ProjectFormState,
  mode: ProjectFormMode,
  savedReady: boolean,
): string | null {
  const meta = providerName(form.provider);
  const authMode = effectiveAuthModeFor(form);
  const useSaved = usesSavedCredentials(form);
  if (mode === "create" && !form.name.trim()) return "Give the project a name.";
  if (mode === "edit" && !form.displayName.trim()) return "Display name is required.";
  if (useSaved) {
    if (!savedReady) {
      return `No saved ${meta} credential. Add it in Settings, or turn off "Use my saved credentials".`;
    }
  } else if (mode === "create") {
    if (authMode === "oauth" && !form.openaiOauthSecret.trim()) {
      return "OAuth auth mode requires an existing OAuth secret name.";
    }
    if (authMode === "api-key") {
      if (form.provider === "anthropic" && !form.anthropicApiKey.trim()) {
        return "Anthropic with API-key auth requires an Anthropic API key.";
      }
      if (form.provider === "openai" && !form.openaiApiKey.trim()) {
        return "OpenAI with API-key auth requires an OpenAI API key.";
      }
    }
  } else {
    if (authMode === "oauth" && !form.openaiOauthSecret.trim()) {
      return "OAuth auth mode requires an OAuth Secret name.";
    }
    if (authMode === "api-key") {
      if (
        form.provider === "anthropic" &&
        !form.claudeApiKeySecret.trim() &&
        !form.providerKeySecret.trim()
      ) {
        return "Anthropic API-key auth requires an Anthropic Secret ref.";
      }
      if (form.provider !== "anthropic" && !form.providerKeySecret.trim()) {
        return `${form.provider} API-key auth requires a provider key Secret ref.`;
      }
    }
  }
  if (form.provider === "copilot" && !form.model.trim()) return "Choose a GitHub Copilot model.";
  return null;
}

/* ── Requests ─────────────────────────────────────────────────── */

export function createRequestFromForm(form: ProjectFormState): CreateProjectRequest {
  const useSaved = usesSavedCredentials(form);
  const name = form.name.trim();
  return create(CreateProjectRequestSchema, {
    name,
    // The server requires a display name; fall back to the name so the
    // field can stay out of the create dialog entirely.
    displayName: form.displayName.trim() || name,
    repoUrl: form.repoUrl.trim(),
    additionalRepoUrls: form.additionalRepoUrls.map((url) => url.trim()).filter(Boolean),
    reviewLoopDisabled: form.reviewLoopDisabled,
    provider: form.provider,
    model: form.model.trim(),
    reasoningLevel: form.reasoningLevel,
    modeRef: form.modeRef.trim(),
    baseBranch: form.baseBranch.trim(),
    timeout: form.timeout.trim(),
    customInstructions: form.customInstructions.trim(),
    allowedModels: splitCommaList(form.allowedModels),
    authMode: effectiveAuthModeFor(form),
    useSavedCredentials: useSaved,
    openaiOauthSecret: useSaved ? "" : form.openaiOauthSecret.trim(),
    githubToken: useSaved ? "" : form.githubToken.trim(),
    anthropicApiKey: useSaved ? "" : form.anthropicApiKey.trim(),
    openaiApiKey: useSaved ? "" : form.openaiApiKey.trim(),
    configureRuntimeProfile: form.configureRuntimeProfile,
    runtimeProfileRef: form.runtimeProfileRef.trim(),
    permissionMode: form.permissionMode,
    egressMode: form.egressMode,
    configureMcpPolicy: form.configureMcpPolicy,
    mcpPolicyRef: form.mcpPolicyRef.trim(),
    mcpPolicyDefaultAction: form.mcpPolicyDefaultAction,
    mcpPolicyAllowedServers: splitCommaList(form.mcpPolicyAllowedServers),
    mcpServerRefs: form.mcpServerRefs,
    image: form.image.trim(),
  });
}

function providerKeysForUpdate(form: ProjectFormState, project: Project) {
  const currentProvider = form.provider.toLowerCase();
  const keys = project.providerKeys
    .filter((key) => key.provider.toLowerCase() !== currentProvider)
    .map((key) => ({
      provider: key.provider,
      secretName: key.secretName,
      secretKey: key.secretKey,
    }));
  if (form.providerKeySecret.trim()) {
    keys.push({
      provider: currentProvider,
      secretName: form.providerKeySecret.trim(),
      secretKey: form.providerKeyKey.trim() || "api-key",
    });
  }
  return keys;
}

export function updateRequestFromForm(
  form: ProjectFormState,
  project: Project,
  options: { isAdmin: boolean },
): UpdateProjectRequest {
  const initial = projectFormFromProject(project);
  const useSaved = usesSavedCredentials(form);
  return create(UpdateProjectRequestSchema, {
    namespace: project.namespace,
    name: project.name,
    displayName: form.displayName.trim(),
    repoUrl: form.repoUrl.trim(),
    additionalRepoUrls: form.additionalRepoUrls.map((url) => url.trim()).filter(Boolean),
    reviewLoopDisabled: form.reviewLoopDisabled,
    baseBranch: form.baseBranch.trim(),
    provider: form.provider,
    authMode: effectiveAuthModeFor(form),
    model: form.model.trim(),
    reasoningLevel: form.reasoningLevel,
    // Omit an unchanged mode so name-only dashboard reads do not erase a
    // version/channel pin configured through the Kubernetes API.
    ...(form.modeRef.trim() !== project.modeRef.trim() ? { modeRef: form.modeRef.trim() } : {}),
    image: form.image.trim(),
    timeout: form.timeout.trim(),
    allowedModels: splitCommaList(form.allowedModels),
    customInstructions: form.customInstructions.trim(),
    useSavedCredentials: useSaved,
    openaiOauthSecret: useSaved ? "" : form.openaiOauthSecret.trim(),
    githubTokenSecret: useSaved ? "" : form.githubTokenSecret.trim(),
    claudeApiKeySecret: useSaved ? "" : form.claudeApiKeySecret.trim(),
    providerKeys: useSaved ? [] : providerKeysForUpdate(form, project),
    configureRuntimeProfile: form.configureRuntimeProfile,
    runtimeProfileRef: form.runtimeProfileRef.trim(),
    permissionMode: form.permissionMode,
    egressMode: form.egressMode,
    configureMcpPolicy: form.configureMcpPolicy,
    mcpPolicyRef: form.mcpPolicyRef.trim(),
    mcpPolicyDefaultAction: form.mcpPolicyDefaultAction,
    mcpPolicyAllowedServers: splitCommaList(form.mcpPolicyAllowedServers),
    mcpServerRefs: form.mcpServerRefs,
    skillRefs: [],
    ...(options.isAdmin
      ? { kubernetesAdmin: form.kubernetesAdmin, dockerInDocker: form.dockerInDocker }
      : {}),
    // Only send the flag when it changed: enabling it clears the flag on
    // every other project in the namespace.
    ...(form.bugSquasher !== initial.bugSquasher ? { bugSquasher: form.bugSquasher } : {}),
  });
}

/* ── Dirty tracking ───────────────────────────────────────────── */

/** Normalized snapshot so whitespace-only edits do not count as changes. */
export function projectFormKey(form: ProjectFormState): string {
  const normalized: Record<string, unknown> = {};
  for (const [key, value] of Object.entries(form)) {
    normalized[key] = Array.isArray(value) ? listKey(value) : typeof value === "string" ? value.trim() : value;
  }
  return JSON.stringify(normalized);
}

export type ProjectFormSection =
  | "general"
  | "model"
  | "agent"
  | "runtime"
  | "tools"
  | "privileged";

const SECTION_FIELDS: Record<ProjectFormSection, (keyof ProjectFormState)[]> = {
  general: ["displayName", "repoUrl", "additionalRepoUrls", "baseBranch"],
  model: [
    "provider",
    "authMode",
    "model",
    "reasoningLevel",
    "useSavedCredentials",
    "githubToken",
    "anthropicApiKey",
    "openaiApiKey",
    "openaiOauthSecret",
    "githubTokenSecret",
    "claudeApiKeySecret",
    "providerKeySecret",
    "providerKeyKey",
    "allowedModels",
  ],
  agent: ["modeRef", "reviewLoopDisabled", "customInstructions", "bugSquasher"],
  runtime: [
    "image",
    "timeout",
    "configureRuntimeProfile",
    "runtimeProfileRef",
    "permissionMode",
    "egressMode",
  ],
  tools: [
    "mcpServerRefs",
    "configureMcpPolicy",
    "mcpPolicyRef",
    "mcpPolicyDefaultAction",
    "mcpPolicyAllowedServers",
  ],
  privileged: ["kubernetesAdmin", "dockerInDocker"],
};

export function sectionChanged(
  section: ProjectFormSection,
  form: ProjectFormState,
  initial: ProjectFormState,
): boolean {
  return SECTION_FIELDS[section].some((field) => {
    const a = form[field];
    const b = initial[field];
    if (Array.isArray(a) && Array.isArray(b)) return listKey(a) !== listKey(b);
    if (typeof a === "string" && typeof b === "string") return a.trim() !== b.trim();
    return a !== b;
  });
}

/** Reset only one section back to its initial values. */
export function resetSection(
  section: ProjectFormSection,
  form: ProjectFormState,
  initial: ProjectFormState,
): ProjectFormState {
  const next = { ...form };
  for (const field of SECTION_FIELDS[section]) {
    (next as Record<string, unknown>)[field] = initial[field];
  }
  return next;
}

/* ── Receipts (collapsed-row summaries) ───────────────────────── */

export function modelSummary(form: ProjectFormState, savedReady: boolean): string {
  const useSaved = usesSavedCredentials(form);
  return [
    providerName(form.provider),
    form.model.trim() || "provider default",
    useSaved ? (savedReady ? "saved credentials" : "no saved credential") : "custom credentials",
  ].join(" · ");
}

export function repoSummary(form: ProjectFormState): string {
  const extras = form.additionalRepoUrls.filter((url) => url.trim()).length;
  return [
    form.baseBranch.trim() ? `branch ${form.baseBranch.trim()}` : "default branch",
    ...(extras ? [`+${extras} extra repo${extras === 1 ? "" : "s"}`] : []),
  ].join(" · ");
}

export function runtimeSummary(form: ProjectFormState): string {
  const imageTail = form.image.trim()
    ? (form.image.trim().split("/").pop() ?? form.image.trim())
    : "default image";
  const policy = form.configureRuntimeProfile
    ? `${form.permissionMode} · ${form.egressMode}`
    : form.runtimeProfileRef.trim() || null;
  return [imageTail, form.timeout.trim() ? `${form.timeout.trim()} timeout` : "default timeout", policy]
    .filter(Boolean)
    .join(" · ");
}

export function agentSummary(form: ProjectFormState): string {
  return [
    form.modeRef.trim() || "Interactive",
    form.reviewLoopDisabled ? "no review loop" : "review loop",
    ...(form.customInstructions.trim() ? ["custom instructions"] : []),
    ...(form.bugSquasher ? ["bug squasher"] : []),
  ].join(" · ");
}

export function toolsSummary(form: ProjectFormState): string {
  const count = form.mcpServerRefs.length;
  const servers = count ? `${count} MCP server${count === 1 ? "" : "s"}` : "no MCP servers";
  const policy = form.configureMcpPolicy
    ? `${form.mcpPolicyDefaultAction.toLowerCase()}-by-default policy`
    : form.mcpPolicyRef.trim()
      ? `policy ${form.mcpPolicyRef.trim()}`
      : null;
  return [servers, policy].filter(Boolean).join(" · ");
}

export function privilegedSummary(form: ProjectFormState): string {
  return (
    [form.kubernetesAdmin ? "Kubernetes admin" : null, form.dockerInDocker ? "Docker-in-Docker" : null]
      .filter(Boolean)
      .join(", ") || "Standard access"
  );
}
