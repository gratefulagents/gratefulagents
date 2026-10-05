export const resourceTabs = [
  ["skills", "Skills"], ["mcp-servers", "MCP servers"], ["runtime-profiles", "Runtime profiles"],
  ["guardrails", "Guardrails"], ["modes", "Modes"], ["roles", "Roles"],
] as const;
export type ResourceKind = (typeof resourceTabs)[number][0];

/** Copy shared by the tab strip, section intros, and dialogs for each kind. */
export const resourceMeta: Record<ResourceKind, { label: string; singular: string; description: string }> = {
  skills: {
    label: "Skills",
    singular: "skill",
    description:
      "Reusable agent instructions, written inline or installed from skills.sh. Installing adds a skill to your library, not to every project: enable it per project under Tools & skills, or let a mode attach it by default.",
  },
  "mcp-servers": {
    label: "MCP servers",
    singular: "MCP server",
    description:
      "Tool servers runs can connect to: a stdio command plus credentials from your saved integrations. Attach them per project, per trigger, or through a mode's defaults.",
  },
  "runtime-profiles": {
    label: "Runtime profiles",
    singular: "runtime profile",
    description:
      "Sandbox, network, and workspace defaults for run pods. Projects and triggers reference a profile by name; the most restrictive of the profile and the mode wins.",
  },
  guardrails: {
    label: "Guardrails",
    singular: "guardrail policy",
    description:
      "Regular-expression rules that inspect tool input and output, then block, warn, or log when they match.",
  },
  modes: {
    label: "Modes",
    singular: "mode",
    description:
      "Behavior templates a run starts from: the system instructions, autonomy, execution strategy, default tools, and limits.",
  },
  roles: {
    label: "Roles",
    singular: "role",
    description:
      "Prompts and tool boundaries for the specialist sub-agents a run can delegate to. The role name must match the agent catalog entry it instructs.",
  },
};

export function canCreateResource(kind: ResourceKind, role?: string) {
  return kind !== "roles" || role === "admin";
}

export function canMutateResource(kind: ResourceKind, role?: string) {
  return kind !== "roles" || role === "admin";
}

export function canDeleteResource(kind: ResourceKind, role?: string) {
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

/* ── Shared editor value helpers ──────────────────────────────── */

export type KeyValueRow = { key: string; value: string };

export function recordToRows(record?: Record<string, string> | null): KeyValueRow[] {
  return Object.entries(record ?? {})
    .sort(([left], [right]) => left.localeCompare(right))
    .map(([key, value]) => ({ key, value }));
}

/**
 * Validates key/value editor rows. Returns null when every key is present and
 * unique; otherwise a message naming the problem. Fully blank rows are
 * ignored so an "Add" click never blocks saving on its own.
 */
export function keyValueRowsError(rows: KeyValueRow[], noun = "Key"): string | null {
  const active = rows.filter((row) => row.key.trim() || row.value.trim());
  if (active.some((row) => !row.key.trim())) return `${noun} names cannot be empty.`;
  const keys = active.map((row) => row.key.trim());
  const duplicates = [...new Set(keys.filter((key, index) => keys.indexOf(key) !== index))];
  if (duplicates.length) {
    return `Duplicate ${noun.toLowerCase()}${duplicates.length === 1 ? "" : "s"}: ${duplicates.join(", ")}`;
  }
  return null;
}

/** Converts editor rows to a record, dropping fully blank rows. Throws on invalid rows. */
export function rowsToRecord(rows: KeyValueRow[], noun = "Key"): Record<string, string> {
  const error = keyValueRowsError(rows, noun);
  if (error) throw new Error(error);
  const record: Record<string, string> = {};
  for (const row of rows) {
    if (!row.key.trim() && !row.value.trim()) continue;
    record[row.key.trim()] = row.value;
  }
  return record;
}

/** Go duration syntax accepted by the API (e.g. 30m, 1h30m, 90s). */
const GO_DURATION_RE = /^(\d+(\.\d+)?(ns|us|µs|ms|s|m|h))+$/;

export function durationError(value: string, label = "Duration"): string | null {
  const trimmed = value.trim();
  if (!trimmed) return null;
  if (!GO_DURATION_RE.test(trimmed)) return `${label} must be a Go duration such as 30m, 1h, or 1h30m.`;
  return null;
}

/** Kubernetes quantity syntax (10Gi, 500m, 2). Mirrors the API's workspace size pattern. */
const QUANTITY_RE = /^[0-9]+(\.[0-9]+)?(Ki|Mi|Gi|Ti|Pi|Ei|m|k|M|G|T|P|E)?$/;

export function quantityError(value: string, label = "Size"): string | null {
  const trimmed = value.trim();
  if (!trimmed) return null;
  if (!QUANTITY_RE.test(trimmed)) return `${label} must be a Kubernetes quantity such as 10Gi, 500m, or 2.`;
  return null;
}

export function regexError(value: string): string | null {
  try {
    new RegExp(value);
    return null;
  } catch (cause) {
    return cause instanceof Error ? cause.message.replace(/^Invalid regular expression: /, "") : "Invalid regular expression.";
  }
}

/** Non-negative integer check for limit fields where 0 means "platform default". */
export function countError(value: string, label: string): string | null {
  const trimmed = value.trim();
  if (!trimmed) return null;
  if (!/^\d+$/.test(trimmed)) return `${label} must be a whole number (0 keeps the default).`;
  return null;
}

/** Splits comma- or newline-separated tokens, trimming blanks. */
export function splitTokens(value: string): string[] {
  return value.split(/[,\n]/).map((token) => token.trim()).filter(Boolean);
}

/** Compact joiner for list rows: "a · b · c" with blanks removed. */
export function joinFacts(facts: Array<string | null | undefined | false>): string {
  return facts.filter(Boolean).join(" · ");
}
