import { useState, useEffect } from "react";
import { client } from "@/lib/client";
import type { ModeTemplate } from "@/rpc/platform/service_pb";

export function useAvailableModes(namespace: string) {
  const [modes, setModes] = useState<ModeTemplate[]>([]);
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    let cancelled = false;

    (async () => {
      try {
        const res = await client.listAvailableModes({ namespace });
        if (!cancelled) {
          setModes(res.modes);
          setLoading(false);
        }
      } catch {
        if (!cancelled) {
          setLoading(false);
        }
      }
    })();

    return () => {
      cancelled = true;
    };
  }, [namespace]);

  return { modes, loading };
}
