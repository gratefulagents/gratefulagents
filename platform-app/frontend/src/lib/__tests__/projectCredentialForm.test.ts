import { describe, expect, it } from "vitest";

import { create } from "@bufbuild/protobuf";
import type { MessageInitShape } from "@bufbuild/protobuf";

import {
  authModeForProviderSwitch,
  emptyPresence,
  oauthSecretForProviderSwitch,
  projectUsesSavedCredentials,
  savedCredentialAvailable,
} from "@/lib/projectCredentialForm";
import { ProjectSchema } from "@/rpc/platform/service_pb";
import type { Project } from "@/rpc/platform/service_pb";

function project(overrides: MessageInitShape<typeof ProjectSchema> = {}): Project {
  return create(ProjectSchema, {
    name: "payments",
    namespace: "user-ns",
    provider: "copilot",
    authMode: "oauth",
    ...overrides,
  });
}

describe("projectUsesSavedCredentials", () => {
  it("detects saved OAuth wiring for the provider", () => {
    const p = project({ provider: "copilot", openaiOauthSecret: "usercred-copilot" });
    expect(projectUsesSavedCredentials(p, "copilot", "oauth")).toBe(true);
  });

  it("rejects another provider's saved secret", () => {
    const p = project({ provider: "anthropic", openaiOauthSecret: "usercred-copilot" });
    expect(projectUsesSavedCredentials(p, "anthropic", "oauth")).toBe(false);
  });

  it("ignores custom secret names", () => {
    const p = project({ openaiOauthSecret: "my-team-oauth" });
    expect(projectUsesSavedCredentials(p, "copilot", "oauth")).toBe(false);
  });

  it("detects saved API-key wiring via providerKeys", () => {
    const p = project({
      provider: "openai",
      authMode: "api-key",
      providerKeys: [{ provider: "openai", secretName: "usercred-openai", secretKey: "api-key" }],
    });
    expect(projectUsesSavedCredentials(p, "openai", "api-key")).toBe(true);
  });

  it("detects saved anthropic API-key wiring via claudeApiKeySecret", () => {
    const p = project({
      provider: "anthropic",
      authMode: "api-key",
      claudeApiKeySecret: "usercred-anthropic",
    });
    expect(projectUsesSavedCredentials(p, "anthropic", "api-key")).toBe(true);
  });

  it("detects dedicated saved OpenRouter and xAI API-key wiring", () => {
    for (const provider of ["openrouter", "xai"]) {
      const p = project({
        provider,
        authMode: "api-key",
        providerKeys: [{ provider, secretName: `usercred-${provider}`, secretKey: "api-key" }],
      });
      expect(projectUsesSavedCredentials(p, provider, "api-key")).toBe(true);
    }
  });

  it("is false for providers without saved credential support", () => {
    const p = project({ provider: "gemini" });
    expect(projectUsesSavedCredentials(p, "gemini", "api-key")).toBe(false);
  });
});

describe("savedCredentialAvailable", () => {
  it("uses dedicated OpenRouter and xAI presence flags", () => {
    const creds = { ...emptyPresence, openrouterApiKey: true, xaiApiKey: true };
    expect(savedCredentialAvailable(creds, "openrouter", "api-key")).toBe(true);
    expect(savedCredentialAvailable(creds, "xai", "api-key")).toBe(true);
    expect(savedCredentialAvailable(emptyPresence, "openrouter", "api-key")).toBe(false);
    expect(savedCredentialAvailable(emptyPresence, "xai", "api-key")).toBe(false);
  });
});

describe("oauthSecretForProviderSwitch", () => {
  const withCreds = { ...emptyPresence, anthropicOauth: true, copilotOauth: true };

  it("swaps a saved-credential secret to the new provider's saved secret", () => {
    // The reported bug: switching copilot -> anthropic kept usercred-copilot,
    // which crashes every new run at agent startup.
    expect(oauthSecretForProviderSwitch("usercred-copilot", "anthropic", withCreds)).toBe(
      "usercred-anthropic",
    );
  });

  it("clears the secret when the new provider has no saved OAuth credential", () => {
    expect(oauthSecretForProviderSwitch("usercred-copilot", "openai", withCreds)).toBe("");
  });

  it("keeps custom secret names untouched", () => {
    expect(oauthSecretForProviderSwitch("my-team-oauth", "anthropic", withCreds)).toBe("my-team-oauth");
  });

  it("keeps the secret when it already matches the provider", () => {
    expect(oauthSecretForProviderSwitch("usercred-copilot", "copilot", emptyPresence)).toBe(
      "usercred-copilot",
    );
  });

  it("clears the secret for providers without OAuth support", () => {
    expect(oauthSecretForProviderSwitch("usercred-copilot", "gemini", withCreds)).toBe("");
    expect(oauthSecretForProviderSwitch("my-team-oauth", "gemini", withCreds)).toBe("");
  });

  it("fills the saved secret when the field was empty and a saved credential exists", () => {
    expect(oauthSecretForProviderSwitch("", "anthropic", withCreds)).toBe("usercred-anthropic");
    expect(oauthSecretForProviderSwitch("", "anthropic", emptyPresence)).toBe("");
  });
});

describe("authModeForProviderSwitch", () => {
  it("forces oauth for copilot", () => {
    expect(authModeForProviderSwitch("api-key", "copilot", false, emptyPresence)).toBe("oauth");
  });

  it("forces api-key for providers without OAuth support", () => {
    expect(authModeForProviderSwitch("oauth", "gemini", true, emptyPresence)).toBe("api-key");
  });

  it("keeps the previous mode when not using saved credentials", () => {
    expect(authModeForProviderSwitch("oauth", "anthropic", false, emptyPresence)).toBe("oauth");
    expect(authModeForProviderSwitch("api-key", "openai", false, emptyPresence)).toBe("api-key");
  });

  it("falls back to api-key when only an API key is saved (copilot -> anthropic)", () => {
    const creds = { ...emptyPresence, anthropicApiKey: true, copilotOauth: true };
    expect(authModeForProviderSwitch("oauth", "anthropic", true, creds)).toBe("api-key");
  });

  it("falls back to oauth when only an OAuth credential is saved", () => {
    const creds = { ...emptyPresence, openaiOauth: true };
    expect(authModeForProviderSwitch("api-key", "openai", true, creds)).toBe("oauth");
  });

  it("keeps the tentative mode when a matching saved credential exists", () => {
    const creds = { ...emptyPresence, anthropicOauth: true, anthropicApiKey: true };
    expect(authModeForProviderSwitch("oauth", "anthropic", true, creds)).toBe("oauth");
  });

  it("keeps the tentative mode when neither mode has a saved credential", () => {
    expect(authModeForProviderSwitch("oauth", "anthropic", true, emptyPresence)).toBe("oauth");
  });
});
