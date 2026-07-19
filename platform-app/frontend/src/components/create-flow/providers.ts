/** Shared model-provider metadata for create flows. */
export type ProviderMeta = {
  id: string;
  name: string;
  hint: string;
  /** OAuth-only providers don't offer an api-key auth mode in this UI. */
  oauthOnly?: boolean;
  /** savedSupported providers can be wired from the user's saved credentials. */
  savedSupported?: boolean;
};

export const PROVIDERS: ProviderMeta[] = [
  { id: "anthropic", name: "Anthropic", hint: "Claude", savedSupported: true },
  { id: "openai", name: "OpenAI", hint: "GPT", savedSupported: true },
  { id: "copilot", name: "GitHub Copilot", hint: "OAuth", oauthOnly: true, savedSupported: true },
  { id: "gemini", name: "Gemini", hint: "Google" },
  { id: "openrouter", name: "OpenRouter", hint: "Gateway", savedSupported: true },
  { id: "groq", name: "Groq", hint: "Fast inference" },
  { id: "xai", name: "xAI", hint: "Grok", savedSupported: true },
];

export function providerMeta(id: string): ProviderMeta {
  return PROVIDERS.find((p) => p.id === id) ?? PROVIDERS[0];
}

export function providerName(id: string): string {
  return PROVIDERS.find((p) => p.id === id)?.name ?? (id || "Anthropic");
}
