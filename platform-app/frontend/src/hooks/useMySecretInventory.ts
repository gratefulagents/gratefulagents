import { useCallback, useEffect, useState } from "react";

import { client } from "@/lib/client";
import type { IntegrationCredentialState, UserSecretState } from "@/rpc/platform/service_pb";

/**
 * Loads reference-safe metadata for Secrets in the caller's personal namespace.
 * Values are never returned by the API. Consumers should call reload when a
 * picker opens so options reflect credentials created elsewhere in the app.
 * When a resource namespace is supplied, caller-owned options are returned
 * only if that resource resolves Secret refs in the caller's own namespace.
 */
export function useMySecretInventory(resourceNamespace?: string) {
  const [secrets, setSecrets] = useState<UserSecretState[]>([]);
  const [integrations, setIntegrations] = useState<IntegrationCredentialState[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const reload = useCallback(async () => {
    setLoading(true);
    try {
      const credentials = await client.listMyCredentials({});
      const inventoryApplies = !resourceNamespace || credentials.namespace === resourceNamespace;
      setSecrets(inventoryApplies ? (credentials.secrets ?? []) : []);
      setIntegrations(inventoryApplies ? (credentials.integrations ?? []) : []);
      setError(null);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to load your secrets");
    } finally {
      setLoading(false);
    }
  }, [resourceNamespace]);

  useEffect(() => {
    void (async () => {
      await reload();
    })();
  }, [reload]);

  return { secrets, integrations, loading, error, reload };
}
