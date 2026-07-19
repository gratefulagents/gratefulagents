/**
 * First-login onboarding: eligibility rules and per-user dismissal state.
 *
 * There is deliberately no server-side onboarding flag — a user counts as
 * "new" while their account has nothing usable yet (no provider credential,
 * no GitHub token, no project). Skips are remembered per user in localStorage
 * so onboarding stays quiet on this device; once the account has real state
 * the flows hide themselves everywhere.
 */

export interface CredentialPresence {
  namespace: string;
  anthropicApiKey: boolean;
  openaiApiKey: boolean;
  openrouterApiKey: boolean;
  anthropicOauth: boolean;
  openaiOauth: boolean;
  copilotOauth: boolean;
  githubToken: boolean;
}

export const emptyCredentialPresence: CredentialPresence = {
  namespace: "",
  anthropicApiKey: false,
  openaiApiKey: false,
  openrouterApiKey: false,
  anthropicOauth: false,
  openaiOauth: false,
  copilotOauth: false,
  githubToken: false,
};

/** Server shape shared by ListMyCredentials responses and OAuth onSaved payloads. */
export interface ServerCredentialPresence {
  namespace: string;
  anthropicApiKeyPresent: boolean;
  openaiApiKeyPresent: boolean;
  openrouterApiKeyPresent: boolean;
  anthropicOauthPresent: boolean;
  openaiOauthPresent: boolean;
  copilotOauthPresent: boolean;
  githubTokenPresent: boolean;
}

export function presenceFromServer(c: ServerCredentialPresence): CredentialPresence {
  return {
    namespace: c.namespace,
    anthropicApiKey: c.anthropicApiKeyPresent,
    openaiApiKey: c.openaiApiKeyPresent,
    openrouterApiKey: c.openrouterApiKeyPresent,
    anthropicOauth: c.anthropicOauthPresent,
    openaiOauth: c.openaiOauthPresent,
    copilotOauth: c.copilotOauthPresent,
    githubToken: c.githubTokenPresent,
  };
}

/** Any model-provider credential (API key or OAuth); the GitHub token doesn't count. */
export function hasProviderCredential(p: CredentialPresence): boolean {
  return (
    p.anthropicApiKey ||
    p.openaiApiKey ||
    p.openrouterApiKey ||
    p.anthropicOauth ||
    p.openaiOauth ||
    p.copilotOauth
  );
}

/* ── Dismissal flags (per user, per device) ───────────────────── */

const WIZARD_DISMISS_KEY = "gratefulagents.onboarding.dismissed.v1";
const CHECKLIST_DISMISS_KEY = "gratefulagents.onboarding.checklistDismissed.v1";

function flagKey(base: string, userId: string | undefined): string {
  return `${base}.${userId || "local"}`;
}

function readFlag(base: string, userId: string | undefined): boolean {
  try {
    return localStorage.getItem(flagKey(base, userId)) === "1";
  } catch {
    return false;
  }
}

function writeFlag(base: string, userId: string | undefined) {
  try {
    localStorage.setItem(flagKey(base, userId), "1");
  } catch {
    /* ignore quota */
  }
}

export function onboardingDismissed(userId?: string): boolean {
  return readFlag(WIZARD_DISMISS_KEY, userId);
}

export function dismissOnboarding(userId?: string) {
  writeFlag(WIZARD_DISMISS_KEY, userId);
}

export function checklistDismissed(userId?: string): boolean {
  return readFlag(CHECKLIST_DISMISS_KEY, userId);
}

export function dismissChecklist(userId?: string) {
  writeFlag(CHECKLIST_DISMISS_KEY, userId);
}

/* ── Eligibility ──────────────────────────────────────────────── */

/**
 * shouldOfferOnboarding decides the first-login redirect to /welcome: only
 * for users who can set things up (not viewers), who haven't skipped it here,
 * and whose account is completely empty.
 */
export function shouldOfferOnboarding(input: {
  presence: CredentialPresence;
  projectCount: number;
  role?: string;
  dismissed: boolean;
}): boolean {
  if (input.dismissed) return false;
  if (input.role === "viewer") return false;
  if (input.projectCount > 0) return false;
  if (hasProviderCredential(input.presence) || input.presence.githubToken) return false;
  return true;
}

export interface SetupProgress {
  provider: boolean;
  github: boolean;
  project: boolean;
}

export function setupProgress(presence: CredentialPresence, projectCount: number): SetupProgress {
  return {
    provider: hasProviderCredential(presence),
    github: presence.githubToken,
    project: projectCount > 0,
  };
}

export function setupStepsDone(p: SetupProgress): number {
  return Number(p.provider) + Number(p.github) + Number(p.project);
}

/**
 * shouldShowChecklist keeps the Home "finish setup" card visible only while
 * the account can't actually run anything (no provider credential or no
 * project). A user who deliberately skipped just the GitHub token is left
 * alone once the essentials exist.
 */
export function shouldShowChecklist(input: {
  progress: SetupProgress;
  role?: string;
  dismissed: boolean;
}): boolean {
  if (input.dismissed) return false;
  if (input.role === "viewer") return false;
  return !(input.progress.provider && input.progress.project);
}

/* ── Project naming ───────────────────────────────────────────── */

/**
 * projectNameFromRepo derives a DNS-safe project name from the display name
 * (preferred) or the repository URL's basename.
 */
export function projectNameFromRepo(repoUrl: string, displayName: string): string {
  let base = displayName.trim();
  if (!base) {
    const path = repoUrl.trim().replace(/\.git$/, "").replace(/\/+$/, "");
    base = path.split("/").pop() ?? "";
    // scp-style URLs (git@github.com:owner/repo) keep everything after ":".
    base = base.split(":").pop() ?? "";
  }
  const slug = base
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "")
    .slice(0, 40)
    .replace(/-+$/g, "");
  return slug || "my-project";
}
