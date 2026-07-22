import { useEffect, useState } from "react";

import { client } from "@/lib/client";

const LOG_POLL_INTERVAL_MS = 5_000;
const LOG_TAIL_LINES = 2_000;

export interface UseAgentRunLogsOptions {
  enabled?: boolean;
}

export function useAgentRunLogs(
  namespace: string,
  name: string,
  phase?: string,
  options?: UseAgentRunLogsOptions,
) {
  const enabled = options?.enabled ?? true;
  const [content, setContent] = useState("");
  const [podName, setPodName] = useState("");
  const [available, setAvailable] = useState(false);
  const [truncated, setTruncated] = useState(false);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [lastUpdated, setLastUpdated] = useState<Date | null>(null);
  const [refreshKey, setRefreshKey] = useState(0);

  useEffect(() => {
    if (!enabled || !namespace || !name) return;

    let cancelled = false;
    let timer: ReturnType<typeof setTimeout> | undefined;
    let activeRequest: AbortController | undefined;

    const load = async () => {
      activeRequest = new AbortController();
      let continuePolling = !isTerminalPhase(phase);
      setLoading(true);
      try {
        const response = await client.getAgentRunLogs(
          { namespace, name, tailLines: LOG_TAIL_LINES },
          { signal: activeRequest.signal },
        );
        if (cancelled) return;
        setContent(response.content);
        setPodName(response.podName);
        setAvailable(response.available);
        setTruncated(response.truncated);
        setError(null);
        setLastUpdated(new Date());
        continuePolling = continuePolling && !response.isComplete;
      } catch (cause) {
        if (cancelled) return;
        setError(cause instanceof Error ? cause.message : "Failed to load run logs");
        // Authorization, missing resources, and backend failures should not
        // generate a permanent request loop. The user can retry explicitly.
        continuePolling = false;
      } finally {
        if (!cancelled) {
          setLoading(false);
          if (continuePolling) {
            timer = setTimeout(() => void load(), LOG_POLL_INTERVAL_MS);
          }
        }
      }
    };

    void load();
    return () => {
      cancelled = true;
      if (timer) clearTimeout(timer);
      activeRequest?.abort();
    };
  }, [enabled, namespace, name, phase, refreshKey]);

  return {
    content,
    podName,
    available,
    truncated,
    loading,
    error,
    lastUpdated,
    refresh: () => setRefreshKey((current) => current + 1),
  };
}

function isTerminalPhase(phase?: string): boolean {
  return phase === "Succeeded" || phase === "Failed" || phase === "Cancelled";
}
