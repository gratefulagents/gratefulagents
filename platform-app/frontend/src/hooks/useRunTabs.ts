import * as React from "react";
import { useLocation } from "react-router-dom";

/**
 * Browser-style tabs for AgentRun detail pages.
 *
 * Visiting `/runs/:namespace/:name` opens (or re-activates) a tab; the strip
 * persists in localStorage so open runs survive reloads and app restarts, and
 * syncs across windows via storage events (same pattern as `useRecents`).
 * The "active" tab is derived from the current route — the store only owns
 * membership and ordering, never navigation state, so the URL stays the
 * single source of truth.
 *
 * Storage is scoped per workspace *and* user (`runTabsScope`): run names and
 * namespaces are resource metadata, so a different user (or the same user on
 * a different backend) sharing the browser profile must not inherit them.
 */

const KEY_PREFIX = "gratefulagents.runtabs.v1";
const CHANGE_EVENT = "gratefulagents:runtabs-changed";
const MAX_TABS = 20;

export type RunTab = {
  /** Route path, e.g. "/runs/demo/run-ui-polish". */
  path: string;
  namespace: string;
  name: string;
};

type Store = { tabs: RunTab[] };

/**
 * Storage scope for the tab strip. Anonymous (logged-out) sessions get their
 * own bucket, so a login never surfaces another identity's run metadata.
 */
export function runTabsScope(workspaceId: string, userId: string | undefined): string {
  return `${workspaceId || "default"}:${userId || "anon"}`;
}

function storageKey(scope: string): string {
  return `${KEY_PREFIX}.${scope}`;
}

/** Parses a location pathname into a run tab, or null when not a run page. */
export function runTabFromPath(pathname: string): RunTab | null {
  const parts = pathname.split("/").filter(Boolean);
  if (parts.length !== 3 || parts[0] !== "runs") return null;
  const [, namespace, name] = parts;
  if (!namespace || !name) return null;
  return { path: `/runs/${namespace}/${name}`, namespace, name };
}

function read(scope: string): Store {
  try {
    const raw = localStorage.getItem(storageKey(scope));
    if (!raw) return { tabs: [] };
    const parsed = JSON.parse(raw) as Store;
    if (!Array.isArray(parsed.tabs)) return { tabs: [] };
    const tabs: RunTab[] = [];
    const seen = new Set<string>();
    for (const entry of parsed.tabs) {
      if (!entry || typeof entry.path !== "string") continue;
      const tab = runTabFromPath(entry.path);
      if (!tab || seen.has(tab.path)) continue;
      seen.add(tab.path);
      tabs.push(tab);
    }
    return { tabs: tabs.slice(0, MAX_TABS) };
  } catch {
    return { tabs: [] };
  }
}

function write(scope: string, store: Store) {
  try {
    localStorage.setItem(storageKey(scope), JSON.stringify(store));
  } catch {
    /* quota */
  }
  window.dispatchEvent(new Event(CHANGE_EVENT));
}

export function getRunTabs(scope: string): RunTab[] {
  return read(scope).tabs;
}

/** Opens a tab for the given run path (no-op when already open). */
export function openRunTab(scope: string, pathname: string) {
  const tab = runTabFromPath(pathname);
  if (!tab) return;
  const { tabs } = read(scope);
  if (tabs.some((t) => t.path === tab.path)) return;
  // Evict the oldest (leftmost) tab when at capacity so the strip stays sane.
  const next = [...tabs, tab].slice(-MAX_TABS);
  write(scope, { tabs: next });
}

/**
 * Closes a tab. Returns the path the UI should navigate to when the closed
 * tab was active: the right neighbor, else the left, else null (caller
 * decides the fallback route).
 */
export function closeRunTab(scope: string, path: string): string | null {
  const { tabs } = read(scope);
  const idx = tabs.findIndex((t) => t.path === path);
  if (idx === -1) return null;
  const next = tabs.filter((t) => t.path !== path);
  write(scope, { tabs: next });
  if (next.length === 0) return null;
  return (next[idx] ?? next[idx - 1]).path;
}

export function closeOtherRunTabs(scope: string, path: string) {
  const { tabs } = read(scope);
  write(scope, { tabs: tabs.filter((t) => t.path === path) });
}

export function closeAllRunTabs(scope: string) {
  write(scope, { tabs: [] });
}

/** Moves the tab at `from` to position `to` (drag-reorder). */
export function moveRunTab(scope: string, from: number, to: number) {
  const { tabs } = read(scope);
  if (from === to || from < 0 || from >= tabs.length || to < 0 || to >= tabs.length) return;
  const next = [...tabs];
  const [tab] = next.splice(from, 1);
  next.splice(to, 0, tab);
  write(scope, { tabs: next });
}

/**
 * Mount once in the shell: opens a tab whenever the user lands on a run
 * detail page, mirroring how a browser materializes a tab per visited page.
 */
export function useRunTabsTracker(scope: string) {
  const location = useLocation();
  React.useEffect(() => {
    openRunTab(scope, location.pathname);
  }, [scope, location.pathname]);
}

/** Subscribes to the tab list for the given scope. */
export function useRunTabs(scope: string): RunTab[] {
  const [tabs, setTabs] = React.useState<RunTab[]>(() => read(scope).tabs);
  React.useEffect(() => {
    function refresh() {
      setTabs(read(scope).tabs);
    }
    refresh(); // Re-read when the workspace/user scope changes.
    window.addEventListener(CHANGE_EVENT, refresh);
    window.addEventListener("storage", refresh);
    return () => {
      window.removeEventListener(CHANGE_EVENT, refresh);
      window.removeEventListener("storage", refresh);
    };
  }, [scope]);
  return tabs;
}
