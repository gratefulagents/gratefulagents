import * as React from "react";
import { useLocation } from "react-router-dom";

const KEY = "gratefulagents.recents.v1";
const MAX = 8;

export type Recent = {
  path: string;
  label: string;
  kind: "run" | "project" | "repo" | "cron";
};

type Store = { items: Recent[] };

function read(): Store {
  try {
    const raw = localStorage.getItem(KEY);
    if (!raw) return { items: [] };
    const parsed = JSON.parse(raw) as Store;
    if (!Array.isArray(parsed.items)) return { items: [] };

    const items: Recent[] = [];
    for (const entry of parsed.items) {
      if (!entry || typeof entry.path !== "string" || typeof entry.label !== "string") {
        continue;
      }
      const kind = classify(entry.path);
      if (!kind) {
        continue;
      }
      items.push({
        path: entry.path,
        label: entry.label,
        kind,
      });
    }
    return { items };
  } catch {
    return { items: [] };
  }
}

function write(store: Store) {
  try {
    localStorage.setItem(KEY, JSON.stringify(store));
  } catch {
    /* quota */
  }
}

function classify(path: string): Recent["kind"] | null {
  if (path.startsWith("/runs/")) return "run";
  if (path.startsWith("/projects/")) return "project";
  if (path.startsWith("/github/") && path.split("/").length > 3) return "repo";
  if (path.startsWith("/cron/") && path.split("/").length > 3) return "cron";
  return null;
}

function niceLabel(path: string): string {
  const parts = path.split("/").filter(Boolean);
  return parts[parts.length - 1] ?? path;
}

/**
 * Tracks navigation to detail pages, keeping the last MAX visits.
 * `useRecentsTracker()` should be mounted once; it pushes on location change.
 */
export function useRecentsTracker() {
  const location = useLocation();
  React.useEffect(() => {
    const kind = classify(location.pathname);
    if (!kind) return;
    const entry: Recent = {
      path: location.pathname,
      label: niceLabel(location.pathname),
      kind,
    };
    const store = read();
    const deduped = [
      entry,
      ...store.items.filter((it) => it.path !== entry.path),
    ].slice(0, MAX);
    write({ items: deduped });
    window.dispatchEvent(new Event("gratefulagents:recents-changed"));
  }, [location.pathname]);
}

/** Subscribes to the recents list. */
export function useRecents(): Recent[] {
  const [items, setItems] = React.useState<Recent[]>(() => read().items);
  React.useEffect(() => {
    function refresh() {
      setItems(read().items);
    }
    window.addEventListener("gratefulagents:recents-changed", refresh);
    window.addEventListener("storage", refresh);
    return () => {
      window.removeEventListener("gratefulagents:recents-changed", refresh);
      window.removeEventListener("storage", refresh);
    };
  }, []);
  return items;
}

export function clearRecents() {
  write({ items: [] });
  window.dispatchEvent(new Event("gratefulagents:recents-changed"));
}
