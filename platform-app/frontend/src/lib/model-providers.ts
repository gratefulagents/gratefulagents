/**
 * Single registry of every model provider the app knows about. Create flows
 * (run/project dialogs), the onboarding wizard, and settings all derive their
 * provider lists and credential checks from here, so a new provider needs
 * exactly one entry.
 */

export type ProviderAuthMode = "api-key" | "oauth";

export interface ModelProviderDef {
  id: string;
  /** Display label, e.g. "Anthropic". */
  label: string;
  /** Short qualifier shown next to the label in pickers, e.g. "Claude". */
  hint: string;
  /** Auth modes the platform supports for this provider. */
  authModes: readonly ProviderAuthMode[];
  /**
   * Whether users can save a credential for this provider (Settings →
   * Credentials, onboarding). Providers without a credential surface are
   * run-dialog-only: selectable there with an ad-hoc key.
   */
  userCredentials: boolean;
}

export const MODEL_PROVIDERS: readonly ModelProviderDef[] = [
  { id: "anthropic", label: "Anthropic", hint: "Claude", authModes: ["api-key", "oauth"], userCredentials: true },
  { id: "openai", label: "OpenAI", hint: "GPT", authModes: ["api-key", "oauth"], userCredentials: true },
  { id: "copilot", label: "GitHub Copilot", hint: "OAuth", authModes: ["oauth"], userCredentials: true },
  { id: "gemini", label: "Gemini", hint: "Google", authModes: ["api-key"], userCredentials: false },
  { id: "openrouter", label: "OpenRouter", hint: "Gateway", authModes: ["api-key"], userCredentials: true },
  { id: "groq", label: "Groq", hint: "Fast inference", authModes: ["api-key"], userCredentials: false },
  { id: "xai", label: "xAI", hint: "Grok", authModes: ["api-key"], userCredentials: true },
];

export function modelProvider(id: string): ModelProviderDef | undefined {
  return MODEL_PROVIDERS.find((p) => p.id === id);
}

export function providerLabel(id: string): string {
  return modelProvider(id)?.label ?? (id || "Anthropic");
}

/**
 * Saved-credential presence flags as reported by ListMyCredentials and
 * UpdateMyCredentials. Structurally satisfied by the generated MyCredentials
 * message and by OAuth onSaved payloads.
 */
export interface ProviderCredentialFlags {
  anthropicApiKeyPresent: boolean;
  anthropicOauthPresent: boolean;
  openaiApiKeyPresent: boolean;
  openaiOauthPresent: boolean;
  openrouterApiKeyPresent: boolean;
  copilotOauthPresent: boolean;
  xaiApiKeyPresent: boolean;
}

const CREDENTIAL_FLAG_FIELDS: Record<
  string,
  Partial<Record<ProviderAuthMode, keyof ProviderCredentialFlags>>
> = {
  anthropic: { "api-key": "anthropicApiKeyPresent", oauth: "anthropicOauthPresent" },
  openai: { "api-key": "openaiApiKeyPresent", oauth: "openaiOauthPresent" },
  copilot: { oauth: "copilotOauthPresent" },
  openrouter: { "api-key": "openrouterApiKeyPresent" },
  xai: { "api-key": "xaiApiKeyPresent" },
};

/** Adapts the client-side CredentialPresence shape (lib/onboarding) to flags. */
export function flagsFromPresence(p: {
  anthropicApiKey: boolean;
  anthropicOauth: boolean;
  openaiApiKey: boolean;
  openaiOauth: boolean;
  openrouterApiKey: boolean;
  copilotOauth: boolean;
  xaiApiKey: boolean;
}): ProviderCredentialFlags {
  return {
    anthropicApiKeyPresent: p.anthropicApiKey,
    anthropicOauthPresent: p.anthropicOauth,
    openaiApiKeyPresent: p.openaiApiKey,
    openaiOauthPresent: p.openaiOauth,
    openrouterApiKeyPresent: p.openrouterApiKey,
    copilotOauthPresent: p.copilotOauth,
    xaiApiKeyPresent: p.xaiApiKey,
  };
}

/** Auth modes the user holds a saved credential for, in declaration order. */
export function credentialAuthModes(
  credentials: ProviderCredentialFlags,
  providerId: string,
): ProviderAuthMode[] {
  const fields = CREDENTIAL_FLAG_FIELDS[providerId];
  const modes = modelProvider(providerId)?.authModes;
  if (!fields || !modes) return [];
  return modes.filter((mode) => {
    const field = fields[mode];
    return field ? credentials[field] : false;
  });
}

/** Whether a saved credential exists for the provider (optionally a specific auth mode). */
export function hasProviderCredential(
  credentials: ProviderCredentialFlags,
  providerId: string,
  authMode?: ProviderAuthMode,
): boolean {
  const modes = credentialAuthModes(credentials, providerId);
  return authMode ? modes.includes(authMode) : modes.length > 0;
}

/** Best auth mode: the first with a saved credential, else the provider's first supported mode. */
export function preferredAuthMode(
  credentials: ProviderCredentialFlags,
  providerId: string,
): ProviderAuthMode {
  return (
    credentialAuthModes(credentials, providerId)[0] ??
    modelProvider(providerId)?.authModes[0] ??
    "api-key"
  );
}
