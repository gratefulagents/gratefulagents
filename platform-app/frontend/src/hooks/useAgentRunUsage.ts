/* eslint-disable react-hooks/set-state-in-effect */
import { useEffect, useState } from "react";

import { client } from "@/lib/client";
import type { AgentRunUsageResponse } from "@/rpc/platform/service_pb";

export function useAgentRunUsage(namespace: string, name: string, enabled = true) {
  const [usage, setUsage] = useState<AgentRunUsageResponse | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (!enabled || !namespace || !name) {
      setUsage(null);
      setLoading(false);
      setError(null);
      return;
    }

    let cancelled = false;
    setLoading(true);
    client
      .getAgentRunUsage({ namespace, name })
      .then((resp) => {
        if (cancelled) return;
        setUsage(resp);
        setError(null);
      })
      .catch((err) => {
        if (cancelled) return;
        setError(err instanceof Error ? err.message : "Failed to load usage");
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });

    return () => {
      cancelled = true;
    };
  }, [enabled, namespace, name]);

  return { usage, loading, error };
}
