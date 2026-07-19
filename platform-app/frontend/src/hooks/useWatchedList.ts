import { useEffect, useSyncExternalStore } from "react";
import { client } from "@/lib/client";
import { getWatchStore, type WatchStoreConfig } from "@/hooks/watchStore";

interface WatchedItem {
  name: string;
  namespace: string;
}

/**
 * Generic hook for list+watch patterns. Fetches an initial list, then streams
 * updates, merging new/updated items into state by (namespace, name).
 *
 * The underlying list+watch loop is shared per (label, namespace): the first
 * mounted subscriber opens it, later subscribers reuse it, and the last
 * unmount stops it after a short linger (see watchStore.ts).
 *
 * Callback props (list, extractList, watch, extractItem) don't need to be
 * memoized — they are captured when the shared store is first created.
 */
export function useWatchedList<T extends WatchedItem, L, E>(opts: {
  list: (req: { namespace: string }) => Promise<L>;
  extractList: (res: L) => T[];
  watch: (
    req: { namespace: string },
    options: { signal: AbortSignal }
  ) => AsyncIterable<E>;
  extractItem: (event: E) => T | undefined;
  /**
   * Returns the event type ("ADDED" | "MODIFIED" | "DELETED"). Events of type
   * DELETED remove the item (identified by extractItem's namespace/name)
   * instead of upserting it — the server emits them when a resource is
   * deleted or the caller's access is revoked.
   */
  extractType?: (event: E) => string;
  label: string;
  namespace: string;
}) {
  const { namespace, label } = opts;

  const store = getWatchStore<T, L, E>(`${label}:${namespace}`, () =>
    makeWatchedListConfig(namespace, opts),
  );

  useEffect(() => {
    store.acquire();
    return () => store.release();
  }, [store]);

  const snapshot = useSyncExternalStore(store.subscribe, store.getSnapshot, store.getSnapshot);

  return { items: snapshot.items, loading: snapshot.loading, error: snapshot.error, refetch: store.refetch };
}

function makeWatchedListConfig<T extends WatchedItem, L, E>(
  namespace: string,
  opts: {
    list: (req: { namespace: string }) => Promise<L>;
    extractList: (res: L) => T[];
    watch: (req: { namespace: string }, options: { signal: AbortSignal }) => AsyncIterable<E>;
    extractItem: (event: E) => T | undefined;
    extractType?: (event: E) => string;
    label: string;
  },
): WatchStoreConfig<T, L, E> {
  const { list, extractList, watch, extractItem, extractType, label } = opts;
  return {
    list: () => list({ namespace }),
    extractList,
    watch: (options) => watch({ namespace }, options),
    applyEvent: (prev, event) => {
      const item = extractItem(event);
      if (!item) {
        return prev;
      }
      const removed = extractType?.(event) === "DELETED";
      const idx = prev.findIndex((p) => p.name === item.name && p.namespace === item.namespace);
      if (removed) {
        if (idx < 0) {
          return prev;
        }
        const next = [...prev];
        next.splice(idx, 1);
        return next;
      }
      if (idx >= 0) {
        const next = [...prev];
        next[idx] = item;
        return next;
      }
      return [...prev, item];
    },
    label,
  };
}

// ── Concrete hooks built on useWatchedList ──────────────────────────

import type {
  LinearProject,
  LinearProjectEvent,
  GitHubRepository,
  GitHubRepositoryEvent,
  Cron,
  CronEvent,
  Project,
  ProjectEvent,
  ListLinearProjectsResponse,
  ListGitHubRepositoriesResponse,
  ListCronsResponse,
  ListProjectsResponse,
} from "@/rpc/platform/service_pb";

export function useLinearProjects(namespace = "") {
  const { items: projects, ...rest } = useWatchedList<
    LinearProject,
    ListLinearProjectsResponse,
    LinearProjectEvent
  >({
    list: client.listLinearProjects.bind(client),
    extractList: (r) => r.projects,
    watch: client.watchLinearProjects.bind(client),
    extractItem: (e) => e.project,
    extractType: (e) => e.type,
    label: "LinearProjects",
    namespace,
  });
  return { projects, ...rest };
}

export function useGitHubRepositories(namespace = "") {
  const { items: repositories, ...rest } = useWatchedList<
    GitHubRepository,
    ListGitHubRepositoriesResponse,
    GitHubRepositoryEvent
  >({
    list: client.listGitHubRepositories.bind(client),
    extractList: (r) => r.repositories,
    watch: client.watchGitHubRepositories.bind(client),
    extractItem: (e) => e.repository,
    extractType: (e) => e.type,
    label: "GitHubRepositories",
    namespace,
  });
  return { repositories, ...rest };
}

export function useCrons(namespace = "") {
  const { items: crons, ...rest } = useWatchedList<
    Cron,
    ListCronsResponse,
    CronEvent
  >({
    list: client.listCrons.bind(client),
    extractList: (r) => r.crons,
    watch: client.watchCrons.bind(client),
    extractItem: (e) => e.cron,
    extractType: (e) => e.type,
    label: "Crons",
    namespace,
  });
  return { crons, ...rest };
}

export function useProjects(namespace = "") {
  const { items: projects, ...rest } = useWatchedList<
    Project,
    ListProjectsResponse,
    ProjectEvent
  >({
    list: client.listProjects.bind(client),
    extractList: (r) => r.projects,
    watch: client.watchProjects.bind(client),
    extractItem: (e) => e.project,
    extractType: (e) => e.type,
    label: "Projects",
    namespace,
  });
  return { projects, ...rest };
}
