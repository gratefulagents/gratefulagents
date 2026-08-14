import { useCallback, useEffect, useState } from "react";

import { client } from "@/lib/client";
import type { ModelDefaults } from "@/rpc/platform/service_pb";

/**
 * useMyModelDefaults loads the caller's saved default provider/model/reasoning
 * level once. Creation surfaces use it (with applyModelDefaults) to prefill
 * fresh forms. Load failures are treated as "no defaults" so creating
 * resources never blocks on this preference.
 *
 * Pass `enabled=false` while the result cannot be used (dialog closed, or an
 * existing resource is being edited) so always-mounted dialog instances don't
 * issue redundant RPCs; the fetch starts the first time `enabled` is true and
 * is never repeated.
 */
export function useMyModelDefaults(enabled = true) {
  const [defaults, setDefaults] = useState<ModelDefaults | null>(null);
  const [loaded, setLoaded] = useState(false);

  const apply = useCallback((d: ModelDefaults) => {
    setDefaults(d);
    setLoaded(true);
  }, []);

  useEffect(() => {
    if (!enabled || loaded) return;
    let active = true;
    (async () => {
      try {
        const d = await client.getMyModelDefaults({});
        if (active) setDefaults(d);
      } catch {
        // No defaults is always a safe answer; the form keeps its fallback.
      } finally {
        if (active) setLoaded(true);
      }
    })();
    return () => {
      active = false;
    };
  }, [enabled, loaded]);

  return { defaults, loaded, apply };
}
