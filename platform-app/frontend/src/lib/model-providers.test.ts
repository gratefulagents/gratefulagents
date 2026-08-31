import { describe, expect, it } from "vitest";

import { PROVIDERS } from "@/components/create-flow/providers";
import {
  MODEL_PROVIDERS,
  credentialAuthModes,
  flagsFromPresence,
  hasProviderCredential,
  modelProvider,
  preferredAuthMode,
  providerLabel,
  type ProviderCredentialFlags,
} from "@/lib/model-providers";

const noCredentials: ProviderCredentialFlags = {
  anthropicApiKeyPresent: false,
  anthropicOauthPresent: false,
  openaiApiKeyPresent: false,
  openaiOauthPresent: false,
  openrouterApiKeyPresent: false,
  copilotOauthPresent: false,
  xaiApiKeyPresent: false,
};

describe("MODEL_PROVIDERS registry", () => {
  it("knows every provider the platform supports, including xAI", () => {
    expect(MODEL_PROVIDERS.map((p) => p.id)).toEqual([
      "anthropic",
      "openai",
      "copilot",
      "gemini",
      "openrouter",
      "groq",
      "xai",
    ]);
    expect(modelProvider("xai")?.label).toBe("xAI");
    expect(providerLabel("xai")).toBe("xAI");
    expect(providerLabel("unknown")).toBe("unknown");
    expect(providerLabel("")).toBe("Anthropic");
  });

  it("keeps gemini and groq run-dialog-only (no user-credential surface)", () => {
    expect(modelProvider("gemini")?.userCredentials).toBe(false);
    expect(modelProvider("groq")?.userCredentials).toBe(false);
    expect(modelProvider("xai")?.userCredentials).toBe(true);
  });

  it("drives the run-dialog PROVIDERS list from the registry", () => {
    expect(PROVIDERS.map((p) => p.id)).toEqual(MODEL_PROVIDERS.map((p) => p.id));
    expect(PROVIDERS.map((p) => p.name)).toEqual(MODEL_PROVIDERS.map((p) => p.label));
    const byId = Object.fromEntries(PROVIDERS.map((p) => [p.id, p]));
    expect(byId.copilot.oauthOnly).toBe(true);
    expect(byId.anthropic.oauthOnly).toBe(false);
    expect(byId.xai.savedSupported).toBe(true);
    expect(byId.gemini.savedSupported).toBe(false);
    expect(byId.groq.savedSupported).toBe(false);
  });
});

describe("credential detection", () => {
  it("lists auth modes backed by saved credentials in declaration order", () => {
    expect(credentialAuthModes(noCredentials, "anthropic")).toEqual([]);
    expect(
      credentialAuthModes(
        { ...noCredentials, anthropicApiKeyPresent: true, anthropicOauthPresent: true },
        "anthropic",
      ),
    ).toEqual(["api-key", "oauth"]);
    expect(credentialAuthModes({ ...noCredentials, xaiApiKeyPresent: true }, "xai")).toEqual([
      "api-key",
    ]);
    expect(credentialAuthModes({ ...noCredentials, copilotOauthPresent: true }, "copilot")).toEqual(
      ["oauth"],
    );
    expect(credentialAuthModes({ ...noCredentials, xaiApiKeyPresent: true }, "gemini")).toEqual([]);
    expect(credentialAuthModes(noCredentials, "unknown")).toEqual([]);
  });

  it("checks presence per provider and optionally per auth mode", () => {
    const creds = { ...noCredentials, openaiOauthPresent: true, xaiApiKeyPresent: true };
    expect(hasProviderCredential(creds, "openai")).toBe(true);
    expect(hasProviderCredential(creds, "openai", "oauth")).toBe(true);
    expect(hasProviderCredential(creds, "openai", "api-key")).toBe(false);
    expect(hasProviderCredential(creds, "xai")).toBe(true);
    expect(hasProviderCredential(creds, "xai", "api-key")).toBe(true);
    expect(hasProviderCredential(creds, "anthropic")).toBe(false);
  });

  it("prefers a saved auth mode and falls back to the provider's first supported mode", () => {
    expect(preferredAuthMode({ ...noCredentials, anthropicOauthPresent: true }, "anthropic")).toBe(
      "oauth",
    );
    expect(
      preferredAuthMode({ ...noCredentials, anthropicApiKeyPresent: true }, "anthropic"),
    ).toBe("api-key");
    expect(preferredAuthMode(noCredentials, "copilot")).toBe("oauth");
    expect(preferredAuthMode(noCredentials, "xai")).toBe("api-key");
    expect(preferredAuthMode(noCredentials, "")).toBe("api-key");
  });

  it("adapts the client-side CredentialPresence shape", () => {
    const flags = flagsFromPresence({
      anthropicApiKey: false,
      anthropicOauth: true,
      openaiApiKey: false,
      openaiOauth: false,
      openrouterApiKey: false,
      copilotOauth: false,
      xaiApiKey: true,
    });
    expect(hasProviderCredential(flags, "anthropic", "oauth")).toBe(true);
    expect(hasProviderCredential(flags, "xai")).toBe(true);
    expect(hasProviderCredential(flags, "openrouter")).toBe(false);
  });
});
