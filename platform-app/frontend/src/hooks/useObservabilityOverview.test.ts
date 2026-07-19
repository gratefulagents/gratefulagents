import { describe, expect, it } from "vitest";
import { timestampDate } from "@bufbuild/protobuf/wkt";

import { observabilityRequest } from "@/hooks/useObservabilityOverview";

describe("observabilityRequest", () => {
  const now = new Date("2026-01-31T12:00:00Z");

  it("uses hourly buckets for 24 hours", () => {
    const request = observabilityRequest("24h", "demo", now);
    expect(request.namespace).toBe("demo");
    expect(request.bucketSeconds).toBe(3600n);
    expect(timestampDate(request.end).toISOString()).toBe(now.toISOString());
    expect(timestampDate(request.start).toISOString()).toBe("2026-01-30T12:00:00.000Z");
  });

  it("uses daily buckets for longer ranges", () => {
    const request = observabilityRequest("30d", "", now);
    expect(request.bucketSeconds).toBe(86400n);
    expect(timestampDate(request.start).toISOString()).toBe("2026-01-01T12:00:00.000Z");
  });
});
