import { useEffect, useState } from "react";

import { client } from "@/lib/client";

/**
 * useAvailableModels fetches model-name suggestions from the live provider
 * catalog for a namespace + provider + auth mode. Consumers keep their model
 * fields free-form; this only powers datalist suggestions plus loading/error
 * copy. Pass enabled=false while the selection has no usable credential.
 */
export function useAvailableModels(input: {
  namespace: string;
  provider: string;
  authMode: string;
  enabled?: boolean;
}) {
  const { namespace, provider, authMode } = input;
  const enabled = input.enabled ?? true;
  const [models, setModels] = useState<string[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    const controller = new AbortController();
    if (!enabled || !namespace || !provider || !authMode) {
      // eslint-disable-next-line react-hooks/set-state-in-effect -- clear stale suggestions when no credential can serve this selection
      setModels([]);
      setLoading(false);
      setError(null);
      return () => controller.abort();
    }

    setModels([]);
    setLoading(true);
    setError(null);
    void client
      .listAvailableModels({ namespace, provider, authMode }, { signal: controller.signal })
      .then(
        (response) => {
          if (!controller.signal.aborted) setModels(response.models);
        },
        (cause: unknown) => {
          if (!controller.signal.aborted) {
            setError(cause instanceof Error ? cause.message : "Failed to load provider models");
          }
        },
      )
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false);
      });
    return () => controller.abort();
  }, [enabled, namespace, provider, authMode]);

  return { models, loading, error };
}
