import { useEffect, useRef, useState } from "react";

import { client } from "@/lib/client";
import type { AgentRunError } from "@/rpc/platform/service_pb";

const MAX_RETAINED_ERRORS = 200;

export interface UseAgentRunErrorsOptions {
  enabled?: boolean;
}

export function useAgentRunErrors(
  namespace: string,
  name: string,
  phase?: string,
  options?: UseAgentRunErrorsOptions,
) {
  const enabled = options?.enabled ?? true;
  const [errors, setErrors] = useState<AgentRunError[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [truncated, setTruncated] = useState(false);
  const keyRef = useRef("");
  const errorsRef = useRef<AgentRunError[]>([]);

  useEffect(() => {
    const key = `${namespace}/${name}`;
    if (keyRef.current !== key) {
      keyRef.current = key;
      errorsRef.current = [];
      setErrors([]);
      setTruncated(false);
      setError(null);
    }
    if (!enabled || !namespace || !name) return;

    let cancelled = false;
    let timer: ReturnType<typeof setTimeout> | undefined;

    const load = async () => {
      setLoading(true);
      try {
        const response = await client.getAgentRunErrors({ namespace, name });
        if (cancelled) return;
        // Keep errors already seen in this dashboard session even if a pod is
        // replaced between polls. Recovered failures remain visible by design.
        const merged = mergeErrors(errorsRef.current, response.errors);
        errorsRef.current = merged.errors;
        setErrors(merged.errors);
        setTruncated((current) => current || response.truncated || merged.evicted);
        setError(null);
        if (!response.isComplete) timer = setTimeout(load, 5_000);
      } catch (cause) {
        if (cancelled) return;
        setError(cause instanceof Error ? cause.message : "Failed to load run errors");
        if (!isTerminalPhase(phase)) timer = setTimeout(load, 5_000);
      } finally {
        if (!cancelled) setLoading(false);
      }
    };

    void load();
    return () => {
      cancelled = true;
      if (timer) clearTimeout(timer);
    };
    // The active tab and run identity govern the request. Error snapshots are
    // deliberately retained and merged rather than restarting on every poll.
  }, [enabled, namespace, name, phase]);

  return { errors, loading, error, truncated };
}

function isTerminalPhase(phase?: string): boolean {
  return phase === "Succeeded" || phase === "Failed" || phase === "Cancelled";
}

function mergeErrors(current: AgentRunError[], incoming: AgentRunError[]): { errors: AgentRunError[]; evicted: boolean } {
  const merged = new Map<string, AgentRunError>();
  for (const entry of [...current, ...incoming]) {
    merged.set(`${entry.source}\u0000${entry.message}`, entry);
  }
  const sorted = [...merged.values()].sort((a, b) => Number(a.timestampUnix - b.timestampUnix));
  return {
    errors: sorted.slice(-MAX_RETAINED_ERRORS),
    evicted: sorted.length > MAX_RETAINED_ERRORS,
  };
}
