import type { Project } from "@/rpc/platform/service_pb";

// Providers whose credentials can be saved per-user (usercred-* Secrets) and
// providers that support OAuth. Kept in sync with the dashboard backend.
export const savedCredentialProviders = new Set(["anthropic", "openai", "openrouter", "xai", "copilot"]);
export const oauthProviders = new Set(["anthropic", "openai", "copilot"]);

export type CredentialPresence = {
  anthropicApiKey: boolean;
  openaiApiKey: boolean;
  openrouterApiKey: boolean;
  xaiApiKey: boolean;
  anthropicOauth: boolean;
  openaiOauth: boolean;
  copilotOauth: boolean;
};

export const emptyPresence: CredentialPresence = {
  anthropicApiKey: false,
  openaiApiKey: false,
  openrouterApiKey: false,
  xaiApiKey: false,
  anthropicOauth: false,
  openaiOauth: false,
  copilotOauth: false,
};

// savedCredentialAvailable reports whether the user has a saved credential
// for the selected provider/auth-mode combination.
export function savedCredentialAvailable(
  presence: CredentialPresence,
  provider: string,
  authMode: string,
): boolean {
  if (provider === "copilot") return presence.copilotOauth;
  if (provider === "anthropic") {
    return authMode === "oauth" ? presence.anthropicOauth : presence.anthropicApiKey;
  }
  if (provider === "openai") {
    return authMode === "oauth" ? presence.openaiOauth : presence.openaiApiKey;
  }
  if (provider === "openrouter") return presence.openrouterApiKey;
  if (provider === "xai") return presence.xaiApiKey;
  return false;
}

export function savedCredentialSecretName(provider: string): string {
  return `usercred-${provider}`;
}

export function isSavedCredentialSecretName(name: string): boolean {
  return name.startsWith("usercred-");
}

export function providerKeyFor(project: Project, provider: string) {
  return project.providerKeys.find((key) => key.provider.toLowerCase() === provider.toLowerCase());
}

// projectUsesSavedCredentials reports whether the project's current credential
// refs are the deterministic usercred-* Secrets that "Use my saved provider
// credentials" wires up. Initializing the settings toggle from this keeps
// saved-cred projects on the saved path across edits — most importantly across
// provider switches, where stale hardcoded refs from the old provider would
// otherwise be re-submitted verbatim and crash new runs at startup.
export function projectUsesSavedCredentials(
  project: Project,
  provider: string,
  authMode: string,
): boolean {
  if (!savedCredentialProviders.has(provider)) return false;
  const effectiveAuthMode = provider === "copilot" ? "oauth" : authMode;
  if (effectiveAuthMode === "oauth") {
    return project.openaiOauthSecret === savedCredentialSecretName(provider);
  }
  const keyProvider = provider === "anthropic" || provider === "openrouter" || provider === "xai" ? provider : "openai";
  if (provider === "anthropic" && project.claudeApiKeySecret === savedCredentialSecretName("anthropic")) {
    return true;
  }
  return providerKeyFor(project, provider)?.secretName === savedCredentialSecretName(keyProvider);
}

// authModeForProviderSwitch derives the auth mode after a provider change:
// copilot is OAuth-only, providers without OAuth support force api-key, and
// otherwise the previous mode is kept — unless the user is on saved
// credentials and only has the other mode's credential saved for the new
// provider, in which case we flip to it so e.g. copilot->anthropic lands on
// api-key when only an Anthropic API key is saved.
export function authModeForProviderSwitch(
  prevAuthMode: "api-key" | "oauth",
  provider: string,
  useSavedCredentials: boolean,
  credentials: CredentialPresence,
): "api-key" | "oauth" {
  const tentative: "api-key" | "oauth" =
    provider === "copilot" ? "oauth" : oauthProviders.has(provider) ? prevAuthMode : "api-key";
  if (
    savedCredentialProviders.has(provider) &&
    useSavedCredentials &&
    !savedCredentialAvailable(credentials, provider, tentative)
  ) {
    const other: "api-key" | "oauth" = tentative === "oauth" ? "api-key" : "oauth";
    if (savedCredentialAvailable(credentials, provider, other)) return other;
  }
  return tentative;
}

// oauthSecretForProviderSwitch re-derives the OAuth Secret field when the
// provider changes instead of carrying the old provider's ref along: keeping
// e.g. usercred-copilot while switching to anthropic persists wiring that
// crashes every new run at pod startup. Custom (non usercred-*) names are
// preserved; saved-credential names are swapped to the new provider's saved
// secret when one exists, and cleared otherwise so validation demands an
// explicit choice.
export function oauthSecretForProviderSwitch(
  current: string,
  provider: string,
  credentials: CredentialPresence,
): string {
  if (!oauthProviders.has(provider)) return "";
  if (current.trim() && !isSavedCredentialSecretName(current)) return current;
  if (current === savedCredentialSecretName(provider)) return current;
  return savedCredentialProviders.has(provider) && savedCredentialAvailable(credentials, provider, "oauth")
    ? savedCredentialSecretName(provider)
    : "";
}
