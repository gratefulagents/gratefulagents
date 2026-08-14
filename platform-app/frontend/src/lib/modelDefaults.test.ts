import { create } from "@bufbuild/protobuf";
import { describe, expect, it } from "vitest";

import { applyModelDefaults, hasActiveModelDefaults } from "@/lib/modelDefaults";
import { ModelDefaultsSchema } from "@/rpc/platform/service_pb";

const fallback = { provider: "anthropic", model: "", reasoningLevel: "" };

describe("applyModelDefaults", () => {
  it("returns the fallback for null defaults", () => {
    expect(applyModelDefaults(null)).toEqual(fallback);
    expect(applyModelDefaults(undefined)).toEqual(fallback);
  });

  it("returns the fallback for effectively empty defaults", () => {
    expect(applyModelDefaults(create(ModelDefaultsSchema, {}))).toEqual(fallback);
    expect(
      applyModelDefaults(
        create(ModelDefaultsSchema, { provider: "  ", model: " ", reasoningLevel: "" }),
      ),
    ).toEqual(fallback);
  });

  it("returns the fallback when defaults are disabled", () => {
    expect(
      applyModelDefaults(
        create(ModelDefaultsSchema, {
          provider: "openai",
          model: "gpt-5",
          reasoningLevel: "high",
          disabled: true,
        }),
      ),
    ).toEqual(fallback);
  });

  it("returns trimmed saved values", () => {
    expect(
      applyModelDefaults(
        create(ModelDefaultsSchema, {
          provider: " openai ",
          model: " gpt-5 ",
          reasoningLevel: "high",
        }),
      ),
    ).toEqual({ provider: "openai", model: "gpt-5", reasoningLevel: "high" });
  });

  it("falls back to anthropic when only a model is saved", () => {
    expect(applyModelDefaults(create(ModelDefaultsSchema, { model: "claude-opus-4-6" }))).toEqual({
      provider: "anthropic",
      model: "claude-opus-4-6",
      reasoningLevel: "",
    });
  });
});

describe("hasActiveModelDefaults", () => {
  it("is false for null, empty, or disabled defaults", () => {
    expect(hasActiveModelDefaults(null)).toBe(false);
    expect(hasActiveModelDefaults(create(ModelDefaultsSchema, {}))).toBe(false);
    expect(
      hasActiveModelDefaults(create(ModelDefaultsSchema, { model: "gpt-5", disabled: true })),
    ).toBe(false);
  });

  it("is true when any value is saved and not disabled", () => {
    expect(hasActiveModelDefaults(create(ModelDefaultsSchema, { provider: "openai" }))).toBe(true);
    expect(hasActiveModelDefaults(create(ModelDefaultsSchema, { reasoningLevel: "low" }))).toBe(
      true,
    );
  });
});
