import { useEffect, useState } from "react";
import { client } from "@/lib/client";
import type { RuntimeImageOption } from "@/rpc/platform/service_pb";

export function useRuntimeImages() {
  const [images, setImages] = useState<RuntimeImageOption[]>([]);
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    let cancelled = false;

    (async () => {
      try {
        const res = await client.listRuntimeImages({});
        if (!cancelled) {
          setImages(res.images);
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
  }, []);

  return { images, loading };
}
