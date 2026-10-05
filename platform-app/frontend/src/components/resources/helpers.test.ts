import { describe, expect, it } from "vitest";
import { buildLinearCreateRequest, initialLinearCreateValues } from "@/components/linear-create";
import {
  canCreateResource, canDeleteResource, canMutateResource, countError, durationError, formatProviderModels, joinFacts, keyValueRowsError,
  parseProviderModels, quantityError, recordToRows, regexError, resourceTabs, rowsToRecord, splitTokens,
} from "@/components/resources/resource-helpers";

describe("resource permissions", () => {
  it("lists only supported reusable resources", () => {
    expect(resourceTabs.map(([kind]) => kind)).toEqual([
      "skills", "mcp-servers", "runtime-profiles", "guardrails", "modes", "roles",
    ]);
  });

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
    expect(request.policies?.permissionMode).toBe("workspace-write");
    expect(request.policies?.egressMode).toBe("restricted");
    expect(Object.keys(request.policies ?? {}).sort()).toEqual([
      "$typeName", "configureRuntimeProfile", "egressMode", "permissionMode",
    ]);
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

describe("editor value helpers", () => {
  it("validates key/value rows and converts them to records", () => {
    expect(keyValueRowsError([{ key: "A", value: "1" }, { key: "", value: "" }])).toBeNull();
    expect(keyValueRowsError([{ key: "", value: "1" }], "Variable")).toBe("Variable names cannot be empty.");
    expect(keyValueRowsError([{ key: "A", value: "1" }, { key: " A ", value: "2" }], "Variable")).toBe("Duplicate variable: A");
    expect(rowsToRecord([{ key: " cpu ", value: "500m" }, { key: "", value: "" }])).toEqual({ cpu: "500m" });
    expect(() => rowsToRecord([{ key: "", value: "x" }])).toThrow("Key names cannot be empty.");
    expect(recordToRows({ b: "2", a: "1" })).toEqual([{ key: "a", value: "1" }, { key: "b", value: "2" }]);
  });

  it("validates durations, quantities, counts, and regular expressions", () => {
    expect(durationError("")).toBeNull();
    expect(durationError("1h30m")).toBeNull();
    expect(durationError("90")).toMatch(/Go duration/);
    expect(quantityError("10Gi")).toBeNull();
    expect(quantityError("500m")).toBeNull();
    expect(quantityError("10 GB")).toMatch(/Kubernetes quantity/);
    expect(countError("0", "Max turns")).toBeNull();
    expect(countError("-1", "Max turns")).toMatch(/whole number/);
    expect(countError("1.5", "Max turns")).toMatch(/whole number/);
    expect(regexError("rm\\s+-rf")).toBeNull();
    expect(regexError("(")).toBeTruthy();
  });

  it("splits tokens and joins facts", () => {
    expect(splitTokens("a, b\nc,,")).toEqual(["a", "b", "c"]);
    expect(joinFacts(["x", null, "", false, "y"])).toBe("x · y");
  });
});
