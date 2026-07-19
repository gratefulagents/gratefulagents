import { useCallback, useEffect, useState } from "react";

import { client } from "@/lib/client";
import {
  presenceFromServer,
  type CredentialPresence,
  type ServerCredentialPresence,
} from "@/lib/onboarding";

/**
 * useMyCredentials loads the caller's saved credential presence flags once and
 * lets save/OAuth flows push fresh server responses back in via `apply` (every
 * credentials RPC returns the updated presence).
 */
export function useMyCredentials() {
  const [presence, setPresence] = useState<CredentialPresence | null>(null);
  const [error, setError] = useState<string | null>(null);

  const apply = useCallback((c: ServerCredentialPresence) => {
    setPresence(presenceFromServer(c));
    setError(null);
  }, []);

  const reload = useCallback(async () => {
    try {
      apply(await client.listMyCredentials({}));
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to load credentials");
    }
  }, [apply]);

  useEffect(() => {
    let active = true;
    client.listMyCredentials({}).then(
      (c) => {
        if (active) apply(c);
      },
      (err: unknown) => {
        if (active) setError(err instanceof Error ? err.message : "Failed to load credentials");
      },
    );
    return () => {
      active = false;
    };
  }, [apply]);

  return { presence, loading: presence === null && error === null, error, reload, apply };
}
