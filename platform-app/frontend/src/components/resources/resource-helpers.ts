export const resourceTabs = [
  ["skills", "Skills"], ["mcp-servers", "MCP servers"], ["runtime-profiles", "Runtime profiles"],
  ["mcp-policies", "MCP policies"], ["guardrails", "Guardrails"], ["modes", "Modes"], ["roles", "Roles"],
] as const;
export type ResourceKind = (typeof resourceTabs)[number][0];

export function canCreateResource(kind: ResourceKind, role?: string) {
  return kind !== "roles" || role === "admin";
}

export function canMutateResource(kind: ResourceKind, role?: string) {
  return (kind !== "modes" && kind !== "roles") || role === "admin";
}

export function formatProviderModels(models?: Record<string, string>) {
  return Object.entries(models ?? {})
    .sort(([left], [right]) => left.localeCompare(right))
    .map(([provider, model]) => `${provider}=${model}`)
    .join(", ");
}

export function parseProviderModels(value: string): Record<string, string> {
  const models: Record<string, string> = {};
  for (const rawEntry of value.split(",")) {
    const entry = rawEntry.trim();
    if (!entry) continue;
    const separator = entry.indexOf("=");
    if (separator < 0) {
      throw new Error(`Provider model entry "${entry}" must use provider=model.`);
    }
    const provider = entry.slice(0, separator).trim().toLowerCase();
    const model = entry.slice(separator + 1).trim();
    if (!provider || !model) {
      throw new Error(`Provider model entry "${entry}" must include both provider and model.`);
    }
    if (Object.hasOwn(models, provider)) {
      throw new Error(`Provider model for "${provider}" is duplicated.`);
    }
    models[provider] = model;
  }
  return models;
}
