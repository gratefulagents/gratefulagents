import { useCallback, useEffect, useRef, useState } from "react";

import { client } from "@/lib/client";

export interface WorkspaceFiles {
  files: string[];
  loading: boolean;
  error: string | null;
  loaded: boolean;
  truncated: boolean;
  /** Trigger a one-time fetch (no-op once loaded). */
  load: () => void;
}

/**
 * useWorkspaceFiles lazily fetches the full, flat list of workspace file paths
 * once per run and caches it for the lifetime of the component. The list powers
 * the composer "@" file picker, which filters it entirely on the client so each
 * keystroke is instant (no per-keystroke round-trip).
 *
 * The fetch only runs when `canLoad` is true (an active run with a sandbox) and
 * after `load()` has been called, so idle/terminal runs never hit the backend.
 */
export function useWorkspaceFiles(
  namespace: string,
  name: string,
  resourceType: string,
  canLoad: boolean,
): WorkspaceFiles {
  const [requested, setRequested] = useState(false);
  const [files, setFiles] = useState<string[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [loaded, setLoaded] = useState(false);
  const [truncated, setTruncated] = useState(false);

  const key = `${namespace}::${name}::${resourceType}`;
  // The run identity whose fetch has been started, used to dedupe without making
  // the fetch depend on (and re-fire from) the status flags it updates. Mutated
  // only inside the effect, never during render.
  const startedKeyRef = useRef<string | null>(null);

  // Reset the cache when the run identity changes. Done during render (not in an
  // effect) per React's "resetting state when a prop changes" guidance.
  const [prevKey, setPrevKey] = useState(key);
  if (key !== prevKey) {
    setPrevKey(key);
    setRequested(false);
    setFiles([]);
    setLoading(false);
    setError(null);
    setLoaded(false);
    setTruncated(false);
  }

  useEffect(() => {
    if (!requested || !canLoad || !namespace || !name || startedKeyRef.current === key) {
      return;
    }
    startedKeyRef.current = key;
    let cancelled = false;
    setLoading(true);
    setError(null);
    client
      .listWorkspaceFiles({ namespace, name, resourceType })
      .then((resp) => {
        if (cancelled) return;
        setFiles(resp.paths ?? []);
        setTruncated(resp.truncated);
        setLoaded(true);
      })
      .catch((err: unknown) => {
        if (cancelled) return;
        setError(err instanceof Error ? err.message : "Failed to list files");
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [requested, canLoad, namespace, name, resourceType, key]);

  const load = useCallback(() => setRequested(true), []);

  return { files, loading, error, loaded, truncated, load };
}


