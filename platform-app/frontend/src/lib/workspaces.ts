// Operator — multi-workspace store (Slack-style).
//
// Source of truth for the set of backends ("workspaces") the app can connect to.
// Each workspace carries its own endpoint URL, Cloudflare Access credentials, and
// (via namespaced keys in client.ts) its own auth session. The active workspace
// drives backendBaseUrl().
//
// Mirrors the persistence pattern used elsewhere: a synchronous localStorage cache
// for the sync getters Connect requires, plus an async secure-store hydrate that
// wins on startup.

import { isTauri } from "./is-tauri";
import { storeGet, storeSet } from "./secure-store";

const WORKSPACES_KEY = "gratefulagents.workspaces";
const ACTIVE_KEY = "gratefulagents.activeWorkspaceId";

// Pre-rebrand multi-workspace keys ("Operator"-era). Read as a fallback.
const LEGACY_WORKSPACES_KEY = "operator.workspaces";
const LEGACY_ACTIVE_KEY = "operator.activeWorkspaceId";

// Legacy single-endpoint keys (pre multi-workspace). Migrated into a workspace.
const LEGACY_ENDPOINT_KEY = "operator.endpoint";
const LEGACY_CF_ID_KEY = "operator.cf_access_client_id";
const LEGACY_CF_SECRET_KEY = "operator.cf_access_client_secret";

// Stable bucket id used to namespace per-workspace data (tokens, user) when no
// workspace is configured yet — e.g. the web build, which is always same-origin.
export const DEFAULT_WORKSPACE_ID = "default";

export interface Workspace {
  id: string;
  name: string;
  endpointUrl: string;
  cfAccessClientId: string;
  cfAccessClientSecret: string;
}

interface WorkspacesState {
  workspaces: Workspace[];
  activeId: string;
}

function normalize(value: string | null | undefined): string {
  return (value ?? "").trim();
}

function normalizeWorkspace(ws: Partial<Workspace>): Workspace {
  return {
    id: normalize(ws.id) || generateId(),
    name: normalize(ws.name) || "Workspace",
    endpointUrl: normalize(ws.endpointUrl),
    cfAccessClientId: normalize(ws.cfAccessClientId),
    cfAccessClientSecret: normalize(ws.cfAccessClientSecret),
  };
}

function generateId(): string {
  try {
    return crypto.randomUUID();
  } catch {
    return `ws-${Date.now()}-${Math.random().toString(36).slice(2, 10)}`;
  }
}

// Derive a friendly default name from an endpoint URL (its host).
export function deriveWorkspaceName(endpointUrl: string): string {
  const url = normalize(endpointUrl);
  if (!url) return isTauri ? "Local" : "Workspace";
  try {
    return new URL(url).host || url;
  } catch {
    return url.replace(/^https?:\/\//, "").replace(/\/.*$/, "") || url;
  }
}

function parseWorkspaces(raw: string | null | undefined): Workspace[] {
  if (!raw) return [];
  try {
    const parsed = JSON.parse(raw) as unknown;
    if (!Array.isArray(parsed)) return [];
    return parsed
      .filter((w): w is Partial<Workspace> => typeof w === "object" && w !== null)
      .map(normalizeWorkspace);
  } catch {
    return [];
  }
}

function legacyWorkspaceFromLocalStorage(): Workspace | null {
  const endpointUrl = normalize(localStorage.getItem(LEGACY_ENDPOINT_KEY));
  const cfAccessClientId = normalize(localStorage.getItem(LEGACY_CF_ID_KEY));
  const cfAccessClientSecret = normalize(localStorage.getItem(LEGACY_CF_SECRET_KEY));
  if (!endpointUrl && !cfAccessClientId && !cfAccessClientSecret) return null;
  return {
    id: DEFAULT_WORKSPACE_ID,
    name: deriveWorkspaceName(endpointUrl),
    endpointUrl,
    cfAccessClientId,
    cfAccessClientSecret,
  };
}

function readLocalState(): WorkspacesState {
  let workspaces = parseWorkspaces(localStorage.getItem(WORKSPACES_KEY));
  let activeId = normalize(localStorage.getItem(ACTIVE_KEY));

  if (workspaces.length === 0) {
    workspaces = parseWorkspaces(localStorage.getItem(LEGACY_WORKSPACES_KEY));
    if (workspaces.length > 0) {
      activeId = normalize(localStorage.getItem(LEGACY_ACTIVE_KEY));
    }
  }

  if (workspaces.length === 0) {
    const legacy = legacyWorkspaceFromLocalStorage();
    if (legacy) {
      workspaces = [legacy];
      activeId = legacy.id;
    } else if (!isTauri) {
      // The web build is served by the gratefulagents backend itself (same-origin). Keep a
      // single implicit workspace so per-workspace data has a stable bucket.
      const implicit = normalizeWorkspace({ id: DEFAULT_WORKSPACE_ID, name: "Local" });
      workspaces = [implicit];
      activeId = implicit.id;
    }
  }

  if (workspaces.length > 0 && !workspaces.some((w) => w.id === activeId)) {
    activeId = workspaces[0].id;
  }

  return { workspaces, activeId };
}

let state: WorkspacesState = readLocalState();
const listeners = new Set<() => void>();

function persist(): void {
  localStorage.setItem(WORKSPACES_KEY, JSON.stringify(state.workspaces));
  localStorage.setItem(ACTIVE_KEY, state.activeId);
  void storeSet(WORKSPACES_KEY, state.workspaces);
  void storeSet(ACTIVE_KEY, state.activeId);
}

function setState(next: WorkspacesState): void {
  state = next;
  persist();
  for (const listener of listeners) listener();
}

export function subscribeWorkspaces(listener: () => void): () => void {
  listeners.add(listener);
  return () => listeners.delete(listener);
}

export function getWorkspaces(): Workspace[] {
  return state.workspaces;
}

export function getActiveWorkspaceId(): string {
  return state.activeId || DEFAULT_WORKSPACE_ID;
}

export function getActiveWorkspace(): Workspace | null {
  return state.workspaces.find((w) => w.id === state.activeId) ?? null;
}

export async function hydrateWorkspaces(): Promise<WorkspacesState> {
  const storedWorkspaces = await storeGet<Workspace[]>(WORKSPACES_KEY);
  const storedActive = normalize(await storeGet<string>(ACTIVE_KEY));

  if (Array.isArray(storedWorkspaces) && storedWorkspaces.length > 0) {
    const workspaces = storedWorkspaces.map(normalizeWorkspace);
    const activeId = workspaces.some((w) => w.id === storedActive)
      ? storedActive
      : workspaces[0].id;
    setState({ workspaces, activeId });
  } else {
    // Nothing in secure store — fall back to the local (possibly migrated) state
    // and write it back so both stores agree.
    setState(readLocalState());
  }

  return state;
}

// Create a new workspace and make it active.
export function addWorkspace(input: Omit<Workspace, "id"> & { id?: string }): Workspace {
  const workspace = normalizeWorkspace({
    ...input,
    name: normalize(input.name) || deriveWorkspaceName(input.endpointUrl),
  });
  setState({
    workspaces: [...state.workspaces, workspace],
    activeId: workspace.id,
  });
  return workspace;
}

// Update fields on an existing workspace (by id). Returns the updated workspace.
export function updateWorkspace(
  id: string,
  patch: Partial<Omit<Workspace, "id">>,
): Workspace | null {
  let updated: Workspace | null = null;
  const workspaces = state.workspaces.map((w) => {
    if (w.id !== id) return w;
    updated = normalizeWorkspace({ ...w, ...patch, id: w.id });
    return updated;
  });
  if (!updated) return null;
  setState({ ...state, workspaces });
  return updated;
}

// Update the active workspace's env, creating one if none exists. Used by the
// connect/settings flows that edit "the current backend".
export function upsertActiveWorkspace(
  env: Pick<Workspace, "endpointUrl" | "cfAccessClientId" | "cfAccessClientSecret"> & {
    name?: string;
  },
): Workspace {
  const active = getActiveWorkspace();
  if (active) {
    return (
      updateWorkspace(active.id, {
        endpointUrl: env.endpointUrl,
        cfAccessClientId: env.cfAccessClientId,
        cfAccessClientSecret: env.cfAccessClientSecret,
        ...(normalize(env.name) ? { name: env.name } : {}),
      }) ?? active
    );
  }
  return addWorkspace({
    name: normalize(env.name) || deriveWorkspaceName(env.endpointUrl),
    endpointUrl: env.endpointUrl,
    cfAccessClientId: env.cfAccessClientId,
    cfAccessClientSecret: env.cfAccessClientSecret,
  });
}

export function setActiveWorkspace(id: string): Workspace | null {
  const target = state.workspaces.find((w) => w.id === id);
  if (!target) return null;
  if (state.activeId === id) return target;
  setState({ ...state, activeId: id });
  return target;
}

// Remove a workspace. If it was active, falls back to the first remaining one.
export function removeWorkspace(id: string): WorkspacesState {
  const workspaces = state.workspaces.filter((w) => w.id !== id);
  let activeId = state.activeId;
  if (activeId === id) {
    activeId = workspaces[0]?.id ?? "";
  }
  setState({ workspaces, activeId });
  return state;
}
