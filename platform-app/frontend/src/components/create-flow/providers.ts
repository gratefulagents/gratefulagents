import { MODEL_PROVIDERS } from "@/lib/model-providers";

/** Shared model-provider metadata for create flows, derived from the registry. */
export type ProviderMeta = {
  id: string;
  name: string;
  hint: string;
  /** OAuth-only providers don't offer an api-key auth mode in this UI. */
  oauthOnly?: boolean;
  /** savedSupported providers can be wired from the user's saved credentials. */
  savedSupported?: boolean;
};

export const PROVIDERS: ProviderMeta[] = MODEL_PROVIDERS.map((p) => ({
  id: p.id,
  name: p.label,
  hint: p.hint,
  oauthOnly: !p.authModes.includes("api-key"),
  savedSupported: p.userCredentials,
}));

export function providerMeta(id: string): ProviderMeta {
  return PROVIDERS.find((p) => p.id === id) ?? PROVIDERS[0];
}

export function providerName(id: string): string {
  return PROVIDERS.find((p) => p.id === id)?.name ?? (id || "Anthropic");
}
