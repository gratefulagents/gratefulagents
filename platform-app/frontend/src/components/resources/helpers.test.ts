import { describe, expect, it } from "vitest";
import { buildLinearCreateRequest, initialLinearCreateValues } from "@/components/linear-create";
import { canCreateResource, canDeleteResource, canMutateResource, formatProviderModels, parseProviderModels } from "@/components/resources/resource-helpers";

describe("resource permissions", () => {
  it("lets members create and edit modes while reserving deletion for admins", () => {
    expect(canCreateResource("modes", "member")).toBe(true);
    expect(canMutateResource("modes", "member")).toBe(true);
    expect(canDeleteResource("modes", "member")).toBe(false);
    expect(canCreateResource("roles", undefined)).toBe(false);
    expect(canMutateResource("roles", undefined)).toBe(false);
    expect(canDeleteResource("roles", undefined)).toBe(false);
    expect(canCreateResource("modes", "admin")).toBe(true);
    expect(canMutateResource("modes", "admin")).toBe(true);
    expect(canDeleteResource("modes", "admin")).toBe(true);
    expect(canMutateResource("runtime-profiles", "member")).toBe(true);
    expect(canDeleteResource("runtime-profiles", "member")).toBe(true);
  });
});

describe("Linear create payload", () => {
  it("normalizes fields and includes practical run defaults", () => {
    const request = buildLinearCreateRequest({
      ...initialLinearCreateValues,
      name: " project-agent ",
      linearApiKey: " lin_api ",
      projectId: " project-id ",
      teamId: " team-id ",
      model: " claude-sonnet-4-6 ",
    });
    expect(request.name).toBe("project-agent");
    expect(request.linearApiKey).toBe("lin_api");
    expect(request.projectId).toBe("project-id");
    expect(request.teamId).toBe("team-id");
    expect(request.useSavedCredentials).toBe(true);
    expect(request.defaults?.model).toBe("claude-sonnet-4-6");
    expect(request.defaults?.provider).toBe("anthropic");
    expect(request.defaults?.authMode).toBe("api-key");
    expect(request.policies?.configureRuntimeProfile).toBe(true);
    expect(request.policies?.configureMcpPolicy).toBe(false);
  });
});

describe("provider model fields", () => {
  it("parses normalized providers and preserves equals signs in model values", () => {
    expect(parseProviderModels(" OpenAI = gpt-5.6-sol, copilot=model=variant ")).toEqual({
      openai: "gpt-5.6-sol",
      copilot: "model=variant",
    });
  });

  it.each(["openai", "openai=", "=gpt-5.6-sol", "openai=a, OpenAI=b"])("rejects invalid mapping %s", (value) => {
    expect(() => parseProviderModels(value)).toThrow();
  });

  it("formats mappings in deterministic provider order", () => {
    expect(formatProviderModels({ openai: "gpt-5.6-sol", anthropic: "luna" })).toBe("anthropic=luna, openai=gpt-5.6-sol");
  });
});
