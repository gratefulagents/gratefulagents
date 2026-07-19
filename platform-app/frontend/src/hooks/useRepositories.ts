/* eslint-disable react-hooks/set-state-in-effect */
import { useCallback, useEffect, useState } from "react";

import { client } from "@/lib/client";
import type { RepositoryInfo } from "@/rpc/platform/service_pb";

export interface RepositoriesState {
  repositories: RepositoryInfo[];
  loading: boolean;
  error: string | null;
  /** Re-fetch the repository list (e.g. after a clone). */
  refresh: () => void;
}

/**
 * useRepositories lists the git repositories present in a running run's sandbox
 * (the original repo plus any cloned at runtime). It re-fetches whenever the run
 * identity changes or `refresh()` is called. Fetching is gated on `enabled` so
 * terminal/idle runs (no sandbox) never hit the backend.
 */
export function useRepositories(
  namespace: string,
  name: string,
  resourceType: string,
  enabled: boolean,
): RepositoriesState {
  const [repositories, setRepositories] = useState<RepositoryInfo[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [nonce, setNonce] = useState(0);

  const key = `${namespace}::${name}::${resourceType}`;
  const [prevKey, setPrevKey] = useState(key);
  if (key !== prevKey) {
    setPrevKey(key);
    setRepositories([]);
    setError(null);
  }

  useEffect(() => {
    if (!enabled || !namespace || !name) {
      return;
    }
    let cancelled = false;
    setLoading(true);
    setError(null);
    client
      .listRepositories({ namespace, name, resourceType })
      .then((resp) => {
        if (!cancelled) setRepositories(resp.repositories ?? []);
      })
      .catch((err: unknown) => {
        if (!cancelled) setError(err instanceof Error ? err.message : "Failed to list repositories");
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [enabled, namespace, name, resourceType, nonce]);

  const refresh = useCallback(() => setNonce((n) => n + 1), []);

  return { repositories, loading, error, refresh };
}
