import type { ModelDefaults } from "@/rpc/platform/service_pb";

/** Values a fresh creation form is seeded with. */
export type AppliedModelDefaults = {
  provider: string;
  model: string;
  reasoningLevel: string;
};

export const FALLBACK_MODEL_DEFAULTS: AppliedModelDefaults = {
  provider: "anthropic",
  model: "",
  reasoningLevel: "",
};

/**
 * hasActiveModelDefaults reports whether the user's saved model defaults
 * should be auto-applied to new projects, triggers, and scan configs (never
 * runs, which follow their project): they exist, are not disabled, and
 * carry at least one non-empty value.
 */
export function hasActiveModelDefaults(defaults: ModelDefaults | null | undefined): boolean {
  if (!defaults || defaults.disabled) return false;
  return Boolean(
    defaults.provider.trim() || defaults.model.trim() || defaults.reasoningLevel.trim(),
  );
}

/**
 * applyModelDefaults resolves the user's saved model defaults into the values
 * a fresh creation form should start with. When there are no active defaults
 * (null, disabled, or effectively empty) it returns the platform fallback:
 * provider "anthropic" with empty model and reasoning level.
 */
export function applyModelDefaults(
  defaults: ModelDefaults | null | undefined,
): AppliedModelDefaults {
  if (!hasActiveModelDefaults(defaults) || !defaults) return { ...FALLBACK_MODEL_DEFAULTS };
  return {
    provider: defaults.provider.trim() || FALLBACK_MODEL_DEFAULTS.provider,
    model: defaults.model.trim(),
    reasoningLevel: defaults.reasoningLevel.trim(),
  };
}
