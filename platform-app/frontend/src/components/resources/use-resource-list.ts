import { useCallback, useEffect, useRef, useState } from "react";

/**
 * Loads a resource list once on mount and exposes reload + local mutation so
 * sections can reflect a save or delete immediately while the server catches
 * up. Stale responses from an earlier load are discarded.
 */
export function useResourceList<T>(load: () => Promise<T[]>) {
  const [rows, setRows] = useState<T[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const generation = useRef(0);

  const reload = useCallback(async () => {
    const current = ++generation.current;
    setLoading(true);
    try {
      const next = await load();
      if (current !== generation.current) return;
      setRows(next);
      setError(null);
    } catch (cause) {
      if (current !== generation.current) return;
      setError(cause instanceof Error ? cause.message : String(cause));
    } finally {
      if (current === generation.current) setLoading(false);
    }
  }, [load]);

  useEffect(() => {
    // Defer the first load past the commit so the effect never sets state
    // synchronously; the generation bump below discards it if we unmount first.
    const counter = generation;
    const timer = window.setTimeout(() => void reload(), 0);
    return () => {
      window.clearTimeout(timer);
      counter.current++;
    };
  }, [reload]);

  return { rows, setRows, loading, error, reload };
}
