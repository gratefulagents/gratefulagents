import { describe, expect, it } from "vitest";

import {
  activeGroups,
  emptyRunOverrides,
  modelReceipt,
  overrideRequestFields,
  prefixedModel,
  repositoryReceipt,
  shortRepo,
  validateOverseer,
} from "./runOverrides";

const project = {
  namespace: "team",
  provider: "anthropic",
  model: "claude-sonnet",
  repoUrl: "https://github.com/acme/repo",
  baseBranch: "main",
  additionalRepoUrls: ["https://github.com/acme/sdk"],
  image: "",
};

describe("runOverrides", () => {
  it("submits only the request and project when nothing is overridden", () => {
    expect(overrideRequestFields(emptyRunOverrides(), project)).toEqual({ model: "" });
    expect(activeGroups(emptyRunOverrides(), project)).toEqual([]);
  });

  it("prefixes bare models with the effective provider", () => {
    const o = { ...emptyRunOverrides(), model: "opus" };
    expect(prefixedModel(o, project)).toBe("anthropic/opus");
    expect(prefixedModel({ ...o, provider: "openai" }, project)).toBe("openai/opus");
    expect(prefixedModel({ ...o, model: "z-ai/glm" }, project)).toBe("z-ai/glm");
    expect(prefixedModel(o, undefined)).toBe("opus");
  });

  it("sends repository fields only once explicitly edited", () => {
    const branchOnly = { ...emptyRunOverrides(), baseBranch: "feature" };
    const fields = overrideRequestFields(branchOnly, project);
    expect(fields.baseBranch).toBe("feature");
    expect(fields.repoUrl).toBeUndefined();
    expect(fields.additionalRepoUrls).toBeUndefined();
    expect(activeGroups(branchOnly, project)).toEqual(["repository"]);
  });

  it("validates and serialises the overseer, keeping an explicit zero cap", () => {
    const o = emptyRunOverrides();
    o.overseer = {
      ...o.overseer,
      enabled: true,
      modeRefName: "review",
      modeRefVersion: "v3",
      modeRefChannel: "stable",
      model: "opus",
      authority: "enforce",
      intervalMinutes: "30",
      maxInterventions: "0",
    };
    expect(validateOverseer(o.overseer)).toBeNull();
    expect(overrideRequestFields(o, project).overseer).toEqual({
      modeRefName: "review",
      modeRefVersion: "v3",
      modeRefChannel: "stable",
      model: "opus",
      authority: "enforce",
      intervalMinutes: 30,
      maxInterventions: 0,
    });
    expect(validateOverseer({ ...o.overseer, modeRefName: "" })).toMatch(/mode name/);
    expect(validateOverseer({ ...o.overseer, intervalMinutes: "0" })).toMatch(/interval/);
    expect(validateOverseer({ ...o.overseer, maxInterventions: "101" })).toMatch(/max interventions/);
  });

  it("renders receipts against the project's defaults", () => {
    const o = emptyRunOverrides();
    expect(modelReceipt(o, project, "Anthropic")).toBe("Anthropic · claude-sonnet");
    expect(modelReceipt(o, { ...project, model: "anthropic/claude-sonnet" }, "Anthropic")).toBe(
      "Anthropic · claude-sonnet",
    );
    expect(modelReceipt({ ...o, provider: "openai" }, project, "OpenAI")).toBe("OpenAI · provider default");
    expect(repositoryReceipt(o, project)).toBe("acme/repo @ main · +1 more");
    expect(repositoryReceipt({ ...o, baseBranch: "hotfix" }, project)).toBe("acme/repo @ hotfix · +1 more");
    expect(shortRepo("git@github.com:acme/repo.git")).toBe("acme/repo");
  });
});
