export type RuntimeProfileRow = {
  name: string;
  permissionMode?: unknown;
  egressMode?: unknown;
  defaultTimeout?: unknown;
  sandboxTemplateRef?: unknown;
  runtimeClassName?: unknown;
  warmPoolRef?: unknown;
  persistWorkspace?: unknown;
  enablePrivateProcfs?: unknown;
  workspaceSize?: unknown;
  commandPath?: unknown;
  commandPathPrepend?: unknown;
  commandPathAppend?: unknown;
  extraReadOnlyPaths?: unknown;
  extraWritablePaths?: unknown;
  commandEnv?: unknown;
  resourceRequests?: unknown;
  resourceLimits?: unknown;
  resourceClaims?: unknown;
  maxConcurrentRuns?: unknown;
  perNamespaceMaxConcurrentRuns?: unknown;
  staleRunTimeout?: unknown;
};

const formatList = (value: unknown): string =>
  Array.isArray(value) ? value.map(String).join("\n") : "";

export function formatStringMap(value: unknown): string {
  if (!value || typeof value !== "object" || Array.isArray(value)) return "";
  return JSON.stringify(value, null, 2) ?? "";
}

export function parseStringMap(value: string, label: string): Record<string, string> {
  if (!value.trim()) return {};
  let parsed: unknown;
  try {
    parsed = JSON.parse(value);
  } catch {
    throw new Error(`${label} must be a JSON object`);
  }
  if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) {
    throw new Error(`${label} must be a JSON object`);
  }
  const result: Record<string, string> = {};
  for (const [key, entry] of Object.entries(parsed)) {
    if (!key.trim() || typeof entry !== "string") {
      throw new Error(`${label} keys must be non-empty and values must be strings`);
    }
    result[key.trim()] = entry;
  }
  return result;
}

export type RuntimeResourceClaimInput = { name: string; request: string };

export function formatResourceClaims(value: unknown): string {
  if (!Array.isArray(value) || value.length === 0) return "";
  return JSON.stringify(value, null, 2) ?? "";
}

export function parseResourceClaims(value: string): RuntimeResourceClaimInput[] {
  if (!value.trim()) return [];
  let parsed: unknown;
  try {
    parsed = JSON.parse(value);
  } catch {
    throw new Error("Resource claims must be a JSON array");
  }
  if (!Array.isArray(parsed)) throw new Error("Resource claims must be a JSON array");
  return parsed.map((entry, index) => {
    if (!entry || typeof entry !== "object" || Array.isArray(entry)) {
      throw new Error(`Resource claim ${index + 1} must be an object`);
    }
    const rawName = "name" in entry ? entry.name : undefined;
    const rawRequest = "request" in entry ? entry.request : "";
    if (typeof rawName !== "string" || (rawRequest !== undefined && typeof rawRequest !== "string")) {
      throw new Error(`Resource claim ${index + 1} fields must be strings`);
    }
    const name = rawName.trim();
    const request = (rawRequest ?? "").trim();
    if (!name) throw new Error(`Resource claim ${index + 1} requires a name`);
    return { name, request };
  });
}

export function runtimeProfileFormFromRow(row: RuntimeProfileRow) {
  return {
    name: row.name,
    permissionMode: String(row.permissionMode ?? ""),
    egressMode: String(row.egressMode ?? ""),
    defaultTimeout: String(row.defaultTimeout ?? ""),
    sandboxTemplateRef: String(row.sandboxTemplateRef ?? ""),
    runtimeClassName: String(row.runtimeClassName ?? ""),
    warmPoolRef: String(row.warmPoolRef ?? ""),
    persistWorkspace: Boolean(row.persistWorkspace),
    enablePrivateProcfs: Boolean(row.enablePrivateProcfs),
    workspaceSize: String(row.workspaceSize ?? ""),
    commandPath: formatList(row.commandPath),
    commandPathPrepend: formatList(row.commandPathPrepend),
    commandPathAppend: formatList(row.commandPathAppend),
    extraReadOnlyPaths: formatList(row.extraReadOnlyPaths),
    extraWritablePaths: formatList(row.extraWritablePaths),
    commandEnv: formatStringMap(row.commandEnv),
    resourceRequests: formatStringMap(row.resourceRequests),
    resourceLimits: formatStringMap(row.resourceLimits),
    resourceClaims: formatResourceClaims(row.resourceClaims),
    maxConcurrentRuns: String(row.maxConcurrentRuns ?? 0),
    perNamespaceMaxConcurrentRuns: String(row.perNamespaceMaxConcurrentRuns ?? 0),
    staleRunTimeout: String(row.staleRunTimeout ?? ""),
  };
}

export function parseStringList(value: string): string[] {
  return value.split(/\r?\n/).map((entry) => entry.trim()).filter(Boolean);
}

export const parseExtraWritablePaths = parseStringList;
