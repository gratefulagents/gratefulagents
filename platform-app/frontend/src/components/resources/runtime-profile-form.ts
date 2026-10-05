import { resourceNameError } from "@/lib/resourceNames";
import type { RuntimeProfile } from "@/rpc/platform/service_pb";
import {
  countError,
  durationError,
  keyValueRowsError,
  quantityError,
  recordToRows,
  rowsToRecord,
  type KeyValueRow,
} from "@/components/resources/resource-helpers";

/**
 * Editor state for a runtime profile. Maps become key/value rows, path lists
 * become one-path-per-line strings, and numbers stay strings so the inputs
 * can hold partial edits without coercion. Resource claims are deliberately
 * absent: the API rejects them from the dashboard and preserves whatever is
 * already on the object.
 */
export type RuntimeProfileDraft = {
  name: string;
  permissionMode: string;
  gitRemoteWrites: string;
  egressMode: string;
  defaultTimeout: string;
  persistWorkspace: boolean;
  workspaceSize: string;
  enablePrivateProcfs: boolean;
  sandboxTemplateRef: string;
  runtimeClassName: string;
  warmPoolRef: string;
  commandPath: string;
  commandPathPrepend: string;
  commandPathAppend: string;
  extraReadOnlyPaths: string;
  extraWritablePaths: string;
  commandEnv: KeyValueRow[];
  resourceRequests: KeyValueRow[];
  resourceLimits: KeyValueRow[];
  maxConcurrentRuns: string;
  perNamespaceMaxConcurrentRuns: string;
  staleRunTimeout: string;
};

export type RuntimeProfileDraftErrors = Partial<Record<keyof RuntimeProfileDraft, string>>;

export const PATH_LIST_FIELDS = [
  "commandPath",
  "commandPathPrepend",
  "commandPathAppend",
  "extraReadOnlyPaths",
  "extraWritablePaths",
] as const satisfies ReadonlyArray<keyof RuntimeProfileDraft>;

const listToLines = (value: unknown): string => (Array.isArray(value) ? value.map(String).join("\n") : "");

export function parseStringList(value: string): string[] {
  return value.split(/\r?\n/).map((entry) => entry.trim()).filter(Boolean);
}

export function emptyRuntimeProfileDraft(): RuntimeProfileDraft {
  return {
    name: "",
    permissionMode: "workspace-write",
    gitRemoteWrites: "enabled",
    egressMode: "restricted",
    defaultTimeout: "",
    persistWorkspace: false,
    workspaceSize: "",
    enablePrivateProcfs: false,
    sandboxTemplateRef: "",
    runtimeClassName: "",
    warmPoolRef: "",
    commandPath: "",
    commandPathPrepend: "",
    commandPathAppend: "",
    extraReadOnlyPaths: "",
    extraWritablePaths: "",
    commandEnv: [],
    resourceRequests: [],
    resourceLimits: [],
    maxConcurrentRuns: "0",
    perNamespaceMaxConcurrentRuns: "0",
    staleRunTimeout: "",
  };
}

/** Subset of RuntimeProfile the editor reads; accepts the proto or a plain row. */
export type RuntimeProfileSource = Partial<
  Pick<
    RuntimeProfile,
    | "name" | "permissionMode" | "gitRemoteWrites" | "egressMode" | "defaultTimeout" | "persistWorkspace"
    | "workspaceSize" | "enablePrivateProcfs" | "sandboxTemplateRef" | "runtimeClassName" | "warmPoolRef"
    | "commandPath" | "commandPathPrepend" | "commandPathAppend" | "extraReadOnlyPaths" | "extraWritablePaths"
    | "commandEnv" | "resourceRequests" | "resourceLimits" | "maxConcurrentRuns" | "perNamespaceMaxConcurrentRuns"
    | "staleRunTimeout"
  >
> & { name: string };

export function draftFromProfile(profile: RuntimeProfileSource): RuntimeProfileDraft {
  return {
    name: profile.name,
    permissionMode: profile.permissionMode || "workspace-write",
    gitRemoteWrites: profile.gitRemoteWrites || "enabled",
    egressMode: profile.egressMode || "restricted",
    defaultTimeout: profile.defaultTimeout ?? "",
    persistWorkspace: Boolean(profile.persistWorkspace),
    workspaceSize: profile.workspaceSize ?? "",
    enablePrivateProcfs: Boolean(profile.enablePrivateProcfs),
    sandboxTemplateRef: profile.sandboxTemplateRef ?? "",
    runtimeClassName: profile.runtimeClassName ?? "",
    warmPoolRef: profile.warmPoolRef ?? "",
    commandPath: listToLines(profile.commandPath),
    commandPathPrepend: listToLines(profile.commandPathPrepend),
    commandPathAppend: listToLines(profile.commandPathAppend),
    extraReadOnlyPaths: listToLines(profile.extraReadOnlyPaths),
    extraWritablePaths: listToLines(profile.extraWritablePaths),
    commandEnv: recordToRows(profile.commandEnv),
    resourceRequests: recordToRows(profile.resourceRequests),
    resourceLimits: recordToRows(profile.resourceLimits),
    maxConcurrentRuns: String(profile.maxConcurrentRuns ?? 0),
    perNamespaceMaxConcurrentRuns: String(profile.perNamespaceMaxConcurrentRuns ?? 0),
    staleRunTimeout: profile.staleRunTimeout ?? "",
  };
}

/** First invalid line in a one-path-per-line field, or null. */
export function pathListError(value: string): string | null {
  const bad = parseStringList(value).find((line) => !line.startsWith("/"));
  return bad ? `"${bad}" must be an absolute path starting with /.` : null;
}

/** First invalid Kubernetes quantity among the row values, or null. */
export function quantityRowsError(rows: KeyValueRow[]): string | null {
  for (const row of rows) {
    if (!row.key.trim() && !row.value.trim()) continue;
    if (!row.value.trim()) return `${row.key.trim() || "Resource"} needs a quantity such as 500m or 2Gi.`;
    const problem = quantityError(row.value, row.key.trim() || "Quantity");
    if (problem) return problem;
  }
  return null;
}

function refError(value: string, label: string): string | null {
  const trimmed = value.trim();
  if (!trimmed) return null;
  return resourceNameError(trimmed) ? `${label} must be a valid resource name (lowercase letters, digits, hyphens).` : null;
}

/**
 * Field-level validation. `creating` adds the name format check that only
 * applies before the object exists (names are locked afterwards).
 */
export function runtimeProfileDraftErrors(draft: RuntimeProfileDraft, creating = true): RuntimeProfileDraftErrors {
  const errors: RuntimeProfileDraftErrors = {};
  const set = (field: keyof RuntimeProfileDraft, message: string | null) => {
    if (message) errors[field] = message;
  };
  if (!draft.name.trim()) errors.name = "A name is required.";
  else if (creating) set("name", resourceNameError(draft.name.trim()));
  set("defaultTimeout", durationError(draft.defaultTimeout, "Default timeout"));
  set("staleRunTimeout", durationError(draft.staleRunTimeout, "Stale run timeout"));
  if (draft.persistWorkspace) set("workspaceSize", quantityError(draft.workspaceSize, "Workspace size"));
  set("sandboxTemplateRef", refError(draft.sandboxTemplateRef, "Sandbox template"));
  set("runtimeClassName", refError(draft.runtimeClassName, "Runtime class"));
  set("warmPoolRef", refError(draft.warmPoolRef, "Warm pool"));
  for (const field of PATH_LIST_FIELDS) set(field, pathListError(draft[field]));
  set("commandEnv", keyValueRowsError(draft.commandEnv, "Variable"));
  set("resourceRequests", keyValueRowsError(draft.resourceRequests, "Resource") ?? quantityRowsError(draft.resourceRequests));
  set("resourceLimits", keyValueRowsError(draft.resourceLimits, "Resource") ?? quantityRowsError(draft.resourceLimits));
  set("maxConcurrentRuns", countError(draft.maxConcurrentRuns, "Max concurrent runs"));
  set("perNamespaceMaxConcurrentRuns", countError(draft.perNamespaceMaxConcurrentRuns, "Per-namespace max concurrent runs"));
  return errors;
}

/** Plain init object for `create(RuntimeProfileSchema, …)`. Throws on the first validation error. */
export function profileFromDraft(draft: RuntimeProfileDraft, creating = true) {
  const errors = runtimeProfileDraftErrors(draft, creating);
  const first = Object.values(errors)[0];
  if (first) throw new Error(first);
  return {
    name: draft.name.trim(),
    permissionMode: draft.permissionMode,
    gitRemoteWrites: draft.gitRemoteWrites === "disabled" ? "disabled" : "enabled",
    egressMode: draft.egressMode,
    defaultTimeout: draft.defaultTimeout.trim(),
    persistWorkspace: draft.persistWorkspace,
    workspaceSize: draft.persistWorkspace ? draft.workspaceSize.trim() : "",
    enablePrivateProcfs: draft.enablePrivateProcfs,
    sandboxTemplateRef: draft.sandboxTemplateRef.trim(),
    runtimeClassName: draft.runtimeClassName.trim(),
    warmPoolRef: draft.warmPoolRef.trim(),
    commandPath: parseStringList(draft.commandPath),
    commandPathPrepend: parseStringList(draft.commandPathPrepend),
    commandPathAppend: parseStringList(draft.commandPathAppend),
    extraReadOnlyPaths: parseStringList(draft.extraReadOnlyPaths),
    extraWritablePaths: parseStringList(draft.extraWritablePaths),
    commandEnv: rowsToRecord(draft.commandEnv, "Variable"),
    resourceRequests: rowsToRecord(draft.resourceRequests, "Resource"),
    resourceLimits: rowsToRecord(draft.resourceLimits, "Resource"),
    maxConcurrentRuns: Number(draft.maxConcurrentRuns) || 0,
    perNamespaceMaxConcurrentRuns: Number(draft.perNamespaceMaxConcurrentRuns) || 0,
    staleRunTimeout: draft.staleRunTimeout.trim(),
    replaceSpec: true,
  };
}
