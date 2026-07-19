import * as React from "react";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";

import { client } from "@/lib/client";
import type { ObservabilityOverviewResponse } from "@/rpc/platform/service_pb";

export type ObservabilityRange = "24h" | "7d" | "30d" | "90d";

const RANGE_DAYS: Record<ObservabilityRange, number> = {
  "24h": 1,
  "7d": 7,
  "30d": 30,
  "90d": 90,
};

export function observabilityRequest(range: ObservabilityRange, namespace = "", now = new Date()) {
  const days = RANGE_DAYS[range];
  return {
    namespace,
    start: timestampFromDate(new Date(now.getTime() - days * 86_400_000)),
    end: timestampFromDate(now),
    bucketSeconds: BigInt(days <= 1 ? 3_600 : 86_400),
  };
}

export function useObservabilityOverview(range: ObservabilityRange, namespace = "") {
  const [data, setData] = React.useState<ObservabilityOverviewResponse | null>(null);
  const [loading, setLoading] = React.useState(true);
  const [error, setError] = React.useState<Error | null>(null);
  const requestId = React.useRef(0);

  const refetch = React.useCallback(async () => {
    const id = ++requestId.current;
    setLoading(true);
    setError(null);
    try {
      const response = await client.getObservabilityOverview(observabilityRequest(range, namespace));
      if (id === requestId.current) setData(response);
    } catch (cause) {
      if (id === requestId.current) setError(cause instanceof Error ? cause : new Error(String(cause)));
    } finally {
      if (id === requestId.current) setLoading(false);
    }
  }, [namespace, range]);

  React.useEffect(() => {
    void Promise.resolve().then(refetch);
    return () => { requestId.current += 1; };
  }, [refetch]);

  return { data, loading, error, refetch };
}
